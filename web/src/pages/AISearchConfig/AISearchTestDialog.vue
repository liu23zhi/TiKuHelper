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
        <a-radio-group v-model:value="testType" :disabled="submitting" @change="resetResult">
          <a-radio-button value="hello">hello 连通性测试</a-radio-button>
          <a-radio-button value="direct">独立工具测试</a-radio-button>
          <a-radio-button value="ai">AI 工具调用测试</a-radio-button>
        </a-radio-group>
      </a-form-item>
      <template v-if="testType !== 'hello'">
        <a-form-item label="测试工具">
          <a-radio-group v-model:value="form.tool" :disabled="submitting" class="test-tool-options" @change="resetResult">
            <a-radio value="web_search">联网搜索（web_search）</a-radio>
            <a-radio value="web_read">网页读取（web_read）</a-radio>
            <a-radio v-if="testType === 'ai'" value="both">搜索＋网页读取</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item v-if="testType === 'ai'" :label="form.tool === 'web_read' ? '网页地址' : '关键词或网页地址'" required>
          <a-input v-model:value="form.target" :disabled="submitting" :type="form.tool === 'web_read' ? 'url' : 'text'" :placeholder="form.tool === 'web_read' ? 'https://www.openai.com/' : '例如 OpenAI 或 https://www.openai.com/'" />
          <div v-if="form.tool === 'web_read'" class="hint">单独测试网页读取时，请提供完整网页地址。</div>
        </a-form-item>
        <a-form-item v-else-if="form.tool === 'web_search'" label="搜索词" required>
          <a-input v-model:value="form.query" :disabled="submitting" placeholder="例如 OpenAI" />
        </a-form-item>
        <a-form-item v-else label="网页地址" required>
          <a-input v-model:value="form.url" :disabled="submitting" type="url" placeholder="https://www.openai.com/" />
        </a-form-item>
        <a-form-item v-if="testType === 'ai'" label="生成的测试提示词">
          <a-textarea v-if="promptPreview.text" :value="promptPreview.text" readonly :auto-size="{ minRows: 3, maxRows: 6 }" aria-label="生成的测试提示词" />
          <div v-else class="hint" role="status">{{ promptPreview.error }}</div>
        </a-form-item>
      </template>
      <template v-if="testType !== 'direct'">
        <a-form-item label="测试模型" required>
          <a-select v-model:value="form.model" :disabled="submitting" show-search allow-clear :options="modelOptions" placeholder="请选择测试模型" />
        </a-form-item>
        <a-form-item label="服务地址">
          <a-input v-model:value="form.baseUrl" :disabled="submitting" name="ai-test-service-endpoint" type="url" autocomplete="off" autocapitalize="none" :spellcheck="false" placeholder="留空时使用当前页面中的服务地址" />
        </a-form-item>
        <a-form-item label="API Key">
          <a-input-password v-model:value="form.apiKey" :disabled="submitting" name="ai-test-service-api-key" autocomplete="new-password" autocapitalize="none" :spellcheck="false" placeholder="留空时使用当前页面中的 API Key" />
        </a-form-item>
        <a-form-item :label="testType === 'ai' ? '系统 Prompt 覆盖' : 'Prompt 覆盖'">
          <a-textarea v-model:value="form.prompt" :disabled="submitting" :auto-size="{ minRows: 3, maxRows: 6 }" placeholder="留空时使用当前页面中的 Prompt" />
        </a-form-item>
      </template>
    </a-form>

    <a-alert v-if="testType === 'hello'" type="info" show-icon class="test-alert" message="固定发送 hello，展示请求体、原始流式响应和解析答案。此测试不写入账单。" />
    <a-alert v-else type="info" show-icon class="test-alert" :message="testType === 'direct' ? '直接调用所选工具，检查工具是否返回结果。' : '根据输入生成提示词，发起真实 AI 请求，检查模型是否实际调用所选工具并返回结果。'" :description="toolTestDescription" />
    <a-alert v-if="submitting && testType !== 'hello' && includesWebRead" type="info" show-icon class="test-alert" message="网页读取为异步任务，提交后将自动轮询，请等待任务完成并返回正文。" />
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
        <section v-if="testType === 'ai' && result.testPrompt" class="test-result-block">
          <h3>本次实际提示词</h3>
          <pre>{{ result.testPrompt }}</pre>
        </section>
        <section class="test-result-block">
          <h3>工具返回结果</h3>
          <pre>{{ result.toolResultText || '-' }}</pre>
        </section>
      </template>
      <section v-if="testType !== 'direct'" class="test-result-block">
        <h3>解析答案</h3>
        <a-space wrap>
          <a-tag v-for="(answer, index) in result.answers" :key="index" color="blue">{{ answer }}</a-tag>
          <span v-if="!result.answers.length" class="hint">暂无解析答案</span>
        </a-space>
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
import { testAISearchConfig, testAIToolCall } from '@/api/TikuComponentApi'
import { buildAISearchTestForm, buildAISearchTestPayload, buildAISearchTestResult, buildAIToolTestPayload, buildAIToolTestPrompt, buildAIToolTestResult } from '@/utils/aiSearchConfigHelpers.mjs'

