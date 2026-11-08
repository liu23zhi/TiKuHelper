// Real Vue/Antd4 and currentUser helpers. Synthetic API, file, Swal and download boundaries only.
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const PAGE = path.join(web, 'src/pages/TikuComponent/index.vue')
const A = { id: 9301, username: 'synthetic-tiku-admin-a', role: 'admin' }
const B = { id: 9302, username: 'synthetic-tiku-admin-b', role: 'admin' }
const row = (id = 101, question = '原始题目', type = 4) => ({
  id, question, type, course_name: '原始课程', options: '[]',
  answer: '["重复答案","重复答案"]', source: 7, source_name: 'SYNTHETIC 来源',
  extra: 'SYNTHETIC 低频拓展字段', custom_fixture_field: '保留附加字段',
})
const ok = data => ({ status: 200, data })
const listing = (items = [row()], total = items.length) => ok({ items, total })
let wrappers = [], histories = [], calls = [], pending = [], deferred = new Set()
let listRows = [row()], listTotal = 12, courseRows = ['原始课程', '另一个课程']
let swalCalls = [], swalPending = [], deferSwal = false, swalAnswer = { isConfirmed: true }
let downloads = [], blobs = [], revoked = [], exportHook = null
const copy = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const apiPath = path.join(web, 'src/api/TikuComponentApi.js')
const api = new Module(apiPath); api.loaded = true
function apiKind(name, payload) { return name === 'importQuestions' ? payload?.dryRun ? 'importDryRun' : 'importExecute' : name }
api.exports = Object.fromEntries(['getCourses', 'getQuestions', 'delQuestions', 'updateQuestions', 'createQuestions', 'importQuestions'].map(name => [name, payload => {
  const kind = apiKind(name, payload)
  calls.push({ name, kind, payload: copy(payload), principal: copy(user.getStoredCurrentUser()) })
  if (deferred.has(kind)) return new Promise((resolve, reject) => pending.push({ kind, payload: copy(payload), resolve, reject }))
  return Promise.resolve(kind === 'getQuestions' ? listing(copy(listRows), listTotal) : kind === 'getCourses' ? ok(courseRows) : kind === 'importDryRun' ? ok({ existingIds: [101], total: 1 }) : kind === 'importExecute' ? ok({ created: 1, updated: 0, skipped: 0 }) : ok({}))
}]))
require.cache[apiPath] = api
const swalPath = require.resolve('sweetalert2')
const swal = new Module(swalPath); swal.loaded = true
const popup = document.createElement('div')
swal.exports = { fire: options => {
  swalCalls.push(copy(options))
  return deferSwal ? new Promise(resolve => swalPending.push({ options, resolve })) : Promise.resolve(swalAnswer)
}, getPopup: () => popup, close: () => {} }
require.cache[swalPath] = swal
window.URL.createObjectURL = blob => {
  blobs.push(blob)
  if (exportHook) { const hook = exportHook; exportHook = null; hook() }
  return 'blob:synthetic-tiku-principal-' + blobs.length
}
window.URL.revokeObjectURL = value => revoked.push(value)
const anchorClick = window.HTMLAnchorElement.prototype.click
window.HTMLAnchorElement.prototype.click = function () { downloads.push({ href: this.href, filename: this.download }) }
process.on('exit', () => { window.HTMLAnchorElement.prototype.click = anchorClick })
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 3)); await nextTick() }
function named(w, name) {
  const b = w.findAll('button').find(b => b.text().replace(/\s/g, '') === name.replace(/\s/g, ''))
  assert.ok(b, 'actual native button ' + name)
  return b.element
}
async function click(element) { element.click(); await settle() }
function dialog(title = '详情内容') {
  const d = [...document.querySelectorAll('.ant-modal')].find(d => d.querySelector('.ant-modal-title')?.textContent.includes(title) && d.closest('.ant-modal-wrap')?.style.display !== 'none')
  assert.ok(d, 'real Antd dialog ' + title)
  return d
}
function modalButton(name, title) {
  const b = [...dialog(title).querySelectorAll('.ant-modal-footer button')].find(b => ([name, ...(name === '确定' ? ['OK', '确认'] : []), ...(name === '取消' ? ['Cancel'] : [])]).includes(b.textContent.replace(/\s/g, '')))
  assert.ok(b, 'real Antd modal button ' + name)
  return b
}
async function input(element, value) { element.value = value; element.dispatchEvent(new Event('input', { bubbles: true })); await settle() }
function last(kind) { const p = pending.filter(p => p.kind === kind).at(-1); assert.ok(p, 'current deferred API ' + kind); return p }
const count = kind => calls.filter(c => c.kind === kind).length
const writes = () => calls.filter(c => ['delQuestions', 'updateQuestions', 'createQuestions', 'importExecute'].includes(c.kind))
async function page(principal = A, reuse = false) {
  if (!reuse) user.setStoredCurrentUser(principal)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/component', component: require(PAGE) }] })
  await router.push('/adapter/component'); await router.isReady(); histories.push(router.options.history)
  const w = mount(require(PAGE), { attachTo: document.body, global: { plugins: [router, Antd], stubs: { transition: false, 'transition-group': false } } })
  wrappers.push(w); await settle(); return w
}
async function select(w) { const checkbox = w.find('tbody input[type="checkbox"]'); assert.ok(checkbox.exists(), 'real Antd row checkbox'); await click(checkbox.element) }
async function twoPages(w) {
  await select(w); listRows = [row(201, '第二页题目')]
  await click(w.find('.ant-pagination-item-2').element); await select(w)
  assert.deepEqual([...w.vm.selectedRowKeys], [101, 201])
}
async function open(w) { await click(named(w, '编辑')) }
function state(w) {
  return copy({ data: w.vm.data, total: w.vm.page.total, selected: [...w.vm.selectedRowKeys],
    selectedQuestions: w.vm.selectedQuestions, form: w.vm.formData, answer: w.vm.answer,
    files: w.vm.fileList, visible: w.vm.visible, courseVisible: w.vm.bulkCourseVisible,
    courseName: w.vm.bulkCourseName, courses: w.vm.courses, loading: w.vm.tabLoading, allLoading: w.vm.selectAllLoading })
}
function witness(name, w) { console.log('TIKU_PRINCIPAL_OBSERVED ' + JSON.stringify({ name, state: state(w), calls, downloads: downloads.length, revoked, swal: swalCalls.map(c => c.title), nativeBrowser: false })) }
function importEvent(text = JSON.stringify([row()])) {
  const target = { value: 'synthetic-file.json', files: [{ name: 'synthetic-file.json', text: () => Promise.resolve(text) }] }
  return { target }
}
afterEach(async () => {
  for (const w of wrappers) if (w.exists()) w.unmount()
  wrappers = []; deferred.clear()
  for (const p of pending) p.resolve(p.kind === 'getQuestions' ? listing() : p.kind === 'getCourses' ? ok([]) : ok({}))
  for (const p of swalPending) p.resolve({ isDismissed: true })
  await settle()
  for (const history of histories) history.destroy()
  histories = []; calls = []; pending = []; deferred = new Set()
  swalCalls = []; swalPending = []; deferSwal = false; swalAnswer = { isConfirmed: true }
  listRows = [row()]; listTotal = 12; courseRows = ['原始课程', '另一个课程']
  downloads = []; blobs = []; revoked = []; exportHook = null
  user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear()
  Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren(); delete require.cache[PAGE]
})

