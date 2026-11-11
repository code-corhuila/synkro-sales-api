"""Regression for the shared `Location` header of the OpenAPI contract.

It runs inside the pinned Schemathesis image and answers one question: with the contract revision under test, does the
SAME validation path that the contract run uses (`response_headers_conformance`) still accept what the Sales API really
returns and still reject malformed values? Declaring a format is not enough: for OpenAPI 3.0 documents the pinned tool
validates with JSON Schema Draft 4, which does not implement the `uri-reference` format, so a declared-but-ignored format
would silently accept anything. Each value below is therefore pushed through the real check.

Usage (inside the container): python location_regression.py /spec
  /spec is the directory holding _shared.yaml of the pinned synkro-docs checkout (read-only).
"""
import json
import sys
import tempfile
import uuid
from pathlib import Path

import requests
import schemathesis
from schemathesis.core.failures import FailureGroup
from schemathesis.core.transport import Response
from schemathesis.specs.openapi.checks import response_headers_conformance

SALE_ID = str(uuid.UUID('d041b243-d04d-4ff4-a27e-4578fdac5c24'))

# (name, Location value, must be accepted)
CASES = [
    ('relative reference as the Sales API returns it', f'/api/v1/sales/{SALE_ID}', True),
    ('absolute URI', f'https://synkro.example.test/api/v1/sales/{SALE_ID}', True),
    ('relative reference with a percent-encoded space', '/api/v1/sales/a%20b', True),
    ('empty value', '', False),
    ('unencoded space', '/api/v1/sales/a b', False),
    ('line break (header injection attempt)', '/api/v1/sales/x\nSet-Cookie: a=b', False),
    ('carriage return', '/api/v1/sales/x\ry', False),
    ('horizontal tab', '/api/v1/sales/x\ty', False),
    ('angle brackets', '/api/v1/sales/<id>', False),
]


def mini_spec(shared_dir: Path) -> str:
    # The header is referenced from the REAL shared contract file, not copied: reverting it must fail this regression.
    shared = (shared_dir / '_shared.yaml').resolve().as_uri()
    return json.dumps({
        'openapi': '3.0.3',
        'info': {'title': 'location-regression', 'version': '1'},
        'paths': {'/created': {'post': {'responses': {'201': {
            'description': 'created',
            'headers': {'Location': {'$ref': f'{shared}#/components/headers/Location'}},
            'content': {'application/json': {'schema': {'type': 'object'}}},
        }}}}},
    })


def response_with(location: str) -> Response:
    request = requests.Request('POST', 'http://synkro-sales-api:8080/api/v1/created').prepare()
    # Built directly (header names lower-case, values as lists) so the value reaches the check exactly as given,
    # including characters an HTTP client could never put on the wire.
    return Response(status_code=201, headers={'content-type': ['application/json'], 'location': [location]},
                    content=b'{}', request=request, elapsed=0.0, verify=False)


def accepted(case, location: str) -> bool:
    try:
        case.validate_response(response_with(location), checks=[response_headers_conformance])
        return True
    except FailureGroup:  # the header check rejected the value; any other error is a real problem and propagates
        return False


def main(shared_dir: Path) -> int:
    with tempfile.TemporaryDirectory() as folder:
        path = Path(folder) / 'location-regression.json'
        path.write_text(mini_spec(shared_dir))
        schema = schemathesis.openapi.from_path(str(path))
    operation = schema['/created']['POST']
    case = operation.Case()
    failures = 0
    for name, value, expected in CASES:
        actual = accepted(case, value)
        verdict = 'ok' if actual == expected else 'WRONG'
        if actual != expected:
            failures += 1
        print(f'{verdict:5} {"accepted" if actual else "rejected":9} (expected {"accept" if expected else "reject"}): {name}: {value!r}')
    print(f'Location regression: {len(CASES) - failures} of {len(CASES)} expectations met, schemathesis {schemathesis.SCHEMATHESIS_VERSION}')
    return 1 if failures else 0


if __name__ == '__main__':
    sys.exit(main(Path(sys.argv[1])))
