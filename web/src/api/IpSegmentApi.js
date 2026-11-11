import request from '../utils/request'

// IP 段归属信息管理（task_doc/71 §3.5）。
//
// 后端契约（internal/api/router.go）：
//   GET    /adapter-service/admin/ip-segments                 段列表（分页 + keyword/asn/llc/source 筛选）
//   GET    /adapter-service/admin/ip-segments/lookup?ip=...   某个 IP 落在哪一段（未命中是 200 + reason）
//   DELETE /adapter-service/admin/ip-segments/:id             删除一条段记录
//   POST   /adapter-service/admin/ip-segments/batch-delete    按筛选批量删除（必须带至少一个条件）
//   POST   /adapter-service/admin/ip-segments/:id/refresh     用该段的样本 IP 重查上游并更新
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
    url: '/adapter-service/admin/ip-segments',
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
    url: '/adapter-service/admin/ip-segments/lookup',
    method: 'get',
    params: { ip: value },
  })
}

export function deleteIPSegment(id) {
  return request({
    errorFeedback: 'local',
    url: `/adapter-service/admin/ip-segments/${readPositiveId(id)}`,
    method: 'delete',
  })
}

// 批量删除只接受筛选条件：不带条件的「全删」后端会 400，
// 所以这里也不给不传参数的空间。
export function batchDeleteIPSegments(filter) {
  return request({
    errorFeedback: 'local',
    url: '/adapter-service/admin/ip-segments/batch-delete',
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
    url: `/adapter-service/admin/ip-segments/${readPositiveId(id)}/refresh`,
    method: 'post',
  })
}
