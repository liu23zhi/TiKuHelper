/**
 * 「账单筛选两页共用同一份定义」+「billing-records 已删除」的源码级判据。
 *
 * 为什么这必须是源码级（不能只测 helper）：
 *   helper 自己写对了，页面**另外**写一份字面量照样能让两页筛出不同结果 ——
 *   而那种缺陷在 helper 的单测里完全不可见。所以这里直接读两个页面的源码，
 *   断言它们**没有**各自的类型取值列表，且都从 billingFilters.mjs 取值。
 *
 * 文件本身只依赖 node 内置模块：本仓 web/node_modules 在部分环境下是空壳，
 * 任何 import vue / @vue/compiler-sfc 的测试都会 ERR_MODULE_NOT_FOUND。
 */
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  BILLING_RECORD_TYPE_VALUES,
} from './billingFilters.mjs'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const readSrc = (relative) => readFile(path.join(__dirname, '..', relative), 'utf8')

const BILLING_PAGE = 'pages/BillingRecordList/index.vue'
const USAGE_PAGE = 'pages/UserUsage/index.vue'
const ROUTER = 'router/router.js'

/** 剥掉注释，避免「注释里解释了 9 个值」被误判成「代码里又写了一份」。 */
function stripComments(source) {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:])\/\/[^\n]*/g, '$1')
    .replace(/<!--[\s\S]*?-->/g, '')
}

/**
 * 抽出源码里所有 `{ label: '…', value: '…' }` 形式的选项字面量。
 * 注释已剥离 —— 两页的注释都提到过类型取值，不剥会把说明误判成实现。
 */
function optionPairs(source) {
  const code = stripComments(source)
  return [...code.matchAll(/\{\s*label:\s*'([^']*)'\s*,\s*value:\s*'([^']*)'\s*\}/g)]
    .map(match => ({ label: match[1], value: match[2] }))
}

/** 抽出源码里形如 `'normal_search'` 的类型字符串字面量（剥离注释后）。 */
function typeLiterals(source) {
  const code = stripComments(source)
  const found = new Set()
  for (const value of BILLING_RECORD_TYPE_VALUES) {
    if (new RegExp(`'${value}'`).test(code)) found.add(value)
  }
  return [...found]
}

// ─────────────────────── B3：两页同一份 ───────────────────────

test('B3：两个页面都不自己写类型选项列表，全部从 billingFilters.mjs 取', async () => {
  for (const relative of [BILLING_PAGE, USAGE_PAGE]) {
    const source = await readSrc(relative)
    // ① 页面里一个 `{ label, value }` 选项字面量都不许有 —— 那份列表只在 helper 里。
    const pairs = optionPairs(source)
    assert.deepEqual(pairs, [],
      `${relative} 里出现了选项字面量 ${JSON.stringify(pairs)}；`
      + '类型/搜索模式选项必须只在 utils/billingFilters.mjs 定义一次（两页各写一份会导致两页筛出不同结果）')

    // ② 页面里不许**集中**出现多个账单类型字面量（那是另一份列表的痕迹）。
    //    阈值取 3：单个类型名在业务代码里有正当用途，两个页面各有真实用例 ——
    //      · UserUsage:522  `buildLevelMetricText(summary, 'recharge')`（等级指标口径参数）
    //      · UserUsage:769  `item?.type !== 'balance_adjustment'`（汇总时排除管理员调整）
    //      · BillingRecordList:`buildBillingRecordTypeText` 等 helper 内部判断
    //    这些都不是「选项列表」；而一份完整的列表有 9 项，写成白名单至少要出现
    //    3 个不同取值才能表达「多类型可选」的意图。∈≤2 时逐条人工可核对，
    //    换回列表形态（≥3）必然变红 —— 这才是这条判据要挡的形态。
    const literals = typeLiterals(source)
    assert.ok(literals.length <= 2,
      `${relative} 里出现了 ${literals.length} 个账单类型字面量 ${JSON.stringify(literals)}；`
      + '超过 2 个说明这里又写了一份类型列表 —— 取值只能引用 billingFilters.mjs 的常量')

    // ③ 两个页面都必须真的 import 了共享 helper 的相应符号。
    assert.match(source, /from '@\/utils\/billingFilters\.mjs'/,
      `${relative} 必须从 @/utils/billingFilters.mjs 取值`)
  }
})

