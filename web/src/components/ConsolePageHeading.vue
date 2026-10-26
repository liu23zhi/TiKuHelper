<template>
  <header class="console-page-heading section-heading page-heading">
    <div class="console-page-heading__text">
      <h1>{{ title }}</h1>
      <p v-if="description" class="page-lead">{{ description }}</p>
      <slot name="description" />
    </div>
    <div v-if="$slots.actions" class="console-page-heading__actions"><slot name="actions" /></div>
  </header>
</template>

<script>
export default {
  name: 'ConsolePageHeading',
  props: { title: { type: String, required: true }, description: { type: String, default: '' } },
}
</script>

<style scoped>
.console-page-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
.console-page-heading__text { min-width: 0; }
.console-page-heading h1 { margin: 0; font-size: 24px; line-height: 1.4; font-weight: 700; color: var(--console-text); }
.console-page-heading .page-lead { margin: 4px 0 0; color: var(--console-muted); font-size: 13px; line-height: 1.7; }
.console-page-heading__actions { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
</style>
