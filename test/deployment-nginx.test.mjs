import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import http from 'node:http';
import https from 'node:https';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const run = join(repo, 'run');
const normalizedPath = (path) => path.replaceAll('\\', '/');

function execute(binary, args, options = {}) {
  return spawnSync(binary, args, { encoding: 'utf8', windowsHide: true, timeout: 20000, ...options });
}

function successful(result) {
  assert.equal(result.error, undefined, result.error?.message);
  assert.equal(result.status, 0, `${result.stdout || ''}${result.stderr || ''}`);
}

test('Compose keeps cache persistence and limits the backend to loopback', (t) => {
  const result = execute('docker', ['compose', '-f', join(run, 'docker-compose.yml'), 'config', '--format', 'json']);
  if (result.error?.code === 'ENOENT') return t.skip('Docker Compose CLI is unavailable');
  successful(result);
  const backend = JSON.parse(result.stdout).services['tiku-adapter'];
  assert.deepEqual(backend.ports.map(({ host_ip, target, published }) => ({ host_ip, target, published })), [
    { host_ip: '127.0.0.1', target: 8060, published: '8060' },
  ]);
  assert.ok(backend.volumes.some(({ source, target }) => target === '/app/data' && resolve(source) === join(run, 'data')));
  const nginx = JSON.parse(result.stdout).services.nginx;
  assert.ok(nginx.volumes.some(({ source, target }) => target === '/app/trusted_proxies.txt' && resolve(source) === join(run, 'trusted_proxies.txt')));
  assert.ok(nginx.volumes.some(({ source, target }) => target === '/app/trusted-proxies-convert.sh' && resolve(source) === join(run, 'nginx/trusted-proxies-convert.sh')));
  assert.ok(!backend.volumes.some(({ target }) => target === '/app/trusted_proxies.txt'));
  assert.equal(Object.hasOwn(backend.environment, 'TRUSTED_PROXIES'), false);
  assert.ok(!Object.keys(backend.environment).some((key) => key.startsWith('EXTENSION_')));
});

async function listen(server) {
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  return server.address().port;
}

async function freePort() {
  const server = http.createServer();
  const port = await listen(server);
  await new Promise((resolveClose) => server.close(resolveClose));
  return port;
}

async function request(port, encrypted, headers) {
  return new Promise((resolveRequest, reject) => {
    const req = (encrypted ? https : http).get({
      hostname: '127.0.0.1', port, path: '/deployment-probe', headers,
      rejectUnauthorized: false, timeout: 2000, agent: false,
    }, (res) => {
      let body = '';
      res.setEncoding('utf8');
      res.on('data', (chunk) => { body += chunk; });
      res.on('end', () => {
        try {
          assert.equal(res.statusCode, 200, body);
          resolveRequest(JSON.parse(body));
        } catch (error) { reject(error); }
      });
    });
    req.on('timeout', () => req.destroy(new Error('proxy probe timed out')));
    req.on('error', reject);
  });
}

