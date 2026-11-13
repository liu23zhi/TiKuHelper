<template>
  <div class="console-page blacklist-page">
    <ConsolePageHeading
      title="黑名单管理"
      description="按 IP / 手机号 / 微信 / 地区拉黑请求来源。规则写入后立即生效；命中记录用于解释「为什么被拦」并判断规则是否误伤。"
    >
      <template #actions>
        <a-button :loading="loadingEntries" :disabled="!canManage() || loadingEntries || writing" @click="loadEntries">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert
      v-if="!canManage()"
      type="warning"
      show-icon
      class="page-alert"
      message="当前身份无黑名单管理权限，请使用管理员账号登录。"
    />

    <template v-if="canManage()">
      <ConsoleSummaryGrid class="blacklist-summary">
        <div>
          <div class="summary-label">筛选结果</div>
          <div class="summary-value">{{ hasLoadedEntries ? entriesTotal : '--' }} 条</div>
        </div>
        <div>
          <div class="summary-label">命中记录</div>
          <div class="summary-value">{{ hasLoadedHits ? hitsTotal : '--' }} 条</div>
        </div>
        <div>
          <div class="summary-label">
            <label :id="fieldId('blacklistEnabledLabel')" :for="fieldId('blacklistEnabled')">黑名单总开关</label>
          </div>
          <div class="summary-value">
            <a-switch
              :id="fieldId('blacklistEnabled')"
              :checked="blacklistEnabled"
              :loading="loadingSwitch"
              :disabled="!canManage()"
              :aria-labelledby="fieldId('blacklistEnabledLabel')"
              v-on="{ 'update:checked': toggleSwitch }"
            />
            <span class="summary-switch-state">{{ blacklistEnabled ? '已开启' : '已关闭' }}</span>
          </div>
          <div v-if="switchError" class="summary-switch-error" role="alert">{{ switchError }}</div>
        </div>
      </ConsoleSummaryGrid>

      <a-tabs v-model:activeKey="activeTab" class="blacklist-tabs">
        <a-tab-pane key="entries" tab="黑名单规则">
          <ConsolePanel title="筛选" description="关键词只匹配脱敏值与拉黑原因，不匹配规则原文。">
            <a-form layout="inline" class="filter-form" :on-finish="reloadEntries">
              <a-form-item label="类型" :html-for="fieldId('kind')">
                <a-select
                  :id="fieldId('kind')"
                  :value="entryFilters.kind"
                  :options="kindOptions"
                  allow-clear
                  placeholder="全部类型"
                  class="filter-kind"
                  :disabled="loadingEntries || writing"
                  @update:value="value => setEntryFilter('kind', value)"
                  @change="reloadEntries"
                />
              </a-form-item>
              <a-form-item label="状态" :html-for="fieldId('enabled')">
                <a-select
                  :id="fieldId('enabled')"
                  :value="entryFilters.enabled"
                  :options="enabledOptions"
                  allow-clear
                  placeholder="全部状态"
                  class="filter-enabled"
                  :disabled="loadingEntries || writing"
                  @update:value="value => setEntryFilter('enabled', value)"
                  @change="reloadEntries"
                />
              </a-form-item>
              <a-form-item label="关键词" :html-for="fieldId('keyword')">
                <a-input
                  :id="fieldId('keyword')"
                  :value="entryFilters.keyword"
                  allow-clear
                  placeholder="脱敏值 / 拉黑原因"
                  class="filter-keyword"
                  :disabled="loadingEntries || writing"
                  @update:value="value => setEntryFilter('keyword', value)"
                  @press-enter="reloadEntries"
                />
              </a-form-item>
              <a-form-item>
                <a-space wrap>
                  <a-button type="primary" html-type="submit" :loading="loadingEntries" :disabled="loadingEntries || writing">查询</a-button>
                  <a-button :disabled="loadingEntries || writing" @click="resetEntryFilters">重置筛选</a-button>
                  <a-button type="primary" ghost :disabled="loadingEntries || writing" @click="openCreate">新增规则</a-button>
                </a-space>
              </a-form-item>
            </a-form>

            <p v-if="entryFilterError" class="filter-error" role="alert">{{ entryFilterError }}</p>
            <a-alert v-if="entryError" type="error" show-icon :message="entryError" class="status-alert" />
          </ConsolePanel>

          <ConsolePanel title="规则列表" :description="entryScopeDescription">
            <ConsoleTableShell label="黑名单规则">
              <a-table
                table-layout="fixed"
                :data-source="entries"
                :columns="entryColumns"
                :loading="loadingEntries"
                :pagination="entryPagination"
                row-key="id"
                :scroll="{ x: sumTableColumnWidths(entryColumns) }"
                :locale="{ emptyText: entryEmptyText }"
                @change="onEntryTableChange"
              >
                <template #bodyCell="{ column, record }">
                  <span v-if="column.key === 'id'">{{ record.id ?? '-' }}</span>
                  <a-tag v-else-if="column.key === 'kind'" :color="kindColor(record.kind)">{{ kindLabel(record.kind) }}</a-tag>
                  <span v-else-if="column.key === 'valueMasked'" class="masked-cell">{{ record.valueMasked || '—' }}</span>
                  <span v-else-if="column.key === 'matchType'">{{ matchTypeLabel(record.matchType) }}</span>
                  <span v-else-if="column.key === 'reason'" class="reason-cell">{{ record.reason || '—' }}</span>
                  <a-switch
                    v-else-if="column.key === 'enabled'"
                    :checked="Boolean(record.enabled)"
                    :disabled="writing || loadingEntries"
                    checked-children="启用"
                    un-checked-children="停用"
                    @change="checked => toggleEnabled(record, checked)"
                  />
                  <span v-else-if="column.key === 'expiresAt'">{{ formatDateTime(record.expiresAt) }}</span>
                  <span v-else-if="column.key === 'createdAt'" class="time-cell">{{ formatDateTime(record.createdAt) }}</span>
                  <template v-else-if="column.key === 'action'">
                    <a-space wrap>
                      <a-button
                        type="link"
                        :disabled="writing || loadingEntries"
                        @click="openHitsFor(record)"
                      >命中记录</a-button>
                      <a-button
                        type="link"
                        danger
                        :disabled="writing || loadingEntries"
                        @click="confirmDelete(record)"
                      >删除</a-button>
                    </a-space>
                  </template>
                </template>
              </a-table>
            </ConsoleTableShell>
          </ConsolePanel>
        </a-tab-pane>

        <a-tab-pane key="hits" tab="命中记录">
          <ConsolePanel title="筛选" description="命中记录只保存脱敏值；clientIp 精确匹配，不做模糊搜索。">
            <a-form layout="inline" class="filter-form" :on-finish="reloadHits">
              <a-form-item label="规则 ID" :html-for="fieldId('entryId')">
                <a-input
                  :id="fieldId('entryId')"
                  :value="hitFilters.entryId"
                  inputmode="numeric"
                  allow-clear
                  placeholder="精确规则 ID"
                  class="filter-id"
                  :disabled="loadingHits"
                  @update:value="value => setHitFilter('entryId', value)"
                  @press-enter="reloadHits"
                />
              </a-form-item>
              <a-form-item label="来源 IP" :html-for="fieldId('clientIp')">
                <a-input
                  :id="fieldId('clientIp')"
                  :value="hitFilters.clientIp"
                  allow-clear
                  placeholder="精确 IP"
                  class="filter-ip"
                  :disabled="loadingHits"
                  @update:value="value => setHitFilter('clientIp', value)"
                  @press-enter="reloadHits"
                />
              </a-form-item>
              <a-form-item label="处置" :html-for="fieldId('action')">
                <a-select
                  :id="fieldId('action')"
                  :value="hitFilters.action"
                  :options="actionOptions"
                  allow-clear
                  placeholder="全部处置"
                  class="filter-action"
                  :disabled="loadingHits"
                  @update:value="value => setHitFilter('action', value)"
                  @change="reloadHits"
                />
              </a-form-item>
              <a-form-item>
                <a-space wrap>
                  <a-button type="primary" html-type="submit" :loading="loadingHits" :disabled="loadingHits">查询</a-button>
                  <a-button :disabled="loadingHits" @click="resetHitFilters">重置筛选</a-button>
                </a-space>
              </a-form-item>
            </a-form>

            <p v-if="hitFilterError" class="filter-error" role="alert">{{ hitFilterError }}</p>
            <a-alert v-if="hitError" type="error" show-icon :message="hitError" class="status-alert" />
          </ConsolePanel>

          <ConsolePanel title="命中列表" :description="hitScopeDescription">
            <ConsoleTableShell label="黑名单命中记录">
              <a-table
                table-layout="fixed"
                class="hits-table"
                :data-source="hits"
                :columns="hitColumns"
                :loading="loadingHits"
                :pagination="hitPagination"
                row-key="id"
                :scroll="{ x: sumTableColumnWidths(hitColumns) }"
                :locale="{ emptyText: hitEmptyText }"
                @change="onHitTableChange"
              >
                <template #bodyCell="{ column, record }">
                  <span v-if="column.key === 'entryId'">{{ record.entryId ?? '-' }}</span>
                  <a-tag v-else-if="column.key === 'kind'" :color="kindColor(record.kind)">{{ kindLabel(record.kind) }}</a-tag>
                  <span v-else-if="column.key === 'valueMasked'" class="masked-cell">{{ record.valueMasked || '—' }}</span>
                  <span v-else-if="column.key === 'clientIp'" class="masked-cell">{{ record.clientIp || '—' }}</span>
                  <span v-else-if="column.key === 'path'" class="path-cell">{{ record.path || '—' }}</span>
                  <a-tag v-else-if="column.key === 'action'" :color="actionColor(record.action)">{{ actionLabel(record.action) }}</a-tag>
                  <span v-else-if="column.key === 'hitCount'" class="count-cell">{{ record.hitCount ?? 1 }}</span>
                  <span v-else-if="column.key === 'createdAt'" class="time-cell">{{ formatDateTime(record.createdAt) }}</span>
                </template>
              </a-table>
            </ConsoleTableShell>
          </ConsolePanel>
        </a-tab-pane>
      </a-tabs>

      <a-modal
        :open="createVisible"
        :title="'新增黑名单规则'"
        :confirm-loading="writing"
        ok-text="保存"
        cancel-text="取消"
        :mask-closable="false"
        @cancel="closeCreate"
        @ok="submitCreate"
      >
        <a-form layout="vertical" class="create-form">
          <a-form-item label="类型" required>
            <a-select
              :value="draft.kind"
              :options="kindOptions"
              :disabled="writing"
              @update:value="value => setDraft('kind', value)"
            />
          </a-form-item>
          <a-form-item label="匹配值" required>
            <a-input
              :value="draft.pattern"
              :disabled="writing"
              :placeholder="patternPlaceholder"
              @update:value="value => setDraft('pattern', value)"
            />
            <p class="field-hint">支持精确 IP、CIDR（203.0.113.0/24）与 IPv4 前缀（203.0.113.）；手机号按 11 位大陆号码归一化。</p>
          </a-form-item>
          <a-form-item label="匹配方式">
            <a-select
              :value="draft.matchType"
              :options="matchTypeOptions"
              :disabled="writing"
              @update:value="value => setDraft('matchType', value)"
            />
          </a-form-item>
          <a-form-item label="拉黑原因" required>
            <a-input
              :value="draft.reason"
              :disabled="writing"
              placeholder="会展示给被拦用户，请不要写规则边界"
              @update:value="value => setDraft('reason', value)"
            />
          </a-form-item>
          <a-form-item label="过期时间">
            <a-input
              :value="draft.expiresAt"
              :disabled="writing"
              placeholder="留空 = 永久；也可填 2026-11-11"
              @update:value="value => setDraft('expiresAt', value)"
            />
          </a-form-item>
          <a-form-item label="创建后立即启用">
            <a-switch :checked="draft.enabled" :disabled="writing" @change="checked => setDraft('enabled', checked)" />
          </a-form-item>
        </a-form>
        <p v-if="createError" class="filter-error" role="alert">{{ createError }}</p>
      </a-modal>
    </template>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, reactive, ref, useId, watch } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import Swal from 'sweetalert2'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import ConsoleTableShell from '@/components/ConsoleTableShell.vue'
