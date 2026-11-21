<template>
  <div class="console-page device-admin-page">
    <ConsolePageHeading
      title="设备管理"
      description="按设备处置：解绑只释放槽位（用户下次请求会静默重绑），封禁则写入全局黑名单并**立即撤销该设备的全部绑定**（用户当场掉线，且无法自行解除）。"
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
      message="当前身份无设备管理权限，请使用管理员账号登录。"
    />

    <template v-if="canManage()">
      <a-alert
        v-if="hasLoaded && !revocationSupported"
        type="warning"
        show-icon
        class="page-alert"
        message="当前数据库缺少 revoked_at / revoked_reason 列（A1-2 未落地）：解绑与封禁的「立即撤销」都不可用，封禁只写黑名单。"
      />

      <ConsoleSummaryGrid class="device-summary">
        <div>
          <div class="summary-label">筛选结果</div>
          <div class="summary-value">{{ hasLoaded ? total : '--' }} 条</div>
        </div>
        <div>
          <div class="summary-label">当前范围</div>
          <div class="summary-value">{{ scopeLabel }}</div>
        </div>
      </ConsoleSummaryGrid>

      <ConsolePanel title="筛选" description="搜索同时匹配设备 ID 与用户名。状态三档：使用中 / 已解绑 / 封禁中（封禁优先级最高）。">
        <a-form layout="inline" class="filter-form" :on-finish="reload">
          <a-form-item label="关键词" :html-for="fieldId('keyword')">
            <a-input
              :id="fieldId('keyword')"
              :value="filters.keyword"
              allow-clear
              placeholder="用户名 / 设备 ID"
              class="filter-keyword"
              :disabled="busy()"
              @update:value="value => setFilter('keyword', value)"
              @press-enter="reload"
            />
          </a-form-item>
          <a-form-item label="状态" :html-for="fieldId('status')">
            <a-select
              :id="fieldId('status')"
              :value="filters.status"
              :options="statusOptions"
              class="filter-status"
              :disabled="busy()"
              @update:value="value => setFilter('status', value)"
              @change="reload"
            />
          </a-form-item>
          <a-form-item>
            <a-space wrap>
              <a-button type="primary" html-type="submit" :loading="loading" :disabled="busy()">查询</a-button>
              <a-button :disabled="busy()" @click="resetFilters">重置筛选</a-button>
              <a-button type="primary" ghost :disabled="busy()" @click="openBanModal('')">按设备 ID 封禁</a-button>
            </a-space>
          </a-form-item>
        </a-form>
        <p v-if="filterError" class="filter-error" role="alert">{{ filterError }}</p>
        <a-alert v-if="listError" type="error" show-icon :message="listError" class="status-alert" />
        <a-alert v-if="actionWarning" type="warning" show-icon :message="actionWarning" class="status-alert" />
      </ConsolePanel>

      <ConsolePanel title="设备绑定" :description="tableDescription">
        <ConsoleTableShell label="设备绑定列表">
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
              <span v-if="column.key === 'deviceId'" class="mono-cell" :title="record.deviceId">{{ shortDeviceId(record.deviceId) }}</span>
              <span v-else-if="column.key === 'deviceLabel'">{{ record.deviceLabel || '—' }}</span>
              <span v-else-if="column.key === 'userId'" class="number-cell">{{ record.userId ?? '-' }}</span>
              <span v-else-if="column.key === 'username'">{{ record.username || '（账号不存在或已归档）' }}</span>
              <span v-else-if="column.key === 'firstSeenAt'" class="time-cell">{{ formatDateTime(record.firstSeenAt) }}</span>
              <span v-else-if="column.key === 'lastSeenAt'" class="time-cell">{{ formatDateTime(record.lastSeenAt) }}</span>
              <span v-else-if="column.key === 'revokedAt'" class="time-cell">
                <template v-if="record.revokedAt">
                  {{ formatDateTime(record.revokedAt) }}
                  <span class="revoke-reason">（{{ revokeReasonText(record.revokedReason) }}）</span>
                </template>
                <template v-else>—</template>
              </span>
              <!-- 永久封禁必须显示「永久」：显示「—」会被读成数据缺失，
                   而「没填」与「永不自动解除」是相反语义。 -->
              <span v-else-if="column.key === 'banExpiresAt'" class="time-cell">
                <template v-if="record.banned">{{ banExpiryText(record.banExpiresAt, formatDateTime) }}</template>
                <template v-else>—</template>
              </span>
              <a-tag v-else-if="column.key === 'status'" :color="deviceStatusColor(record.status)">
                {{ deviceStatusText(record.status) }}
              </a-tag>
              <template v-else-if="column.key === 'action'">
                <a-space wrap>
                  <a-tooltip :title="canUnbindDevice(record, revocationSupported) ? '断开这一行绑定（不封设备）' : unbindBlockedHint(record, revocationSupported)">
                    <a-button
                      type="link"
                      :disabled="busy() || !canUnbindDevice(record, revocationSupported)"
                      @click="confirmUnbind(record)"
                    >解绑</a-button>
                  </a-tooltip>
                  <a-button
                    v-if="deviceBanAction(record) === 'ban'"
                    type="link"
                    danger
                    :disabled="busy()"
                    @click="openBanModal(record.deviceId)"
                  >封禁</a-button>
                  <a-button
                    v-else
                    type="link"
                    :disabled="busy()"
                    @click="confirmUnban(record)"
                  >解封</a-button>
                  <a-tooltip :title="record.revokedAt ? '仅删除绑定历史，保留封禁与拉黑状态' : '请先解绑设备'">
                    <a-button type="link" danger :disabled="busy() || !record.revokedAt" @click="confirmDelete(record)">删除记录</a-button>
                  </a-tooltip>
                </a-space>
              </template>
            </template>
          </a-table>
        </ConsoleTableShell>
      </ConsolePanel>

      <!-- 孤立封禁：有封禁规则但**没有任何绑定行**的设备（137 §9.2 的 `—  已封禁` 那一行）。
           不单独列出来的话，这批设备在页面上完全不可见 ⇒ 永远无法解封。 -->
      <ConsolePanel
        v-if="hasLoaded && orphanBans.length > 0"
        title="未绑定设备的封禁"
        description="这些设备 ID 已被封禁，但当前没有任何绑定行（账号已解绑或从未绑定）。不处理它们会一直拦着这台设备。"
      >
        <a-table
          :data-source="orphanBans"
          :columns="orphanColumns"
          :pagination="false"
          row-key="entryId"
          size="small"
        >
          <template #bodyCell="{ column, record }">
            <span v-if="column.key === 'deviceId'" class="mono-cell" :title="record.deviceId">{{ shortDeviceId(record.deviceId) }}</span>
            <span v-else-if="column.key === 'expiresAt'" class="time-cell">{{ banExpiryText(record.expiresAt, formatDateTime) }}</span>
            <span v-else-if="column.key === 'reason'">{{ record.reason || '—' }}</span>
            <template v-else-if="column.key === 'action'">
              <a-button type="link" :disabled="busy()" @click="confirmUnban(record)">解封</a-button>
            </template>
          </template>
        </a-table>
      </ConsolePanel>

      <a-modal
        :open="banVisible"
        title="封禁设备"
        ok-text="确认封禁"
        cancel-text="取消"
        :confirm-loading="writing"
        @ok="submitBan"
        @cancel="closeBanModal"
      >
        <a-alert
          type="warning"
          show-icon
          class="modal-alert"
          message="封禁会同时做两件事：写入全局黑名单（用户无法自行解除）并立即撤销该设备的全部绑定。用户会当场掉线，且无法自助恢复。"
        />
        <a-form layout="vertical">
          <a-form-item label="设备 ID" required :html-for="fieldId('ban-device')">
            <a-input
              :id="fieldId('ban-device')"
              :value="draft.deviceId"
              placeholder="如 exe:a1b2c3d4"
              :disabled="writing"
              @update:value="value => setDraft('deviceId', value)"
            />
          </a-form-item>
          <a-form-item label="封禁原因" required :html-for="fieldId('ban-reason')">
            <a-input
              :id="fieldId('ban-reason')"
              :value="draft.reason"
              placeholder="必填：运营与申诉都要能看懂"
              :disabled="writing"
              @update:value="value => setDraft('reason', value)"
            />
          </a-form-item>
          <a-form-item label="是否永久封禁">
            <a-switch :checked="draft.permanent" :disabled="writing" @update:checked="value => setDraft('permanent', value)" />
          </a-form-item>
          <a-form-item v-if="!draft.permanent" label="封禁时长（小时）" :html-for="fieldId('ban-hours')">
            <a-input-number
              :id="fieldId('ban-hours')"
              :value="draft.durationHours"
              :min="1"
              :precision="0"
              :disabled="writing"
              style="width: 100%"
              @update:value="value => setDraft('durationHours', value)"
            />
            <p class="field-hint">时长由服务端换算成到期时间，不吃浏览器时钟偏差。</p>
          </a-form-item>
        </a-form>
        <p v-if="banError" class="filter-error" role="alert">{{ banError }}</p>
      </a-modal>
    </template>
    <AdminDataTransfer v-if="!embedded && canManage()" resource="devices" title="设备绑定记录" />

  </div>
