/* Migrated from the removed standalone panel: actual detail actions, Antd and API transport. */
const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
for (const name of ['HTMLInputElement', 'HTMLButtonElement']) global[name] = window[name]
global.FormData = window.FormData
global.getComputedStyle = element => window.getComputedStyle(element)
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue'), axios = require('axios')
const user = require('../../src/utils/currentUser.js')
const owner = { id: 781, role: 'user', status: 'enabled' }, admin = { id: 782, role: 'admin', status: 'enabled' }
const original = { id: 1, orderId: 14, userId: 781, kind: 'original', requestedCents: 1250, status: 'pending_review' }
const manual = { id: 2, userId: 781, kind: 'manual', requestedCents: 1500, status: 'pending_review' }
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADElEQVR42mP8z8BQDwAE/wGJxJrtGQAAAABJRU5ErkJggg==', 'base64')
const image = new Blob([png], { type: 'image/png' })
const priorAdapter = axios.defaults.adapter, priorCreate = URL.createObjectURL, priorRevoke = URL.revokeObjectURL
const calls = [], wrappers = [], created = [], revoked = [], unexpected = [], runtime = []
let record, rejectMutation = false, proofWait = null, changed = 0
function response(config, data, status = 200) { return { status, data, config, headers: {}, request: { memoryOnly: true } } }
axios.defaults.adapter = async config => {
  const url = String(config.url), method = config.method, data = typeof config.data === 'string' ? JSON.parse(config.data) : config.data
  calls.push({ url, method, data, retryPolicy: config.retryPolicy })
  if (/^\/api\/ticket-refunds\/\d+$/.test(url) && method === 'get') return response(config, { ...record })
  if (method === 'post' && rejectMutation) return response(config, { message: 'PRIVATE_PROVIDER_DETAIL_NOT_FOR_DISPLAY' }, 503)
  if (url.endsWith('/decision') && method === 'post') {
    record = { ...record, status: data.action === 'approve' ? 'frozen' : 'rejected', approvedCents: data.approvedCents || record.requestedCents }
    return response(config, { ...record })
  }
  if (url.endsWith('/process') && method === 'post') {
    record = { ...record, status: data.action === 'cancel_unsent' ? 'failed' : 'unknown' }
    return response(config, { ...record }, data.action === 'submit' ? 202 : 200)
  }
  if (url.endsWith('/reconcile') && method === 'post') { record = { ...record, status: data.outcome }; return response(config, { ...record }) }
  if (url.endsWith('/proof') && method === 'post') { record = { ...record, hasProof: true }; return response(config, { hasProof: true }, 201) }
  if (url.endsWith('/proof') && method === 'get') { if (proofWait) await proofWait; return response(config, image) }
  if (/^\/api\/ticket-attachments\/\d+\/admin$/.test(url) && method === 'get') return response(config, image)
  unexpected.push(`${method} ${url}`); throw new Error('Unexpected fixture HTTP')
}
URL.createObjectURL = blob => { created.push(blob); return 'blob:http://localhost/refund-proof-fixture' }
URL.revokeObjectURL = url => revoked.push(url)
const Actions = require('../../src/pages/Tickets/TicketRefundActions.vue')
const state = wrapper => wrapper.vm.$.setupState
const writes = () => calls.filter(call => call.method === 'post')
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function open(row = original, isAdmin = true) {
  record = { ...row }; user.setStoredCurrentUser(isAdmin ? admin : owner)
  const wrapper = mount(Actions, { attachTo: document.body, props: { refund: { ...row, refundId: row.id }, isAdmin, onChanged() { changed++ } }, global: { plugins: [Antd], config: { errorHandler: error => runtime.push(error) } } })
  wrappers.push(wrapper); await settle(); return wrapper
}
function findButton(root, label) { return [...root.querySelectorAll('button')].find(node => node.textContent.replace(/\s/g, '') === label.replace(/\s/g, '')) }
async function confirm(wrapper, label) {
  const trigger = findButton(wrapper.element, label)
  assert.ok(trigger && !trigger.disabled, `enabled ${label}`)
  trigger.click(); await settle()
  const ok = [...document.querySelectorAll('[data-refund-action-confirmation] button.ant-btn-primary')].find(node => node.closest('.ant-modal-wrap')?.style.display !== 'none')
  assert.ok(ok, 'actual Antd confirmation rendered'); ok.click(); await settle()
}
afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  await settle(); Antd.message.destroy(); Antd.Modal.destroyAll(); user.clearStoredCurrentUser(); document.body.replaceChildren()
  assert.deepEqual(unexpected, []); assert.deepEqual(runtime, [])
  calls.length = created.length = revoked.length = unexpected.length = runtime.length = 0
  rejectMutation = false; proofWait = null; changed = 0
})
after(() => { axios.defaults.adapter = priorAdapter; URL.createObjectURL = priorCreate; URL.revokeObjectURL = priorRevoke })

test('真实待审核状态显示审核入口；超额拦截后按分审核，操作只提交一次', async () => {
  const wrapper = await open()
  assert.match(wrapper.text(), /待审核/)
  const input = wrapper.find('input')
  await input.setValue('12.51'); await confirm(wrapper, '审核通过')
  assert.equal(writes().length, 0); assert.match(wrapper.text(), /不能超过申请金额/)
  await input.setValue('12.49'); await confirm(wrapper, '审核通过')
  assert.deepEqual(writes().map(({ url, data, retryPolicy }) => ({ url, data, retryPolicy })), [{ url: '/api/ticket-refunds/1/decision', data: { action: 'approve', approvedCents: 1249 }, retryPolicy: 'single' }])
  assert.match(wrapper.text(), /资金已冻结/); assert.equal(changed, 1)
});

