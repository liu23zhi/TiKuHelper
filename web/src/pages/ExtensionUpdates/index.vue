<template>
  <div class="console-page extension-updates-page" :aria-busy="busy">
    <ConsolePageHeading title="扩展更新" description="管理浏览器扩展的自动检查与缓存版本，保存配置后立即生效。">
      <template #actions>
        <a-button :loading="loading" :disabled="!canManage() || saving || !!refreshing" :onClick="pageEntry.bind(load, false)">刷新状态</a-button>
        <a-button type="primary" :loading="saving" :disabled="!canManage() || busy || !loaded || !dirty || !!validationMessage" :onClick="pageEntry.bind(save)">保存配置</a-button>
      </template>
    </ConsolePageHeading>
    <RequestFailureAlert v-if="saveFailure" :key="saveFailureContextKey" :failure="saveFailure" :message="SAVE_FAILURE_MESSAGE" :busy="saving" :context-key="saveFailureContextKey" :is-current="isCurrentSaveFailure" />
    <a-alert v-else-if="errorMessage" type="error" show-icon :message="errorMessage" role="alert" />
    <a-alert v-if="!canManage()" type="warning" show-icon message="当前身份尚未确认或无扩展管理权限，请重新登录管理员账号。" />
    <ConsolePanel :key="pageEpoch" title="更新设置" description="环境变量优先，其余设置从数据库读取。扩展 ID 留空可停用对应来源。">
      <a-spin :spinning="loading">
        <a-form :model="form" layout="vertical" :disabled="!canManage() || busy || !loaded" :onFinish="pageEntry.bind(save)">
          <div class="extension-config-grid">
            <a-form-item label="自动检查更新" html-for="extension-auto-update">
              <a-switch id="extension-auto-update" :checked="form.autoUpdate" :onUpdate:checked="pageEntry.bind(updateField, 'autoUpdate')" :disabled="busy || !loaded || environmentOverrides.autoUpdate" checked-children="开启" un-checked-children="关闭" />
              <p class="field-hint">{{ environmentOverrides.autoUpdate ? '由环境变量 EXTENSION_UPDATE_ENABLED 控制，修改后需重启服务。' : '关闭后仍可手动检查更新。' }}</p>
            </a-form-item>
            <a-form-item label="检查间隔" html-for="extension-interval" required :validate-status="configValidation.field === 'intervalMinutes' ? 'error' : undefined">
              <div class="interval-input-row">
                <a-input-number id="extension-interval" :aria-invalid="configValidation.field === 'intervalMinutes' ? 'true' : undefined" :aria-describedby="configValidation.field === 'intervalMinutes' ? validationIdPrefix + '-interval-error' : undefined" :value="intervalValue" :onUpdate:value="pageEntry.bind(updateIntervalValue)" :disabled="busy || !loaded || environmentOverrides.intervalMinutes" :min="intervalMinimum" :max="intervalMaximum" :step="intervalStep" class="interval-input" />
                <div class="interval-unit-group">
                  <label for="extension-interval-unit">检查间隔单位</label>
                <a-select id="extension-interval-unit" :value="intervalUnit" :onUpdate:value="pageEntry.bind(updateIntervalUnit)" :disabled="busy || !loaded || environmentOverrides.intervalMinutes" class="interval-unit-select" aria-label="检查间隔单位">
                  <a-select-option v-for="unit in EXTENSION_INTERVAL_UNITS" :key="unit.value" :value="unit.value">{{ unit.label }}</a-select-option>
                </a-select>
                </div>
              </div>
              <p class="field-hint">{{ environmentOverrides.intervalMinutes ? '由环境变量 EXTENSION_UPDATE_INTERVAL 控制，修改后需重启服务。' : '支持分钟、小时、天、周、月；月按 30 天换算。' }}</p>
              <template #help><span v-if="configValidation.field === 'intervalMinutes'" :id="validationIdPrefix + '-interval-error'" role="alert">{{ configValidation.message }}</span></template>
            </a-form-item>
            <a-form-item label="Edge 扩展 ID" html-for="extension-edge-id" :validate-status="configValidation.field === 'edgeId' ? 'error' : undefined">
              <a-input id="extension-edge-id" :aria-invalid="configValidation.field === 'edgeId' ? 'true' : undefined" :aria-describedby="configValidation.field === 'edgeId' ? validationIdPrefix + '-edge-error' : undefined" :value="form.edgeId" :onUpdate:value="pageEntry.bind(updateField, 'edgeId')" :disabled="busy || !loaded || environmentOverrides.edgeId" :maxlength="32" allow-clear placeholder="Edge 商店扩展 ID" />
              <p v-if="environmentOverrides.edgeId" class="field-hint">由环境变量 EXTENSION_EDGE_ID 控制，修改后需重启服务。</p>
              <template #help><span v-if="configValidation.field === 'edgeId'" :id="validationIdPrefix + '-edge-error'" role="alert">{{ configValidation.message }}</span></template>
            </a-form-item>
            <a-form-item label="Chrome 扩展 ID" html-for="extension-chrome-id" :validate-status="configValidation.field === 'chromeId' ? 'error' : undefined">
              <a-input id="extension-chrome-id" :aria-invalid="configValidation.field === 'chromeId' ? 'true' : undefined" :aria-describedby="configValidation.field === 'chromeId' ? validationIdPrefix + '-chrome-error' : undefined" :value="form.chromeId" :onUpdate:value="pageEntry.bind(updateField, 'chromeId')" :disabled="busy || !loaded || environmentOverrides.chromeId" :maxlength="32" allow-clear placeholder="未配置时不检查 Chrome 来源" />
              <p v-if="environmentOverrides.chromeId" class="field-hint">由环境变量 EXTENSION_CHROME_ID 控制，修改后需重启服务。</p>
              <template #help><span v-if="configValidation.field === 'chromeId'" :id="validationIdPrefix + '-chrome-error'" role="alert">{{ configValidation.message }}</span></template>
            </a-form-item>
          </div>
          <a-alert v-if="validationMessage" type="warning" show-icon :message="validationMessage" />
          <p v-else-if="dirty" class="field-hint" role="status">配置尚未保存，请先保存再检查更新。</p>
        </a-form>
      </a-spin>
    </ConsolePanel>
    <div class="extension-source-grid">
      <ExtensionSourceStatus v-for="source in sources" :key="pageEpoch + ':' + source.source" :source="source" :disabled="busy || dirty || !loaded" :refreshing="refreshing === source.source" :onRefresh="pageEntry.bind(refresh)" />
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref, shallowRef, useId, watch } from 'vue'
import { useCurrentUserDisplay, isAdminUser } from '@/utils/currentUser'
import { useCurrentUserRequestScope } from '@/utils/currentUserScope'
import { message } from 'ant-design-vue'
import { getExtensionUpdateConfig, saveExtensionUpdateConfig, refreshExtensionUpdate } from '@/api/ExtensionUpdateApi'
import { extensionDatabasePayload, normalizeExtensionConfig, normalizeExtensionOverrides, normalizeExtensionSources, getExtensionConfigValidation, extensionIntervalToMinutes, splitExtensionInterval, EXTENSION_INTERVAL_MAX_MINUTES, EXTENSION_INTERVAL_UNITS } from '@/utils/extensionUpdateHelpers.mjs'
import ExtensionSourceStatus from './ExtensionSourceStatus.vue'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'

