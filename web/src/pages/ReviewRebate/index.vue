<template>
  <div class="console-page review-rebate-page">
    <ConsolePageHeading title="评价返现" description="提交商店评价截图，审核通过后发放赠金。">
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

    <!--
      总开关关闭：**仍然渲染提交入口**，只是把它整体禁用并说明原因。

      ⚠️ 2026-11-15 用户实测报出的缺陷（「没有申请按钮」）：原实现在这里
      `v-if="loaded && !enabled"` 只渲染一句提示、再用 `<template v-if="loaded && enabled">`
      把规则表与提交表单**整块隐藏**。后果是「未开放」与「功能不存在」在界面上完全同形 ——
      用户看到的是"这个站点没有评价返现"，而真相是"有，只是没开"，两者的下一步
      动作完全相反（等开放 vs 去找客服要功能）。所以关闭态必须让入口可见。

      判据见 reviewRebateClosedEntry.test.mjs：关闭态下提交入口存在但 disabled、
      且给出明确原因文案。
    -->
    <a-alert
      v-if="loaded && !enabled"
      type="info"
      show-icon
      class="page-alert"
      :message="closedTitle"
      data-closed-reason
    >
      <template #description>
        <p class="hint" data-closed-reason-text>{{ closedReason }}</p>
      </template>
    </a-alert>

    <template v-if="loaded && enabled">
      <ConsolePanel title="活动说明">
        <a-descriptions :column="1" size="small" bordered class="program-descriptions">
          <a-descriptions-item v-if="stores.length > 1" label="商店">
            <a-radio-group :value="program.source" :disabled="loading || submitting" @update:value="selectSource">
              <a-radio-button v-for="store in stores" :key="store.source" :value="store.source">
                {{ storeLabel(store.source) }}
              </a-radio-button>
            </a-radio-group>
          </a-descriptions-item>
          <a-descriptions-item label="活动名称">{{ program.name || '—' }}</a-descriptions-item>
          <a-descriptions-item label="商店条目">
            <a v-if="program.storeUrl" :href="program.storeUrl" target="_blank" rel="noopener noreferrer" class="store-link">{{ program.storeUrl }}</a>
            <span v-else>—</span>
          </a-descriptions-item>
          <a-descriptions-item label="每账号可领次数">{{ maxClaimsText }}</a-descriptions-item>
        </a-descriptions>
        <p class="hint">
          提交前请先在商店条目下发表真实评价，并截取能看清星数与评价正文的图。
        </p>
      </ConsolePanel>

      <ConsoleSummaryGrid class="claim-summary">
        <div>
          <div class="summary-label">已领次数</div>
          <div class="summary-value">{{ claimUsage.count }} / {{ maxClaimsText }}</div>
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
    </template>

    <!--
      提交入口：**只要活动信息加载完成就渲染**，与总开关无关。

      这是本轮的核心修法。活动规则、汇总数字这些「没开放就没意义」的内容仍留在
      enabled 分支里（见上下两个 template），但「提交截图」这个面板必须始终可见 ——
      它是用户寻找的那个入口本身。关闭态下逐项 disabled + 顶部原因说明，
      用户能明确区分「有这个功能但没开放」与「这个站点没有这个功能」。
    -->
    <template v-if="loaded">
      <ConsolePanel
        title="提交截图"
        :description="entryDescription"
        data-submit-entry
      >
        <!-- 关闭态下在面板内部再放一条就地说明：用户滚到这里时不必回顶部找原因。 -->
        <a-alert
          v-if="!enabled"
          type="warning"
          show-icon
          class="status-alert"
          :message="closedReason"
          data-entry-closed-notice
        />

        <a-form layout="vertical" :model="form" @finish="submit">
          <a-form-item label="评价截图" :html-for="fieldId('screenshot')" required>
            <a-upload
              accept="image/png,image/jpeg"
              :disabled="entryDisabled"
              :before-upload="selectFile"
              :show-upload-list="false"
              :file-list="[]"
            >
              <a-button :id="fieldId('screenshot')" :disabled="entryDisabled">选择截图</a-button>
            </a-upload>
            <div class="hint">
              <span v-if="form.file">{{ form.file.name }}（{{ formatSize(form.file.size) }}）</span>
              <span v-else>支持 PNG / JPEG，单张不超过 5 MB；服务端会再解码校验一次。</span>
            </div>
          </a-form-item>
          <a-form-item label="评价链接（可选）" :html-for="fieldId('reviewUrl')">
            <a-input
              :id="fieldId('reviewUrl')"
              :value="form.reviewUrl"
              type="url"
              allow-clear
              placeholder="https://…"
              :disabled="entryDisabled"
              @update:value="updateReviewUrl"
            />
            <div class="hint">留空也可以提交，只是核对会慢一些。</div>
          </a-form-item>

          <p v-if="formError" class="form-error" role="alert">{{ formError }}</p>
          <p v-if="limitReached && enabled" class="form-error" role="alert">已达到每账号的领取上限，暂时无法继续提交。</p>

          <a-space wrap>
            <a-button
              type="primary"
              html-type="submit"
              :loading="submitting"
              :disabled="entryDisabled || !form.file"
              data-submit-button
            >
              提交审核
            </a-button>
            <!--
              「清空」只在「关闭态」或「提交中」禁用，**不**受 limitReached 影响。
              原实现是 `:disabled="submitting"` —— 达到上限时用户仍能清掉已选文件；
              若这里直接用 entryDisabled（含 limitReached），开启态的行为就被改了。
              关闭态下禁用是因为本来就没有可提交的内容。
            -->
            <a-button :disabled="!enabled || submitting" @click="resetForm">清空</a-button>
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
    </template>

    <template v-if="loaded && enabled">
      <ConsolePanel title="我的提交">
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
              <span v-else-if="column.key === 'source'">{{ storeLabel(record.source) }}</span>
              <a-tag v-else-if="column.key === 'status'" :color="claimStatusColor(record.status)">{{ claimStatusLabel(record.status) }}</a-tag>
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
import { computed, onBeforeUnmount, onMounted, ref, shallowReactive, shallowRef, useId, watch } from 'vue'
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
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

