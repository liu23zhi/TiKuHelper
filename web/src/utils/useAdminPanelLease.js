import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { getCurrentUserLogoutEpoch, isAdminUser } from './currentUser'
import { currentUserIdentity } from './currentUserScope'

// UI ownership complements server authorization; a retired native callback cannot act as a new owner.
export function useAdminPanelLease(currentUser, { disabled = () => false, resource = () => '', clear, reload = () => {} }) {
  let alive = true, lease = {}
  const mountedLogoutEpoch = getCurrentUserLogoutEpoch()
  const surfaceVersion = ref(0)
  const canManage = computed(() => {
    const user = currentUser.value, id = Number(user?.id)
    return alive && !disabled() && Number.isSafeInteger(id) && id > 0 && id <= 2147483647 &&
      isAdminUser(user) && !['0', 'disabled'].includes(String(user?.status || 'enabled')) &&
      getCurrentUserLogoutEpoch() === mountedLogoutEpoch
  })
  const capture = () => { const owner = lease; return () => owner === lease && canManage.value }
  const bind = (fn, ...args) => { const owns = capture(); return (...events) => { if (owns()) return fn(...args, ...events) } }
  watch([() => currentUserIdentity(currentUser.value), () => currentUser.value?.role, () => currentUser.value?.status, getCurrentUserLogoutEpoch, disabled, resource], () => {
    lease = {}; surfaceVersion.value += 1; clear()
    const owns = capture()
    Promise.resolve().then(() => { if (owns()) reload() })
  }, { immediate: true, flush: 'sync' })
  onBeforeUnmount(() => { alive = false; lease = {}; clear() })
  return { canManage, capture, bind, surfaceVersion }
}
