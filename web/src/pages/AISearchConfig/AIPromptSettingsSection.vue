<template>
  <section :id="sectionId" class="console-panel ai-section" :aria-labelledby="`${sectionId}-heading`" tabindex="-1">
    <header class="section-heading ai-section-heading">
      <div>
        <h2 :id="`${sectionId}-heading`">Prompt</h2>
        <p class="section-description">维护答案格式、提示词与固定输出参数。</p>
      </div>
    </header>
    <div class="console-form-grid execution-grid">
  <a-form-item label="Prompt 设置" :html-for="`${sectionId}-prompt`" class="full-width">
    <a-textarea :id="`${sectionId}-prompt`"
      :value="formValue.prompt"
      @update:value="updatePrompt"
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

  <a-form-item label="温度" :html-for="`${sectionId}-temperature`">
    <a-input :id="`${sectionId}-temperature`" :value="fixedTemperatureText" readonly disabled />
    <div class="hint">固定为 0，用于降低随机性，让相同题目的返回结果更稳定。</div>
  </a-form-item>

  <a-form-item label="Top P" :html-for="`${sectionId}-topP`">
    <a-input :id="`${sectionId}-topP`" :value="fixedTopPText" readonly disabled />
    <div class="hint">固定为 0，与温度一起保持低随机性，不开放自定义调整。</div>
  </a-form-item>
    </div>
  </section>
</template>

<script>
export default {
  name: 'AIPromptSettingsSection',
  props: {
    sectionId: { type: String, required: true },
    formValue: { type: Object, required: true },
    fixedTemperatureText: { type: String, required: true },
    fixedTopPText: { type: String, required: true },
  },
  emits: ['update:form-value'],
  methods: {
    updatePrompt(value) {
      this.$emit('update:form-value', { ...this.formValue, prompt: value })
    },
  },
}
</script>

<style scoped>
.ai-section-heading { padding: 16px 24px; border-bottom: 1px solid var(--console-border); }
.ai-section-heading h2 { margin: 0; font-size: 16px; color: var(--console-text); }
.section-description { margin: 4px 0 0; color: var(--console-muted); font-size: 13px; }
.console-form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px 32px; min-width: 0; padding: 24px; }
.full-width { grid-column: 1 / -1; }
@media (max-width: 768px) {
  .ai-section-heading, .console-form-grid { padding: 16px; }
  .console-form-grid { grid-template-columns: minmax(0, 1fr); }
}
.hint {
  margin-top: 6px;
  color: var(--console-muted);
  font-size: 12px;
  line-height: 1.6;
}

.hint-block {
  margin-top: 6px;
}
</style>
