<template>
  <div class="console-page review-rebate-page">
    <ConsolePageHeading title="评价返现" description="提交商店评价截图，系统抓取公开评价并打分，管理员复核后发放赠金。">
      <template #actions>
        <a-button :loading="loading" :disabled="loading" @click="loadAll">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <section v-if="programFailure" class="page-alert">
      <RequestFailureAlert
        :key="programFailureContextKey"
        :failure="programFailure"
        :message="loaded ? '活动信息刷新失败，页面显示的仍是上次读取的结果。' : '活动信息尚未加载成功，请重试读取。'"
        :retryable="true"
        :busy="loading"
        :context-key="programFailureContextKey"
        :is-current="isCurrentProgramFailure"
        @retry="loadAll"
      />
    </section>

    <!-- 总开关关闭：整页只显示未开启，不展示规则表与提交表单。 -->
    <a-alert v-if="loaded && !enabled" type="info" show-icon class="page-alert" message="评价返现功能未开启">
      <template #description>
        <p class="hint">当前站点没有开放评价返现。开启后这里会显示活动规则、提交入口和你的提交记录。</p>
      </template>
    </a-alert>

    <template v-if="loaded && enabled">
      <ConsolePanel title="活动说明" description="系统只核对「公开评价里存在一条与截图一致的记录」，不做账号归属的强断言。">
        <a-descriptions :column="1" size="small" bordered class="program-descriptions">
          <a-descriptions-item label="活动名称">{{ program.name || '—' }}</a-descriptions-item>
          <a-descriptions-item label="商店条目">
            <a v-if="program.storeUrl" :href="program.storeUrl" target="_blank" rel="noopener noreferrer" class="store-link">{{ program.storeUrl }}</a>
            <span v-else>—</span>
          </a-descriptions-item>
          <a-descriptions-item label="每账号可领次数">{{ maxClaimsText }}</a-descriptions-item>
          <a-descriptions-item label="每账号可领金额">{{ moneyText(program.maxAmountPerUser) }}</a-descriptions-item>
        </a-descriptions>
        <p class="hint">
          提交前请先在商店条目下发表真实评价，并截取能看清星数与评价正文的图。
          所有结果都标注「归属未验证」；置信度低于门槛时一定会转人工复核，不会自动发放。
        </p>
      </ConsolePanel>

      <ConsolePanel title="当前规则" description="规则由管理员配置：命中区间按优先级取第一条。只读展示。">
        <ConsoleTableShell v-if="rules.length" label="评价返现规则表">
          <a-table
            table-layout="fixed"
            :data-source="rules"
            :columns="ruleColumns"
            :pagination="false"
            row-key="id"
            size="small"
            :scroll="{ x: sumTableColumnWidths(ruleColumns) }"
          >
            <template #bodyCell="{ column, record }">
              <span v-if="column.key === 'stars'">{{ rangeText(record.minStars, record.maxStars) }}</span>
              <span v-else-if="column.key === 'contentScore'">{{ rangeText(record.minContentScore, record.maxContentScore) }}</span>
              <span v-else-if="column.key === 'amount'" class="money-cell">{{ moneyText(record.amount) }}</span>
              <span v-else-if="column.key === 'priority'">{{ record.priority ?? '—' }}</span>
              <span v-else-if="column.key === 'remark'">{{ record.remark || '—' }}</span>
            </template>
          </a-table>
        </ConsoleTableShell>
        <a-empty v-else description="管理员还没有配置任何规则，暂时无法按规则计算金额。" />
      </ConsolePanel>

      <ConsoleSummaryGrid class="claim-summary">
        <div>
          <div class="summary-label">已领次数</div>
          <div class="summary-value">{{ claimUsage.count }} / {{ maxClaimsText }}</div>
        </div>
        <div>
          <div class="summary-label">已领金额</div>
          <div class="summary-value" :class="{ 'is-limit': amountLimited }">{{ moneyText(claimUsage.amount) }}</div>
          <div class="summary-hint">上限 {{ moneyText(program.maxAmountPerUser) }}</div>
        </div>
        <div>
          <div class="summary-label">待审核 / 已提交</div>
          <div class="summary-value">{{ claimUsage.pending }} 条</div>
        </div>
        <div>
          <div class="summary-label">被拒绝</div>
          <div class="summary-value">{{ claimUsage.rejected }} 条</div>
        </div>
      </ConsoleSummaryGrid>

      <ConsolePanel title="提交截图" description="必填 1 张截图（PNG / JPEG，≤ 5 MB）。评价链接可选，填写后有助于核对。">
        <a-form layout="vertical" :onFinish="submit">
          <a-form-item label="评价截图" :html-for="fieldId('screenshot')" required>
            <input
              :id="fieldId('screenshot')"
              type="file"
              accept="image/png,image/jpeg"
              :disabled="submitting || limitReached"
              @change="onFileChange"
            />
            <div class="hint">
              <span v-if="selectedFile">{{ selectedFile.name }}（{{ formatSize(selectedFile.size) }}）</span>
              <span v-else>支持 PNG / JPEG，单张不超过 5 MB；服务端会再解码校验一次。</span>
            </div>
          </a-form-item>
          <a-form-item label="评价链接（可选）" :html-for="fieldId('reviewUrl')">
            <a-input
              :id="fieldId('reviewUrl')"
              :value="reviewUrl"
              type="url"
              allow-clear
              placeholder="https://…"
              :disabled="submitting || limitReached"
              @update:value="updateReviewUrl"
            />
            <div class="hint">留空也可以提交，只是核对会慢一些。</div>
          </a-form-item>

          <p v-if="formError" class="form-error" role="alert">{{ formError }}</p>
          <p v-if="limitReached" class="form-error" role="alert">已达到每账号的领取上限，暂时无法继续提交。</p>

          <a-space wrap>
            <a-button type="primary" html-type="submit" :loading="submitting" :disabled="submitting || !selectedFile || limitReached">
              提交审核
            </a-button>
            <a-button :disabled="submitting" @click="resetForm">清空</a-button>
          </a-space>
        </a-form>

        <a-alert
          v-if="submitWarning"
          type="warning"
          show-icon
          class="status-alert"
          :message="submitWarning"
        />
        <section v-if="submitFailure" class="status-alert">
          <RequestFailureAlert
            :key="submitFailureContextKey"
            :failure="submitFailure"
            message="提交结果未确认，请先刷新下面的列表核对，勿重复提交。"
            :busy="submitting"
            :context-key="submitFailureContextKey"
            :is-current="isCurrentSubmitFailure"
          />
        </section>
      </ConsolePanel>

      <ConsolePanel title="我的提交" description="状态徽章反映当前进度；Agent 打分只是建议，最终以管理员复核为准。">
        <section v-if="claimsFailure" class="status-alert">
          <RequestFailureAlert
            :key="claimsFailureContextKey"
            :failure="claimsFailure"
            :message="claimsLoaded ? '提交记录刷新失败，页面显示的仍是上次读取的结果。' : '提交记录尚未加载成功，请重试读取。'"
            :retryable="true"
            :busy="loadingClaims"
            :context-key="claimsFailureContextKey"
            :is-current="isCurrentClaimsFailure"
            @retry="loadClaims"
          />
        </section>

        <ConsoleTableShell v-if="claims.length" label="我的评价返现提交">
          <a-table
            table-layout="fixed"
            :data-source="claims"
            :columns="claimColumns"
            :loading="loadingClaims"
            :pagination="false"
            row-key="id"
            size="small"
            :scroll="{ x: sumTableColumnWidths(claimColumns) }"
          >
            <template #bodyCell="{ column, record }">
              <span v-if="column.key === 'id'">{{ record.id ?? '-' }}</span>
              <a-tag v-else-if="column.key === 'status'" :color="claimStatusColor(record.status)">{{ claimStatusLabel(record.status) }}</a-tag>
              <span v-else-if="column.key === 'agentStars'">{{ record.agentStars ?? '—' }}</span>
              <span v-else-if="column.key === 'agentContentScore'">{{ record.agentContentScore ?? '—' }}</span>
              <span v-else-if="column.key === 'agentConfidence'" :class="{ 'is-low-confidence': isLowConfidence(record) }">
                {{ formatConfidence(record.agentConfidence) }}
                <a-tag v-if="isLowConfidence(record)" color="orange" class="low-tag">低于门槛</a-tag>
              </span>
              <span v-else-if="column.key === 'matchedRule'">{{ record.matchedRuleId ?? '—' }}</span>
              <span v-else-if="column.key === 'awardAmount'" class="money-cell">{{ moneyText(record.awardAmount) }}</span>
              <span v-else-if="column.key === 'reviewNote'" class="note-cell">{{ record.reviewNote || '—' }}</span>
              <span v-else-if="column.key === 'createdAt'" class="time-cell">{{ formatDateTime(record.createdAt) }}</span>
            </template>
          </a-table>
        </ConsoleTableShell>
        <a-empty v-else-if="claimsLoaded && !loadingClaims" description="还没有提交过评价" />
      </ConsolePanel>
    </template>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, useId, watch } from 'vue'
