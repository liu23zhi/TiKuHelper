import request from '../utils/request'

// 白名单管理（T-04，task_doc/123：「请新增白名单管理，规则模仿黑名单。优先级 白名单 > 黑名单」）。
// 后端契约（internal/controller/whitelist_admin.go）：
//   GET    /api/whitelist                  列表：分页 + kind / enabled / keyword
//   POST   /api/whitelist                  新增：归一化 + reason 必填
//   PUT    /api/whitelist/:id/enabled       启停：单列写（不整行覆盖）
//   DELETE /api/whitelist/:id              删除：硬删 + ?reason= 必填（快照审计）
//   GET    /api/whitelist/exempt-ips       按 IP 白名单文本域（T-05）
//   PUT    /api/whitelist/exempt-ips       保存文本域（非法项 400，不静默跳过）
//
// ⚠️ 白名单**没有命中记录接口**：白名单命中等于「本次请求不拦截」，
// 按判据「白名单内 IP 不记 blacklist_hits 行」，它连黑名单的留痕都不产生，
// 因此也没有自己的命中表（见 internal/entity/whitelist.go 的列差异说明）。
// 前端不要照抄黑名单的 hits tab —— 那会请求一个不存在的接口。
//
// 全部使用 errorFeedback: 'local'：页面自己有失败态与重试入口，
// 不能在被页面接管的请求上再弹一次全局错误提示（与 BlacklistApi.js 同约定）。

export function getWhitelistEntries(params) {
  return request({
    errorFeedback: 'local',
    url: '/api/whitelist',
    method: 'get',
    params,
  })
}

// 新增时 pattern 是**运营输入的原始值**，归一化一律由后端做：
// 前端不预归一化（不把 prefix 转 CIDR、不收敛 v4-mapped），否则
// 「前端算的和后端算的不一致」时排错要先分辨是哪一层出的问题。
// matchType 留空表示按类型自动判定（含 "/" → cidr，纯 1–3 段 → prefix）。
export function createWhitelistEntry(payload) {
  return request({
    errorFeedback: 'local',
    url: '/api/whitelist',
    method: 'post',
    data: {
      kind: payload.kind || 'ip',
      pattern: payload.pattern,
      matchType: payload.matchType || '',
      reason: payload.reason,
      enabled: payload.enabled !== false,
      expiresAt: payload.expiresAt || '',
    },
  })
}

// 启停：必须显式传目标状态。后端对缺 enabled 的请求返回 400 而不是猜方向，
// 这里也不给默认值 —— 一个不带目标状态的启停请求没有确定语义。
export function setWhitelistEntryEnabled(id, enabled) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('白名单规则参数错误')
  if (typeof enabled !== 'boolean') throw new Error('缺少目标状态')
  return request({
    errorFeedback: 'local',
    url: `/api/whitelist/${value}/enabled`,
    method: 'put',
    data: { enabled },
  })
}

// 删除：reason 是**必填查询参数**（后端 rule_audit 会把它连同完整快照写进日志）。
// 不传 reason 会被 400 拒掉 —— 前端不要给默认值兜底，否则审计里全是「无原因」。
export function deleteWhitelistEntry(id, reason) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('白名单规则参数错误')
  const trimmed = String(reason == null ? '' : reason).trim()
  if (trimmed.length < 2) throw new Error('请填写删除原因')
  return request({
    errorFeedback: 'local',
    url: `/api/whitelist/${value}`,
    method: 'delete',
    params: { reason: trimmed },
  })
}

// 按 IP 白名单文本域（blacklist_exempt_ips）。
//
// 它与白名单表是**两层**，处置完全相同（命中即完全不查黑名单）：
//   表：带原因 / 过期 / 启停 / 操作人的结构化规则；
//   文本域：一次粘贴一整片网段的运维快捷键。
export function getWhitelistExemptIPs() {
  return request({
    errorFeedback: 'local',
    url: '/api/whitelist/exempt-ips',
    method: 'get',
  })
}

export function updateWhitelistExemptIPs(value) {
  return request({
    errorFeedback: 'local',
    url: '/api/whitelist/exempt-ips',
    method: 'put',
    data: { value: String(value == null ? '' : value) },
  })
}
