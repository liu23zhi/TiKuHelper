// Real Vue/Antd4/currentUser and real OptionBox/AnswerBox. Only CRUD is synthetic.
// TIKU_WEB_SOURCE selects a pristine archived tree for red/rollback runs without rewriting live sources.
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises, DOMWrapper } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const currentUser = require(path.join(web, 'src/utils/currentUser.js'))
const PAGE = path.join(web, 'src/pages/TikuComponent/index.vue')
const OptionBox = require(path.join(web, 'src/components/OptionBox/index.vue'))
const AnswerBox = require(path.join(web, 'src/components/AnswerBox/index.vue'))
const actor = { id: 9301, username: 'synthetic-tiku-admin-a', role: 'admin' }
const copy = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const row = type => ({ id: 101, type, question: 'SYNTHETIC 原始题面', options: '["甲","乙"]',
  answer: '["甲","甲"]', course_name: 'SYNTHETIC 原始课程', source: 7,
  source_name: 'SYNTHETIC 来源', extra: 'SYNTHETIC 低频拓展字段', custom_fixture_field: '保留附加字段' })
let rows = [row(4)], calls = [], wrappers = [], histories = [], vueErrors = []
const ok = data => ({ status: 200, data })
const apiPath = path.join(web, 'src/api/TikuComponentApi.js')
const api = new Module(apiPath); api.loaded = true
api.exports = Object.fromEntries(['getCourses', 'getQuestions', 'delQuestions', 'updateQuestions', 'createQuestions', 'importQuestions'].map(name => [name, payload => {
  calls.push({ name, payload: copy(payload), principal: copy(currentUser.getStoredCurrentUser()) })
  return Promise.resolve(name === 'getQuestions' ? ok({ items: copy(rows), total: rows.length }) : name === 'getCourses' ? ok(['SYNTHETIC 原始课程']) : ok({}))
}]))
require.cache[apiPath] = api
const writes = () => calls.filter(call => ['updateQuestions', 'createQuestions', 'delQuestions', 'importQuestions'].includes(call.name))
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 3)); await nextTick() }
async function click(element) { assert.ok(element, 'native control exists'); element.click(); await settle() }
function named(w, name) {
  const button = w.findAll('button').find(button => button.text().replace(/\s/g, '') === name.replace(/\s/g, ''))
  assert.ok(button, 'actual native button ' + name); return button.element
}
function dialog() {
  const owner = wrappers.at(-1)?.vm.questionDialogForm
  const owned = (owner?.$el || owner)?.closest?.('.ant-modal')
  const found = owned
  assert.ok(found && !found.hidden && found.style.display !== 'none', 'current component owns the visible real Antd details dialog'); return found
}
function modalButton(name) {
  const labels = name === '确定' ? ['确定', 'OK', '确认'] : [name, 'Cancel']
  const button = [...dialog().querySelectorAll('.ant-modal-footer button')].find(button => labels.includes(button.textContent.replace(/\s/g, '')))
  assert.ok(button, 'actual Antd modal button ' + name); return button
}
async function input(element, value) {
  const w = wrappers.at(-1)
  let control, ready = false
  for (let frame = 0; frame < 30; frame += 1) {
    await nextTick(); await flushPromises()
    control = w.findAllComponents(element.tagName === 'TEXTAREA' ? Antd.Textarea : Antd.Input)
      .find(item => item.element === element || item.element.contains(element))
    ready = Boolean(element.isConnected && dialog().contains(element) && !element.disabled
      && w.vm.questionDialogOwner?.isCurrent() && control
      && element.value === String(control.props('value') ?? '')
      && typeof control.vm.$.vnode.props['onUpdate:value'] === 'function')
    if (ready) break
    await new Promise(resolve => requestAnimationFrame(resolve))
  }
  assert.ok(ready, 'current owned native editor input has its controlled value and update binding rendered')
  const before = control.props('value'), updates = control.emitted('update:value')?.length || 0
  element.value = value
  // Keep one real bubbling input; VTU handles Vue's capture/attached timestamp boundary.
  await new DOMWrapper(element).trigger('input'); await settle()
  console.log('TIKU_EDITOR_INPUT_COMMIT_TRACE ' + JSON.stringify({ type: w.vm.formData.type, before, input: value,
    committed: control.props('value'), updates: (control.emitted('update:value')?.length || 0) - updates,
    ownerCurrent: w.vm.questionDialogOwner.isCurrent(), childValues: w.findComponent(OptionBox).exists()
      ? w.findComponent(OptionBox).vm.data.map(item => item.value) : w.findComponent(AnswerBox).vm.data.map(item => item.value) }))
  assert.equal(control.props('value'), value, 'one current native input is rendered into the controlled editor before confirm')
  assert.equal((control.emitted('update:value')?.length || 0) - updates, 1, 'exactly one native input update is observed')
}
async function page(type, overrides = {}) {
  if (arguments.length === 0) type = 4
  rows = [{ ...row(type), ...overrides }]; currentUser.setStoredCurrentUser(actor)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/component', component: require(PAGE) }] })
  await router.push('/adapter/component'); await router.isReady(); histories.push(router.options.history)
  const w = mount(require(PAGE), { attachTo: document.body, global: {
    plugins: [router, Antd], stubs: { transition: false, 'transition-group': false },
    config: { errorHandler: error => vueErrors.push({ name: error.name, message: error.message }) },
  } })
  wrappers.push(w); await settle(); return w
}
async function waitForEditor(w) {
  const deadline = Date.now() + 1000
  let ready = false
  const isReady = () => {
    const option = w.findComponent(OptionBox), answer = w.findComponent(AnswerBox)
    const child = option.exists() ? option : answer.exists() ? answer : null
    const owner = w.vm.questionDialogOwner, form = w.vm.questionDialogForm?.$el
    const component = w.findAllComponents(Antd.Modal).find(item => item.props('title') === '详情内容' && item.props('open'))
    const ref = option.exists() ? w.vm.$refs.optionBox : w.vm.$refs.answerBox
    const controls = child?.findAll('input.ant-input, textarea') || []
    return Boolean(owner?.isCurrent() && form?.isConnected && child && ref?.$el === child.element
      && form.contains(child.element) && controls.length === child.vm.data.length
      && controls.every((input, index) => input.element.value === String(child.vm.data[index].value ?? ''))
      && typeof component?.vm.$.vnode.props.onOk === 'function')
  }
  while (!(ready = isReady()) && Date.now() < deadline) {
    await new Promise(resolve => requestAnimationFrame(resolve)); await settle()
  }
  assert.ok(w.findComponent(OptionBox).exists() || w.findComponent(AnswerBox).exists(), 'a real editor becomes ready, independent of expected type')
  assert.ok(ready, 'current modal form, selected real child ref, native inputs and confirm binding are ready')
}
async function open(w) { await click(named(w, '编辑')); await waitForEditor(w) }