import {
  createBlacklistEntry,
  deleteBlacklistEntry,
  getBlacklistEntries,
  getBlacklistHits,
  setBlacklistEntryEnabled,
} from '@/api/BlacklistApi'
import { getSystemSettings, updateSystemSettings } from '@/api/TikuComponentApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

const KIND_LABELS = { ip: 'IP', phone: '手机号', wechat: '微信', region: '地区' }
const KIND_COLORS = { ip: 'blue', phone: 'purple', wechat: 'green', region: 'orange' }
const MATCH_TYPE_LABELS = { exact: '精确', cidr: 'CIDR', prefix: '前缀' }
const ACTION_LABELS = { blocked: '已拦截', logged_only: '仅留痕' }
const ACTION_COLORS = { blocked: 'red', logged_only: 'gold' }

const emptyEntryFilters = () => ({ kind: undefined, enabled: undefined, keyword: '' })
const emptyHitFilters = () => ({ entryId: '', clientIp: '', action: undefined })
const emptyDraft = () => ({ kind: 'ip', pattern: '', matchType: '', reason: '', expiresAt: '', enabled: true })

function readNonnegativeInt(value) {
  if (!['number', 'string'].includes(typeof value) || String(value).trim() === '') return null
  const number = Number(value)
  return Number.isInteger(number) && number >= 0 ? number : null
}

