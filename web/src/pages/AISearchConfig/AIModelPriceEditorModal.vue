<template>
<a-modal
  :open="open"
  @update:open="$emit('update:open', $event)"
  :title="formValue.id ? '编辑模型价格' : '新增模型价格'"
  ok-text="确定"
  cancel-text="取消"
  :confirm-loading="confirmLoading"
  @ok="$emit('submit')"
  @cancel="$emit('cancel')"
>
  <a-form :label-col="labelCol" :wrapper-col="wrapperCol">
    <a-form-item label="模型名称" :html-for="`${idPrefix}-modelName`" required>
      <a-input :id="`${idPrefix}-modelName`" :value="formValue.modelName" @update:value="updateField('modelName', $event)" placeholder="需与 AI 搜索配置中的模型名一致，例如 GPT-5.6 Sol" />
    </a-form-item>
    <a-form-item label="显示名称" :html-for="`${idPrefix}-displayName`">
      <a-input :id="`${idPrefix}-displayName`" :value="formValue.displayName" @update:value="updateField('displayName', $event)" placeholder="留空时与模型名称相同" />
    </a-form-item>
    <a-form-item label="上下文窗口" :html-for="`${idPrefix}-contextWindowTokens`">
      <a-input :id="`${idPrefix}-contextWindowTokens`" :value="formValue.contextWindowTokens" @update:value="updateField('contextWindowTokens', $event)" inputmode="numeric" autocomplete="off" placeholder="完整 Token 数，0 表示未设置" />
      <div class="hint">填写供应商公布的上下文 Token 数，以其输入/输出预算规则为准；此项不是单次输出上限。</div>
    </a-form-item>
    <a-form-item label="输入单价" :html-for="`${idPrefix}-inputPrice`">
      <a-input-number :id="`${idPrefix}-inputPrice`" :value="formValue.inputPrice" @update:value="updateField('inputPrice', $event)" :min="0" :precision="6" :step="0.01" style="width: 100%" />
      <div class="hint">元 / 百万 token，未命中缓存的输入部分按此计价。</div>
    </a-form-item>
    <a-form-item label="缓存输入单价" :html-for="`${idPrefix}-cachedPrice`">
      <a-input-number :id="`${idPrefix}-cachedPrice`" :value="formValue.cachedPrice" @update:value="updateField('cachedPrice', $event)" :min="0" :precision="6" :step="0.01" style="width: 100%" />
      <div class="hint">元 / 百万 token，上游返回的 cached_tokens 按此计价。</div>
    </a-form-item>
    <a-form-item label="缓存写入单价" :html-for="`${idPrefix}-cacheWritePrice`">
      <a-input-number :id="`${idPrefix}-cacheWritePrice`" :value="formValue.cacheWritePrice" @update:value="updateField('cacheWritePrice', $event)" :min="0" :precision="6" :step="0.01" style="width: 100%" />
      <div class="hint">元 / 百万 token，缓存写入（cache writes）部分按此计价；上游不提供该价格时填 0。</div>
    </a-form-item>
    <a-form-item label="输出单价" :html-for="`${idPrefix}-outputPrice`">
      <a-input-number :id="`${idPrefix}-outputPrice`" :value="formValue.outputPrice" @update:value="updateField('outputPrice', $event)" :min="0" :precision="6" :step="0.01" style="width: 100%" />
      <div class="hint">元 / 百万 token。</div>
    </a-form-item>
    <a-form-item label="倍率" :html-for="`${idPrefix}-multiplier`">
      <a-input-number :id="`${idPrefix}-multiplier`" :value="formValue.multiplier" @update:value="updateField('multiplier', $event)" :min="0.0001" :precision="4" :step="0.1" style="width: 100%" />
      <div class="hint">最终扣费 =（输入 + 缓存输入 + 缓存写入 + 输出 各项金额）× 倍率；默认 1，不改单价即可整体加价或打折。</div>
    </a-form-item>
    <a-form-item label="备注" :html-for="`${idPrefix}-remark`">
      <a-input :id="`${idPrefix}-remark`" :value="formValue.remark" @update:value="updateField('remark', $event)" placeholder="备注" />
    </a-form-item>
  </a-form>
</a-modal>
</template>

<script>
export default {
  name: 'AIModelPriceEditorModal',
  props: {
    // UI identity supplied by the parent instance, never part of the price payload.
    idPrefix: { type: String, required: true },
    open: { type: Boolean, default: false },
    formValue: { type: Object, required: true },
    confirmLoading: { type: Boolean, default: false },
    labelCol: { type: Object, default: () => ({}) },
    wrapperCol: { type: Object, default: () => ({}) },
  },
  emits: ['update:open', 'update:form-value', 'submit', 'cancel'],
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
