/* Register/Profile frontend contract. Actual Vue, Antd, SweetAlert2, currentUser,
 * captcha verification/payload helpers, QR controller and registration coordinator.
 * Only API/router/SDK/socket/BroadcastChannel transports and CSS loading are synthetic.
 * B/M/R select complete web sources; no production owner/coordinator/captcha stub.
 * JS UI/storage evidence is not provider, SMS, Cookie, SDK or database acceptance.
 */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), path.resolve(__dirname, '../../node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLDivElement', 'HTMLVideoElement', 'HTMLAudioElement', 'CustomEvent']) globalThis[key] = window[key]
window.scrollTo = () => {} // jsdom has no layout; not geometric/visual acceptance.
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick, reactive } = require('vue')
const Antd = require('ant-design-vue'), Swal = require('sweetalert2')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const A = { id: 76701, role: 'user', status: 'enabled', username: 'SYNTHETIC_PROFILE_A', nickname: '原昵称', balance: 0.123456, wechatId: '', phone: '13900000000', lastLoginIp: '192.0.2.7' }
const B = { id: 76702, role: 'user', status: 'enabled', username: 'SYNTHETIC_PROFILE_B', nickname: 'B昵称', balance: 0 }
const PW = 'SyntheticPrivate!Register01', OTP = '123456', TOKEN = 'SYNTHETIC_PRIVATE_CAPTCHA_TOKEN', FLOW = 'SYNTHETIC_PRIVATE_REGISTRATION_FLOW'
const PNG = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a8xQAAAAASUVORK5CYII='
const defaults = { siteName: 'SYNTHETIC AUTH', registerEnabled: true, smsLoginEnabled: true, wechatLoginEnabled: true, wechatScanLoginEnabled: true, wechatCodeLoginEnabled: true, wechatDefaultLoginMode: 'code', wechatCodeStaticQrcodeUrl: PNG, captchaProvider: 'local' }
const names = ['settings', 'me', 'locations', 'challenge', 'verify', 'remote', 'register', 'registerSMS', 'phoneSMS', 'profile', 'phone', 'flow', 'flowQR', 'flowStatus', 'stage', 'release', 'qr', 'status', 'codeBind', 'bind', 'unbind']
const calls = Object.fromEntries(names.map(key => [key, []]))
const wrappers = [], notices = [], routes = [], errors = [], network = [], sockets = [], logs = [], channels = [], routeLeaves = []
let settings = { ...defaults }, meReply = A, locationsReply = {}, activeRoute, challengeSequence = 0
function clone(value) { return value === undefined ? undefined : JSON.parse(JSON.stringify(value)) }
function transport(name, payload) {
  let yes, no
  const promise = new Promise((resolve, reject) => { yes = resolve; no = reject })
  const call = { payload: clone(payload), rawPayload: payload, selectedAtIssue: user.getStoredCurrentUser()?.id, completed: false, promise }
  call.resolve = value => { call.completed = true; yes(value) }; call.reject = value => { call.completed = true; no(value) }
  calls[name].push(call); return promise
}
function immediate(name, payload, response) { const p = transport(name, payload); calls[name].at(-1).resolve(response); return p }
function mock(filename, exports) { const m = new Module(filename); m.loaded = true; m.exports = exports; require.cache[filename] = m }
mock(path.join(web, 'src/api/TikuComponentApi.js'), {
  getPublicRegisterStatus: () => immediate('settings', undefined, { status: 200, data: clone(settings) }),
  getCurrentUser: () => meReply === null ? transport('me') : immediate('me', undefined, { status: 200, data: clone(meReply) }),
  getMyLocations: () => locationsReply === null ? transport('locations') : immediate('locations', undefined, { status: 200, data: clone(locationsReply) }),
  createCaptchaChallenge: payload => immediate('challenge', payload, { status: 200, data: { challengeId: 'SYNTHETIC_CHALLENGE_' + (++challengeSequence), imageData: PNG } }),
  verifyCaptchaChallenge: payload => transport('verify', payload), verifyRemoteCaptcha: payload => transport('remote', payload),
  registerUser: payload => transport('register', payload), sendPublicSMSCode: payload => transport('registerSMS', payload),
  sendUserPhoneChangeSMSCode: payload => transport('phoneSMS', payload), updateCurrentUserProfile: payload => transport('profile', payload), updateCurrentUserPhone: payload => transport('phone', payload),
  createWechatRegisterSession: payload => transport('flow', payload), createWechatRegisterQRCode: payload => transport('flowQR', payload), getWechatRegisterStatus: payload => transport('flowStatus', payload), stageWechatRegisterCode: payload => transport('stage', payload), releaseWechatRegisterFlow: payload => transport('release', payload),
  createWechatLoginQRCode: payload => transport('qr', payload), getWechatLoginStatus: payload => transport('status', payload), bindCurrentUserWechatByCode: payload => transport('codeBind', payload), bindCurrentUserWechat: payload => transport('bind', payload), unbindCurrentUserWechat: payload => transport('unbind', payload),
})
mock(require.resolve('vue-router'), { useRoute: () => activeRoute, useRouter: () => ({ currentRoute: { get value() { return activeRoute } }, push: value => { routes.push(clone(value)); return Promise.resolve() } }), onBeforeRouteLeave: fn => routeLeaves.push(fn) })
for (const kind of ['success', 'warning', 'error']) { const native = Antd.message[kind].bind(Antd.message); Antd.message[kind] = (...args) => { notices.push({ kind, text: String(args[0]?.content || args[0] || '') }); return native(...args) } }
function deny(kind, args) { network.push({ kind, args: args.map(String) }); throw new Error('FORBIDDEN_REAL_REGISTER_PROFILE_IO_' + kind) }
window.XMLHttpRequest.prototype.open = function (...args) { return deny('XHR', args) }
globalThis.fetch = window.fetch = (...args) => deny('FETCH', args)
for (const name of ['node:http', 'node:https']) { const native = require(name); native.request = (...args) => deny(name, args); native.get = (...args) => deny(name, args) }
class SyntheticSocket {
  constructor(url) { this.url = url; this.readyState = 0; this.sent = []; sockets.push(this) }
  send(value) { this.sent.push(value) } close() { this.readyState = 3 }
  open() { this.readyState = 1; this.onopen?.({}) }
  status(payload) { this.onmessage?.({ data: JSON.stringify({ type: 'status', ...payload }) }) }
}
class SyntheticChannel {
  constructor(name) { this.name = name; this.handlers = new Set(); this.sent = []; channels.push(this) }
  addEventListener(name, fn) { if (name === 'message') this.handlers.add(fn) }
  removeEventListener(name, fn) { if (name === 'message') this.handlers.delete(fn) }
  postMessage(data) { this.sent.push(clone(data)); for (const other of channels) if (other !== this && !other.closed && other.name === this.name) for (const fn of other.handlers) fn({ data: clone(data) }) }
  close() { this.closed = true; this.handlers.clear() }
}
globalThis.WebSocket = window.WebSocket = SyntheticSocket
globalThis.BroadcastChannel = window.BroadcastChannel = SyntheticChannel
globalThis.sessionStorage = window.sessionStorage
const Register = require(path.join(web, 'src/pages/Register/index.vue')), Profile = require(path.join(web, 'src/pages/Profile/index.vue'))
const flowHelpers = require(path.join(web, 'src/utils/wechatRegisterFlow.mjs'))
async function until(read, label) { const end = Date.now() + 1000; do { const value = read(); if (value) return value; await new Promise(resolve => setTimeout(resolve, 10)); await nextTick() } while (Date.now() < end); assert.fail(label) }
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 5)); await nextTick(); await flushPromises() }
async function open(Page = Register, actor = null) {
  if (actor) user.setStoredCurrentUser(actor); else user.clearStoredCurrentUser()
  const p = Page === Register ? '/register' : '/profile'
  activeRoute = reactive({ path: p, fullPath: p, query: {} })
  const w = mount(Page, { attachTo: document.body, global: { plugins: [Antd], config: { errorHandler: error => errors.push(error) } } })
  wrappers.push(w); await settle(); return w
}
function button(w, label) { return w.findAll('button').find(item => item.text().replace(/\s/g, '') === label.replace(/\s/g, '')) }
function click(w, label) { const item = button(w, label); assert.ok(item, 'REAL_BUTTON_' + label); item.element.click() }
function callback(component, name) { const fn = component.vm.$.vnode.props?.[name]; assert.equal(typeof fn, 'function', 'REAL_NATIVE_CALLBACK_' + name); return fn }
function formCallback(w, index, name) { return callback(w.findAllComponents(Antd.Form)[index], name) }
async function fields(w, username = '  注册_A@01  ', secret = PW) {
  await w.get('input[placeholder="请输入用户名"]').setValue(username); await w.get('input[placeholder="请输入密码"]').setValue(secret)
  await w.get('input[placeholder="可选，便于后台识别"]').setValue('  昵称 A  '); await w.get('input[autocomplete="tel-national"]').setValue('13800000000')
  await w.get('input[placeholder="请输入验证码"]').setValue(OTP)
}
async function submitRegister(w) { await fields(w); await w.get('form').trigger('submit'); await settle() }
async function submitCaptcha(scene) {
  await settle(); const input = document.getElementById('captcha-code-input'); assert.ok(input, 'ACTUAL_SWAL_LOCAL_CAPTCHA_OPEN')
  input.value = '8642'; input.dispatchEvent(new Event('input', { bubbles: true })); Swal.clickConfirm(); await settle()
  const call = calls.verify.at(-1); assert.ok(call, 'ACTUAL_CAPTCHA_VERIFY_REQUEST')
  assert.deepEqual(call.payload, { challengeId: 'SYNTHETIC_CHALLENGE_' + challengeSequence, scene, captchaCode: '8642' }); return call
}
async function acceptCaptcha(scene, token = TOKEN) { const call = await submitCaptcha(scene); call.resolve({ status: 200, data: { captchaToken: token } }); await settle(); return call }
async function resolve(name, data = {}, status = 200) { const call = calls[name].at(-1); assert.ok(call, 'EXPECTED_TRANSPORT_' + name); call.resolve({ status, data }); await settle(); return call }
async function profileFields(w, username = '修改_A@01', nickname = '新昵称') { await w.get('input[placeholder="只能使用中文、英文、数字、下划线和@"]').setValue(username); await w.get('input[placeholder="可选，最多255个字符"]').setValue(nickname) }
async function phoneFields(w, phone = '13800000000') { await w.get('input[autocomplete="tel-national"]').setValue(phone); await w.get('input[placeholder="请输入短信验证码"]').setValue(OTP) }
const nativeLog = console.log, nativeError = console.error
console.log = (...args) => { logs.push(args); nativeLog(...args) }; console.error = (...args) => { logs.push(args); nativeError(...args) }
afterEach(async () => {
  for (const w of wrappers) if (w.exists()) w.unmount(); wrappers.length = 0
  Swal.close(); Antd.Modal.destroyAll(); Antd.message.destroy()
  for (const collection of Object.values(calls)) for (const call of collection) if (!call.completed) call.resolve({ status: 503, message: 'SYNTHETIC_TEST_CLEANUP' })
  await settle(); Swal.close(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle()
  user.clearStoredCurrentUser(); window.sessionStorage.clear(); document.body.replaceChildren()
  for (const collection of Object.values(calls)) collection.length = 0
  settings = { ...defaults }; meReply = A; locationsReply = {}; activeRoute = undefined
  routes.length = notices.length = sockets.length = routeLeaves.length = channels.length = 0
  assert.deepEqual(network.splice(0), [], 'NO_REAL_HTTP_SMS_SDK_WECHAT_DB_IO')
  assert.deepEqual(errors.splice(0), [], 'NO_HIDDEN_NATIVE_VUE_OR_ANTD_EXCEPTION'); logs.length = 0
})

test('REGISTER_PROFILE existing anonymous registration verifies actual captcha then normalized exact payload and login snapshot', async () => {
  const w = await open(); await fields(w, '  注册_A@01  ', '  ' + PW + '  '); await w.get('input[placeholder="请输入验证码"]').setValue(' ' + OTP + ' '); await w.get('form').trigger('submit'); await settle(); assert.equal(calls.register.length, 0); await acceptCaptcha('register_submit', ' ' + TOKEN + ' ')
  assert.deepEqual(calls.register[0].payload, { username: '注册_A@01', password: PW, nickname: '昵称 A', phone: '13800000000', smsCode: OTP, captchaToken: TOKEN })
  await resolve('register'); assert.deepEqual(routes, [{ path: '/login', query: { username: '注册_A@01' } }]); assert.equal(w.vm.registerForm.password, ''); assert.equal(w.vm.submitting, false)
})
test('REGISTER_PROFILE existing registration SMS202 retains six-digit native input and cooldown without false success', async () => {
  const w = await open(); await fields(w); await w.get('input[placeholder="请输入验证码"]').setValue('1x234567')
  assert.equal(w.vm.registerForm.smsCode, OTP); click(w, '发送验证码'); await settle(); await acceptCaptcha('sms_send_register')
  assert.deepEqual(calls.registerSMS[0].payload, { phone: '13800000000', scene: 'register', captchaToken: TOKEN })
  await resolve('registerSMS', {}, 202); assert.equal(w.vm.smsCooldownActive, true); assert.equal(notices.some(x => x.kind === 'success'), false)
  assert.ok(notices.some(x => x.kind === 'warning' && x.text.includes('待核对'))); assert.equal(w.vm.registerForm.password, PW)
})
test('REGISTER_PROFILE existing optional WeChat staged flow retains coordinator token in registration payload', async () => {
  const w = await open(); click(w, '绑定微信'); await settle(); await resolve('flow', { registrationFlowToken: FLOW, status: 'pending' })
  assert.equal(window.sessionStorage.getItem(flowHelpers.REGISTRATION_FLOW_STORAGE_KEY), FLOW)
  await w.get('input[placeholder="请输入微信验证码"]').setValue('  654321  '); click(w, '确认微信验证码'); await settle()
  assert.deepEqual(calls.flow.at(-1).payload, { registrationFlowToken: FLOW }); await resolve('flow', { status: 'pending' })
  assert.deepEqual(calls.stage[0].payload, { registrationFlowToken: FLOW, code: '654321' }); await resolve('stage', { status: 'staged' })
  assert.equal(w.vm.wechatRegisterStaged, true); await submitRegister(w); await acceptCaptcha('register_submit'); assert.equal(calls.register[0].payload.registrationFlowToken, FLOW)
  await resolve('register'); assert.equal(window.sessionStorage.getItem(flowHelpers.REGISTRATION_FLOW_STORAGE_KEY), null)
})
test('REGISTER_PROFILE existing persisted WeChat flow resumes and cancel releases only its token', async () => {
  window.sessionStorage.setItem(flowHelpers.REGISTRATION_FLOW_STORAGE_KEY, FLOW)
  const first = await open(); assert.deepEqual(calls.flow[0].payload, { registrationFlowToken: FLOW }); await resolve('flow', { status: 'staged' })
  assert.equal(first.vm.wechatBindingActive, true); first.unmount(); assert.equal(window.sessionStorage.getItem(flowHelpers.REGISTRATION_FLOW_STORAGE_KEY), FLOW, 'NORMAL_UNMOUNT_PRESERVES_OPAQUE_RECOVERY_TOKEN')
  const w = await open(); assert.deepEqual(calls.flow[1].payload, { registrationFlowToken: FLOW }); await resolve('flow', { status: 'staged' }); assert.equal(w.vm.wechatRegisterStaged, true); click(w, '取消绑定'); await settle()
  assert.deepEqual(calls.release[0].payload, { registrationFlowToken: FLOW }); await resolve('release'); assert.equal(w.vm.wechatBindingActive, false); assert.equal(window.sessionStorage.getItem(flowHelpers.REGISTRATION_FLOW_STORAGE_KEY), null)
})
test('REGISTER_PROFILE existing ordinary profile loads me/local locations and preserves six-decimal balance then self update', async () => {
  locationsReply = { ip: { '192.0.2.7': { address: '合成 IP 地区' } }, phone: { '13900000000': { address: '合成手机地区' } } }
  const w = await open(Profile, A); assert.equal(calls.me.length, 1); assert.equal(calls.locations.length, 1); assert.ok(w.text().includes('普通用户')); assert.ok(w.text().includes('0.123456')); assert.ok(w.text().includes('合成 IP 地区')); assert.ok(w.text().includes('合成手机地区'))
  assert.equal(w.vm.currentUser.username, A.username); await profileFields(w); await w.findAll('form')[0].trigger('submit'); await settle()
  assert.deepEqual(calls.profile[0].payload, { username: '修改_A@01', nickname: '新昵称' }); const saved = { ...A, username: '修改_A@01', nickname: '新昵称' }; await resolve('profile', saved)
  assert.deepEqual(user.getStoredCurrentUser(), saved); assert.equal(w.vm.updatingProfile, false); assert.equal(w.vm.profileForm.nickname, '新昵称'); assert.ok(notices.some(x => x.text === '资料保存成功'))
})
test('REGISTER_PROFILE existing ordinary phone SMS202 cooldown and successful phone update retain valid capability', async () => {
  const w = await open(Profile, A); await phoneFields(w); await w.get('input[placeholder="请输入短信验证码"]').setValue('12x34567'); assert.equal(w.vm.phoneForm.smsCode, OTP)
  click(w, '发送验证码'); await settle(); await acceptCaptcha('sms_send_change_phone'); assert.deepEqual(calls.phoneSMS[0].payload, { phone: '13800000000', captchaToken: TOKEN })
  await resolve('phoneSMS', {}, 202); assert.equal(w.vm.smsCooldownActive, true); assert.ok(notices.some(x => x.text.includes('待核对')))
  await w.findAll('form')[1].trigger('submit'); await settle(); assert.deepEqual(calls.phone[0].payload, { phone: '13800000000', smsCode: OTP })
  const saved = { ...A, phone: '13800000000' }; await resolve('phone', saved); assert.deepEqual(user.getStoredCurrentUser(), saved); assert.equal(w.vm.phoneForm.phone, ''); assert.equal(w.vm.updatingPhone, false)
})
test('REGISTER_PROFILE existing ordinary WeChat code bind and native confirm unbind self update correctly', async () => {
  const w = await open(Profile, A); await w.get('input[placeholder="请输入微信验证码"]').setValue('x654321'); click(w, '微信验证码绑定'); await settle()
  assert.deepEqual(calls.codeBind[0].payload, { code: '654321' }); const bound = { ...A, wechatId: 'SYNTHETIC_WECHAT_ID' }; await resolve('codeBind', bound)
  assert.deepEqual(user.getStoredCurrentUser(), bound); assert.equal(w.vm.wechatCodeForm.code, ''); click(w, '解绑微信');
  const popup = await until(() => { const found = [...document.querySelectorAll('.ant-popover:not(.ant-popover-hidden)')].filter(node => node.textContent.includes('确定解绑当前微信吗')); return found.length === 1 && found[0] }, 'CURRENT_NATIVE_POPOVER_READY')
  const confirm = [...popup.querySelectorAll('button')].find(node => node.textContent.replace(/\s/g, '') === '确认解绑'); assert.ok(confirm, 'REAL_ANTD_POPOVER_CONFIRM'); confirm.click(); await settle(); assert.equal(calls.unbind.length, 1)
  await resolve('unbind', A); assert.deepEqual(user.getStoredCurrentUser(), A); assert.equal(w.vm.unbindingWechat, false); assert.ok(notices.some(x => x.text === '微信已解绑'))
})
test('REGISTER_PROFILE existing ordinary scan QR actual controller accepts owned bind and clears own QR', async () => {
  settings.wechatDefaultLoginMode = 'scan'; const w = await open(Profile, A); click(w, '刷新绑定二维码'); await settle()
  await resolve('qr', { loginToken: 'SYNTHETIC_QR_LOGIN_TOKEN', qrcodeImageData: PNG, expireSeconds: 180 }); assert.ok(w.find('img[alt="微信绑定二维码"]').exists()); assert.equal(sockets.length, 1)
  sockets[0].open(); sockets[0].status({ status: 'need_bind', bindToken: 'SYNTHETIC_QR_BIND_TOKEN' }); await settle(); assert.deepEqual(calls.bind[0].payload, { bindToken: 'SYNTHETIC_QR_BIND_TOKEN' })
  await resolve('bind', { ...A, wechatId: 'SYNTHETIC_SCAN_WECHAT' }); assert.equal(w.vm.currentUser.wechatId, 'SYNTHETIC_SCAN_WECHAT'); assert.equal(w.vm.wechatQrCodeImageData, '')
})

// Finite ownership matrix. Native callbacks below are retained from actual Antd
// VNodes before a transition, not invented events or replacement owner logic.
function assertPrivateFeedback() {
  const text = JSON.stringify({ notices, logs })
  for (const secret of [PW, OTP, '654321', TOKEN, FLOW, 'SYNTHETIC_UNSAFE_UPSTREAM', 'SYNTHETIC_SMS_SECRET', 'SYNTHETIC_BIND_SECRET']) assert.equal(text.includes(secret), false, 'NO_SECRET_OR_RAW_PROVIDER_ERROR_' + secret)
}
async function beginSMS(w, Page) {
  if (Page === Register) await fields(w); else await phoneFields(w)
  click(w, '发送验证码'); await settle()
}
const smsName = Page => Page === Register ? 'registerSMS' : 'phoneSMS'
const smsScene = Page => Page === Register ? 'sms_send_register' : 'sms_send_change_phone'
const smsBusy = (w, Page) => Page === Register ? w.vm.sendingCode : w.vm.sendingPhoneCode
for (const [label, Page] of [['Register', Register], ['Profile', Profile]]) {
  test(`REGISTER_PROFILE ownership ${label} A-B-A retires actual pending captcha with synchronous private DOM erasure and no old SMS`, async () => {
    const w = await open(Page, A); await beginSMS(w, Page); const verify = await submitCaptcha(smsScene(Page))
    const native = w.get(Page === Register ? 'input[placeholder="请输入密码"]' : 'input[placeholder="请输入短信验证码"]').element
    user.setStoredCurrentUser(B); assert.equal(native.value, '', 'PRIVATE_NATIVE_VALUE_IS_CLEARED_SYNCHRONOUSLY'); user.setStoredCurrentUser(A); await settle()
    const count = notices.length; verify.resolve({ status: 200, data: { captchaToken: TOKEN } }); await settle()
    assert.equal(calls[smsName(Page)].length, 0, 'RETIRED_VERIFY_CANNOT_ISSUE_SMS'); assert.equal(w.vm.smsCooldownActive, false); assert.equal(notices.length, count); assert.deepEqual(routes, [])
  })
  test(`REGISTER_PROFILE ownership ${label} old SMS202 cannot release current B busy or start its cooldown`, async () => {
    const w = await open(Page, A); await beginSMS(w, Page); await acceptCaptcha(smsScene(Page)); const old = calls[smsName(Page)][0]
    user.setStoredCurrentUser(B); await settle(); if (Page === Profile) { meReply = B; click(w, '刷新'); await settle() } await beginSMS(w, Page); await acceptCaptcha(smsScene(Page), 'SYNTHETIC_CURRENT_B_CAPTCHA'); assert.equal(calls[smsName(Page)].length, 2); assert.equal(smsBusy(w, Page), true)
    const count = notices.length; old.resolve({ status: 202, message: 'SYNTHETIC_UNSAFE_UPSTREAM' }); await settle(); assert.equal(smsBusy(w, Page), true); assert.equal(w.vm.smsCooldownActive, false); assert.equal(notices.length, count)
    calls[smsName(Page)][1].resolve({ status: 202 }); await settle(); assert.equal(smsBusy(w, Page), false); assert.equal(w.vm.smsCooldownActive, true)
  })
  test(`REGISTER_PROFILE ownership ${label} route departure retires captcha and retained native submit callback`, async () => {
    const w = await open(Page, A); const finish = formCallback(w, Page === Register ? 0 : 1, 'onFinish'); await beginSMS(w, Page); const verify = await submitCaptcha(smsScene(Page))
    activeRoute.path = '/ordinary-next'; activeRoute.fullPath = '/ordinary-next'; await settle(); const count = notices.length
    verify.resolve({ status: 200, data: { captchaToken: TOKEN } }); finish(); await settle()
    assert.equal(calls[smsName(Page)].length, 0); assert.equal(calls.register.length + calls.phone.length, 0); assert.equal(w.vm.smsCooldownActive, false); assert.equal(notices.length, count); assert.deepEqual(routes, [])
  })
  test(`REGISTER_PROFILE ownership ${label} phone draft changed during actual verify cannot issue SMS using later input`, async () => {
    const w = await open(Page, A); await beginSMS(w, Page); const verify = await submitCaptcha(smsScene(Page))
    await w.get('input[autocomplete="tel-national"]').setValue('13700000000'); const count = notices.length; verify.resolve({ status: 200, data: { captchaToken: TOKEN } }); await settle()
    assert.equal(calls[smsName(Page)].length, 0); assert.equal(w.vm.smsCooldownActive, false); assert.equal(notices.length, count)
    assert.equal(Page === Register ? w.vm.registerForm.phone : w.vm.phoneForm.phone, '13700000000')
  })
  test(`REGISTER_PROFILE ownership ${label} native cancellation closes own popup without SMS or input loss`, async () => {
    const w = await open(Page, A); await beginSMS(w, Page); assert.ok(Swal.getPopup()); Swal.clickCancel(); await settle()
    assert.equal(calls.verify.length + calls[smsName(Page)].length, 0); assert.equal(smsBusy(w, Page), false); assert.equal(w.vm.smsCooldownActive, false)
    assert.equal(Page === Register ? w.vm.registerForm.phone : w.vm.phoneForm.phone, '13800000000'); assert.equal(notices.some(x => x.kind === 'success'), false)
  })
  test(`REGISTER_PROFILE ownership ${label} current SMS failure and UNKNOWN retain draft with fixed public feedback and no retry`, async () => {
    const w = await open(Page, A); await beginSMS(w, Page); await acceptCaptcha(smsScene(Page)); const issued = calls[smsName(Page)][0]
    issued.resolve({ status: 400, message: 'SYNTHETIC_UNSAFE_UPSTREAM ' + PW + ' SYNTHETIC_SMS_SECRET ' + TOKEN }); await settle()
    assert.equal(smsBusy(w, Page), false); assert.equal(Page === Register ? w.vm.registerForm.phone : w.vm.phoneForm.phone, '13800000000'); assert.equal(w.vm.smsCooldownActive, false)
    await beginSMS(w, Page); await acceptCaptcha(smsScene(Page)); const unknown = new Error('SYNTHETIC_UNSAFE_UPSTREAM ' + PW); unknown.code = 'UNKNOWN_OUTCOME'; calls[smsName(Page)][1].reject(unknown); await settle()
    assert.equal(calls[smsName(Page)].length, 2); assert.equal(notices.some(x => x.kind === 'success'), false); assert.ok(notices.some(x => x.text.includes('核对'))); assertPrivateFeedback()
  })
}

test('REGISTER_PROFILE ownership registration issued payload snapshot and late A200 never navigates B or clears its new pending submit', async () => {
  const w = await open(Register, A); await submitRegister(w); await acceptCaptcha('register_submit'); const old = calls.register[0]
  const finish = formCallback(w, 0, 'onFinish'); finish(); await settle(); assert.equal(calls.register.length, 1, 'PENDING_NATIVE_REGISTER_IS_NOT_DUPLICATED')
  await w.get('input[placeholder="请输入用户名"]').setValue('issued_later_input'); await w.get('input[placeholder="请输入密码"]').setValue('SYNTHETIC_LATER_PASSWORD'); assert.deepEqual(clone(old.rawPayload), old.payload)
  user.setStoredCurrentUser(B); await settle(); await submitRegister(w); await acceptCaptcha('register_submit', 'SYNTHETIC_B_REGISTER_CAPTCHA'); assert.equal(calls.register.length, 2); assert.equal(w.vm.submitting, true)
  const count = notices.length; old.resolve({ status: 200 }); await settle(); assert.deepEqual(routes, []); assert.deepEqual(user.getStoredCurrentUser(), B); assert.equal(w.vm.submitting, true); assert.equal(w.vm.registerForm.password, PW); assert.equal(notices.length, count)
  calls.register[1].resolve({ status: 400, message: 'SYNTHETIC_UNSAFE_UPSTREAM ' + PW + TOKEN }); await settle(); assert.equal(w.vm.submitting, false); assert.equal(w.vm.registerForm.password, PW); assertPrivateFeedback()
})
test('REGISTER_PROFILE ownership old native register finish/update/validation cannot mutate returned A draft or focus', async () => {
  const w = await open(Register, A); await fields(w)
  const finish = formCallback(w, 0, 'onFinish'), failed = formCallback(w, 0, 'onFinishFailed')
  const password = w.findComponent(Antd.InputPassword); const update = callback(password, 'onUpdate:value'); const native = w.get('input[placeholder="请输入密码"]').element
  user.setStoredCurrentUser(B); assert.equal(native.value, ''); user.setStoredCurrentUser(A); await settle(); await fields(w, 'returned_A', 'SYNTHETIC_RETURNED_A_PASSWORD')
  const focus = document.createElement('button'); document.body.appendChild(focus); focus.focus(); const count = notices.length
  update(PW); finish(); failed({ values: { password: PW, smsCode: OTP }, errorFields: [{ name: ['username'], errors: ['SYNTHETIC_OLD_VALIDATION'] }] }); await settle()
  assert.equal(calls.challenge.length + calls.register.length, 0); assert.equal(w.vm.registerForm.password, 'SYNTHETIC_RETURNED_A_PASSWORD'); assert.equal(notices.length, count); assert.equal(document.activeElement, focus); assertPrivateFeedback()
})
test('REGISTER_PROFILE ownership legitimate same-tick registration input and ordinary route cannot hit a null native ticket', async () => {
  const w = await open(); const username = w.get('input[placeholder="请输入用户名"]'), secret = w.get('input[placeholder="请输入密码"]')
  for (const [input, value] of [[username.element, 'same_tick_A'], [secret.element, PW], [w.get('input[autocomplete="tel-national"]').element, '13800000000'], [w.get('input[placeholder="请输入验证码"]').element, OTP]]) { input.value = value; input.dispatchEvent(new Event('input', { bubbles: true })) }
  w.get('form').element.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle(); await acceptCaptcha('register_submit'); assert.equal(calls.register.length, 1)
  await resolve('register'); assert.deepEqual(routes.at(-1), { path: '/login', query: { username: 'same_tick_A' } })
})

test('REGISTER_PROFILE ownership ordinary profile duplicate/issued snapshot retires result after a newer unsaved nickname', async () => {
  const w = await open(Profile, A); await profileFields(w); await w.findAll('form')[0].trigger('submit'); await settle(); const issued = calls.profile[0]
  formCallback(w, 0, 'onFinish')(); await settle(); assert.equal(calls.profile.length, 1)
  await profileFields(w, '编辑中_A', '稍后仍未保存'); assert.deepEqual(clone(issued.rawPayload), issued.payload); assert.equal(w.vm.updatingProfile, true, 'ISSUED_SAVE_RETAINS_BUSY_UNTIL_SETTLED'); formCallback(w, 0, 'onFinish')(); await settle(); assert.equal(calls.profile.length, 1, 'EDITED_DRAFT_CANNOT_DUPLICATE_ISSUED_SAVE')
  await resolve('profile', { ...A, username: '修改_A@01', nickname: '新昵称' }); assert.equal(user.getStoredCurrentUser().nickname, A.nickname); assert.equal(w.vm.profileForm.nickname, '稍后仍未保存'); assert.equal(w.vm.profileForm.username, '编辑中_A')
})
test('REGISTER_PROFILE ownership own phone self-update preserves independent pending profile task and unsaved nickname', async () => {
  const w = await open(Profile, A); await profileFields(w, '本次保存_A', '本次保存昵称'); await w.findAll('form')[0].trigger('submit'); await settle(); const save = calls.profile[0]
  await phoneFields(w); await w.findAll('form')[1].trigger('submit'); await settle(); await resolve('phone', { ...A, phone: '13800000000' })
  assert.equal(w.vm.profileForm.nickname, '本次保存昵称'); assert.equal(w.vm.updatingProfile, true, 'INDEPENDENT_PROFILE_REQUEST_RETAINS_BUSY')
  save.resolve({ status: 200, data: { ...A, phone: '13800000000', username: '本次保存_A', nickname: '本次保存昵称' } }); await settle(); assert.equal(user.getStoredCurrentUser().username, '本次保存_A'); assert.equal(w.vm.updatingProfile, false)
  await profileFields(w, '后续草稿_A', '未保存昵称'); await phoneFields(w, '13700000000'); await w.findAll('form')[1].trigger('submit'); await settle(); await resolve('phone', { ...A, phone: '13700000000' }); assert.equal(w.vm.profileForm.nickname, '未保存昵称')
})
test('REGISTER_PROFILE ownership profile A-B-A stale save cannot write storage, clear newer busy, or save blank defaults', async () => {
  const w = await open(Profile, A); await profileFields(w); await w.findAll('form')[0].trigger('submit'); await settle(); const old = calls.profile[0]
  user.setStoredCurrentUser(B); user.setStoredCurrentUser(A); await settle()
  await w.findAll('form')[0].trigger('submit'); await settle(); assert.equal(calls.profile.length, 1, 'UNLOADED_DEFAULTS_CANNOT_BE_SAVED')
  meReply = A; click(w, '刷新'); await settle(); await profileFields(w, 'fresh_A', 'fresh_A_nick'); await w.findAll('form')[0].trigger('submit'); await settle(); assert.equal(calls.profile.length, 2); assert.equal(w.vm.updatingProfile, true); const count = notices.length
  old.resolve({ status: 200, data: { ...A, username: 'RETIRED_A' } }); await settle(); assert.equal(user.getStoredCurrentUser().username, A.username); assert.equal(w.vm.updatingProfile, true); assert.equal(notices.length, count)
  calls.profile[1].resolve({ status: 400, message: 'SYNTHETIC_UNSAFE_UPSTREAM ' + PW }); await settle(); assert.equal(w.vm.profileForm.nickname, 'fresh_A_nick'); assertPrivateFeedback()
})
test('REGISTER_PROFILE ownership profile unknown local identity does not block legitimate initial server me or ordinary edit', async () => {
  for (const actor of [null, { role: 'user' }, { id: 'unknown', role: 'user' }]) {
    const w = await open(Profile, actor); assert.deepEqual(user.getStoredCurrentUser(), A); await profileFields(w, 'server_me_A', '合法昵称'); await w.findAll('form')[0].trigger('submit'); await settle()
    assert.deepEqual(calls.profile.at(-1).payload, { username: 'server_me_A', nickname: '合法昵称' }); await resolve('profile', { ...A, username: 'server_me_A', nickname: '合法昵称' }); w.unmount()
  }
})
test('REGISTER_PROFILE ownership profile old native finish/update and QR transport retire synchronously on actor switch', async () => {
  settings.wechatDefaultLoginMode = 'scan'; const w = await open(Profile, A); await phoneFields(w); const finish = formCallback(w, 1, 'onFinish')
  const sms = w.findAllComponents(Antd.Input).find(item => item.props('placeholder') === '请输入短信验证码'); const update = callback(sms, 'onUpdate:value')
  click(w, '刷新绑定二维码'); await settle(); await resolve('qr', { loginToken: 'SYNTHETIC_QR_LOGIN_TOKEN', qrcodeImageData: PNG, expireSeconds: 180 }); const image = w.get('img[alt="微信绑定二维码"]').element, socket = sockets[0]
  user.setStoredCurrentUser(B); assert.equal(image.getAttribute('src') || '', '', 'PRIVATE_QR_SRC_CLEARED_BEFORE_RENDER'); await settle(); user.setStoredCurrentUser(A); await settle(); meReply = A; click(w, '刷新'); await settle(); await phoneFields(w)
  const count = notices.length; update('999999'); finish(); socket.status({ status: 'need_bind', bindToken: 'SYNTHETIC_BIND_SECRET' }); await settle()
  assert.equal(calls.phone.length + calls.bind.length, 0); assert.equal(w.vm.phoneForm.smsCode, OTP); assert.equal(notices.length, count); assertPrivateFeedback()
})
test('REGISTER_PROFILE ownership late profile QR status cannot bind after normal route changes', async () => {
  settings.wechatDefaultLoginMode = 'scan'; const w = await open(Profile, A); click(w, '刷新绑定二维码'); await settle(); await resolve('qr', { loginToken: 'SYNTHETIC_QR_LOGIN_TOKEN', qrcodeImageData: PNG, expireSeconds: 180 })
  const socket = sockets[0]; activeRoute.path = '/ordinary-next'; activeRoute.fullPath = '/ordinary-next'; await settle(); socket.status({ status: 'need_bind', bindToken: 'SYNTHETIC_BIND_SECRET' }); await settle(); assert.equal(calls.bind.length, 0); assert.equal(w.vm.wechatQrCodeImageData, '')
})

test('REGISTER_PROFILE ownership later same-actor registration draft retires issued result without reset or login navigation', async () => {
  const w = await open(); await submitRegister(w); await acceptCaptcha('register_submit'); const issued = calls.register[0]
  await w.get('input[placeholder="请输入用户名"]').setValue('later_unsaved_user'); await w.get('input[placeholder="可选，便于后台识别"]').setValue('后来草稿'); assert.equal(w.vm.submitting, true, 'ISSUED_REGISTER_RETAINS_BUSY_UNTIL_SETTLED'); formCallback(w, 0, 'onFinish')(); await settle(); assert.equal(calls.register.length, 1, 'EDITED_DRAFT_CANNOT_DUPLICATE_ISSUED_REGISTER'); const count = notices.length
  issued.resolve({ status: 200 }); await settle(); assert.deepEqual(routes, []); assert.equal(w.vm.registerForm.username, 'later_unsaved_user'); assert.equal(w.vm.registerForm.nickname, '后来草稿'); assert.equal(w.vm.registerForm.password, PW); assert.equal(notices.length, count)
})
test('REGISTER_PROFILE ownership pending registration flow QR cannot populate a new actor or restart old binding', async () => {
  settings.wechatDefaultLoginMode = 'scan'; const w = await open(Register, A); click(w, '绑定微信'); await settle(); await resolve('flow', { registrationFlowToken: FLOW, status: 'pending' }); await resolve('flow', { status: 'pending' }); const pendingQR = calls.flowQR[0]
  assert.deepEqual(pendingQR.payload, { registrationFlowToken: FLOW, refresh: false }); user.setStoredCurrentUser(B); await settle(); const count = notices.length
  pendingQR.resolve({ status: 200, data: { sessionId: 'SYNTHETIC_RETIRED_SESSION', displayData: PNG } }); await settle()
  assert.equal(w.vm.wechatQrCodeImageData, ''); assert.equal(w.vm.wechatBindingActive, false); assert.equal(calls.flowStatus.length, 0); assert.equal(notices.length, count); assert.equal(window.sessionStorage.getItem(flowHelpers.REGISTRATION_FLOW_STORAGE_KEY), null)
})
test('REGISTER_PROFILE ownership old cancel release cannot clear new real coordinator flow or emit its failure', async () => {
  const w = await open(); click(w, '绑定微信'); await settle(); await resolve('flow', { registrationFlowToken: FLOW, status: 'pending' }); click(w, '取消绑定'); await settle(); const old = calls.release[0]
  assert.equal(w.vm.wechatBindingActive, false); click(w, '绑定微信'); await settle(); await resolve('flow', { registrationFlowToken: 'SYNTHETIC_NEW_FLOW', status: 'pending' }); assert.equal(window.sessionStorage.getItem(flowHelpers.REGISTRATION_FLOW_STORAGE_KEY), 'SYNTHETIC_NEW_FLOW'); const count = notices.length
  old.reject(Object.assign(new Error('SYNTHETIC_UNSAFE_UPSTREAM'), { status: 410 })); await settle(); assert.equal(w.vm.wechatBindingActive, true); assert.equal(window.sessionStorage.getItem(flowHelpers.REGISTRATION_FLOW_STORAGE_KEY), 'SYNTHETIC_NEW_FLOW'); assert.equal(notices.length, count)
})
test('REGISTER_PROFILE ownership registration visibility preserves native five-second polling and terminal flow clears', async () => {
  const originalInterval = globalThis.setInterval, originalClear = globalThis.clearInterval, visibility = Object.getOwnPropertyDescriptor(document, 'visibilityState'), active = new Set(), intervals = []
  globalThis.setInterval = (fn, delay, ...args) => { const handle = originalInterval(fn, delay, ...args); if (delay === 5000) { intervals.push({ fn, delay, handle }); active.add(handle) } return handle }
  globalThis.clearInterval = handle => { active.delete(handle); return originalClear(handle) }
  try {
    const w = await open(); click(w, '绑定微信'); await settle(); await resolve('flow', { registrationFlowToken: FLOW, status: 'pending' }); assert.equal(active.size, 1); assert.equal(intervals[0].delay, 5000)
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' }); document.dispatchEvent(new Event('visibilitychange')); await settle(); assert.equal(active.size, 0)
    const before = calls.flow.length; Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }); document.dispatchEvent(new Event('visibilitychange')); await settle(); assert.equal(active.size, 1); assert.equal(calls.flow.length, before + 1)
    await resolve('flow', {}, 410); assert.equal(w.vm.wechatBindingActive, false); assert.equal(window.sessionStorage.getItem(flowHelpers.REGISTRATION_FLOW_STORAGE_KEY), null); w.unmount(); assert.equal(active.size, 0)
  } finally { globalThis.setInterval = originalInterval; globalThis.clearInterval = originalClear; if (visibility) Object.defineProperty(document, 'visibilityState', visibility); else delete document.visibilityState; for (const handle of active) originalClear(handle) }
})
test('REGISTER_PROFILE ownership unavailable native storage permits legitimate anonymous register and initial server me', async () => {
  const original = Object.getOwnPropertyDescriptor(window, 'localStorage')
  Object.defineProperty(window, 'localStorage', { configurable: true, get() { throw new Error('SYNTHETIC_STORAGE_DENIED') } })
  try {
    const r = await open(); await submitRegister(r); await acceptCaptcha('register_submit'); assert.equal(calls.register.length, 1); await resolve('register', {}, 400); r.unmount()
    const p = await open(Profile); assert.equal(calls.me.length, 1); assert.equal(p.vm.currentUser.id, A.id); await profileFields(p, 'server_me_A', '合法草稿'); const beforeSaveNotice = notices.length; await p.findAll('form')[0].trigger('submit'); await settle(); assert.equal(calls.profile.length, 1); await resolve('profile', { ...A, username: 'server_me_A', nickname: '合法草稿' })
    assert.equal(p.vm.currentUser.username, 'server_me_A'); assert.equal(user.getStoredCurrentUser(), null, 'DENIED_CACHE_IS_NOT_FALSELY_CLAIMED_PERSISTED'); assert.ok(notices.slice(beforeSaveNotice).some(item => item.kind === 'warning' && item.text.includes('本地')), 'EXPLICIT_FIXED_LOCAL_CACHE_FAILURE_FEEDBACK'); assertPrivateFeedback()
    await phoneFields(p); click(p, '发送验证码'); await settle(); const oldVerify = await submitCaptcha('sms_send_change_phone'), oldSubmit = formCallback(p, 0, 'onFinish')
    const privateInput = p.get('input[placeholder="可选，最多255个字符"]').element, phoneInput = p.get('input[autocomplete="tel-national"]').element
    user.clearStoredCurrentUser(); assert.equal(privateInput.value, '', 'BLOCKED_STORAGE_LOGOUT_SYNCHRONOUSLY_ERASES_PRIVATE_NATIVE_INPUT'); assert.equal(phoneInput.value, ''); assert.equal(p.vm.profileForm.nickname, ''); assert.equal(p.vm.phoneForm.phone, ''); assert.equal(p.vm.profileReady, false)
    oldVerify.resolve({ status: 200, data: { captchaToken: TOKEN } }); oldSubmit(); await settle(); assert.equal(calls.phoneSMS.length, 0, 'LOGOUT_RETIRED_VERIFY_CANNOT_CONTINUE_SMS'); assert.equal(calls.profile.length, 1, 'LOGOUT_RETIRED_NATIVE_SUBMIT_CANNOT_ISSUE_SAVE')
  } finally { Object.defineProperty(window, 'localStorage', original) }
})
test('REGISTER_PROFILE ownership current ordinary WeChat failure preserves code and fixed feedback never reveals bind secrets', async () => {
  const w = await open(Profile, A); await w.get('input[placeholder="请输入微信验证码"]').setValue('654321'); click(w, '微信验证码绑定'); await settle()
  calls.codeBind[0].resolve({ status: 400, message: 'SYNTHETIC_UNSAFE_UPSTREAM SYNTHETIC_BIND_SECRET ' + PW }); await settle(); assert.equal(w.vm.wechatCodeForm.code, '654321'); assert.equal(w.vm.wechatCodeSubmitting, false); assert.equal(user.getStoredCurrentUser().wechatId, ''); assertPrivateFeedback()
})
test('REGISTER_PROFILE ownership ordinary initial and refreshed me results cannot overwrite B or release a newer load', async () => {
  meReply = null; const w = await open(Profile, A); const old = calls.me[0]; user.setStoredCurrentUser(B); await settle(); click(w, '刷新'); await settle(); assert.equal(calls.me.length, 2); assert.equal(w.vm.loading, true)
  const count = notices.length; old.resolve({ status: 200, data: A }); await settle(); assert.deepEqual(user.getStoredCurrentUser(), B); assert.equal(w.vm.loading, true); assert.equal(notices.length, count)
  calls.me[1].resolve({ status: 200, data: B }); await settle(); assert.deepEqual(user.getStoredCurrentUser(), B); assert.equal(w.vm.loading, false)
})

