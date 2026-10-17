<template>
  <a-card title="AI 搜索配置" style="width: 100%" :bordered="false" class="page-card">
    <template #extra>
      <a-space>
        <a-button @click="handleLoad" :loading="tabLoading">刷新</a-button>
        <a-button @click="handleOpenTestDialog">测试</a-button>
        <a-button @click="loadSearchEngineInfo" :loading="searchEngineLoading">查看深度思考能力</a-button>
        <a-button type="primary" @click="handleSave" :loading="saving">保存</a-button>
      </a-space>
    </template>

    <a-alert
      type="info"
      show-icon
      class="page-alert"
      message="AI 搜索只在 use=ai 的请求模式下生效。显式传入 url、key、model 时优先使用请求参数；只有带有效 Basic 认证且未显式提供时，才会回退到这里的系统默认配置。"
    />

    <a-alert
      v-if="pricingGaps.configuredModel && !pricingGaps.configuredModelPriced"
      type="warning"
      show-icon
      class="page-alert"
      message="当前默认模型还没有配置价格"
      :description="`模型「${pricingGaps.configuredModel}」不在模型价格表中，调用时会按 0 元计费（不扣费），账单中会记录为未定价。请在下方「模型价格表」中新增该模型，并填写输入 / 缓存输入 / 输出单价。`"
    >
      <template #action>
        <a-button size="small" type="primary" ghost @click="openPriceModalForConfiguredModel">
          去设置价格
        </a-button>
      </template>
    </a-alert>

    <a-alert
      v-else-if="pricingGaps.unresolvedModels.length"
      type="warning"
      show-icon
      class="page-alert"
      message="模型列表中有模型还没有配置价格"
      :description="`以下模型缺少价格，命中时会按 0 元计费：${pricingGaps.unresolvedModels.join('、')}。请在下方「模型价格表」中补充单价。`"
    />

    <a-form :label-col="labelCol" :wrapper-col="wrapperCol">
      <a-form-item label="是否启用">
        <a-switch v-model:checked="formState.enabled" />
      </a-form-item>

      <a-form-item label="服务地址">
        <a-input v-model:value="formState.baseUrl" placeholder="例如 https://api.openai.com 或 https://api.openai.com/v1" />
        <div class="hint">如果地址未包含 /v1，保存时会提示你是否补全；也可以保持原样保存。</div>
      </a-form-item>

      <a-form-item label="API Key">
        <a-input-password v-model:value="formState.apiKey" placeholder="请输入 API Key" />
      </a-form-item>

      <a-form-item label="默认模型">
        <a-input v-model:value="formState.model" placeholder="可手动输入模型名" />
        <div class="hint">也可以点击下方模型列表快速填入。</div>
        <div v-if="pricingGaps.configuredModel && !pricingGaps.configuredModelPriced" class="hint hint-warning">
          该模型尚未配置价格，调用会按 0 元计费。
          <a @click="openPriceModalForConfiguredModel">去设置价格</a>
        </div>
      </a-form-item>

      <a-form-item label="模型列表">
        <div class="model-editor">
          <a-textarea
            v-model:value="formState.manualModelsText"
            :auto-size="{ minRows: 4, maxRows: 8 }"
            placeholder="支持英文逗号、中文逗号或换行分隔，例如\ngpt-5.4,gpt-5.4-mini,gpt-5.3-codex"
          />
          <a-button class="fetch-btn" @click="handleFetchModels" :loading="fetchingModels">
            获取远程模型列表
          </a-button>
        </div>
        <div class="hint">保存时会以此处内容作为模型列表，并自动为缺失模型补充默认架构。</div>
        <div class="hint">标签上带「未定价」的模型需要先在下方「模型价格表」中补价，否则会按 0 元计费。</div>
        <a-space wrap class="model-tags">
          <a-tag
            v-for="item in modelOptions"
            :key="item.value"
            :color="isModelPriced(item.value, priceRows) ? 'blue' : 'orange'"
            style="cursor: pointer"
            @click="selectModel(item.value)"
          >
            {{ item.label }}<span v-if="!isModelPriced(item.value, priceRows)">（未定价）</span>
          </a-tag>
          <span v-if="modelOptions.length === 0" class="hint">暂无模型列表，请先手动填写或获取远程模型。</span>
        </a-space>
      </a-form-item>

      <a-divider orientation="left">AI 搜索预算</a-divider>
      <a-form-item label="模型最大调用次数">
        <a-input-number
          v-model:value="formState.maxAISearchModelCalls"
          :min="MIN_AI_SEARCH_MODEL_CALLS"
          :max="MAX_AI_SEARCH_BUDGET"
          :precision="0"
          :step="1"
          :parser="parseIntegerInput"
          style="width: 180px"
        />
        <span class="unit-label">次</span>
        <div class="hint">每次 AI 搜索最多执行的模型逻辑轮次，默认 4 轮，范围 1～20 轮。同一轮内的失败重试不额外占用轮次，切换备用地址会占用一轮。每一轮提示中都会告知模型剩余调用次数。</div>
      </a-form-item>

      <a-form-item label="最大搜索次数">
        <a-input-number
          v-model:value="formState.maxAISearches"
          :min="MIN_AI_SEARCHES"
          :max="MAX_AI_SEARCH_BUDGET"
          :precision="0"
          :step="1"
          :parser="parseIntegerInput"
          style="width: 180px"
        />
        <span class="unit-label">次</span>
        <div class="hint">每次 AI 搜索最多调用搜索 API 的次数，默认 3 次。每一轮模型调用前都会告知剩余搜索次数，达到最后一轮时必须直接输出答案。</div>
      </a-form-item>

      <a-form-item label="每轮重试次数">
        <a-input-number
          v-model:value="formState.maxAISearchRetries"
          :min="MIN_AI_SEARCH_RETRIES"
          :max="MAX_AI_SEARCH_RETRIES"
          :precision="0"
          :step="1"
          :parser="parseIntegerInput"
          style="width: 180px"
        />
        <span class="unit-label">次</span>
        <div class="hint">每轮模型调用最多尝试的机会（包含首次调用），默认 3 次，范围 1～10 次。单次模型调用失败时会在本轮内自动重试，耗尽后才结束本轮。</div>
      </a-form-item>

      <a-form-item label="联网搜索单价（元/次）">
        <a-input-number
          v-model:value="formState.searchPricePerCall"
          :min="0"
          :precision="6"
          :step="0.01"
          style="width: 180px"
        />
        <span class="unit-label">元 / 次</span>
        <div class="hint">默认 0 元。大于 0 时，每次实际调用联网搜索 API 都单独记一条账单，并合并显示在对应搜题账单详情中；命中本次 AI 搜索内的相同查询缓存不会重复计价。</div>
      </a-form-item>

      <a-divider orientation="left">网页读取</a-divider>

      <a-form-item label="网页读取次数">
        <a-input-number
          v-model:value="formState.maxAIWebReads"
          :min="MIN_AI_WEB_READS"
          :max="MAX_AI_WEB_READS"
          :precision="0"
          :step="1"
          :parser="parseIntegerInput"
          style="width: 180px"
        />
        <span class="unit-label">次</span>
        <div class="hint">深度思考模式下，模型每次 AI 搜索最多调用网页读取工具的次数，默认 5 次，范围 1～20 次。普通模式仍保持原先的一次性回复，不会调用网页读取。</div>
      </a-form-item>

      <a-form-item label="网页读取单价（元/次）">
        <a-input-number
          v-model:value="formState.webReadPricePerCall"
          :min="0"
          :precision="6"
          :step="0.01"
          style="width: 180px"
        />
        <span class="unit-label">元 / 次</span>
        <div class="hint">默认 0 元。大于 0 时，每次成功完成网页读取都会单独写入一条账单，并合并到对应的搜题账单详情中。</div>
      </a-form-item>

      <a-form-item label="Prompt 设置">
        <a-textarea
          v-model:value="formState.prompt"
          :auto-size="{ minRows: 10, maxRows: 18 }"
          placeholder="留空时将自动使用系统内置 Prompt"
        />
        <div class="hint-block">
          <div class="hint">AI 只应输出 JSON，格式固定为 {"answers":["答案1","答案2"]}，系统会自动解析并转换为当前题库接口兼容的答案结构。</div>
          <div class="hint">建议保留“只返回 JSON、信息不足返回不确定、优先返回与选项一致的答案文本”这一类约束，避免输出额外说明影响后续处理。</div>
          <div class="hint">如果你清空 Prompt 保存，后端会自动回退到内置默认 Prompt；下次加载时，页面会直接显示当前生效的默认内容。</div>
          <div class="hint">该输出还需要兼容现有题库聚合结构以及 OCSJS 的后续判断逻辑，所以不建议改成自然语言长段回答。</div>
        </div>
      </a-form-item>

      <a-form-item label="温度">
        <a-input :value="fixedTemperatureText" readonly disabled />
        <div class="hint">固定为 0，用于降低随机性，让相同题目的返回结果更稳定。</div>
      </a-form-item>

      <a-form-item label="Top P">
        <a-input :value="fixedTopPText" readonly disabled />
        <div class="hint">固定为 0，与温度一起保持低随机性，不开放自定义调整。</div>
      </a-form-item>
    </a-form>

    <a-modal
      v-model:visible="capabilityModalVisible"
      title="深度思考能力"
      :footer="null"
      width="960px"
    >
      <a-spin :spinning="capabilityLoading">
        <a-alert
          v-if="capabilityError"
          type="warning"
          show-icon
          class="page-alert"
          :message="capabilityError"
        />

        <div v-if="usedCapabilityCards.length" class="capability-status-grid">
          <div v-for="card in usedCapabilityCards" :key="card.key" class="capability-status-card">
            <div class="capability-status-card__header">
              <span class="capability-status-card__title">{{ card.title }}</span>
              <a-tag :color="card.color">{{ card.status }}</a-tag>
            </div>
            <div class="capability-status-card__meta">{{ card.detail }}</div>
            <div v-if="card.count !== null" class="capability-status-card__count">累计调用 {{ card.count }} 次</div>
          </div>
        </div>
        <a-empty v-else-if="!capabilityLoading" description="暂未使用搜索或网页读取接口" />

        <a-table
          v-if="usedUsageRows.length"
          class="capability-usage-table"
          size="small"
          bordered
          row-key="path"
          :pagination="false"
          :data-source="usedUsageRows"
          :columns="usageColumns"
        />
      </a-spin>
    </a-modal>

    <div class="pricing-grid">
      <a-card :bordered="false" class="sub-card">
        <template #title>
          <div class="sub-card-title-wrap">
            <div>
              <div class="sub-card-title">模型价格表</div>
              <div class="sub-card-subtitle">单价单位为 元 / 百万 token（人民币，无汇率换算）。实际扣费 =（输入 − 缓存输入）× 输入价 + 缓存输入 × 缓存价 + 输出 × 输出价。</div>
            </div>
          </div>
        </template>
        <template #extra>
          <a-space>
            <a-popconfirm title="确定恢复内置默认价格表吗？自定义价格会被清空。" ok-text="确定" cancel-text="取消" @confirm="handleResetModelPrices">
              <a-button :loading="priceResetting">恢复默认</a-button>
            </a-popconfirm>
            <a-button @click="loadAIModelPrices" :loading="priceLoading">刷新</a-button>
            <a-button type="primary" @click="openPriceModal()">新增模型价格</a-button>
          </a-space>
        </template>

        <a-form class="staged-billing-settings" :label-col="labelCol" :wrapper-col="wrapperCol">
          <a-form-item label="启用阶段计费">
            <a-switch v-model:checked="formState.enableAIStagedTokenBilling" />
            <div class="hint">按每次实际上游模型响应的总 token 数应用阶段倍率：3000 以下最高 4x，10 万以上最低 0.3x；关闭时保持现有价格表计费。</div>
          </a-form-item>
          <a-form-item label="平滑变化倍率">
            <a-switch
              v-model:checked="formState.smoothAIStagedTokenMultiplier"
              :disabled="!formState.enableAIStagedTokenBilling"
            />
            <div class="hint">开启后在固定阶段节点之间线性平滑变化，倍率始终限制在 0.3x～4x；阶段计费关闭时只保存此选项，不影响计费。</div>
          </a-form-item>
        </a-form>

        <div class="price-table-wrap">
          <a-table
            bordered
            row-key="id"
            :data-source="priceRows"
            :columns="priceColumns"
            :loading="priceLoading"
            :pagination="{ pageSize: 8, size: 'small' }"
            :scroll="{ x: 'max-content' }"
          >
            <template #inputPrice="{ record }">{{ formatPriceAmount(record.inputPrice) }}</template>
            <template #cachedPrice="{ record }">{{ formatPriceAmount(record.cachedPrice) }}</template>
            <template #cacheWritePrice="{ record }">{{ formatPriceAmount(record.cacheWritePrice) }}</template>
            <template #outputPrice="{ record }">{{ formatPriceAmount(record.outputPrice) }}</template>
            <template #multiplier="{ record }">
              <span :class="{ 'multiplier-cell--custom': Number(record.multiplier || 1) !== 1 }">
                ×{{ formatMultiplier(record.multiplier) }}
              </span>
            </template>
            <template #status="{ record }">
              <a-tag :color="record.status === 'enabled' ? 'green' : 'default'">
                {{ record.status === 'enabled' ? '启用' : '停用' }}
              </a-tag>
            </template>
            <template #priceRemark="{ record }">
              <span>{{ record.remark || '-' }}</span>
              <a-tag v-if="record.isUserModified" color="orange" style="margin-left: 6px">已改</a-tag>
            </template>
            <template #priceAction="{ record }">
              <a-space size="small">
                <a-button type="link" style="padding: 0" @click="openPriceModal(record)">编辑</a-button>
                <a-popconfirm title="确定删除该模型价格吗？" ok-text="确定" cancel-text="取消" @confirm="handleDeleteModelPrice(record)">
                  <a-button type="link" danger style="padding: 0">删除</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table>
        </div>
      </a-card>

    </div>

    <a-modal
      v-model:visible="priceModalVisible"
      :title="priceForm.id ? '编辑模型价格' : '新增模型价格'"
      ok-text="确定"
      cancel-text="取消"
      :confirm-loading="priceSaving"
      @ok="handleSaveModelPrice"
      @cancel="closePriceModal"
    >
      <a-form :label-col="modalLabelCol" :wrapper-col="modalWrapperCol">
        <a-form-item label="模型名称" required>
          <a-input v-model:value="priceForm.modelName" placeholder="需与 AI 搜索配置中的模型名一致，例如 GPT-5.6 Sol" />
        </a-form-item>
        <a-form-item label="显示名称">
          <a-input v-model:value="priceForm.displayName" placeholder="留空时与模型名称相同" />
        </a-form-item>
        <a-form-item label="输入单价">
          <a-input-number v-model:value="priceForm.inputPrice" :min="0" :precision="6" :step="0.01" style="width: 100%" />
          <div class="hint">元 / 百万 token，未命中缓存的输入部分按此计价。</div>
        </a-form-item>
        <a-form-item label="缓存输入单价">
          <a-input-number v-model:value="priceForm.cachedPrice" :min="0" :precision="6" :step="0.01" style="width: 100%" />
          <div class="hint">元 / 百万 token，上游返回的 cached_tokens 按此计价。</div>
        </a-form-item>
        <a-form-item label="缓存写入单价">
          <a-input-number v-model:value="priceForm.cacheWritePrice" :min="0" :precision="6" :step="0.01" style="width: 100%" />
          <div class="hint">元 / 百万 token，缓存写入（cache writes）部分按此计价；上游不提供该价格时填 0。</div>
        </a-form-item>
        <a-form-item label="输出单价">
          <a-input-number v-model:value="priceForm.outputPrice" :min="0" :precision="6" :step="0.01" style="width: 100%" />
          <div class="hint">元 / 百万 token。</div>
        </a-form-item>
        <a-form-item label="倍率">
          <a-input-number v-model:value="priceForm.multiplier" :min="0.0001" :precision="4" :step="0.1" style="width: 100%" />
          <div class="hint">最终扣费 =（输入 + 缓存输入 + 缓存写入 + 输出 各项金额）× 倍率；默认 1，不改单价即可整体加价或打折。</div>
        </a-form-item>
        <a-form-item label="备注">
          <a-input v-model:value="priceForm.remark" placeholder="备注" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:visible="testModalVisible"
      title="测试 AI 搜索配置"
      ok-text="发送 hello 测试"
      cancel-text="关闭"
      :confirm-loading="testSubmitting"
      width="920px"
      @ok="handleRunTest"
    >
      <a-form :label-col="modalLabelCol" :wrapper-col="modalWrapperCol">
        <a-form-item label="测试模型" required>
          <a-select
            v-model:value="testForm.model"
            show-search
            allow-clear
            :options="modelOptions"
            placeholder="请选择模型或先在上方手动填写模型名"
          />
          <div class="hint">会向当前配置对应的 AI 服务发送固定测试问题：hello。</div>
        </a-form-item>
        <a-form-item label="服务地址">
          <a-input v-model:value="testForm.baseUrl" placeholder="留空时使用当前页面中的服务地址" />
        </a-form-item>
        <a-form-item label="API Key">
          <a-input-password v-model:value="testForm.apiKey" placeholder="留空时使用当前页面中的 API Key" />
        </a-form-item>
        <a-form-item label="Prompt 覆盖">
          <a-textarea
            v-model:value="testForm.prompt"
            :auto-size="{ minRows: 4, maxRows: 8 }"
            placeholder="留空时使用当前页面中的 Prompt"
          />
        </a-form-item>
      </a-form>

      <a-alert
        type="info"
        show-icon
        class="test-alert"
        message="测试请求固定发送 hello，弹窗会展示完整请求体、原始流式响应以及最终解析出的答案。"
      />

      <div v-if="hasAISearchTestResult(testResult)" class="test-result-panel">
        <div class="test-result-block">
          <div class="test-result-title">解析答案</div>
          <a-space wrap>
            <a-tag v-for="item in testResult.answers" :key="item" color="blue">{{ item }}</a-tag>
            <span v-if="!testResult.answers.length" class="hint">暂无解析答案</span>
          </a-space>
        </div>
        <div class="test-result-block">
          <div class="test-result-title">请求内容</div>
          <pre class="test-result-pre">{{ testResult.requestBody || '-' }}</pre>
        </div>
        <div class="test-result-block">
          <div class="test-result-title">完整回复</div>
          <pre class="test-result-pre">{{ testResult.rawResponse || '-' }}</pre>
        </div>
      </div>
    </a-modal>
  </a-card>
