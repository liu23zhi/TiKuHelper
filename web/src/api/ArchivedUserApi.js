import request from '../utils/request'

// 已归档账号管理（task_doc/76 §3.2.3 / §3.4）。
// 后端契约（internal/api/router.go）：
//   GET  /api/admin/archived-users                   列表：分页 + reason / keyword / startTime / endTime
//   GET  /api/admin/archived-users/:id               详情：账号快照 + 绑定记录历史
//   POST /api/admin/archived-users/:id/restore       恢复：confirmRename=false 只探冲突
//
// 全部使用 errorFeedback: 'local'：页面自己有失败态与重试入口，
// 不能在被页面接管的请求上再弹一次全局错误提示（与 ReferralApi.js 同约定）。

export function getArchivedUsers(params) {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/archived-users',
    method: 'get',
    params,
  })
}

export function getArchivedUserDetail(id) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('已归档账号参数错误')
  return request({
    errorFeedback: 'local',
    url: `/api/admin/archived-users/${value}`,
    method: 'get',
  })
}

// 恢复：confirmRename 是**语义字段**而不是可选优化。
//
//   confirmRename=false（缺省）→ 用户名被占用时后端返回 409 + 冲突信息，
//                                页面据此弹二次确认；数据一点都不会变。
//   confirmRename=true          → 后端生成「原名_restored_N」并恢复。
//
// 绝不能在前端「猜到冲突就自动传 true」：静默改名会让用户下次登录
// 发现自己名字变了而客服无法解释（76 §3.4.5）。
export function restoreArchivedUser(id, confirmRename) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('已归档账号参数错误')
  return request({
    errorFeedback: 'local',
    url: `/api/admin/archived-users/${value}/restore`,
    method: 'post',
    data: { confirmRename: confirmRename === true },
  })
}
