<template>
  <div class="console-page ip-segment-page">
    <ConsolePageHeading
      title="IP 段归属管理"
      description="上游一次查询会返回 beginip/endip，说明一次查询覆盖一整段。这里维护那些段：段内的后续 IP 直接命中段表，不再重复外呼上游。"
    >
      <template #actions>
        <a-button :loading="loading" :disabled="!canManage() || busy()" @click="load">刷新列表</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert
      v-if="!canManage()"
      type="warning"
      show-icon
      class="page-alert"
      message="当前身份无 IP 段管理权限，请使用管理员账号登录。"
    />

    <template v-if="canManage()">
      <ConsoleSummaryGrid class="segment-summary">
        <div>
          <div class="summary-label">段数量</div>
          <div class="summary-value">{{ hasLoaded ? segmentCount : '--' }}</div>
        </div>
        <div>
          <div class="summary-label">覆盖地址数（上界）</div>
          <div class="summary-value">{{ hasLoaded ? coveredText : '--' }}</div>
        </div>
        <div>
          <div class="summary-label">不同 ASN</div>
          <div class="summary-value">{{ hasLoaded ? asnCount : '--' }}</div>
        </div>
        <div>
          <div class="summary-label">最近刷新</div>
          <div class="summary-value summary-value--time">{{ hasLoaded ? formatDateTime(latestFetchedAt) : '--' }}</div>
        </div>
      </ConsoleSummaryGrid>

      <ConsolePanel title="按 IP 查段" description="IPv4 专用。未命中不是错误：段表里没有这条地址的段归属时，会说明原因（尚未查询过 / 未被上游返回）。">
        <a-form layout="inline" class="lookup-form" :onFinish="runLookup">
          <a-form-item label="IP 地址" :html-for="fieldId('lookup-ip')">
            <a-input
              :id="fieldId('lookup-ip')"
              :value="lookupInput"
              allow-clear
              placeholder="例如 120.239.173.110"
              class="lookup-input"
              :disabled="busy() || lookup.loading"
              @update:value="value => (lookupInput = value)"
              @press-enter="runLookup"
            />
          </a-form-item>
          <a-form-item>
            <a-button type="primary" html-type="submit" :loading="lookup.loading" :disabled="busy() || lookup.loading">查询归属段</a-button>
          </a-form-item>
        </a-form>
        <p v-if="lookup.error" class="filter-error" role="alert">{{ lookup.error }}</p>
        <a-descriptions v-if="lookup.result" bordered size="small" :column="2" class="lookup-result">
          <a-descriptions-item label="查询 IP">{{ lookup.result.ip || '-' }}</a-descriptions-item>
          <a-descriptions-item label="是否命中">
            <a-tag :color="lookup.result.matched ? 'green' : 'default'">{{ lookup.result.matched ? '命中' : '未命中' }}</a-tag>
          </a-descriptions-item>
          <template v-if="lookup.result.matched && lookup.result.segment">
            <a-descriptions-item label="段范围">{{ formatSegmentRange(lookup.result.segment) }}</a-descriptions-item>
            <a-descriptions-item label="归属地">{{ lookup.result.segment.region || '—' }}</a-descriptions-item>
            <a-descriptions-item label="运营商">{{ operatorText(lookup.result.segment) }}</a-descriptions-item>
            <a-descriptions-item label="ASN">{{ lookup.result.segment.asn || '—' }}</a-descriptions-item>
            <a-descriptions-item label="来源">{{ lookup.result.segment.source || '—' }}</a-descriptions-item>
            <a-descriptions-item label="刷新时间">{{ formatDateTime(lookup.result.segment.fetchedAt) }}</a-descriptions-item>
          </template>
          <a-descriptions-item v-else label="未命中原因" :span="2">{{ lookup.result.reason || '该 IP 暂无段归属记录' }}</a-descriptions-item>
        </a-descriptions>
      </ConsolePanel>

      <ConsolePanel title="筛选" description="关键字匹配归属地 / 运营商 / ASN / 段文本；ASN、运营商简称、来源为精确匹配。批量删除只删当前筛选范围，必须至少带一个条件。">
        <a-form layout="inline" class="filter-form" :onFinish="reload">
          <a-form-item label="关键字" :html-for="fieldId('keyword')">
            <a-input
              :id="fieldId('keyword')"
              :value="filters.keyword"
              allow-clear
              placeholder="归属地 / 运营商 / ASN / 段"
              class="filter-keyword"
              :disabled="busy()"
              @update:value="value => setFilter('keyword', value)"
              @press-enter="reload"
            />
          </a-form-item>
          <a-form-item label="ASN" :html-for="fieldId('asn')">
            <a-input
              :id="fieldId('asn')"
              :value="filters.asn"
              allow-clear
              placeholder="例如 AS9808"
              class="filter-narrow"
              :disabled="busy()"
              @update:value="value => setFilter('asn', value)"
              @press-enter="reload"
            />
          </a-form-item>
          <a-form-item label="运营商简称" :html-for="fieldId('llc')">
            <a-input
              :id="fieldId('llc')"
              :value="filters.llc"
              allow-clear
              placeholder="例如 移动"
              class="filter-narrow"
              :disabled="busy()"
              @update:value="value => setFilter('llc', value)"
              @press-enter="reload"
            />
          </a-form-item>
          <a-form-item label="来源" :html-for="fieldId('source')">
            <a-select
              :id="fieldId('source')"
              :value="filters.source"
              :options="sourceOptions"
              allow-clear
              placeholder="全部来源"
              class="filter-narrow"
              :disabled="busy()"
              @update:value="value => setFilter('source', value)"
              @change="reload"
            />
          </a-form-item>
          <a-form-item>
            <a-space wrap>
              <a-button type="primary" html-type="submit" :loading="loading" :disabled="busy()">查询</a-button>
              <a-button :disabled="busy()" @click="resetFilters">重置筛选</a-button>
              <a-button danger :disabled="busy() || !hasAppliedFilter()" @click="confirmBatchDelete">删除筛选结果</a-button>
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
          :busy="busy()"
          :context-key="failureContextKey"
          :is-current="isCurrentFailure"
          @retry="load"
        />
        <a-alert v-else-if="actionWarning" type="warning" show-icon :message="actionWarning" class="status-alert" />
      </ConsolePanel>

      <ConsolePanel title="IP 段记录" :description="scopeDescription">
        <ConsoleTableShell label="IP 段记录">
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
              <span v-else-if="column.key === 'range'" class="range-cell">{{ formatSegmentRange(record) }}</span>
              <span v-else-if="column.key === 'region'">{{ record.region || '—' }}</span>
              <span v-else-if="column.key === 'operator'">{{ operatorText(record) }}</span>
              <span v-else-if="column.key === 'asn'" class="mono-cell">{{ record.asn || '—' }}</span>
              <span v-else-if="column.key === 'source'">{{ record.source || '—' }}</span>
              <span v-else-if="column.key === 'sampleIp'" class="mono-cell">{{ record.sampleIp || '—' }}</span>
              <span v-else-if="column.key === 'fetchedAt'" class="time-cell">{{ formatDateTime(record.fetchedAt) }}</span>
              <template v-else-if="column.key === 'action'">
                <a-space>
                  <a-button type="link" size="small" :disabled="busy() || !record.sampleIp" @click="refreshRow(record)">刷新</a-button>
                  <a-button type="link" size="small" danger :disabled="busy()" @click="confirmDelete(record)">删除</a-button>
                </a-space>
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
import { batchDeleteIPSegments, deleteIPSegment, getIPSegments, lookupIPSegment, refreshIPSegment } from '@/api/IpSegmentApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

