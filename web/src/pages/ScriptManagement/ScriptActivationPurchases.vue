<template>
  <a-modal :open="true" title="购买一次性激活码" width="900px" :footer="null" :mask-closable="!buying" :closable="!buying" :keyboard="!buying" @cancel="close">
    <div v-if="isCurrent()" class="activation-purchases">
      <p class="hint">购买后可自己使用或分享给他人。兑换后保留 30 分钟创建资格，超时未创建将恢复未使用；成功创建后消耗。</p>
      <div class="purchase-actions">
        <a-popconfirm title="确认从钱包购买一次性激活码？" :description="priceText" :disabled="!price.enabled || buying || loading" @confirm="buy">
          <a-button type="primary" :loading="buying" :disabled="!price.enabled || buying || loading" data-standalone-activation-buy>钱包购买 {{ price.enabled ? '¥' + price.price : '暂未开放' }}</a-button>
        </a-popconfirm>
        <a-button :loading="loading" :disabled="buying || loading" @click="load">刷新购买记录</a-button>
      </div>
      <a-alert v-if="error" type="error" show-icon :message="error" />
      <a-alert v-if="latestCode" type="success" show-icon message="购买成功，激活码已保存到购买记录。">
        <template #description><code class="activation-code">{{ latestCode }}</code><a-button size="small" @click="copy(latestCode)">复制</a-button></template>
      </a-alert>
      <a-table class="purchase-table" :data-source="rows" :columns="columns" row-key="entitlementId" size="small" :loading="loading" :scroll="{ x: 800 }" :pagination="{ current: page, pageSize: 10, total, showSizeChanger: false }" @change="changePage">
        <template #bodyCell="{ column, record }">
          <span v-if="column.key === 'createdAt'">{{ dateTime(record.createdAt) }}</span>
          <span v-else-if="column.key === 'price'">¥{{ record.price }}</span>
          <div v-else-if="column.key === 'status'"><a-tag>{{ statusText(record.status) }}</a-tag><p v-if="record.status === 'frozen' && record.frozenUntil" class="hint">保留至 {{ dateTime(record.frozenUntil) }}</p></div>
          <div v-else-if="column.key === 'code'"><code v-if="record.code" class="activation-code">{{ record.code }}</code><span v-else>{{ record.receiptError ? '历史激活码无法恢复，请联系管理员' : '—' }}</span></div>
          <a-space v-else-if="column.key === 'actions'" wrap>
            <a-button size="small" :disabled="!record.code" @click="copy(record.code)">复制</a-button>
            <a-button size="small" :disabled="!record.code || record.status !== 'unused' || buying" @click="use(record)">填入新建脚本</a-button>
          </a-space>
        </template>
      </a-table>
      <div class="purchase-cards">
        <p v-if="!rows.length" class="hint">{{ loading ? '正在读取购买记录…' : '暂无购买记录' }}</p>
        <article v-for="record in rows" :key="record.entitlementId" class="purchase-card">
          <div class="purchase-card-heading"><strong>¥{{ record.price }}</strong><a-tag>{{ statusText(record.status) }}</a-tag></div>
          <time class="hint">{{ dateTime(record.createdAt) }}</time>
          <p v-if="record.status === 'frozen' && record.frozenUntil" class="hint">保留至 {{ dateTime(record.frozenUntil) }}</p>
          <code v-if="record.code" class="activation-code">{{ record.code }}</code>
          <p v-else class="hint">历史激活码无法恢复，请联系管理员</p>
          <a-space wrap><a-button size="small" :disabled="!record.code" @click="copy(record.code)">复制</a-button><a-button size="small" :disabled="!record.code || record.status !== 'unused' || buying" @click="use(record)">填入新建脚本</a-button></a-space>
        </article>
        <div v-if="total > 10" class="purchase-actions"><a-button :disabled="page <= 1 || loading || buying" @click="changePage({ current: page - 1 })">上一页</a-button><span>{{ page }} / {{ Math.ceil(total / 10) }}</span><a-button :disabled="page * 10 >= total || loading || buying" @click="changePage({ current: page + 1 })">下一页</a-button></div>
      </div>
      <div class="purchase-actions"><a-button :disabled="buying" @click="close">关闭</a-button></div>
    </div>
  </a-modal>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { getCurrentUserLogoutEpoch, getStoredCurrentUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { getScriptActivationCodePrice, getScriptActivationCodePurchases, purchaseScriptActivationCode } from '@/api/TikuComponentApi'
import { scriptPurchaseIntent, clearScriptPurchaseIntent } from '@/utils/scriptPurchaseIntent.mjs'

const props = defineProps({ canUse: { type: Function, required: true } })
const emit = defineEmits(['close', 'use'])
const { currentUser } = useCurrentUserDisplay()
const owner = String(currentUser.value?.id || ''), logout = getCurrentUserLogoutEpoch()
let alive = true, readVersion = 0
const loading = ref(false), buying = ref(false), error = ref(''), latestCode = ref('')
const rows = ref([]), page = ref(1), total = ref(0), price = ref({ enabled: false, price: '' })
const priceText = computed(() => `将扣除钱包余额 ¥${price.value.price}。激活码可从购买记录再次查看。`)
const columns = [
  { title: '购买时间', key: 'createdAt', width: 170 }, { title: '金额', key: 'price', width: 90 },
  { title: '状态', key: 'status', width: 165 }, { title: '激活码', key: 'code', width: 250 }, { title: '操作', key: 'actions', width: 170 },
]
function isCurrent() {
  getStoredCurrentUser()
  return alive && props.canUse() && owner && String(currentUser.value?.id || '') === owner && logout === getCurrentUserLogoutEpoch()
}
function retire() { readVersion++; rows.value = []; latestCode.value = ''; error.value = ''; price.value = { enabled: false, price: '' } }
watch(() => [currentUser.value?.id, getCurrentUserLogoutEpoch()], () => { if (!isCurrent()) retire() }, { flush: 'sync' })
function statusText(status) { return ({ unused: '未使用', frozen: '已兑换，待创建', used: '已创建脚本' })[status] || '待核对' }
function dateTime(value) { const date = new Date(value); return value && !Number.isNaN(date.getTime()) ? date.toLocaleString('zh-CN', { hour12: false }) : '—' }
function close() { if (!buying.value) { retire(); emit('close') } }
function use(record) { if (isCurrent() && !buying.value && rows.value.includes(record) && record.status === 'unused' && record.code) emit('use', record.code) }
async function copy(code) {
  if (!isCurrent() || !code) return
  try {
    if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable')
    await navigator.clipboard.writeText(code)
    if (isCurrent()) message.success('激活码已复制')
  } catch { if (isCurrent()) message.info('请手动选择并复制激活码') }
}
async function load() {
  if (!isCurrent() || loading.value) return
  const ticket = ++readVersion
  loading.value = true; error.value = ''
  try {
    const [history, offer] = await Promise.allSettled([getScriptActivationCodePurchases({ page: page.value, pageSize: 10 }), getScriptActivationCodePrice()])
    if (!isCurrent() || ticket !== readVersion) return
    const value = offer.value?.data
    price.value = offer.status === 'fulfilled' && offer.value?.status === 200 && value?.enabled === true && Number(value.price) > 0
      ? { enabled: true, price: String(value.price) } : { enabled: false, price: '' }
    if (history.status !== 'fulfilled' || history.value?.status !== 200 || !Array.isArray(history.value.data?.items)) throw new Error('购买记录未读取成功，请刷新核对。')
    rows.value = history.value.data.items; total.value = Number(history.value.data.total) || 0
  } catch (failure) { if (isCurrent() && ticket === readVersion) error.value = failure.message || '购买记录未读取成功。' }
  finally { if (isCurrent() && ticket === readVersion) loading.value = false }
}
async function changePage(pagination) { if (!loading.value && !buying.value && isCurrent()) { page.value = pagination.current; await load() } }
async function buy() {
  if (!isCurrent() || buying.value || loading.value || !price.value.enabled) return
  buying.value = true; error.value = ''
  const intent = scriptPurchaseIntent(owner, price.value.price)
  try {
    const response = await purchaseScriptActivationCode(intent.key, intent.quote)
    if (!isCurrent()) return
    if (response?.status !== 200 || !/^sac_[a-f0-9]{64}$/.test(response.data?.code || '')) {
      if (response?.data?.code === 'SCRIPT_ACTIVATION_PRICE_CHANGED') { clearScriptPurchaseIntent(owner); await load() }
      throw new Error(response?.data?.message || '购买结果未确认，请刷新记录核对。')
    }
    latestCode.value = response.data.code; clearScriptPurchaseIntent(owner); page.value = 1
    await load()
  } catch (failure) {
    if (isCurrent()) {
      if (failure?.response?.data?.code === 'SCRIPT_ACTIVATION_PRICE_CHANGED') { clearScriptPurchaseIntent(owner); await load() }
      error.value = failure?.response?.data?.message || failure.message || '购买结果未确认，请刷新记录核对。'
    }
  } finally { if (isCurrent()) buying.value = false }
}
onMounted(load)
onBeforeUnmount(() => { alive = false; retire() })
</script>

<style scoped>
.activation-purchases { display: grid; grid-template-columns: minmax(0, 1fr); gap: 16px; min-width: 0; }
.activation-purchases > * { min-width: 0; max-width: 100%; }
.purchase-actions { display: flex; flex-wrap: wrap; gap: 10px; }
.hint { margin: 0; color: var(--console-muted); line-height: 1.7; font-size: 13px; overflow-wrap: anywhere; }
.activation-code { display: block; overflow-wrap: anywhere; user-select: all; white-space: normal; }
.purchase-cards { display: none; }
.purchase-card { display: grid; gap: 10px; padding: 14px; border: 1px solid var(--console-border, #e5e7eb); border-radius: 12px; min-width: 0; }
.purchase-card-heading { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 8px; }
@media (max-width: 640px) { .purchase-table { display: none; } .purchase-cards { display: grid; gap: 12px; } }
</style>
