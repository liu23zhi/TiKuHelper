<template>
  <div class="user-usage-page">
    <a-row :gutter="16" class="summary-row">
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="当前余额" prefix="¥" :value="currentUserBalance" :precision="2" />
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="账单总数" :value="summary.total" />
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="当前页条数" :value="summary.currentPageCount" />
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="累计消费金额" prefix="¥" :value="summary.totalAmount" :precision="2" />
        </a-card>
      </a-col>
    </a-row>

    <a-card :bordered="false" class="page-card recharge-card">
      <template #title>
        <div class="page-title-wrap">
          <div>
            <div class="page-title">账户余额与充值</div>
            <div class="page-subtitle">当前余额与用户管理保持一致，可直接发起充值补充额度。</div>
          </div>
        </div>
      </template>
      <template #extra>
        <a-space>
          <a-button @click="handleRefreshAll" :loading="pageRefreshing">刷新</a-button>
          <a-button type="primary" @click="handleOpenRechargeDialog" :loading="rechargeSubmitting">充值</a-button>
        </a-space>
      </template>

      <a-row :gutter="16" class="recharge-summary-row">
        <a-col :xs="24" :sm="12">
          <a-statistic title="当前账户余额" prefix="¥" :value="currentUserBalance" :precision="2" />
        </a-col>
        <a-col :xs="24" :sm="12">
          <a-statistic title="可用支付方式" :value="paymentMethodSummary.availableCount" />
        </a-col>
      </a-row>

      <a-alert
        type="info"
        show-icon
        class="page-alert"
        message="这里只展示当前账号自己的账单记录。充值创建订单后会跳转至收银台，支付结果以异步通知为准；如余额已变化，可点击刷新同步最新余额。"
      />

      <a-form layout="vertical" class="recharge-form">
        <a-form-item label="充值金额">
          <a-input
            v-model:value="rechargeAmount"
            placeholder="请输入充值金额，例如 10 或 25.50"
            size="large"
          />
        </a-form-item>
      </a-form>
    </a-card>

    <a-card :bordered="false" class="page-card">
      <template #title>
        <div class="page-title-wrap">
          <div>
            <div class="page-title">消费账单</div>
            <div class="page-subtitle">聚焦题目、消费金额、答案来源、访问令牌与请求时间，便于快速回看最近使用情况。</div>
          </div>
        </div>
      </template>

      <a-table
        bordered
        :data-source="dataSource"
        :columns="columns"
        :loading="tableLoading"
        :pagination="pagination"
        @change="handleTableChange"
        row-key="id"
        :scroll="{ x: 1030 }"
      >
        <template #questionText="{ record }">
          <span class="multiline-cell">{{ buildPersonalUsageQuestionText(record) }}</span>
        </template>
        <template #answerSourceLabel="{ record }">
          <span class="multiline-cell">{{ buildPersonalUsageAnswerSourceText(record) }}</span>
        </template>
        <template #amount="{ record }">
          <span>¥ {{ formatAmount(record.amount) }}</span>
        </template>
        <template #accessTokenName="{ record }">
          <span>{{ record.accessTokenName || '-' }}</span>
        </template>
        <template #createdAt="{ record }">
          <span>{{ formatDateTime(record.createdAt) }}</span>
        </template>
        <template #emptyText>
          <a-empty description="当前还没有消费记录。" />
        </template>
      </a-table>
    </a-card>
  </div>
</template>

