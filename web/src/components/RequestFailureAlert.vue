<template>
  <section v-for="entry in entries" :key="entry.serial" class="request-failure" role="alert" aria-live="polite" :data-failure-kind="entry.view.kind">
    <a-alert type="error" show-icon :message="entry.view.title" :closable="closable" @close="dismiss(entry)">
      <template #description>
        <p v-if="message && message !== entry.view.title">{{ message }}</p>
        <p>{{ entry.view.description }}</p>
      </template>
    </a-alert>
    <div class="request-failure__details">
      <template v-if="entry.view.diagnosticId">
        <span>诊断编号</span><code data-request-diagnostic tabindex="0">{{ entry.view.diagnosticId }}</code>
        <a-button html-type="button" size="small" data-action="copy-request-diagnostic" :disabled="busy || copying" @click="copyDiagnostic(entry)">复制诊断编号</a-button>
      </template>
      <span v-else class="request-failure__hint">本次未收到服务端诊断编号，不生成替代编号。</span>
      <a-button v-if="retryable && entry.view.allowRetry" html-type="button" size="small" data-action="retry-request" :loading="busy" @click="retry(entry)">重试读取</a-button>
    </div>
    <p v-if="copyStatus" class="request-failure__hint" role="status">{{ copyStatus }}</p>
    <p class="request-failure__hint">诊断编号不是账单请求号；此处不会自动重新提交操作。</p>
  </section>
</template>

<script>
import { computed, onBeforeUnmount, shallowRef, toRaw, watch } from 'vue'
import { buildRequestFailureView } from '@/utils/requestFailureView.mjs'

function raw(value) { try { return toRaw(value) } catch { return value } }
export default {
  name: 'RequestFailureAlert',
  inheritAttrs: false,
  props: {
    failure: { default: null },
    message: { type: String, default: '请求未完成，请稍后核对。' },
    retryable: { type: Boolean, default: false },
    busy: { type: Boolean, default: false },
    closable: { type: Boolean, default: false },
    contextKey: { type: [String, Number], default: '' },
    isCurrent: { type: Function, default: null },
  },
  emits: ['retry', 'close'],
  setup(props, { emit }) {
    let alive = true, serial = 0
    const current = shallowRef(null)
    const copying = shallowRef(false)
    const copyStatus = shallowRef('')
    watch([() => props.failure, () => props.contextKey, () => props.message], () => {
      serial += 1
      copying.value = false
      copyStatus.value = ''
      const source = raw(props.failure)
      const view = buildRequestFailureView(source, props.message)
      current.value = view ? { source, view, context: props.contextKey, serial } : null
    }, { immediate: true, flush: 'sync' })
    const entries = computed(() => current.value ? [current.value] : [])
    function owns(entry) {
      if (!alive || current.value !== entry || raw(props.failure) !== entry.source || props.contextKey !== entry.context) return false
      try { return !props.isCurrent || props.isCurrent(entry.source, entry.context) === true } catch { return false }
    }
    function retry(entry) {
      if (owns(entry) && !props.busy && props.retryable && entry.view.allowRetry) emit('retry', entry.source, entry.context)
    }
    function dismiss(entry) {
      if (props.closable && owns(entry)) emit('close', entry.source, entry.context)
    }
    async function copyDiagnostic(entry) {
      if (!owns(entry) || props.busy || copying.value || !entry.view.diagnosticId) return
      copying.value = true
      copyStatus.value = ''
      try {
        if (typeof navigator === 'undefined' || typeof navigator.clipboard?.writeText !== 'function') throw new Error('clipboard unavailable')
        await navigator.clipboard.writeText(entry.view.diagnosticId)
        if (owns(entry)) copyStatus.value = '诊断编号已复制。'
      } catch {
        if (owns(entry)) copyStatus.value = '无法自动复制，请手动选择诊断编号复制。'
      } finally {
        if (owns(entry)) copying.value = false
      }
    }
    onBeforeUnmount(() => { alive = false; current.value = null; copying.value = false; copyStatus.value = '' })
    return { entries, copying, copyStatus, retry, dismiss, copyDiagnostic }
  },
}
</script>

<style scoped>
.request-failure { min-width: 0; margin-bottom: 16px; }
.request-failure__details { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-top: 10px; color: var(--console-text, var(--text)); }
.request-failure code { max-width: 100%; overflow-wrap: anywhere; user-select: text; padding: 3px 6px; border: 1px solid var(--console-border, var(--border)); border-radius: 4px; background: var(--console-surface, var(--surface)); color: inherit; }
.request-failure__hint { margin: 8px 0 0; color: var(--console-muted, var(--text-secondary)); font-size: 12px; line-height: 1.6; }
</style>
