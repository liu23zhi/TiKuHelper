/* 「评价返现申请入口在关闭态下仍然可见」的回归钉子（2026-11-15 用户实测报出）。
 *
 * ══ 缺陷形态（用户看到的）══════════════════════════════════════════════════
 *
 * 进「评价返现」页 → 只有一行灰字「当前站点没有开放评价返现」→ **没有任何申请按钮**。
 * 用户的原话就是这个：「没有申请按钮」。
 *
 * ══ 真根因（实测，不是推断）══════════════════════════════════════════════════
 *
 * `pages/ReviewRebate/index.vue` 原实现：
 *   - 关闭态（`loaded && !enabled`）只渲染一条 `<a-alert>`；
 *   - 规则表 / 汇总 / **提交表单**全部包在 `<template v-if="loaded && enabled">` 里。
 * ⇒ 关闭时整块消失。后果是「未开放」与「这站点没这个功能」在界面上**完全同形**，
 *   而两者的下一步动作相反（等开放 vs 去要求开发）。入口必须可见，哪怕不可用。
 *
 * ══ 为什么必须真实挂载，不能只断言源码字符串 ═══════════════════════════════════
 *
 * 既有的 `reviewRebateContract.test.mjs` 是源码正则断言（`assert.match(content, /未启用/)`）。
 * 它能证明「某个字符串还在文件里」，但**证明不了关闭态下按钮真的存在** —— 把
 * `v-if="loaded && enabled"` 加回包裹层，字符串断言照样全绿。这正是「跑了但没测到」。
 *
 * 所以本文件用 tests/helpers/vue-dom.cjs 真挂 SFC，按 DOM 断言行为。
 *
 * ⚠️ 为什么放在 src/utils/（而不是 tests/components/）：`npm run test:unit` 的采集范围是
 * `node --test src/utils/*.test.mjs`，`tests/components/*` 只有 `test:components` 才跑，
 * 而后者**基线就是红的**（多条与本改动无关的用例 8-10 秒超时），不能作为本任务的判据。
 * 把钉子放进没人跑的范围 = 假钉子。`.mjs` 里用 createRequire 只是为了拿到 CJS 的
 * 编译钩子（tests/helpers/vue-dom.cjs 是 CJS），断言逻辑本身仍是 ESM。
 */
import test from 'node:test'
import assert from 'node:assert/strict'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
// src/utils/ → web/
const webRoot = path.resolve(__dirname, '..', '..')
const require = createRequire(import.meta.url)

// ── 依赖就绪性自检（永不 skip 而不出声）──────────────────────────────────
const DEPENDENCIES = ['vue', 'vue-router', '@vue/test-utils', 'jsdom', '@vue/compiler-sfc', '@babel/core', 'ant-design-vue']
const missingDeps = DEPENDENCIES.filter((spec) => {
  try { require.resolve(spec, { paths: [webRoot] }); return false } catch { return true }
})
if (missingDeps.length) {
  // 不静默 success：把原因打到报告里，让「本应执行却一条没执行」可见。
  console.error(`[reviewRebateClosedEntry] 真实挂载依赖缺失：${missingDeps.join(', ')}`)
}
const behaviourSkip = missingDeps.length ? `缺少真实挂载依赖：${missingDeps.join(', ')}` : false

// ── 环境准备 ─────────────────────────────────────────────────────────────
//
// 只 prepare 一次：require.extensions 是全局钩子，重复装会把 Babel 编译层叠起来
// （第二批 require 的 .vue 会拿到上一批的 module.exports 缓存，导致两个用例互相污染）。
let env = null
function prepare() {
  if (env) return env
  const Module = require('node:module')
  const stub = (file, exportsObj) => {
    const m = new Module(file, undefined)
    m.filename = file
    m.loaded = true
    m.exports = exportsObj
    require.cache[file] = m
  }

  // 活动数据用可切换的桩：本文件要分别模拟「未开始」与「已结束」两种关闭态。
  // 真实网络不可接受（用例会依赖外部服务，失败原因不可归因）。
  const state = { programResponse: null }
  stub(path.join(webRoot, 'src', 'api', 'ReviewRebateApi.js'), {
    getReviewRebateProgram: () => Promise.resolve(state.programResponse),
    getMyReviewRebateClaims: () => Promise.resolve({ status: 200, data: { data: [], total: 0, page: 1, pageSize: 20 } }),
    submitReviewRebateClaim: () => Promise.resolve({ status: 201 }),
  })
  stub(path.join(webRoot, 'src', 'utils', 'request.js'), { default: () => new Promise(() => {}) })

  require(path.join(webRoot, 'tests', 'helpers', 'vue-dom.cjs'))

  const user = require(path.join(webRoot, 'src', 'utils', 'currentUser.js'))
  env = {
    state,
    user,
    mount: require('@vue/test-utils').mount,
    flushPromises: require('@vue/test-utils').flushPromises,
    vueRouter: require('vue-router'),
    Antd: require('ant-design-vue'),
    Component: require(path.join(webRoot, 'src', 'pages', 'ReviewRebate', 'index.vue')),
  }
  return env
}

