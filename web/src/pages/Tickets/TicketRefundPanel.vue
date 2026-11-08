<template>
  <ConsolePanel
    title="退款工单"
    :description="reviewer ? '审核退款金额并核对处理结果。冻结不代表已退款。' : '退款申请独立于普通工单。审核通过后冻结相应余额，结果以工单状态为准。'"
    class="refund-panel"
  >
    <a-space wrap class="refund-actions">
      <a-button :loading="loading" :disabled="mutating || proofBusy" @click="load">刷新退款状态</a-button>
      <span v-if="!reviewer && ordersReady">可原路申请的订单：{{ eligibleOrders.length }} 笔</span>
    </a-space>
    <a-alert v-if="notice" :type="noticeType" :message="notice" show-icon />
    <a-alert
      v-if="!reviewer && ordersReadFailed"
      type="warning"
      message="可退款订单未确认，请刷新。未确认前不开放人工申请。"
      show-icon
    />
    <a-alert
      v-if="uncertain"
      type="warning"
      message="上一次操作结果未确认。请先刷新核对，不要重复提交。"
      show-icon
    />

    <form v-if="!reviewer && ordersReady" class="refund-form" @submit.prevent="submitApplication">
      <a-alert
        v-if="kind === 'manual'"
        type="info"
        message="当前没有可原路申请的订单。可提交人工审核；收款码或凭证只能在申请下方的受限入口上传。"
        show-icon
      />
      <div v-else class="refund-field">
        <label :for="fieldId('order')">原充值订单</label>
        <a-select
          :id="fieldId('order')"
          :value="orderId"
          :options="orderOptions"
          :disabled="actionsBlocked"
          placeholder="请选择 60 天内可退款的已付订单"
          @update:value="selectOrder"
        />
      </div>
      <div class="refund-field">
        <label :for="fieldId('amount')">申请金额（元）</label>
        <a-input
          :id="fieldId('amount')"
          :value="requestedYuan"
          inputmode="decimal"
          :maxlength="15"
          :disabled="actionsBlocked"
          placeholder="例如 12.50"
          @update:value="setRequestedYuan"
        />
      </div>
      <div class="refund-field">
        <label :for="fieldId('reason')">退款说明（可选）</label>
        <a-textarea
          :id="fieldId('reason')"
          :value="reason"
          :rows="2"
          :maxlength="200"
          :disabled="actionsBlocked"
          placeholder="请勿填写收款码、支付账号或证件号码"
          @update:value="setReason"
        />
      </div>
      <a-button type="primary" html-type="submit" :loading="mutating" :disabled="actionsBlocked">
        {{ kind === 'manual' ? '提交人工退款申请' : '提交原路退款申请' }}
      </a-button>
    </form>

    <a-spin :spinning="loading">
      <div v-if="rows.length" class="refund-list">
        <a-card v-for="record in rows" :key="record.id" size="small" class="refund-record">
          <div class="refund-record-heading">
            <strong>退款 #{{ record.id }}</strong>
            <a-tag>{{ refundStatusLabel(record.status) }}</a-tag>
          </div>
          <dl class="refund-meta">
            <div><dt>方式</dt><dd>{{ record.kind === 'manual' ? '人工审核' : '原路退款' }}</dd></div>
            <div v-if="reviewer"><dt>申请用户</dt><dd>{{ record.userId || '—' }}</dd></div>
            <div v-if="record.orderId"><dt>充值订单</dt><dd>{{ record.orderId }}</dd></div>
            <div><dt>申请</dt><dd>¥{{ formatRefundCents(record.requestedCents) }}</dd></div>
            <div v-if="record.approvedCents > 0"><dt>审核金额</dt><dd>¥{{ formatRefundCents(record.approvedCents) }}</dd></div>
            <div><dt>申请时间</dt><dd>{{ formatTicketTime(record.createdAt) }}</dd></div>
          </dl>
          <p v-if="record.reason" class="refund-reason">{{ record.reason }}</p>

          <div v-if="record.hasProof || mayUploadProof(record)" class="refund-proof-actions">
            <a-button v-if="record.hasProof" :disabled="actionsBlocked || proofBusy" @click="viewProof(record)">查看受限凭证</a-button>
            <label v-if="mayUploadProof(record)" class="refund-proof-input">
              <span>{{ record.hasProof ? '更换收款码或凭证（PNG / JPEG，最多 2 MiB）' : '上传收款码或凭证（PNG / JPEG，最多 2 MiB）' }}</span>
              <input type="file" accept="image/png,image/jpeg" :disabled="actionsBlocked || proofBusy" @change="uploadProof(record, $event)" />
            </label>
          </div>

          <div v-if="reviewer" class="refund-review">
            <template v-if="record.status === 'pending_review'">
              <label :for="fieldId('review-' + record.id)">审核金额（元）</label>
              <a-input
                :id="fieldId('review-' + record.id)"
                :value="reviewYuan[record.id] ?? formatRefundCents(record.requestedCents)"
                inputmode="decimal"
                :maxlength="15"
                :disabled="actionsBlocked"
                @update:value="value => setReviewYuan(record, value)"
              />
              <a-space wrap>
                <a-popconfirm title="确认审核通过并冻结相应余额？" @confirm="review(record, 'approve')">
                  <a-button type="primary" :disabled="actionsBlocked || (record.kind === 'manual' && !record.hasProof)">审核通过</a-button>
                </a-popconfirm>
                <a-popconfirm title="确认驳回这笔退款申请？" @confirm="review(record, 'reject')">
                  <a-button :disabled="actionsBlocked">驳回</a-button>
                </a-popconfirm>
              </a-space>
            </template>
            <a-popconfirm
              v-if="record.kind === 'original' && record.status === 'frozen'"
              title="确认由已核验的原渠道提交退款？"
              @confirm="process(record, 'submit')"
            >
              <a-button type="primary" :disabled="actionsBlocked">提交原路退款</a-button>
            </a-popconfirm>
            <a-popconfirm
              v-if="record.kind === 'original' && record.status === 'frozen'"
              title="仅尚未向渠道提交的退款可撤销并解冻；已提交的只能核对。确认撤销？"
              @confirm="process(record, 'cancel_unsent')"
            >
              <a-button :disabled="actionsBlocked">撤销未提交退款并解冻</a-button>
            </a-popconfirm>
            <a-button
              v-if="record.kind === 'original' && ['processing', 'unknown'].includes(record.status)"
              :disabled="actionsBlocked"
              @click="process(record, 'query')"
            >查询渠道结果</a-button>
            <template v-if="record.kind === 'manual' && record.status === 'frozen'">
              <label :for="fieldId('reference-' + record.id)">人工核对流水标识</label>
              <a-input
                :id="fieldId('reference-' + record.id)"
                :value="reconcileReference[record.id] ?? ''"
                :maxlength="96"
                :disabled="actionsBlocked"
                placeholder="只填操作凭据编号，不填收款账号"
                @update:value="value => setReconcileReference(record, value)"
              />
              <a-space wrap>
                <a-popconfirm title="仅在核实已实际退款后记录打款。是否继续？" @confirm="reconcile(record, 'succeeded')">
                  <a-button type="primary" :disabled="actionsBlocked">记录已核实的人工打款</a-button>
                </a-popconfirm>
                <a-popconfirm title="仅在明确未退款时确认失败并解除冻结。是否继续？" @confirm="reconcile(record, 'failed')">
                  <a-button :disabled="actionsBlocked">确认未打款并解除冻结</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </div>
        </a-card>
      </div>
      <a-empty v-else-if="loaded && !loading" description="暂无退款工单" />
    </a-spin>
    <a-modal :open="Boolean(proofUrl)" title="受限退款凭证" :footer="null" @cancel="closeProof">
      <img v-if="proofUrl" :src="proofUrl" class="refund-proof-image" alt="本退款工单凭证" referrerpolicy="no-referrer" />
    </a-modal>
  </ConsolePanel>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref, useId } from 'vue'
