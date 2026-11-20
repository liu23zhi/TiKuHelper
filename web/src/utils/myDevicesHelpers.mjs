// 「我的设备」页的纯函数层（137-设备体系设计 §9.1 / 批次 A2-5）。
//
// 为什么要单独一层 .mjs 而不是全写在 index.vue 里：
//   1. `npm run test:unit` 只跑 `src/utils/*.test.mjs`（package.json 的 test:unit 脚本），
//      写在 .vue 里就没有任何单测入口 —— 而这一页的判据（上限上下界、状态映射、
//      时间文案）全是**纯计算**，不需要 DOM 就能测。
//   2. 上限校验必须与后端 `/user/device-limit` 的 400 文案同口径。两处若各写一份，
//      会出现「前端放行、后端 400」的可见缺陷；共用一个纯函数就只有一个口径。
//
// 三条纪律：
//   - **不引用后端的常量字符串**（这里写的是字面量）：后端常量漂移时，
//     前端测试应当红，而不是跟着一起漂移成恒真。
//   - 所有函数对「缺字段/类型不对」都返回**显式的未知态**，不伪造 0/空串。
//     把「读取失败」显示成「0 台」会让用户以为自己的上限变成了 0。
//   - 不做任何请求。请求在 index.vue，注入点便于测试替换。

/** 状态 → 界面元信息（137 §9.1 的三态：使用中 / 已移除 / 已拉黑）。 */
export const DEVICE_STATUS_META = {
  active: { text: '使用中', tone: 'success', canRemove: true, canBlock: true, canUnblock: false },
  removed: { text: '已移除', tone: 'muted', canRemove: false, canBlock: false, canUnblock: false },
  blocked: { text: '已拉黑', tone: 'danger', canRemove: false, canBlock: false, canUnblock: true },
}

const FALLBACK_STATUS_META = { text: '未知', tone: 'muted', canRemove: false, canBlock: false, canUnblock: false }

/** deviceStatusMeta 取状态元信息；未知状态返回「未知」而不是猜一个。 */
export function deviceStatusMeta(status) {
  return DEVICE_STATUS_META[String(status || '')] || FALLBACK_STATUS_META
}

/** readInt 读一个整数字段；缺失/非法返回 null（**不**返回 0）。 */
export function readInt(value) {
  if (typeof value === 'number') return Number.isInteger(value) ? value : null
  if (typeof value !== 'string') return null
  const text = value.trim()
  if (text === '') return null
  if (!/^-?\d+$/.test(text)) return null
  const parsed = Number(text)
  return Number.isSafeInteger(parsed) ? parsed : null
}

/**
 * normalizeDeviceLimit 把后端 `GET /user/device-limit` 的 data 归一化。
 *
 * 关键点：`upperBound` 为 null 表示「等级不限，无上界」；它不是「未知」，
 * 所以不能退回默认值。用 `hasUpperBound` 显式区分两种 nil。
 */
