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

const admin = { id: 111, role: 'admin', username: 'fixture_admin' }
const normal = { id: 222, role: 'user', username: 'fixture_user' }
const ownerCommand = 'powershell -EncodedCommand ZgA='
const row = (name, ownerId) => ({ id: 7, ownerId, name, scheme: 'https', baseDomain: 'fixture.example.test', shortCode: 'a', versionId: 5, command: ownerCommand })
const okay = data => ({ status: 200, data })
const reads = [], writes = [], purchases = [], redemptions = [], updates = [], policyWrites = [], deletes = [], shortWrites = [], mounts = []
let holdReads = false, holdWrites = false, activationPriceFixture = 12.345678
let allowFullNeverExpire = true, allowShortNeverExpire = false
let listFixture = [row('现有脚本', 111)]

const apiPath = path.resolve(__dirname, '../../src/api/TikuComponentApi.js')
const api = new Module(apiPath); api.loaded = true
api.exports = {
  getScripts(params) {
    if (!holdReads) return Promise.resolve(okay(listFixture))
    return new Promise(resolve => reads.push({ params, resolve }))
  },
  getScriptVersions: async () => okay([{ id: 5, version: 'synthetic', nativeLeaseProtocol: true, isCurrent: true }]),
  getScriptDomainSettings: async () => okay({ domains: ['*.example.test'] }),
  getScriptDomainFormats: async () => okay({ formats: [{ raw: '*.example.test', kind: 'wildcard', host: 'example.test', slot: 'prefix' }], allowFullNeverExpire, allowShortNeverExpire }),
  configureScriptShortAuthorization(id, payload) { return new Promise(resolve => shortWrites.push({ id, payload, resolve })) },
  configureScriptLauncherAuthorization(id, payload) { return new Promise(resolve => shortWrites.push({ id, payload, kind: 'full', resolve })) },
  getScriptEscrowPolicy: async () => okay({ targetMicros: 80000000, minimumMicros: 60000000, multiplierMicros: 1000000 }),
  getScriptActivationCodePrice: async () => okay({ enabled: true, price: activationPriceFixture }),
  updateScriptEscrowPolicy: async payload => { policyWrites.push(payload); return okay(payload) },
  updateScriptActivationCodePrice: async payload => okay({ enabled: Number(payload.price) > 0, price: Number(payload.price) }),
  purchaseScriptActivationCode(key, quote) {
    return new Promise(resolve => purchases.push({ key, quote, resolve }))
  },
  async redeemScriptActivationCode(payload) { redemptions.push(payload); return okay({ id: 555 }) },
  async updateScript(payload) { updates.push(payload); return okay({}) },
  deleteScript(payload) {
    return new Promise((resolve, reject) => deletes.push({ payload, resolve, reject }))
  },
  createScript(payload, key) {
    if (!holdWrites) return Promise.resolve(okay({}))
    return new Promise(resolve => writes.push({ payload, key, resolve }))
  },
}
require.cache[apiPath] = api
const PAGE = require('../../src/pages/ScriptManagement/index.vue')

async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function open(account = admin, viewMode) {
  user.setStoredCurrentUser(account)
  const history = createMemoryHistory(), router = createRouter({ history, routes: [
    { path: '/adapter/scripts', name: viewMode === 'admin' ? 'script-admin' : 'scripts', component: PAGE, meta: viewMode ? { viewMode } : {} },
    { path: '/other', component: { render: () => h('div', 'Other') } },
    { path: '/login', component: { render: () => h('div', 'Login') } },
  ] })
  await router.push('/adapter/scripts'); await router.isReady()
  const wrapper = mount(RouterView, { attachTo: document.body, global: { plugins: [router, Antd] } })
  mounts.push({ wrapper, history })
  await settle()
  return { wrapper, router, page: wrapper.findComponent(PAGE), state: wrapper.findComponent(PAGE).vm.$.setupState }
}

