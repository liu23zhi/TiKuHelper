<template>
  <!--
    工单详情内的退款操作区（2026-11-18 重构）。

    为什么是独立组件：原 TicketRefundPanel.vue 是一整套「列表 + 表单 + 审核」
    的工作台，与工单列表并排渲染，等于同一份数据两套 UI。
    现在只保留一个工单列表，退款操作收进详情 —— 本组件只负责**单张工单**的
    退款状态展示与审核动作，不再有自己的列表与提交表单。

    提交入口：走「发起工单 → 退款申请」（已在 createTicketTypeHelpers 里）。
  -->
  <section class="refund-actions" data-testid="ticket-refund-actions">
    <header class="refund-actions-head">
      <h3>退款信息</h3>
      <a-tag :color="statusTone">{{ refundStatusLabel(refund.status) }}</a-tag>
    </header>

    <dl class="refund-meta">
      <div><dt>方式</dt><dd>{{ refund.kind === 'manual' ? '人工审核' : '原路退回' }}</dd></div>
      <div><dt>申请金额</dt><dd>{{ formatRefundCents(refund.requestedCents) }}</dd></div>
      <div v-if="refund.approvedCents > 0">
        <dt>审核金额</dt><dd>{{ formatRefundCents(refund.approvedCents) }}</dd>
      </div>
      <div v-if="refund.orderNo"><dt>关联订单</dt><dd>{{ refund.orderNo }}</dd></div>
    </dl>

    <p v-if="failure" class="refund-failure" role="alert">{{ failure }}</p>
    <p v-if="notice" class="refund-notice" role="status">{{ notice }}</p>

    <!-- 审核动作：仅管理员、且这笔退款还在「待审核」时才出现 -->
    <template v-if="canReview">
      <a-form layout="vertical" class="refund-review-form">
        <a-form-item label="审核金额（元）" :html-for="fieldId('amount')">
          <a-input
            :id="fieldId('amount')"
            v-model:value="approveYuan"
            placeholder="留空表示按申请金额"
            :disabled="busy"
          />
        </a-form-item>
      </a-form>
      <a-space>
        <a-popconfirm title="确认审核通过并冻结相应余额？" @confirm="review('approve')">
          <a-button type="primary" :loading="busy" :disabled="busy">审核通过</a-button>
        </a-popconfirm>
        <a-popconfirm title="确认驳回这笔退款申请？" @confirm="review('reject')">
          <a-button danger :disabled="busy">驳回</a-button>
        </a-popconfirm>
        <a-button :disabled="busy" @click="reload">刷新</a-button>
      </a-space>
    </template>

    <!-- 打款动作：已通过（余额已冻结）才出现 -->
    <template v-else-if="canProcess">
      <a-space>
        <a-popconfirm title="确认已完成打款？" @confirm="process">
          <a-button type="primary" :loading="busy" :disabled="busy">标记已打款</a-button>
        </a-popconfirm>
        <a-button :disabled="busy" @click="reload">刷新</a-button>
      </a-space>
    </template>

    <template v-else>
      <a-space>
        <a-button v-if="refund.hasProof" :disabled="busy" :loading="proofBusy" @click="viewProof">
          查看凭证
        </a-button>
        <a-button :disabled="busy" @click="reload">刷新</a-button>
      </a-space>
    </template>
  </section>
</template>

<script setup>
import { computed, ref, useId, watch } from 'vue'
import {
  getTicketRefundProof, processTicketRefund, reviewTicketRefund,
} from '@/api/TikuComponentApi'
import {
  formatRefundCents, parseRefundYuanCents, refundProofAllowed, refundStatusLabel,
  validRefundID,
} from './refundTicketHelpers.mjs'

const props = defineProps({
  // refund 直接来自工单详情（GET /tickets/:id 的 detail.refund）。
  // 详情已带摘要，本组件**不再自己列表查询** —— 那是原面板的职责。
  refund: { type: Object, required: true },
  isAdmin: Boolean,
  // 操作成功后通知父组件重新拉详情，保证状态与操作按钮同步。
  onChanged: { type: Function, default: () => {} },
})

const uniqueId = useId()
const fieldId = (name) => `ticket-refund-${uniqueId}-${name}`

const busy = ref(false)
const proofBusy = ref(false)
const failure = ref('')
const notice = ref('')
const approveYuan = ref('')

