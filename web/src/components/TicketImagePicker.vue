<template>
  <div class="ticket-image-picker">
    <button type="button" class="ticket-image-picker__button" :disabled="disabled" @click="choose">
      <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path d="M4 17v3h16v-3M12 16V4m-5 5 5-5 5 5" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" /></svg>
      <span>选择图片<span class="ticket-image-picker__limit">（最多 {{ max }} 张）</span></span>
    </button>
    <input ref="input" type="file" :accept="TICKET_ATTACHMENT_ACCEPT" :disabled="disabled" multiple hidden aria-hidden="true" tabindex="-1" style="display: none !important" @change="$emit('change', $event)" />
  </div>
</template>
<script setup>
import { ref } from 'vue'
import { TICKET_ATTACHMENT_ACCEPT } from '@/utils/ticketAttachmentHelpers.mjs'
const props = defineProps({ disabled: Boolean, max: { type: Number, default: 6 } })
defineEmits(['change'])
const input = ref(null)
function choose() { if (!props.disabled) input.value?.click() }
</script>
<style scoped>
.ticket-image-picker__button { display: inline-flex; align-items: center; gap: 8px; padding: 9px 14px; border: 1px dashed var(--console-border, #ccc); border-radius: 8px; background: var(--console-surface, #fff); color: var(--console-text, #333); cursor: pointer; font: inherit; font-size: 13px; }
.ticket-image-picker__button:hover, .ticket-image-picker__button:focus-visible { border-color: var(--primary, #1677ff); color: var(--primary, #1677ff); }
.ticket-image-picker__button:disabled { cursor: not-allowed; opacity: .5; }
.ticket-image-picker__limit { color: var(--console-muted); }
</style>
