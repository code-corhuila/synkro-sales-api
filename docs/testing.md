# Testing

Detailed procedures and delivery notes moved from the repository README at
[source 2bcbb25](https://github.com/code-corhuila/synkro-sales-api/blob/2bcbb25330555c9120c16d6a473d7ea9fb1b3bb4/README.md).
Recorded execution results retain their original scope; moving them does not
constitute a new execution or certify a different revision.

Run commands from the repository root unless a step explicitly changes directory.

## Toolchain and vulnerability scanning

The module and the container image build with Go 1.26.11 (`go.mod`,
`deploy/Dockerfile`, `deploy/ci/mongo-integration.sh` and the CI workflow use the
same patch release). Dependencies that carried reachable advisories are at
`golang.org/x/net` v0.60.0 and `golang.org/x/text` v0.42.0 (their transitive
requirements raise `x/crypto`, `x/sys` and `x/sync`). CI runs
`govulncheck ./...` (v1.9.0); any reachable vulnerability fails the build. Run it
locally with `go install golang.org/x/vuln/cmd/govulncheck@v1.9.0 && govulncheck ./...`.

## Running the tests

```bash
go test ./...
```

## Authorship, idempotency and audit checks

Two workflows run on pull requests to `develop`, `qa` and `main`, each checking out
Sales, Workflow and Sales DB and invoking the cross-service harness from the repository root:

For local execution, prepare the sibling checkouts at the revisions selected by
the corresponding workflow. The authorship check pins Workflow to
`5497cb31e2351dae8ed448101fbe882c023d99e1`; the audit check pins it to
`0cf8cab2011434ed32d20a4f7f3092af5b1b09aa`. Both select Sales DB
`e2168fb52f693892b8bbec4c60597b330abeb845`. Run the matching command below
from this Sales API repository, with those checkouts in the named sibling paths.

```bash
python deploy/ci/security_chain.py --sales . --workflow ../synkro-workflow --sales-db ../synkro-sales-db --idempotency
python deploy/ci/security_chain.py --sales . --workflow ../synkro-workflow --sales-db ../synkro-sales-db --audit --idempotency
```

The first (`security-authorship.yml`) verifies the validated JWT author, Mongo
persistence and conflicting key replay; the second (`security-audit.yml`) adds the
safe actor-action-result audit and generic error envelopes. The harness needs Docker
and the sibling checkouts. See the
[registration security contract](sale-registration-security.md) and the
[registration audit](security-audit.md). Hosted check results are not implied by
these instructions.

## Contract verification

### Contract tests (Tier 3 of `11-quality/testing-strategy.md`)

The `qa` policy (`00-governance/git-conventions.md`, `branching-policy.md`) requires "contract tests (Pact)" as a status
check. The project's own testing strategy defines the contract tier as **OpenAPI conformance run by Schemathesis against the
running service** on every PR. `deploy/ci/contract-tests.sh` implements that for this service; the OpenAPI conformance
result is **not** consumer-driven Pact and must not be reported as such. The same launcher also runs the separate
consumer-driven verification described below.

```bash
bash deploy/ci/contract-tests.sh ../synkro-sales-db ../synkro-docs
```

It builds this checkout, starts it against a disposable MongoDB 7.0 carrying the pinned Sales DB migrations (same pin as the
real-MongoDB launcher, read from `deploy/ci/mongo-integration.sh`), mints throwaway RS256 tokens (a SERVICE token with
`sales:register` for `POST`, an ADMIN token for `GET`), and runs Schemathesis 4.33.2 (image pinned by digest) against the
contract at the pinned `synkro-docs` revision (`DOCS_REF`) with the checks `status_code_conformance`,
`response_schema_conformance`, `content_type_conformance` and `response_headers_conformance`. `/health` is checked directly
because the contract documents it without the `/api/v1` prefix. Changing the contract means changing `DOCS_REF` (and the CI
ref) in the same PR; the Python guard tests keep both equal. Reports are written to `.contract-results/` (ignored by Git).
The conformance step does not check business rules (unit tests), database state (integration tests) or consumer expectations (the Pact verification below).

### Consumer-driven QA contracts

Run `bash deploy/ci/contract-tests.sh /path/to/pinned-sales-db /path/to/pinned-docs` with Node 22 and Docker. Generated consumer artifacts,
their published full commit revisions and UTF-8/LF SHA-256 hashes are recorded in
`deploy/ci/pact/provenance.json`; verification rejects changed artifacts.
Pact JS 17.1.4 is isolated from application dependencies. The owned Node runner
installs system CA certificates required by Pact's HTTP verifier. A deliberately
incompatible HTTP provider must receive the requests and fail verification before
the real API/database verification can pass. State setup affects only the launcher's
disposable database. Real verification failures propagate a nonzero exit and the
launcher removes only its own labelled resources. No broker publication or hosted
green result is implied. This gate complements OpenAPI conformance.
