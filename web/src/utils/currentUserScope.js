import { onBeforeUnmount, onMounted, watch } from 'vue'
import { isAdminUser } from './currentUser'

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
    return () => alive && ticket === epoch
  }
  clear()
  watch(() => [currentUserIdentity(currentUser.value), isAdminUser(currentUser.value)], (next, previous) => {
    if (next[0] === previous[0] && next[1] === previous[1]) return
    epoch += 1
    clear()
    if (next[0] !== previous[0]) reset()
    if (mounted && canLoadForCurrentUser()) reload()
  }, { flush: 'sync' })
  onMounted(() => { mounted = true })
  onBeforeUnmount(() => { alive = false; mounted = false; epoch += 1; clear() })
  return { captureDataOwner, canLoadForCurrentUser }
}
