const assert = require('node:assert/strict')
const { after, afterEach, test } = require('node:test')
require('../helpers/vue-dom.cjs')
const { flushPromises, mount } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const axios = require('axios')
const currentUser = require('../../src/utils/currentUser.js')

const actor = { id: 51, username: 'submitter', role: 'user', status: 'enabled' }
const proof = {
  proof: 'synthetic-proof',
  expiresAt: '2026-11-21T12:05:00Z',
  registeredAt: '2026-10-01T12:00:00Z',
  maskedPhone: '****6789',
}
const previousAdapter = axios.defaults.adapter
const calls = []
let wrapper

axios.defaults.adapter = config => {
  assert.equal(config.url, '/api/tickets/appeal-verification')
  assert.equal(config.method, 'post')
  return new Promise(resolve => {
    calls.push({
      signal: config.signal,
      payload: JSON.parse(config.data),
      finish(status, body) {
        resolve({ status, data: body, headers: {}, config, request: { memoryOnly: true } })
      },
    })
  })
}
const Verification = require('../../src/pages/Tickets/AccountAppealVerification.vue')

async function settle() {
  await nextTick()
  await flushPromises()
  await nextTick()
}

async function open() {
  currentUser.setStoredCurrentUser(actor)
  wrapper = mount(Verification, { attachTo: document.body, props: { open: true }, global: { plugins: [Antd] } })
  await settle()
}

function field(type) {
  const input = document.querySelector(`.ant-modal input[type="${type}"]`)
  assert.ok(input, `expected native ${type} field`)
  return input
}

function enter(input, value) {
  input.value = value
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

function getButton(label) {
  const button = [...document.querySelectorAll('.ant-modal button')].find(element => element.textContent.replace(/\s/g, '') === label)
  assert.ok(button, `expected native button ${label}`)
  return button
}

async function submit() {
  enter(field('text'), '  private-target  ')
  enter(field('password'), 'synthetic password')
  await settle()
  const button = getButton('验证账号')
  assert.equal(button.disabled, false)
  button.click()
  await settle()
  assert.equal(calls.length, 1)
  assert.deepEqual(calls[0].payload, { username: 'private-target', password: 'synthetic password' })
  assert.equal(field('password').value, '')
  return calls[0]
}

afterEach(async () => {
  wrapper?.unmount()
  wrapper = undefined
  for (const call of calls.splice(0)) call.finish(200, { data: proof })
  await settle()
  currentUser.clearStoredCurrentUser()
  document.body.replaceChildren()
})
after(() => { axios.defaults.adapter = previousAdapter })

test('the credentials request uses the appeal endpoint and emits only masked account data and its proof', async () => {
  await open()
  const call = await submit()
  call.finish(200, { data: { ...proof, username: 'private-target', phone: '13912346789' } })
  await settle()
  assert.deepEqual(wrapper.emitted('verified'), [[{ ...proof, username: 'p***t' }]])
  assert.deepEqual(wrapper.emitted('update:open'), [[false]])
})

test('cancel aborts a pending request and its late response cannot verify the account', async () => {
  await open()
  const call = await submit()
  getButton('取消').click()
  await settle()
  assert.equal(call.signal.aborted, true)
  call.finish(200, { data: proof })
  await settle()
  assert.equal(wrapper.emitted('verified'), undefined)
  assert.equal(field('text').value, '')
  assert.equal(field('password').value, '')
})

test('switching accounts aborts verification and clears the prior credentials', async () => {
  await open()
  const call = await submit()
  currentUser.setStoredCurrentUser({ ...actor, id: 52, username: 'different-submitter' })
  await settle()
  assert.equal(call.signal.aborted, true)
  call.finish(200, { data: proof })
  await settle()
  assert.equal(wrapper.emitted('verified'), undefined)
  assert.equal(field('text').value, '')
})

test('a same-user session change retires a late verification', async () => {
  await open()
  const call = await submit()
  currentUser.advanceCurrentSessionRevision()
  call.finish(200, { data: proof })
  await settle()
  assert.equal(wrapper.emitted('verified'), undefined)
  assert.equal(field('password').disabled, false)
})

test('same-session account metadata refresh preserves a pending verification', async () => {
  await open()
  const call = await submit()
  currentUser.setStoredCurrentUser({ ...actor, balance: 12 })
  await settle()
  assert.equal(call.signal.aborted, false)
  call.finish(200, { data: proof })
  await settle()
  assert.deepEqual(wrapper.emitted('verified'), [[{ ...proof, username: 'p***t' }]])
})
