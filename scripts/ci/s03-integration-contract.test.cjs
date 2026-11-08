const { test } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs'), os = require('node:os'), path = require('node:path')
const { spawnSync } = require('node:child_process')
const root = path.resolve(__dirname, '../..')
test('fixed proxy HTTP and page integration is required in the backend job without removing existing strict gates', () => {
  const workflow = fs.readFileSync(path.join(root, '.github/workflows/pr-checks.yml'), 'utf8')
  const backend = workflow.split('  backend:')[1].split('  audit:')[0]
  assert.match(backend, /actions\/setup-go@[a-f0-9]{40}/)
  assert.match(backend, /actions\/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1/)
  assert.match(backend, /node-version: '22\.23\.1'/)
  assert.match(backend, /run: npm ci --prefix web/)
  assert.match(backend, /run: npm run --prefix web test:integration:s03/)
  assert.match(backend, /run: npm run --prefix web test:integration:s02/)
  assert.equal((backend.match(/run: npm ci --prefix web/g) || []).length, 1)
  for (const gate of ['go build ./...', 'go vet ./...', 'go test -short ./... -count=1', 'go test -race ./internal/api ./internal/controller ./internal/service -count=1', 'python3 -B scripts/ci/http-smoke.py']) assert.ok(backend.includes(gate), gate)
  assert.doesNotMatch(backend, /continue-on-error|\|\| true|push: true|secrets\./)
  const scripts = JSON.parse(fs.readFileSync(path.join(root, 'web/package.json'), 'utf8')).scripts
  assert.equal(scripts['test:integration:s03'], 'node scripts/run-s03-proxy-flow.cjs')
})
test('Proxy integration runner propagates compiler failure and never proceeds to the native check', () => {
  const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'tikuhelper-s03-runner-contract-'))
  try {
    const fake = path.join(temp, 'go-failure')
    fs.writeFileSync(fake, '#!/bin/sh\nprintf "S03_COMPILE_FAILURE_FIXTURE\\n" >&2\nexit 23\n', { mode: 0o755 })
    const result = spawnSync(process.execPath, [path.join(root, 'web/scripts/run-s03-proxy-flow.cjs')], {
      cwd: root, encoding: 'utf8', timeout: 15000, env: { ...process.env, TIKU_S03_GO_BINARY: fake },
    })
    assert.ifError(result.error)
    assert.equal(result.status, 23)
    assert.match(result.stderr, /S03_COMPILE_FAILURE_FIXTURE/)
    assert.doesNotMatch(result.stdout, /TAP version|IntelligentImportProxyFlow/)
  } finally {
    assert.equal(path.dirname(path.resolve(temp)), path.resolve(os.tmpdir()))
    assert.ok(path.basename(temp).startsWith('tikuhelper-s03-runner-contract-'))
    fs.rmSync(temp, { recursive: true, force: true })
  }
})
