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
  onError = () => {},
  createWebSocket = (url) => new WebSocket(url),
  buildWebSocketURL = buildWechatLoginWebSocketURL,
  setIntervalFn = setInterval,
  clearIntervalFn = clearInterval,
  now = () => Date.now(),
  pollIntervalMs = 2000,
  wsRecoveryIntervalMs = 30000,
  heartbeatIntervalMs = 500,
  wsStatusTimeoutMs = 2000,
  maxReconnectAttempts = 3,
} = {}) {
  let stopped = true
  let session = null
  let socket = null
  let reconnectFailures = 0
  let pollTimer = null
  let recoveryTimer = null
  let heartbeatTimer = null
  let countdownTimer = null
  let statusTimer = null
  let lastStatus = ''
  let lastRemaining = null
  let resolvingTerminal = null
  let pollingSession = null
  let lastHeartbeatAt = 0

  const clearTimer = (timer) => {
    if (timer !== null) {
      clearIntervalFn(timer)
    }
  }

  const stopHeartbeat = () => {
    clearTimer(heartbeatTimer)
    heartbeatTimer = null
    lastHeartbeatAt = 0
  }

  const stopStatusWatchdog = () => {
    clearTimer(statusTimer)
    statusTimer = null
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
    return onStatus(normalized, payload)
  }

  const stop = () => {
    stopped = true
    clearTimer(pollTimer)
    clearTimer(recoveryTimer)
    clearTimer(heartbeatTimer)
    clearTimer(countdownTimer)
    stopStatusWatchdog()
    pollTimer = null
    recoveryTimer = null
    heartbeatTimer = null
    countdownTimer = null
    resolvingTerminal = null
    pollingSession = null
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
    if (stopped || !session) return
    const activeSession = session
    if (resolvingTerminal === activeSession) return
    const normalized = String(status || '').trim()
    if (normalized === 'success' || normalized === 'need_bind') {
      resolvingTerminal = activeSession
      const retryResolution = () => {
        if (stopped || session !== activeSession) return
        resolvingTerminal = null
        lastStatus = ''
        startPolling()
      }
      let resolution
      try {
        resolution = emitStatus(normalized, payload)
      } catch (error) {
        onError(error)
        retryResolution()
        return
      }
      Promise.resolve(resolution).then((completed) => {
        if (stopped || session !== activeSession) return
        if (completed === false) retryResolution()
        else stop()
      }).catch((error) => {
        if (stopped || session !== activeSession) return
        onError(error)
        retryResolution()
      })
      return
    }
    try {
      Promise.resolve(emitStatus(normalized, payload)).catch((error) => {
        if (!stopped && session === activeSession) onError(error)
      })
    } catch (error) {
      onError(error)
    }
    if (normalized === 'expired') {
      onExpired()
      stop()
    }
  }

  const pollOnce = async () => {
    if (stopped || !session || pollingSession === session || resolvingTerminal === session) {
      return
    }
    const activeSession = session
    pollingSession = activeSession
    try {
      const response = await checkStatus()
      if (stopped || session !== activeSession) return
      if (response?.status !== 200) {
        onError(new Error(response?.message || '微信状态查询失败，正在重试'))
        return
      }
      handleStatus(response?.data?.status, response?.data || {})
    } catch (error) {
      if (!stopped && session === activeSession) onError(error)
    } finally {
      if (pollingSession === activeSession) pollingSession = null
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
    const resetStatusWatchdog = () => {
      stopStatusWatchdog()
      statusTimer = setIntervalFn(() => {
        if (stopped || socket !== nextSocket) return
        if (nextSocket.readyState === 1 && now() - lastHeartbeatAt < wsStatusTimeoutMs) return
        nextSocket.onopen = null
        nextSocket.onmessage = null
        nextSocket.onclose = null
        nextSocket.onerror = null
        nextSocket.close()
        socket = null
        stopHeartbeat()
        stopStatusWatchdog()
        startPolling()
        handleSocketClosed(null)
      }, wsStatusTimeoutMs)
    }
    resetStatusWatchdog()

    nextSocket.onopen = () => {
      if (stopped || socket !== nextSocket) {
        return
      }
      nextSocket.send(JSON.stringify({ type: 'ping', timestamp: now() }))
      lastHeartbeatAt = now()
      stopHeartbeat()
      heartbeatTimer = setIntervalFn(() => {
        if (socket === nextSocket && (nextSocket.readyState === undefined || nextSocket.readyState === 1)) {
          nextSocket.send(JSON.stringify({ type: 'ping', timestamp: now() }))
        }
      }, heartbeatIntervalMs)
    }

    nextSocket.onmessage = (event) => {
      if (stopped || socket !== nextSocket) return
      let message
      try {
        message = JSON.parse(event?.data || '{}')
      } catch (_) {
        return
      }
      if (message.type !== 'status' || !['pending', 'scanned', 'success', 'need_bind', 'expired'].includes(message.status)) {
        if (message.type === 'pong') lastHeartbeatAt = now()
        return
      }
      stopPolling()
      onTransport('websocket')
      if (message.status === 'success' || message.status === 'need_bind') {
        handleStatus(message.status, message)
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
    stopStatusWatchdog()
    if (stopped || !session) {
      return
    }
    if (resolvingTerminal === session) return
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
    if (remaining <= 0 && resolvingTerminal !== session) {
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
