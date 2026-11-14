/**
 * 账单筛选的**唯一**共享定义与工具（138 §3.2.3 / B1-4 / B1-5）。
 *
 * 为什么必须有这个文件（这是本模块存在的全部理由）：
 *   `BillingRecordList`（账单明细）与 `UserUsage`（个人使用 → 账单流水）展示的是
 *   **同一份** `billing_records`。如果两页各写一套类型取值，就会出现
 *   「同一笔账单在 A 页能筛出来、在 B 页筛不出来」—— 用户看到的是账单对不上，
 *   排查成本极高且没有任何报错。所以类型值、参数组装、已启用条件数一律只在这里定义，
 *   两页 import 同一份。
 *
 * 类型值来源（**不要自造**）：`BillingRecordList/index.vue` 原有的 typeOptions 列表，
 * 与后端 `internal/service/billing_record.go` 里真实写入的 type 逐项对应：
 *   normal_search            题目消费
 *   recharge                 充值入账
 *   registration_bonus       新用户赠送
 *   balance_adjustment       管理员调整余额
 *   referral_rebate          邀请返佣
 *   referral_rebate_reversal 邀请返佣冲回
 *   review_rebate            评价返现
 *   script_reservation       脚本预扣
 *   script_refund            脚本退款
 * 新增后端 type 时必须同时改这里，否则新类型在两页都筛不到（可见的漏筛，不是静默错误）。
 *
 * 方向（收入 / 支出）**不是**独立筛选维度：它由 type 隐含决定，且后端
 * `BillingRecordListQuery` 没有 direction 字段（138 §3.2.3 末段）。
 */
import { buildSearchParams, countActiveFilters, normalizeSearchKeyword } from './listSearchHelpers.mjs'
import { buildPersonalUsageBillingRecordParams } from './userPortalHelpers.mjs'

/** 9 个真实账单类型取值（顺序即下拉框顺序，与重构前一致）。 */
export const BILLING_RECORD_TYPE_VALUES = Object.freeze([
  'normal_search',
  'recharge',
  'registration_bonus',
  'balance_adjustment',
  'referral_rebate',
  'referral_rebate_reversal',
  'review_rebate',
  'script_reservation',
  'script_refund',
])

/**
 * 类型下拉框选项：**两页共用同一份**（B3 判据）。
 *
 * 冻结是刻意的：调用方拿到的是共享常量，任何页面若就地 push/splice 会立刻抛错，
 * 而不是悄悄让两页的选项集分叉。
 */
export const BILLING_TYPE_OPTIONS = Object.freeze([
  Object.freeze({ label: '题目消费', value: 'normal_search' }),
  Object.freeze({ label: '充值入账', value: 'recharge' }),
  Object.freeze({ label: '新用户赠送', value: 'registration_bonus' }),
  Object.freeze({ label: '管理员调整余额', value: 'balance_adjustment' }),
  // 邀请返佣（task_doc/70 需求 2）：结算入赠送余额、原订单退款时冲回。
  Object.freeze({ label: '邀请返佣', value: 'referral_rebate' }),
  Object.freeze({ label: '邀请返佣冲回', value: 'referral_rebate_reversal' }),
  Object.freeze({ label: '评价返现', value: 'review_rebate' }),
  Object.freeze({ label: '脚本预扣', value: 'script_reservation' }),
  Object.freeze({ label: '脚本退款', value: 'script_refund' }),
])

/** 搜索模式选项（admin 侧筛选维度，同样只此一份）。 */
export const BILLING_SEARCH_MODE_OPTIONS = Object.freeze([
  Object.freeze({ label: '普通搜索', value: 'normal' }),
  Object.freeze({ label: '深度思考', value: 'deep' }),
  Object.freeze({ label: '深度思考 原生工具', value: 'deep_native' }),
  Object.freeze({ label: '深度思考 MCP编排', value: 'deep_mcp' }),
])

/** 筛选字段全集。新增维度必须同时加进这里，否则会被 buildBillingFilters 丢掉。 */
export const BILLING_FILTER_FIELDS = Object.freeze([
  'keyword',
  'type',
  'userId',
  'requestId',
  'provider',
  'searchMode',
  'clientIp',
  'createdFrom',
  'createdTo',
])

