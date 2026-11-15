"""Real Workflow JWT -> HTTP Sales -> migrated MongoDB; only Customers/Products controlled.

Uses disposable labelled resources and no host ports. Raw diagnostics and credentials
are never printed. Run with --audit to require the joint structured audit trail too.
"""
import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import tempfile
import time
import uuid
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding, rsa

SENSITIVE = []

def run(args, *, data=None, env=None, timeout=180, required=True):
    result = subprocess.run(args, input=data, capture_output=True, env=env, timeout=timeout)
    if required and result.returncode:
        diagnostic = (result.stderr + result.stdout).decode(errors='replace')
        for value in SENSITIVE:
            diagnostic = diagnostic.replace(value, '[REDACTED]')
        raise RuntimeError('Required isolated operation failed: ' + args[1] + ': ' + diagnostic[-1200:])
    return result


def check(value, description):
    if not value:
        raise RuntimeError(description)
    print('PASS ' + description, flush=True)


def wait(probe, seconds=90):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            value = probe()
            if value:
                return value
        except Exception:
            pass
        time.sleep(.3)
    raise RuntimeError('Bounded readiness timeout')


def jwt(key, subject, roles, permissions, marker):
    def encode(value):
        return base64.urlsafe_b64encode(json.dumps(value, separators=(',', ':')).encode()).decode().rstrip('=')
    value = encode(dict(alg='RS256', typ='JWT')) + '.' + encode(dict(
        sub=subject, roles=roles, permissions=permissions, iat=int(time.time()) - 5,
        exp=int(time.time()) + 3600, jti=marker))
    signature = key.sign(value.encode(), padding.PKCS1v15(), hashes.SHA256())
    return value + '.' + base64.urlsafe_b64encode(signature).decode().rstrip('=')


