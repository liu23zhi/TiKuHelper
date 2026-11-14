import request from '../utils/request'

// 黑名单管理（BL-5，46-黑名单设计.md §6）。
// 后端契约（internal/api/router.go）：
//   GET    /api/blacklist                 列表：分页 + kind / enabled / keyword
//   POST   /api/blacklist                 新增：归一化 + reason 必填
//   PUT    /api/blacklist/:id/enabled      启停：单列写（不整行覆盖）
//   DELETE /api/blacklist/:id              删除：硬删，命中记录保留
//   GET    /api/blacklist/hits             命中记录：分页 + entryId/kind/clientIp/action
//
// 全部使用 errorFeedback: 'local'：页面自己有失败态与重试入口，
// 不能在被页面接管的请求上再弹一次全局错误提示（与 ReferralApi.js 同约定）。

export function getBlacklistEntries(params) {
  return request({
    errorFeedback: 'local',
    url: '/api/blacklist',
    method: 'get',
    params,
  })
}

// 新增时 pattern 是**运营输入的原始值**，归一化一律由后端做：
// 前端不预归一化（不做 trim 成 11 位、不把 prefix 转 CIDR），否则
// 「前端算的和后端算的不一致」时排错要先分辨是哪一层出的问题。
// matchType 留空表示按类型自动判定（ip 含 "/" → cidr，纯 1–3 段 → prefix）。
export function createBlacklistEntry(payload) {
  return request({
    errorFeedback: 'local',
    url: '/api/blacklist',
    method: 'post',
    data: {
      kind: payload.kind,
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
export function setBlacklistEntryEnabled(id, enabled) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('黑名单规则参数错误')
  if (typeof enabled !== 'boolean') throw new Error('缺少目标状态')
  return request({
    errorFeedback: 'local',
    url: `/api/blacklist/${value}/enabled`,
    method: 'put',
    data: { enabled },
  })
}

export function deleteBlacklistEntry(id) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('黑名单规则参数错误')
  return request({
    errorFeedback: 'local',
    url: `/api/blacklist/${value}`,
    method: 'delete',
  })
}

export function getBlacklistHits(params) {
  return request({
    errorFeedback: 'local',
    url: '/api/blacklist/hits',
    method: 'get',
    params,
  })
}
