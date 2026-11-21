<template>
  <a-modal
    :open="visible"
    title="退款收款码使用承诺书"
    :mask-closable="false"
    :keyboard="!padVisible"
    :footer="null"
    :width="720"
    wrap-class-name="refund-agreement-dialog"
    @cancel="closeAgreement"
  >
    <div class="refund-agreement" data-refund-agreement>
      <div
        ref="scrollArea"
        class="refund-agreement__text"
        tabindex="0"
        role="region"
        aria-label="退款承诺书全文，请阅读并滚动至底部"
        data-refund-agreement-text
        @scroll="onScroll"
      >
        <pre class="refund-agreement__body">{{ refundAgreementBody(text) }}</pre>
        <div class="refund-agreement__sign">
          <a-button
            v-if="!signed"
            ref="signButton"
            type="primary"
            :disabled="!canSign"
            data-refund-agreement-sign
            @click="openSignaturePad"
          >签署</a-button>
          <div v-else class="refund-agreement__signed" data-refund-agreement-signed>
            <div class="refund-agreement__applicant"><span>申请人（以电子签名方式确认）：</span><img v-if="signaturePreviewUrl" :src="signaturePreviewUrl" alt="本次退款申请的签名" class="refund-agreement__signature" /><span v-else>正在读取签名图片…</span></div>
            <p>签署日期：{{ formatTicketTime(signedAt || localSignedAt) }}</p>
          </div>
        </div>
      </div>
      <p v-if="!signed" class="refund-agreement__progress" data-refund-agreement-progress role="status">
        <span v-if="!payoutQrReady">请先上传或解锁收款码，再签署承诺书。</span>
        <span v-else-if="!scrolledToBottom">请滚动阅读至最底部<span v-if="remainingSeconds > 0">，还需阅读 {{ remainingSeconds }} 秒</span></span>
        <span v-else-if="remainingSeconds > 0">已读到底，还需阅读 {{ remainingSeconds }} 秒</span>
        <span v-else>已读完，请在协议底部签署</span>
      </p>
    </div>
  </a-modal>

  <a-modal
    v-if="padVisible"
    :open="padVisible"
    title="签署承诺书"
    :width="600"
    :mask-closable="false"
    :footer="null"
    :z-index="1100"
    :focus-trigger-after-close="false"
    wrap-class-name="refund-signature-dialog"
    @cancel="closeSignaturePad"
  >
    <div data-refund-signature>
      <p class="refund-signature__hint">请使用鼠标、触控笔或手指签写本人姓名。格线仅作书写参考。</p>
      <canvas
        ref="padCanvas"
        class="refund-signature__canvas refund-signature__canvas--grid"
        width="780"
        height="300"
        aria-label="手写签名区域，两个田字格"
        data-refund-signature-canvas
        @pointerdown.prevent="startStroke"
        @pointermove.prevent="moveStroke"
        @pointerup="endStroke"
        @pointercancel="endStroke"
        @lostpointercapture="endStroke"
      >请使用支持画布的浏览器签署。</canvas>
      <div class="refund-signature__actions">
        <a-button :disabled="uploading" data-refund-signature-clear @click="clearPad">清除</a-button>
        <a-button data-refund-signature-cancel @click="closeSignaturePad">取消</a-button>
        <a-button
          type="primary"
          :disabled="!hasStroke || !canSign"
          :loading="uploading"
          data-refund-signature-confirm
          @click="confirmSignature"
        >确认</a-button>
      </div>
    </div>
  </a-modal>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { getTicketAttachment, uploadTicketAttachment } from '@/api/TicketApi'
import { refundAgreementBody } from '@/utils/refundAgreementDisplay.mjs'
import { formatTicketTime } from '@/utils/ticketHelpers.mjs'

const props = defineProps({
  visible: { type: Boolean, default: false },
  text: { type: String, default: '' },
  // 只由本次退款草稿提供；新申请必须传回 0，不写入持久缓存。
  signedAttachmentId: { type: Number, default: 0 },
  payoutQrReady: { type: Boolean, default: false },
  signedAt: { type: String, default: '' },
})
const emit = defineEmits(['close', 'signed'])
const READ_SECONDS = 10
const remainingSeconds = ref(READ_SECONDS)
const scrolledToBottom = ref(false)
const scrollArea = ref(null)
const signButton = ref(null)
const padVisible = ref(false)
const padCanvas = ref(null)
const hasStroke = ref(false)
const uploading = ref(false)
const signaturePreviewUrl = ref('')
const localSignedAt = ref('')
let drawing = false
let lastPoint = null
let activePointer = null
let timer = null
let remainingMilliseconds = READ_SECONDS * 1000
let lastReadTick = 0
let session = 0
let signatureSession = 0
let previewRequest = 0
let previewAttachmentId = 0

