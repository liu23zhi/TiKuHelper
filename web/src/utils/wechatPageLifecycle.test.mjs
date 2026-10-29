import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import vm from 'node:vm'
import { parse, compileScript } from '@vue/compiler-sfc'

function deferred() {
  let resolve
  const promise = new Promise((done) => { resolve = done })
  return { promise, resolve }
}

function qr(token) {
  return { status: 200, data: { loginToken: token, qrcodeImageData: `data:image/png;base64,${token}`, expireSeconds: 180 } }
}

async function loadWechatPage(page, overrides = {}) {
  const source = await readFile(new URL(`../pages/${page}/index.vue`, import.meta.url), 'utf8')
  const { descriptor } = parse(source)
  const compiled = compileScript(descriptor, { id: 'wechat-lifecycle-regression' })
  const options = compiled.scriptAst.find((node) => node.type === 'ExportDefaultDeclaration').declaration.arguments[0]
  const setup = options.properties.find((property) => property.key?.name === 'setup')
  const stored = []
  const routes = []
  const sessions = []
  let lifecycle
  let unmount
  let nextToken = 0
  const scope = {
    ref: (value) => ({ value }),
    reactive: (value) => value,
    computed: (get) => ({ get value() { return get() } }),
    useRouter: () => ({ push: (value) => routes.push(value) }),
    useRoute: () => ({ query: {} }),
    useSmsCooldown: () => ({ buttonText: '', active: false, start() {} }),
    onMounted() {},
    onBeforeUnmount(callback) { unmount = callback },
    createWechatQrStatusController(options) {
      lifecycle = options
      return { stop() {}, start(session) { sessions.push(session) } }
    },
    createWechatLoginQRCode: async () => qr(`qr-${++nextToken}`),
    getCurrentUser: async () => ({ status: 200, data: { id: 1 } }),
    getWechatLoginStatus: async () => ({ status: 200, data: { status: 'pending' } }),
    setStoredCurrentUser: (user) => stored.push(user),
    resolveHomePath: () => '/adapter/profile',
    normalizeSMSCodeInput: (value) => value,
    buildRegisterSourceLabel: (value) => value,
    ipLocations: {}, phoneLocations: {},
    normalizeLocationKey: (value) => value,
    formatValueWithLocation: (value) => value,
    message: { success() {}, error() {}, warning() {} },
    ...overrides,
  }
  const component = vm.runInNewContext(`({${descriptor.script.content.slice(setup.start, setup.end)}})`, scope).setup()
  component.publicAuthSettings.value.wechatLoginEnabled = true
  component.publicAuthSettings.value.wechatScanLoginEnabled = true
  if (component.loginMode) component.loginMode.value = 'wechat'
  return { component, lifecycle, unmount, stored, routes, sessions }
}

test('scan login cannot finish an old getCurrentUser request after refresh, mode change or unmount', async () => {
  for (const action of ['refresh', 'mode-change', 'unmount']) {
    const user = deferred()
    const nextQRCode = deferred()
    let creates = 0
    const state = await loadWechatPage('Login', {
      getCurrentUser: () => user.promise,
      createWechatLoginQRCode: () => ++creates === 1 ? Promise.resolve(qr('old')) : nextQRCode.promise,
    })
    await state.component.startWechatLogin()
    const completion = state.lifecycle.onStatus('success', { status: 'success', jwt: 'old-jwt' })
    if (action === 'refresh') void state.component.startWechatLogin()
    if (action === 'mode-change') {
      state.component.loginMode.value = 'password'
      state.component.handleLoginModeChange()
    }
    if (action === 'unmount') state.unmount()
    user.resolve({ status: 200, data: { id: 1, username: 'old-user' } })
    assert.equal(await completion, false, action)
    assert.equal(state.stored.length, 0, action)
    assert.equal(state.routes.length, 0, action)
  }
})

for (const page of ['Login', 'Profile']) {
  test(`${page} discards a late QR creation response after a newer QR succeeds`, async () => {
    const requests = []
    const state = await loadWechatPage(page, { createWechatLoginQRCode: () => { const request = deferred(); requests.push(request); return request.promise } })
    const start = page === 'Login' ? state.component.startWechatLogin : state.component.startWechatBind
    const first = start()
    const second = start()
    requests[1].resolve(qr('new'))
    await second
    requests[0].resolve(qr('old'))
    await first
    assert.equal(state.component.wechatQrCodeImageData.value, 'data:image/png;base64,new')
    assert.equal(state.sessions.length, 1)
    assert.equal(state.sessions[0].loginToken, 'new')
  })

  test(`${page} does not activate a pending QR after switching to code mode or unmounting`, async () => {
    for (const action of ['code-mode', 'unmount']) {
      const request = deferred()
      const state = await loadWechatPage(page, { createWechatLoginQRCode: () => request.promise })
      const pending = page === 'Login' ? state.component.startWechatLogin() : state.component.startWechatBind()
      if (action === 'unmount') state.unmount()
      else if (page === 'Login') {
        state.component.wechatLoginSubMode.value = 'code'
        state.component.handleWechatSubModeChange()
      } else {
        state.component.wechatBindSubMode.value = 'code'
        state.component.handleWechatBindSubModeChange()
      }
      request.resolve(qr('old'))
      await pending
      assert.equal(state.component.wechatQrCodeImageData.value, '', action)
      assert.equal(state.sessions.length, 0, action)
    }
  })
}

test('profile binding response cannot overwrite a refreshed QR while its creation is pending', async () => {
  const bind = deferred()
  const nextQRCode = deferred()
  let creates = 0
  const state = await loadWechatPage('Profile', {
    bindCurrentUserWechat: () => bind.promise,
    createWechatLoginQRCode: () => ++creates === 1 ? Promise.resolve(qr('old')) : nextQRCode.promise,
  })
  await state.component.startWechatBind()
  const completion = state.lifecycle.onStatus('need_bind', { status: 'need_bind', bindToken: 'old-bind' })
  void state.component.startWechatBind()
  bind.resolve({ status: 200, data: { id: 1, wechatId: 'old-wechat' } })
  assert.equal(await completion, false)
  assert.equal(state.stored.length, 0)
  assert.equal(state.component.currentUser.wechatId, undefined)
})
