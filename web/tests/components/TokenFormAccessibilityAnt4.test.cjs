/* Actual Vue + Antd + router + current-user/token owner and input helpers.
 * Only getAccessTokens reads are synthetic; CRUD/delete/copy/provider IO deny.
 * TIKU_WEB_SOURCE/UI_TEST_HELPER keep the fixture unchanged for B/M/R.
 * jsdom visible-label association/focus is not reader/contrast/layout certification.
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
const ORDINARY = { id: 77921, role: 'user', username: 'SYNTHETIC_TOKEN_ACCESSIBILITY_USER', balance: 3.234567 }
const ROW = { id: 181, name: '合成令牌_@1', tokenValue: 'SYNTHETIC_ONLY_ACCESSIBILITY_TOKEN_123456', status: 'enabled', totalQuotaLimit: 0.000062, totalAmountUsed: 1.234567, remark: '合成令牌用途备注' }
const CREATE_LABELS = ['令牌名称', '累计额度上限', '备注']
const EDIT_LABELS = ['令牌名称', '状态', '累计额度上限', '备注']
const CHECKBOX_LABELS = ['令牌名称', '访问令牌', '状态', '累计额度上限', '累计已消费', '最近使用时间', '最近使用 IP', '备注', '操作']
const wrappers = [], routers = [], reads = [], mutations = [], network = [], runtimeErrors = []
const clone = value => JSON.parse(JSON.stringify(value))
const compact = value => String(value || '').replace(/\s+/g, ' ').trim()
function denyIO(kind) { network.push(kind); throw new Error('FORBIDDEN_TOKEN_FORM_IO_' + kind) }
function denyMutation(name) { mutations.push(name); throw new Error('FORBIDDEN_TOKEN_FORM_MUTATION_' + name) }
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath)
api.loaded = true
api.exports = {
  getAccessTokens: params => {
    reads.push({ name: 'getAccessTokens', params: clone(params), actor: user.getStoredCurrentUser()?.id })
    return Promise.resolve({ status: 200, data: [clone(ROW)] })
  },
  ...Object.fromEntries(['createAccessToken', 'updateAccessToken', 'deleteAccessToken'].map(name => [name, () => denyMutation(name)])),
}
require.cache[apiPath] = api
window.XMLHttpRequest.prototype.open = function () { return denyIO('XHR') }
globalThis.fetch = window.fetch = () => denyIO('FETCH')
globalThis.WebSocket = window.WebSocket = class { constructor() { denyIO('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => denyIO(name); transport.get = () => denyIO(name) }
Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: () => denyIO('CLIPBOARD') } })
document.execCommand = () => denyIO('EXEC_COMMAND_COPY')
window.addEventListener('error', event => runtimeErrors.push(String(event.error?.stack || event.message)))
window.addEventListener('unhandledrejection', event => runtimeErrors.push(String(event.reason?.stack || event.reason)))
const Page = require(path.join(web, 'src/pages/AccessTokenManagement/index.vue'))
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
  assert.ok(!['确定', '删除', '复制', '显示全部', '显示全部令牌'].includes(text), 'fixture never confirms CRUD, deletes, copies or reveals tokens')
  const matches = [...root.querySelectorAll('button')].filter(item => compact(item.textContent).replace(/\s/g, '') === text.replace(/\s/g, '') && visible(item))
  assert.equal(matches.length, 1, 'one actual visible native button: ' + text); return matches[0]
}
function dialogs(title) { return [...document.querySelectorAll('.ant-modal')].filter(node => compact(node.querySelector('.ant-modal-title')?.textContent) === title && visible(node)) }
async function open(w, mode) {
  const title = mode === 'create' ? '新增令牌' : '编辑令牌', before = new Set(dialogs(title))
  button(w.element, mode === 'create' ? '新增令牌' : '编辑').click()
  const dialog = await until(() => dialogs(title).find(node => !before.has(node)), 'native own modal opens: ' + title)
  await settle()
  assert.equal(w.vm.visible, true); assert.equal(w.vm.isEditing, mode === 'edit')
  if (mode === 'edit') assert.equal(w.vm.formState.id, ROW.id)
  return dialog
}
async function cancel(w, dialog) {
  button(dialog.querySelector('.ant-modal-footer'), '取消').click()
  await until(() => !w.vm.visible && !visible(dialog), 'native own cancel closes dialog')
  await settle()
}
function labels(root) { return [...root.querySelectorAll('.ant-form-item-label > label')].filter(visible).map(label => compact(label.textContent)) }
function field(root, text) {
  const matches = [...root.querySelectorAll('.ant-form-item-label > label')].filter(label => compact(label.textContent) === text && visible(label))
  assert.equal(matches.length, 1, 'one original visible Chinese label: ' + text)
  const label = matches[0], item = label.closest('.ant-form-item')
  const controls = [...item.querySelectorAll('input:not([type="hidden"]),textarea,select')].filter(control => control.closest('.ant-form-item') === item && visible(control))
  assert.equal(controls.length, 1, 'one actual native control for label: ' + text)
  return { label, control: controls[0], item }
}
function associated(root, text) {
  const result = field(root, text), { label, control } = result
  const refs = String(control.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean)
  const native = label.control === control && [...(control.labels || [])].includes(label)
  const aria = refs.some(id => document.getElementById(id) === label && visible(label))
  assert.ok(native || aria, 'visible Chinese label associates with its own actual control, not placeholder/name/title/adjacency: ' + text)
  assert.ok(control.id, 'stable actual control ID: ' + text)
  if (label.htmlFor) {
    assert.equal(label.htmlFor, control.id, 'for targets actual control, not Antd wrapper: ' + text)
    assert.equal(document.getElementById(label.htmlFor), control, 'label resolves its own exact control: ' + text)
  }
  return result
}
function ids(root) { return Object.fromEntries(labels(root).map(text => [text, associated(root, text).control.id])) }
function type(control, value) {
  control.value = value
  control.dispatchEvent(new Event('input', { bubbles: true }))
  control.dispatchEvent(new Event('change', { bubbles: true }))
}
function preserved(w) {
  assert.equal(w.vm.isAdmin, false); assert.equal(w.vm.canLoadForCurrentUser(), true)
  assert.equal(w.vm.showAllTokens, false); assert.deepEqual(w.vm.visibleTokenIds, [])
  assert.equal(w.vm.submitLoading, false); assert.equal(w.vm.dataSource.length, 1)
  assert.deepEqual(clone(w.vm.dataSource[0]), ROW)
  const code = w.element.querySelector('.token-value-text code')
  assert.ok(code?.textContent.includes('******'), 'original native token masking remains')
  assert.ok(!document.body.textContent.includes(ROW.tokenValue), 'full synthetic token never exposed')
}
function noDuplicateIds() {
  const values = [...document.querySelectorAll('[id]')].map(node => node.id).filter(Boolean)
  assert.equal(new Set(values).size, values.length, 'same Vue app and Antd teleports have no duplicate IDs')
  for (const value of values) for (const secret of [ORDINARY.username, String(ORDINARY.id), ROW.name, ROW.tokenValue, ROW.remark]) assert.ok(!value.includes(secret), 'IDs contain no actor/token metadata')
}
async function page(count = 1) {
  user.setStoredCurrentUser(ORDINARY)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/access-token-management', component: { render: () => null } }, { path: '/elsewhere', component: { render: () => null } }] })
  routers.push(router); await router.push('/access-token-management'); await router.isReady()
  const Root = count === 1 ? Page : defineComponent({ render: () => h('main', null, [h(Page, { key: 'first' }), h(Page, { key: 'second' })]) })
  const root = mount(Root, { attachTo: document.body, global: { plugins: [Antd, router], config: { errorHandler: error => runtimeErrors.push(String(error?.stack || error)) } } })
  wrappers.push(root); await settle()
  const pages = count === 1 ? [root] : root.findAllComponents(Page)
  assert.equal(pages.length, count)
  await until(() => pages.every(w => !w.vm.tableLoading && w.vm.dataSource.length === 1), 'ordinary-user read-only own token rows render')
  assert.equal(reads.length, count)
  for (const read of reads) assert.deepEqual(read, { name: 'getAccessTokens', params: {}, actor: ORDINARY.id })
  for (const w of pages) preserved(w)
  return pages
}
afterEach(async () => {
  for (const w of wrappers.splice(0)) if (w.exists()) w.unmount()
  Swal.close(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle()
  for (const router of routers.splice(0)) router.options.history.destroy()
  user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren()
  console.log('TOKEN_FORM_NATIVE_SAFETY ' + JSON.stringify({ arm: process.env.BMR_ARM || 'MODIFIED', reads, mutations, network, runtimeErrors }))
  assert.deepEqual(mutations, [], 'no CRUD/delete/modal-confirm mutation invoked')
  assert.deepEqual(network, [], 'no real provider/network/database/token-copy IO')
  assert.deepEqual(runtimeErrors, [], 'actual runtime errors fail rather than being swallowed')
  reads.length = mutations.length = network.length = runtimeErrors.length = 0
})

test('ordinary-user native create 3 and edit 4 Chinese labels associate with their exact actual controls', async () => {
  const [w] = await page(), create = await open(w, 'create')
  assert.deepEqual(labels(create), CREATE_LABELS)
  const gaps = []
  for (const text of CREATE_LABELS) try { associated(create, text) } catch (error) { gaps.push(error.message) }
  await cancel(w, create)
  const edit = await open(w, 'edit')
  assert.deepEqual(labels(edit), EDIT_LABELS)
  for (const text of EDIT_LABELS) try { associated(edit, text) } catch (error) { gaps.push(error.message) }
  assert.deepEqual(gaps, [], 'all seven original visible label associations are native/precise visible ARIA')
  preserved(w); noDuplicateIds()
})

test('native input/combobox/spinbutton/textarea retain enabled status, zero quota and existing six-place/remark semantics', async () => {
  const [w] = await page(), create = await open(w, 'create')
  assert.equal(w.vm.formState.name, ''); assert.equal(w.vm.formState.status, 'enabled'); assert.equal(w.vm.formState.totalQuotaLimit, 0)
  assert.equal(field(create, '累计额度上限').control.value, '0.000000')
  for (const name of CREATE_LABELS) assert.equal(field(create, name).control.disabled, false, 'original create control remains enabled: ' + name)
  await cancel(w, create)
  const edit = await open(w, 'edit')
  assert.deepEqual(clone(w.vm.formState), { id: ROW.id, name: ROW.name, originalName: ROW.name, status: 'enabled', totalQuotaLimit: 0.000062, remark: ROW.remark })
  assert.equal(field(edit, '令牌名称').control.tagName, 'INPUT')
  assert.equal(field(edit, '状态').control.getAttribute('role'), 'combobox')
  assert.equal(field(edit, '累计额度上限').control.getAttribute('role'), 'spinbutton')
  assert.equal(field(edit, '累计额度上限').control.value, '0.000062')
  assert.equal(field(edit, '备注').control.tagName, 'TEXTAREA'); assert.equal(field(edit, '备注').control.value, ROW.remark)
  assert.equal(compact(edit.querySelector('.ant-select-selection-item')?.textContent), '启用')
  for (const name of EDIT_LABELS) assert.equal(field(edit, name).control.disabled, false, 'original edit control remains enabled: ' + name)
  preserved(w)
})

test('actual create and edit label clicks focus each native enabled control without confirmation or mutation', async () => {
  const [w] = await page()
  for (const mode of ['create', 'edit']) {
    const dialog = await open(w, mode)
    for (const name of mode === 'create' ? CREATE_LABELS : EDIT_LABELS) {
      const { label, control } = associated(dialog, name)
      assert.equal(control.disabled, false)
      document.activeElement?.blur(); label.click(); await settle()
      assert.equal(document.activeElement, control, 'actual FormItem label click focuses native ' + mode + ': ' + name)
    }
    await cancel(w, dialog); preserved(w)
  }
  assert.equal(reads.length, 1)
})

test('associated native controls keep ordinary-user draft wiring, zero and six-place quota without saving or changing loaded token', async () => {
  const [w] = await page(), create = await open(w, 'create')
  type(associated(create, '令牌名称').control, '合成新令牌_@2')
  type(associated(create, '累计额度上限').control, '0.000062')
  type(associated(create, '备注').control, '新建草稿用途')
  await settle()
  assert.equal(w.vm.formState.name, '合成新令牌_@2'); assert.equal(w.vm.formState.totalQuotaLimit, 0.000062)
  assert.equal(field(create, '累计额度上限').control.value, '0.000062'); assert.equal(w.vm.formState.remark, '新建草稿用途')
  assert.equal(w.vm.formState.status, 'enabled'); preserved(w)
  await cancel(w, create)
  const edit = await open(w, 'edit')
  type(associated(edit, '累计额度上限').control, '0'); type(associated(edit, '备注').control, '编辑草稿用途'); await settle()
  assert.equal(w.vm.formState.totalQuotaLimit, 0); assert.equal(w.vm.formState.remark, '编辑草稿用途')
  type(associated(edit, '累计额度上限').control, '1.234567'); await settle()
  assert.equal(w.vm.formState.totalQuotaLimit, 1.234567); assert.equal(field(edit, '累计额度上限').control.value, '1.234567')
  assert.equal(w.vm.formState.status, 'enabled'); preserved(w)
  assert.equal(reads.length, 1)
})

test('all nine original native implicit column-checkbox labels remain valid without selecting or toggling them', async () => {
  const [w] = await page()
  button(w.element, '列设置').click()
  const panel = await until(() => [...document.querySelectorAll('.column-settings-panel')].find(visible), 'actual column settings opens')
  const boxes = [...panel.querySelectorAll('input[type="checkbox"]')].filter(visible)
  assert.equal(boxes.length, 9)
  const names = []
  for (const box of boxes) {
    const label = [...box.labels].find(item => item.contains(box) && visible(item) && compact(item.textContent))
    assert.ok(label, 'original native wrapping checkbox label is associated')
    assert.equal(label.control, box); names.push(compact(label.textContent))
  }
  assert.deepEqual(names, CHECKBOX_LABELS)
  assert.equal(boxes.find(box => box.value === 'name').disabled, true)
  assert.equal(boxes.find(box => box.value === 'operation').disabled, true)
  button(w.element, '列设置').click(); await settle(); preserved(w)
  assert.equal(reads.length, 1)
})

test('one owner IDs remain stable across create/cancel/edit/reopen and ordinary same-actor metadata refresh', async () => {
  const [w] = await page(), create = await open(w, 'create'), createIds = ids(create)
  type(associated(create, '备注').control, '稳定新增草稿'); await settle()
  w.vm.$forceUpdate(); await settle(); assert.deepEqual(ids(create), createIds)
  await cancel(w, create)
  const edit = await open(w, 'edit'), editIds = ids(edit)
  for (const name of CREATE_LABELS) assert.equal(editIds[name], createIds[name], 'same owner field ID across modes: ' + name)
  type(associated(edit, '备注').control, '稳定编辑草稿'); await settle()
  user.setStoredCurrentUser({ ...ORDINARY, nickname: 'Synthetic metadata only', balance: 7.654321 }); await settle()
  assert.deepEqual(ids(edit), editIds); assert.equal(w.vm.formState.remark, '稳定编辑草稿')
  w.vm.$forceUpdate(); await settle(); assert.deepEqual(ids(edit), editIds)
  await cancel(w, edit)
  const reopened = await open(w, 'edit'); assert.deepEqual(ids(reopened), editIds); assert.equal(w.vm.formState.remark, ROW.remark)
  await cancel(w, reopened)
  const recreated = await open(w, 'create'); assert.deepEqual(ids(recreated), createIds)
  assert.equal(w.vm.formState.totalQuotaLimit, 0); assert.equal(w.vm.formState.remark, '')
  preserved(w); noDuplicateIds(); assert.equal(reads.length, 1)
})

test('same Vue app two actual Token owners have self-resolving distinct field IDs and isolated editable models', async () => {
  const [a, b] = await page(2), ae = await open(a, 'edit'), be = await open(b, 'edit')
  assert.notEqual(a.vm.formState, b.vm.formState); assert.notEqual(a.vm.dataSource, b.vm.dataSource)
  const aIds = ids(ae), bIds = ids(be)
  for (const name of EDIT_LABELS) assert.notEqual(aIds[name], bIds[name], 'same app owner IDs distinct: ' + name)
  noDuplicateIds()
  const aName = associated(ae, '令牌名称'), bRemark = associated(be, '备注')
  aName.label.click(); await settle(); assert.equal(document.activeElement, aName.control)
  bRemark.label.click(); await settle(); assert.equal(document.activeElement, bRemark.control)
  type(aName.control, '仅A令牌草稿'); type(associated(ae, '累计额度上限').control, '1.234567')
  type(bRemark.control, '仅B备注草稿'); await settle()
  assert.equal(a.vm.formState.name, '仅A令牌草稿'); assert.equal(b.vm.formState.name, ROW.name)
  assert.equal(a.vm.formState.totalQuotaLimit, 1.234567); assert.equal(b.vm.formState.totalQuotaLimit, ROW.totalQuotaLimit)
  assert.equal(a.vm.formState.remark, ROW.remark); assert.equal(b.vm.formState.remark, '仅B备注草稿')
  assert.deepEqual(ids(ae), aIds); assert.deepEqual(ids(be), bIds)
  preserved(a); preserved(b)
})

test('same-app owner metadata rerender and one-owner cancel/reopen retain both exact ID sets and the other draft', async () => {
  const [a, b] = await page(2), ae = await open(a, 'edit'), be = await open(b, 'edit'), aIds = ids(ae), bIds = ids(be)
  type(associated(ae, '备注').control, 'Owner A stable draft')
  type(associated(be, '备注').control, 'Owner B stable draft'); await settle()
  user.setStoredCurrentUser({ ...ORDINARY, nickname: 'Ordinary metadata refreshed', balance: 9.876543 }); await settle()
  a.vm.$forceUpdate(); b.vm.$forceUpdate(); await settle()
  assert.deepEqual(ids(ae), aIds); assert.deepEqual(ids(be), bIds)
  assert.equal(a.vm.formState.remark, 'Owner A stable draft'); assert.equal(b.vm.formState.remark, 'Owner B stable draft')
  await cancel(a, ae)
  assert.equal(b.vm.visible, true); assert.deepEqual(ids(be), bIds); assert.equal(b.vm.formState.remark, 'Owner B stable draft')
  const reopened = await open(a, 'edit'); assert.deepEqual(ids(reopened), aIds); assert.deepEqual(ids(be), bIds)
  assert.equal(a.vm.formState.remark, ROW.remark); assert.equal(b.vm.formState.remark, 'Owner B stable draft')
  noDuplicateIds(); preserved(a); preserved(b); assert.equal(reads.length, 2)
})
