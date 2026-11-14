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

/** 脱敏逐分钟序列端点。同样走 AuthOrAccessToken，普通用户可访问。 */
export const SERVICE_STATUS_SERIES_PATH = '/api/service-status/series'

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

/* ───────────────────── 接口族分桶（search / other）─────────────────────
 *
 * 后端契约：summary 与 series 两个端点**都**返回 endpoints（138 §4.4 判据 S4）：
 *
 *   endpoints: {
 *     search: { totalRequests, successRequests, avgLatencyMs, samples },
 *     other:  { totalRequests, successRequests, avgLatencyMs, samples },
 *   }
 *
 * 公开端点（summary / series）**不给** failedRequests —— 与公开序列点同一条
 * 取舍（见 internal/controller/publicEndpointCounters 的注释）。
 *
 * # samples 是判「有没有数据」的**唯一**依据
 *
 * 两个桶的 samples 语义是「被采样到的分钟数」，不是「有请求的分钟数」。
 * 因此存在这种形态，而它正是本设计的全部理由：
 *
 *   samples > 0 且 totalRequests === 0   → 采样到了，这套接口确实没人用
 *                                          （服务正常，绝不是「挂掉」）
 *   samples === 0 且 totalRequests === 0 → 根本没采样（服务刚重启 / 采样未接线）
 *
 * 两者的 totalRequests 都是 0，只有 samples 能分开。所以：
 *   - 判「显灰」只能用 samples
 *   - 判「能不能显示成功率」必须同时看 samples>0 **且** totalRequests>0
 *     （采样到 0 请求时也算不出成功率，但那是「没人用」而不是「没数据」，
 *     不能显灰、也不能显示 0%）
 */

/** 置灰时展示的文案（字面量，页面与测试共用同一句，避免两处漂移）。 */
export const SERVICE_STATUS_ENDPOINT_EMPTY_HINT = '暂无搜题接口采样数据（服务重启后从零开始）'

/** 接口族 → 用户可见标题。只允许后端固定的两个枚举值。 */
const ENDPOINT_PRESENTATION = {
  search: { label: '搜题接口', key: 'search' },
  other: { label: '其它接口', key: 'other' },
}

/**
 * 把一个接口族桶压成视图模型。
 *
 * 参数顺序是 (kind, bucket)：kind 只允许后端固定的两个枚举值（'search' / 'other'），
 * 未知取值走 search 之外的兜底标题但不影响三态判定 —— 前端**不**允许凭 kind
 * 编造第三种桶（那会让「固定枚举」在消费端失守）。
 *
 * 返回的 state 是**三态**，不是布尔（合并三态会让两种相反情况共用一个渲染分支）：
 *
 *   'unsampled' → samples === 0：没有任何采样数据 → 置灰 + 不可显示任何百分比
 *   'idle'      → samples > 0 但 totalRequests === 0：采样到了、这套接口没人用
 *                 → 不置灰，但成功率是 '--'（算不出来，也**绝不能**写成 0%）
 *   'measured'  → samples > 0 且 totalRequests > 0：成功率是真实值（含 0%，即全失败）
 *
 * 注意 rateText 的三种取值语义：
 *   '--'（unsampled / idle 或字段非法）、真实百分比（measured）。
 *   measured 且成功率为 0 时是 '0%' —— 这是异常态，页面必须**不置灰**地展示它。
 */
