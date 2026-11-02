# Offline module cache for the real MongoDB integration run

Backlog item: HU-INF-02 (#239). Traceability: NFR-004, NFR-006, NFR-007, NFR-009.
No domain, port or adapter code changes; ADR-010 (MongoDB), ADR-012 (infra
ownership) and ADR-014 (bootstrap apply) stay as they are.

## Observation that motivates the change

A reported run of `deploy/ci/mongo-integration.sh` failed in the first Go container
with `lookup proxy.golang.org: no such host` for five import paths in three modules
(`go-playground/validator/v10`, `google/uuid`, `mongo-driver/v2`). No source compiler
defect was shown, and a compiler run on the default Docker bridge succeeded. The run
used the per-run Mongo bridge. The DNS mechanism is unknown and this change does not
claim a root cause; it removes the dependency on module downloads during the run.

## Sequencing

1. Existing guards: pinned Sales DB revision, clean checkout, `docker info`.
2. Refusal checks for every owned name, now including the module cache volume and
   the preparation container.
3. **Prefetch** (new): create the labelled cache volume, then run one labelled
   container on the default bridge that downloads and verifies the module graph.
4. Only after prefetch succeeds: exclusive network, disposable MongoDB, migrations.
5. Test container and evidence verifier, both with the same cache and offline module
   resolution.

A prefetch failure exits before any database resource exists and prints a fixed
message; raw network diagnostics stay in the private scratch directory, which the
cleanup deletes.

## Cache ownership and lifecycle

- Fresh per run: `synkro-sales-api-it-modcache-<token>`, labelled
  `com.synkro.sales-api.integration=<token>`. Never reused across runs.
- The launcher refuses to start if that name already exists (no adoption).
- Cleanup removes the volume only if this run created it **and** its label still
  equals this run's token. Existing or shared caches are never touched.
- The preparation container (`synkro-sales-api-it-prefetch-<token>`) follows the
  same label check as the other run containers.

## Offline module resolution vs. Mongo connectivity

"Offline" means dependency fetching is disabled, not that networking is disabled.

| Container | Network | Module settings |
| --- | --- | --- |
| prefetch | default bridge | `GOMODCACHE=/gomodcache`, `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly`; source mounted read-only; no database, password or service variables |
| tests | per-run Mongo bridge | same cache, `GOPROXY=off`, `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly`; keeps the existing limited `TEST_*` variables and still reaches the Mongo host |
| verifier | default bridge | same cache and offline settings |

`go.mod` and `go.sum` are not modified: no `tidy`, no `go get`, no version updates,
and checksum verification is not disabled (`go mod verify` runs after the download).
A missing `go.sum` entry fails closed. The Go image stays `golang:1.26.9`; the race
flag, coverage thresholds and the 34 required case names are unchanged.

## Test IDs

Fixture tests: `tests/test_module_cache_launcher.py`. They run the real launcher
against a fake `docker` and a fixture Sales DB checkout. They do not start Docker,
MongoDB or Go.

| ID | Behaviour |
| --- | --- |
| SALES-MC-01 | prefetch happens before network, MongoDB start, build and migration |
| SALES-MC-02 | prefetch container: exact image, read-only source, cache mounted, default bridge, no database or secret variables, no tidy/get |
| SALES-MC-03 | test and verifier containers mount the same cache with offline settings |
| SALES-MC-04 | test container stays on the Mongo bridge with its `TEST_*` variables |
| SALES-MC-05 | prefetch failure stops before any database resource, with a fixed message |
| SALES-MC-06 | success cleanup removes only the owned cache |
| SALES-MC-07 | failure cleanup removes only the owned cache |
| SALES-MC-08 | an existing unowned volume with the same name is refused and not removed |
| SALES-MC-09 | a failed volume creation removes nothing |

These are fixture tests. The real integration run (Docker, MongoDB, Go with `-race`,
34 required cases, coverage gate) is still mandatory and has not been executed by
this change.
