import request from '../utils/request'

// 设备管理（137-设备体系设计-授权与黑名单.md §8.2 / §9.2）。
//
// 后端契约（internal/controller/device_admin.go）：
//
//	GET    /api/admin/devices                  列表：分页 + keyword / status
//	POST   /api/admin/devices/:id/unbind       强制解绑（:id 是**绑定行主键**）
//	POST   /api/admin/device-bans              封禁：reason 必填，expiresAt 空 = 永久
//	DELETE /api/admin/device-bans/:deviceId    解封（:deviceId 是**设备 ID**）
//
// # 四个必须知道的语义（都是后端实测行为，不是猜测）
//
//  1. **封禁/解封走 /admin/device-bans 前缀，不走 /admin/devices/ban**（137 §8.3）。
//     原因：`POST /admin/devices/:id/unbind` 已把 `devices/` 的下一段做成参数节点，
//     同层再放静态段 `ban` 在旧版 gin 上会 panic（新版虽允许，但语义也不清楚：
//     ban 的对象是 device_id、unbind 的对象是绑定行 id，不是同一类资源）。
//
//  2. **解绑 ≠ 解封**。`unbind` 只写 revoked_at（revoked_reason='admin_unbound'），
//     绝不写黑名单；设备下次请求会静默重绑。要真正拦死一台设备必须 `ban`。
//
//  3. **deviceId 必须 encodeURIComponent**：设备指纹形如 `exe:a1b2c3`，含 `:`。
//     不编码时 `:` 会被某些代理/网关当作路径参数分隔符处理。
//
//  4. **expiresAt 空串 = 永久封禁**，不是「不设置」。与账号封禁（UserBanApi.js）同口径。
//
// 全部使用 errorFeedback: 'local'：页面自己有失败态与重试入口，
// 不能在被页面接管的请求上再弹一次全局错误提示（与 UserBanApi.js / BlacklistApi.js 同约定）。

// 列表：keyword 同时匹配 device_id 与 username，status 取 active/revoked/banned。
export function getAdminDevices(params) {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/devices',
    method: 'get',
    params,
  })
}

// 强制解绑：参数是**绑定行 id**（不是 deviceId）。
//
// 为什么不能用 deviceId 解绑：同一台设备可能被多个账号轮流用过，
// 按 deviceId 解绑会一次断掉所有账号 —— 那是封禁的口径，不是解绑。
export function unbindAdminDevice(bindingId) {
  const id = Number(bindingId)
  if (!Number.isInteger(id) || id <= 0) throw new Error('解绑参数错误')
  return request({
    errorFeedback: 'local',
    url: `/api/admin/devices/${id}/unbind`,
    method: 'post',
  })
}

// 封禁设备：expiresAt 由调用方显式给出（null/空 = 永久）。
//
// durationAmount / durationUnit 是「自定义时长封禁」入口：传了时长就由**服务端**
// 算到期时间（durationAmount>0 时优先于 expiresAt）。不让前端自己算绝对时间：
// 前端算时间会吃到浏览器时区与时钟偏差，而「封 7 天」是服务端语义。
export function banAdminDevice(payload) {
  const deviceId = String(payload?.deviceId == null ? '' : payload.deviceId).trim()
  if (!deviceId) throw new Error('设备 ID 不能为空')
  const reason = String(payload?.reason == null ? '' : payload.reason).trim()
  if (!reason) throw new Error('封禁原因不能为空')
  const expiresAt = String(payload?.expiresAt == null ? '' : payload.expiresAt).trim()
  const durationAmount = Number(payload?.durationAmount || 0)
  if (!Number.isInteger(durationAmount) || durationAmount < 0) {
    throw new Error('封禁时长必须是正整数')
  }
  const data = { deviceId, reason, expiresAt }
  // 只在真的传了时长时才带这两个字段：带 durationAmount:0 语义上等于「永久」，
  // 但让请求体看起来和永久封禁一样，排查时无法区分两种意图。
  if (durationAmount > 0) {
    data.durationAmount = durationAmount
    data.durationUnit = String(payload?.durationUnit || 'hour').trim() || 'hour'
  }
  return request({
    errorFeedback: 'local',
    url: '/api/admin/device-bans',
    method: 'post',
    data,
  })
}

// 解封：参数是**设备 ID**（不是规则 id）—— 语义是「这台设备可以再被使用」。
//
// 必须 encodeURIComponent：deviceId 形如 `exe:a1b2c3`，含 `:`。
export function unbanAdminDevice(deviceId) {
  const value = String(deviceId == null ? '' : deviceId).trim()
  if (!value) throw new Error('解封参数错误')
  return request({
    errorFeedback: 'local',
    url: `/api/admin/device-bans/${encodeURIComponent(value)}`,
    method: 'delete',
  })
}
