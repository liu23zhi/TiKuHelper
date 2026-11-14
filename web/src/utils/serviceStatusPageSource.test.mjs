/* 服务器状态页的接线契约测试（路由权限 + 侧边栏可见性 + 页面不越权）。
 *
 * 为什么读源码而不 mount：本 worktree 的 web/node_modules 虽存在，但本仓所有
 * 页面级契约测试（sidebarSectionSource / sidebarOwnerRoute / userBanSource …）
 * 都是读源码派生的，保持一致。行为层由 serviceStatusSummary.test.mjs 覆盖，
 * 这里只钉「结构性事实」：路由是否登记、菜单是否进对组、页面是否越权请求。
 *
 * ⚠️ 每个断言都写**字面量**，不引用从源码里解析出来的变量做期望值，
 * 否则源码漂移时断言与被测对象同步变化 → 恒真 → 变异不会红。
 */
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const testDir = dirname(fileURLToPath(import.meta.url))
const routerPath = resolve(testDir, '../router/router.js')
const menuPath = resolve(testDir, '../components/SiderMenu/index.vue')
const pagePath = resolve(testDir, '../pages/ServiceStatus/index.vue')
const apiPath = resolve(testDir, '../api/ServiceStatusApi.js')

const routerSource = readFileSync(routerPath, 'utf8')
const menuSource = readFileSync(menuPath, 'utf8')
const pageSource = readFileSync(pagePath, 'utf8')
const apiSource = readFileSync(apiPath, 'utf8')

/** 统计出现次数（不用 split，避免把串吃掉）。 */
function countOccurrences(haystack, needle) {
  let count = 0
  for (let at = haystack.indexOf(needle); at !== -1; at = haystack.indexOf(needle, at + needle.length)) count += 1
  return count
}

/**
 * 剥掉注释，只留可执行/可渲染的代码。
 *
 * ⚠️ 这一步是必需的，不是洁癖：本页的注释里**故意**写了反例说明
 * （「不能写成 v-if="successRate === 0"，全失败也是 0」），
 * 直接对全文做 doesNotMatch 会把这段反例说明当成「又改回去了」而假红。
 *
 * 行注释只剥「整行以 // 开头」的，不剥行内的 `//` —— 否则会把
 * `'/api/service-status/summary'` 之类的代码截断（URL 里含斜杠）。
 */
function stripComments(source) {
  return source
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^[ \t]*\/\/[^\n]*$/gm, '')
}

const pageCode = stripComments(pageSource)
const apiCode = stripComments(apiSource)

/** 取出 ADMIN_ONLY_ROUTE_NAMES 那个 Set 的原文区段。 */
function adminOnlySetRegion() {
  const start = routerSource.indexOf('const ADMIN_ONLY_ROUTE_NAMES = new Set([')
  assert.ok(start >= 0, 'router.js 里找不到 ADMIN_ONLY_ROUTE_NAMES')
  const end = routerSource.indexOf('])', start)
  assert.ok(end > start, 'ADMIN_ONLY_ROUTE_NAMES 的 Set 没有闭合')
  return routerSource.slice(start, end)
}

/** 取出某个 `const NAME = [ ... ]` 数组字面量的原文。 */
function arrayLiteral(source, name) {
  const match = new RegExp(`const ${name} = \\[([\\s\\S]*?)\\n\\]`).exec(source)
  assert.ok(match, `找不到 const ${name} = [...]`)
  return match[1]
}

/** 把数组字面量求值成真实对象数组（icon 是组件引用，统一置 null）。 */
function evaluateItems(source, name) {
  const body = arrayLiteral(source, name).replace(/icon:\s*[A-Za-z_$][\w$]*/g, 'icon: null')
  // eslint-disable-next-line no-new-func
  return new Function(`return [${body}]`)()
}

const ADMIN_MENU_ITEMS = evaluateItems(menuSource, 'ADMIN_MENU_ITEMS')
const USER_MENU_ITEMS = evaluateItems(menuSource, 'USER_MENU_ITEMS')
const userKeys = USER_MENU_ITEMS.map((item) => item.key)
const adminKeys = ADMIN_MENU_ITEMS.map((item) => item.key)

