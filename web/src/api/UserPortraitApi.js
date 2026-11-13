import request from '../utils/request'

// 用户画像的两个入口（T-17 / task_doc/98）。
//
// 后端契约（internal/api/router.go，由 captain 接线；handler 见 internal/controller/user_portrait.go）：
//   GET /adapter-service/admin/portrait/overview    全站整体画像（侧边栏「用户画像」页默认视图）
//   GET /adapter-service/admin/users/:id/portrait   单个用户画像（用户管理操作列「画像」按钮）
//
// 两条都挂 controller.Auth + middleware.RequireAdmin，且 handler 内部**二次校验**角色
// （98 号 §0 第 ① ② 层）。普通用户 token 直调会拿到 403。
//
// ⚠️ 这两条接口返回的是**他人**的深度信息（区域、金额、工单、注册来源）。
// 不要复用给普通用户路径：98 号 §0 明确 `GET /user/profile/me`（只看自己）
// 与画像必须是两条不同的接口。
//
// 全部 errorFeedback: 'local'：页面自己有持久失败态与重试入口，
// 不能在被页面接管的请求上再弹一次全局错误提示（与 UserBindingApi.js 同约定）。

// readPortraitUserId 校验目标用户 id。
//
// 非正整数在拼 URL 之前就拒绝：避免把 /undefined、/NaN 这类路径打到后端，
// 那类 404 与「用户不存在」在响应上无法区分，排查时会白跑一圈。
function readPortraitUserId(userId) {
  const value = Number(userId)
  if (!Number.isInteger(value) || value <= 0) throw new Error('用户参数错误')
  return value
}

// 全站整体画像。
export function getPortraitOverview() {
  return request({
    errorFeedback: 'local',
    url: '/adapter-service/admin/portrait/overview',
    method: 'get',
  })
}

// 单个用户画像。userId 是**目标用户**，与当前登录用户无关。
export function getUserPortrait(userId) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/admin/users/${readPortraitUserId(userId)}/portrait`,
    method: 'get',
  })
}
