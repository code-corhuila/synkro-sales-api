# Reports

Detailed procedures and delivery notes moved from the repository README at
[source 2bcbb25](https://github.com/code-corhuila/synkro-sales-api/blob/2bcbb25330555c9120c16d6a473d7ea9fb1b3bb4/README.md).
Recorded execution results retain their original scope; moving them does not
constitute a new execution or certify a different revision.

Run commands from the repository root unless a step explicitly changes directory.

## Sales reports (HU-VEN-10)

| Operation | Required signed identity and permission | Result |
|---|---|---|
| `GET /api/v1/sales/reports/daily` | `ADMIN` or `SALESPERSON`, `reports:read` | `day`, `dailyTotalCents`, `saleCount`, newest day first |
| `GET /api/v1/sales/reports/monthly` | `ADMIN` or `SALESPERSON`, `reports:read` | `month` (first day, `YYYY-MM-DD`), `monthlyTotalCents`, `saleCount`, newest first |
| `GET /api/v1/sales/reports/top-products` | `ADMIN` or `SALESPERSON`, `reports:read` | `productId`, `totalQuantity`, `totalRevenueCents`; quantity descending, then `productId` ascending |

Each response is `{data, meta}` with the shared `page`/`limit`/`total`/`totalPages`
metadata. Reports need `reports:read` (`07-api/authentication.md`), not `sales:read`:
an ADMIN or SALESPERSON token without it is `403`, as are `INVENTORY` and `SERVICE`
tokens. A SALESPERSON's `createdBy` match is taken from the verified token subject and
applied in the MongoDB `$match`; there is no `createdBy`/`userId` query parameter
(any such parameter is a `400`). Report paths are dispatched explicitly before the
sale-identifier match, so `reports` is never read as a sale ID; any other path under
`/api/v1/sales/reports` is `404`, and only `GET` is accepted.

Accepted parameters are `from`, `to`, `page` and `limit`, validated before MongoDB is
touched. `from` is an inclusive UTC midnight and `to` an inclusive UTC day, applied as
the exclusive next midnight. Filters select *sales* by their stored date before
grouping, so a monthly report with `from=2026-10-15` sums only sales from that day on.

### Aggregation design

All three are live `sales.sale` aggregation pipelines (ADR-010 Decision 4); nothing is
stored or cached. The first stage matches `active: true`, the date range and, for a
SALESPERSON, `createdBy` (so `idx_sale_date` / `idx_sale_created_by_date` serve it).
Daily and monthly then `$group` on `$dateTrunc` in `UTC` (a month is already its first
day), summing `totalCents` and counting sales. Top products `$unwind`s `details`,
keeps `details.active: true`, groups by `details.productId` and sums `quantity` and
`subtotalCents`; two lines of one product in a sale both count.

Pagination happens after grouping and ordering. One `$facet` returns the requested
slice (`$sort`/`$skip`/`$limit`) and the count of *all* groups from the same snapshot,
so `meta.total` cannot disagree with `data`; the alternative (a second `$count`
aggregation) can disagree under concurrent writes. `allowDiskUse` is set, and no
sale is read into application memory.

MongoDB's `$sum` silently turns an `int64` overflow into a `double`. Aggregate results
are decoded strictly (integer BSON types only, non-negative, exact field set, UTC
midnight keys, canonical product UUIDs); anything else is an internal failure, shown
to clients as a neutral `500 INTERNAL_ERROR` and never rounded.

Report amounts come from the *stored* `totalCents` and `subtotalCents`; unlike
`GET /sales/{id}`, reports do not re-derive them through the domain. A sale's
`totalCents` is counted whole in daily/monthly totals even if some of its lines are
inactive, while top products drops inactive lines (ADR-010). Current writes only
create active lines.

Coverage from the real-MongoDB run (`deploy/ci/mongo-integration.sh`, race detector
on): domain 100.00%, application 95.71%, infrastructure 88.30%, global 89.72%
(minimums 90/80/60/80). The evidence gate now also requires nine `Reports/*` and five
`HTTP/Reports*` scenarios to execute and pass.
