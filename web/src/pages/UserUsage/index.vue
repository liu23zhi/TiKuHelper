<template>
  <div class="user-usage-page">
    <div class="page-heading">
      <div>
        <h1 class="page-title">账户使用</h1>
        <p class="page-subtitle">管理账户余额，并查看个人消费与充值流水。</p>
      </div>
    </div>

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
      <div class="section-heading">
        <h2 class="page-title">账户余额与充值</h2>
        <p class="page-subtitle">选择金额和支付方式，创建充值订单。</p>
      </div>
      <template #extra>
        <a-button @click="handleRefreshAll" :loading="pageRefreshing">刷新</a-button>
      </template>

      <a-row :gutter="16" class="recharge-summary-row">
        <a-col :xs="24" :lg="10">
          <div class="balance-panel">
            <div class="balance-label">当前账户余额</div>
            <div class="balance-value">¥ {{ formatMoneyValue(currentUserBalance) }}</div>
            <div class="balance-meta">账户状态以最新余额为准</div>
          </div>
        </a-col>
        <a-col :xs="24" :lg="14">
          <a-form layout="vertical" class="recharge-form">
            <a-form-item label="充值金额">
              <a-input-number
                v-model:value="rechargeAmount"
                :min="Math.max(0.01, minRechargeAmount)"
                :precision="2"
                :step="1"
                :parser="parseRechargeAmountInput"
                :formatter="formatRechargeAmountInput"
                placeholder="请输入充值金额，例如 10 或 25.50"
                size="large"
                style="width: 100%"
                @keydown="handleRechargeAmountKeydown"
              />
              <div class="hint">最低充值金额：¥ {{ formatMoneyValue(minRechargeAmount) }}；金额最多支持两位小数。</div>
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
                  :aria-pressed="selectedPaymentMethodId === method.id"
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
      <div class="section-heading">
        <h2 class="page-title">账单流水</h2>
        <p class="page-subtitle">查看题目消费与充值入账的金额变动、来源渠道和请求编号。</p>
      </div>

      <a-table
        class="personal-billing-table"
        bordered
        :data-source="dataSource"
        :columns="columns"
        :loading="tableLoading"
        :pagination="pagination"
        @change="handleTableChange"
        row-key="id"
        table-layout="fixed"
        :scroll="{ x: tableScrollX }"
      >
        <template #typeLabel="{ record }">
          <a-tag :color="record.direction === 'income' ? 'green' : 'blue'">{{ buildBillingRecordTypeText(record) }}</a-tag>
        </template>
        <template #subject="{ record }">
          <OverflowTableText
            :text="buildBillingRecordListSubjectText(record)"
            :expanded="!!expandedSubjects[record.id]"
            @toggle="toggleSubject(record.id)"
          />
        </template>
        <template #amount="{ record }">
          <span :class="record.direction === 'income' ? 'amount-income' : 'amount-expense'">{{ buildBillingRecordAmountText(record) }}</span>
        </template>
        <template #source="{ record }">
          <span class="multiline-cell">{{ buildBillingRecordSourceText(record) }}</span>
        </template>
        <template #searchMode="{ record }">
          <span>{{ buildBillingRecordModeText(record) }}</span>
        </template>
        <template #referenceNo="{ record }">
          <span class="reference-cell">{{ buildBillingRecordReferenceText(record) }}</span>
        </template>
        <template #accessTokenName="{ record }">
          <span>{{ record.accessTokenName || '-' }}</span>
        </template>
        <template #createdAt="{ record }">
          <span class="billing-time-cell">{{ formatDateTime(record.createdAt) }}</span>
        </template>
        <template #feeDetail="{ record }">
          <a-button type="link" style="padding: 0" @click="handleShowFeeDetail(record)">查看详情</a-button>
        </template>
        <template #emptyText>
          <a-empty description="当前还没有消费记录。" />
        </template>
      </a-table>
    </a-card>

    <a-modal
      v-model:open="paymentResultVisible"
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
import Swal from 'sweetalert2'
import OverflowTableText from '@/components/OverflowTableText.vue'
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
  buildBillingRecordFeeDetailHtml,
  buildBillingRecordListSubjectText,
  buildBillingRecordModeText,
  buildBillingRecordReferenceText,
  buildBillingRecordSourceText,
  buildBillingRecordSubjectText,
  buildBillingRecordTypeText,
  billingTableColumnWidths,
  formatBillingRecordTime,
} from '@/utils/userPortalHelpers.mjs'
import { getStoredCurrentUser, setStoredCurrentUser } from '@/utils/currentUser'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'

