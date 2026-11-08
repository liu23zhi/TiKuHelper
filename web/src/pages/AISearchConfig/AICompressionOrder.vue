<template>
  <div :id="`${controlPrefix}-group`" class="compression-order-group" role="group" :aria-labelledby="labelledBy || `${controlPrefix}-label`">
    <span v-if="!labelledBy" :id="`${controlPrefix}-label`">压缩执行顺序</span>
    <ol class="compression-order" aria-label="压缩执行顺序">
      <li
        v-for="(strategy, index) in modelValue"
        :key="strategy"
        :data-strategy="strategy"
        class="compression-order-row"
        :class="{ 'is-dragging': dragging === strategy, 'is-drop-target': dropTarget === strategy && dragging !== strategy }"
        :draggable="!disabled"
        @dragstart="startDrag($event, strategy)"
        @dragover.prevent="dragOver($event, strategy)"
        @drop.prevent="drop(strategy)"
        @dragend="clearDrag"
      >
        <button
          :id="handleId(strategy)"
          type="button"
          class="compression-order-handle"
          :disabled="disabled"
          :aria-label="`拖动${strategyText(strategy)}调整顺序，也可使用方向键`"
          title="拖动调整顺序"
          @click="$event.currentTarget.focus()"
          @pointerdown="startPointerDrag($event, strategy)"
          @pointermove="pointerMove"
          @pointerup="finishPointerDrag"
          @pointercancel="clearDrag"
          @keydown.up.prevent="move(index, index - 1, $event)"
          @keydown.down.prevent="move(index, index + 1, $event)"
        >{{ index + 1 }}</button>
        <span class="compression-order-content">
          <strong>{{ strategyText(strategy) }}</strong>
          <span>{{ strategyDescription(strategy) }}</span>
        </span>
        <span class="compression-order-actions">
          <a-button :id="`${controlPrefix}-${strategy}-up`" size="small" :disabled="disabled || index === 0" :aria-label="`${strategyText(strategy)}上移`" @click="move(index, index - 1, $event)">上移</a-button>
          <a-button :id="`${controlPrefix}-${strategy}-down`" size="small" :disabled="disabled || index === modelValue.length - 1" :aria-label="`${strategyText(strategy)}下移`" @click="move(index, index + 1, $event)">下移</a-button>
        </span>
      </li>
    </ol>
    <div class="compression-order-hint">拖动序号手柄或点击上移、下移调整顺序；按顺序自动尝试，当前方式失败后继续下一种。保存后生效。</div>
    <span class="sr-only" role="status" aria-live="polite">{{ announcement }}</span>
  </div>
</template>

<script>
import { computed, defineComponent, nextTick, onBeforeUnmount, ref, toRaw, useId } from 'vue'
import { AI_CONTEXT_COMPRESSION_STRATEGIES, aiContextCompressionStrategyText, moveAIContextCompressionStrategy } from '@/utils/aiSearchConfigHelpers.mjs'

