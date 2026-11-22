<template>
  <section class="risk-form response-box" aria-label="告警处置">
    <h3>告警处置</h3>
    <template v-if="saved">
      <p v-if="!saved.eligible" class="hint">{{ saved.message || '仅保留通知：此告警没有可单独归因的来源 IP，或不属于两项核心保护。' }}</p>
      <p v-else-if="!saved.blacklistEnabled" class="hint">黑名单总开关已关闭；当前不会拦截。请先在黑名单管理确认全站设置。</p>
      <template v-else>
        <div class="form-row">
          <label>封禁范围<select v-model="action" :disabled="busy">
            <option value="ip">仅来源 IP</option><option v-if="saved.segment" value="segment">来源 IP 所属网段</option><option v-if="saved.cityIsp" value="city_isp">指定国家/地区、省、市和运营商</option>
          </select></label>
          <label v-if="!permanent">有效期（分钟）<input v-model.number="minutes" type="number" min="1" max="525600" :disabled="busy"></label>
        </div>
        <label class="confirm"><input v-model="permanent" type="checkbox" :disabled="busy">永久封禁，直到管理员解除</label>
        <p class="hint">将拦截：<strong data-response-target>{{ target }}</strong> · {{ permanent ? '永久' : minutes + ' 分钟' }}。未返回的网段或运营商不会作为目标。</p>
        <label>处置原因<textarea v-model.trim="reason" rows="2" maxlength="255" :disabled="busy" placeholder="写明人工判定依据"></textarea></label>
        <label v-if="needsConfirm" class="confirm"><input v-model="confirmed" type="checkbox" :disabled="busy">我已核对以上范围和期限，确认执行</label>
        <a-button danger :disabled="!valid || busy" :loading="busy" @click="apply">执行封禁</a-button>
      </template>
      <div v-if="saved.history?.length">
        <h4>处置记录</h4>
        <ul><li v-for="row in saved.history" :key="row.id">
          {{ row.actorId ? '管理员 #' + row.actorId : '自动策略' }} · {{ row.target || '—' }} · {{ statusLabel[row.status] || row.status }}
          <span v-if="row.entryId && row.status !== 'skipped'"> · {{ row.expiresAt ? '到期：' + formatRiskDateTime(row.expiresAt) : '无到期日' }}</span>
          <span v-if="row.reason"> · {{ row.reason }}</span>
        </li></ul>
      </div>
    </template>
    <p v-if="error" role="alert">{{ error }} <a-button size="small" @click="confirmed = false; reload()">刷新处置目标</a-button></p>
    <p v-if="notice" role="status">{{ notice }}</p>
  </section>
</template>
<script setup>
import { computed, ref, watch } from 'vue'
import { applyAlertResponse, getAlertResponse } from '@/api/RiskAdminApi'
import { formatRiskDateTime } from './riskAdminHelpers.mjs'
import { useRiskForm } from './useRiskForm'
const props = defineProps({ alertId: { type: Number, required: true }, captureScope: { type: Function, required: true } })
const action = ref('ip'), minutes = ref(60), permanent = ref(false), confirmed = ref(false), reason = ref('')
const statusLabel = { applied: '已写入黑名单', existing: '已存在，保留原处置', skipped: '未执行' }
const { saved, busy, error, notice, run, reload } = useRiskForm(props.captureScope, () => getAlertResponse(props.alertId))
const target = computed(() => saved.value?.[{ ip: 'ip', segment: 'segment', city_isp: 'cityIsp' }[action.value]] || '')
const needsConfirm = computed(() => action.value !== 'ip' || permanent.value)
watch([action, minutes, permanent, target], () => { confirmed.value = false }, { flush: 'sync' })
const valid = computed(() => saved.value?.eligible && saved.value.blacklistEnabled && target.value && reason.value.trim() &&
  (!needsConfirm.value || confirmed.value) && (permanent.value || (Number.isInteger(minutes.value) && minutes.value >= 1 && minutes.value <= 525600)))
async function apply() {
  if (!valid.value) return
  if (await run(() => applyAlertResponse(props.alertId, {
    action: action.value, durationMinutes: permanent.value ? 0 : minutes.value,
    reason: reason.value.trim(), confirmed: confirmed.value, expectedTarget: target.value,
  }), '处置请求已处理')) {
    confirmed.value = false; reason.value = ''
  }
}
</script>
<style scoped src="./riskForms.css"></style>
<style scoped>
.response-box { margin-top: 18px; padding: 16px; border: 1px solid var(--console-border); border-radius: 8px; }
.response-box ul { padding-left: 18px; }
</style>
