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
| `POST /api/v1/sales` | `SERVICE`, `sub=synkro-workflow`, `sales:register` | `201` + Location; same key and same request returns original sale with `200`; same key with a different request is `422` |
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

### Idempotency key reuse

A key owns exactly one sale. A retry is the *same request* when `customerId`,
`createdBy` and the multiset of lines (`productId`, `quantity`, frozen `unitPriceCents`)
are equal. Line order does not matter, because the Workflow normalises its input by
product before registering. Generated identifiers, the creation instant and the
commercial number are never compared. A replay found by the initial lookup returns
the original sale with `200` and consumes no commercial number.

Reusing the key with any other customer, author, product, quantity or price is
rejected with the service's generic business-rule envelope (the shared error catalog
has no `409`; other services answer a reused key the same way):

```json
{
  "error": "BUSINESS_RULE_VIOLATION",
  "message": "The Idempotency-Key was already used with a different request",
  "details": [{ "field": "Idempotency-Key", "message": "The Idempotency-Key was already used with a different request" }],
  "traceId": "<correlation id>"
}
```

Neither the stored sale nor any stored value is echoed, and no sale is inserted or replaced. The
comparison is made both before the insert and after a duplicate-key error, so
concurrent requests with one key and conflicting input persist exactly one sale and
every loser gets either the original (same request) or this rejection. A Workflow that
receives this `422` with a valid envelope treats the registration as not committed.
As with other insert failures, a concurrent loser can already have advanced the
commercial counter; a gap is accepted to preserve unique numbers without a shared
transaction over the counter and sale. Serial replays and conflicts do not advance it.

### Authorship and trust

`createdBy` is accepted only from the authenticated `synkro-workflow` service
credential; every other caller is rejected (`401`/`403`) before any write or replay.
The Workflow takes it from the validated human token subject and ignores any
`createdBy` in its own request body. Sales computes every subtotal and the total.

Responses use explicit contract DTOs; they never expose an idempotency key or
database metadata. Every request reuses or generates `X-Correlation-Id`; error
`traceId` matches it, and structured JSON logs include it. Unsupported methods
return `400 VALIDATION_ERROR` with `Allow`, preserving the documented code/status
catalog; unknown routes use `404 NOT_FOUND`.
