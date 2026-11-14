import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

// 账号封禁页与 API 的契约钉子（121 §4.8 / 121 决策 6）。
//
// 只做 readFileSync + 字符串/正则断言，**不 import vue**：
// 本仓 worktree 常缺 node_modules，任何 import @/... 的测试在那种环境下直接崩，
// 于是它只能在本机绿、在别处红 —— 那种测试不如不写。
//
// # 为什么钉这几条（每条都对应一个「静默坏掉」的形态）
//
//  1. 永久封禁显示成「—」或空 → 运营把「永久」读成「数据缺失」，于是重复封禁或误解封。
//  2. 页面直接 import request（绕过 UserBanApi.js）→ 换头像那次事故的重演：
//     中间层重新挑字段时把 expiresAt 丢掉，请求看起来成功、语义丢了。
//  3. 解封用 record.id 而不是 userId → 后端按 userId 处理，传错值会解封另一个账号。
//  4. 菜单只加 ADMIN_MENU_ITEMS → 普通用户不该看到封禁入口；加进 USER_MENU_ITEMS 是泄漏。

const web = process.env.TIKU_WEB_SOURCE || path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const read = relative => readFileSync(path.join(web, relative), 'utf8')

const page = () => read('src/pages/UserBan/index.vue')
// userListPage 是「用户管理」页（t203：把「状态」换成封禁控件的那一页）。
// 单独一个读取器：本文件的 page() 指**封禁管理页**，两者是不同文件，
// 合成一个会让断言悄悄测错对象（我第一版就踩了这个：断言 :max 时测的是封禁页，
// 而 :max 在用户管理页，于是「改对了却红、改错了却绿」）。
const userListPage = () => read('src/pages/UserList/index.vue')
const api = () => read('src/api/UserBanApi.js')
const routerSource = () => read('src/router/router.js')
const siderSource = () => read('src/components/SiderMenu/index.vue')

// 「从数组字面量里切出某个 const 的区间」用的小工具。
//
// 直接从 indexOf 切到下一个 `]` 是危险的：数组项里可能含 `]`（例如
// `pageSizeOptions: ['10','20']`）。这里用「行首缩进 + 单独的 `]` 行」作为结束判据，
// 与源码的实际格式化风格一致；找不到就返回 null，让调用方显式 fail
// 而不是静默拿到空串（空串会让所有 doesNotMatch 断言恒真）。
//
// ⚠️ 必须同时支持两种声明形态，因为它们在本仓都真实存在：
//
//	const ADMIN_MENU_ITEMS = [ ... ]              // 普通数组
//	const ADMIN_ONLY_ROUTE_NAMES = new Set([ ... ]) // Set 包装
//
// 只匹配 `= [` 会让 Set 那一种恒返回 null —— 而调用方的 `assert.ok(region)`
// 会把它报成「定位失败」，看起来像源码改了，实际是工具漏了一种形态。
// 这正是本仓记过的「验证工具本身出错」那一类。
const ARRAY_DECL_PATTERNS = [' = new Set([', ' = [']

function sliceConstArray(source, constName) {
  let start = -1
  for (const pattern of ARRAY_DECL_PATTERNS) {
    const marker = `const ${constName}${pattern}`
    const found = source.indexOf(marker)
    if (found >= 0) {
      start = found
      break
    }
  }
  if (start < 0) return null
  const lines = source.slice(start).split('\n')
  const collected = [lines[0]]
  for (let index = 1; index < lines.length; index += 1) {
    collected.push(lines[index])
    const trimmed = lines[index].trim()
    // Set 形态的结束行是 `])`，普通数组是 `]`。
    if (trimmed === ']' || trimmed === '])') return collected.join('\n')
  }
  return null
}

