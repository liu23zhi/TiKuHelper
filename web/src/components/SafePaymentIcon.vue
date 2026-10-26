<template>
  <span class="safe-payment-icon">
    <span v-if="safeSvg" class="safe-payment-icon__svg" v-html="safeSvg"></span>
    <span v-else class="safe-payment-icon__fallback">{{ fallback }}</span>
  </span>
</template>

<script>
export default {
  name: 'SafePaymentIcon',
  props: {
    iconSvg: { type: String, default: '' },
    fallback: { type: String, default: '支' },
  },
  computed: {
    // Payment SVGs are allowlisted and serialized by the service before they
    // reach this component. The backend also sanitizes legacy records on read.
    safeSvg() {
      return String(this.iconSvg || '').trim()
    },
  },
}
</script>

<style scoped>
.safe-payment-icon { display: inline-flex; align-items: center; justify-content: center; }
.safe-payment-icon__svg :deep(svg) { display: block; width: 28px; height: 28px; }
.safe-payment-icon__fallback { color: var(--console-primary); font-size: 24px; font-weight: 600; }
</style>
