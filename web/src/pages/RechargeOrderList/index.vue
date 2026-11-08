<template>
  <div ref="pageRoot" :key="renderKey" class="console-page recharge-order-page"
    @input.capture="renderHandlers.guardEvent" @paste.capture="renderHandlers.guardEvent"
    @change.capture="renderHandlers.guardEvent" @click.capture="renderHandlers.guardEvent"
    @keydown.capture="renderHandlers.guardEvent">
    <ConsolePageHeading title="充值记录" :description="isAdmin ? '查看全部充值订单、支付方式和到账状态。' : '查看自己的充值订单和到账状态。'">
      <template #actions><a-button @click="renderHandlers.load" :loading="tableLoading">刷新</a-button></template>
    </ConsolePageHeading>
    <ConsoleSummaryGrid data-recharge-private>
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

      <RequestFailureAlert v-if="loadFailure" :failure="loadFailure" :message="loadFailureMessage"
        :retryable="true" :busy="tableLoading" :context-key="queryEpoch"
        :is-current="renderHandlers.isCurrentFailure" @retry="renderHandlers.load" />

      <a-form layout="inline" class="filter-form">
        <a-form-item label="关键词" :html-for="filterFieldId('keyword')">
          <a-input-search
            :id="filterFieldId('keyword')"
            :value="keywordInput" @update:value="renderHandlers.setKeyword"
            placeholder="订单号 / 第三方单号 / 用户 / 支付方式"
            allow-clear
            style="width: 280px"
            @search="renderHandlers.applyKeyword"
            @press-enter="renderHandlers.applyKeyword"
          />
        </a-form-item>
        <a-form-item label="状态" :html-for="filterFieldId('status')">
          <a-select
            :id="filterFieldId('status')"
            :value="filters.status" @update:value="renderHandlers.setStatus"
            allow-clear
            placeholder="全部状态"
            style="width: 150px"
            :options="statusOptions"
            @change="renderHandlers.onFilterChange"
          />
        </a-form-item>
        <a-form-item label="支付渠道" :html-for="filterFieldId('provider')">
          <a-input
            :id="filterFieldId('provider')"
            :value="filters.paymentProvider" @update:value="renderHandlers.setProvider"
            placeholder="例如 epay"
            allow-clear
            style="width: 160px"
            @press-enter="renderHandlers.onFilterChange"
            @blur="renderHandlers.onFilterChange"
          />
        </a-form-item>
        <a-form-item v-if="isAdmin" label="用户 ID">
          <a-input-number :value="filters.userId" @update:value="renderHandlers.setUserId" :min="1" placeholder="精确用户" style="width: 130px" @change="renderHandlers.onFilterChange" />
        </a-form-item>
        <a-form-item label="创建日期">
          <a-input :value="filters.createdFrom" @update:value="renderHandlers.setCreatedFrom" type="date" style="width: 155px" @change="renderHandlers.onFilterChange" aria-label="创建日期起始" />
          <span class="filter-range-separator">至</span>
          <a-input :value="filters.createdTo" @update:value="renderHandlers.setCreatedTo" type="date" style="width: 155px" @change="renderHandlers.onFilterChange" aria-label="创建日期截止" />
        </a-form-item>
        <a-form-item label="付款日期">
          <a-input :value="filters.paidFrom" @update:value="renderHandlers.setPaidFrom" type="date" style="width: 155px" @change="renderHandlers.onFilterChange" aria-label="付款日期起始" />
          <span class="filter-range-separator">至</span>
          <a-input :value="filters.paidTo" @update:value="renderHandlers.setPaidTo" type="date" style="width: 155px" @change="renderHandlers.onFilterChange" aria-label="付款日期截止" />
        </a-form-item>
        <a-form-item label="金额范围">
          <a-input-number :value="filters.amountMin" @update:value="renderHandlers.setAmountMin" :min="0" :precision="2" placeholder="最低" style="width: 110px" @change="renderHandlers.onFilterChange" />
          <span class="filter-range-separator">至</span>
          <a-input-number :value="filters.amountMax" @update:value="renderHandlers.setAmountMax" :min="0" :precision="2" placeholder="最高" style="width: 110px" @change="renderHandlers.onFilterChange" />
        </a-form-item>
        <a-form-item>
          <a-space>
            <a-button @click="renderHandlers.resetFilters">重置筛选</a-button>
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
        @change="renderHandlers.handleTableChange"
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
          <a-empty v-if="hasLoaded && !tableLoading && !loadFailure" description="当前还没有充值记录。" />
          <span v-else>{{ tableLoading ? '正在加载充值记录…' : '充值记录尚未加载成功，请重试读取。' }}</span>
        </template>
      </a-table></ConsoleTableShell>
    </ConsolePanel>
  </div>