test('判据①：service-status 路由存在，且**不在** ADMIN_ONLY_ROUTE_NAMES（普通用户可访问）', () => {
  // 路由必须真的登记，且有 component（否则是个死 name）。
  const routeMatch = /path:\s*'service-status'[\s\S]{0,400}?name:\s*'service-status'[\s\S]{0,200}?component:\s*ServiceStatus/.exec(routerSource)
  assert.ok(routeMatch, "router.js 里必须有 path='service-status' 且 component=ServiceStatus 的路由")

  // ★ 核心判据：普通用户可访问 = 不在管理员专属名单里。
  const adminOnly = adminOnlySetRegion()
  assert.ok(!adminOnly.includes("'service-status'"), 'service-status 不能登记进 ADMIN_ONLY_ROUTE_NAMES（那是普通用户可见页）')
  // 反向确认名单本身没被清空（防止有人把整个 Set 注释掉让本用例假绿）。
  assert.ok(adminOnly.includes("'userlist'"), '前置失效：ADMIN_ONLY_ROUTE_NAMES 应当仍含管理员专属路由')
  assert.ok(adminOnly.includes("'token-admin'"), '前置失效：ADMIN_ONLY_ROUTE_NAMES 应当仍含 token-admin')
  // 名单里项数必须足够多（不是被误清空）。
  const listed = adminOnly.match(/'[a-z-]+'/g) || []
  assert.ok(listed.length >= 20, `ADMIN_ONLY_ROUTE_NAMES 项数异常偏少（${listed.length}），名单可能被误删`)
})

test('判据②：菜单项出现在 USER_MENU_ITEMS（普通用户可见区）', () => {
  assert.ok(userKeys.includes('service-status'), 'service-status 必须进 USER_MENU_ITEMS，否则普通用户侧边栏看不到')

  // 管理员侧也要有同 key —— 否则管理员在「普通用户可见」分组里看不到它
  // （shared 分组 = ADMIN ∩ USER 派生，见 SiderMenu 的分区逻辑）。
  assert.ok(adminKeys.includes('service-status'), 'service-status 必须同时进 ADMIN_MENU_ITEMS，否则管理员的 shared 分组里没有它')

  // 两侧文案必须一致：shared 组取用户端文案，不同会让标题失真。
  const userItem = USER_MENU_ITEMS.find((item) => item.key === 'service-status')
  const adminItem = ADMIN_MENU_ITEMS.find((item) => item.key === 'service-status')
  assert.equal(userItem.label, '服务器状态')
  assert.equal(adminItem.label, '服务器状态')

  // key 不能重复出现（重复会让 selectedKeys 无法定位）。
  assert.equal(countOccurrences(arrayLiteral(menuSource, 'USER_MENU_ITEMS'), "key: 'service-status'"), 1)
  assert.equal(countOccurrences(arrayLiteral(menuSource, 'ADMIN_MENU_ITEMS'), "key: 'service-status'"), 1)
})

test('判据②补充：service-status 落在「普通用户可见」分组而不是「仅管理员可见」', () => {
  // shared = ADMIN ∩ USER（filter 派生）。
  const shared = adminKeys.filter((key) => userKeys.includes(key))
  const adminOnlyGroup = adminKeys.filter((key) => !userKeys.includes(key))
  assert.ok(shared.includes('service-status'), 'service-status 必须落在「普通用户可见」= ADMIN ∩ USER 分组')
  assert.ok(!adminOnlyGroup.includes('service-status'), 'service-status 不得落在「仅管理员可见」分组')
})

test('侧边栏图标真的被 import 并可从 setup 可达（否则渲染成空白）', () => {
  // 图标必须在 import 块里。
  assert.match(menuSource, /CloudServerOutlined,?\s*\n\} from '@ant-design\/icons-vue'/)
  // 菜单项引用它。
  assert.match(menuSource, /key: 'service-status', label: '服务器状态', icon: CloudServerOutlined/)
})

test('页面不请求管理员端点（普通用户会 403）', () => {
  // 页面与 API 封装都不得出现 admin 端点路径（注释里的说明已剥掉，只看代码）。
  assert.ok(!pageCode.includes('/api/admin/service-status'), '页面不得引用 admin 端点')
  assert.ok(!apiCode.includes('/api/admin/service-status'), 'API 封装不得提供 admin 端点')
  // 也不允许换一种拼法绕过：任何 admin 路径片段都不该出现在页面代码里。
  assert.ok(!pageCode.includes('admin/service-status'), '页面不得以任何形式拼 admin 路径')
  assert.ok(!apiCode.includes('admin/service-status'), 'API 封装不得以任何形式拼 admin 路径')

  // 允许的两个端点：summary 与 series，都走 AuthOrAccessToken。
  assert.match(apiSource, /url: '\/api\/service-status\/summary'/)
  assert.match(apiSource, /url: '\/api\/service-status\/series'/)
})

