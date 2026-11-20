const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const axios = require('axios')
const currentUser = require('../../src/utils/currentUser.js')

const originalAdapter = axios.defaults.adapter
let records, calls, holdSave, failSave, thresholdValue, configValue, holdThreshold
const wrappers = [], routers = []
const builtin = () => ({ ruleId: 'debt_user', name: '用户欠费', type: 'debt_user', source: 'builtin', threshold: 10, unit: '元', enabled: true, severity: 'medium', description: '内置默认', throttleSeconds: 60, updatedAt: '2026-11-20T08:00:00Z', updatedBy: 1 })
const custom = () => ({ ...builtin(), ruleId: 'custom_demo', name: '自定义欠费', source: 'custom', threshold: 25, description: '自定义说明', updatedBy: 7 })
const response = (config, data, status = 200) => ({ config, data, status, headers: {}, request: {} })
axios.defaults.adapter = async config => {
  const body = typeof config.data === 'string' ? JSON.parse(config.data) : config.data
  calls.push({ method: config.method, url: config.url, body })
  if (config.url === '/api/risk/alerts') return response(config, { data: [], total: 0, page: 1, pageSize: 20 })
  if (config.url === '/api/risk/webhook') return response(config, { data: { enabled: false, configured: false, webhookUrl: '', preview: '', mentionedList: [] } })
  const threshold = () => ({ key: 'risk_rule_debt_user_threshold', ruleId: 'debt_user', label: '用户欠费阈值', value: thresholdValue, defaultValue: 10, unit: '元', configured: true })
  const setting = () => ({ key: 'risk_proxy_asn_list', ruleId: 'proxy_ip', label: 'ASN 名单', value: configValue, kind: 'asn_list', configured: true })
  if (config.url === '/api/risk/thresholds') return response(config, { items: [threshold()] })
  if (config.url === '/api/risk/config') return response(config, { items: [setting()] })
  if (config.url.startsWith('/api/risk/thresholds/')) {
    if (holdThreshold) await holdThreshold
    thresholdValue = config.method === 'delete' ? 10 : body.value
    records.find(item => item.ruleId === 'debt_user').threshold = thresholdValue
    return response(config, { message: '已保存', item: threshold() })
  }
  if (config.url.startsWith('/api/risk/config/')) {
    configValue = config.method === 'delete' ? '' : body.value
    return response(config, { message: '已保存', item: setting() })
  }
  if (config.url === '/api/risk/rule-types') return response(config, { data: [{ type: 'debt_user', name: '用户欠费', defaultThreshold: 10, thresholdUnit: '元', description: '达到欠费阈值时告警' }] })
  if (config.url === '/api/risk/rules' && config.method === 'get') {
    // Real endpoint deliberately retains the old compact shape alongside editable records.
    return response(config, { items: records.map(({ ruleId, enabled, threshold }) => ({ ruleId, enabled, threshold })), data: records.map(item => ({ ...item })) })
  }
  if (config.url === '/api/risk/rules' && config.method === 'post') {
    if (holdSave) await holdSave
    if (failSave) return response(config, { message: failSave }, 400)
    const saved = { ...custom(), ...body, ruleId: 'custom_generated' }
    records.push(saved)
    return response(config, { data: saved }, 201)
  }
  if (config.url.endsWith('/enabled')) {
    const id = config.url.split('/').at(-2)
    const saved = records.find(item => item.ruleId === id)
    saved.enabled = body.enabled
    return response(config, { message: '已保存', item: { ...saved } })
  }
  if (config.url.startsWith('/api/risk/rules/')) {
    const id = config.url.split('/').at(-1)
    if (config.method === 'delete') {
      records = records.filter(item => item.ruleId !== id)
      return response(config, { message: '已删除' })
    }
    const saved = records.find(item => item.ruleId === id)
    Object.assign(saved, body)
    if (id === 'debt_user') thresholdValue = saved.threshold
    return response(config, { data: { ...saved } })
  }
  throw new Error(`Unexpected request ${config.method} ${config.url}`)
}
const RiskAdmin = require('../../src/pages/RiskAdmin/index.vue')

