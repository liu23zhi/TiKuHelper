<template>
  <div class="console-page ticket-image-cache-page">
    <ConsolePageHeading
      title="工单图片缓存管理"
      description="工单图片按 sha256(明文) 去重存储：同一张图上传 100 次也只有一份字节，多个工单图片共享同一份。这里可以查看占用、按检测状态核对，并清理不再被引用的缓存。"
    >
      <template #actions>
        <a-space wrap>
          <a-button :loading="loading" :disabled="!canManage() || busy()" @click="load">刷新列表</a-button>
          <a-button
            :loading="cleaning"
            :disabled="!canManage() || busy()"
            @click="runCleanNow"
          >立即清理一次</a-button>
        </a-space>
      </template>
    </ConsolePageHeading>

    <a-alert
      v-if="!canManage()"
      type="warning"
      show-icon
      class="page-alert"
      message="当前身份无工单图片缓存管理权限，请使用管理员账号登录。"
    />

    <template v-if="canManage()">
      <!-- 四个必须显示的数字：① 总占用 ② 待清理数 ③ scan_state 分组 ④ 保留策略 -->
      <ConsoleSummaryGrid class="cache-summary">
        <div>
          <div class="summary-label">总占用</div>
          <div class="summary-value">{{ hasLoaded ? summary.totalBytesText : '--' }}</div>
          <div class="summary-hint">
            <template v-if="hasLoaded">
              {{ summary.totalCount }} 份图片
              <template v-if="summary.usagePercent !== null"> · 占上限 {{ summary.usagePercent }}%</template>
            </template>
            <template v-else>共几份图片 / 多少字节</template>
          </div>
        </div>
        <div>
          <div class="summary-label">待清理（ref_count = 0）</div>
          <div class="summary-value">{{ hasLoaded ? summary.zeroRefCount : '--' }}</div>
          <div class="summary-hint">不再被任何工单图片引用，超过缓冲期后由延迟删除回收字节</div>
        </div>
        <div>
          <div class="summary-label">按检测状态</div>
          <div v-if="hasLoaded" class="scan-lines">
            <div v-for="row in summary.scanRows" :key="row.key" class="scan-line">
              <span class="scan-line-label">{{ row.label }}</span>
              <span class="scan-line-value">{{ row.count }}</span>
            </div>
          </div>
          <div v-else class="summary-value summary-value--pending">--</div>
        </div>
        <div>
          <div class="summary-label">当前生效的保留策略</div>
          <div v-if="hasLoaded" class="scan-lines">
            <div class="scan-line">
              <span class="scan-line-label">按时间保留</span>
              <span class="scan-line-value">{{ summary.retention.ageText }}</span>
            </div>
            <div class="scan-line">
              <span class="scan-line-label">总量上限</span>
              <span class="scan-line-value">{{ summary.retention.sizeText }}</span>
            </div>
            <div class="scan-line">
              <span class="scan-line-label">后台清理间隔</span>
              <span class="scan-line-value">{{ summary.retention.intervalText }}</span>
            </div>
          </div>
          <div v-else class="summary-value summary-value--pending">--</div>
        </div>
      </ConsoleSummaryGrid>

      <a-alert
        v-if="cleanResult"
        :type="cleanResult.available === false ? 'info' : 'success'"
        show-icon
        class="status-alert"
        :message="cleanResultText"
      />

      <ConsolePanel title="筛选" description="按检测状态核对缓存；关键字只匹配 sha256 前缀（缓存的内容是密文，没有可全文搜索的文本）。">
        <a-form layout="inline" class="filter-form" :onFinish="reload">
          <a-form-item label="媒体类型" :html-for="fieldId('media-type')">
            <a-select
              :id="fieldId('media-type')"
              :value="filters.mediaType"
              :options="mediaTypeOptions"
              allow-clear
              placeholder="全部类型"
              class="filter-narrow"
              :disabled="busy()"
              @update:value="value => setFilter('mediaType', value)"
              @change="reload"
            />
          </a-form-item>
          <a-form-item label="检测状态" :html-for="fieldId('scan-state')">
            <a-select
              :id="fieldId('scan-state')"
              :value="filters.scanState"
              :options="scanStateOptions"
              allow-clear
              placeholder="全部状态"
              class="filter-narrow"
              :disabled="busy()"
              @update:value="value => setFilter('scanState', value)"
              @change="reload"
            />
          </a-form-item>
          <a-form-item label="引用情况" :html-for="fieldId('ref-filter')">
            <a-select
              :id="fieldId('ref-filter')"
              :value="filters.refFilter"
              :options="refFilterOptions"
              class="filter-narrow filter-wide"
              :disabled="busy()"
              @update:value="value => setFilter('refFilter', value)"
              @change="reload"
            />
          </a-form-item>
          <a-form-item label="sha256 前缀" :html-for="fieldId('keyword')">
            <a-input
              :id="fieldId('keyword')"
              :value="filters.keyword"
              allow-clear
              placeholder="例如 a1b2c3"
              class="filter-keyword"
              :disabled="busy()"
              @update:value="value => setFilter('keyword', value)"
              @press-enter="reload"
            />
          </a-form-item>
          <a-form-item>
            <a-space wrap>
              <a-button type="primary" html-type="submit" :loading="loading" :disabled="busy()">查询</a-button>
              <a-button :disabled="busy()" @click="resetFilters">重置筛选</a-button>
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
          :busy="busy()"
          :context-key="failureContextKey"
          :is-current="isCurrentFailure"
          @retry="load"
        />
        <a-alert v-else-if="actionWarning" type="warning" show-icon :message="actionWarning" class="status-alert" />
      </ConsolePanel>

      <ConsolePanel title="缓存记录" :description="scopeDescription">
        <template #actions>
          <a-space wrap>
            <span class="selection-hint">已选 {{ selectedShas.length }} 条</span>
            <a-button
              danger
              :disabled="busy() || selectedShas.length === 0"
              @click="confirmBatchDelete"
            >删除所选</a-button>
            <a-button :disabled="busy() || selectedShas.length === 0" @click="clearSelection">取消选择</a-button>
          </a-space>
        </template>
        <ConsoleTableShell label="工单图片缓存记录">
          <a-table
            table-layout="fixed"
            :data-source="rows"
            :columns="columns"
            :loading="loading"
            :pagination="pagination"
            :row-selection="{ selectedRowKeys: selectedShas, onChange: onSelectionChange }"
            row-key="sha256"
            :scroll="{ x: sumTableColumnWidths(columns) }"
            :locale="{ emptyText: emptyText }"
            @change="onTableChange"
          >
            <template #bodyCell="{ column, record }">
              <span v-if="column.key === 'sha256'" class="mono-cell sha-cell">{{ shortSha(record.sha256) }}</span>
              <span v-else-if="column.key === 'byteSize'" class="mono-cell">{{ formatCacheBytes(record.byteSize) }}</span>
              <span v-else-if="column.key === 'mediaType'">{{ record.mediaType || '—' }}</span>
              <!-- ② 的逐行形态：refCount 是「被几个工单图片引用」 -->
              <a-tag v-else-if="column.key === 'refCount'" :color="record.refCount > 0 ? 'blue' : 'default'">
                {{ record.refCount > 0 ? `被 ${record.refCount} 处引用` : '待清理' }}
              </a-tag>
              <a-tag v-else-if="column.key === 'scanState'" :color="scanStateColor(record.scanState)">
                {{ scanStateText(record.scanState) }}
              </a-tag>
              <span v-else-if="column.key === 'nsfwScore'" class="mono-cell">{{ formatScore(record.nsfwScore) }}</span>
              <span v-else-if="column.key === 'createdAt'" class="time-cell">{{ formatDateTime(record.createdAt) }}</span>
              <span v-else-if="column.key === 'updatedAt'" class="time-cell">{{ formatDateTime(record.updatedAt) }}</span>
              <template v-else-if="column.key === 'action'">
                <!-- 判据 ①：ref_count > 0 时按钮就是禁用态（真正的保护在后端 409） -->
                <a-tooltip :title="canDeleteBlob(record) ? '删除这份缓存字节' : deleteBlockedHint(record)">
                  <a-button
                    type="link"
                    size="small"
                    danger
                    :disabled="busy() || !canDeleteBlob(record)"
                    @click="confirmDelete(record)"
                  >删除</a-button>
                </a-tooltip>
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
import {
  batchDeleteTicketImageCache,
  cleanTicketImageCacheNow,
  deleteTicketImageCache,
  getTicketImageCacheList,
} from '@/api/TicketImageCacheApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'
import {
  MEDIA_TYPE_OPTIONS,
  REF_FILTER_OPTIONS,
  SCAN_STATE_ROWS,
  buildListParams,
  canDeleteBlob,
  describeCleanResult,
  formatCacheBytes,
  isValidSha256,
  readCacheCount,
  readSummary,
  scanStateText,
} from '@/utils/ticketImageCacheHelpers.mjs'

