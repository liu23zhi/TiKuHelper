const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
const helper = require(process.env.UI_TEST_HELPER || path.resolve(__dirname, '../helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils')
const { h, nextTick, effectScope, isReadonly } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const axios = require('axios'), originalAdapter = axios.defaults.adapter, forbidden = []
const shellAdapter = require('../helpers/navigation-shell-api.cjs')({
 actor: user.getStoredCurrentUser, record() {}, deny(kind) { forbidden.push(kind); throw new Error('Forbidden theme shell request: ' + kind) },
})
axios.defaults.adapter = async config => {
 const response = await shellAdapter(config)
 if (config.url === '/api/user/review-rebate/availability') response.data.enabled = true
 return response
}
require('node:test').after(() => { axios.defaults.adapter = originalAdapter })
const production = require(path.join(root, 'src/router/router.js'))
const productionRouter = production.default || production
let wrapper, router
// Fixed expected permission/order matrix, independent of the source under test.
const ADMIN_HREFS = [
  '/profile', '/my-devices', '/user-usage', '/tickets', '/my-referral',
  '/review-rebate', '/my-tokens', '/scripts', '/service-status', '/component',
  '/import', '/userlist', '/user-portrait', '/risk-admin', '/archived-users',
  '/loglist', '/extension-logs', '/recharge-orders', '/billing-admin', '/ip-location-cache', '/ip-segments',
  '/phone-location-cache', '/wechat-bindings', '/phone-bindings', '/search-account-config', '/ai-search-config',
  '/ocr-cache', '/sensitive-word', '/user-exam-tool', '/referral-rebates', '/review-rebate-admin',
  '/referral-invites', '/blacklist', '/whitelist', '/user-bans', '/device-admin',
  '/sensitive-image', '/ticket-image-cache', '/user-levels', '/system-settings', '/extension-updates',
  '/token-admin', '/script-admin', '/script-activation-codes', '/ticket-admin', '/service-status-admin',
]
const USER_HREFS = [
  '/profile', '/my-devices', '/user-usage', '/my-tokens', '/scripts',
  '/my-referral', '/review-rebate', '/tickets', '/service-status',
]
function exactNavigation(expected) {
 const hrefs = wrapper.findAll('#portal-navigation [role="menuitem"]').map(item => item.get('a').attributes('href'))
 assert.equal(new Set(hrefs).size, hrefs.length, 'one menu entry per actual destination')
 assert.deepEqual(hrefs, expected, 'exact authorized menu destinations in order')
 for (const href of hrefs) {
   const resolved = productionRouter.resolve(href)
   assert.ok(resolved.matched.some(record => record.name === 'adapterlayout'), 'real portal route: ' + href)
   assert.equal(resolved.path, href)
   assert.ok(!resolved.matched.at(-1).redirect && !resolved.matched.at(-1).aliasOf, 'canonical menu destination, not a legacy redirect/alias: ' + href)
 }
 return hrefs
}
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
afterEach(async () => { wrapper?.unmount(); wrapper = undefined; Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); router?.options.history.destroy(); router = undefined; document.body.innerHTML = ''; window.localStorage.clear(); helper.setViewport(1440); helper.setSystemDark(false); assert.deepEqual(forbidden.splice(0), []) })
const Leaf = { render: () => h('div', [h('h1', { id: 'fixture-content' }, 'Synthetic content'), h(Antd.Button, { id: 'fixture-button' }, () => 'Button'), h(Antd.Table, { columns: [{ title: 'Field', dataIndex: 'value' }], dataSource: [], pagination: false })]) }
async function open(route = '/login', width = 1440, role = 'admin') {
 helper.setViewport(width)
 if (route !== '/login') user.setStoredCurrentUser({ id: 37, role, balance: 9.876543 })
 const Layout = require(path.join(root, 'src/layout/TikuAdapterLayout.vue'))
 const Login = require(path.join(root, 'src/layout/LoginLayout.vue'))
 const App = require(path.join(root, 'src/App.vue'))
 // Keep the production portal paths, names, aliases and role metadata. Stub only the heavy leaf bodies.
 const portal = productionRouter.options.routes.find(record => record.name === 'adapterlayout')
 assert.equal(portal.path, '/')
 const leaves = portal.children.map(record => record.redirect ? record : { ...record, component: Leaf })
 router = createRouter({ history: createMemoryHistory(), routes: [
   { path: '/', component: Login, children: [{ path: 'login', component: Leaf }] },
   { path: portal.path, name: portal.name, component: Layout, children: leaves },
 ] })
 await router.push(route); await router.isReady()
 wrapper = mount(App, { attachTo: document.body, global: { plugins: [router, Antd] } }); await settle()
 return { Layout, App }
}
test('Ant Design Vue is the installed 4.2.6, real controls mount', async () => { assert.equal(require('ant-design-vue/package.json').version, '4.2.6'); await open(); assert.ok(wrapper.find('.ant-btn').exists()); assert.ok(wrapper.find('.ant-table').exists()) })
test('anonymous real header offers all three theme choices', async () => { await open(); const c=wrapper.find('select[aria-label="外观"]'); assert.ok(c.exists()); assert.deepEqual(c.findAll('option').map(x=>x.attributes('value')),['light','dark','system']); assert.equal(c.element.disabled,false); assert.equal(wrapper.find('.logout-btn').exists(),false) })
test('root ConfigProvider uses Antd4 algorithm, updates real component tokens and inherits body portals', async () => { await open(); const provider=wrapper.findComponent(Antd.ConfigProvider); assert.ok(provider.exists()); const before=provider.props('theme'); assert.equal(before.algorithm,Antd.theme.defaultAlgorithm); const lightHash=wrapper.find('#fixture-button').classes().find(x=>x.startsWith('css-')); const c=wrapper.find('select[aria-label="外观"]'); await c.setValue('dark'); await settle(); assert.equal(document.documentElement.dataset.theme,'dark'); assert.equal(window.localStorage.getItem('tiku.theme-mode.v1'),'dark'); assert.equal(provider.props('theme').algorithm,Antd.theme.darkAlgorithm); assert.notEqual(wrapper.find('#fixture-button').classes().find(x=>x.startsWith('css-')),lightHash); const show=wrapper.vm; assert.ok(show); assert.equal(provider.props('locale').locale,'zh-cn'); console.log('ANTD4_DOM token algorithm light->dark; real Button CSS hash changed; provider locale zh-cn') })
test('system media and cross-tab storage update root and real Antd4 menu; explicit light ignores media', async () => { await open('/profile'); const c=wrapper.find('select[aria-label="外观"]'); assert.ok(c.exists()); helper.setSystemDark(true); await settle(); assert.equal(document.documentElement.dataset.theme,'dark'); assert.ok(wrapper.find('#portal-navigation').classes().includes('ant-menu-dark')); await c.setValue('light'); helper.setSystemDark(false); helper.setSystemDark(true); await settle(); assert.equal(document.documentElement.dataset.theme,'light'); window.dispatchEvent(new window.StorageEvent('storage',{ key:'tiku.theme-mode.v1',newValue:'dark',storageArea:window.localStorage })); await settle(); assert.equal(document.documentElement.dataset.theme,'dark'); assert.equal(c.element.value,'dark') })
test('all upstream administrator navigation, including both location cache pages, is retained', async () => { await open('/profile'); exactNavigation(ADMIN_HREFS); assert.ok(wrapper.find('a[href="/ip-location-cache"]').exists()); assert.ok(wrapper.find('a[href="/phone-location-cache"]').exists()); await wrapper.find('a[href="/ip-location-cache"]').trigger('click'); await settle(); assert.equal(router.currentRoute.value.path,'/ip-location-cache'); for (const path of ADMIN_HREFS) { await wrapper.find('a[href="'+path+'"]').trigger('click'); await settle(); assert.equal(router.currentRoute.value.path,path) } })
test('phone shell opens actual Antd4 Drawer with open API and Escape closes it', async () => { await open('/profile',360); const trigger=wrapper.find('[aria-label="打开导航"]'); assert.ok(trigger.exists()); assert.equal(wrapper.find('.layout-sider').exists(),false); await trigger.trigger('click'); await settle(); const drawer=wrapper.findComponent(Antd.Drawer); assert.ok(drawer.exists()); assert.equal(drawer.props('open'),true); assert.ok(document.querySelector('.ant-drawer-content')); assert.ok(document.querySelector('#mobile-portal-navigation')); const close=document.querySelector('[aria-label="关闭导航"]'); close.dispatchEvent(new window.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true})); await settle(); assert.equal(drawer.props('open'),false); console.log('ANTD4_DRAWER actual body portal open=true -> Escape -> open=false at simulated width 360; no CSS geometry claim') })
test('currentUser display is readonly, shared-reactive and freshness guarded without changing balance precision', async () => { assert.equal(typeof user.useCurrentUserDisplay,'function'); user.setStoredCurrentUser({id:37,role:'admin',balance:9.876543}); const scope=effectScope(); const state=scope.run(()=>user.useCurrentUserDisplay()); try { const guard=state.captureUpdateGuard(); assert.equal(isReadonly(state.currentUser),true); user.setStoredCurrentUser({id:37,role:'user',balance:1.234567}); await settle(); assert.equal(state.currentUser.value.role,'user'); assert.equal(state.currentUser.value.balance,1.234567); assert.equal(guard(),false) } finally { scope.stop() } })
test('same-principal role downgrade immediately leaves new upstream location admin page and hides admin menu', async () => { await open('/ip-location-cache'); const shell=wrapper.find('.layout-shell').element; user.setStoredCurrentUser({id:37,role:'user',balance:9.876543}); await settle(); assert.equal(router.currentRoute.value.path,'/user-usage'); assert.equal(wrapper.find('.layout-shell').element,shell); const hrefs=exactNavigation(USER_HREFS); for (const path of ADMIN_HREFS.filter(href=>!USER_HREFS.includes(href))) assert.ok(!hrefs.includes(path), 'ordinary user cannot see administrator destination '+path); assert.ok([...document.querySelectorAll('.ant-message-notice')].some(x=>x.textContent.includes('管理权限已变更'))); assert.equal(document.querySelectorAll('.ant-modal-confirm').length,0); await wrapper.find('a[href="/tickets"]').trigger('click'); await settle(); assert.equal(router.currentRoute.value.path,'/tickets') })
test('responsive and theme sources use Antd4 API, never removed upstream form or billing fields', () => { const app=fs.readFileSync(path.join(root,'src/App.vue'),'utf8'); assert.match(app,/:theme="antdTheme"/); const shell=fs.readFileSync(path.join(root,'src/layout/TikuAdapterLayout.vue'),'utf8'); assert.match(shell,/:open="drawerVisible"/); assert.match(shell,/@after-open-change/); assert.doesNotMatch(shell,/:visible="drawerVisible"/); assert.match(shell,/100dvh/); assert.match(shell,/ip-location-cache/); assert.match(shell,/phone-location-cache/) })
