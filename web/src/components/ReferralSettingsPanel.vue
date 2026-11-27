<template>
  <ConsolePanel
    v-if="canManage()"
    title="自动返佣规则"
    description="按受邀人单笔到账的实付本金匹配；赠金不计入。区间含下限、不含上限，按从上到下的优先级取首条启用且命中的规则（0% 也会命中，但不产生返佣）。"
    data-auto-rebate-rules
  >
    <a-alert v-if="error" class="rules-message" type="error" show-icon :message="error" />
    <a-alert v-if="saved" class="rules-message" type="success" show-icon message="规则已保存" />
    <p v-if="loading" role="status">正在读取自动返佣规则…</p>
    <form :aria-busy="busy" @submit.prevent="save">
      <div class="rule-control">
        <label :for="fieldId('auto')">启用自动返佣</label>
        <a-switch :id="fieldId('auto')" v-model:checked="enabled" :disabled="!loaded || busy" data-auto-switch @change="clearFeedback" />
      </div>
      <p class="rules-hint">关闭开关、没有启用规则或未命中时不产生自动返佣；不影响邀请码、邀请关系和手动补录。修改在保存后生效。</p>
      <p v-if="loaded && rules.length === 0" class="rules-hint">暂无自动返佣规则。</p>
      <fieldset v-for="(rule, index) in rules" :key="rule.key" class="rule" data-referral-rule :disabled="busy">
        <legend>优先级 {{ index + 1 }}</legend>
        <div class="rule-fields">
          <label :for="fieldId('lower', rule)">下限（元，含）
            <a-input :id="fieldId('lower', rule)" v-model:value="rule.lower" inputmode="decimal" :disabled="busy" data-rule-lower @change="clearFeedback" />
          </label>
          <label :for="fieldId('upper', rule)">上限（元，不含）
            <a-input :id="fieldId('upper', rule)" v-model:value="rule.upper" inputmode="decimal" :disabled="busy || rule.unbounded" data-rule-upper @change="clearFeedback" />
          </label>
          <label :for="fieldId('rate', rule)">返佣比例（%）
            <a-input :id="fieldId('rate', rule)" v-model:value="rule.percent" inputmode="decimal" :disabled="busy" data-rule-rate @change="clearFeedback" />
          </label>
        </div>
        <div class="rule-controls">
          <div class="rule-control">
            <label :for="fieldId('unbounded', rule)">无上限</label>
            <a-switch :id="fieldId('unbounded', rule)" v-model:checked="rule.unbounded" :disabled="busy" data-rule-unbounded @change="clearFeedback" />
          </div>
          <div class="rule-control">
            <label :for="fieldId('enabled', rule)">启用规则</label>
            <a-switch :id="fieldId('enabled', rule)" v-model:checked="rule.enabled" :disabled="busy" data-rule-enabled @change="clearFeedback" />
          </div>
          <div class="rule-controls rule-actions">
            <a-button html-type="button" size="small" :disabled="busy || index === 0" :aria-label="'上移优先级 ' + (index + 1) + ' 的规则'" data-rule-up @click="moveRule(index, -1)">上移</a-button>
            <a-button html-type="button" size="small" :disabled="busy || index === rules.length - 1" :aria-label="'下移优先级 ' + (index + 1) + ' 的规则'" data-rule-down @click="moveRule(index, 1)">下移</a-button>
            <a-button html-type="button" size="small" danger :disabled="busy" :aria-label="'删除优先级 ' + (index + 1) + ' 的规则'" data-rule-delete @click="deleteRule(index)">删除</a-button>
          </div>
        </div>
      </fieldset>
      <div class="rule-controls">
        <a-button html-type="button" :disabled="!loaded || busy || rules.length >= MAX_RULES" data-add-referral-rule @click="addRule">添加规则</a-button>
        <a-button html-type="submit" type="primary" :disabled="!loaded || busy" :loading="saving">保存规则</a-button>
        <a-button html-type="button" :disabled="busy" @click="load">重新读取规则</a-button>
      </div>
    </form>
  </ConsolePanel>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { useRoute } from 'vue-router'
import ConsolePanel from '@/components/ConsolePanel.vue'
import { getReferralRebateRules, saveReferralRebateRules } from '@/api/ReferralApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'

defineOptions({ name: 'ReferralRebateRulesPanel' })

const MAX_RULES = 100
const MAX_CENTS = 99_999_999_999_999
const MAX_RATE_UNITS = 99_999_999
const route = useRoute()
const path = route.path
const { currentUser } = useCurrentUserDisplay()
const logoutEpoch = getCurrentUserLogoutEpoch()
const prefix = 'referral-rules-' + useId()
const fieldId = (field, rule) => prefix + '-' + field + (rule ? '-' + rule.key : '')
const enabled = ref(false), rules = ref([]), loaded = ref(false)
const loading = ref(false), saving = ref(false), error = ref(''), saved = ref(false)
const busy = computed(() => loading.value || saving.value)
let alive = true, mounted = false, version = 0, nextKey = 0

function canManage() {
  const user = currentUser.value
  return alive && route.path === path && getCurrentUserLogoutEpoch() === logoutEpoch
    && Boolean(currentUserIdentity(user)) && isAdminUser(user)
    && !['banned', 'disabled', '0'].includes(String(user?.status ?? 'enabled').toLowerCase())
}
function captureOwner() {
  const owner = version
  return () => canManage() && owner === version
}
function clearFeedback() {
  error.value = ''
  saved.value = false
}

const inRange = (value, max) => Number.isSafeInteger(value) && value >= 0 && value <= max