test('TP01 healthy actor retains literal filters, low-frequency fields, native selection and JSON export', async () => {
  const w = await page()
  w.vm.typeValue = 4
  Object.assign(w.vm.searchValue, { question: '筛选词', source: '7', extra: '拓展筛选', onlyShowEmptyAnswer: true })
  await click(named(w, '搜索'))
  assert.deepEqual(calls.filter(c => c.kind === 'getQuestions').at(-1).payload, { type: 4, pageSize: 10, pageNo: 0, question: '筛选词', source: 7, extra: '拓展筛选', onlyShowEmptyAnswer: true })
  assert.ok(w.vm.columns.some(c => c.key === 'extra') && w.vm.columns.some(c => c.key === 'source_name'))
  await select(w); await click(named(w, '导出选中'))
  assert.equal(downloads.length, 1); assert.match(downloads[0].filename, /\.json$/)
  assert.deepEqual(JSON.parse(await blobs[0].text()), [row()])
  assert.deepEqual(revoked, ['blob:synthetic-tiku-principal-1']); assert.equal(writes().length, 0)
  witness('positive-filters-export', w)
})

for (const transition of ['admin-B', 'role-revoked', 'logout']) test('TP02 sync principal retirement clears rows selection cached payload form files courses: ' + transition, async () => {
  const w = await page(); await select(w); await open(w)
  w.vm.fileList.push('SYNTHETIC_OLD_ATTACHMENT'); w.vm.bulkCourseName = '旧课程'
  if (transition === 'admin-B') user.setStoredCurrentUser(B)
  else if (transition === 'role-revoked') user.setStoredCurrentUser({ ...A, role: 'user' })
  else user.clearStoredCurrentUser()
  assert.deepEqual([...w.vm.data], [], 'PRINCIPAL_SYNC_CLEAR_ROWS')
  assert.deepEqual([...w.vm.selectedRowKeys], [], 'PRINCIPAL_SYNC_CLEAR_SELECTION')
  assert.deepEqual([...w.vm.selectedQuestions], [], 'PRINCIPAL_SYNC_CLEAR_CACHE')
  assert.deepEqual([...w.vm.fileList], [], 'PRINCIPAL_SYNC_CLEAR_FILES')
  assert.equal(w.vm.visible, false); assert.equal(w.vm.bulkCourseVisible, false)
  assert.deepEqual(w.vm.formData, {}); assert.deepEqual([...w.vm.courses], [])
})

for (const transition of ['admin-B', 'role-revoked', 'logout']) test('TP03 old list and courses success cannot publish or clear the new loading: ' + transition, async () => {
  const w = await page(); deferred.add('getQuestions'); deferred.add('getCourses')
  w.vm.fetchData(); w.vm.getCourses(); const oldList = last('getQuestions'), oldCourses = last('getCourses')
  if (transition === 'admin-B') user.setStoredCurrentUser(B)
  else if (transition === 'role-revoked') user.setStoredCurrentUser({ ...A, role: 'user' })
  else user.clearStoredCurrentUser()
  await settle()
  if (transition === 'admin-B') assert.notStrictEqual(last('getQuestions'), oldList, 'B autoloads its own list')
  const before = state(w)
  oldList.resolve(listing([row(909, '迟到A题目')], 999)); oldCourses.resolve(ok(['迟到A课程']))
  await settle(); witness('late-read-' + transition, w)
  assert.deepEqual(state(w), before, 'RETIRED_READ_NO_PUBLICATION_OR_FINALLY')
  if (transition === 'admin-B') {
    assert.equal(w.vm.tabLoading, true, 'B pending list retains loading')
    last('getQuestions').resolve(listing([row(202, 'B当前题目')], 1)); last('getCourses').resolve(ok(['B当前课程'])); await settle()
    assert.equal(w.vm.data[0].question, 'B当前题目'); assert.equal(w.vm.tabLoading, false)
  }
})

