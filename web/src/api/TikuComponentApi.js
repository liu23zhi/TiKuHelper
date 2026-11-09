import request from '../utils/request'


export function login(params) {
  return request({
    url: `/adapter-service/user/login`,
    method: 'post',
    data: params
  });
}

export function logout() {
  return request({
    url: `/adapter-service/user/logout`,
    method: 'post'
  });
}

export function checkUserSession(signal) {
  return request({
    url: '/adapter-service/user/session',
    method: 'get',
    timeout: 10_000,
    retryPolicy: 'single',
    signal,
  })
}

export function registerUser(data) {
  return request({
    url: `/adapter-service/user/register`,
    method: 'post',
    data
  })
}

export function getPublicRegisterStatus() {
  return request({
    url: `/adapter-service/public/register-status`,
    method: 'get'
  })
}

export function sendPublicSMSCode(data) {
  return request({
    url: `/adapter-service/public/sms-code`,
    method: 'post',
    data
  })
}

export function createCaptchaChallenge(data) {
  return request({
    url: `/adapter-service/public/captcha/challenge`,
    method: 'post',
    data
  })
}

export function verifyCaptchaChallenge(data) {
  return request({
    url: `/adapter-service/public/captcha/verify`,
    method: 'post',
    data
  })
}

export function verifyRemoteCaptcha(data) {
  return request({
    url: `/adapter-service/public/captcha/remote-token`,
    method: 'post',
    data
  })
}

export function createWechatRegisterSession(data = {}) {
  return request({
    url: `/adapter-service/public/wechat-register/session`,
    method: 'post',
    data,
  })
}

export function createWechatRegisterQRCode(data) {
  return request({
    url: `/adapter-service/public/wechat-register/qrcode`,
    method: 'post',
    data,
  })
}

export function getWechatRegisterStatus(params) {
  return request({
    url: `/adapter-service/public/wechat-register/status`,
    method: 'get',
    params,
  })
}

export function stageWechatRegisterCode(data) {
  return request({
    url: `/adapter-service/public/wechat-register/code`,
    method: 'post',
    data,
  })
}

export function releaseWechatRegisterFlow(data) {
  return request({
    url: `/adapter-service/public/wechat-register/release`,
    method: 'post',
    data,
  })
}

