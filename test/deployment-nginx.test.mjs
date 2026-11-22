import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtemp, mkdir, readFile, rm, writeFile, cp, rename, access } from 'node:fs/promises';
import http from 'node:http';
import https from 'node:https';
import tls from 'node:tls';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const run = join(repo, 'run');
function execute(binary, args, options = {}) {
  return spawnSync(binary, args, { cwd: repo, encoding: 'utf8', windowsHide: true, timeout: 30000, ...options });
}
function successful(result) {
  assert.equal(result.error, undefined, result.error?.message);
  assert.equal(result.status, 0, `${result.stdout || ''}${result.stderr || ''}`);
  return result.stdout.trim();
}

test('Docker build context excludes operator certificates and Nginx logs', async (t) => {
  const probe = execute('docker', ['version', '--format', '{{.Server.Version}}']);
  if (probe.error?.code === 'ENOENT' || probe.status !== 0) return t.skip('Docker Engine is unavailable');
  const context = await mkdtemp(join(tmpdir(), 'tiku-dockerignore-'));
  const output = join(context, 'output');
  await mkdir(join(context, 'run/nginx/ssl'), { recursive: true });
  await mkdir(join(context, 'run/nginx/logs'), { recursive: true });
  await writeFile(join(context, '.dockerignore'), await readFile(join(repo, '.dockerignore')));
  await writeFile(join(context, 'Dockerfile'), 'FROM scratch\nCOPY . /context\n');
  await writeFile(join(context, 'run/nginx/ssl/private.key'), 'synthetic operator key');
  await writeFile(join(context, 'run/nginx/logs/access.log'), 'synthetic access log');
  await writeFile(join(context, 'retained.txt'), 'must remain');
  const result = execute('docker', ['buildx', 'build', '--file', join(context, 'Dockerfile'), '--output', `type=local,dest=${output}`, context], { timeout: 60000 });
  try {
    successful(result);
    await assert.rejects(access(join(output, 'context/run/nginx/ssl/private.key')));
    await assert.rejects(access(join(output, 'context/run/nginx/logs/access.log')));
    assert.equal(await readFile(join(output, 'context/retained.txt'), 'utf8'), 'must remain');
  } finally {
    await rm(context, { recursive: true, force: true });
  }
});

test('Nginx access log only keeps paths under reserved API prefixes', async () => {
  const config = await readFile(join(run, 'nginx/conf.d/tiku-adapter.conf'), 'utf8');
  const map = /map \$uri \$tiku_log_path \{([^}]+)\}/.exec(config)?.[1];
  assert.ok(map);
  assert.match(map, /default\s+"\[public-entry\]";/);
  const patterns = [...map.matchAll(/^\s*~(\S+)\s+\$uri;/gm)].map((match) => new RegExp(match[1]));
  for (const prefix of ['css', 'js', 'img', 'fonts', 'adapter', 'custom']) {
    assert.equal(patterns.some((pattern) => pattern.test(`/${prefix}/private-entry`)), false, prefix);
  }
  assert.ok(patterns.some((pattern) => pattern.test('/api/scripts')));
  assert.ok(patterns.some((pattern) => pattern.test('/adapter-service/scripts')));
});

test('single-container Compose preserves mounts, ports, and graceful shutdown', (t) => {
  const result = execute('docker', ['compose', '-f', join(run, 'docker-compose.yml'), 'config', '--format', 'json'], { env: { ...process.env, TRUSTED_PROXIES: 'none', TRUSTED_PROXIES_FILE: '' } });
  if (result.error?.code === 'ENOENT') return t.skip('Docker Compose CLI is unavailable');
  const compose = JSON.parse(successful(result));
  assert.equal(compose.name, 'tikuhelper');
  assert.deepEqual(Object.keys(compose.services), ['tikuhelper']);
  const backend = compose.services.tikuhelper;
  assert.equal(backend.stop_grace_period, '30s');
  assert.deepEqual(backend.ports.map(({ host_ip, target, published }) => ({ host_ip, target, published })), [
    { host_ip: '127.0.0.1', target: 8060, published: '8060' },
    { host_ip: undefined, target: 8680, published: '8680' },
    { host_ip: undefined, target: 9043, published: '9043' },
  ]);
  for (const [source, target] of [['data', '/app/data'], ['logs', '/app/logs'], ['config.yaml', '/app/config.yaml'], ['trusted_proxies.txt', '/app/trusted_proxies.txt'], ['nginx/conf.d', '/etc/nginx/conf.d'], ['nginx/ssl', '/etc/nginx/ssl'], ['nginx/html', '/usr/share/nginx/html'], ['nginx/logs', '/var/log/nginx']]) {
    assert.ok(backend.volumes.some((mount) => mount.target === target && resolve(mount.source) === join(run, source)), target);
  }
  assert.deepEqual(backend.healthcheck.test, ['CMD', '/app/container-gateway', '--healthcheck']);
  assert.equal(backend.environment.TRUSTED_PROXIES, 'none');
  assert.ok(!Object.keys(backend.environment).some((key) => key.startsWith('EXTENSION_')));
});

