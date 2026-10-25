<template>
  <div class="console-page billing-page">
    <header class="section-heading page-heading">
      <div>
        <h1>{{ pageTitle }}</h1>
        <p class="page-lead">{{ pageSubtitle }}</p>
      </div>
      <a-button @click="handleLoad" :loading="tableLoading">刷新</a-button>
    </header>
    <a-row :gutter="16" class="summary-row">
      <a-col :xs="12" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="账单总数（筛选结果）" :value="summary.total" />
        </a-card>
      </a-col>
      <a-col :xs="12" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="当前页条数" :value="summary.currentPageCount" />
        </a-card>
      </a-col>
      <a-col :xs="12" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic
            :title="hasActiveFilters ? '累计消费金额（当前筛选，全部页）' : '累计消费金额（全部页）'"
            prefix="¥"
            :value="summary.totalAmount"
            :precision="6"
          />
          <div class="summary-hint">不包含充值、赠送及管理员余额调整</div>
        </a-card>
      </a-col>
      <a-col :xs="12" :sm="12" :lg="6">
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
        <template #username="{ record }">
          <span>{{ record.username || '-' }}</span>
        </template>
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
          <span class="billing-time-cell">{{ formatDateTime(record.createdAt) }}</span>
        </template>
        <template #clientIp="{ record }">
          <span class="location-value">{{ formatValueWithLocation(record.clientIp, ipLocations[normalizeLocationKey(record.clientIp)]) || '-' }}</span>
        </template>
        <template #feeDetail="{ record }">
          <a-button type="link" style="padding: 0" @click="handleShowFeeDetail(record)">查看详情</a-button>
        </template>
        <template #emptyText>
          <a-empty description="当前还没有账单记录。" />
        </template>
      </a-table>
      <div class="mobile-billing-list">
        <article v-for="record in dataSource" :key="record.id" class="mobile-billing-card">
          <header class="mobile-billing-card__header">
            <a-tag :color="record.direction === 'income' ? 'green' : 'blue'">{{ buildBillingRecordTypeText(record) }}</a-tag>
            <strong :class="record.direction === 'income' ? 'amount-income' : 'amount-expense'">{{ buildBillingRecordAmountText(record) }}</strong>
          </header>
          <div class="mobile-billing-card__subject">
            <OverflowTableText
              :text="buildBillingRecordListSubjectText(record)"
              :expanded="!!expandedSubjects[record.id]"
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
    </a-card>
  </div>
</template>

