<template>
  <div :key="aiEntry.epoch" class="console-page ai-settings-page" :onInputCapture="aiEntry.guardNative" :onChangeCapture="aiEntry.guardNative" :onClickCapture="aiEntry.guardNative">
    <ConsolePageHeading title="AI 搜索配置" description="管理服务连接、执行预算和模型价格。">
      <template #actions><a-space wrap class="section-actions">
        <a-button :onClick="aiEntry.bind(handleLoad)" :loading="tabLoading">刷新</a-button>
        <a-button :disabled="!canUseConfigActions()" type="primary" :onClick="aiEntry.bind(handleSave)" :loading="saving">保存配置</a-button>
      </a-space></template>
    </ConsolePageHeading>

    <a-alert v-if="pageNeedsConfigRead" class="page-alert" type="info" show-icon
      message="页面归属已变更，请先刷新读取当前配置。"
      description="旧请求不会替当前账号完成操作；刷新不会自动调用模型或能力服务。" />
    <RequestFailureAlert v-if="configReadFailure" :failure="configReadFailure"
      message="获取 AI 搜索配置失败，请重试读取。" retryable :busy="tabLoading"
      :context-key="configReadContext" :is-current="isCurrentConfigReadFailure"
      :onRetry="aiEntry.bind(retryConfigRead)" />

    <a-alert v-if="hasUnsavedChanges" type="warning" show-icon class="page-alert"
      :message="savedConfigSnapshot === null ? '保存已完成，配置同步待确认' : '有未保存的更改'"
      description="请先保存编辑，或确认放弃后刷新。" />
    <a-alert
      type="info"
      show-icon
      class="page-alert"
      message="AI 搜索只在 use=ai 的请求模式下生效。账号余额必须大于 0；显式传入 url、key、model 时优先使用请求参数，未显式提供时才回退到这里的系统默认配置。"
    />

    <a-alert
      v-if="!pricingKnown"
      type="info"
      show-icon
      class="page-alert"
      message="模型价格待确认"
      description="价格读取尚未确认，暂不判断模型是否已定价。请在下方模型价格表刷新或重试读取。"
    />

    <a-alert
      v-else-if="pricingKnown && pricingGaps.configuredModel && !pricingGaps.configuredModelPriced"
      type="warning"
      show-icon
      class="page-alert"
      message="当前默认模型还没有配置价格"
      :description="`模型「${pricingGaps.configuredModel}」不在模型价格表中，调用时会按 0 元计费（不扣费），账单中会记录为未定价。请在下方「模型价格表」中新增该模型，并填写输入 / 缓存输入 / 输出单价。`"
    >
      <template #action>
        <a-button size="small" type="primary" ghost :onClick="aiEntry.bind(openPriceModalForConfiguredModel)">
          去设置价格
        </a-button>
      </template>
    </a-alert>

    <a-alert
      v-else-if="pricingKnown && pricingGaps.unresolvedModels.length"
      type="warning"
      show-icon
      class="page-alert"
      message="模型列表中有模型还没有配置价格"
      :description="`以下模型缺少价格，命中时会按 0 元计费：${pricingGaps.unresolvedModels.join('、')}。请在下方「模型价格表」中补充单价。`"
    />

    <nav class="ai-section-nav" aria-label="AI 配置分区">
      <a :href="`#${sectionIds.connection}`">连接与模型</a>
      <a :href="`#${sectionIds.advanced}`">高级能力</a>
      <a :href="`#${sectionIds.prompt}`">Prompt</a>
      <a :href="`#${sectionIds.pricing}`">模型价格表</a>
    </nav>

    <a-form :disabled="!canUseAIPage()" layout="vertical" class="ai-config-form" autocomplete="off">
      <AIConnectionModelSection
        :section-id="sectionIds.connection"
        :form-value="formState"
        :fetching-models="fetchingModels"
        :model-options="modelOptions"
        :price-rows="priceRows"
        :pricing-gaps="pricingGaps"
        :pricing-known="pricingKnown"
        v-on="{ 'update:form-value': aiEntry.bind(updateConnectionForm), 'test': aiEntry.bind(handleOpenTestDialog), 'price-for-configured-model': aiEntry.bind(openPriceModalForConfiguredModel), 'price-for-model': aiEntry.bind(openPriceModalForModel), 'fetch-models': aiEntry.bind(handleFetchModels), 'select-model': aiEntry.bind(selectModel) }"

      />
      <RequestFailureAlert v-if="modelFetchFailure" :failure="modelFetchFailure"
        message="获取 AI 模型列表失败，请核对连接后点击“获取远程模型列表”重新获取。"
        :busy="fetchingModels" :context-key="modelFetchContext"
        :is-current="isCurrentModelFetchFailure" />

      <AIAdvancedSettingsSection
        :section-id="sectionIds.advanced"
        :form-value="formState"
        :limits="advancedLimits"
        :integer-parser="parseIntegerInput"
        :search-engine-loading="searchEngineLoading"
        v-on="{ 'update:form-value': aiEntry.bind(updateAdvancedForm), 'view-capability': aiEntry.bind(loadSearchEngineInfo) }"

      />

      <AIPromptSettingsSection
        :section-id="sectionIds.prompt"
        :form-value="formState"
        :fixed-temperature-text="fixedTemperatureText"
        :fixed-top-p-text="fixedTopPText"
        v-on="{ 'update:form-value': aiEntry.bind(updatePromptForm) }"

      />
    </a-form>

    <ConsolePanel v-if="canUseAIPage()" :key="`ocr-${aiEntry.epoch}`" title="图片 OCR 缓存" description="按图片 URL 和图片内容指纹复用成功 OCR 结果；每次请求仍会下载图片确认内容未变化。">
      <template #actions>
        <a-space wrap>
          <a-button :onClick="aiEntry.bind(openOCRCachePage)">管理缓存条目</a-button>
          <a-button :onClick="aiEntry.bind(loadAIOCRCacheStatus)" :loading="ocrCacheLoading">刷新</a-button>
        </a-space>
      </template>
      <RequestFailureAlert v-if="ocrCacheFailure" :failure="ocrCacheFailure" :message="ocrCacheError" :context-key="ocrCacheFailureContext" :is-current="isCurrentOCRFailure" :busy="ocrCacheLoading" />
      <a-alert v-else-if="ocrCacheError" type="error" show-icon :message="ocrCacheError" />
      <div class="ocr-cache-grid">
        <div class="ocr-cache-stat">
          <span class="ocr-cache-stat__label">识别服务</span>
          <a-tag :color="ocrCacheStatus?.configured ? 'green' : 'default'">
            {{ !ocrCacheStatus ? '状态待确认' : ocrCacheStatus.configured ? 'UAPI 已配置' : '未配置有效 UAPI 密钥' }}
          </a-tag>
        </div>
        <div class="ocr-cache-stat">
          <span class="ocr-cache-stat__label">缓存条目</span>
          <strong>{{ ocrCacheStatus?.entries ?? '--' }}</strong>
        </div>
        <div class="ocr-cache-stat">
          <span class="ocr-cache-stat__label">最近更新</span>
          <span>{{ formatAIOCRCacheTime(ocrCacheStatus?.latestUpdatedAt) }}</span>
        </div>
      </div>
      <div class="hint">命中缓存仍按现有搜题计费口径处理；清空只删除已持久化的 OCR 结果，不会取消正在执行的请求。</div>
    </ConsolePanel>

    <AISearchCapabilityDialog
      :key="capabilitySession" :open="capabilityModalVisible" v-on="{ 'update:open': capabilityEntry.bind(value => capabilityModalVisible = value) }"
      :loading="capabilityLoading"
      :error="capabilityError"
      :detail="searchEngineDetail"
      :cards="usedCapabilityCards"
      :rows="usedUsageRows"
      :columns="usageColumns"
    />

    <ConsolePanel :id="sectionIds.pricing" class="pricing-grid" :aria-labelledby="`${sectionIds.pricing}-heading`" tabindex="-1">
        <template #heading>
          <h2 :id="`${sectionIds.pricing}-heading`" class="sub-card-title">模型价格表</h2>
          <p class="sub-card-subtitle">单价单位为 元 / 百万 token（人民币，无汇率换算）。实际扣费 =（输入 − 缓存输入）× 输入价 + 缓存输入 × 缓存价 + 输出 × 输出价。</p>
        </template>
        <template #actions>
          <a-space wrap>
            <a-popconfirm title="确定恢复内置默认价格表吗？自定义价格会被清空。" ok-text="确定" cancel-text="取消" :onConfirm="aiEntry.bind(handleResetModelPrices)">
              <a-button :loading="priceResetting">恢复默认</a-button>
            </a-popconfirm>
            <a-button :onClick="aiEntry.bind(loadAIModelPrices)" :loading="priceLoading">刷新</a-button>
            <a-button type="primary" :onClick="aiEntry.bind(openPriceModal, null)">新增模型价格</a-button>
          </a-space>
        </template>

        <a-form class="staged-billing-settings" :label-col="labelCol" :wrapper-col="wrapperCol">
          <a-form-item label="启用阶段计费" :html-for="`${sectionIds.pricing}-enableAIStagedTokenBilling`">
            <a-switch :id="`${sectionIds.pricing}-enableAIStagedTokenBilling`" :checked="formState.enableAIStagedTokenBilling" v-on="{ 'update:checked': aiEntry.bind(value => formState.enableAIStagedTokenBilling = value) }" />
            <div class="hint">按每次实际上游模型响应的总 token 数应用阶段倍率；具体阈值和倍率以你下方配置的阶段表为准。关闭时保持现有价格表计费。</div>
          </a-form-item>
          <a-form-item label="平滑变化倍率" :html-for="`${sectionIds.pricing}-smoothAIStagedTokenMultiplier`">
            <a-switch :id="`${sectionIds.pricing}-smoothAIStagedTokenMultiplier`"
              :checked="formState.smoothAIStagedTokenMultiplier" v-on="{ 'update:checked': aiEntry.bind(value => formState.smoothAIStagedTokenMultiplier = value) }"
              :disabled="!formState.enableAIStagedTokenBilling"
            />
            <div class="hint">开启后在相邻阶段节点之间线性平滑变化；阶段计费关闭时只保存此选项，不影响计费。</div>
          </a-form-item>
        </a-form>

        <div class="staged-points-editor">
          <div class="staged-points-editor__header">
            <div>
              <div class="sub-card-title">阶段倍率价格表</div>
              <div class="sub-card-subtitle">按单次实际上游模型响应的总 token 数匹配倍率；固定模式取所在区间的节点倍率，平滑模式在相邻节点之间线性插值。</div>
            </div>
            <a-space>
              <a-button size="small" :onClick="aiEntry.bind(resetStagedMultiplierPoints)">恢复默认阶段表</a-button>
              <a-button size="small" type="primary" :onClick="aiEntry.bind(addStagedMultiplierPoint)">新增阶段</a-button>
            </a-space>
          </div>
          <ConsoleTableShell label="阶段倍率价格表"><a-table
            bordered
            size="small"
            :row-key="stagedPointRowKey"
            :pagination="false"
            :data-source="formState.stagedTokenMultiplierPoints"
            :columns="stagedPointColumns"
            table-layout="fixed"
            :scroll="{ x: sumTableColumnWidths(stagedPointColumns) }"
          >
            <template #bodyCell="{ column, record, index }">
              <a-input v-if="column.key === 'tokens'" :value="record.tokens" v-on="{ 'update:value': aiEntry.bind(value => record.tokens = value) }" inputmode="numeric" autocomplete="off" style="width: 180px" />
              <a-input-number v-else-if="column.key === 'multiplier'" :value="record.multiplier" v-on="{ 'update:value': aiEntry.bind(value => record.multiplier = value) }" :precision="6" :step="0.1" style="width: 140px" />
              <a-button v-else-if="column.key === 'action'" type="link" danger :disabled="index === 0 || formState.stagedTokenMultiplierPoints.length <= 1" :onClick="aiEntry.bind(removeStagedMultiplierPoint, index)">删除</a-button>
            </template>
          </a-table></ConsoleTableShell>
          <div v-if="stagedPointError" class="hint hint-warning">{{ stagedPointError }}；修正后再保存。</div>
          <div v-else class="hint">至少保留首个 0 token 节点；后续阈值必须严格递增，倍率必须为有限数值且只能保持不变或递减。阈值不设业务上限，倍率推荐使用 0.3x～4x。</div>
        </div>

        <RequestFailureAlert
          v-if="priceReadFailure"
          :failure="priceReadFailure"
          message="获取模型价格表失败，请重试读取。"
          :retryable="true"
          :busy="priceLoading"
          :context-key="priceReadContext"
          :is-current="isCurrentPriceReadFailure"
          :onRetry="aiEntry.bind(retryPriceRead)"
        />

        <AIModelPriceTable
          :rows="priceRows"
          :loading="priceLoading"
          :read-failure="priceReadFailure"
          :read-confirmed="priceReadConfirmed"
        v-on="{ 'edit': aiEntry.bind(openPriceModal), 'delete': aiEntry.bind(handleDeleteModelPrice) }"

        />
    </ConsolePanel>

    <AIModelPriceEditorModal
      :key="priceDialogSession"
      :id-prefix="`${sectionIds.pricing}-editor`"
      :open="priceModalVisible"
      :form-value="priceForm"
      :confirm-loading="priceSaving"
      :label-col="modalLabelCol"
      :wrapper-col="modalWrapperCol"




      v-on="{ 'update:open': priceDialogEntry.bind(setPriceModalOpen), 'update:form-value': priceDialogEntry.bind(updatePriceForm), submit: priceDialogEntry.bind(handleSaveModelPrice), cancel: priceDialogEntry.bind(closePriceModal) }"
    />

    <AISearchTestDialog :open="testModalVisible" :action-context="aiEntry" v-on="{ 'update:open': aiEntry.bind(value => testModalVisible = value) }" :saved-compression-order="savedCompressionOrder" :config="formState" :model-options="modelOptions" />
  </div>
