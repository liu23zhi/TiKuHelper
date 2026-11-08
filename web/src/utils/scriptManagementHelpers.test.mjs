import test from 'node:test'
import assert from 'node:assert/strict'
import {
  buildScriptCommand,
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
  assert.deepEqual(buildScriptListParams(true, false), { scope: 'self' })
  assert.deepEqual(buildScriptListParams(false, true), { scope: 'self' })
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
