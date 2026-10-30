<template>
  <div class="console-page extension-updates-page" :aria-busy="busy">
    <ConsolePageHeading title="扩展更新" description="管理浏览器扩展的自动检查与缓存版本，保存配置后立即生效。">
      <template #actions>
        <a-button :loading="loading" :disabled="saving || !!refreshing" @click="load(false)">刷新状态</a-button>
        <a-button type="primary" :loading="saving" :disabled="busy || !loaded || !dirty || !!validationMessage" @click="save">保存配置</a-button>
      </template>
    </ConsolePageHeading>
    <a-alert v-if="errorMessage" type="error" show-icon :message="errorMessage" role="alert" />
    <ConsolePanel title="更新设置" description="环境变量优先，其余设置从数据库读取。扩展 ID 留空可停用对应来源。">
      <a-spin :spinning="loading">
        <a-form layout="vertical" :disabled="busy || !loaded" @finish="save">
          <div class="extension-config-grid">
            <a-form-item label="自动检查更新" html-for="extension-auto-update">
              <a-switch id="extension-auto-update" v-model:checked="form.autoUpdate" :disabled="busy || !loaded || environmentOverrides.autoUpdate" checked-children="开启" un-checked-children="关闭" />
              <p class="field-hint">{{ environmentOverrides.autoUpdate ? '由环境变量 EXTENSION_UPDATE_ENABLED 控制，修改后需重启服务。' : '关闭后仍可手动检查更新。' }}</p>
            </a-form-item>
            <a-form-item label="检查间隔" html-for="extension-interval" required>
              <div class="interval-input-row">
                <a-input-number id="extension-interval" v-model:value="intervalValue" :disabled="busy || !loaded || environmentOverrides.intervalMinutes" :min="intervalMinimum" :max="intervalMaximum" :step="intervalStep" class="interval-input" />
                <a-select id="extension-interval-unit" v-model:value="intervalUnit" :disabled="busy || !loaded || environmentOverrides.intervalMinutes" class="interval-unit-select" aria-label="检查间隔单位">
                  <a-select-option v-for="unit in EXTENSION_INTERVAL_UNITS" :key="unit.value" :value="unit.value">{{ unit.label }}</a-select-option>
                </a-select>
              </div>
              <p class="field-hint">{{ environmentOverrides.intervalMinutes ? '由环境变量 EXTENSION_UPDATE_INTERVAL 控制，修改后需重启服务。' : '支持分钟、小时、天、周、月；月按 30 天换算。' }}</p>
            </a-form-item>
            <a-form-item label="Edge 扩展 ID" html-for="extension-edge-id">
              <a-input id="extension-edge-id" v-model:value="form.edgeId" :disabled="busy || !loaded || environmentOverrides.edgeId" :maxlength="32" allow-clear placeholder="Edge 商店扩展 ID" />
              <p v-if="environmentOverrides.edgeId" class="field-hint">由环境变量 EXTENSION_EDGE_ID 控制，修改后需重启服务。</p>
            </a-form-item>
            <a-form-item label="Chrome 扩展 ID" html-for="extension-chrome-id">
              <a-input id="extension-chrome-id" v-model:value="form.chromeId" :disabled="busy || !loaded || environmentOverrides.chromeId" :maxlength="32" allow-clear placeholder="未配置时不检查 Chrome 来源" />
              <p v-if="environmentOverrides.chromeId" class="field-hint">由环境变量 EXTENSION_CHROME_ID 控制，修改后需重启服务。</p>
            </a-form-item>
          </div>
          <a-alert v-if="validationMessage" type="warning" show-icon :message="validationMessage" />
          <p v-else-if="dirty" class="field-hint" role="status">配置尚未保存，请先保存再检查更新。</p>
        </a-form>
      </a-spin>
    </ConsolePanel>
    <div class="extension-source-grid">
      <ExtensionSourceStatus v-for="source in sources" :key="source.source" :source="source" :disabled="busy || dirty || !loaded" :refreshing="refreshing === source.source" @refresh="refresh" />
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { getExtensionUpdateConfig, saveExtensionUpdateConfig, refreshExtensionUpdate } from '@/api/ExtensionUpdateApi'
import { extensionDatabasePayload, normalizeExtensionConfig, normalizeExtensionOverrides, normalizeExtensionSources, validateExtensionConfig, extensionIntervalToMinutes, splitExtensionInterval, EXTENSION_INTERVAL_MAX_MINUTES, EXTENSION_INTERVAL_UNITS } from '@/utils/extensionUpdateHelpers.mjs'
import ExtensionSourceStatus from './ExtensionSourceStatus.vue'

