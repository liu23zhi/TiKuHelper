<template>
  <div v-if="accountMounted" :key="accountKey" ref="accountSurface" class="console-page search-account-page" :onInputCapture="accountEntry.guardNative" :onChangeCapture="accountEntry.guardNative" :onClickCapture="accountEntry.guardNative">
    <ConsolePageHeading title="默认账号配置" description="维护题库渠道、默认参数和单价。">
      <template #actions><a-space wrap>
        <a-button :loading="tabLoading" :disabled="!canManageConfigs() || accountDraining || savingAll" :onClick="accountEntry.bind(handleSearchAccountConfigs)">刷新</a-button>
        <a-button type="primary" :loading="savingAll" :disabled="!canManageConfigs() || accountDraining || !configsLoaded || tabLoading || savingAll" :onClick="accountEntry.bind(onSaveAll)">保存全部配置</a-button>
      </a-space></template>
    </ConsolePageHeading>
    <a-alert v-if="!canManageConfigs()" type="warning" show-icon class="page-alert" message="当前身份尚未确认或无默认账号配置管理权限，请重新登录管理员账号。" />
    <a-alert v-if="configsError" type="error" show-icon class="page-alert" :message="configsError" />
    <section v-if="readFailure" class="page-alert" data-search-account-read-feedback>
      <RequestFailureAlert :key="readFailureContextKey" :failure="readFailure" :message="configsError" :busy="tabLoading" :context-key="readFailureContextKey" :is-current="isCurrentReadFailure" />
    </section>
    <a-alert v-if="hasUnsavedChanges" type="warning" show-icon class="page-alert" message="有未保存的更改，离开或刷新前请先保存。" />
    <section v-if="saveFailure" class="page-alert" data-search-account-save-feedback>
      <RequestFailureAlert :key="saveFailureContextKey" :failure="saveFailure" :message="saveFailureText" :busy="savingAll" :context-key="saveFailureContextKey" :is-current="isCurrentSaveFailure" />
    </section>
    <ConsolePanel title="渠道配置" description="按渠道维护默认参数，保存后应用于后续搜题请求。">
    <a-alert
      type="info"
      show-icon
      class="page-alert"
      message="匿名搜索请求不能使用系统默认账号；只有携带有效 Authorization: Basic xxxx 的请求，在未显式提供参数时才会回退到这里的默认配置。"
    />
    <ConsoleTableShell label="题库默认账号配置"><a-table
      bordered
      :data-source="accountDraining ? drainingRows : dataSource"
      :columns="columns"
      :loading="tabLoading"
      :pagination="false"
      row-key="provider"
      table-layout="fixed"
      :scroll="{ x: sumTableColumnWidths(columns) }"
    >
      <template #bodyCell="{ column, record }">
        <div v-if="column.key === 'defaultParameter'" class="config-cell">
          <div class="config-cell-text">{{ buildSearchAccountDefaultParameterHint(record) }}</div>
          <template v-if="record.provider === 'ai'">
            <a-button type="primary" ghost class="config-ai-button" :disabled="!canManageConfigs() || accountDraining" :onClick="accountEntry.bind(goToAIConfig)">前往 AI 搜索配置页</a-button>
          </template>
          <template v-else-if="record.tokenKey">
            <a-input-password :value="record.tokenValue" v-on="{ 'update:value': accountEntry.bind(updateConfig, record, 'tokenValue') }" :disabled="!canManageConfigs() || accountDraining || !configsLoaded || savingAll" :aria-label="(record.providerLabel || record.provider) + ' 默认 Token'" placeholder="请输入默认 Token" />
          </template>
        </div>
        <div v-else-if="column.key === 'unitPrice'" class="config-cell">
          <div class="config-cell-text">{{ buildSearchAccountUnitPriceHint(record) }}</div>
          <template v-if="record.provider !== 'ai'">
            <a-input-number
              :value="record.flatPrice"
              v-on="{ 'update:value': accountEntry.bind(updateConfig, record, 'flatPrice') }"
              :disabled="!canManageConfigs() || accountDraining || !configsLoaded || savingAll"
              :aria-label="(record.providerLabel || record.provider) + ' 固定单价'"
              :min="0"
              :precision="8"
              :step="0.01"
              style="width: 100%"
              placeholder="请输入固定单价"
            />
          </template>
        </div>
        <a-switch v-else-if="column.key === 'enabled'" :checked="!!record.enabled" :disabled="!canManageConfigs() || accountDraining || !configsLoaded || savingAll" :aria-label="(record.providerLabel || record.provider) + ' 启用'" :onChange="accountEntry.bind(onEnabledChange, record.provider)" />
        <a-input v-else-if="column.key === 'remark'" :value="record.remark" v-on="{ 'update:value': accountEntry.bind(updateConfig, record, 'remark') }" :disabled="!canManageConfigs() || accountDraining || !configsLoaded || savingAll" :aria-label="(record.providerLabel || record.provider) + ' 备注'" placeholder="备注" />
      </template>
    </a-table></ConsoleTableShell>
    </ConsolePanel>
  </div>
