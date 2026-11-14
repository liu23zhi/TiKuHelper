// 脚本域名的「格式模板 + 占位输入」模型（诉求 ④ / ⑤）。
//
// 与 Go 侧 internal/service/script_domain_format.go **逐条对齐**：
// 解析规则、匹配边界、拼装顺序、校验规则都必须一致。两处若漂移，
// 表现是「界面能填但后端拒绝」或更糟的「界面显示 A、实际存成 B」。
//
// 只有两种格式：
//   *.a.com   泛域名：slot 在**前面** →  `[输入框].a.com`
//   a.com/*   路径通配：slot 在**后面** → `a.com/[输入框]`
//
// 边界（与 Go 侧同一套，写在测试里）：
//   - `*.a.com` **不匹配** `a.com` 本身（通配只覆盖子域）
//   - `a.com/*` 匹配**多段**路径（`a.com/x/y` 命中），但**不匹配**根路径

const FORMAT_WILDCARD = 'wildcard'
const FORMAT_PATH = 'path'

// 与 Go 的 validScriptDomainHost 同规则：至少两级、标签 [a-z0-9-]、不以 - 开头/结尾、
// 顶级域纯字母且 ≥2 位。
function isValidHost(host) {
  if (!host || host.length > 253) return false
  if (host.startsWith('.') || host.endsWith('.') || host.includes('..')) return false
  const labels = host.split('.')
  if (labels.length < 2) return false
  for (const label of labels) {
    if (!label || label.length > 63) return false
    if (label.startsWith('-') || label.endsWith('-')) return false
    if (!/^[a-z0-9-]+$/.test(label)) return false
  }
  const tld = labels[labels.length - 1]
  if (tld.length < 2 || !/^[a-z]+$/.test(tld)) return false
  return true
}

/**
 * 解析一条管理员配置的格式。返回 null 表示非法。
 *
 * 不做「猜测纠正」：`a.com` 这类旧写法一律返回 null，由调用方决定报错还是跳过。
 * 静默失效会让管理员以为规则还在生效，那是本轮明确要避免的形态。
 */
