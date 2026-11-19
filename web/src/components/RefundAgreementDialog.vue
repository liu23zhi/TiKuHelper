<template>
  <a-modal
    :visible="visible"
    title="退款收款码使用承诺书"
    :mask-closable="false"
    :footer="null"
    width="720px"
    wrap-class-name="refund-agreement-dialog"
    @cancel="closeAgreement"
  >
    <div class="refund-agreement" data-refund-agreement>
      <!-- 阅读区：必须滚到底才允许签署。
           用原生 div + scroll 事件而不是 a-xxx，避免组件内部不冒泡的老问题。 -->
      <div
        ref="scrollArea"
        class="refund-agreement__text"
        data-refund-agreement-text
        @scroll="onScroll"
      >
        <pre class="refund-agreement__body">{{ text }}</pre>
      </div>

      <p class="refund-agreement__progress" data-refund-agreement-progress>
        <span v-if="!scrolledToBottom">请向下滚动阅读至最底部（仍需读完 {{ remainingSeconds }} 秒）</span>
        <span v-else-if="remainingSeconds > 0">已读到底，还需等待 {{ remainingSeconds }} 秒</span>
        <span v-else>已读完，可以签署</span>
      </p>

      <!-- 签署区：未签署显示按钮；已签署显示签名图。
           「再次打开协议弹窗，原先底下签署的位置应显示用户的签名」。 -->
      <div class="refund-agreement__sign">
        <a-button
          v-if="!signed"
          type="primary"
          :disabled="!canSign"
          data-refund-agreement-sign
          @click="openSignaturePad"
        >签署</a-button>
        <div v-else class="refund-agreement__signed" data-refund-agreement-signed>
          <img v-if="signaturePreviewUrl" :src="signaturePreviewUrl" alt="签名" class="refund-agreement__signature" />
          <span class="refund-agreement__signed-tip">已签署</span>
        </div>
      </div>
    </div>

    <!-- 签名弹窗：独立一层，关闭它**不能**关掉协议弹窗。
         所以它不用 a-modal（那会带自己的遮罩与层级），用一个内联浮层。 -->
    <div v-if="padVisible" class="refund-signature" data-refund-signature>
      <div class="refund-signature__panel">
        <p class="refund-signature__title">请在格内签名</p>
        <!-- 田字格只是背景（CSS），笔画画在 canvas 上。
             签署完成后去掉 --grid 类，格子消失、笔画保留。 -->
        <canvas
          ref="padCanvas"
          class="refund-signature__canvas"
          :class="{ 'refund-signature__canvas--grid': !padSigned }"
          data-refund-signature-canvas
          @mousedown="startStroke"
          @mousemove="moveStroke"
          @mouseup="endStroke"
          @mouseleave="endStroke"
          @touchstart.prevent="startStrokeTouch"
          @touchmove.prevent="moveStrokeTouch"
          @touchend.prevent="endStroke"
        ></canvas>
        <div class="refund-signature__actions">
          <a-button size="small" data-refund-signature-clear @click="clearPad">清除</a-button>
          <a-button
            type="primary"
            size="small"
            :disabled="!hasStroke"
            data-refund-signature-confirm
            @click="confirmSignature"
          >确认</a-button>
        </div>
      </div>
    </div>
  </a-modal>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { uploadTicketAttachment } from '@/api/TicketApi'

const props = defineProps({
  visible: { type: Boolean, default: false },
  text: { type: String, default: '' },
  // 已签署过的附件 ID（同一份退款草稿内重开时回显签名）
  signedAttachmentId: { type: Number, default: 0 },
})
const emit = defineEmits(['close', 'signed'])

// 必须读完 10 秒才允许签署（用户要求）。
const READ_SECONDS = 10
const remainingSeconds = ref(READ_SECONDS)
const scrolledToBottom = ref(false)
const scrollArea = ref(null)

const padVisible = ref(false)
const padCanvas = ref(null)
const padSigned = ref(false)
const hasStroke = ref(false)
const signaturePreviewUrl = ref('')
let drawing = false
let lastPoint = null
let timer = null
let uploading = false

