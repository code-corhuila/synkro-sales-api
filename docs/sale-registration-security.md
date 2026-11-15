# Sale registration security contract

Only a verified `SERVICE` token with subject `synkro-workflow` and permission
`sales:register` may call `POST /api/v1/sales`. Authentication and authorization
run before a write or an idempotent replay; user-supplied identity headers cannot
grant access. Workflow obtains `createdBy` from the validated initiating human
JWT, ignoring body-supplied authorship. Sales computes subtotals and the total.

## Reusing an idempotency key

A key owns one sale. Equivalent input means equal customer, author and multiset
of lines with the same product, quantity and frozen unit price. Line order,
generated identifiers, creation date and commercial number do not determine
equivalence.

The same input returns the original sale with `200`. A different customer,
author, product, quantity or price receives HTTP `422` with this generic envelope:

```json
{
  "error": "BUSINESS_RULE_VIOLATION",
  "message": "The Idempotency-Key was already used with a different request",
  "details": [{"field": "Idempotency-Key", "message": "The Idempotency-Key was already used with a different request"}],
  "traceId": "<correlation id>"
}
```

The rejection does not return stored input or insert/replace a sale.
`Sale.SameRegistration` is checked both on the initial lookup and after a
duplicate-key race; the unique index remains the atomic insertion boundary.
The repository error maps explicitly through the application to the business
error envelope, rather than becoming a persistence failure.

Serial replays/conflicts consume no commercial number. A concurrent insert loser
may already have advanced the counter; the existing gap convention preserves
unique numbers without a shared counter/sale transaction. Every retry still
converges to at most one persisted sale.

The mandatory Mongo scenarios and their evidence gates verify unauthorized
registration/replay, conflicting inputs, concurrency and calculated amounts. The
cross-service harness additionally checks the validated JWT author through real
Workflow and Sales processes and migrated PostgreSQL/MongoDB; Customers and
Products are controlled participants.

Related work: [284](https://github.com/code-corhuila/synkro-docs/issues/284) and
[285](https://github.com/code-corhuila/synkro-docs/issues/285). Canonical design
reconciliation remains separate.
