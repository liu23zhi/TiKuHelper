<template>
  <div class="recharge-result-shell">
    <div class="page-heading">
      <h1>充值结果</h1>
      <p>交易回跳状态与订单信息。</p>
    </div>

    <a-card :bordered="false" class="recharge-result-card">
      <a-result
        :status="resultStatus"
        :title="title"
        :sub-title="subtitle"
      >
        <template #extra>
          <a-space>
            <a-button type="primary" @click="goHome">返回首页</a-button>
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
    </a-card>
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
      refreshPage,
    }
  },
})
</script>

<style scoped>
.recharge-result-shell {
  width: min(900px, 100%);
  min-width: 0;
  margin: 0 auto;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.recharge-result-card {
  width: 100%;
  min-width: 0;
  border: 1px solid var(--console-border, var(--border));
  border-radius: var(--console-radius, 12px);
  box-shadow: 0 2px 8px rgb(15 23 42 / 2%);
}

.notice {
  margin-top: 0;
  margin-bottom: 18px;
  border-radius: 8px;
}

.page-heading {
  min-width: 0;
}

.page-heading h1 {
  margin: 0;
  color: var(--console-text, var(--text));
  font-size: 22px;
  font-weight: 700;
}

.page-heading p {
  margin: 6px 0 0;
  color: var(--console-muted, var(--text-muted));
  font-size: 13px;
  line-height: 1.6;
}

.details {
  display: grid;
  gap: 12px;
  padding: 18px 20px 4px;
  border-top: 1px solid var(--console-border, var(--border));
}

.detail-row {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  font-size: 14px;
  line-height: 1.6;
}

.detail-label {
  color: var(--console-muted, var(--text-muted));
  flex: 0 0 auto;
}

.detail-value {
  color: var(--console-text, var(--text));
  text-align: right;
  word-break: break-all;
}

@media (max-width: 768px) {
  .page-heading h1 {
    font-size: 20px;
  }

  .recharge-result-card :deep(.ant-result) {
    padding: 24px 12px;
  }

  .recharge-result-card :deep(.ant-result-extra .ant-space) {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
  }
}
</style>
