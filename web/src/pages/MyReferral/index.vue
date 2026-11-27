<template>
  <div class="console-page my-referral-page">
    <ConsolePageHeading title="我的邀请" description="把邀请码分享给新用户，保留邀请关系。只有符合自动返佣规则的单笔充值才会产生返佣，结算后可用于消费，不可退款。">
      <template #actions>
        <a-button :loading="loading" :disabled="loading" @click="load">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <!-- 自动返佣状态不限制邀请码的发放和使用。 -->
    <a-alert
      v-if="loaded && !enabled"
      type="info"
      show-icon
      class="page-alert"
      message="自动返佣未开启"
    >
      <template #description>
        <p class="hint">你仍可分享专属邀请码，邀请关系会保留；未开启期间的充值不产生自动返佣。</p>
      </template>
    </a-alert>

    <section v-if="failure" class="page-alert">
      <RequestFailureAlert
        :key="failureContextKey"
        :failure="failure"
        :message="failureMessage"
        :retryable="true"
        :busy="loading"
        :context-key="failureContextKey"
        :is-current="isCurrentFailure"
        @retry="load"
      />
    </section>

    <template v-if="loaded">
      <ConsolePanel title="我的邀请码" description="新用户注册时填写这个码，就会绑定到你名下。邀请码不含任何凭据，可以公开分享。">
        <div class="invite-code-block">
          <output class="invite-code" data-invite-code>{{ code || '邀请码生成中，请稍后刷新' }}</output>
          <div class="invite-code-actions">
            <a-button type="primary" :disabled="!code" @click="copyCode">复制邀请码</a-button>
            <span v-if="copyStatus" class="copy-status" role="status" aria-live="polite">{{ copyStatus }}</span>
          </div>
        </div>
        <p class="hint">只能邀请新注册账户；自己邀请自己会被拒绝。符合规则的充值按到账时的规则计算；规则调整不改写历史。</p>
      </ConsolePanel>

      <ConsoleSummaryGrid class="referral-summary">
        <div>
          <div class="summary-label">已邀请人数</div>
          <div class="summary-value">{{ invitedCount }} 人</div>
        </div>
        <div>
          <div class="summary-label">待结算</div>
          <div class="summary-value">¥ {{ formatMoneyValue(pendingAmount) }}</div>
        </div>
        <div>
          <div class="summary-label">已结算</div>
          <div class="summary-value">¥ {{ formatMoneyValue(settledAmount) }}</div>
        </div>
        <div>
          <div class="summary-label">自动返佣</div>
          <div class="summary-value">{{ enabled ? '已开启' : '未开启' }}</div>
          <div class="summary-hint">按单笔实付本金匹配金额区间；未命中规则不产生自动返佣。</div>
        </div>
      </ConsoleSummaryGrid>

      <ConsolePanel title="返佣情况" description="待结算、已结算金额均已扣除退款冲回；已冲回金额单独列示。">
        <a-descriptions :column="1" size="small" bordered class="referral-descriptions">
          <a-descriptions-item label="返佣记录条数">{{ rebateCount }} 条</a-descriptions-item>
          <a-descriptions-item label="产生返佣的实付本金合计">¥ {{ formatMoneyValue(totalBaseAmount) }}</a-descriptions-item>
          <a-descriptions-item label="已冲回金额">¥ {{ formatMoneyValue(reversedAmount) }}</a-descriptions-item>
        </a-descriptions>

        <div v-if="invitedCount === 0" class="empty-block">
          <p class="empty-title">还没有邀请任何人</p>
          <p class="hint">把上面的邀请码发给还没注册过本站的朋友。对方注册后，命中规则的充值到账才会生成待结算返佣。</p>
        </div>
        <p v-else-if="rebateCount === 0" class="hint">已邀请 {{ invitedCount }} 人，尚无返佣记录；符合规则的充值到账才会产生自动返佣。</p>
        <p v-else-if="pendingAmount <= 0 && settledAmount <= 0" class="hint">已有 {{ rebateCount }} 条返佣记录，但都已被冲回（原充值订单退款）。</p>
      </ConsolePanel>
    </template>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useRoute } from 'vue-router'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import { getMyReferral } from '@/api/ReferralApi'
