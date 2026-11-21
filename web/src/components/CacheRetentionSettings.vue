<template>
  <ConsolePanel v-if="canManage" title="工单图片缓存策略" description="选择按时间、按大小或同时清理。清理保留工单与附件关系，历史图片会显示已清理提示。">
    <a-form :key="surfaceVersion" :model="form" layout="vertical" :onFinish="bind(save)">
      <div class="retention-fields">
        <a-form-item label="清理方式"><a-select :value="form.mode" :options="modes" :disabled="busy || !loaded" :onUpdate:value="bind(setField, 'mode')" /></a-form-item>
        <a-form-item v-if="form.mode !== 'size'" label="图片保留时间（天）"><a-input-number :value="form.maxAgeDays" :min="1" :max="3650" :precision="0" :disabled="busy || !loaded" :onUpdate:value="bind(setField, 'maxAgeDays')" /></a-form-item>
        <a-form-item v-if="form.mode !== 'time'" label="图片总占用上限（GiB）"><a-input-number :value="form.maxTotalGiB" :min="1" :max="1024" :precision="0" :disabled="busy || !loaded" :onUpdate:value="bind(setField, 'maxTotalGiB')" /></a-form-item>
        <a-form-item label="自动检查间隔（分钟）"><a-input-number :value="form.cleanupIntervalMinutes" :min="1" :max="1440" :precision="0" :disabled="busy || !loaded" :onUpdate:value="bind(setField, 'cleanupIntervalMinutes')" /></a-form-item>
      </div>
      <a-space><a-button type="primary" html-type="submit" :loading="busy" :disabled="!loaded || busy">保存图片策略</a-button><a-button :disabled="busy" :onClick="bind(load)">重新读取</a-button></a-space>
    </a-form>
    <a-alert v-if="feedback" :type="failed ? 'error' : 'success'" show-icon :message="feedback" class="retention-feedback" />
  </ConsolePanel>
</template>
<script>
import { reactive, ref } from 'vue'
import ConsolePanel from './ConsolePanel.vue'
import request from '../utils/request'
import { useCurrentUserDisplay } from '../utils/currentUser'
import { useAdminPanelLease } from '../utils/useAdminPanelLease'
const empty = () => ({ mode: 'both', maxAgeDays: 180, maxTotalGiB: 20, cleanupIntervalMinutes: 60 })
const valid = value => value && ['time', 'size', 'both'].includes(value.mode) && [['maxAgeDays', 3650], ['maxTotalGiB', 1024], ['cleanupIntervalMinutes', 1440]].every(([key, max]) => Number.isInteger(value[key]) && value[key] >= 1 && value[key] <= max)
export default {
  components: { ConsolePanel },
  props: { disabled: Boolean },
  setup(props) {
    const { currentUser } = useCurrentUserDisplay()
    const form = reactive(empty())
    const modes = [{ value: 'time', label: '只按时间清理' }, { value: 'size', label: '只按大小清理' }, { value: 'both', label: '先按时间，再按大小清理' }]
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
      const sequence = ++revision, guard = capture()
      const owns = () => sequence === revision && guard()
      busy.value = true; feedback.value = ''
      try {
        const response = await request({ url: '/api/admin/ticket-image-cache/settings', method, ...(method === 'put' ? { data: { ...form } } : {}), errorFeedback: 'local' })
        if (!owns()) return
        if (response?.status !== 200 || !valid(response.data)) throw new Error('invalid response')
        for (const key of Object.keys(empty())) form[key] = response.data[key]
        loaded.value = true
        if (method === 'put') { failed.value = false; feedback.value = '图片缓存策略已保存，下次检查按此执行。' }
      } catch { if (owns()) { failed.value = true; feedback.value = method === 'put' ? '保存图片策略失败，当前编辑已保留，请重试。' : '读取图片策略失败，请重试。' } }
      finally { if (owns()) busy.value = false }
    }
    const load = () => execute('get'), save = () => execute('put')
    return { canManage, form, modes, busy, loaded, feedback, failed, load, save, bind, surfaceVersion, setField }
  },
}
</script>
<style scoped>
.retention-fields { display:grid; grid-template-columns:repeat(auto-fit,minmax(190px,1fr)); gap:0 16px; }
.retention-fields :deep(.ant-input-number) { width:100%; }
.retention-feedback { margin-top:12px; }
</style>
