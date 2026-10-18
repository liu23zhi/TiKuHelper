import test from 'node:test'
import assert from 'node:assert/strict'
import {
  REGISTRATION_FLOW_STORAGE_KEY,
  loadWechatRegisterFlowToken,
  saveWechatRegisterFlowToken,
  clearWechatRegisterFlowToken,
  createWechatRegisterFlowCoordinator,
  appendWechatRegisterFlowToken,
} from './wechatRegisterFlow.mjs'

class MemoryStorage {
  constructor(entries = {}) {
    this.values = new Map(Object.entries(entries))
  }

  getItem(key) {
    return this.values.has(key) ? this.values.get(key) : null
  }

  setItem(key, value) {
    this.values.set(key, String(value))
  }

  removeItem(key) {
    this.values.delete(key)
  }
}

class FakeChannel {
  static channels = new Map()

  constructor(name) {
    this.name = name
    this.listeners = new Set()
    const peers = FakeChannel.channels.get(name) || new Set()
    peers.add(this)
    FakeChannel.channels.set(name, peers)
  }

  addEventListener(type, listener) {
    if (type === 'message') this.listeners.add(listener)
  }

  removeEventListener(type, listener) {
    if (type === 'message') this.listeners.delete(listener)
  }

  postMessage(data) {
    for (const peer of FakeChannel.channels.get(this.name) || []) {
      if (peer === this) continue
      for (const listener of peer.listeners) listener({ data })
    }
  }

  close() {
    FakeChannel.channels.get(this.name)?.delete(this)
  }
}

test('flow token is persisted in session storage and can be cleared', () => {
  const storage = new MemoryStorage()
  assert.equal(loadWechatRegisterFlowToken(storage), '')
  saveWechatRegisterFlowToken(' opaque-token ', storage)
  assert.equal(storage.getItem(REGISTRATION_FLOW_STORAGE_KEY), 'opaque-token')
  assert.equal(loadWechatRegisterFlowToken(storage), 'opaque-token')
  clearWechatRegisterFlowToken(storage)
  assert.equal(loadWechatRegisterFlowToken(storage), '')
})

test('same tab coordinator resumes an existing token without creating another one', () => {
  const storage = new MemoryStorage({ [REGISTRATION_FLOW_STORAGE_KEY]: 'flow-1' })
  const coordinator = createWechatRegisterFlowCoordinator({ storage, channelFactory: null, tabId: 'tab-a' })
  assert.equal(coordinator.token, 'flow-1')
  assert.equal(loadWechatRegisterFlowToken(storage), 'flow-1')
  coordinator.dispose()
})

test('duplicate tabs deterministically keep the lower tab id and clear the other token', () => {
  const storageA = new MemoryStorage({ [REGISTRATION_FLOW_STORAGE_KEY]: 'flow-2' })
  const storageB = new MemoryStorage({ [REGISTRATION_FLOW_STORAGE_KEY]: 'flow-2' })
  const coordinatorB = createWechatRegisterFlowCoordinator({
    storage: storageB,
    channelFactory: (name) => new FakeChannel(name),
    tabId: 'tab-b',
  })
  const coordinatorA = createWechatRegisterFlowCoordinator({
    storage: storageA,
    channelFactory: (name) => new FakeChannel(name),
    tabId: 'tab-a',
  })

  assert.equal(coordinatorA.token, 'flow-2')
  assert.equal(coordinatorB.token, '')
  assert.equal(loadWechatRegisterFlowToken(storageA), 'flow-2')
  assert.equal(loadWechatRegisterFlowToken(storageB), '')
  coordinatorA.dispose()
  coordinatorB.dispose()
})

test('a later tab receives a claim when the lower-id tab started first', () => {
  const storageA = new MemoryStorage({ [REGISTRATION_FLOW_STORAGE_KEY]: 'flow-4' })
  const storageB = new MemoryStorage({ [REGISTRATION_FLOW_STORAGE_KEY]: 'flow-4' })
  const coordinatorA = createWechatRegisterFlowCoordinator({
    storage: storageA,
    channelFactory: (name) => new FakeChannel(name),
    tabId: 'tab-a',
  })
  const coordinatorB = createWechatRegisterFlowCoordinator({
    storage: storageB,
    channelFactory: (name) => new FakeChannel(name),
    tabId: 'tab-b',
  })

  assert.equal(coordinatorA.token, 'flow-4')
  assert.equal(coordinatorB.token, '')
  assert.equal(loadWechatRegisterFlowToken(storageB), '')
  coordinatorA.dispose()
  coordinatorB.dispose()
})

test('registration payload includes a token only for a staged flow', () => {
  const payload = { username: 'user', password: 'secret' }
  assert.deepEqual(appendWechatRegisterFlowToken(payload, ''), payload)
  assert.deepEqual(appendWechatRegisterFlowToken(payload, ' flow-3 '), {
    ...payload,
    registrationFlowToken: 'flow-3',
  })
  assert.equal(Object.hasOwn(appendWechatRegisterFlowToken(payload, ''), 'registrationFlowToken'), false)
})