const signed = computed(() => props.signedAttachmentId > 0)
const canSign = computed(() => props.visible && props.payoutQrReady && Boolean(props.text.trim()) && !signed.value && scrolledToBottom.value && remainingSeconds.value <= 0)

function clearTimer() {
  if (timer !== null) clearInterval(timer)
  timer = null
}

function startTimer() {
  clearTimer()
  remainingMilliseconds = READ_SECONDS * 1000
  remainingSeconds.value = READ_SECONDS
  lastReadTick = Date.now()
  timer = setInterval(() => {
    const now = Date.now()
    if (!document.hidden) remainingMilliseconds -= Math.max(0, now - lastReadTick)
    lastReadTick = now
    remainingSeconds.value = Math.max(0, Math.ceil(remainingMilliseconds / 1000))
    if (remainingSeconds.value === 0) clearTimer()
  }, 250)
}

function onScroll() {
  const el = scrollArea.value
  if (el && el.clientHeight > 0 && el.scrollTop + el.clientHeight >= el.scrollHeight - 8) {
    scrolledToBottom.value = true
  }
}

function clearPreview() {
  if (signaturePreviewUrl.value) URL.revokeObjectURL(signaturePreviewUrl.value)
  signaturePreviewUrl.value = ''
  previewAttachmentId = 0
}

watch(() => [props.visible, props.text], async ([visible]) => {
  const current = ++session
  ++signatureSession
  padVisible.value = false
  uploading.value = false
  endStroke()
  clearTimer()
  if (!visible) return
  scrolledToBottom.value = false
  if (!signed.value) startTimer()
  await nextTick()
  if (current !== session || !props.visible) return
  if (scrollArea.value) scrollArea.value.scrollTop = 0
  onScroll()
  if (signed.value) void loadSignaturePreview(props.signedAttachmentId)
}, { immediate: true })

watch(() => props.signedAttachmentId, id => {
  ++previewRequest
  if (!id) { clearPreview(); localSignedAt.value = '' }
  else if (props.visible) void loadSignaturePreview(id)
})

async function loadSignaturePreview(id) {
  if (previewAttachmentId === id && signaturePreviewUrl.value) return
  clearPreview()
  const current = ++previewRequest
  try {
    const response = await getTicketAttachment(id)
    if (current !== previewRequest || id !== props.signedAttachmentId) return
    const blob = response?.data
    if (blob instanceof Blob && blob.type.startsWith('image/')) {
      signaturePreviewUrl.value = URL.createObjectURL(blob)
      previewAttachmentId = id
    }
  } catch { /* 已签状态仍保留；附件预览不能替代签署记录。 */ }
}

async function openSignaturePad() {
  if (!canSign.value) return
  ++signatureSession
  padVisible.value = true
  hasStroke.value = false
  await nextTick()
  if (!padVisible.value) return
  const canvas = padCanvas.value
  if (!canvas) return
  const ratio = window.devicePixelRatio || 1
  const rect = canvas.getBoundingClientRect()
  // clientWidth 不受弹窗入场缩放动画影响，避免动画开始时导出 1px 画布。
  canvas.width = Math.max(1, Math.round((canvas.clientWidth || rect.width) * ratio))
  canvas.height = Math.max(1, Math.round((canvas.clientHeight || rect.height) * ratio))
  const ctx = canvas.getContext('2d')
  if (!ctx) { message.error('当前浏览器无法使用签名画布'); closeSignaturePad(); return }
  ctx.scale(ratio, ratio)
  ctx.lineWidth = 2.4
  ctx.lineCap = 'round'
  ctx.lineJoin = 'round'
  ctx.strokeStyle = '#1f1f1f'
}

function pointOf(event) {
  const rect = padCanvas.value.getBoundingClientRect()
  return { x: event.clientX - rect.left, y: event.clientY - rect.top }
}

function startStroke(event) {
  if (!padCanvas.value || uploading.value || (event.button !== undefined && event.button !== 0)) return
  drawing = true
  activePointer = event.pointerId
  lastPoint = pointOf(event)
  padCanvas.value.setPointerCapture?.(event.pointerId)
}