const emptyFilters = () => ({ mediaType: undefined, scanState: undefined, refFilter: '', keyword: '' })

// 检测状态的标签颜色。fail-closed 语义：只有 approved 是绿色（会显示图片），
// 其余一律偏「不可用」的色系，避免管理员把「检测中」误读成「正常」。
const SCAN_STATE_COLORS = Object.freeze({
  approved: 'green',
  pending: 'default',
  scanning: 'processing',
  sensitive: 'red',
  failed: 'orange',
})

export default {
  name: 'TicketImageCachePage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell, RequestFailureAlert },
  setup() {
    const route = useRoute()
    const prefix = 'ticket-image-cache-' + useId()
    const fieldId = field => prefix + '-' + field
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const rows = ref([])
    const filters = reactive(emptyFilters())
    const appliedFilters = ref({})
    const loading = ref(false)
    const mutating = ref(false)
    const cleaning = ref(false)
    const hasLoaded = ref(false)
    const filterError = ref('')
    const actionWarning = ref('')
    const loadFailure = shallowRef(null)
    const failureContextKey = ref(0)
    const cleanResult = shallowRef(null)
    const selectedShas = ref([])
    let failureOwner = () => false

    // 四项汇总：全部是**整个筛选范围**的值，不随翻页变化。
    const summary = reactive({
      totalCount: 0, totalBytes: 0, totalBytesText: '0 B', zeroRefCount: 0,
      scanRows: [], retention: { ageText: '—', sizeText: '—', intervalText: '—', limitBytes: null },
      usagePercent: null,
    })

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
    const busy = () => loading.value || mutating.value || cleaning.value

    const mediaTypeOptions = MEDIA_TYPE_OPTIONS
    const refFilterOptions = REF_FILTER_OPTIONS
    const scanStateOptions = SCAN_STATE_ROWS
      .filter(row => row.key !== 'other')
      .map(row => ({ label: row.label, value: row.key }))

    const columns = [
      { title: 'sha256', dataIndex: 'sha256', key: 'sha256', width: 200 },
      { title: '大小', dataIndex: 'byteSize', key: 'byteSize', width: 110 },
      { title: '类型', dataIndex: 'mediaType', key: 'mediaType', width: 110 },
      { title: '引用', dataIndex: 'refCount', key: 'refCount', width: 130 },
      { title: '检测状态', dataIndex: 'scanState', key: 'scanState', width: 110 },
      { title: '敏感分', dataIndex: 'nsfwScore', key: 'nsfwScore', width: 90 },
      { title: '入库时间', dataIndex: 'createdAt', key: 'createdAt', width: 175 },
      { title: '更新时间', dataIndex: 'updatedAt', key: 'updatedAt', width: 175 },
      { title: '操作', key: 'action', width: 90 },
    ]

    const scopeDescription = computed(() => {
      const scope = appliedFilters.value
      const parts = [
        scope.mediaType && `类型：${scope.mediaType}`,
        scope.scanState && `检测状态：${scanStateText(scope.scanState)}`,
        scope.refFilter === 'zero' && '仅待清理',
        scope.refFilter === 'referenced' && '仅被引用',
        scope.keyword && `sha256 前缀：${scope.keyword}`,
      ].filter(Boolean)
      return parts.length ? `当前筛选：${parts.join('；')}。` : '当前范围：全部工单图片缓存。'
    })

    const emptyText = computed(() => (loadFailure.value
      ? '列表加载失败，请重试'
      : hasLoaded.value ? '暂无符合条件的缓存记录' : '缓存列表尚未读取成功'))

    const failureMessage = computed(() => (hasLoaded.value
      ? '缓存列表刷新失败，页面显示的仍是上次读取的结果。'
      : '缓存列表尚未加载成功，请重试读取。'))

    const cleanResultText = computed(() => describeCleanResult(cleanResult.value))

    function shortSha(sha) {
      const text = String(sha ?? '')
      return text.length > 16 ? `${text.slice(0, 10)}…${text.slice(-6)}` : (text || '—')
    }
    function scanStateColor(state) {
      return SCAN_STATE_COLORS[String(state ?? '').trim()] || 'default'
    }
    function formatScore(value) {
      const score = Number(value)
      return Number.isFinite(score) ? score.toFixed(4) : '—'
    }
    function formatDateTime(value) {
      if (!value) return '—'
      const date = new Date(value)
      if (!Number.isFinite(date.getTime())) return '—'
      return date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
    }
    // 「为什么不能删」要给出具体数量，不能只说「被引用」——判据 ① 要求说明几张。
    function deleteBlockedHint(record) {
      const refCount = readCacheCount(record?.refCount) ?? 0
      return refCount > 0
        ? `仍被 ${refCount} 个工单图片引用，需先删除对应工单图片或等待其过期`
        : '暂不可删除'
    }
    function setFilter(field, value) {
      if (!canManage() || busy() || !Object.hasOwn(filters, field)) return
      filters[field] = value
    }

    const clearLoadFailure = () => {
      loadFailure.value = null
      failureOwner = () => false
      failureContextKey.value += 1
    }
    const isCurrentFailure = (source, context) => failureOwner() && Boolean(source)
      && source === loadFailure.value && context === failureContextKey.value

    const buildParams = () => ({
      ...buildListParams(appliedFilters.value),
      page: pagination.current,
      pageSize: pagination.pageSize,
    })

    const applySummary = body => {
      const next = readSummary(body)
      summary.totalCount = next.totalCount
      summary.totalBytes = next.totalBytes
      summary.totalBytesText = next.totalBytesText
      summary.zeroRefCount = next.zeroRefCount
      summary.scanRows = next.scanRows
      summary.retention = next.retention
      summary.usagePercent = next.usagePercent
    }

    const load = async () => {
      if (!canManage() || busy()) return false
      const ticket = ++readVersion
      const owns = () => canManage() && ticket === readVersion
      clearLoadFailure()
      const context = failureContextKey.value
      const ownsFailure = () => owns() && context === failureContextKey.value
      loading.value = true
      try {
        const res = await getTicketImageCacheList(buildParams())
        if (!ownsFailure()) return false
        const body = res?.data
        const items = body?.items
        const total = readCacheCount(body?.total)
        if (res?.status !== 200 || !Array.isArray(items) || total === null) {
          failureOwner = ownsFailure
          loadFailure.value = ensureRequestFailure(res)
          return false
        }
        rows.value = items.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
        pagination.total = total
        applySummary(body)
        const page = readCacheCount(body?.page)
        const size = readCacheCount(body?.pageSize)
        if (page) pagination.current = page
        if (size) pagination.pageSize = size
        hasLoaded.value = true
        filterError.value = ''
        // 刷新后丢掉已不存在于列表中的选择（否则批量删会把看不见的行也带上）。
        const present = new Set(rows.value.map(item => item.sha256))
        selectedShas.value = selectedShas.value.filter(sha => present.has(sha))
        return true
      } catch (error) {
        if (ownsFailure()) { failureOwner = ownsFailure; loadFailure.value = ensureRequestFailure(error) }
        return false
      } finally {
        if (owns()) loading.value = false
      }
    }

    const reload = () => {
      if (!canManage() || busy()) return
      filterError.value = ''
      actionWarning.value = ''
      appliedFilters.value = {
        mediaType: filters.mediaType || '',
        scanState: filters.scanState || '',
        refFilter: filters.refFilter || '',
        keyword: String(filters.keyword || '').trim().toLowerCase(),
      }
      pagination.current = 1
      selectedShas.value = []
      return load()
    }

    const resetFilters = () => {
      if (!canManage() || busy()) return
      Object.assign(filters, emptyFilters())
      appliedFilters.value = {}
      filterError.value = ''
      actionWarning.value = ''
      pagination.current = 1
      selectedShas.value = []
      return load()
    }

    const onTableChange = page => {
      if (!canManage() || busy()) return
      pagination.current = page?.pageSize === pagination.pageSize ? Number(page.current) || 1 : 1
      pagination.pageSize = Number(page?.pageSize) || pagination.pageSize
      return load()
    }

    const onSelectionChange = keys => {
      if (!canManage() || busy()) return
      selectedShas.value = (Array.isArray(keys) ? keys : []).filter(key => typeof key === 'string')
    }
    const clearSelection = () => { selectedShas.value = [] }

    // mutation 统一走这里：串行化、身份校验、失败态收敛。
    // successText 可以是函数：删除/清理要回显真实数字，不能预先写死文案。
    const runMutation = async (action, successText) => {
      if (!canManage() || busy()) return null
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      const context = failureContextKey.value
      const ownsFailure = () => owns() && context === failureContextKey.value
      mutating.value = true
      actionWarning.value = ''
      try {
        const res = await action()
        if (!ownsFailure()) return null
        if (res?.status !== 200) {
          // 409 是**正常业务结果**（被引用不能删），不是故障：用 warning 展示具体原因。
          if (res?.status === 409) {
            actionWarning.value = String(res?.data?.message ?? '').trim()
              || '该缓存仍被工单图片引用，无法删除。'
            return res
          }
          failureOwner = ownsFailure
          loadFailure.value = ensureRequestFailure(res)
          return null
        }
        message.success(typeof successText === 'function' ? successText(res) : successText)
        return res
      } catch (error) {
        if (ownsFailure()) { failureOwner = ownsFailure; loadFailure.value = ensureRequestFailure(error) }
        return null
      } finally {
        if (owns()) mutating.value = false
      }
    }

    const confirmDelete = async record => {
      const sha = String(record?.sha256 || '')
      // 三道闸门缺一不可：标识合法、ref_count 为 0、身份仍有效。
      // isValidSha256 在这里也用一次（不只依赖 API 层），因为它同时挡掉
      // 「列表数据被污染后点了删除」这种把非法路径打到后端的形态。
      if (!isValidSha256(sha) || !canDeleteBlob(record) || !canManage() || busy()) return false
      const result = await Swal.fire({
        title: '确认删除这份缓存？',
        text: `sha256 前缀 ${shortSha(sha)}，大小 ${formatCacheBytes(record.byteSize)}。当前 ref_count = 0（没有任何工单图片引用），删除后字节立即消失且不可恢复。`,
        icon: 'warning',
        showCancelButton: true,
        confirmButtonText: '确认删除',
        cancelButtonText: '取消',
      })
      if (!result.isConfirmed || !canManage()) return false
      await runMutation(() => deleteTicketImageCache(sha), '缓存已删除')
      return load()
    }

    const confirmBatchDelete = async () => {
      if (!canManage() || busy() || selectedShas.value.length === 0) return false
      // 只提交 ref_count = 0 的项：后端也会逐条挡住，但让用户在确认框里
      // 先看到「有几条其实删不掉」比事后收 409 更清楚。
      const deletable = selectedShas.value.filter(sha => {
        const row = rows.value.find(item => item.sha256 === sha)
        return row ? canDeleteBlob(row) : false
      })
      const skipped = selectedShas.value.length - deletable.length
      if (deletable.length === 0) {
        actionWarning.value = '所选记录仍被工单图片引用，无法删除。请先删除对应工单图片或等待其过期。'
        return false
      }
      const result = await Swal.fire({
        title: `确认删除所选的 ${deletable.length} 条缓存？`,
        text: skipped > 0
          ? `另有 ${skipped} 条仍被工单图片引用，会被跳过。删除后字节立即消失且不可恢复。`
          : '这些记录都没有任何工单图片引用。删除后字节立即消失且不可恢复。',
        icon: 'warning',
        showCancelButton: true,
        confirmButtonText: '确认删除',
        cancelButtonText: '取消',
      })
      if (!result.isConfirmed || !canManage()) return false
      await runMutation(() => batchDeleteTicketImageCache(deletable), res => {
        const deleted = readCacheCount(res?.data?.deleted) ?? 0
        const blocked = Array.isArray(res?.data?.blocked) ? res.data.blocked.length : 0
        return blocked > 0
          ? `已删除 ${deleted} 条，${blocked} 条仍被引用而跳过`
          : `已删除 ${deleted} 条缓存`
      })
      selectedShas.value = []
      return load()
    }

    // 判据 ③：清理结果必须把两个数字分开显示 —— describeCleanResult 负责这点，
    // 这里只把后端原样存下来（available=false 也要能显示原因）。
    const runCleanNow = async () => {
      if (!canManage() || busy()) return false
      cleanResult.value = null
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      cleaning.value = true
      actionWarning.value = ''
      try {
        const res = await cleanTicketImageCacheNow()
        if (!owns()) return false
        if (res?.status !== 200) {
          if (res?.status === 503) {
            actionWarning.value = String(res?.data?.message ?? '').trim() || '清理失败，请稍后重试。'
            return false
          }
          actionWarning.value = '清理未完成，请稍后重试。'
          return false
        }
        cleanResult.value = res?.data && typeof res.data === 'object' ? { ...res.data } : null
        message.success(describeCleanResult(cleanResult.value))
        return load()
      } catch (error) {
        if (owns()) actionWarning.value = '清理失败，请稍后重试。'
        return false
      } finally {
        if (owns()) cleaning.value = false
      }
    }

    const resetDraft = () => {
      Object.assign(filters, emptyFilters())
      appliedFilters.value = {}
      filterError.value = ''
      cleanResult.value = null
      selectedShas.value = []
    }

    const retire = () => {
      readVersion += 1
      writeVersion += 1
      pagination.current = 1
    }
    const resetSummary = () => {
      summary.totalCount = 0
      summary.totalBytes = 0
      summary.totalBytesText = '0 B'
      summary.zeroRefCount = 0
      summary.scanRows = []
      summary.retention = { ageText: '—', sizeText: '—', intervalText: '—', limitBytes: null }
      summary.usagePercent = null
    }
    watch(() => [currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status], () => {
      retire()
      rows.value = []
      pagination.total = 0
      hasLoaded.value = false
      resetSummary()
      actionWarning.value = ''
      resetDraft()
      if (mounted && canManage()) void load()
    }, { flush: 'sync' })
    watch(() => getCurrentUserLogoutEpoch(), value => {
      if (value === observedLogout) return
      observedLogout = value
      retire()
      rows.value = []
      pagination.total = 0
      hasLoaded.value = false
      resetSummary()
      clearLoadFailure()
    }, { flush: 'sync' })
    watch(() => route.path, () => {
      retire()
      rows.value = []
      pagination.total = 0
      hasLoaded.value = false
      resetSummary()
      actionWarning.value = ''
      clearLoadFailure()
    }, { flush: 'sync' })
    onMounted(() => {
      mounted = true
      // 只发这一条列表请求：本页不在挂载期补查汇总或触发清理。
      if (canManage()) void load()
    })
    onBeforeUnmount(() => {
      alive = false
      mounted = false
      readVersion += 1
      writeVersion += 1
      clearLoadFailure()
    })

    return {
      fieldId, canManage, busy, columns, rows, filters, appliedFilters, loading, mutating, cleaning,
      hasLoaded, filterError, actionWarning, loadFailure, failureContextKey, failureMessage,
      isCurrentFailure, pagination, scopeDescription, emptyText, summary, cleanResult,
      cleanResultText, mediaTypeOptions, refFilterOptions, scanStateOptions,
      selectedShas, onSelectionChange, clearSelection,
      formatCacheBytes, scanStateText, scanStateColor, formatScore, formatDateTime,
      shortSha, deleteBlockedHint, canDeleteBlob, setFilter, load, reload, resetFilters,
      onTableChange, confirmDelete, confirmBatchDelete, runCleanNow, sumTableColumnWidths,
    }
  },
}
</script>

