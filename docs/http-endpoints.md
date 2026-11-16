# Http endpoints

Detailed procedures and delivery notes moved from the repository README at
[source 2bcbb25](https://github.com/code-corhuila/synkro-sales-api/blob/2bcbb25330555c9120c16d6a473d7ea9fb1b3bb4/README.md).
Recorded execution results retain their original scope; moving them does not
constitute a new execution or certify a different revision.

Run commands from the repository root unless a step explicitly changes directory.

## Sales HTTP endpoints (HU-VEN-09)

All Sales operations require an RS256 token verified against the configured
Auth public key. Claims `sub`, `roles`, `permissions`, `iat`, `exp` and `jti`
are required; expiration and issued-at use at most 30 seconds of clock skew.
Human subjects are canonical UUIDs; service subjects are the documented
service names. Identity headers such as `X-User-Id` have no effect.

| Operation | Required signed identity and permission | Result |
|---|---|---|
| `POST /api/v1/sales` | `SERVICE`, `sub=synkro-workflow`, `sales:register` | `201` + Location; equivalent replay returns the original sale with `200`; conflicting key reuse returns `422` |
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
