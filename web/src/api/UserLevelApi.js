import request from '../utils/request'

// 等级体系与充值阶梯档位（94-*.md §2 C1 / C2）。
//
// 后端契约（internal/api/router.go）：
//   GET    /api/admin/user-levels                等级列表（含停用级）
//   POST   /api/admin/user-levels                新增等级
//   PUT    /api/admin/user-levels/:id            编辑等级
//   DELETE /api/admin/user-levels/:id            删除等级（引用它的用户回落为无等级）
//   POST   /api/admin/user-levels/reevaluate     手动重评一批用户
//   GET    /api/admin/recharge-promotion-tiers   档位表 + 当前生效来源
//   PUT    /api/admin/recharge-promotion-tiers   整体替换档位表
//   POST   /api/admin/recharge-promotion-tiers/preview  按未保存档位试算
//   GET    /api/user/level                       当前登录用户自己的等级
//
// 全部 errorFeedback: 'local'：页面自己有持久失败态与重试入口，
// 不能在被页面接管的请求上再弹一次全局错误提示。

function readPositiveId(id) {
  const value = Number(id)
  if (!Number.isInteger(value) || value <= 0) throw new Error('等级参数错误')
  return value
}

export function getUserLevels() {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/user-levels',
    method: 'get',
  })
}

export function createUserLevel(payload) {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/user-levels',
    method: 'post',
    data: normalizeLevelPayload(payload),
  })
}

export function updateUserLevel(id, payload) {
  return request({
    errorFeedback: 'local',
    url: `/api/admin/user-levels/${readPositiveId(id)}`,
    method: 'put',
    data: normalizeLevelPayload(payload),
  })
}

export function deleteUserLevel(id) {
  return request({
    errorFeedback: 'local',
    url: `/api/admin/user-levels/${readPositiveId(id)}`,
    method: 'delete',
  })
}

export function reevaluateUserLevels(limit) {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/user-levels/reevaluate',
    method: 'post',
    data: limit ? { limit: Number(limit) } : {},
  })
}

// normalizeLevelPayload 逐字段显式挑出来发。
//
// ⚠️ 不要直接 `data: payload` 也不要用对象展开：后端 UserLevelInput 里
// `enabled` 是指针语义（undefined = 保持不变、false = 停用），
// 展开一个带着额外字段的对象会让「停用」与「没传」分不清。本仓踩过一次
// 「helper 算对了但传输层重新挑字段时把它丢了」的坑，这里显式列出。
function normalizeLevelPayload(payload) {
  const data = {
    name: String(payload?.name ?? '').trim(),
    rank: Number(payload?.rank ?? 0),
    minRechargeTotal: Number(payload?.minRechargeTotal ?? 0),
    minConsumptionTotal: Number(payload?.minConsumptionTotal ?? 0),
    deviceLimit: Number(payload?.deviceLimit ?? 0),
    description: String(payload?.description ?? '').trim(),
  }
  // enabled 只在调用方明确给了布尔值时才下发。
  if (typeof payload?.enabled === 'boolean') data.enabled = payload.enabled
  return data
}

export function getRechargePromotionTiers() {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/recharge-promotion-tiers',
    method: 'get',
  })
}

export function replaceRechargePromotionTiers(rows) {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/recharge-promotion-tiers',
    method: 'put',
    data: { rows: normalizeTierRows(rows) },
  })
}

export function previewRechargePromotionTier(paidAmount, rows) {
  return request({
    errorFeedback: 'local',
    url: '/api/admin/recharge-promotion-tiers/preview',
    method: 'post',
    data: { paidAmount: Number(paidAmount ?? 0), rows: normalizeTierRows(rows) },
  })
}

function normalizeTierRows(rows) {
  if (!Array.isArray(rows)) return []
  return rows.map(row => {
    const data = {
      threshold: Number(row?.threshold ?? 0),
      rate: Number(row?.rate ?? 0),
      sortOrder: Number(row?.sortOrder ?? 0),
    }
    if (typeof row?.enabled === 'boolean') data.enabled = row.enabled
    return data
  })
}

export function getMyUserLevel() {
  return request({
    errorFeedback: 'local',
    url: '/api/user/level',
    method: 'get',
  })
}
