import request from '../utils/request'


export function login(params) {
  return request({
    url: `/adapter-service/user/login`,
    method: 'get',
    params
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


export function getPlat() {
  return request({
    url: `/adapter-service/plat`,
    method: 'get',
  })
}

export function getCourses(params) {
  return request({
    url: `/adapter-service/courses`,
    method: 'get',
    params
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

// 获取用户列表
export function getUserList() {
  return request({
    url: `/adapter-service/user`,
    method: 'get'
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

export function getAIModelArchitectures() {
  return request({
    url: `/adapter-service/ai-model-architectures`,
    method: 'get'
  })
}

export function createAIModelArchitecture(data) {
  return request({
    url: `/adapter-service/ai-model-architectures`,
    method: 'post',
    data
  })
}

export function updateAIModelArchitecture(data) {
  return request({
    url: `/adapter-service/ai-model-architectures/${data.id}`,
    method: 'put',
    data
  })
}

export function deleteAIModelArchitecture(data) {
  return request({
    url: `/adapter-service/ai-model-architectures/${data.id}`,
    method: 'delete'
  })
}

export function getAccessTokens() {
  return request({
    url: `/adapter-service/access-tokens`,
    method: 'get'
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
    data
  })
}

export function updateAISearchConfig(data) {
  return request({
    url: `/adapter-service/ai-search-config`,
    method: 'put',
    data
  })
}