import {
  createTicketRefund,
  getEligibleTicketRefundOrders,
  getTicketRefundProof,
  listTicketRefunds,
  processTicketRefund,
  reconcileTicketRefund,
  reviewTicketRefund,
  uploadTicketRefundProof,
} from '@/api/TikuComponentApi'
import { formatTicketTime } from '@/utils/ticketHelpers.mjs'
import {
  formatRefundCents, parseRefundYuanCents, refundListData, refundProofAllowed,
  refundResponseData, refundStatusLabel, validRefundID,
} from './refundTicketHelpers.mjs'

const props = defineProps({
  isAdmin: Boolean,
  captureOwner: { type: Function, required: true },
})

// Never recapture a later login from an event on an old render.
const reviewer = props.isAdmin
const ownsMounted = props.captureOwner(reviewer)
const lifetime = new AbortController()
let active = true, readVersion = 0, proofVersion = 0
function owns() { return active && ownsMounted() && props.isAdmin === reviewer }

const uniqueId = useId()
const fieldId = (field) => 'ticket-refund-' + uniqueId + '-' + field
const loading = ref(false), mutating = ref(false), proofBusy = ref(false), loaded = ref(false)
const uncertain = ref(false), notice = ref(''), noticeType = ref('error')
const rows = ref([])
const eligibleOrders = ref([]), ordersReady = ref(false), ordersReadFailed = ref(false)
const orderId = ref(undefined), requestedYuan = ref(''), reason = ref('')
const reviewYuan = reactive({})
const reconcileReference = reactive({})
const proofUrl = ref('')
const kind = computed(() => eligibleOrders.value.length ? 'original' : 'manual')
const actionsBlocked = computed(() => !loaded.value || loading.value || mutating.value || proofBusy.value || uncertain.value)
const orderOptions = computed(() => eligibleOrders.value.map((order) => ({
  value: validRefundID(order.id),
  label: String(order.orderNo || '#' + order.id) + ' · ¥' + formatRefundCents(order.paidCents)
    + (order.paidAt ? ' · ' + formatTicketTime(order.paidAt) : ''),
})))

