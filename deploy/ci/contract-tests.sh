#!/usr/bin/env bash
# Tier 3 contract tests (11-quality/testing-strategy.md): the real Sales API, started against a disposable MongoDB that
# carries the pinned Sales DB migrations, is exercised by Schemathesis against the OpenAPI contract published in
# synkro-docs (07-api/contracts/openapi/synkro-sales-api.yaml). This is OpenAPI conformance, NOT consumer-driven Pact.
#
#   bash deploy/ci/contract-tests.sh /path/to/synkro-sales-db /path/to/synkro-docs
#
# Everything it creates is disposable, labelled with a per-run token and removed by that label; nothing else is touched.
set -euo pipefail
if command -v cygpath >/dev/null && command -v docker.exe >/dev/null; then
  docker() { command docker.exe "$@"; }
  export -f docker
fi
# One source of truth for the Sales DB pin: the real-MongoDB launcher.
api_dir=$(cd "$(dirname "$0")/../.." && pwd)
DB_REF=$(sed -n 's/^DB_REF=\([0-9a-f]\{40\}\)$/\1/p' "$api_dir/deploy/ci/mongo-integration.sh")
[[ -n $DB_REF ]] || { echo 'DB_REF not found in deploy/ci/mongo-integration.sh' >&2; exit 1; }
# The contract revision this code is verified against. Changing the contract means changing this pin in the same PR.
DOCS_REF=66d44ff8fa879adfc3c59388befc40f9d2d86afd
# Schemathesis 4.33.2, pinned by digest.
SCHEMATHESIS_IMAGE=schemathesis/schemathesis@sha256:2cd2605142dfcaff1e332f1b73b6faed0f7c33dba3dfa72e1799428be984d258
SPEC_REL=07-api/contracts/openapi
db_dir=$(cd "${1:?usage: bash deploy/ci/contract-tests.sh /path/to/synkro-sales-db /path/to/synkro-docs}" && pwd)
docs_dir=$(cd "${2:?usage: bash deploy/ci/contract-tests.sh /path/to/synkro-sales-db /path/to/synkro-docs}" && pwd)
[[ $(git -C "$db_dir" rev-parse HEAD) == "$DB_REF" ]] || { echo 'Sales DB checkout does not match the approved pinned revision' >&2; exit 1; }
[[ -z $(git -C "$db_dir" status --porcelain) ]] || { echo 'Sales DB checkout must be clean (read-only dependency)' >&2; exit 1; }
[[ $(git -C "$docs_dir" rev-parse HEAD) == "$DOCS_REF" ]] || { echo 'synkro-docs checkout does not match the pinned contract revision' >&2; exit 1; }
[[ -z $(git -C "$docs_dir" status --porcelain) ]] || { echo 'synkro-docs checkout must be clean (read-only dependency)' >&2; exit 1; }
[[ -f $docs_dir/$SPEC_REL/synkro-sales-api.yaml && -f $docs_dir/$SPEC_REL/_shared.yaml ]] || { echo 'OpenAPI contract files not found' >&2; exit 1; }
docker info >/dev/null
token=$(openssl rand -hex 12)
export CI_MONGO_CONTAINER="synkro-sales-api-ct-$token"
export MONGODB_NETWORK="synkro-sales-api-ct-net-$token"
CI_ADMIN_PASSWORD=$(openssl rand -hex 24)
CI_SALES_APP_PASSWORD=$(openssl rand -hex 24)
export CI_ADMIN_PASSWORD CI_SALES_APP_PASSWORD
export MONGODB_DATABASE=sales
export MONGODB_OPTIONS='replicaSet=rs0&authSource=admin&serverSelectionTimeoutMS=5000'
export MONGODB_URI="mongodb://admin:$CI_ADMIN_PASSWORD@$CI_MONGO_CONTAINER:27017"
APP_MONGODB_URI="mongodb://sales_app:$CI_SALES_APP_PASSWORD@$CI_MONGO_CONTAINER:27017/sales?$MONGODB_OPTIONS&retryWrites=false"
migrate_image="synkro-sales-api-ct-migrate-image:$token"
api_image="synkro-sales-api-ct-api-image:$token"
runner="synkro-sales-api-ct-migrate-$token"
api="synkro-sales-api-ct-api-$token"
label="com.synkro.sales-api.contract=$token"
scratch=$(mktemp -d)
host_path() { if command -v cygpath >/dev/null; then cygpath -w "$1"; else printf '%s' "$1"; fi; }
export MSYS_NO_PATHCONV=1
# Native (Windows) OpenSSL needs converted paths, Docker does not.
ossl() { env -u MSYS_NO_PATHCONV openssl "$@"; }
mongo_attempted=0
network_owned=0
images=()
cleanup() {
  local exit_status=$? name image
  trap - EXIT
  for name in "$api" "$runner"; do
    if [[ $(docker container inspect --format '{{index .Config.Labels "com.synkro.sales-api.contract"}}' "$name" 2>/dev/null || true) == "$token" ]]; then
      docker rm -f "$name" >/dev/null || exit_status=1
    fi
  done
  if [[ $mongo_attempted == 1 ]] && [[ $(docker container inspect --format '{{index .Config.Labels "com.synkro.sales-db.disposable"}}' "$CI_MONGO_CONTAINER" 2>/dev/null || true) == true ]]; then
    docker rm -f "$CI_MONGO_CONTAINER" >/dev/null || exit_status=1
  fi
  if [[ $network_owned == 1 ]]; then docker network rm "$MONGODB_NETWORK" >/dev/null || exit_status=1; fi
  for image in "${images[@]}"; do
    if docker image inspect "$image" >/dev/null 2>&1; then docker image rm "$image" >/dev/null || exit_status=1; fi
  done
  rm -f "$scratch"/*.log "$scratch"/*.sh "$scratch"/jwt.key "$scratch"/jwt.pem
  rmdir "$scratch" || exit_status=1
  unset CI_ADMIN_PASSWORD CI_SALES_APP_PASSWORD MONGODB_URI APP_MONGODB_URI
  echo "Disposable contract-test cleanup finished (exit $exit_status)"
  exit "$exit_status"
}
trap cleanup EXIT
for name in "$CI_MONGO_CONTAINER" "$runner" "$api"; do
  if docker container inspect "$name" >/dev/null 2>&1; then echo "Refusing existing container: $name" >&2; exit 1; fi
done
if docker network inspect "$MONGODB_NETWORK" >/dev/null 2>&1; then echo 'Refusing existing network' >&2; exit 1; fi
docker network create --label "$label" "$MONGODB_NETWORK" >/dev/null
network_owned=1
mongo_attempted=1
bash "$db_dir/deploy/ci/start-replica-set.sh"
cd "$db_dir"
# shellcheck source=/dev/null
source "$db_dir/deploy/ci/ci-env.sh"
require_disposable_database
cd "$api_dir"
# Real migrations of the pinned Sales DB revision.
images+=("$migrate_image")
docker build -t "$migrate_image" -f "$(host_path "$db_dir/deploy/liquibase.Dockerfile")" "$(host_path "$db_dir/deploy")" >"$scratch/migration.log" 2>&1 || { cat "$scratch/migration.log"; exit 1; }
export LIQUIBASE_COMMAND_URL="$MONGODB_URI/sales?$MONGODB_OPTIONS"
export LIQUIBASE_COMMAND_CHANGELOG_FILE=changelog/changelog-master.yaml
status=0
docker run --rm --name "$runner" --label "$label" --network "$MONGODB_NETWORK" \
  -e LIQUIBASE_COMMAND_URL -e LIQUIBASE_COMMAND_CHANGELOG_FILE \
  -v "$(host_path "$db_dir"):/liquibase/changelog-root:ro" -w /liquibase/changelog-root "$migrate_image" update >"$scratch/migration.log" 2>&1 || status=$?
sed -e "s/$CI_ADMIN_PASSWORD/[REDACTED]/g" -e "s/$CI_SALES_APP_PASSWORD/[REDACTED]/g" "$scratch/migration.log" | tail -5
[[ $status == 0 ]] || exit "$status"
# The service under test, built from this checkout, with a throwaway RS256 key pair (never persisted outside the scratch directory).
images+=("$api_image")
docker build -t "$api_image" -f "$(host_path "$api_dir/deploy/Dockerfile")" "$(host_path "$api_dir")" >"$scratch/api-build.log" 2>&1 || { cat "$scratch/api-build.log"; exit 1; }
ossl genrsa -out "$scratch/jwt.key" 2048 2>/dev/null
ossl rsa -in "$scratch/jwt.key" -pubout -out "$scratch/jwt.pem" 2>/dev/null
chmod 644 "$scratch/jwt.pem"
export APP_MONGODB_URI
docker run -d --name "$api" --label "$label" --network "$MONGODB_NETWORK" --network-alias synkro-sales-api \
  -e HTTP_PORT=8080 -e MONGODB_URI="$APP_MONGODB_URI" -e JWT_PUBLIC_KEY_FILE=/run/public.pem -e MONGODB_TIMEOUT=5s \
  -v "$(host_path "$scratch/jwt.pem"):/run/public.pem:ro" "$api_image" >/dev/null
healthy=0
for _ in $(seq 1 40); do
  if docker exec "$api" wget -qO- http://127.0.0.1:8080/health 2>/dev/null | grep -q '"status":"ok"'; then healthy=1; break; fi
  sleep 1
done
[[ $healthy == 1 ]] || { echo 'Sales API did not become healthy' >&2; docker logs "$api" 2>&1 | tail -20 >&2; exit 1; }
# The contract documents /health WITHOUT the /api/v1 prefix, so it is checked directly instead of through Schemathesis.
health=$(docker exec "$api" wget -qO- http://127.0.0.1:8080/health)
grep -q '"service":"synkro-sales-api"' <<<"$health" && grep -q '"database":"ok"' <<<"$health" || { echo "Unexpected health document: $health" >&2; exit 1; }
echo 'health: documented shape confirmed'

b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
mint() {  # role subject permissions-json
  local now header payload sig
  now=$(date +%s)
  header=$(printf '{"alg":"RS256","typ":"JWT"}' | b64url)
  payload=$(printf '{"sub":"%s","roles":["%s"],"permissions":%s,"iat":%d,"exp":%d,"jti":"%s"}' "$2" "$1" "$3" "$now" "$((now + 1800))" "$(openssl rand -hex 8)" | b64url)
  sig=$(printf '%s.%s' "$header" "$payload" | ossl dgst -sha256 -sign "$scratch/jwt.key" | b64url)
  printf '%s.%s.%s' "$header" "$payload" "$sig"
}
ADMIN_TOKEN=$(mint ADMIN 00000000-0000-4000-8000-000000000001 '["sales:read","reports:read"]')
SERVICE_TOKEN=$(mint SERVICE synkro-workflow '["sales:register"]')
export ADMIN_TOKEN SERVICE_TOKEN
mkdir -p "$api_dir/.contract-results"
CHECKS=status_code_conformance,response_schema_conformance,content_type_conformance,response_headers_conformance
# Run 1: the service token registers sales (POST); run 2: an ADMIN reads them (GET list, detail, reports).
cat >"$scratch/post.sh" <<EOF
schemathesis run /spec/synkro-sales-api.yaml --url http://synkro-sales-api:8080/api/v1 \
  -H "Authorization: Bearer \$SERVICE_TOKEN" --include-method POST --exclude-path /health \
  --checks $CHECKS --max-examples 25 --workers 1 --no-color --report junit --report-dir /out --seed 20261110
EOF
cat >"$scratch/get.sh" <<EOF
schemathesis run /spec/synkro-sales-api.yaml --url http://synkro-sales-api:8080/api/v1 \
  -H "Authorization: Bearer \$ADMIN_TOKEN" --include-method GET --exclude-path /health \
  --checks $CHECKS --max-examples 25 --workers 1 --no-color --report junit --report-dir /out --seed 20261110
EOF
run_schemathesis() {  # name script
  docker run --rm --label "$label" --network "$MONGODB_NETWORK" -e SERVICE_TOKEN -e ADMIN_TOKEN --entrypoint sh \
    -v "$(host_path "$docs_dir/$SPEC_REL"):/spec:ro" -v "$(host_path "$scratch/$2"):/run-contract.sh:ro" \
    -v "$(host_path "$api_dir/.contract-results"):/out" -w /out "$SCHEMATHESIS_IMAGE" /run-contract.sh 2>&1 | sed -e "s/$SERVICE_TOKEN/[REDACTED]/g" -e "s/$ADMIN_TOKEN/[REDACTED]/g" | tee "$api_dir/.contract-results/$1.log"
  return "${PIPESTATUS[0]}"
}
# Both runs always execute so one report shows every mismatch; any failing run fails the script.
contract_status=0
run_schemathesis post post.sh || contract_status=1
run_schemathesis get get.sh || contract_status=1
printf 'contract revision: synkro-docs %s\nsales db revision: %s\nschemathesis: %s\n' "$DOCS_REF" "$DB_REF" "$SCHEMATHESIS_IMAGE" >"$api_dir/.contract-results/revisions.txt"
if [[ $contract_status != 0 ]]; then echo 'Contract tests FAILED: see .contract-results/*.log' >&2; exit 1; fi
echo 'Contract tests passed: the Sales API conforms to its OpenAPI contract on every tested operation'
