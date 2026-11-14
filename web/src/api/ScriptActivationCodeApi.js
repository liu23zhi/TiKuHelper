import request from '../utils/request'

// 管理员预生成一次性脚本激活码的管理接口（仅管理员）。
//
// 与 UserScriptApi 的区分：那边是**用户侧**（自购、兑换自己的码）；
// 这里是管理员侧（预生成、列表、改期/禁用、看使用记录）。

// createScriptActivationCodes 批量生成。后端硬上限见
// service.MaxAdminScriptActivationCodeBatch（当前 100），前端只做提示，
// 真正的边界由后端校验（不能靠前端挡）。
export function createScriptActivationCodes(payload) {
  return request({
    url: '/api/admin/script-activation-codes',
    method: 'post',
    data: {
      count: payload?.count,
      remark: payload?.remark || '',
      expiresAt: payload?.expiresAt || null,
    },
    timeout: 30_000,
  })
}

// getScriptActivationCodes 列表。列表返回的是**掩码后**的码（后 4 位），
// 全码要走详情接口。
export function getScriptActivationCodes(params = {}) {
  return request({
    url: '/api/admin/script-activation-codes',
    method: 'get',
    params: {
      page: params.page || 1,
      pageSize: params.pageSize || 20,
      status: params.status || '',
      usedBy: params.usedBy || 0,
    },
    timeout: 15_000,
  })
}

// getScriptActivationCodeDetail 详情：全码 + 使用记录。
export function getScriptActivationCodeDetail(id) {
  return request({
    url: `/api/admin/script-activation-codes/${id}`,
    method: 'get',
    timeout: 15_000,
  })
}

// updateScriptActivationCode 修改有效期/备注/启用状态。
// 只提交被改动的字段（后端是指针语义：未提交 = 保持不变）。
export function updateScriptActivationCode(id, payload) {
  const data = {}
  if (payload?.remark !== undefined) data.remark = payload.remark
  if (payload?.status !== undefined) data.status = payload.status
  if (payload?.expiresAt !== undefined) data.expiresAt = payload.expiresAt
  if (payload?.clearExpiry !== undefined) data.clearExpiry = payload.clearExpiry
  return request({
    url: `/api/admin/script-activation-codes/${id}`,
    method: 'put',
    data,
    timeout: 15_000,
  })
}