</template>

<script>
import { computed, defineComponent, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, watch } from 'vue';
import { onBeforeRouteLeave, useRouter } from 'vue-router';
import { message, Modal } from 'ant-design-vue';
import RequestFailureAlert from '@/components/RequestFailureAlert.vue';
import { inheritTransportRequestId } from '@/utils/requestHelpers.mjs';
import { inheritRequestFailureView } from '@/utils/requestFailureView.mjs';
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs';
import { getSearchAccountConfigs, updateSearchAccountConfig } from '@/api/TikuComponentApi';
import { isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser';
import { useUnsavedDraft } from '@/utils/useUnsavedDraft';
import {
  buildSearchAccountConfigSavePayloads,
  buildSearchAccountDefaultParameterHint,
  buildSearchAccountUnitPriceHint,
} from '@/utils/paymentMethodHelpers.mjs';

// Keep only fixed public classification and a validated ID, never the PUT config/Token.
const projectSaveFailure = source => {
  const carrier = Object.freeze({})
  inheritTransportRequestId(carrier, source)
  return inheritRequestFailureView(carrier, source)
}

export default defineComponent({
  components: { RequestFailureAlert },
  name: 'SearchAccountConfig',
  setup() {
    const router = useRouter()
    const { currentUser } = useCurrentUserDisplay()
    const dataSource = ref([])
    const tabLoading = ref(false)
    const savingAll = ref(false)
    const configsLoaded = ref(false)
    const configsError = ref('')
    const accountSurface = ref(null)
    const accountMounted = ref(true)
    const accountKey = ref(0)
    const accountDraining = ref(false)
    const drainingRows = ref([])
    const principal = () => {
      const id = currentUser.value?.id
      if (typeof id !== 'number' && (typeof id !== 'string' || !/^[0-9]+$/.test(id.trim()))) return ''
      const value = Number(id)
      return Number.isSafeInteger(value) && value > 0 && value <= 2147483647 ? `id:${value}` : ''
    }
    const role = () => String(currentUser.value?.role || '').trim().toLowerCase()
    let alive = true
    let mounted = false
    let epoch = 0
    let readVersion = 0
    let writeVersion = 0
    let renderVersion = 0
    let refreshPrompt = null
    const canManageConfigs = () => alive && Boolean(principal()) && isAdminUser(currentUser.value)
    const captureOwner = () => {
      const ticket = epoch, identity = principal(), privilege = role()
      return () => alive && ticket === epoch && identity === principal() && privilege === role() && canManageConfigs()
    }
    const saveFailure = shallowRef(null)
    const saveFailureContextKey = ref(0)
    const saveFailureText = ref('')
    let saveFailureOwner = () => false
    const clearSaveFailure = () => {
      saveFailure.value = null
      saveFailureText.value = ''
      saveFailureOwner = () => false
      saveFailureContextKey.value += 1
    }
    // This helper's closure has only authority and numbers, not a save's secret snapshot.
    const captureSaveFailureOwner = (owns, ticket) => {
      const context = saveFailureContextKey.value
      return () => owns() && ticket === writeVersion && context === saveFailureContextKey.value
    }
    const showSaveFailure = (carrier, owns, partial) => {
      if (!carrier || !owns()) return
      saveFailureOwner = owns
      saveFailureText.value = partial
        ? '部分配置已提交，后续保存失败；当前编辑仍保留，请核对后重试。'
        : '保存默认账号配置失败，当前编辑仍保留，请重试。'
      saveFailure.value = carrier
    }
    const isCurrentSaveFailure = (failure, context) => saveFailureOwner() && Boolean(failure)
      && failure === saveFailure.value && context === saveFailureContextKey.value
    const readFailure = shallowRef(null)
    const readFailureContextKey = ref(0)
    let readFailureOwner = () => false
    const clearReadFailure = () => {
      readFailure.value = null
      readFailureOwner = () => false
      readFailureContextKey.value += 1
    }
    const captureReadFailureOwner = (owns, ticket) => {
      const context = readFailureContextKey.value
      return () => owns() && ticket === readVersion && context === readFailureContextKey.value
    }
    const showReadFailure = (carrier, owns) => {
      if (!carrier || !owns()) return
      readFailureOwner = owns
      readFailure.value = carrier
    }
    const isCurrentReadFailure = (failure, context) => readFailureOwner() && Boolean(failure)
      && failure === readFailure.value && context === readFailureContextKey.value
    const makeEntry = () => {
      const owns = captureOwner(), version = renderVersion
      const isCurrent = () => owns() && version === renderVersion && !accountDraining.value
      return {
        guardNative(event) {
          if (isCurrent()) return
          event.preventDefault(); event.stopImmediatePropagation()
        },
        bind: (fn, ...args) => (...events) => isCurrent() ? fn(...args, ...events) : undefined,
      }
    }
    const accountEntry = shallowRef(makeEntry())
    const renewEntry = () => { renderVersion += 1; accountEntry.value = makeEntry() }
    const draftState = reactive({ configs: computed(() => dataSource.value) })
    const { currentConfigSnapshot, savedConfigSnapshot, hasUnsavedChanges, requestRouteLeave: requestDraftLeave } = useUnsavedDraft(draftState, { focusTriggerAfterClose: false })
    const requestRouteLeave = async to => {
      const ticket = epoch
      const allow = await requestDraftLeave(to)
      if (!allow || !alive || ticket !== epoch) return false
      // A real router leave waits for Antd's accepted controlled-input queue.
      await nextTick()
      await nextTick()
      return Boolean(alive && ticket === epoch)
    }
    onBeforeRouteLeave(requestRouteLeave)
    const closeRefreshPrompt = () => {
      const prompt = refreshPrompt
      if (!prompt) return
      refreshPrompt = null
      prompt.resolve(false); prompt.modal?.destroy()
    }
    const confirmRefresh = owns => {
      if (refreshPrompt) return refreshPrompt.promise
      if (!owns()) return Promise.resolve(false)
      const prompt = { resolve: null, modal: null, promise: null }
      prompt.promise = new Promise(resolve => { prompt.resolve = resolve })
      refreshPrompt = prompt
      const settle = allow => {
        if (refreshPrompt !== prompt) return
        refreshPrompt = null
        prompt.resolve(Boolean(allow && owns())); prompt.modal?.destroy()
      }
      prompt.modal = Modal.confirm({
        title: '有未保存的更改，确认刷新？', content: '刷新将放弃当前未保存的默认参数编辑。',
        okText: '放弃更改', cancelText: '保留编辑', autoFocusButton: 'cancel', focusTriggerAfterClose: false,
        onOk: () => settle(true), onCancel: () => settle(false),
      })
      return prompt.promise
    }
    const normalizeConfigs = configs => configs.map(item => ({
      ...item, enabled: !!item.enabled, flatPrice: Number(item?.flatPrice || 0),
    }))
    const reportReadFailure = (carrier = null, ownsFailure = () => false) => {
      configsError.value = configsLoaded.value
        ? '刷新默认账号配置失败，当前已加载的配置与编辑仍保留，请重试。'
        : '获取默认账号配置失败，请重试；未加载成功前不能保存。'
      showReadFailure(carrier, ownsFailure)
      message.error(configsError.value)
    }
    const loadConfigs = () => {
      if (!canManageConfigs() || accountDraining.value || savingAll.value) return
      const owns = captureOwner(), ticket = ++readVersion
      const before = currentConfigSnapshot.value
      clearSaveFailure()
      clearReadFailure()
      const ownsFailure = captureReadFailureOwner(owns, ticket)
      let failure = null
      tabLoading.value = true; configsError.value = ''
      return Promise.resolve().then(() => {
        if (!owns()) return null
        return getSearchAccountConfigs({ errorFeedback: 'local' }).catch(error => {
          // Project only this GET failure, never normalization/cache exceptions.
          if (owns() && ticket === readVersion) failure = projectSaveFailure(error)
          throw error
        })
      }).then(res => {
        if (!owns() || ticket !== readVersion) return
        if (res?.status !== 200 || !Array.isArray(res.data) || res.data.some(item => !item || typeof item !== 'object' || Array.isArray(item))) {
          reportReadFailure(projectSaveFailure(res), ownsFailure)
          return
        }
        // An accepted read cannot replace edits made after that read began.
        if (currentConfigSnapshot.value !== before) return
        dataSource.value = normalizeConfigs(res.data)
        configsLoaded.value = true
        savedConfigSnapshot.value = currentConfigSnapshot.value
        renewEntry()
      }).catch(() => {
        if (owns() && ticket === readVersion) {
          reportReadFailure(failure, ownsFailure)
        }
      }).finally(() => { if (owns() && ticket === readVersion) tabLoading.value = false })
    }
    const handleSearchAccountConfigs = async () => {
      if (!canManageConfigs() || accountDraining.value || savingAll.value) return
      const owns = captureOwner()
      if (hasUnsavedChanges.value && !await confirmRefresh(owns)) return
      if (!owns() || accountDraining.value || savingAll.value) return
      return loadConfigs()
    }
    const updateConfig = (record, field, value) => {
      if (!canManageConfigs() || accountDraining.value || !configsLoaded.value || savingAll.value || !dataSource.value.includes(record)) return
      if (['tokenValue', 'flatPrice', 'remark'].includes(field)) {
        if (record[field] !== value) clearSaveFailure()
        record[field] = value
      }
    }
    const onEnabledChange = (provider, checked) => {
      if (!canManageConfigs() || accountDraining.value || !configsLoaded.value || savingAll.value) return
      const target = dataSource.value.find(item => item.provider === provider)
      if (target) {
        if (target.enabled !== !!checked) clearSaveFailure()
        target.enabled = !!checked
      }
    }
    const goToAIConfig = () => {
      if (canManageConfigs() && !accountDraining.value) router.push('/ai-search-config')
    }
    const onSaveAll = async () => {
      if (!canManageConfigs() || accountDraining.value || !configsLoaded.value || tabLoading.value || savingAll.value) return
      const owns = captureOwner(), ticket = ++writeVersion
      // The existing helper remains authoritative for provider filtering, trims and price precision.
      const payloads = buildSearchAccountConfigSavePayloads(dataSource.value)
      if (!payloads.length) { message.warning('当前没有可保存的默认账号配置'); return }
      const snapshot = currentConfigSnapshot.value
      clearSaveFailure()
      const ownsFailure = captureSaveFailureOwner(owns, ticket)
      let failure = null
      let completed = 0
      let succeeded = false
      savingAll.value = true
      try {
        for (const item of payloads) {
          if (!owns() || ticket !== writeVersion) return
          let res
          try {
            res = await updateSearchAccountConfig(item.provider, item.payload, { errorFeedback: 'local' })
          } catch (error) {
            if (owns() && ticket === writeVersion) failure = projectSaveFailure(error)
            throw error
          }
          // Already-issued requests are not cancelled or described as rolled back.
          if (!owns() || ticket !== writeVersion) return
          if (res?.status !== 200) {
            failure = projectSaveFailure(res)
            throw new Error('默认账号配置提交失败')
          }
          completed += 1
        }
        if (!owns() || ticket !== writeVersion) return
        savedConfigSnapshot.value = snapshot
        message.success('保存成功')
        succeeded = true
      } catch {
        if (!owns() || ticket !== writeVersion) return
        savedConfigSnapshot.value = null
        showSaveFailure(failure, ownsFailure, completed > 0)
        message.error(completed ? '部分配置已提交，后续保存失败；当前编辑仍保留，请核对后重试。' : '保存默认账号配置失败，当前编辑仍保留，请重试。')
      } finally {
        if (owns() && ticket === writeVersion) {
          savingAll.value = false
          if (succeeded && currentConfigSnapshot.value === snapshot) loadConfigs()
        }
      }
    }
    // Clear private pixels synchronously, but keep Antd input refs alive for its
    // queued controlled-input reconciliation before disposing the guarded subtree.
    const hidePrivateSurface = () => {
      const surface = accountSurface.value
      if (!(surface instanceof HTMLElement)) return
      surface.hidden = true; surface.style.display = 'none'
      for (const input of surface.querySelectorAll('input, textarea')) {
        input.value = ''
        if (input.tagName === 'INPUT' && input.type !== 'checkbox' && input.type !== 'radio') input.type = 'password'
      }
      if (surface.contains(document.activeElement)) document.activeElement.blur?.()
    }
    const retireScope = () => {
      clearReadFailure()
      clearSaveFailure()
      epoch += 1; readVersion += 1; writeVersion += 1
      if (!accountDraining.value) drainingRows.value = dataSource.value
      accountDraining.value = true
      hidePrivateSurface()
      closeRefreshPrompt()
      requestDraftLeave({ path: '/login' })
      // Retained row references must not retain the previous actor's private draft.
      for (const row of dataSource.value) { row.tokenValue = ''; row.remark = '' }
      dataSource.value = []
      configsLoaded.value = false; configsError.value = ''
      tabLoading.value = false; savingAll.value = false
      savedConfigSnapshot.value = currentConfigSnapshot.value
      renewEntry()
      const ticket = epoch
      nextTick().then(() => nextTick()).then(async () => {
        if (!alive || ticket !== epoch) return
        accountMounted.value = false
        await nextTick()
        if (!alive || ticket !== epoch) return
        drainingRows.value = []
        accountKey.value += 1; accountMounted.value = true; accountDraining.value = false
        renewEntry()
        if (mounted && canManageConfigs()) loadConfigs()
      })
    }
    watch(() => [principal(), role()], (next, previous) => {
      if (next[0] !== previous[0] || next[1] !== previous[1]) retireScope()
    }, { flush: 'sync' })
    onMounted(() => { mounted = true; if (canManageConfigs()) loadConfigs() })
    onBeforeUnmount(() => { alive = false; mounted = false; retireScope() })
    return {
      readFailure, readFailureContextKey, isCurrentReadFailure,
      saveFailure, saveFailureContextKey, saveFailureText, isCurrentSaveFailure,
      dataSource, tabLoading, savingAll, configsLoaded, configsError, hasUnsavedChanges, requestRouteLeave,
      accountSurface, accountMounted, accountKey, accountDraining, drainingRows, accountEntry, canManageConfigs,
      normalizeConfigs, handleSearchAccountConfigs, onSaveAll, updateConfig, onEnabledChange, goToAIConfig,
      buildSearchAccountDefaultParameterHint, buildSearchAccountUnitPriceHint, sumTableColumnWidths,
      columns: [
        { title: '题库名称', dataIndex: 'providerLabel', key: 'providerLabel', width: 180 },
        { title: 'Provider', dataIndex: 'provider', key: 'provider', width: 140 },
        { title: '默认参数', dataIndex: 'defaultParameter', key: 'defaultParameter', width: 360 },
        { title: '单价', dataIndex: 'unitPrice', key: 'unitPrice', width: 280 },
        { title: '是否启用', dataIndex: 'enabled', key: 'enabled', width: 110 },
        { title: '备注', dataIndex: 'remark', key: 'remark', width: 220 },
      ],
    };
  },
});
</script>

<style scoped>
.page-alert { margin-bottom: 20px; }
.config-cell { display: flex; flex-direction: column; align-items: center; gap: 12px; }
.config-cell-text { color: var(--console-muted); line-height: 1.7; overflow-wrap: anywhere; }
.config-cell :deep(.ant-input-affix-wrapper), .config-cell :deep(.ant-input-number) { width: 100%; }
.config-ai-button { max-width: 100%; white-space: normal; height: auto; min-height: 32px; }
</style>