test('API 覆盖三个端点且路径正确（列表 / 封禁 / 按 userId 解封）', () => {
  const source = api()

  for (const [fn, url, method] of [
    ['getUserBans', '/api/admin/user-bans', 'get'],
    ['createUserBan', '/api/admin/user-bans', 'post'],
    ['revokeUserBan', '/api/admin/user-bans/', 'delete'],
  ]) {
    assert.match(source, new RegExp(`export function ${fn}\\b`), `缺少导出 ${fn}`)
    assert.ok(source.includes(url), `${fn} 必须打到 ${url}`)
    assert.match(source, new RegExp(`method: '${method}'`), `${fn} 必须用 ${method.toUpperCase()}`)
  }

  // 解封路径必须是 ${value}（= userId），不是 ${id}。变量名是语义的一部分：
  // 后端按 userId 处理，写成 id 的人下一轮很可能换成 record.id。
  assert.match(source, /url: `\/api\/admin\/user-bans\/\$\{value\}`/,
    '解封必须打到 /admin/user-bans/:userId（变量名应为 value=userId）')
  assert.match(source, /const value = Number\(userId\)/,
    '解封的入参必须来自 userId')
  assert.doesNotMatch(source, /const value = Number\(id\)/,
    '解封不得用记录 id —— 后端按 userId 处理，传记录 id 会解封到另一个账号')
})

// 自定义时长**无上限**（用户 2026-11-14 指令：「封禁时间不要有限制」）。
//
// 为什么必须正向钉住「没有 max」：删掉一个约束时，如果只断言「365 以内能用」，
// 那么把 :max="365" 加回去也照样通过 —— 约束的存在与否不可观测。
//
// ⚠️ 断言前必须**剥掉注释**：本页把「上一版有 :max="365" 与最长 365 天，已删除」
// 写成了注释（保留历史理由），不剥注释会让 doesNotMatch 命中那段说明文字而假红。
// 这与 TIKU 另一处「对 Go 源码断言前必须剥注释」是同一类陷阱：
// 注释里解释**为什么不能这么写**时，必然包含被禁止的那种写法。
const stripComments = source => source
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/^\s*\/\/[^\n]*$/gm, '')

test('自定义时长没有上限，但保留「用永久封禁」的引导', () => {
  // ⚠️ 读的是**用户管理页**（UserList），不是封禁管理页（page()）。
  const source = stripComments(userListPage())

  // ① 输入框不得再有 :max（有 :min="1" 是对的，那是「正整数」校验不是上限）。
  assert.doesNotMatch(source, /:max="\d+"/,
    '自定义时长输入框不得有 :max 上限（用户明确要求封禁时间不要有限制）')
  assert.match(source, /:min="1"/, '仍必须有 :min="1"（正整数校验，不是上限）')
  // ② 页面文案不得再声明「最长 N 天」。
  assert.doesNotMatch(source, /最长\s*\d+\s*天/, '文案不得再写「最长 N 天」')
  // ③ 但「永久封禁」的引导必须留着：那是引导不是硬拦。
  assert.ok(source.includes('永久封禁'),
    '必须保留「更长时间请选永久封禁」这类引导（永久是独立选项）')
})

test('封禁必须同时提交 userId 与 reason，且不替调用方猜 expiresAt 的默认值', () => {
  const source = api()

  // t203 起请求体改由变量 data 组装（因为要按需追加 durationAmount/durationUnit），
  // 所以断言「data 里含这三个字段」而不是旧的字面量写法。
  assert.match(source, /const data = \{ userId, reason, expiresAt \}/,
    '封禁请求体必须含 userId / reason / expiresAt 三个字段')
  assert.match(source, /data,\s*\n\s*\}\)/, '请求必须提交组装好的 data')
  // reason 必须先 trim 再判空：纯空白也算空（后端同样口径）。
  assert.match(source, /const reason = String\(payload\.reason == null \? '' : payload\.reason\)\.trim\(\)/,
    'reason 必须 trim 后判空')
  assert.match(source, /if \(!reason\) throw new Error\('封禁原因不能为空'\)/,
    '前端也要拒绝空原因（不能等到后端 400）')
  // expiresAt 不得给默认值兜底：默认成永久会让「只想封 1 小时」变成永久。
  assert.doesNotMatch(source, /expiresAt:\s*expiresAt\s*\|\|/,
    '不得为 expiresAt 提供默认值兜底（永久/临时必须由调用方明确）')
})

