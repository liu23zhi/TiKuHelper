<template>
  <div class="console-page billing-page">
    <ConsolePageHeading :title="pageTitle" :description="pageSubtitle">
      <template #actions><a-button @click="handleLoad" :loading="tableLoading">刷新</a-button></template>
    </ConsolePageHeading>
    <ConsoleSummaryGrid>
      <div class="summary-card"><a-statistic title="账单总数（筛选结果）" :value="summary.total" /></div>
      <div class="summary-card"><a-statistic title="当前页条数" :value="summary.currentPageCount" /></div>
      <div class="summary-card">
        <a-statistic :title="hasActiveFilters ? '累计消费金额（当前筛选，全部页）' : '累计消费金额（全部页）'" prefix="¥" :value="summary.totalAmount" :precision="6" />
        <p class="summary-hint">不包含充值、赠送及管理员余额调整</p>
      </div>
      <div class="summary-card">
        <a-statistic title="当前页累计消费金额" prefix="¥" :value="summary.currentPageAmount" :precision="6" />
        <p class="summary-hint">仅统计第 {{ pagination.current }} 页 {{ summary.currentPageCount }} 条</p>
      </div>
    </ConsoleSummaryGrid>

    <ConsolePanel class="page-card">
      <a-alert
        type="info"
        show-icon
        class="page-alert"
        :message="alertMessage"
      />

      <a-form layout="inline" class="filter-form">
        <a-form-item label="关键词">
          <a-input-search
            v-model:value="keywordInput"
            placeholder="题目 / 请求 ID / 用户 / 渠道"
            allow-clear
            style="width: 260px"
            @search="applyKeyword"
            @press-enter="applyKeyword"
          />
        </a-form-item>
        <a-form-item label="类型">
          <a-select
            v-model:value="filters.type"
            allow-clear
            placeholder="全部类型"
            style="width: 160px"
            :options="typeOptions"
            @change="onFilterChange"
          />
        </a-form-item>
        <a-form-item>
          <a-space>
            <a-button @click="resetFilters">重置筛选</a-button>
            <a-tag v-if="activeFilterCount > 0" color="blue">已启用 {{ activeFilterCount }} 个筛选条件</a-tag>
          </a-space>
        </a-form-item>
      </a-form>

      <ConsoleTableShell label="账单记录">
        <a-table
          class="desktop-billing-table"
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
            <template v-if="column.key === 'username'">
              <span>{{ record.username || '-' }}</span>
            </template>
            <template v-else-if="column.key === 'typeLabel'">
              <a-tag :color="record.direction === 'income' ? 'green' : 'blue'">{{ buildBillingRecordTypeText(record) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'subject'">
              <OverflowTableText
                :text="buildBillingRecordListSubjectText(record)"
                :expanded="isSubjectExpanded(record.id)"
                @toggle="toggleSubject(record.id)"
              />
            </template>
            <template v-else-if="column.key === 'source'">
              <span class="multiline-cell">{{ buildBillingRecordSourceText(record) }}</span>
            </template>
            <template v-else-if="column.key === 'searchMode'">
              <span>{{ buildBillingRecordModeText(record) }}</span>
            </template>
            <template v-else-if="column.key === 'amount'">
              <span :class="record.direction === 'income' ? 'amount-income' : 'amount-expense'">{{ buildBillingRecordAmountText(record) }}</span>
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
            <template v-else-if="column.key === 'clientIp'">
              <span class="location-value">{{ formatValueWithLocation(record.clientIp, ipLocations[normalizeLocationKey(record.clientIp)]) || '-' }}</span>
            </template>
            <template v-else-if="column.key === 'feeDetail'">
              <a-button type="link" style="padding: 0" @click="handleShowFeeDetail(record)">查看详情</a-button>
            </template>
            <template v-else-if="column.key === 'effectiveMultiplier'">{{ buildBillingRecordEffectiveMultiplierText(record) }}</template>
            <template v-else>{{ text }}</template>
          </template>
          <template #emptyText>
            <a-empty description="当前还没有账单记录。" />
          </template>
        </a-table>
      </ConsoleTableShell>
      <div class="mobile-billing-list">
        <article v-for="record in dataSource" :key="record.id" class="mobile-billing-card">
          <header class="mobile-billing-card__header">
            <a-tag :color="record.direction === 'income' ? 'green' : 'blue'">{{ buildBillingRecordTypeText(record) }}</a-tag>
            <strong :class="record.direction === 'income' ? 'amount-income' : 'amount-expense'">{{ buildBillingRecordAmountText(record) }}</strong>
          </header>
          <div class="mobile-billing-card__subject">
            <OverflowTableText
              :text="buildBillingRecordListSubjectText(record)"
              :expanded="isSubjectExpanded(record.id)"
              @toggle="toggleSubject(record.id)"
            />
          </div>
          <dl class="mobile-billing-card__meta">
            <div v-if="isAdmin"><dt>用户</dt><dd>{{ record.username || '-' }}</dd></div>
            <div><dt>来源</dt><dd>{{ buildBillingRecordSourceText(record) }}</dd></div>
            <div><dt>时间</dt><dd class="mobile-billing-time">{{ formatDateTime(record.createdAt) }}</dd></div>
            <div><dt>编号</dt><dd>{{ buildBillingRecordReferenceText(record) }}</dd></div>
          </dl>
          <a-button block size="small" @click="handleShowFeeDetail(record)">查看详情</a-button>
        </article>
        <a-empty v-if="!dataSource.length" description="当前还没有账单记录" />
      </div>
      <div class="mobile-billing-pagination">
        <a-pagination :current="pagination.current" :page-size="pagination.pageSize" :total="pagination.total" size="small" show-less-items @change="handleMobilePageChange" />
      </div>
    </ConsolePanel>
    <BillingDetailDialog
      v-model:open="billingDetailOpen"
      :record="selectedBillingRecord"
      :viewer-scope="isAdmin ? 'admin' : 'self'"
      :can-edit-statement="isAdmin"
      :statement-saving="!!visibilitySaving[selectedBillingRecord?.id]"
      @statement-visibility-change="handleStatementVisibilityChange"
    />
  </div>
</template>

<script>
import { computed, defineComponent, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import OverflowTableText from '@/components/OverflowTableText.vue'
import BillingDetailDialog from '@/components/BillingDetailDialog.vue'
import { useTableExpansion } from '@/composables/useTableExpansion.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'
import { getBillingRecords, getIpLocations, getMyLocations, updateBillingStatementVisibility } from '@/api/TikuComponentApi'
import { getStoredCurrentUser, isAdminUser } from '@/utils/currentUser'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'
import { buildSearchParams, countActiveFilters, normalizeSearchKeyword } from '@/utils/listSearchHelpers.mjs'
import {
  collectLocationKeys,
  lookupLocationAddresses,
  normalizeLocationKey,
  formatValueWithLocation as formatValueWithLocationBase,
} from '@/utils/locationHelpers.mjs'
import {
  buildBillingRecordAmountText,
  buildBillingRecordEffectiveMultiplierText,
  buildBillingRecordListSubjectText,
  buildBillingRecordModeText,
  buildBillingRecordReferenceText,
  buildBillingRecordSourceText,
  buildBillingRecordSubjectText,
  buildBillingRecordTypeText,
  billingTableColumnWidths,
  formatBillingRecordTime,
} from '@/utils/userPortalHelpers.mjs'

const dataSource = ref([])
const tableLoading = ref(false)
const visibilitySaving = reactive({})

// 归属地映射：key 为归一化后的 IP，value 为展示文本。
// 页面加载时按当前页的 clientIp 批量查询并落库（服务端持久化，之后翻页零消耗）。
const ipLocations = reactive({})

function formatValueWithLocation(rawValue, address) {
  return formatValueWithLocationBase(rawValue, address)
}

// enrichClientIps 批量拉取本页来源 IP 的归属地。
//
// 失败静默：归属地只是展示增强，未配置密钥或上游异常时对应列退化为只显示原始 IP。
function enrichClientIps(rows) {
  const ips = collectLocationKeys(rows, 'clientIp')
  if (!ips.length) {
    return
  }
  const admin = isAdminUser(getStoredCurrentUser())
  lookupLocationAddresses('ip', ips, admin ? getIpLocations : getMyLocations, admin ? '' : 'ip')
    .then((addresses) => {
      Object.keys(addresses).forEach((key) => {
        ipLocations[normalizeLocationKey(key)] = addresses[key]
      })
    })
}

// 服务端返回的金额统计：totalAmount 覆盖全部筛选结果，currentPageAmount 仅当前页。
const amountSummary = reactive({
  totalAmount: 0,
  currentPageAmount: 0,
})
// 已生效的筛选条件；keywordInput 只是输入框的即时值。
const filters = reactive({
  keyword: '',
  type: undefined,
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
  name: 'BillingRecordList',
  components: { OverflowTableText, BillingDetailDialog },
  setup() {
    const subjectExpansion = useTableExpansion(dataSource)
    const selectedBillingRecord = ref(null)
    const billingDetailOpen = ref(false)
    const currentUser = computed(() => getStoredCurrentUser())
    const isAdmin = computed(() => isAdminUser(currentUser.value))
    const activeFilterCount = computed(() => countActiveFilters({ keyword: filters.keyword, type: filters.type }))

    const summary = computed(() => {
      const rows = Array.isArray(dataSource.value) ? dataSource.value : []
      const answerSources = new Set()
      return rows.reduce((acc, item) => {
        const answerSourceKey = String(item?.answerSourceLabel || item?.providerLabel || item?.provider || '').trim()
        acc.currentPageCount += 1
        if (answerSourceKey) {
          answerSources.add(answerSourceKey)
        }
        acc.providerCount = answerSources.size
        return acc
      }, {
        total: Number(pagination.total || 0),
        currentPageCount: 0,
        // 金额一律用服务端口径：它能覆盖所有页，且已按请求去重、剔除充值等收入项。
        totalAmount: Number(amountSummary.totalAmount || 0),
        currentPageAmount: Number(amountSummary.currentPageAmount || 0),
        providerCount: 0,
      })
    })

    // 当前是否处于筛选状态：筛选会改变「累计消费金额」的口径，需要在界面上说明。
    const hasActiveFilters = computed(() => countActiveFilters({
      keyword: filters.keyword,
      type: filters.type,
    }) > 0)

    const pageTitle = computed(() => (isAdmin.value ? '账单明细' : '我的账单'))
    const pageSubtitle = computed(() => (
      isAdmin.value
        ? '查看平台内全部账单明细，包含题目消费、充值与管理员余额调整，也包含未向用户展示的记录。'
        : '查看当前账号名下的账单流水，包含题目消费、充值入账、来源渠道、请求编号与请求时间。'
    ))
    const alertMessage = computed(() => (
      isAdmin.value
        ? '管理员始终可查看全部账单；通过“账单流水展示”开关调整用户可见性，不改变原始金额和余额记录。'
        : '这里只展示当前账号自己的账单流水，便于快速查看每次消费或充值对应的来源、请求编号与时间。'
    ))
    const subjectColumnWidth = computed(() => subjectExpansion.columnWidth('subject', billingTableColumnWidths.subject, billingTableColumnWidths.subjectExpanded))
    const columns = computed(() => {
      const baseColumns = [
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
          // 深度思考 / 普通搜索：非搜题账单与加列之前的历史行显示占位符。
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
          title: '最终倍率',
          dataIndex: 'effectiveMultiplier',
          key: 'effectiveMultiplier',
          width: billingTableColumnWidths.effectiveMultiplier,
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
          title: '来源 IP',
          dataIndex: 'clientIp',
          key: 'clientIp',
          width: billingTableColumnWidths.clientIp,
        },
        {
          title: '操作',
          dataIndex: 'feeDetail',
          key: 'feeDetail',
          width: billingTableColumnWidths.feeDetail,
        },
      ]

      if (!isAdmin.value) {
        return baseColumns.filter((column) => !['effectiveMultiplier', 'clientIp'].includes(column.key))
      }

      return [
        baseColumns[0],
        {
          title: '用户',
          dataIndex: 'username',
          key: 'username',
          width: billingTableColumnWidths.username,
        },
        ...baseColumns.slice(1),
      ]
    })
    const tableScrollX = computed(() => sumTableColumnWidths(columns.value))

    return {
      dataSource,
      // 归属地展示：映射 + 取值/格式化函数（模板按行取值）。
      ipLocations,
      normalizeLocationKey,
      formatValueWithLocation,
      tableLoading,
      visibilitySaving,
      isAdmin,
      pagination,
      summary,
      filters,
      keywordInput,
      activeFilterCount,
      hasActiveFilters,
      typeOptions: [
        { label: '题目消费', value: 'normal_search' },
        { label: '充值入账', value: 'recharge' },
        { label: '新用户赠送', value: 'registration_bonus' },
        { label: '管理员调整余额', value: 'balance_adjustment' },
      ],
      pageTitle,
      pageSubtitle,
      alertMessage,
      columns,
      tableScrollX,
      selectedBillingRecord,
      billingDetailOpen,
      isSubjectExpanded: (id) => subjectExpansion.isExpanded(id, 'subject'),
      toggleSubject: (id) => subjectExpansion.toggle(id, 'subject'),
      buildBillingRecordEffectiveMultiplierText,
      buildBillingRecordAmountText,
      buildBillingRecordListSubjectText,
      buildBillingRecordModeText,
      buildBillingRecordReferenceText,
      buildBillingRecordSourceText,
      buildBillingRecordSubjectText,
      buildBillingRecordTypeText,
    }
  },
  mounted() {
    this.handleLoad()
  },
  methods: {
    async handleStatementVisibilityChange({ id, showInStatement: show }) {
      if (!this.isAdmin || visibilitySaving[id]) return false
      const record = dataSource.value.find((item) => item.id === id)
        || (this.selectedBillingRecord?.id === id ? this.selectedBillingRecord : null)
      if (!record) return false
      visibilitySaving[id] = true
      try {
        const res = await updateBillingStatementVisibility(id, show)
        if (res?.status !== 200) throw new Error(res?.message || '修改流水可见性失败')
        record.showInStatement = show
        const currentRow = dataSource.value.find((item) => item.id === id)
        if (currentRow) currentRow.showInStatement = show
        if (this.selectedBillingRecord?.id === id) this.selectedBillingRecord.showInStatement = show
        message.success(show ? '已在用户账单流水中显示' : '已从用户账单流水中隐藏，管理员明细仍保留')
        return true
      } catch (error) {
        message.error(error?.response?.data?.message || error?.message || '修改流水可见性失败')
        return false
      } finally {
        visibilitySaving[id] = false
      }
    },
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
      filters.type = undefined
      pagination.current = 1
      this.handleLoad()
    },
    handleLoad() {
      tableLoading.value = true
      getBillingRecords(buildSearchParams({
        keyword: filters.keyword,
        type: filters.type,
      }, pagination.current, pagination.pageSize)).then((res) => {
        if (res?.status === 200) {
          dataSource.value = Array.isArray(res?.data?.data) ? res.data.data : []
          pagination.total = Number(res?.data?.total || 0)
          pagination.current = Number(res?.data?.page || 1)
          pagination.pageSize = Number(res?.data?.pageSize || 10)
          // 金额统计由服务端给出：覆盖全部筛选结果，而非仅当前页。
          amountSummary.totalAmount = Number(res?.data?.summary?.totalAmount || 0)
          amountSummary.currentPageAmount = Number(res?.data?.summary?.currentPageAmount || 0)
          // 来源 IP 归属地：按当前页批量查询并落库（失败静默）。
          enrichClientIps(dataSource.value)
        } else {
          message.error(res?.message || '获取账单列表失败')
        }
      }).catch(() => {
        message.error('获取账单列表失败')
      }).finally(() => {
        tableLoading.value = false
      })
    },
    handleTableChange(paginationData) {
      pagination.current = paginationData.current
      pagination.pageSize = paginationData.pageSize
      this.handleLoad()
    },
    handleMobilePageChange(current, pageSize) {
      pagination.current = current
      pagination.pageSize = pageSize
      this.handleLoad()
    },
    handleShowFeeDetail(record) {
      this.selectedBillingRecord = record
      this.billingDetailOpen = true
    },
    formatAmount(value) {
      return formatMoneyValue(value || 0)
    },
    formatDateTime(value) {
      return formatBillingRecordTime(value)
    },
  },
})
</script>

<style scoped>
.summary-card { padding: 20px; border: 1px solid var(--console-border); border-radius: 10px; background: var(--console-surface); }
.summary-hint { margin: 8px 0 0; font-size: 12px; color: var(--console-muted); line-height: 1.6; }
.page-alert { margin-bottom: 20px; }
.filter-form { display: flex; flex-wrap: wrap; gap: 12px 0; margin-bottom: 20px; }
.filter-form :deep(.ant-form-item) { margin-bottom: 0; }
.multiline-cell, .reference-cell, .location-value, .billing-time-cell { white-space: pre-wrap; overflow-wrap: anywhere; }
.billing-time-cell { font-variant-numeric: tabular-nums; }
.amount-income { color: var(--console-success, #15803d); }
.amount-expense { color: var(--console-danger, #c13e41); }
.mobile-billing-list, .mobile-billing-pagination { display: none; }
@media (max-width: 640px) {
  .summary-card { padding: 16px; }
  .filter-form { display: block; }
  .filter-form :deep(.ant-form-item) { margin: 0 0 12px; }
  .filter-form :deep(.ant-form-item-control), .filter-form :deep(.ant-input-search), .filter-form :deep(.ant-select) { width: 100% !important; }
  .desktop-billing-table { display: none; }
  .mobile-billing-list { display: grid; gap: 12px; }
  .mobile-billing-card { padding: 16px; border: 1px solid var(--console-border); border-radius: 8px; background: var(--console-surface); min-width: 0; }
  .mobile-billing-card__header { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
  .mobile-billing-card__subject { margin: 16px 0; text-align: center; overflow-wrap: anywhere; }
  .mobile-billing-card__meta { display: grid; gap: 8px; margin: 0 0 16px; }
  .mobile-billing-card__meta > div { display: grid; grid-template-columns: 3em minmax(0, 1fr); gap: 12px; }
  .mobile-billing-card__meta dt { color: var(--console-muted); }
  .mobile-billing-card__meta dd { margin: 0; overflow-wrap: anywhere; white-space: pre-wrap; }
  .mobile-billing-time { white-space: pre-line; }
  .mobile-billing-pagination { display: flex; justify-content: center; margin-top: 16px; }
}
</style>
