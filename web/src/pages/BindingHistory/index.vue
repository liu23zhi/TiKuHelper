<template>
  <div class="console-page binding-history-page">
    <ConsolePageHeading :title="pageTitle" :description="pageDescription">
      <template #actions>
        <a-button :loading="loading" :disabled="!canManage() || loading" @click="load">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert
      v-if="!canManage()"
      type="warning"
      show-icon
      class="page-alert"
      message="当前身份无绑定历史管理权限，请使用管理员账号登录。"
    />

    <template v-if="canManage()">
      <ConsoleSummaryGrid class="binding-summary">
        <div>
          <div class="summary-label">筛选结果</div>
          <div class="summary-value">{{ hasLoaded ? total : '--' }} 条</div>
        </div>
        <div>
          <div class="summary-label">当前生效</div>
          <div class="summary-value">{{ hasLoaded ? stateCount.active : '--' }} 条</div>
        </div>
        <div>
          <div class="summary-label">已作废</div>
          <div class="summary-value">{{ hasLoaded ? stateCount.voided : '--' }} 条</div>
        </div>
      </ConsoleSummaryGrid>

      <ConsolePanel title="筛选" :description="filterDescription">
        <a-form layout="inline" class="filter-form" :onFinish="reload">
          <a-form-item label="状态" :html-for="fieldId('state')">
            <a-select
              :id="fieldId('state')"
              :value="filters.state"
              :options="stateOptions"
              allow-clear
              placeholder="全部状态"
              class="filter-state"
              :disabled="loading || voiding"
              @update:value="value => setFilter('state', value)"
              @change="reload"
            />
          </a-form-item>
          <a-form-item label="释放原因" :html-for="fieldId('releaseReason')">
            <a-select
              :id="fieldId('releaseReason')"
              :value="filters.releaseReason"
              :options="releaseReasonOptions"
              allow-clear
              placeholder="全部原因"
              class="filter-reason"
              :disabled="loading || voiding"
              @update:value="value => setFilter('releaseReason', value)"
              @change="reload"
            />
          </a-form-item>
          <a-form-item label="用户 ID" :html-for="fieldId('userId')">
            <a-input
              :id="fieldId('userId')"
              :value="filters.userId"
              inputmode="numeric"
              allow-clear
              placeholder="精确账户 ID"
              class="filter-id"
              :disabled="loading || voiding"
              @update:value="value => setFilter('userId', value)"
              @press-enter="reload"
            />
          </a-form-item>
          <a-form-item label="绑定值" :html-for="fieldId('value')">
            <a-input
              :id="fieldId('value')"
              :value="filters.value"
              allow-clear
              :placeholder="valuePlaceholder"
              class="filter-value"
              :disabled="loading || voiding"
              @update:value="value => setFilter('value', value)"
              @press-enter="reload"
            />
            <div class="hint">按完整值精确匹配（服务端只比哈希）。留空则不按值筛选。</div>
          </a-form-item>
          <a-form-item>
            <a-space wrap>
              <a-button type="primary" html-type="submit" :loading="loading" :disabled="loading || voiding">查询</a-button>
              <a-button :disabled="loading || voiding" @click="resetFilters">重置筛选</a-button>
            </a-space>
          </a-form-item>
        </a-form>
      </ConsolePanel>

      <ConsolePanel :title="listTitle" :description="listDescription">
        <section v-if="loadFailure" class="status-alert">
          <RequestFailureAlert
            :key="failureContextKey"
            :failure="loadFailure"
            :message="hasLoaded ? '列表刷新失败，页面显示的仍是上次读取的结果。' : '列表尚未加载成功，请重试读取。'"
            :retryable="true"
            :busy="loading || voiding"
            :context-key="failureContextKey"
            :is-current="isCurrentFailure"
            @retry="load"
          />
        </section>
        <a-alert v-else-if="actionWarning" type="warning" show-icon :message="actionWarning" class="status-alert" />

        <ConsoleTableShell v-if="rows.length" :label="listTitle">
          <a-table
            table-layout="fixed"
            :data-source="rows"
            :columns="columns"
            :loading="loading"
            :pagination="pagination"
            row-key="id"
            size="small"
            :scroll="{ x: sumTableColumnWidths(columns) }"
            @change="onTableChange"
          >
            <template #bodyCell="{ column, record }">
              <span v-if="column.key === 'id'">{{ record.id ?? '-' }}</span>
              <span v-else-if="column.key === 'user'">{{ buildUserName(record.usernameSnapshot, record.userId) }}</span>
              <span v-else-if="column.key === 'valueMasked'" class="masked-cell">{{ record.valueMasked || '—' }}</span>
              <a-tag v-else-if="column.key === 'state'" :color="stateColor(record.state)">{{ stateLabel(record.state) }}</a-tag>
              <span v-else-if="column.key === 'releaseReason'">{{ releaseReasonLabel(record.releaseReason) }}</span>
              <span v-else-if="column.key === 'releaseSource'">{{ releaseSourceLabel(record.releaseSource) }}</span>
              <span v-else-if="column.key === 'boundAt'" class="time-cell">{{ formatDateTime(record.boundAt) }}</span>
              <span v-else-if="column.key === 'releasedAt'" class="time-cell">{{ formatDateTime(record.releasedAt) }}</span>
              <span v-else-if="column.key === 'voidedAt'" class="time-cell">{{ formatDateTime(record.voidedAt) }}</span>
              <a-space v-else-if="column.key === 'action'" wrap>
                <a-button type="link" :disabled="loading || voiding" @click="openDetail(record)">详情</a-button>
                <a-button
                  v-if="isVoidable(record)"
                  type="link"
                  danger
                  :disabled="loading || voiding"
                  @click="openVoidDialog(record)"
                >作废</a-button>
                <span v-else class="action-disabled">{{ stateLabel(record.state) }}</span>
              </a-space>
            </template>
          </a-table>
        </ConsoleTableShell>
        <a-empty v-else-if="hasLoaded && !loading" :description="emptyDescription" />
      </ConsolePanel>
    </template>

    <!-- ── 详情弹窗 ───────────────────────────────────────────────── -->
    <a-modal
      v-if="detailVisible"
      :key="detailKey"
      :open="detailVisible"
      title="绑定记录详情"
      :footer="null"
      :closable="true"
      @cancel="closeDetail"
    >
      <a-descriptions :column="1" size="small" bordered>
        <a-descriptions-item label="记录号">{{ detailRecord.id ?? '—' }}</a-descriptions-item>
        <a-descriptions-item label="用户">{{ buildUserName(detailRecord.usernameSnapshot, detailRecord.userId) }}</a-descriptions-item>
        <a-descriptions-item :label="kindLabel">{{ detailRecord.valueMasked || '—' }}</a-descriptions-item>
        <a-descriptions-item label="状态">
          <a-tag :color="stateColor(detailRecord.state)">{{ stateLabel(detailRecord.state) }}</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="写入来源">{{ releaseSourceLabel(detailRecord.source) }}</a-descriptions-item>
        <a-descriptions-item label="绑定时间">{{ formatDateTime(detailRecord.boundAt) }}</a-descriptions-item>
        <a-descriptions-item label="释放时间">{{ formatDateTime(detailRecord.releasedAt) }}</a-descriptions-item>
        <a-descriptions-item label="释放原因">{{ releaseReasonLabel(detailRecord.releaseReason) }}</a-descriptions-item>
        <a-descriptions-item v-if="detailRecord.voidedAt" label="作废时间">{{ formatDateTime(detailRecord.voidedAt) }}</a-descriptions-item>
        <a-descriptions-item v-if="detailRecord.voidReason" label="作废理由">{{ detailRecord.voidReason }}</a-descriptions-item>
        <a-descriptions-item label="创建时间">{{ formatDateTime(detailRecord.createdAt) }}</a-descriptions-item>
      </a-descriptions>
    </a-modal>

    <!-- ── 作废弹窗 ───────────────────────────────────────────────── -->
    <a-modal
      v-if="voidDialogVisible"
      :key="voidDialogKey"
      :open="voidDialogVisible"
      title="作废这条绑定历史？"
      ok-text="确认作废"
      cancel-text="取消"
      :confirm-loading="voiding"
      :closable="!voiding"
      :mask-closable="!voiding"
      :keyboard="!voiding"
      :ok-button-props="{ disabled: voiding || !!voidValidation, danger: true }"
      @ok="submitVoid"
      @cancel="closeVoidDialog"
    >
      <a-descriptions :column="1" size="small" bordered class="void-summary">
        <a-descriptions-item label="记录号">{{ voidRecord.id ?? '—' }}</a-descriptions-item>
        <a-descriptions-item label="用户">{{ buildUserName(voidRecord.usernameSnapshot, voidRecord.userId) }}</a-descriptions-item>
        <a-descriptions-item :label="kindLabel">{{ voidRecord.valueMasked || '—' }}</a-descriptions-item>
        <a-descriptions-item label="状态">
          <a-tag :color="stateColor(voidRecord.state)">{{ stateLabel(voidRecord.state) }}</a-tag>
        </a-descriptions-item>
      </a-descriptions>
      <a-alert
        type="warning"
        show-icon
        class="void-hint"
        message="作废不物理删除。该行会保留并可回看，但不再参与任何绑定判定。"
      />
      <a-form layout="vertical" :disabled="voiding">
        <a-form-item :label="'作废理由（必填）'" :html-for="fieldId('voidReason')" required>
          <a-textarea
            :id="fieldId('voidReason')"
            :value="voidReason"
            :rows="3"
            :maxlength="255"
            show-count
            :disabled="voiding"
            placeholder="说明为什么这条历史不该再参与判定，会写进审计"
            @update:value="updateVoidReason"
          />
        </a-form-item>
      </a-form>
      <p v-if="voidValidation" class="form-error" role="alert">{{ voidValidation }}</p>
    </a-modal>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, reactive, ref, shallowRef, useId, watch } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import ConsoleTableShell from '@/components/ConsoleTableShell.vue'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import { getBindingHistory, normalizeBindingKind, voidBindingRecord } from '@/api/UserBindingApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

