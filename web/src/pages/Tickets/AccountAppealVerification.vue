<template>
  <a-modal
    :open="open"
    title="为哪个账号申诉？"
    :footer="null"
    @cancel="close"
  >
    <a-alert v-if="error" class="verification-error" type="error" :message="error" show-icon />
    <a-form layout="vertical" :model="form" @finish="verify">
      <a-form-item label="要申诉的账号" name="username">
        <a-input v-model:value="form.username" autocomplete="off" :disabled="busy" />
      </a-form-item>
      <a-form-item label="该账号的密码" name="password">
        <a-input-password v-model:value="form.password" autocomplete="off" :disabled="busy" />
      </a-form-item>
      <a-space>
        <a-button @click="close">取消</a-button>
        <a-button type="primary" html-type="submit" :loading="busy" :disabled="!form.username.trim() || !form.password">
          验证账号
        </a-button>
      </a-space>
    </a-form>
  </a-modal>
</template>

<script setup>
import { onBeforeUnmount, reactive, ref, watch } from 'vue'
import { verifyTicketAppealAccount } from '@/api/TicketApi'
import { getCurrentSessionRevision, getCurrentUserLogoutEpoch, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'

const props = defineProps({ open: { type: Boolean, default: false } })
const emit = defineEmits(['update:open', 'verified'])
const { currentUser } = useCurrentUserDisplay()
const form = reactive({ username: '', password: '' })
const busy = ref(false)
const error = ref('')
let activeRequest = null

function clear() {
  activeRequest?.abort()
  activeRequest = null
  form.username = ''
  form.password = ''
  busy.value = false
  error.value = ''
}

function close() {
  clear()
  emit('update:open', false)
}

watch([
  () => props.open,
  () => currentUserIdentity(currentUser.value),
  () => currentUser.value?.status,
  getCurrentUserLogoutEpoch,
], clear, { flush: 'sync' })
onBeforeUnmount(clear)

async function verify() {
  const username = form.username.trim()
  if (!props.open || busy.value || !username || !form.password) return
  const request = new AbortController()
  const sessionRevision = getCurrentSessionRevision()
  const owns = () => activeRequest === request && sessionRevision === getCurrentSessionRevision()
  activeRequest = request
  busy.value = true
  error.value = ''
  try {
    const pending = verifyTicketAppealAccount({ username, password: form.password }, request.signal)
    form.password = ''
    const result = await pending
    if (!owns()) return
    const data = result.data?.data
    if (!data?.proof) throw new Error('账号验证结果不完整，请重试')
    const characters = Array.from(username)
    const maskedUsername = characters.length > 2 ? `${characters[0]}***${characters.at(-1)}` : '***'
    emit('verified', {
      proof: data.proof,
      expiresAt: data.expiresAt,
      registeredAt: data.registeredAt,
      maskedPhone: data.maskedPhone,
      username: maskedUsername,
    })
    close()
  } catch (failure) {
    if (owns()) {
      error.value = failure?.data?.message || failure?.message || '账号验证失败，请重试'
    }
  } finally {
    if (activeRequest === request) {
      activeRequest = null
      busy.value = false
      form.password = ''
    }
  }
}
</script>

<style scoped>
.verification-error {
  margin-bottom: 16px;
}
</style>
