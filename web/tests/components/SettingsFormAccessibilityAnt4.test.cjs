/* Actual Vue 3/Antd 4/router/current-user ownership; read-only synthetic GETs.
 * The immutable one-shot DOM RED under external probe/ is reused, never replayed.
 * Identical fixture in B/M/R; no real IO, settings save, payment or provider calls.
 * jsdom names/focus are not screen-reader, contrast, geometry or WCAG certification. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'HTMLVideoElement', 'HTMLAudioElement', 'CustomEvent']) globalThis[key] = window[key]
window.scrollTo = () => {}
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick, defineComponent, h } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const ADMIN = { id: 77941, role: 'admin', username: 'SYNTHETIC_SETTINGS_ACCESSIBILITY_ADMIN', balance: 3.234567 }
const detail = {
  siteName: '合成只读站点', maxLogSizeBytes: 2 * 1024 * 1024 * 1024, registerEnabled: true, newUserBonusAmount: 0.123456, minRechargeAmount: 1.25,
  epayVersion: 'v2', epayGatewayUrl: 'https://synthetic-pay.invalid/submit.php', epayPartnerId: 'SYNTHETIC_PID', epayPlatformPublicKey: 'SYNTHETIC_PLATFORM', epayMerchantPrivateKey: 'SYNTHETIC_PRIVATE',
  epayNotifyUrl: 'https://synthetic-site.invalid/adapter-service/payment/notify', epayReturnUrl: 'https://synthetic-site.invalid/adapter/recharge/result',
  smsProvider: 'smsbao', smsbaoUsername: 'SYNTHETIC_SMS_USER', smsbaoSecret: 'SYNTHETIC_SMS_SECRET', smsCodeSignName: '合成签名', smsCodeTemplate: 'SYNTHETIC {sign} {code}', smsLoginEnabled: true,
  wechatLoginEnabled: true, wechatScanLoginEnabled: true, wechatCodeLoginEnabled: true, wechatDefaultLoginMode: 'code', wechatCodeStaticQrcodeUrl: 'https://synthetic-static.invalid/qr.png',
  wechatApiBaseUrl: 'https://synthetic-wechat.invalid', wechatApiBearerToken: 'SYNTHETIC_BEARER', captchaProvider: 'remote', captchaApiBaseUrl: 'https://synthetic-captcha.invalid', captchaApiKey: 'SYNTHETIC_CAPTCHA', captchaType: 'ROTATE',
  smsbaoCredentialType: 'password', smsbaoRegionMode: 'domestic', uapiApiKey: 'SYNTHETIC_UAPI',
}
const zero = { hits: 0, misses: 0, shared: 0 }
const metrics = { schemaVersion: 1, startedAt: '2026-10-29T00:00:00Z', uptimeSeconds: 1, http: { total: 7, inFlight: 0, failed: 0, latencyCount: 0, latencyTotalMillis: 0, latencyMaxMillis: 0, buckets: [] }, cache: { search: zero, fallback: zero }, billing: { attempts: 0, successes: 0, failures: 0 } }
const LABELS = ["网站名称", "最大日志大小", "允许公开注册", "新用户赠送金额", "充值最低金额", "开启短信登录", "开启微信登录", "启用充值阶梯赠送", "启用账户设备池上限", "启用敏感词检测", "微信扫描", "微信验证码", "默认微信方式", "微信 API 地址", "Bearer Token", "验证码二维码", "验证码提供方", "远程服务地址", "远程服务 Key", "验证码类型", "UApiPro API 密钥", "接口版本", "网关地址", "商户编号", "平台公钥", "商户私钥", "网站 Base 地址", "异步通知地址", "同步跳转地址", "短信提供方", "短信宝用户名", "短信宝密钥", "凭证类型", "发送区域", "短信签名", "验证码模板"]
const SECRET_LABELS = ['Bearer Token', '远程服务 Key', 'UApiPro API 密钥', '平台公钥', '商户私钥', '短信宝密钥']
const wrappers = [], reads = [], mutations = [], network = [], runtimeErrors = []
const clone = value => JSON.parse(JSON.stringify(value))
const compact = value => String(value || '').replace(/\s+/g, ' ').trim()
function denyIO(kind) { network.push(kind); throw new Error('FORBIDDEN_SETTINGS_FORM_IO_' + kind) }
function denyMutation(name) { mutations.push(name); throw new Error('FORBIDDEN_SETTINGS_FORM_MUTATION_' + name) }
function read(name, data) { reads.push({ name, actorId: user.getStoredCurrentUser()?.id }); return Promise.resolve({ status: 200, data: clone(data) }) }
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
const getters = { getSystemSettings: () => read('getSystemSettings', detail), getPaymentMethods: () => read('getPaymentMethods', []), getSystemMetrics: () => read('getSystemMetrics', metrics) }
api.exports = new Proxy(getters, { get(target, key) { if (key === '__esModule' || key === 'then') return undefined; return key in target ? target[key] : () => denyMutation(String(key)) } })
require.cache[apiPath] = api
window.XMLHttpRequest.prototype.open = function () { return denyIO('XHR') }
globalThis.fetch = window.fetch = () => denyIO('FETCH')
globalThis.WebSocket = window.WebSocket = class { constructor() { denyIO('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => denyIO(name); transport.get = () => denyIO(name) }
for (const name of ['node:net', 'node:tls']) { const transport = require(name); transport.connect = () => denyIO(name); if ('createConnection' in transport) transport.createConnection = () => denyIO(name) }
window.addEventListener('error', event => runtimeErrors.push(String(event.error?.stack || event.message)))
window.addEventListener('unhandledrejection', event => runtimeErrors.push(String(event.reason?.stack || event.reason)))
const Page = require(path.join(web, 'src/pages/SystemSettings/index.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
function visible(node) {
  if (!node?.isConnected) return false
  for (let item = node; item && item.nodeType === 1; item = item.parentElement) {
    const style = window.getComputedStyle(item)
    if (item.hidden || item.inert || item.getAttribute('aria-hidden') === 'true' || style.display === 'none' || style.visibility === 'hidden') return false
  }
  return true
}
const controlSelector = 'input:not([type="hidden"]), textarea, select, [role="combobox"], [role="spinbutton"], button[role="switch"]'
function form(w) { const node = w.element.querySelector('form.settings-form'); assert.ok(node, 'actual main form mounted'); return node }
function fields(w) {
  return [...form(w).querySelectorAll('.ant-form-item')].map(item => {
    const label = item.querySelector('.ant-form-item-label > label')
    const controls = [...item.querySelectorAll(controlSelector)].filter(control => control.closest('.ant-form-item') === item && visible(control))
    return { item, label, text: compact(label?.textContent), controls }
  }).filter(entry => visible(entry.label))
}
function field(w, text) { const items = fields(w).filter(entry => entry.text === text); assert.equal(items.length, 1, 'one original visible label: ' + text); return items[0] }
function ariaNodes(control) {
  return String(control.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean).map(id => document.getElementById(id))
}
function name(control) {
  const refs = ariaNodes(control)
  assert.ok(refs.every(node => node && visible(node)), 'every aria-labelledby reference exists and is visible')
  return compact(refs.map(node => node.textContent).join(' ')) || compact(control.getAttribute('aria-label')) || compact([...(control.labels || [])].filter(visible).map(label => label.textContent).join(' '))
}
function associated(entry, control) {
  const { label, text } = entry
  const native = label.control === control && [...(control.labels || [])].includes(label)
  const contextualLogUnit = text === '最大日志大小' && [...(control.labels || [])].some(node => visible(node) && compact(node.textContent) === '最大日志大小 单位' && node.control === control)
  const aria = ariaNodes(control).some(node => node && visible(node) && (node === label || label.contains(node)))
  assert.ok(native || aria || contextualLogUnit, 'visible Chinese label names its exact native control, not name/placeholder/title/adjacency: ' + text)
  assert.ok(control.id, 'actual stable control id: ' + text)
  assert.equal(document.getElementById(control.id), control, 'global id resolves own native control: ' + text)
  assert.ok(name(control).includes(text), 'accessible name retains original Chinese field: ' + text)
  if (native) assert.equal(label.htmlFor, control.id, 'for targets native control, never Antd wrapper: ' + text)
}
function ids(w) { return fields(w).flatMap(entry => entry.controls.map(control => { associated(entry, control); return control.id })) }
async function mountPages(count = 1) {
  user.setStoredCurrentUser(ADMIN)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/system-settings', component: { render: () => null } }, { path: '/elsewhere', component: { render: () => null } }] })
  await router.push('/system-settings'); await router.isReady()
  const Host = defineComponent({ setup: () => () => h('div', Array.from({ length: count }, (_, key) => h(Page, { key }))) })
  const host = mount(Host, { attachTo: document.body, global: { plugins: [Antd, router], config: { errorHandler: error => runtimeErrors.push(String(error?.stack || error)) } } })
  wrappers.push(host); await settle()
  const pages = host.findAllComponents(Page)
  assert.equal(pages.length, count)
  for (const page of pages) { assert.equal(page.vm.settingsLoaded, true); assert.equal(page.vm.paymentMethodsLoaded, true); assert.deepEqual([...page.vm.paymentMethods], []); assert.equal(page.vm.paymentMethodVisible, false) }
  assert.equal(document.querySelector('.ant-modal'), null, 'no payment dialog opened')
  assert.equal(reads.length, count * 3, 'only original settings/payment-empty/metrics reads')
  return pages
}
function type(control, value) { control.value = value; control.dispatchEvent(new Event('input', { bubbles: true })); control.dispatchEvent(new Event('change', { bubbles: true })) }
afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) if (wrapper.exists()) wrapper.unmount()
  Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); user.clearStoredCurrentUser(); document.body.replaceChildren()
  assert.deepEqual(network.splice(0), [], 'ZERO_REAL_IO'); assert.deepEqual(mutations.splice(0), [], 'ZERO_MUTATIONS'); assert.deepEqual(runtimeErrors.splice(0), [], 'ZERO_RUNTIME_ERRORS'); reads.length = 0
})
test('Settings original positives preserve 2 GB, six-decimal bonus, fixed/derived states and six masked synthetic secrets', async () => {
  const [w] = await mountPages()
  assert.equal(field(w, '网站名称').controls[0].value, detail.siteName)
  assert.equal(w.vm.formState.maxLogSizeValue, 2); assert.equal(w.vm.formState.maxLogSizeUnit, 'GB')
  assert.equal(field(w, '允许公开注册').controls[0].getAttribute('aria-checked'), 'true')
  assert.equal(field(w, '新用户赠送金额').controls[0].value, '0.123456')
  for (const text of ['接口版本', '短信提供方']) assert.equal(field(w, text).controls[0].disabled, true)
  for (const text of ['异步通知地址', '同步跳转地址']) assert.equal(field(w, text).controls[0].readOnly, true)
  const secretFields = ['wechatApiBearerToken', 'captchaApiKey', 'uapiApiKey', 'epayPlatformPublicKey', 'epayMerchantPrivateKey', 'smsbaoSecret']
  SECRET_LABELS.forEach((text, index) => { const control = field(w, text).controls[0]; assert.equal(control.type, 'password'); assert.equal(control.value, detail[secretFields[index]]) })
  assert.equal(w.vm.hasUnsavedChanges, false)
  assert.deepEqual(reads.map(entry => entry.name).sort(), ['getPaymentMethods', 'getSystemMetrics', 'getSystemSettings'].sort())
})
test('Settings all 36 visible Chinese FormItem labels associate their own 37 actual controls', async () => {
  const [w] = await mountPages(), entries = fields(w)
  assert.deepEqual(entries.map(entry => entry.text), LABELS)
  assert.equal(entries.length, 36); assert.equal(entries.reduce((sum, entry) => sum + entry.controls.length, 0), 37)
  for (const entry of entries) for (const control of entry.controls) associated(entry, control)
  assert.equal(new Set(ids(w)).size, 37, 'no duplicate actual control IDs')
})
test('Settings maximum log size has separately precise visible value/unit names and exact focus targets', async () => {
  const [w] = await mountPages(), entry = field(w, '最大日志大小')
  assert.equal(entry.controls.length, 2)
  const [value, unit] = entry.controls
  associated(entry, value); associated(entry, unit)
  assert.equal(name(value), '最大日志大小 数值'); assert.equal(name(unit), '最大日志大小 单位')
  for (const [control, text] of [[value, '数值'], [unit, '单位']]) {
    const label = [...entry.item.querySelectorAll('label')].find(node => compact(node.textContent) === (text === '单位' ? '最大日志大小 单位' : text))
    assert.ok(label, 'visible sublabel: ' + text); assert.equal(label.control, control)
    // jsdom does not implement ordinary HTML label-click focus; check the real
    // label.control relation above and native focus below, without a test-only handler.
    control.focus(); await nextTick(); assert.equal(document.activeElement, control, 'associated native control accepts exact focus: ' + text)
  }
  entry.label.click(); await nextTick(); assert.equal(document.activeElement, value, 'original Antd label click focuses the exact numeric control')
})
test('Settings original single-control labels focus native enabled controls without changing synthetic values', async () => {
  const [w] = await mountPages(), original = clone(w.vm.formState)
  for (const entry of fields(w)) {
    if (entry.controls.length !== 1 || entry.controls[0].disabled) continue
    const control = entry.controls[0]; associated(entry, control)
    if (control.getAttribute('role') === 'switch') control.focus(); else entry.label.click()
    await nextTick()
    assert.equal(document.activeElement, control, 'original label focuses exact actual control: ' + entry.text + ' focused=' + document.activeElement.outerHTML)
  }
  assert.deepEqual(clone(w.vm.formState), original, 'label focus does not rewrite business/secret fields')
  assert.equal(w.vm.hasUnsavedChanges, false)
})
test('Settings two instances in one Vue app have distinct stable IDs, own focus and independent models', async () => {
  const [a, b] = await mountPages(2), aIds = ids(a), bIds = ids(b)
  assert.equal(new Set([...aIds, ...bIds]).size, 74, 'same-app instance IDs never collide')
  const aa = field(a, '网站名称'), bb = field(b, '网站名称'); associated(aa, aa.controls[0]); associated(bb, bb.controls[0])
  bb.label.click(); await nextTick(); assert.equal(document.activeElement, bb.controls[0])
  type(aa.controls[0], '合成实例A独立草稿'); await settle()
  assert.equal(a.vm.formState.siteName, '合成实例A独立草稿'); assert.equal(b.vm.formState.siteName, detail.siteName)
  assert.equal(a.vm.hasUnsavedChanges, true); assert.equal(b.vm.hasUnsavedChanges, false)
  a.vm.$forceUpdate(); b.vm.$forceUpdate(); await settle()
  assert.deepEqual(ids(a), aIds); assert.deepEqual(ids(b), bIds)
})
test('Settings conditional remote-captcha controls reuse stable IDs after ordinary rerender and mode reentry', async () => {
  const [w] = await mountPages(), before = ids(w), snapshot = clone(w.vm.formState)
  w.vm.$forceUpdate(); await settle(); assert.deepEqual(ids(w), before)
  w.vm.updateSetting('captchaProvider', 'local'); await settle()
  assert.equal(fields(w).length, 33); assert.equal(field(w, '验证码提供方').controls[0].getAttribute('role'), 'combobox')
  w.vm.updateSetting('captchaProvider', 'remote'); await settle()
  assert.deepEqual(ids(w), before); assert.deepEqual(clone(w.vm.formState), snapshot)
})
test('Settings existing native action-button text remains a positive accessible name', async () => {
  const [w] = await mountPages()
  for (const text of ['刷新', '保存', '从浏览器地址栏获取']) {
    const buttons = [...w.element.querySelectorAll('button')].filter(node => visible(node) && compact(node.textContent).replace(/\s/g, '') === text)
    assert.ok(buttons.length > 0, 'original native text button remains: ' + text)
    assert.ok(buttons.every(node => compact(node.textContent)), 'own native button text names it')
  }
})