</template>

<script>
import { onBeforeUnmount, watch, defineComponent, computed, onMounted, reactive, ref, shallowRef } from 'vue';
import { message, Modal } from 'ant-design-vue';
import AISearchCapabilityDialog from './AISearchCapabilityDialog.vue';
import AISearchTestDialog from './AISearchTestDialog.vue';
import ConsolePageHeading from '@/components/ConsolePageHeading.vue';
import ConsolePanel from '@/components/ConsolePanel.vue';
import ConsoleTableShell from '@/components/ConsoleTableShell.vue';
import RequestFailureAlert from '@/components/RequestFailureAlert.vue';
import { ensureRequestFailure, inheritRequestFailureView } from '@/utils/requestFailureView.mjs'
import { inheritTransportRequestId } from '@/utils/requestHelpers.mjs';
import { useCurrentUserDisplay, isAdminUser, getCurrentUserLogoutEpoch, getStoredCurrentUser } from '@/utils/currentUser';
import { currentUserIdentity } from '@/utils/currentUserScope';
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs';
import { buildAIContextCompressionSettings } from '@/utils/aiSearchConfigHelpers.mjs';
import {
  getAISearchConfig,
  getAISearchModels,
  updateAISearchConfig,
  getAIModelPrices,
  getUAPISearchEngines,
  getUAPIUsageStatus,
  getAIOCRCacheStatus,
  createAIModelPrice,
  updateAIModelPrice,
  deleteAIModelPrice,
  resetAIModelPrices,
} from '@/api/TikuComponentApi';
import {
  buildModelNameOptions,
  buildModelNameText,
  buildAISearchConfigSavePayload,
  buildAIModelPriceSavePayload,
  buildAIModelPricingGaps,
  isModelPriced,
  filterRemoteModelNames,
  DEFAULT_AI_STAGED_TOKEN_MULTIPLIER_POINTS,
} from '@/utils/aiSearchModelHelpers.mjs';
import {
  createStagedPointRowKey,
  getStagedMultiplierPointsError,
  insertStagedMultiplierPoint,
} from '@/utils/aiStagedMultiplierHelpers.mjs';
import { useUnsavedDraft } from '@/utils/useUnsavedDraft'
import AIModelPriceTable from './AIModelPriceTable.vue';
import AIModelPriceEditorModal from './AIModelPriceEditorModal.vue';
import AIConnectionModelSection from './AIConnectionModelSection.vue';
import AIAdvancedSettingsSection from './AIAdvancedSettingsSection.vue';
import AIPromptSettingsSection from './AIPromptSettingsSection.vue';

