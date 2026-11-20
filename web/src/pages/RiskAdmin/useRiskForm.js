import { onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { readRiskMessage, readRiskThrownMessage } from './riskAdminHelpers.mjs'

// 这些表单按管理员和目标 key 挂载，拒绝已离开页面/已切换身份的回包。
export function useRiskForm(captureScope, read, fill = () => {}) {
  const saved = shallowRef(null), busy = ref(false), error = ref(''), notice = ref('')
  let mounted = true
  const run = async (request, success = '') => {
    const owns = captureScope()
    if (!mounted || !owns() || busy.value) return false
    busy.value = true; error.value = ''; notice.value = ''
    try {
      const res = await request()
      if (!mounted || !owns()) return false
      const data = res?.data?.data
      if (res?.status < 200 || res?.status >= 300 || !data || typeof data !== 'object') {
        error.value = readRiskMessage(res, '请求失败，请重试'); return false
      }
      saved.value = data; fill(data)
      if (success) notice.value = data.message || success
      return true
    } catch (e) {
      if (mounted && owns()) error.value = readRiskThrownMessage(e, '请求失败，请重试')
      return false
    } finally { if (mounted && owns()) busy.value = false }
  }
  const reload = () => run(read)
  onMounted(reload)
  onBeforeUnmount(() => { mounted = false })
  return { saved, busy, error, notice, run, reload }
}