test('B3：BillingRecordList 的 typeOptions / searchModeOptions 直接指向共享常量', async () => {
  const source = await readSrc(BILLING_PAGE)
  // return 块里必须是 `typeOptions: BILLING_TYPE_OPTIONS` 这种引用，
  // 而不是 `typeOptions: [ … ]` 数组字面量。
  assert.match(source, /typeOptions:\s*BILLING_TYPE_OPTIONS\b/,
    '账单明细页的 typeOptions 必须直接引用共享常量')
  assert.match(source, /searchModeOptions:\s*BILLING_SEARCH_MODE_OPTIONS\b/,
    '账单明细页的 searchModeOptions 必须直接引用共享常量')
  assert.doesNotMatch(source, /typeOptions:\s*\[/,
    'typeOptions 不得再写成数组字面量（那是第二份定义）')
})

test('B3：UserUsage 的筛选复用同一份 helper，且参数组装走共享入口', async () => {
  const source = await readSrc(USAGE_PAGE)
  assert.match(source, /billingTypeOptions\s*=\s*BILLING_TYPE_OPTIONS/,
    '个人使用页的类型选项必须直接引用共享常量')
  // 取数参数必须走 buildPersonalBillingParams（内部叠加共享筛选 + scope=self），
  // 不能自己拼 query，否则「空串要不要发」这类细节会与账单明细页分叉。
  assert.match(source, /buildPersonalBillingParams\(this\.billingFilters/,
    '个人使用页必须用共享 helper 组装请求参数')
  assert.doesNotMatch(source, /buildPersonalUsageBillingRecordParams\(/,
    '不得绕过共享 helper 直接用旧的纯分页参数（那样筛选条件会被丢掉，两页筛出不同结果）')
})

test('B3：两页的筛选维度命名一致（关键词 / 类型 / 时间范围）', async () => {
  const usage = await readSrc(USAGE_PAGE)
  // 三项都在模板里，且绑定的是共享 filters 对象。
  assert.match(usage, /v-model:value="billingFilters\.type"/)
  assert.match(usage, /v-model:value="billingFilters\.createdFrom"/)
  assert.match(usage, /v-model:value="billingFilters\.createdTo"/)
  assert.match(usage, /v-model:value="billingKeywordInput"/)
  // 已启用条件数也来自共享 helper。
  assert.match(usage, /countBillingActiveFilters\(billingFilters\)/)
})

// ─────────────────────── B1 / B6：billing-records 已删除 ───────────────────────

test('B1：router.js 里不再有 billing-records 路由，且 billing-admin 仍登记为管理员专属', async () => {
  const router = await readSrc(ROUTER)
  // 路由定义（name / path 两处）都必须消失。
  assert.doesNotMatch(router, /name:\s*'billing-records'/, 'billing-records 路由必须已删除')
  assert.doesNotMatch(router, /path:\s*'billing-records'/, 'billing-records 路由路径必须已删除')
  // 但 ADMIN_ONLY_ROUTE_NAMES 里不许有它（它已不是路由），billing-admin 必须在。
  const block = /const ADMIN_ONLY_ROUTE_NAMES = new Set\(\[([\s\S]*?)\]\)/.exec(router)
  assert.ok(block, 'router.js 里必须有 ADMIN_ONLY_ROUTE_NAMES')
  assert.doesNotMatch(block[1], /'billing-records'/, 'ADMIN_ONLY_ROUTE_NAMES 里不该再有已删除的路由名')
  assert.match(block[1], /'billing-admin'/, 'billing-admin 必须仍在 ADMIN_ONLY_ROUTE_NAMES（B6 / B2 判据）')
})

test('B1：billing-admin 仍是唯一账单路由，且指向同一组件、viewMode=admin', async () => {
  const router = await readSrc(ROUTER)
  const adminRoute = /\{[^{}]*?path:\s*'billing-admin'[\s\S]{0,400}?\}/.exec(router)
  assert.ok(adminRoute, 'billing-admin 路由必须存在')
  assert.match(adminRoute[0], /name:\s*'billing-admin'/)
  assert.match(adminRoute[0], /component:\s*BillingRecordList/)
  assert.match(adminRoute[0], /meta:\s*\{\s*viewMode:\s*'admin'\s*\}/)
  // 全仓 router 里出现 BillingRecordList 的次数恰好一次（没有第二个入口）。
  const occurrences = [...router.matchAll(/component:\s*BillingRecordList/g)]
  assert.equal(occurrences.length, 1, 'BillingRecordList 只能被一条路由引用（用户侧入口已删）')
})

test('B1：删除路由后组件里的 self 分支保留并标明「当前无入口，保留备查」', async () => {
  // 红线②：不删 viewMode='self' 分支，只在注释里标明。这里同时钉住
  // 「分支还在」与「注释还在」——只钉前者会让下一个人以为可以随手删掉注释。
  const page = await readSrc(BILLING_PAGE)
  assert.match(page, /if \(routeViewMode\.value === 'self'\) return false/,
    'viewMode=self 分支必须保留（当前无入口，保留备查）')
  assert.match(page, /当前无入口，保留备查/,
    '必须保留「当前无入口，保留备查」的说明，否则下一个维护者会把它当死代码删掉')
  assert.match(page, /metadata\.viewMode|meta\.viewMode/, '组件仍按 route.meta.viewMode 判视图')
})

test('B1：billingDetailView.test.mjs 不再把 billing-records 当作在用的入口', async () => {
  // 原测试里有一行注释写着「用户端走 billing-records（viewMode='self'）」。
  // 那不是断言（只是说明），但它描述的契约已不成立 —— 留着会误导下一个维护者
  // 以为用户侧还有这个入口。所以本改动同步改掉，并在这里钉住。
  const source = await readFile(path.join(__dirname, 'billingDetailView.test.mjs'), 'utf8')
  const code = stripComments(source)
  // 可执行的断言里不许再出现 billing-records 路由名。
  assert.doesNotMatch(code, /'billing-records'/,
    'billingDetailView.test.mjs 的可执行代码里不得再引用已删除的路由名')
  // 说明性文字必须点出新口径（只有 admin 入口）。
  assert.match(source, /已删除|唯一账单入口|只有 admin 入口/,
    'billingDetailView.test.mjs 的视图契约说明必须写清新口径（billing-records 已删除）')
})