const dataSource = ref([])
const expandedSubjects = reactive({})
const paymentMethods = ref([])
const tableLoading = ref(false)
const paymentMethodLoading = ref(false)
const rechargeSubmitting = ref(false)
const rechargeAmount = ref(10)
const selectedPaymentMethodId = ref(null)
const paymentResultVisible = ref(false)
const paymentQRCodeDataUrl = ref('')
const paymentJumpUrl = ref('')
const minRechargeAmount = ref(1)
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

function normalizeMinRechargeAmount(value) {
  const amount = Number(value)
  return Number.isFinite(amount) && amount > 0
    ? Math.round(amount * 100) / 100
    : 1
}

export default defineComponent({
  name: 'UserUsagePage',
  components: { OverflowTableText },
  setup() {
    const subjectColumnWidth = computed(() => dataSource.value.some((record) => expandedSubjects[record.id])
      ? billingTableColumnWidths.subjectExpanded
      : billingTableColumnWidths.subject)
    const tableScrollX = computed(() => [
      'id', 'typeLabel', 'amount', 'source', 'searchMode', 'referenceNo',
      'accessTokenName', 'createdAt', 'feeDetail',
    ].reduce((total, key) => total + billingTableColumnWidths[key], subjectColumnWidth.value))
    const summary = computed(() => {
      const rows = Array.isArray(dataSource.value) ? dataSource.value : []
      return rows.reduce((acc, item) => {
        const amount = Number(item?.amount || 0)
        acc.currentPageCount += 1
        if (Number.isFinite(amount) && item?.direction !== 'income' && item?.type !== 'balance_adjustment') {
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
      minRechargeAmount,
      parseRechargeAmountInput,
      formatRechargeAmountInput,
      handleRechargeAmountKeydown,
      selectedPaymentMethodId,
      paymentResultVisible,
      paymentQRCodeDataUrl,
      paymentJumpUrl,
      rechargeQuickAmounts,
      pagination,
      tableScrollX,
      expandedSubjects,
      summary,
      currentUserBalance,
      pageRefreshing,
      formatMoneyValue,
      buildBillingRecordAmountText,
      buildBillingRecordListSubjectText,
      buildBillingRecordModeText,
      buildBillingRecordReferenceText,
      buildBillingRecordSourceText,
      buildBillingRecordSubjectText,
      buildBillingRecordTypeText,
      fallbackPaymentIcon() {
        return '<span class="payment-method-icon--placeholder">支</span>'
      },
      columns: computed(() => [
        {
          title: '账单 ID',
          dataIndex: 'id',
          key: 'id',
          width: billingTableColumnWidths.id,
        },
        {
          title: '类型',
          dataIndex: 'typeLabel',
          key: 'typeLabel',
          width: billingTableColumnWidths.typeLabel,
          slots: {
            customRender: 'typeLabel',
          },
        },
        {
          title: '项目',
          dataIndex: 'subject',
          key: 'subject',
          width: subjectColumnWidth.value,
          slots: {
            customRender: 'subject',
          },
        },
        {
          title: '金额',
          dataIndex: 'amount',
          key: 'amount',
          width: billingTableColumnWidths.amount,
          slots: {
            customRender: 'amount',
          },
        },
        {
          title: '来源/渠道',
          dataIndex: 'source',
          key: 'source',
          width: billingTableColumnWidths.source,
          slots: {
            customRender: 'source',
          },
        },
        {
          title: '模式',
          dataIndex: 'searchMode',
          key: 'searchMode',
          width: billingTableColumnWidths.searchMode,
          customRender: ({ record }) => buildBillingRecordModeText(record),
        },
        {
          title: '请求 ID',
          dataIndex: 'referenceNo',
          key: 'referenceNo',
          width: billingTableColumnWidths.referenceNo,
          slots: {
            customRender: 'referenceNo',
          },
        },
        {
          title: '访问令牌名称',
          dataIndex: 'accessTokenName',
          key: 'accessTokenName',
          width: billingTableColumnWidths.accessTokenName,
          slots: {
            customRender: 'accessTokenName',
          },
        },
        {
          title: '时间',
          dataIndex: 'createdAt',
          key: 'createdAt',
          width: billingTableColumnWidths.createdAt,
          slots: {
            customRender: 'createdAt',
          },
        },
        {
          title: '操作',
          dataIndex: 'feeDetail',
          key: 'feeDetail',
          width: billingTableColumnWidths.feeDetail,
          slots: {
            customRender: 'feeDetail',
          },
        },
      ]),
    }
  },
  mounted() {
    this.handleRefreshAll()
  },
  methods: {
    handleShowFeeDetail(record) {
      Swal.fire({
        title: '账单详情',
        html: buildBillingRecordFeeDetailHtml(record, {
          showAIModelMeta: false,
          showStatementControl: false,
        }),
        width: 560,
        confirmButtonText: '关闭',
        showCloseButton: true,
        focusConfirm: false,
        buttonsStyling: false,
        heightAuto: false,
        scrollbarPadding: false,
        customClass: {
          popup: 'billing-fee-popup',
          title: 'billing-fee-title',
          htmlContainer: 'billing-fee-html-container',
          actions: 'billing-fee-actions',
          confirmButton: 'billing-fee-confirm',
          closeButton: 'billing-fee-close',
        },
      })
    },
    toggleSubject(id) {
      expandedSubjects[id] = !expandedSubjects[id]
    },
    handleRefreshAll() {
      this.handleRefreshCurrentUser()
      this.handleLoadBillingRecords()
      this.handleLoadPaymentMethods()
    },
    handleRefreshCurrentUser() {
      getCurrentUser().then((res) => {
        if (res?.status === 200 && res?.data) {
          currentUser.value = res.data
          if (Object.prototype.hasOwnProperty.call(res.data, 'minRechargeAmount')) {
            minRechargeAmount.value = normalizeMinRechargeAmount(res.data.minRechargeAmount)
            if (Number(rechargeAmount.value) < minRechargeAmount.value) {
              rechargeAmount.value = minRechargeAmount.value
            }
          }
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
      return formatBillingRecordTime(value)
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
        if (Number(rechargeAmount.value) < minRechargeAmount.value) {
          throw new Error(`充值金额不能低于 ¥ ${formatMoneyValue(minRechargeAmount.value)}`)
        }
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
        if (res?.status === 202) {
          const orderNo = res?.data?.orderNo
          message.warning(`支付平台结果待核对${orderNo ? `（订单号 ${orderNo}）` : ''}，请查看充值记录或联系管理员，勿重复下单`)
          return
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
  width: 100%;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.summary-row {
  margin-bottom: 0;
}

.summary-card {
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: var(--console-radius, 12px);
  box-shadow: 0 2px 8px rgb(15 23 42 / 2%);
}

.page-card {
  min-width: 0;
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: var(--console-radius, 12px);
  box-shadow: 0 2px 8px rgb(15 23 42 / 2%);
}

.summary-card :deep(.ant-card-body) {
  min-height: 104px;
  display: flex;
  align-items: center;
  padding: 16px;
}

.summary-card :deep(.ant-statistic) {
  min-width: 0;
}

.summary-card :deep(.ant-statistic-content) {
  overflow-wrap: anywhere;
}

.page-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  min-width: 0;
}

.page-heading > div {
  min-width: 0;
}

.section-heading {
  margin-bottom: 20px;
  padding-bottom: 16px;
  border-bottom: 1px solid var(--console-border, #e5e7eb);
}

.section-heading .page-subtitle {
  margin-bottom: 0;
}

.recharge-card {
  margin-bottom: 0;
}

.page-title {
  margin: 0;
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
  padding: 22px;
  border: 1px solid var(--console-border, #e5e7eb);
  border-top: 3px solid var(--console-primary, #2563eb);
  border-radius: var(--console-radius, 12px);
  background: #f7f9fc;
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
  height: 38px;
  border-radius: 8px;
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
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: 8px;
  background: #fff;
  cursor: pointer;
  text-align: left;
  transition: border-color 0.2s ease, box-shadow 0.2s ease;
}

.payment-method-card--selected {
  border-color: var(--console-primary, #2563eb);
  background: #f5f8ff;
  box-shadow: 0 0 0 2px rgb(37 99 235 / 10%);
}

.payment-method-card:focus-visible {
  outline: 2px solid var(--console-primary, #2563eb);
  outline-offset: 2px;
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
  color: var(--console-primary, #2563eb);
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
  overflow-wrap: anywhere;
  line-height: 1.6;
}

.personal-billing-table :deep(.ant-table-thead > tr > th),
.personal-billing-table :deep(.ant-table-tbody > tr > td) {
  text-align: center;
  vertical-align: middle;
  white-space: normal;
  overflow-wrap: anywhere;
}

.billing-time-cell {
  display: block;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.reference-cell {
  display: block;
  overflow-wrap: anywhere;
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
  .recharge-quick-amounts {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .balance-value {
    font-size: 30px;
  }
}

@media (max-width: 768px) {
  .page-heading {
    align-items: flex-start;
  }

  .page-title {
    font-size: 20px;
  }
}

@media (max-width: 480px) {
  .page-heading {
    gap: 10px;
  }

  .recharge-summary-row {
    margin-bottom: 4px;
  }

  .payment-method-grid {
    grid-template-columns: minmax(0, 1fr);
  }

  .recharge-actions > .ant-btn {
    width: 100%;
  }
}
</style>
