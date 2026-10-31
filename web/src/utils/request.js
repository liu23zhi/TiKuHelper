import axios from 'axios';
import { advanceCurrentSessionRevision, clearStoredCurrentUser, getCurrentSessionRevision } from './currentUser'
import { message } from 'ant-design-vue';
import { buildAuthFailureHandling, buildResponsePayload, isPublicWechatRegisterRequest } from './requestHelpers.mjs'
import { executeWithRetry, toUserRequestError } from './requestRetry.mjs'

// 创建axios实例
const service = axios.create({
  baseURL: "/",
  timeout: 60 * 1000,
  withCredentials: true,
  validateStatus: function (status) {
    return status >= 200 && status < 600
  }
});
const responseInterceptor = (response, sessionRevision) => {
  if (response.status === 200 && typeof response.data?.jwt === 'string' && response.data.jwt.trim()) {
    advanceCurrentSessionRevision()
  }
  switch (response.status) {
    case 200:
      break;
    case 401:
    case 403: {
      // A previous session's late response must not clear a successful new login.
      if (sessionRevision !== getCurrentSessionRevision()) break
      if (response.status === 401 && isPublicWechatRegisterRequest(response?.config?.url)) {
        break
      }
      const handling = buildAuthFailureHandling(response)
      message.error(handling.userMessage)
      if (handling.shouldLogout) {
        clearStoredCurrentUser()
        window.location.href = '/login'
      }
      break;
    }
    default:
      console.log(response.data?.message);
      break;
  }
  return buildResponsePayload(response);
};

export default async function request(config) {
  const sessionRevision = getCurrentSessionRevision()
  try {
    const response = await executeWithRetry((attemptConfig) => service.request(attemptConfig), config)
    return responseInterceptor(response, sessionRevision)
  } catch (error) {
    throw toUserRequestError(config, error)
  }
}