// 微信 / 手机号两个侧边栏页面**共用这一个组件**，只有 route.meta.bindingKind 不同
// （task_doc/70 §3.6 明确「不要写两份」）。与 LocationCache 的 ip/phone 同一套做法：
// 两处 route 注册指向同一个 component，用 meta 区分，页面内所有文案/占位符都从 kind 派生。

const KIND_LABELS = { wechat: '微信', phone: '手机号' }
const STATE_LABELS = { active: '生效中', released: '已释放', voided: '已作废' }
const STATE_COLORS = { active: 'green', released: 'blue', voided: 'default' }
const RELEASE_REASON_LABELS = { rebind: '换绑释放', closure: '注销释放', admin: '管理员释放' }
const RELEASE_SOURCE_LABELS = {
  user_unbind: '用户解绑',
  admin_clear: '管理员清除',
  force_reassign: '强制改派',
  account_close: '账号注销',
  admin_delete: '管理员删除',
  register: '注册写入',
  wechat_register: '微信注册',
  user_bind: '用户绑定',
  admin_bind: '管理员绑定',
  admin_force_reassign: '管理员强制改派',
  admin_manual_backfill: '管理员补录',
}

function readPositiveInt(value) {
  if (!['number', 'string'].includes(typeof value)) return null
  const text = String(value).trim()
  if (text === '') return null
  const number = Number(text)
  return Number.isInteger(number) && number > 0 ? number : null
}

