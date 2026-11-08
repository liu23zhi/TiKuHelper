/* Actual App/Layout/Sider/VueRouter/Antd. Synthetic leaf/draft/leave guard,
 * persistent later controls/Antd dialog and public metadata API only.
 * Enter is handled by actual Antd rc-menu; no simulated browser default click.
 * jsdom is not physical keyboard, mobile geometry or reader certification. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), fs = require('node:fs'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter); Module._initPaths()
const helper = require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises, DOMWrapper } = require('@vue/test-utils'), { h, ref, nextTick } = require('vue'), { createRouter, createMemoryHistory, onBeforeRouteLeave, isNavigationFailure, NavigationFailureType } = require('vue-router'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const FILES = ['src/layout/TikuAdapterLayout.vue', 'src/components/SiderMenu/index.vue', 'src/App.vue', 'src/utils/currentUser.js'], sha = p => crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'), sourceHashes = Object.fromEntries(FILES.map(p => [p, sha(path.join(web, p))]))
const DRAFT = 'SYNTHETIC 未保存草稿'
let roots = [], histories = [], releases = [], network = [], runtime = [], routerErrors = [], apiCalls = []
function deny(kind) { network.push(kind); throw new Error('FORBIDDEN_DESKTOP_FOCUS_IO_' + kind) }
window.XMLHttpRequest.prototype.open = () => deny('XHR'); globalThis.fetch = window.fetch = () => deny('FETCH'); window.open = () => deny('WINDOW_OPEN')
globalThis.WebSocket = window.WebSocket = class { constructor() { deny('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => deny(name); transport.get = () => deny(name) }
window.addEventListener('error', event => runtime.push(String(event.error?.name || event.message))); window.addEventListener('unhandledrejection', event => runtime.push(String(event.reason?.name || event.reason)))
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = {
  getPublicRegisterStatus: () => { apiCalls.push('synthetic-public-metadata'); return Promise.resolve({ status: 200, data: { siteName: 'SYNTHETIC_DESKTOP_FOCUS' } }) },
  logout: () => { apiCalls.push('synthetic-logout'); return Promise.resolve() },
}; require.cache[apiPath] = api
const Sider = require(path.join(web, 'src/components/SiderMenu/index.vue')), Layout = require(path.join(web, 'src/layout/TikuAdapterLayout.vue')), App = require(path.join(web, 'src/App.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 10)); await nextTick() }
async function until(check, message) { const start = performance.now(); while (!check() && performance.now() - start < 1500) await settle(); assert.ok(check(), message) }
function visible(node) { if (!node?.isConnected) return false; for (let p = node; p; p = p.parentElement) { const s = getComputedStyle(p); if (p.hidden || p.getAttribute('aria-hidden') === 'true' || s.display === 'none' || s.visibility === 'hidden') return false } return true }
function button(label) { const b = [...document.querySelectorAll('button')].find(n => n.getAttribute('aria-label') === label); assert.ok(b, 'actual shell native button ' + label); return b }
function drawer(state) { return state.root.findComponent(Antd.Drawer) }
function openState(state) { const d = drawer(state); return d.exists() && d.props('open') === true }
function navNode(state) { const node = document.getElementById(state.mobile ? 'mobile-portal-navigation' : 'portal-navigation'); assert.ok(node?.isConnected, 'owned actual Antd menu DOM'); return node }
function sider(state) { const id = state.mobile ? 'mobile-portal-navigation' : 'portal-navigation'; const s = state.root.findAllComponents(Sider).find(c => c.props('navigationId') === id); assert.ok(s, 'owned actual Sider'); return s }
function main() { const node = document.querySelector('main#portal-main-content'); assert.ok(node?.isConnected); return node }
function assertHighlight(state) { assert.deepEqual([...navNode(state).querySelectorAll('.ant-menu-item-selected')].map(n => n.querySelector('a')?.getAttribute('href')), [state.router.currentRoute.value.path]) }
async function openMobile(state) {
  button('打开导航').click()
  await until(() => { const nav = document.getElementById('mobile-portal-navigation'), outer = nav?.closest('.ant-drawer'), close = document.querySelector('button[aria-label="关闭导航"]'); return Boolean(openState(state) && outer?.classList.contains('ant-drawer-open') && outer.contains(close) && close?.isConnected && !close.disabled && visible(close) && nav.querySelector('a.navigation-link')) }, 'actual open Drawer owns connected visible enabled navigation and close; no inner-focus precondition')
  sider(state); await settle()
}
async function activate(state, key, mode = 'click') {
  const link = navNode(state).querySelector('a[href="/adapter/' + key + '"]'); assert.ok(link?.isConnected && visible(link)); link.focus(); assert.strictEqual(document.activeElement, link, 'native navigation control receives user focus before activation')
  if (mode === 'Enter') link.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true, cancelable: true }))
  else link.click()
  await settle(); return link
}
async function setDraft(value = DRAFT) { const input = document.querySelector('input[data-navigation-draft]'); assert.ok(input); input.value = value; await new DOMWrapper(input).trigger('input'); await settle(); assert.equal(input.value, value) }
async function app({ mobile = false, refuse = false, redirect = false, pending = false } = {}) {
  helper.setViewport(mobile ? 360 : 1440); user.setStoredCurrentUser({ id: 9891, username: 'SYNTHETIC_NAV_ACTOR', role: 'admin', status: 'enabled', balance: 1 })
  const state = { mobile, guards: [], pushes: [], release: null, pending, root: null, router: null, showDialog: null }
  const Leaf = { setup() {
    const draft = ref('SYNTHETIC 初始草稿')
    onBeforeRouteLeave((to, from) => { state.guards.push({ from: from.path, to: to.path }); if (state.pending && to.path === '/adapter/user-usage') return new Promise(resolve => { state.release = resolve; releases.push(resolve) }); return !refuse })
    return () => h('section', { 'data-synthetic-leaf': '' }, [h('h1', 'SYNTHETIC guarded leaf'), h('input', { 'data-navigation-draft': '', 'aria-label': '合成未保存草稿', value: draft.value, onInput: event => { draft.value = event.target.value } })])
  } }
  const Destination = { render: () => h('h1', { 'data-synthetic-destination': '' }, 'SYNTHETIC destination') }
  const target = redirect ? { path: 'user-usage', redirect: '/adapter/component' } : { path: 'user-usage', component: Destination }
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter', component: Layout, children: [{ path: 'profile', component: Leaf }, { path: 'component', component: Destination }, target] }] }); state.router = router
  router.onError(error => routerErrors.push(error)); await router.push('/adapter/profile'); await router.isReady(); histories.push(router.options.history)
  const Host = { setup() { const dialogVisible = ref(false); state.showDialog = () => { dialogVisible.value = true }; return () => h('div', { 'data-synthetic-host': '' }, [h('button', { 'data-later-control': '', onClick: state.showDialog }, '打开合成后来对话框'), h(App), h(Antd.Modal, { open: dialogVisible.value, title: '合成后来对话框', footer: null, onCancel: () => { dialogVisible.value = false } }, { default: () => h('input', { 'data-later-dialog-input': '', 'aria-label': '合成新对话框输入' }) })]) } }
  const root = mount(Host, { attachTo: document.body, global: { plugins: [router, Antd], config: { errorHandler: error => runtime.push(String(error?.name || error)) } } }); state.root = root; roots.push(root); await settle(); await setDraft()
  const push = router.push.bind(router); router.push = to => push(to).then(result => { state.pushes.push({ to, route: router.currentRoute.value.path, failure: isNavigationFailure(result), aborted: isNavigationFailure(result, NavigationFailureType.aborted), cancelled: isNavigationFailure(result, NavigationFailureType.cancelled), duplicated: isNavigationFailure(result, NavigationFailureType.duplicated) }); return result })
  if (mobile) await openMobile(state)
  assertHighlight(state); return state
}
async function settledPush(state, count = 1) { await until(() => state.pushes.length >= count, 'real router reaches observed terminal outcome'); await settle() }
async function admittedMain(state, route) {
  await settledPush(state); assert.equal(state.router.currentRoute.value.path, route); assert.equal(state.pushes.at(-1).failure, false); assert.ok(main().querySelector('[data-synthetic-destination]'), 'new actual route leaf is rendered inside main before focus acceptance'); assertHighlight(state)
  await until(() => document.activeElement === main(), 'admitted desktop navigation transfers focus to rendered actual main')
}
function witness(label, state, extra = {}) { console.log('DESKTOP_FOCUS_NATIVE ' + JSON.stringify({ label, route: state.router.currentRoute.value.fullPath, activeTag: document.activeElement.tagName, activeId: document.activeElement.id, activeHref: document.activeElement.getAttribute('href'), drawerOpen: state.mobile ? openState(state) : null, pushes: state.pushes, guards: state.guards, realIO: network, runtime, physicalKeyboardOrDeviceClaim: false, ...extra })) }
afterEach(async () => {
  for (const resolve of releases) resolve(true)
  await settle(); for (const root of roots) if (root.exists()) root.unmount(); await settle(); for (const history of histories) history.destroy(); user.clearStoredCurrentUser(); localStorage.clear(); window.sessionStorage.clear(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren(); helper.setViewport(1440); helper.setSystemDark(false)
  console.log('DESKTOP_FOCUS_SAFETY ' + JSON.stringify({ sourceHashes: Object.fromEntries(FILES.map(p => [p, sha(path.join(web, p))])), apiCalls, realIO: network, unexpectedRuntime: runtime, routerErrors: routerErrors.length }))
  for (const p of FILES) assert.equal(sha(path.join(web, p)), sourceHashes[p]); assert.deepEqual(network, []); assert.deepEqual(runtime, []); assert.deepEqual(routerErrors, [])
  roots = []; histories = []; releases = []; network = []; runtime = []; routerErrors = []; apiCalls = []
})

test('DF01 actual Antd Enter admits desktop navigation and focuses main after the new leaf renders', async () => {
  const state = await app(); await activate(state, 'user-usage', 'Enter'); await admittedMain(state, '/adapter/user-usage'); witness('actual-Antd-Enter', state)
})
test('DF02 native redirected desktop navigation focuses actual destination main, not requested leaf', async () => {
  const state = await app({ redirect: true }); await activate(state, 'user-usage'); await admittedMain(state, '/adapter/component'); witness('redirect', state)
})
test('DF03 refused native navigation preserves menu focus, committed route/highlight and unsaved draft', async () => {
  const state = await app({ refuse: true }), link = await activate(state, 'user-usage'); await settledPush(state); assert.equal(state.pushes[0].aborted, true); assert.equal(state.router.currentRoute.value.path, '/adapter/profile'); assertHighlight(state); assert.equal(document.querySelector('input[data-navigation-draft]')?.value, DRAFT); assert.strictEqual(document.activeElement, link, 'refused leave cannot move focus to main'); assert.equal(sider(state).emitted('navigate')?.length || 0, 0); witness('refused', state)
})
test('DF04 delayed old navigation cannot steal later native-control or actual new-dialog focus; cancelled old intent stays retired', async () => {
  const state = await app({ pending: true }); await activate(state, 'user-usage'); await until(() => Boolean(state.release), 'actual leave guard is pending')
  const later = document.querySelector('button[data-later-control]'); later.focus(); assert.strictEqual(document.activeElement, later); state.release(true); await settledPush(state); assert.equal(state.router.currentRoute.value.path, '/adapter/user-usage'); assert.strictEqual(document.activeElement, later, 'late admitted completion preserves newer native control focus'); assert.ok(main().querySelector('[data-synthetic-destination]'))
  await state.router.push('/adapter/profile'); await settle(); state.release = null; await activate(state, 'user-usage'); await until(() => Boolean(state.release), 'second actual guard is pending'); later.click()
  await until(() => { const input = document.querySelector('input[data-later-dialog-input]'), wrap = input?.closest('.ant-modal-wrap'), close = input?.closest('.ant-modal-content')?.querySelector('button.ant-modal-close'); return Boolean(state.root.findComponent(Antd.Modal).props('open') === true && input?.isConnected && !input.disabled && visible(input) && wrap?.contains(input) && close?.isConnected && !close.disabled && visible(close)) }, 'new actual Antd dialog is open and owns visible enabled field/close before native later field focus')
  const field = document.querySelector('input[data-later-dialog-input]'); field.focus(); assert.strictEqual(document.activeElement, field); state.release(true); await settledPush(state, 3); assert.strictEqual(document.activeElement, field, 'late admitted completion cannot take focus out of later dialog'); assert.equal(state.router.currentRoute.value.path, '/adapter/user-usage')
  // A cancelled old route is already exercised by the outcome contract; here a real
  // background navigation supersedes another pending route while the later dialog owns focus.
  await state.router.push('/adapter/profile'); await settle(); state.release = null
  const old = state.router.push('/adapter/user-usage'); await until(() => Boolean(state.release), 'old actual background guard is pending'); await state.router.push('/adapter/component'); await settle(); assert.strictEqual(document.activeElement, field); state.release(true); const failure = await old; await settle(); assert.equal(isNavigationFailure(failure, NavigationFailureType.cancelled), true); assert.equal(state.router.currentRoute.value.path, '/adapter/component'); assert.strictEqual(document.activeElement, field); witness('later-control-dialog-and-cancelled', state)
})
test('DF05 query/hash/background navigation never globally grabs focus; admitted duplicate native activation can focus main', async () => {
  const state = await app(), later = document.querySelector('button[data-later-control]'); later.focus()
  for (const target of ['/adapter/profile?synthetic=background', '/adapter/profile?synthetic=background#synthetic', '/adapter/component', '/adapter/profile']) { await state.router.push(target); await settle(); assert.strictEqual(document.activeElement, later, 'background/query/hash route changes preserve current native control focus') }
  await setDraft(); const before = state.pushes.length; await activate(state, 'profile'); await settledPush(state, before + 1); assert.equal(state.pushes.at(-1).duplicated, true); assert.equal(document.querySelector('input[data-navigation-draft]')?.value, DRAFT); assertHighlight(state)
  await until(() => document.activeElement === main(), 'admitted duplicate click follows existing navigate admission and focuses main'); witness('background-and-duplicate', state)
})
test('DF06 existing mobile successful navigation returns main focus and manual close returns trigger focus', async () => {
  const state = await app({ mobile: true }); await activate(state, 'user-usage'); await settledPush(state); assert.equal(state.router.currentRoute.value.path, '/adapter/user-usage'); assert.ok(main().querySelector('[data-synthetic-destination]')); await until(() => !openState(state) && document.activeElement === main(), 'existing mobile close completes into actual main focus')
  await openMobile(state); button('关闭导航').click(); await until(() => !openState(state) && document.activeElement === button('打开导航'), 'manual close restores native mobile navigation trigger'); witness('mobile-positive', state)
})
