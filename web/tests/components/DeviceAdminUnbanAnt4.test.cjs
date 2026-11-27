/* Real Ant4, DeviceAdmin and Axios request client; the adapter supplies only isolated fixtures. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
for (const key of ['DOMParser', 'HTMLButtonElement', 'HTMLDivElement', 'HTMLVideoElement', 'HTMLAudioElement', 'CustomEvent']) globalThis[key] = window[key]
window.scrollTo = () => {}
const axios = require('axios')
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const Swal = require('sweetalert2')
const { clearStoredCurrentUser, setStoredCurrentUser } = require(path.join(web, 'src/utils/currentUser.js'))

const ADMIN = { id: 78101, username: 'synthetic-admin', role: 'admin' }
const LATER_ADMIN = { id: 78102, username: 'synthetic-later-admin', role: 'admin' }
const DEVICE = 'exe:synthetic-shared-device'
const OLD = {
  id: 7001, userId: 7301, username: 'fixture-first-owner', deviceId: DEVICE,
  status: 'banned', banned: true, revokedReason: 'device_banned', revokedAt: '2026-11-28T01:05:00Z',
  banEntryId: 9001, banRevision: 'opaque-revision/+',
}
const SELECTED = { ...OLD, id: 7002, userId: 7302, username: 'fixture-selected-owner' }
const ORPHAN = { entryId: 9002, deviceId: 'exe:synthetic-orphan', reason: 'fixture no binding' }

const calls = []
let rows = [OLD, SELECTED]
let reply = { deviceId: DEVICE, deleted: 1 }
let wrapper
const create = axios.create
axios.create = config => create({ ...config, adapter: async config => {
  calls.push(config)
  let data
  if (config.method === 'get' && config.url === '/api/admin/devices') {
    data = { data: rows, orphanBans: [ORPHAN], total: rows.length, active: 0, removed: 0, blocked: rows.length, revocationSupported: true }
  } else if (config.method === 'delete' && config.url.startsWith('/api/admin/device-bans/')) {
    data = reply
  } else {
    assert.fail('Unexpected synthetic request: ' + config.method + ' ' + config.url)
  }
  return { data, status: 200, statusText: 'OK', config, headers: {} }
} })
let Page, unbanAdminDevice
try {
  Page = require(path.join(web, 'src/pages/DeviceAdmin/index.vue'))
  ;({ unbanAdminDevice } = require(path.join(web, 'src/api/DeviceAdminApi.js')))
} finally { axios.create = create }

const writes = () => calls.filter(call => call.method === 'delete')
const reads = () => calls.filter(call => call.method === 'get')
const settle = async () => { await nextTick(); await flushPromises(); await nextTick() }
const buttonByText = (root, text) => [...root.querySelectorAll('button')].find(button => button.textContent.replace(/\s/g, '') === text)
const dialog = () => [...document.body.querySelectorAll('.ant-modal-wrap[role="dialog"]')]
  .find(element => element.style.display !== 'none' && element.querySelector('.ant-modal-title')?.textContent === '解除设备全局拉黑') || null

async function page(actor = ADMIN) {
  setStoredCurrentUser(actor)
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/device-admin', component: Page }, { path: '/other', component: { render: () => null } },
  ] })
  await router.push('/device-admin')
  await router.isReady()
  wrapper = mount(Page, { attachTo: document.body, props: { embedded: true }, global: { plugins: [Antd, router] } })
  await settle()
  return router
}

async function open(label) {
  const row = [...wrapper.element.querySelectorAll('tbody tr')].find(element => element.textContent.includes(label))
  assert.ok(row, 'expected displayed account/device row ' + label)
  const action = buttonByText(row, '解封')
  assert.ok(action, 'displayed row owns the unban action')
  action.click()
  await settle()
  const modal = dialog()
  assert.ok(modal, 'unban opens an accessible confirmation with the target identity')
  return modal
}

async function chooseRestore(modal) {
  const checkbox = modal.querySelector('input[type=checkbox]')
  assert.ok(checkbox, 'an eligible binding offers an explicit restore choice')
  assert.equal(checkbox.checked, false, 'restore is never preselected')
  assert.match(checkbox.closest('label').textContent, /恢复.*原绑定/)
  checkbox.click()
  await settle()
  assert.equal(checkbox.checked, true)
}

const primary = modal => modal.querySelector('.ant-modal-footer button.ant-btn-primary')
async function confirm(modal) {
  const button = primary(modal)
  assert.ok(button)
  button.click()
  await settle()
}

afterEach(async () => {
  Swal.close()
  if (wrapper?.exists()) wrapper.unmount()
  Antd.Modal.destroyAll()
  Antd.message.destroy()
  clearStoredCurrentUser()
  await settle()
  document.body.replaceChildren()
  calls.length = 0
  rows = [OLD, SELECTED]
  reply = { deviceId: DEVICE, deleted: 1 }
})

test('same-device accounts: explicit confirmation sends exactly the chosen account binding and current ban', async () => {
  await page()
  reply = { deviceId: DEVICE, deleted: 1, restore: { userId: 7302, bindingId: 7002, status: 'restored' } }
  const modal = await open('fixture-selected-owner')
  assert.match(modal.textContent, /fixture-selected-owner.*7302/s)
  assert.match(modal.textContent, /exe:synthetic-shared-device/)
  assert.doesNotMatch(modal.textContent, /fixture-first-owner|opaque-revision/)
  await chooseRestore(modal)
  assert.equal(writes().length, 0, 'choosing the option does not dispatch a mutation')
  await confirm(modal)
  assert.equal(writes().length, 1)
  const request = writes()[0]
  assert.equal(request.url, '/api/admin/device-bans/exe%3Asynthetic-shared-device')
  assert.deepEqual(request.params, { restoreUserId: 7302, restoreBindingId: 7002, banEntryId: 9001, banRevision: 'opaque-revision/+' })
  assert.deepEqual(Object.fromEntries(new URL(axios.getUri(request), 'http://fixture.invalid').searchParams),
    { restoreUserId: '7302', restoreBindingId: '7002', banEntryId: '9001', banRevision: 'opaque-revision/+' })
  assert.equal(request.data, undefined)
  assert.equal(request.errorFeedback, 'local')
  assert.equal(request.withCredentials, true)
  assert.match(wrapper.text(), /已恢复.*原绑定.*1.*名额/)
})

test('unchecked binding and orphan confirm as old DELETE with no restore parameters or body', async () => {
  await page()
  let modal = await open('fixture-first-owner')
  assert.equal(modal.querySelector('input[type=checkbox]').checked, false)
  await confirm(modal)
  assert.match(wrapper.text(), /已解除.*全局拉黑/)
  assert.doesNotMatch(wrapper.text(), /已恢复.*原绑定/)
  modal = await open('exe:synthetic-orphan')
  assert.equal(modal.querySelector('input[type=checkbox]'), null)
  await confirm(modal)
  assert.deepEqual(writes().map(request => [request.url, request.params, request.data]), [
    ['/api/admin/device-bans/exe%3Asynthetic-shared-device', undefined, undefined],
    ['/api/admin/device-bans/exe%3Asynthetic-orphan', undefined, undefined],
  ])
})

test('no opt-in for unsigned or unrelated revoke history', async () => {
  rows = [{ ...OLD, banRevision: '' }, { ...SELECTED, revokedReason: 'admin_unbound' }]
  await page()
  let modal = await open('fixture-first-owner')
  assert.equal(modal.querySelector('input[type=checkbox]'), null)
  await confirm(modal)
  modal = await open('fixture-selected-owner')
  assert.equal(modal.querySelector('input[type=checkbox]'), null)
  await confirm(modal)
  assert.deepEqual(writes().map(request => request.params), [undefined, undefined])
})

for (const [change, retire] of [
  ['cancel', (_, modal) => buttonByText(modal, '取消').click()],
  ['admin switch', () => setStoredCurrentUser(LATER_ADMIN)],
  ['scope switch', router => router.push('/device-admin?userId=7302')],
  ['route switch', router => router.push('/other')],
  ['list generation', () => buttonByText(wrapper.element, '刷新').click()],
]) {
  test('selected restore cannot submit after ' + change, async () => {
    const router = await page()
    const modal = await open('fixture-first-owner')
    await chooseRestore(modal)
    const staleConfirm = primary(modal)
    await retire(router, modal)
    await settle()
    staleConfirm.click()
    await settle()
    assert.equal(writes().length, 0)
    if (change === 'cancel') {
      const fresh = await open('fixture-selected-owner')
      assert.equal(fresh.querySelector('input[type=checkbox]').checked, false)
    }
  })
}

for (const [label, deleted, status, reason, expected] of [
  ['full quota', 1, 'removed', 'quota_full', /未恢复.*名额已满/],
  ['changed ban', 0, 'unchanged', 'ban_changed', /未.*恢复.*刷新列表/],
  ['already active', 1, 'unchanged', 'binding_active', /已在使用.*未.*恢复/],
]) {
  test('result never reports a restoration on ' + label, async () => {
    await page()
    reply = { deviceId: DEVICE, deleted, restore: { userId: 7302, bindingId: 7002, status, reason } }
    const modal = await open('fixture-selected-owner')
    await chooseRestore(modal)
    await confirm(modal)
    assert.equal(writes().length, 1)
    assert.match(wrapper.text(), expected)
    assert.doesNotMatch(wrapper.text(), /已恢复.*原绑定/)
  })
}

test('ordinary account cannot enter the device unban flow', async () => {
  await page({ id: 78103, username: 'fixture-non-admin', role: 'user' })
  assert.match(wrapper.text(), /无设备管理权限/)
  assert.equal(buttonByText(wrapper.element, '解封'), undefined)
  assert.equal(reads().length, 0)
  assert.equal(writes().length, 0)
})

test('existing global-only API uses an encoded DELETE without query or body', async () => {
  setStoredCurrentUser(ADMIN)
  const res = await unbanAdminDevice(DEVICE)
  assert.equal(res.status, 200)
  assert.deepEqual(res.data, { deviceId: DEVICE, deleted: 1 })
  assert.equal(writes().length, 1)
  assert.equal(writes()[0].url, '/api/admin/device-bans/exe%3Asynthetic-shared-device')
  assert.equal(writes()[0].params, undefined)
  assert.equal(writes()[0].data, undefined)
})

test('partial explicit restore never falls back to a bare DELETE', () => {
  assert.throws(() => unbanAdminDevice(DEVICE, { restoreUserId: 7302, restoreBindingId: 7002, banEntryId: 9001 }), /恢复.*参数/)
  assert.equal(writes().length, 0)
})
