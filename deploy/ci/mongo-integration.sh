#!/usr/bin/env bash
# Start only disposable resources, using the real pinned Sales DB runner.
set -euo pipefail
# Docker Desktop ships a shell wrapper that may not work under Git Bash.
# Export the native client shim so the read-only migration scripts reuse it.
if command -v cygpath >/dev/null && command -v docker.exe >/dev/null; then
  docker() { command docker.exe "$@"; }
  export -f docker
fi
DB_REF=9cfbde7db4bea7bacad70370c60f91f8eb917c04
api_dir=$(cd "$(dirname "$0")/../.." && pwd)
db_dir=$(cd "${1:?usage: bash deploy/ci/mongo-integration.sh /path/to/synkro-sales-db}" && pwd)
[[ $(git -C "$db_dir" rev-parse HEAD) == "$DB_REF" ]] || { echo 'Sales DB checkout does not match the approved pinned revision' >&2; exit 1; }
[[ -z $(git -C "$db_dir" status --porcelain) ]] || { echo 'Sales DB checkout must be clean (read-only dependency)' >&2; exit 1; }
docker info >/dev/null
token=$(openssl rand -hex 12)
export CI_MONGO_CONTAINER="synkro-sales-api-it-$token"
export MONGODB_NETWORK="synkro-sales-api-it-net-$token"
CI_ADMIN_PASSWORD=$(openssl rand -hex 24)
CI_SALES_APP_PASSWORD=$(openssl rand -hex 24)
export CI_ADMIN_PASSWORD CI_SALES_APP_PASSWORD
export MONGODB_DATABASE=sales
export MONGODB_OPTIONS='replicaSet=rs0&authSource=admin&serverSelectionTimeoutMS=5000'
export MONGODB_URI="mongodb://admin:$CI_ADMIN_PASSWORD@$CI_MONGO_CONTAINER:27017"
export TEST_MONGODB_URI="mongodb://sales_app:$CI_SALES_APP_PASSWORD@$CI_MONGO_CONTAINER:27017/sales?$MONGODB_OPTIONS&retryWrites=false"
export TEST_MONGO_HOST="$CI_MONGO_CONTAINER"
export TEST_MONGODB_DISPOSABLE=1 REQUIRE_MONGODB_INTEGRATION=1 CGO_ENABLED=1
runner="synkro-sales-api-it-migrate-$token"
tester="synkro-sales-api-it-go-$token"
image="synkro-sales-api-it-runner:$token"
network_owned=0
mongo_attempted=0
image_attempted=0
scratch=$(mktemp -d)
host_path() { if command -v cygpath >/dev/null; then cygpath -w "$1"; else printf '%s' "$1"; fi; }
# Prevent Git Bash from rewriting Docker's Linux paths and URI arguments.
export MSYS_NO_PATHCONV=1
cleanup() {
  local exit_status=$? name
  trap - EXIT
  for name in "$tester" "$runner"; do
    if [[ $(docker container inspect --format '{{index .Config.Labels "com.synkro.sales-api.integration"}}' "$name" 2>/dev/null || true) == "$token" ]]; then
      docker rm -f "$name" >/dev/null || exit_status=1
    fi
  done
  if [[ $mongo_attempted == 1 ]] && [[ $(docker container inspect --format '{{index .Config.Labels "com.synkro.sales-db.disposable"}}' "$CI_MONGO_CONTAINER" 2>/dev/null || true) == true ]]; then
    docker rm -f "$CI_MONGO_CONTAINER" >/dev/null || exit_status=1
  fi
  if [[ $network_owned == 1 ]]; then docker network rm "$MONGODB_NETWORK" >/dev/null || exit_status=1; fi
  if [[ $image_attempted == 1 ]] && docker image inspect "$image" >/dev/null 2>&1; then docker image rm "$image" >/dev/null || exit_status=1; fi
  # scratch is the exact directory returned by mktemp, never a computed project path.
  rm -f "$scratch/migration.log" "$scratch/tests.json"
  rmdir "$scratch" || exit_status=1
  unset CI_ADMIN_PASSWORD CI_SALES_APP_PASSWORD MONGODB_URI TEST_MONGODB_URI
  echo "Disposable integration cleanup finished (exit $exit_status)"
  exit "$exit_status"
}
trap cleanup EXIT
for name in "$CI_MONGO_CONTAINER" "$runner" "$tester"; do
  if docker container inspect "$name" >/dev/null 2>&1; then echo "Refusing existing container: $name" >&2; exit 1; fi
