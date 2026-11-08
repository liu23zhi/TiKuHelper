import test from 'node:test'
import assert from 'node:assert/strict'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
const webRoot = process.env.TIKU_WEB_SOURCE || path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const {
  REGISTRATION_FLOW_STORAGE_KEY,
  loadWechatRegisterFlowToken,
  saveWechatRegisterFlowToken,
  clearWechatRegisterFlowToken,
  createWechatRegisterFlowCoordinator,
  appendWechatRegisterFlowToken,
} = await import(pathToFileURL(path.join(webRoot, 'src/utils/wechatRegisterFlow.mjs')))

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

test('denied session storage methods keep an in-memory flow without blocking anonymous registration', () => {
  const storage = { getItem() { throw new Error('SYNTHETIC denied') }, setItem() { throw new Error('SYNTHETIC denied') }, removeItem() { throw new Error('SYNTHETIC denied') } }
  assert.equal(loadWechatRegisterFlowToken(storage), '')
  assert.equal(saveWechatRegisterFlowToken(' SYNTHETIC_MEMORY ', storage), 'SYNTHETIC_MEMORY')
  assert.doesNotThrow(() => clearWechatRegisterFlowToken(storage))
  const flow = createWechatRegisterFlowCoordinator({ storage, channelFactory: null })
  flow.save('SYNTHETIC_MEMORY'); assert.equal(flow.token, 'SYNTHETIC_MEMORY')
  flow.clear(); assert.equal(flow.token, ''); flow.dispose()
})

test('unavailable default sessionStorage getter does not prevent creating the public form flow', () => {
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'sessionStorage')
  Object.defineProperty(globalThis, 'sessionStorage', { configurable: true, get() { throw new Error('SYNTHETIC blocked getter') } })
  try {
    const flow = createWechatRegisterFlowCoordinator({ channelFactory: null })
    flow.save('SYNTHETIC_MEMORY'); assert.equal(flow.token, 'SYNTHETIC_MEMORY'); flow.dispose()
  } finally {
    if (previous) Object.defineProperty(globalThis, 'sessionStorage', previous)
    else delete globalThis.sessionStorage
  }
})

test('retired coordinator clear cannot erase a newer flow owned in the same storage', () => {
  const storage = new MemoryStorage()
  const old = createWechatRegisterFlowCoordinator({ storage, channelFactory: null, tabId: 'old' })
  old.save('SYNTHETIC_OLD')
  const current = createWechatRegisterFlowCoordinator({ storage, channelFactory: null, tabId: 'new' })
  current.save('SYNTHETIC_NEW'); old.clear()
  assert.equal(old.token, '')
  assert.equal(current.token, 'SYNTHETIC_NEW')
  assert.equal(loadWechatRegisterFlowToken(storage), 'SYNTHETIC_NEW')
  old.dispose(); current.dispose()
})

test('blocked optional broadcast transport does not disable normal flow persistence', () => {
  const storage = new MemoryStorage()
  const flow = createWechatRegisterFlowCoordinator({ storage, channelFactory: () => { throw new Error('SYNTHETIC broadcast unavailable') } })
  flow.save('SYNTHETIC_FLOW')
  assert.equal(loadWechatRegisterFlowToken(storage), 'SYNTHETIC_FLOW')
  flow.clear(); assert.equal(loadWechatRegisterFlowToken(storage), ''); flow.dispose()
})

test('disposed coordinator cannot save or clear a newer active flow', () => {
  const storage = new MemoryStorage()
  const old = createWechatRegisterFlowCoordinator({ storage, channelFactory: null })
  old.save('SYNTHETIC_OLD'); old.dispose()
  const current = createWechatRegisterFlowCoordinator({ storage, channelFactory: null })
  current.save('SYNTHETIC_NEW')
  old.save('SYNTHETIC_STALE_CALLBACK'); old.clear()
  assert.equal(loadWechatRegisterFlowToken(storage), 'SYNTHETIC_NEW')
  assert.equal(old.token, '')
  current.dispose()
})

test('explicit clear compares the exact local token before touching a newer operation', () => {
  const storage = new MemoryStorage()
  const flow = createWechatRegisterFlowCoordinator({ storage, channelFactory: null })
  flow.save('SYNTHETIC_CURRENT')
  assert.equal(flow.clear('SYNTHETIC_RETIRED'), false)
  assert.equal(flow.token, 'SYNTHETIC_CURRENT')
  assert.equal(loadWechatRegisterFlowToken(storage), 'SYNTHETIC_CURRENT')
  assert.equal(flow.clear('SYNTHETIC_CURRENT'), true)
  assert.equal(flow.token, '')
  assert.equal(loadWechatRegisterFlowToken(storage), '')
  flow.dispose()
})
