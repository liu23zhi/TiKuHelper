const assert = require('node:assert/strict')
const Module = require('node:module')
const { afterEach, test } = require('node:test')
const { setTimeout: delay } = require('node:timers/promises')
require('../helpers/vue-dom.cjs')
global.getComputedStyle = element => window.getComputedStyle(element)
const { flushPromises, mount } = require('@vue/test-utils')
const Antd = require('ant-design-vue')
const user = require('../../src/utils/currentUser.js')
const { clearScriptPurchaseIntent } = require('../../src/utils/scriptPurchaseIntent.mjs')

const firstAccount = { id: 111, username: 'purchase_owner' }
const secondAccount = { id: 222, username: 'other_owner' }
const quote = '12.345678'
const okay = data => ({ status: 200, data })
const codeFor = digit => `sac_${digit.repeat(64)}`
const receipt = (entitlementId, code, status = 'unused') => ({
  entitlementId, code, status, price: quote, createdAt: '2026-11-20T04:00:00Z',
})
const history = (items, total = items.length) => okay({ items, total })
const historyRequests = []
const purchaseRequests = []
const mounts = new Set()
let readHistory = async () => history([])
const offer = price => okay({ enabled: true, price })
let readOffer = async () => offer(quote)

const apiPath = require.resolve('../../src/api/TikuComponentApi.js')
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  getScriptActivationCodePrice: () => readOffer(),
  getScriptActivationCodePurchases(params) {
    historyRequests.push(params)
    return readHistory(params)
  },
  purchaseScriptActivationCode(key, quotedPrice) {
    return new Promise((resolve, reject) => purchaseRequests.push({ key, quotedPrice, resolve, reject }))
  },
}
require.cache[apiPath] = api
const Purchases = require('../../src/pages/ScriptManagement/ScriptActivationPurchases.vue')

async function open(account = firstAccount) {
  user.setStoredCurrentUser(account)
  const wrapper = mount(Purchases, {
    attachTo: document.body,
    global: { plugins: [Antd] },
    props: { canUse: () => true },
  })
  mounts.add(wrapper)
  await flushPromises()
  return wrapper
}

function unmount(wrapper) {
  wrapper.unmount()
  mounts.delete(wrapper)
}

function button(label, scope = document) {
  const element = [...scope.querySelectorAll('button')].find(item => item.textContent.replace(/\s+/g, '') === label)
  assert.ok(element, `expected a native button named ${label}`)
  return element
}

function find(selector) {
  const element = document.querySelector(selector)
  assert.ok(element, `expected a DOM element matching ${selector}`)
  return element
}

const buyButton = 'button[data-standalone-activation-buy]'
const purchasePopup = '.ant-popconfirm:not(.ant-popover-hidden)'

async function openPurchaseConfirmation() {
  const trigger = find(buyButton)
  assert.equal(trigger.disabled, false)
  trigger.click()
  const deadline = Date.now() + 2000
  while (!document.querySelector(purchasePopup) && Date.now() < deadline) await delay(10)
  assert.match(find(`${purchasePopup} .ant-popconfirm-message-title`).textContent, /确认从钱包购买一次性激活码/)
  return find(purchasePopup)
}

async function confirmPurchase(quotedPrice) {
  const popup = await openPurchaseConfirmation()
  if (quotedPrice) assert.ok(popup.querySelector('.ant-popconfirm-description').textContent.includes(`¥${quotedPrice}`))
  find(`${purchasePopup} .ant-popconfirm-buttons button.ant-btn-primary`).click()
  await flushPromises()
}

afterEach(async () => {
  for (const wrapper of mounts) unmount(wrapper)
  Antd.message.destroy()
  await flushPromises()
  document.body.replaceChildren()
  delete navigator.clipboard
  user.clearStoredCurrentUser()
  clearScriptPurchaseIntent(firstAccount.id)
  clearScriptPurchaseIntent(secondAccount.id)
  historyRequests.length = 0
  purchaseRequests.length = 0
  readHistory = async () => history([])
  readOffer = async () => offer(quote)
})

