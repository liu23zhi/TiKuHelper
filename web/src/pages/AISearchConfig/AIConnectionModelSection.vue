<template>
  <section :id="sectionId" class="console-panel ai-section" :aria-labelledby="`${sectionId}-heading`" tabindex="-1">
    <header class="section-heading ai-section-heading">
      <div>
        <h2 :id="`${sectionId}-heading`">连接与模型</h2>
        <p class="section-description">配置 AI 服务连接、认证信息和可用模型。</p>
      </div>
      <a-button @click="$emit('test')">测试 AI、联网与 OCR 工具</a-button>
    </header>
    <div class="console-form-grid connection-grid">
      <a-form-item label="是否启用" :html-for="`${sectionId}-enabled`" class="full-width">
        <a-switch :id="`${sectionId}-enabled`" :checked="formValue.enabled" @update:checked="updateField('enabled', $event)" />
        <span class="switch-description">{{ formValue.enabled ? '已启用 AI 搜索' : 'AI 搜索已关闭' }}</span>
      </a-form-item>

      <a-form-item label="服务地址" :html-for="`${sectionId}-baseUrl`">
        <a-input :id="`${sectionId}-baseUrl`"
          :value="formValue.baseUrl" @update:value="updateField('baseUrl', $event)"
          name="ai-service-endpoint"
          type="url"
          autocomplete="off"
          autocapitalize="none"
          :spellcheck="false"
          placeholder="例如 https://api.openai.com 或 https://api.openai.com/v1"
        />
        <div class="hint">如果地址未包含 /v1，保存时会提示你是否补全；也可以保持原样保存。</div>
      </a-form-item>

      <a-form-item label="API Key" :html-for="`${sectionId}-apiKey`">
        <a-input-password :id="`${sectionId}-apiKey`"
          :value="formValue.apiKey" @update:value="updateField('apiKey', $event)"
          name="ai-service-api-key"
          autocomplete="new-password"
          autocapitalize="none"
          :spellcheck="false"
          placeholder="请输入 API Key"
        />
      </a-form-item>

      <a-form-item label="默认模型" :html-for="`${sectionId}-model`" class="full-width">
        <a-input :id="`${sectionId}-model`" :value="formValue.model" @update:value="updateField('model', $event)" placeholder="可手动输入模型名" />
        <div class="hint">也可以点击下方模型列表快速填入。</div>
        <div v-if="!pricingKnown" class="hint" role="status">模型价格待确认，请先读取模型价格表。</div>
        <div v-else-if="pricingGaps.configuredModel && !pricingGaps.configuredModelPriced" class="hint hint-warning">
          该模型尚未配置价格，调用会按 0 元计费。
          <a @click="$emit('price-for-configured-model')">去设置价格</a>
        </div>
      </a-form-item>

      <a-form-item label="备用模型" :html-for="`${sectionId}-backupModel`" class="full-width">
        <a-auto-complete :id="`${sectionId}-backupModel`" :value="formValue.backupModel" @update:value="updateField('backupModel', $event)" :options="modelOptions" :filter-option="true" allow-clear placeholder="可选；主模型安全策略重试耗尽后切换" />
        <div class="hint">仅当主模型触发上游安全策略错误且本轮重试耗尽后切换；限流、普通上游错误不会切换模型。留空表示不启用备用模型。</div>
        <div v-if="pricingKnown && pricingGaps.backupModel && !pricingGaps.backupModelPriced" class="hint hint-warning">
          该备用模型尚未配置价格，调用会按 0 元计费。<a @click="$emit('price-for-model', pricingGaps.backupModel)">去设置价格</a>
        </div>
      </a-form-item>

      <a-form-item label="模型列表" :html-for="`${sectionId}-manualModelsText`" class="full-width">
        <div class="model-editor">
          <a-textarea :id="`${sectionId}-manualModelsText`"
            :value="formValue.manualModelsText" @update:value="updateField('manualModelsText', $event)"
            :auto-size="{ minRows: 4, maxRows: 8 }"
            placeholder="支持英文逗号、中文逗号或换行分隔，例如\ngpt-5.4,gpt-5.4-mini,gpt-5.3-codex"
          />
          <a-button class="fetch-btn" @click="$emit('fetch-models')" :loading="fetchingModels">
            获取远程模型列表
          </a-button>
        </div>
        <div class="hint">保存时会以此处内容作为可用模型列表，点击下方标签可填入默认模型。</div>
        <div v-if="pricingKnown" class="hint">标签上带「未定价」的模型需要先在下方「模型价格表」中补价，否则会按 0 元计费。</div>
        <div v-else class="hint" role="status">模型价格待确认，标签暂不标记是否已定价。</div>
        <a-space wrap class="model-tags">
          <a-tag
            v-for="item in modelOptions"
            :key="item.value"
            :color="!pricingKnown ? 'default' : isModelPriced(item.value, priceRows) ? 'blue' : 'orange'"
            style="cursor: pointer"
            @click="$emit('select-model', item.value)"
          >
            {{ item.label }}<span v-if="!pricingKnown">（价格待确认）</span><span v-else-if="!isModelPriced(item.value, priceRows)">（未定价）</span>
          </a-tag>
          <span v-if="modelOptions.length === 0" class="hint">暂无模型列表，请先手动填写或获取远程模型。</span>
        </a-space>
      </a-form-item>

    </div>
  </section>
</template>

<script>
import { isModelPriced } from '@/utils/aiSearchModelHelpers.mjs'

export default {
  name: 'AIConnectionModelSection',
  props: {
    sectionId: { type: String, required: true },
    formValue: { type: Object, required: true },
    fetchingModels: { type: Boolean, default: false },
    modelOptions: { type: Array, default: () => [] },
    priceRows: { type: Array, default: () => [] },
    pricingGaps: { type: Object, default: () => ({}) },
    pricingKnown: { type: Boolean, default: true },
  },
  emits: ['update:form-value', 'test', 'price-for-configured-model', 'price-for-model', 'fetch-models', 'select-model'],
  methods: {
    isModelPriced,
    updateField(field, value) {
      this.$emit('update:form-value', { ...this.formValue, [field]: value })
    },
  },
}
</script>

<style scoped>
.hint {
  margin-top: 6px;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 1.6;
}

.hint-warning {
  color: #d46b08;
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

@media (max-width: 768px) {
  .model-editor {
    display: grid;
  }

  .fetch-btn {
    width: 100%;
  }
}
</style>
