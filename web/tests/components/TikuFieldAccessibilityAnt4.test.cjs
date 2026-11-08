/* Actual TikuComponent/Vue/Antd/currentUser/native table and dialogs. Six
 * candidate controls are measured before claiming gaps. Only synthetic readonly
 * questions-search (existing POST transport semantics) and courses GET mocks.
 * CRUD/import/export/upload/provider/real IO are trapped. No focus shim.
 * Frozen fixture supports TIKU_WEB_SOURCE/UI_TEST_HELPER for B/M/R. */
const { test, beforeEach, afterEach } = require('node:test')
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter); Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'HTMLTextAreaElement', 'CustomEvent']) globalThis[key] = window[key]
const { mount, flushPromises, DOMWrapper } = require('@vue/test-utils'), { nextTick, defineComponent, h, watch } = require('vue'), Antd = require('ant-design-vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const zhCN = require('ant-design-vue/lib/locale/zh_CN').default, user = require(path.join(web, 'src/utils/currentUser.js'))
const A = { id: 63101, username: 'SYNTHETIC_TIKU_FIELDS_A', role: 'admin' }, B = { id: 63102, username: 'SYNTHETIC_TIKU_FIELDS_B', role: 'admin' }
const file = path.join(web, 'src/pages/TikuComponent/index.vue'), sourceHash = () => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex'), initialSHA = sourceHash()
const roots = [], histories = [], reads = [], writes = [], network = [], errors = [], observations = []
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value)), compact = value => String(value || '').replace(/\s+/g, ' ').trim()
const row = type => ({ id: 101, type, question: 'SYNTHETIC 原始题目', options: [0, 1, 3].includes(type) ? '["甲","乙"]' : '[]', answer: '["重复答案","重复答案"]', course_name: 'SYNTHETIC 原始课程', source: 7, source_name: 'SYNTHETIC 来源', extra: '', fixture_zero: 0 })
let Page, rows = [row(4)], autoReads = true
function trap(kind) { network.push(kind); throw new Error('FORBIDDEN_TIKU_FIELDS_IO_' + kind) }
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = {
  getQuestions(payload) {
    const call = { name: 'getQuestions', semantic: 'readonly search (existing POST transport)', payload: clone(payload), actor: clone(user.getStoredCurrentUser()), completed: false }; reads.push(call)
    const promise = new Promise(resolve => { call.resolve = value => { call.completed = true; resolve(value) } })
    if (autoReads) call.resolve({ status: 200, data: { items: clone(rows), total: rows.length } })
    return promise
  },
  getCourses() { reads.push({ name: 'getCourses', actor: clone(user.getStoredCurrentUser()), completed: true }); return Promise.resolve({ status: 200, data: ['SYNTHETIC 原始课程', 'SYNTHETIC 第二课程'] }) },
  ...Object.fromEntries(['updateQuestions', 'delQuestions', 'createQuestions', 'importQuestions', 'uploadFile'].map(name => [name, () => { writes.push(name); throw new Error('FORBIDDEN_TIKU_FIELDS_WRITE_' + name) }])),
}; require.cache[apiPath] = api
const swalPath = require.resolve('sweetalert2'), swal = new Module(swalPath); swal.loaded = true
swal.exports = { fire: () => { throw new Error('FORBIDDEN_TIKU_FIELDS_CONFIRMATION') }, getPopup: () => null, close() {} }; require.cache[swalPath] = swal
window.XMLHttpRequest.prototype.open = () => trap('XHR'); globalThis.fetch = window.fetch = () => trap('FETCH'); window.open = () => trap('WINDOW_OPEN')
window.URL.createObjectURL = () => trap('EXPORT_BLOB'); window.HTMLAnchorElement.prototype.click = () => trap('DOWNLOAD')
globalThis.WebSocket = window.WebSocket = class { constructor() { trap('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => trap(name); transport.get = () => trap(name) }
window.addEventListener('error', event => errors.push(String(event.error?.stack || event.message))); window.addEventListener('unhandledrejection', event => errors.push(String(event.reason?.stack || event.reason)))
const candidates = [
  { key: 'filter-type', label: '类型', scope: 'filter', role: 'combobox' },
  { key: 'filter-question', label: '问题', scope: 'filter' },
  { key: 'filter-empty', label: '无答案', scope: 'filter', checkbox: true },
  { key: 'bulk-course', label: '课程名称', scope: 'bulk', role: 'combobox' },
  { key: 'editor-type', label: '类型', scope: 'editor', role: 'combobox' },
  { key: 'editor-question', label: '问题', scope: 'editor', textarea: true },
]
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 4)); await nextTick(); await flushPromises() }
async function until(check, label) { const start = performance.now(); while (performance.now() - start < 2000) { const value = check(); if (value) return value; await settle() } assert.fail(label) }
function visible(node) { if (!node?.isConnected) return false; for (let n = node; n && n.nodeType === 1; n = n.parentElement) { const style = window.getComputedStyle(n); if (n.hidden || style.display === 'none' || style.visibility === 'hidden') return false } return true }
function button(page, name) { const value = [...page.element.querySelectorAll('button')].find(node => compact(node.textContent).replace(/\s/g, '') === name.replace(/\s/g, '')); assert.ok(value, 'actual native button: ' + name); return value }
function modalNodes(title) { return [...document.querySelectorAll('.ant-modal')].filter(node => compact(node.querySelector('.ant-modal-title')?.textContent) === title && visible(node)) }
async function open(twins = false) {
  user.setStoredCurrentUser(A)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/component', component: { render: () => null } }, { path: '/elsewhere', component: { render: () => null } }] }); await router.push('/adapter/component'); await router.isReady(); histories.push(router.options.history)
  const App = defineComponent({ render: () => h(Antd.ConfigProvider, { locale: zhCN }, { default: () => twins ? h('main', [h(Page, { key: 'first' }), h(Page, { key: 'second' })]) : h(Page) }) })
  const root = mount(App, { attachTo: document.body, global: { plugins: [Antd, router], stubs: { transition: false, 'transition-group': false }, config: { errorHandler: error => errors.push(String(error?.stack || error)) } } }); roots.push(root); await settle()
  const pages = root.findAllComponents(Page); assert.equal(pages.length, twins ? 2 : 1); for (const page of pages) { page.__nativeRoot = root; assert.equal(page.vm.data[0].question, rows[0].question); assert.equal(page.vm.tabLoading, false) }
  return pages
}
async function editor(page) {
  if (!page.vm.visible) { button(page, '编辑').click(); await settle() }
  return until(() => { const form = page.vm.questionDialogForm?.$el, modal = form?.closest('.ant-modal'); return page.vm.questionDialogOwner?.isCurrent() && form?.isConnected && visible(modal) && form.querySelector('textarea') ? modal : null }, 'owned actual editor form/input ready')
}
async function bulk(page) {
  if (page.vm.bulkCourseVisible) return page.__nativeBulk
  const checkbox = page.element.querySelector('tbody input[type="checkbox"]'); assert.ok(checkbox, 'actual Antd row checkbox'); if (!checkbox.checked) { checkbox.click(); await settle() }
  assert.deepEqual([...page.vm.selectedRowKeys], [101]); const before = new Set(modalNodes('批量编辑课程名称')); button(page, '批量编辑课程名').click(); await settle()
  const modal = await until(() => modalNodes('批量编辑课程名称').find(node => !before.has(node) && node.querySelector('input')), 'new owned actual bulk course input ready'); assert.ok(page.vm.bulkCourseOwner?.isCurrent()); page.__nativeBulk = modal; return modal
}
async function scope(page, candidate) { return candidate.scope === 'filter' ? page.element.querySelector('.filter-form') : candidate.scope === 'editor' ? editor(page) : bulk(page) }
async function field(page, candidate) {
  const root = await scope(page, candidate), labels = [...root.querySelectorAll('.ant-form-item-label > label')].filter(label => compact(label.textContent) === candidate.label)
  assert.equal(labels.length, 1, 'one visible candidate label: ' + candidate.key)
  const label = labels[0], item = label.closest('.ant-form-item'), inputs = [...item.querySelectorAll('input:not([type="hidden"]),textarea')].filter(input => input.closest('.ant-form-item') === item)
  assert.equal(inputs.length, 1, 'one actual scalar control: ' + candidate.key); const input = inputs[0]
  if (candidate.checkbox) assert.equal(input.type, 'checkbox'); if (candidate.textarea) assert.equal(input.tagName, 'TEXTAREA'); if (candidate.role) assert.equal(input.getAttribute('role'), candidate.role)
  return { root, label, item, input }
}
function sourceObservation(candidate, value) {
  const { input, label } = value, nativeLabels = [...(input.labels || [])], labelledBy = String(input.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean).map(id => ({ id, text: compact(document.getElementById(id)?.textContent) }))
  const result = { key: candidate.key, labelText: compact(label.textContent), labelFor: label.htmlFor, labelControlMatches: label.control === input, tag: input.tagName, type: input.type || '', role: input.getAttribute('role'), inputId: input.id, nativeLabels: nativeLabels.map(n => ({ text: compact(n.textContent), for: n.htmlFor, outerVisibleLabel: n === label })), ariaLabel: input.getAttribute('aria-label') || '', labelledBy, title: input.title || '', placeholderFallback: input.getAttribute('placeholder') || '', describedBy: input.getAttribute('aria-describedby') || '', hintId: value.item.querySelector('.bulk-course-hint')?.id || '', hintText: compact(value.item.querySelector('.bulk-course-hint')?.textContent), checked: candidate.checkbox ? input.checked : null }
  observations.push(result); console.log('TIKU_FIELDS_NATIVE_DOM ' + JSON.stringify(result)); return result
}
async function associated(page, candidate) {
  const value = await field(page, candidate), { input, label } = value
  sourceObservation(candidate, value); assert.ok(input.id, 'native control ID required for exact visible label: ' + candidate.key)
  assert.equal(label.htmlFor, input.id, 'visible label for resolves exact native control: ' + candidate.key); assert.equal(label.control, input); assert.ok([...(input.labels || [])].includes(label)); assert.equal(document.getElementById(input.id), input)
  if (candidate.checkbox) assert.equal([...(input.labels || [])].filter(n => compact(n.textContent)).length, 1, 'one nonempty named label; empty Antd structural innerlabel is allowed')
  return value
}
beforeEach(() => { rows = [row(4)]; autoReads = true; delete require.cache[file]; Page = require(file) })
afterEach(async () => {
  for (const root of roots.splice(0)) if (root.exists()) root.unmount()
  for (const read of reads) if (!read.completed) read.resolve({ status: 503 })
  Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); for (const history of histories.splice(0)) history.destroy(); user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren()
  console.log('TIKU_FIELDS_NATIVE_SAFETY ' + JSON.stringify({ sourceSHA256: sourceHash(), searchReads: reads.filter(x => x.name === 'getQuestions').length, courseReads: reads.filter(x => x.name === 'getCourses').length, writes, realIO: network, runtimeErrors: errors }))
  assert.equal(sourceHash(), initialSHA); assert.deepEqual(writes, []); assert.deepEqual(network, []); assert.deepEqual(errors, [])
  reads.length = writes.length = network.length = errors.length = 0
})
test('measure all six direct candidates without equating missing id or placeholder fallback with empty native name', async () => {
  const [page] = await open(), found = []
  for (const candidate of candidates) found.push(sourceObservation(candidate, await field(page, candidate)))
  assert.equal(found.length, 6); const checkbox = found.find(x => x.key === 'filter-empty'); assert.ok(checkbox.nativeLabels.every(n => n.text === '' || n.text === '无答案')); assert.equal(page.vm.formData.type, 4); assert.deepEqual([...page.vm.selectedRowKeys], [101]); assert.equal(writes.length, 0)
})
for (const candidate of candidates) test('visible Tiku ' + candidate.key + ' label associates with the exact actual Antd native control', async () => { const [page] = await open(); await associated(page, candidate) })
for (const candidate of candidates) test('actual Antd Tiku ' + candidate.key + ' label activation focuses owned control without writes or unintended reads', async () => {
  const [page] = await open(), { label, input } = await associated(page, candidate), beforeReads = reads.length, beforeForm = clone(page.vm.formData), beforeSelected = [...page.vm.selectedRowKeys], beforeChecked = input.checked
  document.activeElement?.blur(); label.click(); await settle(); assert.equal(document.activeElement, input, 'actual Antd/native activation, no focus shim'); assert.equal(reads.length, beforeReads); assert.deepEqual(clone(page.vm.formData), beforeForm); assert.deepEqual([...page.vm.selectedRowKeys], beforeSelected)
  if (candidate.checkbox) { assert.equal(input.checked, !beforeChecked, 'native label default toggles once'); assert.equal(page.vm.searchValue.onlyShowEmptyAnswer, !beforeChecked); label.click(); await settle(); assert.equal(input.checked, beforeChecked); assert.equal(page.vm.searchValue.onlyShowEmptyAnswer, beforeChecked) }
})
test('bulk course existing Chinese hint programmatically describes the exact AutoComplete input', async () => {
  const [page] = await open(), value = await associated(page, candidates.find(x => x.key === 'bulk-course')), hint = value.item.querySelector('.bulk-course-hint')
  assert.ok(hint); assert.equal(compact(hint.textContent), '支持自由输入；若已存在相近课程名，会在下方显示候选项供快速填充。'); assert.ok(hint.id); assert.ok(String(value.input.getAttribute('aria-describedby') || '').split(/\s+/).includes(hint.id)); assert.equal(document.getElementById(hint.id), hint)
})
test('two same Vueapp instances with simultaneous filters/editor/bulk dialogs have twelve distinct anonymous control IDs', async () => {
  const pages = await open(true), controls = []
  for (const page of pages) for (const candidate of candidates) controls.push((await associated(page, candidate)).input)
  const ids = controls.map(input => input.id); assert.equal(new Set(ids).size, 12); const all = [...document.querySelectorAll('[id]')].map(n => n.id).filter(Boolean); assert.equal(new Set(all).size, all.length)
  for (const id of ids) for (const secret of [A.username, B.username, String(A.id), String(B.id)]) assert.ok(!id.includes(secret)); assert.equal(writes.length, 0)
})
test('original filter draft/type/checkbox selection uses native controls and readonly query only when Search is activated', async () => {
  const [page] = await open(), before = reads.filter(x => x.name === 'getQuestions').length, question = page.get('input[placeholder="搜索问题"]')
  await question.setValue('SYNTHETIC_筛选%_'); await settle(); const select = page.findAllComponents(Antd.Select).find(x => x.element.closest('.filter-form')); select.vm.$emit('update:value', 0); await settle()
  const checkbox = page.element.querySelector('.filter-form input[type="checkbox"]'); checkbox.click(); await settle(); assert.equal(page.vm.searchValue.onlyShowEmptyAnswer, true); assert.equal(reads.filter(x => x.name === 'getQuestions').length, before)
  button(page, '搜索').click(); await settle(); const query = reads.filter(x => x.name === 'getQuestions').at(-1).payload; assert.equal(query.type, 0); assert.equal(query.question, 'SYNTHETIC_筛选%_'); assert.equal(query.onlyShowEmptyAnswer, true); assert.equal(query.pageNo, 0); assert.equal(query.pageSize, 10)
})
test('original editor opens all six supported type codes read-only and preserves repeated free-text answers and table/selection snapshots', async () => {
  for (const type of [0, 1, 2, 3, 4, -4]) {
    rows = [row(type)]; const [page] = await open(); const before = clone(page.vm.data[0]); await editor(page); assert.equal(page.vm.formData.type, type); assert.equal(page.vm.formData.answer, rows[0].answer); assert.deepEqual(clone(page.vm.data[0]), before); assert.notStrictEqual(page.vm.formData, page.vm.data[0]); assert.equal(writes.length, 0)
    page.__nativeRoot.unmount(); await settle()
  }
})
test('ordinary identity has no question reads and already-delivered owner retirement clears data selection and dialogs without mutations', async () => {
  user.setStoredCurrentUser(A); const [page] = await open(); await bulk(page); await editor(page); const rowCount = reads.filter(x => x.name === 'getQuestions').length
  user.setStoredCurrentUser({ ...B, role: 'user' }); assert.deepEqual([...page.vm.data], []); assert.deepEqual([...page.vm.selectedRowKeys], []); assert.equal(page.vm.visible, false); assert.equal(page.vm.bulkCourseVisible, false); await settle(); assert.equal(reads.filter(x => x.name === 'getQuestions').length, rowCount); assert.ok(page.text().includes('当前身份无题库管理权限'))
})

