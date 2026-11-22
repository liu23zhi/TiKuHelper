import test from 'node:test'
import assert from 'node:assert/strict'
import {
  buildScriptCommand,
  buildScriptShortCommand,
  buildScriptDownloadURL,
  buildScriptListParams,
  buildScriptSavePayload,
  formatPresetParams,
  normalizeScriptRecord,
  parsePresetParams,
} from './scriptManagementHelpers.mjs'

test('script address prefers the shortest wildcard hostname', () => {
  assert.equal(
    buildScriptDownloadURL({ slug: 'a', baseDomain: 'Example.COM', useWildcardDomain: true }),
    'https://a.example.com',
  )
})

test('script address keeps a path when wildcard domains are unavailable', () => {
  assert.equal(
    buildScriptDownloadURL({ slug: 'a', baseDomain: 'a.com', useWildcardDomain: false }),
    'https://a.a.com/a',
  )
})

test('preset parameters preserve values containing equals signs', () => {
  assert.deepEqual(parsePresetParams('key=abc=123\ncourseId=42'), { key: 'abc=123', courseId: '42' })
  assert.equal(formatPresetParams({ key: 'abc=123', courseId: 42 }), 'key=abc=123\ncourseId=42')
})

test('script list scope only requests all rows for an administrator', () => {
  assert.deepEqual(buildScriptListParams(true, true), { scope: 'all' })
  assert.deepEqual(buildScriptListParams(true, false), { scope: 'all' })
  assert.deepEqual(buildScriptListParams(false, true), { scope: 'self' })
})

test('short commands require an active unexpired server authorization with a safe address', () => {
  const grant = { address: 'https://a.muioi.com', command: 'powershell -c "iex(irm https://a.muioi.com)"', status: 'active', expiresAt: '2026-11-21T10:00:00Z' }
  const now = Date.parse('2026-11-21T09:59:00Z')
  assert.equal(buildScriptShortCommand({ shortAuthorization: grant }, now), grant.command)
  for (const status of ['used', 'disabled', 'expired']) assert.equal(buildScriptShortCommand({ shortAuthorization: { ...grant, status } }, now), '')
  assert.equal(buildScriptShortCommand({ shortAuthorization: grant }, Date.parse(grant.expiresAt)), '')
  assert.equal(buildScriptShortCommand({ shortAuthorization: { ...grant, command: 'powershell -c malicious' } }, now), '')
  assert.equal(buildScriptShortCommand({ shortAuthorization: { ...grant, address: 'https://a.muioi.com);calc;(' } }, now), '')
})

test('save payload includes server parameter shape but never a client-generated executable command', () => {
  const payload = buildScriptSavePayload({
    name: '助手', versionId: 7, slug: 'a', baseDomain: 'a.com',
    useWildcardDomain: false, presetParams: 'key=secret', billingUseTikuPrice: false, priceMultiplier: 1.25,
  })
  assert.equal(payload.versionId, 7)
  assert.equal(Object.hasOwn(payload, 'instruction'), false)
  assert.equal(Object.hasOwn(payload, 'command'), false)
  assert.deepEqual(payload.parameters, { key: 'secret' })
  assert.equal(payload.billingUseTikuPrice, false)
  assert.equal(payload.priceMultiplier, 1.25)
  assert.equal(payload.answerPriceMultiplier, 1.25)
})

test('script billing defaults to and displays an independent multiplier', () => {
  const row = normalizeScriptRecord({ billingEnabled: true, billingUseTikuPrice: false })
  assert.equal(row.priceMultiplier, 1)
})

test('backend instruction and parameters normalize for existing rows', () => {
  const row = normalizeScriptRecord({ instruction: 'powershell -c test', parameters: { key: 'secret' }, shortCode: 'abc123', versionId: 2 })
  assert.equal(row.instruction, 'powershell -c test')
  assert.deepEqual(row.presetParams, { key: 'secret' })
  assert.equal(row.shortCode, 'abc123')
  assert.equal(row.versionId, 2)
})

test('script command never interpolates shell syntax or trusts raw commands', () => {
  for (const domain of ["example.com');calc;('", 'example.com"$(cmd)', 'example.com%27', 'example.com:443', 'example.com@other.test']) {
    assert.equal(buildScriptDownloadURL({ slug: 'a', baseDomain: domain }), '')
    assert.equal(buildScriptCommand({ slug: 'a', baseDomain: domain }), '')
  }
  assert.equal(buildScriptCommand({ downloadUrl: "https://example.com/');calc;('", instruction: 'powershell -c malicious' }), '')
})