export function createWechatLoginQRCode(feedback) {
  const config = {
    url: `/adapter-service/public/wechat-login/qrcode`,
    method: 'post'
  }
  // Only this narrow caller option is accepted; zero-argument consumers stay unchanged.
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function getWechatLoginStatus(params) {
  return request({
    url: `/adapter-service/public/wechat-login/status`,
    method: 'get',
    params
  })
}

export function loginWechatByCode(data) {
  return request({
    url: `/adapter-service/public/wechat-login/code`,
    method: 'post',
    data
  })
}

export function bindWechatLogin(data) {
  return request({
    url: `/adapter-service/public/wechat-login/bind`,
    method: 'post',
    data
  })
}

export function loginBySMS(data) {
  return request({
    url: `/adapter-service/user/login/sms`,
    method: 'post',
    data
  })
}


export function getCourses(params) {
  return request({
    url: `/adapter-service/courses`,
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
    url: `/adapter-service/uapi/phoneinfo`,
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
    url: `/adapter-service/uapi/ipinfo`,
    method: 'get',
    params,
    timeout: 0
  })
}

export function getMyLocations(params = {}) {
  return request({
    url: `/adapter-service/uapi/my-locations`,
    method: 'get',
    params,
    timeout: 0
  })
}

export function getLocationCacheList(params) {
  return request({
    url: `/adapter-service/location-cache`,
    method: 'get',
    params
  })
}

export function deleteLocationCache(params) {
  return request({
    url: `/adapter-service/location-cache`,
    method: 'delete',
    params
  })
}

export function deleteLocationCacheBatch(data) {
  return request({
    url: `/adapter-service/location-cache/batch-delete`,
    method: 'post',
    data,
  })
}

export function refreshLocationCacheBatch(data) {
  return request({
    url: `/adapter-service/location-cache/batch-refresh`,
    method: 'post',
    data,
    timeout: 0
  })
}

export function refreshLocationCache(data) {
  return request({
    url: `/adapter-service/location-cache/refresh`,
    method: 'post',
    data,
    timeout: 0
  })
}

export function manualLookupLocationCache(data) {
  return request({
    url: `/adapter-service/location-cache/manual`,
    method: 'post',
    data,
    timeout: 0
  })
}

export function getUAPISearchEngines() {
  return request({
    url: `/adapter-service/uapi/search/engines`,
    method: 'get'
  })
}

// UAPI status/usage returns endpoint-level usage statistics. Keep the raw
// response in the page so both aggregate and single-endpoint responses can be
// displayed without losing fields added by the upstream API.
export function getUAPIUsageStatus(params) {
  return request({
    url: `/adapter-service/uapi/status/usage`,
    method: 'get',
    params
  })
}

export function searchUAPIAggregate(data) {
  return request({
    url: `/adapter-service/uapi/search/aggregate`,
    method: 'post',
    data,
    timeout: 65000
  })
}

export function getQuestions(data) {
  return request({
    url: `/adapter-service/questions/search`,
    method: 'post',
    data
  })
}

export function updateQuestions(data) {
  return request({
    url: `/adapter-service/questions/${data.id}`,
    method: 'put',
    data
  })
}

export function delQuestions(data) {
  return request({
    url: `/adapter-service/questions/${data.id}`,
    method: 'delete'
  })
}

export function createQuestions(data) {
  return request({
    url: `/adapter-service/questions`,
    method: 'post',
    data
  })
}

export function importQuestions(data) {
  return request({
    url: `/adapter-service/questions/import`,
    method: 'post',
    data
  })
}
// 获取当前登录用户信息
export function getCurrentUser(feedback) {
  const config = {
    url: `/adapter-service/user/info`,
    method: 'get'
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function updateCurrentUserProfile(data) {
  return request({
    url: `/adapter-service/user/profile`,
    method: 'put',
    data,
  })
}

export function sendUserPhoneChangeSMSCode(data) {
  return request({
    url: `/adapter-service/user/phone/sms-code`,
    method: 'post',
    data
  })
}

export function updateCurrentUserPhone(data) {
  return request({
    url: `/adapter-service/user/phone`,
    method: 'put',
    data
  })
}

export function bindCurrentUserWechat(data) {
  return request({
    url: `/adapter-service/user/wechat/bind`,
    method: 'post',
    data
  })
}

export function bindCurrentUserWechatByCode(data) {
  return request({
    url: `/adapter-service/user/wechat/bind-code`,
    method: 'post',
    data
  })
}

export function resolveAdminWechatIDByScan(params) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/user/wechat/scan-id`,
    method: 'get',
    params
  })
}

export function resolveAdminWechatIDByCode(data) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/user/wechat/code-id`,
    method: 'post',
    data
  })
}

export function unbindCurrentUserWechat() {
  return request({
    url: `/adapter-service/user/wechat`,
    method: 'delete'
  })
}

// 获取用户列表（支持关键词 / 角色 / 状态筛选与分页）
export function getUserList(params) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/user`,
    method: 'get',
    params
  })
}
// 删除用户
export function delUser(data) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/user/${data.id}`,
    method: 'delete'
  })
}

// 新增用户
export function addUser(data) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/user`,
    method: 'post',
    data
  })
}

// 更新用户
export function updateUser(data) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/user/${data.id}`,
    method: 'put',
    data
  })
}

export function resetUserPassword(data) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/user/${data.id}/reset-password`,
    method: 'put',
    data: {
      password: data.password,
    }
  })
}

export function adjustUserBalance(data) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/user/${data.id}/balance`,
    method: 'post',
    data: { mode: data.mode, amount: data.amount, showInStatement: data.showInStatement },
  })
}

