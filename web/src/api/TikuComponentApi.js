import request from '../utils/request'
import { buildDeleteQuestionParams } from '../utils/questionArchiveReason.mjs'


export function login(params) {
  return request({
    url: `/api/user/login`,
    method: 'post',
    data: params
  });
}

export function logout() {
  return request({
    url: `/api/user/logout`,
    method: 'post'
  });
}

export function checkUserSession(signal) {
  return request({
    url: '/api/user/session',
    method: 'get',
    timeout: 10_000,
    retryPolicy: 'single',
    signal,
  })
}

export function registerUser(data) {
  return request({
    url: `/api/user/register`,
    method: 'post',
    data
  })
}

export function getPublicRegisterStatus() {
  return request({
    url: `/api/public/register-status`,
    method: 'get'
  })
}

export function sendPublicSMSCode(data) {
  return request({
    url: `/api/public/sms-code`,
    method: 'post',
    data
  })
}

export function createCaptchaChallenge(data) {
  return request({
    url: `/api/public/captcha/challenge`,
    method: 'post',
    data
  })
}

export function verifyCaptchaChallenge(data) {
  return request({
    url: `/api/public/captcha/verify`,
    method: 'post',
    data
  })
}

export function verifyRemoteCaptcha(data) {
  return request({
    url: `/api/public/captcha/remote-token`,
    method: 'post',
    data
  })
}

export function createWechatRegisterSession(data = {}) {
  return request({
    url: `/api/public/wechat-register/session`,
    method: 'post',
    data,
  })
}

export function createWechatRegisterQRCode(data) {
  return request({
    url: `/api/public/wechat-register/qrcode`,
    method: 'post',
    data,
  })
}

export function getWechatRegisterStatus(params) {
  return request({
    url: `/api/public/wechat-register/status`,
    method: 'get',
    params,
  })
}

export function stageWechatRegisterCode(data) {
  return request({
    url: `/api/public/wechat-register/code`,
    method: 'post',
    data,
  })
}

export function releaseWechatRegisterFlow(data) {
  return request({
    url: `/api/public/wechat-register/release`,
    method: 'post',
    data,
  })
}