done
if docker network inspect "$MONGODB_NETWORK" >/dev/null 2>&1; then echo 'Refusing existing network' >&2; exit 1; fi
if docker image inspect "$image" >/dev/null 2>&1; then echo 'Refusing existing runner image tag' >&2; exit 1; fi
docker network create --label "com.synkro.sales-api.integration=$token" "$MONGODB_NETWORK" >/dev/null
network_owned=1
mongo_attempted=1
bash "$db_dir/deploy/ci/start-replica-set.sh"
# Verify tmpfs, the safety label and exact target before migration or probes.
# The owner helper resolves deploy/ci/connection.js relative to its own checkout.
cd "$db_dir"
# shellcheck source=/dev/null
source "$db_dir/deploy/ci/ci-env.sh"
require_disposable_database
cd "$api_dir"
image_attempted=1
docker build -t "$image" -f "$(host_path "$db_dir/deploy/liquibase.Dockerfile")" "$(host_path "$db_dir/deploy")" >"$scratch/migration.log" 2>&1 || { cat "$scratch/migration.log"; exit 1; }
export LIQUIBASE_COMMAND_URL="$MONGODB_URI/sales?$MONGODB_OPTIONS"
export LIQUIBASE_COMMAND_CHANGELOG_FILE=changelog/changelog-master.yaml
status=0
docker run --rm --name "$runner" --label "com.synkro.sales-api.integration=$token" --network "$MONGODB_NETWORK" \
  -e LIQUIBASE_COMMAND_URL -e LIQUIBASE_COMMAND_CHANGELOG_FILE \
  -v "$(host_path "$db_dir"):/liquibase/changelog-root:ro" -w /liquibase/changelog-root "$image" update >"$scratch/migration.log" 2>&1 || status=$?
# Redact the generated credentials even when Liquibase fails.
sed -e "s/$CI_ADMIN_PASSWORD/[REDACTED]/g" -e "s/$CI_SALES_APP_PASSWORD/[REDACTED]/g" "$scratch/migration.log"
[[ $status == 0 ]] || exit "$status"
mkdir -p "$api_dir/.integration-results"
docker run --rm --name "$tester" --label "com.synkro.sales-api.integration=$token" --network "$MONGODB_NETWORK" \
  -e TEST_MONGODB_URI -e TEST_MONGO_HOST -e TEST_MONGODB_DISPOSABLE -e REQUIRE_MONGODB_INTEGRATION -e CGO_ENABLED \
  -v "$(host_path "$api_dir"):/workspace" -w /workspace golang:1.23 bash -euo pipefail -c '
    getent hosts "$TEST_MONGO_HOST"
    go version
    go test deploy/ci/verify-integration.go deploy/ci/verify-integration_test.go deploy/ci/http-evidence_test.go
    go test -count=1 -race -coverprofile=.integration-results/coverage.out -json ./... > .integration-results/tests.json
  ' || { cat "$api_dir/.integration-results/tests.json"; exit 1; }
# Evidence gate: a skipped or absent required scenario must fail the job.
docker run --rm --name "$tester" --label "com.synkro.sales-api.integration=$token" \
  -v "$(host_path "$api_dir"):/workspace" -w /workspace golang:1.23 go run deploy/ci/verify-integration.go
