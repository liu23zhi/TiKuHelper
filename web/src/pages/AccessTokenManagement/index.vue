<template>
  <div class="console-page token-page">
    <ConsolePageHeading title="令牌管理" description="为当前账号创建多个访问令牌，并按令牌维度维护状态、备注与累计额度上限。">
      <template #actions>
      <a-space wrap class="section-actions">
        <a-button
          v-if="isAdmin"
          :type="showAllTokens ? 'primary' : 'default'"
          @click="toggleAllTokens"
        >
          {{ showAllTokens ? '只看我的令牌' : '显示全部令牌' }}
        </a-button>
        <a-button @click="handleLoad" :loading="tableLoading">刷新</a-button>
        <a-button type="primary" @click="openCreateModal">新增令牌</a-button>
      </a-space>
      </template>
    </ConsolePageHeading>
    <ConsoleSummaryGrid>
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="令牌总数" :value="summary.total" />
        </a-card>
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="启用令牌" :value="summary.enabledCount" />
        </a-card>
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="已禁用令牌" :value="summary.disabledCount" />
        </a-card>
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="累计已消费" prefix="¥" :value="summary.totalAmountUsed" :precision="6" />
        </a-card>
        <!-- 可用设备数量（123 号 T-24）。
             ⚠️ 只显示**上限**，没有「已用设备数」—— 后者数据当前不存在：
             ① user_levels.device_limit 全仓业务消费点 0 处（entity/user_level.go:46
                自称「契约占位」）；
             ② account_device_bindings 表尚未建。
             所以不要补 0 / "已用 0 台" 之类的假数据（63 号 U-03 红线）。
             数据源是已存在的 GET /adapter-service/user/level。 -->
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="可用设备数量" :value="deviceLimitText" />
          <p class="device-limit-hint">{{ deviceLimitHint }}</p>
        </a-card>
    </ConsoleSummaryGrid>

    <a-card :bordered="false" class="page-card">
      <section v-if="deleteFailure" data-token-write-feedback="delete" class="page-alert">
        <RequestFailureAlert
          :key="deleteFailureContextKey"
          :failure="deleteFailure"
          :message="deleteFailureMessage"
          :busy="tableLoading"
          :context-key="deleteFailureContextKey"
          :is-current="isCurrentDeleteFailure"
        />
        <p class="hint">刷新只重新读取列表，不会重新提交删除，也不会据此确认本次删除成功。</p>
        <a-space wrap>
          <a-button html-type="button" data-action="reconcile-token-write" :loading="tableLoading" @click="reconcileDeletedToken">刷新令牌核对</a-button>
          <a-button html-type="button" data-action="dismiss-token-delete-failure" @click="clearDeleteFailure">关闭提示</a-button>
        </a-space>
      </section>
      <RequestFailureAlert
        v-if="listFailure"
        :key="listFailureContextKey"
        :failure="listFailure"
        message="获取访问令牌失败"
        retryable
        :busy="tableLoading"
        :context-key="listFailureContextKey"
        :is-current="isCurrentListFailure"
        @retry="retryTokenList"
      />
      <a-alert
        type="info"
        show-icon
        class="page-alert"
        message="令牌默认隐藏中间部分；如需核对完整值，可点击右侧按钮直接查看，也可直接复制。"
      />
      <!-- 访问令牌用于 /adapter-service/search 的 Authorization: Basic xxxx 认证。 -->

      <a-alert
        v-if="balanceAlert"
        :type="balanceAlert.type"
        show-icon
        class="page-alert"
        :message="balanceAlert.message"
        :description="balanceAlert.description"
      />

      <div class="table-preferences-actions">
        <TableColumnSettings
          :model-value="selectedColumnKeys"
          :options="columnOptions"
          :context-key="columnContextKey"
          :priority-keys="priorityColumnKeys"
          @update:model-value="setSelectedColumnKeys"
          @reset="resetColumns"
        />
      </div>
      <ConsoleTableShell label="访问令牌列表">
      <a-table
        bordered
        :data-source="dataSource"
        :columns="columns"
        :loading="tableLoading"
        :pagination="false"
        row-key="id"
        table-layout="fixed"
        :scroll="{ x: tableScrollX }"
      >
        <template #bodyCell="{ column, record }">
          <span v-if="column.key === 'username'">{{ record.username || '-' }}</span>
          <div v-else-if="column.key === 'tokenValue'" class="token-value-cell">
            <a-typography-paragraph
              class="token-value-text"
              :class="{ 'token-value-text--visible': isTokenVisible(record.id) }"
            >
              <code>{{ displayTokenValue(record) }}</code>
            </a-typography-paragraph>
            <a-button class="token-copy-button" shape="round" size="small" @click="copyTokenValue(record)">
              复制
            </a-button>
            <a-button class="token-toggle-button" shape="round" size="small" @click="toggleTokenVisibility(record.id)">
              {{ isTokenVisible(record.id) ? '隐藏中间部分' : '显示全部' }}
            </a-button>
          </div>
          <a-badge v-else-if="column.key === 'status'" :status="statusBadge(record.status)" :text="statusText(record.status)" />
          <span v-else-if="column.key === 'totalQuotaLimit'">{{ quotaText(record.totalQuotaLimit) }}</span>
          <span v-else-if="column.key === 'totalAmountUsed'">¥ {{ formatAmount(record.totalAmountUsed) }}</span>
          <span v-else-if="column.key === 'lastUsedAt'" class="token-time-cell">{{ formatDateTime(record.lastUsedAt) }}</span>
          <span v-else-if="column.key === 'lastUsedIp'">{{ record.lastUsedIp || '-' }}</span>
          <OverflowTableText
            v-else-if="column.key === 'remark'"
            :text="record.remark || '-'"
            :expanded="remarkExpansion.isExpanded(record, 'remark')"
            @toggle="remarkExpansion.toggle(record, 'remark')"
          />
          <a-space v-else-if="column.key === 'operation'" size="small" wrap>
            <a-button type="link" style="padding: 0" @click="openEditModal(record)">编辑</a-button>
            <a-popconfirm
              ok-text="确定"
              cancel-text="取消"
              title="确定删除该令牌吗？"
              @confirm="handleDelete(record.id)"
            >
              <a-button type="link" danger style="padding: 0">删除</a-button>
            </a-popconfirm>
          </a-space>
          <template v-else>{{ record[column.dataIndex] ?? '-' }}</template>
        </template>
        <template #emptyText>
          <a-empty v-if="!tableLoading && !listFailure && canLoadForCurrentUser()" description="当前还没有访问令牌，可点击右上角新增令牌。" />
          <p v-else class="hint" role="status">
            {{ !canLoadForCurrentUser() ? '访问令牌列表尚未确认，请登录后重试。' : tableLoading ? '正在读取访问令牌，请稍候。' : '暂时无法读取访问令牌，请使用上方重试。' }}
          </p>
        </template>
      </a-table>
      </ConsoleTableShell>
    </a-card>

    <a-modal
      v-model:open="visible"
      :wrap-props="{ 'aria-modal': true }"
      :title="modalTitle"
      @ok="handleSubmit"
      :afterClose="handleModalAfterClose"
      ok-text="确定"
      cancel-text="取消"
      :confirm-loading="submitLoading"
    >
      <section v-if="editorFailure" data-token-write-feedback="editor" class="page-alert">
        <RequestFailureAlert
          :key="editorFailureContextKey"
          :failure="editorFailure"
          :message="editorFailureMessage"
          :busy="tableLoading || submitLoading"
          :context-key="editorFailureContextKey"
          :is-current="isCurrentEditorFailure"
        />
        <p class="hint">刷新只重新读取列表，不会重新提交当前草稿，也不会据此确认本次写入成功；同名令牌不能证明本次创建成功。</p>
        <a-space wrap>
          <a-button html-type="button" data-action="reconcile-token-write" :loading="tableLoading" :disabled="submitLoading" @click="reconcileEditorToken">刷新令牌核对</a-button>
          <a-button html-type="button" data-action="dismiss-token-editor-failure" @click="clearEditorFailure">关闭提示</a-button>
        </a-space>
      </section>
      <a-form :model="formState" :label-col="labelCol" :wrapper-col="wrapperCol" layout="horizontal">
        <a-form-item label="令牌名称" :html-for="fieldId('name')" required :validate-status="nameError ? 'error' : undefined">
          <a-input ref="nameInput" :id="fieldId('name')" v-model:value="formState.name" :aria-invalid="nameError ? 'true' : undefined" :aria-describedby="nameError ? fieldId('name-error') : undefined" placeholder="请输入令牌名称" @input="formState.name = limitAccessTokenNameInput(formState.name)" />
          <template #help><span v-if="nameError" :id="fieldId('name-error')" role="alert">{{ nameError }}</span></template>
        </a-form-item>
        <a-form-item v-if="isEditing" label="状态" :html-for="fieldId('status')">
          <a-select :id="fieldId('status')" v-model:value="formState.status" :options="statusOptions" />
        </a-form-item>
        <a-form-item label="累计额度上限" :html-for="fieldId('quota')">
          <a-input-number
            :id="fieldId('quota')"
            v-model:value="formState.totalQuotaLimit"
            :min="0"
            :precision="6"
            style="width: 100%"
            placeholder="0 表示不单独限制"
          />
          <div class="hint">设置为 0 或留空时，表示不单独限制该令牌，消费直接受用户余额约束。金额最多支持 6 位小数。</div>
        </a-form-item>
        <a-form-item label="备注" :html-for="fieldId('remark')">
          <a-textarea :id="fieldId('remark')" v-model:value="formState.remark" :rows="4" placeholder="可选，例如调用来源、用途说明等" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script>
