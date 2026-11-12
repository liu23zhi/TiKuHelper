<template>
  <div class="console-page referral-rebates-page">
    <ConsolePageHeading title="赠送记录" description="四类赠送来源的聚合账本：注册赠送、评价返现、邀请返佣、充值赠金。邀请返佣可手动补录（创建即入账，进赠送余额）。">
      <template #actions>
        <a-button :loading="loading" :disabled="!canManage() || loading || settling" @click="load">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert
      v-if="!canManage()"
      type="warning"
      show-icon
      class="page-alert"
      message="当前身份无赠送记录管理权限，请使用管理员账号登录。"
    />

    <template v-if="canManage()">
      <ConsoleSummaryGrid class="rebate-summary">
        <div>
          <div class="summary-label">筛选结果</div>
          <div class="summary-value">{{ hasLoaded ? total : '--' }} 条</div>
        </div>
        <div>
          <div class="summary-label">待结算合计</div>
          <div class="summary-value">{{ moneyText(pendingAmount) }}</div>
        </div>
        <div>
          <div class="summary-label">已结算合计</div>
          <div class="summary-value">{{ moneyText(settledAmount) }}</div>
        </div>
      </ConsoleSummaryGrid>

      <ConsolePanel title="筛选" description="状态精确匹配；受益人 / 受邀人是账户 ID；订单号精确匹配，不做模糊搜索。">
        <a-form layout="inline" class="filter-form" :onFinish="reload">
          <a-form-item label="状态" :html-for="fieldId('status')">
            <a-select
              :id="fieldId('status')"
              :value="filters.status"
              :options="statusOptions"
              allow-clear
              placeholder="全部状态"
              class="filter-status"
              :disabled="loading || settling"
              @update:value="value => setFilter('status', value)"
              @change="reload"
            />
          </a-form-item>
          <a-form-item label="受益人 ID" :html-for="fieldId('inviterUserId')">
            <a-input
              :id="fieldId('inviterUserId')"
              :value="filters.inviterUserId"
              inputmode="numeric"
              allow-clear
              placeholder="精确账户 ID"
              class="filter-id"
              :disabled="loading || settling"
              @update:value="value => setFilter('inviterUserId', value)"
              @press-enter="reload"
            />
          </a-form-item>
          <a-form-item label="订单号" :html-for="fieldId('orderNo')">
            <a-input
              :id="fieldId('orderNo')"
              :value="filters.orderNo"
              allow-clear
              placeholder="原充值订单号"
              class="filter-order"
              :disabled="loading || settling"
              @update:value="value => setFilter('orderNo', value)"
              @press-enter="reload"
            />
          </a-form-item>
          <a-form-item>
            <a-space wrap>
              <a-button type="primary" html-type="submit" :loading="loading" :disabled="loading || settling">查询</a-button>
              <a-button :disabled="loading || settling" @click="resetFilters">重置筛选</a-button>
            </a-space>
          </a-form-item>
        </a-form>

        <p v-if="filterError" class="filter-error" role="alert">{{ filterError }}</p>
        <RequestFailureAlert
          v-if="loadFailure"
          :key="failureContextKey"
          :failure="loadFailure"
          :message="failureMessage"
          :retryable="true"
          :busy="loading || settling"
          :context-key="failureContextKey"
          :is-current="isCurrentFailure"
          @retry="load"
        />
        <a-alert v-else-if="actionWarning" type="warning" show-icon :message="actionWarning" class="status-alert" />
      </ConsolePanel>

      <!-- B1：管理员手动补录一笔返佣（无需充值订单）。创建即入账，直接进赠送余额。 -->
      <ConsolePanel title="手动补录返佣" description="给指定受益人补一笔返佣。创建即入账（进赠送余额、欠款先抵），同一受益人 + 同一金额 + 同一日期只会记一笔。">
        <a-form layout="inline" class="manual-form" :onFinish="submitManual">
          <a-form-item label="受益人 ID" :html-for="fieldId('manualUser')">
            <a-input
              :id="fieldId('manualUser')"
              :value="manualForm.beneficiaryUserId"
              inputmode="numeric"
              placeholder="精确账户 ID"
              class="filter-id"
              :disabled="manualSubmitting"
              @update:value="value => setManualField('beneficiaryUserId', value)"
            />
          </a-form-item>
          <a-form-item label="金额" :html-for="fieldId('manualAmount')">
            <a-input
              :id="fieldId('manualAmount')"
              :value="manualForm.amount"
              inputmode="decimal"
              placeholder="例如 12.5"
              class="filter-amount"
              :disabled="manualSubmitting"
              @update:value="value => setManualField('amount', value)"
            />
          </a-form-item>
          <a-form-item label="归属日期" :html-for="fieldId('manualDay')">
            <a-input
              :id="fieldId('manualDay')"
              :value="manualForm.day"
              placeholder="YYYY-MM-DD，留空=今天"
              class="filter-order"
              :disabled="manualSubmitting"
              @update:value="value => setManualField('day', value)"
            />
          </a-form-item>
          <a-form-item label="附言" :html-for="fieldId('manualRemark')">
            <a-input
              :id="fieldId('manualRemark')"
              :value="manualForm.remark"
              placeholder="选填，写进记录与账单"
              class="filter-order"
              :disabled="manualSubmitting"
              @update:value="value => setManualField('remark', value)"
            />
          </a-form-item>
          <a-form-item>
            <a-button type="primary" html-type="submit" :loading="manualSubmitting" :disabled="manualSubmitting">补录</a-button>
          </a-form-item>
        </a-form>

        <p v-if="manualError" class="filter-error" role="alert">{{ manualError }}</p>
        <a-alert v-if="manualSuccess" type="success" show-icon :message="manualSuccess" class="status-alert" />
      </ConsolePanel>

      <!-- B2：赠送记录 —— 四类来源聚合（注册赠送 / 评价返现 / 邀请返佣 / 充值赠金）。 -->
      <ConsolePanel title="赠送记录" :description="giftScopeDescription">
        <a-form layout="inline" class="filter-form" :onFinish="reloadGiftRecords">
          <a-form-item label="来源" :html-for="fieldId('giftSource')">
            <a-select
              :id="fieldId('giftSource')"
              :value="giftFilters.source"
              :options="giftSourceOptions"
              allow-clear
              placeholder="全部来源"
              class="filter-status"
              :disabled="giftLoading"
              @update:value="value => setGiftFilter('source', value)"
              @change="reloadGiftRecords"
            />
          </a-form-item>
          <a-form-item label="受益人 ID" :html-for="fieldId('giftUser')">
            <a-input
              :id="fieldId('giftUser')"
              :value="giftFilters.userId"
              inputmode="numeric"
              allow-clear
              placeholder="精确账户 ID"
              class="filter-id"
              :disabled="giftLoading"
              @update:value="value => setGiftFilter('userId', value)"
              @press-enter="reloadGiftRecords"
            />
          </a-form-item>
          <a-form-item label="入账起始" :html-for="fieldId('giftFrom')">
            <a-input
              :id="fieldId('giftFrom')"
              :value="giftFilters.createdFrom"
              placeholder="YYYY-MM-DD"
              class="filter-order"
              :disabled="giftLoading"
              @update:value="value => setGiftFilter('createdFrom', value)"
              @press-enter="reloadGiftRecords"
            />
          </a-form-item>
          <a-form-item label="入账结束" :html-for="fieldId('giftTo')">
            <a-input
              :id="fieldId('giftTo')"
              :value="giftFilters.createdTo"
              placeholder="YYYY-MM-DD"
              class="filter-order"
              :disabled="giftLoading"
              @update:value="value => setGiftFilter('createdTo', value)"
              @press-enter="reloadGiftRecords"
            />
          </a-form-item>
          <a-form-item>
            <a-space wrap>
              <a-button type="primary" html-type="submit" :loading="giftLoading" :disabled="giftLoading">查询</a-button>
              <a-button :disabled="giftLoading" @click="resetGiftFilters">重置筛选</a-button>
            </a-space>
          </a-form-item>
        </a-form>

        <ConsoleSummaryGrid class="gift-summary">
          <div v-for="item in giftSummaryBySource" :key="item.source">
            <div class="summary-label">{{ item.label }}</div>
            <div class="summary-value">{{ moneyText(item.amount) }}</div>
          </div>
          <div>
            <div class="summary-label">合计</div>
            <div class="summary-value">{{ moneyText(giftSummaryTotal) }}</div>
          </div>
        </ConsoleSummaryGrid>

        <p v-if="giftFilterError" class="filter-error" role="alert">{{ giftFilterError }}</p>
        <RequestFailureAlert
          v-if="giftLoadFailure"
          :key="giftFailureContextKey"
          :failure="giftLoadFailure"
          :message="giftFailureMessage"
          :retryable="true"
          :busy="giftLoading"
          :context-key="giftFailureContextKey"
          :is-current="isCurrentGiftFailure"
          @retry="loadGiftRecords"
        />

        <ConsoleTableShell label="赠送记录">
          <a-table
            table-layout="fixed"
            :data-source="giftRows"
            :columns="giftColumns"
            :loading="giftLoading"
            :pagination="giftPagination"
            row-key="id"
            :scroll="{ x: sumTableColumnWidths(giftColumns) }"
            :locale="{ emptyText: giftEmptyText }"
            @change="onGiftTableChange"
          >
            <template #bodyCell="{ column, record }">
              <span v-if="column.key === 'id'">{{ record.id ?? '-' }}</span>
              <span v-else-if="column.key === 'username'">{{ buildUserName(record.username, record.userId) }}</span>
              <a-tag v-else-if="column.key === 'source'" color="blue">{{ record.sourceLabel || record.source }}</a-tag>
              <span v-else-if="column.key === 'amount'" class="money-cell">{{ moneyText(record.amount) }}</span>
              <span v-else-if="column.key === 'debtOffset'">{{ record.debtOffset ? moneyText(record.debtOffset) : '—' }}</span>
              <span v-else-if="column.key === 'giftBalance'">{{ formatGiftBalance(record) }}</span>
              <span v-else-if="column.key === 'remark'" class="remark-cell">{{ record.remark || '—' }}</span>
              <span v-else-if="column.key === 'createdAt'" class="time-cell">{{ formatDateTime(record.createdAt) }}</span>
            </template>
          </a-table>
        </ConsoleTableShell>
      </ConsolePanel>

      <ConsolePanel title="返佣明细" :description="scopeDescription">
          <ConsoleTableShell label="返佣明细">
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
              <span v-if="column.key === 'id'">{{ record.id ?? '-' }}</span>
              <span v-else-if="column.key === 'inviter'">{{ buildUserName(record.inviterUsername, record.inviterUserId) }}</span>
              <span v-else-if="column.key === 'invited'">{{ buildUserName(record.invitedUsername, record.invitedUserId) }}</span>
              <span v-else-if="column.key === 'orderNo'" class="order-cell">{{ record.orderNo || '—' }}</span>
              <span v-else-if="column.key === 'baseAmount'" class="money-cell">{{ moneyText(record.baseAmount) }}</span>
              <span v-else-if="column.key === 'rate'">{{ formatRate(record.rate) }}</span>
              <span v-else-if="column.key === 'amount'" class="money-cell">{{ moneyText(record.amount) }}</span>
              <a-tag v-else-if="column.key === 'status'" :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
              <span v-else-if="column.key === 'createdAt'" class="time-cell">{{ formatDateTime(record.createdAt) }}</span>
              <span v-else-if="column.key === 'settledAt'" class="time-cell">{{ formatDateTime(record.settledAt) }}</span>
              <template v-else-if="column.key === 'action'">
                <a-button
                  v-if="isPending(record)"
                  type="link"
                  :disabled="loading || settling || !!loadFailure"
                  @click="confirmSettle(record)"
                >结算</a-button>
                <span v-else class="action-disabled">{{ record.status === 'settled' ? '已结算' : '不可结算' }}</span>
              </template>
            </template>
          </a-table>
        </ConsoleTableShell>
      </ConsolePanel>
    </template>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, reactive, ref, shallowRef, useId, watch } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import Swal from 'sweetalert2'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import ConsoleTableShell from '@/components/ConsoleTableShell.vue'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import { createManualReferralRebate, getGiftRecordSources, getGiftRecords, getReferralRebates, settleReferralRebate } from '@/api/ReferralApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