test('自定义时长（t203）：传了时长才带 duration 字段，且由服务端算到期时间', () => {
  const source = api()

  // 时长两个字段只在 durationAmount > 0 时追加。
  // 恒带 durationAmount:0 会让「永久封禁」与「封 0 小时」在请求体上无法区分。
  assert.match(source, /if \(durationAmount > 0\) \{/, '时长字段必须条件追加')
  assert.match(source, /data\.durationAmount = durationAmount/, '必须提交 durationAmount')
  assert.match(source, /data\.durationUnit = String\(payload\.durationUnit \|\| 'hour'\)/,
    '必须提交 durationUnit，缺省按小时')
  // 负数/小数必须拒绝（不能静默退化成永久）。
  assert.match(source, /if \(!Number\.isInteger\(durationAmount\) \|\| durationAmount < 0\)/,
    '时长必须是非负整数')
  // 前端不得自己把时长换算成绝对时间：那会把浏览器时区/时钟偏差带进封禁时长。
  assert.doesNotMatch(source, /Date\.now\(\)\s*\+/, '前端不得自行换算到期时间戳')
  assert.doesNotMatch(source, /new Date\(\).*durationAmount/, '前端不得用本地时钟换算时长')
})

test('页面「永久」显示：expiresAt 为空必须是「永久」而不是「—」或空', () => {
  const source = page()

  // 找到 formatExpiresAt 的实现体（它不是数组，直接按函数名切到下一个 }）。
  const start = source.indexOf('function formatExpiresAt(value) {')
  assert.ok(start > 0, '页面必须有 formatExpiresAt —— 「永久」这条判据的唯一落点')
  const body = source.slice(start, source.indexOf('\n    }', start))

  // 空值三态都必须返回「永久」：null / undefined / 空串。
  // 后端对空串也解析为 nil（落库 expires_at IS NULL），所以空串也是永久。
  assert.match(body, /value === null \|\| value === undefined/, '必须显式处理 null / undefined')
  assert.match(body, /String\(value\)\.trim\(\) === ''/, '必须把空串也当作永久（后端同样解析为 nil）')
  assert.ok(body.includes("return '永久'"), 'formatExpiresAt 必须返回字面量「永久」')
  assert.doesNotMatch(body, /return '—'/, 'formatExpiresAt 不得对空值返回「—」（会被读成数据缺失）')

  // 模板里到期时间列必须走 formatExpiresAt，不能走通用的 formatDateTime。
  // 通用格式化对空值返回「—」，直接套用就丢掉了「永久」语义。
  assert.match(source, /column\.key === 'expiresAt'"[^>]*>\{\{ formatExpiresAt\(record\.expiresAt\) \}\}/,
    "到期时间列必须用 formatExpiresAt(record.expiresAt)，不能用 formatDateTime")
})

test('页面只能通过 UserBanApi.js 发请求（不得直接 import request）', () => {
  const source = page()

  assert.doesNotMatch(source, /from '\.\.\/utils\/request'/, '页面不得直接 import request')
  assert.doesNotMatch(source, /from '@\/utils\/request'/, '页面不得直接 import request（别名路径）')
  assert.doesNotMatch(source, /\baxios\b/, '页面不得直接使用 axios')
  assert.doesNotMatch(source, /fetch\(/, '页面不得直接 fetch')
  // 必须从 API 模块导入三个函数（证明请求确实经由它）。
  assert.match(source, /from '@\/api\/UserBanApi'/, '页面必须从 @/api/UserBanApi 导入请求函数')
  for (const fn of ['createUserBan', 'getUserBans', 'revokeUserBan']) {
    assert.ok(source.includes(fn), `页面必须使用 ${fn}（经 API 模块）`)
  }
})

test('三态标签齐全：生效中 / 已过期 / 已撤销', () => {
  const source = page()
  for (const label of ['生效中', '已过期', '已撤销']) {
    assert.ok(source.includes(`'${label}'`), `缺少状态标签「${label}」`)
  }
  // revoked 与 expired 的配色必须不同：两者都是「不再封禁」，
  // 但一个是人为撤销、一个是自然到期，运营靠颜色区分。
  assert.match(source, /STATUS_COLORS = \{ active: 'red', expired: 'default', revoked: 'green' \}/,
    '已撤销与已过期必须是不同颜色')
})

test('永久/临时是互斥的两条路径：临时必须填到期时间', () => {
  const source = page()
  // 永久时 expiresAt 必须传空串（= NULL），不能传 undefined 让后端「没有该字段」。
  assert.match(source, /let expiresAt = ''/, '永久封禁必须传空串（后端解析为 NULL = 永久）')
  assert.match(source, /if \(!draft\.permanent\)/, '必须显式分支处理临时封禁')
  assert.match(source, /临时封禁必须填写到期时间/,
    '临时封禁未填时间时必须报错，不能静默按永久提交')
})

test('router.js 三处接线齐全：懒加载 / 路由项 / ADMIN_ONLY', () => {
  const source = routerSource()

  assert.match(source, /const UserBan = \(\) => import\(\/\* webpackChunkName: "route-user-ban" \*\/ '\.\.\/pages\/UserBan\/index\.vue'\)/,
    '必须有懒加载声明（webpackChunkName 用于产物分组）')
  assert.match(source, /path: 'user-bans',\s*\n\s*name: 'user-bans',\s*\n\s*component: UserBan,/,
    '路由项必须是 path/name/component 三件套')

  const adminOnly = sliceConstArray(source, 'ADMIN_ONLY_ROUTE_NAMES')
  assert.ok(adminOnly, '必须能定位 ADMIN_ONLY_ROUTE_NAMES 数组')
  assert.ok(adminOnly.includes("'user-bans'"),
    'ADMIN_ONLY_ROUTE_NAMES 必须登记 user-bans，否则普通用户直接敲 URL 能进')
})

test('SiderMenu 只加 ADMIN_MENU_ITEMS，绝不加 USER_MENU_ITEMS', () => {
  const source = siderSource()

  const adminItems = sliceConstArray(source, 'ADMIN_MENU_ITEMS')
  assert.ok(adminItems, '必须能定位 ADMIN_MENU_ITEMS 数组')
  assert.match(adminItems, /\{ key: 'user-bans', label: '账号封禁', icon: StopOutlined \}/,
    'ADMIN_MENU_ITEMS 必须有 { key: user-bans, label: 账号封禁, icon: StopOutlined }')

  // 关键判据：普通用户的菜单里不能出现封禁入口。
  const userItems = sliceConstArray(source, 'USER_MENU_ITEMS')
  assert.ok(userItems, '必须能定位 USER_MENU_ITEMS 数组')
  assert.ok(!userItems.includes('user-bans'),
    'USER_MENU_ITEMS 绝不能包含 user-bans —— 普通用户看到封禁入口本身就是信息泄漏')
})

test('ADMIN_MENU_SECTIONS 的派生逻辑未被改动（本轮刚修过文案取值）', () => {
  const source = siderSource()

  // 分区必须仍从 ADMIN_MENU_ITEMS 派生，且 shared 分区的文案取自 USER_MENU_ITEMS。
  // 这两条是本轮 c3d8206 刚修好的，加菜单项时最容易顺手改坏。
  assert.match(source, /USER_MENU_KEYS = new Set\(USER_MENU_ITEMS\.map\(\(item\) => item\.key\)\)/,
    'shared 分区的 key 集合必须仍从 USER_MENU_ITEMS 派生')
  assert.match(source, /ADMIN_MENU_ITEMS\.filter\(\(item\) => USER_MENU_KEYS\.has\(item\.key\)\)/,
    'shared 分区必须仍由 ADMIN_MENU_ITEMS 按 USER_MENU_KEYS 过滤得出')
  assert.match(source, /sharedSectionItem/,
    'shared 分区的文案取值必须仍走 sharedSectionItem（不能直接用 ADMIN_MENU_ITEMS 的对象）')
})
