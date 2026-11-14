import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

// 拆路由引入的「授权判据用错」回归钉子（2026-11-14 用户报的三个症状中的前两个）。
//
// ══ 缺陷形态（用户看到的）══════════════════════════════════════════
//
//   1. 「我的脚本」↔「脚本管理」切换后，顶部出现
//      「当前身份无法管理脚本，请重新登录。」，所有操作被静默拦掉。
//   2. 「微信绑定历史」↔「手机号绑定历史」切换后，顶部出现
//      「当前身份无绑定历史管理权限，请使用管理员账号登录。」
//      管理员明明已登录，却被判成无权限。
//
// ══ 真根因（实测，不是推断）════════════════════════════════════════
//
// 两条路由**共用同一个组件**（router.js 里 scripts/script-admin 都指向
// ScriptManagement；wechat-bindings/phone-bindings 都指向 BindingHistory，
// 后者是 70 号文档点名要求共用，避免两套文案与两处重复逻辑）。
//
// Vue 在两条共用同一 component 的路由之间切换时**复用同一个实例、不重跑 setup**。
// 本文件的行为用例直接断言 `uid` 不变来证明这件事（实测 3→3、29→29）。
// 于是 setup 期间固化的 `mountedPath = route.path` 变成一次性快照：
// 换到另一条路由后 `route.path === mountedPath` 恒为 false ⇒
// canUse()/canManage() 恒为 false ⇒ 告警 + 全站操作被拦。
//
// 修复：判据从「路径没变」改成「当前路由仍是本页自己的路由之一」
// （OWN_ROUTE_NAMES 白名单 + route.matched 兜底）。
//
// ══ 为什么用真实挂载而不是源码字符串断言 ══════════════════════════
//
// `assert.doesNotMatch(source, /route\.path === mountedPath/)` 只证明那行被删了，
// 不能证明新判据在**两条路由上都成立**。真正要钉的行为是：
//   切换路由后实例被复用（uid 不变）、且授权判据仍为 true、且告警不出现。
// 这只有真实 vue + vue-router 挂载才能观测。所以本文件的行为层用
// tests/helpers/vue-dom.cjs（复用仓库既有 SFC 编译钩子）真挂组件。
//
// 结构层只保留一条最小断言（旧判据不得回到可执行代码里），作为快速诊断。

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const webRoot = path.resolve(__dirname, '..', '..')
const require = createRequire(import.meta.url)

// ── 依赖就绪性自检（永不 skip）──────────────────────────────────────
//
// 若 vue / vue-router / @vue/test-utils / jsdom 解析不到，行为用例会全部失败。
// 这里显式探一次并给出可读原因，而不是让 5 条用例各自抛一句看不懂的 MODULE_NOT_FOUND。
function requireAll(specs) {
  const missing = []
  for (const spec of specs) {
    try { require.resolve(spec) } catch { missing.push(spec) }
  }
  return missing
}
const BEHAVIOUR_DEPS = ['vue', 'vue-router', '@vue/test-utils', 'jsdom', '@vue/compiler-sfc', '@babel/core']
const missingDeps = requireAll(BEHAVIOUR_DEPS)
const behaviourSkip = missingDeps.length
  ? `真实挂载缺少依赖：${missingDeps.join(', ')}（node_modules 未安装完整）`
  : false
if (behaviourSkip) {
  // 不静默 success：把原因打到报告里，并让「本应执行却一条没执行」可见。
  console.error(`[sidebarOwnerRoute] 行为用例将被跳过：${behaviourSkip}`)
}

// ── 环境准备：stub API + 装 jsdom/SFC 钩子 + 合成管理员 ──────────────
function prepare() {
  const Module = require('node:module')
  const stub = (file, exportsObj) => {
    const m = new Module(file)
    m.loaded = true
    m.exports = exportsObj
    require.cache[file] = m
  }
  const never = () => new Promise(() => {})
  // 任何真实网络都不可接受：API 与 request 全部 stub 成永不 resolve。
  for (const name of ['TikuComponentApi', 'UserBindingApi', 'ReviewRebateApi']) {
    stub(path.join(webRoot, 'src', 'api', name + '.js'), new Proxy({}, { get: () => never }))
  }
  stub(path.join(webRoot, 'src', 'utils', 'request.js'), { default: never })

  // helper 装入 jsdom 全局并把 .vue/.js 编译成 CommonJS（仓库既有基建）。
  require(path.join(webRoot, 'tests', 'helpers', 'vue-dom.cjs'))

  const user = require(path.join(webRoot, 'src', 'utils', 'currentUser.js'))
  // 合成管理员：症状 2 的 canManage 需要 isAdminUser 为真。
  user.setStoredCurrentUser({ id: 9891, username: 'SYNTH_ADMIN', role: 'admin', status: 'enabled', balance: 1 })
  return { mount: require('@vue/test-utils').mount, flushPromises: require('@vue/test-utils').flushPromises,
    vue: require('vue'), vueRouter: require('vue-router') }
}

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))