const settle = async (flushPromises) => {
  await flushPromises()
  await new Promise((resolve) => setTimeout(resolve, 30))
  await flushPromises()
}

/** 挂载页面并等到「活动信息已加载」（loaded 为真）为止。 */
async function mountPage(environment, programResponse) {
  environment.user.setStoredCurrentUser({
    id: 7788, username: 'SYNTH_PAGE_USER', role: 'user', status: 'enabled', balance: 1,
  })
  environment.state.programResponse = programResponse
  const router = environment.vueRouter.createRouter({
    history: environment.vueRouter.createMemoryHistory(),
    routes: [{ path: '/review-rebate', name: 'review-rebate', component: environment.Component }],
  })
  await router.push('/review-rebate')
  await router.isReady()
  const wrapper = environment.mount(environment.Component, {
    global: { plugins: [router, environment.Antd] },
    attachTo: document.body,
  })
  const deadline = Date.now() + 8000
  while (Date.now() < deadline) {
    const setup = wrapper.vm?.$?.setupState || {}
    if (setup.loaded) break
    await settle(environment.flushPromises)
  }
  const setup = wrapper.vm?.$?.setupState || {}
  assert.equal(setup.loaded, true,
    '前置失效：活动信息没有加载完成，后续断言无从谈起（页面 loadAll 未执行或桩未命中）')
  return { wrapper, setup, router }
}

function teardown(context, environment) {
  try { context.wrapper.unmount() } catch { /* 已卸载 */ }
  try { context.router?.options?.history?.destroy?.() } catch { /* 无 history */ }
  environment.user.clearStoredCurrentUser()
  document.body.replaceChildren()
}

/** 关闭态的两种后端响应。口径见页面里 closedReason 的注释。 */
const RESPONSE_NOT_STARTED = {
  // 总开关关闭：后端在 GetReviewRebateEnabledWithDB 就早退，返回 400「评价返现未启用」。
  status: 400,
  message: '评价返现未启用',
}
const RESPONSE_ENDED = {
  // 活动行存在但 enabled=false：后端能读出活动（含 name/storeUrl），只是入口拒绝。
  // ⚠️ 后端**没有** start_at/end_at 字段（internal/entity/review_rebate.go 无此列），
  // 所以「已结束」只能靠「活动行可读」判定。页面注释写明了这一点。
  status: 200,
  data: {
    program: {
      id: 12, enabled: false, name: '2026 春季评价返现', storeUrl: 'https://example.com/store',
      maxClaimsPerUser: 3, maxAmountPerUser: 5.04,
    },
    rules: [{
      id: 1, minStars: 5, maxStars: 5, minContentScore: 10, maxContentScore: 10,
      amount: 1, priority: 10, remark: '满星满分',
    }],
    usage: {},
  },
}
const RESPONSE_OPEN = {
  status: 200,
  data: {
    program: {
      id: 12, enabled: true, name: '2026 春季评价返现', storeUrl: 'https://example.com/store',
      maxClaimsPerUser: 3, maxAmountPerUser: 5.04, minAgentConfidence: 0.6,
    },
    rules: [{
      id: 1, minStars: 5, maxStars: 5, minContentScore: 10, maxContentScore: 10,
      amount: 1, priority: 10, remark: '满星满分',
    }],
    usage: { approvedCount: 0, approvedAmount: 0, rejectedCount: 0 },
  },
}

/** 取提交入口：以「提交审核」按钮为准 —— 那才是用户找的东西。 */
function submitButtonAppears(wrapper) {
  return wrapper.findAll('button').some((node) => buttonText(node) === '提交审核')
}

