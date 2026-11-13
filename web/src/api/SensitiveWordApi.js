import request from '../utils/request'

// 敏感词检测记录（123 号 T-27）。
//
// 后端契约（internal/controller/sensitive_word_cache.go，本 PR 新增）：
//   GET /adapter-service/admin/sensitive-word-cache          列表（三态筛选 + 分页）
//   GET /adapter-service/admin/sensitive-word-cache/status   三态计数 + 最近检测时间
//
// ⚠️ 这两条接口**只读**。缓存行的写入/重试完全由
// internal/service/sensitive_words.go 负责（hash 键、命中即返回、
// OnConflict upsert、失败上限 3 次都已实现），前端不需要也不应该触发重试。
//
// ⚠️ 路由需要在 internal/api/router.go 里挂上（见汇报）：
//   apiRouter.GET("/admin/sensitive-word-cache", controller.Auth, middleware.RequireAdmin, controller.SensitiveWordCacheList)
//   apiRouter.GET("/admin/sensitive-word-cache/status", controller.Auth, middleware.RequireAdmin, controller.SensitiveWordCacheStatus)

/** 三态枚举，与后端 service.SensitiveWordCacheState* 一一对应。 */
export const SENSITIVE_WORD_CACHE_STATES = Object.freeze(['hit', 'clean', 'failed'])

/** 状态 → 中文标签。 */
export const SENSITIVE_WORD_CACHE_STATE_LABELS = Object.freeze({
  hit: '命中',
  clean: '未命中',
  failed: '失败',
})

export function getSensitiveWordCacheEntries(params, feedback) {
  const config = {
    url: '/adapter-service/admin/sensitive-word-cache',
    method: 'get',
    params,
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}

export function getSensitiveWordCacheStatus(feedback) {
  const config = {
    url: '/adapter-service/admin/sensitive-word-cache/status',
    method: 'get',
  }
  if (feedback?.errorFeedback === 'local') config.errorFeedback = 'local'
  return request(config)
}