export default {
  name: 'BlacklistPage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell },
  setup() {
    const route = useRoute()
    const prefix = 'blacklist-' + useId()
    const fieldId = field => prefix + '-' + field
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const activeTab = ref('entries')
    const entries = ref([])
    const hits = ref([])
    const loadingEntries = ref(false)
    const loadingHits = ref(false)
    const writing = ref(false)
    const hasLoadedEntries = ref(false)
    const hasLoadedHits = ref(false)
    // 黑名单总开关（system_settings.blacklist_enabled）。
    // 这一页与「系统管理」页读写同一个配置，任一处切换另一处刷新后同步。
    const blacklistEnabled = ref(false)
    const loadingSwitch = ref(false)
    const switchError = ref('')
    const entriesTotal = ref(0)
    const hitsTotal = ref(0)
    const entryError = ref('')
    const hitError = ref('')
    const entryFilterError = ref('')
    const hitFilterError = ref('')
    const createError = ref('')
    const createVisible = ref(false)
    const entryFilters = reactive(emptyEntryFilters())
    const hitFilters = reactive(emptyHitFilters())
    const appliedEntryFilters = ref({})
    const appliedHitFilters = ref({})
    const draft = reactive(emptyDraft())

    const entryPagination = reactive({
      current: 1, pageSize: 20, total: 0,
      showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'],
      showTotal: value => `共 ${value} 条`,
    })
    const hitPagination = reactive({
      current: 1, pageSize: 20, total: 0,
      showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'],
      showTotal: value => `共 ${value} 条`,
    })

    let alive = true
    let mounted = false
    let entryReadVersion = 0
    let hitReadVersion = 0
    let writeVersion = 0
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()
    let observedLogout = mountedLogoutEpoch

    const validAdmin = () => {
      const user = currentUser.value
      const id = Number(user?.id)
      return Number.isSafeInteger(id) && id > 0 && id <= 2147483647 && isAdminUser(user)
        && !['disabled', '0'].includes(String(user?.status ?? 'enabled'))
    }
    const canManage = () => alive && route.path === mountedPath
      && getCurrentUserLogoutEpoch() === mountedLogoutEpoch && validAdmin()

    const kindOptions = [
      { label: 'IP', value: 'ip' },
      { label: '手机号', value: 'phone' },
      { label: '微信', value: 'wechat' },
      // 地区匹配（BL-4）尚未接入判定：选项保留但明确禁用，避免运营选了却存不进去。
      { label: '地区（暂未开放）', value: 'region', disabled: true },
    ]
    const enabledOptions = [
      { label: '启用中', value: 'true' },
      { label: '已停用', value: 'false' },
    ]
    const matchTypeOptions = [
      { label: '自动判定', value: '' },
      { label: '精确', value: 'exact' },
      { label: 'CIDR', value: 'cidr' },
      { label: '前缀', value: 'prefix' },
    ]
    const actionOptions = [
      { label: '已拦截', value: 'blocked' },
      { label: '仅留痕', value: 'logged_only' },
    ]

    const entryColumns = [
      { title: 'ID', dataIndex: 'id', key: 'id', width: 80 },
      { title: '类型', dataIndex: 'kind', key: 'kind', width: 100 },
      // 列名刻意写「脱敏值」：管理页**不渲染 pattern 原文**（46-*.md §3.2）。
      { title: '脱敏值', dataIndex: 'valueMasked', key: 'valueMasked', width: 180 },
      { title: '匹配方式', dataIndex: 'matchType', key: 'matchType', width: 110 },
      { title: '拉黑原因', dataIndex: 'reason', key: 'reason', width: 220 },
      { title: '状态', dataIndex: 'enabled', key: 'enabled', width: 110 },
      { title: '过期时间', dataIndex: 'expiresAt', key: 'expiresAt', width: 175 },
      { title: '创建时间', dataIndex: 'createdAt', key: 'createdAt', width: 175 },
      { title: '操作', key: 'action', width: 160 },
    ]
    const hitColumns = [
      { title: '规则 ID', dataIndex: 'entryId', key: 'entryId', width: 90 },
      { title: '类型', dataIndex: 'kind', key: 'kind', width: 100 },
      { title: '脱敏值', dataIndex: 'valueMasked', key: 'valueMasked', width: 170 },
      { title: '来源 IP', dataIndex: 'clientIp', key: 'clientIp', width: 170 },
      { title: '请求路径', dataIndex: 'path', key: 'path', width: 240 },
      { title: '处置', dataIndex: 'action', key: 'action', width: 110 },
      { title: '命中次数', dataIndex: 'hitCount', key: 'hitCount', width: 110 },
      { title: '命中时间', dataIndex: 'createdAt', key: 'createdAt', width: 175 },
    ]

    const patternPlaceholder = computed(() => {
      switch (draft.kind) {
        case 'phone': return '11 位手机号，如 13800001111'
        case 'wechat': return '微信 OpenID（区分大小写）'
        default: return '如 203.0.113.7、203.0.113.0/24 或 203.0.113.'
      }
    })

    const entryScopeDescription = computed(() => {
      const scope = appliedEntryFilters.value
      const parts = [
        scope.kind && `类型：${kindLabel(scope.kind)}`,
        scope.enabled && `状态：${scope.enabled === 'true' ? '启用中' : '已停用'}`,
        scope.keyword && `关键词：${scope.keyword}`,
      ].filter(Boolean)
      return parts.length ? `当前筛选：${parts.join('；')}。` : '当前范围：全部黑名单规则。'
    })
    const hitScopeDescription = computed(() => {
      const scope = appliedHitFilters.value
      const parts = [
        scope.entryId && `规则 ID：${scope.entryId}`,
        scope.clientIp && `来源 IP：${scope.clientIp}`,
        scope.action && `处置：${actionLabel(scope.action)}`,
      ].filter(Boolean)
      return parts.length ? `当前筛选：${parts.join('；')}。` : '当前范围：全部命中记录（按时间倒序）。'
    })

    const entryEmptyText = computed(() => (entryError.value
      ? '列表加载失败，请重试'
      : hasLoadedEntries.value ? '暂无符合条件的黑名单规则' : '黑名单规则尚未读取成功'))
    const hitEmptyText = computed(() => (hitError.value
      ? '命中记录加载失败，请重试'
      : hasLoadedHits.value ? '暂无符合条件的命中记录' : '命中记录尚未读取成功'))

    function kindLabel(value) {
      const normalized = String(value || '').trim().toLowerCase()
      return KIND_LABELS[normalized] || value || '-'
    }
    function kindColor(value) {
      const normalized = String(value || '').trim().toLowerCase()
      return KIND_COLORS[normalized] || 'default'
    }
    function matchTypeLabel(value) {
      const normalized = String(value || '').trim().toLowerCase()
      return MATCH_TYPE_LABELS[normalized] || value || '-'
    }
    function actionLabel(value) {
      const normalized = String(value || '').trim().toLowerCase()
      return ACTION_LABELS[normalized] || value || '-'
    }
    function actionColor(value) {
      const normalized = String(value || '').trim().toLowerCase()
      return ACTION_COLORS[normalized] || 'default'
    }
    function formatDateTime(value) {
      if (!value) return '永久'
      const date = new Date(value)
      if (!Number.isFinite(date.getTime())) return '—'
      return date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
    }

    const buildEntryParams = () => {
      const params = { page: entryPagination.current, pageSize: entryPagination.pageSize }
      const kind = String(appliedEntryFilters.value.kind || '').trim()
      if (kind) params.kind = kind
      // enabled 必须传字符串 'true'/'false'：传 undefined 会把「不筛选」丢掉，
      // 传布尔 false 又会被 axios 序列化成 'false'（碰巧对），索性统一成字符串。
      const enabled = String(appliedEntryFilters.value.enabled || '').trim()
      if (enabled) params.enabled = enabled
      const keyword = String(appliedEntryFilters.value.keyword || '').trim()
      if (keyword) params.keyword = keyword
      return params
    }

    const buildHitParams = () => {
      const params = { page: hitPagination.current, pageSize: hitPagination.pageSize }
      const entryId = String(appliedHitFilters.value.entryId || '').trim()
      if (entryId) params.entryId = entryId
      const clientIp = String(appliedHitFilters.value.clientIp || '').trim()
      if (clientIp) params.clientIp = clientIp
      const action = String(appliedHitFilters.value.action || '').trim()
      if (action) params.action = action
      return params
    }

    const loadEntries = async () => {
      if (!canManage()) return false
      const ticket = ++entryReadVersion
      const owns = () => canManage() && ticket === entryReadVersion
      loadingEntries.value = true
      try {
        const res = await getBlacklistEntries(buildEntryParams())
        if (!owns()) return false
        const body = res?.data
        const items = body?.data
        const count = readNonnegativeInt(body?.total)
        // 失败时**必须**保留上一批行并明确报错：把失败渲染成空列表会让运营
        // 以为规则被清空了（「已知数据保留、失败单独提示」）。
        if (res?.status !== 200 || !Array.isArray(items) || count === null) {
          entryError.value = '黑名单规则刷新失败，页面显示的仍是上次读取的结果。'
          return false
        }
        entries.value = items.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
        entriesTotal.value = count
        entryPagination.total = count
        hasLoadedEntries.value = true
        entryError.value = ''
        return true
      } catch (error) {
        if (owns()) entryError.value = '黑名单规则刷新失败，页面显示的仍是上次读取的结果。'
        return false
      } finally {
        if (owns()) loadingEntries.value = false
      }
    }

    const loadHits = async () => {
      if (!canManage()) return false
      const ticket = ++hitReadVersion
      const owns = () => canManage() && ticket === hitReadVersion
      loadingHits.value = true
      try {
        const res = await getBlacklistHits(buildHitParams())
        if (!owns()) return false
        const body = res?.data
        const items = body?.data
        const count = readNonnegativeInt(body?.total)
        if (res?.status !== 200 || !Array.isArray(items) || count === null) {
          hitError.value = '命中记录刷新失败，页面显示的仍是上次读取的结果。'
          return false
        }
        hits.value = items.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
        hitsTotal.value = count
        hitPagination.total = count
        hasLoadedHits.value = true
        hitError.value = ''
        return true
      } catch (error) {
        if (owns()) hitError.value = '命中记录刷新失败，页面显示的仍是上次读取的结果。'
        return false
      } finally {
        if (owns()) loadingHits.value = false
      }
    }

    const setEntryFilter = (field, value) => {
      if (!canManage() || loadingEntries.value || writing.value || !Object.hasOwn(entryFilters, field)) return
      entryFilters[field] = value
    }
    const setHitFilter = (field, value) => {
      if (!canManage() || loadingHits.value || !Object.hasOwn(hitFilters, field)) return
      hitFilters[field] = value
    }

    const reloadEntries = () => {
      if (!canManage() || loadingEntries.value || writing.value) return
      entryFilterError.value = ''
      appliedEntryFilters.value = {
        kind: entryFilters.kind || '',
        enabled: entryFilters.enabled || '',
        keyword: String(entryFilters.keyword || '').trim(),
      }
      entryPagination.current = 1
      return loadEntries()
    }
    const resetEntryFilters = () => {
      if (!canManage() || loadingEntries.value || writing.value) return
      Object.assign(entryFilters, emptyEntryFilters())
      entryFilterError.value = ''
      appliedEntryFilters.value = {}
      entryPagination.current = 1
      return loadEntries()
    }
    const reloadHits = () => {
      if (!canManage() || loadingHits.value) return
      const entryId = String(hitFilters.entryId || '').trim()
      if (entryId && !/^[0-9]+$/.test(entryId)) { hitFilterError.value = '规则 ID 只能是正整数'; return }
      hitFilterError.value = ''
      appliedHitFilters.value = {
        entryId,
        clientIp: String(hitFilters.clientIp || '').trim(),
        action: hitFilters.action || '',
      }
      hitPagination.current = 1
      return loadHits()
    }
    const resetHitFilters = () => {
      if (!canManage() || loadingHits.value) return
      Object.assign(hitFilters, emptyHitFilters())
      hitFilterError.value = ''
      appliedHitFilters.value = {}
      hitPagination.current = 1
      return loadHits()
    }

    const onEntryTableChange = page => {
      if (!canManage() || loadingEntries.value || writing.value) return
      entryPagination.current = page?.pageSize === entryPagination.pageSize ? Number(page.current) || 1 : 1
      entryPagination.pageSize = Number(page?.pageSize) || entryPagination.pageSize
      return loadEntries()
    }
    const onHitTableChange = page => {
      if (!canManage() || loadingHits.value) return
      hitPagination.current = page?.pageSize === hitPagination.pageSize ? Number(page.current) || 1 : 1
      hitPagination.pageSize = Number(page?.pageSize) || hitPagination.pageSize
      return loadHits()
    }

    // openHitsFor 把「看某条规则的命中」变成一次带筛选的查询。
    //
    // 直接切 tab 不带条件不够：运营点的是某条规则的「命中记录」，
    // 期望看到的是这条规则的命中，而不是全量列表。
    const openHitsFor = record => {
      if (!canManage()) return
      const id = readNonnegativeInt(record?.id)
      activeTab.value = 'hits'
      Object.assign(hitFilters, emptyHitFilters())
      if (id !== null && id > 0) hitFilters.entryId = String(id)
      return reloadHits()
    }

    const openCreate = () => {
      if (!canManage() || writing.value) return
      Object.assign(draft, emptyDraft())
      createError.value = ''
      createVisible.value = true
    }
    const closeCreate = () => {
      if (writing.value) return
      createVisible.value = false
      createError.value = ''
    }
    const setDraft = (field, value) => {
      if (!canManage() || writing.value || !Object.hasOwn(draft, field)) return
      draft[field] = value
    }

    const submitCreate = async () => {
      if (!canManage() || writing.value) return
      const payload = {
        kind: String(draft.kind || '').trim(),
        pattern: String(draft.pattern || '').trim(),
        matchType: String(draft.matchType || '').trim(),
        reason: String(draft.reason || '').trim(),
        expiresAt: String(draft.expiresAt || '').trim(),
        enabled: draft.enabled !== false,
      }
      // 前端只做「必填」这一层校验；类型/格式/归一化一律交给后端，
      // 避免出现「前端放过、后端 400」或反过来的两套判据。
      if (!payload.pattern) { createError.value = '请填写匹配值'; return }
      if (!payload.reason) { createError.value = '请填写拉黑原因'; return }
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      writing.value = true
      createError.value = ''
      let reloadAfter = false
      try {
        const res = await createBlacklistEntry(payload)
        if (!owns()) return
        if (res?.status !== 201 && res?.status !== 200) {
          const detail = String(res?.data?.message || '').trim()
          createError.value = detail || '新增黑名单规则失败'
          return
        }
        message.success('黑名单规则已新增并生效')
        createVisible.value = false
        reloadAfter = true
      } catch (error) {
        if (owns()) {
          const detail = String(error?.response?.data?.message || '').trim()
          createError.value = detail || '新增黑名单规则失败'
        }
      } finally {
        if (owns()) writing.value = false
      }
      // loadEntries 会拒绝在 writing 期间执行，因此必须等锁释放后再刷新。
      if (reloadAfter && owns()) await loadEntries()
    }

    const toggleEnabled = async (record, checked) => {
      if (!canManage() || writing.value) return
      const id = readNonnegativeInt(record?.id)
      if (id === null || id <= 0) return
      const enabled = Boolean(checked)
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      writing.value = true
      entryError.value = ''
      let reloadAfter = false
      try {
        const res = await setBlacklistEntryEnabled(id, enabled)
        if (!owns()) return
        if (res?.status !== 200) {
          entryError.value = String(res?.data?.message || '').trim() || '更新黑名单规则状态失败'
          return
        }
        message.success(enabled ? '规则已启用' : '规则已停用')
        reloadAfter = true
      } catch (error) {
        if (owns()) {
          entryError.value = String(error?.response?.data?.message || '').trim() || '更新黑名单规则状态失败'
        }
      } finally {
        if (owns()) writing.value = false
      }
      if (reloadAfter && owns()) await loadEntries()
    }

    const confirmDelete = async record => {
      if (!canManage() || writing.value) return
      const id = readNonnegativeInt(record?.id)
      if (id === null || id <= 0) return
      const owns = () => canManage()
      const result = await Swal.fire({
        title: '确认删除这条黑名单规则？',
        text: `${kindLabel(record.kind)} ${record.valueMasked || ''}（${record.reason || '无原因'}）。删除后规则立即失效且不可恢复；该规则的历史命中记录会保留。`,
        icon: 'warning',
        showCancelButton: true,
        confirmButtonText: '确认删除',
        cancelButtonText: '取消',
      })
      if (!result?.isConfirmed || !owns()) return
      return removeEntry(record)
    }

    const removeEntry = async record => {
      const id = readNonnegativeInt(record?.id)
      if (id === null || id <= 0 || !canManage()) return
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      writing.value = true
      entryError.value = ''
      let reloadAfter = false
      try {
        const res = await deleteBlacklistEntry(id)
        if (!owns()) return
        if (res?.status !== 200) {
          entryError.value = String(res?.data?.message || '').trim() || '删除黑名单规则失败'
          return
        }
        message.success('黑名单规则已删除')
        reloadAfter = true
      } catch (error) {
        if (owns()) {
          entryError.value = String(error?.response?.data?.message || '').trim() || '删除黑名单规则失败'
        }
      } finally {
        if (owns()) writing.value = false
      }
      if (reloadAfter && owns()) await loadEntries()
    }

    const retire = () => {
      entryReadVersion += 1
      hitReadVersion += 1
      writeVersion += 1
    }

    watch(() => [currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status], () => {
      retire()
      entries.value = []; entriesTotal.value = 0; entryPagination.total = 0; hasLoadedEntries.value = false
      hits.value = []; hitsTotal.value = 0; hitPagination.total = 0; hasLoadedHits.value = false
      createVisible.value = false
      Object.assign(draft, emptyDraft())
      if (mounted && canManage()) void loadEntries()
    }, { flush: 'sync' })
    watch(() => getCurrentUserLogoutEpoch(), value => {
      if (value === observedLogout) return
      observedLogout = value
      retire()
      entries.value = []; entriesTotal.value = 0; entryPagination.total = 0; hasLoadedEntries.value = false
      hits.value = []; hitsTotal.value = 0; hitPagination.total = 0; hasLoadedHits.value = false
      createVisible.value = false
      entryError.value = ''; hitError.value = ''; createError.value = ''
    }, { flush: 'sync' })
    watch(() => route.path, () => {
      retire()
      entries.value = []; entriesTotal.value = 0; entryPagination.total = 0; hasLoadedEntries.value = false
      hits.value = []; hitsTotal.value = 0; hitPagination.total = 0; hasLoadedHits.value = false
      createVisible.value = false
    }, { flush: 'sync' })
    // 命中记录 tab 只在被打开时读取：多数时间运营看的是规则列表，
    // 没必要每次进页面就多打一次接口。
    watch(() => activeTab.value, key => {
      if (key === 'hits' && canManage() && !hasLoadedHits.value && !loadingHits.value) void loadHits()
    })

    // loadSwitch 读取总开关当前值。失败不覆盖已有值、不让页面变红：
    // 开关读不到时最危险的方向是「误显示为已开启」，所以保持上一次已知值并给出提示。
    async function loadSwitch() {
      if (!canManage()) return
      loadingSwitch.value = true
      switchError.value = ''
      try {
        const data = await getSystemSettings({ errorFeedback: 'local' })
        blacklistEnabled.value = data?.blacklistEnabled === true
      } catch (error) {
        switchError.value = '总开关状态读取失败，请刷新重试'
        void error
      } finally {
        loadingSwitch.value = false
      }
    }

    // toggleSwitch 写回总开关。
    //
    // 只提交 blacklistEnabled 一个字段：后端 UpdateSystemSettingInput 用 *bool，
    // nil 表示「未提交 = 保持现值」，所以这里不会顺带改动本页无关的任何配置。
    async function toggleSwitch(next) {
      if (!canManage() || loadingSwitch.value) return
      const previous = blacklistEnabled.value
      blacklistEnabled.value = next === true
      loadingSwitch.value = true
      switchError.value = ''
      try {
        const data = await updateSystemSettings(
          { blacklistEnabled: next === true },
          { errorFeedback: 'local' },
        )
        // 以后端回传为准：避免「本地改了但没落库」显示成已生效。
        blacklistEnabled.value = data?.blacklistEnabled === true
        message.success(blacklistEnabled.value ? '黑名单已开启' : '黑名单已关闭')
      } catch (error) {
        // 回滚到切换前的值，否则界面会显示一个并未生效的状态。
        blacklistEnabled.value = previous
        switchError.value = '总开关保存失败，已回滚到原状态'
        void error
      } finally {
        loadingSwitch.value = false
      }
    }

    onMounted(() => {
      mounted = true
      if (canManage()) {
        void loadEntries()
        void loadSwitch()
      }
    })
    onBeforeUnmount(() => {
      alive = false
      mounted = false
      retire()
    })

    return {
      fieldId, canManage, activeTab,
      blacklistEnabled, loadingSwitch, switchError, toggleSwitch, loadSwitch,
      entries, hits, loadingEntries, loadingHits, writing,
      hasLoadedEntries, hasLoadedHits, entriesTotal, hitsTotal,
      entryError, hitError, entryFilterError, hitFilterError, createError,
      createVisible, entryFilters, hitFilters, draft,
      appliedEntryFilters, appliedHitFilters,
      entryPagination, hitPagination,
      kindOptions, enabledOptions, matchTypeOptions, actionOptions,
      entryColumns, hitColumns, patternPlaceholder,
      entryScopeDescription, hitScopeDescription, entryEmptyText, hitEmptyText,
      kindLabel, kindColor, matchTypeLabel, actionLabel, actionColor, formatDateTime,
      setEntryFilter, setHitFilter, setDraft,
      loadEntries, loadHits, reloadEntries, resetEntryFilters, reloadHits, resetHitFilters,
      onEntryTableChange, onHitTableChange, openHitsFor,
      openCreate, closeCreate, submitCreate, toggleEnabled, confirmDelete,
      sumTableColumnWidths,
    }
  },
}
</script>

