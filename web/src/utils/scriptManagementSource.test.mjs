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
  // 两个入口拆成两个路由、两个 key（2026-11-14 用户实测报出的缺陷驱动）：
  //   scripts（viewMode='self'）→ 我的脚本
  //   script-admin（viewMode='admin'）→ 脚本管理
  assert.match(router, /path: 'scripts',[\s\S]*?name: 'scripts',[\s\S]*?component: ScriptManagement,[\s\S]*?meta: \{ viewMode: 'self' \}/)
  assert.match(router, /path: 'script-admin',[\s\S]*?name: 'script-admin',[\s\S]*?component: ScriptManagement,[\s\S]*?meta: \{ viewMode: 'admin' \}/)
  // `scripts`（我的脚本）对普通用户开放，不能进管理路由名单；
  // 而 `script-admin`（含上传 EXE 与计费政策）**必须**在里面。
  const adminOnlyBlock = router.match(/const ADMIN_ONLY_ROUTE_NAMES[\s\S]*?\]\)/)?.[0] || ''
  assert.doesNotMatch(adminOnlyBlock, /'scripts'/)
  assert.match(adminOnlyBlock, /'script-admin'/)
  assert.match(menu, /\{ key: 'script-admin', label: '脚本管理'/)
  assert.match(menu, /\{ key: 'scripts', label: '我的脚本'/)
  assert.match(api, /url: `\/api\/scripts`/)
  assert.match(api, /url: `\/api\/script-versions\/upload`/)
  assert.match(page, /accept="\.exe,application\/vnd\.microsoft\.portable-executable"/)
  assert.match(page, /显示全部脚本/)
  assert.match(page, /预置参数/)
  assert.match(page, /冻结金模式/)
  assert.match(page, /一次性激活码/)
  assert.doesNotMatch(page, /独立答题倍率（X）/)
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
    url: '/api/scripts',
    method: 'post',
    data: payload,
  })
  assert.equal(Object.hasOwn(invoke(payload), 'headers'), false)
})

test('script deletion retries only after explicit user action and carries the confirmed run as a decimal query string', async () => {
  const api = await read('api/TikuComponentApi.js')
  const start = api.indexOf('export function deleteScript(')
  const end = api.indexOf('\n}', start)
  assert.ok(start >= 0 && end > start)
  const declaration = api.slice(start, end + 2).replace('export ', '')
  const invoke = new Function('request', declaration + '; return deleteScript')(config => config)
  const base = { errorFeedback: 'local', retryPolicy: 'single', url: '/api/scripts/7', method: 'delete' }
  assert.deepEqual(invoke({ id: 7 }), base)
  assert.deepEqual(invoke({ id: 7, confirmActiveSessionId: '9007199254740993' }), {
    ...base, params: { confirmActiveSessionId: '9007199254740993' },
  })
  for (const invalid of [42, true, null, '', '00', '12&confirmActive=true']) {
    assert.throws(() => invoke({ id: 7, confirmActiveSessionId: invalid }), TypeError)
  }
})


test('activation purchase never sends the code or a wallet mutation without its idempotency key', async () => {
  const api = await read('api/TikuComponentApi.js')
  const start = api.indexOf('export function purchaseScriptActivationCode(')
  const end = api.indexOf('\n}', start)
  assert.ok(start >= 0 && end > start)
  const declaration = api.slice(start, end + 2).replace('export ', '')
  const invoke = new Function('request', declaration + '; return purchaseScriptActivationCode')(config => config)
  const key = 'script-activation-purchase-' + '1'.repeat(32)
  assert.deepEqual(invoke(key), {
    errorFeedback: 'local',
    retryPolicy: 'single',
    headers: { 'Idempotency-Key': key },
    url: '/api/script-activation-codes/purchase',
    method: 'post',
  })
  assert.deepEqual(invoke(key, '12.345678').data, { quotedPrice: '12.345678' })
})
