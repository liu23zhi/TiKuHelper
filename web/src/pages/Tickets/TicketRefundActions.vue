<template>
  <section class="refund-actions" data-testid="ticket-refund-actions">
    <header class="refund-actions-head"><h3>退款信息</h3><a-tag v-if="showStatus" :color="statusTone">{{ refundStatusLabel(row.status) }}</a-tag></header>
    <dl class="refund-meta">
      <div><dt>方式</dt><dd>{{ row.kind === 'manual' ? '人工退回' : '原路退回' }}</dd></div>
      <div><dt>申请金额</dt><dd>{{ formatRefundCents(row.requestedCents) }}</dd></div>
      <div v-if="row.approvedCents > 0"><dt>审核金额</dt><dd>{{ formatRefundCents(row.approvedCents) }}</dd></div>
      <div v-if="row.orderNo"><dt>关联订单</dt><dd>{{ row.orderNo }}</dd></div>
      <div v-if="row.reason"><dt>申请理由</dt><dd>{{ row.reason }}</dd></div>
    </dl>
    <p v-if="failure" class="refund-failure" role="alert">{{ failure }}</p>
    <p v-if="notice" class="refund-notice" role="status">{{ notice }}</p>
    <p v-if="loading" role="status">正在读取退款详情…</p>
    <template v-if="canReview">
      <a-form layout="vertical" class="refund-review-form"><a-form-item label="审核金额（元）" :html-for="fieldId('amount')">
        <a-input :id="fieldId('amount')" v-model:value="approveYuan" placeholder="留空表示按申请金额" :disabled="busy || uncertain"
          :aria-describedby="reviewAllocationCents === null ? undefined : fieldId('review-allocation')" />
      </a-form-item></a-form>
      <p v-if="reviewAllocationCents !== null" :id="fieldId('review-allocation')">已分配合计 ¥{{ formatRefundCents(reviewAllocationCents) }}。调整金额时，请先点「修改退款申请」保存新的逐笔分配。</p>
      <p v-if="row.kind === 'manual' && !manualEvidenceReady">请等待申请人上传退款核验凭证，核验后再通过审核。</p>
      <p v-else-if="row.kind === 'manual'">请核验本次收款码、本人签名及承诺书后，再通过审核。</p>
      <a-space>
        <a-button type="primary" :loading="busy" :disabled="busy || uncertain || (row.kind === 'manual' && !manualEvidenceReady)" @click="requestConfirmation('review', 'approve')">审核通过</a-button>
        <a-button danger :disabled="busy || uncertain" @click="requestConfirmation('review', 'reject')">驳回</a-button>
      </a-space>
    </template>
    <ul v-if="orderLines.length" class="refund-order-lines">
      <li v-for="line in orderLines" :key="line.orderId">
        <span>{{ line.orderNo }}</span>
        <span v-if="line.cents">¥{{ formatRefundCents(line.cents) }} · {{ refundStatusLabel(line.status) }}</span>
        <span v-else>{{ line.status === 'succeeded' ? '已标记退款' : '关联人工退款' }}</span>
        <a-button v-if="canProcessOriginal && line.status === 'frozen'" size="small" type="primary" :disabled="busy || uncertain" @click="requestConfirmation('process', 'submit', line.orderId)">提交该笔退款</a-button>
        <a-button v-else-if="canProcessOriginal && ['processing', 'unknown'].includes(line.status)" size="small" :disabled="busy" @click="process('query', line.orderId)">查询该笔结果</a-button>
        <a-button v-if="canProcessOriginal && row.startedAt && line.status === 'frozen'" size="small" danger :disabled="busy || uncertain" @click="requestConfirmation('process', 'cancel_unsent', line.orderId)">撤销该笔未提交退款</a-button>
      </li>
    </ul>
    <template v-if="canProcessOriginal">
      <a-space>
        <a-button v-if="!orderLines.length && row.status === 'frozen' && !row.startedAt" type="primary" :disabled="busy || uncertain" :loading="busy" @click="requestConfirmation('process', 'submit')">提交原路退款</a-button>
        <a-button v-if="!orderLines.length && (row.status !== 'frozen' || row.startedAt)" :disabled="busy" @click="process('query')">查询渠道结果</a-button>
        <a-button v-if="row.status === 'frozen' && !row.startedAt" danger :disabled="busy || uncertain" @click="requestConfirmation('process', 'cancel_unsent')">撤销未提交退款并解冻</a-button>
      </a-space>
    </template>
    <template v-if="canReconcile">
      <p v-if="!manualEvidenceReady">请先核验收款码、签名及承诺书，或受限凭证。</p>
      <p v-if="manualOrdersChanged">本次核销将标记 {{ manualOrderIDs.filter(orderId => orderId !== row.orderId).length }} 笔额外充值订单为已退款。</p>
      <a-form layout="vertical" class="refund-review-form"><a-form-item label="人工打款凭据编号" :html-for="fieldId('reference')"><a-input :id="fieldId('reference')" v-model:value="reference" :maxlength="96" :disabled="busy || uncertain" placeholder="填写实际打款凭据编号" /></a-form-item></a-form>
      <a-space>
        <a-button type="primary" :disabled="busy || uncertain || !manualEvidenceReady" @click="requestConfirmation('reconcile', 'succeeded')">确认人工退款成功</a-button>
        <a-button danger :disabled="busy || uncertain" @click="requestConfirmation('reconcile', 'failed')">确认未完成并解冻</a-button>
      </a-space>
    </template>
    <p v-if="canUploadProof"><input ref="proofUploadInput" type="file" accept="image/png,image/jpeg" hidden style="display:none" aria-label="选择退款核验凭证" :disabled="busy || uncertain" data-refund-proof-upload @change="uploadProof" /><a-button :disabled="busy || uncertain" @click="proofUploadInput?.click()">上传退款核验凭证</a-button> <small>PNG / JPEG，最多 2 MiB</small></p>
    <a-space class="refund-secondary-actions">
      <a-button v-if="canEdit" :disabled="busy || uncertain" @click="openEdit">修改退款申请</a-button>
      <a-button v-if="ready && isAdmin" :disabled="busy || uncertain" @click="openOrderHistory">查看充值记录</a-button>
      <a-button v-if="row.hasProof" :disabled="busy || proofBusy" @click="viewProof">查看受限凭证</a-button>
      <a-button v-if="isAdmin && row.payoutQrAttachmentId" :disabled="proofBusy" @click="viewAttachment(row.payoutQrAttachmentId, '收款码')">查看收款码</a-button>
      <a-button v-if="isAdmin && row.signatureAttachmentId" :disabled="proofBusy" @click="viewAttachment(row.signatureAttachmentId, '签名')">查看签名</a-button>
      <a-button :disabled="busy || loading" @click="reload">刷新</a-button>
    </a-space>
    <details v-if="row.agreementText" @toggle="loadSignedAgreement"><summary>本次签署的承诺书</summary><pre class="refund-agreement-snapshot">{{ refundAgreementBody(row.agreementText) }}</pre>
      <div class="refund-signed-evidence" data-refund-signed-evidence>
        <div>申请人（以电子签名方式确认）：<img v-if="signatureUrl" :src="signatureUrl" alt="本次退款申请的签名" /><span v-else>{{ signatureLoading ? '正在读取签名图片…' : '签名图片暂不可用' }}</span></div>
        <p>签署日期：{{ formatTicketTime(row.agreementSignedAt || row.createdAt) }}</p>
        <p>申请时间：{{ formatTicketTime(row.createdAt) }}</p>
      </div>
    </details>
    <a-modal :open="!!proofUrl" :title="proofTitle" :footer="null" @cancel="closeProof"><img :src="proofUrl" :alt="proofTitle" class="refund-proof-image" /></a-modal>
    <a-modal :open="!!orderMode" :title="orderMode === 'edit' ? '修改退款申请' : '充值记录'" :footer="null" :mask-closable="!busy" @cancel="closeOrders">
      <p v-if="orderFailure" role="alert" class="refund-failure">{{ orderFailure }}</p>
      <a-spin :spinning="ordersLoading">
        <a-form v-if="orderMode === 'edit'" layout="vertical">
          <a-form-item label="退款方式" :html-for="fieldId('edit-kind')">
            <a-select :id="fieldId('edit-kind')" v-model:value="editKind" :disabled="busy"
              :options="[{value: 'original', label: '原路退回'}, {value: 'manual', label: '人工退回'}]" @change="revisionKindChanged" />
          </a-form-item>
          <a-form-item label="申请金额（元）" :html-for="fieldId('edit-amount')">
            <a-input :id="fieldId('edit-amount')" v-model:value="editYuan" :disabled="busy || row.status !== 'pending_review'" @change="reallocate" />
          </a-form-item>
          <a-form-item label="申请理由" :html-for="fieldId('edit-reason')">
            <a-textarea :id="fieldId('edit-reason')" v-model:value="editReason" :maxlength="500" :disabled="busy" :rows="3" />
          </a-form-item>
          <a-checkbox v-if="row.status === 'frozen'" v-model:checked="unpaidConfirmed" :disabled="busy">确认尚未完成打款</a-checkbox>
          <template v-if="needsRevisionEvidence">
            <a-form-item label="收款码（PNG / JPEG）" :html-for="fieldId('edit-qr')">
              <input :id="fieldId('edit-qr')" ref="revisionQrInput" type="file" accept="image/png,image/jpeg" hidden style="display:none" aria-label="选择退款收款码" :disabled="busy || evidenceBusy" @change="uploadRevisionQr" />
              <a-button :disabled="busy || evidenceBusy" @click="revisionQrInput?.click()">{{ revisionQrId ? '替换收款码' : '上传收款码' }}</a-button>
              <span v-if="revisionQrId">已上传</span>
            </a-form-item>
            <a-button :disabled="!revisionQrId || evidenceBusy || busy" @click="openRevisionAgreement">{{ revisionSignatureId ? '查看已签承诺书' : '阅读并签署承诺书' }}</a-button>
            <a-form-item v-if="revisionPhone?.required" :label="'手机验证码 ' + revisionPhone.phoneMasked" :html-for="fieldId('edit-sms')">
              <a-space><a-input :id="fieldId('edit-sms')" v-model:value="revisionSmsCode" :maxlength="6" inputmode="numeric" :disabled="busy" />
                <a-button :disabled="busy || evidenceBusy" @click="sendRevisionSms">发送验证码</a-button>
              </a-space>
            </a-form-item>
          </template>
        </a-form>
        <template v-if="orderMode === 'history' || editKind === 'original' || isAdmin">
          <p v-if="orderMode === 'edit' && editKind === 'original'">已分配 ¥{{ formatRefundCents(allocationTotals.allocated) }} / ¥{{ formatRefundCents(allocationTarget) }}</p>
          <p v-if="orderMode === 'edit' && editKind === 'manual' && isAdmin">随人工退款成功标记的充值订单（可选）</p>
          <p v-if="!ordersLoading && !orders.length">暂无充值记录</p>
          <ul class="refund-order-lines">
            <li v-for="order in orders" :key="order.id">
              <a-checkbox v-if="orderMode === 'edit' && editKind === 'original'" :checked="!!allocationFor(order.id)" :disabled="busy || !order.originalSupported || (!allocationFor(order.id) && allocationTotals.remaining === 0)" @change="toggleAllocation(order.id, $event.target.checked)">{{ order.orderNo }}</a-checkbox>
              <a-checkbox v-else-if="manualSelectionVisible" :checked="manualOrderIDs.includes(order.id)" :disabled="busy || !order.manualSupported || (orderMode === 'history' && order.id === row.orderId)" @change="toggleManualOrder(order.id, $event.target.checked)">{{ order.orderNo }}</a-checkbox>
              <span v-else>{{ order.orderNo }}</span>
              <span>¥{{ formatRefundCents(order.paidCents) }} · {{ order.paymentMethodNameSnapshot || '充值' }} · {{ rechargeStatusLabel(order.status) }} · {{ formatTicketTime(order.paidAt || order.createdAt) }}</span>
              <small v-if="orderMode === 'edit' && editKind === 'original' && !order.originalSupported">{{ order.originalUnsupportedReason }}</small>
              <a-input v-if="orderMode === 'edit' && editKind === 'original' && allocationFor(order.id)" :value="allocationInput(order.id)" :aria-label="'订单 ' + order.orderNo + ' 的退款金额（元）'" :disabled="busy" @update:value="setAllocation(order.id, $event)" />
            </li>
          </ul>
        </template>
      </a-spin>
      <a-space>
        <a-button :disabled="busy" @click="closeOrders">关闭</a-button>
        <a-button v-if="orderMode === 'edit'" type="primary" :disabled="ordersLoading || busy || uncertain || evidenceBusy" :loading="busy" @click="saveRevision">保存修改</a-button>
        <a-button v-else-if="manualSelectionVisible" type="primary" :disabled="ordersLoading || busy" @click="closeOrders">保留本次选择</a-button>
      </a-space>
    </a-modal>
    <RefundAgreementDialog v-if="orderMode === 'edit' && needsRevisionEvidence" :visible="revisionAgreementVisible" :text="revisionAgreement"
      :signed-attachment-id="revisionSignatureId" :signed-at="revisionSignedAt" :payout-qr-ready="!!revisionQrId" @close="revisionAgreementVisible = false" @signed="onRevisionSigned" />
    <a-modal :open="!!confirmation" title="确认退款操作" :footer="null" :mask-closable="false" @cancel="confirmation = null">
      <div data-refund-action-confirmation><p>{{ confirmationText }}</p><a-space><a-button @click="confirmation = null">取消</a-button><a-button type="primary" @click="confirmAction">确认</a-button></a-space></div>
    </a-modal>
  </section>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { getRefundAgreement, getRefundPhoneVerification, getTicketRefundDetail, getTicketRefundProof, getTicketRefundRechargeOrders, processTicketRefund, reconcileTicketRefund, reviewTicketRefund, sendRefundPhoneSMSCode, updateTicketRefund, uploadTicketRefundProof } from '@/api/TikuComponentApi'
