const { test } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs'), os = require('node:os'), path = require('node:path')
const { spawnSync } = require('node:child_process')
const root = path.resolve(__dirname, '../..')
test('real main startup fixture gate is required after the actual static service binary without weakening existing gates', () => {
  const workflow = fs.readFileSync(path.join(root, '.github/workflows/pr-checks.yml'), 'utf8')
  const backend = workflow.split('  backend:')[1].split('  audit:')[0]
  for (const gate of ['go build ./...', 'go vet ./...', 'go test -short -p 1 ./... -count=1', 'go test -short -race -p 1 ./internal/api ./internal/controller ./internal/service ./internal/search -count=1', 'test:integration:s02', 'test:integration:s03', 'python3 -B scripts/ci/http-smoke.py']) assert.ok(backend.includes(gate), gate)
  assert.ok(backend.includes('CGO_ENABLED=0 go build -o ci-output/tiku-ci ./cmd/adapter-service'))
  assert.ok(backend.includes('python3 -B scripts/ci/run-s04-main-flow.py --binary ci-output/tiku-ci --evidence ci-output/s04-main-flow'))
  assert.ok(backend.indexOf('http-smoke.py') < backend.indexOf('run-s04-main-flow.py'))
  assert.doesNotMatch(backend, /continue-on-error|\|\| true|push: true|secrets\./)
  assert.match(workflow, /permissions:\s+contents: read/)
})
test('real main startup wrapper propagates probe compiler failure without invoking Docker or a fake service', () => {
  const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'tikuhelper-s04-runner-contract-'))
  try {
    const fakeGo = path.join(temp, 'go-failure'), binary = path.join(temp, 'synthetic-elf')
    fs.writeFileSync(fakeGo, '#!/bin/sh\nprintf "S04_COMPILE_FAILURE_FIXTURE\\n" >&2\nexit 23\n', { mode: 0o755 })
    fs.writeFileSync(binary, Buffer.from([0x7f, 0x45, 0x4c, 0x46]))
    const result = spawnSync('python3', ['-B', path.join(root, 'scripts/ci/run-s04-main-flow.py'), '--binary', binary, '--evidence', path.join(temp, 'evidence')], { cwd: root, encoding: 'utf8', timeout: 15000, env: { ...process.env, TIKU_S04_GO_BINARY: fakeGo } })
    assert.ifError(result.error); assert.equal(result.status, 23)
    assert.match(result.stderr, /S04_COMPILE_FAILURE_FIXTURE/)
    assert.doesNotMatch(result.stdout, /S04_ACTUAL_MAIN_PASS|docker/)
    assert.ok(!fs.existsSync(path.join(temp, 'evidence')), 'actual main orchestrator did not start')
  } finally {
    assert.equal(path.dirname(path.resolve(temp)), path.resolve(os.tmpdir())); assert.ok(path.basename(temp).startsWith('tikuhelper-s04-runner-contract-'))
    fs.rmSync(temp, { recursive: true, force: true })
  }
})
