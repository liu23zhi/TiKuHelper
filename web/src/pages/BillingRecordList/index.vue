<template>
  <div class="billing-page">
    <a-row :gutter="16" class="summary-row">
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="账单总数（筛选结果）" :value="summary.total" />
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="当前页条数" :value="summary.currentPageCount" />
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic
            :title="hasActiveFilters ? '累计消费金额（当前筛选，全部页）' : '累计消费金额（全部页）'"
            prefix="¥"
            :value="summary.totalAmount"
            :precision="6"
          />
          <div class="summary-hint">不包含充值入账与新用户赠送</div>
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic
            title="当前页累计消费金额"
            prefix="¥"
            :value="summary.currentPageAmount"
            :precision="6"
          />
          <div class="summary-hint">仅统计第 {{ pagination.current }} 页 {{ summary.currentPageCount }} 条</div>
        </a-card>
      </a-col>
    </a-row>

    <a-card :bordered="false" class="page-card">
      <template #title>
        <div class="page-title-wrap">
          <div>
            <div class="page-title">{{ pageTitle }}</div>
            <div class="page-subtitle">{{ pageSubtitle }}</div>
          </div>
        </div>
      </template>
      <template #extra>
        <a-button @click="handleLoad" :loading="tableLoading">刷新</a-button>
      </template>

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

      <a-table
        bordered
        :data-source="dataSource"
        :columns="columns"
        :loading="tableLoading"
        :pagination="pagination"
        @change="handleTableChange"
        row-key="id"
        :scroll="{ x: 1460 }"
      >
        <template #username="{ record }">
          <span>{{ record.username || '-' }}</span>
        </template>
        <template #typeLabel="{ record }">
          <a-tag :color="record.direction === 'income' ? 'green' : 'blue'">{{ buildBillingRecordTypeText(record) }}</a-tag>
        </template>
        <template #subject="{ record }">
          <span class="multiline-cell">{{ buildBillingRecordSubjectText(record) }}</span>
        </template>
        <template #source="{ record }">
          <span class="multiline-cell">{{ buildBillingRecordSourceText(record) }}</span>
        </template>
        <template #searchMode="{ record }">
          <span>{{ buildBillingRecordModeText(record) }}</span>
        </template>
        <template #amount="{ record }">
          <span :class="record.direction === 'income' ? 'amount-income' : 'amount-expense'">{{ buildBillingRecordAmountText(record) }}</span>
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
        <template #clientIp="{ record }">
          <span class="location-value">{{ formatValueWithLocation(record.clientIp, ipLocations[normalizeLocationKey(record.clientIp)]) || '-' }}</span>
        </template>
        <template #feeDetail="{ record }">
          <a-button type="link" style="padding: 0" @click="handleShowFeeDetail(record)">显示详情</a-button>
        </template>
        <template #emptyText>
          <a-empty description="当前还没有账单记录。" />
        </template>
      </a-table>
    </a-card>
  </div>
</template>

<script>
import { computed, defineComponent, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import Swal from 'sweetalert2'
import { getBillingRecords, getIpLocations, getMyLocations } from '@/api/TikuComponentApi'
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
  buildBillingRecordFinalMultiplierText,
  buildBillingRecordFeeDetailHtml,
  buildBillingRecordModeText,
  buildBillingRecordReferenceText,
  buildBillingRecordSourceText,
  buildBillingRecordSubjectText,
  buildBillingRecordTypeText,
} from '@/utils/userPortalHelpers.mjs'

