const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises, config } = require('@vue/test-utils'), { nextTick } = require('vue'), Antd = require('ant-design-vue'), axios = require('axios')
// The shared DOM helper pre-registers these for legacy fixtures. This suite uses
// precisely the production registration below, including the console primitives.
for (const name of ['ConsolePageHeading', 'ConsolePanel', 'ConsoleSummaryGrid', 'ConsoleTableShell']) delete config.global.components[name]
const users = require(path.join(root, 'src/utils/currentUser.js'))
const { registerConsoleUI } = require(path.join(root, 'src/registerUI.js'))
const calls = [], downloads = [], revoked = [], roots = []
const original = { adapter: axios.defaults.adapter, create: window.URL.createObjectURL, revoke: window.URL.revokeObjectURL, click: window.HTMLAnchorElement.prototype.click }
let rows = [], held = null, rejectDownload = false, holdList = false, sequence = 0, submitStatus = 'succeeded'
window.URL.createObjectURL = blob => { downloads.push({ blob }); return 'blob:transfer-result' }
window.URL.revokeObjectURL = value => revoked.push(value)
window.HTMLAnchorElement.prototype.click = function () { downloads.at(-1).filename = this.download }
axios.defaults.adapter = config => {
  const method = String(config.method || 'get').toLowerCase(), url = config.url, body = typeof config.data === 'string' ? JSON.parse(config.data) : config.data
  calls.push({ method, url, body, params: config.params, responseType: config.responseType })
  let status = 200, data
  if (url === '/api/export/tasks' && method === 'get') {
    data = { data: rows.filter(row => row.resource === config.params.resource), total: rows.length, page: 1, pageSize: 10 }
    if (holdList) return new Promise(resolve => { held = () => resolve({ status, data, headers: {}, config }) })
  } else if ((url === '/api/export/tasks' || url === '/api/import/tasks') && method === 'post') {
    const importing = url.includes('/import/'), resource = importing ? body.get('resource') : body.resource
    const row = { taskId: 'exp-native-' + (++sequence), resource, type: importing ? 'import' : 'export', format: importing ? 'json' : body.format, status: submitStatus, masked: importing ? false : body.masked, createdAt: '2026-11-21T12:00:00Z', succeeded: 2, downloadUrl: submitStatus === 'succeeded' ? '/api/export/tasks/exp-native-' + sequence + '/download' : '' }
    rows.unshift(row); status = 202; data = row
  } else if (/^\/api\/export\/tasks\/exp-native-\d+\/download$/.test(url) && method === 'get') {
    status = rejectDownload ? 410 : 200; data = new Blob([rejectDownload ? '{"message":"expired"}' : 'id\n42\n'], { type: 'text/csv' })
  } else if (url === '/api/import/template') data = new Blob(['lookup_key,address'])
  else if (url === '/api/admin/user-bans/import/preview' && method === 'post') data = { previewId: 'prv-native-ban-1', total: 1, targets: [{ row: 2, userId: 42, username: 'server-confirmed-account', nickname: '真实昵称', role: 'user', status: 'enabled', phone: '138****5678', createdAt: '2026-11-01T00:00:00Z', reason: '违规原因' }] }
  else if (url === '/api/admin/user-bans/import/prv-native-ban-1/confirm' && method === 'post') data = { succeeded: 1 }
  else if (url === '/api/admin/user-bans/import/prv-native-ban-1' && method === 'delete') { status = 204; data = {} }
  else throw new Error('Unexpected API operation: ' + method + ' ' + url)
  return Promise.resolve({ status, data, headers: {}, config, request: { memoryOnly: true } })
}
const Component = require(path.join(root, 'src/components/AdminDataTransfer.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function create(props = {}, account = { id: 951, role: 'admin', username: 'transfer-admin' }) {
  if (account) users.setStoredCurrentUser(account); else users.clearStoredCurrentUser()
  const w = mount(Component, { props: { resource: 'logs', title: '操作日志', ...props }, attachTo: document.body, global: { plugins: [{ install: registerConsoleUI }] } }); roots.push(w); await settle(); return w
}
async function click(node) { assert.ok(node, 'native control exists'); node.click(); await settle() }
afterEach(async () => { for (const w of roots.splice(0)) w.unmount(); holdList = false; held?.(); held = null; await settle(); document.body.replaceChildren(); rows = []; calls.length = 0; downloads.length = 0; revoked.length = 0; rejectDownload = false; submitStatus = 'succeeded'; Antd.message.destroy() })
after(() => { axios.defaults.adapter = original.adapter; window.URL.createObjectURL = original.create; window.URL.revokeObjectURL = original.revoke; window.HTMLAnchorElement.prototype.click = original.click })
for (const resource of ['logs', 'billing', 'ip_locations', 'phone_locations', 'wechat_bindings', 'phone_bindings', 'ocr_cache', 'gifts', 'blacklist', 'whitelist', 'user_bans', 'devices', 'sensitive_images', 'archived_users', 'recharge_orders', 'sensitive_words', 'users']) test(resource + ': native click → HTTP submit → visible history/time → blob download', async () => {
  const w = await create({ resource, formats: resource === 'sensitive_images' ? ['json', 'xlsx'] : ['csv', 'json'] })
  assert.deepEqual(calls[0].params, { resource, page: 1, pageSize: 10 }); await click(w.element.querySelector('[data-transfer-open]'))
  const submit = document.querySelector('[data-transfer-submit]'); assert.ok(submit?.closest('[role="dialog"]')); await click(submit)
  assert.deepEqual(calls.find(call => call.method === 'post').body, { resource, format: resource === 'sensitive_images' ? 'json' : 'csv', masked: true })
  assert.match(w.element.querySelector('[data-transfer-task]').textContent, /2026/); assert.match(w.element.querySelector('[data-transfer-task]').textContent, /成功.*2/)
  await click([...document.querySelectorAll('.ant-modal button')].find(button => button.textContent.includes('关闭，查看任务列表')))
  assert.equal(w.vm.visible, false); assert.ok(w.element.querySelector('[data-transfer-task]')); await click(w.element.querySelector('[data-transfer-download]'))
  assert.equal(calls.at(-1).responseType, 'blob'); assert.equal(downloads.length, 1); assert.equal(await downloads[0].blob.text(), 'id\n42\n'); assert.match(downloads[0].filename, new RegExp('^' + resource + '-')); assert.deepEqual(revoked, ['blob:transfer-result'])
  w.unmount(); const reopened = await create({ resource }); assert.ok(reopened.element.querySelector('[data-transfer-task]'), 'history restored after reentry')
})
for (const resource of ['ip_locations', 'phone_locations', 'blacklist', 'whitelist', 'ocr_cache', 'sensitive_images']) test(resource + ': native file input sends multipart import and emits completed reload only once', async () => {
  const w = await create({ resource, importable: true }); await click(w.element.querySelector('[data-transfer-open]'))
  const input = document.querySelector('.ant-modal input[type="file"]'); assert.ok(input)
  const file = new File(['[{"lookup_key":"203.0.113.1","address":"测试"}]'], 'locations.json', { type: 'application/json' })
  Object.defineProperty(input, 'files', { configurable: true, value: [file] }); input.dispatchEvent(new Event('change', { bubbles: true })); await settle()
  const post = calls.find(call => call.url === '/api/import/tasks'); assert.equal(post.body.get('resource'), resource); assert.equal(post.body.get('file').name, 'locations.json')
  assert.equal(w.emitted('imported').length, 1); await click(w.element.querySelector('[data-transfer-refresh]')); assert.equal(w.emitted('imported').length, 1)
})
test('native format selection and masking checkbox preserve explicit JSON unmasked export intent', async () => {
  const w = await create(); await click(w.element.querySelector('[data-transfer-open]'))
  await click(document.querySelector('.ant-modal input[type="checkbox"]'))
  const selector = document.querySelector('.ant-modal .ant-select-selector')
  selector.dispatchEvent(new MouseEvent('mousedown', { bubbles: true })); await settle()
  const option = [...document.querySelectorAll('.ant-select-item-option')].find(node => node.textContent === 'JSON'); await click(option)
  await click(document.querySelector('[data-transfer-submit]')); assert.deepEqual(calls.find(call => call.method === 'post').body, { resource: 'logs', format: 'json', masked: false })
})
test('equivalent inline formats on parent rerender do not close the modal or erase task history', async () => {
  const w = await create({ formats: ['csv', 'json'] }); await click(w.element.querySelector('[data-transfer-open]'))
  const before = calls.length; await w.setProps({ formats: ['csv', 'json'] }); await settle()
  assert.equal(w.vm.visible, true); assert.equal(calls.length, before)
})
test('queued task remains visible after close and polling publishes completion with download', async () => {
  submitStatus = 'pending'; const w = await create(); await click(w.element.querySelector('[data-transfer-open]')); await click(document.querySelector('[data-transfer-submit]'))
  assert.match(w.element.querySelector('[data-transfer-task]').textContent, /排队/); assert.equal(w.find('[data-transfer-download]').exists(), false)
  await click([...document.querySelectorAll('.ant-modal button')].find(button => button.textContent.includes('关闭，查看任务列表')))
  rows = [{ ...rows[0], status: 'succeeded', downloadUrl: '/api/export/tasks/' + rows[0].taskId + '/download' }]
  await new Promise(resolve => setTimeout(resolve, 1650)); await settle()
  assert.ok(calls.filter(call => call.url === '/api/export/tasks' && call.method === 'get').length >= 3)
  assert.match(w.element.querySelector('[data-transfer-task]').textContent, /成功.*2/); assert.ok(w.find('[data-transfer-download]').exists())
})
test('HTTP410 never downloads an error file and preserves task plus visible persistent failure', async () => {
  const w = await create(); await click(w.element.querySelector('[data-transfer-open]')); await click(document.querySelector('[data-transfer-submit]'))
  rejectDownload = true; await click(w.element.querySelector('[data-transfer-download]')); assert.equal(downloads.length, 0); assert.ok(w.findComponent({ name: 'RequestFailureAlert' }).exists()); assert.ok(w.element.querySelector('[data-transfer-task]'))
})
test('ordinary/unknown identity reads nothing; retired native controls cannot submit or publish old task history', async () => {
  const normal = await create({}, { id: 4, role: 'user' }); assert.equal(calls.length, 0); assert.equal(normal.find('[data-transfer-open]').exists(), false); normal.unmount()
  const unknown = await create({}, null); assert.equal(calls.length, 0); unknown.unmount()
  const w = await create(), stale = w.vm.entry, oldButton = w.element.querySelector('[data-transfer-open]')
  holdList = true; const pending = stale.refresh(), release = held; users.setStoredCurrentUser({ id: 952, role: 'admin', username: 'new-admin' }); await nextTick(); holdList = false
  release(); await pending; await settle(); assert.equal(w.vm.tasks.length, 0)
  const before = calls.length; oldButton.click(); await stale.export(); assert.equal(calls.length, before); assert.equal(w.vm.visible, false)
  const previous = w.vm.entry; await w.setProps({ resource: 'phone_locations' }); await settle(); const scoped = calls.length; await previous.export(); assert.equal(calls.length, scoped)
})

test('account ban native file input → HTTP server preview → account details → checkbox and native confirmation', async () => {
  users.setStoredCurrentUser({ id: 951, role: 'admin', username: 'transfer-admin' })
  const BanImport = require(path.join(root, 'src/components/UserBanImport.vue'))
  const w = mount(BanImport, { attachTo: document.body, global: { plugins: [{ install: registerConsoleUI }] } }); roots.push(w)
  const input = w.element.querySelector('input[type="file"]')
  Object.defineProperty(input, 'files', { configurable: true, value: [new File(['user_id,reason\n42,违规原因'], 'bans.csv')] })
  input.dispatchEvent(new Event('change', { bubbles: true })); await settle()
  assert.match(document.body.textContent, /server-confirmed-account/); assert.match(document.body.textContent, /真实昵称/)
  const confirm = document.querySelector('.ant-modal-footer .ant-btn-primary'); assert.ok(confirm.disabled)
  assert.equal(calls.filter(call => call.url.endsWith('/confirm')).length, 0)
  await click(document.querySelector('.ant-modal input[type="checkbox"]')); await click(confirm)
  const submitted = calls.find(call => call.url.endsWith('/confirm')); assert.deepEqual(submitted.body, { confirmed: true })
  assert.equal(w.emitted('imported').length, 1)
})

test('a banned administrator cannot retain transfer history or revive an old export callback after unban', async () => {
  const w = await create({}, { id: 951, role: 'admin', status: 'enabled' })
  const stale = w.vm.entry
  holdList = true; const pending = stale.refresh(), release = held
  users.setStoredCurrentUser({ id: 951, role: 'admin', status: 'banned' }); await nextTick()
  holdList = false; release(); await pending; await settle()
  assert.equal(w.vm.canManage, false); assert.equal(w.find('[data-transfer-open]').exists(), false)
  assert.equal(w.vm.tasks.length, 0)
  const before = calls.length; await stale.export(); assert.equal(calls.length, before)
  users.setStoredCurrentUser({ id: 951, role: 'admin', status: 'enabled' }); await settle()
  const restored = calls.length; await stale.export(); assert.equal(calls.length, restored)
})

test('a banned administrator loses the account-import confirmation and its old native controls', async () => {
  users.setStoredCurrentUser({ id: 951, role: 'admin', status: 'enabled' })
  const BanImport = require(path.join(root, 'src/components/UserBanImport.vue'))
  const w = mount(BanImport, { attachTo: document.body, global: { plugins: [{ install: registerConsoleUI }] } }); roots.push(w)
  const input = w.element.querySelector('input[type="file"]')
  Object.defineProperty(input, 'files', { configurable: true, value: [new File(['user_id,reason\n42,违规原因'], 'bans.csv')] })
  input.dispatchEvent(new Event('change', { bubbles: true })); await settle()
  const stale = w.vm.entry, confirm = document.querySelector('.ant-modal-footer .ant-btn-primary')
  users.setStoredCurrentUser({ id: 951, role: 'admin', status: 'banned' }); await settle()
  assert.equal(w.vm.canManage, false); assert.equal(w.vm.previewId, ''); assert.equal(w.vm.visible, false)
  const before = calls.length; stale.checked({ target: { checked: true } }); await stale.confirm(); confirm.click(); await settle()
  assert.equal(calls.length, before)
  users.setStoredCurrentUser({ id: 951, role: 'admin', status: 'enabled' }); await settle()
  await stale.confirm(); assert.equal(calls.length, before)
})
