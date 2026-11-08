<template>
  <ConsolePanel
    v-if="canView"
    title="运行诊断"
    description="当前服务进程的只读聚合快照，便于排查请求与执行状态。"
    class="system-metrics-panel"
  >
    <template #actions>
      <a-button data-action="refresh-system-metrics" html-type="button" :loading="loading" @click="handleRefresh">刷新诊断</a-button>
    </template>
    <a-alert
      type="info" show-icon class="metrics-notice"
      message="诊断事件，不是账务凭证/收入"
      description="仅统计当前进程，进程重启后清零；首次读取后仅手动刷新，不轮询。不提供排队容量或持久化历史，也不会调用模型服务。"
    />
    <RequestFailureAlert v-if="errorText && failure" :failure="failure" :message="errorText"
      :retryable="true" :busy="loading" :context-key="failureContext" :is-current="isCurrentFailure" @retry="retryFailure" />
    <div v-if="loading" class="metrics-loading" role="status" aria-live="polite" aria-busy="true">
      <a-spin /><span>正在读取运行诊断…</span>
    </div>
    <template v-else-if="metrics">
      <p class="metrics-meta" role="status">进程启动：{{ startedAtText }} · 已运行 {{ count(metrics.uptimeSeconds) }} 秒 · 本次读取：{{ sampledAt }}</p>
      <ConsoleSummaryGrid aria-label="当前进程运行诊断摘要">
        <article class="metrics-card">
          <h3>HTTP 请求</h3>
          <dl>
            <div><dt>已进入请求</dt><dd data-metric="http-total">{{ count(metrics.http.total) }}</dd></div>
            <div><dt>正在处理</dt><dd data-metric="http-in-flight">{{ count(metrics.http.inFlight) }}</dd></div>
            <div><dt>失败响应</dt><dd data-metric="http-failed">{{ count(metrics.http.failed) }}</dd></div>
          </dl>
        </article>
        <article class="metrics-card">
          <h3>已完成请求耗时</h3>
          <dl>
            <div><dt>耗时样本</dt><dd data-metric="latency-count">{{ count(metrics.http.latencyCount) }}</dd></div>
            <div><dt>平均耗时</dt><dd data-metric="latency-mean">{{ meanLatency }}</dd></div>
            <div><dt>最大耗时</dt><dd data-metric="latency-max">{{ maxLatency }}</dd></div>
          </dl>
        </article>
        <article v-for="entry in cacheEntries" :key="entry.key" class="metrics-card">
          <h3>{{ entry.label }}缓存</h3>
          <dl>
            <div><dt>命中</dt><dd :data-metric="`cache-${entry.key}-hits`">{{ count(entry.value.hits) }}</dd></div>
            <div><dt>未命中</dt><dd :data-metric="`cache-${entry.key}-misses`">{{ count(entry.value.misses) }}</dd></div>
            <div><dt>共享执行复用</dt><dd :data-metric="`cache-${entry.key}-shared`">{{ count(entry.value.shared) }}</dd></div>
          </dl>
        </article>
        <article class="metrics-card">
          <h3>结算调用事件</h3>
          <dl>
            <div><dt>调用尝试</dt><dd data-metric="billing-attempts">{{ count(metrics.billing.attempts) }}</dd></div>
            <div><dt>调用成功</dt><dd data-metric="billing-successes">{{ count(metrics.billing.successes) }}</dd></div>
            <div><dt>调用失败</dt><dd data-metric="billing-failures">{{ count(metrics.billing.failures) }}</dd></div>
          </dl>
          <p class="metrics-help">仅非空收费批次的事务调用事件，不代表扣费笔数、到账金额或收入。</p>
        </article>
        <article v-for="entry in taskEntries" :key="entry.key" class="metrics-card">
          <h3>{{ entry.label }}任务</h3>
          <dl>
            <div><dt>当前保留任务</dt><dd :data-metric="`tasks-${entry.key}-total`">{{ count(entry.value.total) }}</dd></div>
            <div><dt>正在运行</dt><dd :data-metric="`tasks-${entry.key}-running`">{{ count(entry.value.running) }}</dd></div>
            <div><dt>已成功</dt><dd :data-metric="`tasks-${entry.key}-succeeded`">{{ count(entry.value.succeeded) }}</dd></div>
            <div><dt>已失败</dt><dd :data-metric="`tasks-${entry.key}-failed`">{{ count(entry.value.failed) }}</dd></div>
          </dl>
        </article>
      </ConsoleSummaryGrid>
      <p v-if="!metrics.tasks" class="metrics-help">当前接口未提供任务摘要；不将未知状态填成零。</p>
      <p class="metrics-help">失败响应按 HTTP 状态码 ≥400 统计。此处搜索/回退缓存统计结果复用，不是上游模型输入 Token 缓存命中率。请求、缓存、结算事件与当前保留任务采用不同统计口径，不能相加推算用户数、排队数或账单金额。</p>
    </template>
    <p v-else-if="!errorText" class="metrics-help" role="status">点击刷新读取当前进程诊断。</p>
  </ConsolePanel>
</template>

<script>
import { computed, onMounted, ref, shallowRef } from 'vue'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import { getSystemMetrics } from '@/api/TikuComponentApi'
import { isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity, useCurrentUserRequestScope } from '@/utils/currentUserScope'