import { getTicketAttachment, getTicketAttachmentAsAdmin, uploadTicketAttachment } from '@/api/TicketApi'
import RefundAgreementDialog from '@/components/RefundAgreementDialog.vue'
import { refundAgreementBody } from '@/utils/refundAgreementDisplay.mjs'
import { formatTicketTime } from '@/utils/ticketHelpers.mjs'
import { getCurrentUserLogoutEpoch, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { formatRefundCents, parseRefundYuanCents, refundFailureMessage, refundProofAllowed, refundResponseData, refundStatusLabel, REFUND_ALLOCATION_MISMATCH_MESSAGE, validRefundID } from './refundTicketHelpers.mjs'
import { computeRefundAllocations, refundAllocationTotals } from './refundAllocation.mjs'

const props = defineProps({ refund: { type: Object, required: true }, isAdmin: Boolean, showStatus: { type: Boolean, default: true }, onChanged: { type: Function, default: () => {} } })
const uniqueId = useId(), fieldId = name => `ticket-refund-${uniqueId}-${name}`
const { currentUser } = useCurrentUserDisplay()
const actor = currentUserIdentity(currentUser.value), actorRole = currentUser.value?.role, logout = getCurrentUserLogoutEpoch()
const detail = ref(null), loading = ref(false), busy = ref(false), proofBusy = ref(false)
const failure = ref(''), notice = ref(''), approveYuan = ref(''), reference = ref(''), uncertain = ref(false)
const proofUrl = ref(''), proofTitle = ref('退款核验凭证')
const proofUploadInput = ref(null), revisionQrInput = ref(null)
const confirmation = ref(null)
const orderMode = ref(''), orders = ref([]), ordersLoading = ref(false), orderFailure = ref('')
const editKind = ref('manual'), editYuan = ref(''), editReason = ref(''), unpaidConfirmed = ref(false)
const allocations = ref([]), manualOrderIDs = ref([]), manualOrdersChanged = ref(false)
const revisionQrId = ref(0), revisionSignatureId = ref(0), revisionSignedAt = ref('')
const revisionAgreement = ref(''), revisionAgreementVisible = ref(false), evidenceBusy = ref(false)
const revisionPhone = ref(null), revisionSmsCode = ref('')
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
const canEdit = computed(() => ready.value && (row.value.status === 'pending_review' || (props.isAdmin && row.value.status === 'frozen' && !row.value.startedAt)))
const orderLines = computed(() => Array.isArray(row.value.orderLines) ? row.value.orderLines : [])
const reviewAllocationCents = computed(() => row.value.kind === 'original' && orderLines.value.length > 0
  ? orderLines.value.reduce((sum, line) => sum + line.cents, 0)
  : null)
const manualSelectionVisible = computed(() => props.isAdmin && (
  (orderMode.value === 'edit' && editKind.value === 'manual') || (orderMode.value === 'history' && canReconcile.value)
))
const needsRevisionEvidence = computed(() => !props.isAdmin && editKind.value === 'manual' && row.value.kind !== 'manual')
const allocationTarget = computed(() => row.value.status === 'frozen' ? row.value.approvedCents : parseRefundYuanCents(editYuan.value) || 0)
const allocationTotals = computed(() => refundAllocationTotals(allocationTarget.value, allocations.value))
const manualEvidenceReady = computed(() => row.value.hasProof || row.value.hasSignedPayoutEvidence)
const canUploadProof = computed(() => ready.value && !props.isAdmin && row.value.kind === 'manual' && ['pending_review', 'frozen'].includes(row.value.status) && !manualEvidenceReady.value)
const statusTone = computed(() => ({ pending_review: 'gold', frozen: 'blue', processing: 'blue', unknown: 'orange', succeeded: 'green', partially_succeeded: 'orange', rejected: 'red', failed: 'red' }[row.value.status] || 'default'))
const signatureUrl = ref(''), signatureLoading = ref(false)
async function loadSignedAgreement(event) {
  if (!event?.target?.open || !owned() || !row.value.signatureAttachmentId || signatureUrl.value || signatureLoading.value) return
  const owns = capture(); signatureLoading.value = true
  try {
    const response = await (props.isAdmin ? getTicketAttachmentAsAdmin : getTicketAttachment)(row.value.signatureAttachmentId)
    if (!owns()) return
    const blob = response?.data
    if (blob instanceof Blob && refundProofAllowed(blob)) signatureUrl.value = URL.createObjectURL(blob)
  } catch { if (owns()) failure.value = '签名图片读取失败，请刷新后重试。' }
  finally { if (owns()) signatureLoading.value = false }
}

function closeProof() { if (proofUrl.value) URL.revokeObjectURL(proofUrl.value); proofUrl.value = '' }
function reset() { version++; confirmation.value = null; detail.value = null; loading.value = busy.value = proofBusy.value = uncertain.value = signatureLoading.value = ordersLoading.value = evidenceBusy.value = false; failure.value = notice.value = approveYuan.value = reference.value = orderMode.value = orderFailure.value = ''; manualOrderIDs.value = []; manualOrdersChanged.value = false; orders.value = []; resetRevisionEvidence(); if (signatureUrl.value) URL.revokeObjectURL(signatureUrl.value); signatureUrl.value = ''; closeProof() }
function reviewAmountMatchesAllocation() {
  if (reviewAllocationCents.value === null) return true
  const amount = approveYuan.value.trim()
  const cents = amount ? parseRefundYuanCents(amount) : row.value.requestedCents
  if (cents === reviewAllocationCents.value) return true
  failure.value = REFUND_ALLOCATION_MISMATCH_MESSAGE
  return false
}
function requestConfirmation(kind, action, orderId) {
  if (!owned() || busy.value || uncertain.value) return
  if (kind === 'review' && action === 'approve' && !reviewAmountMatchesAllocation()) return
  confirmation.value = { kind, action, orderId }
}
function confirmAction() {
  const selected = confirmation.value
  confirmation.value = null
  if (!selected || !owned()) return
  if (selected.kind === 'review') return review(selected.action)
  if (selected.kind === 'process') return process(selected.action, selected.orderId)
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
function closeOrders() { if (!busy.value && !evidenceBusy.value) orderMode.value = '' }
function rechargeStatusLabel(status) { return ({ paid: '已支付', refunded: '已退款', pending: '待支付' }[status] || status) }
function allocationFor(orderId) { return allocations.value.find(line => line.orderId === orderId) }
function allocationInput(orderId) {
  const line = allocationFor(orderId)
  return line?.raw ?? (line?.cents ? formatRefundCents(line.cents) : '')
}
function reallocate(addedOrderId) {
  allocations.value = computeRefundAllocations({ orders: orders.value, requestedCents: allocationTarget.value, selections: allocations.value, addedOrderId })
}
function toggleAllocation(orderId, checked) {
  allocations.value = checked ? [...allocations.value, { orderId, cents: 0, manual: false }] : allocations.value.filter(line => line.orderId !== orderId)
  reallocate(checked ? orderId : undefined)
}
function setAllocation(orderId, raw) {
  allocations.value = allocations.value.map(line => line.orderId === orderId ? { ...line, manual: true, raw, cents: parseRefundYuanCents(raw) || 0 } : line)
  reallocate()
}
function toggleManualOrder(id, checked) {
  manualOrderIDs.value = checked ? [...manualOrderIDs.value, id] : manualOrderIDs.value.filter(orderId => orderId !== id)
  if (orderMode.value === 'history') manualOrdersChanged.value = true
}
async function loadOrders() {
  if (!owned() || ordersLoading.value) return
  const owns = capture()
  ordersLoading.value = true
  orderFailure.value = ''
  try {
    const data = refundResponseData(await getTicketRefundRechargeOrders(id(), props.isAdmin))
    if (owns() && Array.isArray(data)) orders.value = data
  } catch (error) { if (owns()) orderFailure.value = refundFailureMessage(error) || '充值记录读取失败，请重试。' }
  finally { if (owns()) ordersLoading.value = false }
}
function openEdit() {
  if (!canEdit.value || busy.value) return
  editKind.value = row.value.kind
  editYuan.value = formatRefundCents(row.value.requestedCents)
  editReason.value = (row.value.reason || '').split('\n【可退本金核算】', 1)[0]
  unpaidConfirmed.value = false
  resetRevisionEvidence()
  const lines = orderLines.value.filter(line => line.cents > 0)
  allocations.value = lines.map(line => ({ orderId: line.orderId, cents: line.cents, manual: true }))
  if (!lines.length && row.value.kind === 'original' && row.value.orderId) {
    allocations.value = [{ orderId: row.value.orderId, cents: allocationTarget.value, manual: false }]
  }
  manualOrderIDs.value = orderLines.value.filter(line => !line.cents).map(line => line.orderId)
  if (!manualOrderIDs.value.length && row.value.orderId) manualOrderIDs.value = [row.value.orderId]
  orderMode.value = 'edit'
  void loadOrders()
}
function resetRevisionEvidence() {
  revisionQrId.value = revisionSignatureId.value = 0
  revisionSignedAt.value = revisionAgreement.value = revisionSmsCode.value = ''
  revisionAgreementVisible.value = false
  revisionPhone.value = null
}
async function revisionKindChanged() {
  if (!needsRevisionEvidence.value || revisionPhone.value || evidenceBusy.value) return
  const owns = capture()
  evidenceBusy.value = true
  try {
    const data = refundResponseData(await getRefundPhoneVerification())
    if (owns()) revisionPhone.value = data
  } catch { if (owns()) orderFailure.value = '手机号核验状态读取失败，请切换退款方式后重试。' }
  finally { if (owns()) evidenceBusy.value = false }
}
async function uploadRevisionQr(event) {
  const file = event.target.files?.[0]
  event.target.value = ''
  if (!needsRevisionEvidence.value || !file || evidenceBusy.value || busy.value) return
  if (!refundProofAllowed(file)) { orderFailure.value = '收款码必须是 PNG / JPEG 图片，且不超过 2 MiB。'; return }
  const owns = capture()
  evidenceBusy.value = true
  orderFailure.value = ''
  try {
    const data = refundResponseData(await uploadTicketAttachment(file))
    if (!owns()) return
    const attachmentId = Number(data?.data?.id)
    if (!validRefundID(attachmentId)) throw new Error('无效附件')
    revisionQrId.value = attachmentId
    revisionSignatureId.value = 0
    revisionSignedAt.value = ''
  } catch { if (owns()) orderFailure.value = '收款码上传失败，请重新上传。' }
  finally { if (owns()) evidenceBusy.value = false }
}
async function openRevisionAgreement() {
  if (!revisionQrId.value || evidenceBusy.value || busy.value) return
  const owns = capture()
  evidenceBusy.value = true
  orderFailure.value = ''
  try {
    if (!revisionAgreement.value) {
      const data = refundResponseData(await getRefundAgreement())
      if (!owns()) return
      const value = data?.text?.trim()
      if (!value) throw new Error('无效协议')
      revisionAgreement.value = value
    }
    revisionAgreementVisible.value = true
  } catch { if (owns()) orderFailure.value = '承诺书读取失败，请重试。' }
  finally { if (owns()) evidenceBusy.value = false }
}
function onRevisionSigned(attachmentId, signedAt) {
  if (!owned() || !needsRevisionEvidence.value || orderMode.value !== 'edit') return
  revisionSignatureId.value = Number(attachmentId) || 0
  revisionSignedAt.value = String(signedAt || '')
  revisionAgreementVisible.value = false
}
async function sendRevisionSms() {
  if (!revisionPhone.value?.required || evidenceBusy.value || busy.value) return
  const owns = capture()
  evidenceBusy.value = true
  orderFailure.value = ''
  try {
    refundResponseData(await sendRefundPhoneSMSCode())
    if (owns()) orderFailure.value = '验证码已发送至绑定手机号。'
  } catch { if (owns()) orderFailure.value = '验证码发送失败，请稍后重试。' }
  finally { if (owns()) evidenceBusy.value = false }
}
function openOrderHistory() {
  if (!ready.value || !props.isAdmin || busy.value) return
  if (!manualOrdersChanged.value) manualOrderIDs.value = orderLines.value.map(line => line.orderId)
  if (!manualOrdersChanged.value && row.value.orderId) manualOrderIDs.value = [row.value.orderId]
  orderMode.value = 'history'
  void loadOrders()
}
async function saveRevision() {
  if (!canEdit.value || busy.value || uncertain.value || ordersLoading.value) return
  const requestedCents = parseRefundYuanCents(editYuan.value)
  if (!requestedCents) { orderFailure.value = '请输入有效的申请金额。'; return }
  if (row.value.status === 'frozen' && !unpaidConfirmed.value) { orderFailure.value = '请确认尚未完成打款。'; return }
  const payload = { manage: props.isAdmin, kind: editKind.value, requestedCents, reason: editReason.value, unpaidConfirmed: unpaidConfirmed.value }
  if (editKind.value === 'original') {
    if (!allocations.value.length || allocationTotals.value.remaining || allocationTotals.value.excess || allocations.value.some(line =>
      !line.cents || !orders.value.some(order => order.id === line.orderId && order.originalSupported && order.paidCents >= line.cents))) {
      orderFailure.value = '请选择原路退款订单，并将本次金额足额分配到所选订单。'
      return
    }
    payload.allocations = allocations.value.map(({ orderId, cents }) => ({ orderId, cents }))
  } else if (props.isAdmin) payload.manualOrderIds = manualOrderIDs.value
  else if (needsRevisionEvidence.value) {
    if (!revisionQrId.value || !revisionSignatureId.value || !revisionPhone.value ||
      (revisionPhone.value.required && !/^\d{6}$/.test(revisionSmsCode.value))) {
      orderFailure.value = '请上传收款码、签署承诺书并完成所需的手机号核验。'
      return
    }
    Object.assign(payload, { payoutQrAttachmentId: revisionQrId.value, signatureAttachmentId: revisionSignatureId.value,
      agreementText: revisionAgreement.value, phoneSmsCode: revisionSmsCode.value })
  }
  const owns = capture()
  busy.value = true
  orderFailure.value = ''
  try {
    const response = await updateTicketRefund(id(), payload)
    if (!owns()) return
    receive(response)
    orderMode.value = ''
    notice.value = '退款申请已修改。'
    props.onChanged()
  } catch (error) {
    if (owns()) {
      const message = refundFailureMessage(error)
      uncertain.value = !message
      orderFailure.value = message || '修改结果未确认，请关闭后刷新核对。'
    }
  }
  finally { if (owns()) busy.value = false }
}
async function review(action) {
  if (!canReview.value || busy.value || uncertain.value || !['approve', 'reject'].includes(action)) return
  const payload = { action }, amount = approveYuan.value.trim()
  if (action === 'approve') {
    if (!reviewAmountMatchesAllocation()) return
    if (row.value.kind === 'manual' && !manualEvidenceReady.value) { failure.value = '人工退款审核前须核验收款码、签名和协议，或受限凭证'; return }
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
  } catch (error) {
    if (owns()) {
      const message = refundFailureMessage(error)
      uncertain.value = !message
      failure.value = message || '审核结果未确认，请刷新核对后再操作。'
    }
  }
  finally { if (owns()) busy.value = false }
}
async function process(action, orderId) {
  if (!canProcessOriginal.value || busy.value || !['submit', 'query', 'cancel_unsent'].includes(action)) return
  const line = orderId == null ? null : orderLines.value.find(item => item.orderId === orderId)
  if (orderId != null && !line) return
  const target = line || row.value
  const unsent = target.status === 'frozen' && !target.startedAt
  if (action !== 'query' && (!unsent || uncertain.value)) return
  if (action === 'query' && unsent) return
  const owns = capture(); busy.value = true; failure.value = ''
  try {
    const response = await processTicketRefund(id(), orderId == null ? { action } : { action, orderId })
    if (!owns()) return
    receive(response)
    if (response.status === 202 && !line) detail.value = { ...detail.value, status: 'unknown' }
    notice.value = response.status === 202 ? '操作结果未确认，请查询渠道结果。' : action === 'cancel_unsent' && !line ? '退款未完成，冻结已解除。' : '退款状态已更新。'
    props.onChanged()
  } catch (error) {
    if (!owns()) return
    const message = refundFailureMessage(error)
    if (message) failure.value = message
    else if (action === 'submit' && !line) { detail.value = { ...row.value, status: 'unknown' }; notice.value = '操作结果未确认，资金仍冻结，请查询渠道结果。' }
    else { uncertain.value = action !== 'query'; failure.value = '退款处理结果未确认，请刷新或查询渠道结果。' }
  } finally { if (owns()) busy.value = false }
}
async function reconcile(outcome) {
  if (!canReconcile.value || busy.value || uncertain.value || !['succeeded', 'failed'].includes(outcome)) return
  if (outcome === 'succeeded' && !manualEvidenceReady.value) return
  const value = reference.value.trim()
  if (!/^[A-Za-z0-9._-]{1,96}$/.test(value)) { failure.value = '请填写有效的人工打款凭据编号'; return }
  const owns = capture(); busy.value = true; failure.value = ''
  try {
    const payload = { outcome, reference: value }
    if (outcome === 'succeeded' && manualOrdersChanged.value) payload.orderIds = manualOrderIDs.value.filter(orderId => orderId !== row.value.orderId)
    const response = await reconcileTicketRefund(id(), payload)
    if (!owns()) return
    receive(response); notice.value = outcome === 'succeeded' ? '人工退款已核销成功。' : '退款未完成，冻结已解除。'; props.onChanged()
  } catch (error) {
    if (owns()) {
      const message = refundFailureMessage(error)
      uncertain.value = !message
      failure.value = message || '人工核销结果未确认，请刷新核对后再操作。'
    }
  }
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
.refund-order-lines { list-style: none; margin: 8px 0; padding: 0; }
.refund-order-lines li { display: flex; align-items: baseline; flex-wrap: wrap; gap: 8px; border-top: 1px solid var(--console-border); padding: 7px 0; overflow-wrap: anywhere; }
.refund-order-lines .ant-input { max-width: 130px; }
.refund-proof-image { width: 100%; height: auto; }
.refund-agreement-snapshot { white-space: pre-wrap; overflow-wrap: anywhere; font: inherit; }
.refund-signed-evidence { padding-top: 12px; border-top: 1px solid var(--console-border); }
.refund-signed-evidence > div { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.refund-signed-evidence img { height: 80px; max-width: 100%; background: #fff; border: 1px solid var(--console-border); border-radius: 6px; }
</style>