test('TP04 retired all-pages request cannot dispatch page two or repopulate B selection', async () => {
  const w = await page(); deferred.add('getQuestions')
  await click(named(w, '全选所有页')); const old = last('getQuestions')
  user.setStoredCurrentUser(B); await settle()
  const reads = count('getQuestions')
  old.resolve(listing(Array.from({ length: 100 }, (_, i) => row(i + 1)), 201)); await settle()
  assert.equal(count('getQuestions'), reads, 'RETIRED_SELECT_ALL_NO_NEXT_PAGE')
  assert.deepEqual([...w.vm.selectedRowKeys], []); assert.deepEqual([...w.vm.selectedQuestions], [])
})

test('TP05 same loaded module has independent mount-local rows query selection form and busy refs', async () => {
  const a = await page(), b = await page(A, true)
  for (const key of ['data', 'page', 'searchValue', 'selectedRowKeys', 'formData', 'fileList', 'courses']) assert.notStrictEqual(a.vm[key], b.vm[key], 'MOUNT_LOCAL_' + key)
  await select(a); a.vm.searchValue.question = 'A挂载草稿'; await open(a)
  assert.deepEqual([...b.vm.selectedRowKeys], []); assert.notEqual(b.vm.searchValue.question, 'A挂载草稿'); assert.equal(b.vm.visible, false)
})

test('TP06 unmounted list and courses cannot overwrite same-module re-entry', async () => {
  const a = await page(); deferred.add('getQuestions'); deferred.add('getCourses')
  a.vm.fetchData(); a.vm.getCourses(); const oldList = last('getQuestions'), oldCourses = last('getCourses')
  a.unmount(); const b = await page(A, true)
  last('getQuestions').resolve(listing([row(202, '重入题目')], 1)); last('getCourses').resolve(ok(['重入课程'])); await settle()
  const before = state(b); oldList.resolve(listing([row(909, '卸载旧题')], 99)); oldCourses.resolve(ok(['卸载旧课程'])); await settle()
  assert.deepEqual(state(b), before, 'REENTRY_STATE_OWNED_BY_NEW_MOUNT')
})

test('TP07 changing native edit draft then Cancel does not alias table or selection export cache', async () => {
  const w = await page(); await select(w); await open(w)
  await input(dialog().querySelector('textarea'), '未保存的题面草稿')
  assert.equal(w.vm.data[0].question, '原始题目', 'DRAFT_NOT_TABLE_ALIAS')
  assert.equal(w.vm.selectedQuestions[0].question, '原始题目', 'DRAFT_NOT_SELECTION_CACHE_ALIAS')
  await click(modalButton('取消')); await click(named(w, '导出选中'))
  assert.equal(JSON.parse(await blobs[0].text())[0].question, '原始题目'); assert.equal(writes().length, 0)
})

test('TP08 healthy non-objective update preserves repeated answers and low fields without logging answer text', async () => {
  const w = await page(); await open(w)
  const originalLog = console.log, logs = []
  console.log = (...args) => logs.push(args.map(String).join(' '))
  try { await click(modalButton('确定')) } finally { console.log = originalLog }
  const payload = writes().find(c => c.kind === 'updateQuestions')?.payload
  assert.ok(payload); assert.equal(payload.extra, row().extra); assert.equal(payload.custom_fixture_field, row().custom_fixture_field)
  assert.equal(payload.answer, row().answer); assert.equal(w.vm.visible, false)
  assert.equal(logs.some(s => s.includes('重复答案') || s.includes('textAns')), false, 'ANSWER_NOT_CONSOLE_LOGGED')
})

test('TP09 stale edit modal native OK/input and afterClose cannot write or close B new editor', async () => {
  const w = await page(); await open(w)
  const oldInput = dialog().querySelector('textarea'), oldOK = modalButton('确定'), oldAfterClose = w.vm.questionDialogAfterClose
  user.setStoredCurrentUser(B)
  oldInput.value = 'A旧DOM迟到'; oldInput.dispatchEvent(new Event('input', { bubbles: true })); oldOK.click()
  await settle()
  assert.equal(writes().length, 0, 'OLD_EDIT_DOM_CANNOT_WRITE_B')
  await open(w); await input(dialog().querySelector('textarea'), 'B新编辑器')
  const outside = document.createElement('button'); document.body.appendChild(outside); outside.focus()
  oldAfterClose(); await settle()
  assert.equal(w.vm.visible, true); assert.equal(w.vm.formData.question, 'B新编辑器')
  assert.strictEqual(document.activeElement, outside, 'OLD_AFTER_CLOSE_CANNOT_STEAL_B_FOCUS')
})

