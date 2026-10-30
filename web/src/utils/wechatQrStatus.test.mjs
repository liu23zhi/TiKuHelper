import test from 'node:test'
import assert from 'node:assert/strict'
import {
  buildWechatLoginWebSocketURL,
  createWechatQrStatusController,
  resolveWechatQrExpiresAt,
} from './wechatQrStatus.mjs'

function createFakeScheduler() {
  let nextId = 1
  const timers = new Map()
  return {
    timers,
    setIntervalFn(fn, delay) {
      const id = nextId
      nextId += 1
      timers.set(id, { fn, delay })
      return id
    },
    clearIntervalFn(id) {
      timers.delete(id)
    },
    delays() {
      return [...timers.values()].map((timer) => timer.delay)
    },
  }
}

function createFakeSocket(sockets) {
  const socket = {
    readyState: 1,
    sent: [],
    closed: false,
    onopen: null,
    onmessage: null,
    onerror: null,
    onclose: null,
    send(payload) {
      this.sent.push(payload)
    },
    close() {
      this.closed = true
    },
  }
  sockets.push(socket)
  return socket
}

test('resolveWechatQrExpiresAt caps fallback expiry at 180 seconds', () => {
  assert.equal(resolveWechatQrExpiresAt({ expireSeconds: 600 }, 1000), 181000)
  assert.equal(resolveWechatQrExpiresAt({ expiresAtUnix: 123456 }, 1000), 123456)
})

test('buildWechatLoginWebSocketURL uses wss on https pages', () => {
  assert.equal(
    buildWechatLoginWebSocketURL('token a', { protocol: 'https:', host: 'example.com' }),
    'wss://example.com/adapter-service/public/wechat-login/ws?loginToken=token%20a',
  )
})

test('controller retries immediately and falls back after three reconnect attempts', () => {
  const sockets = []
  const scheduler = createFakeScheduler()
  const controller = createWechatQrStatusController({
    checkStatus: async () => ({ status: 200, data: { status: 'pending' } }),
    buildWebSocketURL: (token) => `ws://example.test/${token}`,
    createWebSocket: () => createFakeSocket(sockets),
    setIntervalFn: scheduler.setIntervalFn,
    clearIntervalFn: scheduler.clearIntervalFn,
    now: () => 0,
  })

  controller.start({ loginToken: 'token', expiresAtUnix: 180000 })
  assert.equal(sockets.length, 1)

  sockets[0].onclose()
  sockets[1].onclose()
  sockets[2].onclose()
  sockets[3].onclose()

  assert.equal(sockets.length, 4)
  assert.deepEqual(scheduler.delays(), [1000, 2000, 30000])
  controller.stop()
  assert.equal(scheduler.timers.size, 0)
})

test('controller recovers websocket, stops polling, and sends heartbeat', () => {
  const sockets = []
  const scheduler = createFakeScheduler()
  const transports = []
  const controller = createWechatQrStatusController({
    checkStatus: async () => ({ status: 200, data: { status: 'pending' } }),
    onTransport: (transport) => transports.push(transport),
    buildWebSocketURL: (token) => `ws://example.test/${token}`,
    createWebSocket: () => createFakeSocket(sockets),
    setIntervalFn: scheduler.setIntervalFn,
    clearIntervalFn: scheduler.clearIntervalFn,
    now: () => 0,
  })

  controller.start({ loginToken: 'token', expiresAtUnix: 180000 })
  sockets[0].onclose()
  sockets[1].onclose()
  sockets[2].onclose()
  sockets[3].onclose()
  assert.deepEqual(scheduler.delays(), [1000, 2000, 30000])

  const recovery = [...scheduler.timers.values()].find((timer) => timer.delay === 30000)
  recovery.fn()
  const recoveredSocket = sockets.at(-1)
  recoveredSocket.onopen()
  assert.ok(scheduler.delays().includes(2000), 'keep polling until the recovered socket delivers a status')
  recoveredSocket.onmessage({ data: JSON.stringify({ type: 'status', status: 'pending', remainingSeconds: 180 }) })

  assert.deepEqual(scheduler.delays(), [1000, 1000, 2000])
  assert.equal(transports.at(-1), 'websocket')

  const heartbeatTimers = [...scheduler.timers.values()].filter((timer) => timer.delay === 1000)
  assert.equal(heartbeatTimers.length, 2, 'countdown and heartbeat both use one-second timers')
  const heartbeat = heartbeatTimers.at(-1)
  heartbeat.fn()
  assert.match(recoveredSocket.sent.at(-1), /"type":"ping"/)

  controller.stop()
  assert.equal(scheduler.timers.size, 0)
})

