#!/usr/bin/env python3
"""Actual-main startup qualification in owned network-none synthetic fixtures.

No package installation, production hook, host port, inherited service credential,
real database, or provider request is used. Probe assertions are fixture-only;
main is the supplied actual static service ELF, never a fake server.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import sqlite3
import subprocess
import sys
import tempfile
import time
import traceback
import uuid
import zipfile

LABEL = 'io.tiku.s04.run'
MARKER = 'tikuhelper-s04-synthetic-only\n'
NEW_PASSWORD = 'S04-New-Fixture!2026'
SELECTED_ENV = ['TIKU_APP_MODE', 'TRUSTED_PROXIES', 'TRUSTED_PROXIES_FILE', 'TIKU_ADMIN_PASSWORD', 'SQL_DSN', 'GIN_MODE']
WORKER_ENV = {'EXTENSION_UPDATE_ENABLED': 'false', 'EXTENSION_EDGE_ID': '', 'EXTENSION_CHROME_ID': ''}
SELECTED_ENV += list(WORKER_ENV)
CONFIG = '''database:
  path: /fixture/data/fixture.db
mysql: ""
api: []
elasticsearch:
  addresses: []
oss: {}
sqpProxy:
  enabled: false
limit:
  enable: false
jwtSecret: "S04_SYNTHETIC_JWT_SECRET_ONLY_FIXTURE"
'''


def strict_json(raw, context):
    def invalid(value):
        raise ValueError(context + ': invalid JSON constant ' + value)
    value = json.loads(raw, parse_constant=invalid)
    if not isinstance(value, dict):
        raise AssertionError(context + ': object required')
    return value


def check_probe(raw, operation):
    value = strict_json(raw, 'probe ' + operation)
    assert value.get('ok') is True and value.get('operation') == operation, 'probe did not explicitly succeed'
    if operation == 'health':
        endpoints = value.get('endpoints')
        assert isinstance(endpoints, list) and len(endpoints) == 2
        assert [item.get('path') for item in endpoints] == ['/healthz', '/readyz']
        for item, expected in zip(endpoints, ['ok', 'ready']):
            assert item.get('status') == 200 and item.get('responseStatus') == expected
            assert item.get('cacheControl') == 'no-store'
            assert strict_json(item.get('body', ''), 'health body').get('status') == expected
            assert re.fullmatch(r'[a-f0-9]{32}', item.get('requestID', ''))
    elif operation.startswith('verify-'):
        assert value.get('quickCheck') == 'ok' and value.get('bcrypt') is True
        assert value.get('expectedPasswordMatches') is True
        assert value.get('environmentPasswordMatches') is (operation != 'verify-legacy')
        assert value.get('unrelatedFieldsPreserved') is True and value.get('rwProbe') == [0, 1, 0]
        assert value.get('userCount') == 1 and value.get('readonlyInspection') is True
    elif operation == 'fixture-owner':
        assert isinstance(value.get('uid'), int) and isinstance(value.get('gid'), int)
        assert value.get('allOwned') is True and value.get('symlinksRejected') is True
        assert value.get('permissionsPreserved') is True and value.get('hardlinksRejected') is True and value.get('entries', 0) >= 3
    elif operation == 'seed-legacy':
        assert value.get('legacyPlaintextSeeded') is True and value.get('userCount') == 1
    else:
        raise AssertionError('unknown probe operation')
    return value


def check_container(info, run_id, fixture, expected_env):
    config, host = info['Config'], info['HostConfig']
    assert config.get('Labels', {}).get(LABEL) == run_id
    assert config['User'] == '1000:1000' and config['WorkingDir'] == '/fixture'
    assert config.get('Entrypoint') == ['/tikuhelper'] and config.get('Tty') is False
    assert host['NetworkMode'] == 'none' and host['ReadonlyRootfs'] is True
    assert not host.get('Privileged') and not host.get('PortBindings')
    assert host.get('CapDrop') == ['ALL'] and 'no-new-privileges' in host.get('SecurityOpt', [])
    env = dict(item.split('=', 1) for item in config.get('Env', []))
    assert env == expected_env, 'container environment differs from explicit synthetic allowlist'
    mounts = info.get('Mounts', [])
    assert len(mounts) == 1 and mounts[0]['Type'] == 'bind'
    assert Path(mounts[0]['Source']).resolve() == fixture.resolve()
    assert mounts[0]['Destination'] == '/fixture' and mounts[0]['RW'] is True
    assert not info.get('NetworkSettings', {}).get('Ports')
    return {'network': 'none', 'readOnlyRootfs': True, 'user': '1000:1000', 'ports': [],
            'singleOwnedFixtureBind': True, 'capDropAll': True, 'noNewPrivileges': True,
            'environmentExactlySyntheticAllowlist': True,
            'applicationModeAbsent': 'TIKU_APP_MODE' not in env,
            'trustedProxyAbsent': 'TRUSTED_PROXIES' not in env,
            'trustedProxyFileAbsent': 'TRUSTED_PROXIES_FILE' not in env}


def check_worker_environment(env):
    for key, expected in WORKER_ENV.items():
        assert key in env and env[key] == expected, 'new-worker environment isolation mismatch: ' + key
    return {'extensionAutoUpdateDisabled': True, 'edgeSourceEmpty': True, 'chromeSourceEmpty': True}


def known_owned_resource_absence(result, name, kinds):
    if result.returncode == 0 or result.stdout.strip() not in (b'', b'[]'):
        return False
    assert kinds and all(kind in ('object', 'container', 'image') for kind in kinds)
    pattern = (rb'(?:error(?::| response from daemon:)\s*)?no such (?:'
               + b'|'.join(kind.encode() for kind in kinds) + rb'):\s*'
               + re.escape(name.encode()) + rb'\s*')
    return re.fullmatch(pattern, result.stderr.strip(), flags=re.IGNORECASE) is not None


def check_ticket_fixture_isolation(db, allow_absent=False):
    db = Path(db)
    assert db.is_absolute() and db.name == 'fixture.db' and db.parent.name == 'data', 'owned synthetic database required'
    root = db.parent.parent
    assert root.name == 'fixture' and root.resolve() == root and db.parent.resolve() == db.parent
    assert root.is_dir() and db.parent.is_dir() and not db.is_symlink()
    marker = root / '.s04-fixture'
    assert marker.is_file() and not marker.is_symlink() and marker.stat().st_nlink == 1
    assert marker.read_text() == MARKER, 'owned synthetic marker required'
    if not db.exists():
        assert allow_absent, 'post-start synthetic database must exist'
        # Preserve the original new-database and rejected-bootstrap branches.
        # GetTicketWecomConfigWithDB starts from a zero-value config; absent
        # migrated settings must be verified again after actual main exits.
        return {'databaseAbsent': True, 'defaultPolicyRequiresPostMainVerification': True,
                'effectiveNotificationsDisabled': True, 'effectiveWebhookEmpty': True,
                'readonlyInspection': True}
    assert db.is_file() and db.stat().st_nlink == 1, 'regular unlinked synthetic database required'
    before = sha(db)
    connection = sqlite3.connect(db.as_uri() + '?mode=ro', uri=True)
    try:
        table = connection.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name='system_settings'").fetchone() is not None
        rows = connection.execute('SELECT key, value FROM system_settings WHERE key IN (?, ?)',
                                  ('wecom_ticket_notifications_enabled', 'wecom_ticket_webhook_url')).fetchall() if table else []
        values = {}
        for key, value in rows:
            assert key not in values, 'duplicate synthetic notification setting'
            values[key] = value
        enabled = values.get('wecom_ticket_notifications_enabled', '')
        webhook = values.get('wecom_ticket_webhook_url', '')
        assert isinstance(enabled, str) and enabled.strip().lower() in ('', 'false', '0'), 'synthetic ticket notifications must be disabled'
        assert isinstance(webhook, str) and not webhook.strip(), 'synthetic ticket webhook must be empty'
    except sqlite3.Error as error:
        raise AssertionError('cannot inspect synthetic notification settings') from error
    finally:
        connection.close()
        assert sha(db) == before, 'readonly notification inspection changed database bytes'
    return {'databaseAbsent': False, 'settingsTablePresent': table,
            'enabledSettingAbsent': 'wecom_ticket_notifications_enabled' not in values,
            'webhookSettingAbsent': 'wecom_ticket_webhook_url' not in values,
            'effectiveNotificationsDisabled': True, 'effectiveWebhookEmpty': True,
            'readonlyInspection': True, 'databaseSHA256': before}


def actual_exit(raw, state):
    assert re.fullmatch(rb'[0-9]+\n?', raw), 'docker wait must return one literal process exit code'
    code = int(raw.strip())
    assert state['Running'] is False and state['ExitCode'] == code
    assert state.get('OOMKilled') is False and not state.get('Error')
    return code



def actual_gin_client_ips(raw, expected):
    # Parse only the service PID1's literal stdout. Probe JSON or an observer's
    # echoed header value is not a Gin access-log observation.
    pattern = re.compile(r'^\[GIN\] [0-9]{4}/[0-9]{2}/[0-9]{2} - [0-9]{2}:[0-9]{2}:[0-9]{2} \|\s*([0-9]{3})\s*\|[^|]+\|\s*([^|\s]+)\s*\|\s*GET\s+"(/healthz|/readyz)"\s*$')
    observed = []
    for line in raw.decode('utf-8').splitlines():
        match = pattern.fullmatch(line)
        if match:
            status, address, endpoint = match.groups()
            assert address == expected, 'actual Gin ClientIP mismatch for ' + endpoint + ': ' + address + ' != ' + expected
            observed.append({'path': endpoint, 'status': int(status), 'clientIP': address,
                             'literalLogLineSHA256': hashlib.sha256(line.encode()).hexdigest()})
    for path in ['/healthz', '/readyz']:
        assert any(row['path'] == path and row['status'] == 200 for row in observed), 'missing actual successful Gin log: ' + path
    return {'source': 'actual service PID1 literal stdout Gin access logs',
            'expectedClientIP': expected, 'observations': observed, 'bothEndpointsVerified': True}


def sha(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for part in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(part)
    return h.hexdigest()


class CommandFailure(RuntimeError):
    pass


class Recorder:
    def __init__(self, directory):
        self.directory = directory
        self.directory.mkdir(parents=True, exist_ok=True)
        self.commands = []

    def run(self, label, args, timeout=30, check=True):
        prefix = self.directory / ('%04d-' % len(self.commands) + label)
        started = time.monotonic()
        try:
            result = subprocess.run(args, capture_output=True, timeout=timeout)
            timed_out = False
        except subprocess.TimeoutExpired as error:
            result = subprocess.CompletedProcess(args, 124, error.stdout or b'', error.stderr or b'')
            timed_out = True
        except OSError as error:
            result = subprocess.CompletedProcess(args, 127, b'', (str(error) + '\n').encode())
            timed_out = False
        prefix.with_suffix('.stdout').write_bytes(result.stdout)
        prefix.with_suffix('.stderr').write_bytes(result.stderr)
        prefix.with_suffix('.exit').write_text(str(result.returncode) + '\n')
        record = {'label': label, 'command': args, 'exit': result.returncode, 'timedOut': timed_out,
                  'elapsedSeconds': round(time.monotonic() - started, 3),
                  'stdout': str(prefix.with_suffix('.stdout')), 'stderr': str(prefix.with_suffix('.stderr'))}
        self.commands.append(record)
        (self.directory / 'COMMANDS.json').write_text(json.dumps(self.commands, indent=2) + '\n')
        if timed_out or (check and result.returncode != 0):
            raise CommandFailure(label + ': command exit ' + str(result.returncode))
        return result


class MainFlow:
    def __init__(self, binary, probe, evidence, docker='docker'):
        self.binary, self.probe = binary, probe
        self.run_id = uuid.uuid4().hex
        evidence.mkdir(parents=True, exist_ok=True)
        self.output = Path(tempfile.mkdtemp(prefix='attempt-', dir=evidence))
        (self.output / '.s04-owned-attempt').write_text(self.run_id + '\n')
        self.recorder = Recorder(self.output / 'commands')
        self.docker = docker
        self.image = 'tikuhelper-s04-fixture:' + self.run_id
        self.image_built = False
        self.container_ids = []
        self.container_names = []
        self.host_owner = (os.getuid(), os.getgid())
        self.helper_number = 0
        self.ownership_pending = {}
        self.branches = []
        self.before_env = {key: os.environ.get(key) for key in SELECTED_ENV}
        self.input_hashes = {'actualServiceELFSHA256': sha(binary), 'probeELFSHA256': sha(probe)}
        self.cleanup = []
        self.record_handles()

    def record_handles(self):
        (self.output / 'RESOURCE_HANDLES.json').write_text(json.dumps({'runID': self.run_id, 'label': LABEL, 'containerIDs': self.container_ids, 'containerNames': self.container_names, 'image': self.image, 'imageBuilt': self.image_built, 'restoreFixtureOwners': self.ownership_pending}, indent=2) + '\n')

    def call(self, label, args, **kwargs):
        return self.recorder.run(label, [self.docker] + args, **kwargs)

    def inspect(self, container):
        result = self.call('inspect-' + container[:12], ['inspect', container])
        values = json.loads(result.stdout)
        assert isinstance(values, list) and len(values) == 1
        assert values[0]['Id'] == container
        return values[0]

    def build(self):
        context = self.output / 'image-context'
        context.mkdir()
        for source, name in [(self.binary, 'tikuhelper'), (self.probe, 's04-probe')]:
            shutil.copy2(source, context / name)
            (context / name).chmod(0o555)
        (context / 'Dockerfile').write_text('FROM scratch\nCOPY tikuhelper /tikuhelper\nCOPY s04-probe /s04-probe\nENTRYPOINT ["/tikuhelper"]\n')
        self.call('build', ['build', '--network=none', '--pull=false', '--label', LABEL + '=' + self.run_id,
                            '-t', self.image, str(context)], timeout=120)
        self.image_built = True
        self.record_handles()

    def own_name(self, name):
        assert name.startswith('tikuhelper-s04-') and name.endswith(self.run_id[:10])
        self.container_names.append(name)
        self.record_handles()
        return name

    def remove_owned_fallback(self, name):
        assert name in self.container_names and name.startswith('tikuhelper-s04-') and name.endswith(self.run_id[:10]), 'recorded owned resource required'
        inspected = self.call('fallback-inspect-own', ['inspect', name], check=False)
        if inspected.returncode != 0:
            assert known_owned_resource_absence(inspected, name, ('object', 'container')), 'cannot confirm owned resource absence'
            return
        row = json.loads(inspected.stdout)[0]
        assert row['Config'].get('Labels', {}).get(LABEL) == self.run_id, 'refuse unrelated resource cleanup'
        container = row['Id']
        if row['State']['Running']:
            self.call('failure-only-cleanup-term', ['kill', '--signal=TERM', container], check=False)
            until = time.monotonic() + 12
            while self.inspect(container)['State']['Running'] and time.monotonic() < until:
                time.sleep(0.2)
            if self.inspect(container)['State']['Running']:
                self.call('FAILURE-only-cleanup-kill', ['kill', '--signal=KILL', container])
                self.cleanup.append({'forcedKillInFailedCleanup': True, 'container': container})
                until = time.monotonic() + 5
                while self.inspect(container)['State']['Running'] and time.monotonic() < until:
                    time.sleep(0.1)
        assert self.inspect(container)['State']['Running'] is False
        self.call('fallback-remove-own-stopped', ['rm', container])
        if container in self.container_ids:
            self.container_ids.remove(container)
        self.cleanup.append({'fallbackRemovedOwnContainer': container})

    def fixture_owner(self, fixture, target, label):
        self.helper_number += 1
        name = self.own_name('tikuhelper-s04-owner-' + str(self.helper_number) + '-' + self.run_id[:10])
        args = ['create', '--name', name, '--label', LABEL + '=' + self.run_id, '--network', 'none',
                '--read-only', '--user', '0:0', '--cap-drop', 'ALL', '--cap-add', 'CHOWN',
                '--cap-add', 'DAC_OVERRIDE', '--security-opt', 'no-new-privileges',
                '--workdir', '/fixture', '--entrypoint', '/s04-probe',
                '--mount', 'type=bind,src=' + str(fixture) + ',dst=/fixture', self.image,
                'fixture-owner', '--uid', str(target[0]), '--gid', str(target[1]), '--db', '/fixture/data/fixture.db']
        created = self.call(label + '-owner-create', args)
        container = created.stdout.decode().strip()
        assert re.fullmatch(r'[a-f0-9]{64}', container)
        self.container_ids.append(container)
        self.record_handles()
        try:
            before = self.inspect(container)
            assert before['Config'].get('Labels', {}).get(LABEL) == self.run_id
            assert before['Config']['User'] == '0:0' and before['Config']['Entrypoint'] == ['/s04-probe']
            host = before['HostConfig']
            assert host['NetworkMode'] == 'none' and host['ReadonlyRootfs'] is True
            assert not host.get('Privileged') and not host.get('PortBindings')
            assert {cap.removeprefix('CAP_') for cap in host.get('CapAdd', [])} == {'CHOWN', 'DAC_OVERRIDE'} and host.get('CapDrop') == ['ALL']
            mounts = before['Mounts']
            assert len(mounts) == 1 and Path(mounts[0]['Source']).resolve() == fixture.resolve()
            assert mounts[0]['Destination'] == '/fixture' and mounts[0]['RW'] is True
            self.call(label + '-owner-start', ['start', container])
            waited = self.call(label + '-owner-wait-once', ['wait', container], timeout=20)
            assert actual_exit(waited.stdout, self.inspect(container)['State']) == 0
            logs = self.call(label + '-owner-logs', ['logs', container])
            value = check_probe(logs.stdout, 'fixture-owner')
            assert (value['uid'], value['gid']) == target
            self.call(label + '-owner-remove', ['rm', container])
            self.container_ids.remove(container)
            self.container_names.remove(name)
            self.record_handles()
            return value
        finally:
            if name in self.container_names:
                self.remove_owned_fallback(name)
                self.container_names.remove(name)
                self.record_handles()

    def owner_portability_observation(self):
        # A single explicitly synthetic owner round trip, not another startup
        # matrix or evidence that a hosted runner has executed this test.
        fixture = self.output / 'owner-portability' / 'fixture'
        for path in [fixture, fixture / 'data', fixture / 'logs']:
            path.mkdir(parents=True, exist_ok=True)
            path.chmod(0o700)
        (fixture / '.s04-fixture').write_text(MARKER)
        simulated_owner = self.host_owner if self.host_owner != (1000, 1000) else (1001, 1001)
        (fixture / '.s04-host-owner.json').write_text(json.dumps({'uid': simulated_owner[0], 'gid': simulated_owner[1]}) + '\n')
        (fixture / 'config.yaml').write_text(CONFIG)
        seeded = self.recorder.run('owner-portability-seed', [str(self.probe), 'seed-legacy', '--db', str(fixture / 'data/fixture.db')])
        check_probe(seeded.stdout, 'seed-legacy')
        for name in ['Tiku.txt', 'Tiku-warning.txt', 'Tiku-error.txt', 'Tiku-thinking.txt']:
            p = fixture / 'logs' / name
            p.write_text('S04_SYNTHETIC_OWNER_LOG_ONLY\n')
            p.chmod(0o640)
        archive = fixture / 'logs/archive'
        archive.mkdir(mode=0o700)
        with zipfile.ZipFile(archive / '20261029_000000.zip', 'w') as zipped:
            zipped.writestr('Tiku.txt', 'S04_SYNTHETIC_OWNER_ARCHIVE_ONLY\n')
        before = {p.relative_to(fixture).as_posix(): (sha(p), p.stat().st_mode & 0o777) for p in fixture.rglob('*') if p.is_file()}
        observation = {'scope': 'one synthetic non1000 host owner roundtrip only; not hostedCI or main startup'}
        self.ownership_pending[str(fixture)] = self.host_owner
        self.record_handles()
        try:
            observation['simulatedNon1000Host'] = self.fixture_owner(fixture, simulated_owner, 'owner-portability-non1000')
            observation['mainUID1000'] = self.fixture_owner(fixture, (1000, 1000), 'owner-portability-1000')
        finally:
            observation['restoreActualHost'] = self.fixture_owner(fixture, self.host_owner, 'owner-portability-restore')
            self.ownership_pending.pop(str(fixture), None)
            self.record_handles()
        # The owner-record is explicitly synthetic in this isolated observation.
        # Every byte and permission bit, including that record, is conserved.
        for name, (digest, mode) in before.items():
            p = fixture / name
            assert p.stat().st_uid == self.host_owner[0] and p.stat().st_gid == self.host_owner[1]
            assert (p.stat().st_mode & 0o777) == mode
            assert sha(p) == digest
        observation['restoredHashAndPermissionConservation'] = True
        (fixture.parent / 'RESULT.json').write_text(json.dumps(observation, indent=2) + '\n')
        return observation

    def branch(self, name, mode, proxies, secret, expected_exit, legacy=False):
        directory = self.output / name
        fixture = directory / 'fixture'
        for path in [fixture, fixture / 'data', fixture / 'logs']:
            path.mkdir(parents=True, exist_ok=True)
            path.chmod(0o700)
        (fixture / '.s04-fixture').write_text(MARKER)
        (fixture / '.s04-host-owner.json').write_text(json.dumps({'uid': self.host_owner[0], 'gid': self.host_owner[1]}) + '\n')
        config = fixture / 'config.yaml'
        config.write_text(CONFIG)
        db = fixture / 'data/fixture.db'
        env = {'SQL_DSN': '', 'GIN_MODE': 'release', 'PATH': '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin', **WORKER_ENV}
        for key, value in [('TIKU_APP_MODE', mode), ('TRUSTED_PROXIES', proxies), ('TIKU_ADMIN_PASSWORD', secret)]:
            if value is not None:
                env[key] = value
        result = {'branch': name, 'input': {'mode': mode, 'trustedProxies': proxies,
                  'adminPasswordPresent': secret is not None, 'legacySyntheticSeed': legacy},
                  'databaseAbsentBeforeSeed': not db.exists(), 'configSHA256': sha(config)}
        if legacy:
            seeded = self.recorder.run(name + '-seed-legacy', [str(self.probe), 'seed-legacy', '--db', str(db)])
            result['seed'] = check_probe(seeded.stdout, 'seed-legacy')
            result['seedDatabaseSHA256'] = sha(db)
        result['workerIsolation'] = {'environment': check_worker_environment(env),
                                     'databaseBeforeMain': check_ticket_fixture_isolation(db, allow_absent=not legacy)}
        ownership_changed = self.host_owner != (1000, 1000)
        if ownership_changed:
            self.ownership_pending[str(fixture)] = self.host_owner
            self.record_handles()
            result['prepareMainFixtureOwner'] = self.fixture_owner(fixture, (1000, 1000), name + '-prepare')
        container_name = self.own_name('tikuhelper-s04-' + name.replace('_', '-') + '-' + self.run_id[:10])
        args = ['create', '--name', container_name,
                '--label', LABEL + '=' + self.run_id, '--network', 'none', '--read-only', '--user', '1000:1000',
                '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--workdir', '/fixture',
                '--mount', 'type=bind,src=' + str(fixture) + ',dst=/fixture']
        for key, value in env.items():
            args.extend(['--env', key + '=' + value])
        args.extend([self.image, '-log-dir', '/fixture/logs'])
        created = self.call(name + '-create', args)
        container = created.stdout.decode().strip()
        assert re.fullmatch(r'[a-f0-9]{64}', container)
        self.container_ids.append(container)
        self.record_handles()
        wait_called = False
        completed = False
        try:
            before = self.inspect(container)
            result['safety'] = check_container(before, self.run_id, fixture, env)
            result['workerIsolation']['inspectedEnvironment'] = check_worker_environment(dict(item.split('=', 1) for item in before['Config']['Env']))
            self.call(name + '-start', ['start', container])
            if expected_exit == 0:
                deadline = time.monotonic() + 60
                health = None
                while time.monotonic() < deadline:
                    probe = self.call(name + '-health', ['exec', container, '/s04-probe', 'health'], check=False, timeout=10)
                    if probe.returncode == 0:
                        health = check_probe(probe.stdout, 'health')
                        break
                    state = self.inspect(container)['State']
                    assert state['Running'] is True, name + ': main exited before healthy'
                    time.sleep(0.2)
                assert health is not None, name + ': health deadline reached for same actual process'
                result['health'] = health
                self.call(name + '-sigterm', ['kill', '--signal=TERM', container])
            wait_called = True
            waited = self.call(name + '-wait-once', ['wait', container], timeout=25)
            after = self.inspect(container)
            result['actualExit'] = actual_exit(waited.stdout, after['State'])
            assert result['actualExit'] == expected_exit
            if ownership_changed:
                result['restoreHostFixtureOwner'] = self.fixture_owner(fixture, self.host_owner, name + '-restore')
                ownership_changed = False
                self.ownership_pending.pop(str(fixture), None)
                self.record_handles()
            logs = self.call(name + '-literal-process-logs', ['logs', container])
            (directory / 'process.stdout').write_bytes(logs.stdout)
            (directory / 'process.stderr').write_bytes(logs.stderr)
            output = logs.stdout + logs.stderr
            assert output, 'actual process stdio capture must not be missing/empty'
            if name == 'default_production_missing_proxy':
                # 2026-11-10 语义变更（task_doc/67 §1.1 / §1.3）：
                # TRUSTED_PROXIES 与 TRUSTED_PROXIES_FILE 都为空 = 「没有输入」
                # → 信任所有来源、**正常启动**。生产环境同样适用，
                # 「留空 → 拒绝启动」的旧行为已废止。
                #
                # 所以本 case 现在测的是「缺 proxy 时启动成功、且留下了
                # 可追溯的信任所有告警」，不再是「被拒绝」。
                # 告警由 trusted_proxies.go:453 的 logTrustedProxies 在
                # config.Source == '' 时打印，文案含 TRUSTED_PROXIES，
                # 因此下面的断言在新语义下依然成立（已用真实二进制实测）。
                assert b'TRUSTED_PROXIES' in output
            elif name == 'production_missing_initial_secret':
                assert b'admin bootstrap password' in output
            else:
                assert 'Tiku 服务已停止'.encode() in output, 'actual graceful shutdown marker absent'
                # ⚠️ 2026-11-10 语义变更（task_doc/67 §1.1 / §2）：
                # 「信任所有来源」有两个触发条件，不是只有 development：
                #   ① mode == 'development'  → 走 loadTrustedProxiesLegacy（历史行为，信任所有）
                #   ② TRUSTED_PROXIES 为空 或 none → 按 §2「none 与留空完全等价，都信任所有来源」
                #
                # 两者都会采信 X-Forwarded-For，Gin 的 ClientIP 因此是探针注入的
                # 203.0.113.7，而非真实对端 127.0.0.1。
                #
                # 只按 mode 判断是旧语义（那时 none 对应「List 为空 = 不信任任何转发头」）。
                # 67 号 §2 明确废止了那个理解，所以这里必须同时看 proxies。
                trusted_all = (
                    mode == 'development'
                    or proxies is None
                    or str(proxies).strip() == ''
                    or str(proxies).strip().lower() == 'none'
                )
                expected_ip = '203.0.113.7' if trusted_all else '127.0.0.1'
                result['actualGinClientIP'] = actual_gin_client_ips(logs.stdout, expected_ip)
                result['trustedAllSources'] = trusted_all
                assert any(p.stat().st_size > 0 for p in (fixture / 'logs').glob('Tiku*.txt')), 'real log files were not written'
                operation = 'verify-legacy' if legacy else 'verify-development' if mode == 'development' else 'verify-new'
                verified = self.recorder.run(name + '-' + operation, [str(self.probe), operation, '--db', str(db)])
                result['database'] = check_probe(verified.stdout, operation)
                result['workerIsolation']['databaseAfterMain'] = check_ticket_fixture_isolation(db)
                assert result['workerIsolation']['databaseAfterMain']['settingsTablePresent'], 'main must migrate notification settings before qualification'
                result['databaseSHA256AfterMainAndRollbackProbe'] = sha(db)
            if expected_exit != 0:
                assert not db.exists() and not any((fixture / 'data').iterdir()), 'rejected startup wrote database data'
                result['databaseAbsentAfterReject'] = True
            assert sha(config) == result['configSHA256'], 'main altered fixture config'
            result['waitCount'] = 1
            result['forcedKill'] = False
            result['status'] = 'PASS'
            completed = True
            return result
        finally:
            # A failed case is never greened by cleanup. Only our exact labelled
            # container can receive signals or removal; a success never uses KILL.
            state = self.inspect(container)
            assert state['Config'].get('Labels', {}).get(LABEL) == self.run_id
            if state['State']['Running']:
                self.call(name + '-failure-cleanup-sigterm', ['kill', '--signal=TERM', container], check=False)
                if not wait_called:
                    wait_called = True
                    self.call(name + '-failure-cleanup-wait-once', ['wait', container], timeout=25)
                until = time.monotonic() + 12
                while self.inspect(container)['State']['Running'] and time.monotonic() < until:
                    time.sleep(0.2)
                if self.inspect(container)['State']['Running']:
                    self.call(name + '-FAILURE-forced-cleanup', ['kill', '--signal=KILL', container])
                    result['forcedKill'] = True
                    completed = False
            if not (directory / 'process.stdout').exists():
                logs = self.call(name + '-failed-process-logs', ['logs', container], check=False)
                (directory / 'process.stdout').write_bytes(logs.stdout)
                (directory / 'process.stderr').write_bytes(logs.stderr)
            if ownership_changed:
                result['restoreHostFixtureOwnerInFailure'] = self.fixture_owner(fixture, self.host_owner, name + '-failure-restore')
                ownership_changed = False
                self.ownership_pending.pop(str(fixture), None)
                self.record_handles()
            stopped = self.inspect(container)
            assert stopped['State']['Running'] is False
            self.call(name + '-remove-own-stopped-container', ['rm', container])
            self.container_ids.remove(container)
            self.container_names.remove(container_name)
            self.record_handles()
            result['cleanupOwnContainerRemoved'] = True
            if not completed:
                result['status'] = 'FAIL'
            (directory / 'RESULT.json').write_text(json.dumps(result, indent=2) + '\n')

    def finish(self):
        # Names/tag are persisted BEFORE create/build. Thus a signal between a
        # successful daemon action and Python flag assignment is still bounded
        # to exact known resources and independently checked owner labels.
        for name in self.container_names.copy():
            self.remove_owned_fallback(name)
            self.container_names.remove(name)
            self.record_handles()
        assert not self.container_ids, 'owned actual main container leaked'
        for fixture, target in list(self.ownership_pending.items()):
            self.fixture_owner(Path(fixture), tuple(target), 'failure-restore-recorded-host')
            self.ownership_pending.pop(fixture)
            self.record_handles()
        image = self.call('inspect-own-image', ['image', 'inspect', self.image], check=False)
        if image.returncode == 0:
            row = json.loads(image.stdout)[0]
            assert row['Config'].get('Labels', {}).get(LABEL) == self.run_id
            self.call('remove-own-image', ['image', 'rm', self.image])
            self.image_built = False
            self.record_handles()
            self.cleanup.append({'ownImageRemoved': True})
        else:
            assert known_owned_resource_absence(image, self.image, ('image', 'object')), 'cannot confirm owned image absence'
        assert not any(item.get('forcedKillInFailedCleanup') for item in self.cleanup), 'forced cleanup cannot qualify as acceptance'
        assert {key: os.environ.get(key) for key in SELECTED_ENV} == self.before_env
        assert sha(self.binary) == self.input_hashes['actualServiceELFSHA256']
        assert sha(self.probe) == self.input_hashes['probeELFSHA256']


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--probe', type=Path, required=True)
    parser.add_argument('--evidence', type=Path, required=True)
    parser.add_argument('--docker', default='docker')
    args = parser.parse_args(argv)
    for binary in [args.binary, args.probe]:
        assert binary.is_absolute() and binary.is_file() and not binary.is_symlink()
        with binary.open('rb') as stream:
            assert stream.read(4) == b'\x7fELF', 'actual native ELF required; no fallback or skipped checks'
    assert args.evidence.is_absolute()
    flow = MainFlow(args.binary, args.probe, args.evidence, args.docker)
    def terminate(_signum, _frame):
        raise CommandFailure('orchestrator received SIGTERM; clean only recorded owned resources')
    previous_sigterm = signal.signal(signal.SIGTERM, terminate)
    result = {'scope': 'actual service main in network-none/read-only/user1000 containers; only synthetic fixture credentials/database',
              'runID': flow.run_id, 'inputHashes': flow.input_hashes, 'attempt': str(flow.output), 'status': 'FAIL'}
    exit_code = 1
    try:
        flow.build()
        result['ownerPortabilityObservation'] = flow.owner_portability_observation()
        cases = [('default_production_missing_proxy', None, None, NEW_PASSWORD, 0, False),
                 ('production_missing_initial_secret', 'production', 'none', None, 1, False),
                 ('default_production_strong_new_database', None, 'none', NEW_PASSWORD, 0, False),
                 ('existing_legacy_admin_not_reset', 'production', 'none', NEW_PASSWORD, 0, True),
                 ('explicit_development_compatibility', 'development', None, NEW_PASSWORD, 0, False)]
        # cases 的 5 元组是 (name, TIKU_APP_MODE, TRUSTED_PROXIES, TIKU_ADMIN_PASSWORD,
        # expected_exit, legacy)。proxies=None 表示**不注入** TRUSTED_PROXIES 环境变量
        # （见 branch() 里 `if value is not None`），也就是「两个来源都为空」。
        #
        # ⚠️ 2026-11-10 语义变更（task_doc/67 §1.1 / §1.3）：
        # `default_production_missing_proxy` 的 expected_exit 由 1 改为 0。
        # 旧语义「生产环境留空 → 拒绝启动」已废止，现在留空 = 信任所有来源、
        # 照常启动。仍保持 exit=1 会让容器一直运行到 `docker wait` 超时（exit 124）。
        for case in cases:
            observed = flow.branch(*case)
            assert observed['status'] == 'PASS' and observed['forcedKill'] is False
            flow.branches.append(observed)
            print(json.dumps({'branch': case[0], 'status': observed['status'], 'actualExit': observed['actualExit']}, ensure_ascii=False), flush=True)
        assert len(flow.branches) == 5
        result['status'] = 'PASS'
        exit_code = 0
    except Exception as error:
        result['failure'] = type(error).__name__ + ': ' + str(error)
        print(result['failure'], file=sys.stderr)
        traceback.print_exc(file=sys.stderr)
    finally:
        try:
            flow.finish()
            result['environmentRestored'] = True
            result['inputELFsUnchanged'] = True
        except Exception as error:
            result['cleanupFailure'] = type(error).__name__ + ': ' + str(error)
            traceback.print_exc(file=sys.stderr)
            result['status'] = 'FAIL'
            exit_code = 1
        result['branches'] = flow.branches
        result['cleanup'] = flow.cleanup
        (flow.output / 'RESULT.json').write_text(json.dumps(result, indent=2) + '\n')
        (args.evidence / 'LATEST.json').write_text(json.dumps({'attempt': str(flow.output), 'result': str(flow.output / 'RESULT.json'), 'status': result['status'], 'exit': exit_code}, indent=2) + '\n')
        print(json.dumps({'result': str(flow.output / 'RESULT.json'), 'status': result['status'], 'exit': exit_code}), flush=True)
    signal.signal(signal.SIGTERM, previous_sigterm)
    return exit_code


if __name__ == '__main__':
    sys.exit(main())