function trimBaseUrl(url) {
  return String(url || '').trim().replace(/\/+$/, '')
}

function appendV1(url) {
  const trimmed = trimBaseUrl(url)
  if (!trimmed || trimmed.endsWith('/v1')) {
    return trimmed
  }
  return `${trimmed}/v1`
}

const DEFAULT_MAX_AI_SEARCH_MODEL_CALLS = 4
const DEFAULT_MAX_AI_SEARCHES = 3
const DEFAULT_MAX_AI_SEARCH_RETRIES = 3
const MIN_AI_SEARCH_MODEL_CALLS = 1
const MAX_AI_SEARCH_BUDGET = 20
const MIN_AI_SEARCHES = 1
const MIN_AI_SEARCH_RETRIES = 1
const MAX_AI_SEARCH_RETRIES = 10
const DEFAULT_MAX_AI_WEB_READS = 5
const MIN_AI_WEB_READS = 1
const MAX_AI_WEB_READS = 20

function parseIntegerInput(value) {
  if (value === null || value === undefined || value === '') {
    return ''
  }
  return String(value).replace(/[^0-9]/g, '')
}

function normalizeAISearchBudget(value, fallback) {
  const parsed = Number(value)
  if (!Number.isSafeInteger(parsed) || parsed < 1) {
    return fallback
  }
  return Math.min(parsed, MAX_AI_SEARCH_BUDGET)
}

function normalizeAISearchRetries(value, fallback = DEFAULT_MAX_AI_SEARCH_RETRIES) {
  const parsed = Number(value)
  if (!Number.isSafeInteger(parsed) || parsed < MIN_AI_SEARCH_RETRIES) {
    return fallback
  }
  return Math.min(parsed, MAX_AI_SEARCH_RETRIES)
}

function normalizeAIWebReads(value, fallback = DEFAULT_MAX_AI_WEB_READS) {
  const parsed = Number(value)
  if (!Number.isSafeInteger(parsed) || parsed < MIN_AI_WEB_READS) {
    return fallback
  }
  return Math.min(parsed, MAX_AI_WEB_READS)
}

function normalizeUsageRows(payload) {
  if (Array.isArray(payload?.endpoints)) {
    return payload.endpoints
      .filter(item => item && typeof item === 'object')
      .map(item => ({ path: String(item.path || ''), count: Number(item.count || 0) }))
      .filter(item => item.path)
  }
  if (payload?.path) {
    return [{ path: String(payload.path), count: Number(payload.count || 0) }]
  }
  return []
}

function projectOCRFailure(source) {
  const carrier = Object.freeze({})
  inheritTransportRequestId(carrier, source)
  return inheritRequestFailureView(carrier, source)
}

// DOM identity only: independent of configuration, user and request state.
let aiConfigSectionSequence = 0