test('用户只能查看退款信息；人工审核未上传凭证时不能通过但可以驳回', async () => {
  let wrapper = await open(original, false)
  assert.equal(findButton(wrapper.element, '审核通过'), undefined)
  await state(wrapper).review('approve'); await state(wrapper).process('submit'); assert.equal(writes().length, 0)
  wrapper.unmount()
  wrapper = await open(manual)
  assert.equal(findButton(wrapper.element, '审核通过').disabled, true)
  await state(wrapper).review('approve'); assert.equal(writes().length, 0)
  await confirm(wrapper, '驳回')
  assert.deepEqual(writes()[0].data, { action: 'reject' })
});

test('人工核验凭证使用保护接口，关闭预览回收图片URL', async () => {
  const wrapper = await open(manual, false)
  const input = wrapper.element.querySelector('[data-refund-proof-upload]')
  Object.defineProperty(input, 'files', { configurable: true, value: [new window.File([png], 'synthetic-proof.png', { type: 'image/png' })] })
  input.dispatchEvent(new Event('change', { bubbles: true })); await settle()
  assert.equal(input.value, '')
  assert.equal(writes()[0].url, '/api/ticket-refunds/2/proof')
  assert.equal(writes()[0].data.get('file').name, 'synthetic-proof.png')
  findButton(wrapper.element, '查看受限凭证').click(); await settle()
  assert.deepEqual(created, [image]); assert.equal(state(wrapper).proofUrl, 'blob:http://localhost/refund-proof-fixture')
  document.querySelector('.ant-modal-close').click(); await settle()
  assert.deepEqual(revoked, ['blob:http://localhost/refund-proof-fixture']); assert.equal(state(wrapper).proofUrl, '')
});

test('202原路提交只显示待核对，禁止重发及撤销，保留渠道查询', async () => {
  const wrapper = await open({ ...original, status: 'frozen', approvedCents: 1250 })
  await confirm(wrapper, '提交原路退款')
  assert.match(wrapper.text(), /结果待核对，资金仍冻结/)
  assert.equal(findButton(wrapper.element, '提交原路退款'), undefined)
  await state(wrapper).process('submit'); await state(wrapper).process('cancel_unsent')
  assert.equal(writes().length, 1)
  findButton(wrapper.element, '查询渠道结果').click(); await settle()
  assert.deepEqual(writes().map(call => call.data), [{ action: 'submit' }, { action: 'query' }])
});

test('原路提交HTTP失败不盲重试，也不展示渠道原始详情', async () => {
  rejectMutation = true
  const wrapper = await open({ ...original, status: 'frozen', approvedCents: 1250 })
  await confirm(wrapper, '提交原路退款'); await state(wrapper).process('submit')
  assert.equal(writes().length, 1)
  assert.match(wrapper.text(), /操作结果未确认/); assert.doesNotMatch(wrapper.text(), /PRIVATE_PROVIDER_DETAIL/)
});

test('仅可撤销尚未提交的原路退款，成功后不能再次撤销', async () => {
  const wrapper = await open({ ...original, status: 'frozen', approvedCents: 1250 })
  await confirm(wrapper, '撤销未提交退款并解冻')
  await state(wrapper).process('cancel_unsent')
  assert.deepEqual(writes().map(call => call.data), [{ action: 'cancel_unsent' }])
  assert.match(wrapper.text(), /冻结已解除/); assert.equal(findButton(wrapper.element, '撤销未提交退款并解冻'), undefined)
});

test('人工核销必须填写真实凭据编号，并使用reconcile接口', async () => {
  const wrapper = await open({ ...manual, status: 'frozen', approvedCents: 1500, hasProof: true })
  await confirm(wrapper, '确认人工退款成功')
  assert.equal(writes().length, 0)
  await wrapper.find('input').setValue('FIXTURE-RECON-1')
  await confirm(wrapper, '确认人工退款成功')
  assert.deepEqual(writes().map(({ url, data }) => ({ url, data })), [{ url: '/api/ticket-refunds/2/reconcile', data: { outcome: 'succeeded', reference: 'FIXTURE-RECON-1' } }])
  assert.match(wrapper.text(), /退款成功/)
});

test('详情切换或账户退出时，旧凭证响应不显示且旧动作不发送请求', async () => {
  const wrapper = await open({ ...manual, hasProof: true }, false)
  let release; proofWait = new Promise(resolve => { release = resolve })
  const viewing = state(wrapper).viewProof(); await settle()
  user.setStoredCurrentUser({ ...owner, id: 991 }); await settle()
  release(); await viewing; await settle()
  assert.deepEqual(created, [])
  const before = calls.length
  await state(wrapper).review('approve'); await state(wrapper).reload(); await state(wrapper).viewProof()
  assert.equal(calls.length, before)
});

test('管理员从退款详情查看收款码、签名和已签协议快照', async () => {
  const wrapper = await open({ ...manual, payoutQrAttachmentId: 41, signatureAttachmentId: 42, agreementText: '本次签署的协议快照' })
  assert.match(wrapper.text(), /本次签署的协议快照/)
  findButton(wrapper.element, '查看收款码').click(); await settle()
  assert.ok(calls.some(call => call.url === '/api/ticket-attachments/41/admin'))
  state(wrapper).closeProof()
  findButton(wrapper.element, '查看签名').click(); await settle()
  assert.ok(calls.some(call => call.url === '/api/ticket-attachments/42/admin'))
});