for (const operation of ['updateQuestions', 'createQuestions']) test('TP10 old editor ' + operation + ' success cannot close B dialog or refresh B rows', async () => {
  const w = await page()
  if (operation === 'createQuestions') { await click(named(w, '添加')); await input(dialog().querySelector('textarea'), '创建题'); }
  else await open(w)
  deferred.add(operation); await click(modalButton('确定')); const old = last(operation)
  user.setStoredCurrentUser(B); await settle(); await open(w); await input(dialog().querySelector('textarea'), 'B新编辑器')
  const before = state(w), reads = count('getQuestions')
  old.resolve(ok({})); await settle(); witness('late-' + operation, w)
  assert.equal(count('getQuestions'), reads, 'RETIRED_WRITE_NO_REFRESH')
  assert.deepEqual(state(w), before, 'RETIRED_WRITE_NO_DIALOG_OR_FINALLY')
  assert.equal(document.body.textContent.includes('修改成功') || document.body.textContent.includes('创建成功'), false)
})

test('TP11 healthy native attachment callback appends to real AnswerBox without bypassing child logic', async () => {
  const w = await page(); await open(w)
  await click([...dialog().querySelectorAll('[role="tab"]')].find(t => t.textContent.includes('提交附件')))
  const upload = w.findComponent(Antd.Upload.Dragger), file = { uid: 'accepted-current-upload', name: 'fixture.txt' }
  assert.ok(upload.exists(), 'real current Antd upload pipeline')
  // Without an installed preflight Antd proceeds normally; a provided preflight is exercised, never stubbed.
  assert.equal((upload.props('beforeUpload') || (() => true))(file), true)
  upload.props('onChange')({ file: { ...file, status: 'done', response: 'SYNTHETIC_ATTACHMENT' } }); await settle()
  assert.deepEqual([...w.vm.fileList], ['SYNTHETIC_ATTACHMENT'])
  assert.ok(w.vm.$refs.answerBox.getData().includes('SYNTHETIC_ATTACHMENT'))
  await click(modalButton('确定'))
  assert.ok(writes().find(c => c.kind === 'updateQuestions').payload.answer.includes('SYNTHETIC_ATTACHMENT'))
})

test('TP12 captured upload onChange cannot append or toast over B owner', async () => {
  const w = await page(); await open(w)
  await click([...dialog().querySelectorAll('[role="tab"]')].find(t => t.textContent.includes('提交附件')))
  const upload = w.findComponent(Antd.Upload.Dragger)
  assert.ok(upload.exists(), 'actual Antd upload component')
  const oldChange = upload.props('onChange')
  assert.equal(typeof oldChange, 'function', 'actual captured upload event')
  user.setStoredCurrentUser(B); await settle(); await open(w)
  oldChange({ file: { status: 'done', name: 'A-stale.txt', response: 'SYNTHETIC_A_ATTACHMENT' } }); await settle()
  assert.deepEqual([...w.vm.fileList], []); assert.ok(!w.vm.$refs.answerBox.getData().includes('SYNTHETIC_A_ATTACHMENT'))
  assert.equal(document.body.textContent.includes('A-stale.txt 上传成功'), false)
})

for (const action of ['row', 'bulk']) test('TP13 explicit current ' + action + ' delete confirmation waits, cancels with zero writes, then permits exactly selected IDs', async () => {
  const w = await page(); if (action === 'bulk') await select(w)
  deferSwal = true
  await click(named(w, action === 'row' ? '删除' : '批量删除'))
  assert.equal(writes().length, 0, 'DELETE_REQUIRES_EXPLICIT_CONFIRM')
  assert.equal(swalPending.length, 1); swalPending.shift().resolve({ isDismissed: true }); await settle()
  assert.equal(writes().length, 0)
  await click(named(w, action === 'row' ? '删除' : '批量删除'))
  swalPending.shift().resolve({ isConfirmed: true }); await settle()
  assert.deepEqual(writes().map(c => ({ kind: c.kind, payload: c.payload })), [{ kind: 'delQuestions', payload: { id: 101 } }])
})

for (const action of ['row', 'bulk']) test('TP14 A ' + action + ' delete confirmation cannot dispatch against B same-ID row', async () => {
  const w = await page(); if (action === 'bulk') await select(w)
  deferSwal = true; await click(named(w, action === 'row' ? '删除' : '批量删除'))
  assert.equal(swalPending.length, 1, 'captured A confirmation')
  const old = swalPending.shift(); user.setStoredCurrentUser(B); await settle()
  const reads = count('getQuestions'); old.resolve({ isConfirmed: true }); await settle()
  assert.equal(writes().length, 0, 'RETIRED_DELETE_CONFIRM_NO_DISPATCH'); assert.equal(count('getQuestions'), reads)
})

for (const action of ['delete', 'course']) test('TP15 retired serial bulk ' + action + ' only permits already-entered call and preserves B selection', async () => {
  const w = await page(); await twoPages(w)
  const op = action === 'delete' ? 'delQuestions' : 'updateQuestions'; deferred.add(op)
  if (action === 'delete') await click(named(w, '批量删除'))
  else { await click(named(w, '批量编辑课程名')); await input(dialog('批量编辑课程名称').querySelector('input'), '批改课程'); await click(modalButton('保存', '批量编辑课程名称')) }
  const old = last(op); assert.equal(count(op), 1, 'serial flow starts only first write')
  user.setStoredCurrentUser(B); listRows = [row(101, 'B同ID题目')]; await settle(); await select(w)
  const before = state(w), reads = count('getQuestions'); old.resolve(ok({})); await settle()
  witness('retired-bulk-' + action, w)
  assert.equal(count(op), 1, 'RETIRED_BULK_NO_NEXT_WRITE'); assert.equal(count('getQuestions'), reads)
  assert.deepEqual(state(w), before, 'RETIRED_BULK_NO_CLEAR_B_SELECTION')
  assert.equal(document.body.textContent.includes('成功删除') || document.body.textContent.includes('成功更新'), false)
})