import { getCurrentInstance, computed, defineComponent, ref, shallowRef, useId, watch, nextTick } from 'vue';
import { message, TypographyParagraph } from 'ant-design-vue';
import Swal from 'sweetalert2';
import {
  getAccessTokens,
  createAccessToken,
  updateAccessToken,
  deleteAccessToken,
} from '@/api/TikuComponentApi';
import { useCurrentUserDisplay, isAdminUser, getCurrentUserLogoutEpoch } from '@/utils/currentUser';
import {
  buildAccessTokenBalanceAlert,
  buildAccessTokenListParams,
  normalizeAccessTokenName,
  limitAccessTokenNameInput,
} from '@/utils/accessTokenManagementHelpers.mjs';
import { useCurrentUserRequestScope } from '@/utils/currentUserScope'
import { buildDeviceLimitHint, buildDeviceLimitText } from '@/utils/userLevelHelpers.mjs'
import { getMyUserLevel } from '@/api/UserLevelApi'
import { formatMoneyValue, formatMoneyYuanWithSpace } from '@/utils/moneyFormat.mjs';
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs';
import OverflowTableText from '@/components/OverflowTableText.vue';
import TableColumnSettings from '@/components/TableColumnSettings.vue';
import RequestFailureAlert from '@/components/RequestFailureAlert.vue';
import { buildRequestFailureView, ensureRequestFailure } from '@/utils/requestFailureView.mjs';
import { useTableColumnPreferences } from '@/composables/useTableColumnPreferences.mjs';
import { useTableExpansion } from '@/composables/useTableExpansion.mjs';