<script>
import { computed, defineComponent, getCurrentInstance, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import Swal from 'sweetalert2'
import OverflowTableText from '@/components/OverflowTableText.vue'
import { getBillingRecords, getIpLocations, getMyLocations, updateBillingStatementVisibility } from '@/api/TikuComponentApi'
import { getStoredCurrentUser, useCurrentUserDisplay, isAdminUser } from '@/utils/currentUser'
import { useCurrentUserRequestScope } from '@/utils/currentUserScope'
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

const dataSource = ref([])
const tableLoading = ref(false)
const visibilitySaving = reactive({})
const expandedSubjects = reactive({})

// 归属地映射：key 为归一化后的 IP，value 为展示文本。
// 页面加载时按当前页的 clientIp 批量查询并落库（服务端持久化，之后翻页零消耗）。
const ipLocations = reactive({})

function formatValueWithLocation(rawValue, address) {
  return formatValueWithLocationBase(rawValue, address)
}

// enrichClientIps 批量拉取本页来源 IP 的归属地。
//
// 失败静默：归属地只是展示增强，未配置密钥或上游异常时对应列退化为只显示原始 IP。
function enrichClientIps(rows, owns = () => true) {
  const ips = collectLocationKeys(rows, 'clientIp')
  if (!ips.length) {
    return
  }
  const admin = isAdminUser(getStoredCurrentUser())
  lookupLocationAddresses('ip', ips, admin ? getIpLocations : getMyLocations, admin ? '' : 'ip')
    .then((addresses) => {
      if (!owns()) return
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
  components: { OverflowTableText },
  setup() {
    const { currentUser } = useCurrentUserDisplay()
    const isAdmin = computed(() => isAdminUser(currentUser.value))
    const instance = getCurrentInstance()
    let ownedDetailPopup = null
    const closeScopePopup = () => {
      if (ownedDetailPopup && Swal.getPopup() === ownedDetailPopup) Swal.close()
      ownedDetailPopup = null
    }
    const ownScopePopup = popup => { ownedDetailPopup = popup }
    const { captureDataOwner, canLoadForCurrentUser } = useCurrentUserRequestScope(currentUser, {
      clear() {
        dataSource.value = []
        tableLoading.value = false
        pagination.total = 0
        amountSummary.totalAmount = 0
        amountSummary.currentPageAmount = 0
      Object.keys(ipLocations).forEach(key => { delete ipLocations[key] })
      Object.keys(expandedSubjects).forEach(key => { delete expandedSubjects[key] })
        closeScopePopup()
      },
      reset() { filters.keyword = ''; keywordInput.value = ''; pagination.current = 1; filters.type = undefined },
      reload() { instance.proxy.handleLoad() },
    })
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
    const subjectColumnWidth = computed(() => dataSource.value.some((record) => expandedSubjects[record.id])
      ? billingTableColumnWidths.subjectExpanded
      : billingTableColumnWidths.subject)
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
          // 深度思考 / 普通搜索：非搜题账单与加列之前的历史行显示占位符。
          title: '模式',
          dataIndex: 'searchMode',
          key: 'searchMode',
          width: billingTableColumnWidths.searchMode,
          slots: {
            customRender: 'searchMode',
          },
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
          title: '最终倍率',
          dataIndex: 'effectiveMultiplier',
          key: 'effectiveMultiplier',
          width: billingTableColumnWidths.effectiveMultiplier,
          customRender: ({ record }) => buildBillingRecordEffectiveMultiplierText(record),
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
          title: '来源 IP',
          dataIndex: 'clientIp',
          key: 'clientIp',
          width: billingTableColumnWidths.clientIp,
          slots: {
            customRender: 'clientIp',
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
      ]

      if (!isAdmin.value) {
        return baseColumns
      }

      return [
        baseColumns[0],
        {
          title: '用户',
          dataIndex: 'username',
          key: 'username',
          width: billingTableColumnWidths.username,
          slots: {
            customRender: 'username',
          },
        },
        ...baseColumns.slice(1),
      ]
    })
    const tableScrollX = computed(() => columns.value.reduce((total, column) => total + column.width, 0))

    return {
      captureDataOwner, canLoadForCurrentUser,
      ownScopePopup,
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
      expandedSubjects,
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
    toggleSubject(id) {
      expandedSubjects[id] = !expandedSubjects[id]
    },
    async handleStatementVisibilityChange(record, show) {
      if (!this.isAdmin || !dataSource.value.includes(record)) return false
      const owns = this.captureDataOwner()
      if (visibilitySaving[record.id]) return false
      visibilitySaving[record.id] = true
      try {
        const res = await updateBillingStatementVisibility(record.id, show)
        if (!owns()) return false
        if (res?.status !== 200) throw new Error(res?.message || '修改流水可见性失败')
        record.showInStatement = show
        message.success(show ? '已在用户账单流水中显示' : '已从用户账单流水中隐藏，管理员明细仍保留')
        return true
      } catch (error) {
        message.error(error?.response?.data?.message || error?.message || '修改流水可见性失败')
        return false
      } finally {
        visibilitySaving[record.id] = false
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
      if (!this.canLoadForCurrentUser()) return
      const owns = this.captureDataOwner()
      tableLoading.value = true
      getBillingRecords(buildSearchParams({
        keyword: filters.keyword,
        type: filters.type,
      }, pagination.current, pagination.pageSize)).then((res) => {
        if (!owns()) return
        if (res?.status === 200) {
          dataSource.value = Array.isArray(res?.data?.data) ? res.data.data : []
          pagination.total = Number(res?.data?.total || 0)
          pagination.current = Number(res?.data?.page || 1)
          pagination.pageSize = Number(res?.data?.pageSize || 10)
          // 金额统计由服务端给出：覆盖全部筛选结果，而非仅当前页。
          amountSummary.totalAmount = Number(res?.data?.summary?.totalAmount || 0)
          amountSummary.currentPageAmount = Number(res?.data?.summary?.currentPageAmount || 0)
          // 来源 IP 归属地：按当前页批量查询并落库（失败静默）。
          enrichClientIps(dataSource.value, owns)
        } else {
          message.error(res?.message || '获取账单列表失败')
        }
      }).catch(() => {
        if (!owns()) return
        message.error('获取账单列表失败')
      }).finally(() => {
        if (!owns()) return
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
      if (!this.canLoadForCurrentUser() || !dataSource.value.includes(record)) return
      const detailRecord = { ...record, canEditStatement: this.isAdmin }
      Swal.fire({
        title: '账单详情',
        willOpen: popup => this.ownScopePopup(popup),
        html: buildBillingRecordFeeDetailHtml(detailRecord),
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
        didOpen: () => {
          const toggle = document.querySelector('.billing-statement-toggle')
          if (!toggle) return
          toggle.addEventListener('click', async () => {
            if (toggle.disabled) return
            const show = toggle.getAttribute('aria-checked') !== 'true'
            toggle.disabled = true
            const saved = await this.handleStatementVisibilityChange(record, show)
            if (saved) toggle.setAttribute('aria-checked', String(show))
            toggle.disabled = false
          })
        },
      })
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
.billing-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.page-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}

.page-heading h1 {
  margin: 0;
  color: var(--console-text, var(--text));
  font-size: 20px;
  font-weight: 650;
}

.page-lead {
  margin: 4px 0 0;
  color: var(--console-muted, var(--text-muted));
  font-size: 13px;
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
  border: 1px solid var(--console-border, var(--border));
  border-radius: 8px;
}

.page-card {
  border: 1px solid var(--console-border, var(--border));
  border-radius: var(--console-radius, 12px);
}

.page-alert {
  margin-bottom: 16px;
}

.mobile-billing-list,
.mobile-billing-pagination {
  display: none;
}

.filter-form {
  padding: 16px;
  border: 1px solid var(--console-border, var(--border));
  border-radius: 8px;
  background: var(--console-canvas, var(--bg));
}

@media (max-width: 640px) {
  .billing-page .page-card :deep(.ant-card-body) {
    padding: 12px;
  }

  .desktop-billing-table {
    display: none;
  }

  .mobile-billing-list {
    display: grid;
    gap: 10px;
  }

  .mobile-billing-card {
    display: grid;
    gap: 10px;
    padding: 12px;
    border: 1px solid var(--console-border, var(--border));
    border-radius: 8px;
    background: var(--console-surface, var(--surface));
  }

  .mobile-billing-card__header,
  .mobile-billing-card__adjustment {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
  }

  .mobile-billing-card__subject,
  .mobile-billing-card__meta dd,
  .mobile-billing-card__adjustment strong {
    overflow-wrap: anywhere;
  }

  .mobile-billing-card__subject {
    color: var(--console-text, var(--text));
    font-size: 14px;
    font-weight: 600;
    line-height: 1.6;
  }

  .mobile-billing-card__subject :deep(.overflow-table-text) {
    text-align: left;
  }

  .mobile-billing-card__subject :deep(.overflow-table-text__toggle) {
    font-weight: 400;
  }

  .mobile-billing-time {
    white-space: pre-line;
  }

  .mobile-billing-card__meta {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
    margin: 0;
    padding: 10px 0;
    border-top: 1px solid var(--console-border, var(--border));
    border-bottom: 1px solid var(--console-border, var(--border));
  }

  .mobile-billing-card__meta div {
    min-width: 0;
  }

  .mobile-billing-card__meta dt,
  .mobile-billing-card__adjustment span {
    color: var(--console-muted, var(--text-muted));
    font-size: 11px;
  }

  .mobile-billing-card__meta dd {
    margin: 2px 0 0;
    color: var(--console-text, var(--text));
    font-size: 12px;
  }

  .mobile-billing-card__adjustment {
    padding: 8px;
    border-radius: 6px;
    background: var(--surface-subtle);
  }

  .mobile-billing-pagination {
    display: flex;
    justify-content: flex-end;
    margin-top: 12px;
  }

  .filter-form,
  .filter-form :deep(.ant-form-item) {
    width: 100%;
  }

  .filter-form :deep(.ant-input),
  .filter-form :deep(.ant-input-search),
  .filter-form :deep(.ant-select) {
    width: 100%;
  }
}

.billing-page :deep(.ant-table) {
  font-size: 13px;
}

.billing-page :deep(.desktop-billing-table .ant-table-thead > tr > th),
.billing-page :deep(.desktop-billing-table .ant-table-tbody > tr > td) {
  text-align: center;
  vertical-align: middle;
  white-space: normal;
  overflow-wrap: anywhere;
}

.multiline-cell {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  line-height: 1.6;
}

.billing-time-cell {
  display: block;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.location-value {
  display: block;
  white-space: pre-wrap;
  line-height: 1.5;
  text-align: center;
  overflow-wrap: anywhere;
}

.reference-cell {
  display: block;
  overflow-wrap: anywhere;
}

.amount-income {
  color: var(--success-fg);
  font-weight: 600;
}

.amount-expense {
  color: var(--danger-fg);
  font-weight: 600;
}

:global(.billing-fee-popup) {
  box-sizing: border-box;
  padding: 24px;
  border: 1px solid rgb(148 163 184 / 24%);
  border-radius: 20px;
  background:
    radial-gradient(circle at 92% 0%, rgb(37 99 235 / 10%), transparent 32%),
    linear-gradient(180deg, var(--surface) 0%, var(--surface-subtle) 100%);
  box-shadow:
    0 28px 80px rgb(15 23 42 / 24%),
    0 4px 16px rgb(15 23 42 / 8%);
}

:global(.billing-fee-title) {
  margin: 0 42px 0 0;
  padding: 0;
  color: var(--text);
  font-size: 24px;
  font-weight: 800;
  line-height: 1.25;
  text-align: left;
}

:global(.billing-fee-html-container) {
  margin: 16px 0 0;
  padding: 0;
  overflow: visible;
  text-align: left;
}

:global(.billing-fee-actions) {
  margin: 18px 0 0;
  padding: 0;
}

:global(.billing-fee-confirm) {
  width: 100%;
  min-height: 42px;
  padding: 10px 20px;
  border: 0;
  border-radius: 999px;
  background: var(--text);
  box-shadow: 0 8px 20px rgb(15 23 42 / 18%);
  color: var(--surface);
  font-size: 14px;
  font-weight: 700;
  transition:
    transform 0.16s ease,
    background 0.16s ease;
}

:global(.billing-fee-confirm:hover) {
  background: #1e293b;
  transform: translateY(-1px);
}

:global(.billing-fee-confirm:focus-visible),
:global(.billing-fee-close:focus-visible) {
  outline: 2px solid #93c5fd;
  outline-offset: 3px;
}

:global(.billing-fee-close) {
  top: 20px;
  right: 20px;
  width: 34px;
  height: 34px;
  border-radius: 50%;
  background: rgb(255 255 255 / 72%);
  box-shadow: 0 2px 8px rgb(15 23 42 / 8%);
  color: var(--text-muted);
  font-size: 22px;
  line-height: 1;
  transition:
    background 0.16s ease,
    color 0.16s ease;
}

:global(.billing-fee-close:hover) {
  background: var(--border);
  color: var(--text);
}

:global(.billing-fee-detail) {
  display: flex;
  flex-direction: column;
  gap: 14px;
  text-align: left;
}

:global(.billing-fee-card) {
  box-sizing: border-box;
  width: 100%;
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
  box-shadow: 0 8px 24px rgb(15 23 42 / 6%);
}

:global(.billing-fee-card--question) {
  border-color: rgb(147 197 253 / 28%);
  background:
    radial-gradient(circle at 100% 0%, rgb(96 165 250 / 24%), transparent 38%),
    linear-gradient(135deg, #172554 0%, #1e3a8a 60%, var(--primary-hover) 100%);
  box-shadow: 0 16px 36px rgb(30 64 175 / 22%);
  color: var(--surface);
}

:global(.billing-fee-card--cost) {
  background: var(--surface);
}

:global(.billing-fee-card--metadata) {
  padding: 14px 16px;
  border-color: var(--border);
  background: var(--surface-subtle);
  box-shadow: none;
}

:global(.billing-fee-overview) {
  overflow: hidden;
}

:global(.billing-fee-overview-head) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 10px 16px;
  margin-bottom: 14px;
}

:global(.billing-fee-overview-tags) {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

:global(.billing-fee-status) {
  display: inline-flex;
  align-items: center;
  min-height: 24px;
  padding: 3px 9px;
  border: 1px solid rgb(255 255 255 / 20%);
  border-radius: 999px;
  background: rgb(255 255 255 / 12%);
  color: var(--surface);
  font-size: 12px;
  font-weight: 700;
}

:global(.billing-fee-status--income) {
  background: rgb(34 197 94 / 20%);
  color: #dcfce7;
}

:global(.billing-fee-status--expense) {
  background: rgb(251 113 133 / 18%);
  color: #ffe4e6;
}

:global(.billing-fee-type) {
  color: var(--selected);
  font-size: 13px;
  font-weight: 600;
}

:global(.billing-fee-mode) {
  color: #bfdbfe;
  font-size: 12px;
}

:global(.billing-fee-section-head) {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}

:global(.billing-fee-section-title) {
  color: var(--text);
  font-size: 12px;
  font-weight: 800;
  letter-spacing: 0.1em;
}

:global(.billing-fee-section-subtitle) {
  margin-top: 4px;
  color: var(--placeholder);
  font-size: 11px;
}

:global(.billing-fee-count) {
  flex: 0 0 auto;
  padding: 4px 9px;
  border-radius: 999px;
  background: var(--selected);
  color: var(--primary);
  font-size: 11px;
  font-weight: 700;
}

:global(.billing-fee-question) {
  color: var(--surface);
  font-size: 16px;
  font-weight: 600;
  line-height: 1.65;
  white-space: pre-wrap;
  word-break: break-word;
}

:global(.billing-fee-list) {
  display: grid;
  gap: 11px;
}

:global(.billing-fee-item) {
  box-sizing: border-box;
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 10px;
  overflow: hidden;
  padding: 15px 16px;
  border: 1px solid var(--border);
  border-left: 3px solid #60a5fa;
  border-radius: 10px;
  background: linear-gradient(180deg, var(--surface) 0%, var(--surface-subtle) 100%);
}

:global(.billing-fee-item-head) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
}

:global(.billing-fee-item-meta) {
  display: flex;
  flex-wrap: wrap;
  gap: 7px 18px;
  color: var(--text-muted);
  font-size: 12px;
  line-height: 1.6;
}

:global(.billing-fee-meta-item) {
  display: inline-flex;
  align-items: baseline;
  min-width: 0;
  max-width: 100%;
}

:global(.billing-fee-meta-label) {
  flex: 0 0 auto;
  color: var(--text-muted);
}

:global(.billing-fee-meta-value) {
  color: var(--text);
  font-weight: 600;
  overflow-wrap: anywhere;
}

:global(.billing-fee-meta-item--keyword) {
  width: 100%;
  padding: 9px 11px;
  border: 1px solid var(--selected);
  border-radius: 8px;
  background: var(--selected);
  color: #1e3a8a;
}

:global(.billing-fee-item-detail) {
  margin: -4px 14px 0;
  color: var(--text-muted);
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

:global(.billing-fee-card--metadata .billing-fee-item-detail) {
  margin: 0 0 8px;
}

:global(.billing-fee-card--metadata .billing-fee-item-detail:last-child) {
  margin-bottom: 0;
}

:global(.billing-fee-item-category) {
  margin: -4px 14px 0;
  color: #0f766e;
  font-size: 12px;
  font-weight: 600;
}

:global(.billing-statement-control) {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  margin-top: 4px;
  color: var(--text);
  font-size: 13px;
}

:global(button.billing-statement-switch) {
  box-sizing: border-box;
  display: inline-flex;
  align-items: center;
  justify-content: flex-start;
  flex: 0 0 auto;
  width: 40px;
  min-width: 40px;
  max-width: 40px;
  height: 22px;
  min-height: 22px;
  max-height: 22px;
  overflow: hidden;
  padding: 2px;
  border: 0;
  border-radius: 999px;
  background: var(--placeholder);
  box-shadow: inset 0 0 0 1px rgb(148 163 184 / 45%);
  cursor: pointer;
  line-height: 0;
  vertical-align: middle;
  appearance: none;
  transition: background 0.16s ease;
}

:global(.billing-statement-switch[aria-checked="true"]) {
  justify-content: flex-end;
  background: var(--primary);
  box-shadow: inset 0 0 0 1px rgb(37 99 235 / 55%);
}

:global(.billing-statement-switch:disabled) {
  cursor: wait;
  opacity: 0.65;
}

:global(.billing-statement-switch__thumb) {
  display: block;
  flex: 0 0 16px;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: var(--surface);
  box-shadow: 0 1px 3px rgb(15 23 42 / 24%);
}

:global(.billing-statement-switch:focus-visible) {
  outline: 2px solid #93c5fd;
  outline-offset: 2px;
}

:global(.billing-statement-switch__label) {
  user-select: none;
}

:global(.billing-fee-provider) {
  color: #1e293b;
  font-size: 15px;
  font-weight: 700;
}

:global(.billing-fee-amount) {
  font-variant-numeric: tabular-nums;
  font-weight: 700;
  white-space: nowrap;
}

:global(.billing-fee-amount--income) {
  color: var(--success-fg);
}

:global(.billing-fee-amount--expense) {
  color: var(--danger-fg);
}

:global(.billing-fee-total) {
  box-sizing: border-box;
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  justify-content: space-between;
  gap: 24px;
  margin-top: 16px;
  padding: 16px;
  border: 0;
  border-radius: 10px;
  background: linear-gradient(120deg, var(--text) 0%, #1e293b 100%);
  box-shadow: 0 10px 24px rgb(15 23 42 / 16%);
  color: var(--surface-subtle);
  font-size: 15px;
  font-weight: 700;
}

:global(.billing-fee-total .billing-fee-amount) {
  font-size: 21px;
  font-weight: 800;
}

:global(.billing-fee-total .billing-fee-amount--expense) {
  color: #fecaca;
}

:global(.billing-fee-total .billing-fee-amount--income) {
  color: #bbf7d0;
}

@media (max-width: 480px) {
  :global(.billing-fee-popup) {
    padding: 20px 16px 16px;
    border-radius: 16px;
  }

  :global(.billing-fee-title) {
    font-size: 21px;
  }

  :global(.billing-fee-close) {
    top: 16px;
    right: 16px;
  }

  :global(.billing-fee-card) {
    padding: 14px;
  }

  :global(.billing-fee-item-head) {
    align-items: flex-start;
    flex-wrap: wrap;
    gap: 6px 12px;
  }

  :global(.billing-fee-item-head .billing-fee-amount) {
    margin-left: auto;
  }

  :global(.billing-fee-item-meta) {
    gap: 7px 12px;
  }

  :global(.billing-fee-total) {
    gap: 8px 16px;
    padding: 14px;
  }

  :global(.billing-fee-total .billing-fee-amount) {
    font-size: 19px;
  }
}
</style>
