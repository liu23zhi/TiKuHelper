<template>
  <div class="console-page whitelist-page">
    <ConsolePageHeading
      title="白名单管理"
      description="按 IP / CIDR 放行请求来源：命中的 IP **直接放行且完全不查黑名单**（优先级 白名单 > 黑名单），也不会在黑名单命中记录里留痕。"
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
      message="当前身份无白名单管理权限，请使用管理员账号登录。"
    />

    <template v-if="canManage()">
      <a-alert
        type="info"
        show-icon
        class="page-alert"
        message="白名单优先于黑名单。同一 IP 同时在两张表里时以白名单为准（放行），且不会写入黑名单命中记录。白名单没有总开关 —— 规则生效即放行。"
      />

      <ConsoleSummaryGrid class="whitelist-summary">
        <div>
          <div class="summary-label">筛选结果</div>
          <div class="summary-value">{{ hasLoadedEntries ? entriesTotal : '--' }} 条</div>
        </div>
        <div>
          <div class="summary-label">按 IP 白名单（文本域）</div>
          <div class="summary-value">{{ hasLoadedExempt ? exemptCount : '--' }} 项</div>
        </div>
      </ConsoleSummaryGrid>

      <a-tabs v-model:activeKey="activeTab" class="whitelist-tabs">
        <a-tab-pane key="entries" tab="白名单规则">
          <ConsolePanel title="筛选" description="关键词只匹配脱敏值与放行原因，不匹配规则原文。">
            <a-form layout="inline" class="filter-form" :on-finish="reloadEntries">
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
                  placeholder="脱敏值 / 放行原因"
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
            <ConsoleTableShell label="白名单规则">
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
                  <a-tag v-else-if="column.key === 'kind'" color="blue">{{ kindLabel(record.kind) }}</a-tag>
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

        <a-tab-pane key="exempt" tab="按 IP 白名单">
          <ConsolePanel
            title="批量按 IP 放行"
            description="逗号 / 分号 / 换行分隔，每项是单个 IP 或 CIDR。命中即完全不查黑名单；与上面的规则表是两层，处置相同。保存时会校验格式，写错的项直接拒绝（不会静默跳过）。"
          >
            <a-alert v-if="exemptError" type="error" show-icon :message="exemptError" class="status-alert" />
            <a-form layout="vertical" class="exempt-form">
              <a-form-item label="IP / CIDR 列表" :html-for="fieldId('exemptIPs')">
                <a-textarea
                  :id="fieldId('exemptIPs')"
                  :value="exemptInput"
                  :rows="8"
                  :disabled="loadingExempt || savingExempt"
                  placeholder="203.0.113.7&#10;198.51.100.0/24&#10;2001:db8::/32"
                  @update:value="value => setExemptInput(value)"
                />
              </a-form-item>
              <a-form-item>
                <a-space wrap>
                  <a-button
                    type="primary"
                    :loading="savingExempt"
                    :disabled="loadingExempt || savingExempt || !exemptDirty"
                    @click="saveExemptIPs"
                  >保存并按新配置判定</a-button>
                  <a-button :disabled="loadingExempt || savingExempt || !exemptDirty" @click="resetExemptInput">撤销修改</a-button>
                  <a-button :loading="loadingExempt" :disabled="loadingExempt || savingExempt" @click="loadExemptIPs">重新读取</a-button>
                </a-space>
              </a-form-item>
            </a-form>
            <p class="field-hint">
              已识别 {{ exemptCount }} 项<template v-if="exemptItems.length">：{{ exemptItems.join('、') }}</template>。
              解析结果与原文分开显示 —— 只回原文的话，格式写错（比如全角逗号）会静默少解析几项而无人发现。
            </p>
          </ConsolePanel>
        </a-tab-pane>
      </a-tabs>

      <a-modal
        :open="createVisible"
        title="新增白名单规则"
        :confirm-loading="writing"
        ok-text="保存"
        cancel-text="取消"
        :mask-closable="false"
        @cancel="closeCreate"
        @ok="submitCreate"
      >
        <a-form layout="vertical" class="create-form">
          <a-form-item label="类型">
            <a-select :value="draft.kind" :options="kindOptions" :disabled="true" />
            <p class="field-hint">白名单只支持 IP：中间件能看到的只有客户端 IP，其它粒度没有判定落点。</p>
          </a-form-item>
          <a-form-item label="匹配值" required>
            <a-input
              :value="draft.pattern"
              :disabled="writing"
              placeholder="如 203.0.113.7、203.0.113.0/24 或 203.0.113."
              @update:value="value => setDraft('pattern', value)"
            />
            <p class="field-hint">支持精确 IP、CIDR（203.0.113.0/24）与 IPv4 前缀（203.0.113.）；非法项会被后端拒绝（400）。</p>
          </a-form-item>
          <a-form-item label="匹配方式">
            <a-select
              :value="draft.matchType"
              :options="matchTypeOptions"
              :disabled="writing"
              @update:value="value => setDraft('matchType', value)"
            />
          </a-form-item>
          <a-form-item label="放行原因" required>
            <a-input
              :value="draft.reason"
              :disabled="writing"
              placeholder="如：办公出口固定网段"
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

      <a-modal
        :open="deleteVisible"
        title="删除白名单规则"
        :confirm-loading="writing"
        ok-text="确认删除"
        cancel-text="取消"
        :mask-closable="false"
        @cancel="closeDelete"
        @ok="submitDelete"
      >
        <p class="delete-hint">
          规则 <strong>{{ deleteTarget?.valueMasked || '—' }}</strong>
          （{{ deleteTarget?.reason || '无原因' }}）将被删除，删除后该 IP 立刻重新受黑名单约束。
          删除原因必填：它会连同完整规则快照一起写进审计日志。
        </p>
        <a-form layout="vertical">
          <a-form-item label="删除原因" required :html-for="fieldId('deleteReason')">
            <a-input
              :id="fieldId('deleteReason')"
              :value="deleteReason"
              :disabled="writing"
              placeholder="至少 2 个字，如：网段已停用"
              @update:value="value => setDeleteReason(value)"
            />
          </a-form-item>
        </a-form>
        <p v-if="deleteError" class="filter-error" role="alert">{{ deleteError }}</p>
      </a-modal>
    </template>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, reactive, ref, useId, watch } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import ConsoleTableShell from '@/components/ConsoleTableShell.vue'
