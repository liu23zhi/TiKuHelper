/* 服务状态页的纯视图模型（可直接在 node 里测，不需要 mount Vue）。
 *
 * 为什么把判定抽到 .mjs 而不是写在 index.vue 的 computed 里：
 * 本页最关键的一条语义是「全失败 ≠ 无数据」——
 *
 *   successRateAvailable === false            → 窗口内没有请求，算不出成功率 → 「暂无采样数据」
 *   successRateAvailable === true, 成功率 0%  → 确实测到了请求且全失败 → 异常态（要报警）
 *
 * 这两种情况在 successRate 数值上**完全相同**（都是 0），只靠数字分不开。
 * 如果判定写在 SFC 的 computed 里，单元测试只能靠读源码字符串断言，测不出行为；
 * 抽成纯函数后可以喂真实响应体，把它变成可执行的行为断言，也才可以做变异验证。
 *
 * 字段口径全部对齐 internal/controller/service_status.go 的
 * serviceStatusSummaryResponse（GET /api/service-status/summary）：
 *   schemaVersion / status / successRate / successRateAvailable /
 *   totalRequests / successRequests / failedRequests / avgLatencyMs /
 *   windowMinutes / retained / capacity / restartedAt
 *
 * ⚠️ 该端点**不返回 points**（逐分钟明细只在管理员的 /api/admin/service-status）。
 * 普通用户请求 admin 端点会 403，所以本页永远不去请求它 —— 见 loadServiceStatusSummary。
 */

/** 服务状态端点路径。普通用户可访问（后端走 AuthOrAccessToken）。 */
export const SERVICE_STATUS_SUMMARY_PATH = '/api/service-status/summary'

/** 管理员端点。**本页不得请求**：普通用户 403，且那是运维级逐分钟明细。 */
export const SERVICE_STATUS_ADMIN_PATH = '/api/admin/service-status'

/** 状态取值 → 用户可见文案与语义色。 */
const STATUS_PRESENTATION = {
  healthy: { label: '正常', tone: 'ok', icon: 'check' },
  degraded: { label: '降级', tone: 'warn', icon: 'warning' },
  down: { label: '异常', tone: 'danger', icon: 'close' },
  unknown: { label: '暂无数据', tone: 'muted', icon: 'question' },
}

/** 未知 status 取值时的兜底：不能当成正常，也不谎称异常。 */
const UNKNOWN_PRESENTATION = { label: '未知状态', tone: 'muted', icon: 'question' }

function readFiniteNumber(value) {
  if (typeof value === 'number') return Number.isFinite(value) ? value : null
  if (typeof value === 'string' && value.trim() !== '') {
    const parsed = Number(value)
    return Number.isFinite(parsed) ? parsed : null
  }
  return null
}

function readNonNegativeInteger(value) {
  const parsed = readFiniteNumber(value)
  if (parsed === null || parsed < 0) return null
  return Math.trunc(parsed)
}

/**
 * 判断一份响应体是不是可渲染的 summary。
 *
 * 判据刻意不看 status 字符串（未知取值也照常渲染，只是文案走兜底），
 * 只看「结构化字段是否能被安全读取」—— 格式不认识时应该报「格式不受支持」而不是画出错误数字。
 */
export function isSupportedServiceStatusSummary(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  if (readFiniteNumber(value.schemaVersion) !== 1) return false
  if (typeof value.successRateAvailable !== 'boolean') return false
  for (const field of ['totalRequests', 'successRequests', 'failedRequests', 'avgLatencyMs']) {
    if (readNonNegativeInteger(value[field]) === null) return false
  }
  if (readNonNegativeInteger(value.windowMinutes) === null) return false
  return true
}

/** 把 0–1 的分数格式化成百分数，去掉无意义的尾零：0.9923 → "99.23%"，1 → "100%"。 */
export function formatSuccessRate(rate) {
  const parsed = readFiniteNumber(rate)
  if (parsed === null) return '--'
  const percent = Number((parsed * 100).toFixed(2))
  if (!Number.isFinite(percent)) return '--'
  return `${String(percent).replace(/(\.\d*?)0+$/, '$1').replace(/\.$/, '')}%`
}

/** 毫秒延迟：>=1000 时用秒并保留一位小数，读起来比四位数毫秒直观。 */
export function formatLatency(milliseconds) {
  const parsed = readFiniteNumber(milliseconds)
  if (parsed === null || parsed < 0) return '--'
  if (parsed >= 1000) return `${(parsed / 1000).toFixed(1)} 秒`
  return `${Math.round(parsed)} 毫秒`
}

/** 计数：加千分位。非法值返回 '--'，不伪造 0。 */
export function formatCount(value) {
  const parsed = readNonNegativeInteger(value)
  if (parsed === null) return '--'
  return parsed.toLocaleString('zh-CN')
}

/** restartedAt（RFC3339）→ 本地可读时间。解析失败返回 ''，由调用方决定怎么提示。 */
export function formatRestartedAt(value) {
  if (typeof value !== 'string' || value.trim() === '') return ''
  const parsed = Date.parse(value)
  if (!Number.isFinite(parsed)) return ''
  return new Date(parsed).toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
}

/**
 * 把 summary 响应体转成模板直接可用的视图模型。
 *
 * 这是本文件的核心，也是「全失败 ≠ 无数据」唯一判定点。
 * 返回值的 dataState 是三态，而不是布尔：
 *   'empty'     → 窗口内没有任何请求，成功率算不出来（successRateAvailable=false）
 *   'measured'  → 确实测到了请求，rateText 是真实成功率（含 0%，即全失败）
 *   'unknown'   → 响应体格式不受支持，什么都不该显示
 *
 * 注意 dataState='measured' 且 successRate=0 时：
 *   - rateText 必须是 '0%'
 *   - emptyHint 必须是 ''（不能出现「暂无采样数据」）
 *   - 状态语义由后端 status 决定（此时后端给 down），而不是被前端改写成 unknown
 */
