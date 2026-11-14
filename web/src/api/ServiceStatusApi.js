import request from '../utils/request'

/**
 * 服务健康摘要（普通用户可见）。
 *
 * 后端路由：apiRouter.GET("/service-status/summary", controller.AuthOrAccessToken, ...)
 * 之所以走 AuthOrAccessToken 而不是 RequireAdmin：这是给普通用户看的
 * 「现在好不好」指示，不是运维看板。
 *
 * ⚠️ 刻意**不**提供 admin 版本（/api/admin/service-status）的封装：
 * 本页只做摘要视图。逐分钟明细只对管理员开放，在这里加一个封装等于
 * 给后续维护者留一条「拿不到摘要就降级去要明细」的歧路。
 */
export function getServiceStatusSummary() {
  return request({
    url: '/api/service-status/summary',
    method: 'get',
    retryPolicy: 'single',
  })
}