test('saved script accepts only server launcher shape and ignores a legacy executable field', () => {
  assert.equal(buildScriptCommand({ command: 'powershell -EncodedCommand ZgA=' }), 'powershell -EncodedCommand ZgA=')
  assert.equal(buildScriptCommand({ instruction: 'powershell -EncodedCommand ZgA=' }), '')
  assert.equal(buildScriptCommand({ command: 'powershell -EncodedCommand ZgA=;calc' }), '')
  assert.equal(buildScriptCommand({ command: 'powershell -c raw' }), '')
})

test('compact authorized commands remain copyable without accepting arbitrary PowerShell', () => {
  const command = `powershell -c "iex(irm https://uuid.example.test -Headers @{'X-Script-Launcher'='${'a'.repeat(43)}'} -MaximumRedirection 0 -TimeoutSec 15)"`
  assert.equal(buildScriptCommand({ command }), command)
  assert.equal(buildScriptCommand({ command: command.replace('example.test', "example.test');calc;('") }), '')
  assert.equal(buildScriptCommand({ command: command.replace('https:', 'http:') }), '')
  assert.equal(buildScriptCommand({ command: command.replace(' -MaximumRedirection 0', '') }), '')
})

test('an authorized short command with no expiry stays copyable until revoked', () => {
  const grant = { address: 'https://random.example.test', command: 'powershell -c "iex(irm https://random.example.test)"', status: 'active', expiresAt: null }
  assert.equal(buildScriptShortCommand({ shortAuthorization: grant }), grant.command)
  assert.equal(buildScriptShortCommand({ shortAuthorization: { ...grant, status: 'disabled' } }), '')
})

test('commands paused between authorized windows remain copyable without resetting shared use limits', () => {
  const grant = {
    address: 'https://a.muioi.com', command: 'powershell -c "iex(irm https://a.muioi.com)"', status: 'paused',
    startsAt: '2026-11-21T08:00:00Z', expiresAt: '2026-11-21T12:00:00Z', maxUses: 3, usedCount: 2,
    windows: [{ startsAt: '2026-11-21T08:00:00Z', expiresAt: '2026-11-21T09:00:00Z' }, { startsAt: '2026-11-21T11:00:00Z', expiresAt: '2026-11-21T12:00:00Z' }],
  }
  assert.equal(buildScriptShortCommand({ shortAuthorization: grant }, Date.parse('2026-11-21T10:00:00Z')), grant.command)
  assert.equal(buildScriptShortCommand({ shortAuthorization: { ...grant, usedCount: 3 } }, Date.parse('2026-11-21T11:30:00Z')), '')
  assert.equal(buildScriptShortCommand({ shortAuthorization: grant }, Date.parse('2026-11-21T12:00:00Z')), '')
})

test('consumed activation script qualification cannot reconstruct either launcher command', () => {
  const address = 'https://a.example.test'
  const command = `powershell -c "iex(irm ${address} -MaximumRedirection 0 -TimeoutSec 15)"`
  const row = { command, billingMode: 'activation_code', runConsumedAt: '2026-11-21T09:00:00Z', enabled: true, shortAuthorization: { address, command, status: 'active', expiresAt: null } }
  assert.equal(buildScriptCommand(row), '')
  assert.equal(buildScriptShortCommand(row), '')
  assert.equal(buildScriptCommand({ ...row, billingMode: 'escrow' }), command)
  assert.equal(buildScriptCommand({ ...row, runConsumedAt: null }), command)
})

test('bare UUID launch commands and guarded custom short commands are accepted only in their exact server shape', () => {
  const command = 'powershell -c "iex(irm https://123e4567-e89b-12d3-a456-426614174000.example.test -MaximumRedirection 0 -TimeoutSec 15)"'
  assert.equal(buildScriptCommand({ command }), command)
  assert.equal(buildScriptCommand({ command: command.replace(' -TimeoutSec 15', '') }), '')
  assert.equal(buildScriptCommand({ command: command.replace('example.test', 'example.test;calc') }), '')
  const grant = { address: 'https://a.example.test', status: 'active', command: command.replace('123e4567-e89b-12d3-a456-426614174000.example.test', 'a.example.test'), expiresAt: null }
  assert.equal(buildScriptShortCommand({ shortAuthorization: grant }), grant.command)
  assert.equal(buildScriptShortCommand({ shortAuthorization: { ...grant, command: grant.command.replace('a.example.test', 'b.example.test') } }), '')
})