export function updateBillingStatementVisibility(id, showInStatement) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/billing-records/${id}/statement-visibility`,
    method: 'put',
    data: { showInStatement },
  })
}

export function getLogList(params) {
  return request({
    url: `/adapter-service/logs`,
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
    url: `/adapter-service/logs/${encodeURIComponent(id)}/sensitive-original`,
    method: 'get',
    timeout: 15_000,
    signal,
  })
}

export function getBillingRecords(params) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/billing-records`,
    method: 'get',
    params
  })
}

export function createRechargeOrder(data) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/recharge-orders`,
    method: 'post',
    data
  })
}

export function getRechargeOrders(params) {
  return request({
    url: `/adapter-service/recharge-orders`,
    method: 'get',
    params
  })
}

export function getPaymentMethods(feedback) {
  const config = {
    url: `/adapter-service/payment-methods`,
    method: 'get'
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function createPaymentMethod(data, feedback) {
  const config = {
    url: `/adapter-service/payment-methods`,
    method: 'post',
    data
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function updatePaymentMethod(data, feedback) {
  const config = {
    url: `/adapter-service/payment-methods/${data.id}`,
    method: 'put',
    data
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function deletePaymentMethod(data, feedback) {
  const config = {
    url: `/adapter-service/payment-methods/${data.id}`,
    method: 'delete'
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function getSearchAccountConfigs(feedback) {
  const config = {
    url: `/adapter-service/search-account-configs`,
    method: 'get'
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function updateSearchAccountConfig(provider, data, feedback) {
  const config = {
    url: `/adapter-service/search-account-configs/${provider}`,
    method: 'put',
    data
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function getAISearchConfig() {
  return request({
    url: `/adapter-service/ai-search-config`,
    method: 'get'
  })
}

export function getAIModelPrices() {
  return request({
    url: `/adapter-service/ai-model-prices`,
    method: 'get'
  })
}

export function getAISearchConfigPricingStatus() {
  return request({
    url: `/adapter-service/ai-search-config/pricing-status`,
    method: 'get'
  })
}

export function getAIOCRCacheStatus(feedback) {
  const config = {
    url: `/adapter-service/ai-search-config/ocr-cache`,
    method: 'get'
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function getAIOCRCacheEntries(params, feedback) {
  const config = {
    url: `/adapter-service/ai-search-config/ocr-cache/entries`,
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
    url: `/adapter-service/ai-search-config/ocr-cache/entries/${encodeURIComponent(urlHash)}/image`,
    method: 'get',
    responseType: 'blob',
    timeout: 20 * 1000,
  })
}

export function clearAIOCRCache(params, feedback) {
  const config = {
    url: `/adapter-service/ai-search-config/ocr-cache`,
    method: 'delete',
    params,
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function deleteAIOCRCacheBatch(data) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/ai-search-config/ocr-cache/batch-delete`,
    method: 'post',
    data,
  })
}

export function createAIModelPrice(data) {
  return request({
    url: `/adapter-service/ai-model-prices`,
    method: 'post',
    data
  })
}

export function updateAIModelPrice(data) {
  return request({
    url: `/adapter-service/ai-model-prices/${data.id}`,
    method: 'put',
    data
  })
}

export function deleteAIModelPrice(data) {
  return request({
    url: `/adapter-service/ai-model-prices/${data.id}`,
    method: 'delete'
  })
}

export function resetAIModelPrices() {
  return request({
    url: `/adapter-service/ai-model-prices/reset`,
    method: 'post'
  })
}

export function getAccessTokens(params = {}) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/access-tokens`,
    method: 'get',
    params
  })
}

export function createAccessToken(data) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/access-tokens`,
    method: 'post',
    data
  })
}

