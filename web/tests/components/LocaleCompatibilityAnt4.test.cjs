const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const webRoot = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
const helper = require(path.resolve(webRoot, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils')
const { h, nextTick } = require('vue')
const { createRouter, createMemoryHistory, RouterView } = require('vue-router')
const Antd = require('ant-design-vue')
const { Empty, Table, Pagination, Select, DatePicker, Form, Input } = Antd
const apiPath = path.resolve(webRoot, 'src/api/TikuComponentApi.js')
let publicCalls = 0
let publicResult = () => Promise.resolve({ status: 200, data: { siteName: '中文站点测试' } })
const api = new Module(apiPath)
api.loaded = true
api.exports = { getPublicRegisterStatus: () => { publicCalls++; return publicResult() }, logout: () => Promise.resolve() }
require.cache[apiPath] = api
const App = require(path.resolve(webRoot, 'src/App.vue'))
const PageHeader = require(path.resolve(webRoot,'src/components/PageHeader/index.vue'))
const { siteName } = require(path.resolve(webRoot, 'src/utils/siteName.js'))
const { DEFAULT_SITE_NAME } = require(path.resolve(webRoot, 'src/utils/siteNameHelpers.mjs'))
const colorQuery = '(prefers-color-scheme: dark)'
let wrapper, outside
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function open(render, { rejectPublic = false } = {}) {
  publicCalls = 0
  publicResult = rejectPublic ? () => Promise.reject(new Error('synthetic offline fixture')) : () => Promise.resolve({ status: 200, data: { siteName: '中文站点测试' } })
  const routes = [{ path: '/locale', component: { render: () => h(RouterView) }, children: [
    { path: 'one', component: { name: 'LocaleFirstPage', render: () => h('section', { 'data-route': 'one' }, render()) } },
    { path: 'two', component: { name: 'LocaleSecondPage', render: () => h('section', { 'data-route': 'two' }, render()) } },
  ] }]
  const router = createRouter({ history: createMemoryHistory(), routes })
  await router.push('/locale/one'); await router.isReady()
  wrapper = mount(App, { attachTo: document.body, global: { plugins: [router, Antd] } })
  await settle()
  return router
}
afterEach(async () => {
  wrapper?.unmount(); outside?.unmount(); wrapper = undefined; outside = undefined
  await settle()
  document.body.innerHTML = ''
  window.localStorage.clear(); helper.setSystemDark(false)
  document.documentElement.removeAttribute('data-theme'); document.documentElement.removeAttribute('data-theme-mode')
})
test('App locale reaches real Empty and Table across nested RouterView without adding a DOM wrapper or leaking outside the root', async () => {
  const router = await open(() => [h(Empty), h(Table, { columns: [{ title: '题目', dataIndex: 'question' }], dataSource: [], pagination: false })])
  const read = () => wrapper.findAll('.ant-empty-description').map(node => node.text())
  console.log('LOCALE_EMPTY_TABLE ' + JSON.stringify(read()))
  assert.deepEqual(read(), ['暂无数据', '暂无数据'])
  assert.equal(wrapper.element.id, 'app')
  assert.equal(wrapper.element.firstElementChild.dataset.route, 'one')
  await router.push('/locale/two'); await settle()
  assert.equal(wrapper.element.firstElementChild.dataset.route, 'two')
  assert.deepEqual(read(), ['暂无数据', '暂无数据'])
  outside = mount(Empty, { attachTo: document.body })
  assert.equal(outside.find('.ant-empty-description').text(), 'No data', 'locale must remain scoped to the actual App descendants')
})
test('real Pagination renders Chinese controls and retains native page-change behavior', async () => {
  const pages = []
  await open(() => [h(Pagination, { total: 120, defaultCurrent: 1, showSizeChanger: true, showQuickJumper: true, onChange: page => pages.push(page) })])
  const previous = wrapper.find('.ant-pagination-prev'), next = wrapper.find('.ant-pagination-next')
  const snapshot = { previous: previous.attributes('title'), next: next.attributes('title'), jumper: wrapper.find('.ant-pagination-options-quick-jumper').text(), size: wrapper.find('.ant-select-selection-item').text() }
  console.log('LOCALE_PAGINATION ' + JSON.stringify(snapshot))
  assert.deepEqual(snapshot, { previous: '上一页', next: '下一页', jumper: '跳至页', size: '10 条/页' })
  await next.trigger('click'); await settle()
  assert.deepEqual(pages, [2]); assert.equal(wrapper.find('.ant-pagination-item-active').text(), '2')
})
test('real Select empty dropdown keeps Chinese locale through its body portal', async () => {
  await open(() => [h(Select, { options: [], open: true, style: { width: '200px' } })])
  const popup = document.body.querySelector('.ant-select-dropdown')
  assert.ok(popup && !wrapper.element.contains(popup), 'dropdown must really be rendered outside #app in body')
  const description = popup.querySelector('.ant-empty-description')?.textContent.trim()
  console.log('LOCALE_SELECT_PORTAL ' + JSON.stringify(description))
  assert.equal(description, '暂无数据')
})
test('real DatePicker renders Chinese placeholder and panel labels through its body portal without a value-format override', async () => {
  await open(() => [h(DatePicker, { open: true })])
  const input = wrapper.find('.ant-picker input')
  const popup = document.body.querySelector('.ant-picker-dropdown')
  assert.ok(popup && !wrapper.element.contains(popup), 'date panel must really be rendered in body outside #app')
  const snapshot = { placeholder: input.attributes('placeholder'), today: popup.querySelector('.ant-picker-today-btn')?.textContent.trim() }
  console.log('LOCALE_DATE_PORTAL ' + JSON.stringify(snapshot))
  assert.deepEqual(snapshot, { placeholder: '请选择日期', today: '今天' })
})
test('real App retains one theme-controller lifetime header interaction native Form context and public site-name behavior', async () => {
  await open(() => [h(PageHeader), h(Form, { name:'locale-owned', model: { question: '' } }, { default: () => h(Form.Item, { label: '题目', name: 'question' }, { default: () => h(Input) }) })])
  assert.equal(publicCalls, 1); assert.equal(siteName.value, '中文站点测试'); assert.equal(document.title, '中文站点测试')
  assert.equal(helper.listenerCount(colorQuery), 1)
  const appearance = wrapper.find('select[aria-label="外观"]')
  await appearance.setValue('dark'); await settle()
  assert.equal(document.documentElement.dataset.theme, 'dark'); assert.equal(window.localStorage.getItem('tiku.theme-mode.v1'), 'dark')
  await appearance.setValue('system'); helper.setSystemDark(true); await settle()
  assert.equal(document.documentElement.dataset.theme, 'dark')
  const label = wrapper.find('.ant-form-item-label > label'), input = wrapper.find('.ant-form-item input')
  assert.equal(label.element.htmlFor, input.element.id); assert.ok(input.element.id)
  assert.equal(label.element.control,input.element,'native named Form association remains authoritative')
  assert.equal(input.attributes('aria-labelledby'),undefined,'native named field must not be rewritten by legacy fallback')
  wrapper.unmount(); wrapper = undefined; await settle()
  assert.equal(helper.listenerCount(colorQuery), 0)
  helper.setSystemDark(false); await settle()
  assert.equal(document.documentElement.dataset.theme, 'dark', 'disposed root must no longer react to system preference')
})
test('real App keeps the public-status failure fallback while locale is provided', async () => {
  await open(() => [h(Empty)], { rejectPublic: true })
  assert.equal(publicCalls, 1); assert.equal(siteName.value, DEFAULT_SITE_NAME); assert.equal(document.title, DEFAULT_SITE_NAME)
  assert.equal(wrapper.find('.ant-empty-description').text(), '暂无数据')
})