function normalizeMoneyInput(value) {
  const amount = Number(value || 0)
  if (!Number.isFinite(amount) || amount < 0) {
    return 0
  }
  return amount
}

// CRUD currently returns these fixed messages without a business code. Only an
// exact HTTP/message pair is public copy; arbitrary upstream text stays private.
function tokenWriteFailureMessage(failure, operation) {
  const fallback = `${operation}令牌请求未完成`
  try {
    if (buildRequestFailureView(failure)?.kind === 'unknown-outcome') return `${operation}令牌结果尚未确认`
    const status = failure?.response?.status ?? failure?.status
    const detail = failure?.response?.data?.message ?? failure?.data?.message ?? failure?.message
    if (status === 400 && detail === '令牌名称不能为空') return '令牌名称不能为空，请填写后再提交。'
    if (status === 400 && detail === '令牌名称只能使用中文、英文、数字、下划线和@，且长度不能超过128个字符') return '令牌名称仅支持中文、英文、数字、下划线和@，且不能超过128个字符，请检查后再提交。'
    if (status === 404 && detail === '访问令牌不存在') return '访问令牌不存在，请刷新列表核对。'
    if (status === 403 && detail === '无权限操作访问令牌') return '当前账号无权操作该访问令牌，请确认账号权限。'
    if (status === 400 && detail === '参数错误') return '令牌参数不符合要求，请检查当前输入。'
  } catch { /* malformed replies still receive fixed, persistent feedback */ }
  return fallback
}

