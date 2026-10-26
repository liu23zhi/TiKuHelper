<template>
  <div class="console-page recharge-order-page">
    <ConsolePageHeading title="充值记录" :description="isAdmin ? '查看全部充值订单、支付方式和到账状态。' : '查看自己的充值订单和到账状态。'">
      <template #actions><a-button @click="handleLoad" :loading="tableLoading">刷新</a-button></template>
    </ConsolePageHeading>
    <ConsoleSummaryGrid>
      <div>
          <a-statistic title="充值总数（筛选结果）" :value="summary.total" />
      </div>
      <div>
          <a-statistic title="当前页条数" :value="summary.currentPageCount" />
      </div>
      <div>
          <a-statistic title="累计充值金额（当前筛选，全部页）" prefix="¥" :value="summary.totalAmount" :precision="2" />
          <div class="summary-hint">统计当前权限与筛选条件下的全部订单</div>
      </div>
      <div>
          <a-statistic title="当前页累计充值金额" prefix="¥" :value="summary.currentPageAmount" :precision="2" />
          <div class="summary-hint">仅统计第 {{ pagination.current }} 页 {{ summary.currentPageCount }} 条</div>
      </div>
    </ConsoleSummaryGrid>

    <ConsolePanel title="订单列表" description="按关键词、支付状态和渠道筛选充值记录。">
      <a-alert
        type="info"
        show-icon
        class="page-alert"
        :message="isAdmin ? '管理员可查看全部充值记录，包含用户、订单号、支付方式、金额和支付状态。' : '普通用户只能查看自己的充值记录，支付结果以异步通知入账为准。'"
      />

      <a-form layout="inline" class="filter-form">
        <a-form-item label="关键词">
          <a-input-search
            v-model:value="keywordInput"
            placeholder="订单号 / 第三方单号 / 用户 / 支付方式"
            allow-clear
            style="width: 280px"
            @search="applyKeyword"
            @press-enter="applyKeyword"
          />
        </a-form-item>
        <a-form-item label="状态">
          <a-select
            v-model:value="filters.status"
            allow-clear
            placeholder="全部状态"
            style="width: 150px"
            :options="statusOptions"
            @change="onFilterChange"
          />
        </a-form-item>
        <a-form-item label="支付渠道">
          <a-input
            v-model:value="filters.paymentProvider"
            placeholder="例如 epay"
            allow-clear
            style="width: 160px"
            @press-enter="onFilterChange"
            @blur="onFilterChange"
          />
        </a-form-item>
        <a-form-item>
          <a-space>
            <a-button @click="resetFilters">重置筛选</a-button>
            <a-tag v-if="activeFilterCount > 0" color="blue">已启用 {{ activeFilterCount }} 个筛选条件</a-tag>
          </a-space>
        </a-form-item>
      </a-form>

      <ConsoleTableShell label="充值记录"><a-table
        bordered
        :data-source="dataSource"
        :columns="columns"
        :loading="tableLoading"
        :pagination="pagination"
        row-key="id"
        table-layout="fixed"
        :scroll="{ x: sumTableColumnWidths(columns) }"
        @change="handleTableChange"
      >
        <template #bodyCell="{ column, record }">
          <span v-if="column.key === 'username'">{{ record.username || `用户 #${record.userId || '-'}` }}</span>
          <span v-else-if="column.key === 'amount'" class="money-cell">¥ {{ formatAmount(record.amount) }}</span>
          <a-tag v-else-if="column.key === 'status'" :color="buildStatusColor(record.status)">
            {{ buildStatusLabel(record.status) }}
          </a-tag>
          <span v-else-if="column.key === 'paidAt' || column.key === 'createdAt'" class="time-cell">{{ formatDateTime(record[column.key]) }}</span>
        </template>
        <template #emptyText>
          <a-empty description="当前还没有充值记录。" />
        </template>
      </a-table></ConsoleTableShell>
    </ConsolePanel>
  </div>
</template>

