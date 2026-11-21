import request from '../utils/request'

// IP 段归属信息管理（task_doc/71 §3.5）。
//
// 后端契约（internal/api/router.go）：
//   GET    /api/admin/ip-segments                 段列表（分页 + keyword/asn/llc/source 筛选）
//   GET    /api/admin/ip-segments/lookup?ip=...   某个 IP 落在哪一段（未命中是 200 + reason）
//   DELETE /api/admin/ip-segments/:id             删除一条段记录
//   POST   /api/admin/ip-segments/batch-delete    按筛选批量删除（必须带至少一个条件）
//   POST   /api/admin/ip-segments/:id/refresh     用该段的样本 IP 重查上游并更新
//
// 与 ip-location-cache 的区别：那里是「查过这个键」的单 IP 缓存，
// 这里是「这个网段属于谁」的段表。路径用复数 ip-segments，两者不要混用。
//
// 全部 errorFeedback: 'local'：页面自己有持久失败态与重试入口，
// 不能在被页面接管的请求上再弹一次全局错误提示。

// readPositiveId 校验路径参数：非正整数一律在发出请求前就拒绝，
// 避免把 /undefined、/NaN 这类路径打到后端。
function readPositiveId(id) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('IP 段参数错误')
  return value
}

export function getIPSegments(params) {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/ip-segments',
    method: 'get',
    params,
  })
}

// 按 IP 查段：ip 为空时不发请求，由调用方提示「请输入 IP」。
export function lookupIPSegment(ip) {
  const value = String(ip ?? '').trim()
  if (!value) throw new Error('请输入 IP')
  return request({
    errorFeedback: 'local',
    url: '/api/admin/ip-segments/lookup',
    method: 'get',
    params: { ip: value },
  })
}

export function deleteIPSegment(id) {
  return request({
    errorFeedback: 'local',
    url: `/api/admin/ip-segments/${readPositiveId(id)}`,
    method: 'delete',
  })
}

// 批量删除只接受筛选条件：不带条件的「全删」后端会 400，
// 所以这里也不给不传参数的空间。
export function batchDeleteIPSegments(filter) {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/ip-segments/batch-delete',
    method: 'post',
    data: {
      keyword: String(filter?.keyword ?? '').trim(),
      asn: String(filter?.asn ?? '').trim(),
      llc: String(filter?.llc ?? '').trim(),
      source: String(filter?.source ?? '').trim(),
    },
  })
}

// 刷新会真实外呼上游（计费 + 可能改变段边界），因此只在管理员显式点击时调用，
// 不做任何自动/批量触发。
export function refreshIPSegment(id) {
  return request({
    errorFeedback: 'local',
    url: `/api/admin/ip-segments/${readPositiveId(id)}/refresh`,
    method: 'post',
  })
}
// syncIPSegmentsFromCache 立即把「IP 归属地缓存」里还没沉淀的段写进段表。
//
// 语义与自动沉淀**完全一致**（后端复用同一个 service.PersistMissingIPSegmentsFromCacheRows），
// 只是把时机交给管理员：自动通路要等重启或等某个 IP 恰好被查一次，
// 刚上线段表时可能就是「页面空着、不知道要等多久」。
//
// 它不是「导入」：不读外部文件、不新增数据来源，只把缓存里已有的行补写成段。
export function syncIPSegmentsFromCache() {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/ip-segments/sync-from-cache',
    method: 'post',
  })
}