<style scoped>
.page-alert, .filter-form, .status-alert { margin-bottom: 16px; }
.cache-summary { margin-bottom: 16px; }
.summary-label { color: var(--console-muted); font-size: 14px; }
.summary-value { margin-top: 8px; color: var(--console-text); font-size: clamp(20px, 2.6vw, 28px); font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.summary-value--pending { font-size: 20px; }
.summary-hint { margin-top: 6px; color: var(--console-muted); font-size: 12px; line-height: 1.5; }
.scan-lines { margin-top: 8px; display: grid; gap: 4px; }
.scan-line { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; font-size: 13px; }
.scan-line-label { color: var(--console-muted); }
.scan-line-value { color: var(--console-text); font-weight: 600; font-variant-numeric: tabular-nums; }
.filter-narrow { width: 150px; }
.filter-wide { width: 220px; }
.filter-keyword { width: 200px; }
.filter-error { margin: 0 0 16px; color: var(--console-danger, #c13e41); font-size: 13px; }
.selection-hint { color: var(--console-muted); font-size: 13px; }
.sha-cell, .mono-cell, .time-cell { font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
@media (max-width: 640px) {
  .filter-form :deep(.ant-form-item) { width: 100%; margin-inline-end: 0; }
  .filter-narrow, .filter-wide, .filter-keyword { width: 100%; }
}
</style>
