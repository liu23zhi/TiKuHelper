<template>
  <div class="console-page referral-rebates-page">
    <ConsolePageHeading title="返佣记录" description="邀请返佣账本。结算会把金额计入受益人的赠送余额（可消费、不可退款），可退本金不受影响。">
      <template #actions>
        <a-button :loading="loading" :disabled="!canManage() || loading || settling" @click="load">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert
      v-if="!canManage()"
      type="warning"
      show-icon
      class="page-alert"
      message="当前身份无返佣记录管理权限，请使用管理员账号登录。"
    />

    <template v-if="canManage()">
      <ConsoleSummaryGrid class="rebate-summary">
        <div>
          <div class="summary-label">筛选结果</div>
          <div class="summary-value">{{ hasLoaded ? total : '--' }} 条</div>
        </div>
        <div>
          <div class="summary-label">待结算合计</div>
          <div class="summary-value">{{ moneyText(pendingAmount) }}</div>
        </div>
        <div>
          <div class="summary-label">已结算合计</div>
          <div class="summary-value">{{ moneyText(settledAmount) }}</div>
        </div>
      </ConsoleSummaryGrid>

      <ConsolePanel title="筛选" description="状态精确匹配；受益人 / 受邀人是账户 ID；订单号精确匹配，不做模糊搜索。">
        <a-form layout="inline" class="filter-form" :onFinish="reload">
          <a-form-item label="状态" :html-for="fieldId('status')">
            <a-select
              :id="fieldId('status')"
              :value="filters.status"
              :options="statusOptions"
              allow-clear
              placeholder="全部状态"
              class="filter-status"
              :disabled="loading || settling"
              @update:value="value => setFilter('status', value)"
              @change="reload"
            />
          </a-form-item>
          <a-form-item label="受益人 ID" :html-for="fieldId('inviterUserId')">
            <a-input
              :id="fieldId('inviterUserId')"
              :value="filters.inviterUserId"
              inputmode="numeric"
              allow-clear
              placeholder="精确账户 ID"
              class="filter-id"
              :disabled="loading || settling"
              @update:value="value => setFilter('inviterUserId', value)"
              @press-enter="reload"
            />
          </a-form-item>
          <a-form-item label="订单号" :html-for="fieldId('orderNo')">
            <a-input
              :id="fieldId('orderNo')"
              :value="filters.orderNo"
              allow-clear
              placeholder="原充值订单号"
              class="filter-order"
              :disabled="loading || settling"
              @update:value="value => setFilter('orderNo', value)"
              @press-enter="reload"
            />
          </a-form-item>
          <a-form-item>
            <a-space wrap>
              <a-button type="primary" html-type="submit" :loading="loading" :disabled="loading || settling">查询</a-button>
              <a-button :disabled="loading || settling" @click="resetFilters">重置筛选</a-button>
            </a-space>
          </a-form-item>
        </a-form>

        <p v-if="filterError" class="filter-error" role="alert">{{ filterError }}</p>
        <RequestFailureAlert
          v-if="loadFailure"
          :key="failureContextKey"
          :failure="loadFailure"
          :message="failureMessage"
          :retryable="true"
          :busy="loading || settling"
          :context-key="failureContextKey"
          :is-current="isCurrentFailure"
          @retry="load"
        />
        <a-alert v-else-if="actionWarning" type="warning" show-icon :message="actionWarning" class="status-alert" />
      </ConsolePanel>

      <ConsolePanel title="返佣列表" :description="scopeDescription">
        <ConsoleTableShell label="返佣记录">
          <a-table
            table-layout="fixed"
            :data-source="rows"
            :columns="columns"
            :loading="loading"
            :pagination="pagination"
            row-key="id"
            :scroll="{ x: sumTableColumnWidths(columns) }"
            :locale="{ emptyText: emptyText }"
            @change="onTableChange"
          >
            <template #bodyCell="{ column, record }">
              <span v-if="column.key === 'id'">{{ record.id ?? '-' }}</span>
              <span v-else-if="column.key === 'inviter'">{{ buildUserName(record.inviterUsername, record.inviterUserId) }}</span>
              <span v-else-if="column.key === 'invited'">{{ buildUserName(record.invitedUsername, record.invitedUserId) }}</span>
              <span v-else-if="column.key === 'orderNo'" class="order-cell">{{ record.orderNo || '—' }}</span>
              <span v-else-if="column.key === 'baseAmount'" class="money-cell">{{ moneyText(record.baseAmount) }}</span>
              <span v-else-if="column.key === 'rate'">{{ formatRate(record.rate) }}</span>
              <span v-else-if="column.key === 'amount'" class="money-cell">{{ moneyText(record.amount) }}</span>
              <a-tag v-else-if="column.key === 'status'" :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
              <span v-else-if="column.key === 'createdAt'" class="time-cell">{{ formatDateTime(record.createdAt) }}</span>
              <span v-else-if="column.key === 'settledAt'" class="time-cell">{{ formatDateTime(record.settledAt) }}</span>
              <template v-else-if="column.key === 'action'">
                <a-button
                  v-if="isPending(record)"
                  type="link"
                  :disabled="loading || settling || !!loadFailure"
                  @click="confirmSettle(record)"
                >结算</a-button>
                <span v-else class="action-disabled">{{ record.status === 'settled' ? '已结算' : '不可结算' }}</span>
              </template>
            </template>
          </a-table>
        </ConsoleTableShell>
      </ConsolePanel>
    </template>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, reactive, ref, shallowRef, useId, watch } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import Swal from 'sweetalert2'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import ConsoleTableShell from '@/components/ConsoleTableShell.vue'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import { getReferralRebates, settleReferralRebate } from '@/api/ReferralApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

