import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { USER_USAGE_TAB_ORDER, USER_USAGE_DEFAULT_TAB } from './userLevelHelpers.mjs'

// 这个测试**故意不 import @vue/compiler-sfc**。
//
// 本仓 `web/node_modules` 是空壳：`loginAndRechargePageSource.test.mjs` 因为
// `import { parse, compileScript } from '@vue/compiler-sfc'` 在本地直接
// ERR_MODULE_NOT_FOUND（属于既有环境限制，不在本任务修复范围内）。
// 但「Tab 顺序」与「充值面板内容原样保留」这两条**必须**能被断言，
// 所以这里用纯文本抽取做结构断言 —— 它只依赖 node 内置模块，永远跑得动。

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const pagePath = path.join(__dirname, '..', 'pages', 'UserUsage', 'index.vue')

const readPage = () => readFile(pagePath, 'utf8')

/** 抽出 `<a-tab-pane ...>...</a-tab-pane>` 的所有 pane，按出现顺序返回。 */
function extractTabPanes(source) {
  const panes = []
  const open = /<a-tab-pane\b([^>]*)>/g
  let match
  while ((match = open.exec(source)) !== null) {
    const attrs = match[1]
    const keyMatch = attrs.match(/\bkey="([^"]+)"/)
    const tabMatch = attrs.match(/\btab="([^"]*)"/)
    // 自闭合或成对：本页都是成对写法，取到下一个 </a-tab-pane> 为止。
    const closeIndex = source.indexOf('</a-tab-pane>', open.lastIndex)
    if (closeIndex < 0) continue
    panes.push({
      key: keyMatch?.[1] ?? '',
      tab: tabMatch?.[1] ?? '',
      body: source.slice(open.lastIndex, closeIndex),
      start: match.index,
      end: closeIndex,
    })
  }
  return panes
}

test('UserUsage page renders exactly two tabs with recharge above level', async () => {
  const source = await readPage()
  const panes = extractTabPanes(source)

  // 判据 ①：两个 Tab，且顺序是「充值在上」。
  // 变异方向：把两个 a-tab-pane 对调 → 这里的 key 顺序断言必红。
  assert.deepEqual(panes.map(pane => pane.key), ['recharge', 'level'])
  assert.deepEqual(panes.map(pane => pane.tab), ['充值', '等级提升'])
  assert.deepEqual(panes.map(pane => pane.key), [...USER_USAGE_TAB_ORDER])
  assert.equal(panes[0].key, USER_USAGE_DEFAULT_TAB)
})

test('UserUsage uses the existing Tabs component instead of a third-party tab', async () => {
  const source = await readPage()

  // Tabs 已在 main.js 注册（app.use(Tabs)），所以这里只需要用 a-tabs 标签。
  assert.match(source, /<a-tabs\b/)
  assert.match(source, /<\/a-tabs>/)
  assert.match(source, /v-model:activeKey="activeUsageTab"/)
  assert.match(source, /import \{[^}]*buildUserUsageTabs[^}]*\} from '@\/utils\/userLevelHelpers\.mjs'/)
  assert.match(source, /const usageTabs = buildUserUsageTabs\(\)/)
  assert.match(source, /const activeUsageTab = ref\(USER_USAGE_DEFAULT_TAB\)/)
})