/**
 * 按钮文案归一化。
 *
 * ⚠️ antd 会给「两个汉字的按钮」自动插入空格（实际渲染是「清 空」「刷 新」），
 * 所以按 textContent 精确比较前必须去掉所有空白。不归一化会让
 * `find(node => node.text().trim() === '清空')` 恒为 undefined ——
 * 那不是「按钮不存在」，是选择器写错了，会伪装成失败断言。
 */
function buttonText(node) {
  return String(node.text() || '').replace(/\s+/g, '')
}

// ══════════════════════════════════════════════════════════════════════════
// ① 总开关关闭时，提交入口**仍然存在**（本轮缺陷的核心判据）
// ══════════════════════════════════════════════════════════════════════════
test('① 总开关关闭时提交入口仍存在，但处于禁用态', { skip: behaviourSkip }, async () => {
  const environment = prepare()
  const context = await mountPage(environment, RESPONSE_NOT_STARTED)
  try {
    assert.equal(submitButtonAppears(context.wrapper), true,
      '关闭态下必须仍有「提交审核」按钮 —— 整块隐藏会让「未开放」与「功能不存在」无法区分')

    const button = context.wrapper.findAll('button').find((node) => buttonText(node) === '提交审核')
    assert.notEqual(button.attributes('disabled'), undefined,
      '关闭态下提交按钮必须是 disabled（可见但不可用），不能是「能点但点了报错」')

    const picker = context.wrapper.findAll('button').find((node) => buttonText(node) === '选择截图')
    assert.ok(picker, '关闭态下截图选择按钮必须仍存在')
    assert.equal(picker.element.disabled, true, '关闭态下截图选择按钮必须禁用')

    // entryDisabled 必须是「关闭」这个原因驱动的，不能靠 submitting/limitReached 巧合成立。
    assert.equal(context.setup.enabled, false, '前置失效：本用例要求关闭态')
    assert.equal(context.setup.entryDisabled, true, '关闭态下 entryDisabled 必须为 true')
  } finally {
    teardown(context, environment)
  }
})

// ══════════════════════════════════════════════════════════════════════════
// ② 关闭态必须有明确原因文案
// ══════════════════════════════════════════════════════════════════════════
test('② 关闭态给出明确原因文案，且说明「开放后可在此提交」', { skip: behaviourSkip }, async () => {
  const environment = prepare()
  const context = await mountPage(environment, RESPONSE_NOT_STARTED)
  try {
    const text = context.wrapper.text()
    // 原因必须说清「未开放」这件事，而不是只留一句空白。
    assert.match(text, /未开放|尚未开始/, '关闭态必须明确写出「未开放 / 尚未开始」')
    // 必须告诉用户入口就在这里，否则「看不到按钮」的困惑依旧。
    assert.match(text, /开放后/, '关闭态必须说明「开放后可在此提交」，否则用户仍不知道去哪申请')

    // 就地提示：面板内部也要有一条（data-entry-closed-notice），
    // 用户滚到入口处不必回顶部找原因。
    assert.equal(context.wrapper.find('[data-entry-closed-notice]').exists(), true,
      '关闭态必须在提交入口面板内部就地显示原因')
  } finally {
    teardown(context, environment)
  }
})

// ══════════════════════════════════════════════════════════════════════════
// ③ 总开关开启时行为不变
// ══════════════════════════════════════════════════════════════════════════
test('③ 总开关开启时入口可用，且不显示关闭原因', { skip: behaviourSkip }, async () => {
  const environment = prepare()
  const context = await mountPage(environment, RESPONSE_OPEN)
  try {
    assert.equal(context.setup.enabled, true, '前置失效：开启态没有落成 enabled=true')

    assert.equal(submitButtonAppears(context.wrapper), true, '开启态必须有「提交审核」按钮')

    // 开启态下禁用只能来自「未选文件 / 提交中 / 达上限」，不能来自 enabled。
    assert.equal(context.setup.entryDisabled, false,
      '开启态下 entryDisabled 必须为 false（禁用只能来自未选文件/提交中/达上限）')

    // 关闭原因不得出现在开启态（否则是文案泄漏，用户会以为又没开放）。
    assert.doesNotMatch(context.wrapper.text(), /尚未开放|活动已结束/,
      '开启态不得出现关闭原因文案')

    // 「开放了才有意义」的面板仍在。
    assert.match(context.wrapper.text(), /活动说明/, '开启态必须仍显示活动说明')
    assert.match(context.wrapper.text(), /我的提交/, '开启态必须仍显示「我的提交」面板')
  } finally {
    teardown(context, environment)
  }
})

