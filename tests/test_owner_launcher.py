"""Execute the launcher preflight against an owner with cwd-relative helpers."""
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


class OwnerLauncherTest(unittest.TestCase):
    def test_preflight_runs_inside_read_only_database_owner(self):
        source = (ROOT / 'deploy/ci/mongo-integration.sh').read_text()
        # Execute the actual launcher section; fixture owns only its DB helper.
        phase = source.split('# Verify tmpfs, the safety label and exact target before migration or probes.')[1].split('image_attempted=1')[0]
        with tempfile.TemporaryDirectory(prefix='sales-owner-contract-') as folder:
            base = Path(folder)
            database = base / 'sales-db'
            helper = database / 'deploy/ci'
            helper.mkdir(parents=True)
            (helper / 'connection.js').write_text('owner connection helper')
            (helper / 'ci-env.sh').write_text(
                'require_disposable_database() {\n'
                '  test -f deploy/ci/connection.js || { echo "Owner-relative connection helper unavailable" >&2; return 1; }\n'
                '  test "$PWD" = "$db_dir" || return 1\n'
                '  printf "guard executed inside owner\\n"\n'
                '}\n')
            api = base / 'sales-api'
            api.mkdir()
            environment = dict(os.environ, db_dir=database.as_posix(), api_dir=api.as_posix())
            shell = 'C:/Program Files/Git/bin/bash.exe' if os.name == 'nt' else 'bash'
            result = subprocess.run([shell, '-euo', 'pipefail', '-c', 'db_dir=$(cd "$db_dir" && pwd)' + '\n' + phase], cwd=api,
                                    env=environment, capture_output=True, text=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('guard executed inside owner', result.stdout)


if __name__ == '__main__':
    unittest.main()
