<template>
  <a-modal
    :open="open"
    title="测试 AI 与联网工具"
    :ok-text="submitText"
    cancel-text="关闭"
    :confirm-loading="submitting"
    :closable="!submitting"
    :mask-closable="!submitting"
    width="920px"
    @ok="runTest"
    @cancel="close"
  >
    <a-form layout="vertical" autocomplete="off">
      <a-form-item label="测试类型">
        <a-radio-group v-model:value="testType" :disabled="submitting" class="test-type-options" @change="resetResult">
          <a-radio-button value="hello">hello 连通性测试</a-radio-button>
          <a-radio-button value="direct">独立工具测试</a-radio-button>
          <a-radio-button value="ai">AI 工具调用测试</a-radio-button>
          <a-radio-button value="compression">独立压缩测试</a-radio-button>
          <a-radio-button value="ai_compression">AI 调用压缩测试</a-radio-button>
        </a-radio-group>
      </a-form-item>
      <a-form-item v-if="includesCompression" label="压缩策略">
        <a-radio-group v-model:value="form.compressionMode" :disabled="submitting" class="test-tool-options" @change="resetResult">
          <a-radio value="configured">按已保存顺序自动尝试</a-radio>
          <a-radio value="server">服务器自动压缩</a-radio>
          <a-radio value="remote">独立节点，失败回退本地</a-radio>
          <a-radio value="local">本地压缩</a-radio>
        </a-radio-group>
        <div v-if="form.compressionMode === 'configured'" class="hint">保存的顺序：{{ configuredCompressionOrder }}</div>
        <div class="hint">压缩测试会使用已保存的压缩尝试次数；AI 调用压缩测试默认仅验证历史是否保留。需要验证压缩后联网时，请显式选择工具并填写目标。实际模型和工具用量计入管理员账单。</div>
      </a-form-item>
      <a-form-item v-if="includesCompression" label="待压缩历史" required>
        <a-textarea v-model:value="form.history" :disabled="submitting" :auto-size="{ minRows: 6, maxRows: 14 }" placeholder="粘贴已有的搜索记录、网页内容或工具调用历史。压缩后将展示摘要、前后 Token 估算和执行轨迹。" />
        <div class="hint">测试会主动执行压缩，无需等待触发阈值。默认内容为合成测试资料，可替换为真实历史；过短或无法安全缩小的内容会明确报错。</div>
      </a-form-item>
      <template v-if="testType === 'direct' || testType === 'ai' || testType === 'ai_compression'">
        <a-form-item label="测试工具">
          <a-radio-group v-model:value="form.tool" :disabled="submitting" class="test-tool-options" @change="resetResult">
            <a-radio v-if="testType === 'ai_compression'" value="none">不联网，仅验证历史</a-radio>
            <a-radio value="web_search">联网搜索（web_search）</a-radio>
            <a-radio value="web_read">网页读取（web_read）</a-radio>
            <a-radio v-if="testType !== 'direct'" value="both">搜索＋网页读取</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item v-if="testType === 'direct' && form.tool === 'web_search'" label="搜索词" required>
          <a-input v-model:value="form.query" :disabled="submitting" placeholder="例如 OpenAI" />
        </a-form-item>
        <a-form-item v-else-if="testType === 'direct'" label="网页地址" required>
          <a-input v-model:value="form.url" :disabled="submitting" type="url" placeholder="https://www.openai.com/" />
        </a-form-item>
        <a-form-item v-else-if="form.tool !== 'none'" :label="form.tool === 'web_read' ? '网页地址' : '关键词或网页地址'" required>
          <a-input v-model:value="form.target" :disabled="submitting" :type="form.tool === 'web_read' ? 'url' : 'text'" :placeholder="form.tool === 'web_read' ? 'https://www.openai.com/' : '例如 OpenAI 或 https://www.openai.com/'" />
          <div v-if="form.tool === 'web_read'" class="hint">单独测试网页读取时，请提供完整网页地址。</div>
        </a-form-item>
        <a-form-item v-if="testType === 'ai' || testType === 'ai_compression'" label="生成的测试提示词">
          <a-textarea v-if="promptPreview.text" :value="promptPreview.text" readonly :auto-size="{ minRows: 3, maxRows: 6 }" aria-label="生成的测试提示词" />
          <div v-else class="hint" role="status">{{ promptPreview.error }}</div>
        </a-form-item>
      </template>
      <template v-if="testType === 'hello' || testType === 'ai' || testType === 'compression' || testType === 'ai_compression'">
        <a-form-item label="测试模型" required>
          <a-select v-model:value="form.model" :disabled="submitting" show-search allow-clear :options="modelOptions" placeholder="请选择测试模型" />
        </a-form-item>
        <a-form-item label="服务地址">
          <a-input v-model:value="form.baseUrl" :disabled="submitting" name="ai-test-service-endpoint" type="url" autocomplete="off" autocapitalize="none" :spellcheck="false" placeholder="留空时使用当前页面中的服务地址" />
        </a-form-item>
        <a-form-item label="API Key">
          <a-input-password v-model:value="form.apiKey" :disabled="submitting" name="ai-test-service-api-key" autocomplete="new-password" autocapitalize="none" :spellcheck="false" placeholder="留空时使用当前页面中的 API Key" />
        </a-form-item>
        <a-form-item :label="testType === 'ai' || testType === 'ai_compression' ? '系统 Prompt 覆盖' : 'Prompt 覆盖'">
          <a-textarea v-model:value="form.prompt" :disabled="submitting" :auto-size="{ minRows: 3, maxRows: 6 }" placeholder="留空时使用当前页面中的 Prompt" />
        </a-form-item>
      </template>
    </a-form>

    <a-alert v-if="testType === 'hello'" type="info" show-icon class="test-alert" message="固定发送 hello，展示请求体、原始流式响应和解析答案。此测试不写入账单。" />
    <a-alert v-else type="info" show-icon class="test-alert" :message="testModeDescription" :description="toolTestDescription" />
    <a-alert v-if="submitting && includesWebRead" type="info" show-icon class="test-alert" message="网页读取为异步任务，提交后将自动轮询，请等待任务完成并返回正文。" />
    <a-alert v-if="error" type="error" show-icon class="test-alert" :message="error" />

    <div v-if="result" class="test-result-panel" aria-live="polite">
      <template v-if="testType !== 'hello'">
        <a-alert :type="result.success ? 'success' : 'error'" show-icon :message="result.message || (result.success ? '工具调用成功并返回结果' : '工具调用测试未成功')" />
        <div class="test-result-reference">
          <span>请求编号：{{ result.requestId || '-' }}</span>
          <a-button v-if="result.billingRecord" @click="billingOpen = true">查看本次账单</a-button>
          <span v-else class="hint">本次未生成账单记录</span>
        </div>
        <a-alert v-if="result.billingWarning" type="warning" show-icon :message="result.billingWarning" />
        <section v-if="includesCompression && result.compression" class="test-result-block">
          <h3>压缩结果</h3>
          <div class="compression-summary">
            <a-tag :color="result.compression.success ? 'green' : 'red'">{{ result.compression.success ? '压缩成功' : '压缩失败' }}</a-tag>
            <span>策略：{{ compressionStrategyText(result.compression.strategy) }}</span>
            <span>Token 估算：{{ result.compression.beforeTokens ?? '-' }} → {{ result.compression.afterTokens ?? '-' }}</span>
            <a-tag v-if="result.compression.attemptedStrategies.length > 1 || result.compression.remoteFallback" color="orange">已按顺序回退</a-tag>
          </div>
          <p v-if="result.compression.attemptedStrategies.length" class="compression-attempts">实际尝试：{{ result.compression.attemptedStrategies.map(compressionStrategyText).join(' → ') }}</p>
          <p v-if="result.compression.fallbackReason" class="compression-error">回退原因：{{ result.compression.fallbackReason }}</p>
          <p v-if="result.compression.error" class="compression-error">{{ result.compression.error }}</p>
          <pre>{{ result.compression.summary || '暂无压缩摘要' }}</pre>
        </section>
        <section v-if="(testType === 'ai' || testType === 'ai_compression') && result.testPrompt" class="test-result-block">
          <h3>本次实际提示词</h3>
          <pre>{{ result.testPrompt }}</pre>
        </section>
        <section v-if="testType === 'ai' || testType === 'direct' || (testType === 'ai_compression' && (form.tool !== 'none' || result.toolCalls.length))" class="test-result-block">
          <h3>工具调用详情</h3>
          <AIToolTestResults :calls="result.toolCalls" />
        </section>
      </template>
      <section v-if="testType === 'hello' || testType === 'ai' || testType === 'ai_compression'" class="test-result-block">
        <h3>解析答案</h3>
        <ol v-if="result.answers.length" class="test-answer-list">
          <li v-for="(answer, index) in result.answers" :key="index" class="test-answer-text">{{ answer }}</li>
        </ol>
        <span v-else class="hint">暂无解析答案</span>
      </section>
      <details v-if="testType !== 'hello'" class="test-result-block">
        <summary>调用轨迹及请求响应</summary>
        <div v-for="item in diagnosticBlocks" :key="item.title" class="test-diagnostic-block">
          <h3>{{ item.title }}</h3><pre>{{ item.text || '-' }}</pre>
        </div>
      </details>
      <template v-else>
        <section class="test-result-block"><h3>请求内容</h3><pre>{{ result.requestBody || '-' }}</pre></section>
        <section class="test-result-block"><h3>完整回复</h3><pre>{{ result.rawResponse || '-' }}</pre></section>
      </template>
    </div>
  </a-modal>
  <BillingDetailDialog v-model:open="billingOpen" :record="result?.billingRecord || null" viewer-scope="admin" />
