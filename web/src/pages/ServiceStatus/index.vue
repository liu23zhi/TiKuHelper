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
        :data-status-tone="endpointView.tone"
      >
        <span class="search-status__dot" aria-hidden="true"></span>
        <div>
          <p class="search-status__label" data-status-label>{{ endpointView.statusLabel }}</p>
          <p class="search-status__hint" data-endpoint-hint>
            {{ endpointView.hint || statusHint }}
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

      <p class="hint">数据更新于 {{ historyUpdatedText }}。统计窗口：最近 {{ view.windowText }}。服务重启后采样会重新积累。</p>
      <SearchStatusHistory :payload="historyPayload" :sampled-at="historySampledAt" />
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
  buildServiceStatusView,
  loadServiceStatusSummary,
} from '@/utils/serviceStatusSummary.mjs'
import { loadSearchStatusHistory } from '@/utils/searchStatusHistory.mjs'

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
    const historySampledAt = ref(Date.now())
    const errorMessage = ref('')

    let alive = true
    let readVersion = 0
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()

    // 身份、路由或登录状态改变时，阻止旧请求覆盖当前页面。
    const pageActive = () => alive && route.path === mountedPath
    const canLoad = () => pageActive() && getCurrentUserLogoutEpoch() === mountedLogoutEpoch
      && Boolean(currentUserIdentity(currentUser.value))

    const view = computed(() => buildServiceStatusView(payload.value))
    // 普通用户只显示 search 桶。全站汇总会被健康检查、管理页等请求稀释。
    const endpointView = computed(() => view.value.endpoints.search)
    const statusHint = computed(() => {
      if (endpointView.value.state === 'idle') return endpointView.value.hint
      if (endpointView.value.tone === 'ok') return '最近窗口内搜题请求基本正常。'
      if (endpointView.value.tone === 'warn') return '最近窗口内搜题请求有较多失败，答题可能不稳定。'
      if (endpointView.value.tone === 'danger') return '最近窗口内搜题请求大部分失败，答题可能不可用。'
      return '状态根据搜题接口最近窗口的采样推导。'
    })
    const historyUpdatedText = computed(() => new Date(historySampledAt.value).toLocaleTimeString('zh-CN', {
      timeZone: 'Asia/Shanghai', hour: '2-digit', minute: '2-digit', hour12: false,
    }))

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
        if (!result.ok) {
          errorMessage.value = result.message
        } else {
          payload.value = result.data
        }
        if (history.ok) {
          historyPayload.value = history.data
          historySampledAt.value = Date.now()
        }
        // 刷新失败时保留已显示的快照；首次读取失败才保持空态。
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

    return { loading, loaded, errorMessage, view, endpointView, statusHint, historyPayload, historySampledAt, historyUpdatedText, load }
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
