<template>
  <div class="console-page service-status-page">
    <ConsolePageHeading
      title="服务器状态"
      description="这里只展示搜题接口的运行状态，不受登录、管理页等其它请求影响。"
    >
      <template #actions>
        <a-button
          data-action="refresh-service-status"
          :loading="loading"
          :disabled="loading"
          @click="load"
        >刷新</a-button>
      </template>
    </ConsolePageHeading>

    <section v-if="errorMessage" class="page-alert" role="alert">
      <a-alert type="warning" show-icon :message="errorMessage" />
    </section>

    <div
      v-if="loading && !loaded"
      class="status-loading"
      role="status"
      aria-live="polite"
      aria-busy="true"
    >
      <a-spin />
      <span>正在读取搜题接口状态…</span>
    </div>

    <ConsolePanel v-else-if="loaded" title="搜题接口" description="统计数据来自服务端最近窗口的搜题请求。">
      <div
        class="search-status"
        :class="{ 'search-status--muted': endpointView.greyed }"
        :data-endpoint-kind="endpointView.key"
        :data-endpoint-state="endpointView.state"
        :data-endpoint-greyed="endpointView.greyed ? 'true' : 'false'"
        :data-status-tone="overviewAsOf === null ? 'muted' : endpointView.tone"
      >
        <span class="search-status__dot" aria-hidden="true"></span>
        <div>
          <p class="search-status__label" data-status-label>{{ overviewAsOf === null ? '时窗未确认' : endpointView.statusLabel }}</p>
          <p class="search-status__hint" data-endpoint-hint>
            {{ overviewAsOf === null ? '服务未提供可确认统计时点；下列仅为遗留统计，不表示当前健康。' : (endpointView.hint || statusHint) }}
          </p>
        </div>
      </div>

      <ConsoleSummaryGrid class="search-metrics" aria-label="搜题接口状态摘要">
        <div>
          <div class="summary-label">成功率</div>
          <div class="summary-value" data-metric="search-success-rate" data-endpoint-rate>{{ endpointView.rateText }}</div>
        </div>
        <div>
          <div class="summary-label">平均延迟</div>
          <div class="summary-value" data-metric="search-avg-latency">{{ endpointView.latencyText }}</div>
        </div>
        <div>
          <div class="summary-label">搜题请求</div>
          <div class="summary-value" data-metric="search-total-requests">{{ endpointView.totalText }}</div>
          <div class="summary-hint">成功 {{ endpointView.successText }} 次</div>
        </div>
      </ConsoleSummaryGrid>

      <p class="hint" data-search-overview-time v-if="overviewAsOf !== null">搜题概览：截至 {{ overviewTime }}（Asia/Shanghai），最近 5 个自然分钟。</p>
      <SearchStatusHistory :payload="historyPayload" />
    </ConsolePanel>

    <p v-else-if="!errorMessage" class="hint" role="status">正在准备读取搜题接口状态。</p>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { useRoute } from 'vue-router'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import SearchStatusHistory from '@/components/SearchStatusHistory.vue'
