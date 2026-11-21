<template>
  <a-modal :open="true" :title="`重核验与追缴 · 提交 #${record.id}`" :footer="null" @cancel="emit('close')">
    <a-spin :spinning="busy">
      <a-alert v-if="error" type="error" show-icon :message="error" />
      <a-button v-if="!loaded && !busy" @click="load">重新读取</a-button>
      <template v-if="loaded">
        <a-descriptions v-if="verification" bordered :column="1" size="small">
          <a-descriptions-item label="最近核验">{{ results[verification.result] }}</a-descriptions-item>
          <a-descriptions-item label="理由">{{ verification.reason }}</a-descriptions-item>
          <a-descriptions-item label="证据">{{ verification.evidence }}</a-descriptions-item>
        </a-descriptions>
        <a-descriptions v-if="clawback" title="追缴结果" bordered :column="1" size="small">
          <a-descriptions-item label="实际扣回">{{ moneyText(clawback.deductedAmount) }}</a-descriptions-item>
          <a-descriptions-item label="未追回">{{ moneyText(clawback.shortfallAmount) }}</a-descriptions-item>
        </a-descriptions>
        <a-form v-else :model="form" layout="vertical" :disabled="busy" @finish="save">
          <a-form-item label="核验结论" :html-for="`${prefix}-result`" required>
            <a-select :id="`${prefix}-result`" v-model:value="form.result" :options="options" />
          </a-form-item>
          <a-form-item label="核验理由" :html-for="`${prefix}-reason`" required>
            <a-input :id="`${prefix}-reason`" v-model:value="form.reason" :maxlength="255" />
          </a-form-item>
          <a-form-item label="证据" :html-for="`${prefix}-evidence`" required>
            <a-textarea :id="`${prefix}-evidence`" v-model:value="form.evidence" :rows="3" :maxlength="2000" />
          </a-form-item>
          <a-space wrap>
            <a-button type="primary" html-type="submit" :disabled="!form.result || !form.reason.trim() || !form.evidence.trim()">保存核验结论</a-button>
            <a-popconfirm
              v-if="verification?.result === 'invalid'"
              title="确认追缴？仅扣减可用赠金，余额不足部分记入差额。"
              ok-text="确认追缴"
              cancel-text="取消"
              :disabled="busy"
              @confirm="deduct"
            >
              <a-button danger :disabled="busy">追缴赠金</a-button>
            </a-popconfirm>
          </a-space>
        </a-form>
      </template>
    </a-spin>
  </a-modal>
</template>

<script setup>
import { onBeforeUnmount, onMounted, reactive, ref, useId } from 'vue'
import { getReviewRebateVerification, recordReviewRebateVerification, clawBackReviewRebate } from '@/api/ReviewRebateApi'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'

const props = defineProps({ record: { type: Object, required: true }, canManage: { type: Function, required: true } })
const emit = defineEmits(['close', 'changed'])
const prefix = 'review-verification-' + useId()
const results = { confirmed: '评价仍有效', invalid: '评价无效', inconclusive: '无法确认' }
const options = Object.entries(results).map(([value, label]) => ({ value, label }))
const form = reactive({ result: undefined, reason: '', evidence: '' })
const verification = ref(null)
const clawback = ref(null)
const error = ref('')
const busy = ref(false)
const loaded = ref(false)
let alive = true
const current = () => alive && props.canManage()
const moneyText = value => `¥ ${formatMoneyValue(value)}`

async function run(request, expectedStatus, apply) {
  if (!current() || busy.value) return
  busy.value = true
  error.value = ''
  try {
    const response = await request()
    if (!current()) return
    if (response?.status !== expectedStatus || !response.data) {
      error.value = response?.message || response?.data?.message || '操作失败，请重新读取核验记录。'
      return
    }
    apply(response.data)
  } catch {
    if (current()) error.value = '操作结果未确认，请重新读取核验记录。'
  } finally {
    if (current()) busy.value = false
  }
}

const load = () => run(() => getReviewRebateVerification(props.record.id), 200, data => {
  verification.value = data.verification
  clawback.value = data.clawback
  loaded.value = true
})
function save() {
  if (!form.result || !form.reason.trim() || !form.evidence.trim()) return
  return run(() => recordReviewRebateVerification(props.record.id, { ...form }), 201, data => {
    verification.value = data
    Object.assign(form, { result: undefined, reason: '', evidence: '' })
  })
}
function deduct() {
  if (verification.value?.result !== 'invalid' || clawback.value) return
  return run(() => clawBackReviewRebate(props.record.id, verification.value.id), 200, data => {
    clawback.value = data
    emit('changed')
  })
}
onMounted(load)
onBeforeUnmount(() => { alive = false })
</script>