afterEach(async () => {
  for (const { wrapper, history } of mounts.splice(0)) { wrapper.unmount(); history.destroy() }
  Antd.Modal.destroyAll(); Antd.message.destroy()
  await settle()
  user.clearStoredCurrentUser()
  document.body.replaceChildren()
  reads.length = writes.length = purchases.length = redemptions.length = updates.length = policyWrites.length = deletes.length = shortWrites.length = 0; holdReads = holdWrites = false; activationPriceFixture = 12.345678
  listFixture = [row('现有脚本', 111)]
  allowFullNeverExpire = true; allowShortNeverExpire = false
})

const activeLease = id => ({ status: 409, data: { code: 'SCRIPT_ACTIVE_LEASE_CONFIRM_REQUIRED', activeRunSessionId: id } })
const escrowRow = () => ({ ...row('冻结金脚本', 111), billingMode: 'escrow' })

test('admin script management always reads all scripts and has no scope switch', async () => {
  holdReads = true
  const { page } = await open()
  assert.deepEqual(reads[0].params, { scope: 'all' })
  assert.doesNotMatch(page.text(), /只看我的脚本|显示全部脚本/)
  reads[0].resolve(okay(listFixture))
  await settle()
})

test('administrator using the ordinary script view still reads only own scripts', async () => {
  holdReads = true
  const { page } = await open(admin, 'self')
  assert.deepEqual(reads[0].params, { scope: 'self' })
  assert.doesNotMatch(page.text(), /管理员上传 EXE|脚本计费政策/)
  reads[0].resolve(okay(listFixture))
  await settle()
})

test('own command is visible in the script list and purchases have an independent entry', async () => {
  const { page } = await open(admin, 'self')
  assert.ok(page.text().includes(ownerCommand), 'command should be selectable without opening settings')
  assert.ok(page.find('[data-script-activation-purchases]').exists(), 'standalone purchase and receipt entry')
})

function commandActions(scope) { return [...scope.querySelectorAll('.command-cell button')].map(button => button.textContent.replace(/\s/g, '')) }

test('command actions switch from short generation to settings and copy without exposing other owners', async () => {
  const full = 'powershell -c "iex(irm https://uuid.example.test -MaximumRedirection 0 -TimeoutSec 15)"'
  const short = 'powershell -c "iex(irm https://a.example.test -MaximumRedirection 0 -TimeoutSec 15)"'
  listFixture = [{ ...row('自己的脚本', 111), command: full, compatibilityCommand: 'powershell -EncodedCommand YQA=' }, { ...row('其他脚本', 222), id: 8, downloadUrl: 'https://private.example.test', command: full }]
  const copies = []
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async text => { copies.push(text) } } })
  const { state, page } = await open()
  const own = page.find('tr[data-row-key="7"]').element
  assert.deepEqual(commandActions(own), ['复制长命令', '长命令授权设置', '生成短命令'])
  assert.ok(own.textContent.includes(full))
  const other = page.find('tr[data-row-key="8"]').element
  assert.deepEqual(commandActions(other), [])
  assert.doesNotMatch(other.textContent, /private\.example|powershell/)
  own.querySelector('.command-cell button').click()
  await settle()
  assert.deepEqual(copies, [full])
  state.rows[0].shortAuthorization = { status: 'active', address: 'https://a.example.test', command: short, expiresAt: null }
  await settle()
  assert.deepEqual(commandActions(own), ['复制长命令', '长命令授权设置', '短命令授权设置', '复制短命令'])
  ;[...own.querySelectorAll('button')].find(button => button.textContent.replace(/\s/g, '') === '复制短命令').click()
  await settle()
  assert.deepEqual(copies, [full, short])
  state.rows[0].shortAuthorization.status = 'expired'
  await settle()
  assert.equal([...own.querySelectorAll('button')].find(button => button.textContent.replace(/\s/g, '') === '复制短命令').disabled, true)
})

