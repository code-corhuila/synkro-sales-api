"""Success paths and documented errors of the Sales API against its OpenAPI contract.

Random Schemathesis cases mostly end in 400/422 or 404, so they cannot show that the SUCCESS responses conform. This flow
creates a real sale, keeps the returned id, reads it back, replays it and provokes the documented errors, and every
response goes through Schemathesis' own validation (status code, content type, response schema, response headers).

Runs inside the pinned Schemathesis image against the live Sales API:
  python contract_flow.py /spec/synkro-sales-api.yaml http://synkro-sales-api:8080/api/v1
Tokens come from the environment (ADMIN_TOKEN, SERVICE_TOKEN), never from arguments.

Output: one line per step, then a summary that keeps passed, failed and skipped apart. Skips are declared with their reason.
"""
import os
import re
import sys
import uuid

import schemathesis
import uri_reference_format  # noqa: F401  (RFC 3986 uri-reference check, shared with the Schemathesis runs)
from schemathesis.core.failures import FailureGroup

SPEC, BASE = sys.argv[1], sys.argv[2].rstrip('/')
ADMIN = {'Authorization': 'Bearer ' + os.environ['ADMIN_TOKEN']}
SERVICE = {'Authorization': 'Bearer ' + os.environ['SERVICE_TOKEN']}
schema = schemathesis.openapi.from_path(SPEC)
SALES = schema['/sales']
SALE = schema['/sales/{id}']
REGISTER = SALES['POST']

CUSTOMER = str(uuid.uuid4())
CREATED_BY = str(uuid.uuid4())
PRODUCT_A, PRODUCT_B = str(uuid.uuid4()), str(uuid.uuid4())
KEY = f'{uuid.uuid4()}:register-sale'
BODY = {'customerId': CUSTOMER, 'createdBy': CREATED_BY, 'details': [
    {'productId': PRODUCT_A, 'quantity': 2, 'unitPriceCents': 12550},
    {'productId': PRODUCT_B, 'quantity': 1, 'unitPriceCents': 99999},
]}
EXPECTED_TOTAL = 2 * 12550 + 99999

passed, failed, skipped = [], [], []


def record(step: str, ok: bool, detail: str = '') -> None:
    (passed if ok else failed).append(step)
    print(f'{"PASS" if ok else "FAIL"}  {step}{(": " + detail) if detail else ""}')


def call(operation, expected_status: int, step: str, **case_kwargs):
    """Send the request, require the status, and validate the whole response against the contract."""
    case = operation.Case(**case_kwargs)
    response = case.call(base_url=BASE)
    try:
        case.validate_response(response)
        conformant = True
        problem = ''
    except FailureGroup as error:
        conformant = False
        problem = '; '.join(sorted({failure.title for failure in error.exceptions}))
    status_ok = response.status_code == expected_status
    record(step, conformant and status_ok,
           f'HTTP {response.status_code} (expected {expected_status})' + ('' if conformant else f', contract violations: {problem}'))
    return response


def body_of(response) -> dict:
    try:
        return response.json()
    except Exception:
        return {}


def skip(step: str, reason: str) -> None:
    skipped.append((step, reason))
    print(f'SKIP  {step}: {reason}')


# ---- success path ------------------------------------------------------------------------------------------------
created = call(REGISTER, 201, 'POST /sales with a valid body returns 201',
               headers={**SERVICE, 'Idempotency-Key': KEY}, body=BODY)
sale = body_of(created)
sale_id = sale.get('saleId', '')
location = (created.headers.get('location') or [''])[0]
record('Location is exactly the path of the created sale', location == f'/api/v1/sales/{sale_id}' and sale_id != '',
       f'Location={location!r}, saleId={sale_id!r}')
record('response carries a commercial number VEN-YYYY-NNNNNN', bool(re.fullmatch(r'VEN-[1-9][0-9]{3}-[0-9]{6}', sale.get('commercialNumber', ''))),
       sale.get('commercialNumber', ''))
record('totals are computed by the service, not sent by the client', sale.get('totalCents') == EXPECTED_TOTAL,
       f'totalCents={sale.get("totalCents")} expected {EXPECTED_TOTAL}')
record('createdBy and customerId are the ones sent', sale.get('createdBy') == CREATED_BY and sale.get('customerId') == CUSTOMER)

# ---- idempotent replay -------------------------------------------------------------------------------------------
replay = call(REGISTER, 200, 'same key and same body replays with 200', headers={**SERVICE, 'Idempotency-Key': KEY}, body=BODY)
again = body_of(replay)
record('replay returns the original sale and number', again.get('saleId') == sale_id and again.get('commercialNumber') == sale.get('commercialNumber'))

