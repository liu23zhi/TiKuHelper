import request from '../utils/request'

// 评价返现：用户端「评价返现」+ 管理端「评价返现管理」。
// 后端契约（设计定稿 16-评价返现与邀请返现 §1.8）：
//   GET    /api/user/review-rebate/availability            是否有开启的活动
//   GET    /api/user/review-rebate/program                 当前活动、开放商店与本人的领取状态
//   POST   /api/user/review-rebate/claims                  multipart 提交截图（字段名 file）
//   GET    /api/user/review-rebate/claims                  我的提交列表
//   GET    /api/admin/review-rebate/programs               活动列表（{data:[...]}，与规则表一致）
//   PUT    /api/admin/review-rebate/programs/:id           改活动配置
//   GET    /api/admin/review-rebate/rules[/:id]            规则表读取
//   POST   /api/admin/review-rebate/rules                  新建规则（201）
//   PUT    /api/admin/review-rebate/rules/:id              改规则
//   DELETE /api/admin/review-rebate/rules/:id              删规则（200）
//   GET    /api/admin/review-rebate/claims                 待审列表
//   POST   /api/admin/review-rebate/claims/:id/decision    批准/拒绝（body 用 action）
//   POST   /api/admin/review-rebate/claims/:id/score       触发/重跑 Agent 打分
//   GET    /api/admin/review-rebate/claims/:id/screenshot  截图回显（需管理员凭据）
//
// 字段名以**后端实体为准**：评价链接是 `claimedReviewUrl`（实体叫 ClaimedReviewURL，
// 用来区分「用户声称的链接」与将来可能存在的「系统抓到的链接」）；审核结论是 `action`。
// 后端为兼容老客户端同时接受 `reviewUrl` / `decision`，但这里只发规范名，
// 避免两套名字长期并存、改一处漏一处的老问题。
// 全部 errorFeedback: 'local' —— 页面自己有持久失败态与重试入口。

const userBase = '/api/user/review-rebate'
const adminBase = '/api/admin/review-rebate'

// 由提交表单统一发送截图和评价链接。
// 前端已校验 PNG/JPEG 与 ≤5 MB，服务端仍会自行解码校验（双重保险）。
export function submitReviewRebateClaim(file, reviewUrl = '', source = '') {
  const form = new FormData()
  form.append('file', file)
  const link = String(reviewUrl || '').trim()
  if (link) form.append('claimedReviewUrl', link)
  if (source) form.append('source', source)
  return request({
    errorFeedback: 'local',
    url: `${userBase}/claims`,
    method: 'post',
    data: form,
    timeout: 120 * 1000,
  })
}

export function getReviewRebateProgram(source = '') {
  return request({ errorFeedback: 'local', url: `${userBase}/program`, method: 'get', params: source ? { source } : undefined })
}

export function getReviewRebateAvailability(signal) {
  return request({
    errorFeedback: 'local',
    url: `${userBase}/availability`,
    method: 'get',
    signal,
    retryPolicy: 'single',
    timeout: 10_000,
  })
}

export function getMyReviewRebateClaims() {
  return request({ errorFeedback: 'local', url: `${userBase}/claims`, method: 'get' })
}

export function getReviewRebateVerification(id) {
  return request({ errorFeedback: 'local', url: `${adminBase}/claims/${id}/verification`, method: 'get' })
}

export function recordReviewRebateVerification(id, data) {
  return request({ errorFeedback: 'local', url: `${adminBase}/claims/${id}/verification`, method: 'post', data })
}

export function clawBackReviewRebate(id, verificationId) {
  return request({ errorFeedback: 'local', url: `${adminBase}/claims/${id}/clawback`, method: 'post', data: { verificationId } })
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

export function createReviewRebateProgram(data) {
  return request({ errorFeedback: 'local', url: `${adminBase}/programs`, method: 'post', data })
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

export function decideReviewRebateClaim(id, decision, note = '', approvalSnapshot = '') {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('提交记录参数错误')
  if (!['approve', 'reject'].includes(decision)) throw new Error('审核结论无效')
  return request({
    errorFeedback: 'local',
    url: `${adminBase}/claims/${value}/decision`,
    method: 'post',
    // 规范名是 action（后端 service/controller 都按它取）；decision 只是兼容别名。
    retryPolicy: 'single',
    data: {
      action: decision,
      note: String(note || ''),
      ...(decision === 'approve' && approvalSnapshot ? { approvalSnapshot } : {}),
    },
  })
}

// 只由管理员显式触发一次评分，不自动重放评分请求。
// 结果以管理员列表的持久状态为准；失败不写分数或发放任何款项。
export function scoreReviewRebateClaim(id) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('提交记录参数错误')
  return request({ errorFeedback: 'local', url: `${adminBase}/claims/${value}/score`, method: 'post', retryPolicy: 'single', timeout: 150_000 })
}

// 只在截图端点替换传输：XHR 会自动跟随重定向，无法在下游请求发出前阻止它。
async function screenshotAdapter(config) {
  const controller = new AbortController()
  const abort = () => controller.abort()
  config.signal?.addEventListener('abort', abort, { once: true })
  if (config.signal?.aborted) abort()
  const timer = setTimeout(abort, config.timeout)
  try {
    const response = await fetch(new URL(config.url, window.location.origin), {
      mode: 'same-origin',
      credentials: 'same-origin',
      redirect: 'error',
      cache: 'no-store',
      signal: controller.signal,
    })
    const type = (response.headers.get('content-type') || '').split(';')[0].trim().toLowerCase()
    let data = null
    if (response.status === 200) {
      data = await response.blob()
    } else if (type === 'application/json' || type.endsWith('+json')) {
      // 保留共享鉴权处理所需的业务码（例如 403 ACCOUNT_BANNED）。
      const text = await response.text()
      try { data = JSON.parse(text) } catch { /* 无效错误体不改变 HTTP 状态语义 */ }
    } else {
      await response.body?.cancel()
    }
    return { data, status: response.status, headers: Object.fromEntries(response.headers), config }
  } finally {
    clearTimeout(timer)
    config.signal?.removeEventListener('abort', abort)
  }
}

// 只允许当前记录的规范同源路径，不把后端返回的任意地址交给带凭据的客户端。
export function getReviewRebateScreenshot(id, listedUrl, signal) {
  const value = Number(id)
  if (!Number.isSafeInteger(value) || value <= 0 || value > 2147483647) return null
  const url = `${adminBase}/claims/${value}/screenshot`
  if (listedUrl !== url) return null
  return request({
    errorFeedback: 'local',
    retryPolicy: 'single',
    url,
    method: 'get',
    responseType: 'blob',
    adapter: screenshotAdapter,
    timeout: 20_000,
    signal,
  })
}
