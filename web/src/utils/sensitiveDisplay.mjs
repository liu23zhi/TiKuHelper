/**
 * 敏感文本的展示规则（纯函数，无 Vue / 无网络 / 无 DOM）。
 *
 * 后端契约（internal/controller/logs.go + internal/service/sensitive_mask.go）：
 * - 列表接口对**非管理员**已把 description / oldAnswer / newAnswer 换成遮挡文本，
 *   并下发 `sensitiveMasked: true`；
 * - 命中明细 `sensitiveWords` 里的词本身也被替换成同长度遮挡符；
 * - 原文只能走 `GET /adapter-service/logs/:id/sensitive-original`（Auth + RequireAdmin）。
 *
 * 因此前端**默认状态必须是遮挡**：服务端已遮挡时直接显示它下发的文本，
 * 绝不因为「这次没拿到 masked 字段」就回退成原文。
 */

/** 命中状态：与 utils/logListHelpers.mjs 的 buildSensitiveFields 保持同一语义。 */
export const SENSITIVE_STATE = Object.freeze({
  UNCHECKED: 'unchecked',
  HIT: 'hit',
  CLEAN: 'clean',
})

/**
 * 判断一行记录是否需要遮挡。
 *
 * 判据只有一个：**后端说这行命中了敏感词**（sensitiveFlag === true）。
 * 不用「文本里像不像有敏感词」来猜 —— 那是前端复刻检测，两端必然漂移。
 *
 * @param {object} record
 * @returns {boolean}
 */
export function shouldMaskRecord(record = {}) {
  return record?.sensitiveFlag === true
}

/**
 * 判断服务端是否**已经**遮挡了这行。
 *
 * 只有 `sensitiveMasked === true` 才算。缺字段一律按「未遮挡」处理：
 * 老后端（PR #31 之前）不下发该字段，此时前端必须自己遮，
 * 否则「升级后端前的前端」会把原文直接显示出来。
 *
 * @param {object} record
 * @returns {boolean}
 */
export function isServerMasked(record = {}) {
  return record?.sensitiveMasked === true
}

/**
 * 计算某个字段该显示的文本。
 *
 * @param {object} record 日志行
 * @param {string} text 该字段当前的值（可能已被服务端遮挡）
 * @param {{revealed?: boolean}} options revealed=true 表示管理员已点「显示原文」
 * @returns {{text: string, masked: boolean}}
 *   masked=true 表示返回值是**遮挡形态**，调用方不该再尝试解析其中的 JSON。
 */
export function resolveSensitiveDisplayText(record, text, options = {}) {
  const raw = text === '' || text == null ? '' : String(text)
  if (!shouldMaskRecord(record)) {
    return { text: raw, masked: false }
  }
  if (options.revealed === true) {
    // 展开态：服务端已遮挡时拿不到原文（必须走原文接口补齐），
    // 这里只如实返回当前值，由调用方决定要不要发请求。
    return { text: raw, masked: false }
  }
  if (isServerMasked(record)) {
    return { text: raw, masked: true }
  }
  // 老后端：自己遮，且只报长度，不留任何原文字符。
  return { text: buildLocalMaskPlaceholder(raw), masked: true }
}

/**
 * 本地整段遮挡占位，与后端 BuildSensitiveMaskedContent 的文案保持同构。
 *
 * 用 rune（码点）计数而不是 `String.length`（UTF-16 码元）：
 * 后端按 rune 报「共 N 字」，两边数字必须一致，否则用户会看到
 * 同一条日志在列表里「共 12 字」、在详情里「共 14 字」（AGENTS.md 第 1 条）。
 *
 * @param {string} text
 * @returns {string}
 */
export function buildLocalMaskPlaceholder(text) {
  const value = text === '' || text == null ? '' : String(text)
  if (value.trim() === '') {
    return '（敏感内容已遮挡，原文为空）'
  }
  return `（敏感内容已遮挡：共 ${countRunes(value)} 字）`
}

/**
 * 按 Unicode 码点统计字符数（等价于后端 utf8.RuneCountInString）。
 *
 * @param {string} text
 * @returns {number}
 */
export function countRunes(text) {
  return Array.from(String(text ?? '')).length
}

/**
 * 解析 `sensitiveWords` 明细（后端存的 JSON 文本）。
 *
 * 失败静默返回空：历史行格式不可控，一条脏 JSON 不能让整页报错
 * （与 logListHelpers.mjs 的 parseSensitiveWords 同一纪律）。
 *
 * @param {string} raw
 * @returns {Array<{scope: string, forbidden: boolean, words: string[]}>}
 */
export function parseSensitiveScopes(raw) {
  let parsed
  try {
    parsed = JSON.parse(String(raw || ''))
  } catch {
    return []
  }
  if (!Array.isArray(parsed)) {
    return []
  }
  const scopes = []
  for (const item of parsed) {
    if (!item || typeof item !== 'object') continue
    scopes.push({
      scope: String(item.scope || '').trim(),
      forbidden: item.forbidden === true,
      words: Array.isArray(item.words)
        ? item.words.map((word) => String(word || '')).filter((word) => word !== '')
        : [],
    })
  }
  return scopes
}

/**
 * 生成「哪一部分命中了」的可读摘要，供徽章 tooltip 使用。
 *
 * **不含具体词**：非管理员拿到的词已被服务端替换成遮挡符，
 * 这里再拼一遍没有意义，还会让人误以为「原文里的词都这么短」。
 *
 * @param {string} raw `sensitiveWords` 原始 JSON 文本
 * @returns {{scopes: number, groups: string[], wordCount: number}}
 */
export function buildSensitiveSummary(raw) {
  const scopes = parseSensitiveScopes(raw)
  const hit = scopes.filter((item) => item.forbidden)
  const groups = hit.map((item) => ({ question: '题目', answer: '答案' })[item.scope] || item.scope || '其他')
  return {
    scopes: scopes.length,
    groups,
    wordCount: hit.reduce((total, item) => total + item.words.length, 0),
  }
}

/**
 * 徽章文案：命中时永远是「敏感词」，不随是否展开而变。
 *
 * 展开只改**内容**，不改**标记** —— 否则管理员点开之后就看不出这条被标过。
 *
 * @param {object} record
 * @param {{revealed?: boolean, isAdmin?: boolean}} options
 * @returns {{label: string, color: string, title: string}}
 */
export function buildSensitiveBadgeState(record = {}, options = {}) {
  if (!shouldMaskRecord(record)) {
    return { label: '', color: 'default', title: '' }
  }
  const summary = buildSensitiveSummary(record?.sensitiveWords)
  const where = summary.groups.length ? summary.groups.join('、') : '内容'
  const base = `命中敏感词（${where}）`
  if (options.revealed === true && options.isAdmin === true) {
    return { label: '敏感词', color: 'red', title: `${base}；当前已显示原文` }
  }
  if (options.isAdmin === true) {
    return { label: '敏感词', color: 'red', title: `${base}；原文默认遮挡，可点「显示原文」` }
  }
  return { label: '敏感词', color: 'red', title: `${base}；内容已遮挡，仅管理员可查看原文` }
}

/**
 * 管理员能不能点开原文：只有管理员 + 服务端确实遮挡了，按钮才有意义。
 *
 * 服务端没遮挡（老后端）时内容本来就是原文，按钮会是个假开关。
 *
 * @param {object} record
 * @param {{isAdmin?: boolean}} options
 * @returns {boolean}
 */
export function canRevealOriginal(record = {}, options = {}) {
  return options.isAdmin === true && shouldMaskRecord(record)
}
