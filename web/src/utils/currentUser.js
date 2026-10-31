const CURRENT_USER_STORAGE_KEY = 'adapter_current_user'
const SESSION_REVISION_STORAGE_KEY = 'adapter_session_revision'
let sessionRevision = ''
let sessionRevisionMemoryOnly = false

import { resolveAdapterHomePath } from './userPortalHelpers.mjs'

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

export function getStoredCurrentUser() {
  try {
    const raw = window.localStorage.getItem(CURRENT_USER_STORAGE_KEY)
    if (!raw) {
      return null
    }
    return JSON.parse(raw)
  } catch (error) {
    window.localStorage.removeItem(CURRENT_USER_STORAGE_KEY)
    return null
  }
}

export function setStoredCurrentUser(user) {
  if (!user || typeof user !== 'object') {
    clearStoredCurrentUser()
    return
  }
  window.localStorage.setItem(CURRENT_USER_STORAGE_KEY, JSON.stringify(user))
}

export function clearStoredCurrentUser() {
  window.localStorage.removeItem(CURRENT_USER_STORAGE_KEY)
  advanceCurrentSessionRevision()
}

export function isAdminUser(user) {
  return String(user?.role || '').trim().toLowerCase() === 'admin'
}

export function resolveHomePath(user) {
  return resolveAdapterHomePath(user)
}