export function buildEndpointView(kind, bucket) {
  const presentation = ENDPOINT_PRESENTATION[kind] || { label: String(kind || '未知接口'), key: String(kind || 'unknown') }

  // 缺桶 / 桶不是对象 → 按「没有采样数据」处理（而不是当成 0，见上方注释）。
  if (!bucket || typeof bucket !== 'object' || Array.isArray(bucket)) {
    return {
      key: presentation.key,
      label: presentation.label,
      state: 'unsampled',
      greyed: true,
      samples: 0,
      totalRequests: 0,
      successRequests: 0,
      totalText: '--',
      successText: '--',
      latencyText: '--',
      samplesText: '0',
      rateText: '--',
      hint: SERVICE_STATUS_ENDPOINT_EMPTY_HINT,
    }
  }

  const samples = readNonNegativeInteger(bucket.samples)
  const total = readNonNegativeInteger(bucket.totalRequests)
  const success = readNonNegativeInteger(bucket.successRequests)
  const latency = readNonNegativeInteger(bucket.avgLatencyMs)

  // samples 字段缺失/非法 → 视为「没有采样数据」。这条是刻意的 fail-closed：
  // 宁可显灰（用户知道「暂时没数据」），也不要拿一个猜出来的成功率去误导他。
  const sampled = samples !== null && samples > 0
  const measured = sampled && total !== null && total > 0

  let state = 'unsampled'
  if (measured) state = 'measured'
  else if (sampled) state = 'idle'

  const rate = measured ? success / total : null

  return {
    key: presentation.key,
    label: presentation.label,
    state,
    greyed: !sampled,
    samples: samples === null ? 0 : samples,
    totalRequests: total === null ? 0 : total,
    successRequests: success === null ? 0 : success,
    totalText: formatCount(total),
    successText: formatCount(success),
    latencyText: measured ? formatLatency(latency) : '--',
    samplesText: formatCount(samples),
    rateText: rate === null ? '--' : formatSuccessRate(rate),
    hint: !sampled
      ? SERVICE_STATUS_ENDPOINT_EMPTY_HINT
      : (measured ? '' : `已采样 ${formatCount(samples)} 分钟，但最近窗口内没有搜题请求。`),
  }
}

/**
 * 从一份响应体里取分桶视图。
 *
 * endpoints 缺失（旧后端 / 字段被删）→ 返回 search 桶的 unsampled 视图，
 * 而不是抛异常：前端必须在字段缺失时仍能渲染整页（只是那一块显灰）。
 */
export function buildServiceStatusEndpointsView(payload) {
  const endpoints = payload && typeof payload === 'object' && !Array.isArray(payload)
    ? payload.endpoints
    : null
  const buckets = endpoints && typeof endpoints === 'object' ? endpoints : {}
  return {
    search: buildEndpointView('search', buckets.search),
    other: buildEndpointView('other', buckets.other),
  }
}

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
      // 响应体不支持时也要给出可渲染的分桶视图（全 unsampled、显灰），
      // 否则模板访问 view.endpoints.search 会抛 undefined 异常整页崩。
      endpoints: buildServiceStatusEndpointsView(null),
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
    // 接口族分桶（138 §4.2.2 / S4）。注意它**独立于** dataState：
    // 顶层可能 measured（other 有流量），而 search 桶 unsampled（还没接采样）。
    endpoints: buildServiceStatusEndpointsView(payload),
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

/* ───────────────────────── 逐分钟序列（脱敏） ───────────────────────── */

/** 单个序列点允许的字段（与后端 controller.publicStatusPoint 对齐的白名单）。 */
const SERIES_POINT_FIELDS = ['minute', 'totalRequests', 'successRequests', 'avgLatencyMs']

/**
 * 校验一份 series 响应体。
 *
 * 刻意**不**接受 failedRequests / 四个失败分类 —— 后端已经不给，
 * 这里再挡一遍是为了「万一后端有一天加回来」，前端也不会渲染它。
 * 这条与后端白名单测试是同一个判据的两端。
 */
export function isSupportedServiceStatusSeries(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  if (readFiniteNumber(value.schemaVersion) !== 1) return false
  if (!Array.isArray(value.points)) return false
  return value.points.every((point) => {
    if (!point || typeof point !== 'object' || Array.isArray(point)) return false
    return SERIES_POINT_FIELDS.every((field) => readNonNegativeInteger(point[field]) !== null)
  })
}

/**
 * 把 series 响应体转成可渲染的曲线数据；不支持则返回 null（页面退回摘要视图）。
 *
 * 返回 null 而不是空数组：调用方要能区分「端点失败/格式不认」（不画曲线）
 * 与「端点正常但窗口内确实没有点」（画一条空态说明）。混成空数组会让
 * 后者被当成前者，用户看不出「服务刚重启还没采样」。
 */