test('TP16 healthy serial bulk course retains cache-backed off-page records and refreshes exactly once', async () => {
  const w = await page(); await twoPages(w); const reads = count('getQuestions')
  await click(named(w, '批量编辑课程名')); await input(dialog('批量编辑课程名称').querySelector('input'), '新课程')
  await click(modalButton('保存', '批量编辑课程名称'))
  const payloads = writes().map(c => c.payload)
  assert.deepEqual(payloads.map(p => p.id), [101, 201]); assert.ok(payloads.every(p => p.course_name === '新课程' && p.extra === row().extra))
  assert.equal(count('getQuestions'), reads + 1); assert.deepEqual([...w.vm.selectedRowKeys], []); assert.equal(w.vm.bulkCourseVisible, false)
})

test('TP17 File.text completion after principal retirement cannot start import dry-run', async () => {
  const w = await page(); let resolveText
  const e = importEvent(); e.target.files[0].text = () => new Promise(resolve => { resolveText = resolve })
  const work = w.vm.handleJsonImportChange(e); user.setStoredCurrentUser(B); await settle()
  resolveText(JSON.stringify([row()])); await work; await settle()
  assert.equal(count('importDryRun'), 0, 'RETIRED_FILE_TEXT_NO_PREFLIGHT'); assert.equal(count('importExecute'), 0)
})

test('TP18 retired import dry-run response cannot prompt or dispatch execute', async () => {
  const w = await page(); deferred.add('importDryRun')
  const work = w.vm.handleJsonImportChange(importEvent()); await settle(); const old = last('importDryRun')
  user.setStoredCurrentUser(B); await settle(); old.resolve(ok({ existingIds: [101] })); await work; await settle()
  assert.equal(count('importExecute'), 0, 'RETIRED_DRY_RUN_NO_EXECUTE'); assert.equal(swalCalls.length, 0, 'RETIRED_DRY_RUN_NO_PROMPT')
})

test('TP19 retired duplicate-import Swal confirmation cannot dispatch execute', async () => {
  const w = await page(); deferSwal = true
  const work = w.vm.handleJsonImportChange(importEvent()); await settle(); assert.equal(swalPending.length, 1)
  const old = swalPending.shift(); user.setStoredCurrentUser(B); await settle()
  deferSwal = false; old.resolve({ isConfirmed: true }); await work; await settle()
  assert.equal(count('importExecute'), 0, 'RETIRED_IMPORT_CONFIRM_NO_EXECUTE')
})

test('TP20 retired already-entered import execute cannot publish success or refresh B', async () => {
  const w = await page(); deferred.add('importExecute')
  const work = w.vm.handleJsonImportChange(importEvent()); await settle(); const old = last('importExecute')
  user.setStoredCurrentUser(B); await settle(); const reads = count('getQuestions'), prompts = swalCalls.length
  old.resolve(ok({ created: 1 })); await work; await settle()
  assert.equal(count('getQuestions'), reads, 'RETIRED_EXECUTE_NO_REFRESH'); assert.equal(swalCalls.length, prompts, 'RETIRED_EXECUTE_NO_SUCCESS_POPUP')
})

test('TP21 healthy JSON import deny preserves no-overwrite payload; cancel remains zero writes', async () => {
  const w = await page(); swalAnswer = { isDismissed: true }
  await w.vm.handleJsonImportChange(importEvent()); await settle(); assert.equal(count('importExecute'), 0)
  swalAnswer = { isDenied: true }; const reads = count('getQuestions'); const e = importEvent()
  await w.vm.handleJsonImportChange(e); await settle()
  assert.deepEqual(calls.find(c => c.kind === 'importDryRun').payload, { dryRun: true, overwrite: false, items: [row()] })
  assert.deepEqual(calls.find(c => c.kind === 'importExecute').payload, { overwrite: false, items: [row()] })
  assert.equal(count('getQuestions'), reads + 1); assert.equal(e.target.value, '')
})

test('TP22 old native row and export buttons in the same tick cannot resolve B state as their owner', async () => {
  const w = await page(); await select(w)
  const oldEdit = named(w, '编辑'), oldDelete = named(w, '删除'), oldExport = named(w, '导出选中')
  user.setStoredCurrentUser(B); oldEdit.click(); oldDelete.click(); oldExport.click(); await settle()
  assert.equal(writes().length, 0, 'OLD_NATIVE_ROW_NO_WRITE'); assert.equal(swalCalls.length, 0, 'OLD_NATIVE_ROW_NO_PROMPT')
  assert.equal(w.vm.visible, false, 'OLD_NATIVE_ROW_NO_B_EDITOR'); assert.equal(downloads.length, 0)
})

test('TP23 scope retirement during Blob URL creation revokes URL without cross-scope download', async () => {
  const w = await page(); await select(w)
  exportHook = () => user.setStoredCurrentUser(B)
  await click(named(w, '导出选中'))
  assert.equal(blobs.length, 1, 'export reached actual Blob URL boundary'); assert.equal(downloads.length, 0, 'RETIRED_EXPORT_NO_DOWNLOAD')
  assert.deepEqual(revoked, ['blob:synthetic-tiku-principal-1'])
  assert.equal(document.body.textContent.includes('已导出'), false)
})