test('native short address input is normalized to HTTPS and restored after saving', async () => {
  const { state } = await open()
  state.openShortAuthorization(state.rows[0])
  await settle()
  const input = document.querySelector('[data-script-short-address]')
  assert.ok(input, 'user can choose the short hostname instead of a random slot')
  input.value = 'a.example.test'
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await settle()
  const operation = state.saveShortAuthorization(true)
  assert.deepEqual(shortWrites[0].payload, { enabled: true, address: 'https://a.example.test', durationMinutes: 5, maxUses: 1, startsAt: null })
  const grant = { status: 'active', address: 'https://a.example.test', command: 'powershell -c "iex(irm https://a.example.test -MaximumRedirection 0 -TimeoutSec 15)"', expiresAt: new Date(Date.now() + 300000).toISOString() }
  shortWrites[0].resolve(okay(grant)); await operation
  state.closeShortAuthorization(); await settle(); state.openShortAuthorization(state.rows[0]); await settle()
  assert.equal(document.querySelector('[data-script-short-address]').value, grant.address)
})

test('both authorization dialogs explicitly select limited or permanent time and retain scheduled start', async () => {
  allowShortNeverExpire = true
  const { state } = await open(admin, 'self')
  for (const mode of ['full', 'short']) {
    state.openShortAuthorization(state.rows[0], mode)
    await settle()
    const modal = document.querySelector('.ant-modal')
    const forever = modal.querySelector('input[type="radio"][value="permanent"]')
    assert.ok(forever && !forever.disabled)
    forever.click(); await settle()
    assert.equal(modal.querySelector('input[id$="shortDuration"]'), null)
    state.setShortAuthorizationField('address', 'custom.example.test')
    state.setShortAuthorizationField('startsAt', '2026-12-01T12:30')
    const operation = state.saveShortAuthorization(true)
    assert.equal(shortWrites.at(-1).payload.durationMinutes, 0)
    assert.equal(shortWrites.at(-1).payload.startsAt, new Date('2026-12-01T12:30').toISOString())
    const command = mode === 'full' ? ownerCommand : 'powershell -c "iex(irm https://custom.example.test)"'
    shortWrites.at(-1).resolve(okay({ status: 'scheduled', address: 'https://custom.example.test', command, expiresAt: null }))
    await operation; await settle()
    if (mode === 'full') assert.equal(state.rows[0].downloadUrl, 'https://custom.example.test')
    state.closeShortAuthorization(); await settle()
  }
})

test('disabled permanent policy cannot be bypassed in a limited authorization dialog', async () => {
  allowFullNeverExpire = false
  const { state } = await open(admin, 'self')
  for (const mode of ['full', 'short']) {
    state.openShortAuthorization(state.rows[0], mode); await settle()
    assert.equal(document.querySelector('.ant-modal input[type="radio"][value="permanent"]').disabled, true)
    state.setShortAuthorizationField('durationMode', 'permanent')
    assert.equal(state.shortAuthorizationForm.durationMode, 'limited')
    state.setShortAuthorizationField('address', 'a.example.test')
    state.setShortAuthorizationField('durationMinutes', 0)
    await state.saveShortAuthorization(true)
    assert.equal(shortWrites.length, 0)
    state.closeShortAuthorization(); await settle()
  }
})

test('native time controls apply a finite preset and scheduled start for either command', async () => {
  const { state } = await open(admin, 'self')
  for (const mode of ['full', 'short']) {
    state.openShortAuthorization(state.rows[0], mode); await settle()
    const modal = document.querySelector('.ant-modal')
    modal.querySelector('input[type="radio"][value="limited"]').click()
    modal.querySelector('input[type="radio"][value="scheduled"]').click()
    await settle()
    const input = modal.querySelector('input[type="datetime-local"]')
    input.value = '2026-12-01T12:30'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    ;[...modal.querySelectorAll('button')].find(button => button.textContent.replace(/\s/g, '') === '1小时').click()
    state.setShortAuthorizationField('address', 'scheduled.example.test')
    await settle()
    ;[...modal.querySelectorAll('button')].find(button => /启用授权|生成短命令/.test(button.textContent)).click()
    await settle()
    const request = shortWrites.at(-1)
    assert.equal(request.payload.durationMinutes, 60)
    assert.equal(request.payload.startsAt, new Date('2026-12-01T12:30').toISOString())
    request.resolve(okay({ status: 'scheduled', command: ownerCommand, startsAt: request.payload.startsAt, expiresAt: '2026-12-01T14:30:00Z' }))
    await settle(); state.closeShortAuthorization(); await settle()
  }
})