// The custom input slot must preserve the actual Select->owner value event chain.
test('original native AutoComplete typing updates its owned model once with no new reads or selection mutation', async () => {
  const [page] = await open(), value = await field(page, candidates.find(x => x.key === 'bulk-course'))
  const auto = page.findComponent(Antd.AutoComplete), select = auto.findComponent(Antd.Select)
  assert.ok(auto.exists() && select.exists(), 'real mounted AutoComplete and its actual Select event publisher')
  const beforeReads = reads.length, selected = [...page.vm.selectedRowKeys], beforeEvents = select.emitted('update:value')?.length || 0, commits = []
  const stop = watch(() => page.vm.bulkCourseName, current => commits.push(current), { flush: 'sync' })
  const typed = 'SYNTHETIC 新课程_原生输入'
  try {
    value.input.value = typed
    await new DOMWrapper(value.input).trigger('input'); await settle()
    const events = select.emitted('update:value') || []
    console.log('TIKU_FIELDS_NATIVE_COURSE_COMMIT ' + JSON.stringify({ value: value.input.value, model: page.vm.bulkCourseName, commits, selectUpdates: events.length - beforeEvents, readsBefore: beforeReads, readsAfter: reads.length, nativeInputClasses: value.input.className, selectClasses: auto.element.className }))
    assert.equal(page.vm.bulkCourseName, typed); assert.equal(value.input.value, typed)
    assert.deepEqual(commits, [typed], 'one actual owned reactive model transition')
    assert.equal(events.length - beforeEvents, 1, 'one actual Select update:value event, not callback echo')
    assert.deepEqual(events.at(-1), [typed]); assert.equal(reads.length, beforeReads)
    assert.deepEqual([...page.vm.selectedRowKeys], selected); assert.ok(page.vm.bulkCourseOwner?.isCurrent()); assert.equal(writes.length, 0)
    if (auto.element.classList.contains('ant-select-customize-input')) assert.ok(value.input.classList.contains('ant-input'), 'custom input retains official Antd Input style carrier; no geometry/contrast claim')
  } finally { stop() }
})
