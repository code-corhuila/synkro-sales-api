# synkro-sales-api

> sales bounded context: service API

Part of the **SynkroTech SAS Sales Management System** — organization `code-corhuila`.
Governance and documentation live in [`synkro-docs`](https://github.com/code-corhuila/synkro-docs).

## Running locally

```bash
go run ./cmd/sales-api
```

The service listens on `:8080` (override with `HTTP_PORT`). Verify it:

```bash
curl http://localhost:8080/health
```

## Running the tests

```bash
go test ./...
```

## MongoDB persistence

`internal/adapter/out/persistence.NewMongoRepository(collection)` implements the
application's `SaleRepository`. Inject `client.Database("sales").Collection("sale")`
from one shared official MongoDB v2.8.2 client; its pool and shutdown belong to the
composition root. Connect as `sales_app`, authenticated in `admin`, with only
`sales_writer@sales`. `MONGODB_URI` in `.env.example` documents the future runtime
configuration. URL-encode the password and supply it through secret storage.
Do not use migration administrator credentials in the application.

Runtime wiring is deferred: the current routes and `/health` keep their existing
behavior, and `config.Load` does not yet consume `MONGODB_URI`. The adapter never
creates collections, indexes, roles or clients. Apply the Sales DB migrations before
using it. One complete `InsertOne` stores the sale and idempotency key together;
the migrated unique index is the concurrency boundary. Duplicate errors trigger a
lookup by the same key; an unrelated duplicate `_id` remains an error. Disable
retryable writes on the shared client (`retryWrites=false`) for a single wire attempt;
the adapter never changes the document/key or retries ambiguous insertion failures.

The mapper writes exactly the approved root and detail fields: canonical lowercase
UUID strings, int32 quantities, int64 monetary values, booleans and BSON datetime.
Prices and amounts are copied from the domain. Both initial and replay results use
UTC epoch milliseconds (`UnixMilli`), so sub-millisecond precision is intentionally
lost; the caller's aggregate is not mutated. Restoring through `model.RegisterSale`
verifies every subtotal and the total. Unknown/missing/duplicate fields, wrong BSON
types, invalid UUIDs and inconsistent arithmetic fail with `ErrInvalidSaleDocument`.
Inactive sales or details also fail: the current domain cannot restore those states
faithfully. No domain lifecycle attributes or behavior were introduced.

### Real integration tests

Use Docker and Bash (Git Bash on Windows). The compatible migrations are pinned to
[`synkro-sales-db@ffdc24b`](https://github.com/code-corhuila/synkro-sales-db/tree/ffdc24b9ce7c84b745b30bb1e85b76b07d029475).
The launcher requires a clean checkout at that exact commit and reads it without
modifying it. The approved documentation reviewed for this adapter is
`synkro-docs/main@61056c93e6e0783a9199bf56778e9494739bc458`.

```bash
bash deploy/ci/mongo-integration.sh ../synkro-sales-db
```

The launcher creates an exclusive Docker network and a uniquely named MongoDB 7.0
`rs0` container, using the Sales DB bootstrap, authentication, generated disposable
credentials, tmpfs storage and `com.synkro.sales-db.disposable=true`. It builds and
runs that repository's pinned Liquibase runner with a read-only migration mount;
preflight/postflight verify the actual validator, indexes and grants. A temporary
Go 1.23 Linux container joins the same network, resolves the advertised replica
hostname and runs all tests with race detection as `sales_app`. There are no public
ports or persistent volumes. The integration suite verifies the writable primary,
server version and authenticated principal before any permission probe.

`TEST_MONGODB_URI` is injected by this launcher. Ordinary `go test ./...` skips the
real suite when it is absent. Mandatory runs also set `REQUIRE_MONGODB_INTEGRATION=1`
and attest to the labelled tmpfs target with `TEST_MONGODB_DISPOSABLE=1` and
`TEST_MONGO_HOST`; do not set these for a persistent database. The evidence gate
requires every real scenario to have executed and passed, including 32 concurrent
calls, schema rejection, denied removal, unsafe replay and connection/context errors.
A skipped scenario, missing URI, failed migration or failed test returns nonzero.

Results are saved in the ignored `.integration-results/` directory: Go JSON test
events and full-repository coverage. The gate verifies domain ≥90%, application
≥80%, infrastructure ≥60% (adapters, config and command entrypoint), global ≥80%.
GitHub Actions preserves its ordinary 80% global coverage gate and adds a mandatory
integration job for the permanent branches and PRs targeting `develop`, `qa`, `main`.
Cleanup removes only this run's containers, exclusive network and runner image/tag;
the existing `synkro-mongo` and its volumes are never targeted.

ADR-010 records a historical prerequisite for creating the infrastructure repos;
this task explicitly permits the disposable Sales DB CI bootstrap instead. The
test strategy permits broader local targets, but this launcher intentionally requires
a disposable instance so permission probes cannot touch persistent data. ADR-010,
the data model and migrations include detail `active`; the entity catalog and current
domain omit it, which is why inactive restoration is rejected.

## Branching

Three permanent branches. **None of them accepts a direct commit** — you enter through a child
branch and leave through a Pull Request.

```
develop  <--PR--  feat/... fix/... chore/...
qa       <--PR--  qa/...
main     <--PR--  release/...  hotfix/...
```

Promotion happens **by re-application** (`git cherry-pick -x`), never by merging one permanent
branch into another: `merge develop -> qa` and `merge qa -> main` do not exist in this model.

`main` requires **1 approval from `ariel5253`**. On `develop` and `qa` the team sets its own review
rule.

Full policy: `00-governance/branching-policy.md` in `synkro-docs`.

Root deployment requires SALES_APP_MONGODB_URI, a complete URI for sales_app authenticated in admin with replicaSet=rs0 and bounded connection/server-selection timeouts. The deploy composition maps it to the application's MONGODB_URI. The Sales migration runner's MONGODB_URI is a separate administrator connection; never reuse it for this service.
