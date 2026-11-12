import request from '../utils/request'

// 邀请返现：用户端「我的邀请」+ 管理端「赠送记录」。
// 后端契约（internal/api/router.go）：
//   GET  /adapter-service/user/referral                      本人邀请码与返佣汇总
//   GET  /adapter-service/admin/referral-rebates             返佣列表（状态/受益人/受邀人/订单号）
//   POST /adapter-service/admin/referral-rebates/:id/settle  结算：状态置 settled + 入赠送余额
//   POST /adapter-service/admin/referral-rebates             手动补录一笔返佣（创建即入账）
//   GET  /adapter-service/admin/gift-records                 四类赠送来源聚合账本
//   GET  /adapter-service/admin/gift-records/sources         四类来源的标识与中文名
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

// 返佣结算 = 改状态 + 入赠送余额（gift_balance，可消费、不可退），同一事务。
// 可退本金（balance）不受影响。状态冲突由后端返回 409，调用方据此提示「先刷新核对」。
export function settleReferralRebate(id) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('返佣记录参数错误')
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/admin/referral-rebates/${value}/settle`,
    method: 'post',
  })
}

// 管理员手动补录一笔返佣：**创建即入账**（source=manual，直接进赠送余额）。
//
// 与结算的区别：结算是对已有待结算行改状态；补录是凭空建一行并立刻发钱。
// 同一 (受益人, 金额, 日期) 重复提交后端返回 409，调用方据此提示「这笔已经记过」，
// 而不是当成失败让管理员反复重试。
export function createManualReferralRebate(payload) {
  const beneficiaryUserId = Number(payload?.beneficiaryUserId)
  if (!Number.isInteger(beneficiaryUserId) || beneficiaryUserId <= 0) {
    throw new Error('受益人账户 ID 必须是正整数')
  }
  const amount = Number(payload?.amount)
  if (!Number.isFinite(amount) || amount <= 0) throw new Error('金额必须大于 0')
  const body = { beneficiaryUserId, amount }
  const day = String(payload?.day ?? '').trim()
  if (day) body.day = day
  const remark = String(payload?.remark ?? '').trim()
  if (remark) body.remark = remark
  return request({
    errorFeedback: 'local',
    url: '/adapter-service/admin/referral-rebates',
    method: 'post',
    data: body,
  })
}

// 赠送记录：四类来源（注册赠送 / 评价返现 / 邀请返佣 / 充值赠金）的聚合账本。
export function getGiftRecords(params) {
  return request({
    errorFeedback: 'local',
    url: '/adapter-service/admin/gift-records',
    method: 'get',
    params,
  })
}

// 四类来源的标识与中文名：前端不再自己维护一份文案（两处各写一份必然改一处漏一处）。
export function getGiftRecordSources() {
  return request({
    errorFeedback: 'local',
    url: '/adapter-service/admin/gift-records/sources',
    method: 'get',
  })
}
