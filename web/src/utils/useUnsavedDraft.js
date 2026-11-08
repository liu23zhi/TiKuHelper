import { computed, onBeforeUnmount, ref } from 'vue'
import { Modal } from 'ant-design-vue'

export function useUnsavedDraft(formState, { focusTriggerAfterClose } = {}) {
  const currentConfigSnapshot = computed(() => JSON.stringify(formState))
  const savedConfigSnapshot = ref(currentConfigSnapshot.value)
  const hasUnsavedChanges = computed(() => savedConfigSnapshot.value === null || currentConfigSnapshot.value !== savedConfigSnapshot.value)
  let ticket = null
  const settle = (owned, allow) => {
    if (ticket !== owned) return
    ticket = null
    owned.waiters.forEach(resolve => resolve(allow))
    owned.modal?.destroy()
  }
  const cancel = () => { if (ticket) settle(ticket, false) }
  const requestRouteLeave = to => {
    if (to.path === '/login' || !hasUnsavedChanges.value) { cancel(); return true }
    if (ticket) return new Promise(resolve => ticket.waiters.push(resolve))
    const owned = { waiters: [], modal: null }
    ticket = owned
    const decision = new Promise(resolve => owned.waiters.push(resolve))
    owned.modal = Modal.confirm({
      title: '有未保存的更改，确认离开？', content: '离开将放弃当前未保存的编辑。',
      okText: '放弃并离开', cancelText: '保留编辑', focusTriggerAfterClose,
      onOk: () => settle(owned, true), onCancel: () => settle(owned, false),
    })
    return decision
  }
  onBeforeUnmount(cancel)
  return { currentConfigSnapshot, savedConfigSnapshot, hasUnsavedChanges, requestRouteLeave, cancelPendingLeave: cancel }
}
