/* Actual Vue + Antd + router + current-user/owner/input helpers; only API reads
 * are synthetic. No save, balance confirmation, password reset or external IO.
 * TIKU_WEB_SOURCE and UI_TEST_HELPER let the unchanged fixture qualify B/M/R.
 * DOM associations/clicks are observed in jsdom, not reader/layout certification.
 */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'HTMLVideoElement', 'HTMLAudioElement', 'CustomEvent']) globalThis[key] = window[key]
window.scrollTo = () => {}
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick, defineComponent, h } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue'), Swal = require('sweetalert2')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const ADMIN = { id: 77911, role: 'admin', username: 'SYNTHETIC_ACCESSIBILITY_ADMIN', balance: 3.234567 }
const ROW = { id: 81, username: 'SYNTHETIC_ACCESSIBILITY_TARGET', nickname: 'Synthetic accessibility target', role: 'user', status: 'enabled', balance: 1.234567, phone: '', wechatId: '', perms: 'search', registerSource: 'admin' }
const wrappers = [], routers = [], reads = [], mutations = [], network = [], runtimeErrors = []
const clone = value => JSON.parse(JSON.stringify(value))
const ok = data => ({ status: 200, data })
function denyIO(kind) { network.push(kind); throw new Error('FORBIDDEN_USER_FORM_IO_' + kind) }
function denyMutation(name) { mutations.push(name); throw new Error('FORBIDDEN_USER_FORM_MUTATION_' + name) }
function read(name, params, value) { reads.push({ name, params: params === undefined ? null : clone(params) }); return Promise.resolve(ok(clone(value))) }
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath)
api.loaded = true
api.exports = {
  getUserList: params => read('getUserList', params, { data: [ROW], total: 1, page: 1, pageSize: 10 }),
  getSystemSettings: () => read('getSystemSettings', undefined, { newUserBonusAmount: 0.123456 }),
  getPhoneLocations: () => read('getPhoneLocations', undefined, {}),
  getIpLocations: () => read('getIpLocations', undefined, {}),
  ...Object.fromEntries(['addUser', 'updateUser', 'adjustUserBalance', 'resetUserPassword', 'delUser', 'createWechatLoginQRCode', 'resolveAdminWechatIDByScan', 'resolveAdminWechatIDByCode'].map(name => [name, () => denyMutation(name)])),
}
require.cache[apiPath] = api
window.XMLHttpRequest.prototype.open = function () { return denyIO('XHR') }
globalThis.fetch = window.fetch = () => denyIO('FETCH')
globalThis.WebSocket = window.WebSocket = class { constructor() { denyIO('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => denyIO(name); transport.get = () => denyIO(name) }
window.addEventListener('error', event => runtimeErrors.push(String(event.error?.stack || event.message)))
window.addEventListener('unhandledrejection', event => runtimeErrors.push(String(event.reason?.stack || event.reason)))
const Page = require(path.join(web, 'src/pages/UserList/index.vue'))
const EDIT_LABELS = ['用户名', '昵称', '角色', '状态', '当前余额', '手机号', '重指派同号', '微信 ID', '重指派微信', '创建来源']
const BALANCE_LABELS = ['目标用户', '当前余额', '钱包类型', '修改模式', '调整金额']
const controlSelector = 'input:not([type="hidden"]),textarea,select,button[role="switch"]'
const compact = value => String(value || '').replace(/\s+/g, ' ').trim()
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function until(check, label) {
  const start = process.hrtime.bigint()
  while (Number(process.hrtime.bigint() - start) / 1e6 < 1000) {
    const value = check(); if (value) return value
    await new Promise(resolve => setTimeout(resolve, 5)); await settle()
  }
  assert.fail(label + ' monotonicElapsedMs=' + Number(process.hrtime.bigint() - start) / 1e6)
}
function visible(node) {
  if (!node?.isConnected) return false
  for (let item = node; item && item.nodeType === 1; item = item.parentElement) {
    const style = window.getComputedStyle(item)
    if (item.hidden || item.inert || item.getAttribute('aria-hidden') === 'true' || style.display === 'none' || style.visibility === 'hidden') return false
  }
  return true
}
function button(root, text) {
  const node = [...root.querySelectorAll('button')].find(item => compact(item.textContent).replace(/\s/g, '') === text.replace(/\s/g, '') && visible(item))
  assert.ok(node, 'actual native button: ' + text); return node
}
function shownDialogs(title) { return [...document.querySelectorAll('.ant-modal')].filter(node => compact(node.querySelector('.ant-modal-title')?.textContent) === title && visible(node)) }
async function openedBy(root, action, title) {
  const before = new Set(shownDialogs(title))
  button(root, action).click()
  const node = await until(() => shownDialogs(title).find(item => !before.has(item)), 'native dialog opens: ' + title)
  await settle(); return node
}
function field(root, text) {
  const matches = [...root.querySelectorAll('.ant-form-item-label > label')].filter(label => compact(label.textContent) === text && visible(label))
  assert.equal(matches.length, 1, 'one original visible label: ' + text)
  const label = matches[0], item = label.closest('.ant-form-item')
  const controls = [...item.querySelectorAll(controlSelector)].filter(control => control.closest('.ant-form-item') === item && visible(control))
  assert.equal(controls.length, 1, 'one actual input/control for visible label: ' + text)
  return { label, control: controls[0], item }
}
function associated(root, text) {
  const result = field(root, text), { label, control } = result
  const native = [...(control.labels || [])].includes(label) && label.control === control
  const refs = String(control.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean)
  const aria = refs.some(id => document.getElementById(id) === label && visible(label))
  assert.ok(native || aria, 'visible Chinese label must be associated with its exact native control: ' + text)
  assert.ok(control.id, 'stable actual control ID: ' + text)
  if (label.htmlFor) {
    assert.equal(label.htmlFor, control.id, 'for targets actual input, not wrapper: ' + text)
    assert.equal(document.getElementById(label.htmlFor), control, 'for resolves exact control: ' + text)
  }
  return result
}
function labels(root) { return [...root.querySelectorAll('.ant-form-item-label > label')].filter(visible).map(label => compact(label.textContent)) }
function allAssociated(root) { return labels(root).map(text => associated(root, text)) }
function ids(root) { return Object.fromEntries(labels(root).map(text => [text, associated(root, text).control.id])) }
function type(control, value) {
  control.value = value
  control.dispatchEvent(new Event('input', { bubbles: true }))
  control.dispatchEvent(new Event('change', { bubbles: true }))
}
async function page(count = 1) {
  user.setStoredCurrentUser(ADMIN)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/users', component: { render: () => null } }, { path: '/elsewhere', component: { render: () => null } }] })
  routers.push(router); await router.push('/users'); await router.isReady()
  const Root = count === 1 ? Page : defineComponent({ render: () => h('main', null, [h(Page, { key: 'first' }), h(Page, { key: 'second' })]) })
  const root = mount(Root, { attachTo: document.body, global: { plugins: [Antd, router], config: { errorHandler: error => runtimeErrors.push(String(error?.stack || error)) } } })
  wrappers.push(root); await settle()
  const pages = count === 1 ? [root] : root.findAllComponents(Page)
  assert.equal(pages.length, count)
  for (const w of pages) { assert.equal(w.vm.dataSource.length, 1); assert.equal(w.vm.dataSource[0].id, ROW.id); assert.ok(w.element.textContent.includes(ROW.username)) }
  return pages
}
async function editor(w) { const dialog = await openedBy(w.element.querySelector('.desktop-user-table'), '编辑', '编辑用户'); assert.equal(w.vm.formState.id, ROW.id); return dialog }
async function balance(edit) { return openedBy(edit, '修改余额', '修改用户余额') }
async function cancel(dialog, state) {
  button(dialog.querySelector('.ant-modal-footer'), '取消').click()
  await until(() => state() === false && !visible(dialog), 'native cancel closes own dialog')
  await settle()
}
function noDuplicateIds() {
  const values = [...document.querySelectorAll('[id]')].map(node => node.id).filter(Boolean)
  assert.equal(new Set(values).size, values.length, 'all IDs remain unique within the same Vue app and its teleports')
  for (const value of values) for (const secret of [ADMIN.username, ROW.username, ROW.nickname, '77911']) assert.ok(!value.includes(secret), 'field IDs contain no user metadata')
}
afterEach(async () => {
  for (const w of wrappers.splice(0)) if (w.exists()) w.unmount()
  Swal.close(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle()
  for (const r of routers.splice(0)) r.options.history.destroy()
  user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren()
  const observed = { readCount: reads.length, mutationCount: mutations.length, realIOCount: network.length, runtimeErrorCount: runtimeErrors.length }
  console.log('USER_FORM_NATIVE_SAFETY ' + JSON.stringify(observed))
  assert.deepEqual(mutations, [], 'no save/balance-confirm/reset/scan/resolve mutation was invoked')
  assert.deepEqual(network, [], 'no real network/provider/database IO')
  assert.deepEqual(runtimeErrors, [], 'actual runtime errors fail, never be swallowed')
  reads.length = mutations.length = network.length = runtimeErrors.length = 0
})

test('actual editor 10 and balance 4 visible Chinese labels associate with their exact controls', async () => {
  const [w] = await page(), edit = await editor(w), bal = await balance(edit)
  assert.deepEqual(labels(edit), EDIT_LABELS); assert.deepEqual(labels(bal), BALANCE_LABELS)
  const gaps = []
  for (const [root, names] of [[edit, EDIT_LABELS], [bal, BALANCE_LABELS]]) for (const name of names) {
    try { associated(root, name) } catch (error) { gaps.push(error.message) }
  }
  assert.deepEqual(gaps, [], 'all original 14 visible-label associations; placeholder/name/title/adjacency do not qualify')
})

test('native disabled/read-only controls, comboboxes, spinbutton and six-place balances retain original semantics', async () => {
  const [w] = await page(), edit = await editor(w), bal = await balance(edit)
  const username = field(edit, '用户名').control, source = field(edit, '创建来源').control
  assert.equal(username.disabled, true); assert.equal(username.value, ROW.username); assert.equal(source.disabled, true)
  assert.equal(field(edit, '当前余额').control.readOnly, true); assert.equal(field(edit, '当前余额').control.value, '1.234567')
  assert.equal(field(bal, '当前余额').control.readOnly, true); assert.equal(field(bal, '当前余额').control.value, '1.234567')
  assert.equal(field(bal, '目标用户').control.readOnly, true); assert.ok(field(bal, '目标用户').control.value.includes(ROW.username))
  for (const [root, name] of [[edit, '角色'], [edit, '状态'], [bal, '修改模式']]) assert.equal(field(root, name).control.getAttribute('role'), 'combobox')
  assert.equal(field(bal, '调整金额').control.getAttribute('role'), 'spinbutton')
  for (const name of ['重指派同号', '重指派微信']) assert.equal(field(edit, name).control.getAttribute('role'), 'switch')
  assert.equal(w.vm.balanceSubmitting, false); assert.equal(w.vm.balanceForm.mode, 'increase')
  assert.equal(w.vm.formState.balance, ROW.balance)
})

test('clicking actual enabled text/phone/select labels focuses the native control without submitting', async () => {
  const [w] = await page(), edit = await editor(w), bal = await balance(edit)
  for (const [root, name] of [[edit, '昵称'], [edit, '手机号'], [edit, '微信 ID'], [edit, '角色'], [edit, '状态'], [bal, '修改模式'], [bal, '调整金额']]) {
    const { label, control } = associated(root, name)
    assert.equal(control.disabled, false)
    document.activeElement?.blur()
    label.click(); await settle()
    assert.equal(document.activeElement, control, 'real FormItem label click focuses native control: ' + name)
  }
  assert.deepEqual(mutations, [])
})

test('label activation toggles the actual switches and the original implicit checkbox label once', async () => {
  const [w] = await page(), edit = await editor(w), bal = await balance(edit)
  for (const [name, model] of [['重指派同号', 'forceReassignPhone'], ['重指派微信', 'forceReassignWechat']]) {
    const { label, control } = associated(edit, name)
    assert.equal(control.getAttribute('aria-checked'), 'false')
    label.click(); await settle()
    assert.equal(control.getAttribute('aria-checked'), 'true', 'native label activates switch: ' + name)
    assert.equal(w.vm.formState[model], true)
    label.click(); await settle(); assert.equal(w.vm.formState[model], false)
  }
  const checkbox = bal.querySelector('input[type="checkbox"]')
  assert.ok(checkbox); assert.equal(checkbox.checked, true)
  const ownLabel = [...checkbox.labels].find(label => label.contains(checkbox) && compact(label.textContent).includes('同步到账单流水'))
  assert.ok(ownLabel, 'the original wrapped checkbox label is valid native association')
  ownLabel.click(); await settle(); assert.equal(checkbox.checked, false); assert.equal(w.vm.balanceForm.showInStatement, false)
  ownLabel.click(); await settle(); assert.equal(checkbox.checked, true); assert.equal(w.vm.balanceForm.showInStatement, true)
})

test('the unchanged implicit checkbox has a valid native wrapping label and toggles only its own draft', async () => {
  const [w] = await page(), edit = await editor(w), bal = await balance(edit)
  const checkbox = bal.querySelector('input[type="checkbox"]')
  assert.ok(checkbox); assert.equal(checkbox.checked, true)
  const ownLabel = [...checkbox.labels].find(label => label.contains(checkbox) && compact(label.textContent).includes('同步到账单流水'))
  assert.ok(ownLabel, 'the original wrapped checkbox label is valid native association')
  assert.equal(ownLabel.control, checkbox)
  ownLabel.click(); await settle(); assert.equal(checkbox.checked, false); assert.equal(w.vm.balanceForm.showInStatement, false)
  ownLabel.click(); await settle(); assert.equal(checkbox.checked, true); assert.equal(w.vm.balanceForm.showInStatement, true)
  assert.equal(w.vm.formState.balance, 1.234567); assert.deepEqual(mutations, [])
})

test('native balance mode selection keeps the same amount ID while the visible Chinese label changes', async () => {
  const [w] = await page(), edit = await editor(w), bal = await balance(edit)
  const amountId = associated(bal, '调整金额').control.id, mode = associated(bal, '修改模式')
  mode.label.click(); await settle(); assert.equal(document.activeElement, mode.control)
  mode.item.querySelector('.ant-select-selector').dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
  await settle()
  const option = await until(() => [...document.querySelectorAll('.ant-select-item-option')].find(node => visible(node) && compact(node.textContent) === '覆盖'), 'native override option')
  option.click(); await settle()
  assert.equal(w.vm.balanceForm.mode, 'override')
  const changed = associated(bal, '覆盖后的金额')
  assert.equal(changed.control.id, amountId); assert.equal(changed.control.getAttribute('role'), 'spinbutton')
  type(changed.control, '-0.000062'); await settle()
  assert.equal(w.vm.balanceForm.amount, -0.000062); assert.equal(changed.control.value, '-0.000062')
  assert.equal(w.vm.formState.balance, 1.234567); assert.deepEqual(mutations, [])
})

test('associated native inputs retain model wiring, mainland paste normalization and six-place draft without saving', async () => {
  const [w] = await page(), edit = await editor(w), bal = await balance(edit)
  type(associated(edit, '昵称').control, 'Synthetic changed nickname')
  type(associated(edit, '微信 ID').control, 'synthetic_openid')
  await settle(); assert.equal(w.vm.formState.nickname, 'Synthetic changed nickname'); assert.equal(w.vm.formState.wechatId, 'synthetic_openid')
  const phone = associated(edit, '手机号').control
  assert.equal(phone.getAttribute('inputmode'), 'numeric'); assert.equal(phone.getAttribute('autocomplete'), 'tel-national'); assert.equal(phone.maxLength, 11)
  const paste = new Event('paste', { bubbles: true, cancelable: true })
  Object.defineProperty(paste, 'clipboardData', { value: { getData: () => '+86 13800000000' } })
  phone.dispatchEvent(paste); await settle()
  assert.equal(phone.value, '13800000000'); assert.equal(w.vm.formState.phone, '13800000000')
  type(phone, '13900000000'); await settle(); assert.equal(phone.value, '13900000000'); assert.equal(w.vm.formState.phone, '13900000000')
  type(associated(bal, '调整金额').control, '0.000062'); await settle()
  assert.equal(w.vm.balanceForm.amount, 0.000062); assert.equal(field(bal, '调整金额').control.value, '0.000062')
  assert.equal(w.vm.formState.balance, 1.234567); assert.deepEqual(mutations, [])
})

test('same Vue app two real UserList owners and four open dialogs never collide or cross-write models', async () => {
  const [a, b] = await page(2), ae = await editor(a), be = await editor(b), ab = await balance(ae), bb = await balance(be)
  assert.notEqual(a.vm.userListContext, b.vm.userListContext)
  for (const root of [a.element.querySelector('.filter-form'), b.element.querySelector('.filter-form'), ae, be, ab, bb]) allAssociated(root)
  noDuplicateIds()
  assert.notEqual(ids(ae)['昵称'], ids(be)['昵称']); assert.notEqual(ids(ab)['调整金额'], ids(bb)['调整金额'])
  type(associated(ae, '昵称').control, 'Owner A own draft'); await settle()
  assert.equal(a.vm.formState.nickname, 'Owner A own draft'); assert.equal(b.vm.formState.nickname, ROW.nickname)
  const sw = associated(be, '重指派同号')
  sw.label.click(); await settle()
  assert.equal(b.vm.formState.forceReassignPhone, true); assert.equal(a.vm.formState.forceReassignPhone, false)
  type(associated(ab, '调整金额').control, '0.000062'); await settle()
  assert.equal(a.vm.balanceForm.amount, 0.000062); assert.equal(b.vm.balanceForm.amount, null)
})

test('field IDs remain stable through real rerender, same-actor metadata and cancel/reopen, with other owner intact', async () => {
  const [a, b] = await page(2), ae = await editor(a), be = await editor(b), ab = await balance(ae), bb = await balance(be)
  const original = { ae: ids(ae), be: ids(be), ab: ids(ab), bb: ids(bb) }
  type(associated(ae, '昵称').control, 'Stable A draft'); type(associated(be, '昵称').control, 'Stable B draft'); await settle()
  a.vm.$forceUpdate(); b.vm.$forceUpdate(); await settle()
  user.setStoredCurrentUser({ ...ADMIN, nickname: 'Synthetic metadata only', balance: 7.654321 }); await settle()
  assert.deepEqual(ids(ae), original.ae); assert.deepEqual(ids(be), original.be); assert.deepEqual(ids(ab), original.ab); assert.deepEqual(ids(bb), original.bb)
  assert.equal(a.vm.formState.nickname, 'Stable A draft'); assert.equal(b.vm.formState.nickname, 'Stable B draft')
  await cancel(ab, () => a.vm.balanceVisible)
  const reopenedBalance = await balance(ae); assert.deepEqual(ids(reopenedBalance), original.ab)
  await cancel(reopenedBalance, () => a.vm.balanceVisible); await cancel(ae, () => a.vm.visible)
  const reopenedEdit = await editor(a); assert.deepEqual(ids(reopenedEdit), original.ae)
  assert.deepEqual(ids(be), original.be); assert.deepEqual(ids(bb), original.bb); assert.equal(b.vm.formState.nickname, 'Stable B draft')
  noDuplicateIds()
})

test('create/filter/reset labels use actual native controls and keep create/reset drafts independent without mutation', async () => {
  const [w] = await page()
  const filters = w.element.querySelector('.filter-form')
  assert.deepEqual(labels(filters), ['关键词', '角色', '状态']); allAssociated(filters)
  const create = await openedBy(w.element, '新增用户', '新增用户')
  assert.deepEqual(labels(create), ['用户名', '密码', '昵称', '角色', '状态', '启用赠送', '赠送金额', '创建来源']); allAssociated(create)
  assert.equal(field(create, '用户名').control.disabled, false); assert.equal(field(create, '密码').control.type, 'password')
  assert.equal(field(create, '创建来源').control.disabled, true)
  const giftId = associated(create, '赠送金额').control.id
  const gift = associated(create, '启用赠送')
  gift.label.click(); await settle(); assert.equal(w.vm.formState.giftEnabled, false); assert.ok(!labels(create).includes('赠送金额'))
  gift.label.click(); await settle(); assert.equal(w.vm.formState.giftEnabled, true); assert.equal(associated(create, '赠送金额').control.id, giftId)
  type(associated(create, '用户名').control, 'synthetic_create'); type(associated(create, '密码').control, 'SyntheticCreateOnly!1'); await settle()
  assert.equal(w.vm.formState.username, 'synthetic_create'); assert.equal(w.vm.formState.password, 'SyntheticCreateOnly!1')
  const reset = await openedBy(w.element.querySelector('.desktop-user-table'), '重置密码', '重置用户密码')
  assert.deepEqual(labels(reset), ['目标用户', '新密码']); allAssociated(reset)
  assert.equal(field(reset, '目标用户').control.disabled, true)
  type(associated(reset, '新密码').control, 'SyntheticResetOnly!2'); await settle()
  assert.equal(w.vm.resetPasswordForm.password, 'SyntheticResetOnly!2'); assert.equal(w.vm.formState.password, 'SyntheticCreateOnly!1')
  const resetIds = ids(reset); w.vm.$forceUpdate(); await settle(); assert.deepEqual(ids(reset), resetIds)
  noDuplicateIds(); assert.deepEqual(mutations, [])
})
