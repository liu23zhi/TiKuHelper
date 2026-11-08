import { onScopeDispose, readonly, shallowReadonly, shallowRef, triggerRef } from 'vue'
import { resolveAdapterHomePath } from './userPortalHelpers.mjs'

const CURRENT_USER_STORAGE_KEY = 'adapter_current_user'
// In-memory logout notification for bounded page caches that outlive their mount.
// This is not a server session/authentication version and contains no credentials.
let currentUserLogoutEpoch = 0
export function getCurrentUserLogoutEpoch() { return currentUserLogoutEpoch }
// Display/freshness only: never a replacement for server authentication.
// One shared listener exists only while a page scope or pending request retains it.
const displayOwners = new WeakMap()

function readStoredCurrentUser(host, clearMalformed = true) {
  let raw = null
  let storage
  try {
    storage = host.localStorage
    raw = storage.getItem(CURRENT_USER_STORAGE_KEY)
  } catch {
    // Unavailable storage is unknown ownership, not a logout or a request failure.
    return { raw, user: null, storage, available: false, readable: false }
  }
  try {
    return { raw, user: raw ? JSON.parse(raw) : null, storage, available: true, readable: true }
  } catch {
    if (clearMalformed) {
      try { storage.removeItem(CURRENT_USER_STORAGE_KEY); raw = null } catch { /* best-effort legacy cleanup */ }
    }
    return { raw, user: null, storage, available: true, readable: false }
  }
}

function syncDisplay(host, owner, stored = readStoredCurrentUser(host), force = false) {
  if (!stored.available && !force) return null
  if (force || owner.raw !== stored.raw) {
    owner.raw = stored.raw
    owner.version += 1
    // A mutable legacy getter result must not alias the readonly display snapshot.
    const previous = owner.currentUser.value
    owner.currentUser.value = stored.user && typeof stored.user === 'object' ? readonly(JSON.parse(stored.raw)) : stored.user
    // Explicit clear/storage events are authoritative even when blocked storage
    // leaves null -> null. Do not duplicate already-new object notifications.
    if (force && Object.is(previous, owner.currentUser.value)) triggerRef(owner.currentUser)
  }
  return stored.user
}

function getDisplayOwner(host, stored = readStoredCurrentUser(host)) {
  let owner = displayOwners.get(host)
  if (!owner) {
    owner = { currentUser: shallowRef(stored.user && typeof stored.user === 'object' ? readonly(JSON.parse(stored.raw)) : stored.user), raw: stored.raw, version: 0, consumers: 0, requests: 0 }
    owner.onStorage = (event) => {
      if (event.key !== null && event.key !== CURRENT_USER_STORAGE_KEY) return
      let storage
      try { storage = host.localStorage } catch { return }
      if (event.storageArea !== storage) return
      // Even A -> B -> A or a clear event must retire an in-flight A request.
      syncDisplay(host, owner, undefined, true)
    }
    displayOwners.set(host, owner)
  } else {
    syncDisplay(host, owner, stored)
  }
  return owner
}

function retainDisplayOwner(host, owner, kind) {
  if (owner.consumers + owner.requests === 0) host.addEventListener('storage', owner.onStorage)
  owner[kind] += 1
}

function releaseDisplayOwner(host, owner, kind) {
  owner[kind] -= 1
  if (owner.consumers + owner.requests === 0) {
    host.removeEventListener('storage', owner.onStorage)
    if (displayOwners.get(host) === owner) displayOwners.delete(host)
  }
}

export function getStoredCurrentUser() {
  const stored = readStoredCurrentUser(window)
  const owner = displayOwners.get(window)
  if (owner) syncDisplay(window, owner, stored)
  return stored.user
}

export function setStoredCurrentUser(user) {
  if (!user || typeof user !== 'object') {
    clearStoredCurrentUser()
    return
  }
  window.localStorage.setItem(CURRENT_USER_STORAGE_KEY, JSON.stringify(user))
  const owner = displayOwners.get(window)
  // Identical writes are conservatively treated as a new login/profile generation.
  if (owner) syncDisplay(window, owner, readStoredCurrentUser(window, false), true)
}

export function clearStoredCurrentUser() {
  currentUserLogoutEpoch += 1
  try { window.localStorage.removeItem(CURRENT_USER_STORAGE_KEY) } catch { /* storage denial must not discard a server response */ }
  advanceCurrentSessionRevision()
  const owner = displayOwners.get(window)
  if (owner) syncDisplay(window, owner, readStoredCurrentUser(window, false), true)
}

export function captureCurrentUserLease() {
  const host = window
  // No cleanup on capture/check: a malformed snapshot cannot authorize side effects.
  const stored = readStoredCurrentUser(host, false)
  const owner = getDisplayOwner(host, stored)
  retainDisplayOwner(host, owner, 'requests')
  const version = owner.version
  const logoutEpoch = currentUserLogoutEpoch
  let retired = false
  let released = false
  function inspect() {
    const current = readStoredCurrentUser(host, false)
    retired = retired || released || window !== host || owner.version !== version ||
      currentUserLogoutEpoch !== logoutEpoch || displayOwners.get(host) !== owner ||
      (stored.storage && current.storage && stored.storage !== current.storage) ||
      (stored.available && current.available && stored.raw !== current.raw)
    return { retired: Boolean(retired), current: !retired && stored.readable && current.readable }
  }
  return {
    isCurrent: () => inspect().current,
    // Unknown/blocked storage alone cannot cancel an otherwise legitimate retry.
    isRetired: () => inspect().retired,
    release() {
      if (released) return
      released = true
      releaseDisplayOwner(host, owner, 'requests')
    },
  }
}

export function useCurrentUserDisplay() {
  const host = window
  const owner = getDisplayOwner(host)
  retainDisplayOwner(host, owner, 'consumers')
  onScopeDispose(() => releaseDisplayOwner(host, owner, 'consumers'))
  return {
    // Keep the shallow-ref force signal while its object value stays deeply readonly.
    currentUser: shallowReadonly(owner.currentUser),
    // A freshness guard, not a permission decision.
    captureUpdateGuard() {
      syncDisplay(host, owner)
      const version = owner.version
      const raw = owner.raw
      return () => {
        const stored = readStoredCurrentUser(host, false)
        return window === host && stored.readable && owner.version === version && stored.raw === raw
      }
    },
  }
}

export function isAdminUser(user) {
  return String(user?.role || '').trim().toLowerCase() === 'admin'
}

export function resolveHomePath(user) {
  return resolveAdapterHomePath(user)
}

// Client freshness revision complements, and never replaces, server SID and owner/logout leases.
const SESSION_REVISION_STORAGE_KEY = 'adapter_session_revision'
let sessionRevision = ''
let sessionRevisionMemoryOnly = false

export function getCurrentSessionRevision() {
  if (!sessionRevisionMemoryOnly) {
    try {
      sessionRevision = window.localStorage.getItem(SESSION_REVISION_STORAGE_KEY) || ''
    } catch {
      // Storage access can be disabled by the browser; keep this tab's revision.
    }
  }
  return sessionRevision
}

export function advanceCurrentSessionRevision() {
  sessionRevision = `${Date.now()}-${Math.random()}`
  try {
    window.localStorage.setItem(SESSION_REVISION_STORAGE_KEY, sessionRevision)
    sessionRevisionMemoryOnly = false
  } catch {
    // A failed write must not let an older persisted revision replace this one.
    sessionRevisionMemoryOnly = true
  }
}
