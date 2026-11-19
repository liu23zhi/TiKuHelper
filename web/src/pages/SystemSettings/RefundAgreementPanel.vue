<template>
  <ConsolePanel
    title="退款承诺书"
    description="用户申请人工退款时必须签署的协议全文。修改后立即对下一次签署生效。"
    class="page-card"
  >
    <template #actions>
      <a-space>
        <a-tag v-if="isDefault" color="default" data-refund-agreement-default>当前为内置默认</a-tag>
        <a-tag v-else color="blue" data-refund-agreement-custom>已自定义</a-tag>
        <a-button size="small" :disabled="disabled || loading || saving" @click="handleLoad">重新读取</a-button>
      </a-space>
    </template>

    <a-alert v-if="error" type="error" show-icon :message="error" />

    <a-form layout="vertical">
      <a-form-item
        label="协议全文"
        :html-for="textId"
        :help="`${textLength} / ${maxRunes} 字。留空或超长会被服务端拒绝。`"
        data-refund-agreement-field
      >
        <a-textarea
          :id="textId"
          :value="text"
          :rows="16"
          :maxlength="maxRunes"
          show-count
          :disabled="disabled || loading || saving"
          placeholder="请输入承诺书全文"
          data-refund-agreement-textarea
          @update:value="value => (text = value)"
        />
      </a-form-item>
      <a-form-item>
        <a-space>
          <a-button
            type="primary"
            :loading="saving"
            :disabled="!canSave"
            data-refund-agreement-save
            @click="handleSave"
          >保存协议</a-button>
          <a-button
            :disabled="disabled || loading || saving || !defaultText || text === defaultText"
            data-refund-agreement-restore
            @click="restoreDefault"
          >恢复内置默认</a-button>
        </a-space>
      </a-form-item>
    </a-form>
  </ConsolePanel>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { message } from 'ant-design-vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import { getRefundAgreement, updateRefundAgreement } from '@/api/TikuComponentApi'

const props = defineProps({
  disabled: { type: Boolean, default: false },
})

const text = ref('')
const defaultText = ref('')
const maxRunes = ref(20000)
const isDefault = ref(true)
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const loaded = ref(false)
const textId = `refund-agreement-${useId()}`
let active = true

const textLength = computed(() => Array.from(text.value || '').length)
const canSave = computed(() => loaded.value && !props.disabled && !loading.value && !saving.value && Boolean(text.value.trim()) && textLength.value <= maxRunes.value)

async function handleLoad() {
  if (loading.value || saving.value || props.disabled) return
  loading.value = true
  error.value = ''
  try {
    const response = await getRefundAgreement()
    if (!active) return
    if (response?.status !== 200) throw new Error(response?.data?.message || '读取协议失败')
    const data = response?.data || {}
    text.value = String(data.text || '')
    defaultText.value = String(data.defaultText || '')
    maxRunes.value = Number(data.maxRunes) || 20000
    isDefault.value = data.isDefault === true
    loaded.value = true
  } catch (err) {
    if (!active) return
    error.value = err?.response?.data?.message || err?.message || '读取协议失败'
  } finally {
    if (active) loading.value = false
  }
}

async function handleSave() {
  if (!canSave.value) return
  saving.value = true
  error.value = ''
  try {
    const response = await updateRefundAgreement(text.value)
    if (!active) return
    if (response?.status === 200) {
      text.value = String(response.data?.text || text.value)
      isDefault.value = response.data?.isDefault === true
      message.success('协议已更新')
      return
    }
    error.value = response?.data?.message || '保存协议失败'
  } catch (err) {
    if (!active) return
    error.value = err?.response?.data?.message || err?.message || '保存协议失败'
  } finally {
    if (active) saving.value = false
  }
}

// restoreDefault 把内置默认文本填回输入框（不直接保存）。
//
// 为什么不直接保存：管理员可能只是想看看默认措辞，直接落库会覆盖他刚写的版本。
// 填进输入框后再由他点「保存」，动作是显式的。
function restoreDefault() {
  if (!defaultText.value || props.disabled || loading.value || saving.value) return
  text.value = defaultText.value
  message.info('已填入内置默认文本，点「保存协议」后生效')
}

onMounted(handleLoad)
watch(() => props.disabled, disabled => { if (!disabled && !loaded.value) void handleLoad() })
onBeforeUnmount(() => { active = false })
</script>
