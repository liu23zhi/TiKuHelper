<template>
  <div class="console-page archived-users-page">
    <ConsolePageHeading
      title="已归档账号管理"
      description="管理员删除与用户自助注销的账号都移到这里。归档不物理删除数据：账号行与绑定记录都会保留，可随时恢复。"
    >
      <template #actions>
        <a-button :loading="loading" :disabled="!canManage() || loading" @click="load">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert
      v-if="!canManage()"
      type="warning"
      show-icon
      class="page-alert"
      message="当前身份无已归档账号管理权限，请使用管理员账号登录。"
    />

    <template v-if="canManage()">
      <ConsoleSummaryGrid class="archived-summary">
        <div>
          <div class="summary-label">筛选结果</div>
          <div class="summary-value">{{ hasLoaded ? total : '--' }} 个账号</div>
        </div>
        <div>
          <div class="summary-label">归档原因分布</div>
          <div class="summary-value summary-value--small">
            管理员删除 {{ reasonCounts.admin_delete }} / 用户注销 {{ reasonCounts.user_closure }}
          </div>
        </div>
        <div>
          <div class="summary-label">本次读取时间</div>
          <div class="summary-value summary-value--small">{{ lastLoadedAt || '--' }}</div>
        </div>
      </ConsoleSummaryGrid>

      <ConsolePanel title="筛选" description="归档原因与时间范围按归档时点筛选；关键字只匹配用户名、昵称、手机号、微信，不做通配符解释。">
        <a-form layout="inline" class="filter-form" :on-finish="reload">
          <a-form-item label="归档原因" :html-for="fieldId('reason')">
            <a-select
              :id="fieldId('reason')"
              :value="filters.reason"
              :options="reasonOptions"
              allow-clear
              placeholder="全部原因"
              class="filter-reason"
              :disabled="loading || restoring"
              @update:value="value => setFilter('reason', value)"
              @change="reload"
            />
          </a-form-item>
          <a-form-item label="关键字" :html-for="fieldId('keyword')">
            <a-input
              :id="fieldId('keyword')"
              :value="filters.keyword"
              allow-clear
              placeholder="用户名 / 昵称 / 手机号 / 微信"
              class="filter-keyword"
              :disabled="loading || restoring"
              @update:value="value => setFilter('keyword', value)"
              @press-enter="reload"
            />
          </a-form-item>
          <a-form-item label="归档起" :html-for="fieldId('startTime')">
            <a-input
              :id="fieldId('startTime')"
              :value="filters.startTime"
              type="date"
              class="filter-date"
              :disabled="loading || restoring"
              @update:value="value => setFilter('startTime', value)"
            />
          </a-form-item>
          <a-form-item label="归档止" :html-for="fieldId('endTime')">
            <a-input
              :id="fieldId('endTime')"
              :value="filters.endTime"
              type="date"
              class="filter-date"
              :disabled="loading || restoring"
              @update:value="value => setFilter('endTime', value)"
            />
          </a-form-item>
          <a-form-item>
            <a-space wrap>
              <a-button type="primary" html-type="submit" :loading="loading" :disabled="loading || restoring">查询</a-button>
              <a-button :disabled="loading || restoring" @click="resetFilters">重置筛选</a-button>
            </a-space>
          </a-form-item>
        </a-form>
        <p v-if="filterError" class="filter-error" role="alert">{{ filterError }}</p>
        <a-alert v-if="listError" type="error" show-icon :message="listError" class="status-alert" />
      </ConsolePanel>

      <ConsolePanel title="归档列表" :description="scopeDescription">
        <ConsoleTableShell label="已归档账号">
          <a-table
            table-layout="fixed"
            :data-source="rows"
            :columns="columns"
            :loading="loading"
            :pagination="pagination"
            row-key="id"
            :scroll="{ x: sumTableColumnWidths(columns) }"
            :locale="{ emptyText: emptyText }"
            @change="onTableChange"
          >
            <template #bodyCell="{ column, record }">
              <span v-if="column.key === 'username'" class="archived-username">{{ record.username || '—' }}</span>
              <span v-else-if="column.key === 'nickname'">{{ record.nickname || '—' }}</span>
              <a-tag v-else-if="column.key === 'archivedReason'" :color="reasonColor(record.archivedReason)">
                {{ reasonLabel(record.archivedReason) }}
              </a-tag>
              <span v-else-if="column.key === 'archivedAt'">{{ formatDateTime(record.archivedAt) }}</span>
              <span v-else-if="column.key === 'archivedByUserId'" class="archived-operator">
                {{ record.archivedByUserId ? `#${record.archivedByUserId}` : '—' }}
              </span>
              <span v-else-if="column.key === 'balance'">{{ moneyText(record.balance) }}</span>
              <span v-else-if="column.key === 'giftBalance'">{{ moneyText(record.giftBalance) }}</span>
              <template v-else-if="column.key === 'action'">
                <a-space wrap>
                  <a-button type="link" :disabled="loading || restoring" @click="openDetail(record)">详情</a-button>
                  <a-button type="link" :disabled="loading || restoring" @click="startRestore(record)">恢复</a-button>
                </a-space>
              </template>
            </template>
          </a-table>
        </ConsoleTableShell>
      </ConsolePanel>
    </template>

    <a-modal
      :open="detailOpen"
      title="已归档账号详情"
      :footer="null"
      :width="880"
      destroy-on-close
      @update:open="setDetailOpen"
    >
      <a-spin :spinning="detailLoading">
        <a-alert v-if="detailError" type="error" show-icon :message="detailError" class="status-alert" />
        <template v-if="detailUser">
          <dl class="detail-grid">
            <div><dt>用户名</dt><dd>{{ detailUser.username || '—' }}</dd></div>
            <div><dt>昵称</dt><dd>{{ detailUser.nickname || '—' }}</dd></div>
            <div><dt>账号 ID</dt><dd>{{ detailUser.id }}</dd></div>
            <div><dt>归档原因</dt><dd>{{ reasonLabel(detailUser.archivedReason) }}</dd></div>
            <div><dt>归档时间</dt><dd>{{ formatDateTime(detailUser.archivedAt) }}</dd></div>
            <div><dt>操作者</dt><dd>{{ detailUser.archivedByUserId ? `#${detailUser.archivedByUserId}` : '—' }}</dd></div>
            <div><dt>余额</dt><dd>{{ moneyText(detailUser.balance) }}</dd></div>
            <div><dt>赠送余额</dt><dd>{{ moneyText(detailUser.giftBalance) }}</dd></div>
            <div><dt>注册时间</dt><dd>{{ formatDateTime(detailUser.createdAt) }}</dd></div>
          </dl>
          <h3 class="detail-heading">绑定记录历史</h3>
          <p class="detail-hint">已释放的绑定不再占位，别人可以重新绑定同一个手机号 / 微信。</p>
          <a-table
            size="small"
            :data-source="detailBindings"
            :columns="bindingColumns"
            :pagination="false"
            row-key="id"
            :locale="{ emptyText: '该账号没有绑定记录' }"
          >
            <template #bodyCell="{ column, record }">
              <span v-if="column.key === 'kind'">{{ bindingKindLabel(record.kind) }}</span>
              <span v-else-if="column.key === 'valueMasked'">{{ record.valueMasked || '—' }}</span>
              <a-tag v-else-if="column.key === 'state'" :color="record.state === 'active' ? 'green' : 'default'">
                {{ record.state === 'active' ? '生效中' : '已释放' }}
              </a-tag>
              <span v-else-if="column.key === 'releaseSource'">{{ releaseSourceLabel(record.releaseSource) }}</span>
              <span v-else-if="column.key === 'releasedAt'">{{ formatDateTime(record.releasedAt) }}</span>
            </template>
          </a-table>
        </template>
      </a-spin>
    </a-modal>

    <!--
      用户名冲突的**二次确认**（76 §3.4.2 / 红线 11）。
      确认框里必须显示「会变成什么名字」——建议名由后端校验过不重复，
      所以管理员看到的名字就是恢复后真实会用的名字。
    -->
    <a-modal
      :open="conflictOpen"
      title="用户名已被占用"
      ok-text="确认恢复并使用建议用户名"
      cancel-text="取消"
      :confirm-loading="restoring"
      @ok="confirmRenameRestore"
      @cancel="cancelConflict"
      @update:open="setConflictOpen"
    >
      <p class="conflict-text">原用户名 <strong>{{ conflict.currentUsername }}</strong> 已被账号 #{{ conflict.occupiedByUserId }} 使用。</p>
      <p class="conflict-text">
        继续恢复将为该归档账号生成新用户名
        <strong>{{ conflict.suggestedUsername }}</strong>，请确认后再继续。
      </p>
      <p class="conflict-hint">取消不会修改任何数据：归属档状态保持不变。</p>
      <a-alert v-if="restoreError" type="error" show-icon :message="restoreError" class="status-alert" />
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
import { getArchivedUserDetail, getArchivedUsers, restoreArchivedUser } from '@/api/ArchivedUserApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