// unwrapDevicePayload 剥掉后端 `{data: {...}}` 信封。
//
// 为什么需要它（真实缺陷，2026-11-16）：
//   后端 controller/device_user.go 两个接口都是 c.JSON(200, gin.H{"data": view})，
//   而传输层 buildResponsePayload（requestHelpers.mjs）把**整个响应体**放进 payload.data：
//       const payload = { data: response?.data, ... }
//   ⇒ 页面里 res.data === {data: view}，真正的字段在 res.data.data。
//   原实现直接读 res.data.limit / res.data.devices，全是 undefined，于是：
//     · 上限显示「未知」/来源「未知」/输入框禁用
//     · 设备列表显示「还没有设备」
//   两个现象同源，只修一个会留下另一个。
//
// 判据：**只在顶层缺少本函数关心的字段时才深入 data**。
// 不能无条件 `raw.data || raw` —— 一个恰好同时有顶层 limit 与无关 data 字段的
// 对象会被错误地降级到 data 里读，把合法值覆盖掉。
export function unwrapDevicePayload(raw, probeKeys) {
  const source = raw && typeof raw === 'object' ? raw : {}
  const keys = Array.isArray(probeKeys) ? probeKeys : []
  // 顶层已经带任一关心的字段 → 视为已解包，不再下钻。
  if (keys.some((key) => source[key] !== undefined)) return source
  const inner = source.data
  if (inner && typeof inner === 'object' && !Array.isArray(inner)) return inner
  return source
}
export function normalizeDeviceLimit(raw) {
  const data = unwrapDevicePayload(raw, ['limit', 'source', 'unlimited', 'upperBound'])
  const upperRaw = data.upperBound
  const hasUpperBound = upperRaw !== null && upperRaw !== undefined
  const upper = hasUpperBound ? readInt(upperRaw) : null
  const limit = readInt(data.limit)
  const override = readInt(data.override)
  const levelLimit = readInt(data.levelLimit)
  return {
    loaded: true,
    limit,
    // 上限"已算出"才显示数字；否则显示「未知」并禁用输入。
    limitKnown: limit !== null,
    unlimited: data.unlimited === true,
    source: typeof data.source === 'string' ? data.source : '',
    sourceText: deviceLimitSourceText(data.source),
    override,
    levelLimit,
    levelName: typeof data.levelName === 'string' ? data.levelName : '',
    hasUpperBound: hasUpperBound && upper !== null,
    upperBound: hasUpperBound ? upper : null,
    editable: data.editable === true,
    hookReady: data.hookReady !== false,
  }
}

/** deviceLimitMaximumText 展示等级允许设置的最高上限，与当前生效值分开计算。 */
export function deviceLimitMaximumText(view) {
  const state = view && typeof view === 'object' ? view : {}
  if (state.loaded !== true) return '读取中…'
  if (state.hookReady === false || state.editable !== true) return '暂不可用'
  if (state.hasUpperBound !== true) return '不限'
  const upperBound = readInt(state.upperBound)
  return upperBound !== null && upperBound >= 0 ? `${upperBound} 台` : '未知'
}

/** deviceLimitSourceText 把来源串翻成文案（137 §4 的四个取值）。 */
export function deviceLimitSourceText(source) {
  switch (String(source || '')) {
    case 'user_override':
      return '你自己设置的'
    case 'level':
      return '等级权益'
    case 'level_missing':
      return '未评级（暂不限制）'
    case 'unlimited':
      return '等级不限'
    default:
      // 空串/未知来源：显式说「未知」，不冒充「不限」——两者 limit 都可能为 0。
      return '未知'
  }
}

/** emptyDeviceLimitState 是「还没读到」的初值（不是「读到 0」）。 */
export function emptyDeviceLimitState() {
  return {
    loaded: false,
    limit: null,
    limitKnown: false,
    unlimited: false,
    source: '',
    sourceText: '',
    override: null,
    levelLimit: null,
    levelName: '',
    hasUpperBound: false,
    upperBound: null,
    editable: false,
    hookReady: true,
  }
}

/**
 * validateDeviceLimitInput 校验用户输入的上限（与后端 400 文案同口径）。
 *
 * 返回 `{ ok, value, error, message }`：
 *   - 空输入 → 「请填写设备上限」
 *   - 非整数 → 「设备上限必须是整数」
 *   - 负数 → 「设备上限不能为负数」
 *   - 超过等级值 L → 「不能超过当前等级上限 L 台」（**与后端一字不差**）
 *
 * 为什么要前端也校验一遍：后端的 400 是最后防线，但用户点了保存才看到红字
 * 是糟糕体验；更重要的是**上界必须来自 view.upperBound**（等级值），
 * 而不是当前生效值 —— 与 PUT 的口径一致（见 controller/device_user.go 的注释）。
 */