test('invalid custom addresses show a local error and cannot issue authorization requests', async () => {
  const { state } = await open(admin, 'self')
  state.openShortAuthorization(state.rows[0]); await settle()
  for (const address of ['', 'http://a.example.test', 'a.example.test?token=bad', "a.example.test');calc;('", 'a.example.test/x/../y']) {
    state.setShortAuthorizationField('address', address)
    await state.saveShortAuthorization(true); await settle()
    assert.equal(shortWrites.length, 0)
    assert.match(document.querySelector('.ant-modal [role="alert"].form-error').textContent, /HTTPS 短地址/)
  }
})

test('activation creation uses visible code and a stable wallet purchase label', async () => {
  const { state } = await open(admin, 'self')
  state.openCreate()
  state.setFormField('billingMode', 'activation_code')
  state.purchaseUncertain = true
  await settle()
  const input = document.querySelector('input[placeholder="输入激活码"]')
  assert.equal(input?.type, 'text')
  assert.doesNotMatch(document.body.textContent, /重试同一次购买/)
  assert.match(document.body.textContent, /钱包购买 ¥12\.345678/)
})

test('both script views create a launcher without user-supplied address, scheme or description', async () => {
  holdWrites = true
  for (const view of ['self', 'admin']) {
    const { state, wrapper, router } = await open(admin, view)
    state.openCreate()
    await settle()
    const modal = document.querySelector('.ant-modal')
    assert.doesNotMatch(modal.textContent, /添加域名|协议|说明/)
    assert.equal(modal.querySelector('input[placeholder="子域"]'), null)
    state.setFormField('name', '随机地址')
    const operation = state.handleSubmit()
    const write = writes.at(-1)
    assert.ok(write)
    for (const key of ['baseDomain', 'scheme', 'useWildcardDomain', 'description', 'domains']) {
      assert.equal(Object.hasOwn(write.payload, key), false, key)
    }
    write.resolve(okay({}))
    await operation
    await router.push('/other')
    await settle()
    assert.ok(wrapper.exists())
  }
})