// ══════════════════════════════════════════════════════════════════════════
// ③-b 开启态的另一条分支：达到上限时的按钮状态必须与原实现一致
//
// 这一条防的是「重构时顺手把 limitReached 塞进所有 disabled」——
// 改关闭态时最容易被改坏的就是开启态的既有行为，而它不在上面那条用例的视野里。
// ══════════════════════════════════════════════════════════════════════════
test('③-b 开启且达上限时：「提交审核」禁用，但「清空」仍可用（原行为不变）', { skip: behaviourSkip }, async () => {
  const environment = prepare()
  const reached = {
    status: 200,
    data: {
      program: {
        id: 12, enabled: true, name: '2026 春季评价返现', storeUrl: 'https://example.com/store',
        maxClaimsPerUser: 1, maxAmountPerUser: 5.04, minAgentConfidence: 0.6,
      },
      rules: [{
        id: 1, minStars: 5, maxStars: 5, minContentScore: 10, maxContentScore: 10,
        amount: 1, priority: 10, remark: '满星满分',
      }],
      // 已领 1 次 / 满额 ⇒ countLimited 命中
      usage: { approvedCount: 1, approvedAmount: 5.04, rejectedCount: 0 },
    },
  }
  const context = await mountPage(environment, reached)
  try {
    assert.equal(context.setup.enabled, true, '前置失效：本用例要求开启态')
    assert.equal(context.setup.limitReached, true, '前置失效：本用例要求达上限')

    const buttons = context.wrapper.findAll('button')
    const submit = buttons.find((node) => buttonText(node) === '提交审核')
    const reset = buttons.find((node) => buttonText(node) === '清空')
    assert.ok(submit, '开启态必须有「提交审核」按钮')
    assert.ok(reset, '开启态必须有「清空」按钮')

    assert.notEqual(submit.attributes('disabled'), undefined,
      '达上限时「提交审核」必须禁用')
    assert.equal(reset.attributes('disabled'), undefined,
      '达上限时「清空」必须仍可用 —— 原实现只按 submitting 禁用；'
      + '把它改成也受 limitReached 影响，就是「关闭态的修法顺手改了开启态的行为」')

    // 达上限的文案在开启态必须显示（关闭态才有理由隐藏它）。
    assert.match(context.wrapper.text(), /领取上限/, '开启态达上限必须显示上限提示')
  } finally {
    teardown(context, environment)
  }
})

// ══════════════════════════════════════════════════════════════════════════
// ④ 「未开始」与「已结束」的文案必须区分
// ══════════════════════════════════════════════════════════════════════════
test('④ 关闭态说明未开放或已暂停，不虚构活动起止时间', { skip: behaviourSkip }, async () => {
  const environment = prepare()

  const notStarted = await mountPage(environment, RESPONSE_NOT_STARTED)
  const notStartedText = notStarted.wrapper.text()
  const notStartedReason = String(notStarted.setup.closedReason || '')
  const notStartedHasEntry = submitButtonAppears(notStarted.wrapper)
  teardown(notStarted, environment)

  const ended = await mountPage(environment, RESPONSE_ENDED)
  const endedText = ended.wrapper.text()
  const endedReason = String(ended.setup.closedReason || '')
  const endedHasEntry = submitButtonAppears(ended.wrapper)
  teardown(ended, environment)

  // 两种都是「关闭」，但原因相反：一个还没上线，一个办过就停了。
  assert.notEqual(notStartedReason, endedReason,
    '「未开始」与「已结束」必须给出不同文案 —— 用户下一步动作相反（等开放 vs 问是不是被停权）')
  assert.match(notStartedReason, /未开放|尚未开始/, '未开始的原因文案必须说明「还没开始」')
  assert.match(endedReason, /已暂停/, '禁用活动只能说明暂停，不能推断已结束')
  assert.doesNotMatch(notStartedReason + endedReason, /尚未开始|已结束/)

  // 修复不能在两种关闭态之一失效。
  assert.equal(notStartedHasEntry, true, '未开始态下入口必须存在')
  assert.equal(endedHasEntry, true, '已结束态下入口也必须存在')

  // 文案必须真的渲染到页面，不能只存在于 setup 状态里。
  assert.match(notStartedText, /未开放|尚未开始/, '未开始的文案必须渲染到页面上')
  assert.match(endedText, /已暂停/, '暂停文案必须渲染到页面上')
})
