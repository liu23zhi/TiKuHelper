<template>
  <article class="risk-form policy-card" :data-risk-policy="detector">
    <h3>{{ title }}</h3><p class="hint">{{ description }}</p>
    <template v-if="saved">
      <p>当前策略：<strong>{{ saved.mode === 'temp_ip' ? '临时封单 IP · ' + saved.durationMinutes + ' 分钟' : '仅通知' }}</strong></p>
      <p v-if="!saved.blacklistEnabled" class="hint">黑名单总开关已关闭，暂不拦截访问。</p>
      <div class="form-row">
        <label>命中后<select v-model="mode" :disabled="busy"><option value="notify">仅通知（保留告警）</option><option value="temp_ip">自动临时封单 IP</option></select></label>
        <label v-if="mode === 'temp_ip'">封禁分钟数<input v-model.number="minutes" type="number" min="1" max="1440" :disabled="busy"></label>
      </div>
      <p v-if="mode === 'temp_ip'" class="hint">限能确定单一来源 IP 的告警；范围封禁和永久封禁必须人工确认。</p>
      <a-button :loading="busy" :disabled="!valid" @click="save">保存处置策略</a-button>
    </template>
    <p v-if="error" role="alert">{{ error }} <a-button v-if="!saved" size="small" @click="reload">重试</a-button></p>
    <p v-if="notice" role="status">{{ notice }}</p>
  </article>
</template>
<script setup>
import { computed, ref } from 'vue'
import { getRiskPolicy, putRiskPolicy } from '@/api/RiskAdminApi'
import { useRiskForm } from './useRiskForm'
const props = defineProps({ detector: { type: String, required: true }, title: String, description: String, captureScope: { type: Function, required: true } })
const mode = ref('notify'), minutes = ref(30)
const { saved, busy, error, notice, run, reload } = useRiskForm(props.captureScope, () => getRiskPolicy(props.detector), data => {
  mode.value = data.mode; minutes.value = data.durationMinutes || 30
})
const valid = computed(() => mode.value === 'notify' || (mode.value === 'temp_ip' && Number.isInteger(minutes.value) && minutes.value >= 1 && minutes.value <= 1440))
function save() {
  if (valid.value) return run(() => putRiskPolicy(props.detector, mode.value, mode.value === 'temp_ip' ? minutes.value : saved.value.durationMinutes || 30), '策略已保存')
}
</script>
<style scoped src="./riskForms.css"></style>
<style scoped>
.policy-card { align-content: start; padding: 16px; border: 1px solid var(--console-border); border-radius: 8px; }
</style>
