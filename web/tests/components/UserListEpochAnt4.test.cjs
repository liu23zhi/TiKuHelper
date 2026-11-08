// Real Vue + Antd controls; only API responses are synthetic. No network/database IO.
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const PAGE = process.env.TIKU_USERLIST_TARGET || path.join(web, 'src/pages/UserList/index.vue')
const ADMIN = { id: 7301, username: 'synthetic-admin-a', role: 'admin', balance: 1.234567 }
const OTHER = { id: 7302, username: 'synthetic-admin-b', role: 'admin' }
const ROW = { id: 71, username: 'synthetic-row', nickname: '原始行', role: 'user', status: 'enabled', balance: 1.234567, phone: '', wechatId: '' }
const ok = data => ({ status: 200, data })
const list = (name = '原始行', page = 1, pageSize = 10, total = 1) => ok({ data: [{ ...ROW, nickname: name }], page, pageSize, total })
let wrappers = [], calls = [], pending = [], defer = new Set()
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
const results = { getSystemSettings: { newUserBonusAmount: 2 }, adjustUserBalance: { balance: 1.234629 }, resolveAdminWechatIDByCode: { wechatId: 'SYNTHETIC_WECHAT' }, getPhoneLocations: {}, getIpLocations: {} }
api.exports = Object.fromEntries(['getUserList', 'getSystemSettings', 'getPhoneLocations', 'getIpLocations', 'addUser', 'updateUser', 'adjustUserBalance', 'resetUserPassword', 'delUser', 'createWechatLoginQRCode', 'resolveAdminWechatIDByScan', 'resolveAdminWechatIDByCode'].map(name => [name, payload => {
  calls.push({ name, payload: payload ? JSON.parse(JSON.stringify(payload)) : null })
  if (defer.has(name)) return new Promise((resolve, reject) => pending.push({ name, payload, resolve, reject }))
  return Promise.resolve(name === 'getUserList' ? list('原始行', payload?.page || 1, payload?.pageSize || 10) : ok(results[name] || {}))
}]))
require.cache[apiPath] = api
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function page() { user.setStoredCurrentUser(ADMIN); const w = mount(require(PAGE), { attachTo: document.body, global: { plugins: [Antd] } }); wrappers.push(w); await settle(); return w }
function last(name) { const item = pending.filter(p => p.name === name).at(-1); assert.ok(item, 'synthetic current producer: ' + name); return item }
function readCalls() { return calls.filter(p => p.name === 'getUserList') }
function snapshot(w) { return JSON.stringify({ rows: w.vm.dataSource, total: w.vm.pagination.total, form: w.vm.formState, reset: w.vm.resetPasswordForm, visible: w.vm.visible, balance: w.vm.balanceVisible, busy: w.vm.submitLoading, resetBusy: w.vm.resetPasswordLoading, bonus: w.vm.systemNewUserBonusAmount, phone: w.vm.phoneLocations, ip: w.vm.ipLocations }) }
async function search(w, text) { await w.find('.ant-input-search input').setValue(text); await w.find('.ant-input-search-button').trigger('click'); await settle(); return last('getUserList') }
function cancel(title) { const modal = [...document.querySelectorAll('.ant-modal')].find(m => m.querySelector('.ant-modal-title')?.textContent.includes(title)); assert.ok(modal, 'actual Antd modal ' + title); const button = [...modal.querySelectorAll('.ant-modal-footer button')].find(b => b.textContent.replace(/\s/g, '') === '取消'); assert.ok(button); button.click() }
function witness(label, w) { console.log('USER_EPOCH_OBSERVED ' + JSON.stringify({ label, rows: w.vm.dataSource.map(r => r.nickname), total: w.vm.pagination.total, page: w.vm.pagination.current, loading: w.vm.tabLoading, reads: readCalls().map(c => c.payload), mutations: calls.filter(c => ['addUser', 'updateUser', 'adjustUserBalance', 'resetUserPassword', 'delUser'].includes(c.name)).length })) }
afterEach(async () => { for (const w of wrappers) if (w.exists()) w.unmount(); wrappers = []; for (const p of pending) p.resolve(p.name === 'getUserList' ? list() : ok({})); await settle(); pending = []; calls = []; defer = new Set(); user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren(); delete require.cache[PAGE] })