test('reopening purchase history restores codes for copying and filling a new script', async () => {
  const unusedCode = codeFor('a')
  const usedCode = codeFor('b')
  readHistory = async () => history([receipt(1, unusedCode), receipt(2, usedCode, 'used')])
  const first = await open()
  assert.equal(find('tr[data-row-key="1"] code').textContent, unusedCode)

  button('关闭').click()
  await flushPromises()
  assert.deepEqual(first.emitted('close'), [[]])
  unmount(first)
  assert.doesNotMatch(document.body.textContent, new RegExp(unusedCode))

  const reopened = await open()
  assert.deepEqual(historyRequests, [{ page: 1, pageSize: 10 }, { page: 1, pageSize: 10 }])
  const unused = find('tr[data-row-key="1"]')
  const used = find('tr[data-row-key="2"]')
  assert.equal(unused.querySelector('code').textContent, unusedCode)
  assert.equal(used.querySelector('code').textContent, usedCode)
  assert.equal(button('填入新建脚本', used).disabled, true)

  const copies = []
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: async code => { copies.push(code) } },
  })
  button('复制', unused).click()
  button('填入新建脚本', used).click()
  button('填入新建脚本', unused).click()
  await flushPromises()
  assert.deepEqual(copies, [unusedCode])
  assert.deepEqual(reopened.emitted('use'), [[unusedCode]])
  assert.equal(purchaseRequests.length, 0)
})

test('retrying an uncertain purchase after reopening uses the same key and quote', async () => {
  const purchasedCode = codeFor('c')
  const first = await open()
  await confirmPurchase()
  assert.equal(purchaseRequests.length, 1)
  assert.match(purchaseRequests[0].key, /^script-activation-purchase-[a-f0-9]{32}$/)
  assert.equal(purchaseRequests[0].quotedPrice, quote)

  purchaseRequests[0].reject(new Error('购买请求超时'))
  await flushPromises()
  assert.match(find('.ant-alert-error').textContent, /购买请求超时/)
  button('关闭').click()
  await flushPromises()
  unmount(first)

  await open()
  await confirmPurchase()
  assert.equal(purchaseRequests.length, 2)
  assert.equal(purchaseRequests[1].key, purchaseRequests[0].key)
  assert.equal(purchaseRequests[1].quotedPrice, purchaseRequests[0].quotedPrice)

  readHistory = async () => history([receipt(3, purchasedCode)])
  purchaseRequests[1].resolve(okay({ entitlementId: 3, code: purchasedCode }))
  await flushPromises()
  assert.equal(find('.ant-alert-success code').textContent, purchasedCode)
  assert.equal(find('tr[data-row-key="3"] code').textContent, purchasedCode)
  assert.deepEqual(historyRequests, Array(3).fill({ page: 1, pageSize: 10 }))
})

test('a 409 price change refreshes the quote and requires a new explicit purchase confirmation', async () => {
  const newQuote = '13.654321'
  const newCode = codeFor('6')
  await open()
  await confirmPurchase(quote)
  assert.equal(purchaseRequests.length, 1)
  assert.equal(purchaseRequests[0].quotedPrice, quote)

  let resolveOffer
  readOffer = () => new Promise(resolve => { resolveOffer = resolve })
  purchaseRequests[0].resolve({ status: 409, data: { code: 'SCRIPT_ACTIVATION_PRICE_CHANGED', message: '价格已变更' } })
  await flushPromises()
  assert.equal(historyRequests.length, 2)
  assert.equal(find(buyButton).disabled, true)
  assert.equal(purchaseRequests.length, 1)

  readOffer = async () => offer(newQuote)
  resolveOffer(offer(newQuote))
  await flushPromises()
  assert.match(find('.ant-alert-error').textContent, /价格已变更/)
  assert.ok(find(buyButton).textContent.includes(`¥${newQuote}`))
  assert.equal(purchaseRequests.length, 1)

  await confirmPurchase(newQuote)
  assert.equal(purchaseRequests.length, 2)
  assert.notEqual(purchaseRequests[1].key, purchaseRequests[0].key)
  assert.equal(purchaseRequests[1].quotedPrice, newQuote)

  readHistory = async () => history([{ ...receipt(8, newCode), price: newQuote }])
  purchaseRequests[1].resolve(okay({ entitlementId: 8, code: newCode }))
  await flushPromises()
  assert.equal(find('tr[data-row-key="8"] code').textContent, newCode)
})

