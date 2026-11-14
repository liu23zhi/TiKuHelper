import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

// 令牌管理拆双入口（2026-11-14 用户诉求）的回归钉子。
//
// ══ 用户诉求原文 ══════════════════════════════════════════════════
//
//   「脚本管理 就只显示所有脚本，因为有 我的脚本。
//    请同步修改 令牌管理。添加我的令牌。我的令牌只显示当前账号的令牌，
//    令牌管理是管理员的，显示所有令牌。」
//
// ══ 拆法与脚本管理同一范式 ════════════════════════════════════════
//
//   my-tokens     meta.viewMode='self'   → 标题「我的令牌」，只列自己
//   token-admin   meta.viewMode='admin'  → 标题「令牌管理」，列全站
//
//   组件读 route.meta.viewMode 决定 isAdmin，**不是** isAdminUser(currentUser)。
//
// ══ 为什么要真实挂载 ══════════════════════════════════════════════
//
// 要钉的行为是「管理员从『我的令牌』进来看到的是自己的视图，从『令牌管理』
// 进来才是全站」——这件事只有把同一组件分别挂到两条路由上才能观测。
// 只断言 `routeViewMode` 字符串存在，证明不了 isAdmin 真的跟着入口变。
//
// ⚠️ 另一条关键判据（2026-11-14 踩过的坑）：不能用
// `route.path === mountedPath` 做判据 —— 两条路由共用组件，Vue 复用实例时
// setup 不重跑，mountedPath 会冻结在挂载时路径。所以本文件专门有一条用例
// 断言切换入口后判据仍成立。

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const webRoot = path.resolve(__dirname, '..', '..')
const require = createRequire(import.meta.url)

const BEHAVIOUR_DEPS = ['vue', 'vue-router', '@vue/test-utils', 'jsdom', '@vue/compiler-sfc', '@babel/core']
const missingDeps = BEHAVIOUR_DEPS.filter(spec => {
  try { require.resolve(spec); return false } catch { return true }
})
const behaviourSkip = missingDeps.length
  ? `真实挂载缺少依赖：${missingDeps.join(', ')}（node_modules 未安装完整）`
  : false
if (behaviourSkip) console.error(`[tokenDualEntry] 行为用例将被跳过：${behaviourSkip}`)

const readPage = (...segments) => readFileSync(path.join(webRoot, 'src', 'pages', ...segments), 'utf8')
const readRouter = () => readFileSync(path.join(webRoot, 'src', 'router', 'router.js'), 'utf8')

// ── 环境准备：stub API + 装 jsdom/SFC 钩子 + 合成账号 ────────────────
function prepare() {
  const Module = require('node:module')
  const stub = (file, exportsObj) => {
    const m = new Module(file)
    m.loaded = true
    m.exports = exportsObj
    require.cache[file] = m
  }
  const never = () => new Promise(() => {})

  // 记录列表请求，用来断言「谁在向服务端要全站数据」。
  // 返回空数组即可 —— 本文件测的是请求参数与视图判定，不是渲染数据。
  const listCalls = []
  const tokenApi = new Proxy({}, {
    get: (_t, key) => {
      if (key === 'getAccessTokens') {
        return (params) => { listCalls.push(params); return Promise.resolve({ status: 200, data: [] }) }
      }
      return never
    },
  })
  stub(path.join(webRoot, 'src', 'api', 'TikuComponentApi.js'), tokenApi)
  stub(path.join(webRoot, 'src', 'api', 'UserLevelApi.js'), new Proxy({}, { get: () => never }))
  stub(path.join(webRoot, 'src', 'utils', 'request.js'), { default: never })

  require(path.join(webRoot, 'tests', 'helpers', 'vue-dom.cjs'))

  const user = require(path.join(webRoot, 'src', 'utils', 'currentUser.js'))
  return { listCalls, user, mount: require('@vue/test-utils').mount, flushPromises: require('@vue/test-utils').flushPromises, vue: require('vue'), vueRouter: require('vue-router') }
}

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))

