export const MAX_WECHAT_QR_TTL_SECONDS = 180

export function resolveWechatQrExpiresAt(payload, nowMs = Date.now()) {
  const explicitExpiry = Number(payload?.expiresAtUnix)
  if (Number.isFinite(explicitExpiry) && explicitExpiry > 0) {
    return explicitExpiry
  }
  const expireSeconds = Number(payload?.expireSeconds)
  const ttlSeconds = Number.isFinite(expireSeconds) && expireSeconds > 0
    ? Math.min(expireSeconds, MAX_WECHAT_QR_TTL_SECONDS)
    : MAX_WECHAT_QR_TTL_SECONDS
  return nowMs + ttlSeconds * 1000
}

export function buildWechatLoginWebSocketURL(loginToken, locationLike = window.location) {
  const protocol = locationLike.protocol === 'https:' ? 'wss:' : 'ws:'
  const token = encodeURIComponent(String(loginToken || '').trim())
  return `${protocol}//${locationLike.host}/adapter-service/public/wechat-login/ws?loginToken=${token}`
}

export function createWechatQrStatusController({
  checkStatus,
  onStatus = () => {},
  onTransport = () => {},
  onRemaining = () => {},
  onExpired = () => {},
  createWebSocket = (url) => new WebSocket(url),
  buildWebSocketURL = buildWechatLoginWebSocketURL,
  setIntervalFn = setInterval,
  clearIntervalFn = clearInterval,
  now = () => Date.now(),
  pollIntervalMs = 2000,
  wsRecoveryIntervalMs = 30000,
  heartbeatIntervalMs = 25000,
  maxReconnectAttempts = 3,
} = {}) {
  let stopped = true
  let session = null
  let socket = null
  let reconnectFailures = 0
  let socketOpened = false
  let pollTimer = null
  let recoveryTimer = null
  let heartbeatTimer = null
  let countdownTimer = null
  let lastStatus = ''
  let lastRemaining = null

  const clearTimer = (timer) => {
    if (timer !== null) {
      clearIntervalFn(timer)
    }
  }

  const stopHeartbeat = () => {
    clearTimer(heartbeatTimer)
    heartbeatTimer = null
  }

  const stopPolling = () => {
    clearTimer(pollTimer)
    clearTimer(recoveryTimer)
    pollTimer = null
    recoveryTimer = null
  }

  const emitStatus = (status, payload = {}) => {
    const normalized = String(status || '').trim()
    if (!normalized || normalized === lastStatus) {
      return
    }
    lastStatus = normalized
    Promise.resolve(onStatus(normalized, payload)).catch(() => {})
  }

  const stop = () => {
    stopped = true
    clearTimer(pollTimer)
    clearTimer(recoveryTimer)
    clearTimer(heartbeatTimer)
    clearTimer(countdownTimer)
    pollTimer = null
    recoveryTimer = null
    heartbeatTimer = null
    countdownTimer = null
    session = null
    if (socket) {
      socket.onopen = null
      socket.onmessage = null
      socket.onerror = null
      socket.onclose = null
      socket.close()
      socket = null
    }
  }

  const handleStatus = (status, payload = {}) => {
    emitStatus(status, payload)
    if (status === 'success' || status === 'need_bind') {
      stop()
      return
    }
    if (status === 'expired') {
      onExpired()
      stop()
    }
  }

  const pollOnce = async () => {
    if (stopped) {
      return
    }
    try {
      const response = await checkStatus()
      if (response?.status !== 200) {
        return
      }
      handleStatus(response?.data?.status, response?.data || {})
    } catch (_) {
      // A transient polling failure is retried by the interval.
    }
  }

  const connectWebSocket = () => {
    if (stopped || !session || socket) {
      return
    }
    let nextSocket
    try {
      nextSocket = createWebSocket(buildWebSocketURL(session.loginToken))
    } catch (_) {
      handleSocketClosed(null)
      return
    }
    socket = nextSocket
    socketOpened = false

    nextSocket.onopen = () => {
      if (stopped || socket !== nextSocket) {
        return
      }
      socketOpened = true
      reconnectFailures = 0
      stopPolling()
      onTransport('websocket')
      nextSocket.send(JSON.stringify({ type: 'ping', timestamp: now() }))
      stopHeartbeat()
      heartbeatTimer = setIntervalFn(() => {
        if (socket === nextSocket && (nextSocket.readyState === undefined || nextSocket.readyState === 1)) {
          nextSocket.send(JSON.stringify({ type: 'ping', timestamp: now() }))
        }
      }, heartbeatIntervalMs)
    }

    nextSocket.onmessage = (event) => {
      let message
      try {
        message = JSON.parse(event?.data || '{}')
      } catch (_) {
        return
      }
      if (message.type !== 'status') {
        return
      }
      const remainingSeconds = Number(message.remainingSeconds)
      if (session && Number.isFinite(remainingSeconds) && remainingSeconds >= 0) {
        session.expiresAtUnix = now() + remainingSeconds * 1000
        updateCountdown()
      }
      handleStatus(message.status, message)
    }

    nextSocket.onerror = () => {}
    nextSocket.onclose = () => handleSocketClosed(nextSocket)
  }

  const startPolling = () => {
    if (stopped || pollTimer !== null) {
      return
    }
    onTransport('polling')
    void pollOnce()
    pollTimer = setIntervalFn(() => void pollOnce(), pollIntervalMs)
    recoveryTimer = setIntervalFn(() => {
      if (stopped || socket) {
        return
      }
      reconnectFailures = 0
      connectWebSocket()
    }, wsRecoveryIntervalMs)
  }

  const handleSocketClosed = (closedSocket) => {
    if (closedSocket && socket !== closedSocket) {
      return
    }
    socket = null
    stopHeartbeat()
    if (stopped || !session) {
      return
    }
    if (socketOpened) {
      socketOpened = false
      reconnectFailures = 0
      connectWebSocket()
      return
    }
    reconnectFailures += 1
    if (reconnectFailures <= maxReconnectAttempts) {
      connectWebSocket()
      return
    }
    startPolling()
  }

  const updateCountdown = () => {
    if (stopped || !session) {
      return
    }
    const remaining = Math.max(0, Math.ceil((session.expiresAtUnix - now()) / 1000))
    if (remaining !== lastRemaining) {
      lastRemaining = remaining
      onRemaining(remaining)
    }
    if (remaining <= 0) {
      handleStatus('expired', { status: 'expired' })
    }
  }

  const start = (nextSession) => {
    stop()
    stopped = false
    session = {
      loginToken: String(nextSession?.loginToken || '').trim(),
      expiresAtUnix: resolveWechatQrExpiresAt(nextSession, now()),
    }
    reconnectFailures = 0
    socketOpened = false
    lastStatus = ''
    lastRemaining = null
    if (!session.loginToken) {
      stopped = true
      throw new Error('微信登录令牌为空')
    }
    connectWebSocket()
    updateCountdown()
    if (stopped) {
      return
    }
    countdownTimer = setIntervalFn(updateCountdown, 1000)
  }

  return { start, stop }
}
