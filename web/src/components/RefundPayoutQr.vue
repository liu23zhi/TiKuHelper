<template>
  <div class="refund-payout" data-ticket-payout-qr :aria-busy="busy">
    <div class="refund-payout__frame">
      <button type="button" class="refund-payout__tile" :disabled="disabled || busy"
        :aria-label="error ? '重试加载收款码' : imageDataUrl ? '替换收款码' : hasPrevious ? '解锁收款码' : '上传收款码'"
        data-ticket-payout-qr-tile @click="activate">
        <img v-if="imageDataUrl" :src="imageDataUrl" alt="收款码" class="refund-payout__image" data-ticket-payout-qr-preview />
        <template v-else-if="hasPrevious">
          <img v-if="previewDataUrl" :src="previewDataUrl" alt="已锁定的收款码（模糊预览）" class="refund-payout__image refund-payout__blurred" data-ticket-payout-qr-blurred />
          <svg class="refund-payout__icon refund-payout__lock" viewBox="0 0 1024 1024" aria-hidden="true" data-ticket-payout-qr-lock>
            <path d="M402.57 905.35H176.1c-61.9-0.27-111.97-50.46-112.1-112.36V231.23c0-61.91 49.98-112.2 111.88-112.58H409.1l34.46 56.41H169.33c-29.14 1.89-51.25 27.03-49.39 56.18v499.93l190.52-230.48c13.82-18.55 40.07-22.38 58.62-8.56 3.25 2.42 6.14 5.31 8.56 8.56l133.62 172.03 27.17 34.87-0.08 0.1 21.6 42.97-157.38 154.69z m445.55 0H544.47L654.99 702.4l-51.58-57.98 92.57-92.99a44.234 44.234 0 0 1 34.86-16.85c1.17 0 2.3 0 3.38 0.13a44.414 44.414 0 0 1 35.25 22.34l134.59 172.03V231.23c-2.13-30.01-25.94-53.92-55.94-56.18h-333.5l-14.59-56.41h348.08c61.91 0.4 111.89 50.69 111.89 112.59v561.75c-0.13 61.82-50.07 111.97-111.88 112.37zM651.72 456.3c-46.67-0.03-84.48-37.88-84.45-84.54 0.03-46.67 37.88-84.48 84.54-84.45 46.67 0.03 84.48 37.88 84.45 84.54a84.509 84.509 0 0 1-24.83 59.79 83.99 83.99 0 0 1-59.69 24.65l-0.02 0.01z" fill="currentColor" />
            <path d="M472 486v-34a40 40 0 0 1 80 0v34" fill="none" stroke="currentColor" stroke-width="48" stroke-linecap="round" />
            <path d="M472 486v-34a40 40 0 0 1 80 0v34" fill="none" stroke="#fff" stroke-width="26" stroke-linecap="round" />
            <rect x="437" y="492" width="150" height="120" rx="22" fill="#fff" stroke="currentColor" stroke-width="20" />
            <circle cx="512" cy="542" r="13" fill="currentColor" /><rect x="506" y="546" width="12" height="28" rx="6" fill="currentColor" />
          </svg>
        </template>
        <svg v-else class="refund-payout__icon" viewBox="0 0 1024 1024" aria-hidden="true" data-ticket-payout-qr-add>
          <path d="M716.8 609.6l-112-84a48.224 48.224 0 0 0-57.6 0l-98.56 73.92-131.424-105.12a47.776 47.776 0 0 0-56.832-2.4L128 581.024V224h704v352a32 32 0 0 0 64 0V221.312C896 187.488 868.48 160 834.688 160H125.312C91.488 160 64 187.52 64 221.312v581.376C64 836.512 91.52 864 125.312 864H608a32 32 0 0 0 0-64H128v-141.856l158.368-106.464 161.024 128.8L576 584l102.4 76.8a32 32 0 0 0 38.4-51.2zM576 384c0 52.928 43.072 96 96 96s96-43.072 96-96-43.072-96-96-96-96 43.072-96 96z m128 0c0 17.632-14.368 32-32 32-17.632 0-32-14.368-32-32 0-17.632 14.368-32 32-32 17.632 0 32 14.368 32 32z m224 384h-64v-64a32 32 0 0 0-64 0v64h-64a32 32 0 0 0 0 64h64v64a32 32 0 0 0 64 0v-64h64a32 32 0 0 0 0-64z" fill="currentColor" />
        </svg>
        <span v-if="busy" class="refund-payout__loading" role="status">正在处理…</span>
      </button>
      <button v-if="hasPrevious || imageDataUrl" type="button" class="refund-payout__remove" aria-label="删除收款码" title="删除收款码"
        :disabled="disabled || busy" data-ticket-payout-qr-delete @click="remove">×</button>
    </div>
    <input ref="fileInput" class="refund-payout__input" type="file" :accept="TICKET_ATTACHMENT_ACCEPT" :disabled="disabled || busy" hidden aria-hidden="true" style="display: none !important" tabindex="-1" aria-label="上传或替换收款码" data-ticket-payout-qr-input @change="pick" />
    <p v-if="error" class="refund-payout__error" role="alert">{{ error }}，点击方框重试。</p>
    <p v-else class="refund-payout__hint">{{ busy ? '正在读取或保存收款码' : imageDataUrl ? '点击图片可替换收款码' : hasPrevious ? '收款码已锁定，点击图片并输入登录密码解锁' : '点击方框上传收款码（PNG / JPEG，最大 2 MiB）' }}</p>
    <a-button v-if="hasPrevious && !imageDataUrl" type="link" size="small" :disabled="disabled || busy" data-ticket-payout-qr-replace @click="chooseFile">直接替换收款码</a-button>
    <a-modal :open="unlockVisible" title="解锁收款码" :confirm-loading="busy" :mask-closable="false" :ok-button-props="{ disabled: !password || busy }" @cancel="closeUnlock" @ok="unlock">
      <p>请输入当前登录密码，以查看之前上传的收款码。</p>
      <a-input-password v-model:value="password" autocomplete="current-password" aria-label="当前登录密码" :disabled="busy" @pressEnter="unlock" />
    </a-modal>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { uploadTicketAttachment } from '@/api/TicketApi'