defineOptions({ name: 'ExtensionUpdates' })
const form = reactive(normalizeExtensionConfig())
const initialInterval = splitExtensionInterval(form.intervalMinutes)
const intervalValue = ref(initialInterval.value), intervalUnit = ref(initialInterval.unit)
const savedConfig = ref(null), sources = ref(normalizeExtensionSources())
const databaseConfig = ref(normalizeExtensionConfig()), environmentOverrides = ref(normalizeExtensionOverrides())
const loading = ref(false), saving = ref(false), refreshing = ref(''), loaded = ref(false), errorMessage = ref('')
const busy = computed(() => loading.value || saving.value || !!refreshing.value)
const payload = computed(() => normalizeExtensionConfig({ ...form, intervalMinutes: extensionIntervalToMinutes(intervalValue.value, intervalUnit.value) }))
const dirty = computed(() => savedConfig.value && JSON.stringify(payload.value) !== JSON.stringify(savedConfig.value))
const validationMessage = computed(() => validateExtensionConfig(payload.value))
const intervalFactor = computed(() => EXTENSION_INTERVAL_UNITS.find(unit => unit.value === intervalUnit.value)?.minutes || 1)
const intervalMinimum = computed(() => 1)
const intervalMaximum = computed(() => Math.floor(EXTENSION_INTERVAL_MAX_MINUTES / intervalFactor.value))
const intervalStep = computed(() => 1)
let active = true
onBeforeUnmount(() => { active = false })

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
  if (loading.value) return false
  loading.value = true
  try {
    const response = await getExtensionUpdateConfig()
    if (!active) return false
    if (response.status !== 200) throw new Error('load failed')
    apply(response.data, resetForm || !loaded.value)
    errorMessage.value = ''
    return true
  } catch {
    if (active) errorMessage.value = '读取扩展更新状态失败，请重试。'
    return false
  } finally { if (active) loading.value = false }
}
async function save() {
  if (busy.value || !loaded.value || !dirty.value || validationMessage.value) return
  saving.value = true
  try {
    const response = await saveExtensionUpdateConfig(extensionDatabasePayload(payload.value, databaseConfig.value, environmentOverrides.value))
    if (!active) return
    if (response.status !== 200) throw new Error('save failed')
    apply(response.data, true); errorMessage.value = ''; message.success('扩展更新配置已保存')
  } catch {
    if (active) errorMessage.value = '保存配置失败，请刷新状态核对后重试。'
  } finally { if (active) saving.value = false }
}
async function refresh(source) {
  if (busy.value || dirty.value || !loaded.value) return
  refreshing.value = source
  try {
    const response = await refreshExtensionUpdate(source)
    if (!active) return
    if (response.status !== 200) throw new Error('refresh failed')
    if (await load(false)) message.success('扩展检查完成')
  } catch {
    if (!active) return
    await load(false)
    if (active) errorMessage.value = '检查更新失败，已有缓存保留，请稍后重试。'
  } finally { if (active) refreshing.value = '' }
}
onMounted(() => load(true))
</script>

<style scoped>
.extension-config-grid, .extension-source-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px 24px; }
.extension-config-grid .ant-form-item { margin-bottom: 0; }
.field-hint { margin: 8px 0 0; color: var(--console-muted); font-size: 13px; line-height: 1.7; }
.interval-input-row { display: flex; gap: 8px; }
.interval-input { flex: 1; min-width: 0; }
.interval-unit-select { width: 100px; flex: 0 0 100px; }
.extension-config-grid + .ant-alert { margin-top: 16px; }
@media (max-width: 1000px) { .extension-source-grid { grid-template-columns: 1fr; } }
@media (max-width: 640px) { .extension-config-grid { grid-template-columns: 1fr; } }
</style>