const MAX_UPLOAD_BYTES = 5 * 1024 * 1024
const ACCEPTED_TYPES = ['image/png', 'image/jpeg']
const ACCEPTED_EXTENSIONS = /\.(png|jpe?g)$/i
const storeLabel = source => ({ edge: 'Edge', chrome: 'Chrome' })[source] || '—'

const CLAIM_STATUS = {
  submitted: { label: '已提交', color: 'blue' },
  scraping: { label: '待审核', color: 'blue' },
  scored: { label: '待审核', color: 'blue' },
  pending_review: { label: '待人工复核', color: 'gold' },
  approved: { label: '已通过', color: 'green' },
  rejected: { label: '已拒绝', color: 'red' },
  below_threshold: { label: '待审核', color: 'blue' },
  clawed_back: { label: '已追缴', color: 'default' },
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
    const stores = ref([])
    const canClaim = ref(null)
    const selectedSource = ref('')
    const usage = ref({})
    const enabled = ref(false)

    const loadingClaims = ref(false)
    const claimsLoaded = ref(false)
    const claims = ref([])

    const form = shallowReactive({ file: null, reviewUrl: '' })
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
        pending: readNonnegativeNumber(usage.value.pendingCount) ?? list.filter(item => ['submitted', 'scraping', 'scored', 'pending_review', 'below_threshold'].includes(String(item?.status || '').toLowerCase())).length,
        rejected: rejectedCount ?? list.filter(item => String(item?.status || '').toLowerCase() === 'rejected').length,
      }
    })
    const amountLimited = computed(() => {
      const limit = readNonnegativeNumber(program.value.maxAmountPerUser)
      return limit !== null && limit > 0 && claimUsage.value.amount >= limit
    })
    const countLimited = computed(() => maxClaims.value !== null && maxClaims.value > 0 && claimUsage.value.count >= maxClaims.value)
    const limitReached = computed(() => canClaim.value === null ? (countLimited.value || amountLimited.value) : !canClaim.value)

    // ───────────── 关闭态文案与入口禁用 ─────────────
    //
    // The API has no schedule fields. A disabled switch cannot establish that
    // an activity has not started or has ended; describe only the actual state.
    const programReadable = computed(() => Boolean(
      String(program.value?.name || '').trim() || String(program.value?.storeUrl || '').trim(),
    ))
    const closedReason = computed(() => (programReadable.value
      ? '当前评价返现活动已暂停，暂时无法提交。管理员重新开放后，点击刷新即可继续。'
      : '当前站点未开放评价返现。管理员保存开启活动后，点击刷新即可在下方提交。'))
    const closedTitle = computed(() => (programReadable.value ? '评价返现活动已暂停' : '评价返现尚未开放'))

    // entryDisabled：关闭态、「提交中」、「达上限」三者之一即禁用。
    // 关闭态**优先**：它是最根本的原因，不该被「已上限」之类的文案盖住。
    const switchingSource = computed(() => Boolean(selectedSource.value && selectedSource.value !== program.value.source))
    const entryDisabled = computed(() => !enabled.value || submitting.value || limitReached.value || switchingSource.value)
    const entryDescription = computed(() => (enabled.value
      ? '必填 1 张截图（PNG / JPEG，≤ 5 MB）。评价链接可选，填写后有助于核对。'
      : '当前不可提交。开放后本区域即为提交入口，无需重新进入本页。'))

    const claimColumns = [
      { title: '提交号', dataIndex: 'id', key: 'id', width: 95 },
      { title: '商店', dataIndex: 'source', key: 'source', width: 100 },
      { title: '状态', dataIndex: 'status', key: 'status', width: 130 },
      { title: '提交时间', dataIndex: 'createdAt', key: 'createdAt', width: 175 },
    ]

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
        let res = await getReviewRebateProgram(selectedSource.value || 'active')
        if (!ownsFailure()) return false
        if (selectedSource.value && res?.status === 400 && isReviewRebateDisabledResponse(res)) {
          selectedSource.value = ''
          clearForm()
          res = await getReviewRebateProgram('active')
        }
        if (!ownsFailure()) return false
        // 总开关关闭（或活动被禁用）时后端返回 400「评价返现未启用」。
        // 这不是"加载失败"，而是**正常的未开放状态**：必须落成 enabled=false 的
        // 「功能未开启」提示，否则用户看到的是一个红色失败告警 + 重试按钮，
        // 点多少次都不会好 —— 那才是真的让人以为站点坏了。
        if (res?.status === 400 && isReviewRebateDisabledResponse(res)) {
          program.value = {}
          usage.value = {}
          stores.value = []
          canClaim.value = null
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
        stores.value = Array.isArray(body.stores) ? body.stores : []
        canClaim.value = typeof body.canClaim === 'boolean' ? body.canClaim : null
        usage.value = body.usage && typeof body.usage === 'object'
          ? body.usage
          : (programBody.usage && typeof programBody.usage === 'object' ? programBody.usage : {})
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
    let activeRefresh
    const loadAll = (afterCurrent) => {
      if (!canLoad()) return
      if (activeRefresh) {
        if (typeof afterCurrent !== 'function') return activeRefresh
        const previous = activeRefresh
        return previous.then(() => { if (afterCurrent()) return loadAll() })
      }
      const refresh = async () => {
        await loadProgram()
        if (!canLoad() || activeRefresh !== task) return
        if (loaded.value && enabled.value) await loadClaims()
      }
      loadingAll.value = true
      const task = refresh().finally(() => {
        if (activeRefresh !== task) return
        activeRefresh = undefined
        loadingAll.value = false
      })
      activeRefresh = task
      return task
    }

    function updateReviewUrl(value) {
      if (submitting.value) return
      form.reviewUrl = String(value ?? '')
      formError.value = ''
    }

    function selectFile(file) {
      if (!canLoad() || entryDisabled.value) return false
      formError.value = ''
      form.file = null
      const typeOk = ACCEPTED_TYPES.includes(String(file.type || '').toLowerCase())
        || ACCEPTED_EXTENSIONS.test(String(file.name || ''))
      if (!typeOk) {
        formError.value = '只支持 PNG / JPEG 截图'
      } else if (!(file.size > 0)) {
        formError.value = '截图内容为空，请重新选择'
      } else if (file.size > MAX_UPLOAD_BYTES) {
        formError.value = '截图超过 5 MB，无法提交'
      } else {
        form.file = file
      }
      return false
    }

    // 清空表单内容本身不加锁：提交成功后要在 submitting 仍为 true 时复位。
    function clearForm() {
      form.file = null
      form.reviewUrl = ''
      formError.value = ''
      submitWarning.value = ''
      clearSubmitFailure()
    }
    function resetForm() {
      if (submitting.value) return
      clearForm()
    }

    function selectSource(source) {
      if (loadingAll.value || submitting.value || program.value.source === source) return
      if (!stores.value.some(store => store.source === source)) return
      selectedSource.value = source
      clearForm()
      void loadAll()
    }

    const submit = async () => {
      if (!canLoad() || entryDisabled.value) return
      if (!form.file) { formError.value = '请先选择评价截图'; return }
      const link = form.reviewUrl.trim()
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
        const res = await submitReviewRebateClaim(form.file, link, program.value.source)
        if (!ownsFailure()) return
        if (res?.status === 200 || res?.status === 201) {
          clearForm()
          reloadAfter = true
        } else if (res?.status === 413) {
          formError.value = '截图超过服务端允许的大小'
        } else if (res?.status === 400) {
          formError.value = res?.message || res?.data?.message || '提交内容不符合要求，请核对后重试'
        } else if (res?.status === 409) {
          submitWarning.value = res?.message || '该评价可能已提交过，请先在下方的列表核对，勿重复提交。'
          reloadAfter = true
        } else {
          submitFailureOwner = ownsFailure
          submitFailure.value = ensureRequestFailure(res)
        }
      } catch (error) {
        if (ownsFailure()) { submitFailureOwner = ownsFailure; submitFailure.value = ensureRequestFailure(error) }
      } finally {
        if (owns()) submitting.value = false
      }
      // loadAll/loadClaims 在提交锁释放后再刷新，避免与提交态互相阻塞。
      if (reloadAfter && owns()) await loadAll(owns)
    }

    const retire = () => {
      programVersion += 1; claimsVersion += 1; submitVersion += 1
      activeRefresh = undefined
      program.value = {}; usage.value = {}; enabled.value = false; loaded.value = false
      stores.value = []; canClaim.value = null; selectedSource.value = ''
      claims.value = []; claimsLoaded.value = false; loadingClaims.value = false
      form.file = null; form.reviewUrl = ''; formError.value = ''
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
      loaded, enabled, program, stores, selectSource, storeLabel, loadingClaims, claimsLoaded, claims,
      form, submitting, formError, submitWarning,
      programFailure, programFailureContextKey, isCurrentProgramFailure,
      claimsFailure, claimsFailureContextKey, isCurrentClaimsFailure,
      submitFailure, submitFailureContextKey, isCurrentSubmitFailure,
      maxClaimsText, claimUsage, limitReached,
      programReadable, closedReason, closedTitle, entryDisabled, entryDescription,
      claimColumns, formatSize, formatDateTime, claimStatusLabel, claimStatusColor,
      loadAll, loadClaims, updateReviewUrl, selectFile, resetForm, submit,
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
.hint { margin: 8px 0 0; color: var(--console-muted); font-size: 12px; line-height: 1.7; }
.form-error { margin: 0 0 16px; color: var(--console-danger, #c13e41); font-size: 13px; }
.time-cell { font-variant-numeric: tabular-nums; }
</style>
