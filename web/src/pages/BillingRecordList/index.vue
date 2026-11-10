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
      <div class="summary-card">
        <a-statistic title="当前页缓存命中率" :value="formatBillingCacheHitRate(summary.currentPageCacheHitRate)" :formatter="() => formatBillingCacheHitRate(summary.currentPageCacheHitRate)" />
        <p class="summary-hint">缓存输入 Token / 总输入 Token</p>
      </div>
      <div class="summary-card">
        <a-statistic :title="hasActiveFilters ? '综合缓存命中率（当前筛选，全部页）' : '综合缓存命中率（全部页）'" :value="formatBillingCacheHitRate(summary.totalCacheHitRate)" :formatter="() => formatBillingCacheHitRate(summary.totalCacheHitRate)" />
        <p class="summary-hint">仅统计 AI 模型输入 Token</p>
      </div>
    </ConsoleSummaryGrid>

    <ConsolePanel class="page-card">
      <RequestFailureAlert v-if="listFailure" :failure="listFailure" message="获取账单列表失败"
        :retryable="true" :busy="tableLoading" :context-key="listFailureContext" :is-current="isCurrentListFailure" @retry="guardedRetryListFailure" />
      <a-alert
        type="info"
        show-icon
        class="page-alert"
        :message="alertMessage"
      />

      <a-form layout="inline" class="filter-form">
        <a-form-item label="关键词" :html-for="filterFieldId('keyword')">
          <a-input-search
            :id="filterFieldId('keyword')"
            v-model:value="keywordInput"
            placeholder="题目 / 请求 ID / 用户 / 渠道"
            allow-clear
            style="width: 260px"
            @search="applyKeyword"
            @press-enter="applyKeyword"
          />
        </a-form-item>
        <a-form-item label="类型" :html-for="filterFieldId('type')">
          <a-select
            :id="filterFieldId('type')"
            v-model:value="filters.type"
            allow-clear
            placeholder="全部类型"
            style="width: 160px"
            :options="typeOptions"
            @change="onFilterChange"
          />
        </a-form-item>
        <a-form-item v-if="isAdmin" label="用户 ID">
          <a-input-number v-model:value="filters.userId" :min="1" placeholder="精确用户" style="width: 130px" @change="onFilterChange" />
        </a-form-item>
        <a-form-item label="请求 ID">
          <a-input v-model:value="filters.requestId" allow-clear placeholder="精确请求 ID" style="width: 220px" @press-enter="onFilterChange" @blur="onFilterChange" />
        </a-form-item>
        <a-form-item label="渠道">
          <a-input v-model:value="filters.provider" allow-clear placeholder="例如 ai" style="width: 130px" @press-enter="onFilterChange" @blur="onFilterChange" />
        </a-form-item>
        <a-form-item label="搜索模式">
          <a-select v-model:value="filters.searchMode" allow-clear placeholder="全部模式" style="width: 140px" :options="searchModeOptions" @change="onFilterChange" />
        </a-form-item>
        <a-form-item label="来源 IP">
          <a-input v-model:value="filters.clientIp" allow-clear placeholder="精确 IP" style="width: 160px" @press-enter="onFilterChange" @blur="onFilterChange" />
        </a-form-item>
        <a-form-item label="创建日期">
          <a-input v-model:value="filters.createdFrom" type="date" style="width: 155px" @change="onFilterChange" aria-label="创建日期起始" />
          <span class="filter-range-separator">至</span>
          <a-input v-model:value="filters.createdTo" type="date" style="width: 155px" @change="onFilterChange" aria-label="创建日期截止" />
        </a-form-item>
        <a-form-item>
          <a-space>
            <a-button @click="resetFilters">重置筛选</a-button>
            <a-tag v-if="activeFilterCount > 0" color="blue">已启用 {{ activeFilterCount }} 个筛选条件</a-tag>
          </a-space>
        </a-form-item>
      </a-form>

      <TableColumnSettings :model-value="selectedColumnKeys" :options="columnOptions"
        :context-key="columnScopeKey" :priority-keys="columnPriorityKeys"
        @update:model-value="setSelectedColumnKeys" @reset="resetColumns" />
      <ConsoleTableShell label="账单记录">
        <a-table
          class="desktop-billing-table"
          bordered
          :data-source="dataSource"
          :columns="visibleColumns"
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
            <template v-else-if="column.key === 'cacheHitRate'">{{ formatBillingCacheHitRate(record.cacheHitRate) }}</template>
            <template v-else>{{ text }}</template>
          </template>
          <template #emptyText>
            <a-empty v-if="!listQueryUnconfirmed" description="当前还没有账单记录。" />
            <span v-else>{{ tableLoading ? '正在加载当前查询的账单记录…' : '当前查询的账单记录尚未读取成功，请重试读取。' }}</span>
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
            <div><dt>缓存命中率</dt><dd>{{ formatBillingCacheHitRate(record.cacheHitRate) }}</dd></div>
            <div><dt>时间</dt><dd class="mobile-billing-time">{{ formatDateTime(record.createdAt) }}</dd></div>
            <div><dt>编号</dt><dd>{{ buildBillingRecordReferenceText(record) }}</dd></div>
          </dl>
          <a-button block size="small" @click="handleShowFeeDetail(record)">查看详情</a-button>
        </article>
        <template v-if="!dataSource.length">
          <a-empty v-if="!listQueryUnconfirmed" description="当前还没有账单记录" />
          <span v-else>{{ tableLoading ? '正在加载当前查询的账单记录…' : '当前查询的账单记录尚未读取成功，请重试读取。' }}</span>
        </template>
      </div>
      <div class="mobile-billing-pagination">
        <a-pagination :current="pagination.current" :page-size="pagination.pageSize" :total="pagination.total" size="small" show-less-items @change="handleMobilePageChange" />
      </div>
    </ConsolePanel>
    <BillingDetailDialog
      v-if="selectedBillingRecord"
      v-model:open="billingDetailOpen"
      :record="selectedBillingRecord"
      :viewer-scope="isAdmin ? 'admin' : 'self'"
      :can-edit-statement="isAdmin"
      :statement-saving="!!visibilitySaving[selectedBillingRecord?.id]"
      :statement-failure="statementFailure"
      :statement-failure-context="statementFailureContext"
      :statement-failure-message="statementFailureMessage"
      :is-current-statement-failure="isCurrentStatementFailure"
      :statement-reconcile-busy="statementReconciling"
      :statement-reconcile-failure="statementReconcileFailure"
      :is-current-statement-reconcile-failure="isCurrentStatementReconcileFailure"
      :statement-reconcile-hint="statementReconciliation"
      @statement-visibility-change="handleStatementVisibilityChange"
      @statement-visibility-reconcile="reconcileStatementVisibility"
    />
  </div>
