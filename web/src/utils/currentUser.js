const CURRENT_USER_STORAGE_KEY = 'adapter_current_user'

import { resolveAdapterHomePath } from './userPortalHelpers.mjs'

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
}

export function isAdminUser(user) {
  return String(user?.role || '').trim().toLowerCase() === 'admin'
}

export function resolveHomePath(user) {
  return resolveAdapterHomePath(user)
}