async function request(port, encrypted = false, headers = {}, path = '/probe') {
  return new Promise((resolveRequest, reject) => {
    const req = (encrypted ? https : http).get({ hostname: '127.0.0.1', port, path, headers, rejectUnauthorized: false, timeout: 1500, agent: false }, (res) => {
      let body = '';
      res.setEncoding('utf8');
      res.on('data', (chunk) => { body += chunk; });
      res.on('end', () => {
        try { assert.equal(res.statusCode, 200, body); resolveRequest(JSON.parse(body)); } catch (error) { reject(error); }
      });
    });
    req.on('timeout', () => req.destroy(new Error('proxy probe timed out')));
    req.on('error', reject);
  });
}
async function eventually(check, message, attempts = 120) {
  let last;
  for (let i = 0; i < attempts; i++) {
    try { return await check(); } catch (error) { last = error; await delay(100); }
  }
  throw new Error(`${message}: ${last?.stack || last}`);
}
async function peerSerial(port, servername) {
  return new Promise((resolvePeer, reject) => {
    const socket = tls.connect({ host: '127.0.0.1', port, servername, rejectUnauthorized: false }, () => {
      const serial = socket.getPeerCertificate().serialNumber;
      socket.end(); resolvePeer(serial);
    });
    socket.setTimeout(1500, () => socket.destroy(new Error('TLS probe timeout')));
    socket.on('error', reject);
  });
}
async function websocket(port) {
  return new Promise((resolveUpgrade, reject) => {
    const req = http.request({ host: '127.0.0.1', port, path: '/probe', headers: { Connection: 'Upgrade', Upgrade: 'websocket' } });
    req.on('upgrade', (res, socket) => { socket.destroy(); resolveUpgrade(res.statusCode); });
    req.on('error', reject); req.setTimeout(2000, () => req.destroy(new Error('WS timeout'))); req.end();
  });
}