import { useRoute } from 'vue-router'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import ConsoleTableShell from '@/components/ConsoleTableShell.vue'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import {
  getReviewRebateProgram,
  getMyReviewRebateClaims,
  submitReviewRebateClaim,
} from '@/api/ReviewRebateApi'
import { getCurrentUserLogoutEpoch, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

const MAX_UPLOAD_BYTES = 5 * 1024 * 1024
const ACCEPTED_TYPES = ['image/png', 'image/jpeg']
const ACCEPTED_EXTENSIONS = /\.(png|jpe?g)$/i

const CLAIM_STATUS = {
  submitted: { label: '已提交', color: 'blue' },
  scraping: { label: '抓取中', color: 'blue' },
  scored: { label: '已打分', color: 'cyan' },
  pending_review: { label: '待人工复核', color: 'gold' },
  approved: { label: '已通过', color: 'green' },
  rejected: { label: '已拒绝', color: 'red' },
  below_threshold: { label: '未达门槛', color: 'default' },
  duplicate: { label: '疑似重复', color: 'orange' },
  limit_reached: { label: '超出上限', color: 'orange' },
}

function readNonnegativeNumber(value) {
  if (!['number', 'string'].includes(typeof value) || String(value).trim() === '') return null
  const number = Number(value)
  return Number.isFinite(number) && number >= 0 ? number : null
}

// 后端在总开关关闭 / 活动被禁用时返回 400 + message「评价返现未启用」(service.ErrReviewRebateDisabled)。
// 判据用 message 而不是状态码：400 也可能来自参数错误，那时应该显示失败态。
function isReviewRebateDisabledResponse(res) {
  const message = String(res?.message || res?.data?.message || '')
  return message.includes('未启用')
}

export default {
  name: 'ReviewRebatePage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell, RequestFailureAlert },
  setup() {
    const route = useRoute()
    const prefix = 'review-rebate-' + useId()
    const fieldId = field => `${prefix}-${field}`
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const loaded = ref(false)
    const program = ref({})
    const usage = ref({})
    const enabled = ref(false)
    const rules = ref([])

    const loadingClaims = ref(false)
    const claimsLoaded = ref(false)
    const claims = ref([])

    const selectedFile = ref(null)
    const reviewUrl = ref('')
    const submitting = ref(false)
    const formError = ref('')
    const submitWarning = ref('')

    const programFailure = shallowRef(null)
    const programFailureContextKey = ref(0)
    let programFailureOwner = () => false
    const claimsFailure = shallowRef(null)
    const claimsFailureContextKey = ref(0)
    let claimsFailureOwner = () => false
    const submitFailure = shallowRef(null)
    const submitFailureContextKey = ref(0)
    let submitFailureOwner = () => false

    let alive = true
    let mounted = false
    let programVersion = 0
    let claimsVersion = 0
    let submitVersion = 0
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()
    let observedLogout = mountedLogoutEpoch

    const pageActive = () => alive && route.path === mountedPath
    const canLoad = () => pageActive() && getCurrentUserLogoutEpoch() === mountedLogoutEpoch
      && Boolean(currentUserIdentity(currentUser.value))

    const maxClaims = computed(() => readNonnegativeNumber(program.value.maxClaimsPerUser))
    const maxClaimsText = computed(() => (maxClaims.value === null ? '—' : `${maxClaims.value} 次`))

    const claimUsage = computed(() => {
      const approvedCount = readNonnegativeNumber(usage.value.approvedCount)
      const approvedAmount = readNonnegativeNumber(usage.value.approvedAmount)
      const rejectedCount = readNonnegativeNumber(usage.value.rejectedCount)
      const list = Array.isArray(claims.value) ? claims.value : []
      // 后端的 usage 是权威来源；缺失时才按本地列表兜底统计，避免把「已通过」的实际发放看漏。
      const approved = list.filter(item => String(item?.status || '').toLowerCase() === 'approved')
      const fallbackAmount = approved.reduce((total, item) => total + (readNonnegativeNumber(item?.awardAmount) || 0), 0)
      // claimedCount 是额度口径的历史名字，approvedCount 是同一份数据的另一个键；两个都认。
      const countFallback = readNonnegativeNumber(usage.value.claimedCount) ?? approved.length
      return {
        count: approvedCount ?? countFallback,
        amount: approvedAmount ?? fallbackAmount,
        pending: list.filter(item => ['submitted', 'scraping', 'scored', 'pending_review'].includes(String(item?.status || '').toLowerCase())).length,
        rejected: rejectedCount ?? list.filter(item => String(item?.status || '').toLowerCase() === 'rejected').length,
      }
    })
    const amountLimited = computed(() => {
      const limit = readNonnegativeNumber(program.value.maxAmountPerUser)
      return limit !== null && limit > 0 && claimUsage.value.amount >= limit
    })
    const countLimited = computed(() => maxClaims.value !== null && maxClaims.value > 0 && claimUsage.value.count >= maxClaims.value)
    const limitReached = computed(() => countLimited.value || amountLimited.value)

    const minConfidence = computed(() => readNonnegativeNumber(program.value.minAgentConfidence))

    const ruleColumns = [
      { title: '星数', dataIndex: 'stars', key: 'stars', width: 120 },
      { title: '内容分', dataIndex: 'contentScore', key: 'contentScore', width: 120 },
      { title: '金额', dataIndex: 'amount', key: 'amount', width: 130 },
      { title: '优先级', dataIndex: 'priority', key: 'priority', width: 100 },
      { title: '备注', dataIndex: 'remark', key: 'remark', width: 220 },
    ]
    const claimColumns = [
      { title: '提交号', dataIndex: 'id', key: 'id', width: 95 },
      { title: '状态', dataIndex: 'status', key: 'status', width: 130 },
      { title: 'Agent 星数', dataIndex: 'agentStars', key: 'agentStars', width: 110 },
      { title: 'Agent 内容分', dataIndex: 'agentContentScore', key: 'agentContentScore', width: 125 },
      { title: '置信度', dataIndex: 'agentConfidence', key: 'agentConfidence', width: 170 },
      { title: '命中规则', dataIndex: 'matchedRule', key: 'matchedRule', width: 110 },
      { title: '金额', dataIndex: 'awardAmount', key: 'awardAmount', width: 130 },
      { title: '审核理由', dataIndex: 'reviewNote', key: 'reviewNote', width: 240 },
      { title: '提交时间', dataIndex: 'createdAt', key: 'createdAt', width: 175 },
    ]

    const rangeText = (min, max) => {
      const low = readNonnegativeNumber(min)
      const high = readNonnegativeNumber(max)
      if (low === null && high === null) return '—'
      if (low === null) return `≤ ${high}`
      if (high === null) return `≥ ${low}`
      return low === high ? String(low) : `${low}–${high}`
    }
    // 与 UserUsage 一致：金额最多 6 位小数、去尾零（后端 util.MoneyPrecision = 6）。
    const moneyText = value => {
      const amount = readNonnegativeNumber(value)
      return amount === null ? '—' : `¥ ${formatMoneyValue(amount)}`
    }
    const formatConfidence = value => {
      const confidence = readNonnegativeNumber(value)
      return confidence === null ? '—' : confidence.toFixed(2)
    }
    const formatSize = bytes => {
      const value = Number(bytes)
      if (!Number.isFinite(value) || value <= 0) return '—'
      if (value < 1024) return `${value} B`
      if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
      return `${(value / (1024 * 1024)).toFixed(2)} MB`
    }
    const formatDateTime = value => {
      if (!value) return '—'
      const date = new Date(value)
      return Number.isFinite(date.getTime())
        ? date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
        : '—'
    }
    function claimStatusLabel(value) {
      const key = String(value || '').trim().toLowerCase()
      return CLAIM_STATUS[key]?.label || value || '-'
    }
    function claimStatusColor(value) {
      const key = String(value || '').trim().toLowerCase()
      return CLAIM_STATUS[key]?.color || 'default'
    }
    function isLowConfidence(record) {
      const confidence = readNonnegativeNumber(record?.agentConfidence)
      const threshold = minConfidence.value
      return confidence !== null && threshold !== null && confidence < threshold
    }

    const clearProgramFailure = () => {
      programFailure.value = null; programFailureOwner = () => false; programFailureContextKey.value += 1
    }
    const clearClaimsFailure = () => {
      claimsFailure.value = null; claimsFailureOwner = () => false; claimsFailureContextKey.value += 1
    }
    const clearSubmitFailure = () => {
      submitFailure.value = null; submitFailureOwner = () => false; submitFailureContextKey.value += 1
    }
    const isCurrentProgramFailure = (source, context) => programFailureOwner() && Boolean(source)
      && source === programFailure.value && context === programFailureContextKey.value
    const isCurrentClaimsFailure = (source, context) => claimsFailureOwner() && Boolean(source)
      && source === claimsFailure.value && context === claimsFailureContextKey.value
    const isCurrentSubmitFailure = (source, context) => submitFailureOwner() && Boolean(source)
      && source === submitFailure.value && context === submitFailureContextKey.value

    const loadProgram = async () => {
      if (!canLoad()) return false
      const ticket = ++programVersion
      const owns = () => canLoad() && ticket === programVersion
      clearProgramFailure()
      const context = programFailureContextKey.value
      const ownsFailure = () => owns() && context === programFailureContextKey.value
      try {
        const res = await getReviewRebateProgram()
        if (!ownsFailure()) return false
        // 总开关关闭（或活动被禁用）时后端返回 400「评价返现未启用」。
        // 这不是"加载失败"，而是**正常的未开放状态**：必须落成 enabled=false 的
        // 「功能未开启」提示，否则用户看到的是一个红色失败告警 + 重试按钮，
        // 点多少次都不会好 —— 那才是真的让人以为站点坏了。
        if (res?.status === 400 && isReviewRebateDisabledResponse(res)) {
          program.value = {}
          usage.value = {}
          rules.value = []
          enabled.value = false
          loaded.value = true
          return true
        }
        if (res?.status !== 200 || !res.data || typeof res.data !== 'object') {
          programFailureOwner = ownsFailure
          programFailure.value = ensureRequestFailure(res)
          return false
        }
        const body = res.data
        // 后端可能返回 { program, rules, usage } 或直接把 program 平铺在顶层。
        const programBody = body.program && typeof body.program === 'object' ? body.program : body
        program.value = programBody
        usage.value = body.usage && typeof body.usage === 'object'
          ? body.usage
          : (programBody.usage && typeof programBody.usage === 'object' ? programBody.usage : {})
        rules.value = Array.isArray(body.rules)
          ? body.rules.filter(rule => rule && typeof rule === 'object' && rule.enabled !== false)
          : []
        enabled.value = programBody.enabled === true
        loaded.value = true
        return true
      } catch (error) {
        if (ownsFailure()) { programFailureOwner = ownsFailure; programFailure.value = ensureRequestFailure(error) }
        return false
      }
    }

    const loadClaims = async () => {
      if (!canLoad()) return false
      const ticket = ++claimsVersion
      const owns = () => canLoad() && ticket === claimsVersion
      clearClaimsFailure()
      const context = claimsFailureContextKey.value
      const ownsFailure = () => owns() && context === claimsFailureContextKey.value
      loadingClaims.value = true
      try {
        const res = await getMyReviewRebateClaims()
        if (!ownsFailure()) return false
        const body = res?.data
        const list = Array.isArray(body) ? body : body?.data
        if (res?.status !== 200 || !Array.isArray(list)) {
          claimsFailureOwner = ownsFailure
          claimsFailure.value = ensureRequestFailure(res)
          return false
        }
        claims.value = list.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
        claimsLoaded.value = true
        return true
      } catch (error) {
        if (ownsFailure()) { claimsFailureOwner = ownsFailure; claimsFailure.value = ensureRequestFailure(error) }
        return false
      } finally {
        if (owns()) loadingClaims.value = false
      }
    }

    const loadingAll = ref(false)
    const loadAll = async () => {
      if (!canLoad() || loadingAll.value) return
      loadingAll.value = true
      try {
        await loadProgram()
        if (!canLoad()) return
        if (loaded.value && enabled.value) await loadClaims()
      } finally {
        if (canLoad()) loadingAll.value = false
      }
    }

    function updateReviewUrl(value) {
      if (submitting.value) return
      reviewUrl.value = String(value ?? '')
      formError.value = ''
    }

    function onFileChange(event) {
      formError.value = ''
      const file = event?.target?.files?.[0] || null
      if (!file) { selectedFile.value = null; return }
      const typeOk = ACCEPTED_TYPES.includes(String(file.type || '').toLowerCase())
        || ACCEPTED_EXTENSIONS.test(String(file.name || ''))
      if (!typeOk) {
        selectedFile.value = null
        formError.value = '只支持 PNG / JPEG 截图'
        if (event?.target) event.target.value = ''
        return
      }
      if (!(file.size > 0)) {
        selectedFile.value = null
        formError.value = '截图内容为空，请重新选择'
        if (event?.target) event.target.value = ''
        return
      }
      if (file.size > MAX_UPLOAD_BYTES) {
        selectedFile.value = null
        formError.value = '截图超过 5 MB，无法提交'
        if (event?.target) event.target.value = ''
        return
      }
      selectedFile.value = file
    }

    // 清空表单内容本身不加锁：提交成功后要在 submitting 仍为 true 时复位。
    function clearForm() {
      selectedFile.value = null
      reviewUrl.value = ''
      formError.value = ''
      submitWarning.value = ''
      clearSubmitFailure()
      const input = document.getElementById(fieldId('screenshot'))
      if (input) input.value = ''
    }
    function resetForm() {
      if (submitting.value) return
      clearForm()
    }

    const submit = async () => {
      if (!canLoad() || submitting.value || limitReached.value) return
      if (!selectedFile.value) { formError.value = '请先选择评价截图'; return }
      const link = String(reviewUrl.value || '').trim()
      if (link && !/^https?:\/\//i.test(link)) { formError.value = '评价链接必须以 http:// 或 https:// 开头'; return }
      const ticket = ++submitVersion
      const owns = () => canLoad() && ticket === submitVersion
      clearSubmitFailure()
      const context = submitFailureContextKey.value
      const ownsFailure = () => owns() && context === submitFailureContextKey.value
      formError.value = ''
      submitWarning.value = ''
      submitting.value = true
      let reloadAfter = false
      try {
        const res = await submitReviewRebateClaim(selectedFile.value, link)
        if (!ownsFailure()) return
        if (res?.status === 200 || res?.status === 201) {
          clearForm()
          reloadAfter = true
          return
        }
        if (res?.status === 413) { formError.value = '截图超过服务端允许的大小'; return }
        if (res?.status === 400) {
          formError.value = res?.message || res?.data?.message || '提交内容不符合要求，请核对后重试'
          return
        }
        if (res?.status === 409) {
          submitWarning.value = '该评价可能已提交过，请先在下方的列表核对，勿重复提交。'
          reloadAfter = true
          return
        }
        submitFailureOwner = ownsFailure
        submitFailure.value = ensureRequestFailure(res)
      } catch (error) {
        if (ownsFailure()) { submitFailureOwner = ownsFailure; submitFailure.value = ensureRequestFailure(error) }
      } finally {
        if (owns()) submitting.value = false
      }
      // loadAll/loadClaims 在提交锁释放后再刷新，避免与提交态互相阻塞。
      if (reloadAfter && owns()) await loadAll()
    }

    const retire = () => {
      programVersion += 1; claimsVersion += 1; submitVersion += 1
      program.value = {}; usage.value = {}; rules.value = []; enabled.value = false; loaded.value = false
      claims.value = []; claimsLoaded.value = false; loadingClaims.value = false
      selectedFile.value = null; reviewUrl.value = ''; formError.value = ''
      submitWarning.value = ''; submitting.value = false; loadingAll.value = false
      clearProgramFailure(); clearClaimsFailure(); clearSubmitFailure()
    }

    watch(() => currentUser.value?.id, () => {
      retire()
      if (mounted && canLoad()) void loadAll()
    }, { flush: 'sync' })
    watch(() => getCurrentUserLogoutEpoch(), value => {
      if (value === observedLogout) return
      observedLogout = value
      retire()
    }, { flush: 'sync' })
    watch(() => route.path, retire, { flush: 'sync' })

    onMounted(() => {
      mounted = true
      if (canLoad()) void loadAll()
    })
    onBeforeUnmount(() => {
      alive = false; mounted = false
      retire()
    })

    return {
      fieldId, loading: computed(() => loadingAll.value),
      loaded, enabled, program, rules, loadingClaims, claimsLoaded, claims,
      selectedFile, reviewUrl, submitting, formError, submitWarning,
      programFailure, programFailureContextKey, isCurrentProgramFailure,
      claimsFailure, claimsFailureContextKey, isCurrentClaimsFailure,
      submitFailure, submitFailureContextKey, isCurrentSubmitFailure,
      maxClaimsText, claimUsage, amountLimited, limitReached,
      ruleColumns, claimColumns, rangeText, moneyText, formatConfidence,
      formatSize, formatDateTime, claimStatusLabel, claimStatusColor, isLowConfidence,
      loadAll, loadClaims, updateReviewUrl, onFileChange, resetForm, submit,
      sumTableColumnWidths,
    }
  },
}
</script>

<style scoped>
.page-alert, .status-alert { margin-bottom: 16px; }
.program-descriptions { margin-bottom: 12px; }
.store-link { overflow-wrap: anywhere; }
.claim-summary { margin: 16px 0; }
.summary-label { color: var(--console-muted); font-size: 14px; }
.summary-value { margin-top: 8px; color: var(--console-text); font-size: clamp(20px, 2.6vw, 28px); font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.summary-value.is-limit { color: var(--console-danger, #c13e41); }
.summary-hint { margin-top: 8px; color: var(--console-muted); font-size: 12px; line-height: 1.6; }
.hint { margin: 8px 0 0; color: var(--console-muted); font-size: 12px; line-height: 1.7; }
.form-error { margin: 0 0 16px; color: var(--console-danger, #c13e41); font-size: 13px; }
.money-cell, .time-cell { font-variant-numeric: tabular-nums; }
.note-cell { overflow-wrap: anywhere; white-space: pre-wrap; }
.is-low-confidence { color: var(--console-danger, #c13e41); font-weight: 600; }
.low-tag { margin-inline-start: 6px; }
</style>
