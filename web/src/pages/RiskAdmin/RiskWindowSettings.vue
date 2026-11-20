<template>
  <section class="risk-form" aria-label="共享凌晨时段">
    <h3>凌晨时段（北京时间）</h3>
    <template v-if="saved">
      <p data-window-effective>当前生效：<strong>{{ clock(saved.startHour) }} 至 {{ saved.startHour > saved.endHour ? '次日 ' : '' }}{{ clock(saved.endHour) }}</strong> · {{ saved.configured ? '已配置' : '默认' }}</p>
      <div class="form-row">
        <label>起点（含）<input v-model.number="start" type="number" min="0" max="23" :disabled="busy"></label>
        <label>终点（不含）<input v-model.number="end" type="number" min="0" max="23" :disabled="busy"></label>
      </div>
      <p class="hint">{{ valid ? '支持跨午夜。起止一起保存，对本类型的所有规则生效。' : '请填写两个不同的整点（0–23）。' }}</p>
      <div class="form-row"><a-button type="primary" :loading="busy" :disabled="!valid" @click="save">保存整个时段</a-button>
        <a-button :disabled="busy" @click="reset">恢复默认时段</a-button></div>
    </template>
    <p v-if="error" role="alert">{{ error }} <a-button v-if="!saved" size="small" @click="reload">重试</a-button></p>
    <p v-if="notice" role="status">{{ notice }}</p>
  </section>
</template>
<script setup>
import { computed, ref } from 'vue'
import { getRiskWindow, putRiskWindow, resetRiskWindow } from '@/api/RiskAdminApi'
import { useRiskForm } from './useRiskForm'
const props = defineProps({ captureScope: { type: Function, required: true } })
const start = ref(null), end = ref(null)
const { saved, busy, error, notice, run, reload } = useRiskForm(props.captureScope, getRiskWindow, data => {
  start.value = data.startHour; end.value = data.endHour
})
const clock = hour => String(hour).padStart(2, '0') + ':00'
const valid = computed(() => [start.value, end.value].every(v => Number.isInteger(v) && v >= 0 && v <= 23) && start.value !== end.value)
const save = () => valid.value && run(() => putRiskWindow(start.value, end.value), '共享时段已保存')
const reset = () => run(resetRiskWindow, '共享时段已恢复默认')
</script>
<style scoped src="./riskForms.css"></style>
