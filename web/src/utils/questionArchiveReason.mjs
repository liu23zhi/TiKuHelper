// 题库删除（= 归档）原因的前端契约（Q77-6 定案「预设下拉 + 可自定义输入」）。
//
// 为什么抽成纯函数：页面里的 Swal 弹窗要真实挂载才能驱动（贵且脆），
// 而「原因合不合法」「请求该带什么参数」这两条恰恰是最该被 CI 钉住的部分。
// 抽出来后可以用 node --test 直接断言，且与 index.vue 共用同一份实现，
// 不会出现「测试里 >= 2、页面里 >= 1」这种两份口径。

// 与后端 internal/controller/tiku_archive.go:23 的 archiveReasonMinLength 对齐。
//
// ⚠️ 单位是**字符**不是字节：后端用 utf8.RuneCountInString(strings.TrimSpace(reason))，
// 前端的 String.prototype.length 数的是 UTF-16 码元 —— 对中文（BMP 内）两者相等，
// 所以这里直接用 length 是安全的；但绝不能改成 TextEncoder 之类的字节计算，
// 否则「内容错误」（4 个汉字）会被算成 12 字节，与后端口径分叉。
export const ARCHIVE_REASON_MIN_LENGTH = 2

// 预设选项。值必须与后端 archiveReasonCode（tiku_archive.go:33-45）**逐字一致**，
// 因为后端是按 TrimSpace 后的中文字面量做 switch 匹配的：
// 写成 '违规' 或 '重复' 都会落进默认分支，archived_reason 变成 admin_delete，
// 归档列表按分类筛选时这一条就永远筛不出来。
export const ARCHIVE_REASON_PRESETS = Object.freeze([
  '内容错误',
  '重复题库',
  '违规内容',
  '其他',
])

// 预设值 → 后端会落库的归档分类代码（仅供提示与测试断言，不参与请求）。
//
// 注意「其他」的后端映射是 'other'，自由填写的原因则落 entity.TikuArchivedReasonAdminDelete
// （'admin_delete'）—— 那是后端行为，前端不猜，见 archiveReasonCode 的默认分支。
export const ARCHIVE_REASON_CODES = Object.freeze({
  内容错误: 'content_error',
  重复题库: 'duplicate',
  违规内容: 'violation',
  其他: 'other',
})

// archiveReasonRuneLength 与后端 archivedReasonRuneLength 同口径：去首尾空格后的字符数。
export function archiveReasonRuneLength(value) {
  if (typeof value !== 'string') return 0
  return value.trim().length
}

// normalizeArchiveReason 归一化管理员输入：只去首尾空格。
//
// 中间的空格保留 —— 后端也是只 TrimSpace，两边都不能擅自压中间空格，
// 否则「内容 错误」这类输入在前后端算出不同的字符串。
export function normalizeArchiveReason(value) {
  if (typeof value !== 'string') return ''
  return value.trim()
}

// validateArchiveReason 返回 null 表示合法，否则返回给用户看的错误文案。
//
// 文案与后端 400 的 message 保持一字不差（"请填写删除原因（至少 2 个字符）"），
// 这样同一条约束无论被哪一侧拦下，用户看到的话术都一样。
export function validateArchiveReason(value) {
  const reason = normalizeArchiveReason(value)
  if (archiveReasonRuneLength(reason) < ARCHIVE_REASON_MIN_LENGTH) {
    return '请填写删除原因（至少 2 个字符）'
  }
  return null
}

// buildDeleteQuestionParams 生成 DELETE /questions/:id 的 query 参数。
//
// ⚠️ 后端读的是 c.Query("reason")（internal/controller/tiku.go:303），即 URL query，
// 不是 body、不是 form。调用方必须把这个对象放进 axios config 的 `params` 字段：
//     request({ url, method: 'delete', params: buildDeleteQuestionParams(reason) })
// 直接写成 request({ url, method: 'delete', ...buildDeleteQuestionParams(reason) })
// 会把 reason 当成未知的 axios config 键**静默丢弃**，请求到了后端 reason 为空 → 400。
export function buildDeleteQuestionParams(reason) {
  return { reason: normalizeArchiveReason(reason) }
}