const STATUS_LABELS = { pending: '待结算', settled: '已结算', reversed: '已冲回' }
const STATUS_COLORS = { pending: 'gold', settled: 'green', reversed: 'default' }

function readNonnegativeNumber(value) {
  if (!['number', 'string'].includes(typeof value) || String(value).trim() === '') return null
  const number = Number(value)
  return Number.isFinite(number) && number >= 0 ? number : null
}
function readPositiveInt(value) {
  const number = readNonnegativeNumber(value)
  return Number.isInteger(number) && number > 0 ? number : null
}

const emptyFilters = () => ({ status: undefined, inviterUserId: '', orderNo: '' })

export default {
  name: 'ReferralRebatesPage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell, RequestFailureAlert },
  setup() {
    const route = useRoute()
    const prefix = 'referral-rebates-' + useId()
    const fieldId = field => prefix + '-' + field
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const rows = ref([])
    const filters = reactive(emptyFilters())
    const appliedFilters = ref({})
    const loading = ref(false)
    const settling = ref(false)
    const hasLoaded = ref(false)
    const total = ref(0)
    const amounts = reactive({ pending: null, settled: null })
    const filterError = ref('')
    const actionWarning = ref('')
    const loadFailure = shallowRef(null)
    const failureContextKey = ref(0)
    let failureOwner = () => false
    const pagination = reactive({
      current: 1, pageSize: 20, total: 0,
      showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'],
      showTotal: value => `共 ${value} 条`,
    })

    let alive = true
    let mounted = false
    let readVersion = 0
    let writeVersion = 0
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()
    let observedLogout = mountedLogoutEpoch

    const validAdmin = () => {
      const user = currentUser.value
      const id = Number(user?.id)
      return Number.isSafeInteger(id) && id > 0 && id <= 2147483647 && isAdminUser(user)
        && !['disabled', '0'].includes(String(user?.status ?? 'enabled'))
    }
    // 与其它管理页一致：身份、角色、登出任一变化都不再允许操作。
    const canManage = () => alive && route.path === mountedPath
      && getCurrentUserLogoutEpoch() === mountedLogoutEpoch && validAdmin()

    const pendingAmount = computed(() => amounts.pending)
    const settledAmount = computed(() => amounts.settled)

    const columns = [
      { title: 'ID', dataIndex: 'id', key: 'id', width: 90 },
      { title: '受益人', dataIndex: 'inviter', key: 'inviter', width: 180 },
      { title: '受邀人', dataIndex: 'invited', key: 'invited', width: 180 },
      { title: '订单号', dataIndex: 'orderNo', key: 'orderNo', width: 230 },
      { title: '基数', dataIndex: 'baseAmount', key: 'baseAmount', width: 140 },
      { title: '比例', dataIndex: 'rate', key: 'rate', width: 100 },
      { title: '金额', dataIndex: 'amount', key: 'amount', width: 140 },
      { title: '状态', dataIndex: 'status', key: 'status', width: 110 },
      { title: '创建时间', dataIndex: 'createdAt', key: 'createdAt', width: 175 },
      { title: '结算时间', dataIndex: 'settledAt', key: 'settledAt', width: 175 },
      { title: '操作', key: 'action', width: 110 },
    ]

    const statusOptions = [
      { label: '待结算', value: 'pending' },
      { label: '已结算', value: 'settled' },
      { label: '已冲回', value: 'reversed' },
    ]

    const scopeDescription = computed(() => {
      const scope = appliedFilters.value
      const parts = [
        scope.status && `状态：${statusLabel(scope.status)}`,
        scope.inviterUserId && `受益人 ID：${scope.inviterUserId}`,
        scope.orderNo && `订单号：${scope.orderNo}`,
      ].filter(Boolean)
      return parts.length ? `当前筛选：${parts.join('；')}。` : '当前范围：全部返佣记录。'
    })

    const emptyText = computed(() => (loadFailure.value
      ? '列表加载失败，请重试'
      : hasLoaded.value ? '暂无符合条件的返佣记录' : '返佣记录尚未读取成功'))

    function statusLabel(value) {
      const normalized = String(value || '').trim().toLowerCase()
      return STATUS_LABELS[normalized] || value || '-'
    }
    function statusColor(value) {
      const normalized = String(value || '').trim().toLowerCase()
      return STATUS_COLORS[normalized] || 'blue'
    }
    function isPending(record) {
      return String(record?.status || '').trim().toLowerCase() === 'pending'
    }
    function buildUserName(username, userId) {
      const name = String(username || '').trim()
      const id = readPositiveInt(userId)
      if (name && id) return `${name}（#${id}）`
      if (name) return name
      if (id) return `#${id}`
      return '—'
    }
    // 与 UserUsage 一致：金额最多 6 位小数、去尾零（后端 util.MoneyPrecision = 6）。
    // 未知金额显示 `--`，不拼成「¥ --」这种半成品。
    function moneyText(value) {
      const amount = readNonnegativeNumber(value)
      return amount === null ? '--' : `¥ ${formatMoneyValue(amount)}`
    }
    // 返佣比例是 0–1 分数，展示成百分数并去尾零。
    function formatRate(value) {
      const rate = readNonnegativeNumber(value)
      if (rate === null) return '--'
      const percent = Number((rate * 100).toFixed(4))
      return `${String(percent).replace(/(\.\d*?)0+$/, '$1').replace(/\.$/, '')}%`
    }
    function formatDateTime(value) {
      if (!value) return '—'
      const date = new Date(value)
      if (!Number.isFinite(date.getTime())) return '—'
      return date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
    }
    function setFilter(field, value) {
      if (!canManage() || loading.value || settling.value || !Object.hasOwn(filters, field)) return
      filters[field] = value
    }

    const clearLoadFailure = () => {
      loadFailure.value = null
      failureOwner = () => false
      failureContextKey.value += 1
    }
    const isCurrentFailure = (source, context) => failureOwner() && Boolean(source)
      && source === loadFailure.value && context === failureContextKey.value

    const buildParams = () => {
      const params = { page: pagination.current, pageSize: pagination.pageSize }
      const status = String(appliedFilters.value.status || '').trim()
      if (status) params.status = status
      const inviter = String(appliedFilters.value.inviterUserId || '').trim()
      if (inviter) params.inviterUserId = inviter
      const orderNo = String(appliedFilters.value.orderNo || '').trim()
      if (orderNo) params.orderNo = orderNo
      return params
    }

    const load = async () => {
      if (!canManage() || settling.value) return false
      const ticket = ++readVersion
      const owns = () => canManage() && ticket === readVersion
      clearLoadFailure()
      const context = failureContextKey.value
      const ownsFailure = () => owns() && context === failureContextKey.value
      loading.value = true
      try {
        const res = await getReferralRebates(buildParams())
        if (!ownsFailure()) return false
        const body = res?.data
        const items = body?.data
        const count = readNonnegativeNumber(body?.total)
        if (res?.status !== 200 || !Array.isArray(items) || count === null || !Number.isInteger(count)) {
          failureOwner = ownsFailure
          loadFailure.value = ensureRequestFailure(res)
          return false
        }
        rows.value = items.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
        total.value = count
        pagination.total = count
        amounts.pending = readNonnegativeNumber(body?.summary?.pendingAmount)
        amounts.settled = readNonnegativeNumber(body?.summary?.settledAmount)
        const page = readPositiveInt(body?.page)
        const size = readPositiveInt(body?.pageSize)
        if (page) pagination.current = page
        if (size) pagination.pageSize = size
        hasLoaded.value = true
        filterError.value = ''
        return true
      } catch (error) {
        if (ownsFailure()) { failureOwner = ownsFailure; loadFailure.value = ensureRequestFailure(error) }
        return false
      } finally {
        if (owns()) loading.value = false
      }
    }

    const reload = () => {
      if (!canManage() || loading.value || settling.value) return
      const inviter = String(filters.inviterUserId || '').trim()
      if (inviter && !/^[0-9]+$/.test(inviter)) { filterError.value = '受益人 ID 只能是正整数'; return }
      if (inviter && !readPositiveInt(inviter)) { filterError.value = '受益人 ID 必须大于 0'; return }
      filterError.value = ''
      actionWarning.value = ''
      appliedFilters.value = {
        status: filters.status || '',
        inviterUserId: inviter,
        orderNo: String(filters.orderNo || '').trim(),
      }
      pagination.current = 1
      return load()
    }

    const resetFilters = () => {
      if (!canManage() || loading.value || settling.value) return
      Object.assign(filters, emptyFilters())
      appliedFilters.value = {}
      filterError.value = ''
      pagination.current = 1
      return load()
    }

    const onTableChange = page => {
      if (!canManage() || loading.value || settling.value) return
      pagination.current = page?.pageSize === pagination.pageSize ? Number(page.current) || 1 : 1
      pagination.pageSize = Number(page?.pageSize) || pagination.pageSize
      return load()
    }

    const confirmSettle = async record => {
      if (!canManage() || loading.value || settling.value || !isPending(record)) return
      const owns = () => canManage()
      const result = await Swal.fire({
        title: '确认结算这条返佣？',
        text: `受益人 ${buildUserName(record.inviterUsername, record.inviterUserId)}，金额 ${moneyText(record.amount)}。结算会把该金额计入受益人的赠送余额（可消费、不可退款），可退本金不变，且不可撤销。`,
        icon: 'warning',
        showCancelButton: true,
        confirmButtonText: '确认结算',
        cancelButtonText: '取消',
      })
      if (!result.isConfirmed || !owns()) return
      return settle(record)
    }

    const settle = async record => {
      const id = readPositiveInt(record?.id)
      if (!id || !canManage()) return
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      clearLoadFailure()
      const context = failureContextKey.value
      const ownsFailure = () => owns() && context === failureContextKey.value
      settling.value = true
      actionWarning.value = ''
      let reloadAfter = false
      try {
        const res = await settleReferralRebate(id)
        if (!ownsFailure()) return
        if (res?.status === 409) {
          actionWarning.value = '该记录状态已变更，请刷新核对后再操作。'
          return
        }
        if (res?.status !== 200) {
          failureOwner = ownsFailure
          loadFailure.value = ensureRequestFailure(res)
          return
        }
        message.success('返佣已结算')
        reloadAfter = true
      } catch (error) {
        if (ownsFailure()) { failureOwner = ownsFailure; loadFailure.value = ensureRequestFailure(error) }
      } finally {
        if (owns()) settling.value = false
      }
      // load() 会拒绝在 settling 期间执行，因此必须等锁释放后再刷新。
      if (reloadAfter && owns()) await load()
    }

    const failureMessage = computed(() => (hasLoaded.value
      ? '返佣列表刷新失败，页面显示的仍是上次读取的结果。'
      : '返佣列表尚未加载成功，请重试读取。'))

    const resetDraft = () => {
      Object.assign(filters, emptyFilters())
      appliedFilters.value = {}
      filterError.value = ''
    }

    const retire = () => {
      readVersion += 1; writeVersion += 1
      pagination.current = 1
    }
    watch(() => [currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status], () => {
      retire()
      rows.value = []; total.value = 0; pagination.total = 0; hasLoaded.value = false
      amounts.pending = null; amounts.settled = null; actionWarning.value = ''
      resetDraft()
      if (mounted && canManage()) void load()
    }, { flush: 'sync' })
    watch(() => getCurrentUserLogoutEpoch(), value => {
      if (value === observedLogout) return
      observedLogout = value
      retire()
      rows.value = []; total.value = 0; pagination.total = 0; hasLoaded.value = false
      amounts.pending = null; amounts.settled = null; clearLoadFailure()
    }, { flush: 'sync' })
    watch(() => route.path, () => {
      retire()
      rows.value = []; total.value = 0; pagination.total = 0; hasLoaded.value = false
      amounts.pending = null; amounts.settled = null; actionWarning.value = ''
      clearLoadFailure()
    }, { flush: 'sync' })
    onMounted(() => {
      mounted = true
      if (canManage()) void load()
    })
    onBeforeUnmount(() => {
      alive = false; mounted = false
      readVersion += 1; writeVersion += 1
      clearLoadFailure()
    })

    return {
      fieldId, canManage, columns, rows, filters, appliedFilters, loading, settling,
      hasLoaded, total, pendingAmount, settledAmount, filterError, actionWarning,
      loadFailure, failureContextKey, failureMessage, isCurrentFailure,
      pagination, statusOptions, scopeDescription, emptyText,
      statusLabel, statusColor, isPending, buildUserName, moneyText, formatRate,
      formatDateTime, setFilter, load, reload, resetFilters, onTableChange, confirmSettle,
      sumTableColumnWidths,
    }
  },
}
</script>

<style scoped>
.page-alert, .filter-form, .status-alert { margin-bottom: 16px; }
.rebate-summary { margin-bottom: 16px; }
.summary-label { color: var(--console-muted); font-size: 14px; }
.summary-value { margin-top: 8px; color: var(--console-text); font-size: clamp(20px, 2.6vw, 28px); font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.filter-status { width: 150px; }
.filter-id, .filter-order { width: 200px; }
.filter-error { margin: 0 0 16px; color: var(--console-danger, #c13e41); font-size: 13px; }
.money-cell, .time-cell { font-variant-numeric: tabular-nums; }
.order-cell { overflow-wrap: anywhere; }
.action-disabled { color: var(--console-muted); font-size: 13px; }
@media (max-width: 640px) {
  .filter-form :deep(.ant-form-item) { width: 100%; margin-inline-end: 0; }
  .filter-status, .filter-id, .filter-order { width: 100%; }
}
</style>
