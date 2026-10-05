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

  assert.deepEqual(scheduler.delays(), [1000, 25000])
  assert.equal(transports.at(-1), 'websocket')

  const heartbeat = [...scheduler.timers.values()].find((timer) => timer.delay === 25000)
  heartbeat.fn()
  assert.match(recoveredSocket.sent.at(-1), /"type":"ping"/)

  controller.stop()
  assert.equal(scheduler.timers.size, 0)
})

test('controller stops and closes on terminal status', () => {
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
  assert.equal(sockets[0].closed, true)
  assert.equal(scheduler.timers.size, 0)
})