export default defineComponent({
  name: 'AccessTokenManagement',
  components: { OverflowTableText, TableColumnSettings, RequestFailureAlert, ATypographyParagraph: TypographyParagraph },
  setup() {
    const fieldIdPrefix = 'access-token-' + useId()
    const fieldId = field => fieldIdPrefix + '-' + field
    const nameInput = ref(null)
    const nameError = ref('')
    const clearNameError = () => { nameError.value = '' }
const dataSource = ref([])
const tableLoading = ref(true)
// Do not proxy/clone failures: transport diagnostics are keyed by the original object.
const listFailure = shallowRef(null)
const listFailureContextKey = ref(0)
const clearListFailure = () => { listFailure.value = null; listFailureContextKey.value += 1 }
const editorFailure = shallowRef(null)
const editorFailureContextKey = ref(0)
const editorFailureLogoutEpoch = ref(null)
const editorFailureMessage = ref('')
const clearEditorFailure = () => { editorFailure.value = null; editorFailureLogoutEpoch.value = null; editorFailureContextKey.value += 1 }
const deleteFailure = shallowRef(null)
const deleteFailureContextKey = ref(0)
const deleteFailureLogoutEpoch = ref(null)
const deleteFailureMessage = computed(() => tokenWriteFailureMessage(deleteFailure.value, '删除'))
let deleteFeedbackLease = {}
const clearDeleteFailure = () => { deleteFailure.value = null; deleteFailureLogoutEpoch.value = null; deleteFailureContextKey.value += 1; deleteFeedbackLease = {} }
const visible = ref(false)
const submitLoading = ref(false)
const isEditing = ref(false)
const visibleTokenIds = ref([])
const showAllTokens = ref(false)
const formState = ref({
  id: undefined,
  name: '',
  originalName: '',
  status: 'enabled',
  totalQuotaLimit: 0,
  remark: '',
})

    let viewLease = {}
    let editorLease = {}
    let draftLease = {}
    let listReadLease = {}
    let createdTokenDialog = null
    const retireCreatedTokenDialog = () => {
      const dialog = createdTokenDialog
      createdTokenDialog = null
      if (!dialog) return
      dialog.scrub()
      if (dialog.popup && Swal.getPopup() === dialog.popup) Swal.close()
    }
    const retireEditor = () => { editorLease = {}; submitLoading.value = false; clearEditorFailure(); clearNameError() }
    watch(formState, () => { draftLease = {}; clearEditorFailure() }, { deep: true, flush: 'sync' })
    watch(() => formState.value.name, () => {
      if (!nameError.value) return
      const rawName = String(formState.value.name || '').trim()
      if (isEditing.value && rawName === String(formState.value.originalName || '').trim()) {
        clearNameError()
        return
      }
      try {
        normalizeAccessTokenName(rawName)
        clearNameError()
      } catch (error) {
        nameError.value = error?.message || '令牌名称不符合要求'
      }
    }, { flush: 'sync' })
    watch(visible, open => { if (!open) retireEditor() }, { flush: 'sync' })
    watch(showAllTokens, () => {
      viewLease = {}
      clearListFailure()
      clearDeleteFailure()
      retireCreatedTokenDialog()
      dataSource.value = []; visibleTokenIds.value = []; tableLoading.value = false
      visible.value = false; retireEditor()
    }, { flush: 'sync' })
    const remarkExpansion = useTableExpansion(dataSource)
    const summary = computed(() => {
      const rows = Array.isArray(dataSource.value) ? dataSource.value : []
      return rows.reduce((acc, item) => {
        const status = String(item?.status || '').toLowerCase()
        const amountUsed = Number(item?.totalAmountUsed || 0)
        acc.total += 1
        if (status === 'disabled') {
          acc.disabledCount += 1
        } else {
          acc.enabledCount += 1
        }
        if (Number.isFinite(amountUsed)) {
          acc.totalAmountUsed += amountUsed
        }
        return acc
      }, {
        total: 0,
        enabledCount: 0,
        disabledCount: 0,
        totalAmountUsed: 0,
      })
    })

    const modalTitle = computed(() => isEditing.value ? '编辑令牌' : '新增令牌')
    // 等级摘要 → 可用设备数量上限（123 号 T-24）。
    //
    // 与 Profile 页共用同一套 helper 与同一个接口，不新造数据源。
    // null 表示"未读到"，渲染成 '—'，**不能**退化成「不限」：
    // 把一次读取失败说成"你没有任何设备限制"是错的语义。
    const userLevelSummary = shallowRef(null)
    const deviceLimitText = computed(() => buildDeviceLimitText(userLevelSummary.value?.deviceLimit))
    const deviceLimitHint = computed(() => buildDeviceLimitHint(userLevelSummary.value?.deviceLimit))
    const { currentUser } = useCurrentUserDisplay()
    let observedLogoutEpoch = getCurrentUserLogoutEpoch()
    const instance = getCurrentInstance()
    const { captureDataOwner, canLoadForCurrentUser } = useCurrentUserRequestScope(currentUser, {
      clear() {
        clearListFailure()
        clearDeleteFailure()
        dataSource.value = []; tableLoading.value = false; visibleTokenIds.value = []
        visible.value = false; showAllTokens.value = false; viewLease = {}; retireEditor(); retireCreatedTokenDialog()
        // 设备上限是账号私有数据，账号上下文变化必须清空回「—」。
        userLevelSummary.value = null
        formState.value = { id: undefined, name: '', originalName: '', status: 'enabled', totalQuotaLimit: 0, remark: '' }; isEditing.value = false
      },
      reset() {},
      reload() { instance.proxy.handleLoad() },
    })
    // Explicit logout retires old entries even if storage removal was refused.
    // New attempts after login capture their own epoch, without requiring remount.
    watch(() => [currentUser.value, getCurrentUserLogoutEpoch()], () => {
      const logout = getCurrentUserLogoutEpoch()
      if (logout !== observedLogoutEpoch) { clearEditorFailure(); clearDeleteFailure(); clearNameError() }
      observedLogoutEpoch = logout
    }, { flush: 'sync' })
    const isAdmin = computed(() => isAdminUser(currentUser.value))
    const isCurrentListFailure = (failure, context) => canLoadForCurrentUser()
      && Boolean(failure) && failure === listFailure.value && context === listFailureContextKey.value
    const isCurrentEditorFailure = (failure, context) => canLoadForCurrentUser() && getCurrentUserLogoutEpoch() === editorFailureLogoutEpoch.value && visible.value
      && Boolean(failure) && failure === editorFailure.value && context === editorFailureContextKey.value
    const isCurrentDeleteFailure = (failure, context) => canLoadForCurrentUser() && getCurrentUserLogoutEpoch() === deleteFailureLogoutEpoch.value
      && Boolean(failure) && failure === deleteFailure.value && context === deleteFailureContextKey.value
    const reconcileEditorToken = computed(() => {
      const failure = editorFailure.value, context = editorFailureContextKey.value
      const owns = captureDataOwner(), view = viewLease, editor = editorLease, draft = draftLease
      const logout = getCurrentUserLogoutEpoch()
      const isCurrent = () => owns() && logout === getCurrentUserLogoutEpoch() && view === viewLease && editor === editorLease && draft === draftLease && isCurrentEditorFailure(failure, context)
      return () => { if (isCurrent() && !tableLoading.value && !submitLoading.value) return instance.proxy.handleLoad(isCurrent) }
    })
    const reconcileDeletedToken = computed(() => {
      const failure = deleteFailure.value, context = deleteFailureContextKey.value
      const owns = captureDataOwner(), view = viewLease, lease = deleteFeedbackLease
      const logout = getCurrentUserLogoutEpoch()
      const isCurrent = () => owns() && logout === getCurrentUserLogoutEpoch() && view === viewLease && lease === deleteFeedbackLease && isCurrentDeleteFailure(failure, context)
      return () => { if (isCurrent() && !tableLoading.value) return instance.proxy.handleLoad(isCurrent) }
    })
    const retryTokenList = computed(() => {
      const failure = listFailure.value, context = listFailureContextKey.value
      const owns = captureDataOwner(), lease = viewLease
      return (entryFailure = failure, entryContext = context) => {
        if (owns() && lease === viewLease && entryFailure === failure && entryContext === context && isCurrentListFailure(entryFailure, entryContext)) instance.proxy.handleLoad()
      }
    })
    const balanceAlert = computed(() => buildAccessTokenBalanceAlert(currentUser.value))
    const baseColumns = [
      {
        title: '令牌名称',
        dataIndex: 'name',
        key: 'name',
        width: 180,
      },
      {
        title: '访问令牌',
        dataIndex: 'tokenValue',
        key: 'tokenValue',
        width: 360,
      },
      {
        title: '状态',
        dataIndex: 'status',
        key: 'status',
        width: 120,
      },
      {
        title: '累计额度上限',
        dataIndex: 'totalQuotaLimit',
        key: 'totalQuotaLimit',
        width: 150,
      },
      {
        title: '累计已消费',
        dataIndex: 'totalAmountUsed',
        key: 'totalAmountUsed',
        width: 140,
      },
      {
        title: '最近使用时间',
        dataIndex: 'lastUsedAt',
        key: 'lastUsedAt',
        width: 180,
      },
      {
        title: '最近使用 IP',
        dataIndex: 'lastUsedIp',
        key: 'lastUsedIp',
        width: 150,
      },
      {
        title: '备注',
        dataIndex: 'remark',
        key: 'remark',
        width: 200,
      },
      {
        title: '操作',
        dataIndex: 'operation',
        key: 'operation',
        fixed: 'right',
        width: 120,
      },
    ]
    const allColumns = computed(() => {
      if (!isAdmin.value || !showAllTokens.value) {
        return baseColumns.map(column => column.key === 'remark' ? { ...column, width: remarkExpansion.columnWidth('remark') } : column)
      }
      return [
        {
          title: '用户名',
          dataIndex: 'username',
          key: 'username',
          width: 160,
        },
        ...baseColumns.map(column => column.key === 'remark' ? { ...column, width: remarkExpansion.columnWidth('remark') } : column),
      ]
    })

    const columnScope = computed(() => {
      const identity = currentUser.value?.id ? 'id:' + currentUser.value.id : ''
      return identity ? `${identity}:${isAdmin.value ? 'admin' : 'user'}:${isAdmin.value && showAllTokens.value ? 'all' : 'mine'}` : ''
    })
    const priorityColumnKeys = ['username', 'name', 'status', 'totalQuotaLimit', 'totalAmountUsed', 'operation']
    const { visibleColumns: columns, selectedKeys, columnOptions, scopeKey: columnContextKey, setSelectedKeys, resetColumns } = useTableColumnPreferences({
      pageKey: 'access-token-management', columns: allColumns, scope: columnScope, requiredKeys: ['name', 'username', 'operation'], priorityKeys: priorityColumnKeys,
    })
    const tableScrollX = computed(() => sumTableColumnWidths(columns.value))

    return {
      fieldId, nameInput, nameError,
      captureDataOwner, canLoadForCurrentUser, retireEditor, retireCreatedTokenDialog,
      ownCreatedTokenDialog(dialog) { createdTokenDialog = dialog },
      isCreatedTokenDialogCurrent(dialog) { return createdTokenDialog === dialog },
      releaseCreatedTokenDialog(dialog) { if (createdTokenDialog === dialog) createdTokenDialog = null },
      captureViewOwner() {
        const owns = captureDataOwner(), lease = viewLease
        return () => owns() && lease === viewLease
      },
      captureListReadOwner() {
        const owns = captureDataOwner(), view = viewLease, lease = {}
        listReadLease = lease
        return () => owns() && view === viewLease && lease === listReadLease
      },
      captureEditorOwner() {
        const owns = captureDataOwner(), lease = editorLease
        return () => owns() && lease === editorLease
      },
      captureEditorDraftOwner() {
        const owns = captureDataOwner(), editor = editorLease, draft = draftLease
        const logout = getCurrentUserLogoutEpoch()
        return () => owns() && logout === getCurrentUserLogoutEpoch() && editor === editorLease && draft === draftLease
      },
      captureDeleteFeedbackOwner() {
        clearDeleteFailure()
        const owns = captureDataOwner(), view = viewLease, lease = deleteFeedbackLease
        const logout = getCurrentUserLogoutEpoch()
        return () => owns() && logout === getCurrentUserLogoutEpoch() && view === viewLease && lease === deleteFeedbackLease
      },
      labelCol: { style: { width: '110px' } },
      wrapperCol: { span: 16 },
      dataSource,
      tableLoading,
      listFailure,
      listFailureContextKey,
      clearListFailure,
      retryTokenList,
      isCurrentListFailure,
      editorFailure,
      editorFailureContextKey,
      editorFailureLogoutEpoch,
      editorFailureMessage,
      clearEditorFailure,
      isCurrentEditorFailure,
      reconcileEditorToken,
      deleteFailure,
      deleteFailureContextKey,
      deleteFailureLogoutEpoch,
      deleteFailureMessage,
      clearDeleteFailure,
      isCurrentDeleteFailure,
      reconcileDeletedToken,
      visible,
      submitLoading,
      isEditing,
      visibleTokenIds,
      showAllTokens,
      formState,
      summary,
      // 可用设备数量（T-24）：上限文案 + 说明文案，数据来自 GET /adapter-service/user/level。
      userLevelSummary,
      deviceLimitText,
      deviceLimitHint,
      modalTitle,
      isAdmin,
      balanceAlert,
      statusOptions: [
        { label: '启用', value: 'enabled' },
        { label: '禁用', value: 'disabled' },
      ],
      columns,
      selectedColumnKeys: selectedKeys,
      columnOptions,
      columnContextKey,
      priorityColumnKeys,
      setSelectedColumnKeys: setSelectedKeys,
      resetColumns,
      tableScrollX,
      remarkExpansion,
      limitAccessTokenNameInput,
    }
  },
  mounted() {
    this.handleLoad()
  },
  methods: {
    formatAmount(value) {
      return formatMoneyValue(value || 0)
    },
    statusText(value) {
      return String(value || '').toLowerCase() === 'disabled' ? '禁用' : '启用'
    },
    statusBadge(value) {
      return String(value || '').toLowerCase() === 'disabled' ? 'error' : 'success'
    },
    quotaText(value) {
      const amount = Number(value || 0)
      if (!Number.isFinite(amount) || amount <= 0) {
        return '未限制'
      }
      return formatMoneyYuanWithSpace(amount)
    },
    isTokenVisible(tokenId) {
      return this.visibleTokenIds.includes(tokenId)
    },
    toggleTokenVisibility(tokenId) {
      if (this.isTokenVisible(tokenId)) {
        this.visibleTokenIds = this.visibleTokenIds.filter(id => id !== tokenId)
        return
      }
      this.visibleTokenIds = [...this.visibleTokenIds, tokenId]
    },
    displayTokenValue(record) {
      const rawValue = String(record?.tokenValue || '').trim()
      if (!rawValue) {
        return '-'
      }
      if (this.isTokenVisible(record?.id)) {
        return rawValue
      }
      if (rawValue.length <= 14) {
        return rawValue
      }
      return `${rawValue.slice(0, 8)}******${rawValue.slice(-6)}`
    },
    copyTokenValue(record) {
      if (!this.canLoadForCurrentUser()) return
      const owns = this.captureViewOwner()
      const rawValue = String(record?.tokenValue || '').trim()
      if (!rawValue) {
        message.warning('当前令牌为空，无法复制')
        return
      }
      return this.copyTokenText(rawValue, owns).then((copied) => {
        if (!owns()) return
        if (copied) {
          message.success('访问令牌已复制')
          return
        }
        message.error('复制访问令牌失败，请手动复制')
      })
    },
    async copyTokenText(rawValue, owns = () => true) {
      const value = String(rawValue || '').trim()
      if (!value || !owns()) {
        return false
      }
      if (navigator?.clipboard?.writeText) {
        try {
          await navigator.clipboard.writeText(value)
          return owns()
        } catch (error) {
          // Fall through to the legacy copy path for non-secure or restricted contexts.
        }
      }

      if (!owns()) return false
      const textarea = document.createElement('textarea')
      textarea.value = value
      textarea.setAttribute('readonly', '')
      textarea.style.position = 'fixed'
      textarea.style.top = '-9999px'
      textarea.style.opacity = '0'
      document.body.appendChild(textarea)
      textarea.select()
      textarea.setSelectionRange(0, value.length)

      try {
        return document.execCommand('copy')
      } catch (error) {
        return false
      } finally {
        document.body.removeChild(textarea)
      }
    },
    showCreatedTokenDialog(plainToken) {
      if (!this.canLoadForCurrentUser()) return
      this.retireCreatedTokenDialog()
      const ownsView = this.captureViewOwner()
      const dialog = {
        popup: null,
        scrub() {
          plainToken = ''
          dialog.popup?.querySelector('[data-token-value]')?.replaceChildren()
        },
      }
      this.ownCreatedTokenDialog(dialog)
      const owns = () => ownsView() && this.isCreatedTokenDialogCurrent(dialog) && Swal.getPopup() === dialog.popup
      const result = Swal.fire({
        icon: 'success',
        title: '令牌创建成功',
        html: `
          <div class="token-created-dialog">
            <div class="token-created-dialog__notice">
              <span class="token-created-dialog__notice-mark">!</span>
              <span>请妥善保存访问令牌，避免泄露。</span>
            </div>
            <div class="token-created-dialog__field">
              <div class="token-created-dialog__label">访问令牌</div>
              <code class="token-created-dialog__value" data-token-value></code>
            </div>
          </div>
        `,
        showDenyButton: true,
        confirmButtonText: '我已保存',
        denyButtonText: '复制令牌',
        reverseButtons: true,
        buttonsStyling: false,
        allowOutsideClick: false,
        focusConfirm: false,
        returnFocus: false,
        heightAuto: false,
        scrollbarPadding: false,
        customClass: {
          popup: 'token-created-popup',
          title: 'token-created-popup__title',
          htmlContainer: 'token-created-popup__content',
          actions: 'token-created-popup__actions',
          confirmButton: 'token-created-popup__confirm',
          denyButton: 'token-created-popup__copy',
        },
        didOpen: popup => {
          dialog.popup = popup
          if (!owns()) {
            dialog.scrub()
            if (Swal.getPopup() === popup) Swal.close()
            return
          }
          const tokenElement = popup.querySelector('[data-token-value]')
          if (tokenElement) {
            tokenElement.textContent = plainToken
          }
        },
        willClose: () => {
          dialog.scrub()
          this.releaseCreatedTokenDialog(dialog)
        },
        didDestroy: () => {
          dialog.scrub()
          this.releaseCreatedTokenDialog(dialog)
        },
        preDeny: async () => {
          if (!owns()) return false
          const copied = await this.copyTokenText(plainToken, owns)
          if (!owns()) return false
          const denyButton = dialog.popup.querySelector('.swal2-deny')
          if (copied) {
            if (denyButton) {
              denyButton.textContent = '已复制'
            }
            Swal.showValidationMessage('已复制到剪贴板，请妥善保存')
          } else {
            if (denyButton) {
              denyButton.textContent = '重新复制'
            }
            Swal.showValidationMessage('复制失败，请选中令牌后手动复制')
          }
          return false
        },
      })
      dialog.popup = Swal.getPopup()
      return result
    },
    formatDateTime(value) {
      if (!value) {
        return '-'
      }
      const date = new Date(value)
      if (Number.isNaN(date.getTime())) {
        return value
      }
      return date.toLocaleString()
    },
    handleLoad(isReconciliationCurrent) {
      if (!this.canLoadForCurrentUser()) return
      const ownsView = this.captureViewOwner()
      const ownsRead = this.captureListReadOwner()
      const owns = () => ownsView() && (typeof isReconciliationCurrent !== 'function' || (ownsRead() && isReconciliationCurrent()))
      this.clearListFailure()
      this.tableLoading = true
      return getAccessTokens(buildAccessTokenListParams(this.showAllTokens)).then(res => {
        if (!owns()) return
        if (res?.status === 200) {
          this.clearListFailure()
          this.dataSource = Array.isArray(res?.data) ? res.data : []
          // 令牌列表读到之后顺带读一次等级上限（T-24）。
          // 单独一条请求、单独吞错：设备上限读失败绝不能影响令牌列表本身。
          void this.loadDeviceLimit(owns)
        } else {
          this.clearListFailure()
          this.listFailure = ensureRequestFailure(res)
        }
      }).catch(error => {
        if (!owns()) return
        this.clearListFailure()
        this.listFailure = ensureRequestFailure(error)
      }).finally(() => {
        if (!ownsRead()) return
        this.tableLoading = false
      })
    },
    // loadDeviceLimit 读取当前用户的等级摘要，只取 deviceLimit 用于展示。
    //
    // 独立的 owner（owns 由调用方传入）：它与令牌列表读取共享生命周期，
    // 但失败互不影响。任何失败都保持 null → 界面显示「—」，绝不退化成「不限」。
    loadDeviceLimit(owns) {
      const current = () => (typeof owns === 'function' ? owns() : this.canLoadForCurrentUser())
      if (!current()) return Promise.resolve()
      return getMyUserLevel().then(res => {
        if (!current()) return
        const summary = res?.status === 200 ? res?.data?.data : null
        if (!summary || typeof summary !== 'object' || Array.isArray(summary)) return
        this.userLevelSummary = summary
      }).catch(() => { /* 设备上限读不到就显示「—」，不影响令牌管理其它功能。 */ })
    },
    toggleAllTokens() {
      if (!this.isAdmin) {
        return
      }
      this.showAllTokens = !this.showAllTokens
      this.handleLoad()
    },
    openCreateModal() {
      if (!this.canLoadForCurrentUser()) return
      this.resetFormState()
      this.isEditing = false
      this.visible = true
    },
    openEditModal(record) {
      if (!this.canLoadForCurrentUser()) return
      this.retireEditor()
      this.isEditing = true
      this.formState = {
        id: record.id,
        name: record.name || '',
        originalName: record.name || '',
        status: record.status || 'enabled',
        totalQuotaLimit: Number(record.totalQuotaLimit || 0),
        remark: record.remark || '',
      }
      this.visible = true
    },
    handleSubmit() {
      if (!this.canLoadForCurrentUser() || !this.visible || this.submitLoading) return
      const owns = this.captureEditorOwner()
      const ownsDraft = this.captureEditorDraftOwner()
      const editing = this.isEditing
      const submittedDraft = JSON.stringify(this.formState)
      let name
      const rawName = String(this.formState.name || '').trim()
      const unchangedLegacyName = this.isEditing && rawName === String(this.formState.originalName || '').trim()
      if (unchangedLegacyName) {
        name = rawName
      } else {
        try {
          name = normalizeAccessTokenName(rawName)
        } catch (error) {
          const validationMessage = error?.message || '令牌名称不符合要求'
          const control = this.nameInput
          this.nameError = validationMessage
          nextTick(() => {
            if (ownsDraft() && this.visible && this.nameError === validationMessage && this.nameInput === control) {
              control?.focus?.({ preventScroll: true })
            }
          })
          message.warning(error?.message || '令牌名称不符合要求')
          return
        }
      }
      this.nameError = ''
      const payload = {
        id: this.formState.id,
        name,
        status: String(this.formState.status || 'enabled').trim(),
        totalQuotaLimit: normalizeMoneyInput(this.formState.totalQuotaLimit),
        remark: String(this.formState.remark || '').trim(),
      }

      this.clearEditorFailure()
      this.submitLoading = true
      const submitter = editing
        ? updateAccessToken(payload)
        : createAccessToken({
          name: payload.name,
          totalQuotaLimit: payload.totalQuotaLimit,
          remark: payload.remark,
        })

      return submitter.then(res => {
        if (!owns()) return
        if (res?.status === 200) {
          if (editing) {
            message.success('更新令牌成功')
          } else {
            message.success('创建令牌成功')
            const plainToken = String(res?.data?.token || '').trim()
            if (plainToken) {
              this.showCreatedTokenDialog(plainToken)
            }
          }
          this.handleLoad()
          if (JSON.stringify(this.formState) === submittedDraft) this.visible = false
        } else {
          if (!ownsDraft()) return
          this.editorFailureMessage = tokenWriteFailureMessage(res, editing ? '更新' : '创建')
          this.editorFailureLogoutEpoch = getCurrentUserLogoutEpoch()
          this.editorFailure = ensureRequestFailure(res)
        }
      }).catch(error => {
        if (!owns() || !ownsDraft()) return
        this.editorFailureMessage = tokenWriteFailureMessage(error, editing ? '更新' : '创建')
        this.editorFailureLogoutEpoch = getCurrentUserLogoutEpoch()
        this.editorFailure = ensureRequestFailure(error)
      }).finally(() => {
        if (!owns()) return
        this.submitLoading = false
      })
    },
    handleDelete(id) {
      if (!this.canLoadForCurrentUser()) return
      const owns = this.captureViewOwner()
      const ownsFeedback = this.captureDeleteFeedbackOwner()
      return deleteAccessToken({ id }).then(res => {
        if (!owns()) return
        if (res?.status === 200) {
          message.success('删除令牌成功')
          this.handleLoad()
        } else {
          if (ownsFeedback()) {
            this.deleteFailureLogoutEpoch = getCurrentUserLogoutEpoch()
            this.deleteFailure = ensureRequestFailure(res)
          }
        }
      }).catch(error => {
        if (!owns() || !ownsFeedback()) return
        this.deleteFailureLogoutEpoch = getCurrentUserLogoutEpoch()
        this.deleteFailure = ensureRequestFailure(error)
      })
    },
    handleModalAfterClose() {
      if (!this.visible) this.resetFormState()
    },
    resetFormState() {
      this.retireEditor()
      this.formState = {
        id: undefined,
        name: '',
        originalName: '',
        status: 'enabled',
        totalQuotaLimit: 0,
        remark: '',
      }
      this.isEditing = false
    },
  },
})
</script>

