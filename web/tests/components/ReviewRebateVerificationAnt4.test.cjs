const assert = require('node:assert/strict')
const Module = require('node:module')
const { afterEach, test } = require('node:test')
const { setTimeout: delay } = require('node:timers/promises')
require('../helpers/vue-dom.cjs')
global.getComputedStyle = element => window.getComputedStyle(element)
const { flushPromises, mount } = require('@vue/test-utils')
const { h, ref } = require('vue')
const Antd = require('ant-design-vue')

const claim = { id: 41, status: 'approved' }
const invalid = { id: 73, result: 'invalid', reason: '评价已删除', evidence: '复核截图' }
const empty = { verification: null, clawback: null }
const deduction = { deductedAmount: 1.234567, shortfallAmount: 0.765433 }
const calls = { read: [], save: [], deduct: [] }
const roots = new Set()
let currentRecord = empty

const apiPath = require.resolve('../../src/api/ReviewRebateApi.js')
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  async getReviewRebateVerification(...args) {
    calls.read.push(args)
    return { status: 200, data: currentRecord }
  },
  recordReviewRebateVerification(...args) {
    return new Promise(resolve => calls.save.push({ args, resolve }))
  },
  clawBackReviewRebate(...args) {
    return new Promise(resolve => calls.deduct.push({ args, resolve }))
  },
}
require.cache[apiPath] = api
const VerificationDialog = require('../../src/pages/ReviewRebateAdmin/VerificationDialog.vue')

async function open(verification = null) {
  currentRecord = { verification, clawback: null }
  const visible = ref(true)
  const allowed = ref(true)
  const root = mount({
    setup() {
      return () => visible.value ? h(VerificationDialog, {
        record: claim,
        canManage: () => allowed.value,
        onClose: () => { visible.value = false },
      }) : null
    },
  }, { attachTo: document.body, global: { plugins: [Antd] } })
  roots.add(root)
  await flushPromises()
  return { allowed, dialog: root.getComponent(VerificationDialog) }
}

afterEach(async () => {
  for (const root of roots) root.unmount()
  roots.clear()
  await flushPromises()
  document.body.replaceChildren()
  currentRecord = empty
  for (const requests of Object.values(calls)) requests.length = 0
})

function findButton(label, scope = document) {
  return [...scope.querySelectorAll('button')].find(button => button.textContent.replace(/\s+/g, '') === label)
}

function button(label, scope) {
  const element = findButton(label, scope)
  assert.ok(element, `expected a native button named ${label}`)
  return element
}

function find(selector) {
  const element = document.querySelector(selector)
  assert.ok(element, `expected a DOM element matching ${selector}`)
  return element
}

async function waitForElement(selector) {
  const deadline = Date.now() + 2000
  while (!document.querySelector(selector) && Date.now() < deadline) await delay(10)
  return find(selector)
}

function enterText(selector, value) {
  const control = find(selector)
  control.value = value
  control.dispatchEvent(new Event('input', { bubbles: true }))
}

async function fillVerification(label) {
  find('.ant-modal .ant-select-selector').dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }))
  const menu = await waitForElement('.ant-select-dropdown:not(.ant-select-dropdown-hidden) .ant-select-item-option-content')
  const options = menu.closest('.ant-select-dropdown').querySelectorAll('.ant-select-item-option-content')
  const option = [...options].find(item => item.textContent === label)
  assert.ok(option, `expected a native result option ${label}`)
  option.click()
  enterText('input[id$="-reason"]', invalid.reason)
  enterText('textarea[id$="-evidence"]', invalid.evidence)
  await flushPromises()
}

async function confirmDeduction() {
  button('追缴赠金').click()
  const popup = await waitForElement('.ant-popconfirm:not(.ant-popover-hidden) .ant-popconfirm-inner-content')
  assert.match(popup.textContent, /仅扣减可用赠金，余额不足部分记入差额/)
  return button('确认追缴', popup)
}

