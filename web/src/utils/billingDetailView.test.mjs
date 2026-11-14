import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import * as Vue from 'vue'
import * as billingHelpers from './userPortalHelpers.mjs'
import { useTableExpansion } from '../composables/useTableExpansion.mjs'
import { useTableColumnPreferences } from '../composables/useTableColumnPreferences.mjs'
import { sumTableColumnWidths } from './tableLayout.mjs'
import { buildSearchParams, countActiveFilters } from './listSearchHelpers.mjs'
import { ensureRequestFailure } from './requestFailureView.mjs'

const require = createRequire(import.meta.url)
const { parse, compileScript, compileTemplate } = require('@vue/compiler-sfc')
require('../../tests/helpers/vue-dom.cjs')
const { useCurrentUserRequestScope } = require('./currentUserScope.js')
const { getCurrentUserLogoutEpoch } = require('./currentUser.js')
const RequestFailureAlert = require('../components/RequestFailureAlert.vue')
const detailPath = new URL('../components/BillingDetailDialog.vue', import.meta.url)
const adminPath = new URL('../pages/BillingRecordList/index.vue', import.meta.url)

const sample = {
  id: 41,
  type: 'normal_search',
  direction: 'expense',
  subject: '搜索答案，题目：完整题目',
  searchMode: 'deep',
  effectiveMultiplier: '2.06x',
  amount: 0.054963,
  showInStatement: true,
  feeDetails: [
    { provider: 'ai', category: 'AI模型调用', callIndex: 1, amount: 0.025052, cacheHitRate: 0.1, detail: 'model=gpt-6.1-sol;finalMultiplier=2.06;totalTokens=5949' },
    { provider: 'ai_search', amount: 0.005, detail: '关键词：社交媒体 隐私 <script>' },
    { provider: 'ai', category: 'AI模型调用', callIndex: 2, amount: 0.024911, cacheHitRate: 0.3333333, detail: 'model=gpt-6.1-sol;finalMultiplier=1.73;totalTokens=7083' },
    { provider: 'web_read', amount: 0, url: 'https://example.invalid/a?x=1&y=two', detail: '网页：旧 URL' },
  ],
}

test('billing view model defaults to personal scope and keeps keyword, URL and amounts', () => {
  const view = billingHelpers.buildBillingDetailViewModel(sample, { canEditStatement: true })
  assert.equal(view.canEditStatement, false)
  assert.equal(view.subject, sample.subject)
  assert.equal(view.items[1].title, '联网搜索')
  assert.deepEqual(view.items[1].metadata, [{ label: '关键词', value: '社交媒体 隐私 <script>', keyword: true }])
  assert.deepEqual(view.items[3].metadata, [{ label: '网页', value: 'https://example.invalid/a?x=1&y=two' }])
  assert.equal(view.items[0].amount, '-¥ 0.025052')
  assert.equal(view.amount, '-¥ 0.054963')
  assert.doesNotMatch(JSON.stringify(view), /gpt-6|2\.06|1\.73|5,949|7,083|finalMultiplier|effectiveMultiplier|Token数量|缓存命中率/)
  assert.equal(new Set(view.items.map(item => item.key)).size, view.items.length)
})

test('billing detail view model includes local Tiku search query and candidate count', () => {
  const view = billingHelpers.buildBillingDetailViewModel({
    direction: 'expense',
    amount: 0.01,
    feeDetails: [{
      provider: 'tiku_search',
      amount: 0.01,
      detail: 'query=微积分题；candidate_count=4',
    }],
  })

  assert.equal(view.items[0].title, '本地题库检索')
  assert.deepEqual(view.items[0].metadata, [
    { label: '检索词', value: '微积分题' },
    { label: '候选数', value: '4' },
  ])
})

