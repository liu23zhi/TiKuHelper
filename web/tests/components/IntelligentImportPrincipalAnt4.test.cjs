/* Native import contract: actual Vue/Antd/currentUser/unsaved draft/type helper.
 * Only API/router/upload transport is synthetic. No request may escape this file.
 * The same fixture runs against complete BASELINE/MODIFIED/ROLLBACK src copies.
 */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(root, 'node_modules'), path.resolve(__dirname, '../../node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(root, 'tests/helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const { getQuestionTypeByName } = require(path.join(root, 'src/utils/uitls.js'))
const { toUserRequestError } = require(path.join(root, 'src/utils/requestRetry.mjs'))
const A = { id: 7301, role: 'admin', username: 'SYNTHETIC_IMPORT_A' }
const B = { id: 7302, role: 'admin', username: 'SYNTHETIC_IMPORT_B' }
const calls = { parse: [], reparse: [], create: [] }, notices = [], confirms = [], routes = [], nativeErrors = [], network = []
const wrappers = []
let fileSequence = 0
function transport(name, payload) {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  const call = { payload: JSON.parse(JSON.stringify(payload)), actor: user.getStoredCurrentUser()?.id, promise, completed: false }
  call.resolve = value => { call.completed = true; resolve(value) }
  call.reject = error => { call.completed = true; reject(error) }
  calls[name].push(call)
  return promise
}
function mockModule(filename, exports) { const mock = new Module(filename); mock.loaded = true; mock.exports = exports; require.cache[filename] = mock }
mockModule(path.join(root, 'src/api/api.js'), { parseFile: payload => transport('parse', payload), reParseFile: payload => transport('reparse', payload) })
mockModule(path.join(root, 'src/api/TikuComponentApi.js'), { createQuestions: payload => transport('create', payload) })
mockModule(require.resolve('vue-router'), { useRouter: () => ({ push: value => routes.push(value) }), onBeforeRouteLeave: leave => routes.push({ leave }) })
for (const kind of ['success', 'error', 'warning']) {
  const native = Antd.message[kind].bind(Antd.message)
  Antd.message[kind] = (...args) => { notices.push({ kind, text: String(args[0]?.content || args[0] || '') }); return native(...args) }
}
const nativeConfirm = Antd.Modal.confirm.bind(Antd.Modal)
Antd.Modal.confirm = options => { confirms.push(options); return nativeConfirm(options) }
function refuseNetwork(kind, args) { network.push({ kind, args: args.map(String) }); throw new Error(`FORBIDDEN_REAL_NETWORK_${kind}`) }
window.XMLHttpRequest.prototype.open = function (...args) { return refuseNetwork('XHR', args) }
globalThis.fetch = window.fetch = (...args) => refuseNetwork('FETCH', args)
for (const moduleName of ['node:http', 'node:https']) {
  const native = require(moduleName)
  native.request = (...args) => refuseNetwork(moduleName, args)
  native.get = (...args) => refuseNetwork(moduleName, args)
}
const Page = require(path.join(root, 'src/pages/IntelligentImport/index.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
function paper(tag = 'A') { return [{ content: `SYNTHETIC_IMPORT_PRIVATE_${tag}`, typeAlias: '单选题', options: [{ name: 'A', value: `SYNTHETIC_OPTION_${tag}` }, { name: 'B', value: 'SYNTHETIC_OTHER_OPTION' }], answer: ['A'] }] }
function allTypes() { return ['单选题', '多选题', '填空题', '判断题', '简答题', '大作业', '阅读理解'].map((typeAlias, index) => ({ content: `SYNTHETIC_TYPE_${index}`, typeAlias, options: [{ name: 'A', value: `SYNTHETIC_FIRST_${index}` }, { name: 'B', value: `SYNTHETIC_SECOND_${index}` }], answer: ['B', 'A', 'B'] })) }
async function mountPage(actor = A) {
  if (actor) user.setStoredCurrentUser(actor); else user.clearStoredCurrentUser()
  const w = mount(Page, { attachTo: document.body, global: { plugins: [Antd], config: { errorHandler: error => nativeErrors.push(error) } } }); wrappers.push(w)
  await settle()
  return w
}
function callback(component, name) { const fn = component.vm.$.vnode.props?.[name]; assert.equal(typeof fn, 'function', `NATIVE_CALLBACK_${name}`); return fn }
function button(w, label) { return w.findAll('button').find(item => item.text().replace(/\s/g, '') === label.replace(/\s/g, '')) }
function click(w, label) { const found = button(w, label); assert.ok(found, `NATIVE_BUTTON_${label}`); found.element.click() }
function buttonCallback(w, label) { const component = w.findAllComponents(Antd.Button).find(item => item.text().replace(/\s/g, '') === label.replace(/\s/g, '')); assert.ok(component); return callback(component, 'onClick') }
function uploadCallbacks(w) { const component = w.findComponent(Antd.Upload); assert.equal(component.exists(), true, 'ACTUAL_ANTD_UPLOAD_MOUNTED'); return { before: callback(component, 'beforeUpload'), change: callback(component, 'onChange') } }
function file(tag = 'A') { return { uid: `SYNTHETIC_UID_${++fileSequence}`, name: `SYNTHETIC_${tag}.docx`, type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', size: 100 } }
async function beginUpload(w, tag = 'A') { const native = uploadCallbacks(w), item = file(tag); const accepted = await native.before(item, [item]); await settle(); return { ...native, item, accepted } }
async function doneUpload(w, tag = 'A', rows = paper(tag)) {
  const native = await beginUpload(w, tag)
  native.item.status = 'done'; native.item.response = { crc: `SYNTHETIC_CRC_${tag}` }
  native.change({ file: native.item, fileList: [native.item] }); await settle()
  const call = calls.parse.at(-1); assert.ok(call, 'UPLOAD_DONE_STARTS_PARSE'); assert.deepEqual(call.payload, { file: `SYNTHETIC_CRC_${tag}` })
  call.resolve({ paper: rows }); await settle(); return native
}
function state(w) { return JSON.stringify({ id: w.vm.UploadId, rows: w.vm.showProblemArr, data: w.vm.data, groups: w.vm.problemArr, type: w.vm.type, loading: w.vm.loading, submit: w.vm.submitLoading }) }
function edit(w, text) { const native = w.get('.compileContent [contenteditable]').element; native.innerHTML = `<p class="lines">${text}</p>`; native.dispatchEvent(new Event('input', { bubbles: true })); return native }
afterEach(async () => {
  for (const w of wrappers) if (w.exists()) w.unmount()
  wrappers.length = 0
  for (const [name, collection] of Object.entries(calls)) for (const call of collection) if (!call.completed) call.resolve(name === 'create' ? { status: 503, message: 'SYNTHETIC_CLEANUP' } : { paper: [] })
  await settle(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); user.clearStoredCurrentUser(); document.body.replaceChildren(); window.sessionStorage.clear()
  for (const collection of Object.values(calls)) collection.length = 0
  notices.length = 0; confirms.length = 0; routes.length = 0
  assert.deepEqual(network.splice(0), [], 'NO_REAL_SQP_HTTP_PROVIDER_OR_DATABASE_IO')
  assert.deepEqual(nativeErrors.splice(0), [], 'NO_NATIVE_VUE_OR_ANTD_RUNTIME_ERRORS_HIDDEN')
})

test('SMART_IMPORT existing native Upload beforeUpload and done parse retain exact file CRC and paper', async () => {
  const w = await mountPage()
  const native = await doneUpload(w)
  assert.notEqual(native.accepted, false); assert.equal(w.vm.loading, false)
  assert.equal(w.vm.UploadId, 'SYNTHETIC_CRC_A'); assert.deepEqual(JSON.parse(JSON.stringify(w.vm.data)), paper())
  assert.ok(w.text().includes('SYNTHETIC_IMPORT_PRIVATE_A')); assert.equal(w.findAll('[contenteditable]').length, 1)
})

test('SMART_IMPORT existing native groups filters and paired scroll retain page behavior', async () => {
  const w = await mountPage(); await doneUpload(w, 'A', allTypes())
  assert.equal(w.findAll('.tabsBtn').length, 8); assert.equal(w.vm.type, '全部')
  const tab = w.findAll('.tabsBtn').find(item => item.text().includes('多选题')); tab.element.click(); await settle(); assert.equal(w.vm.type, '多选题')
  const compile = w.get('.compileContent').element, preview = w.get('.previewContent').element
  compile.scrollTop = 71; compile.dispatchEvent(new Event('scroll')); assert.equal(preview.scrollTop, 71)
  preview.scrollTop = 29; preview.dispatchEvent(new Event('scroll')); assert.equal(compile.scrollTop, 29)
})

test('SMART_IMPORT existing native contenteditable correction and reparse keep local HTML order', async () => {
  const w = await mountPage(); await doneUpload(w)
  edit(w, 'SYNTHETIC_CORRECTED_QUESTION'); click(w, '重新识别'); await settle()
  assert.equal(calls.reparse.length, 1); assert.deepEqual(calls.reparse[0].payload, { html: '<p>SYNTHETIC_CORRECTED_QUESTION</p>' })
  calls.reparse[0].resolve({ paper: paper('CORRECTED') }); await settle()
  assert.equal(w.vm.loading, false); assert.equal(w.vm.data[0].content, 'SYNTHETIC_IMPORT_PRIVATE_CORRECTED')
})

test('SMART_IMPORT existing exact create payload retains 0 1 2 3 4 -4 15 and answer option order duplicates', async () => {
  const w = await mountPage(); await doneUpload(w, 'A', allTypes()); click(w, '确认导入'); await settle()
  assert.equal(calls.create.length, 1)
  const expected = allTypes().map((row, index) => ({ question: row.content, type: [0, 1, 2, 3, 4, -4, 15][index], options: JSON.stringify(row.options.map(item => item.value)), answer: JSON.stringify([`SYNTHETIC_SECOND_${index}`, `SYNTHETIC_FIRST_${index}`, `SYNTHETIC_SECOND_${index}`]) }))
  assert.deepEqual(calls.create[0].payload, expected); assert.deepEqual(allTypes().map(row => getQuestionTypeByName(row.typeAlias)), [0, 1, 2, 3, 4, -4, 15])
  calls.create[0].resolve({ status: 200, message: '成功创建7条数据' }); await settle()
  assert.equal(w.vm.submitLoading, false); assert.ok(notices.some(item => item.kind === 'success'))
})

test('SMART_IMPORT native done reads current file response not a previous fileList entry and deduplicates completion', async () => {
  const w = await mountPage(), native = await beginUpload(w)
  const previous = { ...file('PREVIOUS'), status: 'done', response: { crc: 'SYNTHETIC_WRONG_PREVIOUS_CRC' } }
  native.item.status = 'done'; native.item.response = { crc: 'SYNTHETIC_CURRENT_CRC' }
  native.change({ file: native.item, fileList: [previous, native.item] }); await settle()
  assert.equal(calls.parse.length, 1); assert.deepEqual(calls.parse[0].payload, { file: 'SYNTHETIC_CURRENT_CRC' })
  native.change({ file: native.item, fileList: [previous, native.item] }); await settle()
  assert.equal(calls.parse.length, 1, 'ONE_PARSE_PER_ACCEPTED_FILE_COMPLETION')
  calls.parse[0].resolve({ paper: paper('CURRENT') }); await settle(); assert.equal(w.vm.data[0].content, 'SYNTHETIC_IMPORT_PRIVATE_CURRENT')
})

test('SMART_IMPORT anonymous nonadmin and invalid positive32bit IDs have zero private IO through native and direct actions', async () => {
  const invalid = [undefined, null, 0, '', ' ', -1, 1.5, '1.5', '1e3', true, 2147483648, Number.MAX_SAFE_INTEGER + 1]
  for (const actor of [null, { ...A, role: 'user' }, ...invalid.map(id => ({ ...A, id }))]) {
    const w = await mountPage(actor), native = uploadCallbacks(w), item = file('UNAUTHORIZED')
    assert.equal(await native.before(item, [item]), false, 'UNAUTHORIZED_BEFORE_UPLOAD_MUST_REFUSE_TRANSPORT')
    item.status = 'done'; item.response = { crc: 'SYNTHETIC_UNAUTHORIZED_CRC' }
    native.change({ file: item, fileList: [item] })
    Promise.resolve(w.vm.getParseFile()).catch(() => {}); Promise.resolve(w.vm.reParseFile('<p>SYNTHETIC_FORBIDDEN</p>')).catch(() => {})
    w.vm.handleInput(); w.vm.upload(); w.vm.navigateToHome(); button(w, '确认导入')?.element.click(); await settle()
    assert.equal(calls.parse.length + calls.reparse.length + calls.create.length, 0, 'UNAUTHORIZED_ZERO_PRIVATE_IO')
    assert.equal(w.vm.data.length, 0); assert.equal(routes.some(route => typeof route === 'string'), false)
    w.unmount()
  }
  const w = await mountPage({ ...A, id: '7301' }); await doneUpload(w, 'DECIMAL_ID'); assert.equal(calls.parse.length, 1, 'DECIMAL_STRING_ID_ACCEPTED')
})

test('SMART_IMPORT late A parse and its finally cannot populate B or release a pending B upload', async () => {
  const w = await mountPage(), first = await beginUpload(w, 'A')
  first.item.status = 'done'; first.item.response = { crc: 'SYNTHETIC_CRC_A' }; first.change({ file: first.item, fileList: [first.item] }); await settle()
  const old = calls.parse[0]; user.setStoredCurrentUser(B); await settle()
  const current = await beginUpload(w, 'B'), before = state(w), count = notices.length
  old.resolve({ paper: paper('A_RETIRED') }); await settle()
  assert.equal(state(w), before, 'OLD_PARSE_FINALLY_CANNOT_CHANGE_B'); assert.equal(w.vm.loading, true); assert.equal(notices.length, count)
  current.item.status = 'done'; current.item.response = { crc: 'SYNTHETIC_CRC_B' }; current.change({ file: current.item, fileList: [current.item] }); await settle()
  assert.equal(calls.parse.length, 2); calls.parse[1].resolve({ paper: paper('B') }); await settle(); assert.equal(w.vm.data[0].content, 'SYNTHETIC_IMPORT_PRIVATE_B')
})

test('SMART_IMPORT late A reparse cannot populate B or release B reparse loading', async () => {
  const w = await mountPage(); await doneUpload(w)
  edit(w, 'SYNTHETIC_A_REPARSE'); click(w, '重新识别'); await settle(); const old = calls.reparse[0]
  user.setStoredCurrentUser(B); await settle(); await doneUpload(w, 'B')
  edit(w, 'SYNTHETIC_B_REPARSE'); click(w, '重新识别'); await settle(); const current = calls.reparse[1], before = state(w), count = notices.length
  old.resolve({ paper: paper('A_RETIRED') }); await settle()
  assert.equal(state(w), before); assert.equal(w.vm.loading, true); assert.equal(notices.length, count); assert.equal(calls.create.length, 0)
  current.resolve({ paper: paper('B_CORRECTED') }); await settle(); assert.equal(w.vm.data[0].content, 'SYNTHETIC_IMPORT_PRIVATE_B_CORRECTED')
})

test('SMART_IMPORT A to B to A retires original parse Upload and toolbar capabilities', async () => {
  const w = await mountPage(), native = await beginUpload(w, 'ORIGINAL_A'), oldSubmit = buttonCallback(w, '确认导入'), oldReparse = buttonCallback(w, '重新识别')
  native.item.status = 'done'; native.item.response = { crc: 'SYNTHETIC_ORIGINAL_A_CRC' }; native.change({ file: native.item, fileList: [native.item] }); await settle(); const old = calls.parse[0]
  user.setStoredCurrentUser(B); await settle(); user.setStoredCurrentUser(A); await settle(); await doneUpload(w, 'RETURNED_A')
  const before = state(w), parseCount = calls.parse.length, count = notices.length
  old.resolve({ paper: paper('ORIGINAL_A') }); native.change({ file: native.item, fileList: [native.item] }); oldSubmit(); oldReparse(); await settle()
  assert.equal(state(w), before); assert.equal(calls.parse.length, parseCount); assert.equal(calls.reparse.length, 0); assert.equal(calls.create.length, 0); assert.equal(notices.length, count)
  assert.equal(await native.before(file('RETIRED'), []), false, 'ORIGINAL_RENDER_CANNOT_AUTHORIZE_RETURNED_A_UPLOAD')
})

test('SMART_IMPORT current same-actor latest parse wins and older finally cannot release current loading', async () => {
  const w = await mountPage(); await doneUpload(w)
  const oldPromise = Promise.resolve(w.vm.getParseFile()).catch(() => {}); await settle(); const old = calls.parse.at(-1)
  const currentPromise = Promise.resolve(w.vm.getParseFile()).catch(() => {}); await settle(); const current = calls.parse.at(-1)
  assert.notEqual(current, old, 'TWO_LEGAL_CURRENT_READS_START_TWO_TRANSPORTS')
  old.resolve({ paper: paper('OLDER') }); await oldPromise; await settle(); assert.equal(w.vm.loading, true); assert.equal(w.vm.data[0].content, 'SYNTHETIC_IMPORT_PRIVATE_A')
  current.resolve({ paper: paper('NEWEST') }); await currentPromise; await settle(); assert.equal(w.vm.data[0].content, 'SYNTHETIC_IMPORT_PRIVATE_NEWEST'); assert.equal(w.vm.loading, false)
})

test('SMART_IMPORT same-actor native edit during parse is a newer draft and cannot be overwritten', async () => {
  const w = await mountPage(); await doneUpload(w)
  const completion = Promise.resolve(w.vm.getParseFile()).catch(() => {}); await settle(); const pending = calls.parse.at(-1)
  edit(w, 'SYNTHETIC_TYPED_DURING_PARSE'); click(w, '确认导入'); w.vm.upload(); await settle(); assert.equal(calls.create.length, 0)
  pending.resolve({ paper: paper('SERVER_AFTER_EDIT') }); await completion; await settle()
  assert.ok(w.get('.compileContent').text().includes('SYNTHETIC_TYPED_DURING_PARSE')); assert.equal(w.text().includes('SYNTHETIC_IMPORT_PRIVATE_SERVER_AFTER_EDIT'), false)
  assert.equal(w.vm.loading, false); click(w, '重新识别'); await settle(); assert.equal(calls.reparse.length, 1)
  calls.reparse[0].resolve({ paper: paper('EDIT_ACCEPTED') }); await settle(); click(w, '确认导入'); await settle(); assert.equal(calls.create.length, 1)
})

test('SMART_IMPORT unrecognized native DOM edits block old parsed data until explicit reparse', async () => {
  const w = await mountPage(); await doneUpload(w); edit(w, 'SYNTHETIC_NOT_RECOGNIZED')
  click(w, '确认导入'); w.vm.upload(); await settle(); assert.equal(calls.create.length, 0, 'OLD_PARSED_DATA_CANNOT_IMPORT_UNRECOGNIZED_EDITOR')
  click(w, '重新识别'); await settle(); assert.equal(calls.reparse.length, 1); calls.reparse[0].resolve({ paper: paper('RECOGNIZED') }); await settle()
  click(w, '确认导入'); await settle(); assert.equal(calls.create.length, 1); assert.equal(calls.create[0].payload[0].question, 'SYNTHETIC_IMPORT_PRIVATE_RECOGNIZED')
})

test('SMART_IMPORT real native editor input immediately followed by actor switch scrubs bytes before queued render', async () => {
  const w = await mountPage(); await doneUpload(w)
  const editor = edit(w, 'SYNTHETIC_A_NATIVE_PRIVATE_INPUT'), oldScroll = w.get('.compileContent').element, oldPreview = w.get('.previewContent').element
  const oldSubmit = buttonCallback(w, '确认导入'), oldReparse = buttonCallback(w, '重新识别'), oldClear = buttonCallback(w, '清空题目'), oldNativeSubmit = button(w, '确认导入').element, oldUpload = uploadCallbacks(w)
  user.setStoredCurrentUser(B)
  assert.equal(editor.textContent, '', 'PRIVATE_EDITOR_BYTES_SCRUBBED_SYNCHRONOUSLY'); const before = state(w)
  editor.innerHTML = '<p class="lines">SYNTHETIC_RETAINED_PRIVATE_INPUT</p>'; editor.dispatchEvent(new Event('input', { bubbles: true }))
  oldScroll.scrollTop = 97; oldScroll.dispatchEvent(new Event('scroll')); oldSubmit(); oldReparse(); oldClear(); oldNativeSubmit.click()
  const ghost = { ...file('GHOST'), status: 'done', response: { crc: 'SYNTHETIC_GHOST_CRC' } }; oldUpload.change({ file: ghost, fileList: [ghost] })
  assert.equal(state(w), before); assert.equal(calls.create.length, 0); assert.equal(calls.reparse.length, 0); assert.equal(calls.parse.length, 1)
  await settle(); await doneUpload(w, 'B'); const current = state(w), preview = w.get('.previewContent').element; preview.scrollTop = 12
  oldScroll.scrollTop = 122; oldScroll.dispatchEvent(new Event('scroll')); oldPreview.scrollTop = 134; oldPreview.dispatchEvent(new Event('scroll')); oldSubmit(); oldReparse(); oldClear(); await settle()
  assert.equal(state(w), current); assert.equal(preview.scrollTop, 12); assert.equal(calls.create.length, 0); assert.equal(calls.reparse.length, 0)
  await new Promise(resolve => setTimeout(resolve, 25)); await settle(); assert.deepEqual(nativeErrors, [])
})

test('SMART_IMPORT multiple native mounts isolate question refs editor query scope and paired scroll', async () => {
  const first = await mountPage(); await doneUpload(first, 'FIRST')
  const second = await mountPage(); await doneUpload(second, 'SECOND')
  assert.equal(first.vm.data[0].content, 'SYNTHETIC_IMPORT_PRIVATE_FIRST'); assert.equal(second.vm.data[0].content, 'SYNTHETIC_IMPORT_PRIVATE_SECOND')
  const target = first.get('.compileContent').element, other = second.get('.previewContent').element; other.scrollTop = 17; target.scrollTop = 88; target.dispatchEvent(new Event('scroll')); assert.equal(other.scrollTop, 17)
  edit(first, 'SYNTHETIC_ONLY_FIRST_EDITOR'); edit(second, 'SYNTHETIC_ONLY_SECOND_EDITOR'); click(first, '重新识别'); await settle()
  assert.deepEqual(calls.reparse[0].payload, { html: '<p>SYNTHETIC_ONLY_FIRST_EDITOR</p>' }); assert.equal(calls.reparse[0].payload.html.includes('SECOND'), false)
  calls.reparse[0].resolve({ paper: paper('FIRST_CORRECTED') }); await settle(); assert.equal(second.vm.data[0].content, 'SYNTHETIC_IMPORT_PRIVATE_SECOND')
  first.unmount(); assert.equal(second.vm.data[0].content, 'SYNTHETIC_IMPORT_PRIVATE_SECOND')
})

test('SMART_IMPORT unmounted in-flight A upload parse and create cannot mutate remounted B', async () => {
  const old = await mountPage(); await doneUpload(old, 'A'); click(old, '确认导入'); await settle(); const pendingCreate = calls.create[0]
  const oldNative = uploadCallbacks(old); old.unmount(); const w = await mountPage(B); await doneUpload(w, 'B'); const before = state(w), count = notices.length, parses = calls.parse.length
  pendingCreate.resolve({ status: 200, message: 'SYNTHETIC_RETIRED_SUCCESS' }); const ghost = { ...file('UNMOUNTED'), status: 'done', response: { crc: 'SYNTHETIC_UNMOUNTED_CRC' } }; oldNative.change({ file: ghost, fileList: [ghost] }); await settle()
  assert.equal(state(w), before); assert.equal(notices.length, count); assert.equal(calls.create.length, 1); assert.equal(calls.parse.length, parses)
})

for (const completion of ['success', 'non200', 'reject']) {
  test(`SMART_IMPORT retired A create ${completion} has no B toast followup parse write or finally mutation`, async () => {
    const w = await mountPage(); await doneUpload(w); click(w, '确认导入'); await settle(); const old = calls.create[0]
    user.setStoredCurrentUser(B); await settle(); await doneUpload(w, 'B'); click(w, '确认导入'); await settle()
    assert.equal(calls.create.length, 2); const current = calls.create[1], before = state(w), count = notices.length, parses = calls.parse.length
    if (completion === 'reject') old.reject(new Error('SYNTHETIC_RETIRED_CREATE_REJECTED')); else old.resolve({ status: completion === 'success' ? 200 : 503, message: 'SYNTHETIC_RETIRED_CREATE_COMPLETED' })
    await settle(); assert.equal(state(w), before); assert.equal(w.vm.submitLoading, true); assert.equal(notices.length, count); assert.equal(calls.parse.length, parses); assert.equal(calls.create.length, 2)
    current.resolve({ status: 503, message: 'SYNTHETIC_B_REJECTED' }); await settle(); assert.equal(w.vm.submitLoading, false); assert.equal(w.vm.data[0].content, 'SYNTHETIC_IMPORT_PRIVATE_B')
  })
}

for (const failure of ['error', 'missing-response', 'malformed-crc']) {
  test(`SMART_IMPORT native upload ${failure} releases only owned busy and never parses`, async () => {
    const w = await mountPage(), native = await beginUpload(w)
    assert.equal(w.vm.loading, true); native.item.status = failure === 'error' ? 'error' : 'done'
    if (failure === 'malformed-crc') native.item.response = { crc: { untrusted: true } }
    assert.doesNotThrow(() => native.change({ file: native.item, fileList: [native.item] }))
    await settle(); assert.equal(calls.parse.length, 0); assert.equal(w.vm.loading, false); assert.equal(w.vm.UploadId, ''); assert.ok(notices.some(item => item.kind === 'error'))
  })
}

test('SMART_IMPORT refused file and wrong-file callbacks cannot parse or release another accepted upload', async () => {
  const w = await mountPage(), native = uploadCallbacks(w), refused = { ...file('REFUSED'), name: 'SYNTHETIC_REFUSED.txt', type: 'text/plain' }
  assert.equal(await native.before(refused, [refused]), false); refused.status = 'done'; refused.response = { crc: 'SYNTHETIC_REFUSED_CRC' }; native.change({ file: refused, fileList: [refused] }); await settle()
  assert.equal(calls.parse.length, 0); assert.equal(w.vm.loading, false)
  const current = await beginUpload(w, 'CURRENT'), wrong = { ...file('WRONG'), status: 'error' }, before = state(w), count = notices.length
  current.change({ file: wrong, fileList: [wrong] }); await settle(); assert.equal(state(w), before); assert.equal(w.vm.loading, true); assert.equal(notices.length, count); assert.equal(calls.parse.length, 0)
  current.item.status = 'error'; current.change({ file: current.item, fileList: [current.item] }); await settle(); assert.equal(w.vm.loading, false)
})

for (const response of ['empty', 'missing-paper', 'nonarray', 'reject']) {
  test(`SMART_IMPORT parse ${response} does not fake success or import defaults and releases own busy`, async () => {
    const w = await mountPage(); await doneUpload(w)
    const completion = Promise.resolve(w.vm.getParseFile()).catch(() => {}); await settle(); const call = calls.parse.at(-1)
    if (response === 'reject') call.reject(new Error('SYNTHETIC_PARSE_REJECTED')); else call.resolve(response === 'empty' ? { paper: [] } : response === 'missing-paper' ? {} : { paper: { not: 'an array' } })
    await completion; await settle(); assert.equal(w.vm.loading, false)
    if (response !== 'empty') assert.ok(notices.some(item => item.kind === 'error'), 'PARSE_FAILURE_ANNOUNCED_AS_ERROR')
    const before = calls.create.length; click(w, '确认导入'); w.vm.upload(); await settle(); assert.equal(calls.create.length, before, 'INVALID_OR_EMPTY_PARSE_CANNOT_IMPORT_PRIOR_RESULT')
  })
}

for (const failure of ['non200', 'reject']) {
  test(`SMART_IMPORT create ${failure} preserves recognized draft and needs explicit retry without false success`, async () => {
    const w = await mountPage(); await doneUpload(w); click(w, '确认导入'); await settle(); const before = JSON.stringify(w.vm.data)
    if (failure === 'reject') calls.create[0].reject(new Error('SYNTHETIC_CREATE_REJECTED')); else calls.create[0].resolve({ status: 503, message: 'SYNTHETIC_CREATE_FAILED' })
    await settle(); assert.equal(w.vm.submitLoading, false); assert.equal(JSON.stringify(w.vm.data), before); assert.equal(calls.create.length, 1); assert.equal(calls.parse.length, 1)
    assert.equal(notices.some(item => item.kind === 'success' && /SYNTHETIC_CREATE_/.test(item.text)), false); assert.ok(notices.some(item => item.kind === 'error'))
    click(w, '确认导入'); await settle(); assert.equal(calls.create.length, 2, 'ONLY_EXPLICIT_RETRY_STARTS_ANOTHER_CREATE')
  })
}

test('SMART_IMPORT one create snapshots exact payload and blocks repeated native direct and success same-snapshot submits', async () => {
  const w = await mountPage(); await doneUpload(w, 'A', allTypes()); click(w, '确认导入'); w.vm.upload(); button(w, '确认导入').element.click(); await settle()
  assert.equal(calls.create.length, 1); const snapshot = JSON.parse(JSON.stringify(calls.create[0].payload))
  w.vm.data[0].options[0].value = 'SYNTHETIC_MUTATED_AFTER_SUBMIT'; w.vm.data[0].answer.push('B')
  assert.deepEqual(calls.create[0].payload, snapshot, 'ONE_IMMUTABLE_REQUEST_SNAPSHOT')
  calls.create[0].resolve({ status: 200, message: 'SYNTHETIC_CREATED_ONCE' }); await settle()
  // Restoring the exact submitted parsed snapshot cannot create a second import.
  w.vm.data = allTypes(); click(w, '确认导入'); w.vm.upload(); await settle(); assert.equal(calls.create.length, 1, 'SUCCESSFUL_SAME_SNAPSHOT_NOT_IMPORTED_TWICE')
})

test('SMART_IMPORT owner revocation synchronously clears native content draft and every busy flag with zero subsequent IO', async () => {
  const w = await mountPage(); await doneUpload(w); const editor = edit(w, 'SYNTHETIC_REVOKED_CONTENT'); const oldSubmit = buttonCallback(w, '确认导入'), oldNative = uploadCallbacks(w), count = calls.parse.length
  user.setStoredCurrentUser({ ...A, role: 'user' })
  assert.equal(editor.textContent, ''); assert.equal(w.vm.data.length, 0); assert.equal(w.vm.showProblemArr.length, 0); assert.equal(w.vm.loading, false); assert.equal(w.vm.submitLoading, false); assert.equal(w.vm.UploadId, '')
  oldSubmit(); oldNative.change({ file: { ...file('REVOKED'), status: 'done', response: { crc: 'SYNTHETIC_REVOKED_CRC' } }, fileList: [] }); w.vm.upload(); await settle()
  assert.equal(calls.parse.length, count); assert.equal(calls.create.length, 0); assert.equal(calls.reparse.length, 0)
})

test('SMART_IMPORT balance-only profile update preserves actual unsaved DOM draft and its route guard', async () => {
  const w = await mountPage(); await doneUpload(w); edit(w, 'SYNTHETIC_BALANCE_STABLE_DRAFT'); const before = state(w), count = calls.parse.length
  user.setStoredCurrentUser({ ...A, balance: 987.123456 }); await settle(); assert.equal(state(w), before); assert.equal(calls.parse.length, count); assert.ok(w.get('.compileContent').text().includes('SYNTHETIC_BALANCE_STABLE_DRAFT'))
  const leave = routes.find(route => route && typeof route.leave === 'function')?.leave; assert.equal(typeof leave, 'function')
  const decision = leave({ path: '/adapter/search' }); await settle(); const prompt = confirms.at(-1); assert.match(prompt?.title || '', /未保存/); prompt.onCancel(); assert.equal(await decision, false)
})

test('SMART_IMPORT owned clear and reupload prompts preserve cancel and do not destroy unrelated dialog or steal focus', async () => {
  const w = await mountPage(); await doneUpload(w); edit(w, 'SYNTHETIC_CANCEL_CLEAR_DRAFT')
  const unrelated = Antd.Modal.confirm({ title: 'SYNTHETIC_UNRELATED_IMPORT_DIALOG', content: 'SYNTHETIC_OTHER_OWNER' })
  click(w, '清空题目'); await settle(); const first = confirms.at(-1)
  assert.match(first?.title || '', /未保存|清空|放弃/); assert.equal(first.focusTriggerAfterClose, false); first.onCancel(); await settle(); assert.ok(w.get('.compileContent').text().includes('SYNTHETIC_CANCEL_CLEAR_DRAFT'))
  const native = uploadCallbacks(w), item = file('REUPLOAD'); const accepted = Promise.resolve(native.before(item, [item])); await settle(); const second = confirms.at(-1)
  assert.notEqual(second, first); assert.equal(second.focusTriggerAfterClose, false); second.onCancel(); assert.equal(await accepted, false); assert.equal(w.vm.loading, false); assert.equal(calls.parse.length, 1)
  click(w, '清空题目'); await settle(); const retired = confirms.at(-1), focus = document.createElement('input'); document.body.appendChild(focus); focus.focus()
  user.setStoredCurrentUser(B); await settle(); const before = state(w); retired.onOk(); retired.onCancel(); await settle(); assert.equal(state(w), before)
  assert.equal(document.body.textContent.includes('SYNTHETIC_UNRELATED_IMPORT_DIALOG'), true, 'NO_GLOBAL_MODAL_DESTRUCTION'); assert.equal(document.activeElement, focus, 'NO_FOCUS_RETURN_TO_RETIRED_TRIGGER'); unrelated.destroy()
})

test('SMART_IMPORT actual unsaved route guard cancellation acceptance and owner retirement remain scoped', async () => {
  const w = await mountPage(); await doneUpload(w); edit(w, 'SYNTHETIC_LEAVE_DRAFT')
  const leave = routes.find(route => route && typeof route.leave === 'function')?.leave; assert.equal(typeof leave, 'function', 'ACTUAL_USE_UNSAVED_DRAFT_ROUTE_GUARD')
  const first = leave({ path: '/adapter/search' }); await settle(); const prompt = confirms.at(-1); assert.match(prompt?.title || '', /未保存/); assert.equal(prompt.focusTriggerAfterClose, false); prompt.onCancel(); assert.equal(await first, false)
  assert.ok(w.get('.compileContent').text().includes('SYNTHETIC_LEAVE_DRAFT'))
  const second = leave({ path: '/adapter/search' }); await settle(); confirms.at(-1).onOk(); assert.equal(await second, true)
  const pending = leave({ path: '/adapter/search' }); await settle(); user.setStoredCurrentUser(B); await settle(); assert.equal(await pending, false, 'RETIRED_DRAFT_CANNOT_AUTHORIZE_A_ROUTE')
  assert.equal(await leave({ path: '/login' }), true); assert.equal(calls.create.length, 0)
  console.log('SMART_IMPORT_WITNESS actualVue=true actualAntd=true actualCurrentUser=true actualUnsavedDraft=true actualTypeHelper=true syntheticTransportOnly=true externalRequests=0')
})

test('SMART_IMPORT low-frequency supported DTO strings and raw no-option 0 false duplicate answers survive exact payload whitelist', async () => {
  const w = await mountPage()
  const rows = [{ content: 'SYNTHETIC_FREE_TEXT', typeAlias: '简答题', answer: [0, false, 'SYNTHETIC_RAW', 'SYNTHETIC_RAW'], course_name: ' SYNTHETIC_COURSE ', source_name: ' SYNTHETIC_SOURCE_NAME ', extra: ' SYNTHETIC_EXTRA ', id: 998, hash: 'SYNTHETIC_FORBIDDEN_HASH', owner: 999, source: 'SYNTHETIC_UNSUPPORTED_FIELD' }]
  await doneUpload(w, 'LOW_FREQUENCY', rows); click(w, '确认导入'); await settle()
  assert.equal(calls.create.length, 1); assert.deepEqual(calls.create[0].payload, [{ question: 'SYNTHETIC_FREE_TEXT', type: 4, answer: '[0,false,"SYNTHETIC_RAW","SYNTHETIC_RAW"]', course_name: ' SYNTHETIC_COURSE ', source_name: ' SYNTHETIC_SOURCE_NAME ', extra: ' SYNTHETIC_EXTRA ' }])
  calls.create[0].resolve({ status: 200 }); await settle(); assert.equal(w.vm.submitLoading, false)
})

test('SMART_IMPORT invalid option labels never become silent null answers or partial-success writes', async () => {
  const w = await mountPage(), rows = [...paper('VALID'), { ...paper('INVALID')[0], answer: ['Z'] }]
  await doneUpload(w, 'INVALID_LABEL', rows); click(w, '确认导入'); w.vm.upload(); await settle()
  assert.equal(calls.create.length, 0, 'ONE_INVALID_ANSWER_PREVENTS_PARTIAL_WRITE_AND_JSON_NULL'); assert.equal(w.vm.data.length, 2); assert.ok(notices.some(item => ['warning', 'error'].includes(item.kind)))
})

test('SMART_IMPORT missing content or absent answer remains visible for repair but cannot create a partial subset', async () => {
  for (const invalid of [{ typeAlias: '简答题', answer: ['SYNTHETIC_MISSING_CONTENT'] }, { content: '', typeAlias: '简答题', answer: ['SYNTHETIC_EMPTY_CONTENT'] }, { content: 'SYNTHETIC_NO_ANSWER', typeAlias: '简答题' }]) {
    const w = await mountPage(); await doneUpload(w, 'REPAIR', [...paper('VALID'), invalid]); click(w, '确认导入'); w.vm.upload(); await settle()
    assert.equal(calls.create.length, 0, 'INVALID_QUESTION_CANNOT_BE_SILENTLY_FILTERED_INTO_PARTIAL_SUCCESS'); assert.equal(w.vm.data.length, 2); w.unmount()
  }
})

test('SMART_IMPORT explicit empty options and answer arrays retain legacy wire shape rather than tightening valid input', async () => {
  const w = await mountPage(), rows = [{ content: 'SYNTHETIC_EXPLICIT_EMPTY_ARRAYS', typeAlias: '简答题', options: [], answer: [] }, { content: 'SYNTHETIC_ABSENT_OPTIONS', typeAlias: '简答题', answer: ['SYNTHETIC_RAW'] }]
  await doneUpload(w, 'EMPTY_ARRAYS', rows); click(w, '确认导入'); await settle()
  assert.equal(calls.create.length, 1); assert.deepEqual(calls.create[0].payload, [{ question: 'SYNTHETIC_EXPLICIT_EMPTY_ARRAYS', type: 4, options: '[]', answer: '[]' }, { question: 'SYNTHETIC_ABSENT_OPTIONS', type: 4, answer: '["SYNTHETIC_RAW"]' }])
})

test('SMART_IMPORT real backend created-count message distinguishes reported created N from submitted M without claiming all persisted', async () => {
  const w = await mountPage(), rows = [...paper('ONE'), ...paper('TWO'), ...paper('THREE')]
  await doneUpload(w, 'COUNTED', rows); click(w, '确认导入'); await settle(); assert.equal(calls.create[0].payload.length, 3)
  calls.create[0].resolve({ status: 200, message: '成功创建2条数据' }); await settle()
  const result = notices.filter(item => /2/.test(item.text) && /3/.test(item.text))
  assert.ok(result.length > 0, 'SERVER_CREATED_2_AND_SUBMITTED_3_ARE_BOTH_DISCLOSED')
  assert.equal(result.some(item => /全部成功|全部导入成功|全部写入成功/.test(item.text)), false, 'PARTIAL_REPORTED_COUNT_IS_NOT_ALL_PERSISTED')
  assert.equal(w.vm.submitLoading, false); click(w, '确认导入'); w.vm.upload(); await settle(); assert.equal(calls.create.length, 1, 'COMPLETED_SNAPSHOT_IS_NOT_AUTOMATICALLY_WRITTEN_AGAIN')
})

test('SMART_IMPORT unknown backend success text is never echoed as trusted persistence evidence', async () => {
  const w = await mountPage(); await doneUpload(w); click(w, '确认导入'); await settle()
  calls.create[0].resolve({ status: 200, message: 'SYNTHETIC_UNTRUSTED_SERVER_MESSAGE<script>ALL_PERSISTED</script>' }); await settle()
  assert.equal(notices.some(item => /SYNTHETIC_UNTRUSTED_SERVER_MESSAGE|ALL_PERSISTED/.test(item.text)), false, 'UNRECOGNIZED_SERVER_MESSAGE_NOT_ECHOED')
  assert.equal(w.vm.submitLoading, false); assert.equal(calls.create.length, 1)
})

test('SMART_IMPORT owned accepted clear and replacement upload perform exactly their confirmed operation', async () => {
  const w = await mountPage(); await doneUpload(w); edit(w, 'SYNTHETIC_CLEAR_ACCEPT')
  click(w, '清空题目'); await settle(); const clearPrompt = confirms.at(-1); assert.match(clearPrompt?.title || '', /清空/); clearPrompt.onOk(); await settle()
  assert.equal(w.vm.data.length, 0); assert.equal(w.vm.showProblemArr.length, 0); assert.equal(w.vm.UploadId, ''); assert.equal(w.vm.loading, false); assert.equal(calls.parse.length, 1)
  await doneUpload(w, 'SECOND'); edit(w, 'SYNTHETIC_REPLACE_ACCEPT'); const native = uploadCallbacks(w), item = file('REPLACEMENT')
  const accepted = Promise.resolve(native.before(item, [item])); await settle(); const replacePrompt = confirms.at(-1); assert.match(replacePrompt?.title || '', /替换|上传/); replacePrompt.onOk(); assert.equal(await accepted, true); await settle(); assert.equal(w.vm.loading, true)
  item.status = 'done'; item.response = { crc: 'SYNTHETIC_REPLACED_CRC' }; uploadCallbacks(w).change({ file: item, fileList: [item] }); await settle(); assert.equal(calls.parse.length, 3)
  assert.deepEqual(calls.parse[2].payload, { file: 'SYNTHETIC_REPLACED_CRC' }); calls.parse[2].resolve({ paper: paper('REPLACED') }); await settle()
  assert.equal(w.vm.data[0].content, 'SYNTHETIC_IMPORT_PRIVATE_REPLACED'); assert.equal(calls.reparse.length, 0); assert.equal(calls.create.length, 0)
})

for (const failure of ['nonarray', 'reject']) {
  test(`SMART_IMPORT reparse ${failure} preserves native edited draft and releases own loading without followup write`, async () => {
    const w = await mountPage(); await doneUpload(w); edit(w, 'SYNTHETIC_FAILED_REPARSE_DRAFT')
    const completion = Promise.resolve(w.vm.reParseFile('<p>SYNTHETIC_FAILED_REPARSE_DRAFT</p>')).catch(() => {}); await settle(); assert.equal(calls.reparse.length, 1)
    if (failure === 'reject') calls.reparse[0].reject(new Error('SYNTHETIC_REPARSE_REJECTED')); else calls.reparse[0].resolve({ paper: { invalid: true } })
    await completion; await settle(); assert.equal(w.vm.loading, false); assert.ok(w.get('.compileContent').text().includes('SYNTHETIC_FAILED_REPARSE_DRAFT'))
    click(w, '确认导入'); w.vm.upload(); await settle(); assert.equal(calls.create.length, 0); assert.equal(calls.parse.length, 1); assert.equal(calls.reparse.length, 1); assert.ok(notices.some(item => item.kind === 'error'))
  })
}

test('SMART_IMPORT accepted same-text reparse replaces edited native DOM and erases retained old editor bytes', async () => {
  const w = await mountPage(); await doneUpload(w)
  const oldEditor = edit(w, 'SYNTHETIC_DOM_EDIT_NOT_IN_SERVER_RESULT'); click(w, '重新识别'); await settle()
  calls.reparse[0].resolve({ paper: paper() }); await settle()
  const currentEditor = w.get('.compileContent [contenteditable]').element
  assert.notEqual(currentEditor, oldEditor, 'ACCEPTED_PAPER_REBUILDS_NATIVE_EDITOR_EVEN_WHEN_VDOM_TEXT_IS_SAME')
  assert.equal(oldEditor.textContent, '', 'REPLACED_EDITOR_CANNOT_RETAIN_PRIVATE_DRAFT_BYTES')
  assert.equal(currentEditor.textContent.includes('SYNTHETIC_DOM_EDIT_NOT_IN_SERVER_RESULT'), false)
  assert.ok(currentEditor.textContent.includes('SYNTHETIC_IMPORT_PRIVATE_A'))
  click(w, '确认导入'); await settle(); assert.equal(calls.create.length, 1); assert.equal(calls.create[0].payload[0].question, 'SYNTHETIC_IMPORT_PRIVATE_A')
})

test('SMART_IMPORT legacy option values without presentation names still parse and import in answer order', async () => {
  const w = await mountPage(), rows = [{ content: 'SYNTHETIC_OPTION_NAME_OPTIONAL', typeAlias: '多选题', options: [{ value: 'SYNTHETIC_OPTION_FIRST' }, { value: 'SYNTHETIC_OPTION_SECOND' }], answer: ['B', 'A', 'B'] }]
  await doneUpload(w, 'OPTIONAL_NAMES', rows); click(w, '确认导入'); await settle()
  assert.equal(calls.create.length, 1); assert.deepEqual(calls.create[0].payload, [{ question: 'SYNTHETIC_OPTION_NAME_OPTIONAL', type: 1, options: '["SYNTHETIC_OPTION_FIRST","SYNTHETIC_OPTION_SECOND"]', answer: '["SYNTHETIC_OPTION_SECOND","SYNTHETIC_OPTION_FIRST","SYNTHETIC_OPTION_SECOND"]' }])
})

test('SMART_IMPORT real UNKNOWN_OUTCOME create error preserves draft and says unconfirmed rather than failed or automatically replaying', async () => {
  const w = await mountPage(); await doneUpload(w); click(w, '确认导入'); await settle(); const before = JSON.stringify(w.vm.data)
  const networkError = new Error('SYNTHETIC_NETWORK_DOWN'); networkError.code = 'ERR_NETWORK'
  const unknown = toUserRequestError({ method: 'post', url: '/adapter-service/questions' }, networkError)
  assert.equal(unknown.code, 'UNKNOWN_OUTCOME', 'ACTUAL_MUTATION_UNKNOWN_OUTCOME_CONTRACT')
  calls.create[0].reject(unknown); await settle(); await new Promise(resolve => setTimeout(resolve, 25)); await settle()
  assert.equal(JSON.stringify(w.vm.data), before); assert.equal(w.vm.submitLoading, false); assert.equal(calls.create.length, 1); assert.equal(calls.parse.length, 1)
  assert.ok(notices.some(item => /未确认|尚未确认/.test(item.text) && /核对/.test(item.text)), 'UNKNOWN_RESULT_REQUESTS_RECONCILIATION')
  assert.equal(notices.some(item => /导入失败|未写入|已回滚|SYNTHETIC_NETWORK_DOWN/.test(item.text)), false, 'UNKNOWN_WRITE_IS_NOT_REPORTED_AS_KNOWN_FAILURE')
})

test('SMART_IMPORT real editor appended unclassed div and text survive reparse together with existing question type', async () => {
  const w = await mountPage(); await doneUpload(w)
  const editor = w.get('.compileContent [contenteditable]').element, line = editor.querySelector('.lines')
  line.textContent = 'SYNTHETIC_EDITED_EXISTING_LINE'
  const added = document.createElement('div'); added.textContent = 'SYNTHETIC_NEW_BROWSER_PARAGRAPH'; editor.appendChild(added)
  editor.appendChild(document.createTextNode('SYNTHETIC_NEW_PLAIN_TEXT'))
  editor.dispatchEvent(new Event('input', { bubbles: true })); click(w, '重新识别'); await settle()
  assert.equal(calls.reparse.length, 1); const html = calls.reparse[0].payload.html
  assert.ok(html.includes('SYNTHETIC_EDITED_EXISTING_LINE')); assert.ok(html.includes('SYNTHETIC_NEW_BROWSER_PARAGRAPH'), 'NATIVE_NEW_DIV_IS_NOT_SILENTLY_DROPPED')
  assert.ok(html.includes('SYNTHETIC_NEW_PLAIN_TEXT'), 'NATIVE_NEW_TEXT_NODE_IS_NOT_SILENTLY_DROPPED'); assert.ok(html.includes('单选题'), 'EXISTING_QUESTION_TYPE_LABEL_IS_RETAINED')
  assert.ok(html.indexOf('SYNTHETIC_EDITED_EXISTING_LINE') < html.indexOf('SYNTHETIC_NEW_BROWSER_PARAGRAPH')); assert.ok(html.indexOf('SYNTHETIC_NEW_BROWSER_PARAGRAPH') < html.indexOf('SYNTHETIC_NEW_PLAIN_TEXT'))
  calls.reparse[0].resolve({ paper: paper('APPENDED') }); await settle(); assert.equal(w.vm.data[0].content, 'SYNTHETIC_IMPORT_PRIVATE_APPENDED')
})

test('SMART_IMPORT mixed absent-answer draft requires native explicit skip opt-in and preserves unsubmitted rows and explicit empty answers', async () => {
  const w = await mountPage(), rows = [...paper('VALID'), { content: 'SYNTHETIC_MISSING_ANSWER_OPT_IN', typeAlias: '简答题' }, { content: 'SYNTHETIC_EXPLICIT_EMPTY_ANSWER', typeAlias: '简答题', answer: [] }]
  await doneUpload(w, 'MIXED_MISSING', rows); click(w, '确认导入'); w.vm.upload(); await settle(); assert.equal(calls.create.length, 0, 'MISSING_ANSWER_SUBSET_IS_NEVER_SILENT_DEFAULT')
  const input = w.get('input[type="checkbox"]'); assert.equal(input.element.checked, false); input.element.click(); await settle(); assert.equal(input.element.checked, true)
  click(w, '确认导入'); await settle(); assert.equal(calls.create.length, 1)
  assert.deepEqual(calls.create[0].payload, [{ question: 'SYNTHETIC_IMPORT_PRIVATE_VALID', type: 0, options: '["SYNTHETIC_OPTION_VALID","SYNTHETIC_OTHER_OPTION"]', answer: '["SYNTHETIC_OPTION_VALID"]' }, { question: 'SYNTHETIC_EXPLICIT_EMPTY_ANSWER', type: 4, answer: '[]' }])
  assert.equal(w.vm.data.length, 3, 'EXPLICIT_SKIP_NEVER_DELETES_REPAIRABLE_ROWS'); assert.equal(w.vm.data[1].content, 'SYNTHETIC_MISSING_ANSWER_OPT_IN'); assert.equal(Object.hasOwn(w.vm.data[1], 'answer'), false)
  calls.create[0].resolve({ status: 200, message: '成功创建2条数据' }); await settle()
  assert.equal(w.vm.data.length, 3); user.setStoredCurrentUser(B); await settle(); await doneUpload(w, 'B_MIXED', rows)
  assert.equal(w.get('input[type="checkbox"]').element.checked, false, 'OPT_IN_IS_NOT_INHERITED_BY_ANOTHER_ACTOR')
})

test('SMART_IMPORT reported zero created retains real unsaved-route prompt cancel and a neutral nonreplaying duplicate state', async () => {
  const w = await mountPage(); await doneUpload(w); click(w, '确认导入'); await settle(); const before = JSON.stringify(w.vm.data)
  calls.create[0].resolve({ status: 200, message: '成功创建0条数据' }); await settle()
  const leave = routes.find(route => route && typeof route.leave === 'function')?.leave; assert.equal(typeof leave, 'function')
  const decision = leave({ path: '/adapter/search' }); await settle(); const prompt = confirms.at(-1); assert.match(prompt?.title || '', /未保存/, 'ZERO_CREATED_IS_NOT_SAVED_DRAFT')
  prompt.onCancel(); assert.equal(await decision, false); assert.equal(JSON.stringify(w.vm.data), before)
  click(w, '确认导入'); w.vm.upload(); await settle(); assert.equal(calls.create.length, 1, 'ZERO_CREATED_RESPONSE_IS_NOT_AUTOMATICALLY_REPLAYED')
  assert.equal(/当前题目已导入|全部导入成功|全部写入成功/.test(document.body.textContent), false, 'ZERO_RESULT_DUPLICATE_NOTICE_CANNOT_CLAIM_IMPORTED')
})

test('SMART_IMPORT unknown created count retains real unsaved-route prompt cancel and neutral processed-not-imported duplicate state', async () => {
  const w = await mountPage(); await doneUpload(w); click(w, '确认导入'); await settle(); const before = JSON.stringify(w.vm.data)
  calls.create[0].resolve({ status: 200, message: 'SYNTHETIC_UNKNOWN_CREATED_COUNT' }); await settle()
  const leave = routes.find(route => route && typeof route.leave === 'function')?.leave; assert.equal(typeof leave, 'function')
  const decision = leave({ path: '/adapter/search' }); await settle(); const prompt = confirms.at(-1); assert.match(prompt?.title || '', /未保存/, 'UNKNOWN_CREATED_COUNT_IS_NOT_SAVED_DRAFT')
  prompt.onCancel(); assert.equal(await decision, false); assert.equal(JSON.stringify(w.vm.data), before)
  click(w, '确认导入'); w.vm.upload(); await settle(); assert.equal(calls.create.length, 1)
  assert.equal(/当前题目已导入|全部导入成功|全部写入成功|SYNTHETIC_UNKNOWN_CREATED_COUNT/.test(document.body.textContent), false, 'UNKNOWN_RESULT_IS_NEUTRAL_AND_UNTRUSTED_MESSAGE_NOT_ECHOED')
})
