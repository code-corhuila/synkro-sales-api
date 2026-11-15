# Sale registration audit

Sales emits a structured `audit` event for every authorized registration result and
for authorization or validation refusals that reach the registration handler. Its
explicit fields are `service`, `correlationId`, `sagaId`, `step`, `actorId`,
`authorId`, `action` and `result`. The production logger adds `timestamp`, `level`
and `message`. Optional values are omitted; results use a closed vocabulary.

`actorId` comes from verified JWT claims. `authorId` comes from the accepted sale.
Saga and step are extracted only from `<canonical UUID>:<known step>`; the raw key
is never logged. Opaque correlation metadata is preserved as the HTTP contract
requires and safely encoded by the JSON logger. Request bodies, other headers,
credentials and raw exception messages are outside the allowlist.

Mandatory Mongo scenarios verify the allowlist, trace coherence and generic error
envelopes. The cross-service `security_chain.py --audit --idempotency` additionally
captures actual Workflow/Sales application output and errors with known token and
synthetic personal-data markers. It verifies the human author, both verified audit
actors and one trace/saga/step across both services. PostgreSQL and MongoDB are real
and migrated; Customers and Products are controlled participants.

Related requirements: FR-005/FR-007 and NFR-004/NFR-009; issue
[286](https://github.com/code-corhuila/synkro-docs/issues/286). Canonical synkro-docs
reconciliation remains a separate change.
