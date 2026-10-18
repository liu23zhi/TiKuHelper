export const REGISTRATION_FLOW_STORAGE_KEY = 'tikuAdapter.registrationFlowToken'
const CHANNEL_NAME = 'tikuAdapter.wechat-register-flow'

function defaultStorage() {
  return typeof globalThis !== 'undefined' && globalThis.sessionStorage ? globalThis.sessionStorage : null
}

function normalizeToken(token) {
  return String(token || '').trim()
}

export function loadWechatRegisterFlowToken(storage = defaultStorage()) {
  if (!storage || typeof storage.getItem !== 'function') return ''
  return normalizeToken(storage.getItem(REGISTRATION_FLOW_STORAGE_KEY))
}

export function saveWechatRegisterFlowToken(token, storage = defaultStorage()) {
  const normalized = normalizeToken(token)
  if (!storage) return normalized
  if (normalized && typeof storage.setItem === 'function') {
    storage.setItem(REGISTRATION_FLOW_STORAGE_KEY, normalized)
  } else if (typeof storage.removeItem === 'function') {
    storage.removeItem(REGISTRATION_FLOW_STORAGE_KEY)
  }
  return normalized
}

export function clearWechatRegisterFlowToken(storage = defaultStorage()) {
  if (storage && typeof storage.removeItem === 'function') storage.removeItem(REGISTRATION_FLOW_STORAGE_KEY)
}

function createTabId() {
  if (typeof globalThis !== 'undefined' && globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID()
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
}

function makeChannel(channelFactory) {
  if (channelFactory === null) return null
  if (typeof channelFactory === 'function') return channelFactory(CHANNEL_NAME)
  if (typeof globalThis !== 'undefined' && typeof globalThis.BroadcastChannel === 'function') {
    return new globalThis.BroadcastChannel(CHANNEL_NAME)
  }
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
    token = ''
    clearWechatRegisterFlowToken(storage)
    if (typeof onDuplicate === 'function') onDuplicate()
  }

  const onMessage = (event) => {
    const data = event?.data
    if (disposed || !token || !data || data.type !== 'claim' || data.token !== token || data.tabId === tabId) return
    if (String(data.tabId) < String(tabId)) clearDuplicate()
  }

  if (channel) {
    channel.addEventListener?.('message', onMessage)
    if (token) channel.postMessage?.({ type: 'claim', token, tabId })
  }

  return {
    get token() {
      return token
    },
    save(nextToken) {
      token = saveWechatRegisterFlowToken(nextToken, storage)
      if (token) channel?.postMessage?.({ type: 'claim', token, tabId })
      return token
    },
    clear() {
      token = ''
      clearWechatRegisterFlowToken(storage)
    },
    dispose() {
      if (disposed) return
      disposed = true
      channel?.removeEventListener?.('message', onMessage)
      channel?.close?.()
    },
  }
}

export function appendWechatRegisterFlowToken(payload, token) {
  const normalized = normalizeToken(token)
  return normalized ? { ...payload, registrationFlowToken: normalized } : payload
}
