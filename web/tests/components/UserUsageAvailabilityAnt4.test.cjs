// External regression only: real UserUsage + Vue/Antd DOM, synthetic IO only.
const { test } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
assert.ok(web && path.isAbsolute(web), 'explicit isolated TIKU_WEB_SOURCE required')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const currentUser = require(path.join(web, 'src/utils/currentUser.js'))
const usagePath = path.join(web, 'src/pages/UserUsage/index.vue')
const requestPath = path.join(web, 'src/utils/request.js')
const originalRequest = require.cache[requestPath]
const enabledMethod = { id: 17, name: '隔离测试支付方式', enabled: true, sort: 0, epayType: 'fixture', iconSvg: '' }
const emptyDescription = '暂无可用支付方式，请联系管理员。'
let active = null
const transport = new Module(requestPath)
transport.loaded = true
transport.exports = request => {
  assert.ok(active, 'synthetic transport must belong to one current case')
  const method = String(request.method || '').toLowerCase()
  const route = request.url
  const allowedGET = ['/adapter-service/user/info', '/adapter-service/billing-records', '/adapter-service/payment-methods']
  assert.ok((method === 'get' && allowedGET.includes(route)) ||
    (method === 'post' && route === '/adapter-service/recharge-orders'), 'unapproved synthetic route or method')
  active.calls.push({ method, route, data: request.data })
  if (method === 'post') {
    // Even a regression issuing a forbidden order reaches only this inert 500.
    return Promise.resolve({ status: 500, message: 'SYNTHETIC_ORDER_FAILURE_NO_CHECKOUT' })
  }
  if (route === '/adapter-service/user/info') return Promise.resolve({ status: 200, data: { id: 701, username: 'synthetic-u06c', role: 'user', balance: 1.000062 } })
  if (route === '/adapter-service/billing-records') return Promise.resolve({ status: 200, data: { data: [], total: 0, page: 1, pageSize: 10 } })
  active.paymentGETs += 1
  if (active.rejectInitial && active.paymentGETs === 1) return Promise.reject(new Error('SYNTHETIC_PAYMENT_METHOD_GET_REJECT'))
  if (active.delayRefresh && active.paymentGETs === 2) {
    return new Promise(resolve => { active.pendingResolve = resolve })
  }
  return Promise.resolve({ status: 200, data: active.rows })
}
require.cache[requestPath] = transport
async function settle () { await nextTick(); await flushPromises(); await nextTick() }
function orderCalls (state) { return state.calls.filter(x => x.method === 'post' && x.route === '/adapter-service/recharge-orders') }
function createButton (wrapper) {
  const buttons = wrapper.findAll('button').filter(x => x.text().replace(/\s/g, '') === '创建充值订单')
  assert.equal(buttons.length, 1, 'one actual native create-order button')
  assert.equal(buttons[0].element.tagName, 'BUTTON')
  assert.equal(buttons[0].element.isConnected, true)
  return buttons[0]
}
async function nativeClick (button) { button.element.click(); await nextTick(); await settle() }
async function withUsage (options, body) {
  assert.equal(active, null, 'cases cannot overlap')
  const state = { rows: [], calls: [], paymentGETs: 0, pendingResolve: null, openCalls: 0, ...options }
  active = state
  delete require.cache[usagePath] // fresh real module refs; no product ownership repair
  const Usage = require(usagePath)
  const previousOpen = window.open
  const previousFetch = globalThis.fetch
  window.open = () => { state.openCalls += 1; throw new Error('NO_CHECKOUT_WINDOW_AUTHORIZED') }
  globalThis.fetch = () => { throw new Error('NO_LIVE_NETWORK_AUTHORIZED') }
  let wrapper
  try {
    currentUser.setStoredCurrentUser({ id:701, username:'synthetic-u06c', role:'user', balance:1.000062 })
    wrapper = mount(Usage, { attachTo: document.body, global: { plugins: [Antd] } })
    await settle()
    assert.equal(state.paymentGETs, 1, 'actual mounted payment-method GET')
    assert.equal(state.calls.filter(x => x.route === '/adapter-service/user/info').length, 1)
    assert.equal(state.calls.filter(x => x.route === '/adapter-service/billing-records').length, 1)
    await body(wrapper, state)
  } finally {
    // Drain only the test-owned deferred synthetic response before unmount.
    if (state.pendingResolve) {
      const resolve = state.pendingResolve; state.pendingResolve = null
      resolve({ status: 200, data: state.rows }); await settle()
    }
    if (wrapper) wrapper.unmount()
    await settle()
    Antd.message.destroy(); Antd.Modal.destroyAll()
    document.body.replaceChildren()
    window.open = previousOpen
    if (previousFetch === undefined) delete globalThis.fetch
    else globalThis.fetch = previousFetch
    currentUser.setStoredCurrentUser(null)
    active = null
    delete require.cache[usagePath]
    if (originalRequest) require.cache[requestPath] = originalRequest
    else delete require.cache[requestPath]
    require.cache[requestPath] = transport
  }
}
async function assertUnavailable (wrapper, state, expectError) {
  assert.equal(wrapper.vm.paymentMethodLoading, false)
  assert.equal(wrapper.vm.paymentMethods.length, 0, 'real enabled-method projection is empty')
  assert.equal(wrapper.findAll('.payment-method-card').length, 0)
  assert.ok(wrapper.text().includes(emptyDescription), 'original Empty explanation retained')
  if (expectError) assert.ok(document.body.textContent.includes('获取支付方式失败'), 'actual Antd error feedback retained')
  const button = createButton(wrapper)
  await nativeClick(button)
  assert.equal(orderCalls(state).length, 0, 'unavailable state cannot create even a synthetic order')
  assert.equal(state.openCalls, 0)
  assert.equal(button.element.disabled, true, 'U06C_UNAVAILABLE_NATIVE_DISABLED: unavailable create-order button must be disabled')
}

