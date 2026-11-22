const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path'), Module = require('node:module')
require('../helpers/vue-dom.cjs')
for (const name of ['HTMLInputElement', 'HTMLButtonElement', 'HTMLTextAreaElement']) global[name] = window[name]
const { mount, flushPromises } = require('@vue/test-utils')
const { h, nextTick } = require('vue')
const { createMemoryHistory, createRouter, RouterView } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require('../../src/utils/currentUser.js')

const admin = { id: 111, role: 'admin', username: 'domain_admin' }
const otherAdmin = { id: 333, role: 'admin', username: 'other_domain_admin' }
const normal = { id: 222, role: 'user', username: 'domain_user' }
const okay = data => ({ status: 200, data })
const settings = () => ({ domains: ['*.a.com', 'a.com/*'], deniedDomains: ['api.a.com', '*.internal.a.com'], allowFullNeverExpire: true, allowShortNeverExpire: false })
let saved = settings(), holdReads = false
const reads = [], writes = [], mounts = []
const apiPath = path.resolve(__dirname, '../../src/api/TikuComponentApi.js')
const api = new Module(apiPath); api.loaded = true
api.exports = {
  getScripts: async () => okay([]),
  getScriptVersions: async () => okay([]),
  getScriptDomainSettings() {
    if (holdReads) return new Promise(resolve => reads.push({ resolve }))
    reads.push({})
    return Promise.resolve(okay(saved))
  },
  updateScriptDomainSettings(payload) {
    return new Promise((resolve, reject) => writes.push({ payload, resolve, reject }))
  },
  getScriptDomainFormats: async () => okay({ formats: saved.domains.map(raw => ({ raw })), allowFullNeverExpire: saved.allowFullNeverExpire, allowShortNeverExpire: saved.allowShortNeverExpire }),
  getScriptEscrowPolicy: async () => okay({ targetMicros: 80000000, minimumMicros: 60000000, multiplierMicros: 1000000 }),
  getScriptActivationCodePrice: async () => okay({ enabled: false, price: 0 }),
}
require.cache[apiPath] = api
const PAGE = require('../../src/pages/ScriptManagement/index.vue')

async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function open(account = admin, viewMode = account.role === 'admin' ? 'admin' : 'self') {
  user.setStoredCurrentUser(account)
  const history = createMemoryHistory(), router = createRouter({ history, routes: [
    { path: '/adapter/scripts', name: 'scripts', component: PAGE, meta: { viewMode } },
    { path: '/other', component: { render: () => h('div', 'Other') } },
  ] })
  await router.push('/adapter/scripts'); await router.isReady()
  const wrapper = mount(RouterView, { attachTo: document.body, global: { plugins: [router, Antd] } })
  mounts.push({ wrapper, history })
  await settle()
  const page = wrapper.findComponent(PAGE)
  return { wrapper, router, page, state: page.vm.$.setupState }
}
const denyInput = page => page.find('textarea[aria-label="禁止设置的域名"]')
const allowInput = page => page.find('textarea[aria-label="脚本允许域名格式"]')
const saveButton = page => page.findAll('button').find(button => button.text().replace(/\s/g, '') === '保存域名设置')
async function save(page) { await saveButton(page).trigger('click'); await settle(); return writes.at(-1) }

afterEach(async () => {
  for (const { wrapper, history } of mounts.splice(0)) { wrapper.unmount(); history.destroy() }
  Antd.Modal.destroyAll(); Antd.message.destroy()
  await settle(); user.clearStoredCurrentUser(); document.body.replaceChildren()
  saved = settings(); holdReads = false; reads.length = writes.length = 0
})

test('domain settings load the administrator deny list with exact and wildcard guidance', async () => {
  const { page } = await open()
  assert.ok(denyInput(page).exists(), 'administrator can edit prohibited domains')
  assert.equal(denyInput(page).element.value, 'api.a.com\n*.internal.a.com')
  assert.equal(denyInput(page).element.disabled, false)
  assert.equal(saveButton(page).element.disabled, true)
  assert.match(page.text(), /留空时使用当前站点/)
  assert.match(page.text(), /禁止规则优先/)
  assert.match(page.text(), /已有命令立即生效/)
  const label = page.find(`label[for="${denyInput(page).attributes('id')}"]`)
  assert.equal(label.text(), '禁止设置的域名')
  assert.match(denyInput(page).attributes('placeholder'), /api\.a\.com[\s\S]*\*\.internal\.a\.com/)
})

test('domain settings save both overlapping lists atomically and explicitly clear the deny list', async () => {
  const { page, state } = await open()
  await denyInput(page).setValue(' api.a.com \r\n\n *.internal.a.com \n admin.a.com ')
  await allowInput(page).setValue(' *.a.com \n a.com/* \n')
  state.setExpiryPolicy('short', true)
  const request = await save(page)
  assert.deepEqual(request.payload, { domains: ['*.a.com', 'a.com/*', ''], deniedDomains: ['api.a.com', '', '*.internal.a.com', 'admin.a.com'], allowFullNeverExpire: true, allowShortNeverExpire: true })
  assert.equal(denyInput(page).element.disabled, true)
  assert.equal(allowInput(page).element.disabled, true)
  saved = request.payload; request.resolve(okay(saved)); await settle()
  assert.equal(denyInput(page).element.value, saved.deniedDomains.join('\n'))
  assert.equal(saveButton(page).element.disabled, true)
  await denyInput(page).setValue('  \n ')
  const clear = await save(page)
  assert.deepEqual(clear.payload.deniedDomains, [])
  assert.deepEqual(clear.payload.domains, saved.domains)
  saved = clear.payload; clear.resolve(okay(saved)); await settle()
  assert.equal(denyInput(page).element.value, '')
  assert.equal(saveButton(page).element.disabled, true)
  await state.loadDomainSettings(); await settle()
  assert.equal(denyInput(page).element.value, '', 'explicit clearing survives a subsequent settings read')
})

