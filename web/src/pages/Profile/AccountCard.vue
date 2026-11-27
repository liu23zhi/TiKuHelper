<template>
  <section class="account-card" :data-profile-card="kind" :data-card-state="view.state" :aria-labelledby="headingId" :aria-busy="view.state === 'loading'">
    <header><h2 :id="headingId">{{ title }}</h2><p>{{ description }}</p></header>
    <div class="account-card-body">
      <p v-if="view.message" class="account-card-note" :class="{ warning: view.state === 'error' }" role="status">{{ view.message }}</p>
      <slot />
    </div>
  </section>
</template>

<script setup>
defineProps({
  kind: { type: String, required: true },
  headingId: { type: String, required: true },
  title: { type: String, required: true },
  description: { type: String, required: true },
  view: { type: Object, required: true },
})
</script>

<style scoped>
.account-card { min-width: 0; border: 1px solid var(--console-border, #dce5e1); border-radius: var(--console-radius, 12px); background: var(--console-surface, #fff); }
header { padding: 16px 20px; border-bottom: 1px solid var(--console-border, #dce5e1); }
h2 { margin: 0; font-size: 16px; line-height: 1.5; }
header p, .account-card-note { margin: 4px 0 0; color: var(--console-muted, #62766e); font-size: 13px; }
.account-card-body { min-width: 0; padding: 16px 20px; overflow-wrap: anywhere; }
.account-card-note { margin: 0 0 10px; }
.account-card-note:only-child { margin: 0; }
.account-card-note.warning { color: #8a451a; }
</style>
