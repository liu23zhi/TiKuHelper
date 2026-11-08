/* Actual Profile/Vue/Antd/router/current-user/owner/input helpers; only the three
 * API reads return synthetic ordinary-user data. No save, SMS, captcha, QR,
 * binding, confirmation or other business mutation is activated.
 * TIKU_WEB_SOURCE/UI_TEST_HELPER select complete unchanged B/M/R web sources.
 * Native DOM association/focus evidence in jsdom is not reader, contrast,
 * 200%-zoom, geometry, Cookie/provider authentication or WCAG certification.
 */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), path.resolve(__dirname, '../../node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'HTMLVideoElement', 'HTMLAudioElement', 'CustomEvent']) globalThis[key] = window[key]
window.scrollTo = () => {}
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick, defineComponent, h, ref } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue'), Swal = require('sweetalert2')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const ORDINARY = { id: 76751, role: 'user', status: 'enabled', username: 'SYNTHETIC_PROFILE_ACCESSIBILITY', nickname: '合成资料昵称', balance: 0.123456, phone: '13900000000', wechatId: '', registerSource: 'register', createdAt: '2026-10-01T08:00:00+08:00', lastLoginAt: '2026-10-29T08:00:00+08:00', lastLoginIp: '192.0.2.17' }
const DEFAULT_SETTINGS = { smsLoginEnabled: true, wechatLoginEnabled: false, wechatScanLoginEnabled: false, wechatCodeLoginEnabled: false, wechatDefaultLoginMode: 'code', wechatCodeStaticQrcodeUrl: '', captchaProvider: 'local' }
const wrappers = [], routers = [], reads = [], mutations = [], network = [], runtimeErrors = [], diagnostics = []
let settings = { ...DEFAULT_SETTINGS }, me = { ...ORDINARY }, holdMe = false
const pendingMe = []
const clone = value => JSON.parse(JSON.stringify(value)), ok = data => ({ status: 200, data: clone(data) })
function denyIO(kind) { network.push(kind); throw new Error('FORBIDDEN_PROFILE_FORM_IO_' + kind) }
function denyMutation(name) { mutations.push(name); throw new Error('FORBIDDEN_PROFILE_FORM_MUTATION_' + name) }
function read(name, value) { reads.push(name); return Promise.resolve(ok(value)) }
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath)
api.loaded = true
api.exports = {
  getCurrentUser: () => {
    if (!holdMe) return read('getCurrentUser', me)
    reads.push('getCurrentUser')
    return new Promise(resolve => pendingMe.push(value => resolve(ok(value))))
  },
  getPublicRegisterStatus: () => read('getPublicRegisterStatus', settings),
  getMyLocations: () => read('getMyLocations', {}),
  getWechatLoginStatus: () => denyIO('UNEXPECTED_WECHAT_STATUS_READ'),
  ...Object.fromEntries(['bindCurrentUserWechat', 'bindCurrentUserWechatByCode', 'createCaptchaChallenge', 'createWechatLoginQRCode', 'updateCurrentUserProfile', 'sendUserPhoneChangeSMSCode', 'unbindCurrentUserWechat', 'updateCurrentUserPhone', 'verifyCaptchaChallenge', 'verifyRemoteCaptcha'].map(name => [name, () => denyMutation(name)])),
}
require.cache[apiPath] = api
window.XMLHttpRequest.prototype.open = function () { return denyIO('XHR') }
globalThis.fetch = window.fetch = () => denyIO('FETCH')
globalThis.WebSocket = window.WebSocket = class { constructor() { denyIO('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => denyIO(name); transport.get = () => denyIO(name) }
window.addEventListener('error', event => runtimeErrors.push(String(event.error?.stack || event.message)))
window.addEventListener('unhandledrejection', event => runtimeErrors.push(String(event.reason?.stack || event.reason)))
for (const method of ['warn', 'error']) {
  const native = console[method].bind(console)
  console[method] = (...args) => { diagnostics.push({ method, text: args.map(String).join(' ') }); native(...args) }
}
const Profile = require(path.join(web, 'src/pages/Profile/index.vue'))
const LABELS = ['用户名', '昵称', '新手机号', '新手机号验证码']
const controlSelector = 'input:not([type="hidden"]),textarea,select,button[role="switch"]'
const compact = value => String(value || '').replace(/\s+/g, ' ').trim()
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function until(check, label) {
  const start = process.hrtime.bigint()
  while (Number(process.hrtime.bigint() - start) / 1e6 < 1000) {
    const value = check(); if (value) return value
    await new Promise(resolve => setTimeout(resolve, 5)); await settle()
  }
  assert.fail(label + ' monotonicElapsedMs=' + Number(process.hrtime.bigint() - start) / 1e6)
}
function visible(node) {
  if (!node?.isConnected) return false
  for (let item = node; item && item.nodeType === 1; item = item.parentElement) {
    const style = window.getComputedStyle(item)
    if (item.hidden || item.inert || item.getAttribute('aria-hidden') === 'true' || style.display === 'none' || style.visibility === 'hidden') return false
  }
  return true
}
function visibleLabels(root) { return [...root.querySelectorAll('.ant-form-item-label > label')].filter(visible) }
function field(root, text) {
  const labels = visibleLabels(root).filter(label => compact(label.textContent) === text)
  assert.equal(labels.length, 1, 'one original visible Chinese FormItem label: ' + text)
  const label = labels[0], item = label.closest('.ant-form-item')
  const controls = [...item.querySelectorAll(controlSelector)].filter(control => control.closest('.ant-form-item') === item && visible(control))
  assert.equal(controls.length, 1, 'one actual native input for label, not button/prefix/wrapper: ' + text)
  return { label, control: controls[0], item }
}
function nativeOrVisibleAriaName(control) {
  if ([...(control.labels || [])].some(label => visible(label) && compact(label.textContent))) return true
  const refs = String(control.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean)
  if (refs.some(id => { const label = document.getElementById(id); return visible(label) && compact(label.textContent) })) return true
  return Boolean(compact(control.getAttribute('aria-label')))
}
function associated(root, text) {
  const result = field(root, text), { label, control } = result
  const native = label.control === control && [...(control.labels || [])].includes(label)
  const refs = String(control.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean)
  const aria = refs.some(id => document.getElementById(id) === label && visible(label) && compact(label.textContent))
  assert.ok(native || aria, 'actual Chinese label must associate with its exact real input; name/placeholder/title/adjacency are not evidence: ' + text)
  assert.ok(control.id, 'actual control has a stable ID: ' + text)
  if (label.htmlFor) {
    assert.equal(label.htmlFor, control.id, 'label for targets native input: ' + text)
    assert.equal(document.getElementById(label.htmlFor), control, 'label for resolves own input, not another instance: ' + text)
  }
  return result
}
function allAssociated(root) {
  assert.deepEqual(visibleLabels(root).map(label => compact(label.textContent)), LABELS)
  return LABELS.map(label => associated(root, label))
}
function ids(root) { return Object.fromEntries(LABELS.map(text => [text, associated(root, text).control.id])) }
function noDuplicateIds() {
  const values = [...document.querySelectorAll('[id]')].map(node => node.id).filter(Boolean)
  assert.equal(new Set(values).size, values.length, 'all actual rendered IDs are unique in this SMS/main-fields state')
}
function button(root, text) {
  const buttons = [...root.querySelectorAll('button')].filter(node => compact(node.textContent).replace(/\s/g, '') === text.replace(/\s/g, '') && visible(node))
  assert.equal(buttons.length, 1, 'one actual text button: ' + text); return buttons[0]
}
function profileNodes(host) { return host.findAllComponents(Profile) }
async function open({ many = false, waitReady = true } = {}) {
  user.setStoredCurrentUser(ORDINARY)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/profile', component: { render: () => null } }, { path: '/ordinary-next', component: { render: () => null } }] })
  await router.push('/profile'); await router.isReady(); routers.push(router)
  const Host = defineComponent({
    setup() { return { showFirst: ref(true), showSecond: ref(false), repaint: ref(0) } },
    render() { return h('div', { 'data-rerender': this.repaint }, [this.showFirst ? h(Profile, { key: 'first' }) : null, many && this.showSecond ? h(Profile, { key: 'second' }) : null]) },
  })
  const host = mount(Host, { attachTo: document.body, global: { plugins: [Antd, router], config: { errorHandler: error => runtimeErrors.push(String(error?.stack || error)) } } })
  wrappers.push(host); await settle()
  if (waitReady) await until(() => profileNodes(host)[0]?.vm.profileReady, 'ordinary server/me confirms actual Profile')
  return { host, router, first: profileNodes(host)[0] }
}
async function addSecond(host) {
  host.vm.showSecond = true; await settle()
  await until(() => profileNodes(host)[1]?.vm.profileReady, 'second actual same-app Profile confirms server/me')
  return profileNodes(host)[1]
}
async function setInput(control, value) { control.value = value; control.dispatchEvent(new Event('input', { bubbles: true })); await settle() }
async function pastePhone(control, value) {
  const event = new Event('paste', { bubbles: true, cancelable: true })
  Object.defineProperty(event, 'clipboardData', { value: { getData: type => type === 'text' ? value : '' } })
  control.dispatchEvent(event); await settle(); assert.equal(event.defaultPrevented, true, 'real MainlandPhoneInput handles paste')
}
afterEach(async t => {
  for (const host of wrappers) if (host.exists()) host.unmount()
  wrappers.length = 0
  for (const resolve of pendingMe.splice(0)) resolve(ORDINARY)
  Swal.close(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle()
  for (const router of routers) router.options.history.destroy()
  routers.length = 0
  user.clearStoredCurrentUser(); window.sessionStorage.clear(); document.body.replaceChildren()
  const observed = { readNames: [...reads], mutationCount: mutations.length, realIOCount: network.length, runtimeErrorCount: runtimeErrors.length, diagnosticCount: diagnostics.length }
  t.diagnostic(JSON.stringify(observed))
  try {
    assert.deepEqual(mutations, [], 'per case: no SMS/captcha/QR/save/confirm/binding or other business mutation invoked')
    assert.deepEqual(network, [], 'per case: no real network/provider/SMS/Cookie/database IO')
    assert.deepEqual(runtimeErrors, [], 'per case: no hidden actual Vue/Antd/DOM runtime exception')
    assert.deepEqual(diagnostics, [], 'per case: no hidden Vue/Antd warning/error')
  } finally {
    reads.length = mutations.length = network.length = runtimeErrors.length = diagnostics.length = 0
    settings = { ...DEFAULT_SETTINGS }; me = { ...ORDINARY }; holdMe = false
  }
})

test('Profile actual four visible main labels associate with their own inputs; existing editable ordinary profile and six decimals remain', async () => {
  const { first } = await open(), root = first.element
  assert.deepEqual(reads, ['getCurrentUser', 'getPublicRegisterStatus', 'getMyLocations'])
  const fields = allAssociated(root)
  assert.ok(first.vm.profileReady); assert.equal(first.vm.currentUser.role, 'user')
  assert.ok(root.textContent.includes('普通用户')); assert.ok(root.textContent.includes('0.123456')); assert.ok(root.textContent.includes(ORDINARY.phone))
  assert.equal(first.vm.currentUser.balance, 0.123456); assert.equal(first.vm.currentUser.phone, ORDINARY.phone)
  assert.equal(fields[0].control.value, ORDINARY.username); assert.equal(fields[1].control.value, ORDINARY.nickname)
  for (const { control } of fields) { assert.equal(control.disabled, false); assert.equal(control.readOnly, false) }
  assert.equal(fields[0].control.maxLength, 255); assert.equal(fields[1].control.maxLength, 255)
  assert.equal(first.vm.phoneForm.phone, ''); assert.equal(first.vm.phoneForm.smsCode, '')
  assert.equal(button(root, '发送验证码').disabled, true); assert.equal(button(root, '确认更换手机号').disabled, true)
  noDuplicateIds()
})

test('Profile actual label clicks focus all four native enabled inputs without activating submit or SMS', async () => {
  const { first } = await open()
  for (const text of LABELS) {
    const { label, control } = associated(first.element, text)
    if (document.activeElement?.blur) document.activeElement.blur()
    label.click(); await settle()
    assert.equal(document.activeElement, control, 'actual Antd label click focuses real input: ' + text)
  }
  assert.equal(first.vm.profileForm.username, ORDINARY.username); assert.equal(first.vm.phoneForm.phone, '')
})

test('Profile username/nickname edits, +86 native phone paste and six-digit OTP preserve independent existing models', async () => {
  const { first } = await open(), root = first.element
  const initialIds = ids(root), phone = associated(root, '新手机号').control, otp = associated(root, '新手机号验证码').control
  assert.equal(phone.getAttribute('inputmode'), 'numeric'); assert.equal(phone.autocomplete, 'tel-national'); assert.equal(phone.maxLength, 11)
  assert.equal(otp.getAttribute('inputmode'), 'numeric'); assert.equal(otp.maxLength, 6)
  await setInput(associated(root, '用户名').control, '仍可编辑_A@01'); await setInput(associated(root, '昵称').control, '独立未保存昵称')
  await pastePhone(phone, '+86 138 0000 0000'); await setInput(otp, '1x234567')
  assert.equal(first.vm.profileForm.username, '仍可编辑_A@01'); assert.equal(first.vm.profileForm.nickname, '独立未保存昵称')
  assert.equal(first.vm.phoneForm.phone, '13800000000'); assert.equal(phone.value, '13800000000')
  assert.equal(first.vm.phoneForm.smsCode, '123456'); assert.equal(otp.value, '123456')
  assert.deepEqual(Object.keys(first.vm.profileForm).sort(), ['nickname', 'username'])
  assert.deepEqual(Object.keys(first.vm.phoneForm).sort(), ['captchaToken', 'phone', 'smsCode'])
  assert.equal(first.vm.phoneForm.captchaToken, ''); assert.equal(first.vm.currentUser.phone, ORDINARY.phone)
  assert.equal(button(root, '发送验证码').disabled, false); assert.equal(button(root, '确认更换手机号').disabled, false)
  assert.deepEqual(ids(root), initialIds); noDuplicateIds()
})

test('Profile same Vue app instances use unique IDs and each of four visible labels resolves only its own native control', async () => {
  const { host, first } = await open({ many: true }), firstIds = ids(first.element)
  const second = await addSecond(host), secondIds = ids(second.element)
  assert.equal(first.vm.profileReady, true); assert.equal(second.vm.profileReady, true)
  assert.equal(new Set([...Object.values(firstIds), ...Object.values(secondIds)]).size, 8)
  assert.deepEqual(ids(first.element), firstIds)
  for (const text of LABELS) {
    const left = associated(first.element, text), right = associated(second.element, text)
    assert.notEqual(left.control, right.control); assert.notEqual(left.control.id, right.control.id)
    left.label.click(); await settle(); assert.equal(document.activeElement, left.control)
    right.label.click(); await settle(); assert.equal(document.activeElement, right.control)
  }
  noDuplicateIds()
})

test('Profile same-app independent drafts and IDs survive ordinary host rerender and same-actor metadata refresh', async () => {
  const { host, first } = await open({ many: true }), second = await addSecond(host)
  const firstIds = ids(first.element), secondIds = ids(second.element)
  await setInput(associated(first.element, '昵称').control, 'A未保存昵称'); await pastePhone(associated(first.element, '新手机号').control, '13800000000')
  await setInput(associated(second.element, '昵称').control, 'B未保存昵称'); await setInput(associated(second.element, '新手机号验证码').control, '654321')
  host.vm.repaint += 1; await settle()
  user.setStoredCurrentUser({ ...ORDINARY, nickname: '服务器元数据昵称' }); await settle()
  assert.equal(first.vm.profileReady, true); assert.equal(second.vm.profileReady, true)
  assert.equal(first.vm.profileForm.nickname, 'A未保存昵称'); assert.equal(second.vm.profileForm.nickname, 'B未保存昵称')
  assert.equal(first.vm.phoneForm.phone, '13800000000'); assert.equal(second.vm.phoneForm.phone, '')
  assert.equal(first.vm.phoneForm.smsCode, ''); assert.equal(second.vm.phoneForm.smsCode, '654321')
  assert.equal(first.vm.currentUser.nickname, '服务器元数据昵称'); assert.equal(second.vm.currentUser.nickname, '服务器元数据昵称')
  assert.equal(first.vm.currentUser.balance, 0.123456); assert.equal(second.vm.currentUser.phone, ORDINARY.phone)
  assert.deepEqual(ids(first.element), firstIds); assert.deepEqual(ids(second.element), secondIds); noDuplicateIds()
})

test('Profile SMS setting branch closes and restores its actual fields without changing IDs or losing independent drafts', async () => {
  const { first } = await open(), before = ids(first.element)
  await setInput(associated(first.element, '昵称').control, '设置切换未保存昵称')
  await pastePhone(associated(first.element, '新手机号').control, '13800000000'); await setInput(associated(first.element, '新手机号验证码').control, '123456')
  first.vm.publicAuthSettings = { ...first.vm.publicAuthSettings, smsLoginEnabled: false }; await settle()
  assert.deepEqual(visibleLabels(first.element).map(label => compact(label.textContent)), ['用户名', '昵称'])
  assert.ok(first.text().includes('管理员尚未启用手机号')); assert.ok(!first.element.querySelector('input[autocomplete="tel-national"]'))
  assert.equal(first.vm.profileReady, true); assert.equal(first.vm.phoneForm.phone, '13800000000'); assert.equal(first.vm.phoneForm.smsCode, '123456')
  first.vm.publicAuthSettings = { ...first.vm.publicAuthSettings, smsLoginEnabled: true }; await settle()
  assert.deepEqual(ids(first.element), before); assert.equal(associated(first.element, '新手机号').control.value, '13800000000')
  assert.equal(associated(first.element, '新手机号验证码').control.value, '123456'); assert.equal(first.vm.profileForm.nickname, '设置切换未保存昵称'); noDuplicateIds()
})

test('Profile closing one same-app instance leaves survivor IDs/draft intact and a fresh instance cannot collide', async () => {
  const { host } = await open({ many: true }), second = await addSecond(host), survivorIds = ids(second.element)
  await setInput(associated(second.element, '昵称').control, '存活实例未保存昵称')
  host.vm.showFirst = false; await settle()
  assert.equal(profileNodes(host).length, 1); assert.deepEqual(ids(second.element), survivorIds)
  assert.equal(second.vm.profileForm.nickname, '存活实例未保存昵称'); assert.equal(second.vm.profileReady, true)
  host.vm.showFirst = true; await settle()
  await until(() => profileNodes(host)[0]?.vm.profileReady, 'replacement Profile confirms ordinary server/me')
  assert.equal(profileNodes(host).length, 2); assert.deepEqual(ids(second.element), survivorIds)
  assert.equal(second.vm.profileForm.nickname, '存活实例未保存昵称'); allAssociated(profileNodes(host)[0].element); noDuplicateIds()
})

test('Profile actual Card loading branch gates unconfirmed server/me and the completed read exposes enabled associated fields', async () => {
  holdMe = true
  const { first } = await open({ waitReady: false })
  await until(() => first.vm.publicAuthSettings.smsLoginEnabled, 'actual SMS settings read renders original phone branch')
  assert.equal(first.vm.profileReady, false)
  // Antd Card's real loading branch hides its body; do not manufacture controls.
  assert.equal(first.vm.loading, true)
  assert.equal(first.vm.profileForm.username, ''); assert.equal(first.vm.phoneForm.phone, '')
  assert.equal(pendingMe.length, 1); holdMe = false; pendingMe.shift()(ORDINARY); await settle()
  await until(() => first.vm.profileReady, 'held ordinary server/me confirms actual Profile')
  for (const { control } of allAssociated(first.element)) { assert.equal(control.disabled, false); assert.equal(control.readOnly, false) }
  assert.equal(associated(first.element, '用户名').control.value, ORDINARY.username); assert.equal(first.vm.currentUser.balance, 0.123456); noDuplicateIds()
})

test('Profile original readable static text, own-text buttons and legitimate native implicit radio labels are not false form-label gaps', async () => {
  settings = { ...DEFAULT_SETTINGS, wechatLoginEnabled: true, wechatScanLoginEnabled: true, wechatCodeLoginEnabled: true }
  const { first } = await open(), root = first.element
  allAssociated(root)
  const mainInputs = new Set(LABELS.map(text => associated(root, text).control))
  const extra = [...root.querySelectorAll(controlSelector)].filter(control => visible(control) && !mainInputs.has(control))
  assert.ok(extra.some(control => control.type === 'radio'), 'actual existing WeChat mode radios supply implicit native labels')
  for (const control of extra) assert.equal(nativeOrVisibleAriaName(control), true, 'actual non-FormItem controls may have their own native/ARIA label')
  for (const radio of extra.filter(control => control.type === 'radio')) {
    assert.ok([...radio.labels].some(label => label.contains(radio) && compact(label.textContent)), 'native implicit wrapping radio label is legitimate')
  }
  const staticRows = [...root.querySelectorAll('.profile-field')]
  assert.ok(staticRows.length >= 5)
  for (const row of staticRows) { assert.ok(compact(row.textContent)); assert.equal(row.querySelectorAll(controlSelector).length, 0, 'readable static profile text is not an unlabeled input') }
  for (const node of [...root.querySelectorAll('button')].filter(visible)) assert.ok(compact(node.textContent) || compact(node.getAttribute('aria-label')), 'own visible action text names buttons; no unrelated FormItem label is required')
  assert.equal(reads.filter(name => name === 'getCurrentUser').length, 1); noDuplicateIds()
})