test('controller falls back when the websocket handshake or status delivery stalls', async () => {
  for (const opened of [false, true]) {
    const sockets = []
    const scheduler = createFakeScheduler()
    const transports = []
    let polls = 0
    const controller = createWechatQrStatusController({
      checkStatus: async () => { polls += 1; return { status: 200, data: { status: 'pending' } } },
      onTransport: (transport) => transports.push(transport),
      buildWebSocketURL: (token) => `ws://example.test/${token}`,
      createWebSocket: () => createFakeSocket(sockets),
      setIntervalFn: scheduler.setIntervalFn,
      clearIntervalFn: scheduler.clearIntervalFn,
      now: () => 0,
    })
    controller.start({ loginToken: 'token', expiresAtUnix: 180000 })
    if (opened) {
      sockets[0].onopen()
      sockets[0].onmessage({ data: JSON.stringify({ type: 'status', status: 'pending' }) })
    }
    const watchdog = [...scheduler.timers.values()].find((timer) => timer.delay === 2000)
    watchdog.fn()
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(polls, 1)
    assert.equal(sockets[0].closed, true)
    assert.equal(transports.at(-1), 'polling')
    assert.ok(scheduler.delays().includes(30000), 'retain periodic websocket recovery')
    controller.stop()
  }
})

test('controller bounds repeated websocket open-close cycles before polling', () => {
  const sockets = []
  const scheduler = createFakeScheduler()
  const controller = createWechatQrStatusController({
    checkStatus: async () => ({ status: 200, data: { status: 'pending' } }),
    buildWebSocketURL: (token) => `ws://example.test/${token}`,
    createWebSocket: () => createFakeSocket(sockets),
    setIntervalFn: scheduler.setIntervalFn,
    clearIntervalFn: scheduler.clearIntervalFn,
    now: () => 0,
  })
  controller.start({ loginToken: 'token', expiresAtUnix: 180000 })
  for (let attempt = 0; attempt < 4; attempt += 1) {
    sockets[attempt].onopen()
    sockets[attempt].onclose()
  }
  assert.equal(sockets.length, 4)
  assert.deepEqual(scheduler.delays(), [1000, 2000, 30000])
  controller.stop()
})

test('malformed websocket statuses cannot stop polling or reset its recovery deadline', () => {
  const sockets = []
  const scheduler = createFakeScheduler()
  const controller = createWechatQrStatusController({
    checkStatus: async () => ({ status: 200, data: { status: 'pending' } }),
    buildWebSocketURL: (token) => `ws://example.test/${token}`,
    createWebSocket: () => createFakeSocket(sockets),
    setIntervalFn: scheduler.setIntervalFn,
    clearIntervalFn: scheduler.clearIntervalFn,
    now: () => 0,
    maxReconnectAttempts: 0,
  })
  controller.start({ loginToken: 'token', expiresAtUnix: 180000 })
  sockets[0].onclose()
  const recovery = [...scheduler.timers.values()].find((timer) => timer.delay === 30000)
  recovery.fn()
  const recovered = sockets.at(-1)
  recovered.onopen()
  const watchdogTimers = [...scheduler.timers.values()].filter((timer) => timer.delay === 2000)
  assert.equal(watchdogTimers.length, 2, 'polling and websocket watchdog both use the two-second cadence')
  const watchdog = watchdogTimers.at(-1)
  for (const payload of [{ type: 'status' }, { type: 'status', status: 'unknown' }, { type: 'pong' }]) {
    recovered.onmessage({ data: JSON.stringify(payload) })
    assert.ok(scheduler.delays().includes(2000))
    assert.ok([...scheduler.timers.values()].includes(watchdog))
  }
  controller.stop()
})

test('controller reports terminal callback errors and recovers through polling', async () => {
  const sockets = []
  const scheduler = createFakeScheduler()
  const errors = []
  let attempts = 0
  const controller = createWechatQrStatusController({
    checkStatus: async () => ({ status: 200, data: { status: 'success', jwt: 'resolved-jwt' } }),
    onStatus: async () => { attempts += 1; if (attempts === 1) throw new Error('temporary exchange failure'); return true },
    onError: (error) => errors.push(error.message),
    buildWebSocketURL: (token) => `ws://example.test/${token}`,
    createWebSocket: () => createFakeSocket(sockets),
    setIntervalFn: scheduler.setIntervalFn,
    clearIntervalFn: scheduler.clearIntervalFn,
    now: () => 0,
  })
  controller.start({ loginToken: 'token', expiresAtUnix: 180000 })
  sockets[0].onmessage({ data: JSON.stringify({ type: 'status', status: 'success' }) })
  await new Promise((resolve) => setImmediate(resolve))
  assert.deepEqual(errors, ['temporary exchange failure'])
  assert.equal(attempts, 2)
  assert.equal(scheduler.timers.size, 0)
})

