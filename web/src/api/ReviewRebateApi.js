import request from '../utils/request'

// 评价返现：用户端「评价返现」+ 管理端「评价返现管理」。
// 后端契约（设计定稿 16-评价返现与邀请返现 §1.8）：
//   GET    /adapter-service/user/review-rebate/program                 当前活动、规则表（脱敏）、我的已领
//   POST   /adapter-service/user/review-rebate/claims                  multipart 提交截图（字段名 file）
//   GET    /adapter-service/user/review-rebate/claims                  我的提交列表
//   GET    /adapter-service/admin/review-rebate/programs               活动列表（{data:[...]}，与规则表一致）
//   PUT    /adapter-service/admin/review-rebate/programs/:id           改活动配置
//   GET    /adapter-service/admin/review-rebate/rules[/:id]            规则表读取
//   POST   /adapter-service/admin/review-rebate/rules                  新建规则（201）
//   PUT    /adapter-service/admin/review-rebate/rules/:id              改规则
//   DELETE /adapter-service/admin/review-rebate/rules/:id              删规则（200）
//   GET    /adapter-service/admin/review-rebate/claims                 待审列表
//   POST   /adapter-service/admin/review-rebate/claims/:id/decision    批准/拒绝（body 用 action）
//   POST   /adapter-service/admin/review-rebate/claims/:id/score       触发/重跑 Agent 打分
//   GET    /adapter-service/admin/review-rebate/claims/:id/screenshot  截图回显（需管理员凭据）
//
// 字段名以**后端实体为准**：评价链接是 `claimedReviewUrl`（实体叫 ClaimedReviewURL，
// 用来区分「用户声称的链接」与将来可能存在的「系统抓到的链接」）；审核结论是 `action`。
// 后端为兼容老客户端同时接受 `reviewUrl` / `decision`，但这里只发规范名，
// 避免两套名字长期并存、改一处漏一处的老问题。
// 全部 errorFeedback: 'local' —— 页面自己有持久失败态与重试入口。

const userBase = '/adapter-service/user/review-rebate'
const adminBase = '/adapter-service/admin/review-rebate'

// 截图上传：原生 FormData，不引入上传组件。
// 前端已校验 PNG/JPEG 与 ≤5 MB，服务端仍会自行解码校验（双重保险）。
export function submitReviewRebateClaim(file, reviewUrl = '') {
  const form = new FormData()
  form.append('file', file)
  const link = String(reviewUrl || '').trim()
  if (link) form.append('claimedReviewUrl', link)
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
    // 规范名是 action（后端 service/controller 都按它取）；decision 只是兼容别名。
    data: { action: decision, note: String(note || '') },
  })
}

// 触发/重跑一次 Agent 打分。
// 返回 200 + claim；打分失败是 502（claim 保持 submitted，分数与金额都没写）。
export function scoreReviewRebateClaim(id) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('提交记录参数错误')
  return request({ errorFeedback: 'local', url: `${adminBase}/claims/${value}/score`, method: 'post' })
}

// 截图回显地址。
// 截图落在服务端私有目录，**没有可直连的静态 URL**；这个地址必须带管理员凭据访问，
// 非管理员拿到 403。列表里的 screenshotUrl 已经是这个地址，直接用即可。
export function reviewRebateScreenshotUrl(id) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) return ''
  return `${adminBase}/claims/${value}/screenshot`
}
