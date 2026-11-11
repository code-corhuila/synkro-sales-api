"""Regression for the shared `Location` header of the OpenAPI contract.

Runs inside the pinned Schemathesis image. Every value goes through `response_headers_conformance`, the check the contract
run uses, against the REAL `_shared.yaml` (referenced, never copied or modified). Three layers are kept apart:

  declared   the schema says `format: uri-reference` (documentation and validators that support it)
  lexical    the schema `pattern` limits characters and percent-encoding. It is NOT the URI grammar.
  structural an RFC 3986 parser checks the structure (authority, first segment...). With Schemathesis 4.33.2 and OpenAPI 3.0
             this is NOT done by default (Draft 4 ignores the format), so uri_reference_format.py registers it.

Usage (inside the container, PYTHONPATH containing this directory):
  python location_regression.py /spec job        the job configuration: lexical + structural, every expectation must hold
  python location_regression.py /spec lexical    without the structural check: the lexical restriction alone, and the
                                                 structural cases that it is documented NOT to catch (they must pass)
  python location_regression.py /spec cli        the structural check reached through the Schemathesis CLI mechanism
                                                 (SCHEMATHESIS_HOOKS) against a stub server, with and without it
"""
import http.server
import json
import os
import subprocess
import sys
import tempfile
import threading
import uuid
from pathlib import Path

import requests

SALE_ID = str(uuid.UUID('d041b243-d04d-4ff4-a27e-4578fdac5c24'))

# (name, Location value, must be accepted, caught only by the structural check)
CASES = [
    ('relative path as the Sales API returns it', f'/api/v1/sales/{SALE_ID}', True, False),
    ('relative path, no leading slash', f'sales/{SALE_ID}', True, False),
    ('relative path, dot segment before a colon', './1:abc', True, False),
    ('relative path with a percent-encoded space', '/api/v1/sales/a%20b', True, False),
    ('absolute URL', f'https://synkro.example.test/api/v1/sales/{SALE_ID}', True, False),
    ('absolute URL with an IPv6 literal', 'http://[::1]/api/v1/sales/x', True, False),
    ('empty value', '', False, False),
    ('unencoded space', '/api/v1/sales/a b', False, False),
    ('line break inside', '/api/v1/sales/x\nSet-Cookie: a=b', False, False),
    ('carriage return inside', '/api/v1/sales/x\ry', False, False),
    ('horizontal tab inside', '/api/v1/sales/x\ty', False, False),
    ('angle brackets', '/api/v1/sales/<id>', False, False),
    ('trailing LF', f'/api/v1/sales/{SALE_ID}\n', False, False),
    ('trailing CR', f'/api/v1/sales/{SALE_ID}\r', False, False),
    ('trailing CRLF', f'/api/v1/sales/{SALE_ID}\r\n', False, False),
    ('trailing tab', f'/api/v1/sales/{SALE_ID}\t', False, False),
    ('incomplete percent escape', '/api/v1/sales/a%2', False, False),
    ('non-hexadecimal percent escape', '/api/v1/sales/a%zz', False, False),
    ('malformed IPv6 authority', 'http://[', False, True),
    ('first segment that looks like a scheme but is not one', '1:abc', False, True),
]


def mini_spec(shared_dir: Path) -> str:
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


def run_library(shared_dir: Path, structural: bool) -> int:
    if structural:
        import uri_reference_format  # noqa: F401  (installs the RFC 3986 check)
    import schemathesis
    from schemathesis.core.failures import FailureGroup
    from schemathesis.core.transport import Response
    from schemathesis.specs.openapi.checks import response_headers_conformance

    with tempfile.TemporaryDirectory() as folder:
        path = Path(folder) / 'location-regression.json'
        path.write_text(mini_spec(shared_dir))
        schema = schemathesis.openapi.from_path(str(path))
    case = schema['/created']['POST'].Case()
    request = requests.Request('POST', 'http://synkro-sales-api:8080/api/v1/created').prepare()

    def accepted(location: str) -> bool:
        response = Response(status_code=201, headers={'content-type': ['application/json'], 'location': [location]},
                            content=b'{}', request=request, elapsed=0.0, verify=False)
        try:
            case.validate_response(response, checks=[response_headers_conformance])
            return True
        except FailureGroup:
            return False

    wrong = 0
    for name, value, expected, structural_only in CASES:
        note = ''
        if not structural and structural_only:
            expected = True  # documented limit of the pattern alone: it does not check the URI structure
            note = ' [documented limit of the lexical pattern]'
        actual = accepted(value)
        ok = actual == expected
        wrong += 0 if ok else 1
        print(f'{"ok   " if ok else "WRONG"} {"accepted" if actual else "rejected"} (expected {"accept" if expected else "reject"}): {name}: {value!r}{note}')
    print(f'Location regression ({"job: lexical + structural" if structural else "lexical only"}): {len(CASES) - wrong} of {len(CASES)} '
          f'expectations met, schemathesis {schemathesis.SCHEMATHESIS_VERSION}')
    return 1 if wrong else 0


class _Stub(http.server.BaseHTTPRequestHandler):
    location = ''

    def do_POST(self):  # noqa: N802
        self.send_response(201)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Location', type(self).location)
        self.send_header('Content-Length', '2')
        self.end_headers()
        self.wfile.write(b'{}')

    def log_message(self, *args):
        pass


def run_cli(shared_dir: Path) -> int:
    """The structural check, reached the way the job reaches it: the Schemathesis CLI with SCHEMATHESIS_HOOKS."""
    with tempfile.TemporaryDirectory() as folder:
        path = Path(folder) / 'location-regression.json'
        path.write_text(mini_spec(shared_dir))
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), _Stub)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        url = f'http://127.0.0.1:{server.server_address[1]}'
        plan = [  # (Location, hooks on, must the CLI run pass)
            (f'/api/v1/sales/{SALE_ID}', True, True),
            ('1:abc', False, True),   # the pattern alone does not catch it: without the hook the run passes (limit of the pattern)
            ('1:abc', True, False),   # with the hook the CLI run fails on response_headers_conformance
            ('http://[', True, False),
        ]
        wrong = 0
        for location, hooks, expect_pass in plan:
            _Stub.location = location
            env = dict(os.environ, PYTHONPATH=os.path.dirname(os.path.abspath(__file__)))
            if hooks:
                env['SCHEMATHESIS_HOOKS'] = 'uri_reference_format'
            result = subprocess.run(['schemathesis', 'run', str(path), '--url', url, '--checks', 'response_headers_conformance',
                                     '--max-examples', '1', '--no-color'], env=env, capture_output=True, text=True, timeout=120)
            passed = result.returncode == 0
            ok = passed == expect_pass
            wrong += 0 if ok else 1
            print(f'{"ok   " if ok else "WRONG"} CLI run {"passed" if passed else "failed"} (expected {"pass" if expect_pass else "fail"}): '
                  f'Location={location!r}, hooks {"on" if hooks else "off"}')
            if not ok:
                print(result.stdout[-1500:])
        server.shutdown()
    print(f'CLI mechanism: {len(plan) - wrong} of {len(plan)} expectations met')
    return 1 if wrong else 0


if __name__ == '__main__':
    shared, mode = Path(sys.argv[1]), sys.argv[2]
    if mode == 'job':
        sys.exit(run_library(shared, True))
    if mode == 'lexical':
        sys.exit(run_library(shared, False))
    if mode == 'cli':
        sys.exit(run_cli(shared))
    sys.exit(f'unknown mode {mode}')
