/* Native owner regression: actual SearchAccountConfig and Antd; synthetic transport only. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(root, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(root, 'tests/helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const A = { id: 7201, role: 'admin', username: 'SYNTHETIC_A', balance: 1 }
const B = { id: 7202, role: 'admin', username: 'SYNTHETIC_B', balance: 2 }
const reads = [], writes = [], notices = [], confirms = [], routes = [], nativeErrors = []
let wrappers = []
function transport(collection, provider, payload) {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  const call = { provider, payload: payload === undefined ? undefined : JSON.parse(JSON.stringify(payload)), actor: user.getStoredCurrentUser()?.id, promise, completed: false }
  call.resolve = value => { call.completed = true; resolve(value) }
  call.reject = error => { call.completed = true; reject(error) }
  collection.push(call)
  return promise
}
const apiPath = path.join(root, 'src/api/TikuComponentApi.js')
const api = new Module(apiPath); api.loaded = true
api.exports = { getSearchAccountConfigs: () => transport(reads), updateSearchAccountConfig: (provider, payload) => transport(writes, provider, payload) }
require.cache[apiPath] = api
const routerPath = require.resolve('vue-router')
const router = new Module(routerPath); router.loaded = true
router.exports = { useRouter: () => ({ push: value => routes.push(value) }), onBeforeRouteLeave: callback => routes.push({ leave: callback }) }
require.cache[routerPath] = router
for (const kind of ['success', 'error', 'warning']) {
  const native = Antd.message[kind].bind(Antd.message)
  Antd.message[kind] = (...args) => { notices.push({ kind, text: String(args[0]?.content || args[0] || '') }); return native(...args) }
}
const nativeConfirm = Antd.Modal.confirm.bind(Antd.Modal)
Antd.Modal.confirm = options => { confirms.push(options); return nativeConfirm(options) }
const Page = require(path.join(root, 'src/pages/SearchAccountConfig/index.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
function rows(tag = 'A') { return [
  { provider: 'icodef', providerLabel: 'SYNTHETIC_ICODEF', tokenKey: 'token', tokenValue: `SYNTHETIC_TOKEN_${tag}`, enabled: true, remark: `SYNTHETIC_REMARK_${tag}`, flatPrice: 0.12345678 },
  { provider: 'wenku', providerLabel: 'SYNTHETIC_WENKU', tokenKey: 'token', tokenValue: `SYNTHETIC_SECOND_${tag}`, enabled: false, remark: `SYNTHETIC_SECOND_REMARK_${tag}`, flatPrice: 0.000001 },
  { provider: 'ai', providerLabel: 'SYNTHETIC_AI', tokenValue: `SYNTHETIC_AI_${tag}`, enabled: true, remark: '', flatPrice: 0 },
] }
async function mountPage(actor = A, loaded = true) {
  if (actor) user.setStoredCurrentUser(actor); else user.clearStoredCurrentUser()
  const w = mount(Page, { attachTo: document.body, global: { plugins: [Antd], config: { errorHandler: error => nativeErrors.push(error) } } }); wrappers.push(w)
  await settle()
  if (loaded && reads.length) { reads.at(-1).resolve({ status: 200, data: rows(actor?.id === B.id ? 'B' : 'A') }); await settle() }
  return w
}
function button(w, label) { return w.findAll('button').find(item => item.text().replace(/\s/g, '') === label.replace(/\s/g, '')) }
function click(w, label) { const found = button(w, label); assert.ok(found, `NATIVE_BUTTON_${label}`); found.element.click() }
function finishLatest(tag = 'B') { if (reads.at(-1) && !reads.at(-1).completed) reads.at(-1).resolve({ status: 200, data: rows(tag) }) }
function nativeInput(w, placeholder = '请输入默认 Token') { return w.get(`input[placeholder="${placeholder}"]`) }
function callback(component, name) { const fn = component.vm.$.vnode.props?.[name]; assert.equal(typeof fn, 'function', `NATIVE_CALLBACK_${name}`); return fn }
function buttonComponent(w, label) { const item = w.findAllComponents(Antd.Button).find(item => item.text().replace(/\s/g, '') === label.replace(/\s/g, '')); assert.ok(item); return item }
function state(w) { return JSON.stringify({ rows: w.vm.dataSource, loading: w.vm.tabLoading, saving: w.vm.savingAll, dirty: w.vm.hasUnsavedChanges }) }
async function tokenInput(w, value) { await nativeInput(w).setValue(value); await settle() }
afterEach(async () => {
  for (const w of wrappers) if (w.exists()) w.unmount()
  wrappers = []
  for (const c of reads) if (!c.completed) c.resolve({ status: 200, data: [] })
  for (const c of writes) if (!c.completed) c.resolve({ status: 503, message: 'SYNTHETIC_CLEANUP' })
  await settle(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle()
  user.clearStoredCurrentUser(); document.body.replaceChildren(); window.sessionStorage.clear()
  reads.length = 0; writes.length = 0; notices.length = 0; confirms.length = 0; routes.length = 0
  const errors = nativeErrors.splice(0); assert.deepEqual(errors, [], 'ACTUAL_VUE_ANTD_MUST_HAVE_NO_NATIVE_RUNTIME_ERRORS')
})

test('SAC_PRINCIPAL baseline late A GET cannot repopulate B data or clear B loading', async () => {
  const w = await mountPage(A, false), old = reads[0]
  user.setStoredCurrentUser(B); await settle()
  old.resolve({ status: 200, data: rows('A') }); await settle()
  console.log(`SAC_BASELINE_LATE_GET reads=${reads.length} token=${w.vm.dataSource[0]?.tokenValue || 'EMPTY'} loading=${w.vm.tabLoading}`)
  assert.equal(w.vm.dataSource.some(row => row.tokenValue === 'SYNTHETIC_TOKEN_A'), false, 'LATE_A_MUST_NOT_REPOPULATE_B')
  assert.equal(w.vm.tabLoading, true, 'OLD_FINALLY_MUST_NOT_CLEAR_B_LOADING')
  finishLatest(); await settle()
})

test('SAC_PRINCIPAL baseline batch stops before second PUT when first A PUT completes after switch', async () => {
  const w = await mountPage(A)
  click(w, '保存全部配置'); await settle(); assert.equal(writes.length, 1)
  user.setStoredCurrentUser(B); await settle()
  writes[0].resolve({ status: 200 }); await settle()
  console.log(`SAC_BASELINE_BATCH writes=${writes.length} providers=${writes.map(call => call.provider).join(',')} actors=${writes.map(call => call.actor).join(',')}`)
  assert.equal(writes.length, 1, 'RETIRED_A_BATCH_MUST_NOT_SEND_SECOND_PUT')
})

test('SAC_PRINCIPAL anonymous/nonadmin/invalid positive32bit ID performs zero private IO through native and direct actions', async () => {
  const invalid = [undefined, null, 0, '', ' ', -1, 1.5, '1.5', '1e3', true, 2147483648, Number.MAX_SAFE_INTEGER + 1]
  for (const actor of [null, { ...A, role: 'user' }, ...invalid.map(id => ({ ...A, id }))]) {
    const w = await mountPage(actor, false)
    assert.equal(reads.length, 0, 'UNAUTHORIZED_ZERO_PRIVATE_GET')
    w.vm.handleSearchAccountConfigs(); w.vm.onSaveAll(); w.vm.goToAIConfig()
    button(w, '刷新')?.element.click(); button(w, '保存全部配置')?.element.click(); await settle()
    assert.equal(reads.length, 0); assert.equal(writes.length, 0); assert.equal(w.vm.dataSource.length, 0)
    assert.equal(routes.some(route => typeof route === 'string'), false, 'UNAUTHORIZED_NO_ADMIN_NAVIGATION')
    w.unmount()
  }
  const w = await mountPage({ ...A, id: '7201' })
  assert.equal(reads.length, 1, 'DECIMAL_STRING_ID_ACCEPTED'); assert.equal(w.vm.dataSource[0].tokenValue, 'SYNTHETIC_TOKEN_A')
})

test('SAC_PRINCIPAL exact same-actor payload retains all nonAI providers, trim/clear and existing 6/8 precision without extra fields', async () => {
  const w = await mountPage(A)
  await tokenInput(w, '  SYNTHETIC_EDITED_TOKEN  ')
  const first = w.vm.dataSource[0], second = w.vm.dataSource[1]
  first.provider = ' icodef '; first.remark = '  SYNTHETIC_EDITED_REMARK  '; first.flatPrice = 0.87654321; first.enabled = false
  second.tokenValue = ''; second.remark = ' SYNTHETIC_SECOND_NOTE '; second.flatPrice = 0.123456
  click(w, '保存全部配置'); await settle()
  assert.deepEqual(writes.map(({ provider, payload }) => ({ provider, payload })), [{ provider: 'icodef', payload: { tokenValue: 'SYNTHETIC_EDITED_TOKEN', enabled: false, remark: 'SYNTHETIC_EDITED_REMARK', flatPrice: 0.87654321 } }])
  writes[0].resolve({ status: 200 }); await settle()
  assert.deepEqual(writes[1].payload, { tokenValue: '', enabled: false, remark: 'SYNTHETIC_SECOND_NOTE', flatPrice: 0.123456 })
  assert.equal(writes[1].provider, 'wenku'); writes[1].resolve({ status: 200 }); await settle()
  assert.equal(writes.length, 2, 'AI_METADATA_IS_NEVER_WRITTEN_HERE')
  assert.ok(notices.some(item => item.kind === 'success')); assert.equal(w.vm.savingAll, false)
  finishLatest('SAVED'); await settle()
  assert.equal(w.vm.dataSource[0].tokenValue, 'SYNTHETIC_TOKEN_SAVED', 'SUCCESSFUL_FOLLOWUP_GET_IS_RENDERED')
})

test('SAC_PRINCIPAL existing same-actor GET renders full rows, native precision8 input and AI navigation without writing AI metadata', async () => {
  const w = await mountPage(A)
  assert.equal(reads.length, 1); assert.equal(w.vm.tabLoading, false)
  assert.deepEqual(JSON.parse(JSON.stringify(w.vm.dataSource)), rows('A'))
  assert.equal(nativeInput(w).element.value, 'SYNTHETIC_TOKEN_A')
  assert.equal(w.findComponent(Antd.InputNumber).props('precision'), 8, 'EXISTING_NATIVE_EIGHT_DECIMAL_INPUT')
  assert.equal(w.findAllComponents(Antd.InputNumber).length, 2, 'AI_PRICE_NOT_EDITED_IN_THIS_PAGE')
  click(w, '前往 AI 搜索配置页'); await settle()
  assert.ok(routes.includes('/adapter/ai-search-config')); assert.equal(writes.length, 0)
})

test('SAC_PRINCIPAL existing AI-only config remains excluded from default-account writes', async () => {
  const w = await mountPage(A, false)
  reads[0].resolve({ status: 200, data: [rows('A')[2]] }); await settle()
  click(w, '保存全部配置'); await settle()
  assert.equal(writes.length, 0); assert.equal(notices.some(item => item.kind === 'success'), false)
  assert.ok(notices.some(item => item.kind === 'warning'))
})

test('SAC_PRINCIPAL failed initial GET cannot save defaults; explicit retry can load and save', async () => {
  const w = await mountPage(A, false)
  reads[0].resolve({ status: 503, message: 'SYNTHETIC_INITIAL_FAILURE' }); await settle()
  click(w, '保存全部配置'); w.vm.onSaveAll(); await settle(); assert.equal(writes.length, 0)
  assert.equal(w.vm.dataSource.length, 0); assert.ok(notices.some(item => item.kind === 'error'))
  click(w, '刷新'); await settle(); assert.equal(reads.length, 2)
  finishLatest('A'); await settle(); click(w, '保存全部配置'); await settle(); assert.equal(writes.length, 1)
})

test('SAC_PRINCIPAL same-actor latest GET wins and old completion cannot clear current loading', async () => {
  const w = await mountPage(A)
  w.vm.handleSearchAccountConfigs(); await settle(); const old = reads.at(-1)
  w.vm.handleSearchAccountConfigs(); await settle(); const current = reads.at(-1)
  assert.notEqual(old, current); assert.equal(reads.length, 3)
  old.resolve({ status: 200, data: rows('OLDER') }); await settle()
  assert.equal(w.vm.tabLoading, true, 'OLDER_FINALLY_CANNOT_RELEASE_NEWEST_LOADING')
  assert.equal(w.vm.dataSource[0].tokenValue, 'SYNTHETIC_TOKEN_A')
  current.resolve({ status: 200, data: rows('NEWEST') }); await settle()
  assert.equal(w.vm.dataSource[0].tokenValue, 'SYNTHETIC_TOKEN_NEWEST'); assert.equal(w.vm.tabLoading, false)
})

test('SAC_PRINCIPAL real input made during same-actor GET remains a dirty draft rather than being overwritten', async () => {
  const w = await mountPage(A)
  click(w, '刷新'); await settle(); const pending = reads.at(-1)
  await tokenInput(w, 'SYNTHETIC_TYPED_DURING_GET')
  click(w, '保存全部配置'); w.vm.onSaveAll(); await settle(); assert.equal(writes.length, 0, 'GET_PENDING_CANNOT_SAVE_OLD_FORM')
  pending.resolve({ status: 200, data: rows('SERVER_AFTER_EDIT') }); await settle()
  assert.equal(w.vm.dataSource[0].tokenValue, 'SYNTHETIC_TYPED_DURING_GET'); assert.equal(w.vm.tabLoading, false)
  click(w, '刷新'); await settle(); assert.match(confirms.at(-1)?.title || '', /未保存/, 'IN_FLIGHT_EDIT_REMAINS_GUARDED')
  confirms.at(-1).onCancel(); await settle(); assert.equal(reads.length, 2)
})

test('SAC_PRINCIPAL balance-only profile updates preserve real dirty tokens and do not refetch', async () => {
  const w = await mountPage(A)
  await tokenInput(w, 'SYNTHETIC_BALANCE_STABLE_DRAFT')
  const before = state(w), readCount = reads.length
  user.setStoredCurrentUser({ ...A, balance: 987.123456 }); await settle()
  assert.equal(state(w), before); assert.equal(reads.length, readCount)
  click(w, '刷新'); await settle(); assert.match(confirms.at(-1)?.title || '', /未保存/, 'BALANCE_ONLY_UPDATE_RETAINS_UNSAVED_DRAFT_CONFIRMATION')
  confirms.at(-1).onCancel(); await settle()
})

for (const failure of ['reject', 'non200']) {
  test(`SAC_PRINCIPAL retired A GET ${failure} cannot toast/change current B loading or rows`, async () => {
    const w = await mountPage(A, false), old = reads[0]
    user.setStoredCurrentUser(B); await settle(); assert.equal(reads.length, 2)
    const before = state(w), toastCount = notices.length
    if (failure === 'reject') old.reject(new Error('SYNTHETIC_OLD_READ_REJECTED')); else old.resolve({ status: 503, message: 'SYNTHETIC_OLD_READ_FAILED' })
    await settle(); assert.equal(state(w), before); assert.equal(notices.length, toastCount); assert.equal(w.vm.tabLoading, true)
    finishLatest(); await settle(); assert.equal(w.vm.dataSource[0].tokenValue, 'SYNTHETIC_TOKEN_B')
  })
}

test('SAC_PRINCIPAL A to B to A rejects original A request and original rendered toolbar capability', async () => {
  const w = await mountPage(A, false), oldRead = reads[0], oldSave = callback(buttonComponent(w, '保存全部配置'), 'onClick')
  user.setStoredCurrentUser(B); await settle(); finishLatest('B'); await settle()
  user.setStoredCurrentUser(A); await settle(); finishLatest('A_RETURNED'); await settle()
  const before = state(w), readCount = reads.length
  oldRead.resolve({ status: 200, data: rows('A_ORIGINAL') }); oldSave(); await settle()
  assert.equal(state(w), before); assert.equal(reads.length, readCount); assert.equal(writes.length, 0)
})

for (const transition of ['role', 'logout', 'unmount']) {
  test(`SAC_PRINCIPAL ${transition} synchronously retires dirty tokens, row bytes and owned loading without new private IO`, async () => {
    const w = await mountPage(A)
    await tokenInput(w, 'SYNTHETIC_RETIRED_SECRET')
    const input = nativeInput(w).element, oldRow = w.vm.dataSource[0], readCount = reads.length
    if (transition === 'role') user.setStoredCurrentUser({ ...A, role: 'user' })
    else if (transition === 'logout') user.clearStoredCurrentUser()
    else w.unmount()
    assert.equal(w.vm.dataSource.length, 0, 'PRIVATE_ROWS_CLEARED_SYNCHRONOUSLY')
    assert.equal(oldRow.tokenValue, '', 'RETIRED_ROW_SECRET_BYTES_SCRUBBED')
    assert.equal(input.value, '', 'RETIRED_NATIVE_SECRET_BYTES_SCRUBBED')
    assert.equal(w.vm.tabLoading, false); assert.equal(w.vm.savingAll, false)
    await settle(); assert.equal(reads.length, readCount); assert.equal(writes.length, 0)
  })
}

test('SAC_PRINCIPAL one save round snapshots payloads and rejects repeated direct/native submit', async () => {
  const w = await mountPage(A)
  await tokenInput(w, 'SYNTHETIC_ROUND_TOKEN')
  click(w, '保存全部配置'); w.vm.onSaveAll(); button(w, '保存全部配置').element.click(); await settle()
  assert.equal(writes.length, 1, 'ONE_ACTIVE_BATCH_ONLY')
  const expectedSecond = { tokenValue: 'SYNTHETIC_SECOND_A', enabled: false, remark: 'SYNTHETIC_SECOND_REMARK_A', flatPrice: 0.000001 }
  // Direct mutation here simulates a retained reference, not a second authorization channel.
  w.vm.dataSource[1].tokenValue = 'SYNTHETIC_MUTATED_DURING_SAVE'; w.vm.dataSource[1].flatPrice = 9
  writes[0].resolve({ status: 200 }); await settle()
  assert.equal(writes.length, 2); assert.deepEqual(writes[1].payload, expectedSecond, 'PAYLOADS_CAPTURED_ONCE_AT_BATCH_START')
  writes[1].resolve({ status: 200 }); await settle(); assert.equal(writes.length, 2)
})

for (const completion of ['success', 'reject', 'non200']) {
  test(`SAC_PRINCIPAL old A PUT ${completion} cannot send next item, toast, reload or clear a pending B save`, async () => {
    const w = await mountPage(A)
    click(w, '保存全部配置'); await settle(); const old = writes[0]
    user.setStoredCurrentUser(B); await settle(); finishLatest('B'); await settle()
    await tokenInput(w, 'SYNTHETIC_B_PENDING'); click(w, '保存全部配置'); await settle()
    assert.equal(writes.length, 2); const current = writes[1], before = state(w), readCount = reads.length, toastCount = notices.length
    if (completion === 'reject') old.reject(new Error('SYNTHETIC_OLD_WRITE_REJECTED'))
    else old.resolve({ status: completion === 'success' ? 200 : 503, message: 'SYNTHETIC_OLD_WRITE_COMPLETED' })
    await settle(); assert.equal(state(w), before); assert.equal(writes.length, 2); assert.equal(reads.length, readCount); assert.equal(notices.length, toastCount)
    assert.equal(w.vm.savingAll, true); current.resolve({ status: 503, message: 'SYNTHETIC_B_CURRENT_FAILURE' }); await settle()
    assert.equal(w.vm.savingAll, false); assert.equal(w.vm.dataSource[0].tokenValue, 'SYNTHETIC_B_PENDING')
  })
}

for (const failure of ['non200', 'reject']) {
  test(`SAC_PRINCIPAL current batch ${failure} preserves dirty draft and submitted first item is not described as rolled back`, async () => {
    const w = await mountPage(A)
    await tokenInput(w, `SYNTHETIC_FAILURE_DRAFT_${failure}`)
    click(w, '保存全部配置'); await settle(); writes[0].resolve({ status: 200 }); await settle(); assert.equal(writes.length, 2)
    if (failure === 'reject') writes[1].reject(new Error('SYNTHETIC_SECOND_WRITE_REJECTED')); else writes[1].resolve({ status: 503, message: 'SYNTHETIC_SECOND_WRITE_FAILED' })
    await settle(); assert.equal(w.vm.dataSource[0].tokenValue, `SYNTHETIC_FAILURE_DRAFT_${failure}`)
    assert.equal(w.vm.savingAll, false); assert.equal(writes.length, 2); assert.equal(reads.length, 1)
    assert.equal(notices.some(item => item.kind === 'success'), false)
    assert.ok(notices.some(item => item.kind === 'error')); assert.equal(notices.some(item => /已撤销|全部回滚|保存成功/.test(item.text)), false)
    click(w, '刷新'); await settle(); assert.match(confirms.at(-1)?.title || '', /未保存/, 'FAILED_BATCH_RETAINS_ACTUAL_UNSAVED_DRAFT_GUARD')
    confirms.at(-1).onCancel(); await settle()
    click(w, '保存全部配置'); await settle(); assert.equal(writes.length, 3, 'ONLY_EXPLICIT_RETRY_STARTS_NEW_BATCH')
  })
}

test('SAC_PRINCIPAL retained native inputs, row switch and toolbar/AI callbacks are inert across synchronous actor switch', async () => {
  const w = await mountPage(A)
  const input = nativeInput(w).element, remark = nativeInput(w, '备注').element
  const oldInputUpdate = callback(w.findComponent(Antd.InputPassword), 'onUpdate:value')
  const oldSwitch = callback(w.findComponent(Antd.Switch), 'onChange')
  const oldSave = callback(buttonComponent(w, '保存全部配置'), 'onClick'), oldRefresh = callback(buttonComponent(w, '刷新'), 'onClick')
  const oldAI = callback(buttonComponent(w, '前往 AI 搜索配置页'), 'onClick')
  const oldNativeSave = button(w, '保存全部配置').element
  user.setStoredCurrentUser(B)
  const before = state(w), readCount = reads.length
  input.value = 'SYNTHETIC_RETAINED_TOKEN'; input.dispatchEvent(new Event('input', { bubbles: true }))
  remark.value = 'SYNTHETIC_RETAINED_REMARK'; remark.dispatchEvent(new Event('input', { bubbles: true }))
  oldInputUpdate('SYNTHETIC_RETAINED_CALLBACK'); oldSwitch(false); oldSave(); oldRefresh(); oldAI(); oldNativeSave.click()
  assert.equal(state(w), before, 'OLD_SAME_TICK_ACTIONS_HAVE_NO_NEW_ACTOR_AUTHORITY')
  assert.equal(reads.length, readCount); assert.equal(writes.length, 0); assert.equal(routes.some(route => typeof route === 'string'), false)
  await settle(); finishLatest(); await settle()
  const Bstate = state(w); oldInputUpdate('SYNTHETIC_LATE_CALLBACK'); oldSwitch(false); oldSave(); oldRefresh(); oldAI(); await settle()
  assert.equal(state(w), Bstate); assert.equal(writes.length, 0)
})

test('SAC_PRINCIPAL legal Antd native input then immediate actor switch has no null reference or unhandled queued render', async () => {
  const w = await mountPage(A)
  const input = nativeInput(w).element
  input.value = 'SYNTHETIC_VALID_INPUT_THEN_SWITCH'; input.dispatchEvent(new Event('input', { bubbles: true }))
  assert.equal(w.vm.dataSource[0].tokenValue, 'SYNTHETIC_VALID_INPUT_THEN_SWITCH')
  user.setStoredCurrentUser(B)
  assert.equal(input.value, '', 'PRIVATE_DOM_IS_RETIRED_BEFORE_VUE_PATCH')
  await settle(); finishLatest(); await settle()
  // Drain actual Antd's asynchronous selection/input work; do not stub it or
  // install an unhandledRejection catcher that could mask a native failure.
  await new Promise(resolve => setTimeout(resolve, 25)); await settle()
  assert.equal(w.vm.dataSource[0].tokenValue, 'SYNTHETIC_TOKEN_B'); assert.deepEqual(nativeErrors, [])
})

for (const inputType of ['remark', 'price']) {
  test(`SAC_PRINCIPAL legal Antd native ${inputType} input immediately followed by actor switch drains real queued work`, async () => {
    const w = await mountPage(A)
    const input = nativeInput(w, inputType === 'remark' ? '备注' : '请输入固定单价').element
    input.value = inputType === 'remark' ? 'SYNTHETIC_LEGAL_REMARK' : '0.87654321'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    assert.equal(w.vm.dataSource[0][inputType === 'remark' ? 'remark' : 'flatPrice'], inputType === 'remark' ? 'SYNTHETIC_LEGAL_REMARK' : 0.87654321)
    user.setStoredCurrentUser(B)
    assert.equal(input.value, '')
    await settle(); finishLatest(); await settle(); await new Promise(resolve => setTimeout(resolve, 25)); await settle()
    assert.equal(w.vm.dataSource[0].tokenValue, 'SYNTHETIC_TOKEN_B'); assert.deepEqual(nativeErrors, [])
  })
}

// Direct synchronous w.unmount() after controlled Antd input is separately retained
// in native/sync-unmount-library-probe.cjs: baseline and bare Antd reproduce its
// unguarded vc-input nextTick. Product navigation below awaits the actual guard.
for (const routePath of ['/adapter/search', '/login']) for (const inputType of ['token', 'remark', 'price']) {
  test(`SAC_PRINCIPAL actual route guard ${routePath} ${inputType} input decision then unmount drains native work and retires secret bytes`, async () => {
    const w = await mountPage(A), input = nativeInput(w, inputType === 'token' ? '请输入默认 Token' : inputType === 'remark' ? '备注' : '请输入固定单价').element, row = w.vm.dataSource[0]
    input.value = inputType === 'price' ? '0.87654321' : 'SYNTHETIC_INPUT_BEFORE_ROUTE'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    assert.equal(row[inputType === 'token' ? 'tokenValue' : inputType === 'price' ? 'flatPrice' : 'remark'], inputType === 'price' ? 0.87654321 : 'SYNTHETIC_INPUT_BEFORE_ROUTE')
    const leave = routes.find(route => route && typeof route.leave === 'function')?.leave
    assert.equal(typeof leave, 'function')
    const decision = leave({ path: routePath })
    if (routePath !== '/login') {
      await settle(); const confirmation = confirms.at(-1)
      assert.match(confirmation?.title || '', /未保存/); confirmation.onOk()
    }
    assert.equal(await decision, true, 'ACTUAL_GUARD_AUTHORIZES_PRODUCT_NAVIGATION')
    w.unmount()
    assert.equal(input.value, ''); assert.equal(row.tokenValue, ''); assert.equal(w.vm.dataSource.length, 0)
    await settle(); await new Promise(resolve => setTimeout(resolve, 25)); await settle(); assert.deepEqual(nativeErrors, [])
    assert.equal(writes.length, 0)
  })
}

test('SAC_PRINCIPAL owned refresh confirm cancels/accepts dirty draft without global dialog destruction or focus theft', async () => {
  const w = await mountPage(A)
  await tokenInput(w, 'SYNTHETIC_REFRESH_DRAFT')
  const unrelated = Antd.Modal.confirm({ title: 'SYNTHETIC_UNRELATED_DIALOG', content: 'SYNTHETIC_EXTERNAL_OWNER' })
  click(w, '刷新'); await settle(); const first = confirms.at(-1)
  assert.match(first.title, /未保存/); assert.equal(first.focusTriggerAfterClose, false); assert.equal(reads.length, 1)
  first.onCancel(); await settle(); assert.equal(w.vm.dataSource[0].tokenValue, 'SYNTHETIC_REFRESH_DRAFT')
  click(w, '刷新'); await settle(); const second = confirms.at(-1)
  second.onOk(); await settle(); assert.equal(reads.length, 2); finishLatest('REFRESHED'); await settle()
  assert.equal(w.vm.dataSource[0].tokenValue, 'SYNTHETIC_TOKEN_REFRESHED')
  await tokenInput(w, 'SYNTHETIC_A_RETIRED_CONFIRM'); click(w, '刷新'); await settle(); const retired = confirms.at(-1)
  const foreignFocus = document.createElement('input'); document.body.appendChild(foreignFocus); foreignFocus.focus()
  user.setStoredCurrentUser(B); await settle(); finishLatest(); await settle(); const before = state(w), count = reads.length
  retired.onOk(); retired.onCancel(); await settle(); assert.equal(state(w), before); assert.equal(reads.length, count)
  assert.equal(document.body.textContent.includes('SYNTHETIC_UNRELATED_DIALOG'), true, 'NO_GLOBAL_MODAL_DESTROY')
  assert.equal(document.activeElement, foreignFocus, 'NO_FOCUS_RETURN_TO_OLD_ACTOR_CONTROL'); unrelated.destroy()
})

test('SAC_PRINCIPAL real unsaved-draft route guard preserves cancel and retires only its owned pending prompt', async () => {
  const w = await mountPage(A)
  await tokenInput(w, 'SYNTHETIC_LEAVE_DRAFT')
  const leave = routes.find(route => route && typeof route.leave === 'function')?.leave
  assert.equal(typeof leave, 'function', 'ACTUAL_ON_BEFORE_ROUTE_LEAVE_REGISTERED')
  const first = leave({ path: '/adapter/search' }); await settle(); const firstConfirm = confirms.at(-1)
  assert.match(firstConfirm.title, /未保存/); assert.equal(firstConfirm.focusTriggerAfterClose, false)
  firstConfirm.onCancel(); assert.equal(await first, false); assert.equal(w.vm.dataSource[0].tokenValue, 'SYNTHETIC_LEAVE_DRAFT')
  const second = leave({ path: '/adapter/search' }); await settle(); const secondConfirm = confirms.at(-1)
  secondConfirm.onOk(); assert.equal(await second, true)
  const pending = leave({ path: '/adapter/search' }); await settle()
  user.setStoredCurrentUser(B); await settle(); assert.equal(await pending, false, 'RETIRED_PROMPT_CANNOT_AUTHORIZE_OLD_NAVIGATION')
  finishLatest(); await settle()
})

test('SAC_PRINCIPAL unmounted A GET/PUT cannot mutate remounted B or announce rollback of already-sent work', async () => {
  const old = await mountPage(A)
  click(old, '保存全部配置'); await settle(); const oldWrite = writes[0]
  old.unmount(); const w = await mountPage(B), before = state(w), count = reads.length, toastCount = notices.length
  oldWrite.resolve({ status: 200 }); await settle()
  assert.equal(state(w), before); assert.equal(reads.length, count); assert.equal(writes.length, 1); assert.equal(notices.length, toastCount)
  console.log('SAC_PRINCIPAL_WITNESS actualVue=true actualAntd=true actualCurrentUser=true actualScope=true actualUnsavedDraft=true syntheticTransportOnly=true providerCalls=0 databaseCalls=0 paymentProviderCalls=0')
})
