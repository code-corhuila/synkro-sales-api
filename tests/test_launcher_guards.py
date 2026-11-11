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
CONTRACT = ROOT / 'deploy/ci/contract-tests.sh'
WORKFLOW = ROOT / '.github/workflows/ci.yml'
# Sales DB develop revision (merge of PR #18, tree identical to e00e0d3) that this develop code is verified against.
SALES_DB_V2_COMMIT = '70c2c5d603e0083566918144f5f0b63ad4108d7a'
SHELL = 'C:/Program Files/Git/bin/bash.exe' if os.name == 'nt' else 'bash'


def git(folder, *args):
    return subprocess.run(['git', '-C', str(folder), *args], check=True, capture_output=True, text=True).stdout.strip()


class LauncherGuards(unittest.TestCase):
    def test_launcher_and_ci_pin_the_same_sales_db_v2_commit(self):
        launcher = re.search(r'^DB_REF=([0-9a-f]{40})$', LAUNCHER.read_text(), re.M).group(1)
        workflow = re.findall(r'repository: code-corhuila/synkro-sales-db\s+ref: ([0-9a-f]{40})', WORKFLOW.read_text())
        self.assertEqual(launcher, SALES_DB_V2_COMMIT)
        # The real-MongoDB job and the contract-test job each check out the same Sales DB revision.
        self.assertEqual(workflow, [SALES_DB_V2_COMMIT] * 2)

    def test_contract_job_pins_the_same_docs_revision_as_its_script(self):
        script = re.search(r'^DOCS_REF=([0-9a-f]{40})$', CONTRACT.read_text(), re.M).group(1)
        workflow = re.findall(r'repository: code-corhuila/synkro-docs\s+ref: ([0-9a-f]{40})', WORKFLOW.read_text())
        self.assertEqual(workflow, [script])

    def test_contract_script_reads_its_sales_db_pin_from_the_launcher(self):
        source = CONTRACT.read_text()
        self.assertIn("deploy/ci/mongo-integration.sh", source)
        self.assertNotRegex(source, r'(?m)^DB_REF=[0-9a-f]{40}$')

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
            # On Windows hide the native shim; on Linux keep shared tool directories such as /usr/bin.
            kept = [p for p in os.environ['PATH'].split(os.pathsep)
                    if os.name != 'nt' or not any((Path(p) / n).exists() for n in ('docker', 'docker.exe'))]
            env = dict(os.environ, PATH=os.pathsep.join([bin_dir.as_posix(), *kept]))
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

    def run_contract_script(self, prepare):
        """The contract script reads DB_REF from the launcher and must stop on its own guards before reaching Docker."""
        with tempfile.TemporaryDirectory(prefix='sales-contract-guard-') as folder:
            base = Path(folder)
            api = base / 'sales-api'
            (api / 'deploy/ci').mkdir(parents=True)
            database = base / 'sales-db'
            docs = base / 'synkro-docs'
            for repo in (database, docs):
                repo.mkdir()
                git(repo, 'init', '-q')
                git(repo, '-c', 'user.name=t', '-c', 'user.email=t@example.test', 'commit', '-q', '--allow-empty', '-m', 'fixture')
            db_commit = git(database, 'rev-parse', 'HEAD')
            docs_commit = git(docs, 'rev-parse', 'HEAD')
            launcher = re.sub(r'^DB_REF=.*$', 'DB_REF=' + db_commit, LAUNCHER.read_text(), flags=re.M)
            script = re.sub(r'^DOCS_REF=.*$', 'DOCS_REF=' + docs_commit, CONTRACT.read_text(), flags=re.M)
            (api / 'deploy/ci/mongo-integration.sh').write_text(launcher, newline='\n')
            (api / 'deploy/ci/contract-tests.sh').write_text(script, newline='\n')
            prepare(database, docs)
            return subprocess.run([SHELL, str(api / 'deploy/ci/contract-tests.sh'), str(database), str(docs)],
                                  capture_output=True, text=True, timeout=60)

    def test_contract_script_rejects_a_different_sales_db_revision(self):
        def move(database, docs):
            git(database, '-c', 'user.name=t', '-c', 'user.email=t@example.test', 'commit', '-q', '--allow-empty', '-m', 'other')
        result = self.run_contract_script(move)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn('Sales DB checkout does not match the approved pinned revision', result.stderr)

    def test_contract_script_rejects_a_different_contract_revision(self):
        def move(database, docs):
            git(docs, '-c', 'user.name=t', '-c', 'user.email=t@example.test', 'commit', '-q', '--allow-empty', '-m', 'other')
        result = self.run_contract_script(move)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn('synkro-docs checkout does not match the pinned contract revision', result.stderr)

    def test_contract_script_rejects_a_dirty_contract_checkout(self):
        result = self.run_contract_script(lambda database, docs: (docs / 'untracked.txt').write_text('x'))
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn('must be clean', result.stderr)

    def test_location_regression_covers_the_required_values_and_the_real_contract_file(self):
        source = (ROOT / 'deploy/ci/contract/location_regression.py').read_text()
        for value in ("f'/api/v1/sales/{SALE_ID}', True", "'https://synkro.example.test/api/v1/sales/", "('empty value', '', False)",
                      "unencoded space", "line break", "_shared.yaml", "response_headers_conformance"):
            self.assertIn(value, source)
        # the contract run must start with that regression, before anything is built
        script = CONTRACT.read_text()
        self.assertLess(script.index('location_regression.py'), script.index('docker build'))
        self.assertIn('contract_flow.py', script)

    def test_flow_checks_success_paths_with_real_ids_and_replay(self):
        source = (ROOT / 'deploy/ci/contract/contract_flow.py').read_text()
        for step in ("returns 201", "Location is exactly the path of the created sale", "replays with 200",
                     "GET /sales/{id} of the created sale returns 200", "is 404", "is 400", "is 401", "is 403", "is 422"):
            self.assertIn(step, source)

    def test_contract_script_keeps_its_disposable_resource_guards(self):
        source = CONTRACT.read_text()
        for guard in ('Refusing existing container', 'Refusing existing network', 'require_disposable_database',
                      'com.synkro.sales-db.disposable', 'does not match the pinned contract revision'):
            self.assertIn(guard, source)


if __name__ == '__main__':
    unittest.main()