<style scoped>
.table-preferences-actions {
  display: flex;
  justify-content: flex-end;
  margin-bottom: 12px;
}

.token-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}

.page-card {
  border: 1px solid var(--console-border, #dce5e1);
  border-radius: var(--console-radius, 12px);
}

.page-alert {
  margin-bottom: 16px;
}

/* 设备上限说明：说明里必须能看到「已用设备数暂不可用」，否则用户会以为漏显示。 */
.device-limit-hint {
  margin: 6px 0 0;
  color: var(--console-muted, #62766e);
  font-size: 12px;
  line-height: 1.6;
  overflow-wrap: anywhere;
}

.token-page :deep(.ant-table) {
  font-size: 14px;
}

.hint {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--console-muted, #62766e);
}

.token-value-cell {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: nowrap;
  min-width: 0;
}

.token-value-text {
  margin-bottom: 0;
  word-break: break-all;
  flex: 1 1 auto;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.token-value-text--visible {
  white-space: normal;
  overflow: visible;
  text-overflow: clip;
}

.token-copy-button,
.token-toggle-button {
  flex: 0 0 auto;
}

.token-time-cell {
  display: block;
  white-space: pre-wrap;
  font-variant-numeric: tabular-nums;
}

.token-page :deep(.ant-table-cell) {
  text-align: center;
  vertical-align: middle;
  overflow-wrap: anywhere;
  white-space: normal;
}
</style>

<style>
.token-created-popup {
  width: min(34rem, calc(100vw - 2rem));
  padding: 28px 28px 24px;
  border-radius: 12px;
  border: 1px solid var(--console-border, #dce5e1);
  box-shadow: 0 12px 36px rgb(23 43 39 / 12%);
}

.token-created-popup__title {
  color: var(--console-text, #172b27);
  font-size: 23px;
  font-weight: 700;
  letter-spacing: -0.02em;
}

.token-created-popup__content {
  margin-top: 16px;
}

.token-created-dialog {
  color: var(--console-text, #172b27);
  text-align: left;
}

.token-created-dialog__notice {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin-bottom: 16px;
  padding: 12px 14px;
  border: 1px solid #fde68a;
  border-radius: 8px;
  background: #fffbeb;
  color: var(--warning-fg);
  font-size: 13px;
  line-height: 1.6;
}

.token-created-dialog__notice-mark {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  margin-top: 1px;
  border-radius: 50%;
  background: #f59e0b;
  color: var(--surface);
  font-size: 12px;
  font-weight: 800;
}

.token-created-dialog__field {
  padding: 14px;
  border: 1px solid var(--console-border, #dce5e1);
  border-radius: 8px;
  background: var(--console-surface-secondary, #f8faf9);
}

.token-created-dialog__label {
  margin-bottom: 8px;
  color: var(--console-muted, #62766e);
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.token-created-dialog__value {
  display: block;
  padding: 14px 16px;
  border: 1px solid var(--console-border, #dce5e1);
  border-radius: 6px;
  background: var(--console-surface, #fff);
  color: var(--console-text, #172b27);
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
  font-size: 14px;
  line-height: 1.7;
  text-align: left;
  word-break: break-all;
  user-select: all;
}

.token-created-popup__actions {
  width: 100%;
  gap: 10px;
  margin-top: 20px;
}

.token-created-popup__copy,
.token-created-popup__confirm {
  min-width: 126px;
  height: 42px;
  padding: 0 20px;
  border-radius: 8px;
  font-size: 14px;
  font-weight: 600;
  transition: border-color 0.15s ease, background 0.15s ease;
}

.token-created-popup__copy {
  border: 1px solid var(--console-primary, #0f766e);
  background: var(--console-primary, #0f766e);
  color: #ffffff;
}

.token-created-popup__copy:hover {
  border-color: var(--console-primary-hover, #115e59);
  background: var(--console-primary-hover, #115e59);
}

.token-created-popup__confirm {
  border: 1px solid var(--console-border, #dce5e1);
  background: #ffffff;
  color: var(--console-muted, #62766e);
}

.token-created-popup__confirm:hover {
  border-color: var(--console-primary, #0f766e);
  background: var(--console-surface-secondary, #f8faf9);
  color: var(--console-text, #172b27);
}

.token-created-popup__copy:focus-visible,
.token-created-popup__confirm:focus-visible {
  outline: 3px solid rgb(15 118 110 / 20%);
  outline-offset: 2px;
}

@media (max-width: 575px) {
  .token-created-popup {
    width: calc(100vw - 24px);
    padding: 24px 18px 18px;
    border-radius: 12px;
  }

  .token-created-popup__actions {
    flex-wrap: nowrap;
  }

  .token-created-popup__copy,
  .token-created-popup__confirm {
    flex: 1 1 0;
    min-width: 0;
    padding: 0 12px;
  }
}
</style>