test('TP24 role loss and logout refuse direct residual handlers and do not reopen editor or dispatch any API', async () => {
  const w = await page(); await select(w); await open(w)
  for (const principal of [{ ...A, role: 'user' }, null]) {
    if (principal) user.setStoredCurrentUser(principal); else user.clearStoredCurrentUser()
    await settle(); const before = calls.length
    w.vm.showModal(row(), 1); w.vm.openBulkCourseModal(); w.vm.triggerJsonImport(); w.vm.handleExportSelected()
    await w.vm.fetchData(); await w.vm.getCourses(); await w.vm.deleteQuestion(101)
    await w.vm.handleDeleteSelected(); await w.vm.handleBulkCourseSubmit(); await w.vm.confirm()
    await w.vm.handleJsonImportChange(importEvent()); await w.vm.handleSelectAll(); await settle()
    assert.equal(calls.length, before, 'NO_ADMIN_RESIDUAL_IO'); assert.equal(w.vm.visible, false); assert.equal(w.vm.bulkCourseVisible, false)
  }
})

test('TP25 same-ID role revoke and restore cannot revive an older list or course operation', async () => {
  const w = await page(); deferred.add('getQuestions'); deferred.add('getCourses')
  w.vm.fetchData(); w.vm.getCourses(); const oldList = last('getQuestions'), oldCourses = last('getCourses')
  user.setStoredCurrentUser({ ...A, role: 'user' }); user.setStoredCurrentUser(A); await settle()
  const currentList = last('getQuestions'), currentCourses = last('getCourses')
  assert.notStrictEqual(currentList, oldList); currentList.resolve(listing([row(202, '恢复后的新题')], 1)); currentCourses.resolve(ok(['恢复课程'])); await settle()
  const before = state(w); oldList.resolve(listing([row(909, '旧角色题')], 99)); oldCourses.resolve(ok(['旧角色课程'])); await settle()
  assert.deepEqual(state(w), before, 'ROLE_EPOCH_CANNOT_REVIVE')
})

test('TP26 healthy current editor close restores its visible native opener and can reopen', async () => {
  const w = await page(), opener = named(w, '编辑')
  opener.focus(); await click(opener); const after = w.vm.questionDialogAfterClose
  await click(modalButton('取消')); document.body.focus(); after(); await settle()
  assert.strictEqual(document.activeElement, opener); await click(opener); assert.equal(w.vm.visible, true)
})


test('TP27 principal switch clears sensitive question/extra filters before one B read; same actor refresh retains conditions', async () => {
  const w = await page(); w.vm.typeValue = 4
  Object.assign(w.vm.searchValue, { question: 'SYNTHETIC_A_PRIVATE_QUESTION', extra: 'SYNTHETIC_A_PRIVATE_EXTRA', source: '7', onlyShowEmptyAnswer: true })
  await click(named(w, '搜索')); const before = calls.filter(c => c.kind === 'getQuestions').at(-1).payload
  await click(named(w, '刷新')); assert.deepEqual(calls.filter(c => c.kind === 'getQuestions').at(-1).payload, before, 'same actor retains submitted filter')
  const reads = count('getQuestions'), courses = count('getCourses'); user.setStoredCurrentUser(B)
  assert.equal(w.vm.searchValue.question || '', '', 'B_SYNC_CLEARS_A_PRIVATE_QUESTION')
  assert.equal(w.vm.searchValue.extra || '', '', 'B_SYNC_CLEARS_A_PRIVATE_EXTRA')
  await settle(); assert.equal(count('getQuestions'), reads + 1, 'B exactly one safe read'); assert.equal(count('getCourses'), courses + 1)
  const b = calls.filter(c => c.kind === 'getQuestions').at(-1)
  assert.equal(b.principal.id, B.id); assert.equal(b.payload.question, ''); assert.equal(b.payload.extra, '')
  assert.ok(!JSON.stringify(b.payload).includes('SYNTHETIC_A_PRIVATE'))
})

test('TP28 old list/course failures after B starts do not publish diagnostics or toasts or clear B loading', async () => {
  const w = await page(); deferred.add('getQuestions'); deferred.add('getCourses')
  w.vm.fetchData(); w.vm.getCourses(); const oldList = last('getQuestions'), oldCourses = last('getCourses')
  user.setStoredCurrentUser(B); await settle(); const before = state(w)
  oldList.reject(new Error('SYNTHETIC_A_LIST_FAILURE')); oldCourses.reject(new Error('SYNTHETIC_A_COURSE_FAILURE')); await settle()
  assert.deepEqual(state(w), before); assert.equal(w.vm.listFailure, null)
  assert.equal(document.body.textContent.includes('获取课程列表失败') || document.body.textContent.includes('获取题目列表失败'), false)
})

test('TP29 captured old native JSON file change is inert in same-tick principal transition', async () => {
  const w = await page(); const oldInput = w.find('input[type="file"]').element
  Object.defineProperty(oldInput, 'files', { configurable: true, value: importEvent().target.files })
  user.setStoredCurrentUser(B); oldInput.dispatchEvent(new Event('change', { bubbles: true })); await settle()
  assert.equal(count('importDryRun'), 0, 'OLD_NATIVE_FILE_NO_PREFLIGHT'); assert.equal(count('importExecute'), 0)
})