</template>

<script>
import { defineComponent, computed, onMounted, reactive, ref } from 'vue';
import { message, Modal } from 'ant-design-vue';
import {
  getAISearchConfig,
  getAISearchModels,
  testAISearchConfig,
  updateAISearchConfig,
  getAIModelPrices,
  getUAPISearchEngines,
  getUAPIUsageStatus,
  createAIModelPrice,
  updateAIModelPrice,
  deleteAIModelPrice,
  resetAIModelPrices,
} from '@/api/TikuComponentApi';
import {
  buildModelNameOptions,
  buildModelNameText,
  buildAISearchConfigSavePayload,
  buildAIModelPricingGaps,
  isModelPriced,
  filterRemoteModelNames,
} from '@/utils/aiSearchModelHelpers.mjs';
import {
  buildAISearchTestForm,
  buildAISearchTestPayload,
  buildAISearchTestResult,
  hasAISearchTestResult,
} from '@/utils/aiSearchConfigHelpers.mjs';
import { formatMoneyCny, formatMoneyValue } from '@/utils/moneyFormat.mjs';

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
const MIN_AI_SEARCHES = 1
const MAX_AI_SEARCH_BUDGET = 20
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

export default defineComponent({
  name: 'AISearchConfig',
  setup() {
    const tabLoading = ref(true)
    const saving = ref(false)
    const fetchingModels = ref(false)
    const searchEngineLoading = ref(false)
    const searchEngineInfo = ref(null)
    const capabilityModalVisible = ref(false)
    const capabilityError = ref('')
    const usageInfo = ref(null)
    const testModalVisible = ref(false)
    const testSubmitting = ref(false)
    const testForm = reactive({
      baseUrl: '',
      apiKey: '',
      model: '',
      prompt: '',
    })
    const testResult = reactive({
      answers: [],
      requestBody: '',
      rawResponse: '',
    })
    const fixedTemperatureText = '0'
    const fixedTopPText = '0'
    const formState = reactive({
      enabled: false,
      baseUrl: '',
      apiKey: '',
      model: '',
      manualModelsText: '',
      prompt: '',
      remark: '',
      temperature: 0,
      topP: 0,
      maxAISearchModelCalls: DEFAULT_MAX_AI_SEARCH_MODEL_CALLS,
      maxAISearches: DEFAULT_MAX_AI_SEARCHES,
      maxAISearchRetries: DEFAULT_MAX_AI_SEARCH_RETRIES,
      searchPricePerCall: 0,
      maxAIWebReads: DEFAULT_MAX_AI_WEB_READS,
      webReadPricePerCall: 0,
      enableAIStagedTokenBilling: false,
      smoothAIStagedTokenMultiplier: false,
    })

    const priceRows = ref([])
    const priceLoading = ref(false)
    const priceSaving = ref(false)
    const priceResetting = ref(false)
    const priceModalVisible = ref(false)
    const priceForm = reactive({
      id: null,
      modelName: '',
      displayName: '',
      inputPrice: 0,
      cachedPrice: 0,
      cacheWritePrice: 0,
      outputPrice: 0,
      multiplier: 1,
      remark: '',
    })

    const modelOptions = computed(() => {
      return buildModelNameOptions(formState.manualModelsText)
    })

    const usageRows = computed(() => normalizeUsageRows(usageInfo.value))
    const searchUsageRows = computed(() => usageRows.value.filter(item => /\/search\/(?:aggregate|engines)(?:\/|$)/i.test(item.path) && item.count > 0))
    const webReadUsageRows = computed(() => usageRows.value.filter(item => /\/web\/tomarkdown(?:\/|$)/i.test(item.path) && item.count > 0))
    const usedUsageRows = computed(() => [...searchUsageRows.value, ...webReadUsageRows.value])
    const usedCapabilityCards = computed(() => {
      const cards = []
      if (searchUsageRows.value.length) {
        const count = searchUsageRows.value.reduce((sum, item) => sum + item.count, 0)
        const engineAvailable = searchEngineInfo.value?.engine?.available
        cards.push({
          key: 'search',
          title: '联网搜索 API',
          status: searchEngineInfo.value ? (engineAvailable ? '可用' : '不可用') : '已调用',
          color: searchEngineInfo.value ? (engineAvailable ? 'green' : 'red') : 'blue',
          detail: searchEngineInfo.value?.engine?.display_name || searchEngineInfo.value?.engine?.name || '搜索端点已有实际调用记录',
          count,
        })
      }
      if (webReadUsageRows.value.length) {
        const count = webReadUsageRows.value.reduce((sum, item) => sum + item.count, 0)
        cards.push({
          key: 'web-read',
          title: '网页转 Markdown API',
          status: '已调用',
          color: 'green',
          detail: '异步网页读取端点已有实际调用记录',
          count,
        })
      }
      return cards
    })
    const usageColumns = [
      { title: 'UAPI 端点', dataIndex: 'path', key: 'path', ellipsis: true },
      { title: '调用次数', dataIndex: 'count', key: 'count', width: 120, align: 'right' },
    ]

    // 哪些已配置的模型还没有价格：用于页面提示，避免命中后按 0 元计费却无人察觉。
    const pricingGaps = computed(() => buildAIModelPricingGaps(formState, priceRows.value))

    const priceColumns = [
      { title: 'ID', dataIndex: 'id', key: 'id', width: 60 },
      { title: '模型 ID', dataIndex: 'modelName', key: 'modelName', width: 190, ellipsis: true },
      { title: '显示名称', dataIndex: 'displayName', key: 'displayName', width: 165, ellipsis: true },
      { title: '输入（￥/M）', dataIndex: 'inputPrice', key: 'inputPrice', width: 105, align: 'right', slots: { customRender: 'inputPrice' } },
      { title: '缓存输入（￥/M）', dataIndex: 'cachedPrice', key: 'cachedPrice', width: 120, align: 'right', slots: { customRender: 'cachedPrice' } },
      { title: '缓存写入（￥/M）', dataIndex: 'cacheWritePrice', key: 'cacheWritePrice', width: 125, align: 'right', slots: { customRender: 'cacheWritePrice' } },
      { title: '输出（￥/M）', dataIndex: 'outputPrice', key: 'outputPrice', width: 105, align: 'right', slots: { customRender: 'outputPrice' } },
      { title: '倍率', dataIndex: 'multiplier', key: 'multiplier', width: 85, align: 'right', slots: { customRender: 'multiplier' } },
      { title: '备注', dataIndex: 'remark', key: 'remark', width: 140, ellipsis: true, slots: { customRender: 'priceRemark' } },
      { title: '操作', dataIndex: 'action', key: 'action', fixed: 'right', width: 110, slots: { customRender: 'priceAction' } },
    ]

    const loadConfig = () => {
      tabLoading.value = true
      getAISearchConfig().then(res => {
        if (res?.status === 200) {
          const data = res?.data || {}
          formState.enabled = !!data.enabled
          formState.baseUrl = data.baseUrl || ''
          formState.apiKey = data.apiKey || ''
          formState.model = data.model || ''
          formState.manualModelsText = buildModelNameText(data.manualModels || [])
          formState.prompt = data.prompt || ''
          formState.remark = data.remark || ''
          formState.temperature = data.temperature ?? 0
          formState.topP = data.topP ?? 0
          formState.maxAISearchModelCalls = normalizeAISearchBudget(data.maxAISearchModelCalls, DEFAULT_MAX_AI_SEARCH_MODEL_CALLS)
          formState.maxAISearches = normalizeAISearchBudget(data.maxAISearches, DEFAULT_MAX_AI_SEARCHES)
          formState.maxAISearchRetries = normalizeAISearchRetries(data.maxAISearchRetries, DEFAULT_MAX_AI_SEARCH_RETRIES)
          formState.searchPricePerCall = Number(data.searchPricePerCall ?? 0)
          formState.maxAIWebReads = normalizeAIWebReads(data.maxAIWebReads, DEFAULT_MAX_AI_WEB_READS)
          formState.webReadPricePerCall = Number(data.webReadPricePerCall ?? 0)
          formState.enableAIStagedTokenBilling = !!data.enableAIStagedTokenBilling
          formState.smoothAIStagedTokenMultiplier = !!data.smoothAIStagedTokenMultiplier
        } else {
          message.error(res?.message || '获取 AI 搜索配置失败')
        }
      }).catch(() => {
        message.error('获取 AI 搜索配置失败')
      }).finally(() => {
        tabLoading.value = false
      })
    }

    const loadAIModelPrices = () => {
      priceLoading.value = true
      return getAIModelPrices().then(res => {
        if (res?.status === 200) {
          priceRows.value = Array.isArray(res?.data) ? res.data : []
        } else {
          message.error(res?.message || '获取模型价格表失败')
        }
      }).catch(() => {
        message.error('获取模型价格表失败')
      }).finally(() => {
        priceLoading.value = false
      })
    }

    const loadSearchEngineInfo = () => {
      capabilityModalVisible.value = true
      searchEngineLoading.value = true
      capabilityError.value = ''
      searchEngineInfo.value = null
      usageInfo.value = null
      return Promise.allSettled([getUAPISearchEngines(), getUAPIUsageStatus()]).then(([engineResult, usageResult]) => {
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
        capabilityError.value = '深度思考能力获取失败，请检查 UApiPro 密钥或服务状态'
      }).finally(() => {
        searchEngineLoading.value = false
      })
    }

    onMounted(() => {
      loadConfig()
      loadAIModelPrices()
    })

    const resetPriceForm = () => {
      priceForm.id = null
      priceForm.modelName = ''
      priceForm.displayName = ''
      priceForm.inputPrice = 0
      priceForm.cachedPrice = 0
      priceForm.cacheWritePrice = 0
      priceForm.outputPrice = 0
      priceForm.multiplier = 1
      priceForm.remark = ''
    }

    return {
      labelCol: { style: { width: '140px' } },
      wrapperCol: { span: 14 },
      modalLabelCol: { style: { width: '110px' } },
      modalWrapperCol: { span: 16 },
      tabLoading,
      saving,
      fetchingModels,
      searchEngineLoading,
      searchEngineInfo,
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
      testSubmitting,
      testForm,
      testResult,
      fixedTemperatureText,
      fixedTopPText,
      formState,
      MIN_AI_SEARCH_MODEL_CALLS,
      MIN_AI_SEARCHES,
      MAX_AI_SEARCH_BUDGET,
      MIN_AI_SEARCH_RETRIES,
      MAX_AI_SEARCH_RETRIES,
      MIN_AI_WEB_READS,
      MAX_AI_WEB_READS,
      parseIntegerInput,
      modelOptions,
      priceRows,
      priceColumns,
      priceLoading,
      priceSaving,
      priceResetting,
      priceModalVisible,
      priceForm,
      hasAISearchTestResult,
      pricingGaps,
      isModelPriced,
      loadConfig,
      loadAIModelPrices,
      loadSearchEngineInfo,
      resetPriceForm,
    }
  },
  methods: {
    handleLoad() {
      this.loadConfig()
      this.loadAIModelPrices()
    },
    selectModel(model) {
      this.formState.model = model
    },
    handleFetchModels() {
      const params = {}
      const currentBaseUrl = trimBaseUrl(this.formState.baseUrl)
      const currentApiKey = String(this.formState.apiKey || '').trim()
      if (currentBaseUrl) {
        params.url = currentBaseUrl
      }
      if (currentApiKey) {
        params.key = currentApiKey
      }

      this.fetchingModels = true
      getAISearchModels(params).then(res => {
        if (res?.status !== 200) {
          throw new Error(res?.message || '获取 AI 模型列表失败')
        }

        const remoteModels = Array.isArray(res?.data?.models) ? res.data.models : []
        const filteredRemoteModels = filterRemoteModelNames(remoteModels)
        this.formState.manualModelsText = buildModelNameText(filteredRemoteModels)
        message.success(`已获取 ${filteredRemoteModels.length} 个模型，保存后生效`)
      }).catch(error => {
        message.error(error?.message || '获取 AI 模型列表失败')
      }).finally(() => {
        this.fetchingModels = false
      })
    },
    submitSave(baseUrl) {
      const maxAISearchModelCalls = Number(this.formState.maxAISearchModelCalls)
      const maxAISearches = Number(this.formState.maxAISearches)
      const maxAISearchRetries = Number(this.formState.maxAISearchRetries)
      const searchPricePerCall = Number(this.formState.searchPricePerCall)
      const maxAIWebReads = Number(this.formState.maxAIWebReads)
      const webReadPricePerCall = Number(this.formState.webReadPricePerCall)
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
      if (!Number.isSafeInteger(maxAIWebReads) || maxAIWebReads < MIN_AI_WEB_READS || maxAIWebReads > MAX_AI_WEB_READS) {
        message.error(`网页读取次数必须是 ${MIN_AI_WEB_READS}～${MAX_AI_WEB_READS} 的整数`)
        return
      }
      if (!Number.isFinite(webReadPricePerCall) || webReadPricePerCall < 0) {
        message.error('网页读取单价必须是大于或等于 0 的有效金额')
        return
      }
      this.saving = true
      const payload = buildAISearchConfigSavePayload(this.formState, baseUrl)
      updateAISearchConfig(payload).then(res => {
        if (res?.status === 200) {
          message.success('保存成功')
          this.loadConfig()
          return
        }
        throw new Error(res?.message || '保存失败')
      }).catch(error => {
        message.error(error?.message || '保存失败')
      }).finally(() => {
        this.saving = false
      })
    },
    handleSave() {
      const rawBaseUrl = this.formState.baseUrl || ''
      const trimmedBaseUrl = trimBaseUrl(rawBaseUrl)
      const completedBaseUrl = appendV1(rawBaseUrl)

      if (trimmedBaseUrl && !trimmedBaseUrl.endsWith('/v1')) {
        Modal.confirm({
          title: '地址未包含 /v1',
          content: `可选补全为：${completedBaseUrl}。你可以继续补全保存，也可以保持原样保存。`,
          okText: '补全后保存',
          cancelText: '保持原样',
          onOk: () => this.submitSave(completedBaseUrl),
          onCancel: () => this.submitSave(trimmedBaseUrl),
        })
        return
      }

      this.submitSave(trimmedBaseUrl)
    },
    resetTestResult() {
      this.testResult.answers = []
      this.testResult.requestBody = ''
      this.testResult.rawResponse = ''
    },
    handleOpenTestDialog() {
      const nextForm = buildAISearchTestForm(this.formState)
      this.testForm.baseUrl = nextForm.baseUrl
      this.testForm.apiKey = nextForm.apiKey
      this.testForm.model = nextForm.model
      this.testForm.prompt = nextForm.prompt
      this.resetTestResult()
      this.testModalVisible = true
    },
    handleRunTest() {
      let payload
      try {
        payload = buildAISearchTestPayload(this.testForm, this.formState)
      } catch (error) {
        message.warning(error?.message || '请选择要测试的 AI 模型')
        return
      }

      this.testSubmitting = true
      testAISearchConfig(payload).then(res => {
        if (res?.status === 200) {
          const result = buildAISearchTestResult(res)
          this.testResult.answers = result.answers
          this.testResult.requestBody = result.requestBody
          this.testResult.rawResponse = result.rawResponse
          message.success('AI 搜索测试完成')
          return
        }
        message.error(res?.message || 'AI 搜索测试失败')
      }).catch(() => {
        message.error('AI 搜索测试失败')
      }).finally(() => {
        this.testSubmitting = false
      })
    },
    formatPriceAmount(value) {
      const amount = Number(value)
      if (!Number.isFinite(amount)) {
        return '￥0'
      }
      // 价格表统一使用人民币符号，并按统一的 6 位精度去尾零展示。
      return formatMoneyCny(amount)
    },
    formatMultiplier(value) {
      const multiplier = Number(value)
      if (!Number.isFinite(multiplier) || multiplier <= 0) {
        return '1'
      }
      // 最多 4 位小数并去掉尾零，1 显示为 1 而不是 1.0000。
      return formatMoneyValue(multiplier)
    },
    openPriceModal(record) {
      if (record) {
        this.priceForm.id = record.id
        this.priceForm.modelName = record.modelName || ''
        this.priceForm.displayName = record.displayName || ''
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
      const modelName = String(this.pricingGaps?.configuredModel || this.formState.model || '').trim()
      this.resetPriceForm()
      this.priceForm.modelName = modelName
      this.priceForm.displayName = modelName
      this.priceModalVisible = true
    },
    closePriceModal() {
      this.priceModalVisible = false
      this.resetPriceForm()
    },
    handleSaveModelPrice() {
      const modelName = String(this.priceForm.modelName || '').trim()
      if (!modelName) {
        message.warning('请填写模型名称')
        return
      }

      const payload = {
        id: this.priceForm.id,
        modelName,
        displayName: String(this.priceForm.displayName || '').trim(),
        inputPrice: Number(this.priceForm.inputPrice || 0),
        cachedPrice: Number(this.priceForm.cachedPrice || 0),
        cacheWritePrice: Number(this.priceForm.cacheWritePrice || 0),
        outputPrice: Number(this.priceForm.outputPrice || 0),
        multiplier: Number(this.priceForm.multiplier || 1),
        remark: this.priceForm.remark || '',
      }
      this.priceSaving = true
      const action = payload.id ? updateAIModelPrice(payload) : createAIModelPrice(payload)
      action.then(res => {
        if (res?.status === 200) {
          message.success('保存成功')
          this.closePriceModal()
          this.loadAIModelPrices()
          return
        }
        message.error(res?.message || '保存失败')
      }).catch(() => {
        message.error('保存失败')
      }).finally(() => {
        this.priceSaving = false
      })
    },
    handleDeleteModelPrice(record) {
      deleteAIModelPrice({ id: record.id }).then(res => {
        if (res?.status === 200) {
          message.success('删除成功')
          this.loadAIModelPrices()
          return
        }
        message.error(res?.message || '删除失败')
      }).catch(() => {
        message.error('删除失败')
      })
    },
    handleResetModelPrices() {
      this.priceResetting = true
      resetAIModelPrices().then(res => {
        if (res?.status === 200) {
          message.success('已恢复内置默认价格表')
          this.loadAIModelPrices()
          return
        }
        message.error(res?.message || '恢复默认失败')
      }).catch(() => {
        message.error('恢复默认失败')
      }).finally(() => {
        this.priceResetting = false
      })
    },
  }
})
</script>

<style scoped>
.page-card {
  border-radius: 20px;
  /* 表格列较宽时，卡片本身不能被内容顶宽，否则会横向溢出整个页面。 */
  max-width: 100%;
  overflow: hidden;
}

.page-alert {
  margin-bottom: 16px;
}

.hint {
  margin-top: 6px;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 1.6;
}

.hint-warning {
  color: #d46b08;
}

/* 非默认倍率用醒目标色，方便一眼看出哪些模型被加价/打折。 */
.multiplier-cell--custom {
  color: #d46b08;
  font-weight: 600;
}

.hint-block {
  margin-top: 6px;
}

.model-editor {
  display: flex;
  gap: 12px;
  align-items: flex-start;
}

.model-editor :deep(.ant-input-textarea) {
  flex: 1;
}

.fetch-btn {
  flex-shrink: 0;
}

.model-tags {
  margin-top: 10px;
}

.test-alert {
  margin-bottom: 16px;
}

.test-result-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.test-result-block {
  padding: 12px 14px;
  border-radius: 12px;
  background: #fafafa;
  border: 1px solid #f0f0f0;
}

.test-result-title {
  margin-bottom: 8px;
  font-size: 13px;
  font-weight: 600;
  color: #1f2937;
}

.test-result-pre {
  margin: 0;
  max-height: 240px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 12px;
  line-height: 1.6;
  color: #334155;
}

.pricing-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: 16px;
  margin-top: 16px;
  /* grid/flex 子项默认 min-width:auto，会被宽表格顶破容器，必须显式归零。 */
  min-width: 0;
}