</template>

<script>
import { computed, defineComponent, reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import BillingDetailDialog from '@/components/BillingDetailDialog.vue'
import AIToolTestResults from './AIToolTestResults.vue'
import { testAISearchConfig, testAIToolCall } from '@/api/TikuComponentApi'
import { AI_COMPRESSION_TEST_DEFAULTS, AI_COMPRESSION_TEST_HISTORY, aiContextCompressionStrategyText, buildAIContextCompressionSettings, buildAISearchTestForm, buildAISearchTestPayload, buildAISearchTestResult, buildAICompressionToolTestPrompt, buildAIToolTestPayload, buildAIToolTestPrompt, buildAIToolTestResult } from '@/utils/aiSearchConfigHelpers.mjs'

export default defineComponent({
  name: 'AISearchTestDialog',
  components: { BillingDetailDialog, AIToolTestResults },
  props: {
    open: { type: Boolean, default: false },
    config: { type: Object, default: () => ({}) },
    modelOptions: { type: Array, default: () => [] },
    savedCompressionOrder: { type: Array, default: () => buildAIContextCompressionSettings().contextCompressionOrder },
  },
  emits: ['update:open'],
  setup(props, { emit }) {
    const testType = ref('hello')
    const form = reactive({ ...buildAISearchTestForm(null), tool: 'web_search', target: '', query: '', url: '', history: AI_COMPRESSION_TEST_HISTORY })
    let initialized = false
    let compressionTool = AI_COMPRESSION_TEST_DEFAULTS.tool
    let networkTool = 'web_search'
    const submitting = ref(false)
    const result = ref(null)
    const error = ref('')
    const billingOpen = ref(false)
    const submitText = computed(() => testType.value === 'ai_compression' && form.tool === 'none' ? '压缩并验证历史' : ({ hello: '发送 hello 测试', direct: '测试工具', ai: '发起 AI 工具调用', compression: '执行压缩测试', ai_compression: '压缩并调用工具' }[testType.value]))
    const includesCompression = computed(() => ['compression', 'ai_compression'].includes(testType.value))
    const configuredCompressionOrder = computed(() => props.savedCompressionOrder.map(aiContextCompressionStrategyText).join(' → '))
    const includesWebRead = computed(() => ['direct', 'ai', 'ai_compression'].includes(testType.value) && ['web_read', 'both'].includes(form.tool))
    const toolTestDescription = computed(() => `${includesWebRead.value ? '网页读取会先提交异步任务，再轮询获取正文。' : ''}测试会产生真实模型或工具调用费用，并按当前已保存的价格配置写入当前管理员的账单。`)
    const testModeDescription = computed(() => ({
      direct: '直接调用所选工具，检查工具是否返回结果。',
      ai: '根据输入生成提示词，发起真实 AI 请求，检查模型是否实际调用所选工具并返回结果。',
      compression: '独立执行上下文压缩，不发起答题或联网工具调用；压缩模型用量正常计入管理员账单。',
      ai_compression: form.tool === 'none' ? '先压缩给定历史，再通过 AI 回读标记、字段和待办状态，不发起联网请求。' : '先压缩给定历史，再发起所选工具调用，检查压缩后的上下文能否继续完成请求。',
    }[testType.value]))
    const promptPreview = computed(() => {
      try { return { text: testType.value === 'ai_compression' ? buildAICompressionToolTestPrompt(form) : buildAIToolTestPrompt(form), error: '' } }
      catch (validationError) { return { text: '', error: validationError.message } }
    })
    const diagnosticBlocks = computed(() => [
      { title: '压缩指标', text: result.value?.compression ? JSON.stringify(result.value.compression, null, 2) : '' },
      { title: '工具原始返回结果', text: testType.value === 'compression' || (testType.value === 'ai_compression' && form.tool === 'none' && !result.value?.toolCalls?.length) ? '' : result.value?.toolResultText },
      { title: '调用轨迹', text: result.value?.traceText },
      { title: '用量', text: result.value?.usageText },
      { title: '请求内容', text: result.value?.requestBody },
      { title: '完整回复', text: result.value?.rawResponse },
    ].filter(item => item.text))
    function resetResult() {
      result.value = null
      error.value = ''
      billingOpen.value = false
    }
    watch(() => props.open, (open) => {
      if (!open) return
      const { history, compressionMode, ...connection } = buildAISearchTestForm(props.config)
      Object.assign(form, connection)
      if (!initialized) {
        form.history = history || AI_COMPRESSION_TEST_HISTORY
        form.compressionMode = compressionMode
        initialized = true
      }
      resetResult()
    })
    watch(testType, (mode, previous) => {
      if (previous === 'ai_compression') compressionTool = form.tool
      else if (previous === 'ai' || previous === 'direct') networkTool = form.tool
      if (mode === 'ai_compression') {
        form.tool = compressionTool
      } else if (mode === 'ai') {
        form.tool = networkTool
      } else if (mode === 'direct') {
        form.tool = networkTool === 'web_read' ? 'web_read' : 'web_search'
      }
    })
    async function runTest() {
      if (submitting.value) return
      let payload
      try {
        payload = testType.value === 'hello'
          ? buildAISearchTestPayload(form, props.config)
          : buildAIToolTestPayload({ ...form, mode: testType.value }, props.config)
      } catch (validationError) {
        error.value = validationError.message
        return
      }
      resetResult()
      submitting.value = true
      try {
        const response = await (testType.value === 'hello' ? testAISearchConfig(payload) : testAIToolCall(payload))
        if (response?.status !== 200) throw new Error(response?.data?.message || response?.message || '测试失败')
        result.value = testType.value === 'hello' ? buildAISearchTestResult(response) : buildAIToolTestResult(response)
        if (testType.value === 'hello') message.success('AI 搜索测试完成')
      } catch (requestError) {
        error.value = requestError?.response?.data?.message || requestError?.message || '测试请求失败'
      } finally {
        submitting.value = false
      }
    }
    function close() { if (!submitting.value) emit('update:open', false) }
    return { testType, form, submitting, result, error, billingOpen, submitText, includesCompression, includesWebRead, configuredCompressionOrder, toolTestDescription, testModeDescription, compressionStrategyText: aiContextCompressionStrategyText, promptPreview, diagnosticBlocks, resetResult, runTest, close }
  },
})
</script>

<style scoped>
.test-alert { margin-bottom: 16px; }
.test-type-options { display: flex; flex-wrap: wrap; row-gap: 8px; }
.test-tool-options { display: flex; flex-wrap: wrap; gap: 8px 0; }
.test-result-panel { display: grid; grid-template-columns: minmax(0, 1fr); gap: 12px; min-width: 0; }
.test-result-panel :deep(.ant-alert-message), .test-result-panel :deep(.ant-alert-description) { overflow-wrap: anywhere; white-space: pre-wrap; }
.test-result-block { min-width: 0; max-width: 100%; padding: 16px; border: 1px solid var(--console-border); border-radius: 8px; background: var(--console-subtle); }
.test-result-block h3 { margin: 0 0 8px; color: var(--console-text); font-size: 14px; font-weight: 600; }
.test-result-block pre { margin: 0; max-width: 100%; max-height: 280px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 12px; line-height: 1.7; color: var(--console-text); }
.test-answer-list { display: grid; grid-template-columns: minmax(0, 1fr); gap: 12px; margin: 0; padding-inline-start: 24px; color: var(--console-text); }
.test-answer-text { min-width: 0; max-width: 100%; white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.8; }
.test-result-block summary { cursor: pointer; color: var(--console-text); font-weight: 600; }
.test-diagnostic-block { margin-top: 16px; }
.test-result-reference { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 12px; overflow-wrap: anywhere; }
.compression-summary { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 16px; margin-bottom: 12px; color: var(--console-muted); font-size: 12px; }
.compression-error { margin: 8px 0; color: var(--console-danger); white-space: pre-wrap; overflow-wrap: anywhere; }
.compression-attempts { margin: 8px 0; color: var(--console-muted); overflow-wrap: anywhere; }
.hint { color: var(--console-muted); font-size: 12px; }
@media (max-width: 480px) {
  .test-result-block { padding: 12px; }
  :deep(.ant-radio-button-wrapper) { padding-inline: 8px; font-size: 12px; }
}
</style>