<style scoped>
.page-alert, .filter-form, .status-alert { margin-bottom: 16px; }
.blacklist-summary { margin-bottom: 16px; }
.summary-label { color: var(--console-muted); font-size: 14px; }
.summary-value { margin-top: 8px; color: var(--console-text); font-size: clamp(18px, 2.2vw, 24px); font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.filter-kind, .filter-enabled { width: 150px; }
.filter-keyword, .filter-ip, .filter-id { width: 200px; }
.filter-action { width: 150px; }
.filter-error { margin: 0 0 16px; color: var(--console-danger, #c13e41); font-size: 13px; }
/* 总开关：与 summary-value 的数值排版解耦（开关不是数字，不需要 tabular-nums/大字号）。 */
.summary-switch-state { margin-left: 10px; color: var(--console-muted); font-size: 13px; font-weight: 400; font-variant-numeric: normal; }
.summary-switch-error { margin-top: 6px; color: var(--console-danger, #c13e41); font-size: 12px; line-height: 1.4; }
.field-hint { margin: 6px 0 0; color: var(--console-muted); font-size: 12px; line-height: 1.6; }
.masked-cell, .time-cell, .count-cell { font-variant-numeric: tabular-nums; }
.reason-cell, .path-cell { overflow-wrap: anywhere; }
@media (max-width: 640px) {
  .filter-form :deep(.ant-form-item) { width: 100%; margin-inline-end: 0; }
  .filter-kind, .filter-enabled, .filter-keyword, .filter-ip, .filter-id, .filter-action { width: 100%; }
}
</style>