test('administrator detail retains each original round and requires explicit statement edit capability', () => {
  const view = billingHelpers.buildBillingDetailViewModel(sample, { viewerScope: 'admin', canEditStatement: true })
  assert.equal(view.canEditStatement, true)
  assert.equal(view.items[0].title, 'AI 模型调用 第1次')
  assert.equal(view.items[2].title, 'AI 模型调用 第2次')
  assert.deepEqual(view.items[0].metadata, [
    { label: '模型', value: 'gpt-6.1-sol' },
    { label: '最终倍率', value: '2.06x' },
    { label: 'Token数量', value: '5,949' },
    { label: '缓存命中率', value: '10%' },
  ])
  assert.deepEqual(view.items[2].metadata.slice(1), [
    { label: '最终倍率', value: '1.73x' },
    { label: 'Token数量', value: '7,083' },
    { label: '缓存命中率', value: '33.33%' },
  ])
  assert.equal(billingHelpers.buildBillingDetailViewModel(sample, { viewerScope: 'admin' }).canEditStatement, false)
  assert.equal(sample.feeDetails[0].detail, 'model=gpt-6.1-sol;finalMultiplier=2.06;totalTokens=5949')
})

test('administrator model calls distinguish zero cache usage from historical missing input splits', () => {
  const record = { ...sample, feeDetails: [
    { ...sample.feeDetails[0], cacheHitRate: 0 },
    { ...sample.feeDetails[2], cacheHitRate: null },
    { ...sample.feeDetails[0], cacheHitRate: undefined },
  ] }
  const admin = billingHelpers.buildBillingDetailViewModel(record, { viewerScope: 'admin' })
  assert.deepEqual(admin.items.map(item => item.metadata.at(-1)), [
    { label: '缓存命中率', value: '0%' },
    { label: '缓存命中率', value: '-' },
    { label: '缓存命中率', value: '-' },
  ])
  const self = billingHelpers.buildBillingDetailViewModel(record, { viewerScope: 'self' })
  assert.deepEqual(self.items.map(item => item.metadata), [[], [], []])
  assert.doesNotMatch(billingHelpers.buildBillingRecordFeeDetailHtml(record, { showAIModelMeta: false }), /缓存命中率/)
})

