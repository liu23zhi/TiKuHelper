import request from '../utils/request'

// 账号封禁管理（121 §4.8 / 121 决策 6）。
// 后端契约（internal/controller/user_ban_admin.go）：
//   GET    /api/admin/user-bans              列表：分页 + userId / active / keyword
//   POST   /api/admin/user-bans              封禁：reason 必填，expiresAt 空 = 永久
//   DELETE /api/admin/user-bans/:userId      解封：软撤销（把未到期行置为到期 + 复位 status），不删行
//
// # 三个必须知道的语义（都是后端实测行为，不是猜测）
//
//  1. **解封的路径参数是 userId，不是封禁记录的 id**。`service.Unban` 的语义是
//     「解除这个用户的封禁」（处理该用户全部未到期行），不是「作废某一条记录」。
//     用记录 id 会让调用方误以为只解了一条，且传错 id 会解封到另一个账号。
//
//  2. **expiresAt 为空串 = 永久封禁**，不是「不设置」。空串与 undefined 在后端
//     都会被解析成 nil，落库是 `expires_at IS NULL`。
//
//  3. **解封不写 revoked_at**（表里没有这一列）。已撤销/已过期的区分由后端推断
//     （expires_at 与 updated_at 是否相等），前端只消费 status 字段，不要自己推。
//
// 全部使用 errorFeedback: 'local'：页面自己有失败态与重试入口，
// 不能在被页面接管的请求上再弹一次全局错误提示（与 BlacklistApi.js / WhitelistApi.js 同约定）。

export function getUserBans(params) {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/user-bans',
    method: 'get',
    params,
  })
}

// 封禁：expiresAt 必须由调用方明确给出（null/空 = 永久）。
//
// 这里**不**为 expiresAt 提供默认值兜底：默认成永久会让「只想封 1 小时」
// 的运营不小心封成永久，而封禁是风控动作，宁可让调用方显式传。
export function createUserBan(payload) {
  const userId = Number(payload.userId)
  if (!Number.isInteger(userId) || userId <= 0) throw new Error('请选择要封禁的用户')
  const reason = String(payload.reason == null ? '' : payload.reason).trim()
  if (!reason) throw new Error('封禁原因不能为空')
  const expiresAt = String(payload.expiresAt == null ? '' : payload.expiresAt).trim()
  return request({
    errorFeedback: 'local',
    url: '/api/admin/user-bans',
    method: 'post',
    data: { userId, reason, expiresAt },
  })
}

// 解封：参数是 userId（见文件头第 1 条）。reason 走 query，可空（仅记日志）。
export function revokeUserBan(userId, reason) {
  const value = Number(userId)
  if (!Number.isInteger(value) || value <= 0) throw new Error('解封参数错误')
  const trimmed = String(reason == null ? '' : reason).trim()
  return request({
    errorFeedback: 'local',
    url: `/api/admin/user-bans/${value}`,
    method: 'delete',
    params: trimmed ? { reason: trimmed } : {},
  })
}
