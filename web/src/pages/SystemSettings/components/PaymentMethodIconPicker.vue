<template>
  <div class="payment-icon-picker">
    <div class="payment-icon-grid" role="group" aria-label="支付方式图标库">
      <button
        v-for="entry in catalog"
        :key="entry.id"
        type="button"
        class="payment-icon-button"
        :aria-label="`选择${entry.label}图标`"
        :aria-pressed="selectedId === entry.id"
        :disabled="disabled"
        @click="selectIcon(entry.id)"
      >
        <span class="payment-icon-glyph" aria-hidden="true"><component :is="entry.component" /></span>
        <span>{{ entry.label }}</span>
        <span v-if="selectedId === entry.id" class="payment-icon-selected">已选</span>
      </button>
    </div>
    <div class="payment-icon-status" role="status" aria-live="polite">{{ statusText }}</div>
  </div>
</template>

<script>
import { computed, defineComponent } from 'vue';
import { paymentIconCatalog } from '@/utils/paymentIconCatalog';

export default defineComponent({
  name: 'PaymentMethodIconPicker',
  props: {
    value: { type: String, default: '' },
    selectedId: { type: String, default: null },
    disabled: { type: Boolean, default: false },
  },
  emits: ['select'],
  setup(props, { emit }) {
    const statusText = computed(() => {
      const selected = paymentIconCatalog.find(entry => entry.id === props.selectedId);
      if (selected) return `已选择${selected.label}图标，未保存`;
      return props.value ? '已有SVG/自定义值，未改写' : '尚未选择图标，可选用图标库或粘贴 SVG';
    });
    const selectIcon = id => { if (!props.disabled) emit('select', id); };
    return { catalog: paymentIconCatalog, statusText, selectIcon };
  },
});
</script>

<style scoped>
.payment-icon-picker { margin-bottom: 8px; color: var(--text); }
.payment-icon-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 100px), 1fr)); gap: 8px; }
.payment-icon-button { min-height: 44px; padding: 8px; display: flex; align-items: center; justify-content: center; flex-wrap: wrap; gap: 6px; border: 1px solid currentColor; border-radius: 8px; background: transparent; color: inherit; font: inherit; cursor: pointer; }
.payment-icon-button[aria-pressed="true"] { box-shadow: inset 0 0 0 1px currentColor; }
.payment-icon-button:focus-visible { outline: 2px solid currentColor; outline-offset: 2px; }
.payment-icon-button:disabled { cursor: not-allowed; opacity: 0.5; }
.payment-icon-glyph { display: inline-flex; font-size: 24px; }
.payment-icon-selected { font-size: 12px; }
.payment-icon-status { margin-top: 8px; color: var(--text-muted); font-size: 12px; line-height: 1.6; }
</style>
