import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

// 设备管理页 / API / 路由的契约钉子（137 设备体系 §8.2 / §8.3 / §9.2）。
//
// 只做 readFileSync + 字符串/正则断言，**不 import vue**：
// 本仓 worktree 常缺 node_modules，任何 import @/... 的测试在那种环境下直接崩，
// 于是它只能在本机绿、在别处红 —— 那种测试不如不写。
//
// # 为什么钉这几条（每条都对应一个「静默坏掉」的形态）
//
//  1. 封禁 API 写成 `/admin/devices/ban` → 与 `POST /admin/devices/:id/unbind`
//     同层，旧版 gin 直接 panic（137 §8.3）。
//  2. 解绑用 deviceId 而不是绑定行 id → 一次断掉所有账号（那是封禁口径）。
//  3. 解封不过 encodeURIComponent → deviceId 里的 `:` 被当成路径分隔符。
//  4. 页面自己 import request（绕过 DeviceAdminApi.js）→ 中间层重新挑字段时
//     把关键字段丢掉，请求看起来成功、语义丢了（本仓已发生过一次）。
//  5. 路由未登记进 ADMIN_ONLY_ROUTE_NAMES → 普通用户直敲 URL 能进页面。

const web = process.env.TIKU_WEB_SOURCE || path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const read = relative => readFileSync(path.join(web, relative), 'utf8')

const pageSource = () => read('src/pages/DeviceAdmin/index.vue')
const apiSource = () => read('src/api/DeviceAdminApi.js')
const routerSource = () => read('src/router/router.js')

// 从数组/Set 字面量里切出某个 const 的区间（与 userBanSource.test.mjs 同一工具，
// 但这里只需要 Set 一种形态，写法更窄以便断言失败时原因明确）。
function sliceConstSet(source, constName) {
  const marker = `const ${constName} = new Set([`
  const start = source.indexOf(marker)
  if (start < 0) return null
  const end = source.indexOf('])', start)
  if (end < 0) return null
  return source.slice(start, end)
}

