<template>
  <div class="console-page user-usage-page">
    <ConsolePageHeading title="账户使用" description="管理账户余额，并查看个人消费与充值流水。">
      <template #actions><a-button @click="handleRefreshAll" :loading="pageRefreshing">刷新</a-button></template>
    </ConsolePageHeading>

    <ConsoleSummaryGrid>
      <div class="summary-card"><a-statistic title="当前余额" prefix="¥" :value="currentUserBalance" :precision="6" /></div>
      <div class="summary-card"><a-statistic title="账单总数" :value="summary.total" /></div>
      <div class="summary-card"><a-statistic title="当前页条数" :value="summary.currentPageCount" /></div>
      <div class="summary-card"><a-statistic title="当前页消费金额" prefix="¥" :value="summary.totalAmount" :precision="6" /></div>
    </ConsoleSummaryGrid>

    <ConsolePanel title="账户余额与充值" description="选择金额和支付方式，创建充值订单。" class="recharge-card">
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
    </ConsolePanel>

    <ConsolePanel title="账单流水" description="查看题目消费与充值入账的金额变动、来源渠道和请求 ID。" class="page-card">

      <ConsoleTableShell label="个人账单流水">
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
          <template #bodyCell="{ column, record, text }">
            <template v-if="column.key === 'typeLabel'">
              <a-tag :color="record.direction === 'income' ? 'green' : 'blue'">{{ buildBillingRecordTypeText(record) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'subject'">
              <OverflowTableText
                :text="buildBillingRecordListSubjectText(record)"
                :expanded="isSubjectExpanded(record.id)"
                @toggle="toggleSubject(record.id)"
              />
            </template>
            <template v-else-if="column.key === 'amount'">
              <span :class="record.direction === 'income' ? 'amount-income' : 'amount-expense'">{{ buildBillingRecordAmountText(record) }}</span>
            </template>
            <template v-else-if="column.key === 'source'">
              <span class="multiline-cell">{{ buildBillingRecordSourceText(record) }}</span>
            </template>
            <template v-else-if="column.key === 'searchMode'">
              <span>{{ buildBillingRecordModeText(record) }}</span>
            </template>
            <template v-else-if="column.key === 'referenceNo'">
              <span class="reference-cell">{{ buildBillingRecordReferenceText(record) }}</span>
            </template>
            <template v-else-if="column.key === 'accessTokenName'">
              <span>{{ record.accessTokenName || '-' }}</span>
            </template>
            <template v-else-if="column.key === 'createdAt'">
              <span class="billing-time-cell">{{ formatDateTime(record.createdAt) }}</span>
            </template>
            <template v-else-if="column.key === 'feeDetail'">
              <a-button type="link" style="padding: 0" @click="handleShowFeeDetail(record)">查看详情</a-button>
            </template>
            <template v-else>{{ text }}</template>
          </template>
          <template #emptyText>
            <a-empty description="当前还没有消费记录。" />
          </template>
        </a-table>
      </ConsoleTableShell>
    </ConsolePanel>

    <BillingDetailDialog v-model:open="billingDetailOpen" :record="selectedBillingRecord" viewer-scope="self" />

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
import OverflowTableText from '@/components/OverflowTableText.vue'
import BillingDetailDialog from '@/components/BillingDetailDialog.vue'
import { useTableExpansion } from '@/composables/useTableExpansion.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'
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
const paymentMethods = ref([])
const tableLoading = ref(false)
const paymentMethodLoading = ref(false)
const currentUserLoading = ref(false)
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
  components: { OverflowTableText, BillingDetailDialog },
  setup() {
    const subjectExpansion = useTableExpansion(dataSource)
    const subjectColumnWidth = computed(() => subjectExpansion.columnWidth('subject', billingTableColumnWidths.subject, billingTableColumnWidths.subjectExpanded))
    const selectedBillingRecord = ref(null)
    const billingDetailOpen = ref(false)
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

    const pageRefreshing = computed(() => currentUserLoading.value || tableLoading.value || paymentMethodLoading.value)

    // 充值金额输入约束：输入阶段就挡住超出 2 位的金额。
    const parseRechargeAmountInput = (value) => sanitizeRechargeAmountInput(value)
    const formatRechargeAmountInput = (value) => sanitizeRechargeAmountInput(value)
    const handleRechargeAmountKeydown = (event) => {
      if (shouldBlockRechargeAmountKey(event?.target?.value, event?.key)) {
        event.preventDefault()
      }
    }

    const columns = computed(() => [
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
        },
        {
          title: '项目',
          dataIndex: 'subject',
          key: 'subject',
          width: subjectColumnWidth.value,
        },
        {
          title: '金额',
          dataIndex: 'amount',
          key: 'amount',
          width: billingTableColumnWidths.amount,
        },
        {
          title: '来源/渠道',
          dataIndex: 'source',
          key: 'source',
          width: billingTableColumnWidths.source,
        },
        {
          title: '模式',
          dataIndex: 'searchMode',
          key: 'searchMode',
          width: billingTableColumnWidths.searchMode,
        },
        {
          title: '请求 ID',
          dataIndex: 'referenceNo',
          key: 'referenceNo',
          width: billingTableColumnWidths.referenceNo,
        },
        {
          title: '访问令牌名称',
          dataIndex: 'accessTokenName',
          key: 'accessTokenName',
          width: billingTableColumnWidths.accessTokenName,
        },
        {
          title: '时间',
          dataIndex: 'createdAt',
          key: 'createdAt',
          width: billingTableColumnWidths.createdAt,
        },
        {
          title: '操作',
          dataIndex: 'feeDetail',
          key: 'feeDetail',
          width: billingTableColumnWidths.feeDetail,
        },
      ])
    const tableScrollX = computed(() => sumTableColumnWidths(columns.value))

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
      selectedBillingRecord,
      billingDetailOpen,
      isSubjectExpanded: (id) => subjectExpansion.isExpanded(id, 'subject'),
      toggleSubject: (id) => subjectExpansion.toggle(id, 'subject'),
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
      columns,

    }
  },
  mounted() {
    this.handleRefreshAll()
  },
  methods: {
    handleShowFeeDetail(record) {
      this.selectedBillingRecord = record
      this.billingDetailOpen = true
    },
    handleRefreshAll() {
      return Promise.all([
        this.handleRefreshCurrentUser(),
        this.handleLoadBillingRecords(),
        this.handleLoadPaymentMethods(),
      ])
    },
    handleRefreshCurrentUser() {
      currentUserLoading.value = true
      return getCurrentUser().then((res) => {
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
      }).finally(() => {
        currentUserLoading.value = false
      })
    },
    handleLoadBillingRecords() {
      tableLoading.value = true
      return getBillingRecords(buildPersonalUsageBillingRecordParams(pagination.current, pagination.pageSize)).then((res) => {
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
      return getPaymentMethods().then((res) => {
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
.summary-card { padding: 20px; border: 1px solid var(--console-border); border-radius: 10px; background: var(--console-surface); min-width: 0; }
.recharge-summary-row { row-gap: 20px; }
.balance-panel { display: flex; flex-direction: column; justify-content: center; height: 100%; min-height: 190px; padding: 24px; border-radius: 10px; background: var(--console-selected, #e7f3ef); border: 1px solid var(--console-border); }
.balance-label { color: var(--console-muted); }
.balance-value { margin: 12px 0; color: var(--console-primary); font-size: clamp(24px, 3vw, 36px); font-weight: 600; overflow-wrap: anywhere; font-variant-numeric: tabular-nums; }
.balance-meta, .hint { color: var(--console-muted); font-size: 12px; line-height: 1.6; }
.hint { margin-top: 8px; }
.recharge-quick-amounts { display: flex; flex-wrap: wrap; gap: 8px; }
.recharge-quick-amount { min-width: 60px; }
.recharge-actions { display: flex; justify-content: flex-end; }
.payment-method-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 12px; }
.payment-method-card { display: flex; align-items: center; gap: 12px; min-width: 0; padding: 14px; background: var(--console-surface); border: 1px solid var(--console-border); border-radius: 8px; color: var(--console-text); text-align: left; cursor: pointer; transition: border-color .15s, background .15s; }
.payment-method-card--selected { border-color: var(--console-primary); background: var(--console-selected, #e7f3ef); }
.payment-method-card:focus-visible { outline: 2px solid var(--console-primary); outline-offset: 2px; }
.payment-method-icon { display: inline-flex; align-items: center; justify-content: center; flex: 0 0 36px; width: 36px; height: 36px; }
.payment-method-icon :deep(svg), .payment-method-icon svg { width: 28px; height: 28px; }
.payment-method-icon--placeholder { color: var(--console-primary); font-size: 24px; font-weight: 600; }
.payment-method-main { display: grid; gap: 4px; min-width: 0; }
.payment-method-name { font-weight: 600; overflow-wrap: anywhere; }
.payment-method-remark { color: var(--console-muted); font-size: 12px; overflow-wrap: anywhere; }
.amount-income { color: var(--console-success, #15803d); }
.amount-expense { color: var(--console-danger, #c13e41); }
.reference-cell, .multiline-cell, .billing-time-cell { white-space: pre-wrap; overflow-wrap: anywhere; }
.billing-time-cell { font-variant-numeric: tabular-nums; }
.payment-result { display: grid; gap: 16px; text-align: center; }
.payment-qrcode-image { width: 256px; max-width: 100%; height: auto; margin: 0 auto; border: 1px solid var(--console-border); border-radius: 8px; }
.payment-qrcode-empty { display: grid; place-items: center; width: 256px; max-width: 100%; min-height: 220px; margin: 0 auto; color: var(--console-muted); background: var(--console-surface-muted, #f8faf9); border-radius: 8px; }
.payment-result-title { font-size: 16px; font-weight: 600; }
.payment-result-meta { color: var(--console-muted); line-height: 1.6; }
.payment-result-actions { display: grid; gap: 12px; }
@media (max-width: 640px) {
  .summary-card { padding: 16px; }
  .balance-panel { min-height: 150px; padding: 20px; }
  .recharge-actions :deep(.ant-btn) { width: 100%; }
  .payment-method-grid { grid-template-columns: 1fr; }
}
</style>