export function createWechatLoginQRCode(feedback) {
  const config = {
    url: `/api/public/wechat-login/qrcode`,
    method: 'post'
  }
  // Only this narrow caller option is accepted; zero-argument consumers stay unchanged.
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function getWechatLoginStatus(params) {
  return request({
    url: `/api/public/wechat-login/status`,
    method: 'get',
    params
  })
}

export function loginWechatByCode(data) {
  return request({
    url: `/api/public/wechat-login/code`,
    method: 'post',
    data
  })
}

export function bindWechatLogin(data) {
  return request({
    url: `/api/public/wechat-login/bind`,
    method: 'post',
    data
  })
}

export function loginBySMS(data) {
  return request({
    url: `/api/user/login/sms`,
    method: 'post',
    data
  })
}


export function getCourses(params) {
  return request({
    url: `/api/courses`,
    method: 'get',
    params
  })
}

/**
 * 批量查询手机号归属地（管理员）。
 *
 * 密钥保存在服务端设置里，由后端携带，前端不接触密钥。
 * 返回 `{ data: { "<phone>": { address, province, city, sp, areaCode } } }`。
 */
export function getPhoneLocations(params) {
  return request({
    url: `/api/uapi/phoneinfo`,
    method: 'get',
    params,
    timeout: 0
  })
}

/**
 * 批量查询 IP 归属地（管理员）。
 *
 * 返回 `{ data: { "<ip>": { address, region, isp, llc, asn } } }`。
 */
export function getIpLocations(params) {
  return request({
    url: `/api/uapi/ipinfo`,
    method: 'get',
    params,
    timeout: 0
  })
}

export function getMyLocations(params = {}) {
  return request({
    url: `/api/uapi/my-locations`,
    method: 'get',
    params,
    timeout: 0
  })
}

export function getLocationCacheList(params) {
  return request({
    url: `/api/location-cache`,
    method: 'get',
    params
  })
}

export function deleteLocationCache(params) {
  return request({
    url: `/api/location-cache`,
    method: 'delete',
    params
  })
}

export function deleteLocationCacheBatch(data) {
  return request({
    url: `/api/location-cache/batch-delete`,
    method: 'post',
    data,
  })
}

export function refreshLocationCacheBatch(data) {
  return request({
    url: `/api/location-cache/batch-refresh`,
    method: 'post',
    data,
    timeout: 0
  })
}

export function refreshLocationCache(data) {
  return request({
    url: `/api/location-cache/refresh`,
    method: 'post',
    data,
    timeout: 0
  })
}

export function manualLookupLocationCache(data) {
  return request({
    url: `/api/location-cache/manual`,
    method: 'post',
    data,
    timeout: 0
  })
}

export function getUAPISearchEngines() {
  return request({
    url: `/api/uapi/search/engines`,
    method: 'get'
  })
}

// UAPI status/usage returns endpoint-level usage statistics. Keep the raw
// response in the page so both aggregate and single-endpoint responses can be
// displayed without losing fields added by the upstream API.
export function getUAPIUsageStatus(params) {
  return request({
    url: `/api/uapi/status/usage`,
    method: 'get',
    params
  })
}

export function searchUAPIAggregate(data) {
  return request({
    url: `/api/uapi/search/aggregate`,
    method: 'post',
    data,
    timeout: 65000
  })
}

export function getQuestions(data) {
  return request({
    url: `/api/questions/search`,
    method: 'post',
    data
  })
}

export function updateQuestions(data) {
  return request({
    url: `/api/questions/${data.id}`,
    method: 'put',
    data
  })
}

// delQuestions 删除（= 归档）一道题目。
//
// ⚠️ reason 走 **query 参数**，必须放在 config 的 `params` 里：
// 后端是 `reason := c.Query("reason")`（internal/controller/tiku.go:303），
// 而 axios 的 delete 第二个参数是 **config** ——
// 写成 `{ reason }` 会被当成未知 config 键静默丢弃，请求到后端 reason 为空 → 400，
// 症状是「管理员点了删除，前端只显示删除失败」，且没有任何网络层报错。
//
// reason 必填是 Q77-1 §11.8 的定案（后端 archiveReasonMinLength = 2），
// 缺省时这里不抛错、让后端统一回 400 —— 校验提示由页面提前拦截。
export function delQuestions(data, reason) {
  return request({
    url: `/api/questions/${data.id}`,
    method: 'delete',
    params: buildDeleteQuestionParams(reason)
  })
}

export function createQuestions(data) {
  return request({
    url: `/api/questions`,
    method: 'post',
    data
  })
}

export function importQuestions(data) {
  return request({
    url: `/api/questions/import`,
    method: 'post',
    data
  })
}
// 获取当前登录用户信息
export function getCurrentUser(feedback) {
  const config = {
    url: `/api/user/info`,
    method: 'get'
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function updateCurrentUserProfile(data) {
  return request({
    url: `/api/user/profile`,
    method: 'put',
    data,
  })
}

export function sendUserPhoneChangeSMSCode(data) {
  return request({
    url: `/api/user/phone/sms-code`,
    method: 'post',
    data
  })
}

export function updateCurrentUserPhone(data) {
  return request({
    url: `/api/user/phone`,
    method: 'put',
    data
  })
}

export function bindCurrentUserWechat(data) {
  return request({
    url: `/api/user/wechat/bind`,
    method: 'post',
    data
  })
}

export function bindCurrentUserWechatByCode(data) {
  return request({
    url: `/api/user/wechat/bind-code`,
    method: 'post',
    data
  })
}

export function resolveAdminWechatIDByScan(params) {
  return request({
    errorFeedback: 'local',
    url: `/api/user/wechat/scan-id`,
    method: 'get',
    params
  })
}

export function resolveAdminWechatIDByCode(data) {
  return request({
    errorFeedback: 'local',
    url: `/api/user/wechat/code-id`,
    method: 'post',
    data
  })
}

export function unbindCurrentUserWechat() {
  return request({
    url: `/api/user/wechat`,
    method: 'delete'
  })
}

// 获取用户列表（支持关键词 / 角色 / 状态筛选与分页）
export function getUserList(params) {
  return request({
    errorFeedback: 'local',
    url: `/api/user`,
    method: 'get',
    params
  })
}
// 删除用户
export function delUser(data) {
  return request({
    errorFeedback: 'local',
    url: `/api/user/${data.id}`,
    method: 'delete'
  })
}

// 新增用户
export function addUser(data) {
  return request({
    errorFeedback: 'local',
    url: `/api/user`,
    method: 'post',
    data
  })
}

// 更新用户
export function updateUser(data) {
  return request({
    errorFeedback: 'local',
    url: `/api/user/${data.id}`,
    method: 'put',
    data
  })
}

export function resetUserPassword(data) {
  return request({
    errorFeedback: 'local',
    url: `/api/user/${data.id}/reset-password`,
    method: 'put',
    data: {
      password: data.password,
    }
  })
}

export function adjustUserBalance(data) {
  return request({
    errorFeedback: 'local',
    url: `/api/user/${data.id}/balance`,
    method: 'post',
    // bucket 必须透传：管理员可选「充值本金 / 赠送金额」两个桶，
    // 漏掉它会让后端永远按默认本金桶处理（选赠金也不生效）。
    data: { mode: data.mode, amount: data.amount, bucket: data.bucket, showInStatement: data.showInStatement },
  })
}

export function updateBillingStatementVisibility(id, showInStatement) {
  return request({
    errorFeedback: 'local',
    url: `/api/billing-records/${id}/statement-visibility`,
    method: 'put',
    data: { showInStatement },
  })
}

export function getLogList(params) {
  return request({
    url: `/api/logs`,
    method: 'get',
    params
  })
}

/**
 * 取单条日志的敏感原文（仅管理员；路由挂 Auth + RequireAdmin）。
 *
 * 列表接口对非管理员只下发遮挡文本，原文只能走这里 ——
 * 「管理员有按钮可以显示原文」对应的就是这个请求。
 *
 * 响应：{ id, sensitiveFlag, sensitiveWords, sensitiveCheckedAt,
 *        originalDescription, originalOldAnswer, originalNewAnswer,
 *        maskedDescription, maskedOldAnswer, maskedNewAnswer }
 */
export function getLogSensitiveOriginal(id, signal) {
  return request({
    errorFeedback: 'local',
    url: `/api/logs/${encodeURIComponent(id)}/sensitive-original`,
    method: 'get',
    timeout: 15_000,
    signal,
  })
}

export function getBillingRecords(params) {
  return request({
    errorFeedback: 'local',
    url: `/api/billing-records`,
    method: 'get',
    params
  })
}

export function createRechargeOrder(data) {
  return request({
    errorFeedback: 'local',
    url: `/api/recharge-orders`,
    method: 'post',
    data
  })
}

export function getRechargeOrders(params) {
  return request({
    url: `/api/recharge-orders`,
    method: 'get',
    params
  })
}

export function getPaymentMethods(feedback) {
  const config = {
    url: `/api/payment-methods`,
    method: 'get'
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function createPaymentMethod(data, feedback) {
  const config = {
    url: `/api/payment-methods`,
    method: 'post',
    data
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function updatePaymentMethod(data, feedback) {
  const config = {
    url: `/api/payment-methods/${data.id}`,
    method: 'put',
    data
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function deletePaymentMethod(data, feedback) {
  const config = {
    url: `/api/payment-methods/${data.id}`,
    method: 'delete'
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function getSearchAccountConfigs(feedback) {
  const config = {
    url: `/api/search-account-configs`,
    method: 'get'
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function updateSearchAccountConfig(provider, data, feedback) {
  const config = {
    url: `/api/search-account-configs/${provider}`,
    method: 'put',
    data
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function getAISearchConfig() {
  return request({
    url: `/api/ai-search-config`,
    method: 'get'
  })
}

export function getAIModelPrices() {
  return request({
    url: `/api/ai-model-prices`,
    method: 'get'
  })
}

export function getAISearchConfigPricingStatus() {
  return request({
    url: `/api/ai-search-config/pricing-status`,
    method: 'get'
  })
}

export function getAIOCRCacheStatus(feedback) {
  const config = {
    url: `/api/ai-search-config/ocr-cache`,
    method: 'get'
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function getAIOCRCacheEntries(params, feedback) {
  const config = {
    url: `/api/ai-search-config/ocr-cache/entries`,
    method: 'get',
    params,
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function getAIOCRCacheImage(urlHash) {
  return request({
    errorFeedback: 'local',
    retryPolicy: 'single',
    url: `/api/ai-search-config/ocr-cache/entries/${encodeURIComponent(urlHash)}/image`,
    method: 'get',
    responseType: 'blob',
    timeout: 20 * 1000,
  })
}

export function clearAIOCRCache(params, feedback) {
  const config = {
    url: `/api/ai-search-config/ocr-cache`,
    method: 'delete',
    params,
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function deleteAIOCRCacheBatch(data) {
  return request({
    errorFeedback: 'local',
    url: `/api/ai-search-config/ocr-cache/batch-delete`,
    method: 'post',
    data,
  })
}

export function createAIModelPrice(data) {
  return request({
    url: `/api/ai-model-prices`,
    method: 'post',
    data
  })
}

export function updateAIModelPrice(data) {
  return request({
    url: `/api/ai-model-prices/${data.id}`,
    method: 'put',
    data
  })
}

export function deleteAIModelPrice(data) {
  return request({
    url: `/api/ai-model-prices/${data.id}`,
    method: 'delete'
  })
}

export function resetAIModelPrices() {
  return request({
    url: `/api/ai-model-prices/reset`,
    method: 'post'
  })
}

export function getAccessTokens(params = {}) {
  return request({
    errorFeedback: 'local',
    url: `/api/access-tokens`,
    method: 'get',
    params
  })
}

export function createAccessToken(data) {
  return request({
    errorFeedback: 'local',
    url: `/api/access-tokens`,
    method: 'post',
    data
  })
}

export function updateAccessToken(data) {
  return request({
    errorFeedback: 'local',
    url: `/api/access-tokens/${data.id}`,
    method: 'put',
    data
  })
}

export function deleteAccessToken(data) {
  return request({
    errorFeedback: 'local',
    url: `/api/access-tokens/${data.id}`,
    method: 'delete'
  })
}

// Script management is available to every authenticated user. The backend
// applies the owner/admin scope; the UI only uses `scope=all` for admins.
export function getScripts(params = {}) {
  return request({
    errorFeedback: 'local',
    url: `/api/scripts`,
    method: 'get',
    params,
  })
}

export function createScript(data, idempotencyKey) {
  return request({
    errorFeedback: 'local',
    retryPolicy: 'single',
    ...(idempotencyKey ? { headers: { 'Idempotency-Key': idempotencyKey } } : {}),
    url: `/api/scripts`,
    method: 'post',
    data,
  })
}

export function updateScript(data) {
  return request({
    errorFeedback: 'local',
    url: `/api/scripts/${data.id}`,
    method: 'put',
    data,
  })
}

export function deleteScript(data) {
  const sessionId = data.confirmActiveSessionId
  if (sessionId !== undefined && (typeof sessionId !== 'string' || !/^[1-9][0-9]{0,18}$/.test(sessionId))) {
    throw new TypeError('客户端会话确认无效，请刷新后重新确认。')
  }
  return request({
    errorFeedback: 'local',
    retryPolicy: 'single',
    url: `/api/scripts/${data.id}`,
    method: 'delete',
    ...(sessionId === undefined ? {} : { params: { confirmActiveSessionId: sessionId } }),
  })
}

export function getScriptVersions() {
  return request({
    errorFeedback: 'local',
    url: `/api/script-versions`,
    method: 'get',
  })
}

export function uploadScriptVersion(file, version, nativeLeaseProtocol = false) {
  const formData = new FormData()
  formData.append('version', String(version || ''))
  formData.append('file', file)
  if (nativeLeaseProtocol === true) formData.append('nativeLeaseProtocol', 'true')
  return request({
    errorFeedback: 'local',
    url: `/api/script-versions/upload`,
    method: 'post',
    data: formData,
    timeout: 120 * 1000,
  })
}

export function getScriptDomainSettings() {
  return request({
    errorFeedback: 'local',
    url: `/api/script-domain-settings`,
    method: 'get',
  })
}

export function updateScriptDomainSettings(data) {
  return request({
    errorFeedback: 'local',
    url: `/api/script-domain-settings`,
    method: 'put',
    data,
  })
}

// 用户侧只读：管理员配置的域名格式模板列表（诉求 ⑤）。
// 单独一个端点而不是复用 getScriptDomainSettings —— 后者带 RequireAdmin，
// 普通用户读不到，而「用户只能选择管理员配的格式」要求他能读到可选项。
export function getScriptDomainFormats() {
  return request({
    errorFeedback: 'local',
    url: `/api/script-domain-formats`,
    method: 'get',
  })
}

export function getScriptEscrowPolicy() {
  return request({ errorFeedback: 'local', url: `/api/script-escrow-policy`, method: 'get' })
}

export function updateScriptEscrowPolicy(data) {
  return request({ errorFeedback: 'local', retryPolicy: 'single', url: `/api/script-escrow-policy`, method: 'put', data })
}

export function getScriptActivationCodePrice() {
  return request({ errorFeedback: 'local', url: `/api/script-activation-code-price`, method: 'get' })
}

export function updateScriptActivationCodePrice(data) {
  return request({ errorFeedback: 'local', retryPolicy: 'single', url: `/api/script-activation-code-price`, method: 'put', data })
}

export function purchaseScriptActivationCode(idempotencyKey, quotedPrice) {
  return request({
    errorFeedback: 'local',
    retryPolicy: 'single',
    headers: { 'Idempotency-Key': idempotencyKey },
    url: `/api/script-activation-codes/purchase`,
    method: 'post',
    ...(quotedPrice ? { data: { quotedPrice } } : {}),
  })
}

export function redeemScriptActivationCode(data) {
  return request({
    errorFeedback: 'local',
    retryPolicy: 'single',
    url: `/api/script-activation-codes/redeem`,
    method: 'post',
    data,
  })
}

// A diagnostic snapshot is explicit-only: one request per mount or manual refresh.
export function getSystemMetrics() {
  return request({
    url: `/api/system-metrics`,
    method: 'get',
    retryPolicy: 'single',
  })
}

export function getSystemSettings(feedback) {
  const config = {
    url: `/api/system-settings`,
    method: 'get'
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function updateSystemSettings(data, feedback) {
  const config = {
    url: `/api/system-settings`,
    method: 'put',
    data
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function getAISearchModels(params) {
  return request({
    url: `/api/ai-search-config/models`,
    method: 'get',
    params
  })
}

export function testAISearchConfig(data) {
  return request({
    url: `/api/ai-search-config/test`,
    method: 'post',
    data,
    timeout: 0
  })
}

export function testAIToolCall(data, feedback) {
  const config = {
    url: `/api/ai-search-config/tool-test`,
    method: 'post',
    data,
    timeout: 0
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function updateAISearchConfig(data) {
  return request({
    url: `/api/ai-search-config`,
    method: 'put',
    data
  })
}

const ticketRefundBase = '/api/ticket-refunds'

function ticketRefundRequest(config) {
  return request({ errorFeedback: 'local', retryPolicy: 'single', ...config })
}

function ticketRefundItem(id, action) {
  return ticketRefundBase + '/' + encodeURIComponent(id) + '/' + action
}

export function getEligibleTicketRefundOrders(signal) {
  return ticketRefundRequest({ url: ticketRefundBase + '/eligible-orders', method: 'get', signal })
}

export function listTicketRefunds(signal) {
  return ticketRefundRequest({ url: ticketRefundBase, method: 'get', signal })
}

export function createTicketRefund(data, signal) {
  return ticketRefundRequest({ url: ticketRefundBase, method: 'post', data, signal })
}

export function reviewTicketRefund(id, data, signal) {
  return ticketRefundRequest({ url: ticketRefundItem(id, 'decision'), method: 'post', data, signal })
}

export function processTicketRefund(id, data, signal) {
  return ticketRefundRequest({ url: ticketRefundItem(id, 'process'), method: 'post', data, signal })
}

export function reconcileTicketRefund(id, data, signal) {
  return ticketRefundRequest({ url: ticketRefundItem(id, 'reconcile'), method: 'post', data, signal })
}

export function uploadTicketRefundProof(id, file, signal) {
  const data = new FormData()
  data.append('file', file)
  return ticketRefundRequest({ url: ticketRefundItem(id, 'proof'), method: 'post', data, signal })
}

export function getTicketRefundProof(id, signal) {
  return ticketRefundRequest({
    url: ticketRefundItem(id, 'proof'), method: 'get', responseType: 'blob', timeout: 20_000, signal,
  })
}