export default defineComponent({
  name: 'AISearchTestDialog',
  components: { BillingDetailDialog },
  props: {
    open: { type: Boolean, default: false },
    config: { type: Object, default: () => ({}) },
    modelOptions: { type: Array, default: () => [] },
  },
  emits: ['update:open'],
  setup(props, { emit }) {
    const testType = ref('hello')
    const form = reactive({ ...buildAISearchTestForm(null), tool: 'web_search', target: 'OpenAI', query: 'OpenAI', url: 'https://www.openai.com/' })
    const submitting = ref(false)
    const result = ref(null)
    const error = ref('')
    const billingOpen = ref(false)
    const submitText = computed(() => ({ hello: '发送 hello 测试', direct: '测试工具', ai: '发起 AI 工具调用' }[testType.value]))
    const includesWebRead = computed(() => ['web_read', 'both'].includes(form.tool))
    const toolTestDescription = computed(() => `${includesWebRead.value ? '网页读取会先提交异步任务，再轮询获取正文。' : ''}测试会产生真实模型或工具调用费用，并按当前已保存的价格配置写入当前管理员的账单。`)
    const promptPreview = computed(() => {
      try { return { text: buildAIToolTestPrompt(form), error: '' } }
      catch (validationError) { return { text: '', error: validationError.message } }
    })
    const diagnosticBlocks = computed(() => [
      { title: '调用轨迹', text: result.value?.traceText },
      { title: '用量', text: result.value?.usageText },
      { title: '请求内容', text: result.value?.requestBody },
      { title: '完整回复', text: result.value?.rawResponse },
    ])
    function resetResult() {
      result.value = null
      error.value = ''
      billingOpen.value = false
    }
    watch(() => props.open, (open) => {
      if (!open) return
      Object.assign(form, buildAISearchTestForm(props.config))
      resetResult()
    })
    watch(testType, (mode) => {
      if (mode !== 'ai' && form.tool === 'both') form.tool = 'web_search'
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
    return { testType, form, submitting, result, error, billingOpen, submitText, includesWebRead, toolTestDescription, promptPreview, diagnosticBlocks, resetResult, runTest, close }
  },
})
</script>

<style scoped>
.test-alert { margin-bottom: 16px; }
.test-tool-options { display: flex; flex-wrap: wrap; gap: 8px 0; }
.test-result-panel { display: grid; gap: 12px; min-width: 0; }
.test-result-block { min-width: 0; padding: 16px; border: 1px solid var(--console-border); border-radius: 8px; background: var(--console-subtle); }
.test-result-block h3 { margin: 0 0 8px; color: var(--console-text); font-size: 14px; font-weight: 600; }
.test-result-block pre { margin: 0; max-height: 280px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 12px; line-height: 1.7; color: var(--console-text); }
.test-result-block summary { cursor: pointer; color: var(--console-text); font-weight: 600; }
.test-diagnostic-block { margin-top: 16px; }
.test-result-reference { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 12px; overflow-wrap: anywhere; }
.hint { color: var(--console-muted); font-size: 12px; }
@media (max-width: 480px) {
  .test-result-block { padding: 12px; }
  :deep(.ant-radio-button-wrapper) { padding-inline: 8px; font-size: 12px; }
}
</style>