test('container gateway forwards headers and WebSocket, safely rotates SNI snapshots, and supervises children', { timeout: 240000 }, async (t) => {
  const image = process.env.TIKU_GATEWAY_TEST_IMAGE;
  if (!image) return t.skip('Set TIKU_GATEWAY_TEST_IMAGE to run isolated real-container regression');
  successful(execute('docker', ['image', 'inspect', image]));
  const directory = await mkdtemp(join(tmpdir(), 'tiku-gateway-'));
  const name = `tiku-gateway-test-${process.pid}-${Date.now()}`;
  const conf = join(directory, 'conf'); const certs = join(directory, 'ssl');
  await mkdir(conf); await mkdir(certs);
  await cp(join(run, 'nginx/conf.d'), conf, { recursive: true });
  const fixture = join(directory, process.platform === 'win32' ? 'fixture.exe' : 'fixture');
  const backend = join(directory, 'backend');
  successful(execute('go', ['build', '-o', fixture, './internal/gateway/testdata/probe']));
  successful(execute('go', ['build', '-o', backend, './internal/gateway/testdata/probe'], { env: { ...process.env, GOOS: 'linux', GOARCH: 'amd64', CGO_ENABLED: '0' } }));
  let running = false;
  let ports = [];
  const ownedPorts = [];
  const readPorts = () => {
    const binding = JSON.parse(successful(execute('docker', ['inspect', name])))[0].NetworkSettings.Ports;
    ports = [8060, 8680, 9043].map((port) => Number(binding[`${port}/tcp`][0].HostPort));
    ownedPorts.push(...ports.map((port, i) => [port, i === 2]));
  };
  t.after(async () => {
    if (running) successful(execute('docker', ['rm', '-f', name]));
    for (const [port, encrypted] of ownedPorts) {
      await assert.rejects(request(port, encrypted), 'owned temporary port must be released');
    }
    await rm(directory, { recursive: true, force: true });
  });
  const start = (trusted) => {
    successful(execute('docker', ['run', '-d', '--name', name, '--env', `TRUSTED_PROXIES=${trusted}`,
      '--mount', `type=bind,source=${conf},target=/etc/nginx/conf.d,readonly`,
      '--mount', `type=bind,source=${certs},target=/etc/nginx/ssl,readonly`,
      '--mount', `type=bind,source=${backend},target=/app/tiku-adapter,readonly`,
      '-p', '127.0.0.1::8060', '-p', '127.0.0.1::8680', '-p', '127.0.0.1::9043', image]));
    running = true;
    readPorts();
  };
  const certificate = (stem, names, before, after, serial, kind = 'ec') => successful(execute(fixture, ['cert', certs, stem, names, before, after, kind, String(serial)]));
  start('none');
  try {
    await eventually(() => request(ports[0]), 'HTTP-only gateway start');
    assert.match(successful(execute('docker', ['exec', name, '/app/container-gateway', '--healthcheck'])), /HTTP ready; HTTPS 未启用/);
	await eventually(() => assert.rejects(request(ports[2], true)), 'expired TLS listener removed');
    for (const port of ports.slice(0, 2)) {
      const data = await request(port, false, { Host: 'scripts.example:8680', 'X-Forwarded-For': '198.51.100.99, 203.0.113.7', 'X-Forwarded-Proto': 'https', Forwarded: 'for=spoof' });
      assert.equal(data.host, 'scripts.example:8680');
      assert.equal(data.headers['X-Forwarded-For'][0], '198.51.100.99');
      assert.equal(data.headers['X-Forwarded-Proto'][0], 'http');
      assert.equal(data.headers.Forwarded, undefined);
      assert.equal(data.listen, '127.0.0.1:8061'); assert.equal(data.gateway, '1');
    }
    assert.equal(await websocket(ports[1]), 101);
    certificate('old', 'one.example,*.wild.example', '-2h', '2h', 11, 'rsa');
    certificate('new', 'one.example,two.example', '-1h', '1h', 22);
    await rename(join(certs, 'old.certificate'), join(certs, 'fullchain.pem'));
    await rename(join(certs, 'old.private'), join(certs, 'privkey.key'));
	    await eventually(() => assert.match(successful(execute('docker', ['exec', name, '/app/container-gateway', '--healthcheck'])), /HTTP ready/), 'certificate reload health');
    await eventually(async () => assert.equal(await peerSerial(ports[2], 'one.example'), '16'), 'newest exact SAN');
    assert.equal(await peerSerial(ports[2], 'x.wild.example'), '0B');
		assert.equal(await peerSerial(ports[2], 'X.WILD.EXAMPLE'), '0B');
    assert.equal(await peerSerial(ports[2], 'unknown.example'), '0B');
    successful(execute('docker', ['exec', name, 'sh', '-c', 'for key in /run/tiku-gateway/generation-*/*.key; do [ "$(stat -c %a "$key")" = 600 ] || exit 1; done']));
    const tlsData = await request(ports[2], true, { 'X-Forwarded-Proto': 'http' });
    assert.equal(tlsData.headers['X-Forwarded-Proto'][0], 'https');
    await writeFile(join(certs, 'new.private'), 'broken key');
    const original = await readFile(join(conf, 'tiku-adapter.conf'), 'utf8');
    await writeFile(join(conf, 'tiku-adapter.conf'), `${original}\n# trigger safe reload with corrupted source\n`);
    await delay(4000);
    assert.equal(await peerSerial(ports[2], 'one.example'), '16');
    successful(execute('docker', ['exec', name, '/app/container-gateway', '--healthcheck']));
    await writeFile(join(conf, 'tiku-adapter.conf'), 'invalid_nginx_directive;\n');
    await delay(4000);
    assert.equal(await peerSerial(ports[2], 'one.example'), '16');
    successful(execute('docker', ['exec', name, '/app/container-gateway', '--healthcheck']));
    await writeFile(join(conf, 'tiku-adapter.conf'), original);
    certificate('replacement', 'one.example', '-10m', '1h', 33);
    await eventually(async () => assert.equal(await peerSerial(ports[2], 'one.example'), '21'), 'actual worker reload accepts replacement SAN');
    await request(ports[1], false, {}, '/css/private-entry-probe');
    const access = successful(execute('docker', ['exec', name, 'cat', '/var/log/nginx/access.log']));
    assert.match(access, /\[public-entry\]/); assert.ok(!access.includes('/css/private-entry-probe'));
    // A restart retains container-local active.conf: startup must not reload a nonexistent master.
    successful(execute('docker', ['restart', '--time', '25', name]));
    readPorts();
    await eventually(() => request(ports[1]), 'restart with previous runtime state');
    // Explicit proxy peer trust applies to the original TCP peer, independently of XFF.
    const gateway = JSON.parse(successful(execute('docker', ['inspect', name])))[0].NetworkSettings.Networks.bridge.Gateway;
    successful(execute('docker', ['rm', '-f', name])); running = false;
    await rm(certs, { recursive: true }); await mkdir(certs);
    certificate('short-lived', 'expiry.example', '-1m', '10s', 44);
    start(gateway);
    await eventually(() => request(ports[1]), 'gateway with explicit trusted peer');
    const forwarded = await request(ports[1], false, { 'X-Forwarded-Proto': 'https', 'X-Forwarded-For': '203.0.113.9' });
    assert.equal(forwarded.headers['X-Forwarded-Proto'][0], 'https');
    assert.equal(forwarded.headers['X-Forwarded-For'][0], '203.0.113.9');
    await eventually(async () => assert.equal(await peerSerial(ports[2], 'expiry.example'), '2C'), 'short-lived TLS snapshot accepted');
    await eventually(() => assert.match(successful(execute('docker', ['exec', name, '/app/container-gateway', '--healthcheck'])), /HTTP ready; HTTPS 未启用/), 'natural expiry disables TLS', 180);
	    await eventually(() => assert.rejects(request(ports[2], true)), 'expired TLS listener removed');
    await request(ports[1]);
    certificate('renewed', 'expiry.example', '-1m', '1h', 55);
    await eventually(async () => assert.equal(await peerSerial(ports[2], 'expiry.example'), '37'), 'renewed certificate restores TLS');
    // Kill Nginx only; PID 1 must stop its backend and exit.
    successful(execute('docker', ['exec', name, 'sh', '-c', 'kill -TERM "$(cat /run/tiku-gateway/nginx.pid)"']));
    await eventually(() => assert.equal(successful(execute('docker', ['inspect', '--format', '{{.State.Running}}', name])), 'false'), 'Nginx exit propagation');
  } catch (error) {
    const logs = execute('docker', ['logs', name]);
    error.message += `\ncontainer logs:\n${logs.stdout}\n${logs.stderr}`;
    throw error;
  }
});