function branch(w) { return { option: w.findComponent(OptionBox).exists(), answer: w.findComponent(AnswerBox).exists() } }
function witness(name, w) {
  console.log('TIKU_EDITOR_TYPE_OBSERVED ' + JSON.stringify({ name, type: w.vm.formData.type, branch: branch(w),
    visible: w.vm.visible, loading: w.vm.questionSubmitLoading, errors: vueErrors,
    writes: writes(), messages: [...document.querySelectorAll('.ant-message-notice-content')].map(node => node.textContent), nativeBrowser: false }))
}
function expectedPayload(type, answer = '["甲","甲"]', overrides = {}) { return { ...row(type), answer, ...overrides } }
function assertHealthyWrite(w, expected, operation = 'updateQuestions') {
  assert.deepEqual(vueErrors, [], 'EDITOR_NO_UNHANDLED_REF_EXCEPTION')
  assert.equal(writes().length, 1, 'one native OK dispatches one CRUD write')
  assert.equal(writes()[0].name, operation)
  assert.deepEqual(writes()[0].payload, operation === 'createQuestions' ? [expected] : expected, 'literal payload retains type, answer ordering and every low-frequency/unknown field')
  assert.deepEqual(writes()[0].principal, actor)
  assert.equal(w.vm.visible, false); assert.equal(w.vm.questionSubmitLoading, false)
}
function assertVisibleFailure(w, draft, childData, notice) {
  assert.deepEqual(vueErrors, [], 'EDITOR_FAILURE_IS_HANDLED_NOT_A_VUE_EXCEPTION')
  assert.equal(writes().length, 0, 'invalid editor must not call CRUD')
  assert.equal(w.vm.visible, true, 'failed editor remains open')
  assert.deepEqual(copy(w.vm.formData), draft, 'failed editor retains literal draft')
  if (childData) assert.deepEqual(copy(childData.read()), childData.before, 'failed editor retains child answer draft')
  assert.equal(w.vm.questionSubmitLoading, false, 'failed editor is retryable')
  assert.equal(w.vm.questionDialogOwner.pending, false, 'failed editor releases pending flag')
  assert.ok([...document.querySelectorAll('.ant-message-notice-content')].some(node => node.textContent.includes(notice)), 'current failure has the fixed visible validation/editor error')
}
afterEach(async () => {
  for (const w of wrappers) if (w.exists()) w.unmount()
  wrappers = []; await settle()
  for (const history of histories) history.destroy()
  histories = []; calls = []; vueErrors = []; rows = [row(4)]
  currentUser.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear()
  Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren(); delete require.cache[PAGE]
})

