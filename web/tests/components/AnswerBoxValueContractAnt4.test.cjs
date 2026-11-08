const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const webRoot = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(webRoot, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { Button, Row, Col, Input } = require('ant-design-vue')
const AnswerBox = require(process.env.TIKU_ANSWERBOX_SOURCE || path.join(webRoot, 'src/components/AnswerBox/index.vue'))
let wrapper
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
function open(answer = [], warnings = []) {
  wrapper = mount(AnswerBox, {
    attachTo: document.body, props: { answer },
    global: { plugins: [Button, Row, Col, Input], config: { warnHandler: message => warnings.push(message) } },
  })
  return wrapper
}
afterEach(async () => { wrapper?.unmount(); wrapper = undefined; await settle(); document.body.innerHTML = '' })
test('untouched public JSON preserves zero, false, repeated answers, original types and order', async () => {
  const answer = [0, false, '重复答案', '重复答案', '0', 'false', true, 42, ' ']
  open(answer); await settle()
  assert.equal(wrapper.vm.getData(), JSON.stringify(answer))
  assert.deepEqual(wrapper.vm.data.map(item => item.value), answer)
  assert.deepEqual(wrapper.findAll('textarea').map(input => input.element.value), answer.map(String))
})
test('native textarea edit changes only the edited row, not the untouched false or duplicate', async () => {
  const answer = [0, false, '重复答案', '重复答案']
  open(answer); await settle()
  const fields = wrapper.findAll('textarea')
  const ids = fields.map(input => input.element.id)
  await fields[0].setValue('修订答案'); await settle()
  assert.equal(wrapper.vm.getData(), JSON.stringify(['修订答案', false, '重复答案', '重复答案']))
  assert.deepEqual(wrapper.findAll('textarea').map(input => input.element.id), ids)
  assert.deepEqual(answer, [0, false, '重复答案', '重复答案'])
  // Re-entering identical visible text is not an input event and must not coerce false.
  await fields[1].setValue('已编辑'); await settle()
  await fields[1].setValue('false'); await settle()
  assert.equal(wrapper.vm.getData(), JSON.stringify(['修订答案', 'false', '重复答案', '重复答案']))
})
test('programmatic imported zero and false preserve values without stealing focus', async () => {
  open(); await settle()
  const other = document.createElement('button'); other.textContent = '外部操作'; document.body.appendChild(other); other.focus()
  const answer = [false, 0, '重复答案', '重复答案', null, undefined, '']
  for (const value of answer) wrapper.vm.pushAnswer(value)
  await settle()
  assert.equal(document.activeElement, other)
  assert.equal(wrapper.vm.getData(), JSON.stringify([false, 0, '重复答案', '重复答案']))
})
test('adding, editing and deleting a native row retain surviving value types and stable labels', async () => {
  open([0, false]); await settle()
  const originalIds = wrapper.findAll('textarea').map(input => input.element.id)
  await wrapper.find('[aria-label="添加答案"]').trigger('click'); await settle()
  let fields = wrapper.findAll('textarea')
  assert.equal(document.activeElement, fields[2].element)
  assert.equal(wrapper.vm.getData(), JSON.stringify([0, false]))
  await fields[2].setValue('0'); await settle()
  assert.equal(wrapper.vm.getData(), JSON.stringify([0, false, '0']))
  await wrapper.find('[aria-label="删除第 1 条答案"]').trigger('click'); await settle()
  fields = wrapper.findAll('textarea')
  assert.equal(fields[0].element.id, originalIds[1])
  assert.equal(fields[0].element.labels[0].textContent.trim(), '答案 1')
  assert.equal(document.activeElement, fields[0].element)
  assert.equal(wrapper.vm.getData(), JSON.stringify([false, '0']))
})
test('empty values and non-finite numbers do not become JSON null answers; whitespace is unchanged', async () => {
  open([null, undefined, '', NaN, Infinity, -Infinity, 0, false, ' ', '\t', '\n']); await settle()
  assert.equal(wrapper.vm.getData(), JSON.stringify([0, false, ' ', '\t', '\n']))
})
test('non-text display props never warn or rewrite stored booleans and JSON objects without editing', async () => {
  const answer = [false, true, 0, { answer: '原始对象' }, ['原始数组'], null, undefined]
  const warnings = []
  open(answer, warnings); await settle()
  assert.deepEqual(warnings, [], 'real Antd TextArea should not receive unsupported value props')
  assert.deepEqual(wrapper.vm.data.map(item => item.value), answer)
  assert.equal(wrapper.vm.getData(), JSON.stringify(answer.filter(value => value !== null && value !== undefined)))
})
test('queued focus work is retired on unmount while falsey data stays a normal input', async () => {
  open([0, false]); await settle()
  wrapper.vm.addOption()
  wrapper.vm.removeOption(0)
  wrapper.unmount(); wrapper = undefined
  await settle()
  assert.equal(document.querySelector('textarea'), null)
})