// 剥注释：断言「不得出现某写法」时，修复代码的**注释**恰恰要解释那件事，
// 不剥注释会把最有价值的说明当成违规命中（本仓已踩过）。
function stripComments(source) {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/<!--[\s\S]*?-->/g, '')
    .split('\n')
    .map(line => {
      const at = line.search(/(^|\s)\/\//)
      if (at < 0) return line
      const before = line.slice(0, at)
      return (before.match(/['"`]/g) || []).length % 2 === 1 ? line : before
    })
    .join('\n')
}

test('DeviceAdminApi 用 /admin/device-bans 前缀而不是 /admin/devices/ban', () => {
  const code = stripComments(apiSource())
  assert.match(code, /url:\s*'\/api\/admin\/device-bans'/, '封禁必须用 /api/admin/device-bans')
  assert.match(code, /`\/api\/admin\/device-bans\/\$\{encodeURIComponent\(value\)\}`/,
    '解封必须 encodeURIComponent（deviceId 含 `:`）')
  assert.doesNotMatch(code, /\/api\/admin\/devices\/ban/, '不得使用 /admin/devices/ban（旧版 gin 会 panic）')
  assert.match(code, /`\/api\/admin\/devices\/\$\{id\}\/unbind`/, '解绑路径必须是绑定行 id')
  assert.doesNotMatch(code, /\/api\/admin\/devices\/\$\{encodeURIComponent/, '解绑不得按 deviceId')
})

test('DeviceAdminApi 保留四个关键校验与语义', () => {
  const code = stripComments(apiSource())
  // deviceId 必填、reason 必填（封禁是不可自助解除的处置，审计里必须有原因）。
  assert.match(code, /设备 ID 不能为空/)
  assert.match(code, /封禁原因不能为空/)
  // 负数时长必须显式拒掉（静默当永久会把误操作变成不可撤销的处置）。
  assert.match(code, /durationAmount\s*<\s*0/)
  // 时长只在 >0 时才带上 durationUnit（带 0 会让请求体与永久封禁无法区分）。
  assert.match(code, /if\s*\(durationAmount\s*>\s*0\)/)
  // 解绑参数是绑定行 id：必须是正整数校验。
  assert.match(code, /Number\.isInteger\(id\)\s*\|\|\s*id\s*<=\s*0/)
  // 全部请求走 errorFeedback: 'local'（页面自己有失败态，不能重复弹全局提示）。
  const calls = code.match(/errorFeedback:/g) || []
  assert.equal(calls.length, 5, `五条请求都必须带 errorFeedback: 'local'，实际 ${calls.length} 处`)
})

test('DeviceAdminApi 删除已解绑记录按绑定行ID发DELETE，不调用解封接口', () => {
  const calls = []
  const code = apiSource().replace(/^import .*$/gm, '').replace(/export function/g, 'function')
  const remove = new Function('request', `${code}\nreturn deleteAdminDevice;`)(request => { calls.push(request); return request })
  assert.deepEqual(remove(123), { errorFeedback: 'local', url: '/api/admin/devices/123', method: 'delete' })
  for (const value of [0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1, 'device:uuid']) assert.throws(() => remove(value), /删除参数错误/)
  assert.equal(calls.length, 1, '非法记录ID不能发请求')
  const page = pageSource()
  assert.match(page, /:disabled="[^"]*!record\.revokedAt/)
  assert.match(page, /deleteAdminDevice\(/)
  assert.match(page, /设备封禁和账号拉黑状态会保留/)
})

test('DeviceAdmin 页面不直接 import request，只用 DeviceAdminApi', () => {
  const code = stripComments(pageSource())
  assert.doesNotMatch(code, /from ['"]@\/utils\/request['"]/,
    '页面不得绕过 DeviceAdminApi 直接发请求（中间层会重新挑字段）')
  assert.match(code, /from '@\/api\/DeviceAdminApi'/)
  for (const fn of ['banAdminDevice', 'getAdminDevices', 'unbanAdminDevice', 'unbindAdminDevice', 'deleteAdminDevice']) {
    assert.ok(code.includes(fn), `页面必须使用 ${fn}`)
  }
})

test('DeviceAdmin 页面把格式化逻辑交给纯函数（不自己写一份）', () => {
  const code = stripComments(pageSource())
  assert.match(code, /from '@\/utils\/deviceAdminHelpers\.mjs'/)
  for (const fn of ['deviceStatusText', 'deviceStatusColor', 'banExpiryText', 'canUnbindDevice', 'revokeReasonText']) {
    assert.ok(code.includes(fn), `页面必须使用 helper ${fn}`)
  }
  // 永久封禁的判据只能有一处（helper 的 banExpiryText）。
  assert.doesNotMatch(code, /=== null \? '永久'/, '页面不得再写一份「永久」判据')
})

test('DeviceAdmin 页面权限判据用 stillOwnRoute 风格而不是一次性路径快照', () => {
  // 本页只有一条路由，不存在「共用组件路由切换」问题，因此仍然可以用
  // route.path === mountedPath —— 但必须同时校验 admin 身份与登出纪元。
  const code = stripComments(pageSource())
  assert.match(code, /const canManage = \(\) => alive && route\.path === mountedPath/)
  assert.match(code, /getCurrentUserLogoutEpoch\(\) === mountedLogoutEpoch/)
  assert.match(code, /isAdminUser\(user\)/)
  // 非管理员必须看到告警（而不是静默白屏）。
  assert.match(pageSource(), /当前身份无设备管理权限/)
})

test('router.js 注册 device-admin 并登记进 ADMIN_ONLY_ROUTE_NAMES', () => {
  const source = routerSource()
  // ① 路由项存在，name 与 path 都对。
  assert.match(source, /path:\s*'device-admin',\s*\n\s*name:\s*'device-admin',\s*\n\s*component:\s*DeviceAdmin,/,
    '必须注册 device-admin 路由（path/name/component 三行相邻）')
  // ② 懒加载 chunk 名称唯一：与其它页共用 chunk 会让首屏体积失控。
  assert.match(source, /webpackChunkName: "route-device-admin"/)
  // ③ 必须登记进管理员专属集合，否则普通用户直敲 URL 能进页面。
  const setRegion = sliceConstSet(source, 'ADMIN_ONLY_ROUTE_NAMES')
  assert.ok(setRegion, 'ADMIN_ONLY_ROUTE_NAMES 必须仍是 new Set([...]) 形态')
  assert.ok(setRegion.includes(`'device-admin'`),
    'device-admin 必须登记进 ADMIN_ONLY_ROUTE_NAMES —— 能封禁设备等于能切断任何一台机器的服务')
  // ④ 反向钉子：不得把 device-admin 加进公开路由白名单。
  const publicRegion = sliceConstSet(source, 'PUBLIC_ADAPTER_ROUTE_NAMES')
  assert.ok(publicRegion, 'PUBLIC_ADAPTER_ROUTE_NAMES 必须存在')
  assert.ok(!publicRegion.includes(`'device-admin'`), 'device-admin 绝不能进公开白名单')
})

test('页面暴露的 SiderMenu 待插行原文可用（主代理独占该文件，这里只校验形状）', () => {
  // 本文件**不读也不改** SiderMenu（主代理独占）。这里只把待插行原文钉成常量，
  // 让「该插什么」有唯一来源，避免主代理手工抄错 key 或引入新图标。
  const expected = `  { key: 'device-admin', label: '设备管理', icon: DatabaseOutlined },`
  assert.match(expected, /\{ key: 'device-admin', label: '设备管理', icon: DatabaseOutlined \}/)
  // 使用的图标必须**已经**被 SiderMenu 引入（这样主代理插入时不需要动 import 段，
  // 少一处可撞车的写点）。DatabaseOutlined 目前用于 user-exam-tool / ticket-image-cache，
  // 本仓图标复用是既有惯例（FileTextOutlined 已复用 4 次）。
  const menu = read('src/components/SiderMenu/index.vue')
  assert.match(menu, /^\s*DatabaseOutlined,\s*$/m,
    'SiderMenu 必须已从 @ant-design/icons-vue 引入 DatabaseOutlined（否则待插行会渲染成空图标）')
  // 反向钉子：待插行不得被误加进 USER_MENU_ITEMS。
  const userRegion = menu.slice(menu.indexOf('const USER_MENU_ITEMS = ['), menu.indexOf('// 普通用户可见组从 USER_MENU_ITEMS 派生'))
  assert.ok(userRegion.length > 0, 'USER_MENU_ITEMS 区段定位失败')
  assert.ok(!userRegion.includes('device-admin'),
    'device-admin 绝不能加进 USER_MENU_ITEMS —— 普通用户看到该入口本身就是信息泄漏')
})
