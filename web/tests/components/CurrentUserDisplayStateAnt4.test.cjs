const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
const { h, nextTick, isReadonly, effectScope } = require('vue')
const { mount, flushPromises } = require('@vue/test-utils')
const Antd = require('ant-design-vue')
const key = 'adapter_current_user'
const user = require(path.join(root, 'src/utils/currentUser.js'))
const apiPath = path.join(root, 'src/api/TikuComponentApi.js')
const pending = []
let reads = 0
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  getCurrentUser: () => { reads += 1; return new Promise(resolve => pending.push(resolve)) },
  getBillingRecords: () => Promise.resolve({ status: 200, data: { data: [], total: 0 } }),
  getPaymentMethods: () => Promise.resolve({ status: 200, data: [] }),
  createRechargeOrder: () => { throw Error('display fixture cannot create an order') },
}
require.cache[apiPath] = api
const Usage = require(path.join(root, 'src/pages/UserUsage/index.vue'))
const wrappers = []
const initial = { id: 17, role: 'admin', balance: 1.234567, nested: { value: 'original' } }
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
function storage(raw, eventKey = key, area = window.localStorage) {
  if (raw === null) window.localStorage.removeItem(key)
  else window.localStorage.setItem(key, raw)
  window.dispatchEvent(new window.StorageEvent('storage', { key: eventKey, newValue: raw, storageArea: area }))
}
afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) if (wrapper.exists()) wrapper.unmount()
  pending.splice(0)
  await settle()
  window.localStorage.clear()
  Antd.message.destroy()
  document.body.innerHTML = ''
})
test('legacy bytes, arrays, primitive clear, malformed getter, readonly snapshots and shared listener lifetime', async () => {
  assert.equal(typeof user.useCurrentUserDisplay, 'function', 'reactive display composable exists')
  assert.equal(user.setStoredCurrentUser(initial), undefined)
  assert.equal(window.localStorage.getItem(key), JSON.stringify(initial))
  const originalAdd = window.addEventListener
  const originalRemove = window.removeEventListener
  const listeners = new Set()
  window.addEventListener = function(type, fn, ...args) { if (type === 'storage') listeners.add(fn); return originalAdd.call(this, type, fn, ...args) }
  window.removeEventListener = function(type, fn, ...args) { if (type === 'storage') listeners.delete(fn); return originalRemove.call(this, type, fn, ...args) }
  const states = []
  const Consumer = { setup() { const state = user.useCurrentUserDisplay(); states.push(state); return () => h('p', String(state.currentUser.value?.balance || 0)) } }
  try {
    const a = mount(Consumer); wrappers.push(a)
    const b = mount(Consumer); wrappers.push(b)
    assert.equal(listeners.size, 1, 'two consumers share one window storage listener')
    assert.equal(isReadonly(states[0].currentUser), true)
    assert.equal(isReadonly(states[0].currentUser.value), true)
    assert.equal(isReadonly(states[0].currentUser.value.nested), true)
    const warn = console.warn; console.warn = () => {}
    try { states[0].currentUser.value.nested.value = 'mutated'; states[0].currentUser.value = null } finally { console.warn = warn }
    assert.equal(states[0].currentUser.value.nested.value, 'original')
    assert.equal(user.getStoredCurrentUser().nested.value, 'original')
    const guard = states[0].captureUpdateGuard()
    assert.equal(guard(), true)
    user.setStoredCurrentUser({ ...initial, role: 'user', balance: 9.876543 }); await settle()
    assert.equal(guard(), false)
    assert.equal(a.text(), '9.876543'); assert.equal(b.text(), '9.876543')
    a.unmount(); assert.equal(listeners.size, 1)
    const stable = states[1].currentUser.value
    storage(JSON.stringify({ ...initial, balance: 99 }), 'unrelated-key'); await settle()
    assert.equal(states[1].currentUser.value, stable, 'wrong key ignored')
    storage(JSON.stringify({ ...initial, balance: 98 }), key, window.sessionStorage); await settle()
    assert.equal(states[1].currentUser.value, stable, 'other storage area ignored')
    storage(JSON.stringify({ ...initial, role: 'user', balance: 2.345678 })); await settle()
    assert.equal(b.text(), '2.345678')
    storage('{malformed fixture'); await settle()
    assert.equal(states[1].currentUser.value, null)
    assert.equal(window.localStorage.getItem(key), null)
    user.setStoredCurrentUser(initial)
    storage(null); await settle(); assert.equal(b.text(), '0')
    user.setStoredCurrentUser(initial)
    window.localStorage.clear()
    window.dispatchEvent(new window.StorageEvent('storage', { key: null, storageArea: window.localStorage })); await settle()
    assert.equal(b.text(), '0', 'clear-all event clears the display')
    user.setStoredCurrentUser([initial]); await settle()
    assert.deepEqual(user.getStoredCurrentUser(), [initial], 'legacy arrays remain accepted')
    assert.equal(window.localStorage.getItem(key), JSON.stringify([initial]))
    user.setStoredCurrentUser({ toJSON() { return undefined } })
    assert.equal(window.localStorage.getItem(key), 'undefined', 'setter preserves legacy serialization before getter cleanup')
    assert.equal(user.getStoredCurrentUser(), null); assert.equal(window.localStorage.getItem(key), null)
    for (const invalid of [null, undefined, false, 0, '', 'invalid']) {
      assert.equal(user.setStoredCurrentUser(invalid), undefined); assert.equal(user.getStoredCurrentUser(), null)
    }
    window.localStorage.setItem(key, '{broken')
    assert.equal(user.getStoredCurrentUser(), null); assert.equal(window.localStorage.getItem(key), null)
    assert.equal(user.isAdminUser({ role: ' ADMIN ' }), true)
    assert.equal(user.resolveHomePath({ role: 'user' }), '/adapter/user-usage')
    assert.equal(user.clearStoredCurrentUser(), undefined)
    b.unmount(); assert.equal(listeners.size, 0, 'last scope removes listener')
    user.setStoredCurrentUser({ ...initial, balance: 3 })
    const c = mount(Consumer); wrappers.push(c); assert.equal(c.text(), '3'); assert.equal(listeners.size, 1)
    c.unmount(); assert.equal(listeners.size, 0)
    console.log('F03_STATE_CONTRACT_OK readonly-deep legacy-arrays storage-filter clear malformed two-consumers cleanup-remount')
  } finally {
    for (const wrapper of wrappers) if (wrapper.exists()) wrapper.unmount()
    window.addEventListener = originalAdd; window.removeEventListener = originalRemove
  }
})
test('real UserUsage refuses old session, clear, superseded and unmounted responses but accepts current server200', async () => {
  user.setStoredCurrentUser(initial)
  const wrapper = mount(Usage, { attachTo: document.body, global: { plugins: [Antd] } }); wrappers.push(wrapper)
  await settle()
  assert.equal(pending.length, 1)
  const beforeReads = reads
  const changed = { id: 23, role: 'user', balance: 9.876543 }
  user.setStoredCurrentUser(changed); await settle()
  pending.shift()({ status: 200, data: { ...initial, balance: 77 } }); await settle()
  assert.equal(wrapper.find('.balance-value').text(), '¥ 9.876543', 'old account response cannot leak stale balance')
  assert.deepEqual(user.getStoredCurrentUser(), changed)
  assert.equal(reads, beforeReads, 'display update itself does not fetch')
  assert.equal(wrapper.vm.handleRefreshCurrentUser(), undefined, 'old method return remains undefined')
  pending.shift()({ status: 200, data: { ...changed, balance: 2.345678 } }); await settle()
  assert.equal(wrapper.find('.balance-value').text(), '¥ 2.345678', 'current server200 is authoritative')
  wrapper.vm.handleRefreshCurrentUser(); user.clearStoredCurrentUser()
  pending.shift()({ status: 200, data: { ...changed, balance: 66 } }); await settle()
  assert.equal(wrapper.find('.balance-value').text(), '¥ 0'); assert.equal(user.getStoredCurrentUser(), null)
  user.setStoredCurrentUser(changed)
  wrapper.vm.handleRefreshCurrentUser(); wrapper.vm.handleRefreshCurrentUser()
  const oldRequest = pending.shift(); const latestRequest = pending.shift()
  latestRequest({ status: 200, data: { ...changed, balance: 4.567891 } }); await settle()
  oldRequest({ status: 200, data: { ...changed, balance: 55 } }); await settle()
  assert.equal(wrapper.find('.balance-value').text(), '¥ 4.567891', 'superseded request cannot overwrite the latest')
  wrapper.vm.handleRefreshCurrentUser(); const unmounted = pending.shift(); wrapper.unmount()
  unmounted({ status: 200, data: { ...changed, balance: 44 } }); await settle()
  assert.equal(user.getStoredCurrentUser().balance, 4.567891, 'unmounted page cannot publish late result')
  console.log('F03_LATE_SERVER_OK old-account clear superseded unmounted blocked; current-server200 authoritative; balance6 recharge2 unchanged')
})

