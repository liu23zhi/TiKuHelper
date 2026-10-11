<template>
  <div class="user-usage-page">
    <a-row :gutter="16" class="summary-row">
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="当前余额" prefix="¥" :value="currentUserBalance" :precision="6" />
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
          <a-statistic title="累计消费金额" prefix="¥" :value="summary.totalAmount" :precision="6" />
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
        <a-button @click="handleRefreshAll" :loading="pageRefreshing">刷新</a-button>
      </template>

      <a-row :gutter="16" class="recharge-summary-row">
        <a-col :xs="24" :lg="10">
          <div class="balance-panel">
            <div class="balance-label">当前账户余额</div>
            <div class="balance-value">¥ {{ formatMoneyValue(currentUserBalance) }}</div>
            <div class="balance-meta">点击右上角刷新可同步最新余额</div>
          </div>
        </a-col>
        <a-col :xs="24" :lg="14">
          <a-form layout="vertical" class="recharge-form">
            <a-form-item label="充值金额">
              <a-input-number
                v-model:value="rechargeAmount"
                :min="0.01"
                :precision="2"
                :step="1"
                :parser="parseRechargeAmountInput"
                :formatter="formatRechargeAmountInput"
                placeholder="请输入充值金额，例如 10 或 25.50"
                size="large"
                style="width: 100%"
                @keydown="handleRechargeAmountKeydown"
              />
              <div class="hint">充值金额最多两位小数（支付渠道仅支持「分」）。</div>
            </a-form-item>

            <a-form-item label="快捷金额">
              <div class="recharge-quick-amounts">
                <a-button
                  v-for="amount in rechargeQuickAmounts"
                  :key="amount"
                  class="recharge-quick-amount"
                  @click="applyRechargeQuickAmount(amount)"
                >
                  {{ amount }}
                </a-button>
              </div>
            </a-form-item>

            <a-form-item label="支付方式">
              <div v-if="paymentMethods.length" class="payment-method-grid">
                <button
                  v-for="method in paymentMethods"
                  :key="method.id"
                  type="button"
                  class="payment-method-card"
                  :class="{ 'payment-method-card--selected': selectedPaymentMethodId === method.id }"
                  @click="selectPaymentMethod(method.id)"
                >
                  <span class="payment-method-icon" v-html="method.iconSvg || fallbackPaymentIcon(method.name)"></span>
                  <span class="payment-method-main">
                    <span class="payment-method-name">{{ method.name }}</span>
                    <span class="payment-method-remark">{{ method.remark || method.epayType || '可用支付方式' }}</span>
                  </span>
                </button>
              </div>
              <a-empty v-else description="暂无可用支付方式，请联系管理员。" />
            </a-form-item>

            <div class="recharge-actions">
              <a-button type="primary" @click="handleCreateRechargeOrder" :loading="rechargeSubmitting">
                创建充值订单
              </a-button>
            </div>
          </a-form>
        </a-col>
      </a-row>

      <a-alert
        type="info"
        show-icon
        class="page-alert"
        message="这里只展示当前账号自己的账单记录。充值创建订单后会跳转至收银台，支付结果以异步通知为准；如余额已变化，可点击刷新同步最新余额。"
        style="display: none"
      />
    </a-card>

    <a-card :bordered="false" class="page-card">
      <template #title>
        <div class="page-title-wrap">
          <div>
            <div class="page-title">账单流水</div>
            <div class="page-subtitle">展示题目消费与充值入账，便于快速回看最近的金额变动、来源渠道与关联单号。</div>
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
        :scroll="{ x: 1180 }"
      >
        <template #typeLabel="{ record }">
          <a-tag :color="record.direction === 'income' ? 'green' : 'blue'">{{ buildBillingRecordTypeText(record) }}</a-tag>
        </template>
        <template #subject="{ record }">
          <span class="multiline-cell">{{ buildBillingRecordSubjectText(record) }}</span>
        </template>
        <template #amount="{ record }">
          <span :class="record.direction === 'income' ? 'amount-income' : 'amount-expense'">{{ buildBillingRecordAmountText(record) }}</span>
        </template>
        <template #source="{ record }">
          <span class="multiline-cell">{{ buildBillingRecordSourceText(record) }}</span>
        </template>
        <template #referenceNo="{ record }">
          <span class="reference-cell">{{ buildBillingRecordReferenceText(record) }}</span>
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

    <a-modal
      v-model:visible="paymentResultVisible"
      title="扫码支付"
      :footer="null"
      width="420px"
      centered
      @cancel="closePaymentResult"
    >
      <div class="payment-result">
        <img
          v-if="paymentQRCodeDataUrl"
          :src="paymentQRCodeDataUrl"
          alt="支付二维码"
          class="payment-qrcode-image"
        />
        <div v-else class="payment-qrcode-empty">正在生成支付二维码</div>
        <div class="payment-result-title">请使用对应支付 App 扫码完成充值</div>
        <div class="payment-result-meta">
          支付完成后余额会通过异步通知更新，可稍后刷新查看。
        </div>
        <div class="payment-result-actions">
          <a-button v-if="paymentJumpUrl" type="primary" block @click="openPaymentJumpFallback">
            打开支付链接
          </a-button>
          <a-button block @click="handleRefreshAll">刷新余额与记录</a-button>
        </div>
      </div>
    </a-modal>
  </div>
