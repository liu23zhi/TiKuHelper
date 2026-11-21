<template>
  <ConsolePanel title="企业微信工单提醒" description="与工单管理共用同一配置。提醒由异步队列发送，Webhook 完整地址不会回显。" class="page-card">
    <template #actions><a-button size="small" :disabled="!canManage() || loading || saving || testing" @click="open">{{ expanded ? '重新读取' : '配置提醒' }}</a-button></template>
    <section v-if="failure" data-notification-settings-feedback>
      <RequestFailureAlert :failure="failure" :message="failureText" :context-key="failureVersion" :is-current="isCurrentFailure" :busy="loading || saving || testing" />
    </section>
    <a-alert v-if="dirty" type="warning" show-icon message="提醒设置有未保存的更改。测试消息使用服务端已保存的配置。" />
    <TicketNotificationSettingsForm v-if="expanded" :key="surfaceVersion" :config="config" :entry="entry" :loaded="loaded" :disabled="!canManage() || loading" :saving="saving" :testing="testing" :save="save" :test="test" :set-field="setField" :clear-webhook="clearWebhook" :keep-webhook="keepWebhook" />
  </ConsolePanel>
</template>

<script setup>
import { onBeforeUnmount, reactive, ref, shallowRef, watch } from 'vue'
import { message } from 'ant-design-vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import TicketNotificationSettingsForm from '@/components/TicketNotificationSettingsForm.vue'
import { getTicketNotificationConfig, saveTicketNotificationConfig, testTicketNotification } from '@/api/TicketApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'

const props = defineProps({ disabled: Boolean })
const { currentUser } = useCurrentUserDisplay()
const empty = () => ({ enabled: false, configured: false, webhookUrl: '', clearWebhook: false, mentionedList: '' })
const config = reactive(empty())
const expanded = ref(false), loaded = ref(false), loading = ref(false), saving = ref(false), testing = ref(false), dirty = ref(false)
const failure = shallowRef(null), failureText = ref(''), failureVersion = ref(0), surfaceVersion = ref(0)
let alive = true, lease = {}, draftVersion = 0, failureOwner = () => false
const canManage = () => alive && !props.disabled && isAdminUser(currentUser.value) && Number(currentUser.value?.id) > 0 && !['disabled', '0'].includes(String(currentUser.value?.status || 'enabled'))
const capture = () => { const owner = lease; return () => owner === lease && canManage() }
const clearFailure = () => { failure.value = null; failureText.value = ''; failureVersion.value += 1; failureOwner = () => false }
const isCurrentFailure = (item, context) => failureOwner() && item === failure.value && context === failureVersion.value
const showFailure = (error, text, owns) => { if (!owns()) return; failure.value = error; failureText.value = text; failureOwner = owns }
const entry = { bind(fn, ...args) { const owns = capture(); return (...events) => { if (owns()) return fn(...args, ...events) } } }
function open() { if (!canManage()) return; expanded.value = true; void load() }
function setField(field, value) {
  if (!canManage() || !loaded.value || loading.value || saving.value || testing.value) return
  if (!['enabled', 'webhookUrl', 'mentionedList'].includes(field)) return
  config[field] = field === 'enabled' ? Boolean(value) : String(value ?? '')
  dirty.value = true; draftVersion += 1; clearFailure()
}
function clearWebhook() { if (!canManage() || !loaded.value || saving.value || testing.value) return; config.clearWebhook = true; config.webhookUrl = ''; dirty.value = true; draftVersion += 1; clearFailure() }
function keepWebhook() { if (!canManage() || !loaded.value || saving.value || testing.value) return; config.clearWebhook = false; dirty.value = true; draftVersion += 1; clearFailure() }
async function load() {
  if (!canManage() || loading.value || saving.value || testing.value) return
  const owns = capture(), version = draftVersion
  loading.value = true; clearFailure()
  try {
    const response = await getTicketNotificationConfig()
    if (!owns()) return
    const data = response?.data?.data || response?.data
    if (response?.status !== 200 || typeof data?.enabled !== 'boolean' || typeof data?.configured !== 'boolean') throw new Error('invalid configuration response')
    config.configured = data.configured
    if (!dirty.value && version === draftVersion) Object.assign(config, { enabled: data.enabled, webhookUrl: '', clearWebhook: false, mentionedList: Array.isArray(data.mentionedList) ? data.mentionedList.join(',') : String(data.mentionedList || '') })
    loaded.value = true
  } catch (error) { showFailure(error, '企业微信提醒设置读取失败，请重试；未确认配置前不能保存或发送测试。', owns) }
  finally { if (owns()) loading.value = false }
}
async function save() {
  if (!canManage() || !loaded.value || loading.value || saving.value || testing.value) return
  const owns = capture(), version = draftVersion
  const payload = { enabled: config.enabled, clearWebhook: config.clearWebhook, mentionedList: config.mentionedList.split(',').map(item => item.trim()).filter(Boolean), ...(config.webhookUrl.trim() ? { webhookUrl: config.webhookUrl.trim() } : {}) }
  saving.value = true; clearFailure()
  try {
    const response = await saveTicketNotificationConfig(payload)
    if (!owns()) return
    if (response?.status !== 200) throw response
    if (version === draftVersion) { config.webhookUrl = ''; config.clearWebhook = false; dirty.value = false }
    message.success('企业微信提醒设置已保存')
  } catch (error) { showFailure(error, '企业微信提醒设置保存失败，当前编辑仍保留，请核对后重试。', () => owns() && version === draftVersion) }
  finally { if (owns()) saving.value = false }
  if (owns() && !dirty.value) await load()
}
async function test() {
  if (!canManage() || !loaded.value || loading.value || saving.value || testing.value) return
  const owns = capture()
  testing.value = true; clearFailure()
  try {
    const response = await testTicketNotification()
    if (!owns()) return
    if (response?.status !== 200) throw response
    message.success('测试消息已发送')
  } catch (error) { showFailure(error, '测试消息发送失败，请核对已保存的提醒配置。', owns) }
  finally { if (owns()) testing.value = false }
}
watch([() => props.disabled, () => currentUserIdentity(currentUser.value), () => currentUser.value?.role, () => currentUser.value?.status, getCurrentUserLogoutEpoch], () => {
  lease = {}; surfaceVersion.value += 1; draftVersion += 1
  Object.assign(config, empty()); expanded.value = loaded.value = loading.value = saving.value = testing.value = dirty.value = false; clearFailure()
}, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { alive = false; lease = {}; Object.assign(config, empty()); clearFailure() })
</script>
