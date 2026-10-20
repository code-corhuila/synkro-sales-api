"""Execute the launcher preflight against an owner with cwd-relative helpers."""
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MARKER = '# Verify tmpfs, the safety label and exact target before migration or probes.'


class OwnerLauncherTest(unittest.TestCase):
    def run_phase(self, guard, invoke_from='api'):
        source = (ROOT / 'deploy/ci/mongo-integration.sh').read_text()
        # Execute the actual launcher section; fixture owns only its DB helper.
        phase = source.split(MARKER)[1].split('image_attempted=1')[0]
        with tempfile.TemporaryDirectory(prefix='sales-owner-contract-') as folder:
            base = Path(folder)
            database = base / 'sales-db'
            helper = database / 'deploy/ci'
            helper.mkdir(parents=True)
            (helper / 'connection.js').write_text('owner connection helper')
            (helper / 'ci-env.sh').write_text(guard)
            api = base / 'sales-api'
            api.mkdir()
            elsewhere = base / 'elsewhere'
            elsewhere.mkdir()
            environment = dict(os.environ, db_dir=database.as_posix(), api_dir=api.as_posix())
            shell = 'C:/Program Files/Git/bin/bash.exe' if os.name == 'nt' else 'bash'
            script = 'db_dir=$(cd "$db_dir" && pwd); api_dir=$(cd "$api_dir" && pwd)\n' + phase + '\nprintf "after:%s\n" "$PWD"; printf "api:%s\n" "$api_dir"'
            start = api if invoke_from == 'api' else elsewhere
            return subprocess.run([shell, '-euo', 'pipefail', '-c', script], cwd=start,
                                  env=environment, capture_output=True, text=True, timeout=30)

    OWNER_GUARD = (
        'require_disposable_database() {\n'
        '  test -f deploy/ci/connection.js || { echo "Owner-relative connection helper unavailable" >&2; return 1; }\n'
        '  test "$PWD" = "$db_dir" || return 1\n'
        '  printf "guard executed inside owner\n"\n'
        '}\n')

    def test_preflight_runs_inside_read_only_database_owner(self):
        result = self.run_phase(self.OWNER_GUARD)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('guard executed inside owner', result.stdout)

    def test_preflight_finds_owner_helper_when_invoked_from_another_directory(self):
        result = self.run_phase(self.OWNER_GUARD, invoke_from='elsewhere')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('guard executed inside owner', result.stdout)

    def test_launcher_continues_from_the_sales_api_directory(self):
        result = self.run_phase(self.OWNER_GUARD, invoke_from='elsewhere')
        lines = dict(line.split(':', 1) for line in result.stdout.splitlines() if line.startswith(('after:', 'api:')))
        self.assertEqual(lines['after'], lines['api'])

    def test_non_disposable_target_stops_the_launcher(self):
        result = self.run_phase('require_disposable_database() { echo "Refusing database checks" >&2; return 1; }\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Refusing database checks', result.stderr)
        self.assertNotIn('after:', result.stdout)


if __name__ == '__main__':
    unittest.main()