</template>

<script>
import { computed, defineComponent, getCurrentInstance, reactive, ref, shallowRef, watch, markRaw, useId } from 'vue'
import { message } from 'ant-design-vue'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import TableColumnSettings from '@/components/TableColumnSettings.vue'
import { useTableColumnPreferences } from '@/composables/useTableColumnPreferences.mjs'
import OverflowTableText from '@/components/OverflowTableText.vue'
import BillingDetailDialog from '@/components/BillingDetailDialog.vue'
import { useTableExpansion } from '@/composables/useTableExpansion.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'
import { getBillingRecords, getIpLocations, getMyLocations, updateBillingStatementVisibility } from '@/api/TikuComponentApi'
import { getStoredCurrentUser, getCurrentUserLogoutEpoch, useCurrentUserDisplay, isAdminUser } from '@/utils/currentUser'
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
  buildBillingRecordListSubjectText,
  buildBillingRecordModeText,
  buildBillingRecordReferenceText,
  buildBillingRecordSourceText,
  buildBillingRecordSubjectText,
  buildBillingRecordTypeText,
  billingTableColumnWidths,
  formatBillingRecordTime,
  formatBillingCacheHitRate,
} from '@/utils/userPortalHelpers.mjs'

// Only exact public backend reasons are displayable. Keep the original response
// or Error object separately so its transport diagnostic is not lost or echoed.
function statementVisibilityFailureMessage(failure) {
  try {
    if (failure?.code === 'UNKNOWN_OUTCOME') return '网络中断，操作结果未确认。请先刷新核对，勿重复提交。'
    const status = failure?.response?.status ?? failure?.status
    const detail = failure?.response?.data?.message ?? failure?.data?.message ?? failure?.message
    const publicReasons = {
      400: ['账单参数错误', '请选择是否在账单流水显示'],
      403: ['仅管理员可修改余额或账单流水可见性', '无权限执行该操作'],
      404: ['用户或账单不存在'],
      500: ['修改失败，未保存任何变更，请稍后重试'],
    }
    return publicReasons[status]?.find(reason => reason === detail) || '账单流水可见性请求未完成，请先核对当前状态，勿重复提交。'
  } catch { return '账单流水可见性请求未完成，请先核对当前状态，勿重复提交。' }
}

