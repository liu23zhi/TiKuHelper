import request from '../utils/request'

/**
 * 存储状态（task_doc/108:297、109:474/486）。
 *
 * 后端路由：apiRouter.GET("/system/storage-status", controller.Auth, ...)
 *
 * # 为什么这里只有「读」
 *
 * 存储驱动由启动配置决定，运行期不可切换 —— 所以没有对应的写接口。
 * 若将来要支持切换，那是「重启并改配置」，不是本接口的职责。
 *
 * # 为什么 errorFeedback: 'local'
 *
 * 调用方是布局里的悬浮警告条，它自己维护失败态（静默不显示）。
 * 用全局提示会让「警告接口恰好没有」变成一条红色 toast ——
 * 用户本来正是因为存储降级才被提醒，再叠一条取数失败只会更混乱。
 */
export function getStorageStatus() {
  return request({
    errorFeedback: 'local',
    url: '/api/system/storage-status',
    method: 'get',
    retryPolicy: 'single',
  })
}
