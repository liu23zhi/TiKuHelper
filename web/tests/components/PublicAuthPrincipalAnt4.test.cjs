/* Public auth native contract. Actual Vue, Antd, SweetAlert2, currentUser,
 * captcha verification/payload helpers and QR controller; API/router/SDK/socket
 * transports only are synthetic. CSS is a no-op loader, not a geometry test.
 * B/M/R use complete web copies, never a production ownership/captcha stub.
 * This covers frontend JS storage/UI only, not Set-Cookie/session ordering.
 */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), path.resolve(__dirname, '../../node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
// The app CSS chunk has no executable contract. Keep real dialog JS and DOM.
require.extensions['.css'] = module => { module.exports = {} }
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLDivElement', 'HTMLVideoElement', 'HTMLAudioElement', 'CustomEvent']) globalThis[key] = window[key]
window.scrollTo = () => {} // jsdom has no layout/scroll implementation.
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick, reactive, effectScope, watch } = require('vue')
const Antd = require('ant-design-vue')
const Swal = require('sweetalert2')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const A = { id: 75501, role: 'user', username: 'SYNTHETIC_AUTH_A' }
const B = { id: 75502, role: 'user', username: 'SYNTHETIC_AUTH_B' }
const PW = 'SYNTHETIC_PRIVATE_PASSWORD', OTP = '123456', BIND = 'SYNTHETIC_PRIVATE_BIND_TOKEN'
const PNG = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a8xQAAAAASUVORK5CYII='
const defaults = { siteName: 'SYNTHETIC AUTH', registerEnabled: true, smsLoginEnabled: true, wechatLoginEnabled: true, wechatScanLoginEnabled: true, wechatCodeLoginEnabled: true, wechatDefaultLoginMode: 'code', wechatCodeStaticQrcodeUrl: PNG, captchaProvider: 'local' }
const calls = Object.fromEntries(['settings', 'password', 'sms', 'send', 'challenge', 'verify', 'remote', 'qr', 'status', 'code', 'bind', 'me'].map(key => [key, []]))
const wrappers = [], notices = [], routes = [], errors = [], network = [], sockets = [], logs = []
let settings = { ...defaults }, activeRoute, challengeSequence = 0
function clone(value) { return value === undefined ? undefined : JSON.parse(JSON.stringify(value)) }
function transport(name, payload) {
  let yes, no
  const promise = new Promise((resolve, reject) => { yes = resolve; no = reject })
  const call = { payload: clone(payload), rawPayload: payload, completed: false, promise }
  call.resolve = value => { call.completed = true; yes(value) }
  call.reject = value => { call.completed = true; no(value) }
  calls[name].push(call)
  return promise
}
function mock(filename, exports) { const m = new Module(filename); m.loaded = true; m.exports = exports; require.cache[filename] = m }
mock(path.join(web, 'src/api/TikuComponentApi.js'), {
  getPublicRegisterStatus: () => { const p = transport('settings'); calls.settings.at(-1).resolve({ status: 200, data: clone(settings) }); return p },
  login: payload => transport('password', payload), loginBySMS: payload => transport('sms', payload), sendPublicSMSCode: payload => transport('send', payload),
  createCaptchaChallenge: payload => { const p = transport('challenge', payload); calls.challenge.at(-1).resolve({ status: 200, data: { challengeId: 'SYNTHETIC_CHALLENGE_' + (++challengeSequence), imageData: PNG } }); return p },
  verifyCaptchaChallenge: payload => transport('verify', payload), verifyRemoteCaptcha: payload => transport('remote', payload),
  createWechatLoginQRCode: payload => transport('qr', payload), getWechatLoginStatus: payload => transport('status', payload),
  loginWechatByCode: payload => transport('code', payload), bindWechatLogin: payload => transport('bind', payload), getCurrentUser: () => transport('me'),
})
mock(require.resolve('vue-router'), { useRoute: () => activeRoute, useRouter: () => ({ push: route => routes.push(clone(route)) }), onBeforeRouteLeave: () => {} })
for (const kind of ['success', 'warning', 'error']) {
  const native = Antd.message[kind].bind(Antd.message)
  Antd.message[kind] = (...args) => { notices.push({ kind, text: String(args[0]?.content || args[0] || '') }); return native(...args) }
}
function refuseNetwork(kind, args) { network.push({ kind, args: args.map(String) }); throw new Error('FORBIDDEN_REAL_AUTH_NETWORK_' + kind) }
window.XMLHttpRequest.prototype.open = function (...args) { return refuseNetwork('XHR', args) }
globalThis.fetch = window.fetch = (...args) => refuseNetwork('FETCH', args)
for (const name of ['node:http', 'node:https']) { const native = require(name); native.request = (...args) => refuseNetwork(name, args); native.get = (...args) => refuseNetwork(name, args) }
class SyntheticSocket {
  constructor(url) { this.url = url; this.readyState = 0; this.sent = []; sockets.push(this) }
  send(value) { this.sent.push(value) }
  close() { this.readyState = 3 }
  open() { this.readyState = 1; this.onopen?.({}) }
  status(payload) { this.onmessage?.({ data: JSON.stringify({ type: 'status', ...payload }) }) }
}
globalThis.WebSocket = window.WebSocket = SyntheticSocket
const Login = require(path.join(web, 'src/pages/Login/index.vue'))
const Bind = require(path.join(web, 'src/pages/WechatBind/index.vue'))
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 5)); await nextTick(); await flushPromises() }
async function open(Page = Login, actor = null, query = {}) {
  if (actor) user.setStoredCurrentUser(actor); else user.clearStoredCurrentUser()
  activeRoute = reactive({ path: Page === Login ? '/login' : '/wechat-bind', fullPath: Page === Login ? '/login' : '/wechat-bind', query: { ...(Page === Bind ? { bindToken: BIND } : {}), ...query } })
  const w = mount(Page, { attachTo: document.body, global: { plugins: [Antd], config: { errorHandler: error => errors.push(error) } } })
  wrappers.push(w); await settle(); return w
}
function button(w, text) { return w.findAll('button').find(item => item.text().replace(/\s/g, '') === text.replace(/\s/g, '')) }
function click(w, text) { const found = button(w, text); assert.ok(found, 'REAL_BUTTON_' + text); found.element.click() }
function callback(component, name) { const fn = component.vm.$.vnode.props?.[name]; assert.equal(typeof fn, 'function', 'REAL_CALLBACK_' + name); return fn }
function formCallback(w, name) { return callback(w.findComponent(Antd.Form), name) }
async function password(w, username = 'synthetic_user', secret = PW) { await w.get('input[placeholder="请输入用户名"]').setValue(username); await w.get('input[placeholder="请输入密码"]').setValue(secret) }
async function bindFields(w, username = 'synthetic_existing', secret = PW) { await w.get('input[placeholder="请输入已有账号用户名"]').setValue(username); await w.get('input[placeholder="请输入已有账号密码"]').setValue(secret) }
async function mode(w, value) { await w.get('input[value="' + value + '"]').setValue(true); await settle() }
async function beginPassword(w) { await password(w); await w.get('form').trigger('submit'); await settle() }
async function submitCaptcha(scene) {
  await settle()
  const input = document.getElementById('captcha-code-input')
  assert.ok(input, 'ACTUAL_LOCAL_CAPTCHA_DIALOG_IS_OPEN')
  input.value = '8642'; input.dispatchEvent(new Event('input', { bubbles: true })); Swal.clickConfirm(); await settle()
  const verify = calls.verify.at(-1); assert.ok(verify, 'ACTUAL_HELPER_REQUESTED_VERIFY')
  assert.deepEqual(verify.payload, { challengeId: 'SYNTHETIC_CHALLENGE_' + challengeSequence, scene, captchaCode: '8642' })
  return verify
}
async function acceptCaptcha(scene, token = 'SYNTHETIC_CAPTCHA_TOKEN') {
  const verify = await submitCaptcha(scene); verify.resolve({ status: 200, data: { captchaToken: token } }); await settle(); return verify
}
async function finish(name, account = A, statusData) {
  const call = calls[name].at(-1); assert.ok(call, 'EXPECTED_AUTH_TRANSPORT_' + name)
  call.resolve({ status: 200, data: statusData || { status: 'success', jwt: 'SYNTHETIC_NOT_REAL_JWT' } }); await settle()
  assert.equal(calls.me.length, 1, 'ONE_CURRENT_USER_READ_AFTER_AUTH')
  calls.me[0].resolve({ status: 200, data: account }); await settle()
  assert.deepEqual(user.getStoredCurrentUser(), account); assert.deepEqual(routes.at(-1), user.resolveHomePath(account))
}
const nativeConsoleLog = console.log, nativeConsoleError = console.error
console.log = (...args) => { logs.push(args); nativeConsoleLog(...args) }
console.error = (...args) => { logs.push(args); nativeConsoleError(...args) }
afterEach(async () => {
  for (const w of wrappers) if (w.exists()) w.unmount()
  wrappers.length = 0; Swal.close(); Antd.Modal.destroyAll(); Antd.message.destroy()
  for (const [name, collection] of Object.entries(calls)) for (const call of collection) if (!call.completed) call.resolve({ status: 503, message: 'SYNTHETIC_CLEANUP_' + name })
  await settle(); Swal.close(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle()
  user.clearStoredCurrentUser(); document.body.replaceChildren(); window.sessionStorage.clear()
  for (const collection of Object.values(calls)) collection.length = 0
  settings = { ...defaults }; routes.length = notices.length = sockets.length = 0; globalThis.WebSocket = window.WebSocket = SyntheticSocket; delete window.initTAC
  assert.deepEqual(network.splice(0), [], 'NO_REAL_HTTP_SMS_WECHAT_CAPTCHA_PROVIDER_IO')
  assert.deepEqual(errors.splice(0), [], 'NO_HIDDEN_NATIVE_VUE_ANTD_RUNTIME_ERRORS')
  logs.length = 0
})

test('PUBLIC_AUTH existing anonymous password uses actual local captcha verification then exact login and own ordinary-user commit', async () => {
  const w = await open(); await beginPassword(w)
  assert.equal(calls.password.length, 0, 'NO_LOGIN_BEFORE_CAPTCHA_VERIFY')
  assert.deepEqual(calls.challenge[0].payload, { scene: 'login_password' })
  await acceptCaptcha('login_password')
  assert.deepEqual(calls.password.map(x => x.payload), [{ username: 'synthetic_user', password: PW, captchaToken: 'SYNTHETIC_CAPTCHA_TOKEN' }])
  await finish('password'); assert.ok(notices.some(x => x.kind === 'success')); assert.equal(w.vm.submitting, false)
})

test('PUBLIC_AUTH existing SMS 202 starts cooldown without false success and real second captcha retains SMS login payload', async () => {
  const w = await open(); await mode(w, 'sms')
  await w.get('input[autocomplete="tel-national"]').setValue('13800000000'); click(w, '发送验证码'); await settle(); await acceptCaptcha('sms_send_login', 'SYNTHETIC_SEND_CAPTCHA')
  assert.deepEqual(calls.send[0].payload, { phone: '13800000000', scene: 'login', captchaToken: 'SYNTHETIC_SEND_CAPTCHA' })
  calls.send[0].resolve({ status: 202, message: 'SYNTHETIC_UNKNOWN_SMS_RESULT' }); await settle()
  assert.equal(w.vm.smsCooldownActive, true); assert.ok(notices.some(x => x.kind === 'warning' && x.text.includes('待核对')))
  assert.equal(notices.some(x => x.kind === 'success'), false)
  const cooldown = w.findAll('button').find(item => item.text().includes('后重试')); assert.ok(cooldown); assert.equal(cooldown.element.disabled, true)
  await w.get('input[placeholder="请输入验证码"]').setValue(OTP); await w.get('form').trigger('submit'); await settle(); await acceptCaptcha('login_sms', 'SYNTHETIC_LOGIN_CAPTCHA')
  assert.deepEqual(calls.sms[0].payload, { phone: '13800000000', smsCode: OTP, captchaToken: 'SYNTHETIC_LOGIN_CAPTCHA' }); await finish('sms')
})

test('PUBLIC_AUTH existing native WeChat code normalizes and logs into an ordinary user without captcha bypass', async () => {
  const w = await open(); await mode(w, 'wechat'); await w.get('input[placeholder="请输入微信验证码"]').setValue('x123456')
  click(w, '微信验证码登录'); await settle(); assert.deepEqual(calls.code[0].payload, { code: OTP }); assert.equal(calls.challenge.length, 0)
  await finish('code'); assert.equal(w.vm.wechatCodeSubmitting, false)
})

test('PUBLIC_AUTH existing WeChat need_bind keeps exact route token and registration remains available', async () => {
  const w = await open(); await mode(w, 'wechat'); await w.get('input[placeholder="请输入微信验证码"]').setValue(OTP); click(w, '微信验证码登录'); await settle()
  calls.code[0].resolve({ status: 200, data: { status: 'need_bind', bindToken: BIND } }); await settle()
  assert.deepEqual(routes[0], { path: '/wechat-bind', query: { bindToken: BIND } }); assert.equal(calls.me.length, 0)
  click(w, '前往注册页'); await settle(); assert.equal(routes.at(-1), '/register')
})

test('PUBLIC_AUTH existing anonymous native bind submits exact credentials/token and own successful commit navigates once', async () => {
  const w = await open(Bind, null, { bindToken: '  ' + BIND + '  ' }); await bindFields(w); await w.get('form').trigger('submit'); await settle()
  assert.deepEqual(calls.bind.map(x => x.payload), [{ bindToken: BIND, username: 'synthetic_existing', password: PW }]); await finish('bind')
  assert.equal(routes.length, 1); assert.ok(notices.some(x => x.kind === 'success' && x.text.includes('绑定成功'))); assert.equal(w.vm.submitting, false)
})

test('PUBLIC_AUTH existing missing bind token and empty native form cannot bind and return-login route remains usable', async () => {
  const w = await open(Bind, null, { bindToken: '' }); assert.ok(w.text().includes('绑定令牌无效或已过期')); assert.equal(button(w, '绑定并登录').element.disabled, true)
  await w.get('form').trigger('submit'); await settle(); assert.ok(w.text().includes('请输入用户名')); assert.ok(w.text().includes('请输入密码'))
  w.vm.handleBind(); await settle(); assert.equal(calls.bind.length, 0); click(w, '返回登录页'); await settle(); assert.deepEqual(routes, ['/login'])
})

test('PUBLIC_AUTH existing unknown identity and unavailable storage do not gate the first anonymous password request', async () => {
  const original = Object.getOwnPropertyDescriptor(window, 'localStorage')
  for (const actor of [{ role: 'user' }, { id: 'unknown', role: 'user' }, null]) {
    const w = await open(Login, actor)
    if (!actor) Object.defineProperty(window, 'localStorage', { configurable: true, get() { throw new Error('SYNTHETIC_STORAGE_UNAVAILABLE') } })
    try {
      const before = calls.password.length; await beginPassword(w); await acceptCaptcha('login_password')
      assert.equal(calls.password.length, before + 1, 'PUBLIC_AUTH_HAS_NO_ADMIN_OR_POSITIVE_ID_GATE')
      calls.password.at(-1).resolve({ status: 400, message: 'SYNTHETIC_AUTH_REJECT' }); await settle()
    } finally { Object.defineProperty(window, 'localStorage', original); w.unmount() }
  }
})

async function beginAuth(w, Page) {
  if (Page === Bind) { await bindFields(w); await w.get('form').trigger('submit') }
  else { await mode(w, 'wechat'); await w.get('input[placeholder="请输入微信验证码"]').setValue(OTP); click(w, '微信验证码登录') }
  await settle(); return calls[Page === Bind ? 'bind' : 'code'].at(-1)
}
function busy(w, Page) { return Page === Bind ? w.vm.submitting : w.vm.wechatCodeSubmitting }
for (const [label, Page, endpoint] of [['Login', Login, 'code'], ['WechatBind', Bind, 'bind']]) {
  test(`PUBLIC_AUTH ${label} late current-user result cannot overwrite externally selected B or navigate it`, async () => {
    const w = await open(Page), auth = await beginAuth(w, Page)
    auth.resolve({ status: 200, data: { status: 'success', jwt: 'SYNTHETIC_NOT_REAL_JWT' } }); await settle(); assert.equal(calls.me.length, 1)
    user.setStoredCurrentUser(B); const count = notices.length
    calls.me[0].resolve({ status: 200, data: A }); await settle()
    assert.deepEqual(user.getStoredCurrentUser(), B, 'LATE_ME_MUST_NOT_REPLACE_B'); assert.deepEqual(routes, []); assert.equal(notices.length, count)
  })
  test(`PUBLIC_AUTH ${label} A-B-A permanently retires old issued authentication and cannot begin an old me read`, async () => {
    const w = await open(Page, A), auth = await beginAuth(w, Page)
    user.setStoredCurrentUser(B); user.setStoredCurrentUser(A); const count = notices.length
    auth.resolve({ status: 200, data: { status: 'success', jwt: 'SYNTHETIC_NOT_REAL_JWT' } }); await settle()
    assert.equal(calls[endpoint].length, 1); assert.equal(calls.me.length, 0, 'ABA_RETIRED_AUTH_CANNOT_START_NEW_CHAIN'); assert.deepEqual(routes, []); assert.equal(notices.length, count)
  })
  test(`PUBLIC_AUTH ${label} unmount retires pending me without storage or navigation success`, async () => {
    const w = await open(Page), auth = await beginAuth(w, Page)
    auth.resolve({ status: 200, data: { status: 'success', jwt: 'SYNTHETIC_NOT_REAL_JWT' } }); await settle(); assert.equal(calls.me.length, 1)
    w.unmount(); const count = notices.length; calls.me[0].resolve({ status: 200, data: A }); await settle()
    assert.equal(user.getStoredCurrentUser(), null); assert.deepEqual(routes, []); assert.equal(notices.length, count)
  })
  test(`PUBLIC_AUTH ${label} duplicate native submit is one request and old finally cannot release newer B busy`, async () => {
    const w = await open(Page), old = await beginAuth(w, Page)
    if (Page === Bind) formCallback(w, 'onFinish')(); else callback(w.findAllComponents(Antd.Button).find(item => item.text().replace(/\s/g, '') === '微信验证码登录'), 'onClick')()
    await settle(); assert.equal(calls[endpoint].length, 1, 'DUPLICATE_PENDING_AUTH_IS_NOT_REISSUED')
    user.setStoredCurrentUser(B); await settle(); if (Page === Bind) { activeRoute.query.bindToken = 'SYNTHETIC_FRESH_B_BIND_TOKEN'; await settle() } const current = await beginAuth(w, Page); assert.equal(calls[endpoint].length, 2); assert.equal(busy(w, Page), true)
    const count = notices.length; old.resolve({ status: 400, message: 'SYNTHETIC_RETIRED_FAILURE' }); await settle()
    assert.equal(busy(w, Page), true, 'OLD_FINALLY_CANNOT_RELEASE_CURRENT_BUSY'); assert.equal(notices.length, count)
    current.resolve({ status: 400, message: 'SYNTHETIC_CURRENT_FAILURE' }); await settle(); assert.equal(busy(w, Page), false)
  })
  test(`PUBLIC_AUTH ${label} external session synchronously erases typed secret DOM and old native form handlers cannot act on returned A`, async () => {
    const w = await open(Page, A)
    if (Page === Bind) await bindFields(w); else await password(w)
    const secret = w.get('input[type="password"]').element, toggle = w.find(Page === Login ? '[data-auth-password-toggle]' : '.ant-input-password-icon'), oldFinish = formCallback(w, 'onFinish'), oldFailed = formCallback(w, 'onFinishFailed'), oldValidate = Page === Login ? formCallback(w, 'onValidate') : null
    user.setStoredCurrentUser(B); assert.equal(secret.value, '', 'PRIVATE_NATIVE_VALUE_CLEARED_BEFORE_QUEUED_RENDER'); if (toggle.exists()) toggle.element.click(); assert.equal(secret.value, '', 'RETAINED_NATIVE_PASSWORD_TOGGLE_CANNOT_REVIVE_SECRET'); await settle(); assert.equal(secret.value, '', 'SECRET_STAYS_EMPTY_AFTER_CONTROLLED_RENDER'); user.setStoredCurrentUser(A); await settle()
    if (Page === Bind) await bindFields(w, 'returned_a', 'SYNTHETIC_NEW_PASSWORD'); else await password(w, 'returned_a', 'SYNTHETIC_NEW_PASSWORD')
    const focus = document.createElement('button'); document.body.appendChild(focus); focus.focus(); const before = notices.length
    oldFinish(); oldValidate?.('username', false, ['SYNTHETIC_STALE_VALIDATION']); oldFailed({ errorFields: [{ name: ['username'], errors: ['SYNTHETIC_STALE_VALIDATION'] }], values: { password: PW } }); await settle()
    assert.equal(calls[endpoint].length + calls.password.length + calls.challenge.length, 0, 'RETIRED_NATIVE_CALLBACK_HAS_NO_NEW_IO')
    assert.equal(document.activeElement, focus); assert.equal(notices.length, before); assert.equal(w.text().includes('SYNTHETIC_STALE_VALIDATION'), false)
    assert.equal(JSON.stringify(logs).includes(PW), false, 'NO_RAW_PASSWORD_IN_VALIDATION_CONSOLE')
  })
  test(`PUBLIC_AUTH ${label} owned non200 and network failure retain appropriate input without false success or raw credential console`, async () => {
    const w = await open(Page), first = await beginAuth(w, Page)
    first.resolve({ status: 400, message: 'SYNTHETIC_CURRENT_REJECT' }); await settle(); assert.equal(busy(w, Page), false)
    assert.equal(Page === Bind ? w.vm.bindForm.password : w.vm.wechatCodeForm.code, Page === Bind ? PW : OTP)
    const second = await beginAuth(w, Page); second.reject(new Error('SYNTHETIC_CURRENT_NETWORK_FAILURE')); await settle()
    assert.equal(Page === Bind ? w.vm.bindForm.password : w.vm.wechatCodeForm.code, Page === Bind ? PW : OTP)
    assert.equal(calls.me.length, 0); assert.deepEqual(routes, []); assert.equal(notices.some(x => x.kind === 'success'), false)
    assert.equal(JSON.stringify(logs).includes(PW), false); assert.equal(JSON.stringify(logs).includes(BIND), false); assert.equal(JSON.stringify(logs).includes(OTP), false)
  })
}

test('PUBLIC_AUTH password payload is an immutable issued snapshot and later normal input is safe', async () => {
  const w = await open(); await beginPassword(w); await acceptCaptcha('login_password'); const issued = calls.password[0]
  await password(w, 'new_username', 'SYNTHETIC_NEW_PASSWORD'); await settle()
  assert.deepEqual(clone(issued.rawPayload), issued.payload, 'ISSUED_PAYLOAD_CANNOT_ALIAS_REACTIVE_FORM')
  issued.resolve({ status: 200 }); await settle(); assert.equal(calls.me.length, 0, 'EDITED_FORM_RETIRES_OLD_AUTH_CHAIN')
  assert.equal(w.get('input[placeholder="请输入密码"]').element.value, 'SYNTHETIC_NEW_PASSWORD')
})

test('PUBLIC_AUTH pending real captcha verification followed by mode change cannot issue a login of either mode', async () => {
  const w = await open(); await beginPassword(w); const verify = await submitCaptcha('login_password')
  await mode(w, 'sms'); verify.resolve({ status: 200, data: { captchaToken: 'SYNTHETIC_RETIRED_CAPTCHA' } }); await settle()
  assert.equal(calls.password.length + calls.sms.length + calls.send.length, 0); assert.equal(w.vm.loginMode, 'sms'); assert.equal(w.vm.submitting, false)
})

test('PUBLIC_AUTH retired native captcha refresh cannot request more challenges or close an unrelated popup', async () => {
  const w = await open(); await beginPassword(w); const refresh = document.getElementById('captcha-refresh-trigger'); assert.ok(refresh)
  const other = Swal.fire({ title: 'SYNTHETIC_OTHER_POPUP', text: 'Unrelated owner', heightAuto: false, scrollbarPadding: false })
  await settle(); const before = calls.challenge.length; user.setStoredCurrentUser(B); refresh.click(); await settle()
  assert.equal(calls.challenge.length, before); assert.equal(Swal.getTitle()?.textContent, 'SYNTHETIC_OTHER_POPUP', 'RETIRED_CAPTCHA_ONLY_CLOSES_ITS_OWN_POPUP')
  assert.equal(calls.password.length, 0); Swal.close(); await other
})

test('PUBLIC_AUTH real local captcha cancellation and rejected verify preserve current credentials without bypass', async () => {
  const w = await open(); await beginPassword(w); Swal.clickCancel(); await settle()
  assert.equal(calls.password.length + calls.verify.length, 0); assert.equal(w.vm.loginForm.password, PW); assert.equal(w.vm.submitting, false)
  await w.get('form').trigger('submit'); await settle(); const verify = await submitCaptcha('login_password'); verify.resolve({ status: 400, message: 'SYNTHETIC_CAPTCHA_REJECT' }); await settle()
  assert.equal(calls.password.length, 0); assert.equal(w.vm.loginForm.password, PW); assert.equal(w.vm.loginForm.username, 'synthetic_user'); assert.equal(w.vm.submitting, false)
})

test('PUBLIC_AUTH route change retires pending password captcha before new authentication IO', async () => {
  const w = await open(); await beginPassword(w); const verify = await submitCaptcha('login_password')
  activeRoute.path = '/register'; activeRoute.fullPath = '/register'; verify.resolve({ status: 200, data: { captchaToken: 'SYNTHETIC_ROUTE_RETIRED_CAPTCHA' } }); await settle()
  assert.equal(calls.password.length + calls.sms.length, 0); assert.equal(routes.length, 0)
})

test('PUBLIC_AUTH bind route token replacement retires issued old token and old native return handler', async () => {
  const w = await open(Bind); const returnButton = w.findAllComponents(Antd.Button).find(item => item.text().includes('返回登录页')), oldBack = callback(returnButton, 'onClick')
  const issued = await beginAuth(w, Bind); activeRoute.query.bindToken = 'SYNTHETIC_NEW_BIND_TOKEN'; await settle()
  const before = notices.length; issued.resolve({ status: 200 }); oldBack(); await settle(); assert.equal(calls.me.length, 0); assert.deepEqual(routes, []); assert.equal(notices.length, before)
  assert.equal(w.vm.bindToken, 'SYNTHETIC_NEW_BIND_TOKEN'); await bindFields(w); await w.get('form').trigger('submit'); await settle()
  assert.equal(calls.bind.length, 2); assert.equal(calls.bind[1].payload.bindToken, 'SYNTHETIC_NEW_BIND_TOKEN')
})

test('PUBLIC_AUTH stale SMS send result cannot start cooldown in another mode or publish a false result', async () => {
  const w = await open(); await mode(w, 'sms'); await w.get('input[autocomplete="tel-national"]').setValue('13800000000'); click(w, '发送验证码'); await settle(); await acceptCaptcha('sms_send_login')
  await mode(w, 'password'); const count = notices.length; calls.send[0].resolve({ status: 202 }); await settle()
  assert.equal(w.vm.smsCooldownActive, false); assert.equal(w.vm.sendingLoginCode, false); assert.equal(notices.length, count); assert.equal(calls.password.length, 0)
})

test('PUBLIC_AUTH old QR creation and captured native WebSocket handler cannot resume a newer login flow', async () => {
  settings.wechatDefaultLoginMode = 'scan'; const w = await open(); await mode(w, 'wechat'); assert.equal(calls.qr.length, 1)
  calls.qr[0].resolve({ status: 200, data: { loginToken: 'SYNTHETIC_QR_A', qrcodeImageData: PNG, expireSeconds: 180 } }); await settle(); assert.equal(sockets.length, 1)
  const oldMessage = sockets[0].onmessage; sockets[0].open(); click(w, '刷新二维码'); await settle(); assert.equal(calls.qr.length, 2)
  calls.qr[1].resolve({ status: 200, data: { loginToken: 'SYNTHETIC_QR_B', qrcodeImageData: PNG, expireSeconds: 180 } }); await settle()
  const before = notices.length; oldMessage({ data: JSON.stringify({ type: 'status', status: 'success', jwt: 'SYNTHETIC_OLD_JWT', remainingSeconds: 9 }) }); await settle()
  assert.equal(calls.me.length, 0); assert.equal(w.vm.wechatRemainingSeconds, 180); assert.equal(notices.length, before)
  await mode(w, 'password'); sockets.at(-1).status({ status: 'need_bind', bindToken: BIND }); await settle(); assert.deepEqual(routes, [])
})

test('PUBLIC_AUTH delayed QR create after mode departure creates no socket and does not retain an image', async () => {
  settings.wechatDefaultLoginMode = 'scan'; const w = await open(); await mode(w, 'wechat'); assert.equal(calls.qr.length, 1)
  await mode(w, 'password'); calls.qr[0].resolve({ status: 200, data: { loginToken: 'SYNTHETIC_LATE_QR', qrcodeImageData: PNG, expireSeconds: 180 } }); await settle()
  assert.equal(sockets.length, 0); assert.equal(w.vm.wechatQrCodeImageData, ''); assert.equal(w.vm.wechatQrLoading, false); assert.equal(calls.me.length, 0)
})

test('PUBLIC_AUTH real remote captcha SDK success from retired mode cannot start remote verification or login', async () => {
  settings.captchaProvider = 'remote'; let options
  window.initTAC = (_path, config) => { options = config; return Promise.resolve({ init() {}, destroyWindow() {} }) }
  const w = await open(); await beginPassword(w); assert.ok(options, 'ACTUAL_REMOTE_CAPTCHA_HELPER_MOUNTED_SYNTHETIC_SDK')
  await mode(w, 'sms'); options.validSuccess({ id: 'SYNTHETIC_RETIRED_CAPTCHA_ID' }, null, { destroyWindow() {} }); await settle()
  assert.equal(calls.remote.length + calls.password.length + calls.sms.length, 0); assert.equal(document.querySelector('[id^="remote-captcha-box-"]'), null)
})

for (const [label, Page] of [['Login', Login], ['WechatBind', Bind]]) {
  test(`PUBLIC_AUTH ${label} unavailable storage still issues public auth but cannot claim saved-session success`, async () => {
    const w = await open(Page), original = Object.getOwnPropertyDescriptor(window, 'localStorage')
    Object.defineProperty(window, 'localStorage', { configurable: true, get() { throw new Error('SYNTHETIC_BLOCKED_STORAGE') } })
    try {
      const issued = await beginAuth(w, Page); assert.ok(issued, 'BLOCKED_STORAGE_DOES_NOT_GATE_FIRST_PUBLIC_AUTH')
      issued.resolve({ status: 200, data: { status: 'success', jwt: 'SYNTHETIC_NOT_REAL_JWT' } }); await settle(); assert.equal(calls.me.length, 1)
      calls.me[0].resolve({ status: 200, data: A }); await settle()
      assert.deepEqual(routes, []); assert.equal(notices.some(x => x.kind === 'success'), false, 'NO_FAKE_STORAGE_COMMIT_SUCCESS'); assert.equal(busy(w, Page), false)
    } finally { Object.defineProperty(window, 'localStorage', original) }
  })
  test(`PUBLIC_AUTH ${label} synchronous external B during own storage commit prevents blind navigation or false success`, async () => {
    const w = await open(Page), scope = effectScope(); let changed = false
    scope.run(() => { const display = user.useCurrentUserDisplay(); watch(display.currentUser, value => { if (!changed && value?.id === A.id) { changed = true; user.setStoredCurrentUser(B) } }, { flush: 'sync' }) })
    try {
      const issued = await beginAuth(w, Page); issued.resolve({ status: 200, data: { status: 'success', jwt: 'SYNTHETIC_NOT_REAL_JWT' } }); await settle()
      calls.me[0].resolve({ status: 200, data: A }); await settle(); assert.equal(changed, true)
      assert.deepEqual(user.getStoredCurrentUser(), B); assert.deepEqual(routes, []); assert.equal(notices.some(x => x.kind === 'success'), false)
    } finally { scope.stop() }
  })
}

test('PUBLIC_AUTH real QR helper preserves polling/recovery/countdown budgets and rejects old in-flight poll after stop/restart', async () => {
  const { createWechatQrStatusController } = require(path.join(web, 'src/utils/wechatQrStatus.mjs'))
  const timers = new Map(), issuedTimers = [], events = []; let sequence = 0, socketAttempts = 0
  const controller = createWechatQrStatusController({
    checkStatus: () => transport('status', { syntheticControlledPoll: true }),
    onStatus: (status, payload) => events.push({ status, payload: clone(payload) }),
    createWebSocket: () => { socketAttempts += 1; throw new Error('SYNTHETIC_WS_UNAVAILABLE') },
    setIntervalFn: (fn, ms) => { const id = ++sequence; timers.set(id, fn); issuedTimers.push({ id, fn, ms }); return id },
    clearIntervalFn: id => timers.delete(id), now: () => 100000,
  })
  try {
    controller.start({ loginToken: 'SYNTHETIC_POLL_A', expireSeconds: 999 }); assert.equal(socketAttempts, 4, 'ORIGINAL_THREE_RECONNECTS_PLUS_INITIAL_ATTEMPT')
    assert.deepEqual(issuedTimers.map(x => x.ms), [2000, 30000, 1000]); const old = calls.status[0], oldTimer = issuedTimers[0].fn
    controller.stop(); controller.start({ loginToken: 'SYNTHETIC_POLL_B', expireSeconds: 180 }); assert.equal(calls.status.length, 2)
    old.resolve({ status: 200, data: { status: 'success', jwt: 'SYNTHETIC_OLD_POLL_JWT' } }); await settle()
    assert.deepEqual(events, [], 'OLD_POLL_RESULT_CANNOT_EMIT_IN_NEW_QR_GENERATION')
    oldTimer(); await settle(); assert.equal(calls.status.length, 2, 'OLD_TIMER_CANNOT_ISSUE_NEW_POLL_AFTER_RESTART')
    calls.status[1].resolve({ status: 200, data: { status: 'pending' } }); await settle(); assert.deepEqual(events, [{ status: 'pending', payload: { status: 'pending' } }])
  } finally { controller.stop() }
})

test('PUBLIC_AUTH current remote SDK must use actual helper verification before password login', async () => {
  settings.captchaProvider = 'remote'; let options
  window.initTAC = (_path, config) => { options = config; return Promise.resolve({ init() {}, destroyWindow() {} }) }
  const w = await open(); await beginPassword(w); assert.ok(options); assert.equal(calls.password.length, 0)
  options.validSuccess({ captchaId: 'SYNTHETIC_CURRENT_REMOTE_ID' }, null, { destroyWindow() {} }); await settle()
  assert.deepEqual(calls.remote[0].payload, { scene: 'login_password', captchaId: 'SYNTHETIC_CURRENT_REMOTE_ID' }); assert.equal(calls.password.length, 0)
  calls.remote[0].resolve({ status: 200, data: { captchaToken: 'SYNTHETIC_VERIFIED_REMOTE_TOKEN' } }); await settle()
  assert.equal(calls.password[0].payload.captchaToken, 'SYNTHETIC_VERIFIED_REMOTE_TOKEN'); assert.equal(calls.challenge.length, 0); await finish('password')
})

test('PUBLIC_AUTH current response and rejected error never echo raw password SMS code or bind token and unknown bind outcome stays unconfirmed', async () => {
  const raw = 'SYNTHETIC_PASSWORD SYNTHETIC_SMS_CODE SYNTHETIC_BIND_TOKEN'
  const observations = []
  const observe = () => observations.push({ notices: clone(notices), text: document.body.textContent, logs: require('node:util').inspect(logs, { depth: 5 }) })
  const assertNoEcho = () => {
    const visible = JSON.stringify(observations)
    for (const secret of raw.split(' ')) assert.equal(visible.includes(secret), false, 'UNTRUSTED_RESPONSE_MUST_NOT_ECHO_' + secret)
  }
  const w = await open(); await beginPassword(w); await acceptCaptcha('login_password')
  calls.password[0].resolve({ status: 400, message: raw }); await settle(); observe(); assert.equal(w.vm.loginForm.password, PW)
  await w.get('form').trigger('submit'); await settle(); await acceptCaptcha('login_password')
  const unknownLogin = new Error(raw); unknownLogin.code = 'UNKNOWN_OUTCOME'; calls.password[1].reject(unknownLogin); await settle()
  observe(); const unknownLoginText = notices.at(-1).text; assert.equal(w.vm.loginForm.password, PW); assert.equal(calls.password.length, 2)
  await mode(w, 'sms'); await w.get('input[autocomplete="tel-national"]').setValue('13800000000'); click(w, '发送验证码'); await settle(); await acceptCaptcha('sms_send_login')
  calls.send[0].resolve({ status: 200, message: raw }); await settle(); observe(); assert.equal(w.vm.smsCooldownActive, true)
  await mode(w, 'wechat'); await w.get('input[value="scan"]').setValue(true); await settle()
  calls.qr[0].resolve({ status: 400, message: raw }); await settle(); observe(); const qrStatusText = w.vm.wechatLoginStatusText
  click(w, '刷新二维码'); await settle(); calls.qr[1].reject(new Error(raw)); await settle(); observe()
  w.unmount()
  const bind = await open(Bind); await bindFields(bind); await bind.get('form').trigger('submit'); await settle()
  calls.bind[0].resolve({ status: 400, message: raw }); await settle(); observe(); assert.equal(bind.vm.bindForm.password, PW)
  await bind.get('form').trigger('submit'); await settle(); const unknownBind = new Error(raw); unknownBind.code = 'UNKNOWN_OUTCOME'; calls.bind[1].reject(unknownBind); await settle()
  observe(); assert.match(notices.at(-1).text, /尚未确认|待核对|未确认/); assert.equal(bind.vm.bindForm.password, PW); assert.equal(calls.bind.length, 2)
  assert.match(unknownLoginText, /核对|确认/); assert.equal(qrStatusText.includes('SYNTHETIC_'), false); assertNoEcho(); assert.equal(calls.me.length, 0); assert.deepEqual(routes, [])
})


test('PUBLIC_AUTH fresh unregistered SMS alert owns its exact popup, native registration action and queued confirmation retirement', async () => {
 for (const kind of ['current', 'retired', 'queued-confirm', 'foreign']) {
  const w = await open(), start = calls.sms.length
  await mode(w, 'sms'); await w.get('input[autocomplete="tel-national"]').setValue('13800000000')
  await w.get('input[placeholder="请输入验证码"]').setValue(OTP); await w.get('form').trigger('submit'); await settle(); await acceptCaptcha('login_sms')
  assert.equal(calls.sms.length, start + 1, kind + ' only one actually captcha-verified SMS login')
  calls.sms.at(-1).resolve({ status: 400, message: '手机号未绑定账号' }); await settle()
  const popup = Swal.getPopup(), confirm = Swal.getConfirmButton(), count = routes.length
  assert.ok(popup && confirm && confirm.isConnected); assert.equal(Swal.getTitle().textContent, '手机号未注册')
  assert.equal(confirm.textContent, '前往注册'); assert.equal(calls.me.length, 0)
  if (kind === 'current') { confirm.click(); await settle(); assert.equal(routes.length, count + 1); assert.equal(routes.at(-1), '/register') }
  else if (kind === 'foreign') {
   void Swal.fire({ title: 'SYNTHETIC_LATER_FOREIGN_POPUP', confirmButtonText: '保持' }); await settle()
   const foreign = Swal.getPopup(); assert.notEqual(foreign, popup)
   user.setStoredCurrentUser(B); await settle(); assert.equal(Swal.getPopup(), foreign, 'retiring SMS owner never closes a later foreign popup'); assert.equal(routes.length, count); Swal.close()
  } else {
   if (kind === 'queued-confirm') confirm.click()
   user.setStoredCurrentUser(B); await settle()
   assert.notEqual(Swal.getPopup(), popup, kind + ' retired popup closes synchronously through its actual owner signal')
   assert.equal(routes.length, count, kind + ' accepted/queued old confirmation cannot navigate a new identity')
  }
  assert.equal(calls.sms.length, start + 1, kind + ' popup cannot repeat login'); w.unmount(); await settle()
 }
 assert.equal(calls.sms.length, 4); assert.equal(calls.password.length + calls.bind.length + calls.me.length, 0)
})
