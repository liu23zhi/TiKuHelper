const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
global.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const admin = { id: 91, username: 'fixture-admin', role: 'admin', balance: 1.234567 }
const normal = { id: 92, username: 'fixture-user', role: 'user', balance: 1.234567 }
const wrappers = [], calls = []
const row = { id: 71, username: 'fixture-user', nickname: 'Fixture', role: 'user', status: 'enabled', balance: 1.234567, phone: '', wechatId: '', perms: 'search', registerSource: 'admin' }
const token = { id: 17, name: 'fixture-token', tokenValue: 'SYNTHETIC_ONLY_TOKEN_123456', totalQuotaLimit: 0.000062, status: 'enabled', totalAmountUsed: 1.234567, remark: 'fixture' }
const ok = data => ({ status: 200, data })
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = {
 getUserList: payload => { calls.push({ name: 'users', payload }); return Promise.resolve(ok({ data: [{ ...row }], total: 1, page: payload.page, pageSize: payload.pageSize })) },
 getAccessTokens: payload => { calls.push({ name: 'tokens', payload }); return Promise.resolve(ok([{ ...token }])) },
 getSystemSettings: () => Promise.resolve(ok({ newUserBonusAmount: 2 })),
 getPhoneLocations: () => Promise.resolve(ok({})), getIpLocations: () => Promise.resolve(ok({})),
 ...Object.fromEntries(['addUser', 'updateUser', 'adjustUserBalance', 'resetUserPassword', 'delUser', 'createWechatLoginQRCode', 'resolveAdminWechatIDByScan', 'resolveAdminWechatIDByCode', 'createAccessToken', 'updateAccessToken', 'deleteAccessToken'].map(name => [name, () => Promise.resolve(ok({}))])),
}; require.cache[apiPath] = api
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function page(name, account = admin) {
 user.setStoredCurrentUser(account)
 const w = mount(require(path.join(web, 'src/pages', name, 'index.vue')), { attachTo: document.body, global: { plugins: [Antd] } }); wrappers.push(w)
 await settle(); return w
}
function keys(w) { return w.vm.columns.map(column => column.key) }
function settings(w) { const widget = w.findComponent({ name: 'TableColumnSettings' }); assert.ok(widget.exists(), 'real page renders shared column settings'); return widget }
async function choose(w, selected) { settings(w).vm.$emit('update:modelValue', selected); await settle() }
function storedPreferences() { return ['localStorage', 'sessionStorage'].flatMap(area => Array.from({ length: window[area].length }, (_, index) => [area, window[area].key(index), window[area].getItem(window[area].key(index))]).filter(([, key]) => key !== 'adapter_current_user')) }
afterEach(async () => {
 console.log('USER_TOKEN_COLUMN_LEDGER ' + JSON.stringify({ calls, session: storedPreferences() }))
 for (const w of wrappers) if (w.exists()) w.unmount(); wrappers.length = 0
 user.clearStoredCurrentUser(); window.sessionStorage.clear(); window.localStorage.clear(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren(); calls.length = 0
})

test('UserList defaults to every original field and binds visible columns plus width to actual settings', async () => {
 const w = await page('UserList'), initial = keys(w)
 assert.equal(initial.length, 16); assert.ok(initial.includes('phone') && initial.includes('registerIp'))
 const originalWidth = w.vm.tableScrollX
 await choose(w, ['id', 'username', 'operation'])
 assert.deepEqual(keys(w), ['id', 'username', 'operation']); assert.ok(w.vm.tableScrollX < originalWidth)
 const table = w.findAllComponents(Antd.Table)[0]; assert.deepEqual(table.props('columns').map(column => column.key), keys(w))
 assert.equal(table.props('scroll').x, w.vm.tableScrollX)
 console.log('USER_COLUMNS_OBSERVED ' + JSON.stringify({ initial: initial.length, selected: keys(w), width: w.vm.tableScrollX }))
})
test('UserList required ID identity and operation survive empty or tampered selection', async () => {
 const w = await page('UserList'); await choose(w, ['phone', 'not-an-authorized-column'])
 assert.deepEqual(keys(w), ['id', 'username', 'phone', 'operation'])
 await choose(w, []); assert.deepEqual(keys(w), ['id', 'username', 'operation'])
})
test('UserList restores same-tab preferences without retaining keywords phones rows passwords or cross-account state', async () => {
 const a = await page('UserList'); a.vm.keywordInput = '13800138000'; a.vm.filters.keyword = 'PRIVATE_FIXTURE_QUERY'; a.vm.formState.password = 'PRIVATE_FIXTURE_PASSWORD'
 await choose(a, ['id', 'username', 'operation']); a.unmount()
 const b = await page('UserList'); assert.deepEqual(keys(b), ['id', 'username', 'operation'])
 const stored = JSON.stringify(storedPreferences()); assert.ok(!stored.includes('PRIVATE_FIXTURE') && !stored.includes('13800138000') && !stored.includes('Fixture'))
 b.unmount(); const other = await page('UserList', { ...admin, id: 93 }); assert.equal(keys(other).length, 16)
})
test('UserList column reset returns all low-frequency fields rather than deleting their definitions', async () => {
 const w = await page('UserList'); const original = keys(w); await choose(w, ['id', 'username', 'operation'])
 settings(w).vm.$emit('reset'); await settle(); assert.deepEqual(keys(w), original)
})
test('token table preferences preserve token name and operation while recalculating horizontal width', async () => {
 const w = await page('AccessTokenManagement'); const original = keys(w), originalWidth = w.vm.tableScrollX
 assert.ok(original.includes('tokenValue') && original.includes('lastUsedIp') && !original.includes('username'))
 await choose(w, ['remark']); assert.deepEqual(keys(w), ['name', 'remark', 'operation']); assert.ok(w.vm.tableScrollX < originalWidth)
 const table = w.findAllComponents(Antd.Table)[0]; assert.deepEqual(table.props('columns').map(column => column.key), keys(w))
 settings(w).vm.$emit('reset'); await settle(); assert.deepEqual(keys(w), original)
})
test('all-token view keeps a required owner column and cannot import it into ordinary-user views', async () => {
 const w = await page('AccessTokenManagement'); w.vm.toggleAllTokens(); await settle(); assert.ok(keys(w).includes('username'))
 await choose(w, ['name', 'operation', 'tokenValue']); assert.ok(keys(w).includes('username'), 'owner stays visible for administrative all-token view')
 user.setStoredCurrentUser({ ...admin, role: 'user' }); await settle(); assert.equal(w.vm.showAllTokens, false); assert.ok(!keys(w).includes('username'))
 await choose(w, ['name', 'operation', 'username']); assert.deepEqual(keys(w), ['name', 'operation'])
 const count = calls.length; w.vm.toggleAllTokens(); await settle(); assert.equal(calls.length, count); assert.ok(!keys(w).includes('username'))
 console.log('TOKEN_ROLE_COLUMN_OBSERVED ' + JSON.stringify({ allView: w.vm.showAllTokens, columns: keys(w), unprivilegedIO: calls.length - count }))
})
test('token column choices restore in the same tab without persisting token plaintext visibility or another identity', async () => {
 const a = await page('AccessTokenManagement', normal); a.vm.toggleTokenVisibility(17); await choose(a, ['name', 'totalQuotaLimit', 'operation']); a.unmount()
 const b = await page('AccessTokenManagement', normal); assert.deepEqual(keys(b), ['name', 'totalQuotaLimit', 'operation']); assert.equal(b.vm.isTokenVisible(17), false)
 const stored = JSON.stringify(storedPreferences()); assert.ok(!stored.includes(token.tokenValue) && !stored.includes('visibleTokenIds') && !stored.includes('remark'))
 b.unmount(); const other = await page('AccessTokenManagement', { ...normal, id: 94 }); assert.ok(keys(other).includes('tokenValue'))
})
test('logout clears the retired user and token preference scope before relogin', async () => {
 const users = await page('UserList'), tokens = await page('AccessTokenManagement'); await choose(users, ['id', 'username', 'operation']); await choose(tokens, ['name', 'operation'])
 user.clearStoredCurrentUser(); await settle(); users.unmount(); tokens.unmount()
 const nextUsers = await page('UserList'), nextTokens = await page('AccessTokenManagement'); assert.equal(keys(nextUsers).length, 16); assert.ok(keys(nextTokens).includes('tokenValue'))
})

test('UserList restores applied keyword in memory plus safe facets/page size after same-tab remount without persisting keyword', async () => {
 const a = await page('UserList'); a.vm.filters.role = 'user'; a.vm.filters.status = 'disabled'; a.vm.keywordInput = '13800138000'; a.vm.applyKeyword(); await settle()
 a.vm.handleTableChange({ current: 3, pageSize: 50 }); await settle(); a.unmount()
 const b = await page('UserList'); assert.equal(b.vm.filters.role, 'user'); assert.equal(b.vm.filters.status, 'disabled'); assert.equal(b.vm.pagination.pageSize, 50)
 assert.equal(b.vm.pagination.current, 1); assert.equal(b.vm.filters.keyword, '13800138000'); assert.equal(b.vm.keywordInput, '13800138000')
 const last = calls.filter(call => call.name === 'users').at(-1).payload; assert.deepEqual(last, { keyword: '13800138000', role: 'user', status: 'disabled', page: 1, pageSize: 50 })
 assert.ok(!JSON.stringify(storedPreferences()).includes('13800138000'))
 b.vm.resetFilters(); await settle(); b.unmount(); const c = await page('UserList'); assert.equal(c.vm.filters.role, undefined); assert.equal(c.vm.filters.status, undefined)
 console.log('USER_FILTER_RESTORE_OBSERVED ' + JSON.stringify({ role: b.vm.filters.role, status: b.vm.filters.status, remountPage: 1, request: last, resetRole: c.vm.filters.role ?? null }))
})
test('UserList filter session is isolated by identity and role, clears on logout and never restores invalid facets', async () => {
 const a = await page('UserList'); a.vm.filters.role = 'admin'; a.vm.filters.status = 'disabled'; a.vm.pagination.pageSize = 100; await settle(); a.unmount()
 const other = await page('UserList', { ...admin, id: 93 }); assert.equal(other.vm.filters.role, undefined); assert.equal(other.vm.pagination.pageSize, 10); other.unmount()
 const back = await page('UserList'); assert.equal(back.vm.filters.role, 'admin'); assert.equal(back.vm.pagination.pageSize, 100)
 user.setStoredCurrentUser({ ...admin, role: 'user' }); await settle(); assert.equal(back.vm.filters.role, undefined); assert.equal(back.vm.filters.status, undefined); assert.equal(back.vm.pagination.pageSize, 10)
 back.vm.filters.keyword = 'PRIVATE_ROLE_QUERY'; back.vm.keywordInput = 'PRIVATE_ROLE_INPUT'; user.clearStoredCurrentUser(); await settle(); assert.equal(back.vm.filters.keyword, ''); assert.equal(back.vm.keywordInput, ''); back.unmount()
 const logged = await page('UserList'); assert.equal(logged.vm.filters.role, undefined); assert.equal(logged.vm.pagination.pageSize, 10)
})


test('native UserList column popover toggles a checkbox, locks required columns, chooses common fields and restores all', async () => {
 const w = await page('UserList'), original = keys(w)
 await w.find('button.table-column-settings').trigger('click'); await new Promise(resolve => setTimeout(resolve, 150)); await settle()
 const panel = document.querySelector('.column-settings-panel'); assert.ok(panel)
 const labels = [...panel.querySelectorAll('label.ant-checkbox-wrapper')]
 const nickname = labels.find(label => label.textContent.trim() === '昵称'); assert.ok(nickname)
 const required = labels.find(label => label.textContent.trim() === 'ID').querySelector('input'); assert.equal(required.disabled, true)
 nickname.querySelector('input').click(); await settle(); assert.ok(!keys(w).includes('nickname')); assert.ok(keys(w).includes('phone'))
 const common = [...panel.querySelectorAll('button')].find(button => button.textContent.trim() === '常用列'); assert.ok(common); common.click(); await settle()
 assert.deepEqual(keys(w), ['id', 'username', 'nickname', 'role', 'status', 'balance', 'operation'])
 const reset = [...panel.querySelectorAll('button')].find(button => button.textContent.trim() === '恢复全部列'); reset.click(); await settle(); assert.deepEqual(keys(w), original)
 console.log('USER_NATIVE_COLUMN_OBSERVED ' + JSON.stringify({ disabledID: required.disabled, restoredColumns: keys(w).length, lowFrequencyRetained: keys(w).includes('registerIp') }))
})
test('UserList rejects malformed saved session facets and excludes forbidden keyword fields from restoration', async () => {
 const a = await page('UserList'); a.vm.filters.role = 'user'; await settle(); a.unmount()
 const key = storedPreferences().find(([area, key]) => area === 'sessionStorage' && key.includes('user-list'))?.[1]; assert.ok(key)
 window.sessionStorage.setItem(key, JSON.stringify({ version: 1, values: { role: 'ROOT', status: 'deleted', pageSize: 100000, keyword: 'PRIVATE_TAMPERED', password: 'PRIVATE_TAMPERED' } }))
 const b = await page('UserList'); assert.equal(b.vm.filters.role, undefined); assert.equal(b.vm.filters.status, undefined); assert.equal(b.vm.pagination.pageSize, 10); assert.equal(b.vm.filters.keyword, ''); assert.equal(b.vm.keywordInput, '')
})

test('legacy objects without stable IDs keep settings ephemeral and never use username as a preference key', async () => {
 const legacy = { username: 'SYNTHETIC_PRIVATE_LEGACY_NAME', role: 'admin', balance: 1 }
 const users = await page('UserList', legacy)
 await choose(users, ['id', 'username', 'operation'])
 const tokens = await page('AccessTokenManagement', legacy)
 await choose(tokens, ['name', 'operation'])
 assert.equal(storedPreferences().length, 0)
 assert.equal(users.vm.columnContextKey, '')
 assert.equal(tokens.vm.columnContextKey, '')
})