test('domain settings accept old records without deniedDomains as an empty editable list', async () => {
  delete saved.deniedDomains
  const { page } = await open()
  assert.equal(denyInput(page).element.value, '')
  await allowInput(page).setValue('')
  const request = await save(page)
  assert.deepEqual(request.payload, { domains: [], deniedDomains: [], allowFullNeverExpire: true, allowShortNeverExpire: false })
  request.resolve(okay({ ...request.payload })); await settle()
  assert.match(page.text(), /留空时使用当前站点/)
})

test('domain settings display server validation errors and keep both drafts editable for retry', async () => {
  const { page } = await open()
  await allowInput(page).setValue('*.b.com')
  await denyInput(page).setValue('https://api.b.com')
  const invalid = await save(page)
  const detail = '禁止设置的域名第 1 行 https://api.b.com 无效，请填写 api.b.com 或 *.internal.b.com'
  invalid.resolve({ status: 400, data: { message: detail } }); await settle()
  assert.ok(page.find('[role="alert"]').text().includes(detail))
  assert.equal(allowInput(page).element.value, '*.b.com')
  assert.equal(denyInput(page).element.value, 'https://api.b.com')
  assert.equal(denyInput(page).element.disabled, false)
  assert.equal(saveButton(page).element.disabled, false)
  await denyInput(page).setValue('api.b.com')
  const retry = await save(page)
  retry.reject({ response: { data: { message: '网络结果未确认，请重试' } } }); await settle()
  assert.match(page.find('[role="alert"]').text(), /网络结果未确认/)
  assert.equal(denyInput(page).element.value, 'api.b.com')
  assert.equal(saveButton(page).element.disabled, false)
})

for (const field of ['domains', 'deniedDomains']) {
  test(`domain settings preserve blank ${field} rows so server errors identify the actual textarea line`, async () => {
    const { page } = await open()
    const input = field === 'domains' ? allowInput(page) : denyInput(page)
    const first = field === 'domains' ? '*.a.com' : 'api.a.com'
    const draft = `${first}\n\nhttps://bad.a.com`
    await input.setValue(draft)
    const invalid = await save(page)
    assert.deepEqual(invalid.payload[field], [first, '', 'https://bad.a.com'])
    const invalidRow = invalid.payload[field].findIndex(value => value.startsWith('https://')) + 1
    const detail = `${field === 'domains' ? '脚本可用域名' : '禁止设置的域名'}第 ${invalidRow} 行 https://bad.a.com 无效`
    invalid.resolve({ status: 400, data: { message: detail } }); await settle()
    assert.match(page.find('[role="alert"]').text(), /第 3 行 https:\/\/bad\.a\.com/)
    assert.equal(input.element.value, draft)
    assert.equal(input.element.disabled, false)
  })
}

test('a late domain settings read cannot overwrite a newer deny-list draft', async () => {
  const { page, state } = await open()
  holdReads = true
  const reading = state.loadDomainSettings()
  await denyInput(page).setValue('new.a.com')
  reads.at(-1).resolve(okay({ ...saved, domains: ['*.old.com'], deniedDomains: ['old.a.com'] }))
  await reading; await settle()
  assert.equal(denyInput(page).element.value, 'new.a.com')
  assert.equal(allowInput(page).element.value, '*.a.com\na.com/*')
  assert.equal(saveButton(page).element.disabled, false)
})

test('changing administrator retires pending domain saves and native handlers before new drafts load', async () => {
  const { page } = await open()
  await denyInput(page).setValue('old-admin.a.com')
  const oldInput = denyInput(page).element, pending = await save(page)
  saved = { ...settings(), deniedDomains: ['current-admin.a.com'] }
  user.setStoredCurrentUser(otherAdmin); await settle()
  assert.equal(denyInput(page).element.value, 'current-admin.a.com')
  await denyInput(page).setValue('new-edit.a.com')
  oldInput.value = 'stale-handler.a.com'; oldInput.dispatchEvent(new Event('input', { bubbles: true }))
  pending.resolve(okay({ ...settings(), deniedDomains: ['late-response.a.com'] })); await settle()
  assert.equal(denyInput(page).element.value, 'new-edit.a.com')
  assert.equal(denyInput(page).element.disabled, false)
  assert.equal(saveButton(page).element.disabled, false)
  assert.equal(writes.length, 1)
})

test('ordinary script views cannot read, display or save administrator domain settings', async () => {
  for (const account of [normal, admin]) {
    const { page, state } = await open(account, 'self')
    assert.equal(denyInput(page).exists(), false)
    assert.equal(allowInput(page).exists(), false)
    state.setDeniedDomainText('private.a.com')
    await state.saveDomainSettings()
  }
  assert.equal(reads.length, 0)
  assert.equal(writes.length, 0)
})