test('页面空数据分支按 dataState 判定，不用 successRate 数值反推', () => {
  // ★ 这条是防「全失败被显示成无数据」的结构防线：
  // 模板里出现 successRate === 0 / !successRate 之类数值判定即视为回归。
  assert.match(pageCode, /v-if="view\.dataState === 'empty'"/, '空数据提示必须由 dataState==="empty" 驱动')

  // 禁止用 successRate 数值直接判空/判全失败（注释里作为反例提到是允许的，已剥）。
  assert.doesNotMatch(pageCode, /successRate\s*===\s*0/, '不得用 successRate===0 判空数据（全失败也是 0）')
  assert.doesNotMatch(pageCode, /successRate\s*!==\s*0/, '不得用 successRate!==0 反推可用性')
  assert.doesNotMatch(pageCode, /!successRate\b/, '不得用 !successRate 判空数据')
})

test('页面渲染必备信息：状态、成功率、延迟、总数、失败数、数据起点、采样点/容量、窗口口径', () => {
  for (const metric of ['success-rate', 'avg-latency', 'total-requests', 'failed-requests']) {
    assert.ok(pageSource.includes(`data-metric="${metric}"`), `页面缺少 ${metric} 展示`)
  }
  assert.ok(pageSource.includes('data-status-label'), '页面缺少状态点文案')
  // 数据起点 / 采样点 / 容量 / 窗口口径。
  assert.match(pageSource, /已采样点数/)
  assert.match(pageSource, /数据起点/)
  assert.match(pageSource, /统计窗口/)
  assert.match(pageSource, /windowText/)
  assert.match(pageSource, /retainedText/)
  assert.match(pageSource, /capacityText/)
})

test('页面处理 401/403 分流（走 describeServiceStatusFailure，不在页面里硬编码）', () => {
  // 分流逻辑集中在纯函数里（可单测），页面只消费 message。
  assert.match(pageSource, /loadServiceStatusSummary/)
  assert.match(pageSource, /result\.message/)
  // 页面不应自己写 401/403 文案 —— 那是纯函数的职责，重复写会漂移。
  assert.doesNotMatch(pageSource, /=== 403|=== 401/, '页面不应重复实现状态码分流')
})

