"""Fail-closed test-harness contracts only; not actual-main qualification."""
import copy
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('s04_main_flow', Path(__file__).with_name('s04-main-flow.py'))
flow = importlib.util.module_from_spec(spec)
spec.loader.exec_module(flow)


class S04MainFlowContracts(unittest.TestCase):
    def health(self):
        return {'ok': True, 'operation': 'health', 'endpoints': [
            {'path': path, 'status': 200, 'responseStatus': status,
             'cacheControl': 'no-store', 'requestID': 'a' * 32, 'body': json.dumps({'status': status})}
            for path, status in [('/healthz', 'ok'), ('/readyz', 'ready')]]}

    def test_health_requires_real_body_status_metadata_not_only_exit_zero(self):
        good = self.health()
        self.assertEqual(flow.check_probe(json.dumps(good), 'health'), good)
        for mutation in ['status', 'body', 'cacheControl', 'requestID']:
            bad = copy.deepcopy(good)
            bad['endpoints'][1][mutation] = {'status': 503, 'body': '{}', 'cacheControl': '', 'requestID': ''}[mutation]
            with self.subTest(mutation=mutation), self.assertRaises(AssertionError):
                flow.check_probe(json.dumps(bad), 'health')
        for value in [{}, {'ok': False, 'operation': 'health'}, {'ok': True, 'operation': 'health', 'endpoints': []}]:
            with self.assertRaises(AssertionError):
                flow.check_probe(json.dumps(value), 'health')

    def test_json_failures_do_not_become_empty_success(self):
        for raw in ['{', 'null', '[]', '{"value":NaN}']:
            with self.subTest(raw=raw), self.assertRaises((ValueError, AssertionError)):
                flow.strict_json(raw, 'fixture')

    def test_database_probe_requires_password_preservation_and_real_rollback(self):
        good = {'ok': True, 'operation': 'verify-legacy', 'quickCheck': 'ok', 'bcrypt': True,
                'expectedPasswordMatches': True, 'environmentPasswordMatches': False,
                'unrelatedFieldsPreserved': True, 'rwProbe': [0, 1, 0], 'userCount': 1, 'readonlyInspection': True}
        self.assertEqual(flow.check_probe(json.dumps(good), 'verify-legacy'), good)
        for key, value in [('environmentPasswordMatches', True), ('bcrypt', False), ('rwProbe', [0, 1, 1]),
                           ('unrelatedFieldsPreserved', False), ('userCount', 0), ('readonlyInspection', False)]:
            bad = dict(good, **{key: value})
            with self.subTest(key=key), self.assertRaises(AssertionError):
                flow.check_probe(json.dumps(bad), 'verify-legacy')

    def test_owner_probe_requires_real_preflight_and_post_chown_verification(self):
        good = {'ok': True, 'operation': 'fixture-owner', 'uid': 1000, 'gid': 1000,
                'allOwned': True, 'symlinksRejected': True, 'hardlinksRejected': True,
                'permissionsPreserved': True, 'entries': 5}
        self.assertEqual(flow.check_probe(json.dumps(good), 'fixture-owner'), good)
        for key, value in [('allOwned', False), ('symlinksRejected', False), ('hardlinksRejected', False),
                           ('permissionsPreserved', False), ('entries', 0)]:
            with self.subTest(key=key), self.assertRaises(AssertionError):
                flow.check_probe(json.dumps(dict(good, **{key: value})), 'fixture-owner')

    def test_container_contract_rejects_wrong_owner_network_privilege_or_environment(self):
        with tempfile.TemporaryDirectory(prefix='tikuhelper-s04-unit-') as directory:
            fixture = Path(directory)
            good = {'Config': {'Labels': {flow.LABEL: 'owned'}, 'User': '1000:1000',
                              'WorkingDir': '/fixture', 'Entrypoint': ['/tikuhelper'], 'Tty': False,
                              'Env': ['SQL_DSN=', 'GIN_MODE=release']},
                    'HostConfig': {'NetworkMode': 'none', 'ReadonlyRootfs': True, 'Privileged': False,
                                   'PortBindings': {}, 'CapDrop': ['ALL'], 'SecurityOpt': ['no-new-privileges']},
                    'Mounts': [{'Type': 'bind', 'Source': str(fixture), 'Destination': '/fixture', 'RW': True}],
                    'NetworkSettings': {'Ports': {}}}
            self.assertTrue(flow.check_container(good, 'owned', fixture, {'SQL_DSN': '', 'GIN_MODE': 'release'})['readOnlyRootfs'])
            for key, value in [('NetworkMode', 'bridge'), ('ReadonlyRootfs', False), ('Privileged', True), ('PortBindings', {'8060/tcp': []})]:
                bad = copy.deepcopy(good)
                bad['HostConfig'][key] = value
                with self.subTest(key=key), self.assertRaises(AssertionError):
                    flow.check_container(bad, 'owned', fixture, {'SQL_DSN': '', 'GIN_MODE': 'release'})
            with self.assertRaises(AssertionError):
                flow.check_container(good, 'different', fixture, {'SQL_DSN': '', 'GIN_MODE': 'release'})
            bad = copy.deepcopy(good)
            bad['Config']['Env'].append('TIKU_APP_MODE=production')
            with self.assertRaises(AssertionError):
                flow.check_container(bad, 'owned', fixture, {'SQL_DSN': '', 'GIN_MODE': 'release'})

    def test_client_ip_comes_only_from_actual_gin_log_shape_not_observer_echo(self):
        line = '[GIN] 2026/10/29 - 18:33:00 | 200 | 254.869µs |       {ip} | GET      "{path}"\n'
        for address in ['127.0.0.1', '203.0.113.7']:
            raw = ''.join(line.format(ip=address, path=path) for path in ['/healthz', '/readyz']).encode()
            observed = flow.actual_gin_client_ips(raw, address)
            self.assertTrue(observed['bothEndpointsVerified'])
            self.assertEqual([row['clientIP'] for row in observed['observations']], [address, address])
            with self.assertRaises(AssertionError):
                flow.actual_gin_client_ips(raw, '198.51.100.9')
        for raw in [b'{"clientIP":"203.0.113.7","status":200}\n',
                    b'PROBE healthz200 readyz200 ClientIP203.0.113.7\n',
                    line.format(ip='127.0.0.1', path='/healthz').encode()]:
            with self.assertRaises(AssertionError):
                flow.actual_gin_client_ips(raw, '127.0.0.1')

    def test_wait_requires_one_actual_exit_and_no_oom_or_still_running(self):
        state = {'Running': False, 'ExitCode': 0, 'OOMKilled': False, 'Error': ''}
        self.assertEqual(flow.actual_exit(b'0\n', state), 0)
        for raw in [b'', b'0\n0\n', b'not ready', b'137\n']:
            with self.subTest(raw=raw), self.assertRaises(AssertionError):
                flow.actual_exit(raw, state)
        for key, value in [('Running', True), ('OOMKilled', True), ('Error', 'runtime failure')]:
            with self.subTest(key=key), self.assertRaises(AssertionError):
                flow.actual_exit(b'0\n', dict(state, **{key: value}))

    def test_command_failure_records_literal_streams_exit_and_propagates(self):
        with tempfile.TemporaryDirectory(prefix='tikuhelper-s04-recorder-') as directory:
            recorder = flow.Recorder(Path(directory))
            with self.assertRaises(flow.CommandFailure):
                recorder.run('fixture-failure', [sys.executable, '-c', 'import sys;print("S04_STDOUT");print("S04_STDERR",file=sys.stderr);sys.exit(23)'])
            record = recorder.commands[0]
            self.assertEqual(record['exit'], 23)
            self.assertEqual(Path(record['stdout']).read_text(), 'S04_STDOUT\n')
            self.assertEqual(Path(record['stderr']).read_text(), 'S04_STDERR\n')
            self.assertFalse(record['timedOut'])

    def test_command_timeout_is_failure_not_success_or_retry(self):
        with tempfile.TemporaryDirectory(prefix='tikuhelper-s04-timeout-') as directory:
            recorder = flow.Recorder(Path(directory))
            with self.assertRaises(flow.CommandFailure):
                recorder.run('fixture-timeout', [sys.executable, '-c', 'import time;time.sleep(2)'], timeout=0.02)
            self.assertEqual(len(recorder.commands), 1)
            self.assertEqual(recorder.commands[0]['exit'], 124)
            self.assertTrue(recorder.commands[0]['timedOut'])


if __name__ == '__main__':
    unittest.main()