test('an account switch hides previous records and discards a late history response', async () => {
  const visibleCode = codeFor('d')
  const lateCode = codeFor('e')
  const otherCode = codeFor('f')
  readHistory = async () => history([receipt(4, visibleCode)])
  const first = await open()
  assert.equal(find('tr[data-row-key="4"] code').textContent, visibleCode)

  let resolveHistory
  readHistory = () => new Promise(resolve => { resolveHistory = resolve })
  button('刷新购买记录').click()
  await flushPromises()
  assert.equal(historyRequests.length, 2)

  user.setStoredCurrentUser(secondAccount)
  await flushPromises()
  assert.doesNotMatch(document.body.textContent, new RegExp(visibleCode))
  unmount(first)
  readHistory = async () => history([receipt(6, otherCode)])
  await open(secondAccount)
  resolveHistory(history([receipt(5, lateCode)]))
  await flushPromises()

  assert.equal(find('tr[data-row-key="6"] code').textContent, otherCode)
  assert.doesNotMatch(document.body.textContent, new RegExp(lateCode))
})

test('a late purchase result after an account switch cannot publish a code or reload history', async () => {
  const oldCode = codeFor('1')
  await open()
  await confirmPurchase()
  assert.equal(purchaseRequests.length, 1)

  user.setStoredCurrentUser(secondAccount)
  await flushPromises()
  purchaseRequests[0].resolve(okay({ entitlementId: 7, code: oldCode }))
  await flushPromises()

  assert.doesNotMatch(document.body.textContent, new RegExp(oldCode))
  assert.equal(document.querySelector('.ant-alert-success'), null)
  assert.deepEqual(historyRequests, [{ page: 1, pageSize: 10 }])
})

test('native pagination requests the selected page and shows that page’s records', async () => {
  const firstCode = codeFor('3')
  const secondCode = codeFor('4')
  const firstPage = Array.from({ length: 10 }, (_, index) => receipt(index + 10, index ? codeFor('5') : firstCode))
  readHistory = async ({ page }) => history(page === 1 ? firstPage : [receipt(20, secondCode, 'frozen')], 11)
  await open()
  assert.equal(find('tr[data-row-key="10"] code').textContent, firstCode)

  find('.ant-pagination-item-2 a').click()
  await flushPromises()

  assert.deepEqual(historyRequests, [{ page: 1, pageSize: 10 }, { page: 2, pageSize: 10 }])
  assert.equal(find('.ant-pagination-item-active').textContent, '2')
  assert.equal(find('tr[data-row-key="20"] code').textContent, secondCode)
  assert.equal(document.querySelector('tr[data-row-key="10"]'), null)
  assert.equal(button('填入新建脚本', find('tr[data-row-key="20"]')).disabled, true)
})

test('buying waits for a pending page and returns to the latest receipts after purchase', async () => {
  const newCode = codeFor('7')
  const secondCode = codeFor('8')
  const firstPage = Array.from({ length: 10 }, (_, index) => receipt(index + 10, codeFor('9')))
  let resolvePage
  let purchased = false
  readHistory = ({ page }) => {
    if (page === 2) return new Promise(resolve => { resolvePage = resolve })
    if (purchased) return Promise.resolve(history([receipt(21, newCode), ...firstPage.slice(0, 9)], 12))
    return Promise.resolve(history(firstPage, 11))
  }
  await open()

  const popup = await openPurchaseConfirmation()
  const confirm = popup.querySelector('.ant-popconfirm-buttons button.ant-btn-primary')
  find('.ant-pagination-item-2 a').click()
  await flushPromises()
  assert.deepEqual(historyRequests, [{ page: 1, pageSize: 10 }, { page: 2, pageSize: 10 }])
  assert.equal(find(buyButton).disabled, true)
  assert.equal(confirm.isConnected, true)
  find(buyButton).click()
  confirm.click()
  await flushPromises()
  assert.equal(purchaseRequests.length, 0, 'an open confirmation cannot bypass the pending page guard')

  resolvePage(history([receipt(20, secondCode)], 11))
  await flushPromises()
  assert.equal(find('tr[data-row-key="20"] code').textContent, secondCode)
  await confirmPurchase()
  assert.equal(purchaseRequests.length, 1)
  find('.ant-pagination-item-1 a').click()
  await flushPromises()
  assert.equal(historyRequests.length, 2, 'pagination cannot start another read during the purchase')

  purchased = true
  purchaseRequests[0].resolve(okay({ entitlementId: 21, code: newCode }))
  await flushPromises()
  assert.deepEqual(historyRequests, [{ page: 1, pageSize: 10 }, { page: 2, pageSize: 10 }, { page: 1, pageSize: 10 }])
  assert.equal(find('.ant-pagination-item-active').textContent, '1')
  assert.equal(find('tr[data-row-key="21"] code').textContent, newCode)
  assert.equal(document.querySelector('tr[data-row-key="20"]'), null)
})
