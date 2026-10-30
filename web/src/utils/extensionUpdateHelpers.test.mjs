import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { extensionDatabasePayload, normalizeExtensionConfig, normalizeExtensionOverrides, validateExtensionConfig, normalizeExtensionSources, extensionStatusText, formatExtensionSize, formatExtensionTime } from './extensionUpdateHelpers.mjs'

const edgeId = 'lcmeenlnjihhjcjoakekikoaldigonac'
test('extension settings retain explicit disabled sources and validate integer intervals', () => {
  const config = normalizeExtensionConfig({ autoUpdate: true, intervalMinutes: 60, edgeId: ` ${edgeId} `, chromeId: '' })
  assert.deepEqual(config, { autoUpdate: true, intervalMinutes: 60, edgeId, chromeId: '' })
  assert.equal(validateExtensionConfig(config), '')
  assert.equal(validateExtensionConfig({ ...config, edgeId: '', chromeId: '' }), '')
  for (const intervalMinutes of [null, '', 0, -1, 1.2, 10081, NaN]) assert.match(validateExtensionConfig({ ...config, intervalMinutes }), /间隔/)
  for (const intervalMinutes of [1, 10080]) assert.equal(validateExtensionConfig({ ...config, intervalMinutes }), '')
  for (const id of ['a'.repeat(31), 'a'.repeat(33), 'A'.repeat(32), 'q'.repeat(32), 'https://example.com']) assert.match(validateExtensionConfig({ ...config, chromeId: id }), /Chrome/)
  assert.equal(normalizeExtensionConfig({ intervalMinutes: null }).intervalMinutes, null, 'clearing an interval must remain invalid rather than silently saving a default')
})

test('saving editable extension settings retains database values hidden by environment overrides', () => {
  const databaseConfig = { autoUpdate: true, intervalMinutes: 360, edgeId, chromeId: 'b'.repeat(32) }
  const form = { autoUpdate: false, intervalMinutes: 30, edgeId: 'c'.repeat(32), chromeId: '' }
  const overrides = normalizeExtensionOverrides({ autoUpdate: true, intervalMinutes: true, chromeId: true })
  assert.deepEqual(extensionDatabasePayload(form, databaseConfig, overrides), { ...databaseConfig, edgeId: form.edgeId })
  assert.deepEqual(extensionDatabasePayload(form, databaseConfig, normalizeExtensionOverrides()), form)
})

test('extension state separates disabled, empty, updating and preserved-cache failure', () => {
  const sources = normalizeExtensionSources([{ source: 'edge', configured: true, available: true, release: { version: '2.0' }, lastError: 'internal diagnostic' }])
  assert.equal(sources.length, 2)
  assert.equal(extensionStatusText(sources[0]), '检查失败，保留缓存')
  assert.equal(sources[0].release.version, '2.0')
  assert.equal(extensionStatusText(sources[1]), '未启用')
  assert.equal(extensionStatusText({ configured: true, available: false }), '尚无缓存')
  assert.equal(extensionStatusText({ configured: true, updating: true }), '正在检查')
  assert.equal(formatExtensionSize(1048576), '1.00 MB')
  assert.equal(formatExtensionSize(0), '-')
  assert.equal(formatExtensionTime('bad date'), '-')
})

test('extension update page is admin-only and does not display internal errors or server paths', async () => {
  const [router, menu, api, page, status] = await Promise.all([
    readFile(new URL('../router/router.js', import.meta.url), 'utf8'),
    readFile(new URL('../components/SiderMenu/index.vue', import.meta.url), 'utf8'),
    readFile(new URL('../api/ExtensionUpdateApi.js', import.meta.url), 'utf8'),
    readFile(new URL('../pages/ExtensionUpdates/index.vue', import.meta.url), 'utf8'),
    readFile(new URL('../pages/ExtensionUpdates/ExtensionSourceStatus.vue', import.meta.url), 'utf8'),
  ])
  assert.match(router, /ADMIN_ONLY_ROUTE_NAMES[\s\S]*'extension-updates'/)
  assert.match(menu.split('const USER_MENU_ITEMS')[0], /key: 'extension-updates', label: '扩展更新'/)
  assert.doesNotMatch(menu.split('const USER_MENU_ITEMS')[1], /extension-updates/)
  assert.match(api, /extensions\/config/)
  assert.match(api, /method: 'put'/)
  assert.match(api, /method: 'post', timeout: 120_000/)
  assert.match(page, /busy\.value \|\| dirty\.value \|\| !loaded\.value/)
  assert.doesNotMatch(status, /\{\{[^}]*lastError|:message="source\.lastError|downloadUrl|cacheDir|apiKey/)
})