async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function openPage() {
  records = [builtin(), custom()]; calls = []; holdSave = holdThreshold = null; failSave = ''; thresholdValue = 10; configValue = 'AS15169'
  currentUser.setStoredCurrentUser({ id: 7, role: 'admin', username: 'synthetic-admin' })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: RiskAdmin }] })
  routers.push(router)
  await router.push('/'); await router.isReady()
  const wrapper = mount(RiskAdmin, { attachTo: document.body, global: { plugins: [router, Antd] } })
  wrappers.push(wrapper)
  await settle()
  return wrapper
}
function button(wrapper, text) {
  const found = wrapper.findAll('button').find(item => item.text().replace(/\s/g, '') === text)
  assert.ok(found, text)
  return found
}
afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  for (const router of routers.splice(0)) router.options.history.destroy()
  currentUser.clearStoredCurrentUser(); Antd.message.destroy(); Antd.Modal.destroyAll()
  await settle(); document.body.replaceChildren()
})
after(() => { axios.defaults.adapter = originalAdapter })

test('renders editable records rather than legacy compact states and preserves the chosen detector', async () => {
  const wrapper = await openPage()
  assert.equal(wrapper.findAll('.risk-rule-row').length, 2, JSON.stringify({ calls, error: wrapper.vm.rulesError, canManage: wrapper.vm.canManage() }))
  const row = wrapper.findAll('.risk-rule-row')[1]
  assert.match(row.text(), /自定义欠费/)
  assert.match(row.text(), /自定义/)
  assert.match(row.text(), /debt_user/)
  assert.match(row.text(), /25 元/)
  await button(row, '编辑').trigger('click'); await settle()
  assert.equal(wrapper.vm.ruleDraft.type, 'debt_user')
  assert.equal(wrapper.vm.ruleDraft.throttleSeconds, 60)
  wrapper.vm.ruleDraft.name = '更新后的规则'
  wrapper.vm.ruleDraft.threshold = 30
  await wrapper.vm.saveRule(); await settle()
  assert.equal(wrapper.vm.ruleEditorOpen, false)
  assert.match(wrapper.findAll('.risk-rule-row')[1].text(), /更新后的规则/)
  assert.ok(calls.some(item => item.method === 'put' && item.url === '/api/risk/rules/custom_demo' && item.body.threshold === 30))
})

test('creates a server-ID rule, toggles it, and deletes only after confirmation', async () => {
  const wrapper = await openPage()
  await button(wrapper, '新增规则').trigger('click'); await settle()
  wrapper.vm.ruleDraft.name = '新增规则示例'
  wrapper.vm.ruleDraft.threshold = 45
  await wrapper.vm.saveRule(); await settle()
  const create = calls.find(item => item.method === 'post')
  assert.equal(create.body.type, 'debt_user')
  assert.equal(create.body.ruleId, undefined)
  assert.deepEqual(create.body.config, {})
  let row = wrapper.findAll('.risk-rule-row').find(item => item.text().includes('custom_generated'))
  assert.ok(row)
  await row.find('button[role="switch"]').trigger('click'); await settle()
  assert.match(row.text(), /已停用/)
  await button(row, '删除').trigger('click'); await settle()
  await new Promise(resolve => setTimeout(resolve, 60)); await settle()
  assert.equal(calls.some(item => item.method === 'delete'), false, 'opening confirmation cannot delete')
  const confirm = [...document.querySelectorAll('.ant-popconfirm button')].find(item => item.textContent.replace(/\s/g, '') === '删除')
  assert.ok(confirm, 'delete confirmation')
  confirm.click(); await settle()
  assert.ok(calls.some(item => item.method === 'delete' && item.url.endsWith('/custom_generated')))
  assert.equal(wrapper.findAll('.risk-rule-row').length, 2)
})