// Historical malformed substring matches actually reached the wrong ref before the fix.
// Submit before asserting the expected branch so pristine runs capture the actual native-OK TypeError.
for (const type of [10, 13, -1]) test('ET01 historical substring type uses free-text editor and saves without wrong ref: ' + type, async () => {
  const w = await page(type); await open(w)
  const opened = branch(w)
  await click(modalButton('确定')); witness('historical-' + type, w)
  assert.deepEqual(vueErrors, [], 'HISTORICAL_TYPE_NATIVE_OK_MUST_NOT_THROW')
  assert.deepEqual(opened, { option: false, answer: true }, 'unknown historical types are non-objective')
  assertHealthyWrite(w, expectedPayload(type))
})

for (const type of [0, 1, 3, '0', '1', '3', '01', ' +3 ', '-0']) test('ET02 exact objective numeric/string type mounts real OptionBox and serializes its edited values: ' + JSON.stringify(type), async () => {
  const w = await page(type); await open(w)
  assert.deepEqual(branch(w), { option: true, answer: false })
  assert.equal(w.findComponent(OptionBox).props('type'), Number(type) || 0, 'real OptionBox receives the same normalized numeric type')
  const inputs = [...dialog().querySelectorAll('input.ant-input')]
  assert.equal(inputs.length, 2, 'real OptionBox option inputs')
  await input(inputs[1], '乙已编辑')
  await click(dialog().querySelector('button[aria-label="切换选项 B 的答案状态"]'))
  await click(modalButton('确定')); witness('objective-' + JSON.stringify(type), w)
  assertHealthyWrite(w, expectedPayload(Number(type) || 0, '["甲","乙已编辑"]', { options: '["甲","乙已编辑"]' }))
})

for (const type of [2, 4, -4, 15, '2', '4', '-4', '10', '13', '-1', '15', 30, 100, 2147483647, -2147483648, '2147483647', '-2147483648']) test('ET03 non-objective numeric/string and unknown types preserve duplicate text and all fields: ' + JSON.stringify(type), async () => {
  const w = await page(type); await open(w)
  assert.deepEqual(branch(w), { option: false, answer: true }, 'only exact 0/1/3 are objective')
  const answers = [...dialog().querySelectorAll('.answer-editor textarea')]
  assert.equal(answers.length, 2, 'real AnswerBox retains repeated rows')
  await input(dialog().querySelector('textarea'), 'SYNTHETIC 修改题面')
  await input(answers[0], '重复的编辑答案'); await input(answers[1], '重复的编辑答案')
  await click(modalButton('确定')); witness('free-text-' + JSON.stringify(type), w)
  assertHealthyWrite(w, expectedPayload(Number(type), '["重复的编辑答案","重复的编辑答案"]', { question: 'SYNTHETIC 修改题面' }))
})

