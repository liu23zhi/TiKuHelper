import axios from 'axios';
import { clearStoredCurrentUser } from './currentUser'
import { message } from 'ant-design-vue';
import { buildAuthFailureHandling, buildResponsePayload, isPublicWechatRegisterRequest } from './requestHelpers.mjs'

// 创建axios实例
const service = axios.create({
  baseURL: "/",
  timeout: 60 * 1000,
  withCredentials: true,
  validateStatus: function (status) {
    return status >= 200 && status <= 500
  }
});
// request拦截器
service.interceptors.request.use(
  (config) => {
    return config;
  },
  (error) => {
    console.log(error, "error-request");
    Promise.reject(error);
  }
);
// 这里应该拦截 400 或者 500 的请求，然后给出用户提示，待完善
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
      console.log(response.data.message); // 将 msg 展示给用户
      break;
  }
  return buildResponsePayload(response);
};
const errorInterceptor = (error) => {
  // 给出网络错误提示或者各种请求失败的
  return Promise.reject(error);
};
service.interceptors.response.use(responseInterceptor, errorInterceptor);

export default service;
