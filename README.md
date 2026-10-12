# synkro-sales-api

> sales bounded context: service API

Part of the **SynkroTech SAS Sales Management System** — organization `code-corhuila`.
Governance and documentation live in [`synkro-docs`](https://github.com/code-corhuila/synkro-docs).

## Running locally

Apply the Sales DB migrations first. Supply `MONGODB_URI` for `sales_app` and
exactly one of `JWT_PUBLIC_KEY` (PEM, including literal escaped `\n`) or
`JWT_PUBLIC_KEY_FILE` (an accessible public PEM file). Missing or invalid keys
stop startup; there is no development authentication bypass. Never use an
administrator URI or put credentials/private keys in this repository.

```bash
go run ./cmd/sales-api
```

The service listens on `:8080` (override with `HTTP_PORT`). Verify it:

```bash
curl http://localhost:8080/health
```

Public `/health` pings this service's MongoDB replica set: `200` with database
`ok`, or `503` with database `down`. `MONGODB_TIMEOUT` defaults to `5s` (maximum
`10s`), and `MONGODB_MAX_POOL_SIZE` defaults to `10`. The server has explicit
header/read/write/idle limits of 5/10/15/60 seconds and a 20-second shutdown.

## Sales HTTP endpoints (HU-VEN-09)

All Sales operations require an RS256 token verified against the configured
Auth public key. Claims `sub`, `roles`, `permissions`, `iat`, `exp` and `jti`
are required; expiration and issued-at use at most 30 seconds of clock skew.
Human subjects are canonical UUIDs; service subjects are the documented
service names. Identity headers such as `X-User-Id` have no effect.

| Operation | Required signed identity and permission | Result |
|---|---|---|
| `POST /api/v1/sales` | `SERVICE`, `sub=synkro-workflow`, `sales:register` | `201` + Location; same key returns original sale with `200` |
| `GET /api/v1/sales` | `ADMIN` or `SALESPERSON`, `sales:read` | Paginated active sales; salesperson ownership applies to both database reads and counts |
| `GET /api/v1/sales/{id}` | `ADMIN` or `SALESPERSON`, `sales:read` | Active sale; another salesperson's sale returns `403`; missing/inactive returns `404` |

Registration accepts only `customerId`, `createdBy` and 1–100 `details` with
`productId`, integer `quantity`, and integer `unitPriceCents`. It requires an
8–128-character `Idempotency-Key`. The trusted workflow supplies the original
human author; Sales performs no customer/stock calls. The existing domain
generates all calculated amounts and detects arithmetic/storage invariants.
Invalid input is `400`; domain invariants in a new request are `422`; stored
corruption and unexpected persistence failures are neutral `500` responses.

Lists accept only `page` (default 1), `limit` (1–100, default 20), optional
`customerId`, and optional `from`/`to` in `YYYY-MM-DD` format. UTC days are
inclusive using an exclusive next-day end. Sorting is `date DESC, _id DESC`.
Empty lists have `data: []`, `total: 0`, and `totalPages: 0`. Unknown, repeated,
empty or malformed filters are rejected before querying MongoDB.

Responses use explicit contract DTOs; they never expose an idempotency key or
database metadata. Every request reuses or generates `X-Correlation-Id`; error
`traceId` matches it, and structured JSON logs include it. Unsupported methods
return `400 VALIDATION_ERROR` with `Allow`, preserving the documented code/status
catalog; unknown routes use `404 NOT_FOUND`.

## Running the tests

```bash
go test ./...
```

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