test('a native Ant Form submit records the verification and exposes clawback only for invalid', async () => {
  await open({ ...invalid, result: 'confirmed' })
  assert.deepEqual(calls.read, [[claim.id]])
  assert.match(find('.ant-modal').textContent, /评价仍有效/)
  assert.equal(findButton('追缴赠金'), undefined)
  assert.equal(button('保存核验结论').disabled, true)

  await fillVerification('无法确认')
  const firstSubmit = button('保存核验结论')
  assert.equal(firstSubmit.type, 'submit')
  assert.equal(firstSubmit.disabled, false)
  firstSubmit.click()
  await flushPromises()
  assert.equal(calls.save.length, 1)
  assert.deepEqual(calls.save[0].args, [claim.id, {
    result: 'inconclusive', reason: invalid.reason, evidence: invalid.evidence,
  }])

  calls.save[0].resolve({ status: 201, data: { ...invalid, result: 'inconclusive' } })
  await flushPromises()
  assert.match(find('.ant-descriptions').textContent, /无法确认/)
  assert.equal(findButton('追缴赠金'), undefined)

  await fillVerification('评价无效')
  button('保存核验结论').click()
  await flushPromises()
  assert.equal(calls.save.length, 2)
  assert.deepEqual(calls.save[1].args, [claim.id, {
    result: 'invalid', reason: invalid.reason, evidence: invalid.evidence,
  }])
  calls.save[1].resolve({ status: 201, data: invalid })
  await flushPromises()
  assert.match(find('.ant-descriptions').textContent, /评价无效/)
  assert.equal(button('追缴赠金').disabled, false)
  assert.equal(calls.deduct.length, 0)
})

test('confirming clawback sends only the stored verification ID and shows the exact amounts once', async () => {
  const { dialog } = await open(invalid)
  const confirm = await confirmDeduction()
  assert.equal(calls.deduct.length, 0)
  confirm.click()
  confirm.click()
  await flushPromises()
  assert.equal(calls.deduct.length, 1)
  assert.deepEqual(calls.deduct[0].args, [claim.id, invalid.id])
  assert.equal(button('追缴赠金').disabled, true)

  calls.deduct[0].resolve({ status: 200, data: deduction })
  await flushPromises()
  const results = [...document.querySelectorAll('.ant-descriptions')].find(item => item.textContent.includes('追缴结果'))
  assert.ok(results)
  const rows = [...results.querySelectorAll('tr')].map(row => row.textContent)
  assert.deepEqual(rows, ['实际扣回¥ 1.234567', '未追回¥ 0.765433'])
  assert.deepEqual(dialog.emitted('changed'), [[]])
  assert.equal(findButton('追缴赠金'), undefined)
})

test('closing a dialog during a pending clawback discards its late result', async () => {
  const { dialog } = await open(invalid)
  const confirm = await confirmDeduction()
  confirm.click()
  await flushPromises()
  assert.equal(calls.deduct.length, 1)

  find('.ant-modal-close').click()
  await flushPromises()
  assert.deepEqual(dialog.emitted('close'), [[]])
  assert.equal(document.querySelector('.ant-modal'), null)

  await open(invalid)
  calls.deduct[0].resolve({ status: 200, data: deduction })
  await flushPromises()
  assert.equal(dialog.emitted('changed'), undefined)
  assert.doesNotMatch(find('.ant-modal').textContent, /追缴结果|1\.234567|0\.765433/)
  assert.equal(button('追缴赠金').disabled, false)
})

test('a late result after permissions are revoked does not publish amounts or emit a change', async () => {
  const { allowed, dialog } = await open(invalid)
  const confirm = await confirmDeduction()
  confirm.click()
  await flushPromises()
  assert.equal(calls.deduct.length, 1)

  allowed.value = false
  await flushPromises()
  calls.deduct[0].resolve({ status: 200, data: deduction })
  await flushPromises()
  assert.equal(dialog.emitted('changed'), undefined)
  assert.doesNotMatch(document.body.textContent, /追缴结果|1\.234567|0\.765433/)
})