<script>
import { computed, defineComponent, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { getRechargeOrders } from '@/api/TikuComponentApi'
import { getStoredCurrentUser, isAdminUser } from '@/utils/currentUser'
import { buildSearchParams, countActiveFilters, normalizeSearchKeyword } from '@/utils/listSearchHelpers.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

const dataSource = ref([])
const tableLoading = ref(false)
// 服务端返回的金额统计：totalAmount 覆盖全部筛选结果，currentPageAmount 仅当前页。
const amountSummary = reactive({
  totalAmount: 0,
  currentPageAmount: 0,
})
// 已生效的筛选条件；keywordInput 只是输入框的即时值。
const filters = reactive({
  keyword: '',
  status: undefined,
  paymentProvider: '',
})
const keywordInput = ref('')
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
  name: 'RechargeOrderList',
  setup() {
    const currentUser = computed(() => getStoredCurrentUser())
    const isAdmin = computed(() => isAdminUser(currentUser.value))
    const summary = computed(() => ({
      total: Number(pagination.total || 0),
      currentPageCount: Array.isArray(dataSource.value) ? dataSource.value.length : 0,
      totalAmount: Number(amountSummary.totalAmount || 0),
      currentPageAmount: Number(amountSummary.currentPageAmount || 0),
    }))

    return {
      dataSource,
      tableLoading,
      pagination,
      summary,
      isAdmin,
      filters,
      keywordInput,
      sumTableColumnWidths,
      activeFilterCount: computed(() => countActiveFilters({
        keyword: filters.keyword,
        status: filters.status,
        paymentProvider: filters.paymentProvider,
      })),
      statusOptions: [
        { label: '待支付', value: 'pending' },
        { label: '已支付', value: 'paid' },
      ],
      columns: [
        {
          title: '订单号',
          dataIndex: 'orderNo',
          key: 'orderNo',
          width: 240,
        },
        {
          title: '用户',
          dataIndex: 'username',
          key: 'username',
          width: 170,
        },
        {
          title: '金额',
          dataIndex: 'amount',
          key: 'amount',
          width: 120,
        },
        {
          title: '支付方式',
          dataIndex: 'paymentMethodNameSnapshot',
          key: 'paymentMethodNameSnapshot',
          width: 150,
        },
        {
          title: '状态',
          dataIndex: 'status',
          key: 'status',
          width: 120,
        },
        {
          title: '第三方单号',
          dataIndex: 'thirdPartyOrderNo',
          key: 'thirdPartyOrderNo',
          width: 220,
        },
        {
          title: '支付时间',
          dataIndex: 'paidAt',
          key: 'paidAt',
          width: 122,
        },
        {
          title: '创建时间',
          dataIndex: 'createdAt',
          key: 'createdAt',
          width: 122,
        },
      ],
    }
  },
  mounted() {
    this.handleLoad()
  },
  methods: {
    // 点「搜索」或回车时才把输入框内容变成生效的筛选条件。
    applyKeyword() {
      filters.keyword = normalizeSearchKeyword(keywordInput.value)
      keywordInput.value = filters.keyword
      pagination.current = 1
      this.handleLoad()
    },
    onFilterChange() {
      pagination.current = 1
      this.handleLoad()
    },
    resetFilters() {
      filters.keyword = ''
      keywordInput.value = ''
      filters.status = undefined
      filters.paymentProvider = ''
      pagination.current = 1
      this.handleLoad()
    },
    handleLoad() {
      tableLoading.value = true
      getRechargeOrders(buildSearchParams({
        keyword: filters.keyword,
        status: filters.status,
        paymentProvider: filters.paymentProvider,
      }, pagination.current, pagination.pageSize)).then((res) => {
        if (res?.status === 200) {
          dataSource.value = Array.isArray(res?.data?.data) ? res.data.data : []
          pagination.total = Number(res?.data?.total || 0)
          pagination.current = Number(res?.data?.page || 1)
          pagination.pageSize = Number(res?.data?.pageSize || 10)
          amountSummary.totalAmount = Number(res?.data?.summary?.totalAmount || 0)
          amountSummary.currentPageAmount = Number(res?.data?.summary?.currentPageAmount || 0)
        } else {
          message.error(res?.message || '获取充值记录失败')
        }
      }).catch(() => {
        message.error('获取充值记录失败')
      }).finally(() => {
        tableLoading.value = false
      })
    },
    handleTableChange(paginationData) {
      pagination.current = paginationData.current
      pagination.pageSize = paginationData.pageSize
      this.handleLoad()
    },
    formatAmount(value) {
      // 充值金额固定 2 位小数（支付渠道只支持「分」），与余额的 6 位精度区分开。
      const amount = Number(value)
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
      return new Intl.DateTimeFormat('sv-SE', {
        timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
        hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
      }).format(date).replace(' ', '\n')
    },
    buildStatusLabel(value) {
      const normalized = String(value || '').trim().toLowerCase()
      if (normalized === 'paid') return '已支付'
      if (normalized === 'pending') return '待支付'
      if (normalized === 'closed') return '已关闭'
      return value || '-'
    },
    buildStatusColor(value) {
      const normalized = String(value || '').trim().toLowerCase()
      if (normalized === 'paid') return 'green'
      if (normalized === 'pending') return 'gold'
      if (normalized === 'closed') return 'default'
      return 'blue'
    },
  },
})
</script>

<style scoped>
.summary-hint { margin-top: 8px; color: var(--console-muted); font-size: 12px; line-height: 1.6; }
.filter-form { margin: 20px 0; }
.page-alert { margin-bottom: 16px; }
.money-cell, .time-cell { font-variant-numeric: tabular-nums; }
.time-cell { white-space: pre-line; }
@media (max-width: 640px) {
  .filter-form :deep(.ant-form-item) { width: 100%; }
  .filter-form :deep(.ant-form-item-control) { flex: 1; }
  .filter-form :deep(.ant-input-search), .filter-form :deep(.ant-input), .filter-form :deep(.ant-select) { width: 100% !important; }
}
</style>
