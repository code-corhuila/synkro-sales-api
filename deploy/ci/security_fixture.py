"""Controlled Customers/Products and HTTP probe for the real Workflow/Sales test."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.request import Request, urlopen
from urllib.error import HTTPError
import json
import uuid

with open('/fixtures/config.json', encoding='utf-8') as source:
    config = json.load(source)
reservations = {}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, status, value):
        data = json.dumps(value).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def body(self):
        return json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))) or b'{}')

    def do_GET(self):
        if self.path == '/health':
            return self.reply(200, {'status': 'ok'})
        if self.headers.get('Authorization') != 'Bearer ' + config['service']:
            return self.reply(403, {})
        if self.path.startswith('/api/v1/customers/'):
            return self.reply(200, dict(customerId=config['customer'], name=config['name'],
                                        identityDocument=config['document'], registrationDate='2026-10-12T12:00:00Z', active=True))
        if self.path.startswith('/api/v1/stock-reservations/'):
            item = reservations.get(self.path.rsplit('/', 1)[-1])
            return self.reply(200 if item else 404, item or {})
        self.reply(404, {})

    def do_POST(self):
        data = self.body()
        if self.path == '/probe':
            if data['host'] not in ('sales', 'workflow'):
                return self.reply(400, {})
            request = Request('http://' + data['host'] + ':8080' + data['path'],
                              data=None if data.get('body') is None else json.dumps(data['body']).encode(),
                              headers=data['headers'], method=data['method'])
            try:
                response = urlopen(request, timeout=20)
            except HTTPError as error:
                response = error
            with response:
                raw = response.read().decode()
                return self.reply(200, dict(status=response.status, body=json.loads(raw), headers=dict(response.headers)))
        if self.headers.get('Authorization') != 'Bearer ' + config['service']:
            return self.reply(403, {})
        if self.path == '/api/v1/stock-reservations':
            identity = str(uuid.uuid4())
            item = dict(reservationId=identity, status='RESERVED', createdAt='2026-10-12T12:00:00Z',
                        lines=[dict(line, lineId=str(uuid.uuid4()), unitPriceCents=125) for line in data['lines']])
            reservations[identity] = item
            return self.reply(201, item)
        if self.path.endswith('/release'):
            item = reservations[self.path.split('/')[-2]]
            item.update(status='RELEASED', releasedAt='2026-10-12T12:00:00Z')
            return self.reply(200, item)
        self.reply(404, {})


ThreadingHTTPServer(('0.0.0.0', 8080), Handler).serve_forever()
