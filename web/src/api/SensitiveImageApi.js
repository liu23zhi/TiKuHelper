import request from '../utils/request'

// 敏感图片检测管理页（107 Q1 / 109 §2.5）。
//
// 后端契约（internal/controller/sensitive_image.go）：
//   GET  /adapter-service/sensitive-image/entries   检测记录：page / pageSize / status / imageSha256
//   GET  /adapter-service/sensitive-image/quota     额度配置；带 userId 时附当日用量
//   POST /adapter-service/sensitive-image/quota     改额度配置（指针语义：只改传入的键）
//   POST /adapter-service/sensitive-image/detect    手动触发：imageSha256 或 ticketId
//
// ⚠️ router.go 目前**没有**挂这四条路由（见汇报里的待接线清单），
// 所以页面在接线前会得到 404 —— 这是预期状态，不是本文件的问题。
//
// ⚠️ 「50 张/天」的两个概念不要混：
//   - 检测额度（本页管的）：entity.SensitiveImageDailyQuota.UsedCount，避免反复外呼计费；
//   - 工单图片张数/占用：perTicketLimit 限制的是「一个工单能外呼多少张」。
// 本页只读/写前者，后者仅作为配置项展示与修改。
//
// 全部 errorFeedback: 'local'：页面自己有失败态与重试入口，
// 不能在被页面接管的请求上再弹一次全局错误提示（与 BlacklistApi / IpSegmentApi 同约定）。

const ENTRIES_URL = '/adapter-service/sensitive-image/entries'
const QUOTA_URL = '/adapter-service/sensitive-image/quota'
const DETECT_URL = '/adapter-service/sensitive-image/detect'

// readPositiveId 校验工单编号：非正整数在发出请求前就拒绝，
// 避免把 /undefined、/NaN 之类打到后端。
function readPositiveId(id) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('工单编号参数错误')
  return value
}

// getSensitiveImageEntries 拉检测记录列表。
//
// 参数由页面经 buildEntryQuery 生成（两个 Tab 各自独立分页）。
// 刻意不在本层补默认 status：Tab 语义属于页面，放在传输层会让
// 「全部检测记录」这一 Tab 悄悄变成「某状态的记录」。
export function getSensitiveImageEntries(params) {
  return request({
    errorFeedback: 'local',
    url: ENTRIES_URL,
    method: 'get',
    params,
  })
}

// getSensitiveImageQuota 读额度配置；传 userId 时额外带回当日已用与剩余。
//
// 两个读数来自**两张不同的表**（system_settings + sensitive_image_daily_quotas），
// 所以列表不能反推额度，必须走这条独立接口。
export function getSensitiveImageQuota(params) {
  return request({
    errorFeedback: 'local',
    url: QUOTA_URL,
    method: 'get',
    params,
  })
}

// updateSensitiveImageQuota 改额度配置。
//
// 三个键**全部是可选的**：后端用 *bool / *int，收到 nil 表示「未提交 = 保持现值」。
// 因此这里只透传调用方实际给出的键，不补 false / 0 —— 补 0 会被后端当成
// 「把上限改成 0」并返回 400（ErrSensitiveImageQuotaInvalidLimit）。
export function updateSensitiveImageQuota(payload) {
  const body = {}
  if (payload && payload.enabled !== undefined) body.enabled = payload.enabled === true
  if (payload && payload.perTicketLimit !== undefined) body.perTicketLimit = Number(payload.perTicketLimit)
  if (payload && payload.dailyUserLimit !== undefined) body.dailyUserLimit = Number(payload.dailyUserLimit)
  if (!Object.keys(body).length) throw new Error('没有需要保存的额度配置')
  return request({
    errorFeedback: 'local',
    url: QUOTA_URL,
    method: 'post',
    data: body,
  })
}

// manualDetectSensitiveImage 手动触发一次检测（管理员复核通道）。
//
// 两条寻址方式：
//   - imageSha256：按图片内容重判，结论跟着 SHA256 走 —— 同一张图被 N 张工单
//     引用时会同步 N 行附件，响应里的 affectedAttachments 就是那个 N。
//   - ticketId：按工单批量重检该工单的全部图片（按 SHA256 去重，同图只外呼一次）。
//
// 两者都给时后端**只处理 imageSha256**，静默忽略 ticketId，
// 所以本层直接拒绝，逼调用方明确选一条路径（页面用 buildDetectPayload 分流）。
//
// 不检查额度：手动触发是运营动作，不该被某个用户当天的限额挡住 ——
// 额度用满恰恰是最需要人工看的情形。这条语义在后端，前端不重复实现闸门。
export function manualDetectSensitiveImage(payload) {
  const sha = String(payload?.imageSha256 ?? '').trim()
  const ticketId = payload?.ticketId === undefined || payload?.ticketId === null || payload?.ticketId === ''
    ? 0
    : Number(payload.ticketId)
  if (sha && ticketId > 0) {
    throw new Error('图片标识与工单编号只能选一个')
  }
  if (sha) {
    if (!/^[0-9a-f]{64}$/i.test(sha)) throw new Error('图片标识必须是 64 位十六进制')
    return request({
      errorFeedback: 'local',
      url: DETECT_URL,
      method: 'post',
      // 手动重判会真实外呼计费接口，超时给足 60s 的默认值即可，
      // 但必须显式声明 timeout：否则将来默认值被调小会让「重判大图」变成必失败。
      timeout: 60 * 1000,
      data: { imageSha256: sha.toLowerCase(), ticketId: 0 },
    })
  }
  if (!Number.isInteger(ticketId) || ticketId <= 0) {
    throw new Error('需要提供图片标识或有效的工单编号')
  }
  return request({
    errorFeedback: 'local',
    url: DETECT_URL,
    method: 'post',
    timeout: 60 * 1000,
    data: { imageSha256: '', ticketId: readPositiveId(ticketId) },
  })
}