const nonnegative = value => typeof value === 'number' && Number.isFinite(value) && value >= 0
const hasCounts = (value, keys) => value && keys.every(key => nonnegative(value[key]))
function supportedSnapshot(value) {
  return value?.schemaVersion === 1 && typeof value.startedAt === 'string' && Number.isFinite(Date.parse(value.startedAt)) &&
    nonnegative(value.uptimeSeconds) && hasCounts(value.http, ['total', 'inFlight', 'failed', 'latencyCount', 'latencyTotalMillis', 'latencyMaxMillis']) &&
    Array.isArray(value.http.buckets) && value.http.buckets.every(bucket => hasCounts(bucket, ['upperMillis', 'count'])) &&
    hasCounts(value.cache?.search, ['hits', 'misses', 'shared']) && hasCounts(value.cache?.fallback, ['hits', 'misses', 'shared']) &&
    hasCounts(value.billing, ['attempts', 'successes', 'failures']) &&
    (value.tasks === undefined || (hasCounts(value.tasks?.search, ['total', 'running', 'succeeded', 'failed']) && hasCounts(value.tasks?.fallback, ['total', 'running', 'succeeded', 'failed'])))
}
function readError(status) {
  if (Number(status) === 403) return '无权读取运行诊断，请确认管理员权限后重试。'
  if (Number(status) === 401) return '当前会话无法读取运行诊断，请确认登录状态后重试。'
  return '读取运行诊断失败，请稍后重试。'
}

export default {
  name: 'SystemMetricsPanel',
  components: { RequestFailureAlert, ConsolePanel, ConsoleSummaryGrid },
  setup() {
    const { currentUser } = useCurrentUserDisplay()
    const canView = computed(() => Boolean(currentUserIdentity(currentUser.value)) && isAdminUser(currentUser.value))
    const metrics = ref(null)
    const loading = ref(false)
    const errorText = ref('')
    const failure = shallowRef(null)
    const failureContext = ref(0)
    const sampledAt = ref('')
    let latest = 0
    const clear = () => {
      latest += 1
      failure.value = null
      failureContext.value = latest
      metrics.value = null
      loading.value = false
      errorText.value = ''
      sampledAt.value = ''
    }
    const { captureDataOwner, canLoadForCurrentUser } = useCurrentUserRequestScope(currentUser, {
      clear, reset: () => {}, reload: () => {},
    })
    async function handleRefresh() {
      // Display identity is only a local read guard. The server still authorizes every GET.
      if (!canView.value || !canLoadForCurrentUser()) return
      const ticket = ++latest
      failure.value = null
      failureContext.value = ticket
      const ownsData = captureDataOwner()
      const isCurrent = () => ownsData() && ticket === latest && canView.value
      metrics.value = null
      sampledAt.value = ''
      errorText.value = ''
      loading.value = true
      try {
        const response = await getSystemMetrics()
        if (!isCurrent()) return
        if (Number(response?.status) !== 200) {
          failure.value = ensureRequestFailure(response)
          errorText.value = readError(response?.status)
          return
        }
        if (!supportedSnapshot(response.data)) {
          failure.value = ensureRequestFailure(response)
          errorText.value = '诊断数据格式暂不受支持，请刷新重试。'
          return
        }
        metrics.value = response.data
        sampledAt.value = new Date().toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
      } catch (error) {
        if (isCurrent()) { failure.value = ensureRequestFailure(error); errorText.value = readError(error?.response?.status) }
      } finally {
        if (isCurrent()) loading.value = false
      }
    }
    function isCurrentFailure(source, context) {
      return failure.value === source && failureContext.value === context && canView.value && canLoadForCurrentUser() && !loading.value
    }
    function retryFailure(source, context) { if (isCurrentFailure(source, context)) return handleRefresh() }
    // No polling or automatic reads after identity/role changes; re-entry and refresh are explicit.
    onMounted(() => { handleRefresh() })
    const count = value => value.toLocaleString('zh-CN')
    const startedAtText = computed(() => metrics.value ? new Date(metrics.value.startedAt).toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' }) : '')
    const meanLatency = computed(() => metrics.value?.http.latencyCount > 0 ? `${(metrics.value.http.latencyTotalMillis / metrics.value.http.latencyCount).toFixed(1)} ms` : '暂无耗时样本')
    const maxLatency = computed(() => metrics.value?.http.latencyCount > 0 ? `${metrics.value.http.latencyMaxMillis.toFixed(1)} ms` : '暂无耗时样本')
    const cacheEntries = computed(() => metrics.value ? [
      { key: 'search', label: '搜索', value: metrics.value.cache.search },
      { key: 'fallback', label: '回退', value: metrics.value.cache.fallback },
    ] : [])
    const taskEntries = computed(() => metrics.value?.tasks ? [
      { key: 'search', label: '搜索', value: metrics.value.tasks.search },
      { key: 'fallback', label: '回退', value: metrics.value.tasks.fallback },
    ] : [])
    return { canView, metrics, loading, errorText, failure, failureContext, isCurrentFailure, retryFailure, sampledAt, handleRefresh, count, startedAtText, meanLatency, maxLatency, cacheEntries, taskEntries }
  },
}
</script>

<style scoped>
.system-metrics-panel { margin-bottom: 24px; min-width: 0; }
.metrics-notice { margin-bottom: 16px; }
.metrics-meta, .metrics-help { color: var(--console-muted); font-size: 13px; overflow-wrap: anywhere; }
.metrics-meta { margin: 0 0 16px; }
.metrics-loading { display: flex; align-items: center; justify-content: center; gap: 12px; min-height: 140px; color: var(--console-muted); }
.metrics-card h3 { margin: 0 0 16px; font-size: 15px; color: var(--console-text); }
.metrics-card dl { display: grid; gap: 10px; margin: 0; }
.metrics-card dl > div { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 4px 12px; }
.metrics-card dt { color: var(--console-muted); font-size: 13px; }
.metrics-card dd { margin: 0; color: var(--console-text); font-size: 16px; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.metrics-card .metrics-help { margin: 12px 0 0; }
@media (max-width: 768px) { .system-metrics-panel { margin-bottom: 16px; } .metrics-loading { align-items: center; flex-wrap: wrap; } }
</style>
