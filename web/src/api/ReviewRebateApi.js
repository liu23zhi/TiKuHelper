import request from '../utils/request'

// 评价返现：用户端「评价返现」+ 管理端「评价返现管理」。
// 后端契约（设计定稿 16-评价返现与邀请返现 §1.8）：
//   GET    /adapter-service/user/review-rebate/program                 当前活动、规则表（脱敏）、我的已领
//   POST   /adapter-service/user/review-rebate/claims                  multipart 提交截图（字段名 file）
//   GET    /adapter-service/user/review-rebate/claims                  我的提交列表
//   GET    /adapter-service/admin/review-rebate/programs               活动列表
//   PUT    /adapter-service/admin/review-rebate/programs/:id           改活动配置
//   GET    /adapter-service/admin/review-rebate/rules[/:id]            规则表读取
//   POST   /adapter-service/admin/review-rebate/rules                  新建规则
//   PUT    /adapter-service/admin/review-rebate/rules/:id              改规则
//   DELETE /adapter-service/admin/review-rebate/rules/:id              删规则
//   GET    /adapter-service/admin/review-rebate/claims                 待审列表
//   POST   /adapter-service/admin/review-rebate/claims/:id/decision    批准/拒绝
//
// 后端由另一代理并行开发：字段以实际实现为准，联调时按需微调本文件。
// 全部 errorFeedback: 'local' —— 页面自己有持久失败态与重试入口。

const userBase = '/adapter-service/user/review-rebate'
const adminBase = '/adapter-service/admin/review-rebate'

// 截图上传：原生 FormData，不引入上传组件。
// 前端已校验 PNG/JPEG 与 ≤5 MB，服务端仍会自行解码校验（双重保险）。
export function submitReviewRebateClaim(file, reviewUrl = '') {
  const form = new FormData()
  form.append('file', file)
  const link = String(reviewUrl || '').trim()
  if (link) form.append('reviewUrl', link)
  return request({
    errorFeedback: 'local',
    url: `${userBase}/claims`,
    method: 'post',
    data: form,
    timeout: 120 * 1000,
  })
}

export function getReviewRebateProgram() {
  return request({ errorFeedback: 'local', url: `${userBase}/program`, method: 'get' })
}

export function getMyReviewRebateClaims() {
  return request({ errorFeedback: 'local', url: `${userBase}/claims`, method: 'get' })
}

export function getReviewRebatePrograms() {
  return request({ errorFeedback: 'local', url: `${adminBase}/programs`, method: 'get' })
}

export function updateReviewRebateProgram(id, data) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('活动参数错误')
  return request({
    errorFeedback: 'local',
    url: `${adminBase}/programs/${value}`,
    method: 'put',
    data,
  })
}

export function getReviewRebateRules() {
  return request({ errorFeedback: 'local', url: `${adminBase}/rules`, method: 'get' })
}

export function createReviewRebateRule(data) {
  return request({ errorFeedback: 'local', url: `${adminBase}/rules`, method: 'post', data })
}

export function updateReviewRebateRule(id, data) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('规则参数错误')
  return request({ errorFeedback: 'local', url: `${adminBase}/rules/${value}`, method: 'put', data })
}

export function deleteReviewRebateRule(id) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('规则参数错误')
  return request({ errorFeedback: 'local', url: `${adminBase}/rules/${value}`, method: 'delete' })
}

export function getReviewRebateClaims(params) {
  return request({ errorFeedback: 'local', url: `${adminBase}/claims`, method: 'get', params })
}

export function decideReviewRebateClaim(id, decision, note = '') {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('提交记录参数错误')
  if (!['approve', 'reject'].includes(decision)) throw new Error('审核结论无效')
  return request({
    errorFeedback: 'local',
    url: `${adminBase}/claims/${value}/decision`,
    method: 'post',
    data: { decision, note: String(note || '') },
  })
}
