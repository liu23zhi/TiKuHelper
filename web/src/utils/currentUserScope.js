import { onBeforeUnmount, onMounted, watch } from 'vue'
import { isAdminUser, getCurrentUserLogoutEpoch } from './currentUser'

export function currentUserIdentity(user) {
  if (!user || typeof user !== 'object') return ''
  const id = user.id
  if ((typeof id === 'string' || typeof id === 'number') && String(id).trim()) return `id:${String(id).trim()}`
  const username = String(user.username ?? '').trim()
  return username ? `username:${username}` : ''
}

// Display/data ownership only. The existing server session and authorization remain authoritative.
// Balance-only changes preserve same-scope completion order and never initiate a new read.
export function useCurrentUserRequestScope(currentUser, { clear, reset, reload }) {
  let alive = true
  let mounted = false
  let epoch = 0
  const canLoadForCurrentUser = () => alive && Boolean(currentUserIdentity(currentUser.value))
  const captureDataOwner = () => {
    const ticket = epoch
    const logout = getCurrentUserLogoutEpoch()
    return () => alive && ticket === epoch && logout === getCurrentUserLogoutEpoch()
  }
  clear()
  watch(() => [currentUserIdentity(currentUser.value), isAdminUser(currentUser.value), getCurrentUserLogoutEpoch()], (next, previous) => {
    const logoutChanged = next[2] !== previous[2]
    if (!logoutChanged && next[0] === previous[0] && next[1] === previous[1]) return
    epoch += 1
    clear()
    if (logoutChanged || next[0] !== previous[0]) reset()
    // A retained identity after explicit logout must not start an automatic read.
    if (!logoutChanged && mounted && canLoadForCurrentUser()) reload()
  }, { flush: 'sync' })
  onMounted(() => { mounted = true })
  onBeforeUnmount(() => { alive = false; mounted = false; epoch += 1; clear() })
  return { captureDataOwner, canLoadForCurrentUser }
}
