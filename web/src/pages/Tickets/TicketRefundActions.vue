<template>
  <section class="refund-actions" data-testid="ticket-refund-actions">
    <header class="refund-actions-head"><h3>退款信息</h3><a-tag :color="statusTone">{{ refundStatusLabel(row.status) }}</a-tag></header>
    <dl class="refund-meta">
      <div><dt>方式</dt><dd>{{ row.kind === 'manual' ? '人工退回' : '原路退回' }}</dd></div>
      <div><dt>申请金额</dt><dd>{{ formatRefundCents(row.requestedCents) }}</dd></div>
      <div v-if="row.approvedCents > 0"><dt>审核金额</dt><dd>{{ formatRefundCents(row.approvedCents) }}</dd></div>
      <div v-if="row.orderNo"><dt>关联订单</dt><dd>{{ row.orderNo }}</dd></div>
    </dl>
    <p v-if="failure" class="refund-failure" role="alert">{{ failure }}</p>
    <p v-if="notice" class="refund-notice" role="status">{{ notice }}</p>
    <p v-if="loading" role="status">正在读取退款详情…</p>
    <template v-if="canReview">
      <a-form layout="vertical" class="refund-review-form"><a-form-item label="审核金额（元）" :html-for="fieldId('amount')">
        <a-input :id="fieldId('amount')" v-model:value="approveYuan" placeholder="留空表示按申请金额" :disabled="busy || uncertain" />
      </a-form-item></a-form>
      <p v-if="row.kind === 'manual' && !row.hasProof">请等待申请人上传退款核验凭证，核验后再通过审核。</p>
      <a-space>
        <a-button type="primary" :loading="busy" :disabled="busy || uncertain || (row.kind === 'manual' && !row.hasProof)" @click="requestConfirmation('review', 'approve')">审核通过</a-button>
        <a-button danger :disabled="busy || uncertain" @click="requestConfirmation('review', 'reject')">驳回</a-button>
      </a-space>
    </template>
    <template v-if="canProcessOriginal">
      <a-space>
        <a-button v-if="row.status === 'frozen' && !row.startedAt" type="primary" :disabled="busy || uncertain" :loading="busy" @click="requestConfirmation('process', 'submit')">提交原路退款</a-button>
        <a-button v-if="row.status !== 'frozen' || row.startedAt" :disabled="busy" @click="process('query')">查询渠道结果</a-button>
        <a-button v-if="row.status === 'frozen' && !row.startedAt" danger :disabled="busy || uncertain" @click="requestConfirmation('process', 'cancel_unsent')">撤销未提交退款并解冻</a-button>
      </a-space>
    </template>
    <template v-if="canReconcile">
      <a-form layout="vertical" class="refund-review-form"><a-form-item label="人工打款凭据编号" :html-for="fieldId('reference')"><a-input :id="fieldId('reference')" v-model:value="reference" :maxlength="96" :disabled="busy || uncertain" placeholder="填写实际打款凭据编号" /></a-form-item></a-form>
      <a-space>
        <a-button type="primary" :disabled="busy || uncertain" @click="requestConfirmation('reconcile', 'succeeded')">确认人工退款成功</a-button>
        <a-button danger :disabled="busy || uncertain" @click="requestConfirmation('reconcile', 'failed')">确认未完成并解冻</a-button>
      </a-space>
    </template>
    <p v-if="canUploadProof"><label>上传退款核验凭证（PNG / JPEG，最多 2 MiB）<input type="file" accept="image/png,image/jpeg" :disabled="busy || uncertain" data-refund-proof-upload @change="uploadProof" /></label></p>
    <a-space class="refund-secondary-actions">
      <a-button v-if="row.hasProof" :disabled="busy || proofBusy" @click="viewProof">查看受限凭证</a-button>
      <a-button v-if="isAdmin && row.payoutQrAttachmentId" :disabled="proofBusy" @click="viewAttachment(row.payoutQrAttachmentId, '收款码')">查看收款码</a-button>
      <a-button v-if="isAdmin && row.signatureAttachmentId" :disabled="proofBusy" @click="viewAttachment(row.signatureAttachmentId, '签名')">查看签名</a-button>
      <a-button :disabled="busy || loading" @click="reload">刷新</a-button>
    </a-space>
    <details v-if="isAdmin && row.agreementText"><summary>本次签署的承诺书</summary><pre class="refund-agreement-snapshot">{{ row.agreementText }}</pre></details>
    <a-modal :open="!!proofUrl" :title="proofTitle" :footer="null" @cancel="closeProof"><img :src="proofUrl" :alt="proofTitle" class="refund-proof-image" /></a-modal>
    <a-modal :open="!!confirmation" title="确认退款操作" :footer="null" :mask-closable="false" @cancel="confirmation = null">
      <div data-refund-action-confirmation><p>{{ confirmationText }}</p><a-space><a-button @click="confirmation = null">取消</a-button><a-button type="primary" @click="confirmAction">确认</a-button></a-space></div>
    </a-modal>
  </section>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { getTicketRefundDetail, getTicketRefundProof, processTicketRefund, reconcileTicketRefund, reviewTicketRefund, uploadTicketRefundProof } from '@/api/TikuComponentApi'
