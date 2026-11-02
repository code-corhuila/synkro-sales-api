"""Run the real integration launcher end to end against a fake Docker.

Fixture tests only: no Docker, MongoDB or Go is started. A fake `docker` records
every call and keeps the labelled resources the launcher creates, so the order of
the phases, the container arguments and the owner-only cleanup are observed from
the launcher's real behavior. The real integration run remains a separate,
mandatory step. Test IDs are described in docs/design/offline-module-cache.md.
"""
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
LAUNCHER = ROOT / 'deploy/ci/mongo-integration.sh'
SHELL = 'C:/Program Files/Git/bin/bash.exe' if os.name == 'nt' else 'bash'
SEP = '\x1f'
TOKEN = 'a' * 24
MODCACHE = f'synkro-sales-api-it-modcache-{TOKEN}'
FETCHER = f'synkro-sales-api-it-prefetch-{TOKEN}'
TESTER = f'synkro-sales-api-it-go-{TOKEN}'
NETWORK = f'synkro-sales-api-it-net-{TOKEN}'
LABEL = 'com.synkro.sales-api.integration'

FAKE_DOCKER = r'''#!/usr/bin/env bash
state=$FAKE_STATE
{ printf '%s\037' "$@"; printf '\036'; } >> "$state/calls.log"
key() { printf '%s' "${1//:/_}"; }
label='' name='' tag='' prev=''
for a in "$@"; do
  [[ $prev == --label ]] && label=${a#*=}
  [[ $prev == --name ]] && name=$a
  [[ $prev == -t ]] && tag=$a
  prev=$a
done
last=${!#}
case "$1" in
  info) exit 0 ;;
  build) : > "$state/image.$(key "$tag")"; exit 0 ;;
  rm) rm -f "$state/container.$last"; exit 0 ;;
  run)
    if [[ $name == *-prefetch-* ]]; then
      [[ -n ${FAKE_LINGER:-} ]] && printf '%s' "$label" > "$state/container.$name"
      if [[ -n ${FAKE_PREFETCH_FAIL:-} ]]; then
        echo 'dial tcp: lookup proxy.golang.org: no such host' >&2
        exit 1
      fi
    fi
    if [[ -n ${FAKE_TESTS_FAIL:-} && " $* " == *' TEST_MONGODB_URI '* ]]; then exit 1; fi
    exit 0 ;;
  container|network|volume|image)
    file="$state/$1.$(key "$last")"
    case "$2" in
      inspect) [[ -e $file ]] || exit 1; cat "$file"; exit 0 ;;
      create)
        if [[ $1 == volume && -n ${FAKE_VOLUME_CREATE_FAIL:-} ]]; then exit 1; fi
        printf '%s' "$label" > "$file"; exit 0 ;;
      rm) rm -f "$file"; exit 0 ;;
    esac ;;
esac
exit 0
'''
# A function wins over any PATH lookup, so the real openssl is never used.
FAKE_OPENSSL = r'''openssl() {
  local n=$(( $3 * 2 )) out=''
  while (( ${#out} < n )); do out+=a; done
  printf '%s' "$out"
}
'''
START_EVENT = r'''printf 'EVENT\037mongo-start\037\036' >> "$FAKE_STATE/calls.log"
'''


def git(folder, *args):
    return subprocess.run(['git', '-C', str(folder), '-c', 'user.name=t', '-c', 'user.email=t@example.test', *args],
                          check=True, capture_output=True, text=True).stdout.strip()


def write_lf(path, text, executable=False):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, newline='\n')
    if executable:
        path.chmod(path.stat().st_mode | 0o111)


def option_values(call, flag):
    return [call[i + 1] for i, value in enumerate(call[:-1]) if value == flag]


