"""Lightweight S04 worker-isolation contracts; never starts Docker or main."""
import copy
import importlib.util
import inspect
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SOURCE = Path(os.environ.get('S04_FLOW_SOURCE', str(Path(__file__).with_name('s04-main-flow.py'))))
spec = importlib.util.spec_from_file_location('s04_worker_contract_flow', SOURCE)
flow = importlib.util.module_from_spec(spec)
spec.loader.exec_module(flow)
SAFE_WORKERS = {'EXTENSION_UPDATE_ENABLED': 'false', 'EXTENSION_EDGE_ID': '', 'EXTENSION_CHROME_ID': ''}


class BeforeDockerCreate(Exception):
    pass


class WorkerIsolationContracts(unittest.TestCase):
    def capture_create(self, root, mode=None, proxies=None, secret='SYNTHETIC_FIXTURE_ONLY'):
        captured = []

        class Capture(flow.MainFlow):
            def __init__(self):
                self.output = root
                self.run_id = 'a' * 32
                self.host_owner = (1000, 1000)
                self.image = 'tikuhelper-s04-synthetic-contract-only'

            def own_name(self, name):
                return name

            def call(self, label, args, **kwargs):
                captured.append((label, args))
                raise BeforeDockerCreate('sentinel before any Docker command')

        with self.assertRaises(BeforeDockerCreate):
            Capture().branch('synthetic_contract', mode, proxies, secret, 1)
        self.assertEqual(len(captured), 1)
        label, args = captured[0]
        self.assertTrue(label.endswith('-create'))
        self.assertEqual(args[0], 'create')
        env = dict(args[i+1].split('=', 1) for i, value in enumerate(args) if value == '--env')
        self.assertEqual(args[args.index('--network')+1], 'none')
        self.assertIn('--read-only', args)
        self.assertNotIn('--publish', args)
        return env

    def required(self, name):
        value = getattr(flow, name, None)
        self.assertTrue(callable(value), 'missing worker isolation contract: ' + name)
        return value

    def fixture(self, directory):
        root = Path(directory) / 'fixture'
        (root / 'data').mkdir(parents=True)
        (root / '.s04-fixture').write_text(flow.MARKER)
        return root / 'data/fixture.db'

    def seed(self, db, rows=None, table=True):
        with sqlite3.connect(db) as connection:
            if table:
                connection.execute('CREATE TABLE system_settings (key TEXT, value TEXT)')
                connection.executemany('INSERT INTO system_settings (key,value) VALUES (?,?)', rows or [])

    def test_actual_branch_create_has_exact_worker_overrides_and_network_none(self):
        for mode, proxies, secret in [(None, None, 'SYNTHETIC'), ('production', 'none', None),
                                     (None, 'none', 'SYNTHETIC'), ('development', None, 'SYNTHETIC')]:
            with self.subTest(mode=mode, proxies=proxies), tempfile.TemporaryDirectory(prefix='tikuhelper-s04-worker-') as directory:
                env = self.capture_create(Path(directory), mode, proxies, secret)
                self.assertEqual({key: env.get(key) for key in SAFE_WORKERS}, SAFE_WORKERS)

    def test_host_unsafe_worker_and_provider_settings_are_not_inherited(self):
        hostile = dict(SAFE_WORKERS, EXTENSION_UPDATE_ENABLED='true', EXTENSION_EDGE_ID='host-edge',
                       EXTENSION_CHROME_ID='host-chrome', SQL_DSN='SYNTHETIC_REMOTE', PROVIDER_KEY='SYNTHETIC_TOKEN')
        before = dict(os.environ)
        with patch.dict(os.environ, hostile), tempfile.TemporaryDirectory(prefix='tikuhelper-s04-worker-') as directory:
            env = self.capture_create(Path(directory), 'production', 'none')
            self.assertEqual({key: env.get(key) for key in SAFE_WORKERS}, SAFE_WORKERS)
            self.assertEqual(env['SQL_DSN'], '')
            self.assertNotIn('PROVIDER_KEY', env)
        self.assertEqual(dict(os.environ), before)

    def test_worker_environment_guard_rejects_omitted_enabled_or_source_ids(self):
        check = self.required('check_worker_environment')
        self.assertTrue(check(dict(SAFE_WORKERS))['extensionAutoUpdateDisabled'])
        for key in SAFE_WORKERS:
            for value in [None, 'true' if key == 'EXTENSION_UPDATE_ENABLED' else 'SYNTHETIC_SOURCE']:
                env = dict(SAFE_WORKERS)
                if value is None:
                    del env[key]
                else:
                    env[key] = value
                with self.subTest(key=key, omitted=value is None), self.assertRaises(AssertionError):
                    check(env)

    def test_absent_new_database_is_not_created_and_absence_must_be_explicit(self):
        check = self.required('check_ticket_fixture_isolation')
        with tempfile.TemporaryDirectory(prefix='tikuhelper-s04-worker-') as directory:
            db = self.fixture(directory)
            value = check(db, allow_absent=True)
            self.assertTrue(value['databaseAbsent'])
            self.assertFalse(db.exists())
            with self.assertRaises(AssertionError):
                check(db)
            self.assertFalse(db.exists())

    def test_readonly_legacy_missing_table_and_migrated_defaults_or_explicit_disable(self):
        check = self.required('check_ticket_fixture_isolation')
        scenarios = [(False, []), (True, []), (True, [('wecom_ticket_notifications_enabled', 'false'), ('wecom_ticket_webhook_url', '')])]
        for table, rows in scenarios:
            with self.subTest(table=table, explicit=bool(rows)), tempfile.TemporaryDirectory(prefix='tikuhelper-s04-worker-') as directory:
                db = self.fixture(directory)
                self.seed(db, rows, table)
                before = db.read_bytes()
                value = check(db)
                self.assertTrue(value['effectiveNotificationsDisabled'])
                self.assertTrue(value['effectiveWebhookEmpty'])
                self.assertTrue(value['readonlyInspection'])
                self.assertEqual(value['settingsTablePresent'], table)
                self.assertEqual(db.read_bytes(), before)

    def test_unsafe_notification_rows_fail_closed_without_echoing_or_rewriting(self):
        check = self.required('check_ticket_fixture_isolation')
        secret = 'SYNTHETIC_WEBHOOK_TOKEN_NOT_A_CREDENTIAL'
        scenarios = [[('wecom_ticket_notifications_enabled', 'true')], [('wecom_ticket_notifications_enabled', '1')],
                     [('wecom_ticket_notifications_enabled', 'unknown')], [('wecom_ticket_webhook_url', 'https://fixture.invalid/?key='+secret)],
                     [('wecom_ticket_notifications_enabled', 'false'), ('wecom_ticket_webhook_url', 'https://fixture.invalid/?key='+secret)],
                     [('wecom_ticket_notifications_enabled', 'false'), ('wecom_ticket_notifications_enabled', 'true')]]
        for rows in scenarios:
            with self.subTest(rows=len(rows)), tempfile.TemporaryDirectory(prefix='tikuhelper-s04-worker-') as directory:
                db = self.fixture(directory)
                self.seed(db, rows)
                before = db.read_bytes()
                with self.assertRaises(AssertionError) as error:
                    check(db)
                self.assertNotIn(secret, str(error.exception))
                self.assertEqual(db.read_bytes(), before)

    def test_foreign_unmarked_symlink_and_hardlink_database_are_rejected(self):
        check = self.required('check_ticket_fixture_isolation')
        with tempfile.TemporaryDirectory(prefix='tikuhelper-s04-worker-') as directory:
            db = self.fixture(directory)
            self.seed(db)
            marker = db.parent.parent / '.s04-fixture'
            marker.write_text('wrong marker\n')
            with self.assertRaises(AssertionError):
                check(db)
            marker.write_text(flow.MARKER)
            other = db.parent / 'foreign.db'
            os.link(db, other)
            with self.assertRaises(AssertionError):
                check(db)
            other.unlink()
            data = db.read_bytes()
            db.unlink()
            other.write_bytes(data)
            db.symlink_to(other)
            with self.assertRaises(AssertionError):
                check(db)

    def test_guards_are_wired_before_create_and_after_actual_main_shutdown(self):
        source = inspect.getsource(flow.MainFlow.branch)
        first = source.find('check_ticket_fixture_isolation')
        self.assertGreaterEqual(first, 0, 'actual branch must check new-worker DB policy before create')
        self.assertLess(first, source.index("created = self.call"))
        last = source.rfind('check_ticket_fixture_isolation')
        self.assertGreater(last, source.index("result['actualExit']"))
        self.assertIn('check_worker_environment', source)

    def cleanup_subject(self, stderr):
        subject = flow.MainFlow.__new__(flow.MainFlow)
        subject.run_id = 'c' * 32
        name = 'tikuhelper-s04-owner-2-' + subject.run_id[:10]
        subject.container_names = [name]
        subject.container_ids = []
        subject.cleanup = []
        commands = []

        def call(label, args, **kwargs):
            commands.append(args)
            return subprocess.CompletedProcess(args, 1, b'[]\n', stderr.replace(b'OWNED_NAME', name.encode()))

        subject.call = call
        return subject, name, commands

    def test_cleanup_exact_owned_absence_accepts_real_docker_case_variants(self):
        for raw in [b'error: no such object: OWNED_NAME\n', b'Error: No such object: OWNED_NAME\n',
                    b'Error response from daemon: No such container: OWNED_NAME\n']:
            with self.subTest(shape=raw.split(b':', 1)[0]):
                subject, name, commands = self.cleanup_subject(raw)
                self.assertIsNone(subject.remove_owned_fallback(name))
                self.assertEqual(commands, [['inspect', name]], 'absent means no signal or removal command')

    def test_cleanup_unknown_daemon_errors_wrong_target_and_unrecorded_names_still_refuse(self):
        for raw in [b'permission denied; No such object: OWNED_NAME\n', b'Cannot connect to Docker daemon\n',
                    b'Error: No such object: different-target\n']:
            with self.subTest(error=raw.split(b':', 1)[0]):
                subject, name, commands = self.cleanup_subject(raw)
                with self.assertRaises(AssertionError):
                    subject.remove_owned_fallback(name)
                self.assertEqual(commands, [['inspect', name]])
        subject, name, commands = self.cleanup_subject(b'Error: No such object: OWNED_NAME\n')
        subject.container_names = []
        with self.assertRaises(AssertionError):
            subject.remove_owned_fallback(name)
        self.assertEqual(commands, [], 'unrecorded target is refused before Docker')
        check = self.required('known_owned_resource_absence')
        valid = subprocess.CompletedProcess([], 1, b'[]\n', ('error: no such object: '+name+'\n').encode())
        self.assertTrue(check(valid, name, ('object', 'container')))
        for code, stdout in [(0, b'[]\n'), (1, b'unknown output\n')]:
            self.assertFalse(check(subprocess.CompletedProcess([], code, stdout, valid.stderr), name, ('object', 'container')))

    def test_cleanup_named_but_never_created_container_and_absent_image_finish_without_actions(self):
        with tempfile.TemporaryDirectory(prefix='tikuhelper-s04-cleanup-') as directory:
            subject, name, commands = self.cleanup_subject(b'error: no such object: OWNED_NAME\n')
            subject.image = 'tikuhelper-s04-fixture:' + subject.run_id
            subject.image_built = False
            subject.ownership_pending = {}
            subject.record_handles = lambda: None
            subject.before_env = {key: os.environ.get(key) for key in flow.SELECTED_ENV}
            subject.binary = Path(directory) / 'synthetic-contract-not-executed'
            subject.probe = Path(directory) / 'synthetic-probe-not-executed'
            subject.binary.write_bytes(b'contract fixture only')
            subject.probe.write_bytes(b'contract probe only')
            subject.input_hashes = {'actualServiceELFSHA256': flow.sha(subject.binary), 'probeELFSHA256': flow.sha(subject.probe)}
            original = subject.call

            def call(label, args, **kwargs):
                if args[:2] == ['image', 'inspect']:
                    commands.append(args)
                    return subprocess.CompletedProcess(args, 1, b'[]\n', ('error: no such image: '+subject.image+'\n').encode())
                return original(label, args, **kwargs)

            subject.call = call
            subject.finish()
            self.assertEqual(subject.container_names, [])
            self.assertEqual(subject.container_ids, [])
            self.assertEqual(commands, [['inspect', name], ['image', 'inspect', subject.image]])


if __name__ == '__main__':
    unittest.main()