import { getRefundPayoutQrStatus, registerRefundPayoutQr, challengeRefundPayoutQr, unlockRefundPayoutQrDevice, recoverTicketRefundPayoutQr, deleteRefundPayoutQr } from '@/api/TikuComponentApi'
import { TICKET_ATTACHMENT_ACCEPT, validateTicketAttachmentFile } from '@/utils/ticketAttachmentHelpers.mjs'
import { createRefundPayoutDeviceKey, loadRefundPayoutDeviceKey, saveRefundPayoutDeviceKey, clearRefundPayoutDeviceKey, signRefundPayoutChallenge, base64ToBytes } from '@/utils/refundPayoutDevice.mjs'

const props = defineProps({ userId: { type: Number, required: true }, disabled: Boolean })
const emit = defineEmits(['update:attachmentId', 'busy', 'changed'])
const fileInput = ref(null), busy = ref(false), hasPrevious = ref(false), imageDataUrl = ref(''), previewDataUrl = ref(''), error = ref('')
const unlockVisible = ref(false), password = ref(''), attachmentId = ref(null)
const ready = computed(() => Boolean(imageDataUrl.value && attachmentId.value))
let version = 0, alive = true
const owns = current => alive && current === version
const failure = (err, fallback) => err?.response?.data?.message || err?.message || fallback
function setBusy(value) { busy.value = value; emit('busy', value) }
function setImage(id, url) {
  if (!Number.isSafeInteger(Number(id)) || Number(id) <= 0 || !/^data:image\/(png|jpeg);base64,/.test(url || '')) throw new Error('收款码图片数据无效')
  attachmentId.value = Number(id); imageDataUrl.value = url; emit('update:attachmentId', Number(id))
}
function clearImage() { attachmentId.value = null; imageDataUrl.value = ''; emit('update:attachmentId', null) }
async function load() {
  const current = ++version
  clearImage(); hasPrevious.value = false; previewDataUrl.value = ''; error.value = ''; setBusy(true)
  try {
    const { data } = await getRefundPayoutQrStatus()
    if (!owns(current)) return
    if (typeof data?.hasPrevious !== 'boolean') throw new Error('收款码状态无效')
    hasPrevious.value = data.hasPrevious
    // An earlier empty status response must not erase a key just registered by
    // another tab. Stale keys cannot unlock a different server-side key ID.
    if (!data.hasPrevious) { loadRefundPayoutDeviceKey(props.userId, ''); return }
    if (/^data:image\/png;base64,/.test(data.previewDataUrl || '')) previewDataUrl.value = data.previewDataUrl
    const privateKey = loadRefundPayoutDeviceKey(props.userId, data.keyId)
    if (privateKey) {
      try {
        const challenge = (await challengeRefundPayoutQr()).data
        if (!owns(current)) return
        const signature = await signRefundPayoutChallenge(privateKey, challenge.challenge)
        if (!owns(current)) return
        const result = (await unlockRefundPayoutQrDevice({ challengeId: challenge.challengeId, signature })).data
        if (owns(current)) setImage(result.attachmentId, result.imageDataUrl)
      } catch { /* A stale/missing device key leaves the image locked for password recovery. */ }
    }
  } catch (err) { if (owns(current)) error.value = failure(err, '读取收款码失败') }
  finally { if (owns(current)) setBusy(false) }
}
function chooseFile() { if (!props.disabled && !busy.value) fileInput.value?.click() }
function activate() {
  if (error.value) { void load(); return }
  if (hasPrevious.value && !ready.value) { password.value = ''; unlockVisible.value = true; return }
  chooseFile()
}
function readFile(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result || ''))
    reader.onerror = () => reject(new Error('无法读取收款码图片'))
    reader.readAsDataURL(file)
  })
}
async function pick(event) {
  const input = event?.target, file = input?.files?.[0]
  if (input) input.value = ''
  if (!file || props.disabled || busy.value) return
  const invalid = validateTicketAttachmentFile(file)
  if (invalid) { message.warning(`收款码：${invalid}`); return }
  const current = ++version, userId = props.userId
  setBusy(true)
  try {
    const key = await createRefundPayoutDeviceKey()
    let url = await readFile(file)
    if (!owns(current)) return
    const result = (await uploadTicketAttachment(file)).data?.data
    if (!owns(current)) return
    const type = result?.mediaType || (/\.png$/i.test(file.name) ? 'image/png' : 'image/jpeg')
    if (!/^image\/(png|jpeg)$/.test(type)) throw new Error('收款码图片格式无效')
    url = url.replace(/^data:[^;]*;base64,/, `data:${type};base64,`)
    const registered = (await registerRefundPayoutQr({ attachmentId: result.id, publicKey: key.publicKey })).data
    const saved = saveRefundPayoutDeviceKey(userId, registered.keyId, key.privateKey)
    if (!owns(current)) return
    setImage(registered.attachmentId, url); hasPrevious.value = true; previewDataUrl.value = ''; error.value = ''
    emit('changed')
    if (!saved) message.info('收款码已保存；本机存储不可用，下次查看需输入登录密码')
  } catch (err) { if (owns(current)) message.error(failure(err, '收款码上传失败')) }
  finally { if (owns(current)) setBusy(false) }
}
function closeUnlock() { if (busy.value) return; unlockVisible.value = false; password.value = '' }
async function unlock() {
  if (!unlockVisible.value || busy.value || !password.value || props.disabled) return
  const current = ++version, secret = password.value
  password.value = ''; setBusy(true)
  try {
    const result = (await recoverTicketRefundPayoutQr(secret)).data
    if (!owns(current)) return
    setImage(result.attachmentId, result.imageDataUrl); unlockVisible.value = false
    // Password recovery does not turn this device into the original uploading device.
  } catch (err) { if (owns(current)) message.error(failure(err, '解锁收款码失败')) }
  finally { if (owns(current)) setBusy(false) }
}
async function remove() {
  if (busy.value || props.disabled) return
  const current = ++version
  setBusy(true)
  try {
    await deleteRefundPayoutQr()
    if (!owns(current)) return
    clearRefundPayoutDeviceKey(props.userId); clearImage(); hasPrevious.value = false; previewDataUrl.value = ''; error.value = ''
    unlockVisible.value = false; password.value = ''; emit('changed')
  } catch (err) { if (owns(current)) message.error(failure(err, '删除收款码失败')) }
  finally { if (owns(current)) setBusy(false) }
}
async function ensureDraft() {
  if (!ready.value || busy.value) throw new Error('请上传或解锁收款码')
  const current = version, match = /^data:(image\/(?:png|jpeg));base64,(.+)$/.exec(imageDataUrl.value)
  if (!match) throw new Error('请重新上传收款码')
  // The remembered original stays separate from each application's immutable evidence.
  const result = (await uploadTicketAttachment(new File([base64ToBytes(match[2])], 'refund-payout-qr.png', { type: match[1] }))).data?.data
  if (!owns(current)) return null
  const id = Number(result?.id)
  if (!Number.isSafeInteger(id) || id <= 0) throw new Error('收款码上传返回格式异常')
  emit('update:attachmentId', id)
  return id
}
watch(() => props.userId, () => { unlockVisible.value = false; password.value = ''; void load() }, { immediate: true })
onBeforeUnmount(() => { alive = false; ++version; password.value = ''; imageDataUrl.value = ''; emit('busy', false) })
defineExpose({ ensureDraft })
</script>