function formatValueWithLocation(rawValue, address) {
  return formatValueWithLocationBase(rawValue, address)
}

// Live billing rows, summaries and queries belong to one mounted view.
function createBillingListState() {
  const dataSource = ref([])
  const tableLoading = ref(false)

  // 归属地映射：key 为归一化后的 IP，value 为展示文本。
  // 页面加载时按当前页的 clientIp 批量查询并落库（服务端持久化，之后翻页零消耗）。
  const ipLocations = reactive({})

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

  // 服务端统计覆盖完整筛选结果，缓存命中率按输入 Token 加权。
  const billingSummary = reactive({
    totalAmount: 0,
    currentPageAmount: 0,
    currentPageCacheHitRate: null,
    totalCacheHitRate: null,
  })
  // 已生效的筛选条件；keywordInput 只是输入框的即时值。
  const filters = reactive({
    keyword: '',
    type: undefined,
    userId: undefined,
    requestId: '',
    provider: '',
    searchMode: undefined,
    clientIp: '',
    createdFrom: '',
    createdTo: '',
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
  return { dataSource, tableLoading, ipLocations, billingSummary, filters, keywordInput, pagination, enrichClientIps }
}

export default defineComponent({
  name: 'BillingRecordList',
  components: { RequestFailureAlert, TableColumnSettings, OverflowTableText, BillingDetailDialog },
  setup() {
    const filterIdPrefix = 'billing-list-' + useId()
    const filterFieldId = field => filterIdPrefix + '-' + field
    const billingState = markRaw(createBillingListState())
    const { dataSource, tableLoading, ipLocations, billingSummary, filters, keywordInput, pagination } = billingState
    const subjectExpansion = useTableExpansion(dataSource)
    const selectedBillingRecord = ref(null)
    const billingDetailOpen = ref(false)
    const { currentUser } = useCurrentUserDisplay()
    const isAdmin = computed(() => isAdminUser(currentUser.value))
    const instance = getCurrentInstance()
    const closeScopePopup = () => { billingDetailOpen.value = false; selectedBillingRecord.value = null }
    const visibilitySaving = reactive({})
    const statementFailure = shallowRef(null)
    const statementFailureContext = ref(0)
    const statementFailureMessage = computed(() => statementVisibilityFailureMessage(statementFailure.value))
    const statementReconciling = ref(false)
    const statementReconcileFailure = shallowRef(null)
    const statementReconciliation = ref('')
    let statementEntry = {}, statementWriteEntry = {}, statementAttempt = 0, statementFailureTarget = null
    let statementFailureOwner = () => false
    const pendingStatementWrites = new Map()
    function clearStatementFailure() {
      statementFailure.value = null
      statementFailureTarget = null
      statementFailureOwner = () => false
      statementFailureContext.value += 1
      statementReconciling.value = false
      statementReconcileFailure.value = null
      statementReconciliation.value = ''
    }
    function retireStatementEntry() {
      statementEntry = {}
      statementAttempt += 1
      clearStatementFailure()
    }
    function retireStatementPage() {
      retireStatementEntry()
      statementWriteEntry = {}
      pendingStatementWrites.clear()
      Object.keys(visibilitySaving).forEach(key => { delete visibilitySaving[key] })
    }
    let listRequestVersion = 0
    const listFailure = shallowRef(null)
    const listFailureContext = ref(0)
    const publishedListQuery = ref(null)
    const listQueryUnconfirmed = ref(false)
    const prepareListQuery = (params) => {
      const query = JSON.stringify(params)
      if (publishedListQuery.value === null || publishedListQuery.value === query) return
      // Only retire the displayed query result. An issued visibility write and
      // an open record detail keep their independent lifetime and ownership.
      dataSource.value = []
      pagination.total = 0
      billingSummary.totalAmount = 0
      billingSummary.currentPageAmount = 0
      billingSummary.currentPageCacheHitRate = null
      billingSummary.totalCacheHitRate = null
      listQueryUnconfirmed.value = true
    }
    const publishListQuery = (params) => {
      // Bind to captured filters plus the server's effective page, not a later
      // live filter value. Keep initial/retired-owner redaction defaults intact.
      publishedListQuery.value = JSON.stringify({ ...params, page: pagination.current, pageSize: pagination.pageSize })
      listQueryUnconfirmed.value = false
    }
    const { captureDataOwner, canLoadForCurrentUser } = useCurrentUserRequestScope(currentUser, {
      clear() {
        listRequestVersion += 1
        listFailure.value = null
        listFailureContext.value += 1
        publishedListQuery.value = null
        listQueryUnconfirmed.value = false
        dataSource.value = []
        tableLoading.value = false
        pagination.total = 0
        billingSummary.totalAmount = 0
        billingSummary.currentPageAmount = 0
        billingSummary.currentPageCacheHitRate = null
        billingSummary.totalCacheHitRate = null
        Object.keys(ipLocations).forEach(key => { delete ipLocations[key] })
        subjectExpansion.clear()
        retireStatementPage()
        closeScopePopup()
      },
      reset() { filters.keyword = ''; keywordInput.value = ''; pagination.current = 1; filters.type = undefined },
      reload() { instance.proxy.handleLoad() },
    })
    function captureStatementEntry() {
      const entry = statementEntry, logout = getCurrentUserLogoutEpoch(), ownsData = captureDataOwner()
      return () => ownsData() && canLoadForCurrentUser() && isAdmin.value
        && entry === statementEntry && logout === getCurrentUserLogoutEpoch()
    }
    function beginStatementAttempt(id) {
      const ownsEntry = captureStatementEntry(), attempt = ++statementAttempt, ticket = {}
      const writeEntry = statementWriteEntry, logout = getCurrentUserLogoutEpoch(), ownsData = captureDataOwner()
      const ownsWrite = () => ownsData() && canLoadForCurrentUser() && isAdmin.value
        && writeEntry === statementWriteEntry && logout === getCurrentUserLogoutEpoch()
      clearStatementFailure()
      pendingStatementWrites.set(id, ticket)
      visibilitySaving[id] = true
      return {
        owns: () => ownsEntry() && attempt === statementAttempt,
        ownsWrite,
        settle() {
          // Neither query nor dialog changes release an issued record write;
          // an old actor/page or pending ticket cannot release a later write.
          if (ownsWrite() && pendingStatementWrites.get(id) === ticket) {
            pendingStatementWrites.delete(id)
            visibilitySaving[id] = false
          }
        },
      }
    }
    function showStatementFailure(failure, id, owns) {
      if (!owns()) return
      statementFailureTarget = id
      statementFailureOwner = owns
      statementFailure.value = ensureRequestFailure(failure)
    }
    const isCurrentStatementFailure = (failure, context) => statementFailureOwner()
      && Boolean(failure) && failure === statementFailure.value && context === statementFailureContext.value
    const isCurrentStatementReconcileFailure = (failure, context) => isCurrentStatementFailure(statementFailure.value, context)
      && Boolean(failure) && failure === statementReconcileFailure.value
    watch(() => [billingDetailOpen.value, selectedBillingRecord.value?.id], retireStatementEntry, { flush: 'sync' })
    watch(() => instance.proxy.$route?.fullPath, retireStatementPage, { flush: 'sync' })
    let observedStatementLogoutEpoch = getCurrentUserLogoutEpoch()
    watch(() => [currentUser.value, getCurrentUserLogoutEpoch()], () => {
      const logout = getCurrentUserLogoutEpoch()
      if (logout !== observedStatementLogoutEpoch) retireStatementPage()
      observedStatementLogoutEpoch = logout
    }, { flush: 'sync' })
    const reconcileStatementVisibility = computed(() => {
      const failure = statementFailure.value, context = statementFailureContext.value, target = statementFailureTarget
      const owns = captureStatementEntry()
      const isCurrent = () => owns() && isCurrentStatementFailure(failure, context)
      return async (entryFailure = failure, entryContext = context) => {
        if (entryFailure !== failure || entryContext !== context || !isCurrent()
          || visibilitySaving[target] || statementReconciling.value) return
        const currentParams = () => buildSearchParams({ keyword: filters.keyword, type: filters.type, userId: filters.userId, requestId: filters.requestId, provider: filters.provider, searchMode: filters.searchMode, clientIp: filters.clientIp, createdFrom: filters.createdFrom, createdTo: filters.createdTo }, pagination.current, pagination.pageSize)
        const params = currentParams(), query = JSON.stringify(params)
        const queryChanged = () => JSON.stringify(currentParams()) !== query
        const changedQueryHint = '筛选或分页已变化，本次读取未用于账单核对，请重新读取。'
        statementReconciling.value = true
        statementReconcileFailure.value = null
        statementReconciliation.value = ''
        try {
          // Explicit read only: no PUT, list latest-wins ticket, pagination or
          // amount replacement, and never a receipt for the uncertain write.
          const res = await getBillingRecords(params)
          if (!isCurrent()) return
          if (queryChanged()) { statementReconciliation.value = changedQueryHint; return }
          if (res?.status !== 200 || !Array.isArray(res?.data?.data)) {
            statementReconcileFailure.value = ensureRequestFailure(res)
            return
          }
          const row = res.data.data.find(item => item && String(item.id) === String(target))
          if (row && typeof row.showInStatement === 'boolean') {
            if (selectedBillingRecord.value?.id === target) selectedBillingRecord.value.showInStatement = row.showInStatement
            statementReconciliation.value = `已只读核对当前账单流水状态：${row.showInStatement ? '显示' : '隐藏'}。这不代表本次修改已生效，不能据此确认本次操作成功。`
          } else {
            statementReconciliation.value = '当前筛选和分页未返回目标账单的有效流水状态，尚未完成核对；不会据此确认本次操作成功。'
          }
        } catch (error) {
          if (!isCurrent()) return
          if (queryChanged()) statementReconciliation.value = changedQueryHint
          else statementReconcileFailure.value = ensureRequestFailure(error)
        } finally {
          if (isCurrent()) statementReconciling.value = false
        }
      }
    })
    // Identity ownership and query ownership are separate: an old page must not
    // repaint a later search (including its totals, loading state or IP enrichment).
    const isCurrentListFailure = (failure, contextKey) => canLoadForCurrentUser()
      && Boolean(failure) && failure === listFailure.value && contextKey === listFailureContext.value
    const captureListRequest = () => {
      const requestVersion = ++listRequestVersion
      listFailure.value = null
      listFailureContext.value += 1
      const ownsData = captureDataOwner()
      return () => ownsData() && requestVersion === listRequestVersion
    }
    const activeFilterCount = computed(() => countActiveFilters({ keyword: filters.keyword, type: filters.type, userId: filters.userId, requestId: filters.requestId, provider: filters.provider, searchMode: filters.searchMode, clientIp: filters.clientIp, createdFrom: filters.createdFrom, createdTo: filters.createdTo }))

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
        total: listQueryUnconfirmed.value ? '--' : Number(pagination.total || 0),
        currentPageCount: 0,
        // 金额一律用服务端口径：它能覆盖所有页，且已按请求去重、剔除充值等收入项。
        totalAmount: listQueryUnconfirmed.value ? '--' : Number(billingSummary.totalAmount || 0),
        currentPageAmount: listQueryUnconfirmed.value ? '--' : Number(billingSummary.currentPageAmount || 0),
        currentPageCacheHitRate: billingSummary.currentPageCacheHitRate,
        totalCacheHitRate: billingSummary.totalCacheHitRate,
        providerCount: 0,
      })
    })

    // 当前是否处于筛选状态：筛选会改变「累计消费金额」的口径，需要在界面上说明。
    const hasActiveFilters = computed(() => countActiveFilters({ keyword: filters.keyword, type: filters.type, userId: filters.userId, requestId: filters.requestId, provider: filters.provider, searchMode: filters.searchMode, clientIp: filters.clientIp, createdFrom: filters.createdFrom, createdTo: filters.createdTo }) > 0)

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
          title: '缓存命中率',
          dataIndex: 'cacheHitRate',
          key: 'cacheHitRate',
          width: billingTableColumnWidths.cacheHitRate,
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
    const columnPriorityKeys = ['typeLabel', 'subject', 'amount', 'source', 'createdAt', 'feeDetail']
    const { visibleColumns, selectedKeys: selectedColumnKeys, columnOptions, scopeKey: columnScopeKey,
      setSelectedKeys: setSelectedColumnKeys, resetColumns } = useTableColumnPreferences({
      pageKey: 'BillingRecordList', columns,
      scope: () => currentUser.value?.id ? `id:${currentUser.value.id}:role:${currentUser.value.role}:view:billing` : '',
      requiredKeys: ['subject', 'amount', 'feeDetail'], priorityKeys: columnPriorityKeys,
    })
    const tableScrollX = computed(() => sumTableColumnWidths(visibleColumns.value))

    return {
      filterFieldId,
      billingState,
      captureDataOwner, captureListRequest, canLoadForCurrentUser,
      listFailure, listFailureContext, isCurrentListFailure,
      publishedListQuery, listQueryUnconfirmed, prepareListQuery, publishListQuery,
      dataSource,
      // 归属地展示：映射 + 取值/格式化函数（模板按行取值）。
      ipLocations,
      normalizeLocationKey,
      formatValueWithLocation,
      tableLoading,
      visibilitySaving,
      statementFailure, statementFailureContext, statementFailureMessage,
      statementReconciling, statementReconcileFailure, statementReconciliation,
      isCurrentStatementFailure, isCurrentStatementReconcileFailure, reconcileStatementVisibility,
      beginStatementAttempt, showStatementFailure, retireStatementEntry,
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
        // 邀请返佣（task_doc/70 需求 2）：结算入赠送余额、原订单退款时冲回。
        { label: '邀请返佣', value: 'referral_rebate' },
        { label: '邀请返佣冲回', value: 'referral_rebate_reversal' },
        { label: '评价返现', value: 'review_rebate' },
        { label: '脚本预扣', value: 'script_reservation' },
        { label: '脚本退款', value: 'script_refund' },
      ],
      searchModeOptions: [
        { label: '普通搜索', value: 'normal' },
        { label: '深度思考', value: 'deep' },
        { label: '深度思考 原生工具', value: 'deep_native' },
        { label: '深度思考 MCP编排', value: 'deep_mcp' },
      ],
      pageTitle,
      pageSubtitle,
      alertMessage,
      columns, visibleColumns, selectedColumnKeys, columnOptions, columnScopeKey,
      columnPriorityKeys, setSelectedColumnKeys, resetColumns,
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
      formatBillingCacheHitRate,
    }
  },
  mounted() {
    this.handleLoad()
  },
  methods: {
    guardedRetryListFailure(failure, contextKey) {
      if (this.isCurrentListFailure(failure, contextKey)) this.handleLoad()
    },
    async handleStatementVisibilityChange({ id, showInStatement: show }) {
      const { dataSource } = this.billingState
      if (!this.canLoadForCurrentUser() || !this.isAdmin || this.visibilitySaving[id] || typeof show !== 'boolean') return false
      const record = dataSource.value.find((item) => item.id === id)
        || (this.selectedBillingRecord?.id === id ? this.selectedBillingRecord : null)
      if (!record) return false
      const attempt = this.beginStatementAttempt(id)
      const owns = attempt.owns
      try {
        const res = await updateBillingStatementVisibility(id, show)
        if (!attempt.ownsWrite()) return false
        if (res?.status !== 200) {
          if (!owns()) return false
          this.showStatementFailure(res, id, owns)
          message.error(statementVisibilityFailureMessage(res))
          return false
        }
        record.showInStatement = show
        const currentRow = dataSource.value.find((item) => item.id === id)
        if (currentRow) currentRow.showInStatement = show
        if (this.selectedBillingRecord?.id === id) this.selectedBillingRecord.showInStatement = show
        message.success(show ? '已在用户账单流水中显示' : '已从用户账单流水中隐藏，管理员明细仍保留')
        return true
      } catch (error) {
        if (!owns()) return false
        this.showStatementFailure(error, id, owns)
        message.error(statementVisibilityFailureMessage(error))
        return false
      } finally {
        attempt.settle()
      }
    },
    // 点「搜索」或回车时才把输入框内容变成生效的筛选条件。
    applyKeyword() {
      const { filters, keywordInput, pagination } = this.billingState
      filters.keyword = normalizeSearchKeyword(keywordInput.value)
      keywordInput.value = filters.keyword
      pagination.current = 1
      this.handleLoad()
    },
    onFilterChange() {
      const { pagination } = this.billingState
      pagination.current = 1
      this.handleLoad()
    },
    resetFilters() {
      const { filters, keywordInput, pagination } = this.billingState
      filters.keyword = ''
      keywordInput.value = ''
      filters.type = undefined
      filters.userId = undefined
      filters.requestId = ''
      filters.provider = ''
      filters.searchMode = undefined
      filters.clientIp = ''
      filters.createdFrom = ''
      filters.createdTo = ''
      pagination.current = 1
      this.handleLoad()
    },
    handleLoad() {
      const { dataSource, tableLoading, billingSummary, filters, pagination, enrichClientIps } = this.billingState
      if (!this.canLoadForCurrentUser()) return
      const params = buildSearchParams({ keyword: filters.keyword, type: filters.type, userId: filters.userId, requestId: filters.requestId, provider: filters.provider, searchMode: filters.searchMode, clientIp: filters.clientIp, createdFrom: filters.createdFrom, createdTo: filters.createdTo }, pagination.current, pagination.pageSize)
      this.prepareListQuery(params)
      const owns = this.captureListRequest()
      tableLoading.value = true
      getBillingRecords(params).then((res) => {
        if (!owns()) return
        if (res?.status === 200) {
          dataSource.value = Array.isArray(res?.data?.data) ? res.data.data : []
          pagination.total = Number(res?.data?.total || 0)
          pagination.current = Number(res?.data?.page || 1)
          pagination.pageSize = Number(res?.data?.pageSize || 10)
          // 金额统计由服务端给出：覆盖全部筛选结果，而非仅当前页。
          billingSummary.totalAmount = Number(res?.data?.summary?.totalAmount || 0)
          billingSummary.currentPageAmount = Number(res?.data?.summary?.currentPageAmount || 0)
          billingSummary.currentPageCacheHitRate = res?.data?.summary?.currentPageCacheHitRate ?? null
          billingSummary.totalCacheHitRate = res?.data?.summary?.totalCacheHitRate ?? null
          this.publishListQuery(params)
          // 来源 IP 归属地：按当前页批量查询并落库（失败静默）。
          enrichClientIps(dataSource.value, owns)
        } else {
          this.listFailure = ensureRequestFailure(res)
        }
      }).catch((error) => {
        if (!owns()) return
        this.listFailure = ensureRequestFailure(error)
      }).finally(() => {
        if (!owns()) return
        tableLoading.value = false
      })
    },
    handleTableChange(paginationData) {
      const { pagination } = this.billingState
      pagination.current = paginationData.current
      pagination.pageSize = paginationData.pageSize
      this.handleLoad()
    },
    handleMobilePageChange(current, pageSize) {
      const { pagination } = this.billingState
      pagination.current = current
      pagination.pageSize = pageSize
      this.handleLoad()
    },
    handleShowFeeDetail(record) {
      const { dataSource } = this.billingState
      if (!this.canLoadForCurrentUser() || !dataSource.value.includes(record)) return
      this.retireStatementEntry()
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
@media (min-width: 641px) and (max-width: 1279px) {
  .billing-page :deep(.console-summary-grid) { grid-template-columns: repeat(3, minmax(0, 1fr)); }
}
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
  .mobile-billing-card__meta > div { display: grid; grid-template-columns: 5em minmax(0, 1fr); gap: 12px; }
  .mobile-billing-card__meta dt { color: var(--console-muted); }
  .mobile-billing-card__meta dd { margin: 0; overflow-wrap: anywhere; white-space: pre-wrap; }
  .mobile-billing-time { white-space: pre-line; }
  .mobile-billing-pagination { display: flex; justify-content: center; margin-top: 16px; }
}
</style>