// 只做展示侧的数字格式化：后端给的是 uint32 差值之和，可能很大，
// 用千分位而不是科学计数法，避免管理员把 1.2e+9 读成 120 万。
function readNonnegativeNumber(value) {
  if (!['number', 'string'].includes(typeof value) || String(value).trim() === '') return null
  const number = Number(value)
  return Number.isFinite(number) && number >= 0 ? number : null
}
function readPositiveInt(value) {
  const number = readNonnegativeNumber(value)
  return Number.isInteger(number) && number > 0 ? number : null
}
// IPv4 校验：四段十进制且每段 ≤255。段表只覆盖 IPv4，
// 在这里挡掉 IPv6 / 半成品输入，比让上游返回空段再解释更直接。
function isIPv4(value) {
  const parts = String(value ?? '').trim().split('.')
  if (parts.length !== 4) return false
  return parts.every(part => /^\d{1,3}$/.test(part) && Number(part) <= 255)
}

const emptyFilters = () => ({ keyword: '', asn: '', llc: '', source: undefined })

export default {
  name: 'IpSegmentPage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell, RequestFailureAlert },
  setup() {
    const route = useRoute()
    const prefix = 'ip-segment-' + useId()
    const fieldId = field => prefix + '-' + field
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const rows = ref([])
    const filters = reactive(emptyFilters())
    const appliedFilters = ref({})
    const loading = ref(false)
    const mutating = ref(false)
    const hasLoaded = ref(false)
    const filterError = ref('')
    const actionWarning = ref('')
    const loadFailure = shallowRef(null)
    const failureContextKey = ref(0)
    let failureOwner = () => false

    const lookupInput = ref('')
    const lookup = reactive({ loading: false, error: '', result: null })

    const summary = reactive({ segmentCount: null, coveredAddresses: null, asnCount: null, latestFetchedAt: null })
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
    const busy = () => loading.value || mutating.value || lookup.loading

    const segmentCount = computed(() => summary.segmentCount ?? 0)
    const asnCount = computed(() => summary.asnCount ?? 0)
    const latestFetchedAt = computed(() => summary.latestFetchedAt)
    const coveredText = computed(() => {
      const value = summary.coveredAddresses
      return value === null ? '--' : Number(value).toLocaleString('zh-CN')
    })

    const columns = [
      { title: 'ID', dataIndex: 'id', key: 'id', width: 90 },
      { title: '段范围', key: 'range', width: 250 },
      { title: '归属地', dataIndex: 'region', key: 'region', width: 200 },
      { title: '运营商', key: 'operator', width: 160 },
      { title: 'ASN', dataIndex: 'asn', key: 'asn', width: 110 },
      { title: '来源', dataIndex: 'source', key: 'source', width: 110 },
      { title: '样本 IP', dataIndex: 'sampleIp', key: 'sampleIp', width: 150 },
      { title: '刷新时间', dataIndex: 'fetchedAt', key: 'fetchedAt', width: 175 },
      { title: '操作', key: 'action', width: 140 },
    ]

    const sourceOptions = [
      { label: 'standard', value: 'standard' },
      { label: 'commercial', value: 'commercial' },
    ]

    const scopeDescription = computed(() => {
      const scope = appliedFilters.value
      const parts = [
        scope.keyword && `关键字：${scope.keyword}`,
        scope.asn && `ASN：${scope.asn}`,
        scope.llc && `运营商：${scope.llc}`,
        scope.source && `来源：${scope.source}`,
      ].filter(Boolean)
      return parts.length ? `当前筛选：${parts.join('；')}。` : '当前范围：全部 IP 段。'
    })

    const emptyText = computed(() => (loadFailure.value
      ? '列表加载失败，请重试'
      : hasLoaded.value ? '暂无符合条件的 IP 段' : 'IP 段尚未读取成功'))

    const failureMessage = computed(() => (hasLoaded.value
      ? 'IP 段列表刷新失败，页面显示的仍是上次读取的结果。'
      : 'IP 段列表尚未加载成功，请重试读取。'))

    function formatSegmentRange(segment) {
      const begin = String(segment?.beginText || '').trim()
      const end = String(segment?.endText || '').trim()
      if (!begin && !end) return '—'
      if (!begin) return end
      if (!end) return begin
      return `${begin} – ${end}`
    }
    // llc 是中文简称（黑名单地理匹配优先用它），isp 是全名；两者都没有时退回占位符。
    function operatorText(segment) {
      const llc = String(segment?.llc || '').trim()
      if (llc && llc !== 'N/A') return llc
      const isp = String(segment?.isp || '').trim()
      return isp && isp !== 'N/A' ? isp : '—'
    }
    function formatDateTime(value) {
      if (!value) return '—'
      const date = new Date(value)
      if (!Number.isFinite(date.getTime())) return '—'
      return date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
    }
    function setFilter(field, value) {
      if (!canManage() || busy() || !Object.hasOwn(filters, field)) return
      filters[field] = value
    }

    const hasAppliedFilter = () => {
      const scope = appliedFilters.value
      return Boolean(String(scope.keyword || '').trim() || String(scope.asn || '').trim()
        || String(scope.llc || '').trim() || String(scope.source || '').trim())
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
      const scope = appliedFilters.value
      const keyword = String(scope.keyword || '').trim()
      if (keyword) params.keyword = keyword
      const asn = String(scope.asn || '').trim()
      if (asn) params.asn = asn
      const llc = String(scope.llc || '').trim()
      if (llc) params.llc = llc
      const source = String(scope.source || '').trim()
      if (source) params.source = source
      return params
    }

    const load = async () => {
      if (!canManage() || busy()) return false
      const ticket = ++readVersion
      const owns = () => canManage() && ticket === readVersion
      clearLoadFailure()
      const context = failureContextKey.value
      const ownsFailure = () => owns() && context === failureContextKey.value
      loading.value = true
      try {
        const res = await getIPSegments(buildParams())
        if (!ownsFailure()) return false
        const body = res?.data
        const items = body?.items
        const count = readNonnegativeNumber(body?.total)
        if (res?.status !== 200 || !Array.isArray(items) || count === null || !Number.isInteger(count)) {
          failureOwner = ownsFailure
          loadFailure.value = ensureRequestFailure(res)
          return false
        }
        rows.value = items.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
        pagination.total = count
        summary.segmentCount = readNonnegativeNumber(body?.summary?.segmentCount)
        summary.coveredAddresses = readNonnegativeNumber(body?.summary?.coveredAddresses)
        summary.asnCount = readNonnegativeNumber(body?.summary?.asnCount)
        summary.latestFetchedAt = body?.summary?.latestFetchedAt || null
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
      if (!canManage() || busy()) return
      filterError.value = ''
      actionWarning.value = ''
      appliedFilters.value = {
        keyword: String(filters.keyword || '').trim(),
        asn: String(filters.asn || '').trim(),
        llc: String(filters.llc || '').trim(),
        source: filters.source || '',
      }
      pagination.current = 1
      return load()
    }

    const resetFilters = () => {
      if (!canManage() || busy()) return
      Object.assign(filters, emptyFilters())
      appliedFilters.value = {}
      filterError.value = ''
      actionWarning.value = ''
      pagination.current = 1
      return load()
    }

    const onTableChange = page => {
      if (!canManage() || busy()) return
      pagination.current = page?.pageSize === pagination.pageSize ? Number(page.current) || 1 : 1
      pagination.pageSize = Number(page?.pageSize) || pagination.pageSize
      return load()
    }

    // runLookup 只在管理员点击「查询归属段」时触发 —— 挂载期不发任何额外请求。
    const runLookup = async () => {
      if (!canManage() || busy()) return false
      lookup.error = ''
      const ip = String(lookupInput.value || '').trim()
      if (!ip) { lookup.error = '请输入 IP'; lookup.result = null; return false }
      if (!isIPv4(ip)) { lookup.error = '仅支持 IPv4，例如 120.239.173.110'; lookup.result = null; return false }
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      lookup.loading = true
      try {
        const res = await lookupIPSegment(ip)
        if (!owns()) return false
        if (res?.status !== 200 || !res.data || typeof res.data !== 'object') {
          lookup.result = null
          lookup.error = '查询失败，请稍后重试'
          return false
        }
        lookup.result = { ...res.data }
        return true
      } catch (error) {
        if (owns()) { lookup.result = null; lookup.error = '查询失败，请稍后重试' }
        return false
      } finally {
        if (owns()) lookup.loading = false
      }
    }

    // mutation 统一走这里：串行化、身份校验、失败态收敛。返回后 mutating 已释放，
    // 调用方再刷新列表 —— load() 会拒绝在 busy() 期间执行，所以在 finally 里刷不动。
    // successText 可以是函数：批量删除要回显真实删除行数，不能预先写死文案。
    const runMutation = async (action, successText) => {
      if (!canManage() || busy()) return false
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      const context = failureContextKey.value
      const ownsFailure = () => owns() && context === failureContextKey.value
      mutating.value = true
      actionWarning.value = ''
      try {
        const res = await action()
        if (!ownsFailure()) return false
        if (res?.status === 404) {
          actionWarning.value = '该段记录已不存在，请刷新列表核对。'
          return true
        }
        if (res?.status !== 200) {
          failureOwner = ownsFailure
          loadFailure.value = ensureRequestFailure(res)
          return false
        }
        message.success(typeof successText === 'function' ? successText(res) : successText)
        return true
      } catch (error) {
        if (ownsFailure()) { failureOwner = ownsFailure; loadFailure.value = ensureRequestFailure(error) }
        return false
      } finally {
        if (owns()) mutating.value = false
      }
    }

    const refreshRow = async record => {
      const id = readPositiveInt(record?.id)
      if (!id || !canManage() || busy()) return false
      await runMutation(() => refreshIPSegment(id), '该段已按样本 IP 重新查询')
      return load()
    }

    const confirmDelete = async record => {
      const id = readPositiveInt(record?.id)
      if (!id || !canManage() || busy()) return false
      const result = await Swal.fire({
        title: '确认删除这条 IP 段记录？',
        text: `段范围 ${formatSegmentRange(record)}，归属地 ${record.region || '—'}。删除只影响段级缓存，该段内的 IP 之后会重新外呼上游查询（会计费）。`,
        icon: 'warning',
        showCancelButton: true,
        confirmButtonText: '确认删除',
        cancelButtonText: '取消',
      })
      if (!result.isConfirmed || !canManage()) return false
      await runMutation(() => deleteIPSegment(id), '段记录已删除')
      return load()
    }

    const confirmBatchDelete = async () => {
      if (!canManage() || busy() || !hasAppliedFilter()) return false
      const scope = appliedFilters.value
      const scopeText = [
        scope.keyword && `关键字「${scope.keyword}」`,
        scope.asn && `ASN ${scope.asn}`,
        scope.llc && `运营商 ${scope.llc}`,
        scope.source && `来源 ${scope.source}`,
      ].filter(Boolean).join('、')
      const result = await Swal.fire({
        title: '确认删除筛选范围内的全部 IP 段？',
        text: `当前筛选：${scopeText}。删除后这些段内的 IP 会重新外呼上游查询（会计费），且不可撤销。`,
        icon: 'warning',
        showCancelButton: true,
        confirmButtonText: '确认删除',
        cancelButtonText: '取消',
      })
      if (!result.isConfirmed || !canManage()) return false
      // 走同一套 mutation 闸门：与单条删除一样串行化、收敛失败态，成功后再刷新列表。
      await runMutation(() => batchDeleteIPSegments(scope), res => {
        const deleted = readNonnegativeNumber(res?.data?.deleted)
        return deleted === null ? '批量删除完成' : `已删除 ${deleted} 条 IP 段`
      })
      return load()
    }

    const resetDraft = () => {
      Object.assign(filters, emptyFilters())
      appliedFilters.value = {}
      filterError.value = ''
      lookupInput.value = ''
      lookup.error = ''
      lookup.result = null
    }

    const retire = () => {
      readVersion += 1
      writeVersion += 1
      pagination.current = 1
    }
    watch(() => [currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status], () => {
      retire()
      rows.value = []
      pagination.total = 0
      hasLoaded.value = false
      summary.segmentCount = null
      summary.coveredAddresses = null
      summary.asnCount = null
      summary.latestFetchedAt = null
      actionWarning.value = ''
      resetDraft()
      if (mounted && canManage()) void load()
    }, { flush: 'sync' })
    watch(() => getCurrentUserLogoutEpoch(), value => {
      if (value === observedLogout) return
      observedLogout = value
      retire()
      rows.value = []
      pagination.total = 0
      hasLoaded.value = false
      summary.segmentCount = null
      summary.coveredAddresses = null
      summary.asnCount = null
      summary.latestFetchedAt = null
      clearLoadFailure()
    }, { flush: 'sync' })
    watch(() => route.path, () => {
      retire()
      rows.value = []
      pagination.total = 0
      hasLoaded.value = false
      summary.segmentCount = null
      summary.coveredAddresses = null
      summary.asnCount = null
      summary.latestFetchedAt = null
      actionWarning.value = ''
      clearLoadFailure()
    }, { flush: 'sync' })
    onMounted(() => {
      mounted = true
      // 只发这一条列表请求：本页不在挂载期补查汇总或探测上游。
      if (canManage()) void load()
    })
    onBeforeUnmount(() => {
      alive = false
      mounted = false
      readVersion += 1
      writeVersion += 1
      clearLoadFailure()
    })

    return {
      fieldId, canManage, busy, columns, rows, filters, appliedFilters, loading, mutating,
      hasLoaded, filterError, actionWarning, loadFailure, failureContextKey, failureMessage,
      isCurrentFailure, pagination, sourceOptions, scopeDescription, emptyText,
      segmentCount, asnCount, coveredText, latestFetchedAt, hasAppliedFilter,
      lookupInput, lookup, runLookup, formatSegmentRange, operatorText, formatDateTime,
      setFilter, load, reload, resetFilters, onTableChange, refreshRow, confirmDelete,
      confirmBatchDelete, sumTableColumnWidths,
    }
  },
}
</script>

<style scoped>
.page-alert, .filter-form, .status-alert, .lookup-form { margin-bottom: 16px; }
.segment-summary { margin-bottom: 16px; }
.summary-label { color: var(--console-muted); font-size: 14px; }
.summary-value { margin-top: 8px; color: var(--console-text); font-size: clamp(20px, 2.6vw, 28px); font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.summary-value--time { font-size: clamp(14px, 1.6vw, 18px); }
.filter-keyword { width: 240px; }
.filter-narrow { width: 150px; }
.lookup-input { width: 240px; }
.lookup-result { margin-top: 8px; }
.filter-error { margin: 0 0 16px; color: var(--console-danger, #c13e41); font-size: 13px; }
.range-cell, .mono-cell, .time-cell { font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
@media (max-width: 640px) {
  .filter-form :deep(.ant-form-item), .lookup-form :deep(.ant-form-item) { width: 100%; margin-inline-end: 0; }
  .filter-keyword, .filter-narrow, .lookup-input { width: 100%; }
}
</style>