test('short entry authorization keeps complete command and supports expiry plus revocation', async () => {
  const { state } = await open()
  assert.equal(state.rows[0].command, ownerCommand)
  assert.equal(state.rows[0].shortAuthorization, undefined)
  assert.equal(state.mayCopyCommand(state.rows[0]), true)
  assert.doesNotMatch(document.body.textContent, /powershell -c "iex/, '默认只提供完整授权指令，不展示未启用的短命令')
  state.openShortAuthorization(state.rows[0])
  await settle()
  assert.doesNotMatch(document.body.textContent, /powershell -c "iex/, '打开短命令设置也不能在授权前展示可用短命令')
  state.setShortAuthorizationField('address', 'a.example.test')
  state.setShortAuthorizationField('durationMinutes', 7)
  const operation = state.saveShortAuthorization(true)
  state.saveShortAuthorization(true)
  assert.equal(shortWrites.length, 1)
  assert.deepEqual(shortWrites[0].payload, { enabled: true, address: 'https://a.example.test', durationMinutes: 7, maxUses: 1, startsAt: null })
  const grant = { status: 'active', address: 'https://a.example.test', expiresAt: new Date(Date.now() + 7 * 60000).toISOString(), command: 'powershell -c "iex(irm https://a.example.test)"' }
  shortWrites[0].resolve(okay(grant))
  await operation; await settle()
  assert.match(document.body.textContent, /powershell -c "iex\(irm https:\/\/a.example.test\)"/)
  assert.match(document.body.textContent, /有效至/)
  assert.equal(state.rows[0].command, ownerCommand)
  const revoke = state.saveShortAuthorization(false)
  assert.deepEqual(shortWrites[1].payload, { enabled: false })
  shortWrites[1].resolve(okay({ status: 'disabled', address: grant.address }))
  await revoke; await settle()
  assert.doesNotMatch(document.body.textContent, /powershell -c "iex/)
  assert.equal(state.mayCopyCommand(state.rows[0]), true)
})

test('complete command supports its own schedule and use count without enabling a short entry', async () => {
  const { state } = await open()
  state.openShortAuthorization(state.rows[0], 'full')
  assert.equal(state.shortAuthorizationForm.durationMinutes, 0)
  assert.equal(state.shortAuthorizationForm.maxUses, 0)
  state.setShortAuthorizationField('durationMinutes', 45)
  state.setShortAuthorizationField('maxUses', 3)
  state.setShortAuthorizationField('startsAt', '2026-12-01T12:30')
  const pending = state.saveShortAuthorization(true)
  assert.equal(shortWrites[0].kind, 'full')
  assert.deepEqual(shortWrites[0].payload, { enabled: true, durationMinutes: 45, maxUses: 3, startsAt: new Date('2026-12-01T12:30').toISOString() })
  shortWrites[0].resolve(okay({ status: 'scheduled', command: 'powershell -EncodedCommand YQA=', maxUses: 3, usedCount: 0, startsAt: new Date('2026-12-01T12:30').toISOString() }))
  await pending; await settle()
  assert.equal(state.rows[0].command, 'powershell -EncodedCommand YQA=')
  assert.equal(state.rows[0].shortAuthorization, undefined)
  assert.match(document.body.textContent, /待生效|已使用 0 \/ 3 次/)
})

test('retiring an account hides short authorization and rejects its late response', async () => {
  const { state } = await open()
  state.openShortAuthorization(state.rows[0])
  state.setShortAuthorizationField('address', 'private.example.test')
  const pending = state.saveShortAuthorization(true)
  listFixture = [row('下一账号', normal.id)]
  user.setStoredCurrentUser(normal)
  await settle()
  shortWrites[0].resolve(okay({ status: 'active', address: 'https://private.example.test', expiresAt: new Date(Date.now() + 60000).toISOString(), command: 'powershell -c "iex(irm https://private.example.test)"' }))
  await pending; await settle()
  assert.equal(state.shortAuthorizationRecord, null)
  assert.equal(state.rows[0].ownerId, normal.id)
  assert.equal(state.rows[0].shortAuthorization, undefined)
  assert.doesNotMatch(document.body.textContent, /private.example.test/)
})
function clickActiveClose(label) {
  const modal = [...document.querySelectorAll('.ant-modal')].find(el => el.textContent.includes('客户端正在运行'))
  assert.ok(modal, 'second independent confirmation is visible')
  const button = [...modal.querySelectorAll('button')].find(el => el.textContent.replace(/\s+/g, '') === label)
  assert.ok(button, label)
  button.click()
}

test('an offline escrow script closes after one request without a second confirmation', async () => {
  listFixture = [escrowRow()]
  const { state } = await open()
  const operation = state.handleDelete(state.rows[0])
  assert.deepEqual(deletes[0].payload, { id: 7 })
  listFixture = []
  deletes[0].resolve(okay({ state: 'closed', refundedMicros: 80000000 }))
  await operation; await settle()
  assert.equal(deletes.length, 1)
  assert.equal(state.activeCloseConfirmation, null)
  assert.equal(state.rows.length, 0)
  assert.match(document.body.textContent, /结算完成/)
})

test('an active lease opens a second confirmation, and cancelling never submits approval', async () => {
  listFixture = [escrowRow()]
  const { state } = await open()
  const operation = state.handleDelete(state.rows[0])
  deletes[0].resolve(activeLease('9007199254740993'))
  await operation; await settle()
  assert.equal(deletes.length, 1)
  assert.match(document.body.textContent, /立即吊销令牌并通知客户端退出/)
  clickActiveClose('取消')
  await settle()
  assert.equal(state.activeCloseConfirmation, null)
  assert.equal(deletes.length, 1)
  assert.equal(state.rows[0].name, '冻结金脚本')
})

test('confirmation binds to one run and a changed lease requires a fresh user decision', async () => {
  listFixture = [escrowRow()]
  const { state } = await open()
  const operation = state.handleDelete(state.rows[0])
  deletes[0].reject({ response: activeLease('9007199254740993') })
  await operation; await settle()
  clickActiveClose('确认关闭')
  await settle()
  assert.deepEqual(deletes[1].payload, { id: 7, confirmActiveSessionId: '9007199254740993' })
  deletes[1].resolve(activeLease('9007199254740995'))
  await settle()
  assert.equal(deletes.length, 2, 'a new lease is not confirmed automatically')
  clickActiveClose('确认关闭')
  await settle()
  assert.deepEqual(deletes[2].payload, { id: 7, confirmActiveSessionId: '9007199254740995' })
  listFixture = [{ ...escrowRow(), enabled: false, escrowClosingAt: '2026-11-08T00:00:00Z' }]
  deletes[2].resolve({ status: 202, data: { state: 'settling' } })
  await settle()
  assert.equal(state.activeCloseConfirmation, null)
  assert.equal(state.mayCopyCommand(state.rows[0]), false)
  assert.match(document.body.textContent, /待结算/)
  assert.doesNotMatch(document.body.textContent, /已返还|结算完成/)
})

test('changing accounts retires a pending confirmation and cannot reuse its session ID', async () => {
  listFixture = [escrowRow()]
  const { state } = await open()
  const operation = state.handleDelete(state.rows[0])
  deletes[0].resolve(activeLease('42'))
  await operation; await settle()
  assert.ok(state.activeCloseConfirmation)
  listFixture = [{ ...escrowRow(), ownerId: normal.id }]
  user.setStoredCurrentUser(normal)
  await settle()
  await state.confirmActiveScriptClose()
  assert.equal(state.activeCloseConfirmation, null)
  assert.equal(deletes.length, 1)
})

test('script identity change clears rows and prevents late all-scope result publication', async () => {
  const { state, page } = await open()
  assert.equal(state.rows[0].name, '现有脚本')
  assert.ok(page.text().includes(ownerCommand), 'owner can directly select the command in the table')
  assert.equal(state.mayCopyCommand(state.rows[0]), true)
  assert.equal(state.mayCopyCommand(row('其他账号', 222)), false)
  holdReads = true
  state.refreshScripts()
  assert.deepEqual(reads[0].params, { scope: 'all' })
  user.setStoredCurrentUser(normal)
  await settle()
  assert.deepEqual(reads[1].params, { scope: 'self' })
  reads[0].resolve(okay([row('迟到管理员数据', 111)]))
  await settle()
  assert.deepEqual(state.rows, [])
  assert.doesNotMatch(page.text(), /EncodedCommand/, 'retired command is immediately hidden on identity change')
  assert.equal(state.loading, true, 'old request does not clear new loading')
  reads[1].resolve(okay([row('新账号脚本', 222)]))
  await settle()
  assert.equal(state.rows[0].name, '新账号脚本')
  assert.equal(state.loading, false)
})

test('script save deduplicates clicks and retains the idempotency key only for an unchanged retry', async () => {
  const { state, page } = await open()
  holdWrites = true
  state.openCreate()
  await settle()
  state.setFormField('name', '合成脚本')
  const editor = page.findComponent(Antd.Modal)
  assert.ok(editor.exists())
  editor.vm.$emit('ok')
  editor.vm.$emit('ok')
  assert.equal(writes.length, 1)
  assert.match(writes[0].key, /^script-create-[0-9a-f]{32}$/)
  assert.equal(Object.hasOwn(writes[0].payload, 'command'), false)
  writes[0].resolve({ status: 503 })
  await settle()
  assert.equal(state.modalOpen, true)
  editor.vm.$emit('ok')
  assert.equal(writes[0].key, writes[1].key)
  writes[1].resolve({ status: 503 })
  await settle()
  state.setFormField('name', '修改合成脚本')
  editor.vm.$emit('ok')
  assert.notEqual(writes[1].key, writes[2].key)
  user.clearStoredCurrentUser()
  writes[2].resolve(okay({}))
  await settle()
  assert.equal(state.modalOpen, false)
  assert.equal(state.form.name, '')
  assert.equal(state.rows.length, 0)
})

test('leaving the script page asks before discarding a draft but never traps logout', async () => {
  const { state, router } = await open()
  state.openCreate()
  state.setFormField('name', '待保留草稿')
  const navigation = router.push('/other')
  await settle()
  const cancel = document.querySelector('.ant-modal-confirm .ant-modal-confirm-btns .ant-btn-default')
  assert.ok(cancel)
  cancel.click()
  await navigation
  assert.equal(router.currentRoute.value.path, '/adapter/scripts')
  assert.equal(state.form.name, '待保留草稿')
  await router.push('/login')
  await settle()
  assert.equal(router.currentRoute.value.path, '/login')
})

test('native editor Cancel preserves dirty input until the user explicitly discards it', async () => {
  const { state } = await open()
  state.openCreate()
  await settle()
  const input = document.querySelector('input[placeholder="例如：超星答题助手"]')
  assert.ok(input)
  input.value = '需要保留的脚本草稿'
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await settle()
  const click = (selector, label) => {
    const button = [...document.querySelectorAll(selector)].filter(item => item.textContent.replace(/\s+/g, '') === label).at(-1)
    assert.ok(button, label)
    button.click()
  }
  click('.ant-modal-footer button', '取消')
  await settle()
  assert.equal(state.modalOpen, true)
  assert.match(document.querySelector('.ant-modal-confirm-title').textContent, /有未保存的更改/)
  click('.ant-modal-confirm-btns button', '保留编辑')
  await settle()
  assert.equal(input.value, '需要保留的脚本草稿')
  assert.equal(state.modalOpen, true)
  click('.ant-modal-footer button', '取消')
  await settle()
  click('.ant-modal-confirm-btns button', '放弃并离开')
  await settle()
  assert.equal(state.modalOpen, false)
  assert.equal(state.form.name, '')
})


test('wallet purchase retries one key per editor, and retiring the account clears the grant and ignores a late code', async () => {
  const { state, page } = await open()
  state.openCreate()
  state.setFormField('billingMode', 'activation_code')
  const pending = state.buyActivationCode()
  state.buyActivationCode()
  assert.equal(purchases.length, 1)
  assert.match(purchases[0].key, /^script-activation-purchase-[0-9a-f]{32}$/)
  purchases[0].resolve({ status: 503 })
  await pending
  const retry = state.buyActivationCode()
  assert.equal(purchases.length, 2)
  assert.equal(purchases[0].key, purchases[1].key)
  assert.equal(purchases[0].quote, '12.345678')
  assert.equal(purchases[1].quote, purchases[0].quote)
  user.setStoredCurrentUser(normal)
  purchases[1].resolve(okay({ entitlementId: 777, price: 12.345678, code: 'sac_' + 'a'.repeat(64) }))
  await retry; await settle()
  assert.equal(state.modalOpen, false)
  assert.equal(state.activationCode, '')
  assert.equal(state.form.entitlementId, undefined)
  assert.doesNotMatch(page.text(), /sac_[a-f]{64}/)
  state.openCreate()
  state.setFormField('billingMode', 'activation_code')
  const next = state.buyActivationCode()
  assert.notEqual(purchases[1].key, purchases[2].key)
  purchases[2].resolve({ status: 503 })
  await next
})

test('redeeming sends code only to redemption; creation uses entitlement and edit does not mutate billing mode', async () => {
  const { state, page } = await open()
  holdWrites = true
  state.openCreate()
  state.setFormField('billingMode', 'activation_code')
  state.setFormField('name', '一次合成脚本')
  state.setFormField('runDurationMinutes', 35)
  state.setFormField('notifyOnDenied', true)
  await settle()
  const code = 'sac_' + 'c'.repeat(64)
  state.setActivationCode(code)
  await state.redeemActivationCode()
  assert.deepEqual(redemptions[0], { code })
  assert.equal(state.activationCode, '')
  assert.equal(state.form.entitlementId, 555)
  page.findComponent(Antd.Modal).vm.$emit('ok')
  assert.equal(writes.length, 1)
  assert.equal(writes[0].payload.billingMode, 'activation_code')
  assert.equal(writes[0].payload.entitlementId, 555)
  assert.equal(writes[0].payload.runDurationMinutes, 35)
  assert.equal(writes[0].payload.notifyOnDenied, true)
  for (const field of ['billingEnabled', 'priceMultiplier', 'answerPriceMultiplier', 'billingUseTikuPrice', 'useTikuPrice', 'autoTopUp', 'code']) {
    assert.equal(Object.hasOwn(writes[0].payload, field), false, field)
  }
  assert.equal(JSON.stringify(writes[0].payload).includes(code), false)
  writes[0].resolve(okay({}))
  await settle()
  state.rows[0].billingMode = 'escrow'
  state.openEdit(state.rows[0])
  state.setFormField('billingMode', 'activation_code')
  assert.equal(state.form.billingMode, 'escrow')
  state.setFormField('autoTopUp', true)
  await settle()
  page.findComponent(Antd.Modal).vm.$emit('ok')
  await settle()
  assert.equal(updates[0].autoTopUp, true)
  assert.equal(Object.hasOwn(updates[0], 'billingMode'), false)
  assert.equal(Object.hasOwn(updates[0], 'entitlementId'), false)
  assert.equal(Object.hasOwn(updates[0], 'answerPriceMultiplier'), false)
})

test('admin escrow fields are exact six decimals and reject a minimum above the target', async () => {
  const { state } = await open()
  state.setPolicyField('target', '80.000001')
  state.setPolicyField('minimum', '90')
  await state.saveEscrowPolicy()
  assert.equal(policyWrites.length, 0)
  state.setPolicyField('minimum', '60.100002')
  state.setPolicyField('multiplier', '1.100005')
  await state.saveEscrowPolicy()
  assert.deepEqual(policyWrites[0], { targetMicros: 80000001, minimumMicros: 60100002, multiplierMicros: 1100005 })
})


test('a resolved HTTP stale-quote conflict refreshes the quote and requires a new purchase intent', async () => {
  const { state } = await open()
  state.openCreate()
  state.setFormField('billingMode', 'activation_code')
  const first = state.buyActivationCode()
  assert.equal(purchases[0].quote, '12.345678')
  activationPriceFixture = 13
  purchases[0].resolve({ status: 409, data: { code: 'SCRIPT_ACTIVATION_PRICE_CHANGED' } })
  await first; await settle()
  assert.equal(state.purchaseUncertain, false)
  assert.equal(state.activationPrice.price, '13')
  const newIntent = state.buyActivationCode()
  assert.equal(purchases[1].quote, '13')
  assert.notEqual(purchases[1].key, purchases[0].key)
  purchases[1].resolve({ status: 503 })
  await newIntent
})
