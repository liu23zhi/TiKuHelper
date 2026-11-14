import request from '../utils/request'

// 工单图片缓存管理（T-12，用户 2026-11-13 决策「新增工单图片缓存管理」）。
//
// 后端契约（internal/controller/ticket_image_cache.go，路由待装配进
// internal/api/router.go）：
//   GET    /api/admin/ticket-image-cache
//             列表（分页 + mediaType/scanState/refFilter/keyword 筛选 + 四项汇总）
//   DELETE /api/admin/ticket-image-cache/:sha256
//             删除单条（ref_count > 0 时 409 + 被几个工单图片引用）
//   POST   /api/admin/ticket-image-cache/batch-delete
//             批量删除（{"sha256List":[...]}，逐条遵守 ref_count 保护，最多 200 条）
//   POST   /api/admin/ticket-image-cache/clean
//             立即清理一次（返回 agePurged / sizePurged 两个数字，不合并）
//
// 与 /admin/ip-segments 的区别：那条维护「IP 段归属」，这条维护工单图片的
// 二进制缓存（按 sha256 去重的 blob 表）。两者都是管理面，但数据完全不同。
//
// 全部 errorFeedback: 'local'：页面自己有持久失败态与重试入口，
// 不能在被页面接管的请求上再弹一次全局错误提示。

// 批量删除的后端上限（与 service.TicketAttachmentBlobPurgeBatchSize 一致）。
// 前端先挡一次，免得整批 400 之后管理员还得自己找哪条超了。
export const TICKET_IMAGE_CACHE_BATCH_LIMIT = 200

// readSHA256 校验路径参数：非 64 位小写十六进制一律在发出请求前就拒绝，
// 避免把 /undefined、/NaN 这类路径打到后端。
function readSHA256(value) {
  const text = String(value ?? '').trim().toLowerCase()
  if (!/^[0-9a-f]{64}$/.test(text)) throw new Error('工单图片标识不合法')
  return text
}

// getTicketImageCacheList 分页读取缓存列表。
//
// params 由 buildListParams（utils/ticketImageCacheHelpers.mjs）整理，
// 空筛选项不会出现在参数里。
export function getTicketImageCacheList(params) {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/ticket-image-cache',
    method: 'get',
    params,
  })
}

// deleteTicketImageCache 删除单条。
//
// ref_count > 0 时后端返回 409（不是 403/400）：这是「当前状态不允许」，
// 页面据此展示「仍被 N 个工单图片引用」，而不是笼统的「操作失败」。
export function deleteTicketImageCache(sha256) {
  return request({
    errorFeedback: 'local',
    url: `/api/admin/ticket-image-cache/${readSHA256(sha256)}`,
    method: 'delete',
  })
}

// batchDeleteTicketImageCache 批量删除。
//
// 逐条遵守 ref_count 保护：部分成功是正常结果（被引用的被跳过并列出）。
// 空列表不发请求 —— 后端也会 400，但那是一次白跑的往返。
export function batchDeleteTicketImageCache(sha256List) {
  const list = Array.isArray(sha256List) ? sha256List.map(readSHA256) : []
  if (list.length === 0) throw new Error('请先选择要删除的缓存')
  if (list.length > TICKET_IMAGE_CACHE_BATCH_LIMIT) {
    throw new Error(`单次批量删除最多 ${TICKET_IMAGE_CACHE_BATCH_LIMIT} 条`)
  }
  return request({
    errorFeedback: 'local',
    url: '/api/admin/ticket-image-cache/batch-delete',
    method: 'post',
    data: { sha256List: list },
  })
}

// cleanTicketImageCacheNow 立即按当前保留策略清理一次。
//
// 不传参数：清理规则一律取库里已保存的配置，避免「按钮传了一套参数、
// 保存的是另一套」导致管理员以为改配置了其实只影响这一次清理。
//
// 返回值必须同时看 agePurged 与 sizePurged（页面用 describeCleanResult 展示）：
// 合成一个 total 就无法判断是哪条策略生效。
export function cleanTicketImageCacheNow() {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/ticket-image-cache/clean',
    method: 'post',
  })
}
