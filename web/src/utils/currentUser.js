import { onScopeDispose, readonly, shallowRef } from 'vue'
import { resolveAdapterHomePath } from './userPortalHelpers.mjs'

const CURRENT_USER_STORAGE_KEY = 'adapter_current_user'
// Display only: never a replacement for the server session or authorization.
// Weak ownership avoids retaining a replaced window; listeners exist only in scopes.
const displayOwners = new WeakMap()

function readStoredCurrentUser(host, clearMalformed = true) {
  let raw = null
  try {
    raw = host.localStorage.getItem(CURRENT_USER_STORAGE_KEY)
    return { raw, user: raw ? JSON.parse(raw) : null }
  } catch (error) {
    if (clearMalformed) {
      host.localStorage.removeItem(CURRENT_USER_STORAGE_KEY)
      raw = null
    }
    return { raw, user: null }
  }
}

function syncDisplay(host, owner, stored = readStoredCurrentUser(host), force = false) {
  if (force || owner.raw !== stored.raw) {
    owner.raw = stored.raw
    owner.version += 1
    // A mutable legacy getter result must not alias the readonly display snapshot.
    owner.currentUser.value = stored.user && typeof stored.user === 'object' ? JSON.parse(stored.raw) : stored.user
  }
  return stored.user
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
  if (owner) syncDisplay(window, owner, readStoredCurrentUser(window, false), true)
}

export function clearStoredCurrentUser() {
  window.localStorage.removeItem(CURRENT_USER_STORAGE_KEY)
  const owner = displayOwners.get(window)
  if (owner) syncDisplay(window, owner, undefined, true)
}

export function useCurrentUserDisplay() {
  const host = window
  let owner = displayOwners.get(host)
  if (!owner) {
    const stored = readStoredCurrentUser(host)
    owner = { currentUser: shallowRef(stored.user), raw: stored.raw, version: 0, consumers: 0 }
    owner.onStorage = (event) => {
      if (event.storageArea !== host.localStorage) return
      if (event.key !== null && event.key !== CURRENT_USER_STORAGE_KEY) return
      syncDisplay(host, owner, undefined, true)
    }
    displayOwners.set(host, owner)
  } else {
    syncDisplay(host, owner)
  }
  if (owner.consumers++ === 0) host.addEventListener('storage', owner.onStorage)
  onScopeDispose(() => {
    if (--owner.consumers === 0) {
      host.removeEventListener('storage', owner.onStorage)
      displayOwners.delete(host)
    }
  })
  return {
    currentUser: readonly(owner.currentUser),
    // A late server response may update this display only if its session snapshot
    // is still current. This is a freshness guard, not a permission decision.
    captureUpdateGuard() {
      syncDisplay(host, owner)
      const version = owner.version
      const raw = owner.raw
      return () => owner.version === version && host.localStorage.getItem(CURRENT_USER_STORAGE_KEY) === raw
    },
  }
}

export function isAdminUser(user) {
  return String(user?.role || '').trim().toLowerCase() === 'admin'
}

export function resolveHomePath(user) {
  return resolveAdapterHomePath(user)
}