test('controller completes an accepted scan even when the remaining seconds round to zero', async () => {
  const sockets = []
  const scheduler = createFakeScheduler()
  const statuses = []
  let clock = 0
  let complete
  const controller = createWechatQrStatusController({
    checkStatus: async () => ({ status: 200, data: { status: 'pending' } }),
    onStatus: (status) => { statuses.push(status); return new Promise((resolve) => { complete = resolve }) },
    onExpired: () => statuses.push('expired'),
    buildWebSocketURL: (token) => `ws://example.test/${token}`,
    createWebSocket: () => createFakeSocket(sockets),
    setIntervalFn: scheduler.setIntervalFn,
    clearIntervalFn: scheduler.clearIntervalFn,
    now: () => clock,
  })
  controller.start({ loginToken: 'token', expiresAtUnix: 180000 })
  sockets[0].onmessage({ data: JSON.stringify({ type: 'status', status: 'success', remainingSeconds: 0 }) })
  clock = 180000
  const countdown = [...scheduler.timers.values()].find((timer) => timer.delay === 1000)
  countdown.fn()
  assert.deepEqual(statuses, ['success'])
  assert.equal(sockets[0].closed, false)
  complete(true)
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(sockets[0].closed, true)
  assert.equal(scheduler.timers.size, 0)
})

test('controller stops and closes after terminal handling succeeds', async () => {
  const sockets = []
  const scheduler = createFakeScheduler()
  const statuses = []
  const controller = createWechatQrStatusController({
    checkStatus: async () => ({ status: 200, data: { status: 'pending' } }),
    onStatus: (status) => statuses.push(status),
    buildWebSocketURL: (token) => `ws://example.test/${token}`,
    createWebSocket: () => createFakeSocket(sockets),
    setIntervalFn: scheduler.setIntervalFn,
    clearIntervalFn: scheduler.clearIntervalFn,
    now: () => 0,
  })

  controller.start({ loginToken: 'token', expiresAtUnix: 180000 })
  sockets[0].onmessage({ data: JSON.stringify({ type: 'status', status: 'need_bind' }) })

  assert.deepEqual(statuses, ['need_bind'])
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(sockets[0].closed, true)
  assert.equal(scheduler.timers.size, 0)
})

test('controller retries terminal resolution failures and runs one completion at a time', async () => {
  const sockets = []
  const scheduler = createFakeScheduler()
  let attempts = 0
  let finishFirst
  const statuses = []
  const controller = createWechatQrStatusController({
    checkStatus: async () => ({ status: 200, data: { status: 'need_bind', bindToken: 'bound-result' } }),
    onStatus: (status) => {
      statuses.push(status)
      attempts += 1
      if (attempts === 1) return new Promise((resolve) => { finishFirst = resolve })
      return true
    },
    buildWebSocketURL: (token) => `ws://example.test/${token}`,
    createWebSocket: () => createFakeSocket(sockets),
    setIntervalFn: scheduler.setIntervalFn,
    clearIntervalFn: scheduler.clearIntervalFn,
    now: () => 0,
  })
  controller.start({ loginToken: 'token', expiresAtUnix: 180000 })
  sockets[0].onopen()
  sockets[0].onmessage({ data: JSON.stringify({ type: 'status', status: 'need_bind' }) })
  sockets[0].onmessage({ data: JSON.stringify({ type: 'status', status: 'need_bind' }) })
  assert.equal(attempts, 1)
  assert.equal(sockets[0].closed, false)
  sockets[0].onclose()
  assert.equal(sockets.length, 1)
  finishFirst(false)
  await new Promise((resolve) => setImmediate(resolve))
  assert.deepEqual(statuses, ['need_bind', 'need_bind'])
  assert.equal(scheduler.timers.size, 0)
})

test('controller ignores old polling results and terminal completion after a new QR session starts', async () => {
  const sockets = []
  const scheduler = createFakeScheduler()
  let resolveOldPoll
  let resolveOldCompletion
  const statuses = []
  const controller = createWechatQrStatusController({
    checkStatus: () => new Promise((resolve) => { resolveOldPoll = resolve }),
    onStatus: (status) => {
      statuses.push(status)
      return new Promise((resolve) => { resolveOldCompletion = resolve })
    },
    buildWebSocketURL: (token) => `ws://example.test/${token}`,
    createWebSocket: () => createFakeSocket(sockets),
    setIntervalFn: scheduler.setIntervalFn,
    clearIntervalFn: scheduler.clearIntervalFn,
    now: () => 0,
    maxReconnectAttempts: 0,
  })
  controller.start({ loginToken: 'old-poll', expiresAtUnix: 180000 })
  sockets[0].onclose()
  controller.start({ loginToken: 'new', expiresAtUnix: 180000 })
  resolveOldPoll({ status: 200, data: { status: 'success', jwt: 'old-jwt' } })
  await new Promise((resolve) => setImmediate(resolve))
  assert.deepEqual(statuses, [])
  const activeSocket = sockets.at(-1)
  activeSocket.onmessage({ data: JSON.stringify({ type: 'status', status: 'need_bind' }) })
  assert.deepEqual(statuses, ['need_bind'])
  controller.start({ loginToken: 'newest', expiresAtUnix: 180000 })
  const newestSocket = sockets.at(-1)
  resolveOldCompletion(true)
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(newestSocket.closed, false)
  assert.ok(scheduler.timers.size > 0)
  controller.stop()
})
