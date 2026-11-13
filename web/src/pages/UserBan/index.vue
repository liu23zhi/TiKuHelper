<template>
  <div class="console-page user-ban-page">
    <ConsolePageHeading
      title="账号封禁"
      description="按账号封禁：封禁后该账号立即无法使用服务（双读判定：users.status 与 user_bans 任一命中即封禁）。解封是**软撤销** —— 封禁记录永久保留，只让判定失效。"
    >
      <template #actions>
        <a-button :loading="loading" :disabled="!canManage() || loading || writing" @click="loadRows">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert
      v-if="!canManage()"
      type="warning"
      show-icon
      class="page-alert"
      message="当前身份无账号封禁权限，请使用管理员账号登录。"
    />

    <template v-if="canManage()">
      <ConsoleSummaryGrid class="user-ban-summary">
        <div>
          <div class="summary-label">筛选结果</div>
          <div class="summary-value">{{ hasLoaded ? total : '--' }} 条</div>
        </div>
        <div>
          <div class="summary-label">当前范围</div>
          <div class="summary-value">{{ scopeLabel }}</div>
        </div>
      </ConsoleSummaryGrid>

      <ConsolePanel title="筛选" description="到期时间为「永久」表示该封禁不会自动解除；关键词匹配封禁原因与用户名。">
        <a-form layout="inline" class="filter-form" :on-finish="reload">
          <a-form-item label="生效状态" :html-for="fieldId('active')">
            <a-select
              :id="fieldId('active')"
              :value="filters.active"
              :options="activeOptions"
              class="filter-active"
              :disabled="loading || writing"
              @update:value="value => setFilter('active', value)"
              @change="reload"
            />
          </a-form-item>
          <a-form-item label="用户 ID" :html-for="fieldId('userId')">
            <a-input
              :id="fieldId('userId')"
              :value="filters.userId"
              allow-clear
              placeholder="精确匹配，如 42"
              class="filter-user-id"
              :disabled="loading || writing"
              @update:value="value => setFilter('userId', value)"
              @press-enter="reload"
            />
          </a-form-item>
          <a-form-item label="关键词" :html-for="fieldId('keyword')">
            <a-input
              :id="fieldId('keyword')"
              :value="filters.keyword"
              allow-clear
              placeholder="封禁原因 / 用户名"
              class="filter-keyword"
              :disabled="loading || writing"
              @update:value="value => setFilter('keyword', value)"
              @press-enter="reload"
            />
          </a-form-item>
          <a-form-item>
            <a-space wrap>
              <a-button type="primary" html-type="submit" :loading="loading" :disabled="loading || writing">查询</a-button>
              <a-button :disabled="loading || writing" @click="resetFilters">重置筛选</a-button>
              <a-button type="primary" ghost :disabled="loading || writing" @click="openCreate">封禁账号</a-button>
            </a-space>
          </a-form-item>
        </a-form>
        <p v-if="filterError" class="filter-error" role="alert">{{ filterError }}</p>
        <a-alert v-if="listError" type="error" show-icon :message="listError" class="status-alert" />
      </ConsolePanel>

      <ConsolePanel title="封禁记录" :description="tableDescription">
        <ConsoleTableShell label="封禁记录">
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
              <span v-if="column.key === 'userId'" class="number-cell">{{ record.userId ?? '-' }}</span>
              <span v-else-if="column.key === 'username'" class="reason-cell">{{ record.username || '（用户不存在或已归档）' }}</span>
              <span v-else-if="column.key === 'reason'" class="reason-cell">{{ record.reason || '—' }}</span>
              <span v-else-if="column.key === 'source'">{{ sourceLabel(record.source) }}</span>
              <span v-else-if="column.key === 'bannedAt'" class="time-cell">{{ formatDateTime(record.bannedAt) }}</span>
              <!-- 永久封禁必须显示「永久」：显示「—」或空会被读成「数据缺失」，
                   而「没填」与「永久」是相反语义。这是三类状态里最容易被误读的一个。 -->
              <span v-else-if="column.key === 'expiresAt'" class="time-cell">{{ formatExpiresAt(record.expiresAt) }}</span>
              <a-tag v-else-if="column.key === 'status'" :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
              <template v-else-if="column.key === 'action'">
                <a-space wrap>
                  <a-button
                    type="link"
                    danger
                    :disabled="writing || loading || record.status !== 'active'"
                    @click="confirmRevoke(record)"
                  >解封</a-button>
                </a-space>
              </template>
            </template>
          </a-table>
        </ConsoleTableShell>
      </ConsolePanel>

      <a-modal
        :open="createVisible"
        title="封禁账号"
        :confirm-loading="writing"
        ok-text="确认封禁"
        cancel-text="取消"
        :mask-closable="false"
        @cancel="closeCreate"
        @ok="submitCreate"
      >
        <a-form layout="vertical" class="create-form">
          <a-form-item label="用户 ID" required :html-for="fieldId('draftUserId')">
            <a-input
              :id="fieldId('draftUserId')"
              :value="draft.userId"
              :disabled="writing"
              placeholder="要封禁的用户 ID（users.id）"
              @update:value="value => setDraft('userId', value)"
            />
            <p class="field-hint">按账号封禁：账号 ID 是 users.id，可在「用户管理」里查到。</p>
          </a-form-item>
          <a-form-item label="封禁原因" required :html-for="fieldId('draftReason')">
            <a-input
              :id="fieldId('draftReason')"
              :value="draft.reason"
              :disabled="writing"
              placeholder="如：违规刷单"
              @update:value="value => setDraft('reason', value)"
            />
            <p class="field-hint">必填。封禁是风控动作，用户一定会来问「为什么」，没有原因的记录等于没有记录。</p>
          </a-form-item>
          <a-form-item label="是否永久封禁">
            <a-switch :checked="draft.permanent" :disabled="writing" @change="checked => setDraft('permanent', checked)" />
            <p class="field-hint">
              打开 = 永久封禁（不会自动解除）；关闭 = 临时封禁，需填写到期时间。
            </p>
          </a-form-item>
          <a-form-item v-if="!draft.permanent" label="到期时间" required :html-for="fieldId('draftExpiresAt')">
            <a-input
              :id="fieldId('draftExpiresAt')"
              :value="draft.expiresAt"
              :disabled="writing"
              placeholder="如 2026-11-20（当天 23:59:59 生效）或 2026-11-20T12:00:00Z"
              @update:value="value => setDraft('expiresAt', value)"
            />
            <p class="field-hint">到期时间必须晚于当前时间；填过去的时间会被后端拒绝（400）。</p>
          </a-form-item>
        </a-form>
        <p v-if="createError" class="filter-error" role="alert">{{ createError }}</p>
      </a-modal>

      <a-modal
        :open="revokeVisible"
        title="解除封禁"
        :confirm-loading="writing"
        ok-text="确认解封"
        cancel-text="取消"
        :mask-closable="false"
        @cancel="closeRevoke"
        @ok="submitRevoke"
      >
        <p class="revoke-hint">
          将解除 <strong>{{ revokeTarget?.username || '该用户' }}</strong>
          （ID {{ revokeTarget?.userId ?? '-' }}，原因：{{ revokeTarget?.reason || '无' }}）的封禁。
          解封是**软撤销**：封禁记录会永久保留（谁在什么时候因为什么封过），只让判定失效。
        </p>
        <a-form layout="vertical">
          <a-form-item label="解封原因（可选）" :html-for="fieldId('revokeReason')">
            <a-input
              :id="fieldId('revokeReason')"
              :value="revokeReason"
              :disabled="writing"
              placeholder="如：申诉通过"
              @update:value="value => setRevokeReason(value)"
            />
          </a-form-item>
        </a-form>
        <p v-if="revokeError" class="filter-error" role="alert">{{ revokeError }}</p>
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
import { createUserBan, getUserBans, revokeUserBan } from '@/api/UserBanApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