test('healthy native fixture still lists six-digit balances and performs zero writes', async () => {
  const w = await page(); assert.equal(readCalls().length, 1); assert.ok(w.find('tbody').text().includes('1.234567')); assert.equal(w.vm.summary.totalBalance, 1.234567); witness('healthy-baseline-infrastructure', w)
})
test('same principal latest filter owns rows pagination totals and enrichment', async () => {
  const w = await page(); defer.add('getUserList'); const old = await search(w, '旧筛选'), current = await search(w, '新筛选')
  current.resolve(list('新筛选结果', 1, 50, 7)); await settle(); old.resolve(list('旧筛选迟到', 3, 10, 99)); await settle(); witness('latest-filter', w)
  assert.equal(w.vm.dataSource[0].nickname, '新筛选结果', 'LATEST_QUERY_OWNS_ROWS'); assert.equal(w.vm.pagination.current, 1); assert.equal(w.vm.pagination.total, 7); assert.equal(w.vm.pagination.pageSize, 50)
})
test('old finally/error cannot release newer loading or toast; current error visibly recovers on refresh', async () => {
  const w = await page(); defer.add('getUserList'); const old = await search(w, '旧失败'), current = await search(w, '当前请求')
  old.reject(new Error('SYNTHETIC_OLD')); await settle(); witness('old-finally', w); assert.equal(w.vm.tabLoading, true, 'LATEST_LOADING_RETAINED'); assert.ok(!document.body.textContent.includes('获取用户列表失败'))
  current.reject(new Error('SYNTHETIC_CURRENT')); await settle(); assert.ok(document.body.textContent.includes('获取用户列表失败')); assert.equal(w.vm.tabLoading, false)
  const refresh = w.findAll('button').find(b => b.text().replace(/\s/g, '') === '刷新'); await refresh.trigger('click'); await settle(); assert.equal(last('getUserList').payload.keyword, '当前请求'); last('getUserList').resolve(list('已恢复')); await settle(); assert.equal(w.vm.dataSource[0].nickname, '已恢复')
})
test('principal transition synchronously retires visible data forms locations and auto-loads fresh scope once', async () => {
  const w = await page(); w.vm.openEditModal(ROW); w.vm.openBalanceModal(); w.vm.openResetPasswordModal(ROW); w.vm.formState.password = 'SYNTHETIC_OLD_SECRET'; w.vm.phoneLocations.OLD = '旧归属地'; w.vm.ipLocations.OLD = '旧IP'
  defer.add('getUserList'); user.setStoredCurrentUser(OTHER)
  assert.deepEqual(w.vm.dataSource, [], 'PRINCIPAL_SYNC_CLEAR_ROWS'); assert.equal(w.vm.pagination.total, 0); assert.equal(w.vm.visible, false); assert.equal(w.vm.balanceVisible, false); assert.equal(w.vm.resetPasswordVisible, false); assert.equal(w.vm.formState.password, ''); assert.deepEqual(Object.keys(w.vm.phoneLocations), []); assert.deepEqual(Object.keys(w.vm.ipLocations), [])
  await settle(); assert.equal(readCalls().length, 2, 'new principal reload exactly once'); last('getUserList').resolve(list('新账号')); await settle(); witness('new-principal', w)
})
test('same ID role loss and logout clear data and refuse all actions without hidden writes', async () => {
  const w = await page(); user.setStoredCurrentUser({ ...ADMIN, role: 'user' }); assert.deepEqual(w.vm.dataSource, [], 'ROLE_SYNC_CLEAR_ROWS'); await settle(); const count = calls.length
  w.vm.openCreateModal(); w.vm.openEditModal(ROW); w.vm.openResetPasswordModal(ROW); w.vm.openBalanceModal(); w.vm.onDelete(71); await w.vm.onSubmit(); await w.vm.submitResetPassword(); await w.vm.submitBalanceAdjustment(); await w.vm.startWechatScan(); await w.vm.resolveWechatIDByCode(); w.vm.handleUserList(); await settle()
  assert.equal(calls.length, count, 'ordinary user creates zero reads/writes'); assert.equal(w.vm.visible, false); assert.ok(w.text().includes('无用户管理权限'))
  user.setStoredCurrentUser(ADMIN); await settle(); assert.equal(w.vm.dataSource.length, 1); user.clearStoredCurrentUser(); assert.deepEqual(w.vm.dataSource, []); assert.equal(w.vm.pagination.total, 0)
})
test('identity fallback username transitions retire reads without persisting fallback column preferences', async () => {
  const w = await page(); user.setStoredCurrentUser({ username: 'legacy-admin-a', role: 'admin' }); await settle(); defer.add('getUserList'); w.vm.handleUserList(); const old = last('getUserList'); user.setStoredCurrentUser({ username: 'legacy-admin-b', role: 'admin' }); await settle(); assert.notStrictEqual(last('getUserList'), old, 'fallback principal must initiate a fresh read'); last('getUserList').resolve(list('新旧账号')); await settle(); old.resolve(list('旧用户名')); await settle(); assert.equal(w.vm.dataSource[0].nickname, '新旧账号', 'FALLBACK_IDENTITY_OWNS_READ')
})
for (const operation of ['getSystemSettings', 'updateUser', 'addUser', 'adjustUserBalance', 'resetUserPassword', 'delUser', 'resolveAdminWechatIDByCode', 'createWechatLoginQRCode']) {
  test('principal epoch rejects late success/error/finally and follow-up IO: ' + operation, async () => {
    const w = await page(); w.vm.openEditModal(ROW); defer.add(operation); let work
    if (operation === 'getSystemSettings') work = w.vm.loadNewUserBonusDefault()
    else if (operation === 'updateUser') work = w.vm.onSubmit()
    else if (operation === 'addUser') { w.vm.openCreateModal(); Object.assign(w.vm.formState, { username: 'synthetic_new', password: 'QaSynthetic246!' }); work = w.vm.onSubmit() }
    else if (operation === 'adjustUserBalance') { w.vm.openBalanceModal(); w.vm.balanceForm.amount = '0.000062'; work = w.vm.submitBalanceAdjustment() }
    else if (operation === 'resetUserPassword') { w.vm.openResetPasswordModal(ROW); w.vm.resetPasswordForm.password = 'QaSynthetic246!'; work = w.vm.submitResetPassword() }
    else if (operation === 'delUser') work = w.vm.onDelete(71)
    else if (operation === 'createWechatLoginQRCode') work = w.vm.startWechatScan()
    else { w.vm.wechatCode = '123456'; work = w.vm.resolveWechatIDByCode() }
    await settle(); const old = last(operation); user.setStoredCurrentUser(OTHER); await settle(); w.vm.openEditModal({ ...ROW, nickname: '新编辑器' }); const before = snapshot(w), count = calls.length
    if (operation === 'updateUser' || operation === 'delUser') old.reject(new Error('SYNTHETIC_OLD_FAILURE'))
    else old.resolve(operation === 'adjustUserBalance' ? ok({ balance: 99.000062 }) : operation === 'getSystemSettings' ? ok({ newUserBonusAmount: 99 }) : operation === 'createWechatLoginQRCode' ? ok({ qrcodeImageData: 'data:image/png;base64,SYNTHETIC', loginToken: 'OLD_TOKEN' }) : ok({ wechatId: 'SYNTHETIC_OLD' }))
    await work; await settle(); assert.equal(snapshot(w), before, 'PRINCIPAL_RETIRED_NO_PUBLICATION_' + operation); assert.equal(calls.length, count, 'retired completion cannot refresh/poll'); assert.equal(document.querySelectorAll('.ant-message-notice').length, 0, 'retired completion cannot toast'); assert.equal(w.vm.wechatScanLoginToken, '')
  })
}
for (const kind of ['editor', 'balance', 'password']) test('dialog close/retarget retires same-principal late ' + kind + ' mutation', async () => {
  const w = await page(); w.vm.openEditModal(ROW); let work, name
  if (kind === 'editor') { name = 'updateUser'; defer.add(name); work = w.vm.onSubmit(); await settle(); cancel('编辑用户'); w.vm.openEditModal({ ...ROW, id: 72, nickname: '当前编辑' }) }
  else if (kind === 'balance') { name = 'adjustUserBalance'; defer.add(name); w.vm.openBalanceModal(); w.vm.balanceForm.amount = '0.000062'; work = w.vm.submitBalanceAdjustment(); await settle(); const modal = [...document.querySelectorAll('.ant-modal')].find(m => m.querySelector('.ant-modal-title')?.textContent.includes('修改用户余额')); assert.ok(modal); assert.equal([...modal.querySelectorAll('.ant-modal-footer button')].find(b => b.textContent.replace(/\s/g, '') === '取消').disabled, true, 'in-flight financial Cancel remains disabled'); w.vm.openEditModal({ ...ROW, id: 72, nickname: '新余额目标' }); w.vm.openBalanceModal(); w.vm.balanceForm.amount = '0.000063' }
  else { name = 'resetUserPassword'; defer.add(name); w.vm.openResetPasswordModal(ROW); w.vm.resetPasswordForm.password = 'QaSynthetic246!'; work = w.vm.submitResetPassword(); await settle(); cancel('重置用户密码'); w.vm.openResetPasswordModal({ ...ROW, id: 72 }) }
  const before = snapshot(w), count = calls.length; last(name).resolve(name === 'adjustUserBalance' ? ok({ balance: 99.000062 }) : ok({})); await work; await settle(); assert.equal(snapshot(w), before, 'DIALOG_RETIRED_' + kind); assert.equal(calls.length, count); assert.equal(document.querySelectorAll('.ant-message-notice').length, 0)
})
test('delayed weak-password acceptance after principal switch cannot start a write', async () => {
  const w = await page(); w.vm.openCreateModal(); Object.assign(w.vm.formState, { username: 'synthetic_weak', password: 'weak' }); let accept
  w.vm.confirmWeakPassword = () => new Promise(resolve => { accept = resolve }); const work = w.vm.onSubmit(); await settle(); assert.equal(typeof accept, 'function'); user.setStoredCurrentUser(OTHER); await settle(); accept(true); await work; await settle(); assert.equal(calls.filter(c => c.name === 'addUser').length, 0, 'RETIRED_WEAK_CONFIRM_NO_WRITE')
})
test('old actual popconfirm callback cannot delete after principal transition even if new list has same row ID', async () => {
  const w = await page(); const pop = w.findAllComponents(Antd.Popconfirm)[0]; assert.ok(pop.exists()); const confirm = pop.props('onConfirm'); assert.equal(typeof confirm, 'function'); user.setStoredCurrentUser(OTHER); await settle(); confirm(); await settle(); assert.equal(calls.filter(c => c.name === 'delUser').length, 0, 'RETIRED_POPCONFIRM_NO_WRITE')
})
test('scope restoration reloads after safe facets/pageSize restore, never persists keyword, and balance-only display stays live', async () => {
  const w = await page(); w.vm.filters.role = 'user'; w.vm.filters.status = 'disabled'; w.vm.pagination.pageSize = 50; w.vm.keywordInput = 'SYNTHETIC_PRIVATE_KEYWORD'; w.vm.applyKeyword(); await settle(); user.setStoredCurrentUser(OTHER); await settle(); w.vm.filters.role = 'admin'; w.vm.pagination.pageSize = 20; await settle(); user.setStoredCurrentUser(ADMIN); await settle(); witness('restored-facets', w)
  assert.deepEqual(readCalls().at(-1).payload, { keyword: 'SYNTHETIC_PRIVATE_KEYWORD', role: 'user', status: 'disabled', page: 1, pageSize: 50 }, 'RESTORED_FACETS_OWN_QUERY'); assert.equal(w.vm.keywordInput, 'SYNTHETIC_PRIVATE_KEYWORD'); assert.equal(w.vm.filters.keyword, 'SYNTHETIC_PRIVATE_KEYWORD')
  const stored = [window.localStorage, window.sessionStorage].flatMap(s => Array.from({ length: s.length }, (_, i) => s.getItem(s.key(i)))).join(''); assert.ok(!stored.includes('SYNTHETIC_PRIVATE_KEYWORD'))
  const count = readCalls().length; defer.add('getUserList'); w.vm.handleUserList(); const current = last('getUserList'); user.setStoredCurrentUser({ ...ADMIN, balance: 9.000001 }); await settle(); assert.equal(readCalls().length, count + 1, 'balance-only change does not reload'); current.resolve(list('同账号仍有效')); await settle(); assert.equal(w.vm.dataSource[0].nickname, '同账号仍有效')
})
for (const [name, field, value, map] of [['getPhoneLocations', 'phone', '13800138000', 'phoneLocations'], ['getIpLocations', 'lastLoginIp', '203.0.113.7', 'ipLocations']]) test('same-account latest list also retires old enrichment: ' + name, async () => {
  const w = await page(); defer.add('getUserList'); defer.add(name); w.vm.handleUserList(); last('getUserList').resolve(ok({ data: [{ ...ROW, [field]: value }], total: 1, page: 1, pageSize: 10 })); await settle(); const old = last(name); w.vm.handleUserList(); last('getUserList').resolve(list('无归属地的新结果')); await settle(); old.resolve(ok({ [value]: { address: 'SYNTHETIC_OLD_LOCATION' } })); await settle(); assert.equal(w.vm[map][value], undefined, 'LATEST_LIST_RETIRES_ENRICHMENT')
})
test('same-tab return restores only applied keyword from memory; draft and private form fields are not retained', async () => {
  const w = await page(); w.vm.keywordInput = 'SYNTHETIC_APPLIED_PRIVATE'; w.vm.applyKeyword(); await settle(); w.vm.keywordInput = 'SYNTHETIC_UNAPPLIED_PRIVATE'; w.vm.openCreateModal(); w.vm.formState.password = 'SYNTHETIC_PRIVATE_PASSWORD'; w.unmount()
  const back = await page(); assert.equal(back.vm.filters.keyword, 'SYNTHETIC_APPLIED_PRIVATE'); assert.equal(back.vm.keywordInput, 'SYNTHETIC_APPLIED_PRIVATE'); assert.equal(back.vm.formState.password, ''); assert.equal(back.vm.visible, false)
  const stored = [window.localStorage, window.sessionStorage].flatMap(s => Array.from({ length: s.length }, (_, i) => s.getItem(s.key(i)))).join(''); assert.ok(!stored.includes('SYNTHETIC_APPLIED_PRIVATE') && !stored.includes('SYNTHETIC_UNAPPLIED_PRIVATE') && !stored.includes('SYNTHETIC_PRIVATE_PASSWORD'))
})
test('applied keyword memory is isolated by principal and role and each restoration emits exactly one query', async () => {
  const w = await page(); w.vm.keywordInput = 'SYNTHETIC_A'; w.vm.applyKeyword(); await settle(); let count = readCalls().length
  user.setStoredCurrentUser(OTHER); await settle(); assert.equal(w.vm.keywordInput, ''); assert.equal(readCalls().length, count + 1)
  w.vm.keywordInput = 'SYNTHETIC_B'; w.vm.applyKeyword(); await settle(); count = readCalls().length
  user.setStoredCurrentUser({ ...OTHER, role: 'user' }); await settle(); assert.equal(w.vm.keywordInput, ''); assert.equal(readCalls().length, count)
  user.setStoredCurrentUser(OTHER); await settle(); assert.equal(w.vm.keywordInput, 'SYNTHETIC_B'); assert.equal(readCalls().length, count + 1)
  user.setStoredCurrentUser(ADMIN); await settle(); assert.equal(w.vm.keywordInput, 'SYNTHETIC_A'); assert.equal(readCalls().length, count + 2)
  user.clearStoredCurrentUser(); user.setStoredCurrentUser(ADMIN); await settle(); assert.equal(w.vm.keywordInput, ''); assert.equal(w.vm.filters.keyword, '')
})
test('keyword memory retains no more than eight principal entries', async () => {
  const w = await page(); w.vm.keywordInput = 'SYNTHETIC_EVICT_FIRST'; w.vm.applyKeyword(); await settle()
  for (let i = 0; i < 8; i += 1) { user.setStoredCurrentUser({ ...OTHER, id: 7500 + i }); await settle(); w.vm.keywordInput = 'SYNTHETIC_ENTRY_' + i; w.vm.applyKeyword(); await settle() }
  user.setStoredCurrentUser(ADMIN); await settle(); assert.equal(w.vm.filters.keyword, '', 'ninth entry evicts the oldest keyword')
  user.setStoredCurrentUser({ ...OTHER, id: 7507 }); await settle(); assert.equal(w.vm.filters.keyword, 'SYNTHETIC_ENTRY_7')
})
test('logout while UserList is unmounted invalidates memory even after same-ID login, without changing display on balance updates', async () => {
  const w = await page(); w.vm.keywordInput = 'SYNTHETIC_BEFORE_LOGOUT'; w.vm.applyKeyword(); await settle(); w.unmount()
  const epoch = user.getCurrentUserLogoutEpoch?.(); user.setStoredCurrentUser({ ...ADMIN, balance: 9.000001 }); assert.equal(user.getCurrentUserLogoutEpoch?.(), epoch)
  user.clearStoredCurrentUser(); assert.equal(user.getCurrentUserLogoutEpoch?.(), epoch + 1, 'LOGOUT_EPOCH_IS_MEMORY_ONLY'); user.setStoredCurrentUser(ADMIN)
  const back = await page(); assert.equal(back.vm.filters.keyword, '', 'unmounted logout clears prior same-principal keyword'); assert.equal(back.vm.keywordInput, '')
})
test('retired native afterClose callbacks do not reset reopened editor/password dialogs', async () => {
  const w = await page(); w.vm.openEditModal(ROW); await settle(); const modal = w.findAllComponents(Antd.Modal)[0], oldClose = modal.props('afterClose'); cancel('编辑用户'); w.vm.openEditModal({ ...ROW, id: 72, nickname: '重开编辑器' }); oldClose(); assert.equal(w.vm.formState.nickname, '重开编辑器')
  w.vm.openResetPasswordModal(ROW); await settle(); const resetModal = w.findAllComponents(Antd.Modal).find(m => m.props('title') === '重置用户密码'), oldResetClose = resetModal.props('afterClose'); cancel('重置用户密码'); w.vm.openResetPasswordModal({ ...ROW, id: 72 }); w.vm.resetPasswordForm.password = 'SYNTHETIC_NEW_PASSWORD'; oldResetClose(); assert.equal(w.vm.resetPasswordForm.id, 72); assert.equal(w.vm.resetPasswordForm.password, 'SYNTHETIC_NEW_PASSWORD')
})
