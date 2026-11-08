/* Actual Vue/Antd OptionBox widget. No page save/API/provider or real IO.
 * Frozen fixture accepts TIKU_WEB_SOURCE/UI_TEST_HELPER for native B/M/R.
 * Names are observed from DOM sources; no reader/device/physical keyboard claim.
 * Existing answer toggle/type/getData behavior is intentionally preserved. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter); Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises, DOMWrapper } = require('@vue/test-utils'), { nextTick, defineComponent, h } = require('vue'), Antd = require('ant-design-vue')
const file = path.join(web, 'src/components/OptionBox/index.vue'), sourceHash = () => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex'), initialSHA = sourceHash()
const OptionBox = require(file), roots = [], network = [], errors = []
const initial = ['SYNTHETIC甲', 'SYNTHETIC乙']
const copy = value => JSON.parse(JSON.stringify(value)), compact = value => String(value || '').replace(/\s+/g, ' ').trim()
function trap(kind) { network.push(kind); throw new Error('FORBIDDEN_OPTION_WIDGET_IO_' + kind) }
window.XMLHttpRequest.prototype.open = function () { return trap('XHR') }; globalThis.fetch = window.fetch = () => trap('FETCH'); window.open = () => trap('WINDOW_OPEN')
globalThis.WebSocket = window.WebSocket = class { constructor() { trap('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => trap(name); transport.get = () => trap(name) }
window.addEventListener('error', event => errors.push(String(event.error?.stack || event.message))); window.addEventListener('unhandledrejection', event => errors.push(String(event.reason?.stack || event.reason)))
let computedName, nameLibrary = 'explicit DOM naming-source observation, not full AccName'
try { computedName = require('dom-accessibility-api').computeAccessibleName; nameLibrary = 'installed dom-accessibility-api' } catch (error) { if (error.code !== 'MODULE_NOT_FOUND') throw error }
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
function naming(element) {
  const refs = String(element.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean).map(id => document.getElementById(id)?.textContent || '').join(' ')
  const labels = [...(element.labels || [])].map(label => compact(label.textContent)).filter(Boolean)
  const image = element.querySelector('[role="img"][aria-label]:not([aria-hidden="true"])')
  const sources = { ariaLabel: element.getAttribute('aria-label') || '', ariaLabelledby: compact(refs), labels, title: element.getAttribute('title') || '', placeholder: element.getAttribute('placeholder') || '', text: element.tagName === 'INPUT' ? '' : compact(element.textContent), exposedImageLabel: image?.getAttribute('aria-label') || '' }
  const name = computedName ? computedName(element) : compact(sources.ariaLabel || sources.ariaLabelledby || labels.join(' ') || sources.text || sources.exposedImageLabel || sources.title || sources.placeholder)
  return { method: nameLibrary, name, sources }
}
function rows(w) { return [...w.element.querySelectorAll('.rowStyle')].map(row => ({ row, input: row.querySelector('input'), toggle: row.querySelector('button.CheckStyle'), actions: [...row.querySelectorAll('button')].filter(button => !button.classList.contains('CheckStyle')) })) }
function payload(w) { const result = w.vm.getData(); assert.deepEqual(Object.keys(result).sort(), ['answer', 'options']); return { options: JSON.parse(result.options), answer: JSON.parse(result.answer) } }
function observation(w, stage) { const result = { stage, fields: rows(w).map((row, index) => ({ position: index + 1, value: row.input.value, id: row.input.id, inputName: naming(row.input), toggleName: naming(row.toggle), pressed: row.toggle.getAttribute('aria-pressed'), actions: row.actions.map(naming) })), payload: payload(w) }; console.log('OPTION_WIDGET_DOM ' + JSON.stringify(result)); return result }
async function widget(props = {}, twins = false) {
  const input = { options: copy(initial), answer: ['SYNTHETIC甲'], type: 0, ...copy(props) }
  const Root = twins ? defineComponent({ render: () => h('section', [h(OptionBox, { ...input, options: copy(input.options), answer: copy(input.answer), key: 'first' }), h(OptionBox, { ...input, options: copy(input.options), answer: copy(input.answer), key: 'second' })]) }) : OptionBox
  const root = mount(Root, { ...(twins ? {} : { props: input }), attachTo: document.body, global: { plugins: [Antd], config: { errorHandler: error => errors.push(String(error?.stack || error)) } } }); roots.push(root); await settle()
  const boxes = twins ? root.findAllComponents(OptionBox) : [root]; assert.equal(boxes.length, twins ? 2 : 1)
  for (const box of boxes) observation(box, 'mount')
  return boxes
}
function namedInputRows(w) {
  const values = rows(w).map((row, index) => { const name = naming(row.input).name; assert.ok(name.includes('选项'), 'actual option input has authored option-content purpose, not its value: row ' + (index + 1)); assert.ok(name.includes(String(index + 1)), 'input name distinguishes current one-based row position, not possibly repeated display key'); return name })
  assert.equal(new Set(values).size, values.length, 'each current row has a distinct text-field name within its widget')
}
function noDuplicateIds() { const ids = [...document.querySelectorAll('[id]')].map(node => node.id).filter(Boolean); assert.equal(new Set(ids).size, ids.length, 'if controls have IDs they never collide across rows/widgets; aria-only naming does not require artificial IDs') }
afterEach(async () => {
  for (const root of roots.splice(0)) if (root.exists()) root.unmount()
  Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren()
  console.log('OPTION_WIDGET_SAFETY ' + JSON.stringify({ sourceSHA256: sourceHash(), apiOrProviderCalls: 0, realIO: network, runtimeErrors: errors }))
  assert.equal(sourceHash(), initialSHA); assert.deepEqual(network, []); assert.deepEqual(errors, [])
  network.length = errors.length = 0
})

test('actual option text inputs have distinct authored Chinese option-content names instead of an absent name', async () => { const [w] = await widget(); namedInputRows(w) })
test('actual add icon controls state the Chinese append-option action rather than only the existing icon shape', async () => {
  const [w] = await widget()
  for (const { actions } of rows(w)) { assert.equal(actions.length, 2); const name = naming(actions[0]).name; assert.match(name, /(?:添加|新增).*选项/); assert.doesNotMatch(name, /(?:之后|后面|后方|后添加)/, 'addOption appends to end, not after the clicked row') }
})
test('actual remove icon controls name their exact current one-based row operation', async () => {
  const [w] = await widget()
  for (const [index, { actions }] of rows(w).entries()) { const name = naming(actions[1]).name; assert.match(name, /(?:删除|移除)/); assert.ok(name.includes(String(index + 1))); assert.ok(name.includes('选项')) }
})
test('same Vueapp twin widgets name their own actual inputs and have no accidental ID collisions', async () => {
  const boxes = await widget({}, true); for (const w of boxes) namedInputRows(w); noDuplicateIds()
  assert.notEqual(rows(boxes[0])[0].input, rows(boxes[1])[0].input)
})
test('remove-middle then append preserves old display-key behavior while text names track distinct current rows', async () => {
  const [w] = await widget({ options: ['SYNTHETIC甲', 'SYNTHETIC乙', 'SYNTHETIC丙'], answer: ['SYNTHETIC乙', 'SYNTHETIC丙'] })
  rows(w)[1].actions[1].click(); await settle(); rows(w)[0].actions[0].click(); await settle()
  assert.deepEqual(payload(w), { options: ['SYNTHETIC甲', 'SYNTHETIC丙', ''], answer: ['SYNTHETIC丙'] })
  observation(w, 'remove-middle-then-append'); namedInputRows(w); noDuplicateIds()
})
test('existing answer toggle already has its Chinese name and pressed semantics; old icon sources are not falsely called empty', async () => {
  const [w] = await widget()
  for (const [index, row] of rows(w).entries()) { assert.equal(row.toggle.getAttribute('aria-label'), '切换选项 ' + w.vm.data[index].key + ' 的答案状态'); assert.equal(row.toggle.getAttribute('aria-pressed'), String(index === 0)); for (const action of row.actions) assert.ok(naming(action).name, 'English plus-circle/minus-circle source or later Chinese purpose is already nonempty') }
})
for (const type of [0, 1, 3]) test('original type ' + type + ' ordered getData and independently toggleable checked semantics remain unchanged', async () => {
  const options = copy(initial), answer = ['SYNTHETIC甲'], [w] = await widget({ type, options, answer }); assert.deepEqual(payload(w), { options: initial, answer })
  rows(w)[1].toggle.click(); await settle(); assert.deepEqual(payload(w).answer, initial, 'widget itself permits both checked even for type0; no new radio policy')
  rows(w)[0].toggle.click(); await settle(); assert.deepEqual(payload(w), { options: initial, answer: ['SYNTHETIC乙'] }); assert.deepEqual(options, initial); assert.deepEqual(answer, ['SYNTHETIC甲'])
})
test('actual native input remains focusable and typing changes one local value without changing checked/order contract', async () => {
  const [w] = await widget(), input = rows(w)[0].input; input.focus(); assert.equal(document.activeElement, input)
  await new DOMWrapper(input).setValue('SYNTHETIC更新甲'); await settle(); assert.equal(rows(w)[0].input.value, 'SYNTHETIC更新甲'); assert.deepEqual(payload(w), { options: ['SYNTHETIC更新甲', 'SYNTHETIC乙'], answer: ['SYNTHETIC更新甲'] }); observation(w, 'native-typing')
})
test('actual add and remove actions retain append ordering and repeated selected-value JSON', async () => {
  const [w] = await widget({ options: ['SYNTHETIC相同', 'SYNTHETIC相同'], answer: ['SYNTHETIC相同'] }); assert.deepEqual(payload(w).answer, ['SYNTHETIC相同', 'SYNTHETIC相同'])
  rows(w)[0].actions[0].click(); await settle(); assert.deepEqual(payload(w), { options: ['SYNTHETIC相同', 'SYNTHETIC相同', ''], answer: ['SYNTHETIC相同', 'SYNTHETIC相同'] })
  rows(w)[0].actions[1].click(); await settle(); assert.deepEqual(payload(w), { options: ['SYNTHETIC相同', ''], answer: ['SYNTHETIC相同'] }); observation(w, 'append-remove-preserved')
})
test('empty widget keeps its existing named add action and original public empty-to-one JSON contract', async () => {
  const [w] = await widget({ options: [], answer: [] }); assert.deepEqual(payload(w), { options: [], answer: [] }); const add = w.get('button.addBut').element; assert.ok(naming(add).name.replace(/\s+/g, '').includes('添加'), 'Antd may render 添加 as 添 加; visible Chinese action is retained'); add.click(); await settle(); assert.deepEqual(payload(w), { options: [''], answer: [] }); assert.equal(rows(w).length, 1)
})
test('same-app sibling widgets retain independent local values/check states through actual typing and append', async () => {
  const [first, second] = await widget({}, true); await new DOMWrapper(rows(first)[0].input).setValue('SYNTHETIC仅第一实例'); rows(first)[1].actions[0].click(); await settle(); assert.deepEqual(payload(second), { options: initial, answer: ['SYNTHETIC甲'] }); assert.deepEqual(payload(first), { options: ['SYNTHETIC仅第一实例', 'SYNTHETIC乙', ''], answer: ['SYNTHETIC仅第一实例'] }); noDuplicateIds()
})