// 状态 → 可执行动作。判据集中在 computed 里，模板只读结果，
// 避免「按钮可见但点了报错」这种状态错配。
const status = computed(() => String(props.refund?.status || ''))
const canReview = computed(() => {
  if (!props.isAdmin || status.value !== 'pending') return false
  // 人工退款（kind=manual）在审核通过前**必须先有凭证**：
  // 后端 controller/ticket_refund.go:193-200 会以 ErrTicketRefundProofRequired 拒绝，
  // 前端提前拦一道，避免管理员点了「审核通过」才看到错误。
  // 驳回不需要凭证（驳回不动钱）。
  return true
})
const canProcess = computed(() => props.isAdmin && status.value === 'approved')
const statusTone = computed(() => {
  if (status.value === 'pending') return 'gold'
  if (status.value === 'approved') return 'blue'
  if (status.value === 'completed') return 'green'
  if (status.value === 'rejected') return 'red'
  return 'default'
})

function reset() {
  failure.value = ''
  notice.value = ''
  approveYuan.value = ''
}
// 切换到另一张工单时清空上一张的提示与草稿，避免串单。
watch(() => props.refund?.refundId, reset)

function describeError(error) {
  return error?.message || String(error || '未知错误')
}

async function review(decision) {
  if (!validRefundID(props.refund?.refundId) || busy.value) return
  // 审核金额留空 = 按申请金额（不传字段，由后端取默认）。
  // 传空串会被后端判成非法数字，所以这里必须真的不发该字段。
  // ⚠️ 后端字段名是 action（controller/ticket_refund.go:181），不是 decision。
  // 写成 decision 不会报错，只会被判成非法请求 —— 是「点了没反应」的经典形态。
  const payload = { action: decision }
  const trimmed = approveYuan.value.trim()
  if (trimmed) {
    const cents = parseRefundYuanCents(trimmed)
    if (cents === null || cents <= 0) {
      failure.value = '审核金额必须是大于 0 的数字'
      return
    }
    payload.approvedCents = cents
  }
  busy.value = true
  failure.value = ''
  try {
    const res = await reviewTicketRefund(props.refund.refundId, payload)
    if (res?.status !== 200) {
      failure.value = '审核失败：' + describeError(res?.message)
      return
    }
    notice.value = decision === 'approve' ? '已审核通过，余额已冻结。' : '已驳回。'
    props.onChanged()
  } catch (error) {
    failure.value = '审核失败：' + describeError(error)
  } finally {
    busy.value = false
  }
}

async function process() {
  if (!validRefundID(props.refund?.refundId) || busy.value) return
  busy.value = true
  failure.value = ''
  try {
    const res = await processTicketRefund(props.refund.refundId, {})
    if (res?.status !== 200) {
      failure.value = '打款标记失败：' + describeError(res?.message)
      return
    }
    notice.value = '已标记打款完成。'
    props.onChanged()
  } catch (error) {
    failure.value = '打款标记失败：' + describeError(error)
  } finally {
    busy.value = false
  }
}

async function viewProof() {
  if (!validRefundID(props.refund?.refundId) || proofBusy.value) return
  proofBusy.value = true
  failure.value = ''
  try {
    const res = await getTicketRefundProof(props.refund.refundId)
    const blob = res?.data
    if (!(blob instanceof Blob)) {
      failure.value = '凭证读取失败：响应不是文件'
      return
    }
    if (!refundProofAllowed(blob)) {
      failure.value = '凭证格式不受支持'
      return
    }
    const url = URL.createObjectURL(blob)
    window.open(url, '_blank', 'noopener')
    // 不立即 revoke：新标签页可能还在加载，交给浏览器回收。
  } catch (error) {
    failure.value = '凭证读取失败：' + describeError(error)
  } finally {
    proofBusy.value = false
  }
}

function reload() { props.onChanged() }
</script>

<style scoped>
.refund-actions { margin-top: 16px; padding: 12px; border: 1px solid var(--console-border); border-radius: 8px; }
.refund-actions-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.refund-actions-head h3 { margin: 0; font-size: 14px; }
.refund-meta { display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr)); gap: 8px; margin: 0 0 12px; }
.refund-meta div { display: flex; flex-direction: column; gap: 2px; }
.refund-meta dt { color: var(--console-muted); font-size: 12px; }
.refund-meta dd { margin: 0; }
.refund-review-form { max-width: 240px; }
.refund-failure { color: var(--danger); margin: 8px 0; }
.refund-notice { color: var(--success); margin: 8px 0; }
</style>
