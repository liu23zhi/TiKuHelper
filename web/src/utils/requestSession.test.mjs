import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import vm from 'node:vm'
import { buildAuthFailureHandling, buildResponsePayload, isPublicWechatRegisterRequest } from './requestHelpers.mjs'

function deferred() {
  let resolve
  const promise = new Promise((done) => { resolve = done })
  return { promise, resolve }
}

async function loadRequest(send) {
  const source = await readFile(new URL('./request.js', import.meta.url), 'utf8')
  const currentUser = await readFile(new URL('./currentUser.js', import.meta.url), 'utf8')
  const storage = new Map([['adapter_current_user', JSON.stringify({ id: 1, nickname: 'old' })]])
  const browser = {
    localStorage: { getItem: (key) => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value), removeItem: (key) => storage.delete(key) },
    location: { href: '/adapter/profile' },
  }
  const scope = {
    window: browser,
    axios: { create: () => ({ request: send }) },
    executeWithRetry: (request, config) => request(config),
    toUserRequestError: (_config, error) => error,
    buildAuthFailureHandling,
    buildResponsePayload,
    isPublicWechatRegisterRequest,
    message: { error() {} },
    console,
  }
  const plain = (code) => code.replace(/^import .+$/gm, '').replace(/export default /g, '').replace(/export /g, '')
  const request = vm.runInNewContext(`${plain(currentUser)}\n${plain(source)}\nrequest`, scope)
  return { request, browser, storage }
}

test('a late 401 from the previous session cannot clear a successful new login', async () => {
  const oldRequest = deferred()
  const state = await loadRequest((config) => config.url.endsWith('/login')
    ? Promise.resolve({ status: 200, data: { jwt: 'new-session' }, config })
    : oldRequest.promise)
  const old = state.request({ url: '/adapter-service/user', method: 'get' })
  await state.request({ url: '/adapter-service/user/login', method: 'post' })
  state.storage.set('adapter_current_user', JSON.stringify({ id: 1, nickname: 'new' }))
  oldRequest.resolve({ status: 401, data: { message: '账号已在其他设备登录，请重新登录' } })
  await old
  assert.equal(state.browser.location.href, '/adapter/profile')
  assert.equal(JSON.parse(state.storage.get('adapter_current_user')).nickname, 'new')
})

test('a current-session 401 still clears user state and returns to login', async () => {
  const state = await loadRequest(async (config) => ({ status: 401, data: { message: '账号已在其他设备登录，请重新登录' }, config }))
  await state.request({ url: '/adapter-service/user/session', method: 'get' })
  assert.equal(state.storage.has('adapter_current_user'), false)
  assert.equal(state.browser.location.href, '/login')
})

for (const failure of ['SecurityError', 'QuotaExceededError']) {
  test(`${failure} cannot fail requests or let an old 401 clear a new in-memory session`, async () => {
    const oldRequest = deferred()
    const state = await loadRequest((config) => config.url.endsWith('/login')
      ? Promise.resolve({ status: 200, data: { jwt: 'new-session' }, config })
      : oldRequest.promise)
    state.storage.set('adapter_session_revision', 'persisted-old-session')
    if (failure === 'SecurityError') {
      Object.defineProperty(state.browser, 'localStorage', { get() { throw new DOMException('Storage blocked', failure) } })
    } else {
      state.browser.localStorage.setItem = () => { throw new DOMException('Storage full', failure) }
    }
    const old = state.request({ url: '/adapter-service/user', method: 'get' }).then(
      (response) => ({ response }),
      (error) => ({ error }),
    )
    await assert.doesNotReject(state.request({ url: '/adapter-service/user/login', method: 'post' }))
    oldRequest.resolve({ status: 401, data: { message: '账号已在其他设备登录，请重新登录' } })
    const result = await old
    assert.equal(result.error, undefined)
    assert.equal(result.response.status, 401)
    assert.equal(state.browser.location.href, '/adapter/profile')
    assert.equal(state.storage.has('adapter_current_user'), true)
  })
}
