<template>
  <label class="theme-control" title="外观">
    <span class="theme-control-icon" aria-hidden="true">{{ state.resolved === 'dark' ? '☾' : '☀' }}</span>
    <span class="theme-control-label">外观</span>
    <select aria-label="外观" :value="state.mode" :disabled="!controller" @change="changeMode">
      <option value="light">亮色</option>
      <option value="dark">夜间</option>
      <option value="system">跟随系统</option>
    </select>
  </label>
</template>

<script>
import { defineComponent, inject, onBeforeUnmount, ref } from 'vue'
import { THEME_CONTROLLER_KEY } from '../../utils/themeMode.mjs'

export default defineComponent({
  name: 'ThemeControl',
  setup() {
    const controller = inject(THEME_CONTROLLER_KEY, null)
    const state = ref(controller?.getState() || { mode: 'system', resolved: 'light' })
    const unsubscribe = controller?.subscribe(next => { state.value = next })
    onBeforeUnmount(() => unsubscribe?.())
    const changeMode = event => controller?.setMode(event.target.value)
    return { controller, state, changeMode }
  },
})
</script>

<style scoped>
.theme-control {
  display: inline-flex;
  align-items: center;
  gap: var(--control-icon-label-gap);
  min-height: var(--control-touch-min);
  margin: var(--g-zero);
  color: var(--text, #1D1D1F);
  font-size: 14px;
  white-space: nowrap;
}
.theme-control-icon { font-size: 18px; }
.theme-control select {
  min-width: var(--theme-control-min-width);
  min-height: var(--control-touch-min);
  padding: var(--theme-control-padding);
  border: var(--layout-divider-width) solid var(--control-border, #85858B);
  border-radius: var(--control-radius);
  background: var(--surface, #FFFFFF);
  color: var(--text, #1D1D1F);
  font: inherit;
  cursor: pointer;
}
.theme-control select:focus-visible {
  outline: var(--focus-outline-width) solid var(--focus, #0066CC);
  outline-offset: var(--focus-outline-offset);
}
@media (max-width: 480px) {
  .theme-control-icon { display: none; }
}
</style>