class Run:
    def __init__(self, result, state):
        self.result = result
        text = (state / 'calls.log').read_text(encoding='utf-8') if (state / 'calls.log').exists() else ''
        # Arguments may span lines (the Go script), so records end with a record separator.
        self.calls = [record.split(SEP)[:-1] for record in text.split('\x1e')[:-1]]
        self.output = result.stdout + result.stderr

    def index(self, *prefix):
        for position, call in enumerate(self.calls):
            if call[:len(prefix)] == list(prefix):
                return position
        return None

    def runs(self, name):
        return [c for c in self.calls if c[:1] == ['run'] and name in option_values(c, '--name')]

    def removed(self, kind):
        return [c[-1] for c in self.calls if c[:2] == [kind, 'rm']]


class ModuleCacheLauncher(unittest.TestCase):
    def execute(self, environment=None, prepare=None):
        with tempfile.TemporaryDirectory(prefix='sales-module-cache-') as folder:
            base = Path(folder)
            api, database, bin_dir, state = base / 'sales-api', base / 'sales-db', base / 'bin', base / 'state'
            state.mkdir()
            git_init = ['git', '-C', str(database)]
            database.mkdir()
            subprocess.run([*git_init, 'init', '-q'], check=True)
            write_lf(database / 'deploy/ci/start-replica-set.sh', START_EVENT)
            write_lf(database / 'deploy/ci/ci-env.sh', 'require_disposable_database() { :; }\n')
            write_lf(database / 'deploy/liquibase.Dockerfile', 'FROM scratch\n')
            git(database, 'add', '-A')
            git(database, 'commit', '-q', '-m', 'fixture')
            commit = git(database, 'rev-parse', 'HEAD')
            script = LAUNCHER.read_text().replace('DB_REF=344d917264bbb5ad17c40d2e3a33f217a7a94866', 'DB_REF=' + commit)
            write_lf(api / 'deploy/ci/mongo-integration.sh', script)
            write_lf(bin_dir / 'docker', FAKE_DOCKER, executable=True)
            write_lf(base / 'fake-env.sh', FAKE_OPENSSL)
            # Hide the Windows shim without removing Linux directories that also supply bash/git.
            kept = [p for p in os.environ['PATH'].split(os.pathsep)
                    if os.name != 'nt' or not any((Path(p) / n).exists() for n in ('docker', 'docker.exe'))]
            path = os.pathsep.join([bin_dir.as_posix(), *kept])
            self.assertIsNone(shutil.which('docker.exe', path=path))
            env = dict(os.environ, PATH=path, FAKE_STATE=state.as_posix(), BASH_ENV=(base / 'fake-env.sh').as_posix(),
                       **(environment or {}))
            if prepare:
                prepare(state)
            result = subprocess.run([SHELL, str(api / 'deploy/ci/mongo-integration.sh'), str(database)],
                                    env=env, capture_output=True, text=True, timeout=60)
            return Run(result, state)

    def test_sales_mc_01_prefetch_happens_before_any_database_resource(self):
        run = self.execute()
        self.assertEqual(run.result.returncode, 0, run.output)
        prefetch = run.index('run', '--rm', '--name', FETCHER)
        self.assertIsNotNone(prefetch, run.output)
        self.assertLess(run.index('volume', 'create'), prefetch)
        for later in (run.index('network', 'create'), run.index('EVENT', 'mongo-start'), run.index('build')):
            self.assertIsNotNone(later)
            self.assertLess(prefetch, later)

    def test_sales_mc_02_prefetch_container_is_isolated_and_checksum_verified(self):
        call = self.execute().runs(FETCHER)[0]
        self.assertIn('golang:1.26.9', call)
        self.assertNotIn('--network', call)
        self.assertIn(f'{MODCACHE}:/gomodcache', option_values(call, '-v'))
        sources = [m for m in option_values(call, '-v') if m.endswith(':/workspace:ro')]
        self.assertEqual(len(sources), 1)
        allowed = {'GOMODCACHE=/gomodcache', 'GOTOOLCHAIN=local', 'GOFLAGS=-mod=readonly'}
        self.assertEqual(set(option_values(call, '-e')), allowed)
        self.assertIn(f'{LABEL}={TOKEN}', option_values(call, '--label'))
        command = call[-1]
        self.assertIn('go mod download', command)
        self.assertIn('go mod verify', command)
        for forbidden in ('tidy', 'go get', 'GONOSUMDB', 'GOSUMDB', 'GOFLAGS=-mod=mod'):
            self.assertNotIn(forbidden, ' '.join(call))

    def test_sales_mc_03_go_containers_share_the_cache_and_resolve_offline(self):
        tests, verifier = self.execute().runs(TESTER)
        offline = {'GOMODCACHE=/gomodcache', 'GOPROXY=off', 'GOTOOLCHAIN=local', 'GOFLAGS=-mod=readonly'}
        for call in (tests, verifier):
            self.assertIn('golang:1.26.9', call)
            self.assertIn(f'{MODCACHE}:/gomodcache', option_values(call, '-v'))
            self.assertTrue(offline <= set(option_values(call, '-e')), call)

    def test_sales_mc_04_integration_tests_stay_connected_to_mongo(self):
        tests, verifier = self.execute().runs(TESTER)
        self.assertEqual(option_values(tests, '--network'), [NETWORK])
        self.assertIn('TEST_MONGODB_URI', option_values(tests, '-e'))
        self.assertIn('TEST_MONGO_HOST', option_values(tests, '-e'))
        self.assertIn('-race', tests[-1])
        self.assertNotIn('none', option_values(tests, '--network') + option_values(verifier, '--network'))
        self.assertFalse([e for e in option_values(verifier, '-e') if e.startswith('TEST_')])

    def test_sales_mc_05_prefetch_failure_stops_before_the_database(self):
        run = self.execute({'FAKE_PREFETCH_FAIL': '1', 'FAKE_LINGER': '1'})
        self.assertNotEqual(run.result.returncode, 0)
        self.assertIn('Module cache preparation failed', run.result.stderr)
        self.assertNotIn('proxy.golang.org', run.output)
        for absent in (('network', 'create'), ('EVENT', 'mongo-start'), ('build',)):
            self.assertIsNone(run.index(*absent), run.output)
        self.assertEqual([c for c in run.calls if c[:1] == ['run']], [run.runs(FETCHER)[0]])
        self.assertIn(['rm', '-f', FETCHER], run.calls)
        self.assertEqual(run.removed('volume'), [MODCACHE])

    def test_sales_mc_06_success_cleanup_removes_only_the_owned_cache(self):
        run = self.execute()
        self.assertEqual(run.result.returncode, 0, run.output)
        self.assertEqual(run.removed('volume'), [MODCACHE])
        self.assertEqual(run.removed('network'), [NETWORK])

    def test_sales_mc_07_failure_cleanup_removes_only_the_owned_cache(self):
        run = self.execute({'FAKE_TESTS_FAIL': '1'})
        self.assertNotEqual(run.result.returncode, 0)
        self.assertEqual(run.removed('volume'), [MODCACHE])
        self.assertEqual(run.removed('network'), [NETWORK])

    def test_sales_mc_08_existing_volume_is_refused_and_never_removed(self):
        for existing_label in ('', 'another-run'):
            with self.subTest(label=existing_label):
                run = self.execute(prepare=lambda state: (state / f'volume.{MODCACHE}').write_text(existing_label))
                self.assertEqual(run.result.returncode, 1, run.output)
                self.assertIn('Refusing existing module cache volume', run.result.stderr)
                self.assertEqual(run.removed('volume'), [])
                for absent in (('volume', 'create'), ('network', 'create'), ('EVENT', 'mongo-start')):
                    self.assertIsNone(run.index(*absent))
                self.assertEqual([c for c in run.calls if c[:1] == ['run']], [])

    def test_sales_mc_09_failed_volume_creation_removes_nothing(self):
        run = self.execute({'FAKE_VOLUME_CREATE_FAIL': '1'})
        self.assertNotEqual(run.result.returncode, 0)
        self.assertEqual(run.removed('volume'), [])
        self.assertEqual([c for c in run.calls if c[:1] == ['run']], [])


if __name__ == '__main__':
    unittest.main()