def execute(sales, workflow, database, audit=False, idempotency=False):
    identity = uuid.uuid4().hex
    prefix = 'synkro-security-chain-' + identity
    network = prefix + '-net'
    containers, images = [], []
    caller, spoof, customer, product, trace = (str(uuid.uuid4()) for _ in range(5))
    marker = 'TOKEN_MARKER_' + identity
    name, document = 'SyntheticCustomer_' + identity, 'SyntheticDocument_' + identity
    password = 'PASSWORD_MARKER_' + identity
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    human = jwt(key, caller, ['SALESPERSON'], ['sales:create'], marker)
    service = jwt(key, 'synkro-workflow', ['SERVICE'],
                  ['customers:read', 'stock:reserve', 'stock:release', 'sales:register'], marker)
    admin_password, sales_password, workflow_password = (secrets.token_hex(24) for _ in range(3))
    SENSITIVE.extend((human, service, admin_password, sales_password, workflow_password))
    env = dict(os.environ, CI_ADMIN_PASSWORD=admin_password, CI_SALES_APP_PASSWORD=sales_password)

    def launch(suffix, image, args=(), command=()):
        target = prefix + '-' + suffix
        containers.append(target)
        run(['docker', 'run', '-d', '--name', target, '--label', 'synkro.security-chain=' + identity,
             '--network', network, *args, image, *command])
        return target

    def build(suffix, source, dockerfile):
        image = prefix + '-' + suffix + ':test'
        images.append(image)
        print('BUILD isolated ' + suffix, flush=True)
        run(['docker', 'build', '--label', 'synkro.security-chain=' + identity, '-t', image,
             '-f', str(dockerfile), str(source)], timeout=1200)
        return image

    def mongo(expression, auth=True):
        credential = ['--username', 'admin', '--password', admin_password, '--authenticationDatabase', 'admin'] if auth else []
        return run(['docker', 'exec', mongodb, 'mongosh', '--quiet', *credential, '--eval', expression], timeout=30).stdout.decode().strip()

    def sql(expression):
        return run(['docker', 'exec', '-i', postgres, 'psql', '-X', '-U', 'postgres', '-d', 'synkro',
                    '-qAt', '-v', 'ON_ERROR_STOP=1'], data=expression.encode()).stdout.decode().strip()

    def probe(host, path, method='GET', body=None, credential=human, idem=None, correlation=trace):
        headers = {'Content-Type': 'application/json', 'X-Correlation-Id': correlation}
        if credential:
            headers['Authorization'] = 'Bearer ' + credential
        if idem:
            headers['Idempotency-Key'] = idem
        payload = json.dumps(dict(host=host, path=path, method=method, body=body, headers=headers)).encode()
        code = "import sys,urllib.request; print(urllib.request.urlopen(urllib.request.Request('http://localhost:8080/probe',data=sys.stdin.buffer.read(),headers={'Content-Type':'application/json'}),timeout=25).read().decode())"
        return json.loads(run(['docker', 'exec', '-i', fixture, 'python', '-c', code], data=payload, timeout=35).stdout)

    network_created = False
    with tempfile.TemporaryDirectory(prefix='synkro-security-chain-') as temporary:
        folder = Path(temporary)
        (folder / 'public.pem').write_bytes(key.public_key().public_bytes(serialization.Encoding.PEM, serialization.PublicFormat.SubjectPublicKeyInfo))
        (folder / 'workflow-password').write_text(workflow_password)
        (folder / 'service-token').write_text(service)
        (folder / 'config.json').write_text(json.dumps(dict(service=service, customer=customer, name=name, document=document)))
        shutil.copyfile(Path(__file__).with_name('security_fixture.py'), folder / 'fixture.py')
        try:
            run(['docker', 'network', 'create', '--label', 'synkro.security-chain=' + identity, network])
            network_created = True
            mongodb = launch('mongo', 'mongo:7.0', ['--tmpfs', '/data/db', '--tmpfs', '/data/configdb', '--entrypoint', 'bash'],
                             ['-c', 'openssl rand -base64 48 > /tmp/keyfile && chmod 400 /tmp/keyfile && chown mongodb /tmp/keyfile && exec docker-entrypoint.sh mongod --replSet rs0 --bind_ip_all --keyFile /tmp/keyfile'])
            wait(lambda: mongo('db.adminCommand({ping:1}).ok', False) == '1')
            mongo('rs.initiate({_id:"rs0",members:[{_id:0,host:"' + mongodb + ':27017"}]})', False)
            wait(lambda: mongo('db.hello().isWritablePrimary', False) == 'true')
            bootstrap = 'db.createUser({user:"admin",pwd:process.env.CI_ADMIN_PASSWORD,roles:["root"]}); db.auth("admin",process.env.CI_ADMIN_PASSWORD); db.createUser({user:"sales_app",pwd:process.env.CI_SALES_APP_PASSWORD,roles:[]});'
            run(['docker', 'exec', '-e', 'CI_ADMIN_PASSWORD', '-e', 'CI_SALES_APP_PASSWORD', mongodb,
                 'mongosh', '--quiet', 'admin', '--eval', bootstrap], env=env)
            runner = build('migrations', database / 'deploy', database / 'deploy/liquibase.Dockerfile')
            migration_env = dict(env, LIQUIBASE_COMMAND_URL='mongodb://admin:' + admin_password + '@' + mongodb + ':27017/sales?replicaSet=rs0&authSource=admin',
                                 LIQUIBASE_COMMAND_CHANGELOG_FILE='changelog/changelog-master.yaml')
            run(['docker', 'run', '--rm', '--label', 'synkro.security-chain=' + identity, '--network', network,
                 '-e', 'LIQUIBASE_COMMAND_URL', '-e', 'LIQUIBASE_COMMAND_CHANGELOG_FILE',
                 '-v', str(database) + ':/liquibase/changelog-root:ro', '-w', '/liquibase/changelog-root', runner, 'update'], env=migration_env, timeout=300)
            postgres = launch('pg', 'postgres:16.15-alpine3.23', ['--tmpfs', '/var/lib/postgresql/data', '-e', 'POSTGRES_DB=synkro', '-e', 'POSTGRES_PASSWORD=' + admin_password])
            wait(lambda: run(['docker', 'exec', postgres, 'pg_isready', '-U', 'postgres', '-d', 'synkro'], required=False).returncode == 0)
            sql("REVOKE CREATE ON SCHEMA public FROM PUBLIC; CREATE ROLE workflow_app LOGIN PASSWORD '" + workflow_password + "'")
            flyway_env = dict(env, FLYWAY_URL='jdbc:postgresql://' + postgres + ':5432/synkro', FLYWAY_USER='postgres', FLYWAY_PASSWORD=admin_password)
            run(['docker', 'run', '--rm', '--label', 'synkro.security-chain=' + identity, '--network', network,
                 '-e', 'FLYWAY_URL', '-e', 'FLYWAY_USER', '-e', 'FLYWAY_PASSWORD', '-v', str(workflow / 'db') + ':/flyway/workflow:ro',
                 'flyway/flyway:11.20.3-alpine', '-workingDirectory=/flyway/workflow', '-configFiles=flyway.toml', 'migrate'], env=flyway_env)
            fixture = launch('fixture', 'python:3.13.7-alpine3.22', ['--network-alias', 'participants', '-v', str(folder) + ':/fixtures:ro'], ['python', '/fixtures/fixture.py'])
            sales_image = build('sales', sales, sales / 'deploy/Dockerfile')
            sales_container = launch('sales', sales_image, ['--network-alias', 'sales', '-v', str(folder / 'public.pem') + ':/run/public.pem:ro',
                '-e', 'JWT_PUBLIC_KEY_FILE=/run/public.pem', '-e', 'MONGODB_URI=mongodb://sales_app:' + sales_password + '@' + mongodb + ':27017/sales?replicaSet=rs0&authSource=admin&retryWrites=false'])
            workflow_image = build('workflow', workflow, workflow / 'deploy/Dockerfile')
            workflow_container = launch('workflow', workflow_image, ['--network-alias', 'workflow', '-v', str(folder) + ':/fixtures:ro',
                '-e', 'WORKFLOW_DATABASE_URL=jdbc:postgresql://' + postgres + ':5432/synkro', '-e', 'WORKFLOW_APP_PASSWORD_FILE=/fixtures/workflow-password',
                '-e', 'JWT_PUBLIC_KEY_FILE=/fixtures/public.pem', '-e', 'SERVICE_TOKEN_FILE=/fixtures/service-token',
                '-e', 'CUSTOMERS_BASE_URL=http://participants:8080', '-e', 'PRODUCTS_BASE_URL=http://participants:8080', '-e', 'SALES_BASE_URL=http://sales:8080'])
            wait(lambda: probe('sales', '/health', credential=None)['status'] == 200)
            wait(lambda: probe('workflow', '/health', credential=None)['status'] == 200)
            input_body = dict(customerId=customer, createdBy=spoof, name=name, identityDocument=document, password=password,
                              lines=[dict(productId=product, quantity=2)])
            first = probe('workflow', '/api/v1/sagas/register-sale', 'POST', input_body, idem='chain-' + identity)
            check(first['status'] == 201, 'validated human JWT starts actual Workflow')
            saga_id = first['body']['sagaId']
            def finished():
                response = probe('workflow', '/api/v1/sagas/' + saga_id)
                return response if response['body'].get('status') == 'COMPLETED' else None
            final = wait(finished)
            sale_id = final['body']['saleId']
            sale_key = saga_id + ':register-sale'
            persisted = 'const d=db.getSiblingDB("sales").sale.findOne({idempotencyKey:' + json.dumps(sale_key) + '}); print(JSON.stringify({count:db.getSiblingDB("sales").sale.countDocuments({idempotencyKey:' + json.dumps(sale_key) + '}),author:d.createdBy===' + json.dumps(caller) + ',total:Number(d.totalCents),subtotal:Number(d.details[0].subtotalCents)}))'
            stored = json.loads(mongo(persisted))
            check(stored == dict(count=1, author=True, total=250, subtotal=250), 'JWT sub survives spoofed body through real Workflow and persisted Mongo sale; computed integer totals')
            sale_body = dict(customerId=customer, createdBy=caller, details=[dict(productId=product, quantity=2, unitPriceCents=125)])
            replay = probe('sales', '/api/v1/sales', 'POST', sale_body, service, sale_key)
            check(replay['status'] == 200 and replay['body']['saleId'] == sale_id, 'authorized exact replay returns original sale')
            conflict_body = dict(sale_body, details=[dict(productId=product, quantity=2, unitPriceCents=999)])
            conflict = probe('sales', '/api/v1/sales', 'POST', conflict_body, service, sale_key)
            if idempotency:
                check(conflict['status'] == 422 and conflict['body'].get('error') == 'BUSINESS_RULE_VIOLATION', 'conflicting frozen price receives generic rejection')
                check(all(value not in json.dumps(conflict['body']) for value in (sale_id, caller, customer, product)), 'conflict does not reveal stored sale input')
            denied = probe('sales', '/api/v1/sales', 'POST', sale_body, human, sale_key)
            check(denied['status'] == 403 and json.loads(mongo(persisted))['count'] == 1, 'human cannot replay or duplicate persisted sale')
            malformed = probe('workflow', '/api/v1/sagas/register-sale', 'POST', dict(input_body, customerId=name), idem='invalid-' + identity)
            check(malformed['status'] == 400, 'synthetic personal data rejected with generic validation envelope')
            bad = probe('sales', '/api/v1/sales', 'POST', sale_body, marker, sale_key)
            check(bad['status'] == 401, 'known token marker rejected')
            run(['docker', 'pause', mongodb])
            try:
                unavailable = probe('sales', '/api/v1/sales', 'POST', sale_body, service, str(uuid.uuid4()) + ':register-sale')
                check(unavailable['status'] == 500 and unavailable['body'].get('error') == 'INTERNAL_ERROR', 'real database timeout returns generic error envelope')
            finally:
                run(['docker', 'unpause', mongodb])
            logs = [run(['docker', 'logs', container]).stdout.decode() + run(['docker', 'logs', container]).stderr.decode() for container in (workflow_container, sales_container)]
            captured = '\n'.join(logs) + json.dumps([malformed, bad, denied, unavailable] + ([conflict] if idempotency else []))
            check(all(value not in captured for value in (human, service, marker, name, document, password, 'Authorization', 'MongoServerError', 'MongoWriteException')), 'joint Workflow/Sales application logs and error responses contain no sensitive markers')
            if audit:
                for source, raw in zip(('Workflow', 'Sales'), logs):
                    events = []
                    for line in raw.splitlines():
                        try:
                            event = json.loads(line)
                            if event.get('correlationId') == trace and event.get('sagaId') == saga_id:
                                events.append(event)
                        except (ValueError, AttributeError):
                            continue
                    actor = caller if source == 'Workflow' else 'synkro-workflow'
                    action, result = ('execute-step', 'succeeded') if source == 'Workflow' else ('register-sale', 'created')
                    allowed = {'timestamp', 'service', 'correlationId', 'sagaId', 'step', 'actorId', 'action', 'result'}
                    if source == 'Sales':
                        allowed.update(('level', 'message', 'authorId'))
                    check(any(all(field in event for field in ('timestamp', 'service', 'step', 'actorId', 'action', 'result'))
                              and event['step'] == 'register-sale' and event['actorId'] == actor
                              and event['action'] == action and event['result'] == result
                              and (source != 'Sales' or event.get('authorId') == caller)
                              and set(event).issubset(allowed) for event in events),
                          source + ' allowlisted audit links trace, saga, step, verified actor, action, result and time')
            print('PASS cross-service security chain; Customers and Products are controlled; Workflow, Sales and both stores are real', flush=True)
        finally:
            for container in reversed(containers):
                metadata = run(['docker', 'inspect', container], required=False)
                if metadata.returncode:
                    continue
                labels = json.loads(metadata.stdout)[0]['Config'].get('Labels') or {}
                if labels.get('synkro.security-chain') != identity:
                    raise RuntimeError('Refusing unowned container cleanup')
                run(['docker', 'rm', '-fv', container])
            if network_created:
                metadata = json.loads(run(['docker', 'network', 'inspect', network]).stdout)[0]
                if metadata.get('Labels', {}).get('synkro.security-chain') != identity:
                    raise RuntimeError('Refusing unowned network cleanup')
                run(['docker', 'network', 'rm', network])
            for image in reversed(images):
                run(['docker', 'image', 'rm', image], required=False)
            print('CLEANED owned disposable security resources', flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--sales', type=Path, required=True)
    parser.add_argument('--workflow', type=Path, required=True)
    parser.add_argument('--sales-db', type=Path, required=True)
    parser.add_argument('--audit', action='store_true')
    parser.add_argument('--idempotency', action='store_true')
    args = parser.parse_args()
    try:
        execute(args.sales.resolve(), args.workflow.resolve(), args.sales_db.resolve(), args.audit, args.idempotency)
    except Exception as error:
        print('FAIL ' + str(error), flush=True)
        raise SystemExit(1)
