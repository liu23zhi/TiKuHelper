/* 「我的设备」入口在两个角色下都可见的回归钉子（2026-11-15 实测）。
 *
 * ══ 缺陷形态（实测，不是推断）══════════════════════════════════════════════
 *
 * 用真实 SiderMenu 挂载（tests/helpers/vue-dom.cjs）分别在 user / admin 角色下渲染，
 * 采样实际生成的 `<a class="navigation-link">` 列表：
 *
 *   普通用户（10 项）：含 /my-devices  ✅
 *   管理员  （40 项）：**不含** /my-devices  ❌
 *
 * 原因：`my-devices` 只登记在 USER_MENU_ITEMS，没进 ADMIN_MENU_ITEMS。
 * 而 `visibleMenuItems = isAdmin ? ADMIN_MENU_ITEMS : USER_MENU_ITEMS`
 * ⇒ 管理员侧边栏完全没有「我的设备」。
 *
 * ══ 为什么这是真实的用户可见缺陷，不是设计取舍 ═══════════════════════════════
 *
 * 设备上限是**账号权益**：`EffectiveDeviceLimit` 走 users.level_id → user_levels.device_limit
 * （internal/service/device_pool.go:473），管理员自己一样受上限约束、一样会被挡。
 * 137 §5 第 6 步的文案就是「请到『我的设备』移除不用的设备」——
 * 而管理员如果进不去那个页面，这句文案对他就指向一个不存在的入口：
 * 他只能去「设备管理」（全站设备，需 RequireAdmin），既越权也找不到自己那台。
 *
 * ⚠️ 为什么既有测试没抓到（假钉子的根因）
 *
 *   - `sidebarSectionSource.test.mjs` 只断言「USER_MENU_ITEMS 里在 ADMIN 里的 key 必须进
 *     shared 组」，方向是 ADMIN → USER 的过滤；某 key 只在一侧时它不会红。
 *   - `roleCopyConsistency.test.mjs` 的 pairs 只覆盖 tickets/scripts/billing/my-tokens 四对，
 *     my-devices 不在其中。
 *   - `myDevicesHelpers.test.mjs` 只断言 router.js 注册了路由，**不碰菜单**。
 *   ⇒ 三层断言都「通过」，但真实渲染路径一次都没被采样过。
 *
 * ══ 为什么这里必须真实挂载 ═══════════════════════════════════════════════════
 *
 * 源码断言 `assert.match(menuSource, /key: 'my-devices'/)` 在当前缺陷状态下**也是绿的**
 * （那一行确实在 USER_MENU_ITEMS 里）。只有按角色渲染、数实际 DOM 出的链接，
 * 才能区分「两边都有」与「只有普通用户有」。
 *
 * ⚠️ 放在 src/utils/：`npm run test:unit` 采集 `src/utils/*.test.mjs`；
 * `tests/components/*` 只有 `test:components` 跑，而后者基线就红（与本改动无关的用例超时）。
 */
import test from 'node:test'
import assert from 'node:assert/strict'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
const webRoot = path.resolve(__dirname, '..', '..')
const require = createRequire(import.meta.url)

const DEPENDENCIES = ['vue', 'vue-router', '@vue/test-utils', 'jsdom', '@vue/compiler-sfc', '@babel/core', 'ant-design-vue']
const missingDeps = DEPENDENCIES.filter((spec) => {
  try { require.resolve(spec, { paths: [webRoot] }); return false } catch { return true }
})
if (missingDeps.length) {
  console.error(`[myDevicesMenuVisibility] 真实挂载依赖缺失：${missingDeps.join(', ')}`)
}
const behaviourSkip = missingDeps.length ? `缺少真实挂载依赖：${missingDeps.join(', ')}` : false

