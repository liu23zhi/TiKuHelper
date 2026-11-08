export const REGISTRATION_FLOW_STORAGE_KEY = 'tikuAdapter.registrationFlowToken'
const CHANNEL_NAME = 'tikuAdapter.wechat-register-flow'

function defaultStorage() {
  try { return typeof globalThis !== 'undefined' ? globalThis.sessionStorage || null : null }
  catch { return null }
}

function normalizeToken(token) {
  return String(token || '').trim()
}

export function loadWechatRegisterFlowToken(storage = defaultStorage()) {
  if (!storage || typeof storage.getItem !== 'function') return ''
  try { return normalizeToken(storage.getItem(REGISTRATION_FLOW_STORAGE_KEY)) } catch { return '' }
}

export function saveWechatRegisterFlowToken(token, storage = defaultStorage()) {
  const normalized = normalizeToken(token)
  if (!storage) return normalized
  try {
    if (normalized && typeof storage.setItem === 'function') storage.setItem(REGISTRATION_FLOW_STORAGE_KEY, normalized)
    else if (typeof storage.removeItem === 'function') storage.removeItem(REGISTRATION_FLOW_STORAGE_KEY)
  } catch { /* Memory-only flow remains usable when storage is denied. */ }
  return normalized
}

export function clearWechatRegisterFlowToken(storage = defaultStorage()) {
  try {
    if (storage && typeof storage.removeItem === 'function') storage.removeItem(REGISTRATION_FLOW_STORAGE_KEY)
  } catch { /* Explicit local cleanup must not block the public form. */ }
}

function clearOwnedToken(token, storage) {
  if (!storage || typeof storage.getItem !== 'function') return
  try {
    if (normalizeToken(storage.getItem(REGISTRATION_FLOW_STORAGE_KEY)) === token && typeof storage.removeItem === 'function') {
      storage.removeItem(REGISTRATION_FLOW_STORAGE_KEY)
    }
  } catch { /* Unknown storage ownership cannot justify deleting a newer token. */ }
}

function createTabId() {
  if (typeof globalThis !== 'undefined' && globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID()
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
}

function makeChannel(channelFactory) {
  try {
    if (channelFactory === null) return null
    if (typeof channelFactory === 'function') return channelFactory(CHANNEL_NAME)
    if (typeof globalThis !== 'undefined' && typeof globalThis.BroadcastChannel === 'function') {
      return new globalThis.BroadcastChannel(CHANNEL_NAME)
    }
  } catch { /* Optional browser coordination can be unavailable; server leases still apply. */ }
  return null
}

/**
 * Coordinates the opaque flow token in one browser tab. A copied tab that
 * starts with the same token loses a deterministic lexicographic election.
 */
export function createWechatRegisterFlowCoordinator({
  storage = defaultStorage(),
  channelFactory,
  tabId = createTabId(),
  onDuplicate,
} = {}) {
  let token = loadWechatRegisterFlowToken(storage)
  const channel = makeChannel(channelFactory)
  let disposed = false

  const clearDuplicate = () => {
    const previous = token
    token = ''
    clearOwnedToken(previous, storage)
    if (typeof onDuplicate === 'function') onDuplicate()
  }

  const onMessage = (event) => {
    const data = event?.data
    if (disposed || !data || data.tabId === tabId) return
    if (data.type === 'hello') {
      if (token) channel?.postMessage?.({ type: 'claim', token, tabId })
      return
    }
    if (!token || data.type !== 'claim' || data.token !== token) return
    if (String(data.tabId) < String(tabId)) clearDuplicate()
  }

  if (channel) {
    channel.addEventListener?.('message', onMessage)
    channel.postMessage?.({ type: 'hello', tabId })
    if (token) channel.postMessage?.({ type: 'claim', token, tabId })
  }

  return {
    get token() {
      return token
    },
    save(nextToken) {
      if (disposed) return token
      token = saveWechatRegisterFlowToken(nextToken, storage)
      if (token) channel?.postMessage?.({ type: 'claim', token, tabId })
      return token
    },
    clear(expectedToken = token) {
      if (disposed || normalizeToken(expectedToken) !== token) return false
      const previous = token
      token = ''
      clearOwnedToken(previous, storage)
      return true
    },
    dispose() {
      if (disposed) return
      disposed = true
      token = ''
      channel?.removeEventListener?.('message', onMessage)
      channel?.close?.()
    },
  }
}

export function appendWechatRegisterFlowToken(payload, token) {
  const normalized = normalizeToken(token)
  return normalized ? { ...payload, registrationFlowToken: normalized } : payload
}
