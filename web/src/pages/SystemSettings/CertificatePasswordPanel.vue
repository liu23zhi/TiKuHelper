<template>
  <ConsolePanel
    title="证书密码候选"
    description="网关扫描加密的 PFX / 私钥文件时，按下列候选密码依次尝试。密码明文保存在数据库中，页面只显示数量和更新时间。"
    class="settings-section"
  >
    <a-alert v-if="error" type="error" show-icon :message="error" />
    <a-form layout="vertical">
      <a-form-item label="候选密码（每行一条）" :html-for="fieldId">
        <a-textarea
          :id="fieldId"
          v-model:value="draft"
          :disabled="disabled || loading || saving"
          :rows="5"
          placeholder="每行输入一个证书密码；留空并保存可清空候选"
          data-certificate-passwords-input
        />
        <div class="hint">
          当前已保存 {{ count }} 条候选密码<span v-if="updatedAt">，更新时间：{{ formatTime(updatedAt) }}</span>。最多 32 条，单条最多 4096 字节。刷新不会回显历史密码，重新保存会覆盖旧列表。
        </div>
      </a-form-item>
      <a-form-item>
        <a-button
          type="primary"
          :loading="saving"
          :disabled="disabled || loading || saving"
          data-certificate-passwords-save
          @click="handleSave"
        >保存证书密码候选</a-button>
      </a-form-item>
    </a-form>
  </ConsolePanel>
</template>

<script setup>
import { onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { message } from 'ant-design-vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import {
  getGatewayCertificatePasswordState,
  updateGatewayCertificatePasswords,
} from '@/api/TikuComponentApi'

const props = defineProps({ disabled: { type: Boolean, default: false } })

const draft = ref('')
const count = ref(0)
const updatedAt = ref('')
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const fieldId = `gateway-certificate-passwords-${useId()}`
let active = true

function formatTime(value) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return String(value)
  return date.toLocaleString('zh-CN', { hour12: false })
}

async function loadState() {
  if (props.disabled) return
  loading.value = true
  error.value = ''
  try {
    const response = await getGatewayCertificatePasswordState({ errorFeedback: 'local' })
    if (!active) return
    if (response?.status !== 200) throw new Error(response?.data?.message || '读取证书密码候选失败')
    count.value = Number(response.data?.count || 0)
    updatedAt.value = String(response.data?.updatedAt || '')
    // The server deliberately never returns the old plaintext values.
    draft.value = ''
  } catch (err) {
    if (active) error.value = err?.response?.data?.message || err?.message || '读取证书密码候选失败'
  } finally {
    if (active) loading.value = false
  }
}

async function handleSave() {
  if (saving.value || props.disabled) return
  saving.value = true
  error.value = ''
  try {
    const passwords = String(draft.value || '').split(/\n/).map(line => line.replace(/\r$/, ''))
    const response = await updateGatewayCertificatePasswords(passwords, { errorFeedback: 'local' })
    if (!active) return
    if (response?.status !== 200) throw new Error(response?.data?.message || '保存证书密码候选失败')
    count.value = Number(response.data?.count || 0)
    updatedAt.value = String(response.data?.updatedAt || '')
    draft.value = ''
    message.success('证书密码候选已保存，历史密码不会回显')
  } catch (err) {
    if (active) error.value = err?.response?.data?.message || err?.message || '保存证书密码候选失败'
  } finally {
    if (active) saving.value = false
  }
}

onMounted(loadState)
watch(() => props.disabled, disabled => { if (!disabled) void loadState() })
onBeforeUnmount(() => { active = false })
</script>