</template>

<script>
import { computed, defineComponent, getCurrentInstance, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, useId, watch } from 'vue'
import { useRouter } from 'vue-router'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import { getRechargeOrders } from '@/api/TikuComponentApi'
import { useCurrentUserDisplay, isAdminUser, getCurrentUserLogoutEpoch } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import { buildSearchParams, countActiveFilters, normalizeSearchKeyword } from '@/utils/listSearchHelpers.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

// Missing or malformed statistics are unknown, never an invented zero.
function readNonnegativeNumber(value) {
  if (!['number', 'string'].includes(typeof value) || String(value).trim() === '') return null
  const number = Number(value)
  return Number.isFinite(number) && number >= 0 ? number : null
}
const orderFields = ['id', 'userId', 'username', 'orderNo', 'paymentProvider', 'paymentMethodNameSnapshot', 'amount', 'status', 'thirdPartyOrderNo', 'paidAt', 'createdAt', 'updatedAt']

export default defineComponent({
  name: 'RechargeOrderList',
  components: { RequestFailureAlert },
  setup() {
    const filterIdPrefix = 'recharge-orders-' + useId()
    const filterFieldId = field => filterIdPrefix + '-' + field
    const dataSource = ref([]), tableLoading = ref(false), hasLoaded = ref(false)
    const loadFailure = shallowRef(null), summaryIncomplete = ref(false)
    // 服务端统计：totalAmount 覆盖全部筛选结果，currentPageAmount 仅当前页。
    const amountSummary = reactive({ totalAmount: null, currentPageAmount: null })
    // keywordInput 是草稿；只有搜索后 keyword 才进入 GET 参数。
    const filters = reactive({ keyword: '', status: undefined, paymentProvider: '',
    userId: undefined,
    createdFrom: '',
    createdTo: '',
    paidFrom: '',
    paidTo: '',
    amountMin: undefined,
    amountMax: undefined,
  })
    const keywordInput = ref('')
    const pagination = reactive({
      current: 1, pageSize: 10, total: 0, showSizeChanger: true, showQuickJumper: true,
      showTotal: total => `共 ${total} 条`, pageSizeOptions: ['10', '20', '50', '100'],
    })
    const { currentUser } = useCurrentUserDisplay()
    const isAdmin = computed(() => isAdminUser(currentUser.value))
    const instance = getCurrentInstance()
    const router = useRouter()
    const route = () => router?.currentRoute?.value || instance?.proxy?.$route || {}
    const routeKey = () => JSON.stringify([route().fullPath, route().path, route().query, route().params, route().hash])
    const mountedPath = route().path
    const principal = user => JSON.stringify([currentUserIdentity(user), String(user?.role || '').trim().toLowerCase(), String(user?.status || '').trim().toLowerCase()])
    const pageRoot = ref(null), renderKey = ref(0), queryEpoch = ref(0)
    let alive = true, mounted = false, renderEpoch = 0, loadedQuery = ''
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()
    let observedLogoutEpoch = mountedLogoutEpoch
    const pageActive = () => alive && route().path === mountedPath
    // Keep the original id/username availability rule; server authorization is authoritative.
    // Explicit logout retires this mount even if cache removal was denied and A
    // is still readable. Unknown storage alone is not a logout; login remounts.
    const canLoadForCurrentUser = () => pageActive() && getCurrentUserLogoutEpoch() === mountedLogoutEpoch && Boolean(currentUserIdentity(currentUser.value))
    const clearData = () => {
      dataSource.value = []; pagination.total = 0; hasLoaded.value = false
      amountSummary.totalAmount = null; amountSummary.currentPageAmount = null
      summaryIncomplete.value = false; loadedQuery = ''
    }
    const resetInputs = () => {
      filters.keyword = ''; keywordInput.value = ''; filters.status = undefined; filters.paymentProvider = ''; filters.userId = undefined; filters.createdFrom = ''; filters.createdTo = ''; filters.paidFrom = ''; filters.paidTo = ''; filters.amountMin = undefined; filters.amountMax = undefined; pagination.current = 1
    }
    const scrubPrivateDom = () => {
      pageRoot.value?.querySelectorAll('input, textarea').forEach(input => {
        input.value = ''; input.defaultValue = ''; input.removeAttribute('value')
      })
      // Preserve native/Vue nodes while synchronously retiring private text.
      pageRoot.value?.querySelectorAll('[data-recharge-private], .ant-table-tbody').forEach(node => {
        const texts = node.ownerDocument.createTreeWalker(node, 4)
        while (texts.nextNode()) texts.currentNode.nodeValue = ''
      })
    }
    const retireView = () => {
      const epoch = ++renderEpoch
      queryEpoch.value += 1; tableLoading.value = false; loadFailure.value = null
      clearData(); resetInputs(); scrubPrivateDom()
      // Drain Antd's accepted controlled input work before disposing its refs.
      nextTick().then(() => nextTick()).then(() => {
        if (!alive || epoch !== renderEpoch) return
        renderKey.value = epoch
        if (mounted && canLoadForCurrentUser()) void handleLoad()
      })
    }
    const handleLoad = async () => {
      if (!canLoadForCurrentUser() || renderKey.value !== renderEpoch) return
      const params = buildSearchParams({ keyword: filters.keyword, status: filters.status, paymentProvider: filters.paymentProvider, userId: filters.userId, createdFrom: filters.createdFrom, createdTo: filters.createdTo, paidFrom: filters.paidFrom, paidTo: filters.paidTo, amountMin: filters.amountMin, amountMax: filters.amountMax }, pagination.current, pagination.pageSize)
      const fingerprint = JSON.stringify(params), sequence = ++queryEpoch.value
      const actor = principal(currentUser.value), location = routeKey(), epoch = renderEpoch
      const logout = getCurrentUserLogoutEpoch()
      const owns = () => pageActive() && epoch === renderEpoch && sequence === queryEpoch.value &&
        actor === principal(currentUser.value) && location === routeKey() && logout === getCurrentUserLogoutEpoch()
      if (loadedQuery !== fingerprint) clearData()
      loadFailure.value = null; tableLoading.value = true
      try {
        const res = await getRechargeOrders(params)
        if (!owns()) return
        const body = res?.data, total = readNonnegativeNumber(body?.total)
        if (res?.status !== 200 || !Array.isArray(body?.data) || total === null || !Number.isInteger(total)) {
          loadFailure.value = ensureRequestFailure(res); return
        }
        const rows = body.data
        // A legacy response without owner metadata is not proof of a foreign row.
        // Explicit foreign ownership must not leak rows OR its aggregate amounts.
        if (rows.some(row => !row || typeof row !== 'object' || Array.isArray(row) ||
          (!isAdmin.value && currentUser.value?.id != null && row.userId != null && String(row.userId) !== String(currentUser.value.id)))) {
          clearData(); loadFailure.value = Object.freeze({ code: 'MALFORMED_RESPONSE' }); return
        }
        dataSource.value = rows.map(row => Object.fromEntries(orderFields.filter(key => Object.hasOwn(row, key)).map(key => [key, row[key]])))
        pagination.total = total
        const page = readNonnegativeNumber(body.page), pageSize = readNonnegativeNumber(body.pageSize)
        pagination.current = Number.isInteger(page) && page > 0 ? page : params.page
        pagination.pageSize = Number.isInteger(pageSize) && pageSize > 0 ? pageSize : params.pageSize
        amountSummary.totalAmount = readNonnegativeNumber(body.summary?.totalAmount)
        amountSummary.currentPageAmount = readNonnegativeNumber(body.summary?.currentPageAmount)
        summaryIncomplete.value = amountSummary.totalAmount === null || amountSummary.currentPageAmount === null
        hasLoaded.value = true; loadedQuery = fingerprint
        if (summaryIncomplete.value) loadFailure.value = Object.freeze({ code: 'MALFORMED_RESPONSE' })
      } catch (error) {
        if (owns()) loadFailure.value = ensureRequestFailure(error)
      } finally {
        if (owns()) tableLoading.value = false
      }
    }
    const applyKeyword = () => {
      filters.keyword = normalizeSearchKeyword(keywordInput.value); keywordInput.value = filters.keyword
      pagination.current = 1; return handleLoad()
    }
    const onFilterChange = () => { pagination.current = 1; return handleLoad() }
    const resetFilters = () => { resetInputs(); return handleLoad() }
    const handleTableChange = value => { pagination.current = value.current; pagination.pageSize = value.pageSize; return handleLoad() }
    const renderHandlers = computed(() => {
      const epoch = renderKey.value, query = queryEpoch.value
      const actor = principal(currentUser.value), location = routeKey(), logout = getCurrentUserLogoutEpoch()
      const current = () => canLoadForCurrentUser() && epoch === renderEpoch && query === queryEpoch.value &&
        actor === principal(currentUser.value) && location === routeKey() && logout === getCurrentUserLogoutEpoch()
      const run = action => (...args) => { if (current()) return action(...args) }
      return {
        guardEvent(event) { if (!current()) { event.preventDefault(); event.stopImmediatePropagation() } },
        load: run(handleLoad), applyKeyword: run(applyKeyword), onFilterChange: run(onFilterChange),
        resetFilters: run(resetFilters), handleTableChange: run(handleTableChange),
        setKeyword: run(value => { keywordInput.value = value }), setStatus: run(value => { filters.status = value }),
        setProvider: run(value => { filters.paymentProvider = value }),
        setUserId: run(value => { filters.userId = value }),
        setCreatedFrom: run(value => { filters.createdFrom = value }),
        setCreatedTo: run(value => { filters.createdTo = value }),
        setPaidFrom: run(value => { filters.paidFrom = value }),
        setPaidTo: run(value => { filters.paidTo = value }),
        setAmountMin: run(value => { filters.amountMin = value }),
        setAmountMax: run(value => { filters.amountMax = value }),
        isCurrentFailure: (failure, context) => current() && failure === loadFailure.value && context === queryEpoch.value,
      }
    })
    watch(() => [currentUser.value, getCurrentUserLogoutEpoch()], ([user], [previous]) => {
      const logout = getCurrentUserLogoutEpoch()
      const changed = principal(user) !== principal(previous) || logout !== observedLogoutEpoch
      observedLogoutEpoch = logout
      if (changed) retireView()
    }, { flush: 'sync' })
    watch(routeKey, retireView, { flush: 'sync' })
    onMounted(() => { mounted = true; void handleLoad() })
    onBeforeUnmount(() => { alive = false; mounted = false; retireView() })
    const summary = computed(() => ({
      total: hasLoaded.value ? pagination.total : '--',
      currentPageCount: hasLoaded.value ? dataSource.value.length : '--',
      totalAmount: amountSummary.totalAmount ?? '--',
      currentPageAmount: amountSummary.currentPageAmount ?? '--',
    }))

    return {
      filterFieldId,
      canLoadForCurrentUser, pageRoot, renderKey, queryEpoch, renderHandlers, hasLoaded, loadFailure,
      handleLoad, applyKeyword, onFilterChange, resetFilters, handleTableChange,
      loadFailureMessage: computed(() => summaryIncomplete.value
        ? '充值记录已读取，但金额统计尚未确认；请重试读取，此处不会创建订单或支付。'
        : hasLoaded.value
        ? '保留上次已读取结果，当前刷新未成功；请重试读取，此处不会创建订单或支付。'
        : '充值记录未加载成功，请核对当前筛选条件后重试读取；此处不会创建订单或支付。'),
      dataSource,
      tableLoading,
      pagination,
      summary,
      isAdmin,
      filters,
      keywordInput,
      sumTableColumnWidths,
      activeFilterCount: computed(() => countActiveFilters({ keyword: filters.keyword, status: filters.status, paymentProvider: filters.paymentProvider, userId: filters.userId, createdFrom: filters.createdFrom, createdTo: filters.createdTo, paidFrom: filters.paidFrom, paidTo: filters.paidTo, amountMin: filters.amountMin, amountMax: filters.amountMax })),
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
  methods: {
    formatAmount(value) {
      // 充值金额固定 2 位小数（支付渠道只支持「分」），与余额的 6 位精度区分开。
      const amount = readNonnegativeNumber(value)
      return amount !== null ? amount.toFixed(2) : '--'
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