test('validation failure keeps the rule draft and modal open', async () => {
  const wrapper = await openPage()
  await wrapper.vm.openCreateRule(); await settle()
  wrapper.vm.ruleDraft.name = '保留草稿'
  failSave = '阈值必须大于 0'
  await wrapper.vm.saveRule(); await settle()
  assert.equal(wrapper.vm.ruleEditorOpen, true)
  assert.equal(wrapper.vm.ruleDraft.name, '保留草稿')
  assert.match(wrapper.vm.rulesError, /阈值必须大于 0/)
})

test('a pending save cannot close or overwrite the next administrator rule editor', async () => {
  const wrapper = await openPage()
  await wrapper.vm.openCreateRule(); await settle()
  wrapper.vm.ruleDraft.name = '旧账号草稿'
  let release
  holdSave = new Promise(resolve => { release = resolve })
  const saving = wrapper.vm.saveRule(); await settle()
  currentUser.setStoredCurrentUser({ id: 8, role: 'admin', username: 'next-admin' }); await settle()
  assert.equal(wrapper.vm.ruleEditorOpen, false, 'switching administrator clears old draft')
  assert.equal(wrapper.vm.ruleWriting, false)
  await wrapper.vm.openCreateRule(); await settle()
  wrapper.vm.ruleDraft.name = '新账号草稿'
  release(); await saving; await settle()
  assert.equal(wrapper.vm.ruleEditorOpen, true, 'old completion cannot close new editor')
  assert.equal(wrapper.vm.ruleDraft.name, '新账号草稿')
  assert.equal(wrapper.vm.rulesSuccess, '')
})

test('legacy threshold and text settings load real envelopes and keep the rule editor synchronized', async () => {
  const wrapper = await openPage()
  assert.equal(wrapper.vm.thresholds.length, 1)
  assert.equal(wrapper.vm.configItems.length, 1)
  assert.equal(wrapper.vm.configItemDraft.risk_proxy_asn_list, 'AS15169')
  wrapper.vm.thresholdDraft.risk_rule_debt_user_threshold = 55
  await wrapper.vm.saveThreshold(wrapper.vm.thresholds[0]); await settle()
  assert.equal(wrapper.vm.rules.find(item => item.ruleId === 'debt_user').threshold, 55)
  wrapper.vm.openEditRule(wrapper.vm.rules.find(item => item.ruleId === 'debt_user'))
  wrapper.vm.ruleDraft.threshold = 65
  await wrapper.vm.saveRule(); await settle()
  assert.equal(wrapper.vm.thresholdDraft.risk_rule_debt_user_threshold, 65)
  await wrapper.vm.resetThreshold(wrapper.vm.thresholds[0]); await settle()
  assert.equal(wrapper.vm.rules.find(item => item.ruleId === 'debt_user').threshold, 10)
  wrapper.vm.configItemDraft.risk_proxy_asn_list = 'AS16509'
  await wrapper.vm.saveConfigItem(wrapper.vm.configItems[0]); await settle()
  assert.equal(wrapper.vm.configItems[0].value, 'AS16509')
})

test('retired threshold completion cannot update the next admin status or start follow-up reads', async () => {
  const wrapper = await openPage()
  assert.equal(wrapper.vm.thresholds.length, 1)
  let release
  holdThreshold = new Promise(resolve => { release = resolve })
  const saving = wrapper.vm.saveThreshold(wrapper.vm.thresholds[0]); await settle()
  currentUser.setStoredCurrentUser({ id: 8, role: 'admin', username: 'next-admin' }); await settle()
  assert.equal(wrapper.vm.thresholdWriting, false)
  const count = calls.length
  release(); await saving; await settle()
  assert.equal(calls.length, count)
  assert.equal(wrapper.vm.thresholdSuccess, '')
})
