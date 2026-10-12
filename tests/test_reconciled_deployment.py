"""Render the delivered Compose contract with distinct disposable inputs."""
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


class ReconciledDeployment(unittest.TestCase):
    def test_application_configuration_and_public_key_mount(self):
        with tempfile.TemporaryDirectory(prefix='synkro-sales-config-') as folder:
            public = Path(folder) / 'public.pem'
            public.write_text('disposable-public-material')
            app_uri = 'mongodb://sales_app:application-sentinel@synkro-mongo:27017/sales?authSource=admin&replicaSet=rs0&connectTimeoutMS=2000&serverSelectionTimeoutMS=5000'
            values = dict(os.environ, SALES_APP_MONGODB_URI=app_uri,
                          MONGODB_URI='mongodb://migration_admin:distinct-sentinel@synkro-mongo:27017',
                          JWT_PUBLIC_KEY='public-inline-input-must-not-be-selected-with-file',
                          JWT_PUBLIC_KEY_PATH=public.as_posix(), MONGODB_TIMEOUT='3s', MONGODB_MAX_POOL_SIZE='7')
            result = subprocess.run(['docker', 'compose', '-f', str(ROOT / 'deploy/compose.yml'),
                                     'config', '--format', 'json'], check=True, capture_output=True, text=True, env=values)
            model = json.loads(result.stdout)
            service = model['services']['synkro-sales-api']
            settings = service['environment']
            self.assertEqual(settings['MONGODB_URI'], app_uri)
            self.assertEqual(settings['MONGODB_TIMEOUT'], '3s')
            self.assertEqual(settings['MONGODB_MAX_POOL_SIZE'], '7')
            self.assertEqual(settings.get('JWT_PUBLIC_KEY', ''), '')
            self.assertEqual(settings['JWT_PUBLIC_KEY_FILE'], '/run/secrets/sales_jwt_public_key')
            self.assertNotIn('ports', service)
            self.assertTrue(model['networks']['platform']['external'])
            secret = next(value for value in service['secrets'] if Path(value['target']).name == 'sales_jwt_public_key')
            self.assertEqual(secret['target'] if secret['target'].startswith('/') else '/run/secrets/' + secret['target'], settings['JWT_PUBLIC_KEY_FILE'])
            self.assertEqual(Path(model['secrets'][secret['source']]['file']).resolve(), public.resolve())
            self.assertNotIn('migration_admin', json.dumps(service))


if __name__ == '__main__':
    unittest.main()