export function validateDeviceLimitInput(input, view) {
  const text = String(input === null || input === undefined ? '' : input).trim()
  if (text === '') {
    return { ok: false, value: null, error: 'required', message: '请填写设备上限' }
  }
  if (!/^\d+$/.test(text)) {
    return { ok: false, value: null, error: 'not-integer', message: '设备上限必须是整数' }
  }
  const value = Number(text)
  if (!Number.isSafeInteger(value)) {
    return { ok: false, value: null, error: 'not-integer', message: '设备上限必须是整数' }
  }
  const state = view && typeof view === 'object' ? view : {}
  if (state.hasUpperBound && state.upperBound !== null && value > state.upperBound) {
    return {
      ok: false,
      value,
      error: 'above-level',
      message: `不能超过当前等级上限 ${state.upperBound} 台`,
    }
  }
  return { ok: true, value, error: '', message: '' }
}

/** normalizeDeviceItem 归一化列表里的一台设备（缺字段一律显式置空/未知）。 */
export function normalizeDeviceItem(raw) {
  const item = raw && typeof raw === 'object' ? raw : {}
  const status = typeof item.status === 'string' ? item.status : ''
  const meta = deviceStatusMeta(status)
  const blockId = readInt(item.blockId)
  return {
    id: readInt(item.id) ?? 0,
    deviceId: typeof item.deviceId === 'string' ? item.deviceId : '',
    deviceLabel: typeof item.deviceLabel === 'string' ? item.deviceLabel : '',
    // lastIp 是「最近一次使用时的来源 IP」，由后端在登记设备时写入。
    // 缺失/非字符串一律置空串（**不伪造** '0.0.0.0' 或 '未知'），
    // 展示层用 deviceIPText 统一翻成「未记录」——那是文案决定，不是数据决定。
    lastIp: typeof item.lastIp === 'string' ? item.lastIp : '',
    lastSeenAt: typeof item.lastSeenAt === 'string' ? item.lastSeenAt : '',
    status,
    statusText: typeof item.statusText === 'string' && item.statusText ? item.statusText : meta.text,
    tone: meta.tone,
    revokedAt: typeof item.revokedAt === 'string' ? item.revokedAt : '',
    revokedReason: typeof item.revokedReason === 'string' ? item.revokedReason : '',
    blocked: item.blocked === true,
    // blockId 只在 >0 时可用（列表里的「解除拉黑」按钮依赖它）。
    blockId: blockId !== null && blockId > 0 ? blockId : 0,
    blockOnly: item.blockOnly === true,
    removable: item.removable === true,
    // renamable：只有真实的绑定行（id>0）能改名。
    //
    // 孤儿拉黑行（id=0、blockOnly=true）没有绑定行，重命名接口按 id 定位，
    // 传 0 只会拿到 404。在这里显式算成 false，而不是让用户点了按钮才发现失败。
    renamable: (readInt(item.id) ?? 0) > 0,
  }
}

/**
 * deviceIPText 把来源 IP 翻成文案。
 *
 * 两态而不是三态：**没有 IP** 一律「未记录」。
 * 为什么不能显示成空单元格：空白会被读成「渲染坏了」或「这台设备没有 IP」，
 * 而真实语义是「还没采到」——老数据（补列前的行）与采不到 IP 的请求都落在这里。
 */
export function deviceIPText(value) {
  const text = String(value || '').trim()
  return text === '' ? '未记录' : text
}

/** DEVICE_LABEL_MAX_RUNES 是设备名的字符上限，与后端 device_label VARCHAR(64) 一致。 */
export const DEVICE_LABEL_MAX_RUNES = 64

