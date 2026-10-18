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

export function createWechatLoginQRCode() {
  return request({
    url: `/adapter-service/public/wechat-login/qrcode`,
    method: 'post'
  })
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

export function refreshLocationCache(data) {
  return request({
    url: `/adapter-service/location-cache/refresh`,
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
export function getCurrentUser() {
  return request({
    url: `/adapter-service/user/info`,
    method: 'get'
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
    url: `/adapter-service/user/wechat/scan-id`,
    method: 'get',
    params
  })
}

export function resolveAdminWechatIDByCode(data) {
  return request({
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
    url: `/adapter-service/user`,
    method: 'get',
    params
  })
}
// 删除用户
export function delUser(data) {
  return request({
    url: `/adapter-service/user/${data.id}`,
    method: 'delete'
  })
}

// 新增用户
export function addUser(data) {
  return request({
    url: `/adapter-service/user`,
    method: 'post',
    data
  })
}

// 更新用户
export function updateUser(data) {
  return request({
    url: `/adapter-service/user/${data.id}`,
    method: 'put',
    data
  })
}

export function resetUserPassword(data) {
  return request({
    url: `/adapter-service/user/${data.id}/reset-password`,
    method: 'put',
    data: {
      password: data.password,
    }
  })
}

export function getLogList(params) {
  return request({
    url: `/adapter-service/logs`,
    method: 'get',
    params
  })
}

export function getBillingRecords(params) {
  return request({
    url: `/adapter-service/billing-records`,
    method: 'get',
    params
  })
}

export function createRechargeOrder(data) {
  return request({
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

export function getPaymentMethods() {
  return request({
    url: `/adapter-service/payment-methods`,
    method: 'get'
  })
}

export function createPaymentMethod(data) {
  return request({
    url: `/adapter-service/payment-methods`,
    method: 'post',
    data
  })
}

export function updatePaymentMethod(data) {
  return request({
    url: `/adapter-service/payment-methods/${data.id}`,
    method: 'put',
    data
  })
}

export function deletePaymentMethod(data) {
  return request({
    url: `/adapter-service/payment-methods/${data.id}`,
    method: 'delete'
  })
}

export function getSearchAccountConfigs() {
  return request({
    url: `/adapter-service/search-account-configs`,
    method: 'get'
  })
}

export function updateSearchAccountConfig(provider, data) {
  return request({
    url: `/adapter-service/search-account-configs/${provider}`,
    method: 'put',
    data
  })
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
    url: `/adapter-service/access-tokens`,
    method: 'get',
    params
  })
}

export function createAccessToken(data) {
  return request({
    url: `/adapter-service/access-tokens`,
    method: 'post',
    data
  })
}

export function updateAccessToken(data) {
  return request({
    url: `/adapter-service/access-tokens/${data.id}`,
    method: 'put',
    data
  })
}

export function deleteAccessToken(data) {
  return request({
    url: `/adapter-service/access-tokens/${data.id}`,
    method: 'delete'
  })
}

export function getSystemSettings() {
  return request({
    url: `/adapter-service/system-settings`,
    method: 'get'
  })
}

export function updateSystemSettings(data) {
  return request({
    url: `/adapter-service/system-settings`,
    method: 'put',
    data
  })
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

export function updateAISearchConfig(data) {
  return request({
    url: `/adapter-service/ai-search-config`,
    method: 'put',
    data
  })
}