// These are not integer type strings: parseInt must not silently reinterpret them as 0/1/3.
for (const type of [null, undefined, '', '   ', 'abc', '0junk', '1e2', '0x10', '3.5', 1.5, true, [], {}, 2147483648, -2147483649, '2147483648', '-2147483649']) test('ET04 invalid/missing type never selects objective and cannot write a coerced type: ' + JSON.stringify(type), async () => {
  const w = await page(type); await open(w)
  assert.deepEqual(branch(w), { option: false, answer: true }, 'invalid type has no objective substring/coercion')
  await input(dialog().querySelector('textarea'), 'SYNTHETIC 待修正题型草稿')
  const draft = copy(w.vm.formData), child = w.findComponent(AnswerBox)
  const childData = { before: copy(child.vm.data), read: () => child.vm.data }
  await click(modalButton('确定')); witness('invalid-' + JSON.stringify(type), w)
  assertVisibleFailure(w, draft, childData, '题型必须为32位整数，请重新选择。')
})

for (const type of [0, 4]) test('ET05 missing selected child ref reports error, keeps draft and makes zero writes: ' + type, async () => {
  const w = await page(type); await open(w)
  const child = w.findComponent(type === 0 ? OptionBox : AnswerBox)
  assert.ok(child.exists(), 'real child mounted before fault injection')
  await input(dialog().querySelector('textarea'), 'SYNTHETIC 缺失ref仍保留草稿')
  const draft = copy(w.vm.formData), childData = { before: copy(child.vm.data), read: () => child.vm.data }
  // Inject only a ref-lifetime failure. Do not stub either editor's renderer or serialization logic.
  w.vm.$.refs[type === 0 ? 'optionBox' : 'answerBox'] = null
  await click(modalButton('确定')); witness('missing-ref-' + type, w)
  assertVisibleFailure(w, draft, childData, '题目编辑器尚未就绪，请稍后重试。')
})

test('ET06 native create retains objective JSON contract and uses array payload once', async () => {
  const w = await page(); await click(named(w, '添加')); await waitForEditor(w)
  await input(dialog().querySelector('textarea'), 'SYNTHETIC 新建题目')
  const add = [...dialog().querySelectorAll('button')].find(button => button.textContent.replace(/\s/g, '') === '添加')
  await click(add)
  await input(dialog().querySelector('input.ant-input'), '新建选项')
  await click(dialog().querySelector('button[aria-label="切换选项 A 的答案状态"]'))
  await click(modalButton('确定')); witness('create', w)
  assertHealthyWrite(w, { type: 0, question: 'SYNTHETIC 新建题目', options: '["新建选项"]', answer: '["新建选项"]' }, 'createQuestions')
})

async function chooseType(label) {
  dialog().querySelector('.ant-select-selector').dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
  await settle()
  const option = [...document.querySelectorAll('.ant-select-dropdown:not(.ant-select-dropdown-hidden) .ant-select-item-option')].find(node => node.textContent.trim() === label)
  assert.ok(option, 'real Antd type option ' + label)
  await click(option)
}
for (const [start, selected, label] of [[10, 0, '单选题'], [0, 4, '简答题']]) test('ET07 native type selection changes mounted editor and submit follows that same branch: ' + start + ' -> ' + selected, async () => {
  const w = await page(start); await open(w)
  await chooseType(label)
  assert.equal(w.vm.formData.type, selected)
  assert.deepEqual(branch(w), { option: selected === 0, answer: selected !== 0 })
  await click(modalButton('确定')); witness('native-type-switch-' + start + '-' + selected, w)
  assertHealthyWrite(w, expectedPayload(selected, selected === 0 ? '["甲"]' : '["甲","甲"]'))
})

test('ET08 historical free-text native save keeps repeated answers and 0/false value types in order', async () => {
  const answer = '["重复答案","重复答案",0,false]'
  const w = await page(13, { answer }); await open(w)
  assert.deepEqual(branch(w), { option: false, answer: true })
  const child = w.findComponent(AnswerBox)
  assert.deepEqual(child.vm.data.map(item => item.value), ['重复答案', '重复答案', 0, false], 'real child receives original ordered answer values')
  await click(modalButton('确定')); witness('free-text-falsey-preservation', w)
  assertHealthyWrite(w, expectedPayload(13, answer))
})