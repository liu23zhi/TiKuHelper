/* Actual App/Layout/Sider/VueRouter/Antd Drawer + higher host Antd Modal.
 * Only leaf data/models, host foreground response and public metadata API are synthetic.
 * The later field is explicitly focused and really edited; no automatic-enter focus,
 * rootClass ownership marker, backdrop click or browser focus shim is required.
 * jsdom is not physical mobile hit-testing, geometry, animation or reader certification. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), fs = require('node:fs'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter); Module._initPaths()
const helper = require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises, DOMWrapper } = require('@vue/test-utils'), { h, ref, nextTick } = require('vue'), { createRouter, createMemoryHistory, isNavigationFailure } = require('vue-router'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const FILES = ['src/layout/TikuAdapterLayout.vue', 'src/components/SiderMenu/index.vue', 'src/App.vue', 'src/utils/currentUser.js'], sha = p => crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'), sourceHashes = Object.fromEntries(FILES.map(p => [p, sha(path.join(web, p))]))
const DRAFT = 'SYNTHETIC 保留草稿', LATER = 'SYNTHETIC 后来实际输入'
let roots = [], histories = [], network = [], runtime = [], routerErrors = [], apiCalls = []
function deny(kind) { network.push(kind); throw new Error('FORBIDDEN_MOBILE_FOCUS_IO_' + kind) }
window.XMLHttpRequest.prototype.open = () => deny('XHR'); globalThis.fetch = window.fetch = () => deny('FETCH'); window.open = () => deny('WINDOW_OPEN')
globalThis.WebSocket = window.WebSocket = class { constructor() { deny('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => deny(name); transport.get = () => deny(name) }
window.addEventListener('error', event => runtime.push(String(event.error?.name || event.message))); window.addEventListener('unhandledrejection', event => runtime.push(String(event.reason?.name || event.reason)))
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = { getPublicRegisterStatus: () => { apiCalls.push('synthetic-public-metadata'); return Promise.resolve({ status: 200, data: { siteName: 'SYNTHETIC_MOBILE_FOCUS' } }) }, logout: () => { apiCalls.push('synthetic-logout'); return Promise.resolve() } }; require.cache[apiPath] = api
const Sider = require(path.join(web, 'src/components/SiderMenu/index.vue')), Layout = require(path.join(web, 'src/layout/TikuAdapterLayout.vue')), App = require(path.join(web, 'src/App.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 10)); await nextTick() }
async function until(check, message) { const start = performance.now(); while (!check() && performance.now() - start < 1500) await settle(); assert.ok(check(), message) }
function visible(node) { if (!node?.isConnected) return false; for (let p = node; p; p = p.parentElement) { const s = getComputedStyle(p); if (p.hidden || p.inert || p.getAttribute('aria-hidden') === 'true' || s.display === 'none' || s.visibility === 'hidden') return false } return true }
function button(label) { const b = document.querySelector('button[aria-label="' + label + '"]'); assert.ok(b?.isConnected, 'actual navigation button ' + label); return b }
function drawer(state) { return state.root.findComponent(Antd.Drawer) }
function openState(state) { const d = drawer(state); return d.exists() && d.props('open') === true }
function main() { const node = document.querySelector('main#portal-main-content'); assert.ok(node?.isConnected); return node }
async function openMobile(state) {
  const trigger = button('打开导航'); trigger.focus(); trigger.click()
  await until(() => { const nav = document.getElementById('mobile-portal-navigation'), outer = nav?.closest('.ant-drawer'), close = document.querySelector('button[aria-label="关闭导航"]'); return Boolean(openState(state) && outer?.classList.contains('ant-drawer-open') && outer.contains(close) && close?.isConnected && !close.disabled && visible(close) && nav.querySelector('a.navigation-link')) }, 'actual open Drawer owns connected visible enabled navigation and close, no inner-focus precondition')
  assert.ok(state.root.findAllComponents(Sider).some(s => s.props('navigationId') === 'mobile-portal-navigation')); await settle()
}
async function cancel(state) { assert.equal(openState(state), true); const close = button('关闭导航'); assert.ok(visible(close) && !close.disabled); close.focus(); const started = performance.now(); close.click(); await settle(); assert.equal(openState(state), false); return started }
async function navigate(state) { const link = document.querySelector('#mobile-portal-navigation a[href="/adapter/user-usage"]'); assert.ok(link?.isConnected && visible(link)); link.focus(); const started = performance.now(); link.click(); await until(() => state.pushes.length === 1 && state.router.currentRoute.value.path === '/adapter/user-usage', 'actual native route commits'); assert.equal(state.pushes[0].failure, false); assert.ok(main().querySelector('[data-synthetic-destination]')); assert.equal(openState(state), false); return started }
async function throughCloseWindow(started) { await new Promise(resolve => setTimeout(resolve, Math.max(0, 350 - (performance.now() - started)))); await settle() }
async function edit(input, value = LATER) { assert.ok(input?.isConnected && visible(input) && !input.disabled && !input.readOnly); input.focus(); assert.strictEqual(document.activeElement, input); input.value = value; await new DOMWrapper(input).trigger('input'); await settle(); assert.equal(input.value, value) }
async function foregroundModal(state) {
  state.showModal() // host application foreground response, not a behind-mask click.
  await until(() => { const input = document.querySelector('input[data-current-modal-input]'), wrap = input?.closest('.ant-modal-wrap'); return Boolean(state.modalOpen() && input?.isConnected && visible(input) && !input.disabled && !input.readOnly && wrap?.getAttribute('role') === 'dialog' && Number(getComputedStyle(wrap).zIndex) === 2000) }, 'actual higher foreground Antd Modal owns a usable native field; no automatic focus or rootClass marker')
  const input = document.querySelector('input[data-current-modal-input]'); await edit(input); assert.equal(state.modalText(), LATER); assert.ok(state.modalUpdates() >= 1); return input
}
async function app() {
  helper.setViewport(360); user.setStoredCurrentUser({ id: 9891, username: 'SYNTHETIC_MOBILE_ACTOR', role: 'admin', status: 'enabled', balance: 1 })
  const state = { root: null, router: null, layout: null, pushes: [] }
  const Leaf = { setup() { const draft = ref('SYNTHETIC 初始草稿'); return () => h('section', [h('h1', 'SYNTHETIC profile'), h('input', { 'data-navigation-draft': '', 'aria-label': '合成未保存草稿', value: draft.value, onInput: event => { draft.value = event.target.value } })]) } }
  const Destination = { setup() { const value = ref(''); state.destinationText = () => value.value; return () => h('section', { 'data-synthetic-destination': '' }, [h('h1', 'SYNTHETIC destination'), h('input', { 'data-destination-input': '', 'aria-label': '合成新页面输入', value: value.value, onInput: event => { value.value = event.target.value } })]) } }
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter', component: Layout, children: [{ path: 'profile', component: Leaf }, { path: 'user-usage', component: Destination }] }] }); state.router = router; router.onError(error => routerErrors.push(error)); await router.push('/adapter/profile'); await router.isReady(); histories.push(router.options.history)
  const Host = { setup() {
    const appVisible = ref(true), modalVisible = ref(false), modalValue = ref(''), updates = ref(0), hostValue = ref('')
    state.hideApp = () => { appVisible.value = false }; state.showModal = () => { modalVisible.value = true }; state.modalOpen = () => modalVisible.value; state.modalText = () => modalValue.value; state.modalUpdates = () => updates.value; state.hostText = () => hostValue.value
    return () => h('div', [h('input', { 'data-host-input': '', 'aria-label': '合成持续宿主输入', value: hostValue.value, onInput: event => { hostValue.value = event.target.value } }), appVisible.value ? h(App) : h('div', { 'data-old-app-unmounted': '' }), h(Antd.ConfigProvider, { theme: { token: { motion: false } } }, { default: () => h(Antd.Modal, { open: modalVisible.value, forceRender: true, title: 'SYNTHETIC current foreground dialog', zIndex: 2000, focusTriggerAfterClose: false, footer: null, onCancel: () => { modalVisible.value = false } }, { default: () => h(Antd.Input, { 'data-current-modal-input': '', 'aria-label': '合成当前对话框输入', value: modalValue.value, 'onUpdate:value': value => { modalValue.value = value; updates.value++ } }) }) })])
  } }
  const root = mount(Host, { attachTo: document.body, global: { plugins: [router, Antd], config: { errorHandler: error => runtime.push(String(error?.name || error)) } } }); state.root = root; roots.push(root); await settle(); state.layout = root.findComponent(Layout)
  const push = router.push.bind(router); router.push = to => push(to).then(result => { state.pushes.push({ to, route: router.currentRoute.value.path, failure: isNavigationFailure(result) }); return result })
  await edit(document.querySelector('input[data-navigation-draft]'), DRAFT); await openMobile(state); return state
}
function witness(label, state, extra = {}) { console.log('MOBILE_FOCUS_NATIVE ' + JSON.stringify({ label, route: state.router.currentRoute.value.path, activeTag: document.activeElement.tagName, activeLabel: document.activeElement.getAttribute('aria-label'), activeIsTrigger: document.activeElement === document.querySelector('button[aria-label="打开导航"]'), drawerOpen: openState(state), rendered: state.layout.exists() ? state.layout.vm.drawerRendered : false, modalOpen: state.modalOpen(), modalModel: state.modalText(), modalUpdates: state.modalUpdates(), realIO: network, runtime, physicalMobileOrReaderClaim: false, ...extra })) }
afterEach(async () => {
  for (const root of roots) if (root.exists()) root.unmount(); await settle(); for (const history of histories) history.destroy(); user.clearStoredCurrentUser(); localStorage.clear(); window.sessionStorage.clear(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren(); helper.setViewport(1440); helper.setSystemDark(false)
  console.log('MOBILE_FOCUS_SAFETY ' + JSON.stringify({ sourceHashes: Object.fromEntries(FILES.map(p => [p, sha(path.join(web, p))])), apiCalls, realIO: network, unexpectedRuntime: runtime, routerErrors: routerErrors.length }))
  for (const p of FILES) assert.equal(sha(path.join(web, p)), sourceHashes[p]); assert.deepEqual(network, []); assert.deepEqual(runtime, []); assert.deepEqual(routerErrors, [])
  roots = []; histories = []; network = []; runtime = []; routerErrors = []; apiCalls = []
})

test('MF01 normal native mobile cancel returns focus to navigation trigger and retains draft', async () => {
  const state = await app(), started = await cancel(state); await throughCloseWindow(started); assert.strictEqual(document.activeElement, button('打开导航')); assert.equal(state.layout.vm.drawerRendered, false); assert.equal(document.querySelector('input[data-navigation-draft]').value, DRAFT); witness('normal-trigger', state)
})
test('MF02 native successful mobile route closes navigation and focuses rendered actual main', async () => {
  const state = await app(), started = await navigate(state); await throughCloseWindow(started); assert.strictEqual(document.activeElement, main()); assert.equal(state.layout.vm.drawerRendered, false); witness('normal-content', state)
})
test('MF03 old native cancel completion cannot take focus from later higher Antd Modal real input/model', async () => {
  const state = await app(), started = await cancel(state), input = await foregroundModal(state), acquiredAtMs = performance.now() - started; await throughCloseWindow(started); assert.strictEqual(document.activeElement, input, 'old close cannot focus background trigger while the later usable foreground dialog owns native input'); assert.equal(state.modalText(), LATER); assert.ok(state.modalUpdates() >= 1); assert.equal(state.modalOpen(), true); assert.ok(visible(input)); assert.equal(state.layout.vm.drawerRendered, false); witness('foreground-modal', state, { acquiredAtMs })
})
test('MF04 old route-close completion preserves later editable destination control and its actual model', async () => {
  const state = await app(), started = await navigate(state), input = document.querySelector('input[data-destination-input]'); const mask = document.querySelector('.mobile-navigation-drawer .ant-drawer-mask'); assert.ok(!mask || !visible(mask) || getComputedStyle(mask).pointerEvents === 'none', 'old closing mask does not own foreground native page control'); await edit(input); assert.equal(state.destinationText(), LATER); await throughCloseWindow(started); assert.strictEqual(document.activeElement, input, 'old close cannot override newer native page control focus with main'); assert.equal(state.destinationText(), LATER); assert.equal(state.layout.vm.drawerRendered, false); witness('later-destination-control', state)
})
test('MF05 reopen retires the earlier close completion; it cannot clear later open Drawer, and new close still works', async () => {
  const state = await app(), oldStarted = await cancel(state); await openMobile(state); const currentClose = button('关闭导航'); currentClose.focus(); await throughCloseWindow(oldStarted); assert.equal(openState(state), true); assert.equal(state.layout.vm.drawerRendered, true); assert.strictEqual(document.activeElement, currentClose); assert.ok(document.getElementById('mobile-portal-navigation')?.isConnected); const newStarted = await cancel(state); await throughCloseWindow(newStarted); assert.strictEqual(document.activeElement, button('打开导航')); assert.equal(state.layout.vm.drawerRendered, false); witness('reopen-newclose', state)
})
test('MF06 viewport retirement and later Layout unmount cannot let old close completion steal persistent host control focus', async () => {
  const state = await app(), host = document.querySelector('input[data-host-input]'); let started = await cancel(state); await edit(host); helper.setViewport(1440); await settle(); await throughCloseWindow(started); assert.strictEqual(document.activeElement, host); assert.equal(state.hostText(), LATER); assert.equal(state.layout.vm.drawerRendered, false)
  helper.setViewport(360); await settle(); await openMobile(state); started = await cancel(state); await edit(host, LATER + ' 卸载'); state.hideApp(); await settle(); assert.ok(document.querySelector('[data-old-app-unmounted]')); await throughCloseWindow(started); assert.strictEqual(document.activeElement, host); assert.equal(state.hostText(), LATER + ' 卸载'); assert.ok(!document.querySelector('.layout-shell')); witness('viewport-and-unmount', state)
})
