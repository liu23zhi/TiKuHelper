import axios from 'axios';
import { clearStoredCurrentUser, captureCurrentUserLease, getCurrentSessionRevision, advanceCurrentSessionRevision } from './currentUser'
import { message } from 'ant-design-vue';
import { buildAuthFailureHandling, buildResponsePayload, isPublicWechatRegisterRequest, captureTransportRequestError } from './requestHelpers.mjs'
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
const responseInterceptor = (response, lease, config, sessionRevision) => {
  if (response.status === 200 && typeof response.data?.jwt === 'string' && response.data.jwt.trim()
    && !lease.isRetired() && sessionRevision === getCurrentSessionRevision()) {
    advanceCurrentSessionRevision()
  }
  switch (response.status) {
    case 200:
      break;
    case 401:
    case 403: {
      if (response.status === 401 && isPublicWechatRegisterRequest(response?.config?.url)) {
        break
      }
      // Deliver the original payload/diagnostic ID, but never affect a later login.
      if (!lease.isCurrent() || sessionRevision !== getCurrentSessionRevision()) break
      const handling = buildAuthFailureHandling(response)
      // A caller with owned persistent feedback suppresses only this toast.
      // Authentication, cleanup, lease checks and the original payload remain.
      if (config?.errorFeedback !== 'local') message.error(handling.userMessage)
      if (handling.shouldLogout) {
        clearStoredCurrentUser()
        window.location.href = '/login'
      }
      break;
    }
    default:
      break;
  }
  return buildResponsePayload(response);
};

export default async function request(config) {
  const lease = captureCurrentUserLease()
  const sessionRevision = getCurrentSessionRevision()
  let dispatched = false
  try {
    const response = await executeWithRetry((attemptConfig) => {
      // Never replay A's intent with B's cookie after an observed ownership change.
      // Unknown storage does not block the first dispatch or imply retirement.
      if (dispatched && lease.isRetired()) {
        const error = new Error('请求归属已变更，已停止旧请求重试。')
        error.name = 'AbortError'
        error.code = 'ERR_CANCELED'
        throw error
      }
      dispatched = true
      return service.request(attemptConfig)
    }, config)
    return responseInterceptor(response, lease, config, sessionRevision)
  } catch (error) {
    throw captureTransportRequestError(toUserRequestError(config, error))
  } finally {
    lease.release()
  }
}