test('real Nginx cleans forwarded headers on HTTP/HTTPS and trusts only configured upstreams', { timeout: 60000 }, async (t) => {
  const nginx = process.env.NGINX_BINARY || 'nginx';
  const openssl = process.env.OPENSSL_BINARY || 'openssl';
  if (execute(nginx, ['-v']).error?.code === 'ENOENT') return t.skip('Set NGINX_BINARY to enable the real proxy regression');
  if (execute(openssl, ['version']).error?.code === 'ENOENT') return t.skip('Set OPENSSL_BINARY to generate a temporary TLS certificate');

  const directory = await mkdtemp(join(tmpdir(), 'tiku-nginx-test-'));
  const prefix = `${normalizedPath(directory)}/`;
  let child;
  let nginxRunning = false;
  let processOutput = '';
  let nginxPort;
  let tlsPort;
  const backend = http.createServer((req, res) => {
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify(req.headers));
  });
  const stop = async () => {
    if (!nginxRunning) return;
    successful(execute(nginx, ['-p', prefix, '-c', 'nginx.conf', '-s', 'stop'], { cwd: directory }));
    nginxRunning = false;
    if (child?.exitCode === null) await Promise.race([once(child, 'exit'), delay(3000)]);
    if (child?.exitCode === null) child.kill();
    for (const port of [nginxPort, tlsPort]) {
      await assert.rejects(request(port, port === tlsPort, {}), 'temporary Nginx listener must be released');
    }
  };
  t.after(async () => {
    try { await stop(); } finally {
      backend.closeAllConnections();
      await new Promise((resolveClose) => backend.close(resolveClose));
      await rm(directory, { recursive: true, force: true });
    }
  });

  await mkdir(join(directory, 'logs'));
  // The official Windows package expects these relative prefix directories even
  // when proxy buffering is disabled in the included configuration.
  await mkdir(join(directory, 'temp/client_body_temp'), { recursive: true });
  await mkdir(join(directory, 'temp/proxy_temp'), { recursive: true });
  await mkdir(join(directory, 'temp/fastcgi_temp'), { recursive: true });
  await mkdir(join(directory, 'temp/uwsgi_temp'), { recursive: true });
  await mkdir(join(directory, 'temp/scgi_temp'), { recursive: true });
  const backendPort = await listen(backend);
  successful(execute(openssl, ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'key.pem',
    '-out', 'cert.pem', '-days', '1', '-subj', '/CN=localhost'], { cwd: directory,
    env: { ...process.env, MSYS_NO_PATHCONV: '1' } }));
  const actual = await readFile(join(run, 'nginx/conf.d/tiku-adapter.conf'), 'utf8');
  await writeFile(join(directory, 'tiku-proxy.inc'), await readFile(join(run, 'nginx/conf.d/tiku-proxy.inc')));
  await writeFile(join(directory, 'trusted-proxies.generated.conf'), '# generated probe\n');

  for (const trustedUpstream of [false, true]) {
    nginxPort = await freePort();
    tlsPort = await freePort();
    const config = actual
      .replace('server tiku-adapter:8060;', `server 127.0.0.1:${backendPort};`)
      .replace('listen 8680;', `listen 127.0.0.1:${nginxPort};`)
      .replace('listen 9043 ssl;', `listen 127.0.0.1:${tlsPort} ssl;`)
      .replaceAll('/etc/nginx/conf.d/tiku-proxy.inc', `${prefix}tiku-proxy.inc`)
      .replace('/tmp/trusted-proxies.generated.conf', `${prefix}trusted-proxies.generated.conf`)
      .replace('/etc/nginx/ssl/fullchain.pem', `${prefix}cert.pem`)
      .replace('/etc/nginx/ssl/privkey.key', `${prefix}key.pem`);
    await writeFile(join(directory, 'trusted-proxies.generated.conf'), trustedUpstream
      ? 'set_real_ip_from 127.0.0.1;\n'
      : 'set_real_ip_from 0.0.0.0/0;\nset_real_ip_from ::/0;\n');
    await writeFile(join(directory, 'nginx.conf'), `daemon off; master_process off;\npid nginx.pid;\nerror_log logs/error.log;\nevents {}\nhttp { ${config}\n}\n`);
    const validation = execute(nginx, ['-p', prefix, '-c', 'nginx.conf', '-t'], { cwd: directory });
    successful(validation);
    t.diagnostic(validation.stderr.trim());
    child = spawn(nginx, ['-p', prefix, '-c', 'nginx.conf'], { cwd: directory, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
    child.stdout.on('data', (data) => { processOutput += data; });
    child.stderr.on('data', (data) => { processOutput += data; });
    child.on('error', (error) => { processOutput += error.message; });
    nginxRunning = true;
    let ready = false;
    for (let i = 0; i < 80; i++) {
      try { await request(nginxPort, false, {}); ready = true; break; } catch { await delay(50); }
    }
    assert.ok(ready, processOutput);
    for (const encrypted of [false, true]) {
      const received = await request(encrypted ? tlsPort : nginxPort, encrypted, {
        'X-Forwarded-For': '198.51.100.99, 203.0.113.7', 'X-Real-IP': '198.51.100.98',
        Forwarded: 'for=198.51.100.97', 'X-Forwarded-Proto': 'spoofed',
      });
      const expected = trustedUpstream ? '203.0.113.7' : '198.51.100.99';
      assert.equal(received['x-forwarded-for'], expected);
      assert.equal(received['x-real-ip'], expected);
      assert.equal(received.forwarded, undefined);
      assert.equal(received['x-forwarded-proto'], encrypted ? 'https' : 'http');
      assert.equal(received['x-forwarded-port'], String(encrypted ? tlsPort : nginxPort));
    }
    await stop();
  }
});