test('detail dialog uses escaped Vue text, defaults to self, and only emits edits for the current ID', async () => {
  const source = await readFile(detailPath, 'utf8')
  const script = parse(source).descriptor.script.content
    .replace(/import[^\n]+\n/g, '')
    .replace('export default', 'return')
  const component = new Function('computed', 'defineComponent', 'ref', 'watch', 'onBeforeUnmount', 'document', 'buildBillingDetailViewModel', 'RequestFailureAlert', script)(
    Vue.computed, Vue.defineComponent, Vue.ref, Vue.watch, Vue.onBeforeUnmount, document, billingHelpers.buildBillingDetailViewModel, RequestFailureAlert,
  )
  assert.equal(component.props.viewerScope.default, 'self')
  assert.equal(component.props.canEditStatement.default, false)
  assert.doesNotMatch(source, /v-html|innerHTML|insertAdjacentHTML|document\.write|addEventListener/, 'detail content remains escaped Vue text; focus ownership may inspect document')
  assert.match(source, /:focus-trigger-after-close="false"/)
  assert.match(source, /onBeforeUnmount\(\(\) => \{ alive = false;/)
  const props = Vue.reactive({ open: true, record: sample, viewerScope: 'self', canEditStatement: true, statementSaving: false })
  const events = []
  const state = component.setup(props, { emit: (...event) => events.push(event) })
  state.requestStatementChange(false)
  assert.deepEqual(events, [])
  props.viewerScope = 'admin'
  props.statementSaving = true
  state.requestStatementChange(false)
  assert.deepEqual(events, [])
  props.statementSaving = false
  props.open = false
  state.requestStatementChange(false)
  assert.deepEqual(events, [])
  props.open = true
  props.record = { ...sample, id: 42 }
  state.requestStatementChange(false)
  assert.deepEqual(events, [['statement-visibility-change', { id: 42, showInStatement: false }]])
})

async function loadBillingPage(role = 'admin', save = async () => ({ status: 200 }), load = async () => ({ status: 200, data: {} }), viewMode = '') {
  const source = await readFile(adminPath, 'utf8')
  const script = parse(source).descriptor.script.content
    .replace(/import[\s\S]*?from ['"][^'"]+['"];?\r?\n/g, '')
    .replace('export default', 'return')
  const dependencies = {
    computed: Vue.computed,
    defineComponent: Vue.defineComponent,
    reactive: Vue.reactive,
    ref: Vue.ref,
    shallowRef: Vue.shallowRef,
    markRaw: Vue.markRaw,
    useId: Vue.useId,
    watch: Vue.watch,
    getCurrentUserLogoutEpoch,
    ...billingHelpers,
    useTableExpansion,
    // Exercise the real computed preferences; isolate only optional browser persistence.
    useTableColumnPreferences: options => useTableColumnPreferences({ ...options, storage: null }),
    sumTableColumnWidths,
    RequestFailureAlert,
    TableColumnSettings: {},
    OverflowTableText: {},
    BillingDetailDialog: {},
    getStoredCurrentUser: () => ({ id: 1, role }),
    useCurrentUserDisplay: () => ({ currentUser: Vue.ref({ id: 1, role }) }),
    useCurrentUserRequestScope,
    getCurrentInstance: () => ({ proxy: { handleLoad() {} } }),
    isAdminUser: user => user.role === 'admin',
    // 视图模式由**入口**决定（2026-11-14 拆路由）：见 BillingRecordList 里 routeViewMode。
    useRoute: () => ({ meta: viewMode ? { viewMode } : {} }),
    normalizeLocationKey: value => String(value || '').trim(),
    formatValueWithLocationBase: value => value,
    formatMoneyValue: value => String(value),
    buildSearchParams,
    countActiveFilters,
    ensureRequestFailure,
    normalizeSearchKeyword: value => String(value || '').trim(),
    getBillingRecords: load,
    collectLocationKeys: () => [],
    updateBillingStatementVisibility: save,
    message: { success() {}, error() {} },
  }
  const component = new Function(...Object.keys(dependencies), script)(...Object.values(dependencies))
  const scope = Vue.effectScope()
  const state = scope.run(() => component.setup())
  const context = new Proxy(state, {
    get: (target, key) => Vue.unref(target[key]),
    set(target, key, value) {
      if (Vue.isRef(target[key])) target[key].value = value
      else target[key] = value
      return true
    },
  })
  return { component, state, context, close: () => scope.stop() }
}

test('billing columns keep admin metadata separate and expansion adjusts the whole project column', async () => {
  for (const role of ['admin', 'user']) {
    // 视图由**入口**决定（2026-11-14 拆路由）：管理端账户走 billing-admin（viewMode='admin'），
    // 用户端走 billing-records（viewMode='self'）。按入口而不是按角色加载。
    const page = await loadBillingPage(role, undefined, undefined, role === 'admin' ? 'admin' : 'self')
    try {
      const keys = page.state.columns.value.map(column => column.key)
      assert.deepEqual(page.state.visibleColumns.value.map(column => column.key), keys, 'default visible columns preserve every role-allowed source field in order')
      assert.equal(keys.includes('effectiveMultiplier'), role === 'admin')
      assert.equal(keys.includes('username'), role === 'admin')
      assert.equal(keys.includes('clientIp'), role === 'admin')
      assert.equal(keys.includes('cacheHitRate'), true)
      assert.equal(page.state.columns.value.find(column => column.key === 'source').width, 160)
      page.state.dataSource.value = [{ id: 1 }, { id: 2 }]
      const collapsedWidth = page.state.tableScrollX.value
      page.state.toggleSubject(1)
      assert.equal(page.state.columns.value.find(column => column.key === 'subject').width, 236)
      assert.equal(page.state.tableScrollX.value, collapsedWidth + 66)
      page.state.toggleSubject(1)
      assert.equal(page.state.tableScrollX.value, collapsedWidth)
      assert.equal(page.state.isSubjectExpanded(2), false)
      page.state.setSelectedColumnKeys(['subject', 'amount', 'feeDetail'])
      assert.deepEqual(page.state.visibleColumns.value.map(column => column.key), ['subject', 'amount', 'feeDetail'])
      assert.deepEqual(page.state.columns.value.map(column => column.key), keys, 'display selection does not shrink original role-scoped columns')
      const selectedWidth = page.state.tableScrollX.value
      assert.equal(selectedWidth, sumTableColumnWidths(page.state.visibleColumns.value))
      assert.ok(selectedWidth < collapsedWidth, 'scroll width follows selected columns, not the full source')
      page.state.toggleSubject(1)
      assert.equal(page.state.tableScrollX.value, selectedWidth + 66, 'selected project column still expands by the original amount')
      page.state.toggleSubject(1)
      page.state.resetColumns()
      assert.deepEqual(page.state.visibleColumns.value.map(column => column.key), keys)
      assert.equal(page.state.tableScrollX.value, collapsedWidth)
    } finally { page.close() }
  }
})

test('billing page uses server weighted rates and preserves unknown values when reloading', async () => {
  const responses = [
    { status: 200, data: { data: [{ id: 1, cacheHitRate: 0.99 }], total: 2, page: 1, pageSize: 1, summary: { totalAmount: 3, currentPageAmount: 1, currentPageCacheHitRate: 0.1, totalCacheHitRate: 0.46 } } },
    { status: 200, data: { data: [], total: 0, page: 1, pageSize: 1, summary: { currentPageCacheHitRate: null, totalCacheHitRate: null } } },
  ]
  const page = await loadBillingPage('admin', undefined, async () => responses.shift())
  try {
    page.component.methods.handleLoad.call(page.context)
    await new Promise(resolve => setImmediate(resolve))
    assert.equal(page.state.summary.value.currentPageCacheHitRate, 0.1)
    assert.equal(page.state.summary.value.totalCacheHitRate, 0.46)
    page.state.filters.keyword = 'model'
    assert.equal(page.state.hasActiveFilters.value, true)
    page.component.methods.handleLoad.call(page.context)
    await new Promise(resolve => setImmediate(resolve))
    assert.equal(page.state.summary.value.currentPageCacheHitRate, null)
    assert.equal(page.state.summary.value.totalCacheHitRate, null)
  } finally { page.close() }
})

test('pending statement save updates only its captured ID after dialog switch and list refresh', async () => {
  let resolveSave
  const requests = []
  const page = await loadBillingPage('admin', (id, value) => {
    requests.push({ id, value })
    return new Promise(resolve => { resolveSave = resolve })
  })
  try {
    const first = { id: 1, showInStatement: true }
    const second = { id: 2, showInStatement: true }
    page.state.dataSource.value = [first, second]
    page.state.selectedBillingRecord.value = first
    const save = page.component.methods.handleStatementVisibilityChange.call(page.context, { id: 1, showInStatement: false })
    assert.equal(page.state.visibilitySaving[1], true)
    await page.component.methods.handleStatementVisibilityChange.call(page.context, { id: 1, showInStatement: false })
    assert.equal(requests.length, 1, 'same ID must not issue a second in-flight save')
    page.state.billingDetailOpen.value = false
    page.state.selectedBillingRecord.value = second
    page.state.dataSource.value = [{ id: 1, showInStatement: true }, second]
    resolveSave({ status: 200 })
    assert.equal(await save, true)
    assert.equal(first.showInStatement, false)
    assert.equal(page.state.dataSource.value[0].showInStatement, false)
    assert.equal(second.showInStatement, true)
    assert.equal(page.state.selectedBillingRecord.value.id, 2)
    assert.equal(page.state.visibilitySaving[1], false)
  } finally { page.close() }
})

test('failed or unauthorized statement save does not change the current bill', async () => {
  for (const role of ['admin', 'user']) {
    let calls = 0
    const page = await loadBillingPage(role, async () => { calls += 1; throw new Error('失败') })
    try {
      const record = { id: 1, showInStatement: true }
      page.state.dataSource.value = [record]
      page.state.selectedBillingRecord.value = record
      assert.equal(await page.component.methods.handleStatementVisibilityChange.call(page.context, { id: 1, showInStatement: false }), false)
      assert.equal(record.showInStatement, true)
      assert.equal(calls, role === 'admin' ? 1 : 0)
      assert.notEqual(page.state.visibilitySaving[1], true)
    } finally { page.close() }
  }
})

test('log, billing and detail components compile with modern table slots', async () => {
  for (const path of [detailPath, adminPath, new URL('../pages/UserUsage/index.vue', import.meta.url), new URL('../pages/LogList/index.vue', import.meta.url)]) {
    const source = await readFile(path, 'utf8')
    const { descriptor, errors } = parse(source, { filename: path.pathname })
    assert.deepEqual(errors, [])
    const script = compileScript(descriptor, { id: path.pathname })
    const template = compileTemplate({ source: descriptor.template.content, filename: path.pathname, id: path.pathname, compilerOptions: { bindingMetadata: script.bindings } })
    assert.deepEqual(template.errors, [])
    if (path !== detailPath) {
      assert.match(source, /#bodyCell=/)
      assert.doesNotMatch(source, /slots:\s*\{|#(?:username|subject|searchMode|requestSnapshot)=/)
    }
  }
})
