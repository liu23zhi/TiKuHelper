/* Native owner regression: actual SystemSettings, child panels, validation and Antd.
 * Only transport is mocked. Every value is a synthetic sentinel; no provider/DB/payment is used.
 * TIKU_WEB_SOURCE selects the same fixture against archived baseline, candidate, or rollback.
 */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(root, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(root, 'tests/helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const A = { id: 7101, role: 'admin', username: 'SYNTHETIC_A', balance: 1 }
const B = { id: 7102, role: 'admin', username: 'SYNTHETIC_B', balance: 2 }
const zero = { hits: 0, misses: 0, shared: 0 }
const metricSnapshot = { schemaVersion: 1, startedAt: '2026-10-29T00:00:00Z', uptimeSeconds: 1, http: { total: 7, inFlight: 0, failed: 0, latencyCount: 0, latencyTotalMillis: 0, latencyMaxMillis: 0, buckets: [] }, cache: { search: zero, fallback: zero }, billing: { attempts: 0, successes: 0, failures: 0 } }
const names = ['getSystemSettings', 'getPaymentMethods', 'updateSystemSettings', 'createPaymentMethod', 'updatePaymentMethod', 'deletePaymentMethod']
const calls = Object.fromEntries(names.map(name => [name, []]))
const notices = []
const confirms = []
let metricsReads = 0
let wrappers = []
function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject, completed: false }
}
function transport(name, payload) {
  const pending = deferred()
  const call = { ...pending, actor: user.getStoredCurrentUser()?.id || null, payload: payload === undefined ? undefined : JSON.parse(JSON.stringify(payload)) }
  const resolve = call.resolve, reject = call.reject
  call.resolve = value => { call.completed = true; resolve(value) }
  call.reject = value => { call.completed = true; reject(value) }
  calls[name].push(call)
  return call.promise
}
const apiPath = path.join(root, 'src/api/TikuComponentApi.js')
const api = new Module(apiPath); api.loaded = true
api.exports = Object.fromEntries(names.map(name => [name, payload => transport(name, payload)]))
api.exports.getSystemMetrics = () => { metricsReads += 1; return Promise.resolve({ status: 200, data: metricSnapshot }) }
require.cache[apiPath] = api
for (const kind of ['success', 'error', 'warning']) {
  const native = Antd.message[kind].bind(Antd.message)
  Antd.message[kind] = (...args) => { notices.push({ kind, text: String(args[0]?.content || args[0] || '') }); return native(...args) }
}
const nativeConfirm = Antd.Modal.confirm.bind(Antd.Modal)
Antd.Modal.confirm = options => { confirms.push(options); return nativeConfirm(options) }
const Page = require(path.join(root, 'src/pages/SystemSettings/index.vue'))
const PaymentPanel = require(path.join(root, 'src/pages/SystemSettings/PaymentMethodPanel.vue'))
const IconPicker = require(path.join(root, 'src/pages/SystemSettings/components/PaymentMethodIconPicker.vue'))
const MetricsPanel = require(path.join(root, 'src/pages/SystemSettings/SystemMetricsPanel.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
function detail(tag = 'A') {
  return {
    siteName: `SYNTHETIC_SITE_${tag}`, maxLogSizeBytes: 2 * 1024 * 1024 * 1024,
    registerEnabled: true, newUserBonusAmount: 0.123456, minRechargeAmount: 1.25,
    epayVersion: 'v2', epayGatewayUrl: 'https://synthetic-pay.invalid/submit.php', epayPartnerId: `SYNTHETIC_PID_${tag}`,
    epayPlatformPublicKey: `SYNTHETIC_PLATFORM_${tag}`, epayMerchantPrivateKey: `SYNTHETIC_PRIVATE_${tag}`,
    epayNotifyUrl: 'https://synthetic-site.invalid/adapter-service/payment/notify', epayReturnUrl: 'https://synthetic-site.invalid/adapter/recharge/result',
    smsProvider: 'smsbao', smsbaoUsername: `SYNTHETIC_SMS_USER_${tag}`, smsbaoSecret: `SYNTHETIC_SMS_${tag}`,
    smsCodeSignName: 'SYNTHETIC_SIGN', smsCodeTemplate: 'SYNTHETIC {sign} {code}', smsLoginEnabled: true,
    wechatLoginEnabled: true, wechatScanLoginEnabled: true, wechatCodeLoginEnabled: true, wechatDefaultLoginMode: 'code',
    wechatCodeStaticQrcodeUrl: 'https://synthetic-static.invalid/qr.png', wechatApiBaseUrl: 'https://synthetic-wechat.invalid', wechatApiBearerToken: `SYNTHETIC_BEARER_${tag}`,
    captchaProvider: 'remote', captchaApiBaseUrl: 'https://synthetic-captcha.invalid', captchaApiKey: `SYNTHETIC_CAPTCHA_${tag}`, captchaType: 'ROTATE',
    smsbaoCredentialType: 'password', smsbaoRegionMode: 'domestic', uapiApiKey: `SYNTHETIC_UAPI_${tag}`,
    rechargePromotionEnabled: false, deviceLimitEnabled: false,
  }
}
function row(tag = 'A') { return { id: tag === 'A' ? 71011 : 71021, name: `SYNTHETIC_PAYMENT_${tag}`, iconSvg: '', epayType: 'alipay', enabled: true, sort: 3, remark: `SYNTHETIC_REMARK_${tag}` } }
async function mountPage(actor = A, loaded = true) {
  if (actor) user.setStoredCurrentUser(actor); else user.clearStoredCurrentUser()
  const w = mount(Page, { attachTo: document.body, global: { plugins: [Antd] } })
  wrappers.push(w)
  await settle()
  if (loaded && calls.getSystemSettings.length && calls.getPaymentMethods.length) {
    calls.getSystemSettings.at(-1).resolve({ status: 200, data: detail(actor?.id === B.id ? 'B' : 'A') })
    calls.getPaymentMethods.at(-1).resolve({ status: 200, data: [row(actor?.id === B.id ? 'B' : 'A')] })
    await settle()
  }
  return w
}
function writeCount() { return names.slice(2).reduce((sum, name) => sum + calls[name].length, 0) }
function readCounts() { return [calls.getSystemSettings.length, calls.getPaymentMethods.length] }
function button(w, label) { return w.findAll('button').find(item => item.text().replace(/\s/g, '') === label.replace(/\s/g, '')) }
function click(w, label) { const found = button(w, label); assert.ok(found, `NATIVE_BUTTON_${label}`); found.element.click() }
function callback(component, name) {
  const value = component.vm.$.vnode.props?.[name]
  assert.equal(typeof value, 'function', `NATIVE_VNODE_CALLBACK_${name}`)
  return value
}
function nativeInput(w, placeholder) { return w.get(`input[placeholder="${placeholder}"]`) }
async function siteInput(w, value) { await nativeInput(w, '例如 题库').setValue(value); await settle() }
function secrets(w) { return ['epayPlatformPublicKey', 'epayMerchantPrivateKey', 'smsbaoSecret', 'wechatApiBearerToken', 'captchaApiKey', 'uapiApiKey'].map(key => w.vm.formState[key]) }
function nonsecretBState(w) { return JSON.stringify({ site: w.vm.formState.siteName, secret: secrets(w), rows: w.vm.paymentMethods, modal: w.vm.paymentMethodFormState, visible: w.vm.paymentMethodVisible }) }
function finishLatest(tag) {
  for (const name of ['getSystemSettings', 'getPaymentMethods']) {
    const c = calls[name].at(-1)
    if (c && !c.completed) c.resolve({ status: 200, data: name === 'getSystemSettings' ? detail(tag) : [row(tag)] })
  }
}
afterEach(async () => {
  for (const w of wrappers) { if (w.exists()) w.unmount() }
  wrappers = []
  for (const name of names) for (const c of calls[name]) if (!c.completed) c.resolve({ status: 200, data: name === 'getPaymentMethods' ? [] : {} })
  await settle()
  Antd.Modal.destroyAll(); Antd.message.destroy(); await settle()
  user.clearStoredCurrentUser(); document.body.replaceChildren(); window.sessionStorage.clear()
  names.forEach(name => { calls[name].length = 0 }); notices.length = 0; confirms.length = 0; metricsReads = 0
})

test('SS_PRINCIPAL anonymous/nonadmin mount and native actions perform zero settings/payment IO', async () => {
  for (const actor of [null, { id: 7103, role: 'user', username: 'SYNTHETIC_USER' }]) {
    const w = await mountPage(actor, false)
    assert.deepEqual(readCounts(), [0, 0], 'UNAUTHORIZED_ZERO_PRIVATE_READS')
    for (const label of ['刷新', '保存', '新增支付方式', '从浏览器地址栏获取']) button(w, label)?.element.click()
    w.vm.handleSave(); w.vm.handleLoad(); w.vm.handleDeletePaymentMethod(71011)
    await settle()
    assert.deepEqual(readCounts(), [0, 0]); assert.equal(writeCount(), 0)
    assert.deepEqual(secrets(w), ['', '', '', '', '', ''])
    w.unmount()
  }
})

test('SS_PRINCIPAL main private IO requires a stable positive 32bit userid, not an admin label or username-only fixture', async () => {
  const invalid = [undefined, null, 0, '', ' ', -1, 1.5, '1.5', '1e3', true, 2147483648, Number.MAX_SAFE_INTEGER + 1]
  for (const id of invalid) {
    const w = await mountPage({ id, role: 'admin', username: 'SYNTHETIC_LABEL_WITHOUT_VALID_ID' }, false)
    assert.deepEqual(readCounts(), [0, 0], 'INVALID_ID_ZERO_PRIVATE_GET')
    w.vm.handleSave(); w.vm.handleLoad(); w.vm.handleDeletePaymentMethod(71011)
    button(w, '保存')?.element.click(); button(w, '新增支付方式')?.element.click(); await settle()
    assert.equal(writeCount(), 0, 'INVALID_ID_ZERO_PRIVATE_WRITE')
    assert.equal(w.vm.paymentMethodVisible, false); w.unmount()
  }
  const w = await mountPage({ ...A, id: '7101' })
  assert.deepEqual(readCounts(), [1, 1], 'DECIMAL_STRING_USERID_SUPPORTED')
  click(w, '保存'); await settle(); assert.equal(calls.updateSystemSettings.length, 1)
})

test('SS_PRINCIPAL failed initial settings load cannot save default blanks; explicit successful retry enables save', async () => {
  const w = await mountPage(A, false)
  assert.equal(w.findComponent(PaymentPanel).text().includes('当前还没有支付方式'), false, 'PENDING_PAYMENT_READ_IS_NOT_A_SUCCESSFUL_EMPTY_LIST')
  calls.getSystemSettings[0].reject(new Error('SYNTHETIC_INITIAL_READ_REJECTED'))
  calls.getPaymentMethods[0].resolve({ status: 503, message: 'SYNTHETIC_INITIAL_PAYMENT_READ_FAILED' }); await settle()
  assert.equal(w.findComponent(PaymentPanel).text().includes('支付方式尚未加载成功，请重试'), true, 'FAILED_PAYMENT_READ_HAS_REAL_ERROR_EMPTY_STATE')
  assert.equal(w.findComponent(PaymentPanel).text().includes('当前还没有支付方式'), false, 'FAILED_PAYMENT_READ_IS_NOT_A_SUCCESSFUL_EMPTY_LIST')
  w.vm.handleSave(); button(w, '保存')?.element.click(); await settle()
  assert.equal(writeCount(), 0, 'NO_DEFAULT_BLANK_SAVE_BEFORE_LOAD')
  assert.ok(document.querySelector('.ant-alert-error') || document.querySelector('.ant-message-error'))
  click(w, '刷新'); await settle()
  assert.deepEqual(readCounts(), [2, 2], 'EXPLICIT_INITIAL_RETRY')
  finishLatest('A'); await settle()
  click(w, '保存'); await settle()
  assert.equal(calls.updateSystemSettings.length, 1)
  assert.equal(calls.updateSystemSettings[0].payload.uapiApiKey, 'SYNTHETIC_UAPI_A')
})

test('SS_PRINCIPAL payment-first load allows independent dirty payment draft while initial settings response still fills and marks only settings saved', async () => {
  const w = await mountPage(A, false), settingsRead = calls.getSystemSettings[0]
  calls.getPaymentMethods[0].resolve({ status: 200, data: [row('A')] }); await settle()
  click(w, '新增支付方式'); await settle()
  assert.equal(w.vm.paymentMethodVisible, true, 'PAYMENT_DOMAIN_NOT_DISABLED_UNTIL_MAIN_LOAD')
  const name = document.querySelector('input[placeholder="例如 支付宝"]')
  assert.ok(name); name.value = 'SYNTHETIC_PAYMENT_FIRST_DRAFT'; name.dispatchEvent(new Event('input', { bubbles: true })); await settle()
  settingsRead.resolve({ status: 200, data: detail('A') }); await settle()
  assert.equal(w.vm.formState.siteName, 'SYNTHETIC_SITE_A', 'PAYMENT_EDIT_DOES_NOT_SUPPRESS_INITIAL_SETTINGS_READ')
  assert.equal(w.vm.settingsLoaded, true, 'SETTINGS_BECOMES_USABLE_AFTER_ITS_OWN_SUCCESS')
  assert.equal(w.vm.paymentMethodFormState.name, 'SYNTHETIC_PAYMENT_FIRST_DRAFT')
  assert.equal(w.vm.paymentMethodVisible, true); assert.equal(w.vm.hasUnsavedChanges, true, 'SETTINGS_LOAD_DOES_NOT_MARK_PAYMENT_DRAFT_SAVED')
})

test('SS_PRINCIPAL initial A read retires on B; late A settings/rows/finally cannot affect current B loading or secrets', async () => {
  const w = await mountPage(A, false)
  const childInstance = w.findComponent(MetricsPanel).vm.$
  const oldSettings = calls.getSystemSettings[0], oldRows = calls.getPaymentMethods[0]
  user.setStoredCurrentUser(B)
  assert.deepEqual(secrets(w), ['', '', '', '', '', ''], 'SYNCHRONOUS_PRIVATE_RETIREMENT')
  await settle()
  assert.deepEqual(readCounts(), [2, 2], 'NEW_ADMIN_READS_OWN_PRIVATE_SCOPE')
  oldSettings.resolve({ status: 200, data: detail('A') }); oldRows.resolve({ status: 200, data: [row('A')] })
  await settle()
  assert.deepEqual(secrets(w), ['', '', '', '', '', ''], 'LATE_A_CANNOT_REPOPULATE_SECRETS')
  assert.equal(w.vm.tabLoading, true); assert.equal(w.vm.paymentMethodLoading, true)
  finishLatest('B'); await settle()
  assert.deepEqual(secrets(w), ['SYNTHETIC_PLATFORM_B', 'SYNTHETIC_PRIVATE_B', 'SYNTHETIC_SMS_B', 'SYNTHETIC_BEARER_B', 'SYNTHETIC_CAPTCHA_B', 'SYNTHETIC_UAPI_B'])
  assert.equal(w.vm.paymentMethods[0].id, row('B').id); assert.equal(w.vm.tabLoading, false); assert.equal(w.vm.paymentMethodLoading, false)
  assert.equal(w.findComponent(MetricsPanel).vm.$, childInstance, 'PRINCIPAL_CHANGE_DOES_NOT_REMOUNT_METRICS_CHILD')
  assert.equal(metricsReads, 1, 'PARENT_CHANGE_DOES_NOT_AUTOMATICALLY_REFETCH_METRICS')
})

test('SS_PRINCIPAL old rejected/non200 reads cannot display errors or clear B pending reads', async () => {
  const w = await mountPage(A, false), oldSettings = calls.getSystemSettings[0], oldPayment = calls.getPaymentMethods[0]
  user.setStoredCurrentUser(B); await settle()
  const before = nonsecretBState(w), noticesBefore = notices.length
  oldSettings.reject(new Error('SYNTHETIC_RETIRED_SETTINGS_REJECTION'))
  oldPayment.resolve({ status: 503, message: 'SYNTHETIC_RETIRED_PAYMENT_FAILURE' }); await settle()
  assert.equal(nonsecretBState(w), before); assert.equal(notices.length, noticesBefore)
  assert.equal(w.vm.tabLoading, true); assert.equal(w.vm.paymentMethodLoading, true)
  assert.equal(document.querySelector('.ant-alert-error'), null)
  finishLatest('B'); await settle()
})

test('SS_PRINCIPAL A to B to A does not resurrect original A request or captured toolbar authority', async () => {
  const w = await mountPage(A, false), originalA = calls.getSystemSettings[0]
  const saveComponent = w.findAllComponents(Antd.Button).find(item => item.text().replace(/\s/g, '') === '保存')
  const oldSave = callback(saveComponent, 'onClick')
  user.setStoredCurrentUser(B); await settle(); finishLatest('B'); await settle()
  user.setStoredCurrentUser(A); await settle(); finishLatest('A_RETURNED'); await settle()
  const before = nonsecretBState(w), writes = writeCount(), reads = readCounts()
  originalA.resolve({ status: 200, data: detail('A_ORIGINAL_LATE') }); oldSave(); await settle()
  assert.equal(nonsecretBState(w), before); assert.equal(writeCount(), writes); assert.deepEqual(readCounts(), reads)
  assert.equal(w.vm.formState.siteName, 'SYNTHETIC_SITE_A_RETURNED')
})

test('SS_PRINCIPAL valid Antd input immediately followed by synchronous switch retires private pixels and queued native work without unhandled errors', async () => {
  const w = await mountPage(A)
  const input = nativeInput(w, '请输入密码或 ApiKey').element
  input.value = 'SYNTHETIC_LEGAL_INPUT_THEN_SWITCH'
  input.dispatchEvent(new Event('input', { bubbles: true }))
  assert.equal(w.vm.formState.smsbaoSecret, 'SYNTHETIC_LEGAL_INPUT_THEN_SWITCH')
  user.setStoredCurrentUser(B)
  assert.equal(input.value, '', 'PRIVATE_DOM_BYTES_CLEARED_SYNCHRONOUSLY')
  assert.deepEqual(secrets(w), ['', '', '', '', '', ''])
  await settle(); finishLatest('B'); await settle()
  assert.equal(w.vm.formState.smsbaoSecret, 'SYNTHETIC_SMS_B')
  assert.equal(writeCount(), 0)
})

test('SS_PRINCIPAL role change/logout/unmount retire actual dirty secrets, payment dialog, rows and busy state', async () => {
  for (const transition of ['role', 'logout', 'unmount']) {
    const w = await mountPage(A)
    await nativeInput(w, '请输入密码或 ApiKey').setValue('SYNTHETIC_UNSAVED_SECRET')
    click(w, '新增支付方式'); await settle()
    const modalName = document.querySelector('input[placeholder="例如 支付宝"]')
    assert.ok(modalName); modalName.value = 'SYNTHETIC_UNSAVED_PAYMENT'; modalName.dispatchEvent(new Event('input', { bubbles: true })); await settle()
    assert.equal(w.vm.paymentMethodVisible, true)
    const reads = readCounts()
    if (transition === 'role') user.setStoredCurrentUser({ ...A, role: 'user' })
    else if (transition === 'logout') user.clearStoredCurrentUser()
    else w.unmount()
    assert.deepEqual(secrets(w), ['', '', '', '', '', ''], 'RETIRE_UNSAVED_SECRET_IN_MEMORY')
    assert.equal(w.vm.paymentMethods.length, 0); assert.equal(w.vm.paymentMethodVisible, false)
    assert.equal(w.vm.paymentMethodFormState.name, ''); assert.equal(w.vm.paymentMethodSubmitting, false); assert.equal(w.vm.saving, false)
    await settle(); assert.deepEqual(readCounts(), reads, 'NO_PRIVATE_READ_ON_REVOKE')
    assert.equal(document.body.textContent.includes('SYNTHETIC_UNSAVED_PAYMENT'), false)
    if (transition !== 'unmount') { user.setStoredCurrentUser(B); await settle(); finishLatest('B'); await settle(); assert.equal(w.vm.formState.siteName, 'SYNTHETIC_SITE_B'); w.unmount() }
  }
})

test('SS_PRINCIPAL balance-only profile change preserves dirty draft/dialog and does not refetch', async () => {
  const w = await mountPage(A)
  await siteInput(w, 'SYNTHETIC_DIRTY_SITE')
  click(w, '新增支付方式'); await settle()
  const prior = nonsecretBState(w), reads = readCounts()
  user.setStoredCurrentUser({ ...A, balance: 987.654321 }); await settle()
  assert.equal(nonsecretBState(w), prior); assert.deepEqual(readCounts(), reads)
  assert.equal(w.vm.paymentMethodVisible, true)
})

test('SS_PRINCIPAL actual dirty refresh confirmation retains on cancel, discards only on owned accept, and does not destroy unrelated dialogs', async () => {
  const w = await mountPage(A)
  assert.equal(w.vm.hasUnsavedChanges, false, 'REAL_CLEAN_SNAPSHOT_AFTER_LOAD')
  await siteInput(w, 'SYNTHETIC_CONFIRMED_DRAFT')
  assert.equal(w.vm.hasUnsavedChanges, true, 'REAL_NATIVE_INPUT_MARKS_DIRTY')
  const unrelated = Antd.Modal.confirm({ title: 'SYNTHETIC_UNRELATED_DIALOG', content: 'SYNTHETIC_EXTERNAL_OWNER' })
  click(w, '刷新'); await settle()
  const first = confirms.at(-1)
  assert.match(first.title, /未保存/); assert.deepEqual(readCounts(), [1, 1])
  assert.equal(first.focusTriggerAfterClose, false, 'REFRESH_CONFIRM_MUST_NOT_REFOCUS_RETIRED_OPENER')
  assert.equal(document.body.textContent.includes('SYNTHETIC_UNRELATED_DIALOG'), true)
  first.onCancel(); await settle()
  assert.equal(w.vm.formState.siteName, 'SYNTHETIC_CONFIRMED_DRAFT'); assert.deepEqual(readCounts(), [1, 1])
  click(w, '刷新'); await settle(); confirms.at(-1).onOk(); await settle()
  assert.deepEqual(readCounts(), [2, 2]); finishLatest('REFRESH_ACCEPTED'); await settle()
  assert.equal(w.vm.formState.siteName, 'SYNTHETIC_SITE_REFRESH_ACCEPTED'); assert.equal(w.vm.hasUnsavedChanges, false)
  await siteInput(w, 'SYNTHETIC_OLD_OWNER_DRAFT'); click(w, '刷新'); await settle()
  const retiredPrompt = confirms.at(-1)
  user.setStoredCurrentUser(B)
  const bFocus = document.createElement('button'); bFocus.textContent = 'SYNTHETIC_B_FOCUS'; document.body.append(bFocus); bFocus.focus()
  await settle(); finishLatest('B'); await settle()
  assert.equal(document.activeElement, bFocus, 'OWNED_REFRESH_RETIREMENT_PRESERVES_CURRENT_B_FOCUS')
  const before = nonsecretBState(w), reads = readCounts()
  retiredPrompt.onOk(); first.onOk(); await settle()
  assert.equal(nonsecretBState(w), before); assert.deepEqual(readCounts(), reads)
  assert.equal(document.activeElement, bFocus)
  assert.equal(document.body.textContent.includes('SYNTHETIC_UNRELATED_DIALOG'), true, 'ONLY_PAGE_OWNED_CONFIRMS_DESTROYED')
  unrelated.destroy()
})

test('SS_PRINCIPAL actual payment-close and route-leave dirty confirmations retire only their old owner', async () => {
  const w = await mountPage(A)
  click(w, '新增支付方式'); await settle()
  const name = document.querySelector('input[placeholder="例如 支付宝"]')
  name.value = 'SYNTHETIC_DIRTY_PAYMENT'; name.dispatchEvent(new Event('input', { bubbles: true })); await settle()
  assert.equal(w.vm.hasUnsavedChanges, true)
  callback(w.findComponent(Antd.Modal), 'onCancel')(); await settle()
  const closePrompt = confirms.at(-1); assert.match(closePrompt.title, /未保存.*支付方式/)
  closePrompt.onCancel(); await settle(); assert.equal(w.vm.paymentMethodVisible, true)
  await siteInput(w, 'SYNTHETIC_ROUTE_DRAFT')
  const leave = w.vm.requestRouteLeave({ path: '/adapter/users' }); await settle()
  const routePrompt = confirms.at(-1); assert.match(routePrompt.title, /未保存/)
  assert.equal(routePrompt.focusTriggerAfterClose, false, 'ROUTE_CONFIRM_MUST_NOT_REFOCUS_RETIRED_OPENER')
  user.setStoredCurrentUser(B)
  const bFocus = document.createElement('button'); bFocus.textContent = 'SYNTHETIC_B_ROUTE_FOCUS'; document.body.append(bFocus); bFocus.focus()
  await settle(); finishLatest('B'); await settle()
  assert.equal(document.activeElement, bFocus, 'OWNED_ROUTE_RETIREMENT_PRESERVES_CURRENT_B_FOCUS')
  assert.equal(await leave, false, 'OLD_ROUTE_LEAVE_PROMPT_SETTLED_AS_RETIRED')
  const before = nonsecretBState(w)
  closePrompt.onOk(); routePrompt.onOk(); await settle()
  assert.equal(nonsecretBState(w), before); assert.equal(w.vm.paymentMethodVisible, false); assert.equal(writeCount(), 0)
  assert.equal(document.activeElement, bFocus)
})

test('SS_PRINCIPAL same actor newest refresh wins; input made during current read is not overwritten', async () => {
  const w = await mountPage(A)
  w.vm.loadSettings(); await settle(); const first = calls.getSystemSettings.at(-1)
  w.vm.loadSettings(); await settle(); const second = calls.getSystemSettings.at(-1)
  assert.notEqual(first, second)
  second.resolve({ status: 200, data: detail('NEWEST') }); await settle()
  first.resolve({ status: 200, data: detail('STALE') }); await settle()
  assert.equal(w.vm.formState.siteName, 'SYNTHETIC_SITE_NEWEST')
  w.vm.loadSettings(); await settle(); const editingRead = calls.getSystemSettings.at(-1)
  await siteInput(w, 'SYNTHETIC_TYPED_DURING_READ')
  editingRead.resolve({ status: 200, data: detail('SERVER') }); await settle()
  assert.equal(w.vm.formState.siteName, 'SYNTHETIC_TYPED_DURING_READ', 'IN_FLIGHT_INPUT_IS_A_REAL_DIRTY_DRAFT')
  assert.equal(w.vm.tabLoading, false)
  w.vm.loadPaymentMethods(); await settle(); const oldRows = calls.getPaymentMethods.at(-1)
  w.vm.loadPaymentMethods(); await settle(); const latestRows = calls.getPaymentMethods.at(-1)
  latestRows.resolve({ status: 200, data: [row('B')] }); await settle()
  oldRows.resolve({ status: 200, data: [row('A')] }); await settle()
  assert.equal(w.vm.paymentMethods[0].id, row('B').id, 'LATEST_PAYMENT_READ_WINS')
})

test('SS_PRINCIPAL same-actor successful load retires captured old form callbacks and refresh-pending save is disabled', async () => {
  const w = await mountPage(A)
  const input = w.findAllComponents(Antd.Input).find(item => item.attributes('placeholder') === '例如 题库')
  const oldInput = callback(input, 'onUpdate:value')
  const oldSave = callback(w.findAllComponents(Antd.Button).find(item => item.text().replace(/\s/g, '') === '保存'), 'onClick')
  w.vm.loadSettings(); oldSave(); await settle()
  assert.equal(writeCount(), 0, 'REFRESH_PENDING_CANNOT_SAVE_OLD_SNAPSHOT')
  calls.getSystemSettings.at(-1).resolve({ status: 200, data: detail('SAME_ACTOR_NEW_FORM') }); await settle()
  oldInput('SYNTHETIC_RETIRED_SAME_ACTOR_INPUT'); oldSave(); await settle()
  assert.equal(w.vm.formState.siteName, 'SYNTHETIC_SITE_SAME_ACTOR_NEW_FORM')
  assert.equal(writeCount(), 0, 'OLD_FORM_ENTRY_NOT_REUSED_BY_SAME_ACTOR_NEW_FORM')
})

test('SS_PRINCIPAL current GET non200/rejection retains draft and renders native errors; no automatic retry', async () => {
  const w = await mountPage(A)
  await siteInput(w, 'SYNTHETIC_FAILURE_DRAFT')
  for (const failure of ['non200', 'reject']) {
    w.vm.loadSettings(); await settle(); const current = calls.getSystemSettings.at(-1), reads = readCounts()
    if (failure === 'non200') current.resolve({ status: 503, message: 'SYNTHETIC_SETTINGS_UNAVAILABLE' }); else current.reject(new Error('SYNTHETIC_TRANSPORT_FAILURE'))
    await settle()
    assert.equal(w.vm.formState.siteName, 'SYNTHETIC_FAILURE_DRAFT'); assert.equal(w.vm.tabLoading, false)
    assert.deepEqual(readCounts(), reads)
    assert.ok(document.querySelector('.ant-message-error') || document.querySelector('.ant-alert-error'), 'NATIVE_VISIBLE_CURRENT_READ_ERROR')
  }
  w.vm.loadPaymentMethods(); await settle(); const paymentRead = calls.getPaymentMethods.at(-1)
  paymentRead.reject(new Error('SYNTHETIC_PAYMENT_READ_FAILURE')); await settle()
  assert.equal(w.vm.paymentMethods[0].id, row('A').id); assert.equal(w.vm.paymentMethodLoading, false)
})

test('SS_PRINCIPAL save sent once for A is not cancelled but cannot reload, toast or release B save', async () => {
  const w = await mountPage(A)
  await siteInput(w, 'SYNTHETIC_A_SAVED_DRAFT')
  click(w, '保存'); click(w, '保存'); await settle()
  assert.equal(calls.updateSystemSettings.length, 1, 'SAME_OWNER_SAVE_SINGLE_FLIGHT')
  const old = calls.updateSystemSettings[0]
  user.setStoredCurrentUser(B); await settle(); finishLatest('B'); await settle()
  await siteInput(w, 'SYNTHETIC_B_PENDING_DRAFT'); click(w, '保存'); await settle()
  assert.equal(calls.updateSystemSettings.length, 2)
  const current = calls.updateSystemSettings[1], reads = readCounts(), before = nonsecretBState(w), toastCount = notices.length
  old.resolve({ status: 200, data: detail('A_LATE') }); await settle()
  assert.equal(nonsecretBState(w), before); assert.equal(w.vm.saving, true)
  assert.deepEqual(readCounts(), reads); assert.equal(notices.length, toastCount, 'OLD_SAVE_NO_TOAST_OR_RELOAD')
  current.resolve({ status: 200, data: detail('B') }); await settle()
  assert.equal(w.vm.saving, false); assert.equal(w.vm.formState.siteName, 'SYNTHETIC_B_PENDING_DRAFT')
})

test('SS_PRINCIPAL current save non200/rejection preserves real dirty draft and permits explicit retry only', async () => {
  const w = await mountPage(A)
  for (const failure of ['non200', 'reject']) {
    await siteInput(w, `SYNTHETIC_SAVE_${failure}`); click(w, '保存'); await settle()
    const current = calls.updateSystemSettings.at(-1), count = writeCount()
    if (failure === 'non200') current.resolve({ status: 503, message: 'SYNTHETIC_SAVE_UNAVAILABLE' }); else current.reject(new Error('SYNTHETIC_SAVE_REJECTED'))
    await settle()
    assert.equal(w.vm.formState.siteName, `SYNTHETIC_SAVE_${failure}`); assert.equal(w.vm.saving, false)
    assert.equal(writeCount(), count); assert.ok(document.querySelector('.ant-message-error') || document.querySelector('.ant-alert-error'))
  }
})

test('SS_PRINCIPAL captured old native input, toolbar, child actions, Modal OK/afterClose and icon picker are inert on same-tick switch', async () => {
  const w = await mountPage(A)
  const oldInput = nativeInput(w, '请输入密码或 ApiKey').element
  const oldSave = button(w, '保存').element, oldRefresh = button(w, '刷新').element
  const oldBase = button(w, '从浏览器地址栏获取').element
  const panel = w.findComponent(PaymentPanel)
  const oldCreate = callback(panel, 'onCreate'), oldEdit = callback(panel, 'onEdit'), oldDelete = callback(panel, 'onDelete')
  click(w, '新增支付方式'); await settle()
  const oldOK = callback(w.findComponent(Antd.Modal), 'onOk')
  const oldClose = w.findComponent(Antd.Modal).props('afterClose')
  const oldIcon = callback(w.findComponent(IconPicker), 'onSelect')
  const oldNativeName = document.querySelector('input[placeholder="例如 支付宝"]')
  user.setStoredCurrentUser(B)
  const reads = readCounts(), writes = writeCount(), before = nonsecretBState(w)
  oldInput.value = 'SYNTHETIC_OLD_INPUT'; oldInput.dispatchEvent(new Event('input', { bubbles: true }))
  oldNativeName.value = 'SYNTHETIC_OLD_DIALOG'; oldNativeName.dispatchEvent(new Event('input', { bubbles: true }))
  oldSave.click(); oldRefresh.click(); oldBase.click()
  oldCreate(); oldEdit(row('A')); oldDelete(row('A').id); oldOK(); oldClose(); oldIcon('antd:alipay:v1')
  assert.equal(nonsecretBState(w), before, 'SAME_TICK_OLD_CALLBACKS_CANNOT_EDIT_B')
  assert.equal(writeCount(), writes); assert.deepEqual(readCounts(), reads)
  await settle(); finishLatest('B'); await settle()
  click(w, '新增支付方式'); await settle()
  const BmodalBefore = JSON.stringify(w.vm.paymentMethodFormState)
  oldClose(); oldIcon('antd:alipay:v1'); oldOK(); oldDelete(row('A').id); await settle()
  assert.equal(JSON.stringify(w.vm.paymentMethodFormState), BmodalBefore); assert.equal(w.vm.paymentMethodVisible, true)
  assert.equal(writeCount(), writes)
})

test('SS_PRINCIPAL real native payment popconfirm retains its A row lease across same-tick B switch', async () => {
  const w = await mountPage(A)
  const pop = w.findComponent(Antd.Popconfirm)
  assert.equal(pop.exists(), true, 'REAL_PAYMENT_POPCONFIRM_MOUNTED')
  const oldConfirm = callback(pop, 'onConfirm')
  user.setStoredCurrentUser(B)
  oldConfirm(); await settle(); finishLatest('B'); await settle()
  assert.equal(calls.deletePaymentMethod.length, 0, 'OLD_NATIVE_CONFIRM_NO_B_WRITE')
  assert.equal(w.vm.paymentMethods[0].id, row('B').id)
})

for (const operation of ['createPaymentMethod', 'updatePaymentMethod', 'deletePaymentMethod']) {
  test(`SS_PRINCIPAL already-sent A ${operation} has no continuation/toast and cannot clear B pending`, async () => {
    const w = await mountPage(A)
    if (operation === 'deletePaymentMethod') w.vm.handleDeletePaymentMethod(row('A').id)
    else {
      if (operation === 'createPaymentMethod') click(w, '新增支付方式'); else w.vm.openEditPaymentMethodModal(row('A'))
      await settle(); w.vm.paymentMethodFormState.name = 'SYNTHETIC_SENT_A'; w.vm.paymentMethodFormState.ePayType = 'alipay'
      w.vm.handleSubmitPaymentMethod(); w.vm.handleSubmitPaymentMethod()
    }
    await settle(); assert.equal(calls[operation].length, 1, 'OLD_MUTATION_WAS_ALREADY_SENT_EXACTLY_ONCE')
    const old = calls[operation][0]
    user.setStoredCurrentUser(B); await settle(); finishLatest('B'); await settle()
    click(w, '新增支付方式'); await settle()
    w.vm.paymentMethodFormState.name = 'SYNTHETIC_PENDING_B'; w.vm.paymentMethodFormState.ePayType = 'wxpay'; w.vm.handleSubmitPaymentMethod(); await settle()
    const current = calls.createPaymentMethod.at(-1), reads = readCounts(), before = nonsecretBState(w), toastCount = notices.length
    old.resolve({ status: 200, data: {} }); await settle()
    assert.equal(nonsecretBState(w), before); assert.equal(w.vm.paymentMethodSubmitting, true)
    assert.deepEqual(readCounts(), reads); assert.equal(notices.length, toastCount)
    current.resolve({ status: 503, message: 'SYNTHETIC_B_EXPLICIT_RETRY' }); await settle()
    assert.equal(w.vm.paymentMethodSubmitting, false); assert.equal(w.vm.paymentMethodVisible, true); assert.equal(w.vm.paymentMethodFormState.name, 'SYNTHETIC_PENDING_B')
  })

  test(`SS_PRINCIPAL current ${operation} non200/rejection keeps draft or rows and retries only explicitly`, async () => {
    const w = await mountPage(A)
    if (operation !== 'deletePaymentMethod') {
      if (operation === 'createPaymentMethod') click(w, '新增支付方式'); else w.vm.openEditPaymentMethodModal(row('A'))
      await settle()
      const name = document.querySelector('input[placeholder="例如 支付宝"]')
      name.value = 'SYNTHETIC_CURRENT_PAYMENT_DRAFT'; name.dispatchEvent(new Event('input', { bubbles: true })); await settle()
      w.vm.paymentMethodFormState.ePayType = 'alipay'
    }
    for (const failure of ['non200', 'reject']) {
      const beforeCalls = calls[operation].length, reads = readCounts()
      if (operation === 'deletePaymentMethod') { w.vm.handleDeletePaymentMethod(row('A').id); w.vm.handleDeletePaymentMethod(row('A').id) }
      else { w.vm.handleSubmitPaymentMethod(); w.vm.handleSubmitPaymentMethod() }
      await settle()
      assert.equal(calls[operation].length, beforeCalls + 1, 'CURRENT_PAYMENT_SINGLE_FLIGHT')
      const current = calls[operation].at(-1)
      if (failure === 'non200') current.resolve({ status: 503, message: 'SYNTHETIC_CURRENT_PAYMENT_FAILURE' })
      else current.reject(new Error('SYNTHETIC_CURRENT_PAYMENT_REJECTION'))
      await settle()
      assert.equal(calls[operation].length, beforeCalls + 1); assert.deepEqual(readCounts(), reads)
      assert.equal(w.vm.paymentMethods[0].id, row('A').id)
      if (operation !== 'deletePaymentMethod') {
        assert.equal(w.vm.paymentMethodFormState.name, 'SYNTHETIC_CURRENT_PAYMENT_DRAFT')
        assert.equal(w.vm.paymentMethodVisible, true); assert.equal(w.vm.paymentMethodSubmitting, false)
      }
      assert.ok(document.querySelector('.ant-message-error') || document.querySelector('.ant-alert-error'), 'CURRENT_PAYMENT_NATIVE_ERROR_VISIBLE')
    }
  })
}

test('SS_PRINCIPAL exact settings payload preserves low-frequency fields, v2, precision and intentional empty/clear secrets', async () => {
  const w = await mountPage(A)
  await siteInput(w, '  SYNTHETIC_EDITED_SITE  ')
  w.vm.formState.newUserBonusAmount = 0.1234567; w.vm.formState.minRechargeAmount = 2.349
  w.vm.formState.epayVersion = 'v1'; w.vm.formState.uapiApiKey = ''; w.vm.formState.smsbaoSecret = '__CLEAR__'
  click(w, '保存'); await settle()
  const expected = { ...detail('A'), siteName: 'SYNTHETIC_EDITED_SITE', newUserBonusAmount: 0.123457, minRechargeAmount: 2.35, epayVersion: 'v2', uapiApiKey: '', smsbaoSecret: '__CLEAR__' }
  assert.equal(calls.updateSystemSettings.length, 1)
  assert.deepEqual(calls.updateSystemSettings[0].payload, expected, 'EXACT_FULL_SETTINGS_PAYLOAD')
  calls.updateSystemSettings[0].resolve({ status: 200, data: expected }); await settle()
  assert.ok(notices.some(item => item.kind === 'success'))
  assert.equal(w.vm.saving, false)
})

test('SS_PRINCIPAL successful main settings save does not mark unrelated dirty payment draft as saved', async () => {
  const w = await mountPage(A)
  click(w, '新增支付方式'); await settle()
  const name = document.querySelector('input[placeholder="例如 支付宝"]')
  name.value = 'SYNTHETIC_PAYMENT_NOT_SAVED_BY_MAIN'; name.dispatchEvent(new Event('input', { bubbles: true })); await settle()
  await siteInput(w, 'SYNTHETIC_MAIN_SAVE_ONLY')
  click(w, '保存'); await settle(); calls.updateSystemSettings[0].resolve({ status: 200, data: {} }); await settle()
  assert.equal(w.vm.hasUnsavedChanges, true)
  assert.equal(w.vm.paymentMethodFormState.name, 'SYNTHETIC_PAYMENT_NOT_SAVED_BY_MAIN')
  callback(w.findComponent(Antd.Modal), 'onCancel')(); await settle()
  assert.match(confirms.at(-1).title, /未保存.*支付方式/)
  confirms.at(-1).onCancel(); await settle(); assert.equal(w.vm.paymentMethodVisible, true)
})

test('SS_PRINCIPAL native validation/SVG negatives remain real; icon selection and exact create/update/delete payloads preserve fields', async () => {
  const w = await mountPage(A)
  click(w, '新增支付方式'); await settle()
  w.vm.handleSubmitPaymentMethod(); await settle(); assert.equal(writeCount(), 0, 'REAL_EMPTY_NAME_VALIDATION')
  const name = document.querySelector('input[placeholder="例如 支付宝"]')
  name.value = ' SYNTHETIC_PAYMENT_NEW '; name.dispatchEvent(new Event('input', { bubbles: true })); await settle()
  w.vm.handleSubmitPaymentMethod(); await settle(); assert.equal(writeCount(), 0, 'REAL_EMPTY_CHANNEL_VALIDATION')
  w.vm.paymentMethodFormState.ePayType = ' wxpay '; w.vm.paymentMethodFormState.iconSVG = '<svg onload="alert(1)"/>'
  w.vm.handleSubmitPaymentMethod(); await settle(); assert.equal(writeCount(), 0, 'REAL_UNSAFE_SVG_VALIDATION')
  const icon = document.querySelector('.payment-icon-button'); assert.ok(icon); icon.click(); await settle()
  assert.equal(w.vm.paymentMethodIconCatalogId, 'antd:alipay:v1')
  assert.equal(w.vm.isSafePaymentIcon(w.vm.paymentMethodFormState.iconSVG), true)
  w.vm.paymentMethodFormState.sort = 9; w.vm.paymentMethodFormState.enabled = false; w.vm.paymentMethodFormState.remark = ' SYNTHETIC_NOTE '
  const svg = w.vm.paymentMethodFormState.iconSVG
  w.vm.handleSubmitPaymentMethod(); w.vm.handleSubmitPaymentMethod(); await settle()
  assert.equal(calls.createPaymentMethod.length, 1)
  assert.deepEqual(calls.createPaymentMethod[0].payload, { name: 'SYNTHETIC_PAYMENT_NEW', iconSvg: svg, epayType: 'wxpay', enabled: false, sort: 9, remark: 'SYNTHETIC_NOTE' })
  calls.createPaymentMethod[0].resolve({ status: 200, data: {} }); await settle(); finishLatest('A'); await settle()
  w.vm.openEditPaymentMethodModal(row('A')); await settle(); w.vm.paymentMethodFormState.remark = ' SYNTHETIC_UPDATED '
  w.vm.handleSubmitPaymentMethod(); await settle()
  assert.deepEqual(calls.updatePaymentMethod[0].payload, { id: row('A').id, name: row('A').name, iconSvg: '', epayType: 'alipay', enabled: true, sort: 3, remark: 'SYNTHETIC_UPDATED' })
  calls.updatePaymentMethod[0].resolve({ status: 200, data: {} }); await settle(); finishLatest('A'); await settle()
  const pop = w.findComponent(Antd.Popconfirm); callback(pop, 'onConfirm')(); await settle()
  assert.deepEqual(calls.deletePaymentMethod[0].payload, { id: row('A').id })
  calls.deletePaymentMethod[0].resolve({ status: 200 }); await settle(); finishLatest('A'); await settle()
})

test('SS_PRINCIPAL diagnostics child remains mounted, separate and unmodified by parent save/refresh', async () => {
  const w = await mountPage(A)
  const child = w.findComponent(MetricsPanel), instance = child.vm.$
  assert.equal(metricsReads, 1); assert.equal(child.vm.metrics.http.total, 7)
  await siteInput(w, 'SYNTHETIC_METRICS_INDEPENDENT_DRAFT')
  button(w, '刷新诊断').element.click(); await settle()
  assert.equal(metricsReads, 2); assert.equal(w.vm.formState.siteName, 'SYNTHETIC_METRICS_INDEPENDENT_DRAFT')
  assert.deepEqual(readCounts(), [1, 1]); assert.equal(w.findComponent(MetricsPanel).vm.$, instance)
  click(w, '保存'); await settle(); calls.updateSystemSettings[0].resolve({ status: 200, data: {} }); await settle()
  assert.equal(metricsReads, 2); assert.equal(child.vm.metrics.http.total, 7)
  assert.equal(w.findComponent(MetricsPanel).vm.$, instance)
})

test('SS_PRINCIPAL unmounted A promises cannot alter remounted B form/payment/busy or show old feedback', async () => {
  const old = await mountPage(A, false), oldSettings = calls.getSystemSettings[0], oldPayment = calls.getPaymentMethods[0]
  old.unmount()
  const w = await mountPage(B), before = nonsecretBState(w), toastCount = notices.length
  oldSettings.resolve({ status: 200, data: detail('A') }); oldPayment.reject(new Error('SYNTHETIC_OLD_UNMOUNT_FAILURE')); await settle()
  assert.equal(nonsecretBState(w), before); assert.equal(notices.length, toastCount)
  assert.equal(w.vm.tabLoading, false); assert.equal(w.vm.paymentMethodLoading, false)
  console.log('SS_PRINCIPAL_WITNESS actualVue=true actualAntd=true actualChildren=true syntheticTransportOnly=true providerCalls=0 databaseCalls=0 paymentProviderCalls=0')
})