test('REGISTER_PROFILE ownership optional registration WeChat mode preserves account draft and independent pending SMS', async () => {
  const w = await open(); click(w, '绑定微信'); await settle(); await resolve('flow', { registrationFlowToken: FLOW, status: 'pending' }); await fields(w)
  await w.get('input[placeholder="请输入微信验证码"]').setValue('654321'); const oldCode = callback(w.findAllComponents(Antd.Button).find(item => item.text().replace(/\s/g, '') === '确认微信验证码'), 'onClick')
  click(w, '发送验证码'); await settle(); await acceptCaptcha('sms_send_register'); const issued = calls.registerSMS[0]
  await w.get('input[value="scan"]').setValue(true); await settle(); oldCode(); await settle(); assert.equal(calls.stage.length, 0); assert.equal(w.vm.sendingCode, true)
  assert.equal(w.vm.registerForm.username, '  注册_A@01  '); assert.equal(w.vm.registerForm.password, PW); assert.equal(w.vm.registerForm.phone, '13800000000'); assert.equal(w.vm.registerForm.smsCode, OTP); assert.equal(window.sessionStorage.getItem(flowHelpers.REGISTRATION_FLOW_STORAGE_KEY), FLOW)
  issued.resolve({ status: 202 }); await settle(); assert.equal(w.vm.sendingCode, false); assert.equal(w.vm.smsCooldownActive, true)
})
test('REGISTER_PROFILE ownership ordinary WeChat mode preserves nickname draft and independent pending phone change', async () => {
  const w = await open(Profile, A); await profileFields(w, '未保存_A', '保留未保存昵称'); await phoneFields(w); await w.get('input[placeholder="请输入微信验证码"]').setValue('654321')
  const oldCode = callback(w.findAllComponents(Antd.Button).find(item => item.text().replace(/\s/g, '') === '微信验证码绑定'), 'onClick')
  await w.findAll('form')[1].trigger('submit'); await settle(); const issued = calls.phone[0]; await w.get('input[value="scan"]').setValue(true); await settle(); oldCode(); await settle()
  assert.equal(calls.codeBind.length, 0); assert.equal(w.vm.updatingPhone, true); assert.equal(w.vm.profileForm.nickname, '保留未保存昵称'); assert.equal(w.vm.phoneForm.smsCode, OTP)
  issued.resolve({ status: 200, data: { ...A, phone: '13800000000' } }); await settle(); assert.equal(user.getStoredCurrentUser().phone, '13800000000'); assert.equal(w.vm.updatingPhone, false); assert.equal(w.vm.profileForm.nickname, '保留未保存昵称')
})
test('REGISTER_PROFILE ownership legitimate ordinary profile native input and submit in one tick retain current ownership', async () => {
  const w = await open(Profile, A)
  for (const [selector, value] of [['input[placeholder="只能使用中文、英文、数字、下划线和@"]', 'same_tick_profile_A'], ['input[placeholder="可选，最多255个字符"]', '同tick昵称']]) { const input = w.get(selector).element; input.value = value; input.dispatchEvent(new Event('input', { bubbles: true })) }
  w.findAll('form')[0].element.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle(); assert.deepEqual(calls.profile[0].payload, { username: 'same_tick_profile_A', nickname: '同tick昵称' }); await resolve('profile', { ...A, username: 'same_tick_profile_A', nickname: '同tick昵称' }); assert.equal(user.getStoredCurrentUser().username, 'same_tick_profile_A')
})
test('REGISTER_PROFILE ownership same ordinary actor metadata update preserves unsaved nickname phone and unrelated pending SMS', async () => {
  const w = await open(Profile, A); await profileFields(w, '未保存_A', '未保存的昵称'); await phoneFields(w); click(w, '发送验证码'); await settle(); await acceptCaptcha('sms_send_change_phone'); const issued = calls.phoneSMS[0]
  const meCount = calls.me.length, refreshed = { ...A, balance: 0.987654, lastLoginAt: '2026-10-29T01:02:03Z' }; user.setStoredCurrentUser(refreshed)
  assert.equal(w.vm.profileForm.nickname, '未保存的昵称', 'SAME_ACTOR_METADATA_DOES_NOT_ERASE_NICKNAME'); assert.equal(w.vm.phoneForm.phone, '13800000000'); assert.equal(w.vm.phoneForm.smsCode, OTP); assert.equal(w.vm.sendingPhoneCode, true); await settle()
  assert.equal(w.vm.currentUser.balance, 0.987654); assert.equal(calls.me.length, meCount, 'SAME_ACTOR_METADATA_NEEDS_NO_NEW_AUTHENTICATION_READ'); assert.equal(calls.phoneSMS.length, 1)
  issued.resolve({ status: 202 }); await settle(); assert.equal(w.vm.sendingPhoneCode, false); assert.equal(w.vm.smsCooldownActive, true); assert.equal(w.vm.profileForm.nickname, '未保存的昵称'); assert.equal(w.vm.phoneForm.phone, '13800000000'); assert.deepEqual(user.getStoredCurrentUser(), refreshed)
})
