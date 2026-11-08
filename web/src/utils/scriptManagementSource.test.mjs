import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const testDir = dirname(fileURLToPath(import.meta.url))
const read = (relative) => readFile(resolve(testDir, '..', relative), 'utf8')

test('script management is routed for both scopes and exposes admin upload controls', async () => {
  const [router, menu, api, page] = await Promise.all([
    read('router/router.js'),
    read('components/SiderMenu/index.vue'),
    read('api/TikuComponentApi.js'),
    read('pages/ScriptManagement/index.vue'),
  ])
  assert.match(router, /path: 'scripts',[\s\S]*name: 'scripts',[\s\S]*component: ScriptManagement/)
  assert.doesNotMatch(router.match(/const ADMIN_ONLY_ROUTE_NAMES[\s\S]*?\]\)/)?.[0] || '', /'scripts'/)
  assert.match(menu, /\{ key: 'scripts', label: '脚本管理'/)
  assert.match(menu, /\{ key: 'scripts', label: '我的脚本'/)
  assert.match(api, /url: `\/adapter-service\/scripts`/)
  assert.match(api, /url: `\/adapter-service\/script-versions\/upload`/)
  assert.match(page, /accept="\.exe,application\/vnd\.microsoft\.portable-executable"/)
  assert.match(page, /显示全部脚本/)
  assert.match(page, /预置参数/)
  assert.match(page, /跟随题库计费/)
  assert.match(page, /独立答题倍率（X）/)
  assert.match(page, /发布为最新版本/)
  assert.match(page, /PowerShell \+ \.NET WebClient/)
})

test('paid script creation sends exactly one stable idempotency header without changing the payload', async () => {
  const api = await read('api/TikuComponentApi.js')
  const start = api.indexOf('export function createScript(')
  const end = api.indexOf('\n}', start)
  assert.ok(start >= 0 && end > start)
  const declaration = api.slice(start, end + 2).replace('export ', '')
  const invoke = new Function('request', declaration + '; return createScript')(config => config)
  const payload = { name: 'fixture script' }, key = 'script-create-' + '1'.repeat(32)
  assert.deepEqual(invoke(payload, key), {
    errorFeedback: 'local',
    retryPolicy: 'single',
    headers: { 'Idempotency-Key': key },
    url: '/adapter-service/scripts',
    method: 'post',
    data: payload,
  })
  assert.equal(Object.hasOwn(invoke(payload), 'headers'), false)
})