test('production image starts the real backend against a fresh isolated database', { timeout: 90000 }, async (t) => {
  const image = process.env.TIKU_GATEWAY_TEST_IMAGE;
  if (!image) return t.skip('Set TIKU_GATEWAY_TEST_IMAGE to run real backend smoke');
  const directory = await mkdtemp(join(tmpdir(), 'tiku-gateway-backend-'));
  const name = `tiku-gateway-backend-${process.pid}-${Date.now()}`;
  const data = join(directory, 'data'); const certs = join(directory, 'ssl'); const config = join(directory, 'config.yaml');
  await mkdir(data); await mkdir(certs);
  await writeFile(config, 'database:\n  path: data/tiku.db\napi: []\nlimit:\n  enable: false\n');
  let running = false; let port;
  t.after(async () => {
    if (running) successful(execute('docker', ['rm', '-f', name]));
    if (port) await assert.rejects(request(port), 'real-backend temporary port must be released');
    await rm(directory, { recursive: true, force: true });
  });
  successful(execute('docker', ['run', '-d', '--name', name,
    '--env', 'TIKU_APP_MODE=production', '--env', 'TIKU_ADMIN_PASSWORD=GatewayFixturePassword246!',
    '--env', 'TRUSTED_PROXIES=none', '--env', 'EXTENSION_UPDATE_ENABLED=false',
    '--env', 'EXTENSION_EDGE_ID=', '--env', 'EXTENSION_CHROME_ID=',
    '--mount', `type=bind,source=${data},target=/app/data`,
    '--mount', `type=bind,source=${config},target=/app/config.yaml,readonly`,
    '--mount', `type=bind,source=${certs},target=/etc/nginx/ssl,readonly`,
    '-p', '127.0.0.1::8060', image]));
  running = true;
  port = Number(JSON.parse(successful(execute('docker', ['inspect', name])))[0].NetworkSettings.Ports['8060/tcp'][0].HostPort);
  try {
    await eventually(() => request(port, false, {}, '/readyz'), 'real backend readiness', 300);
    assert.match(successful(execute('docker', ['exec', name, '/app/container-gateway', '--healthcheck'])), /HTTP ready; HTTPS 未启用/);
    successful(execute('docker', ['exec', name, 'test', '-s', '/app/data/tiku.db']));
    successful(execute('docker', ['stop', '--time', '25', name]));
    assert.equal(successful(execute('docker', ['inspect', '--format', '{{.State.ExitCode}}', name])), '0');
  } catch (error) {
    const logs = execute('docker', ['logs', name]); error.message += `\nreal-backend logs:\n${logs.stdout}\n${logs.stderr}`; throw error;
  }
});
