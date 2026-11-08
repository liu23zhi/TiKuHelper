<template>
  <div class="console-page ocr-cache-page">
    <a-alert v-if="!canManage()" type="warning" show-icon message="当前身份无 OCR 缓存管理权限，请使用管理员账号登录。" />
    <div v-if="canManage()" :key="frameKey" ref="pageRoot" :onInputCapture="pageEntry.guardNative" :onChangeCapture="pageEntry.guardNative" :onClickCapture="pageEntry.guardNative" :onKeydownCapture="pageEntry.guardNative">
    <ConsolePageHeading title="OCR 缓存管理" description="查看和维护已保存的图片识别结果。">
      <template #actions>
        <a-button type="primary" :disabled="deleting || adding || !canAct()" :onClick="pageEntry.bind(openAddDialog)">手动添加</a-button>
        <a-button :loading="loading" :disabled="deleting || adding || !canAct()" :onClick="pageEntry.bind(load)">刷新列表</a-button>
      </template>
    </ConsolePageHeading>
    <ConsoleSummaryGrid>
      <a-statistic title="筛选结果" :value="hasLoaded ? matched : '--'" suffix="条" />
      <a-statistic title="当前页" :value="hasLoaded ? rows.length : '--'" suffix="条" />
      <a-statistic title="当前选择" :value="selectedCount" suffix="条" />
      <div class="provider-status"><span>识别服务</span><a-tag :color="configured ? 'green' : 'default'">{{ configured === undefined ? (loading ? '加载中' : '状态未知') : configured ? 'UAPI 已配置' : '未配置有效 UAPI 密钥' }}</a-tag></div>
    </ConsoleSummaryGrid>
    <a-alert v-if="operationWarning" type="warning" show-icon :message="operationWarning" class="operation-alert" />
    <ConsolePanel title="筛选缓存" description="关键词匹配识别文本、图片 URL、URL 哈希、图片哈希或图片类型；更新时间按上海时间筛选，包含结束日期当天。">
      <a-form :model="filters" layout="inline" class="filter-form" :onFinish="pageEntry.bind(reload)">
        <a-form-item label="关键词" :html-for="fieldId('keyword')">
          <a-input :id="fieldId('keyword')" :value="filters.keyword" :onUpdate:value="pageEntry.bind(setFilter, 'keyword')" allow-clear placeholder="识别文本 / 图片 URL / URL 哈希 / 图片哈希" class="keyword-input" :disabled="deleting || adding || !canAct()" />
        </a-form-item>
        <a-form-item label="图片类型" :html-for="fieldId('contentType')">
          <a-select :id="fieldId('contentType')" :value="filters.contentType" :onUpdate:value="pageEntry.bind(setFilter, 'contentType')" class="content-type-select" :disabled="deleting || adding || !canAct()">
            <a-select-option value="">全部类型</a-select-option>
            <a-select-option v-for="type in contentTypes" :key="type" :value="type">{{ type }}</a-select-option>
          </a-select>
        </a-form-item>
        <a-form-item label="更新时间">
          <a-space class="date-filter-space">
            <a-input :value="filters.updatedFrom" :onUpdate:value="pageEntry.bind(setFilter, 'updatedFrom')" type="date" aria-label="更新时间起始日期" :disabled="deleting || adding || !canAct()" />
            <span>至</span>
            <a-input :value="filters.updatedTo" :onUpdate:value="pageEntry.bind(setFilter, 'updatedTo')" type="date" aria-label="更新时间结束日期" :disabled="deleting || adding || !canAct()" />
          </a-space>
        </a-form-item>
        <a-form-item>
          <a-space wrap>
            <a-button type="primary" html-type="submit" :loading="loading" :disabled="deleting || adding || !canAct()">查询</a-button>
            <a-button :disabled="deleting || adding || !canAct()" :onClick="pageEntry.bind(resetFilters)">重置筛选</a-button>
          </a-space>
        </a-form-item>
      </a-form>
      <RequestFailureAlert v-if="failure" :failure="failure" :message="errorMessage" :context-key="failureContext" :is-current="isCurrentFailure" :busy="loading || deleting" />
      <a-alert v-else-if="errorMessage" type="error" show-icon :message="errorMessage" class="status-alert" />
    </ConsolePanel>
    <ConsolePanel title="识别结果" :description="scopeDescription">
      <a-alert v-if="hasLoaded && pagination.total" type="info" show-icon class="selection-alert">
        <template #message>
          <div class="selection-alert-content">
            <span>{{ selectionSummary }}</span>
            <a-button danger size="small" :loading="deleting" :disabled="loading || adding || deleting || !!errorMessage || !selectedCount" :onClick="pageEntry.bind(deleteSelected)">删除所选</a-button>
            <a-button type="link" size="small" :disabled="allCurrentPageSelected" :onClick="pageEntry.bind(handleSelectCurrentPage)">全选本页</a-button>
            <a-button type="link" size="small" :disabled="!hasCurrentPageSelection" :onClick="pageEntry.bind(handleClearCurrentPage)">取消本页全选</a-button>
            <a-button type="link" size="small" :disabled="allRowsSelected" :onClick="pageEntry.bind(handleSelectAll)">全选全部</a-button>
            <a-button type="link" size="small" :disabled="!selectedCount" :onClick="pageEntry.bind(handleClearSelection)">取消全部全选</a-button>
          </div>
        </template>
      </a-alert>
      <p class="cache-hint">保存识别文本、完整图片 URL 与哈希，不保存原始图片。命中缓存仍按现有搜题计费口径处理；删除不会取消正在执行的请求，旧请求的结果不会重新写回缓存。</p>
      <ConsoleTableShell label="OCR 缓存记录">
        <a-table table-layout="fixed" :data-source="rows" :columns="columns" :loading="loading" :pagination="pagination" row-key="urlHash" :row-selection="rowSelection" :scroll="{ x: sumTableColumnWidths(columns, 48) }" :locale="{ emptyText: errorMessage ? '列表加载失败，请重试' : hasLoaded ? '暂无符合条件的 OCR 缓存' : 'OCR 缓存尚未读取成功' }" :onChange="pageEntry.bind(onTableChange)">
          <template #bodyCell="{ column, record }">
            <span v-if="column.key === 'id'">{{ record.id ?? '-' }}</span>
            <OverflowTableText v-else-if="column.key === 'ocrText'" :text="record.ocrText || '-'" :expanded="expandedOCRText.has(record.urlHash)" :onToggle="pageEntry.bind(toggleOCRText, record)" />
            <span v-else-if="column.key === 'url'" class="url-value">{{ record.url || '历史记录未保存 URL' }}</span>
            <code v-else-if="column.key === 'urlHash' || column.key === 'imageSha256'" class="hash-value">{{ record[column.key] || '-' }}</code>
            <span v-else-if="column.key === 'updatedAt'">{{ formatDate(record.updatedAt) }}</span>
            <a-space v-else-if="column.key === 'action'" wrap>
              <a-button type="link" :onClick="pageEntry.bind(showDetails, record)">查看全文</a-button>
              <a-popconfirm title="删除这条 OCR 缓存？" ok-text="删除" cancel-text="取消" :onConfirm="pageEntry.bind(deleteSingle, record)">
                <a-button type="link" danger :disabled="loading || adding || deleting || !!errorMessage">删除</a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </a-table>
      </ConsoleTableShell>
    </ConsolePanel>
    <a-modal v-if="addVisible" :key="addKey" :open="addVisible" title="手动添加 OCR 缓存" ok-text="识别并添加" cancel-text="取消" :confirm-loading="adding" :closable="!adding" :mask-closable="!adding" :keyboard="!adding" :cancel-button-props="{ disabled: adding }" :onOk="addEntry.bind(addOCRCache)" :onCancel="addEntry.bind(closeAddDialog)" :focus-trigger-after-close="false" :wrap-props="{ 'aria-modal': true }">
      <div ref="addSurface">
      <a-alert type="info" show-icon message="填写可直接访问的图片 URL，系统会自动识别并保存缓存；相同图片可复用已有结果，调用按现有 OCR 价格计入当前管理员账单。" class="add-hint" />
      <a-form :model="addForm" layout="vertical" :onFinish="addEntry.bind(addOCRCache)">
        <a-form-item label="图片 URL" :html-for="fieldId('addImageURL')" name="url" required>
          <a-input :id="fieldId('addImageURL')" :value="addImageURL" :onUpdate:value="addEntry.bind(setAddURL)" type="url" :disabled="adding" placeholder="https://example.com/image.png" :onPressEnter="addEntry.bind(submitAddFromInput)" />
        </a-form-item>
      </a-form>
      <a-alert v-if="addError" type="error" show-icon :message="addError" class="status-alert" />
      <a-alert v-if="addBillingWarning" type="warning" show-icon :message="addBillingWarning" class="status-alert" />
      <p v-if="addRequestId" class="request-id">请求编号：{{ addRequestId }}</p>
      <template v-if="addRecognizedText">
        <h3 class="response-title">识别结果</h3>
        <pre class="ocr-text">{{ addRecognizedText }}</pre>
      </template>
      </div>
    </a-modal>
    <a-modal v-if="detailMounted" :key="detailKey" :open="detailVisible" :onUpdate:open="detailEntry.bind(updateDetailOpen)" :onCancel="detailEntry.bind(closeDetail)" :after-close="detailAfterClose" :focus-trigger-after-close="false" :wrap-props="{ 'aria-modal': true }" title="OCR 缓存详情" :footer="null" width="760px">
      <div ref="detailSurface">
      <a-descriptions :column="1" size="small" bordered>
        <a-descriptions-item label="图片 URL"><span class="url-value">{{ detailRecord.url || '历史记录未保存 URL' }}</span></a-descriptions-item>
        <a-descriptions-item label="URL 哈希"><code class="hash-value">{{ detailRecord.urlHash }}</code></a-descriptions-item>
        <a-descriptions-item label="图片 SHA-256"><code class="hash-value">{{ detailRecord.imageSha256 }}</code></a-descriptions-item>
        <a-descriptions-item label="图片类型">{{ detailRecord.contentType || '-' }}</a-descriptions-item>
        <a-descriptions-item label="更新时间（上海）">{{ formatDate(detailRecord.updatedAt) }}</a-descriptions-item>
      </a-descriptions>
      <div class="current-image-tools">
        <a-button type="primary" :loading="imageLoading" :disabled="!detailRecord.url || configured !== true" :onClick="detailEntry.bind(loadCurrentImage)">获取当前图片</a-button>
        <p v-if="configured !== true" class="cache-hint">未确认有效 UAPI 密钥，暂不可获取图片；已缓存的识别文本仍可查看。</p>
        <a-alert v-if="imageError" type="error" show-icon :message="imageError" />
        <a-spin v-if="imageLoading" tip="正在获取图片" />
        <img v-if="currentImageUrl" :src="currentImageUrl" alt="缓存图片预览" class="current-image-preview" :onError="detailEntry.bind(handleCurrentImageError, currentImageUrl)" />
      </div>
      <h3 class="response-title">识别全文</h3>
      <pre class="ocr-text">{{ detailRecord.ocrText || '-' }}</pre>
      </div>
    </a-modal>
    </div>
  </div>