function moveStroke(event) {
  if (!drawing || !padCanvas.value || event.pointerId !== activePointer || uploading.value) return
  const ctx = padCanvas.value.getContext('2d')
  const point = pointOf(event)
  ctx.beginPath()
  ctx.moveTo(lastPoint.x, lastPoint.y)
  ctx.lineTo(point.x, point.y)
  ctx.stroke()
  if (point.x !== lastPoint.x || point.y !== lastPoint.y) hasStroke.value = true
  lastPoint = point
}

function endStroke() { drawing = false; activePointer = null; lastPoint = null }

function clearPad() {
  if (uploading.value) return
  const canvas = padCanvas.value
  if (!canvas) return
  endStroke()
  canvas.getContext('2d')?.clearRect(0, 0, canvas.width, canvas.height)
  hasStroke.value = false
}

async function confirmSignature() {
  const canvas = padCanvas.value
  if (!canSign.value || !padVisible.value || !canvas || !hasStroke.value || uploading.value) return
  const current = signatureSession
  const owned = () => current === signatureSession && props.visible && padVisible.value
  uploading.value = true
  endStroke()
  try {
    // 格子由 CSS 绘制，PNG 仅含透明背景与签名字迹。
    const blob = await new Promise(resolve => canvas.toBlob(resolve, 'image/png'))
    if (!owned()) return
    if (!(blob instanceof Blob)) throw new Error('签名导出失败')
    const response = await uploadTicketAttachment(new File([blob], 'refund-signature.png', { type: 'image/png' }))
    if (!owned()) return
    const id = Number(response?.data?.data?.id)
    if (!Number.isSafeInteger(id) || id <= 0) throw new Error('签名上传返回格式异常')
    ++previewRequest
    clearPreview()
    signaturePreviewUrl.value = URL.createObjectURL(blob)
    previewAttachmentId = id
    localSignedAt.value = new Date().toISOString()
    padVisible.value = false
    emit('signed', id, localSignedAt.value)
    emit('close')
  } catch (error) {
    if (owned()) message.error(error?.message || '签名上传失败')
  } finally {
    if (current === signatureSession) uploading.value = false
  }
}

function closeSignaturePad() {
  ++signatureSession
  padVisible.value = false
  uploading.value = false
  endStroke()
  nextTick(() => signButton.value?.$el?.focus())
}

function closeAgreement() {
  if (padVisible.value) { closeSignaturePad(); return }
  ++session
  clearTimer()
  emit('close')
}

onBeforeUnmount(() => {
  ++session
  ++signatureSession
  ++previewRequest
  clearTimer()
  clearPreview()
})
</script>

<style scoped>
.refund-agreement__text {
  max-height: 46vh;
  overflow-y: auto;
  padding: 12px 16px;
  border: 1px solid var(--console-border);
  border-radius: 8px;
  background: var(--console-surface-sunken, rgba(0, 0, 0, 0.03));
}
.refund-agreement__body {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  font: inherit;
  font-size: 14px;
  line-height: 24px;
}
.refund-agreement__progress { margin: 12px 0 0; color: var(--console-muted); font-size: 12px; }
.refund-agreement__sign { margin-top: 16px; }
.refund-agreement__signed { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; }
.refund-agreement__signature { max-width: 100%; height: 80px; border: 1px solid var(--console-border); border-radius: 6px; background: #fff; }
.refund-agreement__applicant { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.refund-agreement__signed-tip { color: var(--success, #52c41a); font-size: 13px; }
.refund-signature__hint { margin: 0 0 12px; color: var(--console-muted); }
.refund-signature__canvas {
  display: block;
  width: 100%;
  height: auto;
  aspect-ratio: 2.6;
  border: 1px solid var(--console-border);
  border-radius: 4px;
  background-color: #fff;
  touch-action: none;
  cursor: crosshair;
}
/* 两个并排的宽田字格：每格内部各有一条横线、一条竖线，独立于画布像素。 */
.refund-signature__canvas--grid {
  background-image:
    linear-gradient(to right, rgba(0, 0, 0, 0.16) 1px, transparent 1px),
    linear-gradient(to bottom, rgba(0, 0, 0, 0.16) 1px, transparent 1px),
    linear-gradient(to right, transparent calc(50% - 0.5px), rgba(0, 0, 0, 0.32) calc(50% - 0.5px), rgba(0, 0, 0, 0.32) calc(50% + 0.5px), transparent calc(50% + 0.5px));
  background-size: 25% 100%, 100% 50%, 100% 100%;
}
.refund-signature__actions { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: 8px; margin-top: 16px; }
</style>