/**
 * validateDeviceLabel 校验用户输入的设备名（与后端 400 文案同口径）。
 *
 * 返回 `{ ok, value, error, message }`：
 *   - 含换行/制表符/控制字符 → 拒绝
 *   - 超过 64 个字符 → 拒绝
 *   - 空串/纯空白 → **通过**（语义是「恢复默认显示」）
 *
 * 为什么空串必须通过：用户想取消自己起的名字是合理需求。把它判成非法，
 * 用户就只能随便填一个字符来「清空」，反而造出脏数据。
 *
 * 为什么必须复刻后端的控制字符校验：后端 400 是最后防线，但用户点了保存
 * 才看到红字是糟糕体验；更重要的是**两处文案必须一致**，否则同一个错误
 * 在不同层显示不同的说法（这正是本仓把校验放进纯函数层的原因）。
 */
export function validateDeviceLabel(raw) {
  const text = String(raw === null || raw === undefined ? '' : raw).trim()
  if (text === '') {
    return { ok: true, value: '', error: '', message: '' }
  }
  // 与控制字符同口径：\r \n \t 与 C0/C1 区段、DEL。
  // 用码点判断而不是正则字面量，避免把中文/emoji 误伤。
  for (const ch of text) {
    const code = ch.codePointAt(0)
    if (code === 0 || code === 0x7f || (code < 0x20 && code >= 0x00)) {
      return { ok: false, value: '', error: 'control-char', message: '设备名称不能包含换行或控制字符' }
    }
  }
  // 按**字符**（码点）计长度而不是 .length：后者是 UTF-16 码元数，
  // emoji 会被算成 2，导致「用户看到 32 个 emoji 就被拒」。
  const runes = Array.from(text)
  if (runes.length > DEVICE_LABEL_MAX_RUNES) {
    return {
      ok: false,
      value: '',
      error: 'too-long',
      message: `设备名称最多 ${DEVICE_LABEL_MAX_RUNES} 个字符`,
    }
  }
  return { ok: true, value: text, error: '', message: '' }
}

/** normalizeDeviceList 归一化 `GET /user/devices` 的 data。 */
export function normalizeDeviceList(raw) {
  // 同 normalizeDeviceLimit：后端返回 {data: {...}} 信封，必须剥掉。
  const data = unwrapDevicePayload(raw, ['devices', 'total', 'active', 'blocked'])
  const list = Array.isArray(data.devices) ? data.devices : []
  return {
    loaded: true,
    devices: list.map(normalizeDeviceItem),
    total: readInt(data.total) ?? list.length,
    active: readInt(data.active) ?? 0,
    blocked: readInt(data.blocked) ?? 0,
    schemaReady: data.schemaReady !== false,
  }
}

/** emptyDeviceListState 是列表的初值（loaded=false 便于区分「没读到」和「读到 0 台」）。 */
export function emptyDeviceListState() {
  return { loaded: false, devices: [], total: 0, active: 0, blocked: 0, schemaReady: true }
}

/**
 * describeLastSeen 把 ISO 时间转成「N 分钟前」。
 *
 * 三态而不是两态：**没有时间** 返回「从未活跃」，无法解析 返回「未知时间」。
 * 把两者都显示成「刚刚」会让「10 天没用的设备」看起来是活的（137 §7 的
 * 闲置解绑是用户理解上限的关键信息）。
 */
export function describeLastSeen(value, now) {
  const text = String(value || '').trim()
  if (text === '') return '从未活跃'
  const stamp = Date.parse(text)
  if (!Number.isFinite(stamp)) return '未知时间'
  const reference = now instanceof Date ? now.getTime() : Number(now)
  if (!Number.isFinite(reference)) return '未知时间'
  const diffMs = reference - stamp
  if (diffMs < 0) return '刚刚'
  const minutes = Math.floor(diffMs / 60000)
  if (minutes < 1) return '刚刚'
  if (minutes < 60) return `${minutes} 分钟前`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时前`
  const days = Math.floor(hours / 24)
  return `${days} 天前`
}

/** maskDeviceId 截断长设备指纹（展示用；后端 device_id 最长 191）。 */
export function maskDeviceId(value) {
  const text = String(value || '')
  if (text.length <= 22) return text
  return `${text.slice(0, 14)}…${text.slice(-6)}`
}
