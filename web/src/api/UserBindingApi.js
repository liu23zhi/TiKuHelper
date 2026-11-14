import request from '../utils/request'

// 绑定历史（task_doc/70 §3.6 共用组件 / §3.7 voided 风险）。
// 后端契约（internal/api/router.go）：
//   GET  /api/admin/users/:id/bindings   单用户弹窗（用户管理操作列「查看」）
//   GET  /api/admin/bindings?kind=       侧边栏两个页面共用的列表
//   POST /api/admin/bindings             手工补录一条已释放的历史
//   POST /api/admin/bindings/:id/void    作废一条历史
//
// 四条全部 Auth + middleware.RequireAdmin。返回值里的手机号/微信 OpenID
// **已经由后端脱敏**（valueMasked），前端不得展示明文字段——接口本来也不下发。
//
// 全部 errorFeedback: 'local'：页面自己有失败态与重试入口（与 ArchivedUserApi.js 同约定）。

// 两个侧边栏页面共用一个 list 调用，只有 kind 不同（70 §3.6 明确「不要写两份」）。
export const BINDING_KINDS = ['wechat', 'phone']

export function normalizeBindingKind(kind) {
  const value = String(kind || '').trim().toLowerCase()
  return BINDING_KINDS.includes(value) ? value : ''
}

export function getAdminUserBindings(userId, params) {
  const value = Number(userId)
  if (!Number.isInteger(value) || value <= 0) throw new Error('用户参数错误')
  return request({
    errorFeedback: 'local',
    url: `/api/admin/users/${value}/bindings`,
    method: 'get',
    params,
  })
}

// kind 必填且走白名单：后端同样校验，这里先拦一次是为了在拼 URL 之前
// 就把「传了个 kind=foo」变成可见的开发期错误，而不是打到一个 400 上。
export function getBindingHistory(kind, params) {
  const normalized = normalizeBindingKind(kind)
  if (!normalized) throw new Error('绑定类型只能是 wechat 或 phone')
  return request({
    errorFeedback: 'local',
    url: '/api/admin/bindings',
    method: 'get',
    params: { ...(params || {}), kind: normalized },
  })
}

// 手工补录。只写 released 行（后端强制）：插 active 会把值从真正持有它的账号手里抢走。
export function createBindingHistory(data) {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/bindings',
    method: 'post',
    data,
  })
}

// 作废。reason 必填（后端要求 ≥2 字符），且作废会同时把 active_key 置 NULL。
export function voidBindingRecord(id, reason) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('绑定记录参数错误')
  const text = String(reason || '').trim()
  if ([...text].length < 2) throw new Error('请填写作废理由（至少 2 个字符）')
  return request({
    errorFeedback: 'local',
    url: `/api/admin/bindings/${value}/void`,
    method: 'post',
    params: { reason: text },
  })
}