/**
 * 新建一份空筛选条件。
 *
 * 返回**可变对象**（页面用 reactive 包住后直接 v-model 绑定），
 * 与上面的 frozen 常量区分：常量是选项集，这份是用户在界面上的当前取值。
 */
export function createBillingFilters() {
  return {
    keyword: '',
    type: undefined,
    userId: undefined,
    requestId: '',
    provider: '',
    searchMode: undefined,
    clientIp: '',
    createdFrom: '',
    createdTo: '',
  }
}

/** 把任意来源对象归一化成 9 个已知字段（未知字段丢弃，缺失字段补空值）。 */
export function buildBillingFilters(source) {
  const filters = createBillingFilters()
  const raw = source && typeof source === 'object' ? source : {}
  for (const field of BILLING_FILTER_FIELDS) {
    if (Object.prototype.hasOwnProperty.call(raw, field)) {
      filters[field] = raw[field]
    }
  }
  return filters
}

/** 关键词归一化：去首尾空白并截断，与后端 SearchKeywordMaxLength 一致。 */
export function normalizeBillingKeyword(value) {
  return normalizeSearchKeyword(value)
}

/**
 * 组装请求参数：空值一律剔除，页码由显式参数决定（不受 filters 里同名键影响）。
 *
 * 这是两页共用的**唯一**入口 —— 谁都不许自己拼一遍，否则「空串要不要发」
 * 「页码从几开始」这类细节会分叉。
 */
export function buildBillingFilterParams(filters, page = 1, pageSize = 10) {
  return buildSearchParams(buildBillingFilters(filters), page, pageSize)
}

/** 已生效的筛选条件数（空串 / null / undefined 不算）。 */
export function countBillingActiveFilters(filters) {
  return countActiveFilters(buildBillingFilters(filters))
}

/** 是否存在任何生效筛选条件。 */
export function hasBillingActiveFilters(filters) {
  return countBillingActiveFilters(filters) > 0
}

/**
 * 就地清空筛选条件（保留对象身份，v-model 绑定不会失效）。
 * 返回同一个对象便于链式使用。
 */
export function resetBillingFilters(target) {
  const empty = createBillingFilters()
  const filters = target && typeof target === 'object' ? target : empty
  for (const field of BILLING_FILTER_FIELDS) {
    filters[field] = empty[field]
  }
  return filters
}

/**
 * 「个人使用 → 账单流水」的请求参数。
 *
 * ⚠️ `scope: 'self'` 必须**始终**存在且不可被筛选条件覆盖：
 * 后端 `controller/billing_record.go:34-36` 读到 `scope=self` 后强制把角色降成 user，
 * 从而只返回本人记录。少这个字段，普通用户会拿到全站账单（B5 判据）。
 *
 * 这里复用 `userPortalHelpers.buildPersonalUsageBillingRecordParams` 来产生
 * page/pageSize/scope 三项 —— 那是 `scope: 'self'` 语义的既有唯一定义处，
 * 本函数只负责在其上叠加共享筛选条件，不重新实现一遍分页归一化。
 * 叠加顺序是「先分页+scope，再铺筛选」：所以即便调用方传入的 filters 里
 * 混进 `page`/`pageSize`/`scope`（例如误把整个 query 对象当 filters），
 * 这三项也会被前面的权威值重新覆盖 —— `scope` 不可能被外部值改掉。
 */
export function buildPersonalBillingParams(filters, page = 1, pageSize = 10) {
  const base = buildPersonalUsageBillingRecordParams(page, pageSize)
  return {
    ...buildBillingFilterParams(filters, base.page, base.pageSize),
    ...base,
  }
}

/** 把账单类型 value 翻成文案；未知值原样返回，不编造中文名。 */
export function buildBillingTypeLabel(value) {
  const normalized = String(value ?? '').trim()
  if (!normalized) return ''
  const option = BILLING_TYPE_OPTIONS.find((item) => item.value === normalized)
  return option ? option.label : normalized
}