</template>

<script>
import AdminDataTransfer from '@/components/AdminDataTransfer.vue'
import { computed, onBeforeUnmount, onMounted, reactive, ref, useId, watch } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import Swal from 'sweetalert2'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import ConsoleTableShell from '@/components/ConsoleTableShell.vue'
import { banAdminDevice, deleteAdminDevice, getAdminDevices, unbanAdminDevice, unbindAdminDevice } from '@/api/DeviceAdminApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'
import {
  DEVICE_STATUS_ROWS,
  banExpiryText,
  canUnbindDevice,
  deviceBanAction,
  deviceStatusColor,
  deviceStatusText,
  readDeviceCount,
  revokeReasonText,
  shortDeviceId,
  unbindBlockedHint,
} from '@/utils/deviceAdminHelpers.mjs'

const emptyFilters = () => ({ keyword: '', status: '' })
const emptyDraft = () => ({ deviceId: '', reason: '', permanent: true, durationHours: 24 })

export default {
  props: { userId: { type: Number, default: 0 }, embedded: Boolean },
  name: 'DeviceAdminPage',
  components: { AdminDataTransfer, ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell },
  setup(props = {}) {
    const route = useRoute()
    const prefix = 'device-admin-' + useId()
    const fieldId = field => prefix + '-' + field
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const rows = ref([])
    const orphanBans = ref([])
    const loading = ref(false)
    const writing = ref(false)
    const hasLoaded = ref(false)
    const total = ref(0)
    const listError = ref('')
    const filterError = ref('')
    const actionWarning = ref('')
    // 后端明确回「表缺撤销列」时置 false：页面据此禁用解绑按钮并解释原因，
    // 而不是点了才收 503。
    const revocationSupported = ref(true)
    const filters = reactive(emptyFilters())
    const appliedFilters = ref({})
    // scopedUserId 来自 ?userId=（A4-2：用户管理页「设备」按钮跳进来时带上）。
    //
    // 与 filters 分开存是刻意的：filters 是用户可改的表单，本值是「进入本页时的
    // 上下文」，不该出现在搜索框里让用户以为能改。0 = 未限定（设备管理页常态）。
    //
    // 解析失败一律按 0 处理：一个手改的坏 URL 不该让整页报错，显示全部设备是
    // 可接受的降级（与后端 userId 解析失败的口径一致）。
    const scopedUserId = computed(() => {
      const raw = String(props.userId > 0 ? props.userId : route.query.userId ?? "").trim()
      const parsed = Number(raw)
      return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : 0
    })

    const banVisible = ref(false)
    const banError = ref('')
    const draft = reactive(emptyDraft())

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
    const busy = () => loading.value || writing.value

    const statusOptions = [
      { label: '全部状态', value: '' },
      ...DEVICE_STATUS_ROWS.map(row => ({ label: row.label, value: row.key })),
    ]

    const columns = [
      { title: '设备 ID', dataIndex: 'deviceId', key: 'deviceId', width: 190 },
      { title: '设备名', dataIndex: 'deviceLabel', key: 'deviceLabel', width: 150 },
      { title: '用户 ID', dataIndex: 'userId', key: 'userId', width: 90 },
      { title: '所属账号', dataIndex: 'username', key: 'username', width: 150 },
      { title: '首次出现', dataIndex: 'firstSeenAt', key: 'firstSeenAt', width: 175 },
      { title: '最近活跃', dataIndex: 'lastSeenAt', key: 'lastSeenAt', width: 175 },
      { title: '解绑时间', dataIndex: 'revokedAt', key: 'revokedAt', width: 220 },
      { title: '封禁到期', dataIndex: 'banExpiresAt', key: 'banExpiresAt', width: 165 },
      { title: '状态', dataIndex: 'status', key: 'status', width: 100 },
      { title: '操作', key: 'action', width: 230 },
    ]

    const orphanColumns = [
      { title: '设备 ID', dataIndex: 'deviceId', key: 'deviceId', width: 190 },
      { title: '封禁原因', dataIndex: 'reason', key: 'reason', width: 220 },
      { title: '到期时间', dataIndex: 'expiresAt', key: 'expiresAt', width: 165 },
      { title: '操作', key: 'action', width: 90 },
    ]

    const scopeLabel = computed(() => {
      const scope = appliedFilters.value
      const parts = [
        scope.keyword && `关键词：${scope.keyword}`,
        scope.status && `状态：${deviceStatusText(scope.status)}`,
      // 按账号限定时必须**显式**写在说明里（A4-2）。
      //
      // 否则「只看到 2 台设备」与「这台机器上真的只有 2 台」看起来一样，
      // 而管理员会据此判断「这个人没多开」—— 一个静默的作用域收窄不该是隐形的。
      scopedUserId.value > 0 && `仅显示账号 #${scopedUserId.value}`,
      ].filter(Boolean)
      return parts.join('；') || '全部设备'
    })

    const tableDescription = computed(() => '每行是「某账号的某台设备」的一次绑定。同一台设备可能出现在多个账号下（工作室的典型形态）—— 封禁按 device_id 生效，会一次影响全部账号。')

    const emptyText = computed(() => {
      if (listError.value) return '列表加载失败，请重试'
      if (!hasLoaded.value) return '设备列表尚未读取成功'
      return '暂无符合条件的设备'
    })

    const formatDateTime = value => {
      if (!value) return '—'
      const date = new Date(value)
      if (!Number.isFinite(date.getTime())) return '—'
      return date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
    }

    const buildParams = () => {
      const params = { page: pagination.current, pageSize: pagination.pageSize }
      const keyword = String(appliedFilters.value.keyword || '').trim()
      if (keyword) params.keyword = keyword
      const status = String(appliedFilters.value.status || '').trim()
      if (status) params.status = status
      // 按账号过滤（A4-2：从用户管理页的「设备」按钮跳进来）。
      //
      // ⚠️ 必须随**每次请求**一起带上，不能只在挂载时用一次：
      // 一旦翻页/刷新时丢掉它，第 2 页就会变回「全部用户的设备」——
      // 而列表说明仍写着「仅显示该账号」，是「看起来正常但内容是错的」那类故障。
      //
      // 值来自 route.query，与 filters 分开存：filters 是用户可改的表单，
      // userId 是「进入本页时的上下文」，不该出现在搜索框里让用户以为能改。
      if (scopedUserId.value > 0) params.userId = String(scopedUserId.value)
      return params
    }

    const loadRows = async () => {
      if (!canManage()) return false
      const ticket = ++readVersion
      const owns = () => canManage() && ticket === readVersion
      loading.value = true
      try {
        const res = await getAdminDevices(buildParams())
        if (!owns()) return false
        const body = res?.data
        const items = body?.data
        const count = readDeviceCount(body?.total)
        // 失败时必须保留上一批行并明确报错：渲染成空列表会让运营
        // 以为设备都被清空了（最危险的误读）。
        if (res?.status !== 200 || !Array.isArray(items) || count === null) {
          listError.value = '设备列表刷新失败，页面显示的仍是上次读取的结果。'
          return false
        }
        rows.value = items.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
        orphanBans.value = Array.isArray(body?.orphanBans)
          ? body.orphanBans.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
          : []
        // ⚠️ 用 === false 判而不是 !body?.revocationSupported：
        // 旧后端不返回该字段时它是 undefined，用 `!` 会把「不支持」误判出来、
        // 把所有解绑按钮禁掉。缺字段 = 按支持处理（后端会如实 503）。
        revocationSupported.value = body?.revocationSupported !== false
        total.value = count
        pagination.total = count
        hasLoaded.value = true
        listError.value = ''
        return true
      } catch (error) {
        if (owns()) listError.value = '设备列表刷新失败，页面显示的仍是上次读取的结果。'
        return false
      } finally {
        if (owns()) loading.value = false
      }
    }

    const setFilter = (field, value) => {
      if (!canManage() || busy() || !Object.hasOwn(filters, field)) return
      filters[field] = value
    }
    const setDraft = (field, value) => {
      if (!canManage() || writing.value || !Object.hasOwn(draft, field)) return
      draft[field] = value
    }

    const reload = () => {
      if (!canManage() || busy()) return
      filterError.value = ''
      actionWarning.value = ''
      appliedFilters.value = {
        keyword: String(filters.keyword || '').trim(),
        status: String(filters.status || '').trim(),
      }
      pagination.current = 1
      void loadRows()
    }

    const resetFilters = () => {
      if (!canManage() || busy()) return
      Object.assign(filters, emptyFilters())
      appliedFilters.value = {}
      pagination.current = 1
      void loadRows()
    }

    const onTableChange = next => {
      if (!canManage() || writing.value) return
      pagination.current = Number(next?.current) || 1
      pagination.pageSize = Number(next?.pageSize) || pagination.pageSize
      void loadRows()
    }

    const runWrite = async (action, successText) => {
      if (!canManage() || busy()) return null
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      writing.value = true
      actionWarning.value = ''
      try {
        const res = await action()
        if (!owns()) return null
        if (typeof res?.status !== 'number' || res.status < 200 || res.status >= 300) {
          // 400/404/503 都是「可解释的业务结果」，原样展示后端文案：
          // 运营要能据此改对，而不是看到一句「操作失败」。
          actionWarning.value = String(res?.data?.message || '').trim() || '操作失败，请稍后重试。'
          return res
        }
        message.success(typeof successText === 'function' ? successText(res) : successText)
        return res
      } catch (error) {
        if (owns()) {
          actionWarning.value = String(error?.response?.data?.message || '').trim() || '操作失败，请稍后重试。'
        }
        return null
      } finally {
        if (owns()) writing.value = false
      }
    }

    const confirmUnbind = async record => {
      // 三道闸门缺一不可：结构支持、行可解绑、身份仍有效。
      if (!canUnbindDevice(record, revocationSupported.value) || !canManage() || busy()) return false
      const result = await Swal.fire({
        title: '确认强制解绑这台设备？',
        text: `设备 ${shortDeviceId(record.deviceId)}，账号 ${record.username || record.userId}。`
          + '解绑只释放槽位，下次请求会静默重绑；要拦住这台设备请用「封禁」。',
        icon: 'warning',
        showCancelButton: true,
        confirmButtonText: '确认解绑',
        cancelButtonText: '取消',
      })
      if (!result.isConfirmed || !canManage()) return false
      await runWrite(() => unbindAdminDevice(record.id), '已解绑该设备')
      return loadRows()
    }

    const confirmDelete = async record => {
      if (!record?.revokedAt || !canManage() || busy()) return false
      const result = await Swal.fire({ title: '删除这条设备记录？', text: '仅清除已解绑的历史记录；设备封禁和账号拉黑状态会保留。', icon: 'warning', showCancelButton: true, confirmButtonText: '删除记录', cancelButtonText: '取消' })
      if (!result.isConfirmed || !canManage()) return false
      await runWrite(() => deleteAdminDevice(record.id), '已删除设备记录')
      return loadRows()
    }

    const openBanModal = deviceId => {
      if (!canManage() || busy()) return
      Object.assign(draft, emptyDraft())
      draft.deviceId = String(deviceId || '')
      banError.value = ''
      banVisible.value = true
    }
    const closeBanModal = () => {
      if (writing.value) return
      banVisible.value = false
      banError.value = ''
    }

    const submitBan = async () => {
      if (!canManage() || busy()) return
      const deviceId = String(draft.deviceId || '').trim()
      if (!deviceId) {
        banError.value = '设备 ID 不能为空。'
        return
      }
      const reason = String(draft.reason || '').trim()
      if (!reason) {
        banError.value = '封禁原因不能为空。'
        return
      }
      // 永久与临时互斥：永久不传时长（= NULL），临时必须给正整数。
      // 不替运营猜 —— 猜成永久会把「只想封 1 小时」变成永久。
      let durationAmount = 0
      if (!draft.permanent) {
        durationAmount = Number(draft.durationHours)
        if (!Number.isInteger(durationAmount) || durationAmount <= 0) {
          banError.value = '临时封禁必须填写正整数小时（或打开「是否永久封禁」）。'
          return
        }
      }
      banError.value = ''
      const res = await runWrite(() => banAdminDevice({ deviceId, reason, durationAmount, durationUnit: 'hour' }),
        payload => {
          const revoked = Number(payload?.data?.revokedBindings) || 0
          return revoked > 0 ? `已封禁该设备并撤销 ${revoked} 条绑定` : '已封禁该设备'
        })
      if (res && typeof res.status === 'number' && res.status >= 200 && res.status < 300) {
        banVisible.value = false
        await loadRows()
      }
    }

    const confirmUnban = async record => {
      if (!canManage() || busy()) return false
      const deviceId = String(record?.deviceId || '').trim()
      if (!deviceId) return false
      const result = await Swal.fire({
        title: '确认解封这台设备？',
        text: `设备 ${shortDeviceId(deviceId)} 将可以再次使用。解封不恢复已撤销的绑定（那是审计事实）。`,
        icon: 'warning',
        showCancelButton: true,
        confirmButtonText: '确认解封',
        cancelButtonText: '取消',
      })
      if (!result.isConfirmed || !canManage()) return false
      await runWrite(() => unbanAdminDevice(deviceId), '已解封该设备')
      return loadRows()
    }

    const retire = () => {
      readVersion += 1
      writeVersion += 1
      pagination.current = 1
    }

    watch(() => [currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status], () => {
      retire()
      rows.value = []
      orphanBans.value = []
      total.value = 0
      pagination.total = 0
      hasLoaded.value = false
      actionWarning.value = ''
      if (mounted && canManage()) void loadRows()
    }, { flush: 'sync' })
    watch(() => getCurrentUserLogoutEpoch(), value => {
      if (value === observedLogout) return
      observedLogout = value
      retire()
      rows.value = []
      orphanBans.value = []
      total.value = 0
      pagination.total = 0
      hasLoaded.value = false
    }, { flush: 'sync' })
    watch(() => route.path, () => {
      retire()
      rows.value = []
      orphanBans.value = []
      total.value = 0
      pagination.total = 0
      hasLoaded.value = false
      banVisible.value = false
    }, { flush: 'sync' })

// ?userId= 变化必须重新取数（A4-2）。
//
// 为什么要单独一条 watch：上面那条 `route.path` 的 watch 只在**路径**变化时
// 清空数据，而「从用户 A 的设备跳到用户 B 的设备」是同一个 path 上的 query 变化，
// 它不会触发。不加这条，用户会在 B 的面板里继续看到 A 的设备 —— 而且标题
// 已经变成「仅显示账号 #B」，是最容易让人做出错误判断的那种不一致。
//
// 刻意不复用那条清空逻辑：这里要的是「清空后立刻重新取数」，而不是「清空等下次挂载」。
watch(scopedUserId, () => {
  if (!alive || !canManage()) return
  readVersion += 1
  rows.value = []
  orphanBans.value = []
  total.value = 0
  pagination.total = 0
  pagination.current = 1
  hasLoaded.value = false
  void loadRows()
})

    onMounted(() => {
      mounted = true
      // 只发这一条列表请求：本页不在挂载期补查任何汇总。
      if (canManage()) void loadRows()
    })
    onBeforeUnmount(() => {
      alive = false
      mounted = false
      readVersion += 1
      writeVersion += 1
    })

    return {
      fieldId, canManage, busy, columns, orphanColumns, rows, orphanBans, filters,
      loading, writing, hasLoaded, total, listError, filterError, actionWarning,
      revocationSupported, pagination, scopeLabel, tableDescription, emptyText, scopedUserId,
      statusOptions, formatDateTime, setFilter, reload, resetFilters, onTableChange,
      confirmUnbind, confirmDelete, openBanModal, banVisible, banError, draft, setDraft, submitBan, closeBanModal,
      confirmUnban, loadRows,
      // 纯展示 helper 直出给模板（页面不再重复一份格式化逻辑）。
      shortDeviceId, deviceStatusText, deviceStatusColor, revokeReasonText,
      banExpiryText, canUnbindDevice, unbindBlockedHint, deviceBanAction,
      sumTableColumnWidths,
    }
  },
}
</script>

<style scoped>
.page-alert, .filter-form, .status-alert { margin-bottom: 16px; }
.device-summary { margin-bottom: 16px; }
.summary-label { color: var(--console-muted); font-size: 14px; }
.summary-value { margin-top: 8px; color: var(--console-text); font-size: clamp(20px, 2.6vw, 28px); font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.filter-keyword { width: 220px; }
.filter-status { width: 150px; }
.filter-error { margin: 0 0 16px; color: var(--console-danger, #c13e41); font-size: 13px; }
.field-hint { margin: 6px 0 0; color: var(--console-muted); font-size: 12px; }
.modal-alert { margin-bottom: 16px; }
.mono-cell, .time-cell, .number-cell { font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.revoke-reason { color: var(--console-muted); font-size: 12px; }
@media (max-width: 640px) {
  .filter-form :deep(.ant-form-item) { width: 100%; margin-inline-end: 0; }
  .filter-keyword, .filter-status { width: 100%; }
}
</style>
