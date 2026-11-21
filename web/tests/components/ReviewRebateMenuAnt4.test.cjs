const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises, config } = require('@vue/test-utils')
const { h, nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const axios = require('axios')
const { message } = require('ant-design-vue')
const { registerConsoleUI } = require('../../src/registerUI.js')
const { useCurrentUserDisplay, setStoredCurrentUser, getStoredCurrentUser, clearStoredCurrentUser } = require('../../src/utils/currentUser.js')

for (const name of ['ConsolePageHeading', 'ConsolePanel', 'ConsoleSummaryGrid', 'ConsoleTableShell']) delete config.global.components[name]

const availabilityUrl = '/api/user/review-rebate/availability'
const programUrl = '/api/admin/review-rebate/programs'
const member = { id: 1, role: 'user', status: 'enabled' }
const admin = { id: 2, role: 'admin', status: 'enabled' }
const calls = []
const mounts = []
let readAvailability
let program

axios.defaults.adapter = async config => {
  calls.push({ url: config.url, method: config.method, actor: getStoredCurrentUser()?.id })
  let data
  if (config.url === availabilityUrl) data = await readAvailability()
  else if (config.url === programUrl && config.method === 'get') data = { data: [program] }
  else if (config.url === `${programUrl}/${program?.id}` && config.method === 'put') {
    program = { ...program, ...JSON.parse(config.data) }
    data = program
  } else if (['/api/admin/review-rebate/rules', '/api/admin/review-rebate/claims'].includes(config.url)) data = { data: [] }
  else throw new Error(`Unexpected fixture operation ${config.method} ${config.url}`)
  return { status: 200, data, headers: {}, config, request: { memoryOnly: true } }
}

const Sider = require('../../src/components/SiderMenu/index.vue')
const Review = require('../../src/pages/ReviewRebateAdmin/index.vue')

async function settle() {
  await nextTick()
  await flushPromises()
  await nextTick()
  await flushPromises()
}

function deferred() {
  let resolve
  const promise = new Promise(done => { resolve = done })
  return { promise, resolve }
}

async function open(actor, withManagement = false) {
  setStoredCurrentUser(actor)
  const history = createMemoryHistory()
  const router = createRouter({ history, routes: [{ path: '/review-rebate-admin', component: Review }] })
  await router.push('/review-rebate-admin')
  await router.isReady()
  const wrapper = mount({
    setup() {
      const { currentUser } = useCurrentUserDisplay()
      return () => h('div', [h(Sider, { currentUser: currentUser.value }), withManagement && h(Review)])
    },
  }, { attachTo: document.body, global: { plugins: [router, { install: registerConsoleUI }] } })
  mounts.push({ wrapper, history })
  await settle()
  return wrapper
}

const selfEntry = wrapper => wrapper.find('nav a[href="/review-rebate"]')
const adminEntry = wrapper => wrapper.find('nav a[href="/review-rebate-admin"]')
const availabilityCalls = () => calls.filter(call => call.url === availabilityUrl)

afterEach(async () => {
  for (const { wrapper, history } of mounts.splice(0)) {
    wrapper.unmount()
    history.destroy()
  }
  clearStoredCurrentUser()
  message.destroy()
  await settle()
  document.body.replaceChildren()
  calls.length = 0
})

test('ordinary user entry is hidden until availability is confirmed and on refresh failure', async () => {
  const firstRead = deferred()
  readAvailability = () => firstRead.promise
  const wrapper = await open(member)
  assert.equal(selfEntry(wrapper).exists(), false)
  assert.equal(adminEntry(wrapper).exists(), false)
  window.dispatchEvent(new Event('focus'))
  document.dispatchEvent(new Event('visibilitychange'))
  assert.equal(availabilityCalls().length, 1)

  firstRead.resolve({ enabled: true })
  await settle()
  assert.equal(selfEntry(wrapper).text(), '评价返现')

  readAvailability = async () => { throw new Error('availability failed') }
  window.dispatchEvent(new Event('focus'))
  await settle()
  assert.equal(selfEntry(wrapper).exists(), false)

  readAvailability = async () => ({ enabled: false })
  document.dispatchEvent(new Event('visibilitychange'))
  await settle()
  assert.equal(selfEntry(wrapper).exists(), false)
  assert.equal(availabilityCalls().length, 3)
  assert.deepEqual(calls, availabilityCalls())
})

test('late availability cannot reveal the entry after actor change, ban, disable or logout', async () => {
  const oldRead = deferred()
  readAvailability = () => oldRead.promise
  const wrapper = await open(member)
  readAvailability = async () => ({ enabled: false })
  setStoredCurrentUser(admin)
  await settle()
  assert.equal(adminEntry(wrapper).text(), '评价返现管理')
  oldRead.resolve({ enabled: true })
  await settle()
  assert.equal(selfEntry(wrapper).exists(), false)
  assert.deepEqual(availabilityCalls().map(call => call.actor), [member.id, admin.id])

  const beforeBan = deferred()
  readAvailability = () => beforeBan.promise
  window.dispatchEvent(new Event('focus'))
  setStoredCurrentUser({ ...admin, status: 'banned' })
  beforeBan.resolve({ enabled: true })
  await settle()
  assert.equal(selfEntry(wrapper).exists(), false)
  assert.equal(adminEntry(wrapper).exists(), false)

  const beforeDisable = deferred()
  readAvailability = () => beforeDisable.promise
  setStoredCurrentUser(admin)
  await settle()
  setStoredCurrentUser({ ...admin, status: 'disabled' })
  beforeDisable.resolve({ enabled: true })
  await settle()
  assert.equal(selfEntry(wrapper).exists(), false)
  window.dispatchEvent(new Event('focus'))
  assert.equal(availabilityCalls().length, 4)

  const beforeLogout = deferred()
  readAvailability = () => beforeLogout.promise
  setStoredCurrentUser(admin)
  await settle()
  clearStoredCurrentUser()
  beforeLogout.resolve({ enabled: true })
  await settle()
  assert.equal(selfEntry(wrapper).exists(), false)
  assert.equal(availabilityCalls().length, 5)
})

test('administrator save refreshes the ordinary entry while retaining the management entry', async () => {
  program = { id: 15, name: '合成活动', enabled: false, maxClaimsPerUser: 1, maxAmountPerUser: 0, minAgentConfidence: 0.8, policyVersion: 'v1' }
  readAvailability = async () => ({ enabled: program.enabled })
  const wrapper = await open(admin, true)
  assert.equal(selfEntry(wrapper).exists(), false)
  assert.equal(adminEntry(wrapper).text(), '评价返现管理')
  const management = wrapper.findComponent(Review)
  const save = management.findAll('button').find(button => button.text().replace(/\s/g, '') === '保存活动配置')
  assert.ok(save)
  const enabledControl = management.find('button[role="switch"]')
  const beforeSave = deferred()
  const readSavedAvailability = readAvailability
  readAvailability = () => beforeSave.promise
  window.dispatchEvent(new Event('focus'))
  readAvailability = readSavedAvailability
  await enabledControl.trigger('click')
  await save.trigger('click')
  await settle()
  assert.equal(program.enabled, true)
  assert.equal(selfEntry(wrapper).text(), '评价返现')
  assert.equal(adminEntry(wrapper).text(), '评价返现管理')
  beforeSave.resolve({ enabled: false })
  await settle()
  assert.equal(selfEntry(wrapper).text(), '评价返现')

  await enabledControl.trigger('click')
  await save.trigger('click')
  await settle()
  assert.equal(program.enabled, false)
  assert.equal(selfEntry(wrapper).exists(), false)
  assert.equal(adminEntry(wrapper).text(), '评价返现管理')
  assert.equal(availabilityCalls().length, 4)
})