test('recharge flow is untouched by the tab refactor (regression gate)', async () => {
  const source = await readPage()
  const panes = extractTabPanes(source)
  const rechargeBody = panes.find(pane => pane.key === 'recharge')?.body ?? ''
  assert.ok(rechargeBody.length > 0, '充值 pane 必须存在')

  // 这一组是重构前 loginAndRechargePageSource.test.mjs 对充值面板的关键断言，
  // 逐条搬进来。它们全部落在**充值 pane 内部** —— 如果重构把充值内容
  // 挪到 pane 外或删掉，这里会红。
  assert.match(rechargeBody, /ConsolePanel title="账户余额与充值"/)
  assert.match(rechargeBody, /v-for="amount in rechargeQuickAmounts"/)
  assert.match(rechargeBody, /class="recharge-quick-amounts"/)
  assert.match(rechargeBody, /class="payment-method-grid"/)
  assert.match(rechargeBody, /@click="applyRechargeQuickAmount\(amount\)"/)
  assert.match(rechargeBody, /@click="handleCreateRechargeOrder"/)
  assert.match(rechargeBody, /:loading="rechargeSubmitting"/)
  assert.match(rechargeBody, /@click="selectPaymentMethod\(method.id\)"/)
  assert.match(rechargeBody, /@click="handleLoadPaymentMethods"/)
  assert.match(rechargeBody, /暂无可用支付方式/)
  // 账单流水与充值同属"充值"这一侧（重构前就在同一页同一层级）。
  assert.match(rechargeBody, /ConsolePanel title="账单流水"/)
  assert.match(rechargeBody, /@change="handleTableChange"/)
  assert.match(rechargeBody, /handleShowFeeDetail\(record\)/)

  // 快捷金额数组定义在 <script> 里，不在模板 —— 单独按全文断言。
  assert.match(source, /const rechargeQuickAmounts = \[10, 20, 50, 100, 200, 500\]/)

  // 这些充值方法必须**一个都没少**，且仍是可调用的方法定义。
  for (const method of ['handleRefreshAll', 'handleLoadBillingRecords', 'handleLoadPaymentMethods', 'handleCreateRechargeOrder', 'applyRechargeQuickAmount', 'selectPaymentMethod', 'handleTableChange']) {
    assert.match(source, new RegExp(`\\n    (?:async )?${method}\\(`), `充值方法 ${method} 必须仍存在`)
  }
  // 充值金额输入的两个 parser/formatter 仍然绑定在 a-input-number 上。
  assert.match(source, /:parser="parseRechargeAmountInput"/)
  assert.match(source, /:formatter="formatRechargeAmountInput"/)
  assert.match(source, /@keydown="handleRechargeAmountKeydown"/)
  // 支付结果弹窗与二维码渲染逻辑未动。
  assert.match(source, /v-model:open="paymentResultVisible"/)
  assert.match(source, /openPaymentJumpFallback/)
  assert.match(source, /QRCode\.toDataURL/)
})

test('recharge refresh keeps its original three-call sequence', async () => {
  const source = await readPage()
  // 既有用例 loginAndRechargePageSource.test.mjs:244-257 用 deepEqual 钉死了
  // 「handleRefreshAll 恰好调用这三个方法、且顺序不变」。
  // 那个文件在本地因缺 @vue/compiler-sfc 跑不起来，所以这里用文本级
  // 断言兜住同一件事：一旦往这个链里塞了第四个调用（例如等级读取），必红。
  const body = source.match(/handleRefreshAll\(\) \{([\s\S]*?)\n {4}\},/)
  assert.ok(body, 'handleRefreshAll 必须存在')
  const calls = [...body[1].matchAll(/this\.(handle[A-Za-z]+)\(/g)].map(match => match[1])
  assert.deepEqual(calls, ['handleRefreshCurrentUser', 'handleLoadBillingRecords', 'handleLoadPaymentMethods'])
})

test('level tab exposes current level, both calibers and the next-tier gap', async () => {
  const source = await readPage()
  const panes = extractTabPanes(source)
  const levelBody = panes.find(pane => pane.key === 'level')?.body ?? ''
  assert.ok(levelBody.length > 0, '等级提升 pane 必须存在')

  // 判据 ②：当前等级 + 两个口径值 + 下一档门槛与差额。
  assert.match(levelBody, /data-user-usage-current-level/)
  assert.match(levelBody, /data-user-usage-recharge-total/)
  assert.match(levelBody, /data-user-usage-consumption-total/)
  assert.match(levelBody, /data-user-usage-next-level/)
  assert.match(levelBody, /data-user-usage-next-recharge/)
  assert.match(levelBody, /data-user-usage-next-consumption/)
  assert.match(levelBody, /data-user-usage-recharge-gap/)
  assert.match(levelBody, /data-user-usage-consumption-gap/)
  assert.match(levelBody, /nextLevelRequirement\.available/)
  assert.match(levelBody, /nextLevelRequirement\.reason/)

  // 数据源必须是已存在的两个接口，不新造。
  assert.match(source, /import \{ getMyUserLevel, getUserLevels \} from '@\/api\/UserLevelApi'/)
})

test('level loading is lazy and never joins the recharge refresh chain', async () => {
  const source = await readPage()
  // 等级读取挂在「切到等级 Tab」上，而不是首屏或刷新按钮：
  // 这样充值链路的可观察行为与重构前逐项一致。
  assert.match(source, /watch\(activeUsageTab, \(tab\) => \{\n\s+if \(tab === 'level'\) void loadUserLevel\(\)/)
  // handleRefreshAll 里不得出现等级读取。
  const body = source.match(/handleRefreshAll\(\) \{([\s\S]*?)\n {4}\},/)
  assert.doesNotMatch(body[1], /loadUserLevel|handleLoadUserLevel/)
})