export function buildServiceStatusSeriesView(payload) {
  if (!isSupportedServiceStatusSeries(payload)) return null

  const points = payload.points.map((point) => {
    const total = readNonNegativeInteger(point.totalRequests)
    const success = readNonNegativeInteger(point.successRequests)
    const minute = readNonNegativeInteger(point.minute)
    // 成功率按点重算：后端不传 failedRequests，这里也不需要它。
    // total=0 的点不该出现（后端跳过空分钟），真出现就按「无请求」处理，不当成 0%。
    const rate = total > 0 ? success / total : null
    return {
      minute,
      totalRequests: total,
      successRequests: success,
      avgLatencyMs: readNonNegativeInteger(point.avgLatencyMs),
      successRate: rate,
      rateText: rate === null ? '--' : formatSuccessRate(rate),
      timeText: minute === null ? '--' : formatMinuteLabel(minute),
      latencyText: formatLatency(point.avgLatencyMs),
      totalText: formatCount(total),
      // 柱高按「该点请求数 / 本序列最大请求数」，便于画色带；
      // 全为 0 时没有可比较的基准，统一给 0。
      barHeight: 0,
    }
  })

  const peak = points.reduce((max, point) => Math.max(max, point.totalRequests), 0)
  for (const point of points) {
    point.barHeight = peak > 0 ? Math.round((point.totalRequests / peak) * 100) : 0
  }

  return {
    points,
    capacityText: formatCount(payload.capacity),
    retainedText: formatCount(payload.retained),
    restartedAtText: formatRestartedAt(payload.restartedAt),
    // 空序列用真实标志，不用 points.length 猜 —— 调用方据此显示「刚重启、还没采样」。
    isEmpty: points.length === 0,
    // S4：series 端点也返回分桶（后端 serviceStatusSeriesResponse.endpoints），
    // 所以视图也要带出来。页面用它回答「曲线里那些红点来自哪个接口族」——
    // 只有 summary 带分桶时，用户会看到「曲线全红而摘要说搜题正常」的矛盾。
    endpoints: buildServiceStatusEndpointsView(payload),
  }
}

/** Unix 分钟数 → HH:MM（上海时区）。非法值返回 '--'。 */
export function formatMinuteLabel(minute) {
  const parsed = readFiniteNumber(minute)
  if (parsed === null || parsed < 0) return '--'
  const date = new Date(parsed * 60 * 1000)
  if (!Number.isFinite(date.getTime())) return '--'
  return date.toLocaleTimeString('zh-CN', {
    hour: '2-digit', minute: '2-digit', hour12: false, timeZone: 'Asia/Shanghai',
  })
}

/**
 * 请求 series 的唯一入口。
 *
 * 失败一律返回 ok:false 且 series:null，**不抛异常** ——
 * 页面必须在「曲线拿不到」时仍然显示摘要，不能整页崩。
 * 这里也**不** fallback 到 admin 端点（普通用户 403）。
 */
export async function loadServiceStatusSeries(fetchSeries) {
  if (typeof fetchSeries !== 'function') {
    return { ok: false, status: 0, message: describeServiceStatusFailure(0), series: null }
  }
  let response
  try {
    response = await fetchSeries(SERVICE_STATUS_SERIES_PATH)
  } catch (error) {
    const status = resolveFailureStatus(error)
    return { ok: false, status, message: describeServiceStatusFailure(status), series: null }
  }
  const status = resolveFailureStatus(response)
  if (status !== 200) {
    return { ok: false, status, message: describeServiceStatusFailure(status), series: null }
  }
  const data = response?.data ?? null
  const series = buildServiceStatusSeriesView(data)
  if (!series) {
    return { ok: false, status, message: '服务状态曲线数据格式暂不受支持。', series: null }
  }
  return { ok: true, status, message: '', series }
}