export default defineComponent({
  name: 'AISearchConfig',
  components: { RequestFailureAlert, ConsolePageHeading, ConsolePanel, ConsoleTableShell, AISearchCapabilityDialog, AISearchTestDialog, AIModelPriceTable, AIModelPriceEditorModal, AIConnectionModelSection, AIAdvancedSettingsSection, AIPromptSettingsSection },
  setup() {
    const sectionPrefix = `ai-config-${++aiConfigSectionSequence}`
    const sectionIds = {
      connection: `${sectionPrefix}-connection`,
      advanced: `${sectionPrefix}-advanced`,
      prompt: `${sectionPrefix}-prompt`,
      pricing: `${sectionPrefix}-pricing`,
    }
    const advancedLimits = Object.freeze({
      minModelCalls: MIN_AI_SEARCH_MODEL_CALLS,
      maxBudget: MAX_AI_SEARCH_BUDGET,
      minSearches: MIN_AI_SEARCHES,
      minRetries: MIN_AI_SEARCH_RETRIES,
      maxRetries: MAX_AI_SEARCH_RETRIES,
      minWebReads: MIN_AI_WEB_READS,
      maxWebReads: MAX_AI_WEB_READS,
    })
    const tabLoading = ref(true)
    const saving = ref(false)
    const fetchingModels = ref(false)
    const modelFetchFailure = shallowRef(null)
    const modelFetchContext = ref(0)
    const searchEngineLoading = ref(false)
    const searchEngineInfo = ref(null)
    const capabilityModalVisible = ref(false)
    const capabilityError = ref('')
    const usageInfo = ref(null)
    const testModalVisible = ref(false)
    const ocrCacheLoading = ref(false)
    const ocrCacheStatus = ref(null)
    const ocrCacheError = ref('')
    const ocrCacheFailure = shallowRef(null), ocrCacheFailureContext = ref(0)
    let ocrReadVersion = 0, ocrFailureOwner = () => false
    const clearOCRFailure = () => { ocrCacheFailure.value = null; ocrCacheFailureContext.value += 1; ocrFailureOwner = () => false }
    const savedCompressionOrder = ref(buildAIContextCompressionSettings().contextCompressionOrder)
    const fixedTemperatureText = '0'
    const fixedTopPText = '0'
    const formState = reactive({
      ...buildAIContextCompressionSettings(),
      enabled: false,
      baseUrl: '',
      apiKey: '',
      model: '',
      backupModel: '',
      manualModelsText: '',
      prompt: '',
      remark: '',
      temperature: 0,
      topP: 0,
      maxAISearchModelCalls: DEFAULT_MAX_AI_SEARCH_MODEL_CALLS,
      maxAISearches: DEFAULT_MAX_AI_SEARCHES,
      maxAISearchRetries: DEFAULT_MAX_AI_SEARCH_RETRIES,
      searchPricePerCall: 0,
      tikuSearchPricePerCall: 0,
      maxAIWebReads: DEFAULT_MAX_AI_WEB_READS,
      webReadPricePerCall: 0,
      imageOCRPricePerCall: 0,
      enableAIStagedTokenBilling: false,
      smoothAIStagedTokenMultiplier: false,
      stagedTokenMultiplierPoints: DEFAULT_AI_STAGED_TOKEN_MULTIPLIER_POINTS.map(point => ({ ...point, tokens: String(point.tokens) })),
    })

    const initialConfigSnapshot = JSON.stringify(formState)
    const { currentConfigSnapshot, savedConfigSnapshot, hasUnsavedChanges, requestRouteLeave, cancelPendingLeave } = useUnsavedDraft(formState, { focusTriggerAfterClose: false })
    const pageNeedsConfigRead = ref(false)
    const configReadFailure = shallowRef(null)
    const configReadContext = ref(0)
    let retireAIPageDisplay = () => {}
    let refreshAIEntry = () => {}

    const priceRows = ref([])
    const priceLoading = ref(false)
    // Only the price GET lane owns these states; config and modal save leases stay independent.
    const { currentUser } = useCurrentUserDisplay()
    const priceReadFailure = shallowRef(null)
    const priceReadConfirmed = ref(false)
    const priceReadContext = ref(0)
    let priceReadAlive = true
    let priceReadEpoch = 0
    let priceReadRetired = false
    const pricePrincipal = user => JSON.stringify([currentUserIdentity(user), String(user?.role || '').trim().toLowerCase(), String(user?.status || '')])
    let priceReadPrincipal = pricePrincipal(currentUser.value)
    let priceReadLogoutEpoch = getCurrentUserLogoutEpoch()
    const retirePriceRead = () => {
      priceReadEpoch += 1
      priceReadContext.value += 1
      priceReadFailure.value = null
      priceReadConfirmed.value = false
      priceLoading.value = false
      priceRows.value = []
    }
    watch(currentUser, user => {
      const next = pricePrincipal(user), logout = getCurrentUserLogoutEpoch()
      if (next === priceReadPrincipal && logout === priceReadLogoutEpoch) return
      priceReadPrincipal = next
      priceReadLogoutEpoch = logout
      priceReadRetired = !currentUserIdentity(user) || !isAdminUser(user) || user?.status === 'disabled'
      retirePriceRead()
      retireAIPageDisplay()
      refreshAIEntry()
    }, { flush: 'sync' })
    // Unknown initial cache does not replace server authorization or suppress the original first GET.
    const canReadPrices = () => priceReadAlive && !priceReadRetired
    const syncPriceReadPrincipal = () => {
      getStoredCurrentUser()
      return pricePrincipal(currentUser.value)
    }
    const capturePriceReadOwner = () => {
      const epoch = priceReadEpoch, principal = priceReadPrincipal, logout = priceReadLogoutEpoch
      return () => {
        // Inspect actual storage as well as the reactive display before a late callback publishes.
        const current = syncPriceReadPrincipal()
        return canReadPrices() && epoch === priceReadEpoch && principal === current && logout === getCurrentUserLogoutEpoch()
      }
    }
    // Render entries capture the producer, never the principal present when an old callback fires.
    const canUseAIPage = () => {
      syncPriceReadPrincipal()
      const user = currentUser.value
      return canReadPrices() && Boolean(currentUserIdentity(user)) && isAdminUser(user) && user?.status !== 'disabled'
    }
    const canUseConfigActions = () => canUseAIPage() && !pageNeedsConfigRead.value
    const captureAIPageOwner = () => capturePriceReadOwner()
    const makeAIEntry = () => {
      const owns = captureAIPageOwner()
      const isCurrent = () => owns() && canUseAIPage()
      return {
        epoch: priceReadEpoch, isCurrent,
        bind: (fn, ...args) => (...events) => isCurrent() ? fn(...args, ...events) : undefined,
        guardNative: event => {
          if (isCurrent()) return
          event.preventDefault()
          event.stopImmediatePropagation()
        },
      }
    }
    const aiEntry = shallowRef(makeAIEntry())
    refreshAIEntry = () => { aiEntry.value = makeAIEntry() }
    const ownedConfirmations = new Set()
    const openAIConfirmation = (options, owns = captureAIPageOwner()) => {
      if (!owns() || !canUseAIPage()) return
      let modal
      const guarded = callback => (...args) => {
        if (!owns()) return
        callback?.(...args)
      }
      modal = Modal.confirm({
        ...options, focusTriggerAfterClose: false,
        onOk: guarded(options.onOk), onCancel: guarded(options.onCancel),
        afterClose: () => { ownedConfirmations.delete(modal); options.afterClose?.() },
      })
      ownedConfirmations.add(modal)
      return modal
    }
    const capabilitySession = ref(0)
    let capabilityGeneration = 0
    watch(capabilityModalVisible, open => {
      capabilityGeneration += 1
      if (open) capabilitySession.value += 1
      searchEngineLoading.value = false
      if (!capabilityModalVisible.value) {
        searchEngineInfo.value = null
        usageInfo.value = null
        capabilityError.value = ''
      }
    }, { flush: 'sync' })
    const isCurrentConfigReadFailure = (failure, context) => canReadPrices()
      && failure === configReadFailure.value && context === configReadContext.value
    const requestOwnedRouteLeave = to => {
      const owns = captureAIPageOwner()
      const decision = requestRouteLeave(to)
      return decision && typeof decision.then === 'function'
        ? decision.then(allow => owns() ? allow : false) : decision
    }

    const isCurrentModelFetchFailure = (failure, context) => canUseConfigActions()
      && failure === modelFetchFailure.value && context === modelFetchContext.value
      && priceReadLogoutEpoch === getCurrentUserLogoutEpoch()
      && priceReadPrincipal === syncPriceReadPrincipal()
    const isCurrentPriceReadFailure = (failure, context) => canReadPrices()
      && failure === priceReadFailure.value && context === priceReadContext.value
      && priceReadLogoutEpoch === getCurrentUserLogoutEpoch()
      && priceReadPrincipal === syncPriceReadPrincipal()
    onBeforeUnmount(() => {
      priceReadAlive = false
      retirePriceRead()
      retireAIPageDisplay()
      refreshAIEntry()
    })
    const priceSaving = ref(false)
    const priceResetting = ref(false)
    const priceModalVisible = ref(false)
    const priceDialogSession = ref(0)
    let priceSaveDisplayAlive = true
    let priceSaveDisplayGeneration = 0
    const invalidatePriceSaveDisplay = () => {
      priceSaveDisplayGeneration += 1
      priceSaving.value = false
    }
    watch(priceModalVisible, open => {
      invalidatePriceSaveDisplay()
      if (!open) priceDialogSession.value += 1
    }, { flush: 'sync' })
    let priceSaveDisplayOwner = () => false
    const capturePriceSaveDisplayContext = () => {
      priceSaveDisplayOwner = capturePriceReadOwner()
      return ++priceSaveDisplayGeneration
    }
    const canPublishPriceSaveDisplay = ticket => (
      priceSaveDisplayAlive && priceModalVisible.value && ticket === priceSaveDisplayGeneration && priceSaveDisplayOwner()
    )
    onBeforeUnmount(() => {
      priceSaveDisplayAlive = false
      invalidatePriceSaveDisplay()
    })
    const priceForm = reactive({
      id: null,
      modelName: '',
      displayName: '',
      contextWindowTokens: 0,
      inputPrice: 0,
      cachedPrice: 0,
      cacheWritePrice: 0,
      outputPrice: 0,
      multiplier: 1,
      remark: '',
    })

    const dialogEntry = session => computed(() => {
      const page = aiEntry.value, ticket = session.value
      const isCurrent = () => page.isCurrent() && ticket === session.value
      return { isCurrent, bind: (fn, ...args) => (...events) => isCurrent() ? fn(...args, ...events) : undefined }
    })
    const priceDialogEntry = dialogEntry(priceDialogSession)
    const capabilityEntry = dialogEntry(capabilitySession)

    const modelOptions = computed(() => {
      return buildModelNameOptions(formState.manualModelsText)
    })
    const stagedPointError = computed(() => getStagedMultiplierPointsError(formState.stagedTokenMultiplierPoints))
    const stagedPointRowKey = createStagedPointRowKey()

    const usageRows = computed(() => normalizeUsageRows(usageInfo.value))
    const searchUsageRows = computed(() => usageRows.value.filter(item => /\/search\/(?:aggregate|engines)(?:\/|$)/i.test(item.path) && item.count > 0))
    const webReadUsageRows = computed(() => usageRows.value.filter(item => /\/web\/tomarkdown(?:\/|$)/i.test(item.path) && item.count > 0))
    const ocrUsageRows = computed(() => usageRows.value.filter(item => /\/image\/ocr(?:\/|$)/i.test(item.path) && item.count > 0))
    const usedUsageRows = computed(() => [...searchUsageRows.value, ...webReadUsageRows.value, ...ocrUsageRows.value])
    const searchEngineDetail = computed(() => {
      const response = searchEngineInfo.value
      if (!response?.engine || typeof response.engine !== 'object') return null
      return {
        engine: response.engine,
        limits: response.limits || {},
        features: Array.isArray(response.engine.features) ? response.engine.features : [],
        supportedParameters: Array.isArray(response.supported_parameters) ? response.supported_parameters : [],
      }
    })
    const usedCapabilityCards = computed(() => {
      const cards = []
      if (searchUsageRows.value.length) {
        const count = searchUsageRows.value.reduce((sum, item) => sum + item.count, 0)
        const engineAvailable = searchEngineInfo.value?.engine?.available
        cards.push({
          key: 'search',
          title: '联网搜索 API',
          status: searchEngineInfo.value ? (engineAvailable ? '可用' : '不可用') : '服务商有记录',
          color: searchEngineInfo.value ? (engineAvailable ? 'green' : 'red') : 'blue',
          detail: searchEngineInfo.value?.engine?.display_name || searchEngineInfo.value?.engine?.name || '服务商返回的搜索端点累计调用统计',
          count,
        })
      }
      if (webReadUsageRows.value.length) {
        const count = webReadUsageRows.value.reduce((sum, item) => sum + item.count, 0)
        cards.push({
          key: 'web-read',
          title: '网页转 Markdown API',
          status: '服务商有记录',
          color: 'green',
          detail: '服务商返回的异步网页读取端点累计调用统计',
          count,
        })
      }
      if (ocrUsageRows.value.length) {
        const count = ocrUsageRows.value.reduce((sum, item) => sum + item.count, 0)
        cards.push({
          key: 'image-ocr',
          title: '图片 OCR API',
          status: '服务商有记录',
          color: 'green',
          detail: '服务商返回的图片 OCR 端点累计调用统计',
          count,
        })
      }
      return cards
    })
    const usageColumns = [
      { title: '调用位置', dataIndex: 'path', key: 'path', width: 540 },
      { title: '调用次数', dataIndex: 'count', key: 'count', width: 120 },
    ]

    // 哪些已配置的模型还没有价格：用于页面提示，避免命中后按 0 元计费却无人察觉。
    const pricingGaps = computed(() => buildAIModelPricingGaps(formState, priceRows.value))
    const pricingKnown = computed(() => priceReadConfirmed.value && !priceLoading.value && !priceReadFailure.value)

    const stagedPointColumns = [
      { title: '总 token 起点', dataIndex: 'tokens', key: 'tokens', width: 220 },
      { title: '倍率', dataIndex: 'multiplier', key: 'multiplier', width: 190 },
      { title: '操作', dataIndex: 'action', key: 'action', width: 90 },
    ]

    const loadConfig = (expectedSnapshot = currentConfigSnapshot.value) => {
      if (!canReadPrices()) return
      const owns = captureAIPageOwner()
      configReadContext.value += 1
      configReadFailure.value = null
      tabLoading.value = true
      return getAISearchConfig().then(res => {
        if (!owns()) return
        if (res?.status === 200 && res.data && typeof res.data === 'object' && !Array.isArray(res.data)) {
          const data = res?.data || {}
          const nextForm = { ...buildAIContextCompressionSettings(data) }
          nextForm.enabled = !!data.enabled
          nextForm.baseUrl = data.baseUrl || ''
          nextForm.apiKey = data.apiKey || ''
          nextForm.model = data.model || ''
          nextForm.backupModel = data.backupModel || ''
          nextForm.manualModelsText = buildModelNameText(data.manualModels || [])
          nextForm.prompt = data.prompt || ''
          nextForm.remark = data.remark || ''
          nextForm.temperature = data.temperature ?? 0
          nextForm.topP = data.topP ?? 0
          nextForm.maxAISearchModelCalls = normalizeAISearchBudget(data.maxAISearchModelCalls, DEFAULT_MAX_AI_SEARCH_MODEL_CALLS)
          nextForm.maxAISearches = normalizeAISearchBudget(data.maxAISearches, DEFAULT_MAX_AI_SEARCHES)
          nextForm.maxAISearchRetries = normalizeAISearchRetries(data.maxAISearchRetries, DEFAULT_MAX_AI_SEARCH_RETRIES)
          nextForm.searchPricePerCall = Number(data.searchPricePerCall ?? 0)
          nextForm.tikuSearchPricePerCall = Number(data.tiku_search_price_per_call ?? 0)
          nextForm.maxAIWebReads = normalizeAIWebReads(data.maxAIWebReads, DEFAULT_MAX_AI_WEB_READS)
          nextForm.webReadPricePerCall = Number(data.webReadPricePerCall ?? 0)
          nextForm.imageOCRPricePerCall = Number(data.imageOCRPricePerCall ?? 0)
          nextForm.enableAIStagedTokenBilling = !!data.enableAIStagedTokenBilling
          nextForm.smoothAIStagedTokenMultiplier = !!data.smoothAIStagedTokenMultiplier
          nextForm.stagedTokenMultiplierPoints = Array.isArray(data.stagedTokenMultiplierPoints) && data.stagedTokenMultiplierPoints.length
            ? data.stagedTokenMultiplierPoints.map(point => ({ tokens: String(point.tokens ?? 0), multiplier: Number(point.multiplier ?? 1) }))
            : DEFAULT_AI_STAGED_TOKEN_MULTIPLIER_POINTS.map(point => ({ ...point, tokens: String(point.tokens) }))
          savedCompressionOrder.value = [...nextForm.contextCompressionOrder]
          savedConfigSnapshot.value = JSON.stringify(nextForm)
          if (currentConfigSnapshot.value === expectedSnapshot) Object.assign(formState, nextForm)
          pageNeedsConfigRead.value = false
          configReadFailure.value = null
        } else {
          configReadFailure.value = ensureRequestFailure(res)
        }
      }).catch(error => {
        if (owns()) configReadFailure.value = ensureRequestFailure(error)
      }).finally(() => {
        if (owns()) tabLoading.value = false
      })
    }

    const loadAIModelPrices = () => {
      if (!canReadPrices()) return
      const owns = capturePriceReadOwner()
      priceReadContext.value += 1
      priceReadFailure.value = null
      priceReadConfirmed.value = false
      priceLoading.value = true
      return getAIModelPrices().then(res => {
        if (!owns()) return
        if (res?.status === 200 && Array.isArray(res.data)) {
          priceRows.value = res.data
          priceReadFailure.value = null
          priceReadConfirmed.value = true
        } else {
          priceReadFailure.value = ensureRequestFailure(res)
        }
      }).catch(error => {
        if (owns()) priceReadFailure.value = ensureRequestFailure(error)
      }).finally(() => {
        if (owns()) priceLoading.value = false
      })
    }
    const retryPriceRead = (failure, context) => {
      if (!priceLoading.value && isCurrentPriceReadFailure(failure, context)) return loadAIModelPrices()
    }

    const loadSearchEngineInfo = () => {
      if (!canUseConfigActions()) return
      const ownsPage = captureAIPageOwner()
      capabilityModalVisible.value = true
      const generation = ++capabilityGeneration
      const owns = () => ownsPage() && capabilityModalVisible.value && generation === capabilityGeneration
      searchEngineLoading.value = true
      capabilityError.value = ''
      searchEngineInfo.value = null
      usageInfo.value = null
      return Promise.allSettled([getUAPISearchEngines(), getUAPIUsageStatus()]).then(([engineResult, usageResult]) => {
        if (!owns()) return
        const errors = []
        if (engineResult.status === 'fulfilled' && engineResult.value?.status === 200) {
          searchEngineInfo.value = engineResult.value?.data || null
        } else {
          errors.push('搜索引擎状态获取失败')
        }
        if (usageResult.status === 'fulfilled' && usageResult.value?.status === 200) {
          usageInfo.value = usageResult.value?.data || null
        } else {
          errors.push('UAPI 端点使用统计获取失败')
        }
        capabilityError.value = errors.length ? `${errors.join('；')}，请检查 UApiPro 密钥或服务状态` : ''
      }).catch(() => {
        if (owns()) capabilityError.value = '深度思考能力获取失败，请检查 UApiPro 密钥或服务状态'
      }).finally(() => {
        if (owns()) searchEngineLoading.value = false
      })
    }

    // This factory captures authority/scalars, never the operation's response or form.
    const captureOCRFailureOwner = (owns, ticket, context) => () => owns()
      && ticket === ocrReadVersion
      && context === ocrCacheFailureContext.value
    const isCurrentOCRFailure = (failure, context) => ocrFailureOwner()
      && failure === ocrCacheFailure.value && context === ocrCacheFailureContext.value
    const loadAIOCRCacheStatus = () => {
      if (!canReadPrices()) return
      const owns = captureAIPageOwner(), ticket = ++ocrReadVersion
      clearOCRFailure(); ocrCacheError.value = ''; ocrCacheLoading.value = true
      const ownsFailure = captureOCRFailureOwner(owns, ticket, ocrCacheFailureContext.value)
      return getAIOCRCacheStatus({ errorFeedback: 'local' }).then(res => {
        if (!owns() || ticket !== ocrReadVersion) return
        if (res?.status !== 200 || !res.data || typeof res.data !== 'object') {
          ocrCacheError.value = '读取图片 OCR 缓存状态失败，请重试。'
          ocrFailureOwner = ownsFailure; ocrCacheFailure.value = projectOCRFailure(res); return
        }
        ocrCacheStatus.value = res.data
      }).catch(error => {
        if (!ownsFailure()) return
        ocrCacheError.value = '读取图片 OCR 缓存状态失败，请重试。'
        ocrFailureOwner = ownsFailure; ocrCacheFailure.value = projectOCRFailure(error)
      }).finally(() => { if (owns() && ticket === ocrReadVersion) ocrCacheLoading.value = false })
    }

    onMounted(() => {
      loadConfig()
      loadAIModelPrices()
      loadAIOCRCacheStatus()
    })

    const resetPriceForm = () => {
      priceForm.id = null
      priceForm.modelName = ''
      priceForm.displayName = ''
      priceForm.contextWindowTokens = 0
      priceForm.inputPrice = 0
      priceForm.cachedPrice = 0
      priceForm.cacheWritePrice = 0
      priceForm.outputPrice = 0
      priceForm.multiplier = 1
      priceForm.remark = ''
    }

    // A controlled modal emits update:open before cancel; reset as part of that
    // same owned close, before the session invalidates the following cancel event.
    const setPriceModalOpen = open => {
      priceModalVisible.value = open
      if (!open) resetPriceForm()
    }
    const retryConfigRead = (failure, context) => {
      if (!tabLoading.value && isCurrentConfigReadFailure(failure, context)) return loadConfig()
    }
    retireAIPageDisplay = () => {
      ocrReadVersion += 1; clearOCRFailure()
      ocrCacheLoading.value = false; ocrCacheStatus.value = null; ocrCacheError.value = ''
      cancelPendingLeave()
      const confirmations = [...ownedConfirmations]
      ownedConfirmations.clear()
      confirmations.forEach(modal => modal?.destroy())
      priceModalVisible.value = false
      invalidatePriceSaveDisplay()
      resetPriceForm()
      testModalVisible.value = false
      capabilityModalVisible.value = false
      capabilityGeneration += 1
      searchEngineLoading.value = false
      searchEngineInfo.value = null
      usageInfo.value = null
      capabilityError.value = ''
      fetchingModels.value = false
      modelFetchContext.value += 1
      modelFetchFailure.value = null
      saving.value = false
      priceResetting.value = false
      tabLoading.value = false
      configReadContext.value += 1
      configReadFailure.value = null
      Object.assign(formState, JSON.parse(initialConfigSnapshot))
      savedCompressionOrder.value = [...formState.contextCompressionOrder]
      savedConfigSnapshot.value = currentConfigSnapshot.value
      pageNeedsConfigRead.value = true
    }

    return {
      sectionIds,
      advancedLimits,
      currentConfigSnapshot, savedConfigSnapshot, hasUnsavedChanges, requestRouteLeave: requestOwnedRouteLeave,
      aiEntry, setPriceModalOpen, priceDialogEntry, priceDialogSession, capabilityEntry, capabilitySession, canUseAIPage, canUseConfigActions, captureAIPageOwner, openAIConfirmation,
      pageNeedsConfigRead, configReadFailure, configReadContext, isCurrentConfigReadFailure, retryConfigRead,
      labelCol: { style: { width: '140px' } },
      wrapperCol: { span: 14 },
      modalLabelCol: { style: { width: '110px' } },
      modalWrapperCol: { span: 16 },
      tabLoading,
      saving,
      fetchingModels,
      modelFetchFailure, modelFetchContext, isCurrentModelFetchFailure,
      searchEngineLoading,
      searchEngineInfo,
      searchEngineDetail,
      capabilityModalVisible,
      capabilityError,
      capabilityLoading: searchEngineLoading,
      usageInfo,
      usageRows,
      usedUsageRows,
      usedCapabilityCards,
      searchUsageRows,
      webReadUsageRows,
      usageColumns,
      testModalVisible,
      savedCompressionOrder,
      fixedTemperatureText,
      fixedTopPText,
      formState,
      MIN_AI_SEARCH_MODEL_CALLS,
      MIN_AI_SEARCHES,
      MIN_AI_SEARCH_RETRIES,
      MIN_AI_WEB_READS,
      parseIntegerInput,
      modelOptions,
      priceRows,
      priceLoading,
      priceReadFailure,
      priceReadConfirmed,
      priceReadContext,
      isCurrentPriceReadFailure,
      retryPriceRead,
      priceSaving,
      priceResetting,
      priceModalVisible,
      invalidatePriceSaveDisplay,
      capturePriceSaveDisplayContext,
      canPublishPriceSaveDisplay,
      priceForm,
      ocrCacheLoading,
      ocrCacheStatus,
      ocrCacheError, ocrCacheFailure, ocrCacheFailureContext, isCurrentOCRFailure,
      stagedPointColumns,
      stagedPointError,
      stagedPointRowKey,
      pricingGaps,
      pricingKnown,
      isModelPriced,
      loadConfig,
      loadAIModelPrices,
      loadSearchEngineInfo,
      loadAIOCRCacheStatus,
      resetPriceForm,
    }
  },
  beforeRouteLeave(to) { return this.requestRouteLeave(to) },
  methods: {
    sumTableColumnWidths,
      handleLoad() {
      if (!this.canUseAIPage()) return
      const owns = this.captureAIPageOwner()
      const snapshot = this.currentConfigSnapshot
      const refresh = () => {
        if (!owns() || this.currentConfigSnapshot !== snapshot) return
        this.loadConfig()
        this.loadAIModelPrices()
        this.loadAIOCRCacheStatus()
      }
      if (!this.hasUnsavedChanges) { refresh(); return }
      this.openAIConfirmation({
        title: '有未保存的更改，确认刷新？', content: '刷新将放弃当前未保存的编辑，并重新加载配置和模型价格。',
        okText: '放弃并刷新', cancelText: '保留编辑', onOk: refresh,
      }, owns)
    },
    openOCRCachePage() { if (this.canUseAIPage()) return this.$router.push('/ocr-cache') },
    formatAIOCRCacheTime(value) {
      if (!value) return '暂无'
      const date = new Date(value)
      return Number.isNaN(date.getTime()) ? '暂无' : date.toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' })
    },
    selectModel(model) {
      if (!this.canUseAIPage()) return
      this.formState.model = model
    },
    handleFetchModels() {
      if (!this.canUseConfigActions() || this.fetchingModels) return
      const owns = this.captureAIPageOwner()
      const params = {}
      const currentBaseUrl = trimBaseUrl(this.formState.baseUrl)
      const currentApiKey = String(this.formState.apiKey || '').trim()
      const currentManualModelsText = this.formState.manualModelsText
      if (currentBaseUrl) params.url = currentBaseUrl
      if (currentApiKey) params.key = currentApiKey
      this.modelFetchContext += 1
      this.modelFetchFailure = null
      this.fetchingModels = true
      return getAISearchModels(params).then(res => {
        if (!owns()) return
        if (res?.status !== 200) {
          this.modelFetchFailure = ensureRequestFailure(res)
          message.error('获取 AI 模型列表失败')
          return
        }
        if (trimBaseUrl(this.formState.baseUrl) !== currentBaseUrl ||
          String(this.formState.apiKey || '').trim() !== currentApiKey ||
          this.formState.manualModelsText !== currentManualModelsText) {
          message.info('连接或模型列表已修改，已保留当前草稿，请重新获取')
          return
        }
        const remoteModels = Array.isArray(res?.data?.models) ? res.data.models : []
        const filteredRemoteModels = filterRemoteModelNames(remoteModels)
        this.formState.manualModelsText = buildModelNameText(filteredRemoteModels)
        message.success('已获取 ' + filteredRemoteModels.length + ' 个模型，保存后生效')
      }).catch(error => {
        if (!owns()) return
        this.modelFetchFailure = ensureRequestFailure(error)
        message.error('获取 AI 模型列表失败')
      }).finally(() => {
        if (owns()) this.fetchingModels = false
      })
    },
    submitSave(baseUrl, expectedOwner, expectedSnapshot) {
      if (!this.canUseConfigActions() || this.saving) return
      const owns = typeof expectedOwner === 'function' ? expectedOwner : this.captureAIPageOwner()
      if (!owns() || (expectedSnapshot !== undefined && this.currentConfigSnapshot !== expectedSnapshot)) return
      const stagedPointError = getStagedMultiplierPointsError(this.formState.stagedTokenMultiplierPoints)
      if (stagedPointError) {
        message.error(stagedPointError)
        return
      }
      const maxAISearchModelCalls = Number(this.formState.maxAISearchModelCalls)
      const maxAISearches = Number(this.formState.maxAISearches)
      const maxAISearchRetries = Number(this.formState.maxAISearchRetries)
      const searchPricePerCall = Number(this.formState.searchPricePerCall)
      const tikuSearchPricePerCall = Number(this.formState.tikuSearchPricePerCall)
      const maxAIWebReads = Number(this.formState.maxAIWebReads)
      const webReadPricePerCall = Number(this.formState.webReadPricePerCall)
      const imageOCRPricePerCall = Number(this.formState.imageOCRPricePerCall)
      if (!Number.isSafeInteger(maxAISearchModelCalls) || maxAISearchModelCalls < MIN_AI_SEARCH_MODEL_CALLS || maxAISearchModelCalls > MAX_AI_SEARCH_BUDGET) {
        message.error(`模型最大调用次数必须是 ${MIN_AI_SEARCH_MODEL_CALLS}～${MAX_AI_SEARCH_BUDGET} 的整数`)
        return
      }
      if (!Number.isSafeInteger(maxAISearches) || maxAISearches < MIN_AI_SEARCHES || maxAISearches > MAX_AI_SEARCH_BUDGET) {
        message.error(`最大搜索次数必须是 ${MIN_AI_SEARCHES}～${MAX_AI_SEARCH_BUDGET} 的整数`)
        return
      }
      if (!Number.isSafeInteger(maxAISearchRetries) || maxAISearchRetries < MIN_AI_SEARCH_RETRIES || maxAISearchRetries > MAX_AI_SEARCH_RETRIES) {
        message.error(`每轮重试次数必须是 ${MIN_AI_SEARCH_RETRIES}～${MAX_AI_SEARCH_RETRIES} 的整数`)
        return
      }
      if (!Number.isFinite(searchPricePerCall) || searchPricePerCall < 0) {
        message.error('联网搜索单价必须是大于或等于 0 的有效金额')
        return
      }
      if (!Number.isFinite(tikuSearchPricePerCall) || tikuSearchPricePerCall < 0) {
        message.error('本地题库检索单价必须是大于或等于 0 的有效金额')
        return
      }
      if (!Number.isSafeInteger(maxAIWebReads) || maxAIWebReads < MIN_AI_WEB_READS || maxAIWebReads > MAX_AI_WEB_READS) {
        message.error(`网页读取次数必须是 ${MIN_AI_WEB_READS}～${MAX_AI_WEB_READS} 的整数`)
        return
      }
      if (!Number.isFinite(webReadPricePerCall) || webReadPricePerCall < 0) {
        message.error('网页读取单价必须是大于或等于 0 的有效金额')
        return
      }
      if (!Number.isFinite(imageOCRPricePerCall) || imageOCRPricePerCall < 0) {
        message.error('图片 OCR 单价必须是大于或等于 0 的有效金额')
        return
      }
      let payload
      try {
        payload = { ...buildAISearchConfigSavePayload(this.formState, baseUrl), ...buildAIContextCompressionSettings(this.formState) }
      } catch (error) {
        message.error(error.message)
        return
      }
      const submittedSnapshot = this.currentConfigSnapshot
      this.saving = true
      return updateAISearchConfig(payload).then(res => {
        if (!owns()) return
        if (res?.status === 200) {
          message.success('保存成功')
          this.savedConfigSnapshot = null
          this.loadConfig(submittedSnapshot)
          return
        }
        message.error('保存失败')
      }).catch(() => {
        if (owns()) message.error('保存失败')
      }).finally(() => {
        if (owns()) this.saving = false
      })
    },
    handleSave() {
      if (!this.canUseConfigActions() || this.saving) return
      const owns = this.captureAIPageOwner()
      const snapshot = this.currentConfigSnapshot
      const rawBaseUrl = this.formState.baseUrl || ''
      const trimmedBaseUrl = trimBaseUrl(rawBaseUrl)
      const completedBaseUrl = appendV1(rawBaseUrl)
      const submit = baseUrl => {
        if (!owns()) return
        if (this.currentConfigSnapshot !== snapshot) {
          message.warning('配置草稿已变化，请重新确认后保存')
          return
        }
        this.submitSave(baseUrl, owns, snapshot)
      }
      if (trimmedBaseUrl && !trimmedBaseUrl.endsWith('/v1')) {
        this.openAIConfirmation({
          title: '地址未包含 /v1',
          content: '可选补全为：' + completedBaseUrl + '。你可以继续补全保存，也可以保持原样保存。',
          okText: '补全后保存', cancelText: '保持原样',
          onOk: () => submit(completedBaseUrl), onCancel: () => submit(trimmedBaseUrl),
        }, owns)
        return
      }
      submit(trimmedBaseUrl)
    },
    addStagedMultiplierPoint() {
      if (!this.canUseAIPage()) return
      try {
        this.formState.stagedTokenMultiplierPoints = insertStagedMultiplierPoint(this.formState.stagedTokenMultiplierPoints)
      } catch (error) {
        message.warning(error?.message || '无法新增阶段节点')
      }
    },
    removeStagedMultiplierPoint(index) {
      if (!this.canUseAIPage()) return
      if (index === 0) {
        message.warning('首个 0 token 节点不能删除')
        return
      }
      if (this.formState.stagedTokenMultiplierPoints.length <= 1) {
        message.warning('阶段表至少需要保留首个 0 token 节点')
        return
      }
      this.formState.stagedTokenMultiplierPoints.splice(index, 1)
    },
    resetStagedMultiplierPoints() {
      if (!this.canUseAIPage()) return
      this.formState.stagedTokenMultiplierPoints = DEFAULT_AI_STAGED_TOKEN_MULTIPLIER_POINTS.map(point => ({ ...point, tokens: String(point.tokens) }))
    },
    handleOpenTestDialog() {
      if (!this.canUseConfigActions()) return
      this.testModalVisible = true
    },
    updatePromptForm(nextValue) {
      if (!this.canUseAIPage()) return
      Object.assign(this.formState, nextValue)
    },
    updateAdvancedForm(nextValue) {
      if (!this.canUseAIPage()) return
      Object.assign(this.formState, nextValue)
    },
    updateConnectionForm(nextValue) {
      if (!this.canUseAIPage()) return
      Object.assign(this.formState, nextValue)
    },
    updatePriceForm(nextValue) {
      if (!this.canUseAIPage()) return
      Object.assign(this.priceForm, nextValue)
    },
    openPriceModal(record) {
      if (!this.canUseAIPage()) return
      this.priceDialogSession += 1
      this.invalidatePriceSaveDisplay()
      if (record) {
        this.priceForm.id = record.id
        this.priceForm.modelName = record.modelName || ''
        this.priceForm.displayName = record.displayName || ''
        this.priceForm.contextWindowTokens = record.contextWindowTokens
        this.priceForm.inputPrice = Number(record.inputPrice || 0)
        this.priceForm.cachedPrice = Number(record.cachedPrice || 0)
        this.priceForm.cacheWritePrice = Number(record.cacheWritePrice || 0)
        this.priceForm.outputPrice = Number(record.outputPrice || 0)
        this.priceForm.multiplier = Number(record.multiplier || 1)
        this.priceForm.remark = record.remark || ''
      } else {
        this.resetPriceForm()
      }
      this.priceModalVisible = true
    },
    // 从「默认模型未配置价格」提示直接跳到新增价格，并预填模型名。
    openPriceModalForConfiguredModel() {
      if (!this.canUseAIPage()) return
      this.priceDialogSession += 1
      this.invalidatePriceSaveDisplay()
      const modelName = String(this.pricingGaps?.configuredModel || this.formState.model || '').trim()
      this.openPriceModalForModel(modelName)
    },
    openPriceModalForModel(modelName) {
      const normalizedModelName = String(modelName || '').trim()
      if (!normalizedModelName) return
      this.resetPriceForm()
      this.priceForm.modelName = normalizedModelName
      this.priceForm.displayName = normalizedModelName
      this.priceModalVisible = true
    },
    closePriceModal() {
      if (!this.canUseAIPage()) return
      this.priceModalVisible = false
      this.resetPriceForm()
    },
    handleSaveModelPrice() {
      if (!this.canUseAIPage() || this.priceSaving) return
      const displayTicket = this.capturePriceSaveDisplayContext()
      if (!this.canPublishPriceSaveDisplay(displayTicket)) return
      let payload
      try {
        payload = buildAIModelPriceSavePayload(this.priceForm)
      } catch (error) {
        message.warning(error.message)
        return
      }
      this.priceSaving = true
      const action = payload.id ? updateAIModelPrice(payload) : createAIModelPrice(payload)
      action.then(res => {
        if (!this.canPublishPriceSaveDisplay(displayTicket)) return
        if (res?.status === 200) {
          message.success('保存成功')
          this.closePriceModal()
          this.loadAIModelPrices()
          return
        }
        message.error('保存失败')
      }).catch(() => {
        if (!this.canPublishPriceSaveDisplay(displayTicket)) return
        message.error('保存失败')
      }).finally(() => {
        if (!this.canPublishPriceSaveDisplay(displayTicket)) return
        this.priceSaving = false
      })
    },
    handleDeleteModelPrice(record) {
      if (!this.canUseAIPage()) return
      const owns = this.captureAIPageOwner()
      const payload = { id: record.id }
      return deleteAIModelPrice(payload).then(res => {
        if (!owns()) return
        if (res?.status === 200) {
          message.success('删除成功')
          this.loadAIModelPrices()
          return
        }
        message.error('删除失败')
      }).catch(() => {
        if (owns()) message.error('删除失败')
      })
    },
    handleResetModelPrices() {
      if (!this.canUseAIPage() || this.priceResetting) return
      const owns = this.captureAIPageOwner()
      this.priceResetting = true
      return resetAIModelPrices().then(res => {
        if (!owns()) return
        if (res?.status === 200) {
          message.success('已恢复内置默认价格表')
          this.loadAIModelPrices()
          return
        }
        message.error('恢复默认失败')
      }).catch(() => {
        if (owns()) message.error('恢复默认失败')
      }).finally(() => {
        if (owns()) this.priceResetting = false
      })
    }
  }
})
</script>

