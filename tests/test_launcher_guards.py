"""Run the real MongoDB integration launcher against fixture Sales DB checkouts.

The launcher is executed unmodified except for the pinned revision, which is
replaced by the fixture commit so the exact-pin and clean-checkout guards can be
exercised. A fake `docker` stops the run at the first Docker call.
"""
import os
import re
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
LAUNCHER = ROOT / 'deploy/ci/mongo-integration.sh'
WORKFLOW = ROOT / '.github/workflows/ci.yml'
# synkro-sales-db tag v2.0.0 (annotated tag object 5bbad89) resolves to this commit.
SALES_DB_V2_COMMIT = '344d917264bbb5ad17c40d2e3a33f217a7a94866'
SHELL = 'C:/Program Files/Git/bin/bash.exe' if os.name == 'nt' else 'bash'


def git(folder, *args):
    return subprocess.run(['git', '-C', str(folder), *args], check=True, capture_output=True, text=True).stdout.strip()


class LauncherGuards(unittest.TestCase):
    def test_launcher_and_ci_pin_the_same_sales_db_v2_commit(self):
        launcher = re.search(r'^DB_REF=([0-9a-f]{40})$', LAUNCHER.read_text(), re.M).group(1)
        workflow = re.findall(r'repository: code-corhuila/synkro-sales-db\s+ref: ([0-9a-f]{40})', WORKFLOW.read_text())
        self.assertEqual(launcher, SALES_DB_V2_COMMIT)
        self.assertEqual(workflow, [SALES_DB_V2_COMMIT])

    def run_launcher(self, prepare):
        with tempfile.TemporaryDirectory(prefix='sales-launcher-guard-') as folder:
            base = Path(folder)
            api = base / 'sales-api'
            (api / 'deploy/ci').mkdir(parents=True)
            database = base / 'sales-db'
            database.mkdir()
            git(database, 'init', '-q')
            git(database, '-c', 'user.name=t', '-c', 'user.email=t@example.test', 'commit', '-q', '--allow-empty', '-m', 'fixture')
            fixture_commit = git(database, 'rev-parse', 'HEAD')
            script = re.sub(r'^DB_REF=.*$', 'DB_REF=' + fixture_commit, LAUNCHER.read_text(), flags=re.M)
            (api / 'deploy/ci/mongo-integration.sh').write_text(script, newline='\n')
            bin_dir = base / 'bin'
            bin_dir.mkdir()
            fake = bin_dir / 'docker'
            fake.write_text('#!/usr/bin/env bash\necho "DOCKER-REACHED $*" >&2\nexit 42\n', newline='\n')
            fake.chmod(fake.stat().st_mode | stat.S_IEXEC)
            prepare(database, fixture_commit)
            env = dict(os.environ, PATH=bin_dir.as_posix() + os.pathsep + os.environ['PATH'])
            return subprocess.run([SHELL, str(api / 'deploy/ci/mongo-integration.sh'), str(database)],
                                  env=env, capture_output=True, text=True, timeout=60)

    def test_exact_clean_pin_proceeds_to_disposable_resources(self):
        result = self.run_launcher(lambda database, commit: None)
        self.assertEqual(result.returncode, 42, result.stderr)
        self.assertIn('DOCKER-REACHED info', result.stderr)

    def test_different_sales_db_revision_is_rejected(self):
        def move(database, commit):
            git(database, '-c', 'user.name=t', '-c', 'user.email=t@example.test', 'commit', '-q', '--allow-empty', '-m', 'other')
        result = self.run_launcher(move)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn('does not match the approved pinned revision', result.stderr)
        self.assertNotIn('DOCKER-REACHED', result.stderr)

    def test_dirty_sales_db_checkout_is_rejected(self):
        result = self.run_launcher(lambda database, commit: (database / 'untracked.txt').write_text('x'))
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn('must be clean', result.stderr)
        self.assertNotIn('DOCKER-REACHED', result.stderr)

    def test_every_disposable_resource_guard_remains_in_the_launcher(self):
        source = LAUNCHER.read_text()
        for guard in ('Refusing existing container', 'Refusing existing network', 'Refusing existing runner image tag',
                      'require_disposable_database', 'com.synkro.sales-db.disposable'):
            self.assertIn(guard, source)


if __name__ == '__main__':
    unittest.main()
