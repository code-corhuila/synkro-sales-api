# Persistence and integration

Detailed procedures and delivery notes moved from the repository README at
[source 2bcbb25](https://github.com/code-corhuila/synkro-sales-api/blob/2bcbb25330555c9120c16d6a473d7ea9fb1b3bb4/README.md).
Recorded execution results retain their original scope; moving them does not
constitute a new execution or certify a different revision.

Run commands from the repository root unless a step explicitly changes directory.

## MongoDB persistence

`internal/adapter/out/persistence.NewMongoRepository(collection)` implements the
application's `SaleRepository`. Inject `client.Database("sales").Collection("sale")`
from one shared official MongoDB v2.8.2 client; its pool and shutdown belong to the
composition root. Connect as `sales_app`, authenticated in `admin`, with only
`sales_writer@sales`. `MONGODB_URI` in `.env.example` documents the runtime
configuration. URL-encode the password and supply it through secret storage.
Do not use migration administrator credentials in the application.

The composition root wires registration, query use cases, JWT verification,
and the database health check with the same pooled client. The adapter never
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
[`synkro-sales-db@c6bacec`](https://github.com/code-corhuila/synkro-sales-db/tree/c6bacecc0f7c8743cf03c0a39f2fc7455db8c536),
the revision selected on QA (`DB_REF` in `deploy/ci/mongo-integration.sh` and the `ref` in `.github/workflows/ci.yml`); the launcher pins the commit, not a tag.
Earlier QA documentation named the Sales DB revision `e3ef534d1c205db78f74cfe70e53f7086ac4d1b5`
(yearly commercial sale number structures, changesets `sale-011`…`sale-014`, on top of the released `v2.0.0` (`344d917`) schema);
the script and workflow are authoritative for the current pin.
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
Go 1.26.9 Linux container joins the same network, resolves the advertised replica
hostname and runs all tests with race detection as `sales_app`. There are no public
ports or persistent volumes. The integration suite verifies the writable primary,
server version and authenticated principal before any permission probe.

Before any database resource exists, the launcher fills a fresh, run-owned Docker
volume with the module graph (`go mod download` and `go mod verify`, read-only source,
default bridge, no database variables). The test and verifier containers mount that
cache with `GOPROXY=off`, `GOTOOLCHAIN=local` and `GOFLAGS=-mod=readonly`, so modules
are never downloaded during the run while the tests still reach Mongo on the run
network. A preparation failure stops the run before the network or MongoDB exist.
Cleanup removes the cache only when its label matches the run; an existing volume with
that name is refused. Design and test IDs: `docs/design/offline-module-cache.md`.
The fixture tests (`tests/test_module_cache_launcher.py`) use a fake Docker; they do
not replace the real integration run above.

`TEST_MONGODB_URI` is injected by this launcher. Ordinary `go test ./...` skips the
real suite when it is absent. Mandatory runs also set `REQUIRE_MONGODB_INTEGRATION=1`
and attest to the labelled tmpfs target with `TEST_MONGODB_DISPOSABLE=1` and
`TEST_MONGO_HOST`; do not set these for a persistent database. The evidence gate
requires every real scenario to have executed and passed, including 32 concurrent
calls, schema rejection, denied removal, unsafe replay and connection/context errors.
A skipped scenario, missing URI, failed migration or failed test returns nonzero.

HU-VEN-09 adds ten mandatory HTTP scenarios to those ten persistence scenarios:
creation, replay, ADMIN reads, salesperson filtering, foreign-sale rejection,
unknown/inactive exclusion, pagination/UTC boundaries, corrupt-document rejection,
database failure propagation, and denied deletion. HTTP tests use generated test
RSA keys and the real registration/query use cases; no authentication bypass is
used. For a local pinned dependency copy, clone it inside the ignored
`.integration-results/` directory with `core.autocrlf=false`, check out the exact
revision above, and pass that path to the launcher. The original dependency
checkout does not need to change. Git Bash uses the native Docker Desktop client.

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

### Contract review notes for HU-VEN-09

The 16 requested source documents were read from
`synkro-docs/main@61056c93e6e0783a9199bf56778e9494739bc458`.
The story is [HU-VEN-09, issue #240](https://github.com/code-corhuila/synkro-docs/issues/240).
No documentation repository or applied migration was changed.

The Sales OpenAPI omits a detail `400` response and persistence `500` responses,
although this story and shared guidelines require them. Its server base ends
in `/api/v1`, which would place its `/health` path under that prefix; the
explicit public `/health` requirement is implemented. The closed error catalog
has no method-not-allowed status/code pairing, so unsupported methods use the
documented `400 VALIDATION_ERROR`. Approval is required for a later OpenAPI
amendment; these gaps were reported before implementation.

The entity catalog and domain omit a detail lifecycle flag while ADR-010 and
OpenAPI include it. The existing decoder still rejects inactive details and
inconsistent amounts instead of inventing state. Root inactive documents are
excluded in MongoDB and return `404`. The functional requirement's old `CHECK`
description and parts of the traceability matrix remain historical and do not
describe the delivered MongoDB adapter. ADR-006 supersedes ADR-002's older
direct-token authorship rule: workflow body `createdBy` remains the human author.
