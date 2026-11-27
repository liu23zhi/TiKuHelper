const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const axios = require('axios')
const savedAdapter = axios.defaults.adapter
let respond
axios.defaults.adapter = async config => {
  const body = typeof config.data === 'string' ? JSON.parse(config.data) : config.data
  return { config, status: 200, headers: {}, request: {}, data: { data: await respond(config.method, config.url, body) } }
}
const Window = require('../../src/pages/RiskAdmin/RiskWindowSettings.vue')
const Response = require('../../src/pages/RiskAdmin/RiskAlertResponse.vue')
const wrappers = []
const settle = async () => { await flushPromises(); await nextTick() }
async function open(component, props, adapter) {
  respond = adapter
  const wrapper = mount(component, { props: { ...props, captureScope: () => () => true }, global: { plugins: [Antd] } })
  wrappers.push(wrapper); await settle(); return wrapper
}
const click = async (w, text) => {
  const b = w.findAll('button').find(el => el.text().replace(/\s/g, '') === text)
  assert.ok(b, text); await b.trigger('click'); await settle(); return b
}
afterEach(() => { for (const w of wrappers.splice(0)) w.unmount() })
after(() => { axios.defaults.adapter = savedAdapter })

test('共享窗口以一条请求写入跨天起止，展示服务端核准值；等时段不提交', async () => {
  const calls = []
  const w = await open(Window, {}, (method, url, body) => {
    calls.push({ method, url, body })
    return { startHour: 2, endHour: 5, defaultStartHour: 2, defaultEndHour: 5, configured: method === 'put', ...body }
  })
  const [start, end] = w.findAll('input')
  await start.setValue(5)
  await click(w, '保存整个时段')
  assert.equal(calls.length, 1)
  await start.setValue(22); await end.setValue(6)
  await click(w, '保存整个时段')
  assert.deepEqual(calls.at(-1), { method: 'put', url: '/api/risk/offhours-window', body: { startHour: 22, endHour: 6 } })
  assert.match(w.get('[data-window-effective]').text(), /22:00 至 次日 06:00/)
  await click(w, '恢复默认时段')
  assert.equal(calls.at(-1).method, 'delete'); assert.match(w.get('[data-window-effective]').text(), /02:00 至 05:00/)
})

test('城市+运营商和永久均需核对，实际请求不允许客户端指定目标，结果显示操作记录', async () => {
  const writes = []
  const w = await open(Response, { alertId: 17 }, (method, url, body) => {
    assert.equal(url, '/api/risk/alerts/17/response')
    if (method === 'post') writes.push(body)
    return { eligible: true, ip: '192.0.2.1', segment: '', cityIsp: '样例国家 · 样例省 · 样例市 · 测试运营商', blacklistEnabled: true,
      history: method === 'post' ? [{ id: 1, actorId: 7, target: '样例国家 · 样例省 · 样例市 · 测试运营商', durationMinutes: 0, reason: body.reason, status: 'applied' }] : [] }
  })
  assert.equal(w.find('option[value="segment"]').exists(), false)
  await w.get('select').setValue('city_isp')
  assert.equal(w.get('[data-response-target]').text(), '样例国家 · 样例省 · 样例市 · 测试运营商')
  await w.get('textarea').setValue('管理员已核对测试记录')
  await w.findAll('input[type=checkbox]')[0].setValue(true)
  await click(w, '执行封禁'); assert.equal(writes.length, 0)
  await w.findAll('input[type=checkbox]')[1].setValue(true)
  await click(w, '执行封禁')
  assert.deepEqual(writes, [{ action: 'city_isp', durationMinutes: 0, reason: '管理员已核对测试记录', confirmed: true, expectedTarget: '样例国家 · 样例省 · 样例市 · 测试运营商' }])
  assert.match(w.text(), /管理员 #7/)
})

test('仅通知的告警和关闭黑名单总开关的告警均无执行入口', async () => {
  for (const values of [{ eligible: false, blacklistEnabled: true }, { eligible: true, blacklistEnabled: false }]) {
    const w = await open(Response, { alertId: 18 }, () => ({ ...values, ip: '', history: [] }))
    assert.ok(!w.text().includes('执行封禁')); assert.equal(w.find('select').exists(), false)
  }
})

test('封禁撤销先确认，再只发送规则处置编号并显示保留的审计', async () => {
  const writes = []
  const w = await open(Response, { alertId: 17 }, (method, url, body) => {
    assert.equal(url, '/api/risk/alerts/17/response')
    if (method === 'post') writes.push(body)
    const auto = { id: 7, actorId: 0, ruleId: 'custom_rule', target: '192.0.2.37-192.0.2.52', sourceIpMasked: '192.0.2.*',
      entryId: 3, status: 'applied', canRevoke: method === 'get' }
    return { eligible: true, ip: '192.0.2.41', blacklistEnabled: true,
      history: method === 'get' ? [auto] : [{ id: 8, actorId: 11, target: auto.target, status: 'revoked' }, auto] }
  })
  await click(w, '撤销自动封禁')
  assert.equal(writes.length, 0)
  const confirm = w.findComponent(Antd.Popconfirm)
  await confirm.vm.$emit('confirm'); await settle()
  assert.deepEqual(writes, [{ action: 'revoke_auto', responseId: 7, confirmed: true }])
  assert.match(w.text(), /已撤销/)
  assert.match(w.text(), /192.0.2.37-192.0.2.52/)
  assert.match(w.text(), /192.0.2.\*/)
})