const emptyFilters = () => ({ state: undefined, releaseReason: undefined, userId: '', value: '' })

export default {
  name: 'BindingHistoryPage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell, RequestFailureAlert },
  setup() {
    const route = useRoute()
    const prefix = 'binding-history-' + useId()
    const fieldId = field => `${prefix}-${field}`
    const { currentUser } = useCurrentUserDisplay()

    // ── 为什么不能用 route.path === mountedPath 做授权判据（2026-11-14 实测缺陷）──────
    //
    // 本页被 **两条路由共用同一个组件**（见 router.js，70 号文档点名要求共用，
    // 避免两套文案与两处重复逻辑）：
    //     wechat-bindings  (meta.bindingKind='wechat')
    //     phone-bindings   (meta.bindingKind='phone')
    // 共用 component 意味着 Vue 在两者间切换时**复用同一实例、不重跑 setup**
    // （实测 uid 不变：11 → 11）。setup 期间固化的 mountedPath 永远停在首次挂载的那条路径，
    // 换到另一条后 `route.path === mountedPath` 恒 false ⇒ canManage() 恒 false ⇒
    // 页面顶部出现「当前身份无绑定历史管理权限，请使用管理员账号登录。」
    // 管理员明明已登录，却被判成无权限。
    //
    // 正确判据是「当前路由是否仍是本页自己的路由之一」，用 route.name 白名单表达。
    // 注意这里**不能**用 route.meta.bindingKind 来判断：那个字段回答的是"显示哪一类数据"，
    // 不是"我还在不在本页" —— 反例是把两条路由的 meta 都写错时它照样"通过"。
    //
    // 新增第三条绑定类路由（如 qq-bindings）时必须把 name 加进这个集合。
    const OWN_ROUTE_NAMES = Object.freeze(['wechat-bindings', 'phone-bindings'])

    const stillOwnRoute = () => {
      const name = String(route?.name || '')
      if (name) return OWN_ROUTE_NAMES.includes(name)
      const matched = route?.matched || []
      return matched.some(record => OWN_ROUTE_NAMES.includes(String(record?.name || '')))
    }

    // kind 从路由 meta 取，且必须是白名单内的值 —— 取不到就当作 wechat 会让
    // 「手机号页显示微信数据」这种最坏情况静默发生，所以退化成空串并在
    // canManage() 里拦掉（页面显示告警而不是错误的列表）。
    const kind = computed(() => normalizeBindingKind(route.meta?.bindingKind))
    const kindLabel = computed(() => KIND_LABELS[kind.value] || '绑定值')
    const pageTitle = computed(() => (kind.value ? `${kindLabel.value}绑定历史` : '绑定历史'))
    const pageDescription = computed(() => (kind.value
      ? `${kindLabel.value}的历史绑定与释放记录。作废的行保留可回看，但不再参与任何绑定判定。`
      : '路由缺少 bindingKind，无法确定要显示哪一类绑定历史。'))
    const listTitle = computed(() => `${kindLabel.value}绑定记录`)
    const listDescription = computed(() => '按写入时间倒序。绑定值已脱敏，服务端不下发明文。')
    const filterDescription = computed(() => '状态与释放原因是精确匹配；用户 ID 是账户 ID；绑定值按完整值精确匹配。')
    const valuePlaceholder = computed(() => (kind.value === 'phone' ? '完整手机号' : '完整微信 OpenID'))
    const emptyDescription = computed(() => `暂无${kindLabel.value}绑定记录`)

    const rows = ref([])
    const filters = reactive(emptyFilters())
    const appliedFilters = ref({})
    const loading = ref(false)
    const hasLoaded = ref(false)
    const total = ref(0)
    const stateCount = reactive({ active: 0, voided: 0 })
    const actionWarning = ref('')
    const loadFailure = shallowRef(null)
    const failureContextKey = ref(0)
    let failureOwner = () => false

    const voiding = ref(false)
    const voidDialogVisible = ref(false)
    const voidDialogKey = ref(0)
    const voidRecord = ref({})
    const voidReason = ref('')
    const voidValidation = ref('')

    const detailVisible = ref(false)
    const detailKey = ref(0)
    const detailRecord = ref({})

    const pagination = reactive({
      current: 1,
      pageSize: 20,
      total: 0,
      showSizeChanger: true,
      pageSizeOptions: ['10', '20', '50', '100'],
      showTotal: value => `共 ${value} 条`,
    })

    let alive = true
    let readVersion = 0
    let writeVersion = 0
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()

    const validAdmin = () => {
      const user = currentUser.value
      const id = Number(user?.id)
      return Number.isSafeInteger(id) && id > 0 && id <= 2147483647 && isAdminUser(user)
        && !['disabled', '0'].includes(String(user?.status ?? 'enabled'))
    }
    const canManage = () => alive && stillOwnRoute()
      && getCurrentUserLogoutEpoch() === mountedLogoutEpoch && validAdmin() && Boolean(kind.value)

    const stateOptions = Object.keys(STATE_LABELS).map(value => ({ value, label: STATE_LABELS[value] }))
    const releaseReasonOptions = Object.keys(RELEASE_REASON_LABELS).map(value => ({ value, label: RELEASE_REASON_LABELS[value] }))

    const columns = [
      { title: '记录号', dataIndex: 'id', key: 'id', width: 90 },
      { title: '用户', dataIndex: 'user', key: 'user', width: 180 },
      { title: kindLabel, dataIndex: 'valueMasked', key: 'valueMasked', width: 190 },
      { title: '状态', dataIndex: 'state', key: 'state', width: 110 },
      { title: '释放原因', dataIndex: 'releaseReason', key: 'releaseReason', width: 130 },
      { title: '释放来源', dataIndex: 'releaseSource', key: 'releaseSource', width: 130 },
      { title: '绑定时间', dataIndex: 'boundAt', key: 'boundAt', width: 175 },
      { title: '释放时间', dataIndex: 'releasedAt', key: 'releasedAt', width: 175 },
      { title: '作废时间', dataIndex: 'voidedAt', key: 'voidedAt', width: 175 },
      { title: '操作', key: 'action', width: 140 },
    ]

    function stateLabel(value) {
      return STATE_LABELS[String(value || '').trim().toLowerCase()] || value || '-'
    }
    function stateColor(value) {
      return STATE_COLORS[String(value || '').trim().toLowerCase()] || 'default'
    }
    function releaseReasonLabel(value) {
      const key = String(value || '').trim().toLowerCase()
      return RELEASE_REASON_LABELS[key] || value || '—'
    }
    function releaseSourceLabel(value) {
      const key = String(value || '').trim().toLowerCase()
      return RELEASE_SOURCE_LABELS[key] || value || '—'
    }
    // 只有 released 行可作废：active 行作废会让 users.phone 仍写着该值而唯一索引
    // 已被放开（后端会拒绝，这里先不提供入口，避免用户点了才被拒）。
    function isVoidable(record) {
      return String(record?.state || '').trim().toLowerCase() === 'released'
    }
    function buildUserName(username, userId) {
      const name = String(username || '').trim()
      const id = readPositiveInt(userId)
      if (name && id) return `${name}（#${id}）`
      if (name) return name
      if (id) return `#${id}`
      return '—'
    }
    function formatDateTime(value) {
      if (!value) return '—'
      const date = new Date(value)
      return Number.isFinite(date.getTime())
        ? date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
        : '—'
    }
    function setFilter(field, value) {
      if (!canManage() || loading.value || voiding.value || !Object.hasOwn(filters, field)) return
      filters[field] = value
    }

    const clearLoadFailure = () => {
      loadFailure.value = null
      failureOwner = () => false
      failureContextKey.value += 1
    }
    const isCurrentFailure = (source, context) => failureOwner() && Boolean(source)
      && source === loadFailure.value && context === failureContextKey.value

    const buildParams = () => {
      const params = { page: pagination.current, pageSize: pagination.pageSize }
      const scope = appliedFilters.value
      if (scope.state) params.state = scope.state
      if (scope.releaseReason) params.releaseReason = scope.releaseReason
      const userId = readPositiveInt(scope.userId)
      if (userId) params.userId = userId
      const value = String(scope.value || '').trim()
      if (value) params.value = value
      return params
    }

    const load = async () => {
      if (!canManage()) return false
      const ticket = ++readVersion
      const owns = () => canManage() && ticket === readVersion
      clearLoadFailure()
      const context = failureContextKey.value
      const ownsFailure = () => owns() && context === failureContextKey.value
      loading.value = true
      try {
        const res = await getBindingHistory(kind.value, buildParams())
        if (!ownsFailure()) return false
        const body = res?.data
        const list = Array.isArray(body?.items) ? body.items : []
        if (res?.status !== 200 || !Array.isArray(body?.items)) {
          failureOwner = ownsFailure; loadFailure.value = ensureRequestFailure(res); return false
        }
        rows.value = list.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
        total.value = Number(body.total) || 0
        pagination.total = total.value
        actionWarning.value = ''
        hasLoaded.value = true
        // 摘要里的「生效中 / 已作废」只在未按状态筛选时有意义：筛了状态就
        // 只剩那一类，两个数字会误导成「全站只有这么多」。
        if (!appliedFilters.value.state) {
          stateCount.active = rows.value.filter(row => String(row.state).toLowerCase() === 'active').length
          stateCount.voided = rows.value.filter(row => String(row.state).toLowerCase() === 'voided').length
        } else {
          stateCount.active = 0
          stateCount.voided = 0
        }
        return true
      } catch (error) {
        if (ownsFailure()) { failureOwner = ownsFailure; loadFailure.value = ensureRequestFailure(error) }
        return false
      } finally {
        if (owns()) loading.value = false
      }
    }

    const reload = () => {
      if (!canManage() || loading.value || voiding.value) return
      if (filters.userId && !readPositiveInt(filters.userId)) {
        actionWarning.value = '用户 ID 必须是正整数；留空则不按用户筛选。'
        return
      }
      appliedFilters.value = { ...filters }
      pagination.current = 1
      load()
    }
    const resetFilters = () => {
      if (!canManage() || loading.value || voiding.value) return
      Object.assign(filters, emptyFilters())
      appliedFilters.value = {}
      pagination.current = 1
      actionWarning.value = ''
      load()
    }
    const onTableChange = (page) => {
      if (!canManage() || loading.value || voiding.value) return
      pagination.current = Number(page?.current) || 1
      pagination.pageSize = Number(page?.pageSize) || 20
      load()
    }

    // ── 详情 ────────────────────────────────────────────────────
    const openDetail = record => {
      if (!canManage()) return
      detailRecord.value = { ...record }
      detailKey.value += 1
      detailVisible.value = true
    }
    const closeDetail = () => { detailVisible.value = false }

    // ── 作废 ────────────────────────────────────────────────────
    const openVoidDialog = record => {
      if (!canManage() || voiding.value || !isVoidable(record)) return
      voidRecord.value = { ...record }
      voidReason.value = ''
      voidValidation.value = ''
      voidDialogKey.value += 1
      voidDialogVisible.value = true
    }
    const closeVoidDialog = () => { if (!voiding.value) voidDialogVisible.value = false }
    const updateVoidReason = value => {
      if (voiding.value) return
      voidReason.value = String(value ?? '')
      voidValidation.value = ''
    }
    const submitVoid = async () => {
      if (!canManage() || voiding.value) return
      const reason = String(voidReason.value || '').trim()
      // 后端要求 ≥2 字符；前端先拦一次给出具体提示，省一次 400 往返。
      if ([...reason].length < 2) {
        voidValidation.value = '请填写作废理由（至少 2 个字符）。'
        return
      }
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      voiding.value = true
      try {
        const res = await voidBindingRecord(voidRecord.value.id, reason)
        if (!owns()) return
        if (res?.status !== 200) {
          voidValidation.value = res?.status === 409
            ? '该记录已作废或状态已变更，请刷新后核对。'
            : '作废请求未完成，请核对后再操作。'
          return
        }
        voidDialogVisible.value = false
        message.success('已作废')
        await load()
      } catch (error) {
        if (owns()) voidValidation.value = ensureRequestFailure(error)?.message || '作废请求未完成，请核对后再操作。'
      } finally {
        if (owns()) voiding.value = false
      }
    }

    onMounted(() => { if (canManage()) load() })
    onBeforeUnmount(() => { alive = false; readVersion += 1; writeVersion += 1 })

    // 路由切换（微信页 ↔ 手机号页）时组件被复用，kind 变了必须重读，
    // 否则会出现「切到手机号页但列表还是微信的」。
    watch(kind, (next, previous) => {
      if (next === previous) return
      rows.value = []
      hasLoaded.value = false
      pagination.current = 1
      appliedFilters.value = { ...filters }
      if (canManage()) load()
    })

    return {
      fieldId, canManage, loading, kind, kindLabel, pageTitle, pageDescription,
      listTitle, listDescription, filterDescription, valuePlaceholder, emptyDescription,
      rows, filters, hasLoaded, total, stateCount, actionWarning,
      loadFailure, failureContextKey, isCurrentFailure,
      columns, pagination, stateOptions, releaseReasonOptions,
      stateLabel, stateColor, releaseReasonLabel, releaseSourceLabel, buildUserName, formatDateTime,
      setFilter, reload, resetFilters, onTableChange, load,
      detailVisible, detailKey, detailRecord, openDetail, closeDetail,
      voiding, voidDialogVisible, voidDialogKey, voidRecord, voidReason, voidValidation,
      openVoidDialog, closeVoidDialog, updateVoidReason, submitVoid,
      isVoidable,
      sumTableColumnWidths,
    }
  },
}
</script>

<style scoped>
.page-alert, .status-alert { margin-bottom: 16px; }
.binding-summary { margin-bottom: 16px; }
.filter-form { margin-bottom: 8px; }
.filter-state, .filter-reason { width: 150px; }
.filter-id { width: 140px; }
.filter-value { width: 220px; }
.hint { margin-top: 4px; color: var(--console-muted); font-size: 12px; line-height: 1.6; }
.form-error { margin: 8px 0 0; color: var(--console-danger, #c13e41); font-size: 13px; }
.masked-cell, .time-cell { font-variant-numeric: tabular-nums; }
.action-disabled { color: var(--console-muted); font-size: 12px; }
.void-summary { margin-bottom: 12px; }
.void-hint { margin-bottom: 12px; }
</style>
