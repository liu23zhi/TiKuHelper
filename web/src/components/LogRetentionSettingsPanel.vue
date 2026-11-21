<template>
  <ConsolePanel v-if="canManage" title="日志保留策略" description="日志总量上限沿用上方站点与日志配置；这里管理归档时间和检查间隔，单独保存。活动日志不会因过期策略被删除。">
    <a-form :key="surfaceVersion" :model="form" layout="vertical" :onFinish="bind(save)">
      <div class="log-retention-fields">
        <a-form-item label="启用归档清理"><a-switch :checked="form.enabled" :disabled="busy || !loaded" :onUpdate:checked="bind(setField, 'enabled')" /></a-form-item>
        <a-form-item label="归档保留天数"><a-input-number :value="form.maxAgeDays" :min="1" :max="3650" :precision="0" :disabled="busy || !loaded" :onUpdate:value="bind(setField, 'maxAgeDays')" /></a-form-item>
        <a-form-item label="检查间隔（分钟）"><a-input-number :value="form.cleanupIntervalMinutes" :min="1" :max="1440" :precision="0" :disabled="busy || !loaded" :onUpdate:value="bind(setField, 'cleanupIntervalMinutes')" /></a-form-item>
      </div>
      <a-space wrap><a-button type="primary" html-type="submit" :loading="busy" :disabled="!loaded || busy">保存日志策略</a-button><a-button :disabled="busy" :onClick="bind(load)">重新读取</a-button></a-space>
    </a-form>
    <a-alert v-if="feedback" :type="failed ? 'error' : 'success'" :message="feedback" show-icon class="log-retention-feedback" />
  </ConsolePanel>
</template>
<script>
import { reactive, ref } from 'vue'
import ConsolePanel from './ConsolePanel.vue'
import request from '../utils/request'
import { useCurrentUserDisplay } from '../utils/currentUser'
import { useAdminPanelLease } from '../utils/useAdminPanelLease'
const empty = () => ({ enabled: true, maxAgeDays: 30, cleanupIntervalMinutes: 60 })
const valid = value => value && typeof value.enabled === 'boolean' && [['maxAgeDays', 3650], ['cleanupIntervalMinutes', 1440]].every(([key, max]) => Number.isInteger(value[key]) && value[key] >= 1 && value[key] <= max)
export default {
  components: { ConsolePanel }, props: { disabled: Boolean },
  setup(props) {
    const { currentUser } = useCurrentUserDisplay()
    const form = reactive(empty())
    const busy = ref(false), loaded = ref(false), feedback = ref(''), failed = ref(false)
    let revision = 0
    const { canManage, capture, bind, surfaceVersion } = useAdminPanelLease(currentUser, {
      disabled: () => props.disabled,
      clear: () => { revision++; Object.assign(form, empty()); busy.value = loaded.value = failed.value = false; feedback.value = '' },
      reload: () => { void execute('get') },
    })
    function setField(field, value) { if (loaded.value && !busy.value && canManage.value && Object.hasOwn(form, field)) { form[field] = value; feedback.value = '' } }
    async function execute(method) {
      if (!canManage.value || busy.value || (method === 'put' && !loaded.value)) return
      if (method === 'put' && !valid(form)) { failed.value = true; feedback.value = '请填写范围内的完整整数策略。'; return }
      const ticket = ++revision, guard = capture()
      const owns = () => ticket === revision && guard()
      busy.value = true; feedback.value = ''
      try {
        const response = await request({ url: '/api/admin/log-settings', method, ...(method === 'put' ? { data: { ...form } } : {}), errorFeedback: 'local' })
        if (!owns()) return
        if (response?.status !== 200) throw new Error('request failed')
        const settings = response.data?.settings
        if (!valid(settings)) throw new Error('invalid settings')
        Object.assign(form, { enabled: settings.enabled, maxAgeDays: settings.maxAgeDays, cleanupIntervalMinutes: settings.cleanupIntervalMinutes }); loaded.value = true
        if (method === 'put') { failed.value = false; feedback.value = response.data.warning || '日志保留策略已保存。' }
      } catch { if (owns()) { failed.value = true; feedback.value = method === 'put' ? '保存日志策略失败，当前编辑已保留，请重试。' : '读取日志策略失败，请重试。' } }
      finally { if (owns()) busy.value = false }
    }
    const load = () => execute('get'), save = () => execute('put')
    return { canManage, form, busy, loaded, feedback, failed, load, save, bind, surfaceVersion, setField }
  },
}
</script>
<style scoped>.log-retention-fields{display:grid;grid-template-columns:repeat(auto-fit,minmax(190px,1fr));gap:0 16px}.log-retention-feedback{margin-top:12px}</style>
