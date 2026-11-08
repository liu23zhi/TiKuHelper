<template>
  <section :id="sectionId" class="console-panel ai-section" :aria-labelledby="`${sectionId}-heading`" tabindex="-1">
        <header class="section-heading ai-section-heading">
          <div>
            <h2 :id="`${sectionId}-heading`">高级能力</h2>
            <p class="section-description">设置模型轮次、联网搜索与网页读取预算。</p>
          </div>
          <a-button @click="$emit('view-capability')" :loading="searchEngineLoading">查看深度思考能力</a-button>
        </header>
        <div class="console-form-grid execution-grid">
      <a-divider orientation="left" class="full-width">AI 搜索预算</a-divider>
      <a-form-item label="模型最大调用次数" :html-for="`${sectionId}-maxAISearchModelCalls`">
        <a-input-number :id="`${sectionId}-maxAISearchModelCalls`"
          :value="formValue.maxAISearchModelCalls" @update:value="updateField('maxAISearchModelCalls', $event)"
          :min="limits.minModelCalls"
          :max="limits.maxBudget"
          :precision="0"
          :step="1"
          :parser="integerParser"
          style="width: 180px"
        />
        <span class="unit-label">次</span>
        <div class="hint">每次 AI 搜索最多执行的模型逻辑轮次，默认 4 轮，范围 1～20 轮。同一轮内的失败重试不额外占用轮次，切换备用地址会占用一轮。每一轮提示中都会告知模型剩余调用次数。</div>
      </a-form-item>

      <a-form-item label="最大搜索次数" :html-for="`${sectionId}-maxAISearches`">
        <a-input-number :id="`${sectionId}-maxAISearches`"
          :value="formValue.maxAISearches" @update:value="updateField('maxAISearches', $event)"
          :min="limits.minSearches"
          :max="limits.maxBudget"
          :precision="0"
          :step="1"
          :parser="integerParser"
          style="width: 180px"
        />
        <span class="unit-label">次</span>
        <div class="hint">深度思考模式下，每次 AI 搜索最多调用搜索 API 的次数，默认 3 次，范围 1～20 次。每一轮模型调用前都会告知剩余搜索次数，最后一轮模型调用直接输出答案。</div>
      </a-form-item>

      <a-form-item label="每轮重试次数" :html-for="`${sectionId}-maxAISearchRetries`">
        <a-input-number :id="`${sectionId}-maxAISearchRetries`"
          :value="formValue.maxAISearchRetries" @update:value="updateField('maxAISearchRetries', $event)"
          :min="limits.minRetries"
          :max="limits.maxRetries"
          :precision="0"
          :step="1"
          :parser="integerParser"
          style="width: 180px"
        />
        <span class="unit-label">次</span>
        <div class="hint">每轮模型调用最多尝试的机会（包含首次调用），默认 3 次，范围 1～10 次。单次模型调用失败时会在本轮内自动重试，耗尽后才结束本轮。</div>
      </a-form-item>

      <a-form-item label="联网搜索单价（元/次）" :html-for="`${sectionId}-searchPricePerCall`">
        <a-input-number :id="`${sectionId}-searchPricePerCall`"
          :value="formValue.searchPricePerCall" @update:value="updateField('searchPricePerCall', $event)"
          :min="0"
          :precision="6"
          :step="0.01"
          style="width: 180px"
        />
        <span class="unit-label">元 / 次</span>
        <div class="hint">默认 0 元。每次成功完成联网搜索 API 调用都会单独记一条账单，单价为 0 时也会保留 0 元明细；命中本次 AI 搜索内的相同查询缓存不会重复计价。</div>
      </a-form-item>

      <a-form-item label="本地题库检索单价（元/次）" :html-for="`${sectionId}-tikuSearchPricePerCall`">
        <a-input-number :id="`${sectionId}-tikuSearchPricePerCall`"
          :value="formValue.tikuSearchPricePerCall" @update:value="updateField('tikuSearchPricePerCall', $event)"
          :min="0" :precision="6" :step="0.01" style="width: 180px"
        />
        <span class="unit-label">元 / 次</span>
        <div class="hint">默认 0 元。深度思考模式下成功完成本地题库检索按此单价计费，账单详情显示检索词与候选数。</div>
      </a-form-item>

      <a-divider orientation="left" class="full-width">网页读取</a-divider>

      <a-form-item label="网页读取次数" :html-for="`${sectionId}-maxAIWebReads`">
        <a-input-number :id="`${sectionId}-maxAIWebReads`"
          :value="formValue.maxAIWebReads" @update:value="updateField('maxAIWebReads', $event)"
          :min="limits.minWebReads"
          :max="limits.maxWebReads"
          :precision="0"
          :step="1"
          :parser="integerParser"
          style="width: 180px"
        />
        <span class="unit-label">次</span>
        <div class="hint">深度思考模式下，模型每次 AI 搜索最多调用网页读取工具的次数，默认 5 次，范围 1～20 次。普通模式仍保持原先的一次性回复，不会调用网页读取。</div>
      </a-form-item>

      <a-form-item label="网页读取单价（元/次）" :html-for="`${sectionId}-webReadPricePerCall`">
        <a-input-number :id="`${sectionId}-webReadPricePerCall`"
          :value="formValue.webReadPricePerCall" @update:value="updateField('webReadPricePerCall', $event)"
          :min="0"
          :precision="6"
          :step="0.01"
          style="width: 180px"
        />
        <span class="unit-label">元 / 次</span>
        <div class="hint">默认 0 元。每次成功完成网页读取都会单独写入一条账单，单价为 0 时也保留 0 元明细；失败调用只进入诊断。</div>
      </a-form-item>

      <a-form-item label="图片 OCR 单价（元/次）" :html-for="`${sectionId}-imageOCRPricePerCall`">
        <a-input-number :id="`${sectionId}-imageOCRPricePerCall`" :value="formValue.imageOCRPricePerCall" @update:value="updateField('imageOCRPricePerCall', $event)" :min="0" :precision="6" :step="0.01" style="width: 180px" />
        <span class="unit-label">元 / 次</span>
        <div class="hint">默认 0 元。配置有效 UAPI 密钥后，服务器下载题图并上传 OCR，原图仍发送给视觉模型。成功 OCR 在单价为 0 时也保留 0 元明细，失败只进入诊断；测试须明确发起。</div>
      </a-form-item>

      <a-divider orientation="left" class="full-width">上下文压缩</a-divider>
      <a-form-item label="自动压缩上下文" :html-for="`${sectionId}-contextCompressionEnabled`" class="full-width">
        <a-switch :id="`${sectionId}-contextCompressionEnabled`" :checked="formValue.contextCompressionEnabled" @update:checked="updateField('contextCompressionEnabled', $event)" />
        <div class="hint">按模型价格表中的上下文窗口估算触发比例；未设置窗口时，仅在上游明确返回上下文超限后尝试。摘要会额外请求当前模型并按实际 Token 计费，不是零外呼的纯本地处理。压缩后会重新注入原题和系统规则，不将估算值当作精确 Token 数。</div>
      </a-form-item>
      <template v-if="formValue.contextCompressionEnabled">
        <a-form-item :html-for="`${sectionId}-contextCompressionOrder-entry`" class="full-width">
          <template #label><span :id="`${sectionId}-contextCompressionOrder-label`">压缩执行顺序</span></template>
          <AICompressionOrder :id-prefix="`${sectionId}-contextCompressionOrder`" :labelled-by="`${sectionId}-contextCompressionOrder-label`" :model-value="formValue.contextCompressionOrder" @update:model-value="updateField('contextCompressionOrder', $event)" />
          <div class="hint">按已保存顺序尝试服务器自动压缩、独立节点或本地模型摘要；本地模型摘要仍会外呼当前模型，并按实际 Token 计费。</div>
        </a-form-item>
        <a-form-item label="压缩触发比例" :html-for="`${sectionId}-contextCompressionTriggerPercent`">
          <a-input-number :id="`${sectionId}-contextCompressionTriggerPercent`" :value="formValue.contextCompressionTriggerPercent" @update:value="updateField('contextCompressionTriggerPercent', $event)" :min="50" :max="95" :precision="0" :step="1" style="width: 140px" />
          <span class="unit-label">%</span>
          <div class="hint">估算上下文达到窗口的此比例时触发，范围 50～95%。</div>
        </a-form-item>
        <a-form-item label="压缩目标比例" :html-for="`${sectionId}-contextCompressionTargetPercent`">
          <a-input-number :id="`${sectionId}-contextCompressionTargetPercent`" :value="formValue.contextCompressionTargetPercent" @update:value="updateField('contextCompressionTargetPercent', $event)" :min="10" :max="Math.max(10, Number(formValue.contextCompressionTriggerPercent) - 1)" :precision="0" :step="1" style="width: 140px" />
          <span class="unit-label">%</span>
          <div class="hint">至少 10%，且必须小于触发比例；保留原题和规则所需的空间。</div>
        </a-form-item>
        <a-form-item label="压缩最大尝试次数" :html-for="`${sectionId}-contextCompressionMaxAttempts`" class="full-width">
          <a-input-number :id="`${sectionId}-contextCompressionMaxAttempts`" :value="formValue.contextCompressionMaxAttempts" @update:value="updateField('contextCompressionMaxAttempts', $event)" :min="1" :precision="0" :step="1" :parser="integerParser" style="width: 140px" />
          <span class="unit-label">次</span>
          <div class="hint">填写正整数，含首次；独立压缩、服务器压缩测试和本地摘要各阶段按此次数尝试，长历史可分块摘要后归并。正常答题的服务器自动压缩与同次模型请求共用每轮重试次数；回退不重复已完成工具，实际模型用量均计入账单。</div>
        </a-form-item>
      </template>

        </div>
      </section>
</template>

<script>
import { Divider } from 'ant-design-vue'
import AICompressionOrder from './AICompressionOrder.vue'
export default {
  components: { ADivider: Divider, AICompressionOrder },
  name: 'AIAdvancedSettingsSection',
  props: {
    sectionId: { type: String, required: true },
    formValue: { type: Object, required: true },
    limits: { type: Object, required: true },
    integerParser: { type: Function, required: true },
    searchEngineLoading: { type: Boolean, default: false },
  },
  emits: ['update:form-value', 'view-capability'],
  methods: {
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
</style>
