<template>
  <div class="theme-controls">
    <label class="theme-control" title="外观">
      <span class="theme-control-icon" aria-hidden="true">{{ state.resolved === 'dark' ? '☾' : '☀' }}</span>
      <span class="theme-control-label">外观</span>
      <select aria-label="外观" :value="state.mode" :disabled="!controller" @change="changeMode">
        <option value="light">亮色</option>
        <option value="dark">夜间</option>
        <option value="system">跟随系统</option>
      </select>
    </label>
    <label class="theme-control" title="动效">
      <span class="theme-control-label">动效</span>
      <select aria-label="动效" :value="motion.mode" :disabled="!controller" @change="changeMotion">
        <option value="auto">跟随系统</option>
        <option value="on">开启</option>
        <option value="off">关闭</option>
      </select>
    </label>
  </div>
</template>

<script>
import { defineComponent, inject, onBeforeUnmount, ref } from 'vue'
import { THEME_CONTROLLER_KEY } from '../../utils/themeMode.mjs'

export default defineComponent({
  name: 'ThemeControl',
  setup() {
    const controller = inject(THEME_CONTROLLER_KEY, null)
    const state = ref(controller?.getState() || { mode: 'system', resolved: 'light' })
    const motion = ref(controller?.getMotionState() || { mode: 'auto', resolved: 'on' })
    const unsubscribe = controller?.subscribe(next => { state.value = next })
    const unsubscribeMotion = controller?.subscribeMotion(next => { motion.value = next })
    onBeforeUnmount(() => { unsubscribe?.(); unsubscribeMotion?.() })
    const changeMode = event => controller?.setMode(event.target.value)
    const changeMotion = event => controller?.setMotion(event.target.value)
    return { controller, state, motion, changeMode, changeMotion }
  },
})
</script>

<style scoped>
.theme-controls {
  display: inline-flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--header-actions-gap);
}
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