// 把组件挂到给定路线上，返回采样函数。
async function mountOnRoute(env, Component, routeDef, sample) {
  const { mount, flushPromises, vue, vueRouter } = env
  const router = vueRouter.createRouter({
    history: vueRouter.createMemoryHistory(),
    routes: [{ ...routeDef, component: Component }],
  })
  const Host = vue.defineComponent({ render: () => vue.h(vueRouter.RouterView) })
  await router.push(routeDef.path)
  await router.isReady()
  const wrapper = mount(Host, { global: { plugins: [router] }, attachTo: document.body })
  await flushPromises(); await sleep(80); await flushPromises()
  const result = sample(wrapper, router)
  return { wrapper, router, result }
}

// 只取可执行代码做「旧判据是否还在」的断言（注释里正是要解释它为什么不行）。
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

// ══════════════════════════════════════════════════════════════
// 路由契约（结构层，快速诊断）
// ══════════════════════════════════════════════════════════════

test('router.js 里令牌是两个路由、两个 name、各自声明 viewMode', () => {
  const source = readRouter()

  // my-tokens：用户入口，viewMode='self'
  assert.match(source, /path: 'my-tokens',\s*\n\s*name: 'my-tokens',\s*\n\s*component: AccessTokenManagement,\s*\n\s*meta: \{ viewMode: 'self' \}/,
    'my-tokens 必须是独立路由且声明 viewMode=self')
  // token-admin：管理入口，viewMode='admin'
  assert.match(source, /path: 'token-admin',\s*\n\s*name: 'token-admin',\s*\n\s*component: AccessTokenManagement,\s*\n\s*meta: \{ viewMode: 'admin' \}/,
    'token-admin 必须是独立路由且声明 viewMode=admin')

  // 两个 name 必须不同 —— 同一个 name 侧边栏无法分别高亮（脚本管理踩过这个坑）。
  assert.notEqual('my-tokens', 'token-admin')
})

test('token-admin 登记进 ADMIN_ONLY_ROUTE_NAMES，my-tokens 不在其中', () => {
  const source = readRouter()
  const block = /const ADMIN_ONLY_ROUTE_NAMES = new Set\(\[([\s\S]*?)\]\)/.exec(source)
  assert.ok(block, 'router.js 里必须有 ADMIN_ONLY_ROUTE_NAMES')

  assert.match(block[1], /'token-admin'/, 'token-admin 必须登记（普通用户直敲 URL 要被挡）')
  assert.doesNotMatch(block[1], /'my-tokens'/, 'my-tokens 绝不能登记 —— 它是普通用户自己的入口')
})

test('旧入口 access-tokens 保留为重定向，不留 404', () => {
  const source = readRouter()
  // 用户的书签/历史里还留着 /adapter/access-tokens，直接 404 等于页面凭空消失。
  assert.match(source, /path: 'access-tokens',[\s\S]{0,400}redirect:/,
    'access-tokens 必须重定向到新入口，不能变成死链')
})

test('组件用 route.meta.viewMode 决定视图，可执行代码里不残留 mountedPath 判据', () => {
  const code = stripComments(readPage('AccessTokenManagement', 'index.vue'))
  assert.doesNotMatch(code, /route\.path\s*===\s*mountedPath/,
    '不得用「路径没变」当判据 —— 两条路由共用组件时实例复用会让它恒 false')
  assert.doesNotMatch(code, /mountedPath/, 'mountedPath 快照不得保留')
  assert.match(code, /route\?\.meta\?\.viewMode|route\.meta\.viewMode/,
    '必须读 route.meta.viewMode 决定视图')
})

test('页面标题/说明按入口切换，两侧文案点明数据范围', () => {
  const source = readPage('AccessTokenManagement', 'index.vue')
  assert.match(source, /:title="pageTitle"/, '标题必须绑定 pageTitle')
  assert.match(source, /:description="pageDescription"/, '说明必须绑定 pageDescription')
  assert.match(source, /'令牌管理' : '我的令牌'/, 'pageTitle 必须按入口在两支之间切换')
  // 用户侧必须点明「只显示你自己的令牌」，否则用户无法判断范围。
  assert.match(source, /只显示你自己的令牌/)
  assert.match(source, /管理全站访问令牌/)
})

// ══════════════════════════════════════════════════════════════
// 行为层：真实挂载
// ══════════════════════════════════════════════════════════════

