<template>
  <section class="console-panel">
    <header v-if="title || description || $slots.actions || $slots.heading" class="console-panel__heading">
      <div><slot name="heading"><h2 v-if="title">{{ title }}</h2><p v-if="description">{{ description }}</p></slot></div>
      <div v-if="$slots.actions" class="console-panel__actions"><slot name="actions" /></div>
    </header>
    <div class="console-panel__body"><slot /></div>
  </section>
</template>

<script>
export default {
  name: 'ConsolePanel',
  props: { title: { type: String, default: '' }, description: { type: String, default: '' } },
}
</script>

<style scoped>
.console-panel__heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; flex-wrap: wrap; padding: 16px 24px; border-bottom: 1px solid var(--console-border); }
.console-panel__heading h2 { margin: 0; font-size: 16px; color: var(--console-text); }
.console-panel__heading p { margin: 4px 0 0; color: var(--console-muted); font-size: 13px; }
.console-panel__actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.console-panel__body { min-width: 0; padding: 24px; }
@media (max-width: 768px) { .console-panel__heading, .console-panel__body { padding: 16px; } }
</style>