<style scoped>
.refund-payout__frame { position: relative; width: 164px; max-width: 100%; }
.refund-payout__tile { position: relative; display: flex; align-items: center; justify-content: center; overflow: hidden; width: 100%; aspect-ratio: 1; padding: 12px; color: var(--console-text, #333); background: var(--console-surface, #fff); border: 1px dashed var(--console-border, #ccc); border-radius: 8px; cursor: pointer; }
.refund-payout__tile:hover, .refund-payout__tile:focus-visible { border-color: var(--primary, #1677ff); outline-offset: 3px; }
.refund-payout__tile:disabled { cursor: wait; opacity: .65; }
.refund-payout__image { width: 100%; height: 100%; object-fit: contain; }
.refund-payout__blurred { position: absolute; inset: 0; opacity: .25; }
.refund-payout__icon { width: 80px; height: 80px; position: relative; }
.refund-payout__lock { width: 90px; height: 90px; }
.refund-payout__remove { position: absolute; left: -9px; top: -9px; width: 26px; height: 26px; border-radius: 50%; border: 1px solid #fff; background: #d9363e; color: #fff; font: 22px/20px sans-serif; cursor: pointer; opacity: 0; }
.refund-payout__frame:hover .refund-payout__remove, .refund-payout__frame:focus-within .refund-payout__remove { opacity: 1; }
.refund-payout__input { display: none; }
.refund-payout__hint, .refund-payout__error { margin: 8px 0 0; color: var(--console-muted); font-size: 12px; }
.refund-payout__error { color: var(--danger, #d9363e); }
.refund-payout__loading { position: absolute; inset: 0; display: grid; place-items: center; background: var(--console-surface, #fff); font-size: 13px; }
@media (hover: none) { .refund-payout__remove { opacity: 1; } }
</style>