const REASON_LABELS = { admin_delete: '管理员删除', user_closure: '用户注销' }
const REASON_COLORS = { admin_delete: 'red', user_closure: 'gold' }
const RELEASE_SOURCES = {
  user_unbind: '用户解绑',
  admin_clear: '管理员清除',
  force_reassign: '强制换绑',
  account_close: '用户注销',
  admin_delete: '管理员删除',
}
const emptyFilters = () => ({ reason: undefined, keyword: '', startTime: '', endTime: '' })

export default {
  name: 'ArchivedUsersPage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell },
  setup() {
    const route = useRoute()
    const prefix = 'archived-users-' + useId()
    const fieldId = field => prefix + '-' + field
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const rows = ref([])
    const filters = reactive(emptyFilters())
    const appliedFilters = ref({})
    const loading = ref(false)
    const restoring = ref(false)
    const hasLoaded = ref(false)
    const total = ref(0)
    const lastLoadedAt = ref('')
    const filterError = ref('')
    const listError = ref('')
    const restoreError = ref('')
    const detailOpen = ref(false)
    const detailLoading = ref(false)
    const detailError = ref('')
    const detailUser = shallowRef(null)
    const detailBindings = ref([])
    const conflictOpen = ref(false)
    const conflict = reactive({ id: null, currentUsername: '', occupiedByUserId: null, suggestedUsername: '' })

    const pagination = reactive({
      current: 1, pageSize: 10, total: 0,
      showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'],
      showTotal: value => `共 ${value} 条`,
    })

    let alive = true
    let mounted = false
    let readVersion = 0
    let observedLogout = getCurrentUserLogoutEpoch()
    const mountedLogoutEpoch = observedLogout

    // 与其它管理页一致：身份、角色、登出任一变化都不再允许操作。
    const canManage = () => alive && route.path === mountedPath
      && getCurrentUserLogoutEpoch() === mountedLogoutEpoch && validAdmin()
    function validAdmin() {
      const user = currentUser.value
      const id = Number(user?.id)
      return Number.isSafeInteger(id) && id > 0 && id <= 2147483647 && isAdminUser(user)
    }

    const reasonOptions = [
      { label: '管理员删除', value: 'admin_delete' },
      { label: '用户注销', value: 'user_closure' },
    ]

    const columns = [
      { title: '用户名', dataIndex: 'username', key: 'username', width: 180 },
      { title: '昵称', dataIndex: 'nickname', key: 'nickname', width: 150 },
      { title: '归档原因', dataIndex: 'archivedReason', key: 'archivedReason', width: 120 },
      { title: '归档时间', dataIndex: 'archivedAt', key: 'archivedAt', width: 175 },
      { title: '操作者', dataIndex: 'archivedByUserId', key: 'archivedByUserId', width: 100 },
      { title: '余额', dataIndex: 'balance', key: 'balance', width: 120 },
      { title: '赠送余额', dataIndex: 'giftBalance', key: 'giftBalance', width: 120 },
      { title: '操作', key: 'action', width: 150, fixed: 'right' },
    ]

    const bindingColumns = [
      { title: '类型', dataIndex: 'kind', key: 'kind', width: 90 },
      { title: '值（脱敏）', dataIndex: 'valueMasked', key: 'valueMasked', width: 160 },
      { title: '状态', dataIndex: 'state', key: 'state', width: 100 },
      { title: '释放来源', dataIndex: 'releaseSource', key: 'releaseSource', width: 120 },
      { title: '释放时间', dataIndex: 'releasedAt', key: 'releasedAt', width: 175 },
    ]

    const reasonCounts = computed(() => {
      const counts = { admin_delete: 0, user_closure: 0 }
      for (const row of rows.value) {
        const key = String(row?.archivedReason || '').trim()
        if (Object.prototype.hasOwnProperty.call(counts, key)) counts[key] += 1
      }
      return counts
    })

    const scopeDescription = computed(() => {
      const scope = appliedFilters.value || {}
      const parts = [
        scope.reason && `原因：${reasonLabel(scope.reason)}`,
        scope.keyword && `关键字：${scope.keyword}`,
        scope.startTime && `归档起：${scope.startTime}`,
        scope.endTime && `归档止：${scope.endTime}`,
      ].filter(Boolean)
      return parts.length ? `当前筛选：${parts.join('；')}。` : '当前范围：全部已归档账号。'
    })

    const emptyText = computed(() => (listError.value
      ? '列表加载失败，请重试'
      : hasLoaded.value ? '暂无符合条件的已归档账号' : '已归档账号尚未读取成功'))

    function reasonLabel(value) {
      const key = String(value || '').trim()
      return REASON_LABELS[key] || key || '—'
    }
    function reasonColor(value) {
      return REASON_COLORS[String(value || '').trim()] || 'default'
    }
    function bindingKindLabel(value) {
      const key = String(value || '').trim()
      if (key === 'phone') return '手机号'
      if (key === 'wechat') return '微信'
      return key || '—'
    }
    function releaseSourceLabel(value) {
      const key = String(value || '').trim()
      return RELEASE_SOURCES[key] || key || '—'
    }
    function moneyText(value) {
      const amount = Number(value)
      return Number.isFinite(amount) ? `¥ ${formatMoneyValue(amount)}` : '--'
    }
    function formatDateTime(value) {
      if (!value) return '—'
      const date = new Date(value)
      if (!Number.isFinite(date.getTime())) return '—'
      return date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
    }
    function setFilter(field, value) {
      if (!canManage() || loading.value || restoring.value || !Object.hasOwn(filters, field)) return
      filters[field] = value
    }
    function onTableChange(pageInfo) {
      if (!canManage() || loading.value || restoring.value) return
      pagination.current = pageInfo?.current || 1
      pagination.pageSize = pageInfo?.pageSize || pagination.pageSize
      void load()
    }

    const buildParams = () => {
      const params = { page: pagination.current, pageSize: pagination.pageSize }
      const scope = appliedFilters.value || {}
      if (scope.reason) params.reason = scope.reason
      if (scope.keyword) params.keyword = scope.keyword
      if (scope.startTime) params.startTime = scope.startTime
      // 后端把日期型 endTime 当作「含当天」（内部换成次日零点开区间），
      // 所以前端原样传 YYYY-MM-DD，不做 +1 天换算，避免两边都加一次。
      if (scope.endTime) params.endTime = scope.endTime
      return params
    }

    // force=true 用于「写操作成功之后的刷新」。
    // ⚠️ 不能在 restoring 仍为 true 时被静默拒绝：那样恢复成功后列表不会刷新，
    // 界面上仍然显示这个账号（用户会以为恢复失败并重复点击）。
    const load = async (force = false) => {
      if (!canManage() || (restoring.value && !force)) return false
      const ticket = ++readVersion
      const owns = () => alive && ticket === readVersion
      loading.value = true
      listError.value = ''
      try {
        const res = await getArchivedUsers(buildParams())
        if (!owns()) return false
        if (res?.status !== 200) {
          listError.value = res?.message || '加载已归档账号失败，请稍后重试。'
          return false
        }
        const body = res?.data || {}
        rows.value = Array.isArray(body.data) ? body.data : []
        total.value = Number(body.total) || 0
        pagination.total = total.value
        hasLoaded.value = true
        lastLoadedAt.value = formatDateTime(new Date().toISOString())
        return true
      } catch (error) {
        if (owns()) listError.value = '加载已归档账号失败，请稍后重试。'
        return false
      } finally {
        if (owns()) loading.value = false
      }
    }

    const reload = () => {
      if (!canManage() || loading.value || restoring.value) return
      filterError.value = validateFilters()
      if (filterError.value) return
      appliedFilters.value = {
        reason: filters.reason,
        keyword: String(filters.keyword || '').trim(),
        startTime: String(filters.startTime || '').trim(),
        endTime: String(filters.endTime || '').trim(),
      }
      pagination.current = 1
      void load()
    }

    // 前端只做「明显错误」的拦截（格式与区间方向），业务判定一律信后端：
    // 两边都实现一套日期语义，出问题时要先分辨是哪一层的差一天。
    function validateFilters() {
      const pattern = /^\d{4}-\d{2}-\d{2}$/
      for (const field of ['startTime', 'endTime']) {
        const value = String(filters[field] || '').trim()
        if (value && !pattern.test(value)) return '日期格式应为 YYYY-MM-DD'
      }
      const start = String(filters.startTime || '').trim()
      const end = String(filters.endTime || '').trim()
      if (start && end && start > end) return '归档起始日期不能晚于结束日期'
      return ''
    }

    const resetFilters = () => {
      if (!canManage() || loading.value || restoring.value) return
      Object.assign(filters, emptyFilters())
      filterError.value = ''
      appliedFilters.value = {}
      pagination.current = 1
      void load()
    }

    const openDetail = async (record) => {
      if (!canManage() || loading.value) return
      const id = Number(record?.id)
      if (!Number.isInteger(id) || id <= 0) return
      detailOpen.value = true
      detailLoading.value = true
      detailError.value = ''
      detailUser.value = null
      detailBindings.value = []
      try {
        const res = await getArchivedUserDetail(id)
        if (!detailOpen.value) return
        if (res?.status !== 200) {
          detailError.value = res?.message || '读取账号详情失败，请稍后重试。'
          return
        }
        const body = res?.data || {}
        detailUser.value = body.data || null
        detailBindings.value = Array.isArray(body.bindings) ? body.bindings : []
      } catch (error) {
        if (detailOpen.value) detailError.value = '读取账号详情失败，请稍后重试。'
      } finally {
        detailLoading.value = false
      }
    }

    // 恢复：先探冲突（confirmRename=false），409 才弹二次确认。
    const startRestore = async (record) => {
      if (!canManage() || loading.value || restoring.value) return
      const id = Number(record?.id)
      if (!Number.isInteger(id) || id <= 0) return
      restoring.value = true
      restoreError.value = ''
      try {
        const res = await restoreArchivedUser(id, false)
        if (res?.status === 200) {
          message.success('已恢复该账号')
          // force：此刻 restoring 仍是 true，普通 load 会自我拒绝，列表就不会刷新。
          await load(true)
          return
        }
        const body = res?.data || {}
        if (res?.status === 409 && body.conflict === 'username') {
          conflict.id = id
          conflict.currentUsername = String(body.currentUsername || record?.username || '')
          conflict.occupiedByUserId = body.occupiedByUserId ?? null
          conflict.suggestedUsername = String(body.suggestedUsername || '')
          conflictOpen.value = true
          return
        }
        restoreError.value = body.message || res?.message || '恢复账号失败，请稍后重试。'
        conflictOpen.value = false
        message.error(restoreError.value)
      } catch (error) {
        restoreError.value = '恢复账号失败，请稍后重试。'
        message.error(restoreError.value)
      } finally {
        restoring.value = false
      }
    }

    const confirmRenameRestore = async () => {
      if (!canManage() || restoring.value) return
      const id = Number(conflict.id)
      if (!Number.isInteger(id) || id <= 0) return
      restoring.value = true
      restoreError.value = ''
      try {
        const res = await restoreArchivedUser(id, true)
        if (res?.status === 200) {
          conflictOpen.value = false
          message.success(`已恢复该账号，用户名改为 ${res?.data?.data?.username || conflict.suggestedUsername}`)
          await load(true)
          return
        }
        restoreError.value = res?.message || '恢复账号失败，请稍后重试。'
      } catch (error) {
        restoreError.value = '恢复账号失败，请稍后重试。'
      } finally {
        restoring.value = false
      }
    }

    const cancelConflict = () => {
      if (restoring.value) return
      conflictOpen.value = false
      restoreError.value = ''
      conflict.id = null
      conflict.suggestedUsername = ''
    }

    // 弹窗开关一律走具名处理函数（不在模板里给 ref 赋值）：
    // 模板内联赋值在 setup 返回 ref 时依赖代理行为，写错方向会静默不生效。
    const setDetailOpen = value => { detailOpen.value = value === true }
    const setConflictOpen = value => { if (!value) cancelConflict() }

    const resetView = () => {
      readVersion += 1
      rows.value = []
      total.value = 0
      pagination.total = 0
      hasLoaded.value = false
      lastLoadedAt.value = ''
      detailOpen.value = false
      conflictOpen.value = false
      restoreError.value = ''
      listError.value = ''
      filterError.value = ''
    }

    // 与其它管理页一致：操作者/角色/状态/登出/路由任一变化都作废在途读取，
    // 否则 A 的列表可能被 B 的响应覆盖（跨账号数据串台）。
    watch(() => [currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status], () => {
      resetView()
      if (mounted && canManage()) void load()
    }, { flush: 'sync' })
    watch(() => getCurrentUserLogoutEpoch(), value => {
      if (value === observedLogout) return
      observedLogout = value
      resetView()
    }, { flush: 'sync' })
    watch(() => route.path, () => { resetView() }, { flush: 'sync' })

    onMounted(() => {
      mounted = true
      if (canManage()) void load()
    })
    onBeforeUnmount(() => {
      alive = false
      mounted = false
      readVersion += 1
    })

    return {
      fieldId, canManage, rows, filters, loading, restoring, hasLoaded, total, lastLoadedAt,
      filterError, listError, restoreError, pagination, columns, bindingColumns, reasonOptions,
      reasonCounts, scopeDescription, emptyText, sumTableColumnWidths,
      detailOpen, detailLoading, detailError, detailUser, detailBindings,
      conflictOpen, conflict, load, reload, resetFilters, onTableChange, setFilter,
      openDetail, startRestore, confirmRenameRestore, cancelConflict, setDetailOpen, setConflictOpen,
      reasonLabel, reasonColor, bindingKindLabel, releaseSourceLabel, moneyText, formatDateTime,
    }
  },
}
</script>

<style scoped>
.archived-users-page { display: flex; flex-direction: column; gap: 16px; }
.filter-form { row-gap: 12px; }
.filter-keyword { width: 220px; }
.filter-reason { width: 160px; }
.filter-date { width: 160px; }
.filter-error { margin: 8px 0 0; color: var(--danger, #cf1322); }
.status-alert { margin-top: 12px; }
.archived-username { font-weight: 600; }
.archived-operator { color: var(--text-muted, #6b7280); }
.summary-value--small { font-size: 14px; }
.detail-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px; margin: 0; }
.detail-grid div { display: flex; gap: 8px; }
.detail-grid dt { color: var(--text-muted, #6b7280); min-width: 72px; }
.detail-grid dd { margin: 0; }
.detail-heading { margin: 20px 0 4px; font-size: 15px; }
.detail-hint { margin: 0 0 12px; color: var(--text-muted, #6b7280); font-size: 13px; }
.conflict-text { margin: 0 0 8px; }
.conflict-hint { margin: 0; color: var(--text-muted, #6b7280); font-size: 13px; }
@media (max-width: 767px) {
  .filter-keyword, .filter-reason, .filter-date { width: 100%; }
}
</style>