</template>

<script>
import { computed, defineComponent, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import QRCode from 'qrcode'
import {
  createRechargeOrder,
  getBillingRecords,
  getCurrentUser,
  getPaymentMethods,
} from '@/api/TikuComponentApi'
import {
  buildRechargeOrderPayload,
  sanitizeRechargeAmountInput,
  shouldBlockRechargeAmountKey,
  sortPaymentMethodsForDisplay,
} from '@/utils/paymentMethodHelpers.mjs'
import {
  buildPersonalUsageBillingRecordParams,
  buildBillingRecordAmountText,
  buildBillingRecordReferenceText,
  buildBillingRecordSourceText,
  buildBillingRecordSubjectText,
  buildBillingRecordTypeText,
} from '@/utils/userPortalHelpers.mjs'
import { getStoredCurrentUser, setStoredCurrentUser } from '@/utils/currentUser'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'

const dataSource = ref([])
const paymentMethods = ref([])
const tableLoading = ref(false)
const paymentMethodLoading = ref(false)
const rechargeSubmitting = ref(false)
const rechargeAmount = ref(10)
const selectedPaymentMethodId = ref(null)
const paymentResultVisible = ref(false)
const paymentQRCodeDataUrl = ref('')
const paymentJumpUrl = ref('')
const rechargeQuickAmounts = [10, 20, 50, 100, 200, 500]
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
        if (Number.isFinite(amount) && item?.direction !== 'income') {
          acc.totalAmount += amount
        }
        return acc
      }, {
        total: Number(pagination.total || 0),
        currentPageCount: 0,
        totalAmount: 0,
      })
    })

    const currentUserBalance = computed(() => {
      const balance = Number(currentUser.value?.balance || 0)
      return Number.isFinite(balance) ? balance : 0
    })

    const pageRefreshing = computed(() => tableLoading.value || paymentMethodLoading.value)

    // 充值金额输入约束：输入阶段就挡住超出 2 位的金额。
    const parseRechargeAmountInput = (value) => sanitizeRechargeAmountInput(value)
    const formatRechargeAmountInput = (value) => sanitizeRechargeAmountInput(value)
    const handleRechargeAmountKeydown = (event) => {
      if (shouldBlockRechargeAmountKey(event?.target?.value, event?.key)) {
        event.preventDefault()
      }
    }

    return {
      currentUser,
      dataSource,
      paymentMethods,
      tableLoading,
      paymentMethodLoading,
      rechargeSubmitting,
      rechargeAmount,
      parseRechargeAmountInput,
      formatRechargeAmountInput,
      handleRechargeAmountKeydown,
      selectedPaymentMethodId,
      paymentResultVisible,
      paymentQRCodeDataUrl,
      paymentJumpUrl,
      rechargeQuickAmounts,
      pagination,
      summary,
      currentUserBalance,
      pageRefreshing,
      formatMoneyValue,
      buildBillingRecordAmountText,
      buildBillingRecordReferenceText,
      buildBillingRecordSourceText,
      buildBillingRecordSubjectText,
      buildBillingRecordTypeText,
      fallbackPaymentIcon() {
        return '<span class="payment-method-icon--placeholder">支</span>'
      },
      columns: [
        {
          title: '账单 ID',
          dataIndex: 'id',
          key: 'id',
          width: 90,
        },
        {
          title: '类型',
          dataIndex: 'typeLabel',
          key: 'typeLabel',
          width: 120,
          slots: {
            customRender: 'typeLabel',
          },
        },
        {
          title: '项目',
          dataIndex: 'subject',
          key: 'subject',
          width: 260,
          slots: {
            customRender: 'subject',
          },
        },
        {
          title: '金额',
          dataIndex: 'amount',
          key: 'amount',
          width: 130,
          slots: {
            customRender: 'amount',
          },
        },
        {
          title: '来源/渠道',
          dataIndex: 'source',
          key: 'source',
          width: 160,
          slots: {
            customRender: 'source',
          },
        },
        {
          title: '关联单号',
          dataIndex: 'referenceNo',
          key: 'referenceNo',
          width: 220,
          slots: {
            customRender: 'referenceNo',
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
          paymentMethods.value = sortPaymentMethodsForDisplay(rows).filter((item) => !!item.enabled)
          if (!selectedPaymentMethodId.value && paymentMethods.value.length) {
            selectedPaymentMethodId.value = paymentMethods.value[0].id
          }
          if (selectedPaymentMethodId.value && !paymentMethods.value.some((item) => item.id === selectedPaymentMethodId.value)) {
            selectedPaymentMethodId.value = paymentMethods.value[0]?.id || null
          }
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
    applyRechargeQuickAmount(amount) {
      // 快捷金额是数字，保持与 a-input-number 的取值类型一致。
      rechargeAmount.value = Number(amount)
    },
    selectPaymentMethod(id) {
      selectedPaymentMethodId.value = Number(id || 0)
    },
    closePaymentResult() {
      paymentResultVisible.value = false
    },
    openPaymentJumpFallback() {
      if (!paymentJumpUrl.value) {
        return
      }
      window.open(paymentJumpUrl.value, '_blank', 'noopener,noreferrer')
    },
    async showPaymentResult(data) {
      paymentQRCodeDataUrl.value = ''
      paymentJumpUrl.value = data?.payUrl || ''
      if (data?.payType === 'qrcode' && data?.qrcodeContent) {
        paymentQRCodeDataUrl.value = await QRCode.toDataURL(data.qrcodeContent, {
          margin: 1,
          width: 256,
          errorCorrectionLevel: 'M',
        })
        paymentResultVisible.value = true
        return
      }
      if (data?.payUrl || data?.payInfo) {
        paymentJumpUrl.value = data.payUrl || data.payInfo
        paymentResultVisible.value = true
        return
      }
      throw new Error('支付平台未返回可用支付参数')
    },
    async handleCreateRechargeOrder() {
      if (!selectedPaymentMethodId.value) {
        message.warning('请选择支付方式')
        return
      }
      let payload
      try {
        payload = buildRechargeOrderPayload(rechargeAmount.value, selectedPaymentMethodId.value)
      } catch (error) {
        message.warning(error?.message || '充值参数有误')
        return
      }

      rechargeSubmitting.value = true
      createRechargeOrder(payload).then((res) => {
        if (res?.status === 200) {
          return this.showPaymentResult(res.data).then(() => {
            message.success('充值订单创建成功')
            this.handleRefreshCurrentUser()
          })
        }
        message.error(res?.message || '创建充值订单失败')
      }).catch((error) => {
        message.error(error?.response?.data?.message || error?.message || '创建充值订单失败')
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

.balance-panel {
  min-height: 100%;
  padding: 24px;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  background: #f8fafc;
}

.balance-label {
  color: #64748b;
  font-size: 13px;
}

.balance-value {
  margin-top: 10px;
  color: #0f172a;
  font-size: 36px;
  font-weight: 700;
  line-height: 1.2;
}

.balance-meta {
  margin-top: 8px;
  color: #64748b;
  font-size: 12px;
}

.recharge-form {
  max-width: 100%;
}

.recharge-quick-amounts {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 12px;
}

.recharge-quick-amount {
  height: 42px;
  border-radius: 12px;
}

.recharge-actions {
  display: flex;
  justify-content: flex-start;
}

.payment-method-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
}

.payment-method-card {
  min-height: 74px;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px;
  border: 1px solid #d9e2f2;
  border-radius: 8px;
  background: #fff;
  cursor: pointer;
  text-align: left;
  transition: border-color 0.2s ease, box-shadow 0.2s ease;
}

.payment-method-card--selected {
  border-color: #1677ff;
  box-shadow: 0 0 0 2px rgba(22, 119, 255, 0.12);
}

.payment-method-icon {
  width: 42px;
  height: 42px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  background: #f4f7fb;
  overflow: hidden;
  flex: 0 0 auto;
}

.payment-method-icon :deep(svg),
.payment-method-icon svg {
  width: 26px;
  height: 26px;
}

.payment-method-icon--placeholder {
  color: #195ca3;
  font-weight: 700;
}

.payment-method-main {
  min-width: 0;
  display: grid;
  gap: 4px;
}

.payment-method-name {
  color: #1f2937;
  font-size: 14px;
  font-weight: 600;
}

.payment-method-remark {
  color: #6b7280;
  font-size: 12px;
  line-height: 1.5;
  word-break: break-word;
}

.multiline-cell {
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.6;
}

.reference-cell {
  display: inline-block;
  max-width: 220px;
  word-break: break-all;
}

.amount-income {
  color: #16a34a;
  font-weight: 600;
}

.amount-expense {
  color: #dc2626;
  font-weight: 600;
}

.payment-result {
  display: grid;
  gap: 14px;
  justify-items: center;
  text-align: center;
}

.payment-qrcode-image {
  width: min(256px, 74vw);
  height: min(256px, 74vw);
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  padding: 10px;
  background: #fff;
}

.payment-qrcode-empty {
  width: min(256px, 74vw);
  min-height: min(256px, 74vw);
  display: grid;
  place-items: center;
  border: 1px dashed #cbd5e1;
  border-radius: 8px;
  color: #64748b;
  background: #f8fafc;
}

.payment-result-title {
  color: #0f172a;
  font-size: 16px;
  font-weight: 600;
}

.payment-result-meta {
  color: #64748b;
  font-size: 13px;
  line-height: 1.7;
}

.payment-result-actions {
  width: min(260px, 100%);
  display: grid;
  gap: 10px;
}

@media (max-width: 640px) {
  .summary-card,
  .page-card {
    border-radius: 8px;
  }

  .recharge-quick-amounts {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .balance-value {
    font-size: 30px;
  }
}
</style>
