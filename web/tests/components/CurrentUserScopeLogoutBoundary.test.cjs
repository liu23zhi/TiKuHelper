/* Actual Vue scope/display helpers; synthetic storage only, no server auth claim. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module'), fs = require('node:fs')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils')
const { defineComponent, h, ref, nextTick } = require('vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const { useCurrentUserRequestScope } = require(path.join(web, 'src/utils/currentUserScope.js'))
const KEY = 'adapter_current_user'
const A = { id: 87341, role: 'admin', username: 'SYNTHETIC_SCOPE_LOGOUT_A', balance: 1.234567 }
const B = { id: 87342, role: 'admin', username: 'SYNTHETIC_SCOPE_LOGOUT_B', balance: 2.345678 }
const wrappers = [], observations = [], realIO = [], runtime = []
let activeCase = '', frames = []
const storagePrototype = window.Storage.prototype, removeItem = storagePrototype.removeItem, getItem = storagePrototype.getItem
function deny(kind) { realIO.push(kind); throw new Error('FORBIDDEN_SCOPE_LOGOUT_IO_' + kind) }
window.XMLHttpRequest.prototype.open = () => deny('XHR')
globalThis.fetch = window.fetch = () => deny('FETCH')
globalThis.WebSocket = window.WebSocket = class { constructor() { deny('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => deny(name); transport.get = () => deny(name) }
window.addEventListener('error', event => runtime.push(String(event.error?.stack || event.message)))
window.addEventListener('unhandledrejection', event => runtime.push(String(event.reason?.stack || event.reason)))
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
function begin(name) { activeCase = name; frames = []; user.setStoredCurrentUser({ ...A }) }
async function createFrame(display = true) {
  const frame = { events: [], privateDraft: 'SYNTHETIC_PRIVATE_DRAFT', currentUser: null, scope: null }
  const wrapper = mount(defineComponent({ setup() {
    const currentUser = display ? user.useCurrentUserDisplay().currentUser : ref({ ...A })
    frame.currentUser = currentUser
    frame.scope = useCurrentUserRequestScope(currentUser, {
      clear() { frame.events.push('clear'); frame.privateDraft = '' },
      reset() { frame.events.push('reset') },
      reload() { frame.events.push('reload') },
    })
    return () => h('div')
  } }))
  wrappers.push(wrapper); frame.wrapper = wrapper; frames.push(frame); await settle(); return frame
}
function refusedLogout() {
  let failures = 0
  storagePrototype.removeItem = function (key) { if (key === KEY) { failures += 1; throw new Error('SYNTHETIC_STORAGE_REMOVE_REFUSED') } return removeItem.call(this, key) }
  try { user.clearStoredCurrentUser() } finally { storagePrototype.removeItem = removeItem }
  assert.equal(failures, 1)
  assert.equal(user.getStoredCurrentUser()?.id, A.id, 'fault really retains the same cached synthetic identity')
  return failures
}
afterEach(async () => {
  storagePrototype.removeItem = removeItem; storagePrototype.getItem = getItem
  const row = { case: activeCase, frames: frames.map(frame => ({ events: [...frame.events], privateDraftEmpty: frame.privateDraft === '' })), realIO: [...realIO], runtime: [...runtime] }
  observations.push(row); console.log('SCOPE_LOGOUT_SAFETY ' + JSON.stringify(row))
  for (const wrapper of wrappers.splice(0)) if (wrapper.exists()) wrapper.unmount()
  await settle(); user.clearStoredCurrentUser(); await settle()
  assert.deepEqual(realIO, []); assert.deepEqual(runtime, [])
  if (process.env.CURRENT_SCOPE_LOGOUT_LEDGER) fs.writeFileSync(process.env.CURRENT_SCOPE_LOGOUT_LEDGER, JSON.stringify({ cases: observations, realIO, runtime, privateValuesRecorded: false }, null, 2) + '\n')
})
test('SL01 explicit logout retires same-cached-user scope, clears/reset once and does not auto reload', async () => {
  begin('SL01'); const frame = await createFrame(), old = frame.scope.captureDataOwner()
  frame.privateDraft = 'SYNTHETIC_PRIVATE_DRAFT'; const before = [...frame.events], epoch = user.getCurrentUserLogoutEpoch()
  refusedLogout(); await settle()
  assert.equal(user.getCurrentUserLogoutEpoch(), epoch + 1)
  assert.equal(frame.currentUser.value.id, A.id)
  assert.equal(old(), false, 'logical logout retires captured data ownership even when identity/admin cache is unchanged')
  assert.deepEqual(frame.events.slice(before.length), ['clear', 'reset'])
  assert.equal(frame.privateDraft, '')
  assert.equal(frame.scope.canLoadForCurrentUser(), true, 'data identity accessor is not redefined as server authentication')
})
test('SL02 captured owner directly observes real logout epoch even without a reactive display event', async () => {
  begin('SL02'); const frame = await createFrame(false), old = frame.scope.captureDataOwner()
  refusedLogout(); await settle()
  assert.equal(frame.currentUser.value.id, A.id)
  assert.equal(old(), false, 'snapshot guard must not rely solely on watcher scheduling or display subscriptions')
})
test('SL03 same-actor balance metadata preserves ownership, draft and existing completion order', async () => {
  begin('SL03'); const frame = await createFrame(), old = frame.scope.captureDataOwner(), before = [...frame.events]
  frame.privateDraft = 'SYNTHETIC_PRIVATE_DRAFT'
  user.setStoredCurrentUser({ ...A, balance: 0.000062 }); await settle()
  const newer = frame.scope.captureDataOwner(); let result = ''
  if (newer()) result = 'newer-completed-first'
  if (old()) result = 'older-completed-last'
  assert.equal(old(), true); assert.equal(newer(), true); assert.equal(result, 'older-completed-last')
  assert.equal(frame.currentUser.value.balance, 0.000062)
  assert.deepEqual(frame.events, before); assert.equal(frame.privateDraft, 'SYNTHETIC_PRIVATE_DRAFT')
})
test('SL04 identity and same-identity role transitions keep the original clear/reset/reload protocol', async () => {
  begin('SL04'); const frame = await createFrame(), oldA = frame.scope.captureDataOwner(), before = frame.events.length
  user.setStoredCurrentUser({ ...B }); await settle()
  assert.equal(oldA(), false); assert.deepEqual(frame.events.slice(before), ['clear', 'reset', 'reload'])
  const oldB = frame.scope.captureDataOwner(), roleStart = frame.events.length
  user.setStoredCurrentUser({ ...B, role: 'user' }); await settle()
  assert.equal(oldB(), false); assert.deepEqual(frame.events.slice(roleStart), ['clear', 'reload'])
})
test('SL05 unmount remains a terminal ownership boundary without later logout callbacks', async () => {
  begin('SL05'); const frame = await createFrame(), old = frame.scope.captureDataOwner()
  frame.wrapper.unmount(); await settle(); const before = [...frame.events]
  refusedLogout(); await settle()
  assert.equal(old(), false); assert.deepEqual(frame.events, before)
})
test('SL06 unknown storage read alone is not invented as an explicit logout or a new scope', async () => {
  begin('SL06'); const frame = await createFrame(), old = frame.scope.captureDataOwner(), before = [...frame.events], epoch = user.getCurrentUserLogoutEpoch()
  storagePrototype.getItem = function (key) { if (key === KEY) throw new Error('SYNTHETIC_STORAGE_READ_UNKNOWN'); return getItem.call(this, key) }
  try { assert.equal(user.getStoredCurrentUser(), null); await settle() } finally { storagePrototype.getItem = getItem }
  assert.equal(user.getCurrentUserLogoutEpoch(), epoch); assert.equal(old(), true)
  assert.equal(frame.currentUser.value.id, A.id); assert.deepEqual(frame.events, before)
})
test('SL07 fresh same-ID snapshot admits new ownership without resurrecting the pre-logout ticket', async () => {
  begin('SL07'); const frame = await createFrame(), old = frame.scope.captureDataOwner()
  refusedLogout(); await settle(); user.setStoredCurrentUser({ ...A }); await settle()
  const fresh = frame.scope.captureDataOwner()
  assert.equal(old(), false); assert.equal(fresh(), true); assert.equal(frame.scope.canLoadForCurrentUser(), true)
  let explicitCurrentWork = 0; if (fresh()) explicitCurrentWork += 1
  assert.equal(explicitCurrentWork, 1, 'fresh client snapshot owns explicit work; this does not certify a server login')
})
