const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), fs = require('node:fs'), Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(process.env.UI_TEST_HELPER || path.join(root, 'tests/helpers/vue-dom.cjs'))
for (const name of ['DOMParser', 'CustomEvent', 'HTMLTemplateElement', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLSelectElement', 'HTMLTextAreaElement', 'HTMLVideoElement', 'HTMLAudioElement']) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: window[name] })
window.scrollTo = () => {} // jsdom has no scroll geometry; these tests only observe popup ownership.
const { mount, flushPromises } = require('@vue/test-utils'), { h, nextTick } = require('vue')
const { createRouter, createMemoryHistory, RouterView } = require('vue-router')
const Antd = require('ant-design-vue'), Swal = require('sweetalert2')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const first = { id: 37, username: 'fixture-user', role: 'user', balance: 9.876543, minRechargeAmount: 1 }
const second = { id: 38, username: 'fixture-other', role: 'user', balance: 1.234567, minRechargeAmount: 5 }
const marker = 'SYNTHETIC_PREVIOUS_ACCOUNT_SUBJECT'
const record = { id: 8101, type: 'normal_search', typeLabel: 'Synthetic expense', direction: 'expense', subject: marker, amount: 0.123456, provider: 'fixture' }
const ok = data => ({ status: 200, data })
const apiPath = path.join(root, 'src/api/TikuComponentApi.js'), api = new Module(apiPath)
api.loaded = true; api.exports = {
 getCurrentUser: () => Promise.resolve(ok(user.getStoredCurrentUser())),
 getBillingRecords: () => Promise.resolve(ok({ data: user.getStoredCurrentUser()?.id === 37 ? [{ ...record }] : [], total: user.getStoredCurrentUser()?.id === 37 ? 1 : 0, page: 1, pageSize: 10 })),
 getPaymentMethods: () => Promise.resolve(ok([])),
 getPublicRegisterStatus: () => Promise.resolve(ok({ siteName: 'Fixture', captchaEnabled: false, wechatLoginEnabled: false })),
 getAISearchConfig: () => Promise.resolve(ok({ enabled: false, baseUrl: '', apiKey: '', model: 'fixture-model', manualModels: ['fixture-model'], prompt: 'saved prompt' })),
 getAIModelPrices: () => Promise.resolve(ok([])), logout: () => Promise.resolve(ok({})),
}
require.cache[apiPath] = api
let wrapper, router
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await nextTick() }
async function popupSettled() { await settle(); await new Promise(resolve => setTimeout(resolve, 35)); await settle() }
const Usage = require(path.join(root, 'src/pages/UserUsage/index.vue'))
const AI = require(path.join(root, 'src/pages/AISearchConfig/index.vue'))
async function mountUsage() { user.setStoredCurrentUser(first); wrapper = mount(Usage, { attachTo: document.body, global: { plugins: [Antd] } }); await settle(); wrapper.vm.handleShowFeeDetail(wrapper.vm.dataSource[0]); await popupSettled(); assert.ok(Swal.getPopup()?.textContent.includes(marker), 'actual fee popup contains the synthetic prior-account subject') }
function storageLogout() { window.localStorage.removeItem('adapter_current_user'); window.dispatchEvent(new window.StorageEvent('storage', { key: 'adapter_current_user', newValue: null, storageArea: window.localStorage })) }
afterEach(async () => { wrapper?.unmount(); wrapper = undefined; Swal.close(); Antd.Modal.destroyAll(); Antd.message.destroy(); await popupSettled(); router?.options.history.destroy(); router = undefined; document.body.innerHTML = ''; window.localStorage.clear() })
test('UserUsage closes its actual owned fee popup when principal37 becomes38', async () => { await mountUsage(); user.setStoredCurrentUser(second); await popupSettled(); assert.equal(wrapper.vm.dataSource.length, 0); assert.equal(Swal.getPopup()?.textContent.includes(marker) || false, false); assert.equal(document.querySelectorAll('.billing-fee-popup').length, 0) })
test('same-principal balance-only refresh retains the current owned billing popup', async () => { await mountUsage(); const popup = Swal.getPopup(); user.setStoredCurrentUser({ ...first, balance: 8.765432 }); await popupSettled(); assert.equal(Swal.getPopup(), popup); assert.ok(popup.textContent.includes(marker)) })
test('unmounting UserUsage closes its own actual fee popup', async () => { await mountUsage(); wrapper.unmount(); wrapper = undefined; await popupSettled(); assert.equal(document.querySelectorAll('.billing-fee-popup').length, 0) })
test('principal change and unmount do not close a newer popup owned by somebody else', async () => { await mountUsage(); Swal.fire({ title: 'UNRELATED_NEW_POPUP', text: 'fixture', animation: false, heightAuto: false, scrollbarPadding: false }); await popupSettled(); const unrelated = Swal.getPopup(); assert.ok(unrelated.textContent.includes('UNRELATED_NEW_POPUP')); user.setStoredCurrentUser(second); await popupSettled(); assert.equal(Swal.getPopup(), unrelated); wrapper.unmount(); wrapper = undefined; await popupSettled(); assert.equal(Swal.getPopup(), unrelated) })
test('selected payment card and glyph use existing semantic tokens; source-token contrast stays readable in both themes', () => {
 const source = fs.readFileSync(path.join(root, 'src/pages/UserUsage/index.vue'), 'utf8')
 const selected = source.match(/\.payment-method-card--selected\s*\{([^}]+)\}/)[1]
 const glyph = source.match(/\.payment-method-icon\s*\{([^}]+)\}/)[1]
 assert.match(selected, /background:\s*var\(--selected\)/); assert.match(glyph, /background:\s*var\(--surface-subtle\)/)
 const css = fs.readFileSync(path.join(root, 'src/styles/theme-tokens.css'), 'utf8')
 const luminance = hex => { const rgb = hex.slice(1).match(/../g).map(x => parseInt(x, 16) / 255).map(x => x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4); return 0.2126 * rgb[0] + 0.7152 * rgb[1] + 0.0722 * rgb[2] }
 for (const theme of ['light', 'dark']) { const body = css.match(new RegExp("html\\[data-theme='" + theme + "'\\]\\s*\\{([^}]+)\\}"))[1]; const color = key => body.match(new RegExp('--' + key + ':\\s*(#[0-9a-fA-F]{6})'))[1]; for (const background of ['selected', 'surface-subtle']) { const a = luminance(color('text')), b = luminance(color(background)); assert.ok((Math.max(a,b)+0.05)/(Math.min(a,b)+0.05) >= 4.5, theme+' '+background) } }
})
async function openShell(leaf) {
 user.setStoredCurrentUser(leaf === 'ai-search-config' ? { ...first, role: 'admin' } : first)
 const Shell = require(path.join(root, 'src/layout/TikuAdapterLayout.vue'))
 router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter', component: Shell, children: [{ path: 'user-usage', component: Usage }, { path: 'ai-search-config', component: AI }] }, { path: '/login', component: { render: () => h('h1', { id: 'fixture-login' }, 'Login fixture') } }] })
 await router.push('/adapter/' + leaf); await router.isReady(); wrapper = mount({ render: () => h(RouterView) }, { attachTo: document.body, global: { plugins: [router, Antd] } }); await settle()
}
test('cross-tab identity removal on ordinary UserUsage goes to login instead of retaining the recharge shell', async () => { await openShell('user-usage'); storageLogout(); await settle(); assert.equal(router.currentRoute.value.path, '/login'); assert.ok(wrapper.find('#fixture-login').exists()); assert.equal(wrapper.find('.layout-shell').exists(), false) })
test('cross-tab identity removal from a dirty actual AI page goes to login without a discard dialog', async () => { await openShell('ai-search-config'); const ai = wrapper.findComponent(AI); await ai.find('textarea[placeholder="留空时将自动使用系统内置 Prompt"]').setValue('SYNTHETIC_DIRTY_BEFORE_LOGOUT'); assert.equal(ai.vm.hasUnsavedChanges, true); storageLogout(); await settle(); assert.equal(router.currentRoute.value.path, '/login'); assert.equal(wrapper.findComponent(AI).exists(), false); assert.equal(document.querySelectorAll('.ant-modal-confirm').length, 0) })
test('normal dirty AI navigation still asks for confirmation and preserves edits on cancel', async () => { await openShell('ai-search-config'); const ai = wrapper.findComponent(AI); await ai.find('textarea[placeholder="留空时将自动使用系统内置 Prompt"]').setValue('SYNTHETIC_NORMAL_DIRTY'); const navigation = router.push('/adapter/user-usage'); await settle(); assert.equal(router.currentRoute.value.path, '/adapter/ai-search-config'); const modal = [...document.querySelectorAll('.ant-modal-confirm')].at(-1); assert.ok(modal); [...modal.querySelectorAll('button')].find(x => x.textContent.includes('保留编辑')).click(); await navigation; await settle(); assert.equal(router.currentRoute.value.path, '/adapter/ai-search-config'); assert.equal(ai.vm.formState.prompt, 'SYNTHETIC_NORMAL_DIRTY') })