defineOptions({ name: 'ExtensionUpdates' })
const validationIdPrefix = `extension-validation-${useId()}`
const form = reactive(normalizeExtensionConfig())
const initialInterval = splitExtensionInterval(form.intervalMinutes)
const intervalValue = ref(initialInterval.value), intervalUnit = ref(initialInterval.unit)
const savedConfig = ref(null), sources = ref(normalizeExtensionSources())
const databaseConfig = ref(normalizeExtensionConfig()), environmentOverrides = ref(normalizeExtensionOverrides())
const loading = ref(false), saving = ref(false), refreshing = ref(''), loaded = ref(false), errorMessage = ref('')
const SAVE_FAILURE_MESSAGE = '保存配置失败，请刷新状态核对后重试。'
const saveFailure = shallowRef(null), saveFailureContextKey = ref(0)
let saveDraftLease = {}, saveFailureOwner = () => false
const busy = computed(() => loading.value || saving.value || !!refreshing.value)
const payload = computed(() => normalizeExtensionConfig({ ...form, intervalMinutes: extensionIntervalToMinutes(intervalValue.value, intervalUnit.value) }))
const saveDraftPayload = computed(() => extensionDatabasePayload(payload.value, databaseConfig.value, environmentOverrides.value))
const dirty = computed(() => savedConfig.value && JSON.stringify(payload.value) !== JSON.stringify(savedConfig.value))
const configValidation = computed(() => getExtensionConfigValidation(payload.value))
const validationMessage = computed(() => configValidation.value.message)
const intervalFactor = computed(() => EXTENSION_INTERVAL_UNITS.find(unit => unit.value === intervalUnit.value)?.minutes || 1)
const intervalMinimum = computed(() => 1)
const intervalMaximum = computed(() => Math.floor(EXTENSION_INTERVAL_MAX_MINUTES / intervalFactor.value))
const intervalStep = computed(() => 1)
let active = true, loadVersion = 0
const pageEpoch = ref(0)
const { currentUser } = useCurrentUserDisplay()
function canManage() {
  const id = Number(currentUser.value?.id)
  return active && Number.isInteger(id) && id > 0 && id <= 2147483647
    && String(currentUser.value?.status || 'enabled') === 'enabled' && isAdminUser(currentUser.value)
}
function clearOwned() {
  retireSaveDraft()
  loadVersion += 1; pageEpoch.value += 1
  Object.assign(form, normalizeExtensionConfig())
  const interval = splitExtensionInterval(form.intervalMinutes)
  intervalValue.value = interval.value; intervalUnit.value = interval.unit
  savedConfig.value = null; sources.value = normalizeExtensionSources()
  databaseConfig.value = normalizeExtensionConfig(); environmentOverrides.value = normalizeExtensionOverrides()
  loading.value = saving.value = loaded.value = false; refreshing.value = ''; errorMessage.value = ''
}
const scope = useCurrentUserRequestScope(currentUser, {
  clear: clearOwned, reset() {}, reload() { if (canManage()) load(true) },
})
function capturePage() {
  const owns = scope.captureDataOwner(), epoch = pageEpoch.value
  return () => owns() && epoch === pageEpoch.value && canManage()
}
function clearSaveFailure() {
  saveFailure.value = null
  saveFailureOwner = () => false
  saveFailureContextKey.value += 1
}
function retireSaveDraft() { saveDraftLease = {}; clearSaveFailure() }
function captureSaveDraftOwner() {
  const owns = capturePage(), draft = saveDraftLease
  return () => owns() && draft === saveDraftLease && loaded.value
}
function showSaveFailure(failure, owns) {
  if (!owns()) return
  saveFailureOwner = owns
  saveFailure.value = ensureRequestFailure(failure)
}
function isCurrentSaveFailure(failure, context) {
  return saveFailureOwner() && Boolean(failure) && failure === saveFailure.value && context === saveFailureContextKey.value
}
const pageEntry = { bind(action, ...args) {
  const owns = capturePage()
  return (...events) => { if (owns()) return action(...args, ...events) }
} }
function updateField(key, value) {
  if (canManage() && loaded.value && !busy.value && !environmentOverrides.value[key]) form[key] = value
}
function updateIntervalValue(value) {
  if (canManage() && loaded.value && !busy.value && !environmentOverrides.value.intervalMinutes) intervalValue.value = value
}
function updateIntervalUnit(value) {
  if (canManage() && loaded.value && !busy.value && !environmentOverrides.value.intervalMinutes) intervalUnit.value = value
}
// Effective database fields retire only feedback, never an issued save's busy state.
watch(['autoUpdate', 'intervalMinutes', 'edgeId', 'chromeId'].map(key => () => saveDraftPayload.value[key]), retireSaveDraft, { flush: 'sync' })
watch(() => currentUser.value?.status, () => { clearOwned(); if (canManage()) load(true) }, { flush: 'sync' })
onBeforeUnmount(() => { active = false; retireSaveDraft() })