export function updateAccessToken(data) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/access-tokens/${data.id}`,
    method: 'put',
    data
  })
}

export function deleteAccessToken(data) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/access-tokens/${data.id}`,
    method: 'delete'
  })
}

// Script management is available to every authenticated user. The backend
// applies the owner/admin scope; the UI only uses `scope=all` for admins.
export function getScripts(params = {}) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/scripts`,
    method: 'get',
    params,
  })
}

export function createScript(data, idempotencyKey) {
  return request({
    errorFeedback: 'local',
    retryPolicy: 'single',
    ...(idempotencyKey ? { headers: { 'Idempotency-Key': idempotencyKey } } : {}),
    url: `/adapter-service/scripts`,
    method: 'post',
    data,
  })
}

export function updateScript(data) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/scripts/${data.id}`,
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
    url: `/adapter-service/scripts/${data.id}`,
    method: 'delete',
    ...(sessionId === undefined ? {} : { params: { confirmActiveSessionId: sessionId } }),
  })
}

export function getScriptVersions() {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/script-versions`,
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
    url: `/adapter-service/script-versions/upload`,
    method: 'post',
    data: formData,
    timeout: 120 * 1000,
  })
}

export function getScriptDomainSettings() {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/script-domain-settings`,
    method: 'get',
  })
}

export function updateScriptDomainSettings(data) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/script-domain-settings`,
    method: 'put',
    data,
  })
}

export function getScriptEscrowPolicy() {
  return request({ errorFeedback: 'local', url: `/adapter-service/script-escrow-policy`, method: 'get' })
}

export function updateScriptEscrowPolicy(data) {
  return request({ errorFeedback: 'local', retryPolicy: 'single', url: `/adapter-service/script-escrow-policy`, method: 'put', data })
}

export function getScriptActivationCodePrice() {
  return request({ errorFeedback: 'local', url: `/adapter-service/script-activation-code-price`, method: 'get' })
}

export function updateScriptActivationCodePrice(data) {
  return request({ errorFeedback: 'local', retryPolicy: 'single', url: `/adapter-service/script-activation-code-price`, method: 'put', data })
}

export function purchaseScriptActivationCode(idempotencyKey, quotedPrice) {
  return request({
    errorFeedback: 'local',
    retryPolicy: 'single',
    headers: { 'Idempotency-Key': idempotencyKey },
    url: `/adapter-service/script-activation-codes/purchase`,
    method: 'post',
    ...(quotedPrice ? { data: { quotedPrice } } : {}),
  })
}

export function redeemScriptActivationCode(data) {
  return request({
    errorFeedback: 'local',
    retryPolicy: 'single',
    url: `/adapter-service/script-activation-codes/redeem`,
    method: 'post',
    data,
  })
}

// A diagnostic snapshot is explicit-only: one request per mount or manual refresh.
export function getSystemMetrics() {
  return request({
    url: `/adapter-service/system-metrics`,
    method: 'get',
    retryPolicy: 'single',
  })
}

export function getSystemSettings(feedback) {
  const config = {
    url: `/adapter-service/system-settings`,
    method: 'get'
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function updateSystemSettings(data, feedback) {
  const config = {
    url: `/adapter-service/system-settings`,
    method: 'put',
    data
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function getAISearchModels(params) {
  return request({
    url: `/adapter-service/ai-search-config/models`,
    method: 'get',
    params
  })
}

export function testAISearchConfig(data) {
  return request({
    url: `/adapter-service/ai-search-config/test`,
    method: 'post',
    data,
    timeout: 0
  })
}

export function testAIToolCall(data, feedback) {
  const config = {
    url: `/adapter-service/ai-search-config/tool-test`,
    method: 'post',
    data,
    timeout: 0
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function updateAISearchConfig(data) {
  return request({
    url: `/adapter-service/ai-search-config`,
    method: 'put',
    data
  })
}

const ticketRefundBase = '/adapter-service/ticket-refunds'

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