const signed = computed(() => props.signedAttachmentId > 0)
const canSign = computed(() => scrolledToBottom.value && remainingSeconds.value <= 0)

function clearTimer() {
  if (timer) { clearInterval(timer); timer = null }
}

function startTimer() {
  clearTimer()
  remainingSeconds.value = READ_SECONDS
  timer = setInterval(() => {
    if (remainingSeconds.value > 0) remainingSeconds.value -= 1
    if (remainingSeconds.value <= 0) clearTimer()
  }, 1000)
}

// onScroll：滚到底才算读完。留 8px 容差 —— 不同 DPI 下 scrollHeight 与
// clientHeight 的取整可能差 1px，卡死会让用户永远签不了。
function onScroll() {
  const el = scrollArea.value
  if (!el) return
  const reached = el.scrollTop + el.clientHeight >= el.scrollHeight - 8
  if (reached) scrolledToBottom.value = true
}

// 打开时重置阅读进度（每次都要重新读、重新签）。
watch(() => props.visible, async visible => {
  if (!visible) { clearTimer(); padVisible.value = false; return }
  scrolledToBottom.value = false
  startTimer()
  await nextTick()
  // 文本短到不需要滚动时（管理员把协议改得很短），直接视为已读完，
  // 否则用户会卡在一个「永远滚不到底」的死局里。
  const el = scrollArea.value
  if (el && el.scrollHeight <= el.clientHeight + 8) scrolledToBottom.value = true
  if (props.signedAttachmentId > 0) void loadSignaturePreview(props.signedAttachmentId)
})

async function loadSignaturePreview(id) {
  try {
    const { getTicketAttachment } = await import('@/api/TicketApi')
    const response = await getTicketAttachment(id)
    const blob = response?.data
    if (blob instanceof Blob && blob.type.startsWith('image/')) {
      if (signaturePreviewUrl.value) URL.revokeObjectURL(signaturePreviewUrl.value)
      signaturePreviewUrl.value = URL.createObjectURL(blob)
    }
  } catch { /* 读不回来就不显示预览，签署状态仍在 */ }
}

function openSignaturePad() {
  if (!canSign.value) return
  padVisible.value = true
  padSigned.value = false
  hasStroke.value = false
  nextTick(() => initPad())
}

// initPad 用设备像素比放大画布，否则高分屏上笔画会糊成一片。
function initPad() {
  const canvas = padCanvas.value
  if (!canvas) return
  const ratio = window.devicePixelRatio || 1
  const rect = canvas.getBoundingClientRect()
  canvas.width = Math.max(1, Math.round(rect.width * ratio))
  canvas.height = Math.max(1, Math.round(rect.height * ratio))
  const ctx = canvas.getContext('2d')
  ctx.scale(ratio, ratio)
  ctx.lineWidth = 2.4
  ctx.lineCap = 'round'
  ctx.lineJoin = 'round'
  ctx.strokeStyle = '#1f1f1f'
  hasStroke.value = false
  padSigned.value = false
}

function pointOf(event) {
  const canvas = padCanvas.value
  const rect = canvas.getBoundingClientRect()
  return { x: event.clientX - rect.left, y: event.clientY - rect.top }
}

function startStroke(event) {
  if (!padCanvas.value || padSigned.value) return
  drawing = true
  lastPoint = pointOf(event)
}

function moveStroke(event) {
  if (!drawing || !padCanvas.value) return
  const ctx = padCanvas.value.getContext('2d')
  const point = pointOf(event)
  ctx.beginPath()
  ctx.moveTo(lastPoint.x, lastPoint.y)
  ctx.lineTo(point.x, point.y)
  ctx.stroke()
  lastPoint = point
  hasStroke.value = true
}

function endStroke() { drawing = false }

function startStrokeTouch(event) {
  const touch = event.touches?.[0]
  if (touch) startStroke(touch)
}
function moveStrokeTouch(event) {
  const touch = event.touches?.[0]
  if (touch) moveStroke(touch)
}

function clearPad() {
  const canvas = padCanvas.value
  if (!canvas) return
  canvas.getContext('2d').clearRect(0, 0, canvas.width, canvas.height)
  hasStroke.value = false
  padSigned.value = false
}

