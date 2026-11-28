const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises } = require('@vue/test-utils')
const { h, nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const axios = require('axios')
const { message } = require('ant-design-vue')
const { registerConsoleUI } = require('../../src/registerUI.js')
const user = require('../../src/utils/currentUser.js')
const admin = { id: 18, role: 'admin', status: 'enabled' }, member = { id: 28, role: 'user', status: 'enabled' }
const base = '/api/admin/extension-logs'
const receipts = base + '/receipts'
const event = (kind, more = {}) => ({ event: kind, time: '2026-11-18T21:20:00Z', received_at: '2026-11-18T21:20:01Z', level: 'info', mode: 'extension', version: '1.2.3', user_id: 9, ...more })
const status = (data, code = 200) => ({ status: code, data })
let calls = [], respond, defaultPolicy
const mounted = []
axios.defaults.adapter = async config => {
  const record = { path: config.url, method: config.method, params: config.params, data: config.data ? JSON.parse(config.data) : null }
  calls.push(record)
  let r
  if (config.url === '/api/user/review-rebate/availability') r = status({ enabled: false })
  else if (config.url === base + '/settings' && config.method === 'get' && defaultPolicy) r = status(defaultPolicy)
  else r = await respond(record)
  return { ...r, headers: {}, config, request: { memoryOnly: true } }
}
const Page = require('../../src/pages/ExtensionLogs/index.vue')
const Sider = require('../../src/components/SiderMenu/index.vue')
const actualRouter = require('../../src/router/router.js').default
const settle = async () => { await nextTick(); await flushPromises(); await nextTick() }
const open = async (actor = admin, menu = false) => {
  user.setStoredCurrentUser(actor)
  const history = createMemoryHistory(), routes = [{ path: '/:pathMatch(.*)*', component: { render: () => null } }]
  const router = createRouter({ history, routes })
  await router.push('/extension-logs'); await router.isReady()
  const wrapper = mount(menu ? { setup() { const { currentUser } = user.useCurrentUserDisplay(); return () => h(Sider, { currentUser: currentUser.value }) } } : Page, {
    attachTo: document.body, global: { plugins: [router, { install: registerConsoleUI }] },
  })
  mounted.push({ wrapper, history }); await settle(); return wrapper
}
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r }); return { promise, resolve } }
const by = (w, key) => w.get('[data-action="' + key + '"]')
afterEach(async () => {
  for (const x of mounted.splice(0)) { x.wrapper.unmount(); x.history.destroy() }
  user.clearStoredCurrentUser(); message.destroy(); document.body.replaceChildren()
  calls = []; respond = null; defaultPolicy = null; await settle()
})
test('administrator pages real structured events and refreshes without changing settings', async () => {
  defaultPolicy = { enabled: true, retention_days: 7, max_gb: 10 }
  let reads = 0
  respond = c => {
    assert.equal(c.method, 'get'); assert.equal(c.path, receipts); assert.equal(c.params.limit, 50)
    if (c.params.cursor) { assert.equal(c.params.cursor, 'opaque/+:cursor'); return status({ entries: [event('network.offline')], next_cursor: '' }) }
    reads++
    return status({ entries: [event(reads === 1 ? 'sync.success' : 'extension.ready', { count: 0, duration_ms: 0, raw: 'SYNTHETIC_NOT_PUBLIC' })], next_cursor: reads === 1 ? 'opaque/+:cursor' : '' })
  }
  const w = await open()
  assert.match(w.text(), /同步成功/); assert.doesNotMatch(w.text(), /SYNTHETIC_NOT_PUBLIC/)
  assert.match(w.get('tbody').text(), /0 毫秒/); assert.equal(w.get('#extension-log-retention').element.value, '7')
  await by(w, 'older').trigger('click'); await settle(); assert.match(w.get('tbody').text(), /同步成功.*网络离线/)
  await by(w, 'refresh').trigger('click'); await settle()
  assert.match(w.get('tbody').text(), /扩展就绪/); assert.doesNotMatch(w.get('tbody').text(), /网络离线|同步成功/)
  assert.equal(calls.filter(c => c.path === base + '/settings').length, 1, 'refresh does not discard or reload policy')
})
test('real form saves bounded integers, preserves a draft on failure and reports success only from the server', async () => {
  defaultPolicy = { enabled: true, retention_days: 7, max_gb: 10 }; let saves = 0
  respond = c => {
    if (c.path === receipts) return status({ entries: [], next_cursor: '' })
    assert.equal(c.path, base + '/settings'); assert.equal(c.method, 'put'); assert.deepEqual(c.data, { enabled: false, retention_days: 6, max_gb: 2 })
    return ++saves === 1 ? status({}, 503) : status(c.data)
  }
  const w = await open(); assert.match(w.text(), /暂无扩展诊断日志/)
  const days = w.get('#extension-log-retention'), size = w.get('#extension-log-size')
  await days.setValue('0'); assert.ok(by(w, 'save').element.disabled)
  await days.setValue('6'); await size.setValue('2'); await w.get('#extension-log-enabled').setValue(false); await w.get('form[aria-label="回传与保留设置"]').trigger('submit'); await settle()
  assert.match(w.text(), /保存失败/); assert.equal(days.element.value, '6')
  await w.get('form[aria-label="回传与保留设置"]').trigger('submit'); await settle(); assert.equal(saves, 2); assert.match(w.text(), /设置已保存/)
  assert.ok(by(w, 'save').element.disabled)
})
test('a failed page is not presented as empty; admin data disappears immediately on demotion and late completion stays discarded', async () => {
  const pending = deferred(); let read = 0
  defaultPolicy = { enabled: true, retention_days: 7, max_gb: 10 }
  respond = () => ++read === 1 ? status({}, 503) : pending.promise
  const w = await open(); assert.match(w.text(), /日志读取失败/); assert.doesNotMatch(w.text(), /暂无扩展诊断日志/)
  await by(w, 'refresh').trigger('click'); await settle()
  user.setStoredCurrentUser({ ...admin, role: 'user' }); await settle()
  assert.equal(w.find('table').exists(), false); assert.equal(w.find('form').exists(), false); assert.match(w.text(), /管理员/)
  pending.resolve(status({ entries: [event('sync.failed')], next_cursor: '' })); await settle()
  assert.doesNotMatch(w.text(), /同步失败/); assert.equal(calls.filter(c => c.path === receipts).length, 2)
  user.clearStoredCurrentUser(); await settle(); assert.equal(w.find('form').exists(), false)
})
test('menu exposes the lazy admin route only for admins and normal users cannot open it by URL', async () => {
  defaultPolicy = { enabled: true, retention_days: 7, max_gb: 10 }; respond = () => { throw Error('unrelated read') }
  const menu = await open(admin, true)
  assert.equal(menu.get('a[href="/extension-logs"]').text(), '扩展诊断日志')
  assert.equal(actualRouter.resolve('/extension-logs').name, 'extension-logs')
  assert.equal(calls.filter(c => c.path.startsWith(base)).length, 0, 'navigation alone must not fetch logs')
  user.setStoredCurrentUser(member); await settle()
  assert.equal(menu.find('a[href="/extension-logs"]').exists(), false)
  await actualRouter.push('/extension-logs')
  assert.equal(actualRouter.currentRoute.value.path, user.resolveHomePath(member))
  const guard = await open(member); assert.equal(guard.find('table').exists(), false)
  assert.equal(calls.filter(c => c.path.startsWith(base)).length, 0)
})
test('a pending write cannot replace the next admin’s policy or display its success, and logout clears all rows', async () => {
  const pending = deferred()
  defaultPolicy = { enabled: true, retention_days: 5, max_gb: 4 }
  respond = c => c.method === 'put' ? pending.promise : status({ entries: [event('runtime.error')], next_cursor: '' })
  const w = await open()
  await w.get('#extension-log-retention').setValue('7'); await w.get('form[aria-label="回传与保留设置"]').trigger('submit'); await settle()
  defaultPolicy = { enabled: true, retention_days: 6, max_gb: 3 }
  user.setStoredCurrentUser({ ...admin, id: 19 }); await settle()
  pending.resolve(status({ enabled: true, retention_days: 7, max_gb: 4 })); await settle()
  assert.equal(w.get('#extension-log-retention').element.value, '6'); assert.doesNotMatch(w.text(), /设置已保存。/)
  user.clearStoredCurrentUser(); await settle(); assert.equal(w.find('form').exists(), false); assert.doesNotMatch(w.text(), /运行错误/)
})
test('admin can locate one real launch by all three exact keys, paginate the selected launch, and clear or edit filters', async () => {
  const launch = '80c9548d-8a88-4e9b-abd3-22e57b9b67ac', device = 'device-45'
  const e = event('session.start', { device_id: device, launch_id: launch })
  defaultPolicy = { enabled: true, retention_days: 7, max_gb: 10 }
  respond = c => status({ entries: [e], next_cursor: c.params.cursor ? '' : 'next/opaque' })
  const w = await open()
  assert.match(w.get('tbody').text(), /本次启动（新文件）/); assert.match(w.get('tbody').text(), /device-45/); assert.match(w.get('tbody').text(), /普通浏览器扩展.*不适用/)
  assert.equal(w.get('#extension-log-enabled').element.checked, true)
  const queries = () => calls.filter(c => c.path === receipts).map(c => c.params)
  await by(w, 'locate-launch').trigger('click'); await settle()
  const select = { user_id: 9, device_id: device, launch_id: launch, mode: 'extension' }
  assert.deepEqual(queries().at(-1), { limit: 50, ...select })
  await by(w, 'older').trigger('click'); await settle()
  assert.deepEqual(queries().at(-1), { limit: 50, ...select, cursor: 'next/opaque' })
  await by(w, 'clear-filter').trigger('click'); await settle()
  assert.deepEqual(queries().at(-1), { limit: 50 }); assert.equal(w.get('#extension-log-launch').element.value, '')
  await w.get('#extension-log-user').setValue('-5'); assert.ok(by(w, 'filter').element.disabled)
  await w.get('#extension-log-user').setValue('8'); await w.get('#extension-log-device').setValue('device-88')
  await w.get('form[aria-label="日志筛选"]').trigger('submit'); await settle()
  assert.deepEqual(queries().at(-1), { limit: 50, user_id: 8, device_id: 'device-88' })
})
test('synthetic native-mode records distinguish browsers on one device without inventing a browser for exam mode', async () => {
  const launch = '15017b3c-a6d6-4bec-b348-2e70fb5325d1'
  const native = event('session.start', { mode: 'native_browser', browser_id: 'browser-2', device_id: 'same-machine', launch_id: launch })
  defaultPolicy = { enabled: true, retention_days: 7, max_gb: 10 }
  respond = () => status({ entries: [native, { ...native, mode: 'native_exam', browser_id: undefined }], next_cursor: '' })
  const w = await open()
  assert.match(w.get('tbody').text(), /原生浏览器运行.*browser-2.*原生考试运行.*不适用/)
  const queries = () => calls.filter(c => c.path === receipts).map(c => c.params)
  await w.findAll('[data-action="locate-launch"]')[0].trigger('click'); await settle()
  assert.deepEqual(queries().at(-1), { limit: 50, user_id: 9, device_id: 'same-machine', launch_id: launch, mode: 'native_browser', browser_id: 'browser-2' })
  await w.get('#extension-log-mode').setValue('native_exam'); assert.equal(w.find('#extension-log-browser').exists(), false)
  await w.get('form[aria-label="日志筛选"]').trigger('submit'); await settle()
  assert.equal(queries().at(-1).mode, 'native_exam'); assert.equal(queries().at(-1).browser_id, undefined)
})

test('receipt view defaults to event time and switching to history keeps the existing endpoint and provenance label', async () => {
  defaultPolicy = { enabled: true, retention_days: 7, max_gb: 10 }
  respond = c => {
    assert.ok(c.path === receipts || c.path === base)
    return status({ entries: [event(c.path === receipts ? 'extension.ready' : 'session.start')], next_cursor: '' })
  }
  const w = await open()
  assert.match(w.text(), /按事件发生时间排序/)
  assert.match(w.get('tbody').text(), /扩展就绪/)
  assert.equal(w.find('[data-action="export"]').exists(), true)
  await by(w, 'show-history').trigger('click'); await settle()
  assert.match(w.text(), /服务器接收顺序/)
  assert.match(w.get('tbody').text(), /本次启动/)
  assert.equal(w.find('[data-action="export"]').exists(), false)
  assert.equal(calls.filter(c => c.path === base).length, 1)
})