test('TP30 old bulk modal native save cannot borrow B scope or overwrite B course draft', async () => {
  const w = await page(); await select(w); await click(named(w, '批量编辑课程名'))
  await input(dialog('批量编辑课程名称').querySelector('input'), 'A旧课程')
  const oldSave = modalButton('保存', '批量编辑课程名称')
  user.setStoredCurrentUser(B); oldSave.click(); await settle()
  assert.equal(count('updateQuestions'), 0, 'OLD_BULK_NATIVE_SAVE_NO_B_WRITE')
  await select(w); await click(named(w, '批量编辑课程名')); await input(dialog('批量编辑课程名称').querySelector('input'), 'B当前课程')
  assert.equal(w.vm.bulkCourseName, 'B当前课程')
})

test('TP31 captured modal onOk callback cannot submit a newly opened B editor', async () => {
  const w = await page(); await open(w)
  const modal = w.findAllComponents(Antd.Modal).find(m => m.props('title') === '详情内容')
  assert.ok(modal); const oldOK = modal.props('onOk'); assert.equal(typeof oldOK, 'function')
  user.setStoredCurrentUser(B); await settle(); await open(w); await input(dialog().querySelector('textarea'), 'B新题面')
  const before = state(w); oldOK(); await settle()
  assert.equal(writes().length, 0, 'OLD_MODAL_CALLBACK_CANNOT_SUBMIT_B'); assert.deepEqual(state(w), before)
})

test('TP32 healthy import overwrite confirmation retains all fields and explicit true choice', async () => {
  const w = await page(); await w.vm.handleJsonImportChange(importEvent()); await settle()
  const execute = calls.find(c => c.kind === 'importExecute')
  assert.deepEqual(execute.payload, { overwrite: true, items: [row()] }); assert.equal(execute.principal.id, A.id)
  assert.ok(swalCalls.some(s => s.title === '检测到重复题目')); assert.ok(swalCalls.some(s => s.title === 'JSON 导入成功'))
})

test('TP33 already-entered single delete completion cannot remove B same-ID selection or refresh B', async () => {
  const w = await page(); deferred.add('delQuestions'); await click(named(w, '删除')); const old = last('delQuestions')
  user.setStoredCurrentUser(B); await settle(); await select(w)
  const before = state(w), reads = count('getQuestions'); old.resolve(ok({})); await settle()
  assert.deepEqual(state(w), before, 'OLD_ROW_DELETE_CANNOT_CLEAR_SAME_ID_B'); assert.equal(count('getQuestions'), reads)
  assert.equal(document.body.textContent.includes('删除成功'), false)
})

test('TP34 logout performs zero additional reads and returning owner gets one clean fresh read', async () => {
  const w = await page(); await select(w); const reads = count('getQuestions'), courses = count('getCourses')
  user.clearStoredCurrentUser(); await settle()
  assert.equal(count('getQuestions'), reads); assert.equal(count('getCourses'), courses)
  user.setStoredCurrentUser(A); await settle()
  assert.equal(count('getQuestions'), reads + 1); assert.equal(count('getCourses'), courses + 1)
  assert.deepEqual([...w.vm.selectedRowKeys], []); assert.deepEqual([...w.vm.selectedQuestions], [])
})

test('TP35 same actor current list failure still exposes real recovery without authorizing any write', async () => {
  const w = await page(); deferred.add('getQuestions'); await click(named(w, '刷新'))
  last('getQuestions').reject(new Error('SYNTHETIC_CURRENT_READ_FAILURE')); await settle()
  assert.ok(w.vm.listFailure); assert.ok(document.body.textContent.includes('获取题目列表失败')); assert.equal(w.vm.tabLoading, false)
  await click(named(w, '重试读取')); last('getQuestions').resolve(listing([row(404, '已恢复')], 1)); await settle()
  assert.equal(w.vm.data[0].question, '已恢复'); assert.equal(writes().length, 0)
})


test('TP36 captured upload preflight rejects stale owner before any native transport can start', async () => {
  const w = await page(); await open(w)
  await click([...dialog().querySelectorAll('[role="tab"]')].find(t => t.textContent.includes('提交附件')))
  const upload = w.findComponent(Antd.Upload.Dragger); assert.ok(upload.exists())
  const beforeUpload = upload.props('beforeUpload') || (() => true)
  assert.equal(beforeUpload({ uid: 'A-accepted-upload', name: 'fixture.txt' }), true, 'healthy current upload is not removed')
  user.setStoredCurrentUser(B); await settle()
  assert.strictEqual(beforeUpload({ uid: 'A-stale-upload', name: 'stale.txt' }), Antd.Upload.LIST_IGNORE, 'STALE_UPLOAD_PREFLIGHT_NO_TRANSPORT')
  assert.equal(writes().length, 0)
})


test('TP37 accepted A native input immediately before principal switch cannot leave stale Antd activity or corrupt B', async () => {
  const w = await page(); await open(w); const textarea = dialog().querySelector('textarea')
  textarea.value = '合法A输入后立即切换'; textarea.dispatchEvent(new Event('input', { bubbles: true }))
  user.setStoredCurrentUser(B); await settle()
  assert.deepEqual(w.vm.formData, {}, 'RETIRED_ACCEPTED_INPUT_DRAFT_CLEARED'); assert.equal(w.vm.visible, false)
  await open(w); assert.equal(w.vm.formData.question, '原始题目'); assert.equal(writes().length, 0)
})


