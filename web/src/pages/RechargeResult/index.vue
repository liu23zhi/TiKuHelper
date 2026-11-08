<template>
  <div class="console-page recharge-result-shell">
    <ConsolePageHeading title="充值结果" description="查看支付回跳状态与订单信息。" />

    <ConsolePanel class="recharge-result-card">
      <a-result
        :status="resultStatus"
        :title="title"
        :sub-title="subtitle"
      >
        <template #extra>
          <a-space wrap>
            <a-button type="primary" @click="goHome">返回首页</a-button>
            <a-button @click="goRecords">查看充值记录</a-button>
            <a-button @click="refreshPage">刷新页面</a-button>
          </a-space>
        </template>
      </a-result>

      <a-alert
        class="notice"
        type="info"
        show-icon
        message="支付结果以异步通知为准；当前页面只负责展示回跳信息，不会触发结算。"
      />

      <div v-if="hasQuery" class="details">
        <div class="detail-row">
          <span class="detail-label">订单号</span>
          <span class="detail-value">{{ orderNo || '—' }}</span>
        </div>
        <div class="detail-row">
          <span class="detail-label">交易状态</span>
          <span class="detail-value">{{ tradeStatus || '—' }}</span>
        </div>
        <div class="detail-row">
          <span class="detail-label">金额</span>
          <span class="detail-value">{{ money || '—' }}</span>
        </div>
      </div>
    </ConsolePanel>
  </div>
</template>

<script>
import { computed, defineComponent } from 'vue'
import { Result } from 'ant-design-vue'
import { useRoute, useRouter } from 'vue-router'
import { getStoredCurrentUser, resolveHomePath } from '../../utils/currentUser'

function normalizeValue(value) {
  return String(value || '').trim()
}

export default defineComponent({
  name: 'RechargeResult',
  components: { AResult: Result },
  setup() {
    const route = useRoute()
    const router = useRouter()

    const orderNo = computed(() => normalizeValue(route.query.out_trade_no || route.query.orderNo))
    const tradeStatus = computed(() => normalizeValue(route.query.trade_status || route.query.tradeStatus).toUpperCase())
    const money = computed(() => normalizeValue(route.query.money))

    const resultStatus = computed(() => {
      if (tradeStatus.value === 'TRADE_SUCCESS') {
        return 'success'
      }
      if (!tradeStatus.value) {
        return 'info'
      }
      if (tradeStatus.value === 'WAIT_BUYER_PAY') {
        return 'warning'
      }
      return 'error'
    })

    const title = computed(() => {
      if (tradeStatus.value === 'TRADE_SUCCESS') {
        return '支付已完成'
      }
      if (tradeStatus.value === 'WAIT_BUYER_PAY') {
        return '支付处理中'
      }
      if (tradeStatus.value) {
        return `支付状态：${tradeStatus.value}`
      }
      return '已返回支付结果页'
    })

    const subtitle = computed(() => {
      if (tradeStatus.value === 'TRADE_SUCCESS') {
        return '页面上的成功状态仅表示回跳成功，最终入账仍以异步通知为准。'
      }
      if (tradeStatus.value === 'WAIT_BUYER_PAY') {
        return '当前交易还未完成，请稍后刷新订单列表查看最终结果。'
      }
      return '如果回跳信息不完整或状态未更新，请稍后刷新页面或查看订单列表。'
    })

    const hasQuery = computed(() => !!(orderNo.value || tradeStatus.value || money.value))

    const goHome = () => {
      const currentUser = getStoredCurrentUser()
      router.push(resolveHomePath(currentUser))
    }

    const goRecords = () => router.push({ name: 'recharge-orders' })

    const refreshPage = () => {
      window.location.reload()
    }

    return {
      orderNo,
      tradeStatus,
      money,
      resultStatus,
      title,
      subtitle,
      hasQuery,
      goHome,
      goRecords,
      refreshPage,
    }
  },
})
</script>

<style scoped>
.recharge-result-shell { max-width: 900px; margin: 0 auto; }
.notice { margin-bottom: 20px; }
.details { display: grid; gap: 16px; padding-top: 20px; border-top: 1px solid var(--console-border); }
.detail-row { display: grid; grid-template-columns: 90px minmax(0, 1fr); gap: 16px; line-height: 1.7; }
.detail-label { color: var(--console-muted); }
.detail-value { color: var(--console-text); text-align: right; overflow-wrap: anywhere; font-variant-numeric: tabular-nums; }
@media (max-width: 768px) { .recharge-result-card :deep(.ant-result) { padding: 24px 0; } .recharge-result-card :deep(.ant-result-extra .ant-space) { justify-content: center; } }
</style>