export function buildServiceStatusView(payload) {
  if (!isSupportedServiceStatusSummary(payload)) {
    return {
      supported: false,
      dataState: 'unknown',
      status: 'unknown',
      statusLabel: UNKNOWN_PRESENTATION.label,
      tone: UNKNOWN_PRESENTATION.tone,
      rateText: '--',
      successRateText: '--',
      latencyText: '--',
      totalText: '--',
      failedText: '--',
      successText: '--',
      windowText: '--',
      retainedText: '--',
      capacityText: '--',
      restartedAtText: '',
      emptyHint: '',
      showBreakdown: false,
    }
  }

  const available = payload.successRateAvailable === true
  const successRate = readFiniteNumber(payload.successRate)
  // ⚠️ 关键：可用性只看 successRateAvailable，**绝不**用 successRate !== 0 反推。
  // 全失败时 successRate 恰为 0，用数值判会把最该报警的状态显示成「暂无数据」。
  const measured = available && successRate !== null

  const statusKey = typeof payload.status === 'string' ? payload.status : ''
  const presentation = STATUS_PRESENTATION[statusKey] || UNKNOWN_PRESENTATION

  const total = readNonNegativeInteger(payload.totalRequests)
  const failed = readNonNegativeInteger(payload.failedRequests)
  const success = readNonNegativeInteger(payload.successRequests)
  const windowMinutes = readNonNegativeInteger(payload.windowMinutes)
  const retained = readNonNegativeInteger(payload.retained)
  const capacity = readNonNegativeInteger(payload.capacity)

  // 无数据时状态词取「暂无数据」，即使后端 status 是 unknown 也是这个文案；
  // 有数据时用后端推导的状态词（后端才是阈值判定的唯一权威）。
  const statusLabel = measured ? presentation.label : '暂无采样数据'

  return {
    supported: true,
    dataState: measured ? 'measured' : 'empty',
    status: measured ? (statusKey || 'unknown') : 'empty',
    statusLabel,
    tone: measured ? presentation.tone : 'muted',
    rateText: measured ? formatSuccessRate(successRate) : '--',
    successRateText: measured ? formatSuccessRate(successRate) : '--',
    latencyText: formatLatency(payload.avgLatencyMs),
    totalText: formatCount(total),
    failedText: formatCount(failed),
    successText: formatCount(success),
    windowText: windowMinutes === null ? '--' : `最近 ${windowMinutes} 分钟`,
    retainedText: formatCount(retained),
    capacityText: formatCount(capacity),
    restartedAtText: formatRestartedAt(payload.restartedAt),
    // 无数据时明确说「窗口内没有请求」，而不是让用户对着 0% 猜。
    emptyHint: measured ? '' : '最近窗口内没有可供统计的请求，采样数据积累后会显示成功率。',
    // 失败分解（upstream/timeout/auth/quota）只在 admin 端点的 summary 里有；
    // summary 端点不返回，所以这里恒为 false，保留字段是为了将来复用。
    showBreakdown: false,
  }
}

/** HTTP 状态码 → 面向普通用户的提示。401/403 分开说，其它归为通用失败。 */
export function describeServiceStatusFailure(status) {
  const code = Number(status)
  if (code === 401) return '登录状态已失效，请重新登录后查看。'
  if (code === 403) return '当前账号无权查看服务状态。'
  if (code === 404) return '服务状态接口暂不可用，请稍后重试。'
  if (code >= 500) return '服务状态接口暂时异常，请稍后重试。'
  return '服务状态读取失败，请稍后重试。'
}

/**
 * 从一次请求结果里取出来源状态码。
 * 兼容三种形态：axios 响应（.status）、异常对象（.response.status）、以及裸数字。
 */
export function resolveFailureStatus(input) {
  if (typeof input === 'number') return input
  if (input && typeof input === 'object') {
    if (typeof input.status === 'number') return input.status
    if (typeof input.response?.status === 'number') return input.response.status
  }
  return 0
}

/**
 * 请求 summary 的唯一入口。
 *
 * 刻意把 fetch 注入进来：
 *   1) 测试可以断言「只请求了 summary 路径、没请求 admin 路径」而不需要真起服务；
 *   2) 页面里传 utils/request（带 cookie 与统一错误处理），测试里传桩函数。
 *
 * 绝不 fallback 到 SERVICE_STATUS_ADMIN_PATH —— 普通用户会拿 403，
 * 而且逐分钟明细本就是运维数据，不该因为「摘要拿不到」而降级去要。
 */
export async function loadServiceStatusSummary(fetchSummary) {
  if (typeof fetchSummary !== 'function') {
    return { ok: false, status: 0, message: describeServiceStatusFailure(0), data: null }
  }
  let response
  try {
    response = await fetchSummary(SERVICE_STATUS_SUMMARY_PATH)
  } catch (error) {
    const status = resolveFailureStatus(error)
    return { ok: false, status, message: describeServiceStatusFailure(status), data: null }
  }
  const status = resolveFailureStatus(response)
  if (status !== 200) {
    return { ok: false, status, message: describeServiceStatusFailure(status), data: null }
  }
  const data = response?.data ?? null
  if (!isSupportedServiceStatusSummary(data)) {
    return { ok: false, status, message: '服务状态数据格式暂不受支持，请稍后重试。', data: null }
  }
  return { ok: true, status, message: '', data }
}