test('TP38 double native create OK admits one call; old A completion cannot clear pending B submit', async () => {
  const w = await page(); deferred.add('createQuestions'); await click(named(w, '添加'))
  await input(dialog().querySelector('textarea'), 'A创建题')
  const oldOK = modalButton('确定'); oldOK.click(); oldOK.click(); await settle()
  assert.equal(count('createQuestions'), 1, 'DOUBLE_NATIVE_OK_ONE_CREATE'); const old = last('createQuestions')
  user.setStoredCurrentUser(B); await settle(); await click(named(w, '添加')); await input(dialog().querySelector('textarea'), 'B创建题')
  await click(modalButton('确定')); const current = last('createQuestions'); assert.notStrictEqual(current, old)
  assert.equal(count('createQuestions'), 2); assert.equal(w.vm.questionSubmitLoading, true, 'B submit owns busy')
  const before = state(w), reads = count('getQuestions')
  old.resolve(ok({})); await settle()
  assert.equal(w.vm.questionSubmitLoading, true, 'OLD_CREATE_CANNOT_RELEASE_B_BUSY'); assert.deepEqual(state(w), before)
  assert.equal(count('getQuestions'), reads); assert.equal(count('createQuestions'), 2)
  current.resolve(ok({})); await settle(); assert.equal(w.vm.questionSubmitLoading, false); assert.equal(w.vm.visible, false)
})


// Targeted error-feedback supplement. Historical 46-case B/M/R receipts stay unchanged.
test('TF01 current native save HTTP rejection keeps editable draft and accepted attachment without an extra refresh', async () => {
  const w = await page(); await open(w); await input(dialog().querySelector('textarea'), '保存失败仍应保留的草稿')
  await click([...dialog().querySelectorAll('[role="tab"]')].find(t => t.textContent.includes('提交附件')))
  const upload = w.findComponent(Antd.Upload.Dragger), file = { uid: 'save-rejected-current-upload', name: 'fixture.txt' }
  assert.ok(upload.exists()); assert.equal((upload.props('beforeUpload') || (() => true))(file), true)
  upload.props('onChange')({ file: { ...file, status: 'done', response: 'SYNTHETIC_SAVE_REJECTED_ATTACHMENT' } }); await settle()
  assert.deepEqual([...w.vm.fileList], ['SYNTHETIC_SAVE_REJECTED_ATTACHMENT'])
  deferred.add('updateQuestions'); const reads = count('getQuestions')
  await click(modalButton('确定')); const save = last('updateQuestions')
  assert.equal(count('updateQuestions'), 1, 'one entered save, not undone')
  save.resolve({ status: 422, message: 'SYNTHETIC_VALIDATION_REJECTED' }); await settle()
  console.log('TIKU_FEEDBACK_SUPPLEMENT ' + JSON.stringify({ name: 'TF01', visible: w.vm.visible, draft: w.vm.formData.question, files: [...w.vm.fileList], readsBefore: reads, readsAfter: count('getQuestions'), writes: writes(), busy: w.vm.questionSubmitLoading, errorVisible: document.body.textContent.includes('修改失败'), falseSuccess: document.body.textContent.includes('修改成功') }))
  assert.equal(w.vm.visible, true, 'HTTP_REJECTED_SAVE_RETAINS_EDITABLE_DIALOG')
  assert.equal(w.vm.formData.question, '保存失败仍应保留的草稿')
  assert.deepEqual([...w.vm.fileList], ['SYNTHETIC_SAVE_REJECTED_ATTACHMENT'], 'HTTP_REJECTED_SAVE_RETAINS_ATTACHMENTS')
  assert.ok(w.vm.$refs.answerBox.getData().includes('SYNTHETIC_SAVE_REJECTED_ATTACHMENT'))
  assert.equal(count('getQuestions'), reads, 'HTTP_REJECTED_SAVE_NO_EXTRA_REFRESH')
  assert.equal(w.vm.questionSubmitLoading, false)
  assert.equal(document.body.textContent.includes('修改失败'), true); assert.equal(document.body.textContent.includes('修改成功'), false)
  assert.equal(count('updateQuestions'), 1)
})

test('TF02 current native row delete HTTP rejection keeps selected record and visible error without an extra refresh', async () => {
  const w = await page(); await select(w); deferred.add('delQuestions')
  const selected = copy(w.vm.selectedQuestions), rows = copy(w.vm.data), reads = count('getQuestions')
  await click(named(w, '删除')); const deletion = last('delQuestions')
  assert.equal(count('delQuestions'), 1, 'one confirmed delete already entered')
  deletion.resolve({ status: 409, message: 'SYNTHETIC_DELETE_REJECTED' }); await settle()
  console.log('TIKU_FEEDBACK_SUPPLEMENT ' + JSON.stringify({ name: 'TF02', selected: [...w.vm.selectedRowKeys], rows: w.vm.data, readsBefore: reads, readsAfter: count('getQuestions'), writes: writes(), errorVisible: document.body.textContent.includes('删除失败'), falseSuccess: document.body.textContent.includes('删除成功') }))
  assert.deepEqual([...w.vm.selectedRowKeys], [101], 'HTTP_REJECTED_DELETE_RETAINS_SELECTION')
  assert.deepEqual(copy(w.vm.selectedQuestions), selected); assert.deepEqual(copy(w.vm.data), rows)
  assert.equal(count('getQuestions'), reads, 'HTTP_REJECTED_DELETE_NO_EXTRA_REFRESH')
  assert.equal(document.body.textContent.includes('删除失败'), true); assert.equal(document.body.textContent.includes('删除成功'), false)
  assert.equal(count('delQuestions'), 1, 'no second write or claimed undo')
})
