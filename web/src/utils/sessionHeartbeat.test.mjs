import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import vm from 'node:vm'
import { parse, compileScript } from '@vue/compiler-sfc'
import { computed, ref, watch, nextTick, effectScope } from 'vue'

function deferred() {
  let resolve
  let reject
  const promise = new Promise((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

async function flush() {
  await new Promise((resolve) => setImmediate(resolve))
}

async function mountLayout({ visible = true, user = { id: 1 }, checkSession = async () => ({ status: 200 }), sessionRevision = () => '' } = {}) {
  const source = await readFile(new URL('../layout/TikuAdapterLayout.vue', import.meta.url), 'utf8')
  const { descriptor } = parse(source)
  const compiled = compileScript(descriptor, { id: 'session-heartbeat-regression' })
  const options = compiled.scriptAst.find((node) => node.type === 'ExportDefaultDeclaration').declaration
  const setup = options.properties.find((property) => property.key?.name === 'setup')
  const mounted = []
  const unmounted = []
  const timers = new Map()
  const browser = new EventTarget()
  browser.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} })
  browser.setTimeout = setTimeout; browser.clearTimeout = clearTimeout
  browser.setInterval = (callback, delay) => { const id = Symbol(); timers.set(id, { callback, delay }); return id }
  browser.clearInterval = (id) => timers.delete(id)
  const document = new EventTarget()
  document.visibilityState = visible ? 'visible' : 'hidden'
  document.querySelectorAll = () => []; document.activeElement = null
  const requests = []
  const routes = []
  const scope = {
    computed, ref, watch, nextTick,
    useCurrentUserDisplay: () => ({ currentUser: ref(user) }),
    currentUserIdentity: value => value?.id ? `id:${value.id}` : '',
    getCurrentUserLogoutEpoch: () => 0,
    message: { warning() {}, error() {} },
    onMounted: (callback) => mounted.push(callback),
    onBeforeUnmount: (callback) => unmounted.push(callback),
    useRouter: () => ({ currentRoute: { value: { path: '/profile' } }, push: (path) => routes.push(path) }),
    getStoredCurrentUser: () => user,
    getCurrentSessionRevision: sessionRevision,
    isAdminUser: () => true,
    checkUserSession: (signal) => { requests.push(signal); return checkSession(signal) },
    window: browser,
    document,
    AbortController,
  }
  const vueScope = effectScope()
  vueScope.run(() => vm.runInNewContext(`({${descriptor.script.content.slice(setup.start, setup.end)}})`, scope).setup())
  mounted.forEach((callback) => callback())
  return { browser, document, requests, routes, timers, unmount: () => { unmounted.forEach((callback) => callback()); vueScope.stop() } }
}

test('protected layout checks immediately, checks every 30 seconds, and stops after unmount', async () => {
  const state = await mountLayout()
  assert.equal(state.requests.length, 1, 'an idle protected page must detect displaced sessions')
  assert.equal(state.requests[0].aborted, false)
  await flush()
  const timer = [...state.timers.values()][0]
  assert.equal(timer.delay, 30_000)
  await timer.callback()
  assert.equal(state.requests.length, 2)
  state.unmount()
  assert.equal(state.timers.size, 0)
  state.browser.dispatchEvent(new Event('focus'))
  state.document.dispatchEvent(new Event('visibilitychange'))
  assert.equal(state.requests.length, 2)
})

test('hidden pages pause checks and resume immediately on visibility or focus', async () => {
  const state = await mountLayout({ visible: false })
  assert.equal(state.requests.length, 0)
  const timer = [...state.timers.values()][0]
  assert.ok(timer, 'a hidden page must keep a timer ready for later checks')
  await timer.callback()
  assert.equal(state.requests.length, 0)
  state.document.visibilityState = 'visible'
  state.document.dispatchEvent(new Event('visibilitychange'))
  assert.equal(state.requests.length, 1)
  await flush()
  state.browser.dispatchEvent(new Event('focus'))
  assert.equal(state.requests.length, 2)
  state.unmount()
})

test('heartbeats do not overlap and unmount aborts the pending check', async () => {
  const pending = deferred()
  const state = await mountLayout({ checkSession: () => pending.promise })
  assert.equal(state.requests.length, 1)
  state.browser.dispatchEvent(new Event('focus'))
  state.document.dispatchEvent(new Event('visibilitychange'))
  await [...state.timers.values()][0].callback()
  assert.equal(state.requests.length, 1)
  state.unmount()
  assert.equal(state.requests[0].aborted, true)
  pending.resolve({ status: 200 })
  await flush()
  assert.equal(state.timers.size, 0)
})

test('a temporary network failure does not end later session checks', async () => {
  const pending = deferred()
  const state = await mountLayout({ checkSession: () => pending.promise })
  assert.equal(state.requests.length, 1)
  pending.reject(new Error('Network Error'))
  await flush()
  state.browser.dispatchEvent(new Event('focus'))
  assert.equal(state.requests.length, 2)
  state.unmount()
})

test('a rejected session stops periodic checks while the global handler logs out', async () => {
  const state = await mountLayout({ checkSession: async () => ({ status: 401 }) })
  await flush()
  assert.equal(state.requests.length, 1)
  assert.equal(state.timers.size, 0)
  state.browser.dispatchEvent(new Event('focus'))
  assert.equal(state.requests.length, 1)
  state.unmount()
})

test('a late heartbeat from an earlier login keeps checking the new shared session', async () => {
  let revision = 'old-session'
  const pending = deferred()
  const state = await mountLayout({ checkSession: () => pending.promise, sessionRevision: () => revision })
  assert.equal(state.requests.length, 1)
  revision = 'new-session'
  pending.resolve({ status: 401 })
  await flush()
  assert.equal(state.timers.size, 1, 'a late old-session failure must not stop the new session heartbeat')
  state.browser.dispatchEvent(new Event('focus'))
  assert.equal(state.requests.length, 2)
  state.unmount()
})

test('an unauthenticated layout redirects without issuing a heartbeat', async () => {
  const state = await mountLayout({ user: null })
  assert.equal(state.requests.length, 0)
  assert.equal(state.timers.size, 0)
  assert.deepEqual(state.routes, ['/login'])
  state.unmount()
})
