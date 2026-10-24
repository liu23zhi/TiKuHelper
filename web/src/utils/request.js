import axios from 'axios';
import { clearStoredCurrentUser } from './currentUser'
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
const responseInterceptor = (response) => {
  switch (response.status) {
    case 200:
      break;
    case 401:
    case 403: {
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
  try {
    const response = await executeWithRetry((attemptConfig) => service.request(attemptConfig), config)
    return responseInterceptor(response)
  } catch (error) {
    throw toUserRequestError(config, error)
  }
}