function show(message, type = 'error') {
  if (!owns()) return
  notice.value = message
  noticeType.value = type
}

function closeProof() {
  proofVersion += 1
  if (proofUrl.value) URL.revokeObjectURL(proofUrl.value)
  proofUrl.value = ''
}

async function load() {
  if (!owns() || loading.value || mutating.value || proofBusy.value) return
  const version = ++readVersion
  closeProof()
  loading.value = true
  notice.value = ''
  ordersReadFailed.value = false
  if (!reviewer) ordersReady.value = false
  const current = () => owns() && readVersion === version
  try {
    const signal = lifetime.signal
    const itemsRequest = listTicketRefunds(signal)
      .then(refundResponseData).then(refundListData)
    const ordersRequest = reviewer ? Promise.resolve(null) : getEligibleTicketRefundOrders(signal)
      .then(refundResponseData).then(refundListData).then((items) => {
        if (items.some((item) => !Number.isSafeInteger(item.paidCents) || item.paidCents <= 0)) {
          throw new Error('订单金额未确认')
        }
        return items
      })
    const [items, orders] = await Promise.allSettled([itemsRequest, ordersRequest])
    if (!current()) return
    if (items.status === 'fulfilled') {
      rows.value = items.value
      loaded.value = true
      uncertain.value = false
      for (const id of Object.keys(reviewYuan)) delete reviewYuan[id]
      for (const id of Object.keys(reconcileReference)) delete reconcileReference[id]
    } else {
      loaded.value = false
      rows.value = []
      show('退款列表未加载，请刷新后重试。')
    }
    if (!reviewer && orders.status === 'fulfilled') {
      eligibleOrders.value = orders.value
      ordersReady.value = true
      if (orderId.value && !orders.value.some((row) => validRefundID(row.id) === orderId.value)) {
        orderId.value = undefined
      }
    } else if (!reviewer) {
      eligibleOrders.value = []
      ordersReadFailed.value = true
    }
  } finally {
    if (current()) loading.value = false
  }
}