# ---- read back (GET 200) ----------------------------------------------------------------------------------------
detail = call(SALE['GET'], 200, 'GET /sales/{id} of the created sale returns 200', headers=ADMIN, path_parameters={'id': sale_id})
found = body_of(detail)
record('detail is the sale that was created', found.get('saleId') == sale_id and found.get('commercialNumber') == sale.get('commercialNumber')
       and found.get('totalCents') == EXPECTED_TOTAL and len(found.get('details', [])) == 2)
listing = call(SALES['GET'], 200, 'GET /sales (ADMIN) returns 200', headers=ADMIN, query={'limit': 100})
record('the created sale is in the listing', sale_id in [item.get('saleId') for item in body_of(listing).get('data', [])])

# ---- documented errors -------------------------------------------------------------------------------------------
call(REGISTER, 400, 'POST without Idempotency-Key is 400', headers={**SERVICE}, body=BODY)
call(REGISTER, 400, 'POST with an empty details array is 400', headers={**SERVICE, 'Idempotency-Key': f'{uuid.uuid4()}:register-sale'},
     body={**BODY, 'details': []})
call(REGISTER, 401, 'POST without a token is 401', headers={'Idempotency-Key': f'{uuid.uuid4()}:register-sale'}, body=BODY)
call(REGISTER, 403, 'POST with an ADMIN token (no sales:register) is 403', headers={**ADMIN, 'Idempotency-Key': f'{uuid.uuid4()}:register-sale'}, body=BODY)
call(REGISTER, 422, 'POST whose amount overflows int64 is 422 BUSINESS_RULE_VIOLATION',
     headers={**SERVICE, 'Idempotency-Key': f'{uuid.uuid4()}:register-sale'},
     body={**BODY, 'details': [{'productId': PRODUCT_A, 'quantity': 2147483647, 'unitPriceCents': 9223372036854775807}]})
# OBSERVATION, not an assertion: the Sales contract documents ONE replay outcome (200, same sale, nothing created) and is silent about a
# body that differs from the stored one (conflicting input is a Workflow rule, ADR-015). What the service does is recorded here
# without being judged correct; it is to be contrasted with the current Sales contract by its owners. It adds no pass and no fail.
before = body_of(listing).get('meta', {}).get('total')
other = REGISTER.Case(headers={**SERVICE, 'Idempotency-Key': KEY}, body={**BODY, 'details': BODY['details'][:1]})
other_response = other.call(base_url=BASE)
after = body_of(SALES['GET'].Case(headers=ADMIN, query={'limit': 100}).call(base_url=BASE)).get('meta', {}).get('total')
other_body = body_of(other_response)
print(f'NOTE  same key, DIFFERENT body (not documented by the Sales contract): HTTP {other_response.status_code}, '
      f'returns the stored sale={other_body.get("saleId") == sale_id}, totalCents={other_body.get("totalCents")} (stored {EXPECTED_TOTAL}), '
      f'sales total before={before} after={after}. The service ignores the new body; this is not asserted as correct.')
call(SALE['GET'], 404, 'GET of an unknown but well-formed id is 404', headers=ADMIN, path_parameters={'id': str(uuid.uuid4())})
call(SALE['GET'], 400, 'GET of a malformed id is 400', headers=ADMIN, path_parameters={'id': 'not-a-uuid'})
call(SALE['GET'], 400, 'GET detail with a query parameter is 400', headers=ADMIN, path_parameters={'id': sale_id}, query={'x': '1'})
call(SALE['GET'], 401, 'GET detail without a token is 401', path_parameters={'id': sale_id})
call(SALES['GET'], 400, 'GET /sales with limit out of range is 400', headers=ADMIN, query={'limit': 1000})

skip('POST /sales answering 422 for the exhausted yearly sequence', 'needs the yearly counter at its ceiling; covered against real MongoDB by HTTP/RegistrationAtTheYearlyCeiling (42-scenario launcher) and by the browser scenario yearly-ceiling-through-workflow')
skip('403 for a SALESPERSON reading another user\'s sale', 'needs a SALESPERSON token and a sale owned by someone else; covered by HTTP/SalespersonOwnership and HTTP/ForeignSaleForbidden (42-scenario launcher)')
skip('500 INTERNAL_ERROR responses', 'only reachable by breaking the database or the stored data; covered by HTTP/DatabaseFailure and persisted-corruption tests')
skip('reports (daily, monthly, top-products)', 'exercised by the Schemathesis GET run of contract-tests.sh against live data, not repeated here')

print(f'contract flow: {len(passed)} passed, {len(failed)} failed, {len(skipped)} skipped (declared above with their reason), schemathesis {schemathesis.SCHEMATHESIS_VERSION}')
sys.exit(1 if failed else 0)