// mountAcrossRoutes：在 from 处挂载，再 push 到 to，分别采样。
//
// 采样函数拿到 wrapper 与 router，返回本用例关心的可观测事实。
async function mountAcrossRoutes(env, Component, routes, from, to, sample) {
  const { mount, flushPromises, vue, vueRouter } = env
  const router = vueRouter.createRouter({
    history: vueRouter.createMemoryHistory(),
    routes: routes.map(route => ({ ...route, component: Component })),
  })
  const Host = vue.defineComponent({ render: () => vue.h(vueRouter.RouterView) })
  await router.push(from)
  await router.isReady()
  const wrapper = mount(Host, { global: { plugins: [router] }, attachTo: document.body })
  await flushPromises(); await sleep(80); await flushPromises()
  const before = sample(wrapper, router, Component)
  await router.push(to)
  await flushPromises(); await sleep(120); await flushPromises()
  const after = sample(wrapper, router, Component)
  wrapper.unmount()
  return { before, after }
}

// 只取可执行代码行做「旧判据是否还在」的断言。
//
// 不能对全文 doesNotMatch：修复代码的**注释**恰恰要写清
// 「route.path === mountedPath 为什么不能用」——那是给后来人看的说明。
// 把注释当违规命中，会逼着大家删掉最有价值的注释，或写成看不懂的绕口令。
function stripComments(source) {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/<!--[\s\S]*?-->/g, '')
    .split('\n')
    .map(line => {
      const at = line.search(/(^|\s)\/\//)
      if (at < 0) return line
      const before = line.slice(0, at)
      // 引号内不截断（这些文件里的 URL 形如 'https://…'）。
      return (before.match(/['"`]/g) || []).length % 2 === 1 ? line : before
    })
    .join('\n')
}

const readPage = (...segments) => readFileSync(path.join(webRoot, 'src', 'pages', ...segments), 'utf8')

const SCRIPT_ROUTES = [
  { path: '/adapter/scripts', name: 'scripts', meta: { viewMode: 'self' } },
  { path: '/adapter/script-admin', name: 'script-admin', meta: { viewMode: 'admin' } },
]
const BINDING_ROUTES = [
  { path: '/adapter/wechat-bindings', name: 'wechat-bindings', meta: { bindingKind: 'wechat' } },
  { path: '/adapter/phone-bindings', name: 'phone-bindings', meta: { bindingKind: 'phone' } },
]

// ══════════════════════════════════════════════════════════════
// 结构层（快速诊断；真正的判据在下面的行为层）
// ══════════════════════════════════════════════════════════════

test('脚本管理：可执行代码里不再出现 route.path === mountedPath', () => {
  const source = readPage('ScriptManagement', 'index.vue')
  const code = stripComments(source)
  assert.doesNotMatch(code, /route\.path\s*===\s*mountedPath/,
    'canUse 不得用「路径没变」当授权判据 —— 共用组件的两条路由切换后路径必然变')
  assert.doesNotMatch(code, /mountedPath/,
    'mountedPath 一次性快照不得保留（残留就会被后来人再次拿去比较）')
  assert.match(code, /const canUse = \(\) => alive && stillOwnRoute\(\)/,
    'canUse 必须使用 stillOwnRoute()')
  // 注释必须留下拆路由的坑，否则后来人会再踩。
  assert.match(source, /为什么不能用 route\.path === mountedPath 做授权判据/,
    '必须留下拆路由引入的坑的说明')
})

test('绑定历史：可执行代码里不再出现 route.path === mountedPath', () => {
  const source = readPage('BindingHistory', 'index.vue')
  const code = stripComments(source)
  assert.doesNotMatch(code, /route\.path\s*===\s*mountedPath/,
    'canManage 不得用「路径没变」当授权判据')
  assert.doesNotMatch(code, /mountedPath/)
  assert.match(code, /const canManage = \(\) => alive && stillOwnRoute\(\)/,
    'canManage 必须使用 stillOwnRoute()')
  assert.match(source, /为什么不能用 route\.path === mountedPath 做授权判据/)
})

// ══════════════════════════════════════════════════════════════
// 行为层：真实 vue + vue-router 挂载
// ══════════════════════════════════════════════════════════════

test('脚本管理：scripts → script-admin 切换后实例被复用，授权判据仍为 true 且无告警',
  { skip: behaviourSkip }, async () => {
    const env = prepare()
    const Component = require(path.join(webRoot, 'src', 'pages', 'ScriptManagement', 'index.vue'))
    const { before, after } = await mountAcrossRoutes(env, Component, SCRIPT_ROUTES,
      '/adapter/scripts', '/adapter/script-admin', (wrapper, router) => {
        const child = wrapper.findComponent(Component)
        assert.ok(child.exists(), 'SiderMenu 之外：脚本页必须真的渲染出来')
        const state = child.vm.$?.setupState || {}
        assert.equal(typeof state.canUse, 'function', 'setup 必须暴露 canUse（本用例的被测对象）')
        return {
          uid: child.vm.$.uid,
          routeName: String(router.currentRoute.value.name),
          canUse: state.canUse(),
          alertShown: wrapper.html().includes('当前身份无法管理脚本'),
        }
      })

    // 前提：两条路由确实是同一个组件实例（这正是缺陷的成因，也是本用例的意义所在）。
    assert.equal(before.uid, after.uid,
      '前提不成立：两条路由没有复用实例。若这里失败，说明路由结构变了，本用例需重新评估而非直接放宽')
    assert.equal(before.routeName, 'scripts')
    assert.equal(after.routeName, 'script-admin')

    // 核心断言：换到另一条路由后，授权判据必须仍然为 true。
    assert.equal(before.canUse, true, 'scripts 入口必须可操作')
    assert.equal(after.canUse, true,
      'script-admin 入口必须可操作 —— 实例复用后仍要判定为本页，否则「当前身份无法管理脚本」会误报')
    // 用户可见结果：告警不得出现。
    assert.equal(before.alertShown, false)
    assert.equal(after.alertShown, false,
      '不得显示「当前身份无法管理脚本」—— 管理员/用户身份都没问题，是判据错了')
  })

test('绑定历史：wechat-bindings → phone-bindings 切换后实例被复用，canManage 仍为 true 且无告警',
  { skip: behaviourSkip }, async () => {
    const env = prepare()
    const Component = require(path.join(webRoot, 'src', 'pages', 'BindingHistory', 'index.vue'))
    const { before, after } = await mountAcrossRoutes(env, Component, BINDING_ROUTES,
      '/adapter/wechat-bindings', '/adapter/phone-bindings', (wrapper, router) => {
        const child = wrapper.findComponent(Component)
        assert.ok(child.exists(), '绑定历史页必须真的渲染出来')
        const state = child.vm.$?.setupState || {}
        assert.equal(typeof state.canManage, 'function', 'setup 必须暴露 canManage（本用例的被测对象）')
        return {
          uid: child.vm.$.uid,
          routeName: String(router.currentRoute.value.name),
          canManage: state.canManage(),
          alertShown: wrapper.html().includes('当前身份无绑定历史管理权限'),
        }
      })

    assert.equal(before.uid, after.uid,
      '前提不成立：两条绑定路由没有复用实例（路由结构变了）')
    assert.equal(before.routeName, 'wechat-bindings')
    assert.equal(after.routeName, 'phone-bindings')

    assert.equal(before.canManage, true, '微信绑定历史必须可管理')
    assert.equal(after.canManage, true,
      '手机号绑定历史必须可管理 —— 实例复用后仍要判定为本页，否则管理员被误判成无权限')
    assert.equal(before.alertShown, false)
    assert.equal(after.alertShown, false,
      '不得显示「当前身份无绑定历史管理权限」')
  })

test('绑定历史：非管理员确实看不到管理内容（修复不能把权限判定放开）',
  { skip: behaviourSkip }, async () => {
    const env = prepare()
    const user = require(path.join(webRoot, 'src', 'utils', 'currentUser.js'))
    // 换成普通用户：canManage 必须为 false。这是修复的安全边界 ——
    // 本缺陷修的是「本页路由身份」，绝不能顺手把角色校验也绕过。
    user.setStoredCurrentUser({ id: 4242, username: 'SYNTH_USER', role: 'user', status: 'enabled', balance: 1 })
    const Component = require(path.join(webRoot, 'src', 'pages', 'BindingHistory', 'index.vue'))
    const { after } = await mountAcrossRoutes(env, Component, BINDING_ROUTES,
      '/adapter/wechat-bindings', '/adapter/phone-bindings', (wrapper, router) => {
        const child = wrapper.findComponent(Component)
        const state = child.vm.$?.setupState || {}
        return {
          routeName: String(router.currentRoute.value.name),
          canManage: typeof state.canManage === 'function' ? state.canManage() : null,
          alertShown: wrapper.html().includes('当前身份无绑定历史管理权限'),
        }
      })
    assert.equal(after.routeName, 'phone-bindings')
    assert.equal(after.canManage, false,
      '普通用户即使在本页路由上也不得获得管理权限（角色校验必须仍然生效）')
    assert.equal(after.alertShown, true,
      '普通用户看到权限告警是正确的 —— 这条与「管理员误报」是相反方向的对照')
  })