function selectOrder(value) { if (owns() && !actionsBlocked.value) orderId.value = validRefundID(value) }
function setRequestedYuan(value) { if (owns() && !actionsBlocked.value) requestedYuan.value = String(value ?? '') }
function setReason(value) { if (owns() && !actionsBlocked.value) reason.value = String(value ?? '') }
function currentRecord(record) { return owns() && rows.value.includes(record) && !actionsBlocked.value }
function setReviewYuan(record, value) {
  if (reviewer && currentRecord(record)) reviewYuan[record.id] = String(value ?? '')
}
function setReconcileReference(record, value) {
  if (reviewer && currentRecord(record)) reconcileReference[record.id] = String(value ?? '')
}

function failureMessage(error) {
  if (error?.code === 'REFUND_CHANNEL_UNAVAILABLE') return '退款渠道尚未核验或未启用，不能提交原路退款。'
  if (error?.code === 'REFUND_CHANNEL_UNKNOWN') return '渠道结果待核对，资金继续冻结；请勿重新提交。'
  if (error?.code === 'REFUND_INSUFFICIENT_BALANCE') return '可用余额不足，无法冻结审核金额。'
  if (error?.code === 'REFUND_ORDER_UNAVAILABLE') return '原订单已不可退款，请刷新订单和工单状态。'
  if (error?.code === 'REFUND_PROOF_REQUIRED') return '人工退款必须先上传并核对受限凭证。'
  if (error?.code === 'REFUND_PROOF_INVALID') return '凭证不符合图片格式或大小要求。'
  if (error?.httpStatus === 409) return '状态或余额已变化，请刷新核对后再处理。'
  if (error?.httpStatus === 401 || error?.httpStatus === 403) return '当前账号无权执行此退款操作。'
  if (error?.httpStatus === 400 || error?.httpStatus === 422) return '资料或金额不符合退款要求，请核对后再试。'
  return '操作未确认，请刷新核对状态，不要重复提交。'
}

async function mutate(call, success, after) {
  if (!owns() || actionsBlocked.value) return
  closeProof()
  notice.value = ''
  mutating.value = true
  let refresh = false
  try {
    refundResponseData(await call(lifetime.signal))
    if (!owns()) return
    refresh = true
    after?.()
  } catch (error) {
    if (!owns()) return
    if (!error?.httpStatus || error.httpStatus >= 500 || error.httpStatus === 409) uncertain.value = true
    show(failureMessage(error))
  } finally {
    if (owns()) mutating.value = false
  }
  if (refresh && owns()) {
    await load()
    if (owns() && loaded.value && !notice.value) show(success, 'info')
  }
}

function submitApplication() {
  if (reviewer || !owns() || actionsBlocked.value || !ordersReady.value) return
  const requestedCents = parseRefundYuanCents(requestedYuan.value)
  if (requestedCents == null) return show('请输入大于 0、最多两位小数的退款金额。')
  const order = eligibleOrders.value.find((item) => validRefundID(item.id) === orderId.value)
  if (kind.value === 'original' && !order) return show('请选择一笔可退款的充值订单。')
  if (order && requestedCents > order.paidCents) return show('申请金额不能超过原订单实付金额。')
  const safeReason = reason.value.trim()
  if (safeReason.length > 200) return show('退款说明最多 200 字。')
  const input = { kind: kind.value, requestedCents }
  if (order) input.orderId = order.id
  if (safeReason) input.reason = safeReason
  return mutate((signal) => createTicketRefund(input, signal), '申请已提交，等待审核。', () => {
    orderId.value = undefined
    requestedYuan.value = ''
    reason.value = ''
  })
}