test('管理员进 my-tokens 看到自己的视图（标题「我的令牌」、不发 scope=all）',
  { skip: behaviourSkip }, async () => {
    const env = prepare()
    // 管理员账号 —— 这是关键：**身份是管理员，但入口是用户侧**。
    env.user.setStoredCurrentUser({ id: 9891, username: 'SYNTH_ADMIN', role: 'admin', status: 'enabled', balance: 1 })

    const Component = require(path.join(webRoot, 'src', 'pages', 'AccessTokenManagement', 'index.vue'))
    const { wrapper, result } = await mountOnRoute(env, Component,
      { path: '/adapter/my-tokens', name: 'my-tokens', meta: { viewMode: 'self' } },
      (w) => {
        const child = w.findComponent(Component)
        assert.ok(child.exists(), '令牌页必须真的渲染出来')
        const state = child.vm.$?.setupState || {}
        return {
          isAdmin: typeof state.isAdmin === 'boolean' ? state.isAdmin : null,
          pageTitle: typeof state.pageTitle === 'string' ? state.pageTitle : null,
          hasShowAllButton: w.html().includes('显示全部令牌'),
        }
      })
    wrapper.unmount()

    // 核心断言：管理员在用户入口下 **不是** 管理员视图。
    assert.equal(result.isAdmin, false,
      '管理器身份 + self 入口 ⇒ 必须按用户视图渲染（这是「我的令牌」的全部意义）')
    assert.equal(result.pageTitle, '我的令牌', '标题必须是「我的令牌」')
    assert.equal(result.hasShowAllButton, false,
      '用户入口不应出现「显示全部令牌」按钮 —— 那个开关等于把全站数据交出去')

    // 请求参数：不能带 scope=all。
    const scoped = env.listCalls.filter(params => params && params.scope === 'all')
    assert.equal(scoped.length, 0,
      `用户入口不得请求全站数据，实际发了 ${scoped.length} 次 scope=all`)
  })

test('管理员进 token-admin 看到管理视图（标题「令牌管理」、有显示全部开关）',
  { skip: behaviourSkip }, async () => {
    const env = prepare()
    env.user.setStoredCurrentUser({ id: 9891, username: 'SYNTH_ADMIN', role: 'admin', status: 'enabled', balance: 1 })

    const Component = require(path.join(webRoot, 'src', 'pages', 'AccessTokenManagement', 'index.vue'))
    const { wrapper, result } = await mountOnRoute(env, Component,
      { path: '/adapter/token-admin', name: 'token-admin', meta: { viewMode: 'admin' } },
      (w) => {
        const child = w.findComponent(Component)
        const state = child.vm.$?.setupState || {}
        return {
          isAdmin: typeof state.isAdmin === 'boolean' ? state.isAdmin : null,
          pageTitle: typeof state.pageTitle === 'string' ? state.pageTitle : null,
          hasShowAllButton: w.html().includes('显示全部令牌'),
        }
      })
    wrapper.unmount()

    assert.equal(result.isAdmin, true, '管理员 + admin 入口 ⇒ 管理视图')
    assert.equal(result.pageTitle, '令牌管理', '标题必须是「令牌管理」')
    assert.equal(result.hasShowAllButton, true,
      '管理入口必须有「显示全部令牌」开关 —— 那是全站视图的入口')
  })

test('普通用户进 my-tokens 也是用户视图（身份与入口一致，最常规路径）',
  { skip: behaviourSkip }, async () => {
    const env = prepare()
    env.user.setStoredCurrentUser({ id: 4242, username: 'SYNTH_USER', role: 'user', status: 'enabled', balance: 1 })

    const Component = require(path.join(webRoot, 'src', 'pages', 'AccessTokenManagement', 'index.vue'))
    const { wrapper, result } = await mountOnRoute(env, Component,
      { path: '/adapter/my-tokens', name: 'my-tokens', meta: { viewMode: 'self' } },
      (w) => {
        const child = w.findComponent(Component)
        const state = child.vm.$?.setupState || {}
        return {
          isAdmin: typeof state.isAdmin === 'boolean' ? state.isAdmin : null,
          pageTitle: typeof state.pageTitle === 'string' ? state.pageTitle : null,
        }
      })
    wrapper.unmount()

    assert.equal(result.isAdmin, false)
    assert.equal(result.pageTitle, '我的令牌')
  })

