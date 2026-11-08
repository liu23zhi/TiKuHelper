const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue')
const Antd = require('ant-design-vue'), QRCode = require('qrcode')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const adminA = { id: 91, role: 'admin', username: 'fixture-admin-a', balance: 1.234567 }
const adminB = { id: 92, role: 'admin', username: 'fixture-admin-b', balance: 2.345678 }
const record = () => ({ id: 8091, subject: 'SYNTHETIC_OWNER_BILLING', type: 'normal_search', direction: 'expense', amount: 0.123456, showInStatement: false })
const ok = data => ({ status: 200, data })
let pendingSaves = [], pendingOrders = [], pendingQR = [], orderMode = 'hold', wrapper
const originalQR = QRCode.toDataURL
const apiPath = path.join(root, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = {
 getCurrentUser: () => Promise.resolve(ok(user.getStoredCurrentUser())),
 getBillingRecords: () => Promise.resolve(ok({ data: [record()], total: 1, page: 1, pageSize: 10 })),
 getPaymentMethods: () => Promise.resolve(ok([{ id: 91, enabled: true, name: 'fixture-only', epayType: 'fixture', iconSvg: '' }])),
 getIpLocations: () => Promise.resolve(ok({})), getMyLocations: () => Promise.resolve(ok({})),
 updateBillingStatementVisibility: (id, show) => new Promise((resolve, reject) => pendingSaves.push({ id, show, resolve, reject })),
 createRechargeOrder: payload => orderMode === 'qr'
  ? Promise.resolve(ok({ payType: 'qrcode', qrcodeContent: 'SYNTHETIC_NO_PAYMENT' }))
  : new Promise(resolve => pendingOrders.push({ payload, resolve })),
}
require.cache[apiPath] = api
const Billing = require(path.join(root, 'src/pages/BillingRecordList/index.vue'))
const Usage = require(path.join(root, 'src/pages/UserUsage/index.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function mountAs(component) {
 user.setStoredCurrentUser(adminA)
 wrapper = mount(component, { attachTo: document.body, global: { plugins: [Antd] } })
 await settle(); return wrapper
}
afterEach(async () => {
 wrapper?.unmount(); wrapper = undefined; QRCode.toDataURL = originalQR
 pendingSaves = []; pendingOrders = []; pendingQR = []; orderMode = 'hold'
 await settle(); Antd.message.destroy(); Antd.Modal.destroyAll(); document.body.innerHTML = ''; user.clearStoredCurrentUser()
})
test('late old-admin visibility success cannot repaint a new owner dialog or release its active save', async () => {
 await mountAs(Billing)
 wrapper.vm.handleShowFeeDetail(wrapper.vm.dataSource[0]); await settle()
 const oldSave = wrapper.vm.handleStatementVisibilityChange({ id:8091, showInStatement:false })
 assert.equal(pendingSaves.length, 1); assert.equal(wrapper.vm.visibilitySaving[8091], true)
 user.setStoredCurrentUser(adminB); await settle()
 assert.equal(wrapper.vm.billingDetailOpen, false); assert.equal(wrapper.vm.selectedBillingRecord, null)
 wrapper.vm.handleShowFeeDetail(wrapper.vm.dataSource[0]); await settle()
 const newSave = wrapper.vm.handleStatementVisibilityChange({ id:8091, showInStatement:true })
 assert.equal(pendingSaves.length, 2); assert.equal(wrapper.vm.visibilitySaving[8091], true)
 pendingSaves[0].resolve(ok({})); assert.equal(await oldSave, false); await settle()
 assert.equal(wrapper.vm.visibilitySaving[8091], true, 'old finally must not release the new save')
 assert.equal(wrapper.vm.selectedBillingRecord.showInStatement, false, 'old result cannot repaint new dialog')
 pendingSaves[1].resolve(ok({})); assert.equal(await newSave, true); await settle()
 assert.equal(wrapper.vm.visibilitySaving[8091], false)
 assert.equal(wrapper.vm.selectedBillingRecord.showInStatement, true)
 assert.equal(wrapper.vm.billingDetailOpen, true, 'current owner keeps its dialog open after save')
})
test('a completed order response belonging to the retired account cannot display a pay link or success in the new scope', async () => {
 await mountAs(Usage); wrapper.vm.handleCreateRechargeOrder(); await settle()
 assert.equal(pendingOrders.length, 1); assert.deepEqual(pendingOrders[0].payload, { amount:10, paymentMethodId:91 })
 user.setStoredCurrentUser(adminB); await settle()
 pendingOrders[0].resolve(ok({ payType:'jump', payUrl:'https://fixture.invalid/no-checkout' })); await settle()
 assert.equal(wrapper.vm.paymentResultVisible, false)
 assert.equal(wrapper.vm.paymentJumpUrl, '')
 assert.equal(wrapper.vm.paymentQRCodeDataUrl, '')
 assert.equal(wrapper.vm.rechargeSubmitting, false)
 assert.equal(document.body.textContent.includes('充值订单创建成功'), false)
})
test('late QR encoding is discarded after identity retirement rather than reopening a stale payment dialog', async () => {
 orderMode = 'qr'; QRCode.toDataURL = () => new Promise(resolve => pendingQR.push(resolve))
 await mountAs(Usage); wrapper.vm.handleCreateRechargeOrder(); await settle()
 assert.equal(pendingQR.length, 1)
 user.setStoredCurrentUser(adminB); await settle()
 pendingQR[0]('data:image/png;base64,SYNTHETIC_OLD_QR'); await settle()
 assert.equal(wrapper.vm.paymentResultVisible, false)
 assert.equal(wrapper.vm.paymentQRCodeDataUrl, '')
 assert.equal(wrapper.vm.paymentJumpUrl, '')
 assert.equal(document.body.textContent.includes('充值订单创建成功'), false)
})