<style scoped>
.ai-section-nav {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 16px;
}

.ai-section-nav a {
  display: inline-flex;
  align-items: center;
  min-height: 44px;
  padding: 8px 14px;
  border: 1px solid var(--border);
  border-radius: 12px;
  color: var(--text);
  text-decoration: none;
}

.ai-section-nav a:hover {
  background: var(--surface-subtle);
}

.ai-section-nav a:focus-visible,
.ai-section:focus-visible,
.pricing-grid:focus-visible {
  outline: 2px solid var(--primary);
  outline-offset: 3px;
}

.ai-section,
.pricing-grid {
  scroll-margin-top: 24px;
}

.ai-config-form { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
.console-form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); column-gap: 32px; row-gap: 8px; }
.full-width { grid-column: 1 / -1; }
.switch-description { margin-left: 12px; color: var(--console-muted); }
.unit-label { margin-left: 8px; color: var(--console-muted); }
.hint { margin-top: 8px; color: var(--console-muted); font-size: 12px; line-height: 1.8; overflow-wrap: anywhere; }
.hint-warning, .multiplier-cell--custom { color: var(--console-warning); }
.multiplier-cell--custom { font-weight: 600; }
.model-editor { display: flex; gap: 12px; align-items: flex-start; }
.model-editor :deep(.ant-input-textarea) { min-width: 0; flex: 1; }
.fetch-btn { flex-shrink: 0; }
.model-tags { margin-top: 12px; }
.model-tags :deep(.ant-tag) { padding: 4px 8px; border-radius: 6px; }
.pricing-grid { display: grid; gap: 16px; min-width: 0; }
.ocr-cache-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 16px; margin-top: 16px; }
.ocr-cache-stat { display: flex; align-items: center; gap: 10px; min-width: 0; }
.ocr-cache-stat__label { color: var(--console-muted); white-space: nowrap; }
.ocr-cache-stat > :last-child { overflow-wrap: anywhere; }
.staged-points-editor { margin: 20px 0 24px; }
.staged-points-editor__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; flex-wrap: wrap; margin-bottom: 16px; }
.staged-points-editor__header > div { flex: 1 1 320px; min-width: 0; }
.staged-points-editor__header :deep(.ant-space) { flex-wrap: wrap; }
.sub-card-title { color: var(--console-text); font-size: 15px; font-weight: 600; }
.sub-card-subtitle { margin-top: 6px; color: var(--console-muted); font-size: 13px; line-height: 1.7; overflow-wrap: anywhere; }
@media (max-width: 768px) { .console-form-grid { grid-template-columns: minmax(0, 1fr); } .ocr-cache-grid { grid-template-columns: minmax(0, 1fr); } .model-editor { display: grid; } .fetch-btn { width: 100%; } .staged-billing-settings :deep(.ant-form-item) { display: block; } .staged-billing-settings :deep(.ant-form-item-label) { width: 100% !important; text-align: left; } .staged-billing-settings :deep(.ant-form-item-control) { max-width: 100%; } }
</style>