export function parseScriptDomainFormat(raw) {
  const trimmed = String(raw ?? '').trim()
  if (!trimmed) return null
  if (trimmed.includes('://') || /[\s\\'"?#]/.test(trimmed)) return null
  const lowered = trimmed.toLowerCase()

  const slash = lowered.indexOf('/')
  if (slash >= 0) {
    const host = lowered.slice(0, slash)
    const pathPart = lowered.slice(slash)
    // 只有 `host/*` 是新语法；`host/a/*`、`host/a/**` 都是上一版的段级 glob。
    if (pathPart !== '/*' || host.includes('*') || !isValidHost(host)) return null
    return { raw: `${host}/*`, host, kind: FORMAT_PATH, slot: 'suffix' }
  }

  const stars = (lowered.match(/\*/g) || []).length
  if (stars !== 1) return null
  if (!lowered.startsWith('*.')) return null
  const host = lowered.slice(2)
  if (!isValidHost(host)) return null
  return { raw: `*.${host}`, host, kind: FORMAT_WILDCARD, slot: 'prefix' }
}

/** 归一化管理员提交的格式列表（去非法、去重、排序）。 */
export function normalizeScriptDomainFormats(values) {
  const seen = new Set()
  const result = []
  for (const value of Array.isArray(values) ? values : []) {
    const parsed = parseScriptDomainFormat(value)
    if (!parsed) continue
    if (seen.has(parsed.raw)) continue
    seen.add(parsed.raw)
    result.push(parsed.raw)
  }
  return result.sort()
}

/**
 * 校验用户填进占位框的内容。返回错误文案，合法时返回空串。
 *
 * 与 Go 的 normalizeScriptDomainSlot 同规则。⚠️ 子域**不能**先 trim 点：
 * 用户输入 `.x` 若被悄悄变成 `x`，他看到的字面与生成的域名不是同一个东西。
 */
export function validateScriptDomainSlot(kind, raw) {
  const value = String(raw ?? '').trim()
  if (!value) return '请填写域名占位内容'
  if (kind === FORMAT_WILDCARD) {
    if (/[/\\*\s?#]/.test(value)) return '子域只能包含字母、数字与连字符'
    if (value.startsWith('.') || value.endsWith('.') || value.includes('..')) return '子域的点只能出现在标签之间'
    for (const label of value.toLowerCase().split('.')) {
      if (!label || label.length > 63) return '子域标签长度必须在 1 到 63 之间'
      if (label.startsWith('-') || label.endsWith('-')) return '子域标签不能以连字符开头或结尾'
      if (!/^[a-z0-9-]+$/.test(label)) return '子域只能包含字母、数字与连字符'
    }
    return ''
  }
  if (kind === FORMAT_PATH) {
    const body = value.replace(/^\/+|\/+$/g, '')
    if (!body) return '请填写路径'
    if (/[\\*\s?#]/.test(body)) return '路径不能包含 *、空格、反斜杠、? 或 #'
    for (const segment of body.split('/')) {
      if (!segment) return '路径不能有空的路径段'
      if (segment === '.' || segment === '..') return '路径不能包含 . 或 .. 段'
      if (!/^[A-Za-z0-9._~-]+$/.test(segment)) return '路径只能包含字母、数字与 - _ . ~'
    }
    return ''
  }
  return '未知的域名格式'
}

/**
 * 把「格式 + 占位内容」拼成最终域名。
 *
 * ⚠️ 顺序由格式种类决定，**不是**「输入框永远在前面」：
 *     wildcard + x   → x.a.com   （slot 在前）
 *     path     + p/q → a.com/p/q （slot 在后）
 * 把这条写反会让泛域名槽位产出主从颠倒的地址。测试专门钉这一条。
 */
export function buildScriptDomainFromFormat(format, slot) {
  const parsed = typeof format === 'string' ? parseScriptDomainFormat(format) : format
  if (!parsed) return ''
  const error = validateScriptDomainSlot(parsed.kind, slot)
  if (error) return ''
  const value = String(slot).trim()
  if (parsed.slot === 'prefix') {
    return `${value.toLowerCase()}.${parsed.host}`
  }
  return `${parsed.host}/${value.replace(/^\/+|\/+$/g, '')}`
}

/** 逆运算：给定具体域名与格式，取回槽位内容（UI 回填编辑态用）。 */
export function splitScriptDomainForFormat(format, domain) {
  const parsed = typeof format === 'string' ? parseScriptDomainFormat(format) : format
  if (!parsed) return null
  const value = String(domain ?? '').trim().toLowerCase()
  if (!value) return null
  if (parsed.slot === 'prefix') {
    const suffix = `.${parsed.host}`
    if (value.length <= suffix.length || !value.endsWith(suffix)) return null
    const slot = value.slice(0, value.length - suffix.length)
    return validateScriptDomainSlot(parsed.kind, slot) ? null : slot
  }
  const prefix = `${parsed.host}/`
  if (!value.startsWith(prefix)) return null
  const slot = value.slice(prefix.length)
  return validateScriptDomainSlot(parsed.kind, slot) ? null : slot
}

/**
 * 一个脚本可以有多个域名格式（诉求 ⑤ 最后一句）。把用户填的每一行拼出来。
 *
 * 返回 { domains, errors }：errors 非空时调用方必须阻止提交并**指出是哪一行**，
 * 不能只说「请填写完整」。
 */
export function buildScriptDomainForms(entries) {
  const domains = []
  const errors = []
  for (const [index, entry] of (Array.isArray(entries) ? entries : []).entries()) {
    const format = entry?.format
    const slot = entry?.slot
    if (!format) {
      errors.push({ index, message: '请选择域名格式' })
      continue
    }
    const parsed = parseScriptDomainFormat(format)
    if (!parsed) {
      errors.push({ index, message: `第 ${index + 1} 行的域名格式无效` })
      continue
    }
    const message = validateScriptDomainSlot(parsed.kind, slot)
    if (message) {
      errors.push({ index, message })
      continue
    }
    const domain = buildScriptDomainFromFormat(parsed, slot)
    if (!domain) {
      errors.push({ index, message: '无法生成域名' })
      continue
    }
    if (!domains.includes(domain)) domains.push(domain)
  }
  return { domains, errors }
}