test('legacy getter objects and arrays remain mutable detached copies; display keeps raw JSON nonfinite semantics', () => {
  assert.equal(typeof user.useCurrentUserDisplay, 'function')
  user.setStoredCurrentUser(initial)
  const scope = effectScope()
  const state = scope.run(() => user.useCurrentUserDisplay())
  try {
    const raw = JSON.stringify({ id: 23, role: 'user', balance: 8.654321, nested: { role: 'user' } })
    window.localStorage.setItem(key, raw)
    const legacy = user.getStoredCurrentUser()
    assert.equal(isReadonly(legacy), false, 'legacy getter keeps its mutable return API')
    legacy.balance = 999; legacy.nested.role = 'admin'
    assert.equal(state.currentUser.value.balance, 8.654321)
    assert.equal(state.currentUser.value.nested.role, 'user')
    assert.equal(window.localStorage.getItem(key), raw)
    const arrayRaw = JSON.stringify([{ id: 24, nested: { value: 'original' } }])
    window.localStorage.setItem(key, arrayRaw)
    const legacyArray = user.getStoredCurrentUser()
    legacyArray[0].nested.value = 'mutated'; legacyArray.push({ id: 99 })
    assert.equal(state.currentUser.value.length, 1)
    assert.equal(state.currentUser.value[0].nested.value, 'original')
    assert.equal(window.localStorage.getItem(key), arrayRaw)
    window.localStorage.setItem(key, '{"id":24,"balance":1e999}')
    const nonfinite = user.getStoredCurrentUser()
    assert.equal(nonfinite.balance, Infinity)
    assert.equal(state.currentUser.value.balance, Infinity, 'do not stringify-clone Infinity into null')
    nonfinite.balance = 999; assert.equal(state.currentUser.value.balance, Infinity)
    window.localStorage.setItem(key, '1e999')
    assert.equal(user.getStoredCurrentUser(), Infinity); assert.equal(state.currentUser.value, Infinity)
    const before = window.localStorage.getItem(key)
    const circular = {}; circular.self = circular
    assert.throws(() => user.setStoredCurrentUser(circular), TypeError, 'legacy serialization throw unchanged')
    assert.equal(window.localStorage.getItem(key), before)
    console.log('F03_GETTER_DETACHED_OK object nested array mutable-legacy readonly-snapshot raw-Infinity serialization-throw')
  } finally { scope.stop() }
})