import { getCurrentUserLogoutEpoch, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'

// 只读数字：缺失/非法一律当未知，不伪造 0 当成「确实没有」。
function readNumber(value) {
  if (!['number', 'string'].includes(typeof value) || String(value).trim() === '') return null
  const number = Number(value)
  return Number.isFinite(number) ? number : null
}

export default {
  name: 'MyReferralPage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, RequestFailureAlert },
  setup() {
    const route = useRoute()
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const loading = ref(false)
    const loaded = ref(false)
    const summary = ref({})
    const enabled = ref(false)
    const copyStatus = ref('')
    const failure = shallowRef(null)
    const failureContextKey = ref(0)
    let failureOwner = () => false

    let alive = true
    let mounted = false
    let readVersion = 0
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()
    let observedLogout = mountedLogoutEpoch

    // 与其它用户页一致：身份/路由/登出任一变化都不再让旧响应写回页面。
    const pageActive = () => alive && route.path === mountedPath
    const canLoad = () => pageActive() && getCurrentUserLogoutEpoch() === mountedLogoutEpoch
      && Boolean(currentUserIdentity(currentUser.value))

    const code = computed(() => String(summary.value.code || ''))
    const invitedCount = computed(() => readNumber(summary.value.invitedCount) ?? 0)
    const rebateCount = computed(() => readNumber(summary.value.rebateCount) ?? 0)
    const pendingAmount = computed(() => readNumber(summary.value.pendingAmount) ?? 0)
    const settledAmount = computed(() => readNumber(summary.value.settledAmount) ?? 0)
    const reversedAmount = computed(() => readNumber(summary.value.reversedAmount) ?? 0)
    const totalBaseAmount = computed(() => readNumber(summary.value.totalBaseAmount) ?? 0)

    const clearFailure = () => { failure.value = null; failureOwner = () => false; failureContextKey.value += 1 }
    const isCurrentFailure = (source, context) => failureOwner() && source === failure.value
      && context === failureContextKey.value

    const load = async () => {
      if (!canLoad() || loading.value) return false
      const ticket = ++readVersion
      const owns = () => canLoad() && ticket === readVersion
      clearFailure()
      const context = failureContextKey.value
      const ownsFailure = () => owns() && context === failureContextKey.value
      loading.value = true
      try {
        const res = await getMyReferral()
        if (!ownsFailure()) return false
        if (res?.status !== 200 || !res.data || typeof res.data !== 'object') {
          failureOwner = ownsFailure
          failure.value = ensureRequestFailure(res)
          return false
        }
        summary.value = res.data
        enabled.value = res.data.enabled === true
        loaded.value = true
        return true
      } catch (error) {
        if (ownsFailure()) { failureOwner = ownsFailure; failure.value = ensureRequestFailure(error) }
        return false
      } finally {
        if (owns()) loading.value = false
      }
    }

    const failureMessage = computed(() => loaded.value
      ? '邀请信息刷新失败，页面显示的仍是上次读取的结果。'
      : '邀请信息尚未加载成功，请重试读取。')

    const copyCode = async () => {
      if (!code.value) return
      copyStatus.value = ''
      try {
        if (typeof navigator === 'undefined' || typeof navigator.clipboard?.writeText !== 'function') {
          throw new Error('clipboard unavailable')
        }
        await navigator.clipboard.writeText(code.value)
        if (pageActive()) copyStatus.value = '邀请码已复制'
      } catch {
        if (pageActive()) copyStatus.value = '无法自动复制，请手动选中邀请码复制'
      }
    }

    const retire = () => {
      readVersion += 1
      summary.value = {}; enabled.value = false; loaded.value = false
      copyStatus.value = ''; loading.value = false; clearFailure()
    }
    watch(() => currentUser.value?.id, () => {
      retire()
      if (mounted && canLoad()) void load()
    }, { flush: 'sync' })
    watch(() => getCurrentUserLogoutEpoch(), (value) => {
      if (value === observedLogout) return
      observedLogout = value
      retire()
    }, { flush: 'sync' })
    watch(() => route.path, retire, { flush: 'sync' })

    onMounted(() => {
      mounted = true
      if (canLoad()) void load()
    })
    onBeforeUnmount(() => {
      alive = false; mounted = false
      retire()
    })

    return {
      loading, loaded, enabled, code, invitedCount, rebateCount,
      pendingAmount, settledAmount, reversedAmount, totalBaseAmount, copyStatus,
      failure, failureContextKey, failureMessage, isCurrentFailure,
      formatMoneyValue, load, copyCode,
    }
  },
}
</script>

<style scoped>
.page-alert { margin-bottom: 16px; }
.referral-summary { margin-bottom: 16px; }
.summary-label { color: var(--console-muted); font-size: 14px; }
.summary-value { margin-top: 8px; color: var(--console-text); font-size: clamp(20px, 2.6vw, 28px); font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.summary-hint { margin-top: 8px; color: var(--console-muted); font-size: 12px; line-height: 1.6; }
.invite-code-block { display: flex; flex-wrap: wrap; align-items: center; gap: 16px; }
/* 邀请码是要念给朋友、手抄在纸上的东西：大字号 + 等宽 + 字距，避免 0/O 混淆。 */
.invite-code { padding: 12px 20px; font-family: var(--console-mono, ui-monospace, SFMono-Regular, Consolas, monospace); font-size: clamp(24px, 4vw, 34px); font-weight: 700; letter-spacing: 0.16em; line-height: 1.4; color: var(--console-primary); background: var(--console-subtle, #f6f8f7); border: 1px solid var(--console-border); border-radius: var(--console-radius, 8px); overflow-wrap: anywhere; user-select: all; }
.invite-code-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
.copy-status { color: var(--console-success, #15803d); font-size: 13px; }
.hint { margin: 12px 0 0; color: var(--console-muted); font-size: 13px; line-height: 1.7; }
.empty-block { margin-top: 4px; }
.empty-title { margin: 0 0 4px; color: var(--console-text); font-size: 14px; font-weight: 600; }
.referral-descriptions { margin-bottom: 20px; }
</style>