test('U06C empty successful GET disables the native order button with existing explanation and zero order calls', async () => {
  await withUsage({ rows: [] }, async (wrapper, state) => assertUnavailable(wrapper, state, false))
})
test('U06C all-disabled payment methods stay unavailable and never create an order', async () => {
  await withUsage({ rows: [{ ...enabledMethod, enabled: false }, { ...enabledMethod, id: 18, enabled: false }] }, async (wrapper, state) => assertUnavailable(wrapper, state, false))
})
test('U06C rejected initial payment-method GET keeps unavailable semantics and existing error feedback', async () => {
  await withUsage({ rejectInitial: true }, async (wrapper, state) => assertUnavailable(wrapper, state, true))
})
test('U06C delayed refresh disables ordering while a previously loaded enabled method remains', async () => {
  await withUsage({ rows: [enabledMethod], delayRefresh: true }, async (wrapper, state) => {
    const button = createButton(wrapper)
    assert.equal(button.element.disabled, false, 'loaded method starts usable')
    assert.equal(wrapper.vm.paymentMethods.length, 1)
    assert.equal(wrapper.vm.selectedPaymentMethodId, 17)
    wrapper.vm.handleLoadPaymentMethods(); await settle()
    assert.equal(state.paymentGETs, 2)
    assert.equal(typeof state.pendingResolve, 'function', 'second actual GET is held by this fixture')
    assert.equal(wrapper.vm.paymentMethodLoading, true)
    assert.equal(wrapper.vm.paymentMethods[0].id, 17, 'old loaded method remains during refresh')
    const disabledWhileLoading = button.element.disabled
    await nativeClick(button)
    const duringOrderCalls = orderCalls(state).length
    const resolve = state.pendingResolve; state.pendingResolve = null
    resolve({ status: 200, data: [enabledMethod] }); await settle()
    assert.equal(wrapper.vm.paymentMethodLoading, false)
    assert.equal(createButton(wrapper).element.disabled, false, 'successful refresh restores usable button')
    assert.equal(state.openCalls, 0)
    assert.equal(disabledWhileLoading, true, 'U06C_LOADING_NATIVE_DISABLED: payment-method loading must disable ordering')
    assert.equal(duringOrderCalls, 0, 'loading state cannot create even a synthetic order')
  })
})
test('U06C enabled method restores a usable button and preserves the exact inert synthetic order payload', async () => {
  await withUsage({ rows: [enabledMethod] }, async (wrapper, state) => {
    const button = createButton(wrapper)
    assert.equal(wrapper.vm.paymentMethodLoading, false)
    assert.equal(wrapper.vm.paymentMethods.length, 1)
    assert.equal(wrapper.vm.selectedPaymentMethodId, 17)
    assert.equal(button.element.disabled, false)
    wrapper.vm.rechargeAmount = 12.34
    await settle(); await nativeClick(button)
    const orders = orderCalls(state)
    assert.equal(orders.length, 1, 'exactly one synthetic 500 order request')
    assert.deepEqual(orders[0].data, { amount: 12.34, paymentMethodId: 17 }, 'original amount and method payload retained')
    assert.equal(wrapper.vm.rechargeSubmitting, false)
    assert.equal(wrapper.vm.paymentResultVisible, false, 'synthetic failure never starts checkout')
    assert.equal(wrapper.vm.paymentJumpUrl, '')
    assert.equal(wrapper.vm.paymentQRCodeDataUrl, '')
    assert.equal(state.openCalls, 0)
    assert.ok(document.body.textContent.includes('SYNTHETIC_ORDER_FAILURE_NO_CHECKOUT'), 'actual failure feedback is visible')
  })
})