// 三态标签。后端的 status 由 expires_at 与 updated_at 推断得出
// （表里没有 revoked_at 列，见 controller 的 userBanRecordStatus 注释）。
const STATUS_LABELS = { active: '生效中', expired: '已过期', revoked: '已撤销' }
const STATUS_COLORS = { active: 'red', expired: 'default', revoked: 'green' }
const SOURCE_LABELS = { system: '系统自动', admin: '管理员' }

const emptyFilters = () => ({ active: 'true', userId: '', keyword: '' })
const emptyDraft = () => ({ userId: '', reason: '', permanent: true, expiresAt: '' })

function readNonnegativeInt(value) {
  if (!['number', 'string'].includes(typeof value) || String(value).trim() === '') return null
  const number = Number(value)
  return Number.isInteger(number) && number >= 0 ? number : null
}

export default {
  name: 'UserBanPage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell },
  setup() {
    const route = useRoute()
    const prefix = 'user-ban-' + useId()
    const fieldId = field => prefix + '-' + field
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const rows = ref([])
    const loading = ref(false)
    const writing = ref(false)
    const hasLoaded = ref(false)
    const total = ref(0)
    const listError = ref('')
    const filterError = ref('')
    const filters = reactive(emptyFilters())
    const appliedFilters = ref({})

    const createVisible = ref(false)
    const createError = ref('')
    const draft = reactive(emptyDraft())

    const revokeVisible = ref(false)
    const revokeTarget = ref(null)
    const revokeReason = ref('')
    const revokeError = ref('')

    const pagination = reactive({
      current: 1, pageSize: 20, total: 0,
      showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'],
      showTotal: value => `共 ${value} 条`,
    })

    let alive = true
    let mounted = false
    let readVersion = 0
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

    const activeOptions = [
      { label: '只看生效中', value: 'true' },
      { label: '全部（含已过期 / 已撤销）', value: 'false' },
    ]

    const columns = [
      { title: '用户 ID', dataIndex: 'userId', key: 'userId', width: 100 },
      { title: '用户名', dataIndex: 'username', key: 'username', width: 160 },
      { title: '封禁原因', dataIndex: 'reason', key: 'reason', width: 220 },
      { title: '来源', dataIndex: 'source', key: 'source', width: 100 },
      { title: '封禁时间', dataIndex: 'bannedAt', key: 'bannedAt', width: 175 },
      { title: '到期时间', dataIndex: 'expiresAt', key: 'expiresAt', width: 175 },
      { title: '状态', dataIndex: 'status', key: 'status', width: 100 },
      { title: '操作', key: 'action', width: 100 },
    ]

    const scopeLabel = computed(() => {
      const scope = appliedFilters.value
      const parts = [
        scope.active === 'true' ? '只看生效中' : '全部记录',
        scope.userId && `用户 ${scope.userId}`,
        scope.keyword && `关键词：${scope.keyword}`,
      ].filter(Boolean)
      return parts.join('；') || '全部记录'
    })

    const tableDescription = computed(() => (appliedFilters.value.active === 'true'
      ? '当前只显示生效中的封禁（未撤销且未到期）。查历史记录请把「生效状态」切到「全部」。'
      : '当前显示全部记录，含已过期与已撤销 —— 这些是审计事实，不会随解封删除。'))

    const emptyText = computed(() => {
      if (listError.value) return '列表加载失败，请重试'
      if (!hasLoaded.value) return '封禁记录尚未读取成功'
      return appliedFilters.value.active === 'true' ? '当前没有生效中的封禁' : '暂无封禁记录'
    })

    function statusLabel(value) {
      return STATUS_LABELS[String(value || '').trim()] || value || '-'
    }
    function statusColor(value) {
      return STATUS_COLORS[String(value || '').trim()] || 'default'
    }
    function sourceLabel(value) {
      return SOURCE_LABELS[String(value || '').trim()] || value || '-'
    }
    function formatDateTime(value) {
      if (!value) return '—'
      const date = new Date(value)
      if (!Number.isFinite(date.getTime())) return '—'
      return date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
    }
    // ⚠️ 本函数是「永久」这条判据的唯一落点。
    // expiresAt 为 null/undefined = 永久封禁（后端落库是 expires_at IS NULL），
    // 必须显示「永久」而不是「—」或空：后者会被读成「数据缺失」，
    // 而「没有到期时间」与「查询不到」是相反语义。
    // 空字符串同样按「永久」处理（后端对空串也解析为 nil）。
    function formatExpiresAt(value) {
      if (value === null || value === undefined || String(value).trim() === '') return '永久'
      const date = new Date(value)
      if (!Number.isFinite(date.getTime())) return '永久'
      return date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
    }

    const buildParams = () => {
      const params = { page: pagination.current, pageSize: pagination.pageSize }
      const active = String(appliedFilters.value.active || '').trim()
      if (active) params.active = active
      const userId = String(appliedFilters.value.userId || '').trim()
      if (userId) params.userId = userId
      const keyword = String(appliedFilters.value.keyword || '').trim()
      if (keyword) params.keyword = keyword
      return params
    }

    const loadRows = async () => {
      if (!canManage()) return false
      const ticket = ++readVersion
      const owns = () => canManage() && ticket === readVersion
      loading.value = true
      try {
        const res = await getUserBans(buildParams())
        if (!owns()) return false
        const body = res?.data
        const items = body?.data
        const count = readNonnegativeInt(body?.total)
        // 失败时必须保留上一批行并明确报错：把失败渲染成空列表会让运营
        // 以为封禁被清空了 —— 那是最危险的误读（以为没人被封）。
        if (res?.status !== 200 || !Array.isArray(items) || count === null) {
          listError.value = '封禁记录刷新失败，页面显示的仍是上次读取的结果。'
          return false
        }
        rows.value = items.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
        total.value = count
        pagination.total = count
        hasLoaded.value = true
        listError.value = ''
        return true
      } catch (error) {
        if (owns()) listError.value = '封禁记录刷新失败，页面显示的仍是上次读取的结果。'
        return false
      } finally {
        if (owns()) loading.value = false
      }
    }

    const setFilter = (field, value) => {
      if (!canManage() || loading.value || writing.value || !Object.hasOwn(filters, field)) return
      filters[field] = value
    }
    const setDraft = (field, value) => {
      if (!canManage() || writing.value || !Object.hasOwn(draft, field)) return
      draft[field] = value
    }
    const setRevokeReason = value => {
      if (!canManage() || writing.value) return
      revokeReason.value = String(value == null ? '' : value)
    }

    const reload = () => {
      if (!canManage() || loading.value || writing.value) return
      filterError.value = ''
      const userId = String(filters.userId || '').trim()
      if (userId && !/^[1-9]\d*$/.test(userId)) {
        // 前端先拦：非正整数会让后端 400，但页面要能立刻指出是哪个字段错了。
        filterError.value = '用户 ID 必须是正整数（留空表示不按用户筛选）。'
        return
      }
      appliedFilters.value = {
        active: filters.active === 'false' ? 'false' : 'true',
        userId,
        keyword: String(filters.keyword || '').trim(),
      }
      pagination.current = 1
      void loadRows()
    }

    const resetFilters = () => {
      if (!canManage() || loading.value || writing.value) return
      Object.assign(filters, emptyFilters())
      reload()
    }

    const onTableChange = next => {
      if (!canManage() || writing.value) return
      pagination.current = Number(next?.current) || 1
      pagination.pageSize = Number(next?.pageSize) || pagination.pageSize
      void loadRows()
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
      const userId = String(draft.userId || '').trim()
      if (!/^[1-9]\d*$/.test(userId)) {
        createError.value = '请填写正确的用户 ID（正整数）。'
        return
      }
      const reason = String(draft.reason || '').trim()
      if (!reason) {
        createError.value = '封禁原因不能为空。'
        return
      }
      // 永久与临时是互斥的两条路径：永久必须传空 expiresAt（= NULL），
      // 临时必须给出时间。这里不替运营猜 —— 猜成永久会把「只想封 1 小时」变成永久。
      let expiresAt = ''
      if (!draft.permanent) {
        expiresAt = String(draft.expiresAt || '').trim()
        if (!expiresAt) {
          createError.value = '临时封禁必须填写到期时间（或打开「是否永久封禁」）。'
          return
        }
      }
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      writing.value = true
      createError.value = ''
      let created = false
      try {
        const res = await createUserBan({ userId, reason, expiresAt })
        if (!owns()) return
        if (res?.status !== 201) {
          // 后端对空原因 / 过去时间返回 400 + 具体文案，原样展示：
          // 运营要能据此改对，而不是看到一句「封禁失败」。
          createError.value = String(res?.data?.message || '').trim() || '封禁失败'
          return
        }
        message.success(draft.permanent ? '已永久封禁该账号' : '已临时封禁该账号')
        created = true
      } catch (error) {
        if (owns()) {
          createError.value = String(error?.response?.data?.message || '').trim() || '封禁失败'
        }
      } finally {
        if (owns()) writing.value = false
      }
      if (created && owns()) {
        createVisible.value = false
        await loadRows()
      }
    }

    const confirmRevoke = record => {
      if (!canManage() || writing.value || !record) return
      revokeTarget.value = { ...record }
      revokeReason.value = ''
      revokeError.value = ''
      revokeVisible.value = true
    }
    const closeRevoke = () => {
      if (writing.value) return
      revokeVisible.value = false
      revokeError.value = ''
    }

    const submitRevoke = async () => {
      if (!canManage() || writing.value) return
      const target = revokeTarget.value
      if (!target || !Number.isInteger(Number(target.userId))) {
        revokeError.value = '解封目标无效，请刷新后重试。'
        return
      }
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      writing.value = true
      revokeError.value = ''
      let revoked = false
      try {
        // 参数是 userId（不是记录 id）：后端的解封语义是「解除这个用户的封禁」，
        // 会处理该用户全部未到期行。传记录 id 会解封错账号。
        const res = await revokeUserBan(target.userId, revokeReason.value)
        if (!owns()) return
        if (res?.status !== 200) {
          revokeError.value = String(res?.data?.message || '').trim() || '解封失败'
          return
        }
        message.success('已解除该账号的封禁（记录已保留）')
        revoked = true
      } catch (error) {
        if (owns()) {
          revokeError.value = String(error?.response?.data?.message || '').trim() || '解封失败'
        }
      } finally {
        if (owns()) writing.value = false
      }
      if (revoked && owns()) {
        revokeVisible.value = false
        await loadRows()
      }
    }

    const retire = () => {
      readVersion += 1
      writeVersion += 1
    }

    watch(() => [currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status], () => {
      retire()
      rows.value = []; total.value = 0; pagination.total = 0; hasLoaded.value = false
      createVisible.value = false
      revokeVisible.value = false
      Object.assign(draft, emptyDraft())
      if (mounted && canManage()) void loadRows()
    }, { flush: 'sync' })
    watch(() => getCurrentUserLogoutEpoch(), value => {
      if (value === observedLogout) return
      observedLogout = value
      retire()
      rows.value = []; total.value = 0; pagination.total = 0; hasLoaded.value = false
      createVisible.value = false
      revokeVisible.value = false
      listError.value = ''; createError.value = ''; revokeError.value = ''
    }, { flush: 'sync' })
    watch(() => route.path, () => {
      retire()
      rows.value = []; total.value = 0; pagination.total = 0; hasLoaded.value = false
      createVisible.value = false
      revokeVisible.value = false
    }, { flush: 'sync' })

    onMounted(() => {
      mounted = true
      if (canManage()) void loadRows()
    })
    onBeforeUnmount(() => {
      alive = false
      mounted = false
      retire()
    })

    return {
      fieldId, canManage,
      rows, loading, writing, hasLoaded, total, listError, filterError,
      filters, appliedFilters, activeOptions, columns, pagination,
      scopeLabel, tableDescription, emptyText,
      statusLabel, statusColor, sourceLabel, formatDateTime, formatExpiresAt,
      setFilter, loadRows, reload, resetFilters, onTableChange,
      createVisible, createError, draft, openCreate, closeCreate, submitCreate, setDraft,
      revokeVisible, revokeTarget, revokeReason, revokeError,
      confirmRevoke, closeRevoke, submitRevoke, setRevokeReason,
      sumTableColumnWidths,
    }
  },
}
</script>

<style scoped>
.page-alert, .filter-form, .status-alert { margin-bottom: 16px; }
.user-ban-summary { margin-bottom: 16px; }
.summary-label { color: var(--console-muted); font-size: 14px; }
.summary-value { margin-top: 8px; color: var(--console-text); font-size: clamp(18px, 2.2vw, 24px); font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.filter-active { width: 210px; }
.filter-user-id { width: 150px; }
.filter-keyword { width: 200px; }
.filter-error { margin: 0 0 16px; color: var(--console-danger, #c13e41); font-size: 13px; }
.field-hint { margin: 6px 0 0; color: var(--console-muted); font-size: 12px; line-height: 1.6; }
.revoke-hint { margin: 0 0 16px; color: var(--console-text); font-size: 13px; line-height: 1.7; }
.create-form { max-width: 560px; }
.number-cell, .time-cell { font-variant-numeric: tabular-nums; }
.reason-cell { overflow-wrap: anywhere; }
@media (max-width: 640px) {
  .filter-form :deep(.ant-form-item) { width: 100%; margin-inline-end: 0; }
  .filter-active, .filter-user-id, .filter-keyword { width: 100%; }
  .create-form { max-width: 100%; }
}
</style>