function review(record, action) {
  if (!reviewer || !currentRecord(record) || record.status !== 'pending_review') return
  const input = { action }
  if (action === 'approve') {
    if (record.kind === 'manual' && !record.hasProof) return show('请先核对受限凭证。')
    const approvedCents = parseRefundYuanCents(reviewYuan[record.id] ?? formatRefundCents(record.requestedCents))
    if (!approvedCents || approvedCents > record.requestedCents) return show('审核金额须大于 0，且不能超过申请金额。')
    input.approvedCents = approvedCents
  }
  return mutate((signal) => reviewTicketRefund(record.id, input, signal),
    action === 'approve' ? '审核通过，余额已冻结；这不是退款成功。' : '退款申请已驳回。')
}

function process(record, action) {
  if (!reviewer || !currentRecord(record) || record.kind !== 'original') return
  if (action === 'submit' || action === 'cancel_unsent') {
    if (record.status !== 'frozen') return
  } else if (action !== 'query' || !['processing', 'unknown'].includes(record.status)) return
  return mutate((signal) => processTicketRefund(record.id, { action }, signal), '操作已返回，请以最新工单状态为准。')
}

function reconcile(record, outcome) {
  if (!reviewer || !currentRecord(record) || record.kind !== 'manual' || record.status !== 'frozen') return
  const reference = (reconcileReference[record.id] ?? '').trim()
  if (!/^[A-Za-z0-9._-]{1,96}$/.test(reference)) return show('请输入不超过 96 位的流水标识，只含字母、数字、点、下划线或连字符。')
  return mutate((signal) => reconcileTicketRefund(record.id, { outcome, reference }, signal), '已记录核对结果，请以最新工单状态为准。')
}

function mayUploadProof(record) {
  return !reviewer && record.kind === 'manual' && record.status === 'pending_review'
}

async function uploadProof(record, event) {
  const file = event.target?.files?.[0]
  if (event.target) event.target.value = ''
  if (!currentRecord(record) || !mayUploadProof(record)) return
  if (!refundProofAllowed(file)) return show('仅接受不超过 2 MiB 的 PNG 或 JPEG 图片。')
  return mutate((signal) => uploadTicketRefundProof(record.id, file, signal), '受限凭证已上传。')
}

async function viewProof(record) {
  if (!currentRecord(record) || !record.hasProof) return
  closeProof()
  const version = proofVersion
  proofBusy.value = true
  notice.value = ''
  try {
    const image = refundResponseData(await getTicketRefundProof(record.id, lifetime.signal))
    if (!owns() || version !== proofVersion || !rows.value.includes(record)) return
    if (!(image instanceof Blob) || !refundProofAllowed(image)) return show('返回的凭证格式无效，未显示。')
    proofUrl.value = URL.createObjectURL(image)
  } catch {
    if (owns() && version === proofVersion) show('未能读取受限凭证，请确认权限后重试。')
  } finally {
    if (owns()) proofBusy.value = false
  }
}

onMounted(load)
onBeforeUnmount(() => {
  active = false
  lifetime.abort()
  closeProof()
})
</script>

<style scoped>
.refund-panel { margin-bottom: 16px; }
.refund-form, .refund-list, .refund-review { display: grid; gap: 12px; }
.refund-form { margin: 16px 0; max-width: 42rem; }
.refund-field { display: grid; gap: 4px; }
.refund-actions { margin-bottom: 8px; }
.refund-list { margin-block: 12px; }
.refund-record-heading { display: flex; justify-content: space-between; align-items: center; gap: 8px; }
.refund-meta { display: flex; flex-wrap: wrap; gap: 8px 18px; margin-block: 8px; }
.refund-meta dt { font-size: 0.86em; }
.refund-meta dd { margin: 0; }
.refund-reason { overflow-wrap: anywhere; }
.refund-proof-actions, .refund-proof-input { display: grid; gap: 6px; }
.refund-proof-actions, .refund-review { margin-top: 12px; justify-items: start; }
.refund-proof-image { max-width: 100%; max-height: 70vh; object-fit: contain; }
</style>