test('入口切换（token-admin → my-tokens）后实例被复用，且 isAdmin 仍跟随入口而不是角色',
  { skip: behaviourSkip }, async () => {
    const env = prepare()
    env.user.setStoredCurrentUser({ id: 9891, username: 'SYNTH_ADMIN', role: 'admin', status: 'enabled', balance: 1 })

    const Component = require(path.join(webRoot, 'src', 'pages', 'AccessTokenManagement', 'index.vue'))
    const { mount, flushPromises, vue, vueRouter } = env
    const router = vueRouter.createRouter({
      history: vueRouter.createMemoryHistory(),
      routes: [
        { path: '/adapter/my-tokens', name: 'my-tokens', component: Component, meta: { viewMode: 'self' } },
        { path: '/adapter/token-admin', name: 'token-admin', component: Component, meta: { viewMode: 'admin' } },
      ],
    })
    const Host = vue.defineComponent({ render: () => vue.h(vueRouter.RouterView) })
    await router.push('/adapter/token-admin')
    await router.isReady()
    const wrapper = mount(Host, { global: { plugins: [router] }, attachTo: document.body })
    await flushPromises(); await sleep(80); await flushPromises()

    const sample = () => {
      const child = wrapper.findComponent(Component)
      const state = child.vm.$?.setupState || {}
      return { uid: child.vm.$.uid, isAdmin: typeof state.isAdmin === 'boolean' ? state.isAdmin : null, title: typeof state.pageTitle === 'string' ? state.pageTitle : null }
    }
    const before = sample()

    await router.push('/adapter/my-tokens')
    await flushPromises(); await sleep(120); await flushPromises()
    const after = sample()
    wrapper.unmount()

    // 前提：两条路由确实复用同一实例（这正是 mountedPath 判据会失效的原因）。
    assert.equal(before.uid, after.uid,
      '前提不成立：两条令牌路由没有复用实例。若这里失败说明路由结构变了，需重新评估而不是放宽断言')

    assert.equal(before.isAdmin, true, 'token-admin 下应为管理视图')
    assert.equal(after.isAdmin, false, '切到 my-tokens 后必须变成用户视图')
    assert.equal(after.title, '我的令牌',
      '切换入口后标题必须跟着变 —— 若仍是「令牌管理」，说明视图判定被冻结在挂载时（mountedPath 那类缺陷）')
  })

test('管理员在 token-admin 打开「显示全部」后切到 my-tokens，必须复位（不能带着 scope=all）',
  { skip: behaviourSkip }, async () => {
    const env = prepare()
    env.user.setStoredCurrentUser({ id: 9891, username: 'SYNTH_ADMIN', role: 'admin', status: 'enabled', balance: 1 })

    const Component = require(path.join(webRoot, 'src', 'pages', 'AccessTokenManagement', 'index.vue'))
    const { mount, flushPromises, vue, vueRouter } = env
    const router = vueRouter.createRouter({
      history: vueRouter.createMemoryHistory(),
      routes: [
        { path: '/adapter/my-tokens', name: 'my-tokens', component: Component, meta: { viewMode: 'self' } },
        { path: '/adapter/token-admin', name: 'token-admin', component: Component, meta: { viewMode: 'admin' } },
      ],
    })
    const Host = vue.defineComponent({ render: () => vue.h(vueRouter.RouterView) })
    await router.push('/adapter/token-admin')
    await router.isReady()
    const wrapper = mount(Host, { global: { plugins: [router] }, attachTo: document.body })
    await flushPromises(); await sleep(80); await flushPromises()

    const state = () => wrapper.findComponent(Component).vm.$?.setupState || {}
    // 打开「显示全部令牌」——这一步在 token-admin 是合法的。
    state().showAllTokens = true
    await flushPromises(); await sleep(60)
    assert.equal(state().showAllTokens, true, '前置：token-admin 下应能打开显示全部')

    await router.push('/adapter/my-tokens')
    await flushPromises(); await sleep(120); await flushPromises()

    const afterShowAll = state().showAllTokens
    wrapper.unmount()

    // 核心：切到用户入口后必须复位。否则「我的令牌」会带着 scope=all 请求全站数据 ——
    // 正好违反用户诉求「我的令牌只显示当前账号的令牌」。
    assert.equal(afterShowAll, false,
      '切到 my-tokens 后「显示全部」必须复位，否则我的令牌页会请求全站数据')
  })