const STATUS_LABELS = { pending: '待结算', settled: '已结算', reversed: '已冲回' }
const STATUS_COLORS = { pending: 'gold', settled: 'green', reversed: 'default' }

function readNonnegativeNumber(value) {
  if (!['number', 'string'].includes(typeof value) || String(value).trim() === '') return null
  const number = Number(value)
  return Number.isFinite(number) && number >= 0 ? number : null
}
function readPositiveInt(value) {
  const number = readNonnegativeNumber(value)
  return Number.isInteger(number) && number > 0 ? number : null
}

const emptyFilters = () => ({ status: undefined, inviterUserId: '', orderNo: '' })

export default {
  name: 'ReferralRebatesPage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell, RequestFailureAlert },
  setup() {
    const route = useRoute()
    const prefix = 'referral-rebates-' + useId()
    const fieldId = field => prefix + '-' + field
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const rows = ref([])
    const filters = reactive(emptyFilters())
    const appliedFilters = ref({})
    const loading = ref(false)
    const settling = ref(false)
    const hasLoaded = ref(false)
    const total = ref(0)
    const amounts = reactive({ pending: null, settled: null })
    const filterError = ref('')
    const actionWarning = ref('')
    const loadFailure = shallowRef(null)
    const failureContextKey = ref(0)
    let failureOwner = () => false
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
    // 与其它管理页一致：身份、角色、登出任一变化都不再允许操作。
    const canManage = () => alive && route.path === mountedPath
      && getCurrentUserLogoutEpoch() === mountedLogoutEpoch && validAdmin()

    const pendingAmount = computed(() => amounts.pending)
    const settledAmount = computed(() => amounts.settled)

    const columns = [
      { title: 'ID', dataIndex: 'id', key: 'id', width: 90 },
      { title: '受益人', dataIndex: 'inviter', key: 'inviter', width: 180 },
      { title: '受邀人', dataIndex: 'invited', key: 'invited', width: 180 },
      { title: '订单号', dataIndex: 'orderNo', key: 'orderNo', width: 230 },
      { title: '基数', dataIndex: 'baseAmount', key: 'baseAmount', width: 140 },
      { title: '比例', dataIndex: 'rate', key: 'rate', width: 100 },
      { title: '金额', dataIndex: 'amount', key: 'amount', width: 140 },
      { title: '状态', dataIndex: 'status', key: 'status', width: 110 },
      { title: '创建时间', dataIndex: 'createdAt', key: 'createdAt', width: 175 },
      { title: '结算时间', dataIndex: 'settledAt', key: 'settledAt', width: 175 },
      { title: '操作', key: 'action', width: 110 },
    ]

    const statusOptions = [
      { label: '待结算', value: 'pending' },
      { label: '已结算', value: 'settled' },
      { label: '已冲回', value: 'reversed' },
    ]

    const scopeDescription = computed(() => {
      const scope = appliedFilters.value
      const parts = [
        scope.status && `状态：${statusLabel(scope.status)}`,
        scope.inviterUserId && `受益人 ID：${scope.inviterUserId}`,
        scope.orderNo && `订单号：${scope.orderNo}`,
      ].filter(Boolean)
      return parts.length ? `当前筛选：${parts.join('；')}。` : '当前范围：全部返佣明细。'
    })

    const emptyText = computed(() => (loadFailure.value
      ? '列表加载失败，请重试'
      : hasLoaded.value ? '暂无符合条件的返佣明细' : '返佣明细尚未读取成功'))

    function statusLabel(value) {
      const normalized = String(value || '').trim().toLowerCase()
      return STATUS_LABELS[normalized] || value || '-'
    }
    function statusColor(value) {
      const normalized = String(value || '').trim().toLowerCase()
      return STATUS_COLORS[normalized] || 'blue'
    }
    function isPending(record) {
      return String(record?.status || '').trim().toLowerCase() === 'pending'
    }

    // ---- B1：手动补录返佣 ----
    const manualForm = reactive({ beneficiaryUserId: '', amount: '', day: '', remark: '' })
    const manualSubmitting = ref(false)
    const manualError = ref('')
    const manualSuccess = ref('')

    function setManualField(key, value) {
      manualForm[key] = value
      // 用户一改动就清掉上一次的结果提示，避免「明明改了参数却还显示上一笔成功」。
      manualError.value = ''
      manualSuccess.value = ''
    }

    // submitManual 提交一笔补录。
    //
    // 幂等由后端兜底（同人同额同日返回 409），前端**不做本地去重**：
    // 本地记住「我提交过」在刷新/换设备后就失效，唯一可靠的锚点在服务端账单行。
    const submitManual = async () => {
      if (!canManage() || manualSubmitting.value) return
      manualError.value = ''
      manualSuccess.value = ''
      const userId = readPositiveInt(manualForm.beneficiaryUserId)
      if (!userId) { manualError.value = '受益人 ID 必须是大于 0 的整数'; return }
      const amount = Number(manualForm.amount)
      if (!Number.isFinite(amount) || amount <= 0) { manualError.value = '金额必须大于 0'; return }
      // 金额按 6 位小数口径收敛（与后端 util.RoundMoney 一致），避免浮点尾巴。
      const normalizedAmount = Math.round(amount * 1e6) / 1e6
      if (normalizedAmount <= 0) { manualError.value = '金额必须大于 0'; return }
      const day = String(manualForm.day || '').trim()
      if (day && !/^\d{4}-\d{2}-\d{2}$/.test(day)) { manualError.value = '归属日期必须是 YYYY-MM-DD'; return }

      manualSubmitting.value = true
      try {
        const res = await createManualReferralRebate({
          beneficiaryUserId: userId, amount: normalizedAmount, day, remark: manualForm.remark,
        })
        if (res?.status !== 200) {
          // 409 = 幂等命中，要明确告诉管理员「这笔已经记过」，不能笼统报失败。
          if (res?.status === 409) {
            manualError.value = res?.data?.message || '该受益人在该日期已有同金额的返佣记录'
          } else {
            manualError.value = res?.data?.message || '补录失败，请重试'
          }
          return
        }
        const created = res?.data
        manualSuccess.value = `已补录 ${moneyText(created?.amount)} 给账户 #${userId}（已入赠送余额）`
        // 补录会改变钱包与账单，两个列表都要重读，否则界面还是旧数据。
        manualForm.amount = ''
        manualForm.remark = ''
        await Promise.all([load(), loadGiftRecords()])
      } catch (error) {
        manualError.value = (error && error.message) || '补录失败，请重试'
      } finally {
        manualSubmitting.value = false
      }
    }

    // ---- B2：赠送记录（四类来源聚合）----
    const emptyGiftFilters = () => ({ source: undefined, userId: '', createdFrom: '', createdTo: '' })
    const giftRows = ref([])
    const giftFilters = reactive(emptyGiftFilters())
    const giftLoading = ref(false)
    const giftHasLoaded = ref(false)
    const giftSummaryTotal = ref(null)
    // giftSummaryBySource 保序：后端已按固定四类顺序返回（含补 0 的项），
    // 前端不再排序或过滤，避免「筛选后某一类消失」让图例抖动。
    const giftSummaryBySource = ref([])
    const giftFilterError = ref('')
    const giftLoadFailure = shallowRef(null)
    const giftFailureContextKey = ref(0)
    let giftFailureOwner = () => false
    let giftReadVersion = 0
    const giftPagination = reactive({
      current: 1, pageSize: 20, total: 0,
      showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'],
      showTotal: value => `共 ${value} 条`,
    })

    // 来源下拉：由后端 /sources 提供，前端不自己写死文案。
    const giftSourceOptions = ref([])

    const giftColumns = [
      { title: 'ID', dataIndex: 'id', key: 'id', width: 90 },
      { title: '受益人', dataIndex: 'username', key: 'username', width: 200 },
      { title: '来源', dataIndex: 'source', key: 'source', width: 130 },
      { title: '金额', dataIndex: 'amount', key: 'amount', width: 140 },
      { title: '抵欠款', dataIndex: 'debtOffset', key: 'debtOffset', width: 130 },
      { title: '赠送余额', dataIndex: 'giftBalance', key: 'giftBalance', width: 190 },
      { title: '备注', dataIndex: 'remark', key: 'remark', width: 260 },
      { title: '入账时间', dataIndex: 'createdAt', key: 'createdAt', width: 175 },
    ]

    const giftScopeDescription = computed(() => {
      const scope = giftAppliedFilters.value
      const parts = [
        scope.source && `来源：${sourceLabel(scope.source)}`,
        scope.userId && `受益人 ID：${scope.userId}`,
        scope.createdFrom && `起始：${scope.createdFrom}`,
        scope.createdTo && `结束：${scope.createdTo}`,
      ].filter(Boolean)
      return parts.length ? `当前筛选：${parts.join('；')}。` : '当前范围：全部赠送记录（四类来源合计）。'
    })

    const giftEmptyText = computed(() => (giftLoadFailure.value
      ? '赠送记录加载失败，请重试'
      : giftHasLoaded.value ? '暂无符合条件的赠送记录' : '赠送记录尚未读取成功'))

    function sourceLabel(value) {
      const normalized = String(value || '').trim()
      const match = giftSourceOptions.value.find(option => option.value === normalized)
      return match ? match.label : (normalized || '-')
    }

    // formatGiftBalance 显示赠金桶前后值。
    //
    // 前后值可能是 null（历史账单没有快照）—— 那时显示「—」而不是 0：把「不知道」
    // 显示成 0 会让管理员以为这笔赠送没进赠金桶。
    function formatGiftBalance(record) {
      const before = record?.giftBalanceBefore
      const after = record?.giftBalanceAfter
      if (typeof before !== 'number' || typeof after !== 'number') return '—'
      return `${moneyText(before)} → ${moneyText(after)}`
    }

    const giftAppliedFilters = ref({})

    function setGiftFilter(key, value) {
      giftFilters[key] = value
    }

    function buildGiftParams() {
      const params = { page: giftPagination.current, pageSize: giftPagination.pageSize }
      const source = String(giftAppliedFilters.value.source || '').trim()
      if (source) params.source = source
      const userId = readPositiveInt(giftAppliedFilters.value.userId)
      if (userId) params.userId = userId
      const from = String(giftAppliedFilters.value.createdFrom || '').trim()
      if (from) params.createdFrom = from
      const to = String(giftAppliedFilters.value.createdTo || '').trim()
      if (to) params.createdTo = to
      return params
    }

    function clearGiftLoadFailure() {
      if (giftFailureOwner()) { giftLoadFailure.value = null; giftFailureContextKey.value += 1 }
      giftFailureOwner = () => false
    }

    const loadGiftRecords = async () => {
      if (!canManage()) return false
      const ticket = ++giftReadVersion
      const owns = () => canManage() && ticket === giftReadVersion
      clearGiftLoadFailure()
      const context = giftFailureContextKey.value
      const ownsFailure = () => owns() && context === giftFailureContextKey.value
      giftLoading.value = true
      try {
        const res = await getGiftRecords(buildGiftParams())
        if (!ownsFailure()) return false
        const body = res?.data
        const items = body?.data
        const count = readNonnegativeNumber(body?.total)
        if (res?.status !== 200 || !Array.isArray(items) || count === null || !Number.isInteger(count)) {
          giftFailureOwner = ownsFailure
          giftLoadFailure.value = ensureRequestFailure(res)
          return false
        }
        giftRows.value = items.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
        giftPagination.total = count
        giftSummaryTotal.value = readNonnegativeNumber(body?.summary?.totalAmount)
        const bySource = body?.summary?.bySource
        giftSummaryBySource.value = Array.isArray(bySource)
          ? bySource.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
          : []
        const page = readPositiveInt(body?.page)
        const size = readPositiveInt(body?.pageSize)
        if (page) giftPagination.current = page
        if (size) giftPagination.pageSize = size
        giftHasLoaded.value = true
        giftFilterError.value = ''
        return true
      } catch (error) {
        if (ownsFailure()) { giftFailureOwner = ownsFailure; giftLoadFailure.value = ensureRequestFailure(error) }
        return false
      } finally {
        if (owns()) giftLoading.value = false
      }
    }

    const reloadGiftRecords = () => {
      if (!canManage() || giftLoading.value) return
      const userId = String(giftFilters.userId || '').trim()
      if (userId && !/^[0-9]+$/.test(userId)) { giftFilterError.value = '受益人 ID 只能是正整数'; return }
      if (userId && !readPositiveInt(userId)) { giftFilterError.value = '受益人 ID 必须大于 0'; return }
      const from = String(giftFilters.createdFrom || '').trim()
      const to = String(giftFilters.createdTo || '').trim()
      if (from && !/^\d{4}-\d{2}-\d{2}$/.test(from)) { giftFilterError.value = '起始日期必须是 YYYY-MM-DD'; return }
      if (to && !/^\d{4}-\d{2}-\d{2}$/.test(to)) { giftFilterError.value = '结束日期必须是 YYYY-MM-DD'; return }
      giftFilterError.value = ''
      giftAppliedFilters.value = { source: giftFilters.source || '', userId, createdFrom: from, createdTo: to }
      giftPagination.current = 1
      return loadGiftRecords()
    }

    const resetGiftFilters = () => {
      Object.assign(giftFilters, emptyGiftFilters())
      giftAppliedFilters.value = {}
      giftFilterError.value = ''
      giftPagination.current = 1
      return loadGiftRecords()
    }

    const onGiftTableChange = (pager) => {
      const current = readPositiveInt(pager?.current)
      const size = readPositiveInt(pager?.pageSize)
      if (current) giftPagination.current = current
      if (size) {
        giftPagination.pageSize = size
        giftPagination.current = 1
      }
      return loadGiftRecords()
    }

    const isCurrentGiftFailure = () => Boolean(giftLoadFailure.value) && canManage()

    // giftFailureMessage 是失败提示文案：与返佣明细同一套口径，便于用户理解
    // 「是整页挂了还是只有赠送记录挂了」。
    const giftFailureMessage = computed(() => (giftLoadFailure.value
      ? '赠送记录读取失败'
      : '赠送记录读取失败，请重试'))

    // 来源下拉：一次读取，失败不阻塞页面（下拉为空时用原始标识兜底显示）。
    const loadGiftSourceOptions = async () => {
      try {
        const res = await getGiftRecordSources()
        const items = res?.data?.data
        if (res?.status !== 200 || !Array.isArray(items)) return
        giftSourceOptions.value = items
          .filter(item => item && typeof item.source === 'string' && item.source)
          .map(item => ({ label: item.label || item.source, value: item.source }))
      } catch {
        // 下拉拿不到只影响筛选便利性，不影响列表本身。
      }
    }

    function buildUserName(username, userId) {
      const name = String(username || '').trim()
      const id = readPositiveInt(userId)
      if (name && id) return `${name}（#${id}）`
      if (name) return name
      if (id) return `#${id}`
      return '—'
    }
    // 与 UserUsage 一致：金额最多 6 位小数、去尾零（后端 util.MoneyPrecision = 6）。
    // 未知金额显示 `--`，不拼成「¥ --」这种半成品。
    function moneyText(value) {
      const amount = readNonnegativeNumber(value)
      return amount === null ? '--' : `¥ ${formatMoneyValue(amount)}`
    }
    // 返佣比例是 0–1 分数，展示成百分数并去尾零。
    function formatRate(value) {
      const rate = readNonnegativeNumber(value)
      if (rate === null) return '--'
      const percent = Number((rate * 100).toFixed(4))
      return `${String(percent).replace(/(\.\d*?)0+$/, '$1').replace(/\.$/, '')}%`
    }
    function formatDateTime(value) {
      if (!value) return '—'
      const date = new Date(value)
      if (!Number.isFinite(date.getTime())) return '—'
      return date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
    }
    function setFilter(field, value) {
      if (!canManage() || loading.value || settling.value || !Object.hasOwn(filters, field)) return
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
      const status = String(appliedFilters.value.status || '').trim()
      if (status) params.status = status
      const inviter = String(appliedFilters.value.inviterUserId || '').trim()
      if (inviter) params.inviterUserId = inviter
      const orderNo = String(appliedFilters.value.orderNo || '').trim()
      if (orderNo) params.orderNo = orderNo
      return params
    }

    const load = async () => {
      if (!canManage() || settling.value) return false
      const ticket = ++readVersion
      const owns = () => canManage() && ticket === readVersion
      clearLoadFailure()
      const context = failureContextKey.value
      const ownsFailure = () => owns() && context === failureContextKey.value
      loading.value = true
      try {
        const res = await getReferralRebates(buildParams())
        if (!ownsFailure()) return false
        const body = res?.data
        const items = body?.data
        const count = readNonnegativeNumber(body?.total)
        if (res?.status !== 200 || !Array.isArray(items) || count === null || !Number.isInteger(count)) {
          failureOwner = ownsFailure
          loadFailure.value = ensureRequestFailure(res)
          return false
        }
        rows.value = items.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
        total.value = count
        pagination.total = count
        amounts.pending = readNonnegativeNumber(body?.summary?.pendingAmount)
        amounts.settled = readNonnegativeNumber(body?.summary?.settledAmount)
        const page = readPositiveInt(body?.page)
        const size = readPositiveInt(body?.pageSize)
        if (page) pagination.current = page
        if (size) pagination.pageSize = size
        hasLoaded.value = true
        filterError.value = ''
        return true
      } catch (error) {
        if (ownsFailure()) { failureOwner = ownsFailure; loadFailure.value = ensureRequestFailure(error) }
        return false
      } finally {
        if (owns()) loading.value = false
      }
    }

    const reload = () => {
      if (!canManage() || loading.value || settling.value) return
      const inviter = String(filters.inviterUserId || '').trim()
      if (inviter && !/^[0-9]+$/.test(inviter)) { filterError.value = '受益人 ID 只能是正整数'; return }
      if (inviter && !readPositiveInt(inviter)) { filterError.value = '受益人 ID 必须大于 0'; return }
      filterError.value = ''
      actionWarning.value = ''
      appliedFilters.value = {
        status: filters.status || '',
        inviterUserId: inviter,
        orderNo: String(filters.orderNo || '').trim(),
      }
      pagination.current = 1
      return load()
    }

    const resetFilters = () => {
      if (!canManage() || loading.value || settling.value) return
      Object.assign(filters, emptyFilters())
      appliedFilters.value = {}
      filterError.value = ''
      pagination.current = 1
      return load()
    }

    const onTableChange = page => {
      if (!canManage() || loading.value || settling.value) return
      pagination.current = page?.pageSize === pagination.pageSize ? Number(page.current) || 1 : 1
      pagination.pageSize = Number(page?.pageSize) || pagination.pageSize
      return load()
    }

    const confirmSettle = async record => {
      if (!canManage() || loading.value || settling.value || !isPending(record)) return
      const owns = () => canManage()
      const result = await Swal.fire({
        title: '确认结算这条返佣？',
        text: `受益人 ${buildUserName(record.inviterUsername, record.inviterUserId)}，金额 ${moneyText(record.amount)}。结算会把该金额计入受益人的赠送余额（可消费、不可退款），可退本金不变，且不可撤销。`,
        icon: 'warning',
        showCancelButton: true,
        confirmButtonText: '确认结算',
        cancelButtonText: '取消',
      })
      if (!result.isConfirmed || !owns()) return
      return settle(record)
    }

    const settle = async record => {
      const id = readPositiveInt(record?.id)
      if (!id || !canManage()) return
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      clearLoadFailure()
      const context = failureContextKey.value
      const ownsFailure = () => owns() && context === failureContextKey.value
      settling.value = true
      actionWarning.value = ''
      let reloadAfter = false
      try {
        const res = await settleReferralRebate(id)
        if (!ownsFailure()) return
        if (res?.status === 409) {
          actionWarning.value = '该记录状态已变更，请刷新核对后再操作。'
          return
        }
        if (res?.status !== 200) {
          failureOwner = ownsFailure
          loadFailure.value = ensureRequestFailure(res)
          return
        }
        message.success('返佣已结算')
        reloadAfter = true
      } catch (error) {
        if (ownsFailure()) { failureOwner = ownsFailure; loadFailure.value = ensureRequestFailure(error) }
      } finally {
        if (owns()) settling.value = false
      }
      // load() 会拒绝在 settling 期间执行，因此必须等锁释放后再刷新。
      if (reloadAfter && owns()) await load()
    }

    const failureMessage = computed(() => (hasLoaded.value
      ? '返佣列表刷新失败，页面显示的仍是上次读取的结果。'
      : '返佣列表尚未加载成功，请重试读取。'))

    const resetDraft = () => {
      Object.assign(filters, emptyFilters())
      appliedFilters.value = {}
      filterError.value = ''
    }

    const retire = () => {
      readVersion += 1; writeVersion += 1
      pagination.current = 1
    }
    watch(() => [currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status], () => {
      retire()
      rows.value = []; total.value = 0; pagination.total = 0; hasLoaded.value = false
      amounts.pending = null; amounts.settled = null; actionWarning.value = ''
      resetDraft()
      if (mounted && canManage()) void load()
    }, { flush: 'sync' })
    watch(() => getCurrentUserLogoutEpoch(), value => {
      if (value === observedLogout) return
      observedLogout = value
      retire()
      rows.value = []; total.value = 0; pagination.total = 0; hasLoaded.value = false
      amounts.pending = null; amounts.settled = null; clearLoadFailure()
    }, { flush: 'sync' })
    watch(() => route.path, () => {
      retire()
      rows.value = []; total.value = 0; pagination.total = 0; hasLoaded.value = false
      amounts.pending = null; amounts.settled = null; actionWarning.value = ''
      clearLoadFailure()
    }, { flush: 'sync' })
    onMounted(() => {
      mounted = true
      if (canManage()) {
        void load()
        // 赠送记录是独立数据集（四类来源聚合），与返佣明细分开读：
        // 混在一个请求里会让任一接口失败时两个列表一起空白。
        void loadGiftRecords()
        void loadGiftSourceOptions()
      }
    })
    onBeforeUnmount(() => {
      alive = false; mounted = false
      readVersion += 1; writeVersion += 1
      giftReadVersion += 1
      clearLoadFailure()
      clearGiftLoadFailure()
    })

    return {
      fieldId, canManage, columns, rows, filters, appliedFilters, loading, settling,
      hasLoaded, total, pendingAmount, settledAmount, filterError, actionWarning,
      loadFailure, failureContextKey, failureMessage, isCurrentFailure,
      pagination, statusOptions, scopeDescription, emptyText,
      statusLabel, statusColor, isPending, buildUserName, moneyText, formatRate,
      formatDateTime, setFilter, load, reload, resetFilters, onTableChange, confirmSettle,
      sumTableColumnWidths,
      // B1：手动补录
      manualForm, manualSubmitting, manualError, manualSuccess, setManualField, submitManual,
      // B2：赠送记录（四类聚合）
      giftRows, giftFilters, giftLoading, giftHasLoaded, giftSummaryBySource, giftSummaryTotal,
      giftFilterError, giftLoadFailure, giftFailureContextKey, giftFailureMessage,
      giftPagination, giftSourceOptions, giftColumns, giftScopeDescription, giftEmptyText,
      formatGiftBalance, setGiftFilter, loadGiftRecords, reloadGiftRecords, resetGiftFilters,
      onGiftTableChange, isCurrentGiftFailure,
    }
  },
}
</script>

<style scoped>
.page-alert, .filter-form, .status-alert { margin-bottom: 16px; }
.rebate-summary { margin-bottom: 16px; }
.summary-label { color: var(--console-muted); font-size: 14px; }
.summary-value { margin-top: 8px; color: var(--console-text); font-size: clamp(20px, 2.6vw, 28px); font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.filter-status { width: 150px; }
.filter-id, .filter-order { width: 200px; }
.filter-error { margin: 0 0 16px; color: var(--console-danger, #c13e41); font-size: 13px; }
.money-cell, .time-cell { font-variant-numeric: tabular-nums; }
.order-cell { overflow-wrap: anywhere; }
.action-disabled { color: var(--console-muted); font-size: 13px; }
@media (max-width: 640px) {
  .filter-form :deep(.ant-form-item) { width: 100%; margin-inline-end: 0; }
  .filter-status, .filter-id, .filter-order { width: 100%; }
}
</style>
