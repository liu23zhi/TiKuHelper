const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const helperRoot = process.env.TIKU_WEB_SOURCE || require('node:path').resolve(__dirname, '../..')
require(require('node:path').join(helperRoot,'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { Button, Row, Col, Input } = require('ant-design-vue')
const path = require('node:path')
const webRoot = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
const AnswerBox = require(path.resolve(webRoot, 'src/components/AnswerBox/index.vue'))
let wrapper
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
function open(answer = []) {
  wrapper = mount(AnswerBox, { attachTo: document.body, props: { answer }, global: { plugins: [Button, Row, Col, Input] } })
  return wrapper
}
afterEach(async () => { wrapper?.unmount(); wrapper=undefined; await settle(); document.body.innerHTML='' })
test('AnswerBox textareas have real rendered label/id associations', () => {
  open(['第一条', '第二条'])
  const inputs=wrapper.findAll('textarea')
  assert.equal(inputs.length,2)
  for (const [index,input] of inputs.entries()) {
    assert.ok(input.element.id, 'textarea must have a stable native id')
    assert.ok(input.element.labels.length, 'actual label-for association required, not merely a name attribute')
    assert.equal(input.element.labels[0].textContent.trim(),'答案 '+(index+1))
  }
  assert.notEqual(inputs[0].element.id,inputs[1].element.id)
})
test('AnswerBox icon actions are named and adding focuses the new answer', async () => {
  open(['现有'])
  const add=wrapper.find('[aria-label="添加答案"]')
  assert.ok(add.exists(), 'add icon button needs a readable accessible name')
  assert.ok(wrapper.find('[aria-label="删除第 1 条答案"]').exists())
  await add.trigger('click'); await settle()
  const fields=wrapper.findAll('textarea')
  assert.equal(fields.length,2)
  assert.equal(document.activeElement,fields[1].element)
})
test('deleting the last answer restores focus to the named empty-state add button', async () => {
  open(['唯一答案'])
  const remove=wrapper.find('[aria-label="删除第 1 条答案"]')
  assert.ok(remove.exists())
  remove.element.focus(); await remove.trigger('click'); await settle()
  const add=wrapper.find('[aria-label="添加答案"]')
  assert.ok(add.exists())
  assert.equal(document.activeElement,add.element)
})
test('AnswerBox public data methods preserve repeated free-text answers', async () => {
  open(['重复答案','重复答案'])
  assert.equal(wrapper.vm.getData(),JSON.stringify(['重复答案','重复答案']))
  wrapper.vm.pushAnswer('第三条'); await settle()
  assert.equal(wrapper.vm.getData(),JSON.stringify(['重复答案','重复答案','第三条']))
})

test('deleting a middle row retains survivor ids and focuses the next answer', async () => {
  open(['甲','乙','丙'])
  const ids=wrapper.findAll('textarea').map(input=>input.element.id)
  const remove=wrapper.find('[aria-label="删除第 2 条答案"]')
  assert.ok(remove.exists()); await remove.trigger('click'); await settle()
  const remaining=wrapper.findAll('textarea')
  assert.deepEqual(remaining.map(input=>input.element.id),[ids[0],ids[2]])
  assert.equal(document.activeElement,remaining[1].element)
  assert.equal(wrapper.vm.getData(),JSON.stringify(['甲','丙']))
})
test('AnswerBox instances do not produce duplicate input ids', async () => {
  open(['第一组'])
  const second=mount(AnswerBox,{attachTo:document.body,props:{answer:['第二组']},global:{plugins:[Button,Row,Col,Input]}})
  try {
    const firstId=wrapper.find('textarea').element.id
    const secondId=second.find('textarea').element.id
    assert.ok(firstId && secondId)
    assert.notEqual(firstId,secondId)
  } finally { second.unmount() }
})
test('the editor exposes a polite status update after local row changes', async () => {
  open(['现有'])
  const add=wrapper.find('[aria-label="添加答案"]')
  assert.ok(add.exists()); await add.trigger('click'); await settle()
  const status=wrapper.find('[role="status"]')
  assert.ok(status.exists())
  assert.equal(status.attributes('aria-live'),'polite')
  assert.ok(status.text().includes('2'))
})
test('programmatic pushAnswer does not steal focus from another field', async () => {
  open(['现有'])
  const other=document.createElement('button');other.textContent='外部操作';document.body.appendChild(other);other.focus()
  wrapper.vm.pushAnswer('导入结果');await settle()
  assert.equal(document.activeElement,other)
  other.remove()
})

test('AnswerBox keeps public root attrs and one persistent polite region through empty transition',async()=>{wrapper=mount(AnswerBox,{attachTo:document.body,props:{answer:['唯一']},attrs:{class:'caller-answer','data-caller':'retained'},global:{plugins:[Button,Row,Col,Input]}});assert.equal(wrapper.element.getAttribute('data-caller'),'retained');assert.ok(wrapper.element.classList.contains('caller-answer'));const status=wrapper.find('[role="status"]').element;await wrapper.find('[aria-label="删除第 1 条答案"]').trigger('click');await settle();assert.equal(wrapper.find('[role="status"]').element,status);assert.equal(wrapper.find('[aria-label="添加答案"]').element,document.activeElement)})
