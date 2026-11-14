<template>
  <div class="console-page service-status-page">
    <ConsolePageHeading
      title="服务器状态"
      description="本站搜题服务的当前健康度。数据来自服务进程内存中的分钟级采样，重启后从零重新积累。"
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
      <span>正在读取服务器状态…</span>
    </div>

    <template v-else-if="loaded">
      <ConsolePanel>
        <div class="status-hero" :data-status-tone="view.tone" :data-data-state="view.dataState">
          <div class="status-hero__dot" aria-hidden="true"></div>
          <div class="status-hero__body">
            <p class="status-hero__label" data-status-label>{{ view.statusLabel }}</p>
            <p class="status-hero__hint">{{ heroHint }}</p>
          </div>
        </div>
      </ConsolePanel>

      <!--
        ⚠️ 这里必须是 v-if="view.dataState === 'empty'"，不能写成 v-if="successRate === 0"。
        全失败（数据可用 + 成功率 0%）与无数据在数值上完全相同，
        用数值判会把握手报警的状态显示成「暂无数据」。
      -->
      <section v-if="view.dataState === 'empty'" class="page-alert" data-empty-hint role="status">
        <a-alert type="info" show-icon message="暂无采样数据" :description="view.emptyHint" />
      </section>

      <ConsoleSummaryGrid class="status-summary" aria-label="服务状态摘要">
        <div>
          <div class="summary-label">成功率</div>
          <div class="summary-value" data-metric="success-rate">{{ view.rateText }}</div>
          <div class="summary-hint">{{ rateHint }}</div>
        </div>
        <div>
          <div class="summary-label">平均延迟</div>
          <div class="summary-value" data-metric="avg-latency">{{ view.latencyText }}</div>
          <div class="summary-hint">窗口内已完成请求的平均耗时</div>
        </div>
        <div>
          <div class="summary-label">请求总数</div>
          <div class="summary-value" data-metric="total-requests">{{ view.totalText }}</div>
          <div class="summary-hint">其中成功 {{ view.successText }} 次</div>
        </div>
        <div>
          <div class="summary-label">失败请求</div>
          <div class="summary-value" data-metric="failed-requests">{{ view.failedText }}</div>
          <div class="summary-hint">失败率按同一窗口计算</div>
        </div>
      </ConsoleSummaryGrid>

      <!--
        逐分钟曲线（脱敏序列端点）。这是**增强区块**：
        series 失败时整块不渲染，摘要与说明照常显示 —— 曲线拿不到不该让整页崩。
        失败四分类不在这里出现（后端不给），见 controller.publicStatusPoint 的取舍说明。
      -->
      <ConsolePanel
        v-if="seriesView"
        title="最近每分钟趋势"
        description="按自然分钟聚合的请求量与成功率。只保留画曲线需要的量，不含失败原因分类。"
        class="series-panel"
      >
        <div v-if="seriesView.isEmpty" class="series-empty" data-series-empty role="status">
          <p class="empty-title">暂无逐分钟数据</p>
          <p class="hint">服务刚重启时曲线需要重新积累，每分钟生成一个点。下面的摘要仍然有效。</p>
        </div>
        <template v-else>
          <!-- 色带：每个点一根柱，高度按该分钟请求数占本序列峰值的比例。 -->
          <div class="series-strip" data-series-strip role="img" :aria-label="seriesAriaLabel">
            <span
              v-for="(point, index) in seriesView.points"
              :key="index"
              class="series-bar"
              :data-series-bar="index"
              :data-rate-tone="point.rateText === '0%' ? 'danger' : 'normal'"
              :style="{ height: point.barHeight + '%' }"
              :title="`${point.timeText} · ${point.totalText} 次 · 成功率 ${point.rateText}`"
            ></span>
          </div>
          <div class="series-axis">
            <span>{{ seriesView.points[0].timeText }}</span>
            <span>{{ seriesView.points[seriesView.points.length - 1].timeText }}</span>
          </div>

          <a-descriptions :column="1" size="small" bordered class="status-descriptions">
            <a-descriptions-item label="已采样点数">
              {{ seriesView.retainedText }} / {{ seriesView.capacityText }} 分钟
            </a-descriptions-item>
            <a-descriptions-item label="曲线起点">
              {{ seriesView.restartedAtText || '服务启动时间不可用' }}
            </a-descriptions-item>
          </a-descriptions>

          <!-- 分钟明细：只列最近 10 条，避免把 1440 行全铺在页面上。 -->
          <table class="series-table" data-series-table>
            <caption class="series-caption">最近 {{ recentPoints.length }} 分钟明细</caption>
            <thead>
              <tr>
                <th scope="col">时间</th>
                <th scope="col">请求</th>
                <th scope="col">成功</th>
                <th scope="col">成功率</th>
                <th scope="col">平均延迟</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(point, index) in recentPoints" :key="index" :data-series-row="index">
                <td>{{ point.timeText }}</td>
                <td>{{ point.totalText }}</td>
                <td>{{ formatCount(point.successRequests) }}</td>
                <td>{{ point.rateText }}</td>
                <td>{{ point.latencyText }}</td>
              </tr>
            </tbody>
          </table>
        </template>
      </ConsolePanel>

      <ConsolePanel title="采样说明" description="这些数字的边界，避免误读。">
        <a-descriptions :column="1" size="small" bordered class="status-descriptions">
          <a-descriptions-item label="统计窗口">{{ view.windowText }}</a-descriptions-item>
          <a-descriptions-item label="已采样点数">
            {{ view.retainedText }} / {{ view.capacityText }} 分钟
          </a-descriptions-item>
          <a-descriptions-item label="数据起点">
            {{ view.restartedAtText || '服务启动时间不可用' }}
          </a-descriptions-item>
        </a-descriptions>
        <p class="hint">
          状态由最近 {{ view.windowText.replace('最近 ', '') }}的请求推导：成功率 ≥ 95% 为正常，
          70%–95% 为降级，低于 70% 为异常。窗口内没有任何请求时显示「暂无采样数据」——
          这与「全部失败」不同，后者成功率是 0% 并会显示为异常。
        </p>
        <p class="hint">
          数据只存在服务进程内存里：不写数据库、重启即清零。因此曲线会在重启后重新积累，
          「数据起点」就是本次重启的时刻。
        </p>
      </ConsolePanel>
    </template>

    <p v-else-if="!errorMessage" class="hint" role="status">点击刷新读取服务器状态。</p>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { useRoute } from 'vue-router'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import { getServiceStatusSeries, getServiceStatusSummary } from '@/api/ServiceStatusApi'
