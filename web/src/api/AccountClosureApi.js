import request from '../utils/request'

// 注销申请（task_doc/76 §3.3.5）。三条都是「本人」接口，后端一律用当前登录用户，
// 不接受前端传 userID。
//
//   GET  /api/user/closure-status    当前状态、剩余天数、是否可自助注销
//   POST /api/user/closure-request   申请：需当前密码 + 知情确认 + 有手机号
//   POST /api/user/closure-cancel    撤销：等待期内随时可撤销
//
// 全部使用 errorFeedback: 'local'：Profile 页自己有失败态展示，
// 不在被页面接管的请求上再弹一次全局错误。

export function getClosureStatus() {
  return request({
    errorFeedback: 'local',
    url: '/api/user/closure-status',
    method: 'get',
  })
}

// acknowledge 是「我已了解：注销后账号将移入归档，无法自行恢复」的勾选结果。
// 由**用户**勾选后传 true；缺省一律 false（后端也会拒绝），
// 前端不能替用户默认勾上不可撤销操作的知情确认。
export function requestAccountClosure(password, acknowledge) {
  return request({
    errorFeedback: 'local',
    url: '/api/user/closure-request',
    method: 'post',
    data: { password, acknowledge: acknowledge === true },
  })
}

export function cancelAccountClosure() {
  return request({
    errorFeedback: 'local',
    url: '/api/user/closure-cancel',
    method: 'post',
  })
}