<script>
import { computed, defineComponent, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import Swal from 'sweetalert2'
import {
  createRechargeOrder,
  getBillingRecords,
  getCurrentUser,
  getPaymentMethods,
} from '@/api/TikuComponentApi'
import {
  buildRechargeOrderPayload,
  buildRechargePaymentMethodOptions,
  buildPaymentMethodSwalHtml,
  sortPaymentMethodsForDisplay,
} from '@/utils/paymentMethodHelpers.mjs'
import {
  buildPersonalUsageAnswerSourceText,
  buildPersonalUsageBillingRecordParams,
  buildPersonalUsageQuestionText,
} from '@/utils/userPortalHelpers.mjs'
import { getStoredCurrentUser, setStoredCurrentUser } from '@/utils/currentUser'

const dataSource = ref([])
const paymentMethods = ref([])
const tableLoading = ref(false)
const paymentMethodLoading = ref(false)
const rechargeSubmitting = ref(false)
const rechargeAmount = ref('10')
const currentUser = ref(getStoredCurrentUser())
const pagination = reactive({
  current: 1,
  pageSize: 10,
  total: 0,
  showSizeChanger: true,
  showQuickJumper: true,
  showTotal: (total) => `共 ${total} 条`,
  pageSizeOptions: ['10', '20', '50', '100'],
})

export default defineComponent({
  name: 'UserUsagePage',
  setup() {
    const summary = computed(() => {
      const rows = Array.isArray(dataSource.value) ? dataSource.value : []
      return rows.reduce((acc, item) => {
        const amount = Number(item?.amount || 0)
        acc.currentPageCount += 1
        if (Number.isFinite(amount)) {
          acc.totalAmount += amount
        }
        return acc
      }, {
        total: Number(pagination.total || 0),
        currentPageCount: 0,
        totalAmount: 0,
      })
    })

    const paymentMethodSummary = computed(() => ({
      availableCount: Array.isArray(paymentMethods.value) ? paymentMethods.value.filter((item) => item?.enabled).length : 0,
    }))

    const currentUserBalance = computed(() => {
      const balance = Number(currentUser.value?.balance || 0)
      return Number.isFinite(balance) ? balance : 0
    })

    const pageRefreshing = computed(() => tableLoading.value || paymentMethodLoading.value)

    return {
      currentUser,
      dataSource,
      paymentMethods,
      tableLoading,
      paymentMethodLoading,
      rechargeSubmitting,
      rechargeAmount,
      pagination,
      summary,
      paymentMethodSummary,
      currentUserBalance,
      pageRefreshing,
      buildPersonalUsageAnswerSourceText,
      buildPersonalUsageQuestionText,
      columns: [
        {
          title: '账单 ID',
          dataIndex: 'id',
          key: 'id',
          width: 90,
        },
        {
          title: '题目',
          dataIndex: 'questionText',
          key: 'questionText',
          width: 280,
          slots: {
            customRender: 'questionText',
          },
        },
        {
          title: '消费金额',
          dataIndex: 'amount',
          key: 'amount',
          width: 120,
          slots: {
            customRender: 'amount',
          },
        },
        {
          title: '答案来源',
          dataIndex: 'answerSourceLabel',
          key: 'answerSourceLabel',
          width: 180,
          slots: {
            customRender: 'answerSourceLabel',
          },
        },
        {
          title: '访问令牌名称',
          dataIndex: 'accessTokenName',
          key: 'accessTokenName',
          width: 180,
          slots: {
            customRender: 'accessTokenName',
          },
        },
        {
          title: '请求时间',
          dataIndex: 'createdAt',
          key: 'createdAt',
          width: 180,
          slots: {
            customRender: 'createdAt',
          },
        },
      ],
    }
  },
  mounted() {
    this.handleRefreshAll()
  },
  methods: {
    handleRefreshAll() {
      this.handleRefreshCurrentUser()
      this.handleLoadBillingRecords()
      this.handleLoadPaymentMethods()
    },
    handleRefreshCurrentUser() {
      getCurrentUser().then((res) => {
        if (res?.status === 200 && res?.data) {
          currentUser.value = res.data
          setStoredCurrentUser(res.data)
        }
      }).catch(() => {
      })
    },
    handleLoadBillingRecords() {
      tableLoading.value = true
      getBillingRecords(buildPersonalUsageBillingRecordParams(pagination.current, pagination.pageSize)).then((res) => {
        if (res?.status === 200) {
          dataSource.value = Array.isArray(res?.data?.data) ? res.data.data : []
          pagination.total = Number(res?.data?.total || 0)
          pagination.current = Number(res?.data?.page || 1)
          pagination.pageSize = Number(res?.data?.pageSize || 10)
        } else {
          message.error(res?.message || '获取账单列表失败')
        }
      }).catch(() => {
        message.error('获取账单列表失败')
      }).finally(() => {
        tableLoading.value = false
      })
    },
    handleLoadPaymentMethods() {
      paymentMethodLoading.value = true
      getPaymentMethods().then((res) => {
        if (res?.status === 200) {
          const rows = Array.isArray(res?.data) ? res.data : []
          paymentMethods.value = sortPaymentMethodsForDisplay(rows)
        } else {
          message.error(res?.message || '获取支付方式失败')
        }
      }).catch(() => {
        message.error('获取支付方式失败')
      }).finally(() => {
        paymentMethodLoading.value = false
      })
    },
    handleTableChange(paginationData) {
      pagination.current = paginationData.current
      pagination.pageSize = paginationData.pageSize
      this.handleLoadBillingRecords()
    },
    formatAmount(value) {
      const amount = Number(value || 0)
      return Number.isFinite(amount) ? amount.toFixed(2) : '0.00'
    },
    formatDateTime(value) {
      if (!value) {
        return '-'
      }
      const date = new Date(value)
      if (Number.isNaN(date.getTime())) {
        return value
      }
      return date.toLocaleString()
    },
    async handleOpenRechargeDialog() {
      const options = buildRechargePaymentMethodOptions(paymentMethods.value)
      if (!options.length) {
        await Swal.fire({
          icon: 'warning',
          title: '暂无可用支付方式',
          text: '请联系管理员至少启用一个支付方式后再进行充值。',
          confirmButtonText: '我知道了',
          heightAuto: false,
          scrollbarPadding: false,
        })
        return
      }

      const selectedPaymentMethodId = await new Promise((resolve) => {
        let settled = false
        Swal.fire({
          title: '请选择支付方式',
          html: `<div class="payment-option-list">${buildPaymentMethodSwalHtml(options)}</div>`,
          showConfirmButton: false,
          showCancelButton: true,
          cancelButtonText: '取消',
          focusConfirm: false,
          heightAuto: false,
          scrollbarPadding: false,
          didOpen: () => {
            const container = Swal.getHtmlContainer()
            if (!container) {
              return
            }
            container.querySelectorAll('[data-payment-method-id]').forEach((button) => {
              button.addEventListener('click', () => {
                if (settled) {
                  return
                }
                settled = true
                resolve(Number(button.getAttribute('data-payment-method-id') || 0))
                Swal.close()
              })
            })
          },
          willClose: () => {
            if (!settled) {
              settled = true
              resolve(null)
            }
          },
        })
      })

      if (!selectedPaymentMethodId) {
        return
      }

      let payload
      try {
        payload = buildRechargeOrderPayload(rechargeAmount.value, selectedPaymentMethodId)
      } catch (error) {
        message.warning(error?.message || '充值参数有误')
        return
      }

      rechargeSubmitting.value = true
      createRechargeOrder(payload).then((res) => {
        if (res?.status === 200 && res?.data?.payUrl) {
          message.success('充值订单创建成功，正在跳转支付页面')
          window.open(res.data.payUrl, '_blank', 'noopener,noreferrer')
          this.handleRefreshCurrentUser()
          return
        }
        message.error(res?.message || '创建充值订单失败')
      }).catch((error) => {
        message.error(error?.response?.data?.message || '创建充值订单失败')
      }).finally(() => {
        rechargeSubmitting.value = false
      })
    },
  },
})
</script>

<style scoped>
.user-usage-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.summary-row {
  margin-bottom: 0;
}

.summary-card {
  border-radius: 18px;
}

.page-card {
  border-radius: 20px;
}

.recharge-card {
  margin-bottom: 0;
}

.page-title-wrap {
  display: flex;
  align-items: center;
}

.page-title {
  font-size: 22px;
  font-weight: 700;
  color: #1f2937;
}

.page-subtitle {
  margin-top: 6px;
  color: #6b7280;
  font-size: 13px;
}

.page-alert {
  margin-bottom: 16px;
}

.recharge-summary-row {
  margin-bottom: 16px;
}

.recharge-form {
  max-width: 420px;
}

.multiline-cell {
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.6;
}
</style>

<style>
.payment-option-list {
  display: grid;
  gap: 12px;
  margin-top: 8px;
}

.payment-option {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 14px 16px;
  border: 1px solid #d9e2f2;
  border-radius: 16px;
  background: #fff;
  cursor: pointer;
  transition: all 0.2s ease;
}

.payment-option:hover {
  border-color: #2f80ed;
  box-shadow: 0 10px 24px rgba(47, 128, 237, 0.14);
}

.payment-option-main {
  display: flex;
  align-items: center;
  gap: 12px;
  text-align: left;
}

.payment-option-icon {
  width: 42px;
  height: 42px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 12px;
  background: #f4f7fb;
  overflow: hidden;
  flex: 0 0 auto;
}

.payment-option-icon svg {
  width: 26px;
  height: 26px;
}

.payment-option-icon--placeholder {
  color: #195ca3;
  font-weight: 700;
}

.payment-option-title {
  font-size: 15px;
  font-weight: 600;
  color: #1f2937;
}

.payment-option-remark {
  margin-top: 4px;
  font-size: 12px;
  line-height: 1.6;
  color: #6b7280;
}

.payment-option-arrow {
  color: #195ca3;
  font-size: 13px;
  font-weight: 600;
}
</style>