</template>

<script>
import { computed, defineComponent, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, useId, watch } from 'vue'
import { useRoute } from 'vue-router'
import { Descriptions, message } from 'ant-design-vue'
import Swal from 'sweetalert2'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import OverflowTableText from '@/components/OverflowTableText.vue'
import { buildAIToolTestPayload } from '@/utils/aiSearchConfigHelpers.mjs'
import { getAIOCRCacheEntries, getAIOCRCacheImage, deleteAIOCRCacheBatch, testAIToolCall } from '@/api/TikuComponentApi'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'
import { useCurrentUserDisplay, isAdminUser, getCurrentUserLogoutEpoch, getStoredCurrentUser } from '@/utils/currentUser'
import { inheritTransportRequestId } from '@/utils/requestHelpers.mjs'
import { inheritRequestFailureView } from '@/utils/requestFailureView.mjs'

const emptyFilters = () => ({ keyword: '', contentType: '', updatedFrom: '', updatedTo: '' })
const projectFailure = source => {
  const carrier = Object.freeze({})
  inheritTransportRequestId(carrier, source)
  return inheritRequestFailureView(carrier, source)
}
const nonnegative = value => {
  if (!['number', 'string'].includes(typeof value) || String(value).trim() === '') return null
  const number = Number(value)
  return Number.isFinite(number) && number >= 0 ? number : null
}
export default defineComponent({
  name: 'OCRCache',
  components: { ADescriptions: Descriptions, ADescriptionsItem: Descriptions.Item, RequestFailureAlert, OverflowTableText },
  methods: { sumTableColumnWidths },
  setup() {
    const route = useRoute(), mountedPath = route.path
    const prefix = 'ocr-cache-' + useId(), fieldId = field => prefix + '-' + field
    const rows = ref([]), filters = reactive(emptyFilters()), appliedFilters = ref({})
    const loading = ref(false), deleting = ref(false), hasLoaded = ref(false)
    const configured = ref(undefined), matched = ref(0), errorMessage = ref('')
    const pageRoot = ref(null), frameKey = ref(0), draining = ref(false), queryVersion = ref(0)
    const detailVisible = ref(false), detailMounted = ref(false), detailKey = ref(0)
    const detailRecord = ref({}), detailSurface = ref(null), detailAfterClose = shallowRef(null)
    const addVisible = ref(false), adding = ref(false), addKey = ref(0), addSurface = ref(null), addImageURL = ref('')
    const addForm = reactive({ url: addImageURL })
    const addError = ref(''), addRequestId = ref(''), addRecognizedText = ref(''), addBillingWarning = ref('')
    const operationWarning = ref('')
    const imageLoading = ref(false), imageError = ref(''), currentImageUrl = ref('')
    const expandedOCRText = ref(new Set())
    let imageVersion = 0
    const failure = shallowRef(null), failureContext = ref(0)
    const pagination = reactive({ current: 1, pageSize: 20, total: 0, showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'] })
    const { currentUser } = useCurrentUserDisplay()
    let alive = true, mounted = false, epoch = 0, readVersion = 0, writeVersion = 0
    let detailOwner = null, pendingPrompt = null, failureOwner = () => false, loadedQuery = ''
    const principal = () => JSON.stringify([currentUser.value?.id ?? '', String(currentUser.value?.role || '').trim().toLowerCase(), String(currentUser.value?.status ?? '')])
    let observedPrincipal = principal(), observedLogout = getCurrentUserLogoutEpoch(), authorizedLogout = observedLogout
    const validAdmin = () => {
      const user = currentUser.value, id = Number(user?.id)
      return Number.isSafeInteger(id) && id > 0 && id <= 2147483647 && isAdminUser(user)
        && !['disabled', '0'].includes(String(user?.status ?? 'enabled'))
    }
    const canManage = () => alive && route.path === mountedPath && authorizedLogout === getCurrentUserLogoutEpoch() && validAdmin()
    const canAct = () => canManage() && !draining.value
    const captureOwner = () => {
      const ticket = epoch, identity = principal(), logout = getCurrentUserLogoutEpoch()
      return () => {
        getStoredCurrentUser()
        return canManage() && ticket === epoch && identity === principal() && logout === getCurrentUserLogoutEpoch()
      }
    }
    const makeEntry = (extra = () => true, queryBound = true) => {
      const owns = captureOwner(), query = queryVersion.value, frame = frameKey.value
      const isCurrent = () => owns() && canAct() && (!queryBound || query === queryVersion.value) && frame === frameKey.value && extra()
      return { isCurrent,
        guardNative(event) { if (!isCurrent()) { event.preventDefault(); event.stopImmediatePropagation() } },
        bind: (fn, ...args) => (...events) => isCurrent() ? fn(...args, ...events) : undefined,
      }
    }
    const pageEntry = computed(() => { frameKey.value; queryVersion.value; return makeEntry() })
    const detailEntry = shallowRef(makeEntry(() => false, false))
    const addEntry = shallowRef(makeEntry(() => false, false))
    const clearFailure = () => { failure.value = null; failureOwner = () => false; failureContext.value += 1 }
    // Scalar owner factory: never close over an async response, URL filter, record or operation.
    const captureFailureOwner = (owns, ticket, kind, context) => () => owns()
      && ticket === (kind === 'read' ? readVersion : writeVersion) && context === failureContext.value
    const isCurrentFailure = (source, context) => failureOwner() && source === failure.value && context === failureContext.value
    const visible = node => {
      if (!(node instanceof HTMLElement) || !node.isConnected) return false
      for (let e = node; e; e = e.parentElement) {
        const style = window.getComputedStyle(e)
        if (e.hidden || e.inert || e.getAttribute('aria-hidden') === 'true' || style.display === 'none' || style.visibility === 'hidden') return false
      }
      return true
    }
    const hidePrivate = node => {
      if (!(node instanceof HTMLElement)) return
      node.style.visibility = 'hidden'; node.setAttribute('aria-hidden', 'true')
      if (node.contains(document.activeElement)) document.activeElement.blur?.()
      node.querySelectorAll('input,textarea').forEach(input => { input.value = ''; input.defaultValue = ''; input.removeAttribute('value') })
      const texts = document.createTreeWalker(node, 4)
      while (texts.nextNode()) texts.currentNode.nodeValue = ''
    }
    const clearImageState = () => {
      imageVersion += 1
      if (currentImageUrl.value) URL.revokeObjectURL(currentImageUrl.value)
      currentImageUrl.value = ''; imageError.value = ''; imageLoading.value = false
    }
    const resetAddResult = () => {
      addError.value = ''; addRequestId.value = ''; addRecognizedText.value = ''; addBillingWarning.value = ''
    }
    let addOpener = null
    const retireAdd = () => {
      addOpener = null
      hidePrivate(addSurface.value)
      addKey.value += 1; addEntry.value = makeEntry(() => false, false)
      adding.value = false; addVisible.value = false; addImageURL.value = ''; resetAddResult()
    }
    const retireDetail = () => {
      clearImageState()
      detailOwner = null; detailEntry.value = makeEntry(() => false, false); detailAfterClose.value = null
      hidePrivate(detailSurface.value); detailVisible.value = false; detailRecord.value = {}; detailMounted.value = false
    }
    const closePrompt = () => {
      const own = pendingPrompt; pendingPrompt = null
      if (own?.popup && Swal.getPopup() === own.popup) Swal.close()
    }
    const clearData = () => { rows.value = []; matched.value = 0; pagination.total = 0; hasLoaded.value = false; configured.value = undefined; loadedQuery = '' }
    const retireScope = () => {
      const ticket = ++epoch; readVersion += 1; writeVersion += 1; queryVersion.value += 1; draining.value = true
      hidePrivate(pageRoot.value); retireDetail(); retireAdd(); closePrompt(); clearFailure(); clearData(); clearSelectionState()
      expandedOCRText.value = new Set(); operationWarning.value = '';
      Object.assign(filters, emptyFilters()); appliedFilters.value = {}; pagination.current = 1
      loading.value = false; deleting.value = false; errorMessage.value = ''
      return nextTick().then(() => nextTick()).then(() => {
        if (!alive || ticket !== epoch) return
        frameKey.value += 1; draining.value = false
        if (mounted && canManage()) return load()
      })
    }
    watch(() => [principal(), getCurrentUserLogoutEpoch()], ([next, logout]) => {
      if (next === observedPrincipal && logout === observedLogout) return
      const logoutChanged = logout !== observedLogout
      if (!logoutChanged && next !== observedPrincipal && validAdmin()) authorizedLogout = logout
      observedPrincipal = next; observedLogout = logout
      void retireScope()
    }, { flush: 'sync' })
    watch(() => route.fullPath, () => { void retireScope() }, { flush: 'sync' })
    const contentTypes = computed(() => [...new Set(['image/png', 'image/jpeg', 'image/webp', 'image/gif', 'image/bmp', 'image/tiff', ...rows.value.map(row => row.contentType), filters.contentType].filter(Boolean))])
    const hasFilters = computed(() => Object.keys(appliedFilters.value).length > 0)
    const scopeDescription = computed(() => {
      const scope = appliedFilters.value
      return [scope.keyword && `关键词：${scope.keyword}`, scope.contentType && `类型：${scope.contentType}`, scope.updatedFrom && `起始：${scope.updatedFrom}`, scope.updatedTo && `结束：${scope.updatedTo}`].filter(Boolean).join('；') || '当前范围：全部 OCR 缓存。'
    })
    const columns = [
      { title: 'ID', dataIndex: 'id', key: 'id', width: 90 },
      { title: '识别文本', dataIndex: 'ocrText', key: 'ocrText', width: 320 },
      { title: '图片 URL', dataIndex: 'url', key: 'url', width: 320 },
      { title: '图片类型', dataIndex: 'contentType', key: 'contentType', width: 140 },
      { title: 'URL 哈希', dataIndex: 'urlHash', key: 'urlHash', width: 210 },
      { title: '图片 SHA-256', dataIndex: 'imageSha256', key: 'imageSha256', width: 210 },
      { title: '更新时间（上海）', dataIndex: 'updatedAt', key: 'updatedAt', width: 180 },
      { title: '操作', key: 'action', width: 160 },
    ]
    const selectedKeys = ref(new Set())
    const deselectedKeys = ref(new Set())
    const selectAllMode = ref(false)
    const recordIdentity = record => String(record?.urlHash || '')
    const currentPageKeys = computed(() => rows.value.map(recordIdentity).filter(Boolean))
    const selectedRowKeys = computed(() => selectAllMode.value
      ? currentPageKeys.value.filter(key => !deselectedKeys.value.has(key))
      : currentPageKeys.value.filter(key => selectedKeys.value.has(key)))
    const selectedCount = computed(() => selectAllMode.value
      ? Math.max(0, matched.value - deselectedKeys.value.size)
      : selectedKeys.value.size)
    const allRowsSelected = computed(() => selectAllMode.value && deselectedKeys.value.size === 0 && matched.value > 0)
    const allCurrentPageSelected = computed(() => currentPageKeys.value.length > 0 && selectedRowKeys.value.length === currentPageKeys.value.length)
    const hasCurrentPageSelection = computed(() => selectedRowKeys.value.length > 0)
    const selectionSummary = computed(() => `已选择 ${selectedCount.value} 条，共 ${matched.value} 条`)
    const rowSelection = computed(() => ({ columnWidth: 48, selectedRowKeys: selectedRowKeys.value, onChange: pageEntry.value.bind(onSelectionChange), getCheckboxProps: () => ({ disabled: loading.value || deleting.value || adding.value || !!errorMessage.value }) }))
    const canSelect = () => canAct() && !loading.value && !deleting.value && !adding.value && hasLoaded.value && !errorMessage.value
    const toggleOCRText = record => {
      if (!canAct() || !rows.value.includes(record)) return
      const next = new Set(expandedOCRText.value)
      if (next.has(record.urlHash)) next.delete(record.urlHash)
      else next.add(record.urlHash)
      expandedOCRText.value = next
    }
    const formatDate = value => {
      const date = value ? new Date(value) : null
      return date && Number.isFinite(date.getTime()) ? date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' }) : '-'
    }
    const setFilter = (field, value) => { if (canAct() && !deleting.value && Object.hasOwn(filters, field)) filters[field] = value }
    const load = async () => {
      if (!canAct() || deleting.value) return false
      const owns = captureOwner(), ticket = ++readVersion
      queryVersion.value += 1; clearFailure(); errorMessage.value = ''; loading.value = true
      const ownsFailure = captureFailureOwner(owns, ticket, 'read', failureContext.value)
      const params = { ...appliedFilters.value, page: pagination.current, pageSize: pagination.pageSize }
      const query = JSON.stringify(params)
      if (query !== loadedQuery) clearData()
      try {
        const res = await getAIOCRCacheEntries(params, { errorFeedback: 'local' })
        if (!owns() || ticket !== readVersion) return false
        const data = res?.data, total = nonnegative(data?.total)
        if (res?.status !== 200 || !Array.isArray(data?.data) || total === null || !Number.isInteger(total)) {
          errorMessage.value = '读取 OCR 缓存失败，请重试。'; failureOwner = ownsFailure; failure.value = projectFailure(res); return false
        }
        const lastPage = Math.max(1, Math.ceil(total / pagination.pageSize))
        if (pagination.current > lastPage) { pagination.current = lastPage; return await load() }
        rows.value = data.data.map(row => ({ ...row }))
        const visibleKeys = new Set(rows.value.map(recordIdentity))
        expandedOCRText.value = new Set([...expandedOCRText.value].filter(key => visibleKeys.has(key)))
        matched.value = nonnegative(data.summary?.matched) ?? total; pagination.total = total
        const page = nonnegative(data.page), size = nonnegative(data.pageSize)
        if (Number.isInteger(page) && page > 0) pagination.current = page
        if (Number.isInteger(size) && size > 0) pagination.pageSize = size
        configured.value = typeof data.configured === 'boolean' ? data.configured : undefined
        hasLoaded.value = true; loadedQuery = JSON.stringify({ ...appliedFilters.value, page: pagination.current, pageSize: pagination.pageSize }); return true
      } catch (error) {
        if (!ownsFailure()) return false
        errorMessage.value = hasLoaded.value ? '刷新 OCR 缓存未成功，已保留当前查询的上次结果。' : '读取 OCR 缓存失败，请重试。'
        failureOwner = ownsFailure; failure.value = projectFailure(error); return false
      } finally { if (owns() && ticket === readVersion) loading.value = false }
    }
    const reload = () => {
      if (!canAct() || deleting.value) return
      clearFailure()
      if (filters.updatedFrom && filters.updatedTo && filters.updatedFrom > filters.updatedTo) { errorMessage.value = '起始日期不能晚于结束日期'; return }
      appliedFilters.value = Object.fromEntries(Object.entries(filters).map(([key, value]) => [key, String(value || '').trim()]).filter(([, value]) => value))
      pagination.current = 1; retireDetail(); clearSelectionState(); return load()
    }
    const resetFilters = () => { if (canAct() && !deleting.value) { Object.assign(filters, emptyFilters()); return reload() } }
    const onTableChange = page => {
      if (!canAct() || deleting.value) return
      pagination.current = page.pageSize === pagination.pageSize ? page.current : 1
      pagination.pageSize = page.pageSize; return load()
    }
    const showDetails = (record, event) => {
      if (!canAct() || loading.value || deleting.value || !rows.value.includes(record)) return
      showDetail(record, event)
    }
    const showDetail = (record, event) => {
      retireDetail()
      const owns = captureOwner(), key = ++detailKey.value
      const owner = { key, owns, opener: event?.currentTarget || document.activeElement }
      owner.isCurrent = () => owns() && detailOwner === owner && detailVisible.value
      detailOwner = owner; detailRecord.value = { ...record }; detailMounted.value = true; detailVisible.value = true
      detailEntry.value = makeEntry(owner.isCurrent, false)
      detailAfterClose.value = async () => {
        if (!owns() || detailOwner !== owner || detailVisible.value) return
        const target = owner.opener, dialog = detailSurface.value?.closest('[role="dialog"]')
        retireDetail(); await nextTick()
        if (!owns() || detailOwner || key !== detailKey.value || !visible(target) || target.tabIndex < 0 || target.matches(':disabled,[aria-disabled="true"]')) return
        const active = document.activeElement
        if (active !== document.body && active !== target && !dialog?.contains(active)) return
        if ([...document.querySelectorAll('[role="dialog"]')].some(node => node !== dialog && visible(node))) return
        target.focus({ preventScroll: true })
      }
    }
    const closeDetail = () => {
      if (!detailOwner?.isCurrent()) return
      detailVisible.value = false
      clearImageState()
    }
    const updateDetailOpen = value => { if (!value) closeDetail() }
    const loadCurrentImage = async () => {
      const owner = detailOwner
      if (!owner?.isCurrent() || imageLoading.value) return
      clearImageState()
      const ticket = ++imageVersion
      const owns = () => owner.isCurrent() && ticket === imageVersion
      const { url, urlHash } = detailRecord.value
      if (!url) { imageError.value = '该历史缓存记录没有保存图片 URL'; return }
      if (!urlHash) { imageError.value = '该缓存记录缺少 URL 哈希，无法获取图片'; return }
      if (configured.value !== true) { imageError.value = '未配置有效 UAPI 密钥，未发出图片请求。'; return }
      imageLoading.value = true
      try {
        const res = await getAIOCRCacheImage(urlHash)
        if (!owns()) return
        if (configured.value !== true) { imageError.value = '未配置有效 UAPI 密钥，已停止预览。'; return }
        const blob = res?.data
        if (res?.status !== 200 || !(blob instanceof Blob) || !/^image\/(?:png|jpeg|webp|gif|bmp|tiff)$/i.test(blob.type)) {
          imageError.value = '获取当前图片失败，请核对图片是否可用。'; return
        }
        currentImageUrl.value = URL.createObjectURL(blob)
      } catch {
        if (owns()) imageError.value = '获取当前图片失败，请重试。'
      } finally { if (owns()) imageLoading.value = false }
    }
    const handleCurrentImageError = imageURL => {
      if (!imageURL || imageURL !== currentImageUrl.value) return
      clearImageState(); imageError.value = '图片显示失败'
    }
    const openAddDialog = event => {
      if (!canAct() || adding.value || deleting.value || addVisible.value) return
      retireAdd()
      const key = addKey.value
      addVisible.value = true
      addEntry.value = makeEntry(() => key === addKey.value && addVisible.value, false)
      addOpener = event?.currentTarget
    }
    const setAddURL = value => { if (!adding.value) addImageURL.value = value }
    const closeAddDialog = () => {
      if (adding.value) return
      const target = addOpener, owns = captureOwner()
      addOpener = null; retireAdd()
      void nextTick(() => {
        if (owns() && !addVisible.value && visible(target) && document.activeElement === document.body) target.focus({ preventScroll: true })
      })
    }
    const submitAddFromInput = event => { event.preventDefault(); return addOCRCache() }
    const addOCRCache = async () => {
      if (!addEntry.value.isCurrent() || adding.value || deleting.value) return
      let payload
      try {
        const { mode, tool, url } = buildAIToolTestPayload({ mode: 'direct', tool: 'image_ocr', url: addImageURL.value })
        payload = { mode, tool, url }
      } catch (error) {
        addError.value = error.message || '请填写有效的图片 URL'
        return
      }
      const owns = addEntry.value.isCurrent
      const opener = addOpener
      resetAddResult(); adding.value = true
      try {
        const res = await testAIToolCall(payload, { errorFeedback: 'local' })
        if (!owns()) return
        const data = res?.data || {}
        addRequestId.value = data.requestId || ''
        addBillingWarning.value = data.billingWarning || ''
        addRecognizedText.value = Array.isArray(data.toolResult) ? (data.toolResult.find(item => item?.tool === 'image_ocr')?.text || '') : ''
        if (res?.status !== 200 || !data.success) {
          addError.value = '图片识别未成功，请先刷新列表核对，勿重复提交。'; return
        }
        if (!data.ocrCacheEntry?.urlHash) {
          addError.value = '识别已完成，但未确认缓存保存成功。请先刷新列表核对，勿重复提交。'; return
        }
        operationWarning.value = addBillingWarning.value ? (addBillingWarning.value + (addRequestId.value ? ' 请求编号：' + addRequestId.value : '')) : ''
        addOpener = null
        retireAdd()
        showDetail({ ...data.ocrCacheEntry, url: payload.url }, { currentTarget: opener })
        message.success('OCR 缓存已添加')
        pagination.current = 1; clearSelectionState()
        await load()
      } catch {
        if (owns()) addError.value = '图片识别请求结果未确认，请先刷新列表核对，勿重复提交。'
      } finally { if (owns()) adding.value = false }
    }
    const clearSelectionState = () => {
      selectedKeys.value = new Set()
      deselectedKeys.value = new Set()
      selectAllMode.value = false
    }
    const handleSelectCurrentPage = () => {
      if (!canSelect() || !currentPageKeys.value.length) return
      if (selectAllMode.value) {
        const next = new Set(deselectedKeys.value)
        currentPageKeys.value.forEach(key => next.delete(key))
        deselectedKeys.value = next
        return
      }
      const next = new Set(selectedKeys.value)
      currentPageKeys.value.forEach(key => next.add(key))
      selectedKeys.value = next
    }
    const handleClearCurrentPage = () => {
      if (!canSelect() || !currentPageKeys.value.length) return
      if (selectAllMode.value) {
        const next = new Set(deselectedKeys.value)
        currentPageKeys.value.forEach(key => next.add(key))
        deselectedKeys.value = next
        return
      }
      const next = new Set(selectedKeys.value)
      currentPageKeys.value.forEach(key => next.delete(key))
      selectedKeys.value = next
    }
    const handleSelectAll = () => {
      if (!canSelect() || !matched.value) return
      selectedKeys.value = new Set()
      deselectedKeys.value = new Set()
      selectAllMode.value = true
    }
    const handleClearSelection = () => { if (canSelect()) clearSelectionState() }
    function onSelectionChange(keys) {
      if (!canSelect()) return
      const selected = new Set(Array.isArray(keys) ? keys : [])
      if (selectAllMode.value) {
        const next = new Set(deselectedKeys.value)
        currentPageKeys.value.forEach(key => selected.has(key) ? next.delete(key) : next.add(key))
        deselectedKeys.value = next
        return
      }
      const next = new Set(selectedKeys.value)
      currentPageKeys.value.forEach(key => selected.has(key) ? next.add(key) : next.delete(key))
      selectedKeys.value = next
    }
    const selectedDeletePayload = () => selectAllMode.value
      ? { ...appliedFilters.value, items: [], all: true, except: [...deselectedKeys.value] }
      : { items: [...selectedKeys.value], all: false, except: [] }
    const runDelete = async (body, count, record) => {
      if (!canSelect() || !count) return
      const owns = captureOwner(), ticket = ++writeVersion
      clearFailure(); deleting.value = true
      const ownsFailure = captureFailureOwner(owns, ticket, 'write', failureContext.value)
      const prompt = record ? null : { popup: null }
      let reloadAfter = false
      try {
        if (prompt) {
          pendingPrompt = prompt
          const scope = body.all ? scopeDescription.value + '包含其他分页。' : ''
          const result = await Swal.fire({
            title: '确认删除所选 OCR 缓存？',
            text: scope + '将删除 ' + count + ' 条已选择的缓存，正在执行的请求不会被取消。',
            icon: 'warning', showCancelButton: true, confirmButtonText: '确认删除', cancelButtonText: '取消',
            didOpen: popup => { prompt.popup = popup; if (!owns() && Swal.getPopup() === popup) Swal.close() },
          })
          if (pendingPrompt === prompt) pendingPrompt = null
          if (!result.isConfirmed || !owns() || ticket !== writeVersion) return
        }
        const res = await deleteAIOCRCacheBatch(body)
        if (!owns() || ticket !== writeVersion) return
        if (res?.status !== 200) {
          errorMessage.value = '删除 OCR 缓存请求未完成，请重新读取核对。'; failureOwner = ownsFailure; failure.value = projectFailure(res); return
        }
        if (!record || detailRecord.value.urlHash === record.urlHash) retireDetail()
        if (record) {
          const selected = new Set(selectedKeys.value), deselected = new Set(deselectedKeys.value)
          selected.delete(record.urlHash); deselected.delete(record.urlHash)
          selectedKeys.value = selected; deselectedKeys.value = deselected
          if (rows.value.length === 1 && pagination.current > 1) pagination.current -= 1
        } else {
          pagination.current = 1; clearSelectionState()
        }
        const deleted = nonnegative(res.data?.deleted)
        message.success(deleted === null ? '删除请求已完成' : '已删除 ' + deleted + ' 条 OCR 缓存')
        reloadAfter = true
      } catch (error) {
        if (!ownsFailure()) return
        errorMessage.value = '删除 OCR 缓存请求未完成，请重新读取核对。'; failureOwner = ownsFailure; failure.value = projectFailure(error)
      } finally {
        if (pendingPrompt === prompt) pendingPrompt = null
        if (owns() && ticket === writeVersion) deleting.value = false
      }
      if (reloadAfter && owns() && ticket === writeVersion) await load()
    }
    const deleteSelected = () => runDelete(selectedDeletePayload(), selectedCount.value)
    const deleteSingle = record => {
      if (!record?.urlHash || !rows.value.includes(record)) return
      return runDelete({ items: [record.urlHash], all: false, except: [] }, 1, record)
    }
    onMounted(() => { mounted = true; if (canManage()) void load() })
    onBeforeUnmount(() => { alive = false; mounted = false; void retireScope() })
    return { operationWarning, selectedCount, selectionSummary, allCurrentPageSelected, allRowsSelected, hasCurrentPageSelection, rowSelection,
      handleSelectCurrentPage, handleClearCurrentPage, handleSelectAll, handleClearSelection, onSelectionChange, deleteSelected, deleteSingle,
      addVisible, addKey, addEntry, adding, addSurface, addForm, addImageURL, addError, addBillingWarning, addRequestId, addRecognizedText,
      openAddDialog, closeAddDialog, addOCRCache, submitAddFromInput, setAddURL,
      imageLoading, imageError, currentImageUrl, loadCurrentImage, handleCurrentImageError, expandedOCRText, toggleOCRText, pageRoot, frameKey, pageEntry, canManage, canAct, fieldId, hasLoaded, failure, failureContext, isCurrentFailure, rows, filters, loading, deleting, configured, matched, errorMessage, detailVisible, detailMounted, detailKey, detailRecord, detailSurface, detailEntry, detailAfterClose, closeDetail, updateDetailOpen, pagination, contentTypes, hasFilters, scopeDescription, columns, formatDate, load, reload, resetFilters, onTableChange, showDetails, setFilter }
  },
})
</script>

<style scoped>
.keyword-input { width: 300px; max-width: 100%; }
.content-type-select { width: 170px; }
.filter-form { row-gap: 16px; }
.status-alert { margin-top: 16px; }
.operation-alert, .add-hint { margin-bottom: 16px; }
.selection-alert { margin-bottom: 20px; }
.selection-alert-content { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.request-id { margin: 12px 0 0; overflow-wrap: anywhere; color: var(--console-muted); }

.provider-status { display: flex; flex-direction: column; align-items: flex-start; gap: 12px; color: var(--console-muted); }
.cache-hint { margin: 0 0 20px; color: var(--console-muted); line-height: 1.7; }
.hash-value { overflow-wrap: anywhere; white-space: normal; }
.url-value { overflow-wrap: anywhere; white-space: normal; }
.response-title { margin: 20px 0 8px; font-size: 14px; }
.current-image-tools { display: grid; justify-items: start; gap: 12px; margin-top: 16px; }
.current-image-preview { display: block; max-width: 100%; max-height: 55vh; object-fit: contain; border: 1px solid var(--console-border); border-radius: var(--console-radius); }
.ocr-text { max-height: 440px; padding: 16px; margin: 0; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--console-text); background: var(--console-subtle); border: 1px solid var(--console-border); border-radius: var(--console-radius); }
@media (max-width: 600px) {
  .keyword-input, .content-type-select { width: 100%; }
  .filter-form :deep(.ant-form-item) { width: 100%; margin-inline-end: 0; }
  .filter-form :deep(.ant-form-item-control) { flex: 1; min-width: 0; }
  .date-filter-space { display: flex; flex-wrap: wrap; }
  .date-filter-space :deep(.ant-space-item) { min-width: 0; }
}
</style>