test('页面在挂载时自动读一次，并有手动刷新按钮', () => {
  assert.match(pageSource, /onMounted\(\(\) => \{[\s\S]{0,200}?void load\(\)/)
  assert.match(pageSource, /data-action="refresh-service-status"/)
})

test('页面渲染逐分钟曲线，且曲线来自 series 端点而非 admin 端点', () => {
  // 曲线区块必须真的挂在模板里（色带 + 分钟明细表）。
  assert.ok(pageSource.includes('data-series-strip'), '页面缺少色带元素')
  assert.ok(pageSource.includes('data-series-table'), '页面缺少分钟明细表')
  assert.ok(pageSource.includes('data-series-empty'), '页面缺少「刚重启、还没采样」空态')

  // 曲线数据只能来自 loadServiceStatusSeries（series 端点）。
  assert.match(pageSource, /loadServiceStatusSeries/)
  assert.match(pageSource, /getServiceStatusSeries/)
  // 仍然不得出现 admin 端点（曲线不能用 admin 那条换）。
  assert.ok(!pageCode.includes('admin'), '页面代码不得包含 admin 字样（会诱导后来者接 admin 端点）')
  // 不得把 points 直接当 summary 的字段用（summary 端点不返回 points）。
  assert.doesNotMatch(pageCode, /view\.points/, 'summary 视图没有 points —— 曲线必须走 seriesView')
})

test('曲线是增强区块：series 失败时整块不渲染，摘要照常显示（不整页崩）', () => {
  // 曲线面板必须受 seriesView 守卫 —— seriesView 为 null（端点失败）时不渲染。
  assert.match(pageCode, /v-if="seriesView"/, '曲线面板必须由 seriesView 守卫，否则端点失败会渲染半截')
  // series 失败只允许影响 seriesView，不得把 errorMessage 也置上（那会让整页变成错误态）。
  const loadRegion = pageCode.slice(pageCode.indexOf('const load = async'), pageCode.indexOf('const retire'))
  assert.ok(loadRegion.includes('loadServiceStatusSeries'), 'load 里必须请求 series')
  assert.match(loadRegion, /series\.ok/, 'series 失败必须被判定，不能假定总是成功')
  // 失败分支只写 seriesView（或什么都不写），不得写 errorMessage。
  assert.doesNotMatch(loadRegion, /series[\s\S]{0,120}errorMessage\.value\s*=/, 'series 失败不得污染整页错误态')
})

test('路由只新增了 service-status 那一条，未重排既有路由', () => {
  // 结构防线：service-status 必须在 ticket-admin 之后（追加，不是插到中间）。
  const ticketAdminAt = routerSource.indexOf("name: 'ticket-admin'")
  const serviceStatusAt = routerSource.indexOf("name: 'service-status'")
  assert.ok(ticketAdminAt > 0 && serviceStatusAt > ticketAdminAt, 'service-status 路由必须追加在 ticket-admin 之后')
  // 该路由只能出现一次（不能既在 children 里又在别处）。
  assert.equal(countOccurrences(routerSource, "name: 'service-status'"), 1)
})

/* ───────────── 接口族分桶区块（138 §4.3 / B3-4）─────────────
 *
 * 这一组钉的是**接线**：三态判定本身由 serviceStatusEndpoint.test.mjs 用真实
 * 函数喂真实响应体验证（行为层），这里只确认页面确实把它渲染出来、并且
 * 没有在模板里另写一套错误的判定。
 *
 * 只看源码字符串会是假钉子，所以每条都要求「有一个只有正确接线才可能出现的
 * 结构」——例如置灰必须由 endpointView.greyed 驱动，而不是页面自己算。
 */

test('页面渲染接口分桶区块，且置灰由 endpointView.greyed 驱动', () => {
  // 区块与卡片必须在模板里（缺一个就等于功能没接上）。
  assert.ok(pageSource.includes('data-endpoint-kind'), '页面缺少分桶卡片（data-endpoint-kind）')
  assert.ok(pageSource.includes('data-endpoint-rate'), '页面缺少分桶成功率元素')
  assert.ok(pageSource.includes('data-endpoint-hint'), '页面缺少分桶文案元素')
  assert.ok(pageSource.includes('data-endpoint-greyed'), '页面缺少置灰标记（测试要靠它断言）')

  // ★ 核心：置灰必须绑 endpointView.greyed。
  assert.match(pageCode, /:class="\{ 'endpoint-card--greyed': endpointView\.greyed \}"/,
    '置灰必须由 endpointView.greyed 驱动')
  assert.match(pageCode, /endpointView\.greyed \? 'true' : 'false'/,
    'data-endpoint-greyed 必须反映真实置灰状态')
})

test('页面不得用 totalRequests==0 判分桶显灰（S2 变异的主要形态）', () => {
  // 这是 S2 的实现形态：用 totalRequests 判灰会在「采样到了但 0 请求」时误显灰。
  assert.doesNotMatch(pageCode, /totalRequests\s*===?\s*0/,
    '不得用 totalRequests===0 判显灰（真 0 请求 vs 没采样同形）')
  assert.doesNotMatch(pageCode, /totalRequests\s*>\s*0/,
    '不得用 totalRequests>0 判有没有数据（那只证明有请求，不证明有采样）')
  assert.doesNotMatch(pageCode, /endpointView\.totalRequests/,
    '页面不应直接读 totalRequests 做判定（判定在 buildEndpointView 里）')
})

test('页面不得用成功率数值判分桶显灰（S3 变异的主要形态）', () => {
  // 有采样但成功率 0% 必须正常显示 0%，不能因为数值是 0 就显灰/当无数据。
  assert.doesNotMatch(pageCode, /endpointView\.rateText\s*===?\s*'0%'/,
    '不得用 rateText==="0%" 判分桶状态（全失败恰好是 0%）')
  assert.doesNotMatch(pageCode, /rateText\s*!==\s*'--'/,
    '不得用 rateText 反推有没有数据')
})

test('分桶视图来自 buildServiceStatusView（唯一判定点），页面不自算', () => {
  // 单一来源：页面只取 view.endpoints.search，不重新算 samples/total 的关系。
  assert.match(pageCode, /const endpointView = computed\(\(\) => view\.value\.endpoints\.search\)/,
    'endpointView 必须由 view.endpoints.search 派生')
  // endpointView 必须暴露给模板（否则模板里读不到，渲染成空）。
  assert.match(pageCode, /[ ]{2,}endpointView,/, 'setup() 的返回值里必须有 endpointView')
})

test('置灰文案是「暂无搜题接口采样数据（服务重启后从零开始）」', () => {
  // 文案由 .mjs 提供（单一来源），页面只渲染 endpointView.hint。
  // 断言 .mjs 里确实有这句字面量，且页面没有另写一份。
  const helper = readFileSync(resolve(testDir, './serviceStatusSummary.mjs'), 'utf8')
  assert.ok(helper.includes('暂无搜题接口采样数据（服务重启后从零开始）'),
    '置灰文案必须存在于 serviceStatusSummary.mjs（单一来源）')
  assert.match(pageCode, /endpointView\.hint/, '页面必须渲染 endpointView.hint')
  assert.ok(!pageCode.includes('暂无搜题接口采样数据'),
    '页面不得重复硬编码置灰文案（会与 helper 漂移）')
})