let env = null
function prepare() {
  if (env) return env
  process.env.NODE_PATH = [path.join(webRoot, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
  require('node:module')._initPaths()

  require(path.join(webRoot, 'tests', 'helpers', 'vue-dom.cjs'))
  const { reviewRebateAvailabilityResponse } = require(path.join(webRoot, 'tests', 'helpers', 'review-rebate-availability.cjs'))
  require('axios').defaults.adapter = async config => {
    const response = reviewRebateAvailabilityResponse(config)
    if (response) return response
    throw new Error(`Unexpected fixture operation ${config.method} ${config.url}`)
  }
  require.extensions['.css'] = (m) => { m.exports = {} }
  for (const key of ['DOMParser', 'HTMLButtonElement', 'HTMLDivElement', 'CustomEvent']) {
    globalThis[key] = window[key]
  }

  env = {
    mount: require('@vue/test-utils').mount,
    flushPromises: require('@vue/test-utils').flushPromises,
    vue: require('vue'),
    vueRouter: require('vue-router'),
    Antd: require('ant-design-vue'),
    user: require(path.join(webRoot, 'src', 'utils', 'currentUser.js')),
    Sider: require(path.join(webRoot, 'src', 'components', 'SiderMenu', 'index.vue')),
  }
  return env
}

/**
 * 以指定角色真实渲染 SiderMenu，返回实际出现过的 href 列表。
 *
 * 只采 `<a class="navigation-link">` —— 那是模板里唯一生成可点击入口的元素
 * （SiderMenu/index.vue 的 a-menu-item 内容）。数这个比数 a-menu-item 更稳：
 * 后者是 antd 内部结构，版本升级会变。
 */
async function renderedLinks(environment, role) {
  environment.user.setStoredCurrentUser({
    id: role === 'admin' ? 9101 : 9102,
    username: `SYNTH_${role.toUpperCase()}`,
    role,
    status: 'enabled',
    balance: 1,
  })
  const router = environment.vueRouter.createRouter({
    history: environment.vueRouter.createMemoryHistory(),
    routes: [{ path: '/:rest(.*)', component: { render: () => environment.vue.h('div') } }],
  })
  await router.push('/user-usage')
  await router.isReady()
  const wrapper = environment.mount(environment.Sider, {
    props: { currentUser: environment.user.getStoredCurrentUser() },
    global: { plugins: [router, environment.Antd] },
    attachTo: document.body,
  })
  await environment.flushPromises()
  await new Promise((resolve) => setTimeout(resolve, 40))
  await environment.flushPromises()

  const links = [...wrapper.element.querySelectorAll('a.navigation-link')]
    .map((node) => ({ href: node.getAttribute('href'), label: node.textContent.trim() }))
  // 采样结果留证据：失败时能直接看到两侧实际渲染了什么。
  const snapshot = { role, count: links.length, hrefs: links.map((item) => item.href) }

  wrapper.unmount()
  environment.user.clearStoredCurrentUser()
  document.body.replaceChildren()
  assert.ok(snapshot.count > 0,
    `前置失效：${role} 侧边栏一个入口都没渲染出来（${JSON.stringify(snapshot)}）`)
  return snapshot
}

// ══════════════════════════════════════════════════════════════════════════
// ① 核心判据：「我的设备」在两个角色下都必须真的渲染出来
// ══════════════════════════════════════════════════════════════════════════
test('① 「我的设备」入口在普通用户与管理员两侧都渲染出来', { skip: behaviourSkip }, async () => {
  const environment = prepare()
  const asUser = await renderedLinks(environment, 'user')
  const asAdmin = await renderedLinks(environment, 'admin')

  assert.equal(asUser.hrefs.includes('/my-devices'), true,
    `普通用户必须能看到「我的设备」。实际渲染：${JSON.stringify(asUser.hrefs)}`)
  assert.equal(asAdmin.hrefs.includes('/my-devices'), true,
    '管理员侧边栏同样必须有「我的设备」—— 设备上限是账号权益，管理员自己也会被挡在外面；'
    + '只有「设备管理」（全站设备）而没有自助入口，他就没有任何办法移除自己那台设备。'
    + `实际渲染：${JSON.stringify(asAdmin.hrefs)}`)
})

// ══════════════════════════════════════════════════════════════════════════
// ② 反向防线：加了自助入口不等于泄漏运营入口
// ══════════════════════════════════════════════════════════════════════════
test('② 「我的设备」不得变成管理员专属，同时「设备管理」不得泄漏给普通用户', { skip: behaviourSkip }, async () => {
  const environment = prepare()
  const asUser = await renderedLinks(environment, 'user')
  const asAdmin = await renderedLinks(environment, 'admin')

  // my-devices 是「我自己的设备」：任何角色都能进（后端 /user/devices* 恒取当前用户）。
  assert.equal(asUser.hrefs.includes('/my-devices'), true, '普通用户必须有自助入口')

  // device-admin 是「全站设备」：能封禁任何人的设备，绝不能出现在普通用户侧边栏。
  assert.equal(asUser.hrefs.includes('/device-admin'), false,
    `「设备管理」（全站设备、可全局封禁）绝不能出现在普通用户菜单里。实际渲染：${JSON.stringify(asUser.hrefs)}`)
  assert.equal(asAdmin.hrefs.includes('/device-admin'), true,
    '管理员必须保留「设备管理」入口（修「我的设备」时不得把它挤掉）')

  // 两个入口必须同时存在：它们是「我的」与「全站」两个不同作用域，不能互相替代。
  assert.equal(asAdmin.hrefs.filter((href) => href === '/my-devices').length, 1,
    '「我的设备」在管理员菜单里必须恰好出现一次（不能重复渲染）')
})