// confirmSignature：把画布转成 PNG 上传，拿到附件 ID 后回传父组件。
async function confirmSignature() {
  const canvas = padCanvas.value
  if (!canvas || !hasStroke.value || uploading) return
  uploading = true
  try {
    // 去掉田字格：格子是 CSS 背景，不在 canvas 像素里，
    // 所以这里导出的本来就是「只有笔画、没有格子」的图。
    const blob = await new Promise(resolve => canvas.toBlob(resolve, 'image/png'))
    if (!(blob instanceof Blob)) { message.error('签名导出失败'); return }
    const file = new File([blob], 'refund-signature.png', { type: 'image/png' })
    const response = await uploadTicketAttachment(file, undefined)
    const id = Number(response?.data?.data?.id)
    if (!Number.isSafeInteger(id) || id <= 0) { message.error('签名上传返回格式异常'); return }
    if (signaturePreviewUrl.value) URL.revokeObjectURL(signaturePreviewUrl.value)
    signaturePreviewUrl.value = URL.createObjectURL(blob)
    padSigned.value = true
    // 签完先让格子消失（保留笔画），再关掉签名弹窗与协议弹窗。
    padVisible.value = false
    emit('signed', id)
    emit('close')
  } catch (error) {
    message.error(error?.message || '签名上传失败')
  } finally {
    uploading = false
  }
}

// closeAgreement：只关协议弹窗本身。签名弹窗有自己的关闭路径，
// 用户关闭签名弹窗时**不能**连带关掉协议弹窗（用户明确要求）。
function closeAgreement() {
  clearTimer()
  padVisible.value = false
  emit('close')
}

onBeforeUnmount(() => {
  clearTimer()
  if (signaturePreviewUrl.value) URL.revokeObjectURL(signaturePreviewUrl.value)
})
</script>

<style scoped>
.refund-agreement__text {
  max-height: 46vh;
  overflow-y: auto;
  padding: 12px 14px;
  border: 1px solid var(--console-border);
  border-radius: 8px;
  background: var(--console-surface-sunken, rgba(0, 0, 0, 0.03));
}
.refund-agreement__body {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  font: inherit;
  font-size: 13px;
  line-height: 22px;
}
.refund-agreement__progress {
  margin: 10px 0 0;
  color: var(--console-muted);
  font-size: 12px;
}
.refund-agreement__sign {
  margin-top: 12px;
}
.refund-agreement__signed {
  display: flex;
  align-items: center;
  gap: 10px;
}
.refund-agreement__signature {
  height: 72px;
  border: 1px solid var(--console-border);
  border-radius: 6px;
  background: #fff;
}
.refund-agreement__signed-tip {
  color: var(--success, #52c41a);
  font-size: 13px;
}
.refund-signature {
  position: fixed;
  inset: 0;
  z-index: 1200;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.45);
}
.refund-signature__panel {
  width: 520px;
  max-width: 92vw;
  padding: 16px;
  border-radius: 10px;
  background: var(--console-surface, #fff);
}
.refund-signature__title {
  margin: 0 0 10px;
  font-weight: 600;
}
.refund-signature__canvas {
  display: block;
  width: 100%;
  height: 200px;
  border: 1px solid var(--console-border);
  border-radius: 6px;
  background: #fff;
  touch-action: none;
  cursor: crosshair;
}
/* 田字格背景：只在**签署前**显示。
   用户要求「田字格只是背景，前面完成之后不要显示田字格」——
   格子是 CSS 画的，确认签名后去掉这个类即可，笔画像素不受影响。 */
.refund-signature__canvas--grid {
  background-image:
    linear-gradient(to right, rgba(0, 0, 0, 0.12) 1px, transparent 1px),
    linear-gradient(to bottom, rgba(0, 0, 0, 0.12) 1px, transparent 1px),
    linear-gradient(to right, rgba(0, 0, 0, 0.26) 1px, transparent 1px),
    linear-gradient(to bottom, rgba(0, 0, 0, 0.26) 1px, transparent 1px);
  background-size:
    50% 50%,
    50% 50%,
    100% 100%,
    100% 100%;
}
.refund-signature__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 12px;
}
</style>