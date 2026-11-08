import request from '../utils/request'

// 邀请返现：用户端「我的邀请」+ 管理端「返佣记录」。
// 后端契约（internal/api/router.go）：
//   GET  /adapter-service/user/referral                      本人邀请码与返佣汇总
//   GET  /adapter-service/admin/referral-rebates             返佣列表（状态/受益人/受邀人/订单号）
//   POST /adapter-service/admin/referral-rebates/:id/settle  把 pending 置为 settled（不入钱包）
//
// 全部使用 errorFeedback: 'local'：页面自己有持久失败态与重试入口，
// 不能在被页面接管的请求上再弹一次全局错误提示。

export function getMyReferral() {
  return request({
    errorFeedback: 'local',
    url: '/adapter-service/user/referral',
    method: 'get',
  })
}

export function getReferralRebates(params) {
  return request({
    errorFeedback: 'local',
    url: '/adapter-service/admin/referral-rebates',
    method: 'get',
    params,
  })
}

// 返佣结算是独立账本操作：只改状态，不写 users.balance。
// 状态冲突由后端返回 409，调用方据此提示「先刷新核对」。
export function settleReferralRebate(id) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('返佣记录参数错误')
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/admin/referral-rebates/${value}/settle`,
    method: 'post',
  })
}
