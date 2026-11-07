import test from 'node:test'
import assert from 'node:assert/strict'
import {
  buildScriptCommand,
  buildScriptListParams,
  buildScriptSavePayload,
  formatPresetParams,
  normalizeScriptRecord,
  parsePresetParams,
} from './scriptManagementHelpers.mjs'

test('script command prefers the shortest wildcard hostname', () => {
  assert.equal(
    buildScriptCommand({ slug: 'a', baseDomain: 'Example.COM', useWildcardDomain: true }),
    `powershell -c "iex (New-Object Net.WebClient).DownloadString('https://a.example.com')"`,
  )
})

test('script command keeps a path when wildcard domains are unavailable', () => {
  assert.equal(
    buildScriptCommand({ slug: 'a', baseDomain: 'a.com', useWildcardDomain: false }),
    `powershell -c "iex (New-Object Net.WebClient).DownloadString('https://a.a.com/a')"`,
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

test('save payload includes generated instruction and server parameter shape', () => {
  const payload = buildScriptSavePayload({
    name: '助手', versionId: 7, slug: 'a', baseDomain: 'a.com',
    useWildcardDomain: false, presetParams: 'key=secret', billingUseTikuPrice: false, priceMultiplier: 1.25,
  })
  assert.equal(payload.versionId, 7)
  assert.equal(payload.instruction, `powershell -c "iex (New-Object Net.WebClient).DownloadString('https://a.a.com/a')"`)
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
