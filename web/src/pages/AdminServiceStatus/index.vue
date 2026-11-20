<template>
  <div class="console-page admin-service-status-page">
    <ConsolePageHeading
      title="服务器状态详情"
      description="管理员诊断视图：查看最近 24 小时请求状态、失败分类和当前进程指标。采样数据在服务重启后清零。"
    >
      <template #actions>
        <a-button data-action="refresh-admin-service-status" :loading="loading" :disabled="loading" @click="load">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <section v-if="errorMessage" class="page-alert" role="alert">
      <a-alert type="warning" show-icon :message="errorMessage" />
    </section>
    <div v-if="loading && !payload" class="status-loading" role="status" aria-live="polite" aria-busy="true">
      <a-spin /><span>正在读取服务器诊断…</span>
    </div>

    <template v-else-if="payload">
      <ConsolePanel title="API 调用状态" description="服务端按最近窗口统计 API 请求；逐分钟失败分类只在本管理员页面展示。">
        <div class="status-hero" :data-status-tone="tone">
          <span class="status-hero__dot" aria-hidden="true"></span>
          <div>
            <strong>{{ statusLabel }}</strong>
            <p>最近 {{ summary.windowMinutes }} 分钟 · 成功率 {{ percent(summary.successRate) }}</p>
          </div>
        </div>
        <ConsoleSummaryGrid class="status-summary" aria-label="API 调用摘要">
          <article class="metric-card"><span>请求总数</span><strong data-metric="requests">{{ count(summary.totalRequests) }}</strong></article>
          <article class="metric-card"><span>成功请求</span><strong data-metric="successes">{{ count(summary.successRequests) }}</strong></article>
          <article class="metric-card"><span>失败请求</span><strong data-metric="failures">{{ count(summary.failedRequests) }}</strong></article>
          <article class="metric-card"><span>平均延迟</span><strong data-metric="latency">{{ latency(summary.avgLatencyMs) }}</strong></article>
        </ConsoleSummaryGrid>
        <a-descriptions :column="1" size="small" bordered>
          <a-descriptions-item label="上游失败">{{ count(summary.breakdown.upstream) }}</a-descriptions-item>
          <a-descriptions-item label="超时失败">{{ count(summary.breakdown.timeout) }}</a-descriptions-item>
          <a-descriptions-item label="认证失败">{{ count(summary.breakdown.auth) }}</a-descriptions-item>
          <a-descriptions-item label="额度失败">{{ count(summary.breakdown.quota) }}</a-descriptions-item>
          <a-descriptions-item label="进程启动">{{ restartedAt }}</a-descriptions-item>
          <a-descriptions-item label="保留采样点">{{ count(payload.retained) }} / {{ count(payload.capacity) }}</a-descriptions-item>
        </a-descriptions>
        <p class="hint">这些是 API 请求诊断事件，不代表用户数、账务金额或上游调用量。</p>
      </ConsolePanel>

      <ConsolePanel title="逐分钟 API 调用" description="最近 24 小时的分钟级采样；仅展示最近 30 个点。">
        <p v-if="recentPoints.length === 0" class="hint" role="status">暂无采样点，服务启动或重启后会逐分钟积累。</p>
        <div v-else class="table-scroll">
          <table class="status-table">
            <thead><tr><th>时间</th><th>请求</th><th>成功</th><th>失败</th><th>上游</th><th>超时</th><th>认证</th><th>额度</th><th>平均延迟</th></tr></thead>
            <tbody>
              <tr v-for="point in recentPoints" :key="point.minute">
                <td>{{ pointTime(point.minute) }}</td><td>{{ count(point.totalRequests) }}</td><td>{{ count(point.successRequests) }}</td>
                <td>{{ count(point.failedRequests) }}</td><td>{{ count(point.upstreamFailures) }}</td><td>{{ count(point.timeoutFailures) }}</td>
                <td>{{ count(point.authFailures) }}</td><td>{{ count(point.quotaFailures) }}</td><td>{{ latency(point.avgLatencyMs) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </ConsolePanel>

      <SystemMetricsPanel />
    </template>
    <p v-else-if="!errorMessage" class="hint" role="status">点击刷新读取服务器诊断。</p>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import SystemMetricsPanel from '@/pages/SystemSettings/SystemMetricsPanel.vue'
import { getAdminServiceStatus } from '@/api/AdminServiceStatusApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity, useCurrentUserRequestScope } from '@/utils/currentUserScope'

const nonnegative = value => typeof value === 'number' && Number.isFinite(value) && value >= 0
function isSupportedStatus(value) {
  const summary = value?.summary
  return value?.schemaVersion === 1 && Array.isArray(value.points) &&
    summary && ['totalRequests', 'successRequests', 'failedRequests', 'avgLatencyMs', 'windowMinutes'].every(key => nonnegative(summary[key])) &&
    ['upstream', 'timeout', 'auth', 'quota'].every(key => nonnegative(summary.breakdown?.[key])) &&
    value.points.every(point => ['minute', 'totalRequests', 'successRequests', 'failedRequests', 'avgLatencyMs', 'upstreamFailures', 'timeoutFailures', 'authFailures', 'quotaFailures'].every(key => nonnegative(point[key])))
}

export default {
  name: 'AdminServiceStatusPage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, SystemMetricsPanel },
  setup() {
    const { currentUser } = useCurrentUserDisplay()
    const canView = computed(() => Boolean(currentUserIdentity(currentUser.value)) && isAdminUser(currentUser.value))
    const payload = shallowRef(null)
    const loading = ref(false)
    const errorMessage = ref('')
    let requestVersion = 0
    const clear = () => {
      requestVersion += 1
      payload.value = null
      loading.value = false
      errorMessage.value = ''
    }
    const { captureDataOwner, canLoadForCurrentUser } = useCurrentUserRequestScope(currentUser, { clear, reset: () => {}, reload: () => { void load() } })
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()
    async function load() {
      if (!canView.value || !canLoadForCurrentUser() || getCurrentUserLogoutEpoch() !== mountedLogoutEpoch || loading.value) return false
      const ticket = ++requestVersion
      const owns = captureDataOwner()
      const isCurrent = () => owns() && ticket === requestVersion && canView.value
      payload.value = null
      errorMessage.value = ''
      loading.value = true
      try {
        const response = await getAdminServiceStatus()
        if (!isCurrent()) return false
        if (Number(response?.status) !== 200) {
          errorMessage.value = Number(response?.status) === 403 ? '无权读取服务器详情，请确认管理员权限。' : '读取服务器详情失败，请稍后重试。'
          return false
        }
        if (!isSupportedStatus(response.data)) {
          errorMessage.value = '服务器详情数据格式暂不受支持，请刷新重试。'
          return false
        }
        payload.value = response.data
        return true
      } catch (error) {
        if (isCurrent()) errorMessage.value = Number(error?.response?.status) === 403 ? '无权读取服务器详情，请确认管理员权限。' : '读取服务器详情失败，请稍后重试。'
        return false
      } finally {
        if (isCurrent()) loading.value = false
      }
    }
    onMounted(() => { if (canView.value) void load() })
    onBeforeUnmount(clear)

    const summary = computed(() => payload.value?.summary || { windowMinutes: 0, successRate: 0, totalRequests: 0, successRequests: 0, failedRequests: 0, avgLatencyMs: 0, breakdown: {} })
    const recentPoints = computed(() => (payload.value?.points || []).slice(-30))
    const statusLabels = { healthy: '正常', degraded: '降级', down: '异常', unknown: '暂无数据' }
    const statusLabel = computed(() => statusLabels[summary.value.status] || '状态未知')
    const tone = computed(() => ({ healthy: 'ok', degraded: 'warn', down: 'danger', unknown: 'muted' }[summary.value.status] || 'muted'))
    const count = value => Number.isFinite(Number(value)) ? Number(value).toLocaleString('zh-CN') : '—'
    const percent = value => `${(Number(value || 0) * 100).toFixed(2).replace(/\.00$/, '')}%`
    const latency = value => `${count(value)} 毫秒`
    const restartedAt = computed(() => {
      const value = payload.value?.restartedAt
      const date = value ? new Date(value) : null
      return date && Number.isFinite(date.getTime()) ? date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' }) : '未知'
    })
    const pointTime = minute => new Date(Number(minute) * 60000).toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
    return { payload, loading, errorMessage, summary, recentPoints, statusLabel, tone, count, percent, latency, restartedAt, pointTime, load }
  },
}
</script>

<style scoped>
.page-alert { margin-bottom: 16px; }
.status-loading { display: flex; align-items: center; justify-content: center; gap: 12px; min-height: 140px; color: var(--console-muted); }
.status-hero { display: flex; align-items: center; gap: 14px; margin-bottom: 18px; }
.status-hero__dot { width: 12px; height: 12px; flex: 0 0 auto; border-radius: 50%; background: var(--console-muted); }
.status-hero[data-status-tone='ok'] .status-hero__dot { background: var(--console-success, #15803d); }
.status-hero[data-status-tone='warn'] .status-hero__dot { background: var(--console-warning, #b45309); }
.status-hero[data-status-tone='danger'] .status-hero__dot { background: var(--console-danger, #b91c1c); }
.status-hero strong { color: var(--console-text); font-size: 20px; }
.status-hero p, .hint { margin: 4px 0 0; color: var(--console-muted); font-size: 13px; line-height: 1.7; }
.status-summary { margin-bottom: 18px; }
.metric-card { display: grid; gap: 8px; padding: 14px; border: 1px solid var(--console-border); border-radius: 8px; }
.metric-card span { color: var(--console-muted); font-size: 13px; }
.metric-card strong { color: var(--console-text); font-size: 22px; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.table-scroll { overflow-x: auto; }
.status-table { width: 100%; border-collapse: collapse; font-variant-numeric: tabular-nums; }
.status-table th, .status-table td { padding: 9px 10px; border-bottom: 1px solid var(--console-border); text-align: right; white-space: nowrap; font-size: 12px; }
.status-table th:first-child, .status-table td:first-child { text-align: left; }
.status-table th { color: var(--console-muted); font-weight: 500; }
.status-table td { color: var(--console-text); }
</style>