const dataSource = ref([])
const tableLoading = ref(false)

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
  setup() {
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
        ? '查看平台内全部账单流水，包含题目消费、充值入账、来源渠道、请求编号与请求时间。'
        : '查看当前账号名下的账单流水，包含题目消费、充值入账、来源渠道、请求编号与请求时间。'
    ))
    const alertMessage = computed(() => (
      isAdmin.value
        ? '管理员可查看全部账单。'
        : '这里只展示当前账号自己的账单流水，便于快速查看每次消费或充值对应的来源、请求编号与时间。'
    ))
    const columns = computed(() => {
      const baseColumns = [
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
          width: 280,
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
          width: 180,
          slots: {
            customRender: 'source',
          },
        },
        {
          // 深度思考 / 普通搜索：非搜题账单与加列之前的历史行显示占位符。
          title: '模式',
          dataIndex: 'searchMode',
          key: 'searchMode',
          width: 120,
          slots: {
            customRender: 'searchMode',
          },
        },
        {
          title: '请求编号',
          dataIndex: 'referenceNo',
          key: 'referenceNo',
          width: 240,
          slots: {
            customRender: 'referenceNo',
          },
        },
        {
          title: '最终倍率',
          dataIndex: 'finalMultiplier',
          key: 'finalMultiplier',
          width: 120,
          customRender: ({ record }) => buildBillingRecordFinalMultiplierText(record),
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
        {
          title: '来源 IP',
          dataIndex: 'clientIp',
          key: 'clientIp',
          width: 200,
          slots: {
            customRender: 'clientIp',
          },
        },
        {
          title: '操作',
          dataIndex: 'feeDetail',
          key: 'feeDetail',
          width: 120,
          slots: {
            customRender: 'feeDetail',
          },
        },
      ]

      if (!isAdmin.value) {
        return baseColumns
      }

      return [
        baseColumns[0],
        {
          title: '用户名',
          dataIndex: 'username',
          key: 'username',
          width: 160,
          slots: {
            customRender: 'username',
          },
        },
        ...baseColumns.slice(1),
      ]
    })

    return {
      dataSource,
      // 归属地展示：映射 + 取值/格式化函数（模板按行取值）。
      ipLocations,
      normalizeLocationKey,
      formatValueWithLocation,
      tableLoading,
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
      ],
      pageTitle,
      pageSubtitle,
      alertMessage,
      columns,
      buildBillingRecordAmountText,
      buildBillingRecordFinalMultiplierText,
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
    handleShowFeeDetail(record) {
      Swal.fire({
        title: '账单详情',
        html: buildBillingRecordFeeDetailHtml(record),
        width: 560,
        confirmButtonText: '关闭',
        heightAuto: false,
        scrollbarPadding: false,
        customClass: {
          popup: 'billing-fee-popup',
        },
      })
    },
    formatAmount(value) {
      return formatMoneyValue(value || 0)
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
  },
})
</script>

<style scoped>
.billing-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.summary-hint {
  margin-top: 4px;
  font-size: 12px;
  color: rgba(0, 0, 0, 0.45);
}

.filter-form {
  margin: 12px 0 16px;
  display: flex;
  flex-wrap: wrap;
  gap: 8px 0;
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

.multiline-cell {
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.6;
}

.location-value {
  display: inline-block;
  white-space: pre-line;
  line-height: 1.5;
  text-align: left;
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

:global(.billing-fee-popup) {
  border-radius: 22px;
}

:global(.billing-fee-detail) {
  display: flex;
  flex-direction: column;
  gap: 14px;
  text-align: left;
}

:global(.billing-fee-card) {
  border: 1px solid #e2e8f0;
  border-radius: 16px;
  padding: 16px;
  box-shadow: 0 10px 28px rgb(15 23 42 / 8%);
}

:global(.billing-fee-card--question) {
  background: linear-gradient(135deg, #f8fafc, #eef2ff);
}

:global(.billing-fee-card--cost) {
  background: #fff;
}

:global(.billing-fee-section-title) {
  margin-bottom: 10px;
  color: #64748b;
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 0.12em;
}

:global(.billing-fee-question) {
  color: #0f172a;
  font-size: 15px;
  font-weight: 600;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-word;
}

:global(.billing-fee-list) {
  display: grid;
  gap: 10px;
}

:global(.billing-fee-item) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 24px;
  padding: 12px 14px;
  border: 1px solid #e2e8f0;
  border-radius: 12px;
  background: #f8fafc;
}

:global(.billing-fee-item-detail) {
  margin: -4px 14px 0;
  color: #64748b;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

:global(.billing-fee-provider) {
  color: #334155;
}

:global(.billing-fee-amount) {
  font-weight: 700;
  white-space: nowrap;
}

:global(.billing-fee-amount--income) {
  color: #16a34a;
}

:global(.billing-fee-amount--expense) {
  color: #dc2626;
}

:global(.billing-fee-total) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 24px;
  margin-top: 14px;
  padding: 14px;
  border: 1px solid #bfdbfe;
  border-radius: 12px;
  background: linear-gradient(135deg, #eff6ff, #f8fafc);
  color: #1f2937;
  font-size: 16px;
  font-weight: 600;
}
</style>