import { getServiceStatusSeries, getServiceStatusSummary } from '@/api/ServiceStatusApi'
import { getCurrentUserLogoutEpoch, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import {
  buildEndpointView,
  loadServiceStatusSummary,
} from '@/utils/serviceStatusSummary.mjs'
import { currentSearchSnapshot, formatStatusTime, loadSearchStatusHistory, parseStatusTimestamp, searchSummaryFallback } from '@/utils/searchStatusHistory.mjs'

function confirmedClock(snapshot) {
  const asOf = parseStatusTimestamp(snapshot?.asOf)
  const restartedAt = parseStatusTimestamp(snapshot?.restartedAt)
  return asOf !== null && restartedAt !== null && restartedAt <= asOf ? { asOf, restartedAt } : null
}

export default {
  name: 'ServiceStatusPage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, SearchStatusHistory },
  setup() {
    const route = useRoute()
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()
    const loading = ref(false)
    const loaded = ref(false)
    const payload = shallowRef(null)
    const historyPayload = shallowRef(null)
    const errorMessage = ref('')

    let alive = true
    let readVersion = 0
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()

    // 身份、路由或登录状态改变时，阻止旧请求覆盖当前页面。
    const pageActive = () => alive && route.path === mountedPath
    const canLoad = () => pageActive() && getCurrentUserLogoutEpoch() === mountedLogoutEpoch
      && Boolean(currentUserIdentity(currentUser.value))

    // 同一份可信 series 同时提供时间轴和当前搜题桶。独立 GET 的 summary
    // 可能来自时间回拨前或另一代进程，不能把它与 series 的 asOf 拼接。
    const hasTimedHistory = computed(() => parseStatusTimestamp(historyPayload.value?.asOf) !== null)
    const overview = computed(() => hasTimedHistory.value ? historyPayload.value : payload.value)
    const overviewAsOf = computed(() => parseStatusTimestamp(overview.value?.asOf))
    const overviewTime = computed(() => formatStatusTime(overviewAsOf.value))
    const endpointView = computed(() => {
      if (hasTimedHistory.value) return buildEndpointView('search', currentSearchSnapshot(historyPayload.value))
      return buildEndpointView('search', searchSummaryFallback(overview.value?.endpoints?.search))
    })
    const statusHint = computed(() => {
      if (endpointView.value.state === 'idle') return endpointView.value.hint
      if (endpointView.value.tone === 'ok') return '最近窗口内搜题请求基本正常。'
      if (endpointView.value.tone === 'warn') return '最近窗口内搜题请求有较多失败，答题可能不稳定。'
      if (endpointView.value.tone === 'danger') return '最近窗口内搜题请求大部分失败，答题可能不可用。'
      return '状态根据搜题接口最近窗口的采样推导。'
    })


    const load = async () => {
      if (!canLoad() || loading.value) return false
      const ticket = ++readVersion
      const owns = () => canLoad() && ticket === readVersion
      errorMessage.value = ''
      loading.value = true
      try {
        // 公开摘要只读取一次；状态和指标均从 endpoints.search 派生。
        const [result, history] = await Promise.all([
          loadServiceStatusSummary(() => getServiceStatusSummary()),
          loadSearchStatusHistory(() => getServiceStatusSeries()),
        ])
        if (!owns()) return false
        const previousSummaryClock = confirmedClock(payload.value)
        const freshSummaryClock = result.ok ? confirmedClock(result.data) : null
        const acceptSummary = result.ok && (!previousSummaryClock ||
          (freshSummaryClock && freshSummaryClock.restartedAt >= previousSummaryClock.restartedAt))
        if (acceptSummary) payload.value = result.data
        if (!result.ok) errorMessage.value = result.message

        const summaryClock = confirmedClock(payload.value)
        const cachedClock = confirmedClock(historyPayload.value)
        const cachedDiscarded = summaryClock && cachedClock && (
          cachedClock.restartedAt < summaryClock.restartedAt ||
          (acceptSummary && freshSummaryClock && cachedClock.restartedAt === freshSummaryClock.restartedAt &&
            cachedClock.asOf > freshSummaryClock.asOf)
        )
        const incoming = history.ok && parseStatusTimestamp(history.data?.asOf) !== null ? history.data : null
        const incomingClock = confirmedClock(incoming)
        const olderGeneration = incomingClock && summaryClock && incomingClock.restartedAt < summaryClock.restartedAt

        // 同代两份新读不能按到达顺序拼合。优先可信新序列；缺读时仅明确
        // 重启或回拨使旧序列失效，普通前向更新/短暂读失败仍保留旧快照。
        if (incoming && !olderGeneration && (!cachedDiscarded || incomingClock)) historyPayload.value = incoming
        else if (cachedDiscarded) historyPayload.value = null
        // 首次读失败仍保持空态。
        loaded.value = loaded.value || result.ok || history.ok
        return true
      } finally {
        if (owns()) loading.value = false
      }
    }

    const retire = () => {
      readVersion += 1
      payload.value = null
      historyPayload.value = null
      loaded.value = false
      loading.value = false
      errorMessage.value = ''
    }

    onMounted(() => {
      if (canLoad()) void load()
    })
    onBeforeUnmount(() => {
      alive = false
      retire()
    })

    return { loading, loaded, errorMessage, endpointView, statusHint, historyPayload, overviewAsOf, overviewTime, load }
  },
}
</script>

<style scoped>
.page-alert { margin-bottom: 16px; }
.status-loading { display: flex; align-items: center; justify-content: center; gap: 12px; min-height: 140px; color: var(--console-muted); }
.hint { margin: 12px 0 0; color: var(--console-muted); font-size: 13px; line-height: 1.7; }
.search-status { display: flex; align-items: center; gap: 14px; padding: 14px 16px; border: 1px solid var(--console-border); border-radius: 10px; background: var(--console-surface, transparent); }
.search-status__dot { flex: 0 0 auto; width: 12px; height: 12px; border-radius: 50%; background: var(--console-muted); }
.search-status__label { margin: 0; color: var(--console-text); font-size: 18px; font-weight: 600; }
.search-status__hint { margin: 4px 0 0; color: var(--console-muted); font-size: 13px; line-height: 1.6; }
.search-status[data-status-tone='ok'] .search-status__dot { background: var(--console-success, #15803d); }
.search-status[data-status-tone='warn'] .search-status__dot { background: var(--console-warning, #b45309); }
.search-status[data-status-tone='danger'] .search-status__dot { background: var(--console-danger, #b91c1c); }
.search-status[data-status-tone='muted'] .search-status__dot { background: var(--console-muted); }
.search-status--muted { opacity: .72; }
.search-metrics { margin-top: 16px; }
.summary-label { color: var(--console-muted); font-size: 14px; }
.summary-value { margin-top: 6px; color: var(--console-text); font-size: clamp(20px, 2.6vw, 28px); font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.summary-hint { margin-top: 4px; color: var(--console-muted); font-size: 12px; }
@media (max-width: 768px) { .search-status { align-items: flex-start; } }
</style>