import { getTicketAttachmentAsAdmin } from '@/api/TicketApi'
import { getCurrentUserLogoutEpoch, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { formatRefundCents, parseRefundYuanCents, refundProofAllowed, refundResponseData, refundStatusLabel, validRefundID } from './refundTicketHelpers.mjs'

const props = defineProps({ refund: { type: Object, required: true }, isAdmin: Boolean, onChanged: { type: Function, default: () => {} } })
const uniqueId = useId(), fieldId = name => `ticket-refund-${uniqueId}-${name}`
const { currentUser } = useCurrentUserDisplay()
const actor = currentUserIdentity(currentUser.value), actorRole = currentUser.value?.role, logout = getCurrentUserLogoutEpoch()
const detail = ref(null), loading = ref(false), busy = ref(false), proofBusy = ref(false)
const failure = ref(''), notice = ref(''), approveYuan = ref(''), reference = ref(''), uncertain = ref(false)
const proofUrl = ref(''), proofTitle = ref('退款核验凭证')
const confirmation = ref(null)
const confirmationText = computed(() => ({ approve: '确认审核通过并冻结相应余额？', reject: '确认驳回这笔退款申请？', submit: '确认向原支付渠道提交退款？', cancel_unsent: '确认撤销未提交的退款并解除冻结？', succeeded: '确认已依据凭据完成打款？', failed: '确认退款未完成并解除冻结？' }[confirmation.value?.action] || ''))
let alive = true, version = 0
const owned = () => alive && actor === currentUserIdentity(currentUser.value) && actorRole === currentUser.value?.role && logout === getCurrentUserLogoutEpoch() && currentUser.value?.status === 'enabled'
const row = computed(() => detail.value || props.refund)
const id = () => validRefundID(props.refund?.refundId ?? props.refund?.id)
const capture = () => { const expected = version, refundId = id(); return () => owned() && expected === version && refundId === id() }
const ready = computed(() => owned() && !!detail.value && !loading.value)
const canReview = computed(() => ready.value && props.isAdmin && row.value.status === 'pending_review')
const canProcessOriginal = computed(() => ready.value && props.isAdmin && row.value.kind === 'original' && ['frozen', 'processing', 'unknown'].includes(row.value.status))
const canReconcile = computed(() => ready.value && props.isAdmin && row.value.kind === 'manual' && ['frozen', 'processing', 'unknown'].includes(row.value.status))
const canUploadProof = computed(() => ready.value && !props.isAdmin && row.value.kind === 'manual' && row.value.status === 'pending_review' && !row.value.hasProof)
const statusTone = computed(() => ({ pending_review: 'gold', frozen: 'blue', processing: 'blue', unknown: 'orange', succeeded: 'green', rejected: 'red', failed: 'red' }[row.value.status] || 'default'))

function closeProof() { if (proofUrl.value) URL.revokeObjectURL(proofUrl.value); proofUrl.value = '' }
function reset() { version++; confirmation.value = null; detail.value = null; loading.value = busy.value = proofBusy.value = uncertain.value = false; failure.value = notice.value = approveYuan.value = reference.value = ''; closeProof() }
function requestConfirmation(kind, action) { if (owned() && !busy.value && !uncertain.value) confirmation.value = { kind, action } }
function confirmAction() {
  const selected = confirmation.value
  confirmation.value = null
  if (!selected || !owned()) return
  if (selected.kind === 'review') return review(selected.action)
  if (selected.kind === 'process') return process(selected.action)
  if (selected.kind === 'reconcile') return reconcile(selected.action)
}
function receive(response) {
  const data = refundResponseData(response)
  if (!data || validRefundID(data.id) !== id()) throw new Error('退款详情格式无效')
  detail.value = data
  return data
}
async function load() {
  if (!owned() || !id() || loading.value) return
  const owns = capture(); loading.value = true; failure.value = ''
  try { const response = await getTicketRefundDetail(id()); if (owns()) { receive(response); uncertain.value = false } }
  catch { if (owns()) failure.value = '退款详情读取失败，请刷新重试。' }
  finally { if (owns()) loading.value = false }
}
function reload() { if (!owned() || busy.value) return; void load(); props.onChanged() }
async function review(action) {
  if (!canReview.value || busy.value || uncertain.value || !['approve', 'reject'].includes(action)) return
  const payload = { action }, amount = approveYuan.value.trim()
  if (action === 'approve') {
    if (row.value.kind === 'manual' && !row.value.hasProof) { failure.value = '人工退款审核前须核验受限凭证'; return }
    if (amount) {
      const cents = parseRefundYuanCents(amount)
      if (cents === null || cents > row.value.requestedCents) { failure.value = '审核金额必须大于 0，且不能超过申请金额'; return }
      payload.approvedCents = cents
    }
  }
  const owns = capture(); busy.value = true; failure.value = ''
  try {
    const response = await reviewTicketRefund(id(), payload)
    if (!owns()) return
    receive(response); notice.value = action === 'approve' ? '已审核通过，余额已冻结。' : '已驳回。'; props.onChanged()
  } catch { if (owns()) { uncertain.value = true; failure.value = '审核结果未确认，请刷新核对后再操作。' } }
  finally { if (owns()) busy.value = false }
}
async function process(action) {
  if (!canProcessOriginal.value || busy.value || !['submit', 'query', 'cancel_unsent'].includes(action)) return
  const unsent = row.value.status === 'frozen' && !row.value.startedAt
  if (action !== 'query' && (!unsent || uncertain.value)) return
  if (action === 'query' && unsent) return
  const owns = capture(); busy.value = true; failure.value = ''
  try {
    const response = await processTicketRefund(id(), { action })
    if (!owns()) return
    receive(response)
    if (response.status === 202) detail.value = { ...detail.value, status: 'unknown' }
    notice.value = response.status === 202 ? '操作结果未确认，请查询渠道结果。' : action === 'cancel_unsent' ? '退款未完成，冻结已解除。' : '退款状态已更新。'
    props.onChanged()
  } catch {
    if (!owns()) return
    if (action === 'submit') { detail.value = { ...row.value, status: 'unknown' }; notice.value = '操作结果未确认，资金仍冻结，请查询渠道结果。' }
    else { uncertain.value = action !== 'query'; failure.value = '退款处理结果未确认，请刷新或查询渠道结果。' }
  } finally { if (owns()) busy.value = false }
}
async function reconcile(outcome) {
  if (!canReconcile.value || busy.value || uncertain.value || !['succeeded', 'failed'].includes(outcome)) return
  const value = reference.value.trim()
  if (!/^[A-Za-z0-9._-]{1,96}$/.test(value)) { failure.value = '请填写有效的人工打款凭据编号'; return }
  const owns = capture(); busy.value = true; failure.value = ''
  try {
    const response = await reconcileTicketRefund(id(), { outcome, reference: value })
    if (!owns()) return
    receive(response); notice.value = outcome === 'succeeded' ? '人工退款已核销成功。' : '退款未完成，冻结已解除。'; props.onChanged()
  } catch { if (owns()) { uncertain.value = true; failure.value = '人工核销结果未确认，请刷新核对后再操作。' } }
  finally { if (owns()) busy.value = false }
}
async function uploadProof(event) {
  const input = event?.target, file = input?.files?.[0]
  if (input) input.value = ''
  if (!canUploadProof.value || busy.value || uncertain.value || !file) return
  if (!refundProofAllowed(file)) { failure.value = '凭证必须是 PNG / JPEG 图片，且不超过 2 MiB'; return }
  const owns = capture(); busy.value = true; failure.value = ''
  try {
    const response = await uploadTicketRefundProof(id(), file)
    if (!owns()) return
    refundResponseData(response); detail.value = { ...row.value, hasProof: true }; props.onChanged()
  } catch { if (owns()) { uncertain.value = true; failure.value = '凭证提交结果未确认，请刷新核对。' } }
  finally { if (owns()) busy.value = false }
}
async function showImage(fetchImage, title) {
  if (!ready.value || proofBusy.value) return
  const owns = capture(); proofBusy.value = true; failure.value = ''
  try {
    const response = await fetchImage()
    if (!owns()) return
    const blob = refundResponseData(response)
    if (!(blob instanceof Blob) || !refundProofAllowed(blob)) throw new Error('图片格式无效')
    closeProof(); proofTitle.value = title; proofUrl.value = URL.createObjectURL(blob)
  } catch { if (owns()) failure.value = '图片读取失败，请刷新重试。' }
  finally { if (owns()) proofBusy.value = false }
}
function viewProof() { if (row.value.hasProof) return showImage(() => getTicketRefundProof(id()), '退款核验凭证') }
function viewAttachment(attachmentId, title) { if (props.isAdmin && validRefundID(attachmentId)) return showImage(() => getTicketAttachmentAsAdmin(attachmentId), title) }
watch(() => [id(), props.refund?.status, props.refund?.approvedCents, props.isAdmin, currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status, getCurrentUserLogoutEpoch()], () => { reset(); void load() })
onMounted(load)
onBeforeUnmount(() => { alive = false; reset() })
</script>

<style scoped>
.refund-actions { margin-top: 16px; padding: 12px; border: 1px solid var(--console-border); border-radius: 8px; }
.refund-actions-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.refund-actions-head h3 { margin: 0; font-size: 14px; }
.refund-meta { display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr)); gap: 8px; margin: 0 0 12px; }
.refund-meta div { display: flex; flex-direction: column; gap: 2px; }
.refund-meta dt { color: var(--console-muted); font-size: 12px; }
.refund-meta dd { margin: 0; }
.refund-review-form { max-width: 280px; margin-top: 12px; }
.refund-failure { color: var(--danger); margin: 8px 0; }
.refund-notice { color: var(--success); margin: 8px 0; }
.refund-secondary-actions { margin-top: 12px; }
.refund-proof-image { width: 100%; height: auto; }
.refund-agreement-snapshot { white-space: pre-wrap; overflow-wrap: anywhere; font: inherit; }
</style>