import { getCurrentUserLogoutEpoch, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import {
  buildServiceStatusView,
  formatCount,
  loadServiceStatusSeries,
  loadServiceStatusSummary,
  SERVICE_STATUS_SUMMARY_PATH,
} from '@/utils/serviceStatusSummary.mjs'

export default {
  name: 'ServiceStatusPage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid },
  setup() {
    const route = useRoute()
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const loading = ref(false)
    const loaded = ref(false)
    const payload = shallowRef(null)
    const seriesView = shallowRef(null)
    const errorMessage = ref('')

    // 分钟明细只展示最近 N 条：1440 行全铺出来既慢又没人看。
    const SERIES_TABLE_LIMIT = 10
    const recentPoints = computed(() => {
      const points = seriesView.value?.points ?? []
      return points.slice(-SERIES_TABLE_LIMIT)
    })

    const seriesAriaLabel = computed(() => {
      const points = seriesView.value?.points ?? []
      if (points.length === 0) return '逐分钟请求量色带（暂无数据）'
      const peak = points.reduce((max, point) => Math.max(max, point.totalRequests), 0)
      return `逐分钟请求量色带，共 ${points.length} 个点，峰值 ${peak} 次请求每分钟`
    })

    let alive = true
    let readVersion = 0
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()

    // 与其它用户页同一套守卫：身份/路由/登出任一变化都不再让旧响应写回页面。
    const pageActive = () => alive && route.path === mountedPath
    const canLoad = () => pageActive() && getCurrentUserLogoutEpoch() === mountedLogoutEpoch
      && Boolean(currentUserIdentity(currentUser.value))

    const view = computed(() => buildServiceStatusView(payload.value))

    const heroHint = computed(() => {
      if (view.value.dataState === 'empty') return '最近窗口内没有请求，暂无成功率可算。'
      if (view.value.tone === 'ok') return '最近窗口内请求基本全部成功。'
      if (view.value.tone === 'warn') return '最近窗口内有较多失败请求，答题可能不稳定。'
      if (view.value.tone === 'danger') return '最近窗口内大部分请求失败，答题可能不可用。'
      return '状态由服务端按最近窗口推导。'
    })

    const rateHint = computed(() => {
      if (view.value.dataState !== 'measured') return '窗口内无请求，算不出成功率'
      if (view.value.rateText === '0%') return '窗口内请求全部失败'
      return '窗口内成功请求占比'
    })

    const load = async () => {
      if (!canLoad() || loading.value) return false
      const ticket = ++readVersion
      const owns = () => canLoad() && ticket === readVersion
      errorMessage.value = ''
      loading.value = true
      try {
        // 只打 summary 端点。普通用户请求 /api/admin/service-status 会 403，
        // 所以这里刻意没有「摘要失败就降级要明细」的分支。
        const result = await loadServiceStatusSummary(() => getServiceStatusSummary())
        if (!owns()) return false
        if (!result.ok) {
          errorMessage.value = result.message
          return false
        }
        payload.value = result.data
        loaded.value = true
        // 曲线是**增强**：并发取，失败只隐藏这一块，绝不影响已显示的摘要。
        // 这里刻意 await 而不是 fire-and-forget，是为了让「刷新」按钮的 loading
        // 覆盖整个刷新过程，避免用户看到曲线晚一拍才出现。
        const series = await loadServiceStatusSeries(() => getServiceStatusSeries())
        if (owns() && series.ok) seriesView.value = series.series
        return true
      } finally {
        if (owns()) loading.value = false
      }
    }

    const retire = () => {
      readVersion += 1
      payload.value = null
      seriesView.value = null
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

    return {
      loading,
      loaded,
      errorMessage,
      view,
      seriesView,
      recentPoints,
      seriesAriaLabel,
      heroHint,
      rateHint,
      load,
      formatCount,
      summaryPath: SERVICE_STATUS_SUMMARY_PATH,
    }
  },
}
</script>

<style scoped>
.page-alert { margin-bottom: 16px; }
.status-summary { margin-bottom: 16px; }
.status-loading { display: flex; align-items: center; justify-content: center; gap: 12px; min-height: 140px; color: var(--console-muted); }
.summary-label { color: var(--console-muted); font-size: 14px; }
.summary-value { margin-top: 8px; color: var(--console-text); font-size: clamp(20px, 2.6vw, 28px); font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.summary-hint { margin-top: 8px; color: var(--console-muted); font-size: 12px; line-height: 1.6; }
.status-descriptions { margin-bottom: 16px; }
.hint { margin: 12px 0 0; color: var(--console-muted); font-size: 13px; line-height: 1.7; }

.status-hero { display: flex; align-items: center; gap: 16px; }
.status-hero__dot { flex: 0 0 auto; width: 14px; height: 14px; border-radius: 50%; background: var(--console-muted); }
.status-hero__label { margin: 0; color: var(--console-text); font-size: clamp(18px, 2.4vw, 24px); font-weight: 600; }
.status-hero__hint { margin: 6px 0 0; color: var(--console-muted); font-size: 13px; line-height: 1.7; }
/* 语义色只走 border/背景，文字保持正文色，避免浅色主题下对比度不足。 */
.status-hero[data-status-tone='ok'] .status-hero__dot { background: var(--console-success, #15803d); }
.status-hero[data-status-tone='warn'] .status-hero__dot { background: var(--console-warning, #b45309); }
.status-hero[data-status-tone='danger'] .status-hero__dot { background: var(--console-danger, #b91c1c); }
.status-hero[data-status-tone='muted'] .status-hero__dot { background: var(--console-muted); }
@media (max-width: 768px) { .status-hero { align-items: flex-start; } }

/* 逐分钟色带：纯 CSS 柱状，不引入图表库（首屏体积已经很紧）。 */
.series-panel { margin-bottom: 16px; }
.series-strip { display: flex; align-items: flex-end; gap: 2px; height: 120px; padding: 8px 0; border-bottom: 1px solid var(--console-border); overflow-x: auto; }
.series-bar { flex: 1 1 0; min-width: 2px; max-width: 24px; min-height: 2px; border-radius: 2px 2px 0 0; background: var(--console-primary); }
/* 该分钟全部失败时用危险色标出来，与摘要里的「异常」语义一致。 */
.series-bar[data-rate-tone='danger'] { background: var(--console-danger, #b91c1c); }
.series-axis { display: flex; justify-content: space-between; gap: 12px; margin-top: 6px; color: var(--console-muted); font-size: 12px; font-variant-numeric: tabular-nums; }
.series-empty { padding: 8px 0; }
.empty-title { margin: 0 0 4px; color: var(--console-text); font-size: 14px; font-weight: 600; }
.series-table { width: 100%; margin-top: 16px; border-collapse: collapse; font-variant-numeric: tabular-nums; }
.series-caption { padding-bottom: 8px; color: var(--console-muted); font-size: 13px; text-align: left; }
.series-table th, .series-table td { padding: 8px 12px; border-bottom: 1px solid var(--console-border); text-align: left; font-size: 13px; }
.series-table th { color: var(--console-muted); font-weight: 500; white-space: nowrap; }
.series-table td { color: var(--console-text); }
@media (max-width: 768px) {
  .series-strip { height: 90px; }
  .series-table th, .series-table td { padding: 6px 8px; }
}
</style>
