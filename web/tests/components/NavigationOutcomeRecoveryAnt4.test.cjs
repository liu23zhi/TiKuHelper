/* Actual App/Layout/Sider/VueRouter/Antd and native navigation controls.
 * Only leaf content/leave decision and public metadata/logout API are synthetic.
 * No physical mobile geometry, login/provider, actual draft-confirm Modal claim. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), fs = require('node:fs'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter); Module._initPaths()
const helper = require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises, DOMWrapper } = require('@vue/test-utils'), { h, ref, nextTick } = require('vue'), { createRouter, createMemoryHistory, onBeforeRouteLeave, isNavigationFailure, NavigationFailureType } = require('vue-router'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const FILES = ['src/components/SiderMenu/index.vue', 'src/layout/TikuAdapterLayout.vue', 'src/App.vue', 'src/utils/currentUser.js'], sha = p => crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'), sourceHashes = Object.fromEntries(FILES.map(p => [p, sha(path.join(web, p))]))
const PRIVATE = 'SYNTHETIC_PRIVATE_NAVIGATION_ERROR_URL', DRAFT = 'SYNTHETIC 未保存草稿', FIXED = '页面暂时无法打开，请稍后重试。'
let roots = [], histories = [], releases = [], network = [], runtime = [], routerErrors = [], apiCalls = [], notices = [], expectedRouterErrorCount = 0
function deny(kind) { network.push(kind); throw new Error('FORBIDDEN_NAVIGATION_OUTCOME_IO_' + kind) }
window.XMLHttpRequest.prototype.open = () => deny('XHR'); globalThis.fetch = window.fetch = () => deny('FETCH'); window.open = () => deny('WINDOW_OPEN')
globalThis.WebSocket = window.WebSocket = class { constructor() { deny('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => deny(name); transport.get = () => deny(name) }
window.addEventListener('error', event => runtime.push(String(event.error?.name || event.message))); window.addEventListener('unhandledrejection', event => runtime.push(String(event.reason?.name || event.reason)))
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = {
  getPublicRegisterStatus: () => { apiCalls.push('synthetic-public-metadata'); return Promise.resolve({ status: 200, data: { siteName: 'SYNTHETIC_NAV_OUTCOME' } }) },
  logout: () => { apiCalls.push('synthetic-logout'); return Promise.resolve() },
}; require.cache[apiPath] = api
const nativeMessageError = Antd.message.error.bind(Antd.message)
Antd.message.error = (...args) => { notices.push(String(args[0]?.content || args[0] || '')); return nativeMessageError(...args) }
const Sider = require(path.join(web, 'src/components/SiderMenu/index.vue')), Layout = require(path.join(web, 'src/layout/TikuAdapterLayout.vue')), App = require(path.join(web, 'src/App.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 10)); await nextTick() }
async function until(check, message) { const start = performance.now(); while (!check() && performance.now() - start < 1500) await settle(); assert.ok(check(), message) }
function visible(node) { if (!node?.isConnected) return false; for (let p = node; p; p = p.parentElement) { const s = getComputedStyle(p); if (p.hidden || p.getAttribute('aria-hidden') === 'true' || s.display === 'none' || s.visibility === 'hidden') return false } return true }
function button(label) { const b = [...document.querySelectorAll('button')].find(n => n.getAttribute('aria-label') === label); assert.ok(b, 'actual shell native button ' + label); return b }
function drawer(state) { return state.root.findComponent(Antd.Drawer) }
function openState(state) { const d = drawer(state); return d.exists() && d.props('open') === true }
function sider(state) { const id = state.mobile ? 'mobile-portal-navigation' : 'portal-navigation'; const s = state.root.findAllComponents(Sider).find(c => c.props('navigationId') === id); assert.ok(s, 'actual owned Sider ' + id); return s }
function navNode(state) { const id = state.mobile ? 'mobile-portal-navigation' : 'portal-navigation'; const node = document.getElementById(id); assert.ok(node && node.isConnected, 'owned actual Antd menu DOM'); return node }
function selected(state) { return [...navNode(state).querySelectorAll('.ant-menu-item-selected')].map(n => n.querySelector('a')?.getAttribute('href')) }
function assertActualHighlight(state, path = state.router.currentRoute.value.path) { assert.deepEqual(selected(state), [path], 'actual Antd selected menu follows actual committed route, not refused/requested target') }
async function openMobile(state) {
  button('打开导航').click()
  await until(() => openState(state) && document.querySelector('#mobile-portal-navigation')?.isConnected && visible(document.querySelector('.mobile-navigation-dialog')), 'actual mobile Drawer is open and owns connected navigation controls')
  await until(() => { const nav = document.getElementById('mobile-portal-navigation'), outer = nav?.closest('.ant-drawer'), close = document.querySelector('button[aria-label="关闭导航"]'); return Boolean(outer?.classList.contains('ant-drawer-open') && outer.contains(close) && close?.isConnected && !close.disabled && visible(close) && nav.querySelector('a.navigation-link')) }, 'actual open Drawer owns visible enabled navigation and close controls')
  sider(state); await settle()
}
async function nativeMenu(state, key) { const link = navNode(state).querySelector('a[href="/adapter/' + key + '"]'); assert.ok(link, 'actual native navigation link ' + key); link.click(); await settle() }
async function app({ mobile = true, refuse = false, redirect = false, pending = false, throwing = false } = {}) {
  helper.setViewport(mobile ? 360 : 1440); user.setStoredCurrentUser({ id: 9891, username: 'SYNTHETIC_NAV_ACTOR', role: 'admin', status: 'enabled', balance: 1 })
  const state = { mobile, guards: [], pushes: [], release: null, thrown: new Error(PRIVATE), root: null, router: null }
  const Leaf = { setup() {
    const draft = ref('SYNTHETIC 初始草稿')
    onBeforeRouteLeave((to, from) => {
      state.guards.push({ from: from.path, to: to.path })
      if (throwing) throw state.thrown
      if (pending && to.path === '/adapter/user-usage') return new Promise(resolve => { state.release = resolve; releases.push(resolve) })
      return !refuse
    })
    return () => h('section', { 'data-synthetic-leaf': '' }, [h('h1', 'SYNTHETIC guarded leaf'), h('input', { 'data-navigation-draft': '', 'aria-label': '合成未保存草稿', value: draft.value, onInput: event => { draft.value = event.target.value } })])
  } }
  const Destination = { render: () => h('h1', { 'data-synthetic-destination': '' }, 'SYNTHETIC destination') }
  const target = redirect ? { path: 'user-usage', redirect: '/adapter/component' } : { path: 'user-usage', component: Destination }
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter', component: Layout, children: [{ path: 'profile', component: Leaf }, { path: 'component', component: Destination }, target] }] }); state.router = router
  router.onError(error => routerErrors.push(error)); await router.push('/adapter/profile'); await router.isReady(); histories.push(router.options.history)
  const root = mount(App, { attachTo: document.body, global: { plugins: [router, Antd], config: { errorHandler: error => runtime.push(String(error?.name || error)) } } }); state.root = root; roots.push(root); await settle()
  const input = document.querySelector('input[data-navigation-draft]'); assert.ok(input); input.value = DRAFT; await new DOMWrapper(input).trigger('input'); await settle(); assert.equal(input.value, DRAFT)
  const push = router.push.bind(router)
  router.push = to => push(to).then(result => { state.pushes.push({ to, route: router.currentRoute.value.path, failure: isNavigationFailure(result), aborted: isNavigationFailure(result, NavigationFailureType.aborted), cancelled: isNavigationFailure(result, NavigationFailureType.cancelled), duplicated: isNavigationFailure(result, NavigationFailureType.duplicated), type: result?.type || null }); return result }, error => { state.pushes.push({ to, route: router.currentRoute.value.path, rejected: true, sameOriginalError: error === state.thrown }); throw error })
  if (mobile) await openMobile(state)
  assertActualHighlight(state, '/adapter/profile'); return state
}
async function settledPush(state, count = 1) { await until(() => state.pushes.length >= count, 'real router promise reaches observed terminal outcome'); await settle() }
function witness(label, state, extra = {}) { console.log('NAVIGATION_OUTCOME_NATIVE ' + JSON.stringify({ label, route: state.router.currentRoute.value.path, drawerOpen: state.mobile ? openState(state) : null, pushes: state.pushes, guards: state.guards, routerErrorCount: routerErrors.length, expectedRouterErrorCount, notices, network, unexpectedRuntime: runtime, physicalMobileClaim: false, ...extra })) }
afterEach(async () => {
  for (const resolve of releases) resolve(true)
  await settle(); for (const root of roots) if (root.exists()) root.unmount(); await settle(); for (const history of histories) history.destroy(); user.clearStoredCurrentUser(); localStorage.clear(); window.sessionStorage.clear(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren(); helper.setViewport(1440); helper.setSystemDark(false)
  console.log('NAVIGATION_OUTCOME_SAFETY ' + JSON.stringify({ sourceHashes: Object.fromEntries(FILES.map(p => [p, sha(path.join(web, p))])), apiCalls, realIO: network, unexpectedRuntime: runtime, routerErrorsObserved: routerErrors.length, expectedRouterErrors: expectedRouterErrorCount }))
  for (const p of FILES) assert.equal(sha(path.join(web, p)), sourceHashes[p]); assert.deepEqual(network, []); assert.deepEqual(runtime, []); assert.equal(routerErrors.length, expectedRouterErrorCount)
  roots = []; histories = []; releases = []; network = []; runtime = []; routerErrors = []; apiCalls = []; notices = []; expectedRouterErrorCount = 0
})

for (const mobile of [true, false]) test('NO01 ' + (mobile ? 'mobile' : 'desktop') + ' refused leave preserves actual route, actual highlight and leaf draft; mobile Drawer stays open', async () => {
  const state = await app({ mobile, refuse: true }), current = sider(state); await nativeMenu(state, 'user-usage'); await settledPush(state); witness('refused-' + mobile, state)
  assert.equal(state.pushes[0].aborted, true, 'actual VueRouter guard returned aborted failure'); assert.equal(state.router.currentRoute.value.path, '/adapter/profile'); assertActualHighlight(state); assert.equal(document.querySelector('input[data-navigation-draft]')?.value, DRAFT); assert.equal(current.emitted('navigate')?.length || 0, 0, 'failed leave is not a successful navigate event'); if (mobile) assert.equal(openState(state), true)
})
test('NO02 allowed native mobile navigation closes Drawer and highlight follows actual successful route', async () => {
  const state = await app(); await nativeMenu(state, 'user-usage'); await settledPush(state); assert.equal(state.pushes[0].failure, false); assert.equal(state.router.currentRoute.value.path, '/adapter/user-usage'); assert.equal(openState(state), false); await openMobile(state); assertActualHighlight(state); witness('success', state)
})
test('NO03 native redirected navigation closes Drawer and highlights actual redirect destination', async () => {
  const state = await app({ redirect: true }); await nativeMenu(state, 'user-usage'); await settledPush(state); assert.equal(state.pushes[0].failure, false); assert.equal(state.router.currentRoute.value.path, '/adapter/component'); assert.equal(openState(state), false); await openMobile(state); assertActualHighlight(state, '/adapter/component'); witness('redirect', state)
})
test('NO04 duplicate current-page native click retains original mobile close behavior, not blanket failure suppression', async () => {
  const state = await app(); await nativeMenu(state, 'profile'); await settledPush(state); assert.equal(state.pushes[0].duplicated, true); assert.equal(state.router.currentRoute.value.path, '/adapter/profile'); assert.equal(openState(state), false); await openMobile(state); assertActualHighlight(state); assert.equal(document.querySelector('input[data-navigation-draft]')?.value, DRAFT); witness('duplicate-close', state)
})
test('NO05 superseded old real navigation cannot close a later opened Drawer or overwrite actual route/highlight', async () => {
  const state = await app({ pending: true }); await nativeMenu(state, 'user-usage'); await until(() => Boolean(state.release), 'actual leaf guard holds first navigation'); await nativeMenu(state, 'component'); await until(() => state.router.currentRoute.value.path === '/adapter/component', 'new native intent commits its actual route'); await settle(); assert.equal(openState(state), false); await openMobile(state); assertActualHighlight(state); state.release(true); await settledPush(state, 2); const old = state.pushes.find(p => p.to === '/adapter/user-usage'); assert.ok(old?.cancelled, 'actual VueRouter cancellation, not mocked failure result'); witness('superseded', state); assert.equal(openState(state), true, 'old cancelled completion does not close later Drawer'); assert.equal(state.router.currentRoute.value.path, '/adapter/component'); assertActualHighlight(state)
})
test('NO06 real throwing guard preserves same rejection/onError and reports fixed safe feedback without wrong highlight/Drawer close', async () => {
  const state = await app({ throwing: true }); expectedRouterErrorCount = 1
  await assert.rejects(() => sider(state).vm.handleSelect({ key: 'user-usage' }), error => error === state.thrown, 'public method preserves original router rejection rather than swallowing it'); await settle(); assert.strictEqual(routerErrors[0], state.thrown); assert.equal(state.router.currentRoute.value.path, '/adapter/profile'); assertActualHighlight(state); assert.equal(openState(state), true); assert.ok(notices.includes(FIXED), 'fixed public navigation feedback'); assert.ok(!document.body.textContent.includes(PRIVATE) && !notices.some(n => n.includes(PRIVATE))); assert.equal(document.querySelector('input[data-navigation-draft]')?.value, DRAFT); witness('throw', state)
})