// Scale from decimal digits, not multiplication by a floating-point yuan/percent value.
function parseFixed(value, places, maximum) {
  const raw = value.trim()
  if (!/^(?:\d+(?:\.\d*)?|\.\d+)$/.test(raw)) return null
  const [whole, fraction = ''] = raw.split('.')
  if (fraction.length > places) return null
  const units = Number((whole || '0') + fraction.padEnd(places, '0'))
  return inRange(units, maximum) ? units : null
}
function fixedText(units, places) {
  const raw = String(units).padStart(places + 1, '0')
  return raw.slice(0, -places) + '.' + raw.slice(-places)
}
function readPolicy(policy) {
  if (typeof policy?.enabled !== 'boolean' || !Array.isArray(policy.rules) || policy.rules.length > MAX_RULES) return false
  const valid = policy.rules.every(rule => typeof rule?.enabled === 'boolean'
    && inRange(rule.lowerCents, MAX_CENTS) && (rule.upperCents === null
      || (inRange(rule.upperCents, MAX_CENTS) && rule.upperCents > rule.lowerCents))
    && inRange(rule.rateUnits, MAX_RATE_UNITS))
  if (!valid) return false
  enabled.value = policy.enabled
  rules.value = policy.rules.map(rule => ({
    key: ++nextKey, enabled: rule.enabled, lower: fixedText(rule.lowerCents, 2),
    upper: rule.upperCents === null ? '' : fixedText(rule.upperCents, 2),
    unbounded: rule.upperCents === null, percent: fixedText(rule.rateUnits, 6).replace(/0+$/, '').replace(/\.$/, ''),
  }))
  loaded.value = true
  return true
}
function buildPolicy() {
  const ordered = []
  for (const [index, rule] of rules.value.entries()) {
    const lowerCents = parseFixed(rule.lower, 2, MAX_CENTS)
    const upperCents = rule.unbounded ? null : parseFixed(rule.upper, 2, MAX_CENTS)
    const rateUnits = parseFixed(rule.percent, 6, MAX_RATE_UNITS)
    const label = '优先级 ' + (index + 1) + '：'
    if (lowerCents === null || (!rule.unbounded && upperCents === null)) {
      error.value = label + '金额必须在 0 到 999999999999.99 元之间，最多两位小数。'
      return null
    }
    if (upperCents !== null && upperCents <= lowerCents) {
      error.value = label + '上限必须大于下限，或选择无上限。'
      return null
    }
    if (rateUnits === null) {
      error.value = label + '返佣比例须大于等于 0% 且小于 100%，最多六位小数。'
      return null
    }
    ordered.push({ enabled: rule.enabled, lowerCents, upperCents, rateUnits })
  }
  return { enabled: enabled.value, rules: ordered }
}

function canEdit() {
  return canManage() && loaded.value && !busy.value
}
function addRule() {
  if (!canEdit() || rules.value.length >= MAX_RULES) return
  clearFeedback()
  rules.value.push({ key: ++nextKey, enabled: true, lower: '0', upper: '', unbounded: false, percent: '' })
}
function moveRule(index, direction) {
  const next = index + direction
  if (!canEdit() || next < 0 || next >= rules.value.length) return
  clearFeedback()
  const [rule] = rules.value.splice(index, 1)
  rules.value.splice(next, 0, rule)
}
function deleteRule(index) {
  if (!canEdit()) return
  clearFeedback()
  rules.value.splice(index, 1)
}

async function load() {
  if (!canManage() || busy.value) return
  const owns = captureOwner()
  loading.value = true
  clearFeedback()
  try {
    const res = await getReferralRebateRules()
    if (owns() && (res?.status !== 200 || !readPolicy(res.data))) error.value = res?.message || '自动返佣规则读取失败，请重新读取。'
  } catch {
    if (owns()) error.value = '自动返佣规则读取失败，请重新读取。'
  } finally {
    if (owns()) loading.value = false
  }
}
async function save() {
  if (!canEdit()) return
  clearFeedback()
  const policy = buildPolicy()
  if (!policy) return
  const owns = captureOwner()
  saving.value = true
  try {
    const res = await saveReferralRebateRules(policy)
    if (!owns()) return
    if (res?.status === 200 && readPolicy(res.data)) saved.value = true
    else error.value = res?.message || '未能确认规则已保存，请重新读取核对。'
  } catch {
    if (owns()) error.value = '未能确认规则已保存，请重新读取核对。'
  } finally {
    if (owns()) saving.value = false
  }
}

watch(() => [route.path, currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status, getCurrentUserLogoutEpoch()], () => {
  version += 1
  loaded.value = loading.value = saving.value = enabled.value = false
  rules.value = []
  clearFeedback()
  if (mounted && canManage()) void load()
}, { flush: 'sync' })
onMounted(() => { mounted = true; void load() })
onBeforeUnmount(() => { alive = false; mounted = false; version += 1 })
</script>

<style scoped>
.rule { margin: 0 0 16px; padding: 8px 12px 12px; border: 1px solid var(--console-border); border-radius: var(--console-radius, 8px); min-inline-size: 0; }
.rule legend { float: none; width: auto; margin: 0; padding: 0 4px; font-size: 14px; }
.rule-fields { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin-bottom: 12px; }
.rule-fields label { display: grid; gap: 4px; }
.rule-control, .rule-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.rule-controls { gap: 12px; }
.rule-actions { margin-inline-start: auto; }
.rules-message { margin-bottom: 12px; }
.rules-hint { margin: 8px 0 12px; color: var(--console-muted); }
@media (max-width: 600px) {
  .rule-fields { grid-template-columns: 1fr; }
  .rule-actions { margin-inline-start: 0; }
}
</style>