function apply(data, resetForm) {
  if (!data?.config || !Array.isArray(data.sources)) throw new Error('invalid response')
  const replaceForm = resetForm || !loaded.value || !dirty.value
  savedConfig.value = normalizeExtensionConfig(data.config)
  databaseConfig.value = normalizeExtensionConfig(data.databaseConfig || data.config)
  environmentOverrides.value = normalizeExtensionOverrides(data.environmentOverrides)
  sources.value = normalizeExtensionSources(data.sources)
  if (replaceForm) {
    Object.assign(form, savedConfig.value)
    const interval = splitExtensionInterval(savedConfig.value.intervalMinutes)
    intervalValue.value = interval.value
    intervalUnit.value = interval.unit
  }
  else for (const key of Object.keys(environmentOverrides.value)) {
    if (environmentOverrides.value[key]) {
      form[key] = savedConfig.value[key]
      if (key === 'intervalMinutes') {
        const interval = splitExtensionInterval(savedConfig.value.intervalMinutes)
        intervalValue.value = interval.value
        intervalUnit.value = interval.unit
      }
    }
  }
  loaded.value = true
}
async function load(resetForm = false) {
  if (!canManage() || loading.value) return false
  const owns = capturePage(), version = ++loadVersion
  retireSaveDraft()
  loading.value = true
  try {
    const response = await getExtensionUpdateConfig()
    if (!owns() || version !== loadVersion) return false
    if (response.status !== 200) throw new Error('load failed')
    apply(response.data, resetForm || !loaded.value)
    errorMessage.value = ''
    return true
  } catch {
    if (owns() && version === loadVersion) errorMessage.value = '读取扩展更新状态失败，请重试。'
    return false
  } finally { if (owns() && version === loadVersion) loading.value = false }
}
async function save() {
  if (!canManage() || busy.value || !loaded.value || !dirty.value || validationMessage.value) return
  const owns = capturePage()
  const ownsDraft = captureSaveDraftOwner()
  clearSaveFailure()
  saving.value = true
  try {
    const response = await saveExtensionUpdateConfig(extensionDatabasePayload(payload.value, databaseConfig.value, environmentOverrides.value))
    if (!owns()) return
    if (response.status !== 200) throw response
    apply(response.data, true); errorMessage.value = ''; message.success('扩展更新配置已保存')
  } catch (error) {
    if (owns()) {
      showSaveFailure(error, ownsDraft)
      errorMessage.value = '保存配置失败，请刷新状态核对后重试。'
    }
  } finally { if (owns()) saving.value = false }
}
async function refresh(source) {
  if (!canManage() || busy.value || dirty.value || !loaded.value) return
  const owns = capturePage()
  retireSaveDraft()
  refreshing.value = source
  try {
    const response = await refreshExtensionUpdate(source)
    if (!owns()) return
    if (response.status !== 200) throw new Error('refresh failed')
    if (await load(false) && owns()) message.success('扩展检查完成')
  } catch {
    if (!owns()) return
    await load(false)
    if (owns()) errorMessage.value = '检查更新失败，已有缓存保留，请稍后重试。'
  } finally { if (owns()) refreshing.value = '' }
}
onMounted(() => { if (canManage()) load(true) })
</script>

<style scoped>
.extension-config-grid, .extension-source-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px 24px; }
.extension-config-grid .ant-form-item { margin-bottom: 0; }
.field-hint { margin: 8px 0 0; color: var(--console-muted); font-size: 13px; line-height: 1.7; }
.interval-input-row { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.interval-input { flex: 1 1 120px; min-width: 0; }
.interval-unit-group { display: flex; align-items: center; gap: 8px; flex: 0 0 auto; }
.interval-unit-select { width: 100px; flex: 0 0 100px; }
.extension-config-grid + .ant-alert { margin-top: 16px; }
@media (max-width: 1000px) { .extension-source-grid { grid-template-columns: 1fr; } }
@media (max-width: 640px) { .extension-config-grid { grid-template-columns: 1fr; } }
</style>