export default defineComponent({
  name: 'AICompressionOrder',
  props: {
    modelValue: { type: Array, required: true },
    disabled: { type: Boolean, default: false },
    idPrefix: { type: String, default: '' },
    labelledBy: { type: String, default: '' },
  },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    const fallbackPrefix = `ai-compression-order-${useId()}`
    const controlPrefix = computed(() => props.idPrefix || fallbackPrefix)
    // The server strategy is always present in normalized configuration. Its
    // native handle remains the field's stable label/focus entry after sorting.
    const handleId = strategy => `${controlPrefix.value}-${strategy === 'server' ? 'entry' : `${strategy}-handle`}`
    let alive = true
    onBeforeUnmount(() => { alive = false })
    const dragging = ref('')
    const dropTarget = ref('')
    const announcement = ref('')
    let pointerActive = false
    function clearDrag() { dragging.value = ''; dropTarget.value = ''; pointerActive = false }
    function move(from, to, event) {
      if (props.disabled || from === to || from < 0 || from >= props.modelValue.length || to < 0 || to >= props.modelValue.length) return
      const next = moveAIContextCompressionStrategy(props.modelValue, from, to)
      const focusTarget = event?.currentTarget
      const ownsFocus = focusTarget && document.activeElement === focusTarget
      emit('update:modelValue', next)
      announcement.value = `${aiContextCompressionStrategyText(props.modelValue[from])}已移到第 ${to + 1} 位`
      if (ownsFocus) nextTick(() => {
        // Only an accepted, still-current controlled update may restore focus.
        // A retired/rejected parent update or a later focus choice wins.
        if (!alive || props.disabled || toRaw(props.modelValue) !== next || !focusTarget.isConnected) return
        if (document.activeElement === focusTarget || document.activeElement === document.body) focusTarget.focus()
      })
    }
    function startDrag(event, strategy) {
      if (props.disabled) { event.preventDefault(); return }
      dragging.value = strategy
      if (event.dataTransfer) {
        event.dataTransfer.effectAllowed = 'move'
        event.dataTransfer.setData('text/plain', strategy)
      }
    }
    function dragOver(event, strategy) {
      if (!dragging.value || props.disabled) return
      dropTarget.value = strategy
      if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
    }
    function drop(strategy) {
      if (dragging.value) move(props.modelValue.indexOf(dragging.value), props.modelValue.indexOf(strategy))
      clearDrag()
    }
    function startPointerDrag(event, strategy) {
      if (props.disabled || event.button !== 0) return
      event.preventDefault()
      dragging.value = strategy
      pointerActive = true
      event.currentTarget.setPointerCapture(event.pointerId)
    }
    function pointerMove(event) {
      if (!pointerActive) return
      const row = document.elementFromPoint(event.clientX, event.clientY)?.closest('.compression-order-row')
      const list = event.currentTarget.closest('.compression-order')
      dropTarget.value = row && list?.contains(row) ? row.dataset.strategy : ''
    }
    function finishPointerDrag(event) {
      if (!pointerActive) return
      pointerMove(event)
      if (dropTarget.value) drop(dropTarget.value)
      else clearDrag()
    }
    function strategyDescription(strategy) { return AI_CONTEXT_COMPRESSION_STRATEGIES.find(item => item.value === strategy)?.description || '' }
    return { controlPrefix, handleId, dragging, dropTarget, announcement, move, startDrag, dragOver, drop, clearDrag, startPointerDrag, pointerMove, finishPointerDrag, strategyText: aiContextCompressionStrategyText, strategyDescription }
  },
})
</script>

<style scoped>
.compression-order { display: grid; gap: 8px; max-width: 760px; padding: 0; margin: 0; list-style: none; }
.compression-order-row { display: flex; align-items: center; gap: 12px; padding: 12px; border: 1px solid var(--console-border); border-radius: 8px; background: var(--console-subtle); cursor: grab; }
.compression-order-row:active { cursor: grabbing; }
.compression-order-row.is-dragging { opacity: 0.5; }
.compression-order-row.is-drop-target { border-color: var(--console-accent, #1677ff); }
.compression-order-handle { flex: 0 0 30px; width: 30px; height: 32px; padding: 0; border: 1px solid var(--console-border); border-radius: 4px; background: var(--console-subtle); color: var(--console-muted); font-variant-numeric: tabular-nums; text-align: center; cursor: grab; touch-action: none; user-select: none; }
.compression-order-handle:active { cursor: grabbing; }
.compression-order-content { display: grid; flex: 1; gap: 4px; min-width: 0; overflow-wrap: anywhere; }
.compression-order-content strong { color: var(--console-text); font-size: 14px; font-weight: 600; }
.compression-order-content > span, .compression-order-hint { color: var(--console-muted); font-size: 12px; line-height: 1.8; }
.compression-order-actions { display: flex; flex: 0 0 auto; gap: 8px; }
.compression-order-hint { margin-top: 8px; overflow-wrap: anywhere; }
.sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
@media (max-width: 480px) {
  .compression-order-row { flex-wrap: wrap; gap: 8px; padding: 10px; }
  .compression-order-actions { justify-content: flex-end; width: 100%; }
}
</style>