.sub-card {
  border-radius: 20px;
  min-width: 0;
}

/* ant-design 卡片内容区同样是 flex 子项，需要允许收缩。 */
.sub-card :deep(.ant-card-body),
.sub-card :deep(.ant-card-head) {
  min-width: 0;
}

.sub-card-title-wrap {
  display: flex;
  align-items: center;
  min-width: 0;
}

.sub-card-title {
  font-size: 18px;
  font-weight: 700;
  color: #1f2937;
}

.sub-card-subtitle {
  margin-top: 6px;
  color: #6b7280;
  font-size: 13px;
}

.price-table-wrap {
  width: 100%;
  max-width: 100%;
  min-width: 0;
  overflow-x: auto;
}

.capability-detail {
  margin-top: 6px;
  color: #6b7280;
  font-size: 12px;
}

.capability-status-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 12px;
  margin: 4px 0 16px;
}

.capability-status-card {
  padding: 16px;
  border: 1px solid #e5e7eb;
  border-radius: 12px;
  background: linear-gradient(135deg, #f8fbff 0%, #ffffff 100%);
}

.capability-status-card__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.capability-status-card__title {
  color: #0f172a;
  font-size: 15px;
  font-weight: 700;
}

.capability-status-card__meta {
  margin-top: 10px;
  min-height: 20px;
  color: #64748b;
  font-size: 12px;
  line-height: 1.6;
}

.capability-status-card__count {
  margin-top: 12px;
  color: #2563eb;
  font-size: 13px;
  font-weight: 600;
}

.capability-usage-table {
  margin-top: 16px;
}

@media (max-width: 768px) {
  .page-card,
  .sub-card {
    border-radius: 8px;
  }

  .model-editor {
    display: grid;
  }

  .fetch-btn {
    width: 100%;
  }

  .sub-card-title {
    font-size: 16px;
  }
}
</style>