import {
  createWhitelistEntry,
  deleteWhitelistEntry,
  getWhitelistEntries,
  getWhitelistExemptIPs,
  setWhitelistEntryEnabled,
  updateWhitelistExemptIPs,
} from '@/api/WhitelistApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

const KIND_LABELS = { ip: 'IP' }
const MATCH_TYPE_LABELS = { exact: '精确', cidr: 'CIDR', prefix: '前缀' }

const emptyEntryFilters = () => ({ enabled: undefined, keyword: '' })
const emptyDraft = () => ({ kind: 'ip', pattern: '', matchType: '', reason: '', expiresAt: '', enabled: true })

function readNonnegativeInt(value) {
  if (!['number', 'string'].includes(typeof value) || String(value).trim() === '') return null
  const number = Number(value)
  return Number.isInteger(number) && number >= 0 ? number : null
}

export default {
  name: 'WhitelistPage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell },
  setup() {
    const route = useRoute()
    const prefix = 'whitelist-' + useId()
    const fieldId = field => prefix + '-' + field
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const activeTab = ref('entries')
    const entries = ref([])
    const loadingEntries = ref(false)
    const writing = ref(false)
    const hasLoadedEntries = ref(false)
    const entriesTotal = ref(0)
    const entryError = ref('')
    const entryFilterError = ref('')
    const createError = ref('')
    const createVisible = ref(false)
    const entryFilters = reactive(emptyEntryFilters())
    const appliedEntryFilters = ref({})
    const draft = reactive(emptyDraft())

    // 按 IP 白名单文本域（T-05）。
    const exemptInput = ref('')
    const exemptSaved = ref('')
    const exemptItems = ref([])
    const exemptCount = ref(0)
    const loadingExempt = ref(false)
    const savingExempt = ref(false)
    const hasLoadedExempt = ref(false)
    const exemptError = ref('')

    // 删除弹窗：reason 必填。
    const deleteVisible = ref(false)
    const deleteTarget = ref(null)
    const deleteReason = ref('')
    const deleteError = ref('')

    const entryPagination = reactive({
      current: 1, pageSize: 20, total: 0,
      showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'],
      showTotal: value => `共 ${value} 条`,
    })

    let alive = true
    let mounted = false
    let entryReadVersion = 0
    let exemptReadVersion = 0
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

    const exemptDirty = computed(() => exemptInput.value !== exemptSaved.value)

    // 白名单只有 ip 一种粒度（界面不给别的选项，避免选了存不进去）。
    const kindOptions = [{ label: 'IP', value: 'ip' }]
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

    const entryColumns = [
      { title: 'ID', dataIndex: 'id', key: 'id', width: 80 },
      { title: '类型', dataIndex: 'kind', key: 'kind', width: 90 },
      // 列名刻意写「脱敏值」：管理页不渲染 pattern 原文（与黑名单同约定）。
      { title: '脱敏值', dataIndex: 'valueMasked', key: 'valueMasked', width: 180 },
      { title: '匹配方式', dataIndex: 'matchType', key: 'matchType', width: 110 },
      { title: '放行原因', dataIndex: 'reason', key: 'reason', width: 220 },
      { title: '状态', dataIndex: 'enabled', key: 'enabled', width: 110 },
      { title: '过期时间', dataIndex: 'expiresAt', key: 'expiresAt', width: 175 },
      { title: '创建时间', dataIndex: 'createdAt', key: 'createdAt', width: 175 },
      { title: '操作', key: 'action', width: 110 },
    ]

    const entryScopeDescription = computed(() => {
      const scope = appliedEntryFilters.value
      const parts = [
        scope.enabled && `状态：${scope.enabled === 'true' ? '启用中' : '已停用'}`,
        scope.keyword && `关键词：${scope.keyword}`,
      ].filter(Boolean)
      return parts.length ? `当前筛选：${parts.join('；')}。` : '当前范围：全部白名单规则。'
    })

    const entryEmptyText = computed(() => (entryError.value
      ? '列表加载失败，请重试'
      : hasLoadedEntries.value ? '暂无符合条件的白名单规则' : '白名单规则尚未读取成功'))

    function kindLabel(value) {
      const normalized = String(value || '').trim().toLowerCase()
      return KIND_LABELS[normalized] || value || '-'
    }
    function matchTypeLabel(value) {
      const normalized = String(value || '').trim().toLowerCase()
      return MATCH_TYPE_LABELS[normalized] || value || '-'
    }
    function formatDateTime(value) {
      if (!value) return '永久'
      const date = new Date(value)
      if (!Number.isFinite(date.getTime())) return '—'
      return date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
    }

    const buildEntryParams = () => {
      const params = { page: entryPagination.current, pageSize: entryPagination.pageSize }
      // enabled 必须传字符串 'true'/'false'：传 undefined 会把「不筛选」丢掉，
      // 传布尔 false 又会被 axios 序列化成 'false'（碰巧对），索性统一成字符串。
      const enabled = String(appliedEntryFilters.value.enabled || '').trim()
      if (enabled) params.enabled = enabled
      const keyword = String(appliedEntryFilters.value.keyword || '').trim()
      if (keyword) params.keyword = keyword
      return params
    }

    const loadEntries = async () => {
      if (!canManage()) return false
      const ticket = ++entryReadVersion
      const owns = () => canManage() && ticket === entryReadVersion
      loadingEntries.value = true
      try {
        const res = await getWhitelistEntries(buildEntryParams())
        if (!owns()) return false
        const body = res?.data
        const items = body?.data
        const count = readNonnegativeInt(body?.total)
        // 失败时**必须**保留上一批行并明确报错：把失败渲染成空列表会让运营
        // 以为规则被清空了（「已知数据保留、失败单独提示」）。
        if (res?.status !== 200 || !Array.isArray(items) || count === null) {
          entryError.value = '白名单规则刷新失败，页面显示的仍是上次读取的结果。'
          return false
        }
        entries.value = items.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
        entriesTotal.value = count
        entryPagination.total = count
        hasLoadedEntries.value = true
        entryError.value = ''
        return true
      } catch (error) {
        if (owns()) entryError.value = '白名单规则刷新失败，页面显示的仍是上次读取的结果。'
        return false
      } finally {
        if (owns()) loadingEntries.value = false
      }
    }

    const loadExemptIPs = async () => {
      if (!canManage()) return false
      const ticket = ++exemptReadVersion
      const owns = () => canManage() && ticket === exemptReadVersion
      loadingExempt.value = true
      try {
        const res = await getWhitelistExemptIPs()
        if (!owns()) return false
        if (res?.status !== 200 || typeof res?.data !== 'object') {
          exemptError.value = '按 IP 白名单读取失败，页面显示的仍是上次读取的结果。'
          return false
        }
        const body = res.data
        const value = typeof body.value === 'string' ? body.value : ''
        const items = Array.isArray(body.items) ? body.items.filter(item => typeof item === 'string') : []
        const count = readNonnegativeInt(body.count)
        exemptInput.value = value
        exemptSaved.value = value
        exemptItems.value = items
        // count 与 items 必须一致：不一致说明服务端解析和它自己的汇总对不上，
        // 此时宁可显示 items 长度并报错，也不要让「已识别 N 项」说谎。
        exemptCount.value = count === null ? items.length : count
        if (count !== null && count !== items.length) {
          exemptError.value = '按 IP 白名单解析结果与汇总数量不一致，请重新读取后再保存。'
          return false
        }
        hasLoadedExempt.value = true
        exemptError.value = ''
        return true
      } catch (error) {
        if (owns()) exemptError.value = '按 IP 白名单读取失败，页面显示的仍是上次读取的结果。'
        return false
      } finally {
        if (owns()) loadingExempt.value = false
      }
    }

    const setEntryFilter = (field, value) => {
      if (!canManage() || loadingEntries.value || writing.value || !Object.hasOwn(entryFilters, field)) return
      entryFilters[field] = value
    }
    const setExemptInput = value => {
      if (!canManage() || loadingExempt.value || savingExempt.value) return
      exemptInput.value = String(value == null ? '' : value)
    }
    const resetExemptInput = () => {
      if (!canManage() || savingExempt.value) return
      exemptInput.value = exemptSaved.value
      exemptError.value = ''
    }
    const setDraft = (field, value) => {
      if (!canManage() || writing.value || !Object.hasOwn(draft, field)) return
      draft[field] = value
    }
    const setDeleteReason = value => {
      if (!canManage() || writing.value) return
      deleteReason.value = String(value == null ? '' : value)
    }

    const reloadEntries = () => {
      if (!canManage() || loadingEntries.value || writing.value) return
      entryFilterError.value = ''
      appliedEntryFilters.value = {
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
    const onEntryTableChange = page => {
      if (!canManage() || loadingEntries.value || writing.value) return
      entryPagination.current = page?.pageSize === entryPagination.pageSize ? Number(page.current) || 1 : 1
      entryPagination.pageSize = Number(page?.pageSize) || entryPagination.pageSize
      return loadEntries()
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

    const submitCreate = async () => {
      if (!canManage() || writing.value) return
      const payload = {
        kind: 'ip',
        pattern: String(draft.pattern || '').trim(),
        matchType: String(draft.matchType || '').trim(),
        reason: String(draft.reason || '').trim(),
        expiresAt: String(draft.expiresAt || '').trim(),
        enabled: draft.enabled !== false,
      }
      // 前端只做「必填」这一层校验；格式/归一化一律交给后端，
      // 避免出现「前端放过、后端 400」或反过来的两套判据。
      if (!payload.pattern) { createError.value = '请填写匹配值'; return }
      if (!payload.reason) { createError.value = '请填写放行原因'; return }
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      writing.value = true
      createError.value = ''
      let reloadAfter = false
      try {
        const res = await createWhitelistEntry(payload)
        if (!owns()) return
        if (res?.status !== 201 && res?.status !== 200) {
          const detail = String(res?.data?.message || '').trim()
          createError.value = detail || '新增白名单规则失败'
          return
        }
        message.success('白名单规则已新增并生效')
        createVisible.value = false
        reloadAfter = true
      } catch (error) {
        if (owns()) {
          const detail = String(error?.response?.data?.message || '').trim()
          createError.value = detail || '新增白名单规则失败'
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
        const res = await setWhitelistEntryEnabled(id, enabled)
        if (!owns()) return
        if (res?.status !== 200) {
          entryError.value = String(res?.data?.message || '').trim() || '更新白名单规则状态失败'
          return
        }
        message.success(enabled ? '规则已启用（立即放行）' : '规则已停用（不再放行）')
        reloadAfter = true
      } catch (error) {
        if (owns()) {
          entryError.value = String(error?.response?.data?.message || '').trim() || '更新白名单规则状态失败'
        }
      } finally {
        if (owns()) writing.value = false
      }
      if (reloadAfter && owns()) await loadEntries()
    }

    const confirmDelete = record => {
      if (!canManage() || writing.value) return
      const id = readNonnegativeInt(record?.id)
      if (id === null || id <= 0) return
      deleteTarget.value = { ...record }
      deleteReason.value = ''
      deleteError.value = ''
      deleteVisible.value = true
    }
    const closeDelete = () => {
      if (writing.value) return
      deleteVisible.value = false
      deleteTarget.value = null
      deleteReason.value = ''
      deleteError.value = ''
    }

    const submitDelete = async () => {
      if (!canManage() || writing.value) return
      const id = readNonnegativeInt(deleteTarget.value?.id)
      if (id === null || id <= 0) { deleteError.value = '规则参数错误'; return }
      const reason = String(deleteReason.value || '').trim()
      // 与后端一致：去空白后 < 2 字直接拦下（后端也会 400，这里只是省一次往返）。
      if ([...reason].length < 2) { deleteError.value = '请填写删除原因（至少 2 个字）'; return }
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      writing.value = true
      deleteError.value = ''
      let reloadAfter = false
      try {
        const res = await deleteWhitelistEntry(id, reason)
        if (!owns()) return
        if (res?.status !== 200) {
          deleteError.value = String(res?.data?.message || '').trim() || '删除白名单规则失败'
          return
        }
        message.success('白名单规则已删除')
        deleteVisible.value = false
        deleteTarget.value = null
        deleteReason.value = ''
        reloadAfter = true
      } catch (error) {
        if (owns()) {
          deleteError.value = String(error?.response?.data?.message || '').trim() || '删除白名单规则失败'
        }
      } finally {
        if (owns()) writing.value = false
      }
      if (reloadAfter && owns()) await loadEntries()
    }

    const saveExemptIPs = async () => {
      if (!canManage() || savingExempt.value || loadingExempt.value) return
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      savingExempt.value = true
      exemptError.value = ''
      let readAfter = false
      try {
        const res = await updateWhitelistExemptIPs(exemptInput.value)
        if (!owns()) return
        if (res?.status !== 200) {
          // 后端对非法项返回 400 + 具体原因（哪一项不是合法 IP/CIDR），
          // 原样展示：运营要能据此改对，而不是看到一句「保存失败」。
          exemptError.value = String(res?.data?.message || '').trim() || '保存按 IP 白名单失败'
          return
        }
        message.success('按 IP 白名单已保存并生效')
        readAfter = true
      } catch (error) {
        if (owns()) {
          exemptError.value = String(error?.response?.data?.message || '').trim() || '保存按 IP 白名单失败'
        }
      } finally {
        if (owns()) savingExempt.value = false
      }
      // 重新读取：用服务端解析出的 items/count 覆盖本地输入，避免「本地看着保存了、
      // 实际解析出的是另一批」（例如全角分隔符被服务端当成普通字符而非分隔符）。
      if (readAfter && owns()) await loadExemptIPs()
    }

    const retire = () => {
      entryReadVersion += 1
      exemptReadVersion += 1
      writeVersion += 1
    }

    watch(() => [currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status], () => {
      retire()
      entries.value = []; entriesTotal.value = 0; entryPagination.total = 0; hasLoadedEntries.value = false
      createVisible.value = false
      deleteVisible.value = false
      Object.assign(draft, emptyDraft())
      if (mounted && canManage()) void loadEntries()
    }, { flush: 'sync' })
    watch(() => getCurrentUserLogoutEpoch(), value => {
      if (value === observedLogout) return
      observedLogout = value
      retire()
      entries.value = []; entriesTotal.value = 0; entryPagination.total = 0; hasLoadedEntries.value = false
      createVisible.value = false
      deleteVisible.value = false
      entryError.value = ''; createError.value = ''; deleteError.value = ''; exemptError.value = ''
    }, { flush: 'sync' })
    watch(() => route.path, () => {
      retire()
      entries.value = []; entriesTotal.value = 0; entryPagination.total = 0; hasLoadedEntries.value = false
      createVisible.value = false
      deleteVisible.value = false
    }, { flush: 'sync' })
    // 文本域 tab 只在被打开时读取：多数时间运营看的是规则列表。
    watch(() => activeTab.value, key => {
      if (key === 'exempt' && canManage() && !hasLoadedExempt.value && !loadingExempt.value) void loadExemptIPs()
    })

    onMounted(() => {
      mounted = true
      if (canManage()) void loadEntries()
    })
    onBeforeUnmount(() => {
      alive = false
      mounted = false
      retire()
    })

    return {
      fieldId, canManage, activeTab,
      entries, loadingEntries, writing, hasLoadedEntries, entriesTotal,
      entryError, entryFilterError, createError,
      createVisible, entryFilters, draft, appliedEntryFilters, entryPagination,
      kindOptions, enabledOptions, matchTypeOptions, entryColumns,
      entryScopeDescription, entryEmptyText,
      kindLabel, matchTypeLabel, formatDateTime,
      setEntryFilter, setDraft, loadEntries, reloadEntries, resetEntryFilters, onEntryTableChange,
      openCreate, closeCreate, submitCreate, toggleEnabled, confirmDelete,
      // 删除弹窗
      deleteVisible, deleteTarget, deleteReason, deleteError, setDeleteReason, closeDelete, submitDelete,
      // 按 IP 白名单
      exemptInput, exemptSaved, exemptItems, exemptCount, loadingExempt, savingExempt,
      hasLoadedExempt, exemptError, exemptDirty,
      setExemptInput, resetExemptInput, loadExemptIPs, saveExemptIPs,
      sumTableColumnWidths,
    }
  },
}
</script>

<style scoped>
.page-alert, .filter-form, .status-alert { margin-bottom: 16px; }
.whitelist-summary { margin-bottom: 16px; }
.summary-label { color: var(--console-muted); font-size: 14px; }
.summary-value { margin-top: 8px; color: var(--console-text); font-size: clamp(18px, 2.2vw, 24px); font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.filter-enabled { width: 150px; }
.filter-keyword { width: 200px; }
.filter-error { margin: 0 0 16px; color: var(--console-danger, #c13e41); font-size: 13px; }
.field-hint { margin: 6px 0 0; color: var(--console-muted); font-size: 12px; line-height: 1.6; }
.delete-hint { margin: 0 0 16px; color: var(--console-text); font-size: 13px; line-height: 1.7; }
.exempt-form { max-width: 720px; }
.masked-cell, .time-cell { font-variant-numeric: tabular-nums; }
.reason-cell { overflow-wrap: anywhere; }
@media (max-width: 640px) {
  .filter-form :deep(.ant-form-item) { width: 100%; margin-inline-end: 0; }
  .filter-enabled, .filter-keyword { width: 100%; }
  .exempt-form { max-width: 100%; }
}
</style>
