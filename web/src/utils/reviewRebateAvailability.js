import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { getReviewRebateAvailability } from '../api/ReviewRebateApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from './currentUser'
import { currentUserIdentity } from './currentUserScope'

const availabilityChanged = 'review-rebate-availability-changed'

export function notifyReviewRebateAvailabilityChanged() {
  window.dispatchEvent(new Event(availabilityChanged))
}

export function useReviewRebateAvailability(getCurrentUser) {
  const enabled = ref(false)
  const { currentUser: storedCurrentUser } = useCurrentUserDisplay()
  let mounted = false
  let pending = null

  function canRead(user) {
    const status = String(user?.status ?? '').trim().toLowerCase()
    return !['banned', 'disabled', '0'].includes(status)
  }

  function actor() {
    const user = getCurrentUser()
    const stored = storedCurrentUser.value
    const identity = currentUserIdentity(user)
    return identity && identity === currentUserIdentity(stored) && canRead(user) && canRead(stored) ? identity : ''
  }

  function invalidate() {
    pending?.abort()
    pending = null
    enabled.value = false
  }

  async function refresh() {
    if (!mounted || !actor() || pending) return
    const request = new AbortController()
    pending = request
    try {
      const result = await getReviewRebateAvailability(request.signal)
      if (pending === request) enabled.value = result?.status === 200 && result.data?.enabled === true
    } catch {
      if (pending === request) enabled.value = false
    } finally {
      if (pending === request) pending = null
    }
  }

  function refreshWhenVisible() {
    if (document.visibilityState !== 'hidden') void refresh()
  }

  function onAvailabilityChanged() {
    invalidate()
    void refresh()
  }

  watch([actor, () => isAdminUser(getCurrentUser()), getCurrentUserLogoutEpoch], onAvailabilityChanged, { flush: 'sync' })
  onMounted(() => {
    mounted = true
    window.addEventListener(availabilityChanged, onAvailabilityChanged)
    window.addEventListener('focus', refreshWhenVisible)
    document.addEventListener('visibilitychange', refreshWhenVisible)
    void refresh()
  })
  onBeforeUnmount(() => {
    mounted = false
    invalidate()
    window.removeEventListener(availabilityChanged, onAvailabilityChanged)
    window.removeEventListener('focus', refreshWhenVisible)
    document.removeEventListener('visibilitychange', refreshWhenVisible)
  })
  return enabled
}
