<template>
  <div class="console-page ocr-cache-page">
    <ConsolePageHeading title="OCR 缓存管理" description="查看和维护已保存的图片识别结果。">
      <template #actions>
        <a-space wrap>
          <a-button type="primary" :disabled="deleting || adding" @click="openAddDialog">手动添加</a-button>
          <a-button :loading="loading" :disabled="deleting || adding" @click="load">刷新列表</a-button>
        </a-space>
      </template>
    </ConsolePageHeading>
    <ConsoleSummaryGrid>
      <a-statistic title="筛选结果" :value="matched" suffix="条" />
      <a-statistic title="当前页" :value="rows.length" suffix="条" />
      <a-statistic title="当前选择" :value="selectedCount" suffix="条" />
      <div class="provider-status"><span>识别服务</span><a-tag :color="configured ? 'green' : 'default'">{{ configured === undefined ? (loading ? '加载中' : '状态未知') : configured ? 'UAPI 已配置' : '未配置有效 UAPI 密钥' }}</a-tag></div>
    </ConsoleSummaryGrid>
    <a-alert v-if="operationWarning" type="warning" show-icon :message="operationWarning" class="operation-alert" />
    <ConsolePanel title="筛选缓存" description="关键词匹配识别文本、图片 URL、URL 哈希、图片哈希或图片类型；更新时间按上海时间筛选，包含结束日期当天。">
      <a-form :model="filters" layout="inline" class="filter-form" @finish="reload">
        <a-form-item label="关键词">
          <a-input v-model:value="filters.keyword" allow-clear placeholder="识别文本 / 图片 URL / URL 哈希 / 图片哈希" class="keyword-input" :disabled="deleting" />
        </a-form-item>
        <a-form-item label="图片类型">
          <a-select v-model:value="filters.contentType" class="content-type-select" :disabled="deleting">
            <a-select-option value="">全部类型</a-select-option>
            <a-select-option v-for="type in contentTypes" :key="type" :value="type">{{ type }}</a-select-option>
          </a-select>
        </a-form-item>
        <a-form-item label="更新时间">
          <a-space class="date-filter-space">
            <a-input v-model:value="filters.updatedFrom" type="date" aria-label="更新时间起始日期" :disabled="deleting" />
            <span>至</span>
            <a-input v-model:value="filters.updatedTo" type="date" aria-label="更新时间结束日期" :disabled="deleting" />
          </a-space>
        </a-form-item>
        <a-form-item>
          <a-space wrap>
            <a-button type="primary" html-type="submit" :loading="loading" :disabled="deleting">查询</a-button>
            <a-button :disabled="deleting" @click="resetFilters">重置筛选</a-button>
          </a-space>
        </a-form-item>
      </a-form>
      <a-alert v-if="errorMessage" type="error" show-icon :message="errorMessage" class="status-alert" />
    </ConsolePanel>
    <ConsolePanel title="识别结果" :description="scopeDescription">
      <a-alert v-if="pagination.total" type="info" show-icon class="selection-alert">
        <template #message>
          <div class="selection-alert-content">
            <span>{{ selectionSummary }}</span>
            <a-button danger size="small" :loading="deleting" :disabled="!selectedCount" @click="deleteSelected">删除所选</a-button>
            <a-button type="link" size="small" :disabled="allCurrentPageSelected" @click="handleSelectCurrentPage">全选本页</a-button>
            <a-button type="link" size="small" :disabled="!hasCurrentPageSelection" @click="handleClearCurrentPage">取消本页全选</a-button>
            <a-button type="link" size="small" :disabled="allRowsSelected" @click="handleSelectAll">全选全部</a-button>
            <a-button type="link" size="small" :disabled="!selectedCount" @click="handleClearSelection">取消全部全选</a-button>
          </div>
        </template>
      </a-alert>
      <!-- <p class="cache-hint">保存识别文本、完整图片 URL 与哈希，不保存原始图片。命中缓存仍按现有搜题计费口径处理；删除不会取消正在执行的请求，旧请求的结果不会重新写回缓存。</p> -->
      <ConsoleTableShell label="OCR 缓存记录">
        <a-table table-layout="fixed" :data-source="rows" :columns="columns" :loading="loading" :pagination="pagination" row-key="urlHash" :row-selection="rowSelection" :scroll="{ x: sumTableColumnWidths(columns, 48) }" :locale="{ emptyText: errorMessage ? '列表加载失败，请重试' : '暂无符合条件的 OCR 缓存' }" @change="onTableChange">
          <template #bodyCell="{ column, record }">
            <span v-if="column.key === 'id'">{{ record.id ?? '-' }}</span>
            <OverflowTableText v-else-if="column.key === 'ocrText'" :text="record.ocrText || '-'" :expanded="expandedOCRText.has(record.urlHash)" @toggle="toggleOCRText(record.urlHash)" />
            <span v-else-if="column.key === 'url'" class="url-value">{{ record.url || '历史记录未保存 URL' }}</span>
            <code v-else-if="column.key === 'urlHash' || column.key === 'imageSha256'" class="hash-value">{{ record[column.key] || '-' }}</code>
            <span v-else-if="column.key === 'updatedAt'">{{ formatDate(record.updatedAt) }}</span>
            <a-space v-else-if="column.key === 'action'" wrap>
              <a-button type="link" @click="showDetails(record)">查看全文</a-button>
              <a-popconfirm title="删除这条 OCR 缓存？" ok-text="删除" cancel-text="取消" @confirm="deleteSingle(record)">
                <a-button type="link" danger :disabled="deleting">删除</a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </a-table>
      </ConsoleTableShell>
    </ConsolePanel>
    <a-modal :open="addVisible" title="手动添加 OCR 缓存" ok-text="识别并添加" cancel-text="取消" :confirm-loading="adding" :closable="!adding" :mask-closable="!adding" :keyboard="!adding" :cancel-button-props="{ disabled: adding }" @ok="addOCRCache" @cancel="closeAddDialog">
      <a-alert type="info" show-icon message="填写可直接访问的图片 URL，系统会自动识别并保存缓存；相同图片可复用已有结果，调用按现有 OCR 价格计入当前管理员账单。" class="add-hint" />
      <a-form :model="addForm" layout="vertical" @finish="addOCRCache">
        <a-form-item label="图片 URL" name="url" required>
          <a-input v-model:value="addImageURL" type="url" :disabled="adding" placeholder="https://example.com/image.png" @press-enter.prevent="addOCRCache" />
        </a-form-item>
      </a-form>
      <a-alert v-if="addError" type="error" show-icon :message="addError" class="status-alert" />
      <a-alert v-if="addBillingWarning" type="warning" show-icon :message="addBillingWarning" class="status-alert" />
      <p v-if="addRequestId" class="request-id">请求编号：{{ addRequestId }}</p>
      <template v-if="addRecognizedText">
        <h3 class="response-title">识别结果</h3>
        <pre class="ocr-text">{{ addRecognizedText }}</pre>
      </template>
    </a-modal>
    <a-modal :open="detailVisible" title="OCR 缓存详情" :footer="null" width="760px" @update:open="onDetailVisibilityChange">
      <a-descriptions :column="1" size="small" bordered>
        <a-descriptions-item label="图片 URL"><span class="url-value">{{ detailRecord.url || '历史记录未保存 URL' }}</span></a-descriptions-item>
        <a-descriptions-item label="URL 哈希"><code class="hash-value">{{ detailRecord.urlHash }}</code></a-descriptions-item>
        <a-descriptions-item label="图片 SHA-256"><code class="hash-value">{{ detailRecord.imageSha256 }}</code></a-descriptions-item>
        <a-descriptions-item label="图片类型">{{ detailRecord.contentType || '-' }}</a-descriptions-item>
        <a-descriptions-item label="更新时间（上海）">{{ formatDate(detailRecord.updatedAt) }}</a-descriptions-item>
      </a-descriptions>
      <div class="current-image-tools">
        <a-button type="primary" :loading="imageLoading" :disabled="!detailRecord.url" @click="loadCurrentImage">获取当前图片</a-button>
        <a-alert v-if="imageError" type="error" show-icon :message="imageError" />
        <a-spin v-if="imageLoading" tip="正在获取图片" />
        <img v-if="currentImageUrl" :src="currentImageUrl" alt="缓存图片预览" class="current-image-preview" @error="handleCurrentImageError" />
      </div>
      <h3 class="response-title">识别全文</h3>
      <pre class="ocr-text">{{ detailRecord.ocrText || '-' }}</pre>
    </a-modal>
  </div>
</template>

<script>
import { computed, defineComponent, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { Descriptions, message } from 'ant-design-vue'
import Swal from 'sweetalert2'
import { getAIOCRCacheEntries, getAIOCRCacheImage, deleteAIOCRCacheBatch, testAIToolCall } from '@/api/TikuComponentApi'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'
import OverflowTableText from '@/components/OverflowTableText.vue'
import { buildAIToolTestPayload } from '@/utils/aiSearchConfigHelpers.mjs'

const emptyFilters = () => ({ keyword: '', contentType: '', updatedFrom: '', updatedTo: '' })
const requestError = (error, fallback) => error?.response?.data?.message || error?.message || fallback

export default defineComponent({
  name: 'OCRCache',
  components: { OverflowTableText, ADescriptions: Descriptions, ADescriptionsItem: Descriptions.Item },
  methods: { sumTableColumnWidths },
  setup() {
    const rows = ref([])
    const expandedOCRText = ref(new Set())
    const toggleOCRText = key => {
      const next = new Set(expandedOCRText.value)
      next.has(key) ? next.delete(key) : next.add(key)
      expandedOCRText.value = next
    }
    const filters = reactive(emptyFilters())
    const appliedFilters = ref({})
    const loading = ref(false)
    const deleting = ref(false)
    const addVisible = ref(false)
    const adding = ref(false)
    const addImageURL = ref('')
    const addForm = reactive({ url: addImageURL })
    const addError = ref('')
    const addRequestId = ref('')
    const addRecognizedText = ref('')
    const addBillingWarning = ref('')
    const operationWarning = ref('')
    const configured = ref(undefined)
    const matched = ref(0)
    const errorMessage = ref('')
    const detailVisible = ref(false)
    const detailRecord = ref({})
    const imageLoading = ref(false)
    const imageError = ref('')
    const currentImageUrl = ref('')
    const pagination = reactive({ current: 1, pageSize: 20, total: 0, showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'] })
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
    const rowSelection = computed(() => ({ columnWidth: 48, selectedRowKeys: selectedRowKeys.value, onChange: onSelectionChange }))
    const formatDate = value => {
      const date = value ? new Date(value) : null
      return date && Number.isFinite(date.getTime()) ? date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' }) : '-'
    }
    let loadGeneration = 0
    let imageGeneration = 0
    let addGeneration = 0
    const releaseCurrentImage = () => {
      if (currentImageUrl.value) URL.revokeObjectURL(currentImageUrl.value)
      currentImageUrl.value = ''
    }
    const clearImageState = () => {
      imageGeneration++
      releaseCurrentImage()
      imageLoading.value = false
      imageError.value = ''
    }
    const load = async () => {
      if (deleting.value) return
      const generation = ++loadGeneration
      loading.value = true
      errorMessage.value = ''
      try {
        const res = await getAIOCRCacheEntries({ ...appliedFilters.value, page: pagination.current, pageSize: pagination.pageSize })
        if (generation !== loadGeneration) return
        if (res?.status !== 200) throw new Error(res?.message || '获取 OCR 缓存失败')
        const data = res.data || {}
        const total = Number(data.total || 0)
        const lastPage = Math.max(1, Math.ceil(total / pagination.pageSize))
        if (pagination.current > lastPage) { pagination.current = lastPage; return await load() }
        rows.value = Array.isArray(data.data) ? data.data : []
        matched.value = Number(data.summary?.matched ?? total)
        pagination.total = total
        pagination.current = Number(data.page || pagination.current)
        pagination.pageSize = Number(data.pageSize || pagination.pageSize)
        configured.value = data.configured
      } catch (error) {
        if (generation === loadGeneration) errorMessage.value = requestError(error, '获取 OCR 缓存失败')
      } finally {
        if (generation === loadGeneration) loading.value = false
      }
    }
    const reload = () => {
      if (deleting.value) return
      if (filters.updatedFrom && filters.updatedTo && filters.updatedFrom > filters.updatedTo) {
        errorMessage.value = '起始日期不能晚于结束日期'
        return
      }
      appliedFilters.value = Object.fromEntries(Object.entries(filters).map(([key, value]) => [key, String(value || '').trim()]).filter(([, value]) => value))
      clearSelectionState()
      pagination.current = 1
      rows.value = []
      matched.value = 0
      pagination.total = 0
      return load()
    }
    const resetFilters = () => { if (!deleting.value) { Object.assign(filters, emptyFilters()); return reload() } }
    const onTableChange = page => {
      if (deleting.value) return
      pagination.current = page.pageSize === pagination.pageSize ? page.current : 1
      pagination.pageSize = page.pageSize
      return load()
    }
    const showDetails = record => {
      clearImageState()
      detailRecord.value = record
      detailVisible.value = true
    }
    const closeDetails = () => {
      detailVisible.value = false
      clearImageState()
    }
    const onDetailVisibilityChange = visible => {
      if (visible) detailVisible.value = true
      else closeDetails()
    }
    const loadCurrentImage = async () => {
      clearImageState()
      const generation = ++imageGeneration
      const record = detailRecord.value
      if (!record?.url) {
        imageError.value = '该历史缓存记录没有保存图片 URL'
        return
      }
      if (!record?.urlHash) {
        imageError.value = '该缓存记录缺少 URL 哈希，无法获取图片'
        return
      }
      imageLoading.value = true
      try {
        const res = await getAIOCRCacheImage(record.urlHash)
        if (generation !== imageGeneration || !detailVisible.value || detailRecord.value.urlHash !== record.urlHash) return
        if (res?.status !== 200) {
          let messageText = res?.message
          if (!messageText && typeof res?.data?.text === 'function') {
            try { messageText = JSON.parse(await res.data.text())?.message } catch { /* Keep the fallback for non-JSON error responses. */ }
          }
          throw new Error(messageText || '获取当前图片失败')
        }
        const blob = res.data
        if (!(blob instanceof Blob) || !blob.type.startsWith('image/')) throw new Error('图片服务未返回有效图片')
        currentImageUrl.value = URL.createObjectURL(blob)
      } catch (error) {
        if (generation === imageGeneration) imageError.value = requestError(error, '获取当前图片失败')
      } finally {
        if (generation === imageGeneration) imageLoading.value = false
      }
    }
    const handleCurrentImageError = () => {
      releaseCurrentImage()
      imageError.value = '图片显示失败'
    }
    const resetAddResult = () => {
      addError.value = ''
      addRequestId.value = ''
      addRecognizedText.value = ''
      addBillingWarning.value = ''
    }
    const openAddDialog = () => {
      if (adding.value || deleting.value) return
      addGeneration++
      addImageURL.value = ''
      resetAddResult()
      addVisible.value = true
    }
    const closeAddDialog = () => {
      if (adding.value) return
      addGeneration++
      addVisible.value = false
    }
    const addOCRCache = async () => {
      if (adding.value || deleting.value || !addVisible.value) return
      let payload
      try {
        const { mode, tool, url } = buildAIToolTestPayload({ mode: 'direct', tool: 'image_ocr', url: addImageURL.value })
        payload = { mode, tool, url }
      } catch (error) {
        addError.value = requestError(error, '请填写有效的图片 URL')
        return
      }
      const generation = ++addGeneration
      resetAddResult()
      adding.value = true
      try {
        const res = await testAIToolCall(payload)
        if (generation !== addGeneration) return
        const data = res?.data || {}
        addRequestId.value = data.requestId || ''
        addBillingWarning.value = data.billingWarning || ''
        addRecognizedText.value = Array.isArray(data.toolResult) ? (data.toolResult.find(item => item?.tool === 'image_ocr')?.text || '') : ''
        if (res?.status !== 200 || !data.success) throw new Error(data.message || res?.message || '图片识别失败')
        if (!data.ocrCacheEntry?.urlHash) throw new Error(data.ocrCacheWarning || '识别已完成，但未确认缓存保存成功。请先刷新列表核对，勿重复提交。')
        operationWarning.value = addBillingWarning.value ? `${addBillingWarning.value}${addRequestId.value ? ` 请求编号：${addRequestId.value}` : ''}` : ''
        addVisible.value = false
        addImageURL.value = ''
        // The diagnostic response redacts URL tokens; keep the administrator's
        // original input for this preview while all cache lookups use urlHash.
        showDetails({ ...data.ocrCacheEntry, url: payload.url })
        message.success('OCR 缓存已添加')
        pagination.current = 1
        await load()
      } catch (error) {
        if (generation === addGeneration) addError.value = requestError(error, '图片识别请求失败，请先刷新列表核对后再试')
      } finally {
        if (generation === addGeneration) adding.value = false
      }
    }
    const clearSelectionState = () => {
      selectedKeys.value = new Set()
      deselectedKeys.value = new Set()
      selectAllMode.value = false
    }
    const handleSelectCurrentPage = () => {
      if (!currentPageKeys.value.length) return
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
      if (!currentPageKeys.value.length) return
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
      if (!matched.value) return
      selectedKeys.value = new Set()
      deselectedKeys.value = new Set()
      selectAllMode.value = true
    }
    const handleClearSelection = () => clearSelectionState()
    function onSelectionChange(keys) {
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
    const deleteSelected = async () => {
      if (loading.value || adding.value || deleting.value || errorMessage.value || !selectedCount.value) return
      try {
        const result = await Swal.fire({
          title: '确认删除所选 OCR 缓存？',
          text: `将删除 ${selectedCount.value} 条已选择的缓存，正在执行的请求不会被取消。`,
          icon: 'warning', showCancelButton: true, confirmButtonText: '确认删除', cancelButtonText: '取消',
        })
        if (!result.isConfirmed) return
        deleting.value = true
        const res = await deleteAIOCRCacheBatch(selectedDeletePayload())
        if (res?.status !== 200) throw new Error(res?.message || '删除 OCR 缓存失败')
        closeDetails()
        message.success(`已删除 ${Number(res.data?.deleted || 0)} 条 OCR 缓存`)
        pagination.current = 1
        clearSelectionState()
        await load()
      } catch (error) {
        errorMessage.value = requestError(error, '删除 OCR 缓存失败')
      } finally { deleting.value = false }
    }
    const deleteSingle = async record => {
      if (!record?.urlHash || deleting.value) return
      deleting.value = true
      try {
        const res = await deleteAIOCRCacheBatch({ items: [record.urlHash], all: false, except: [] })
        if (res?.status !== 200) throw new Error(res?.message || '删除 OCR 缓存失败')
        if (detailRecord.value.urlHash === record.urlHash) closeDetails()
        if (selectAllMode.value) {
          const next = new Set(deselectedKeys.value)
          next.delete(record.urlHash)
          deselectedKeys.value = next
        } else {
          const next = new Set(selectedKeys.value)
          next.delete(record.urlHash)
          selectedKeys.value = next
        }
        message.success('已删除 1 条 OCR 缓存')
        if (rows.value.length === 1 && pagination.current > 1) pagination.current--
        await load()
      } catch (error) {
        errorMessage.value = requestError(error, '删除 OCR 缓存失败')
      } finally { deleting.value = false }
    }
    onMounted(load)
    onBeforeUnmount(() => { loadGeneration++; addGeneration++; clearImageState() })
    return { rows, expandedOCRText, toggleOCRText, filters, loading, deleting, addVisible, adding, addImageURL, addForm, addError, addRequestId, addRecognizedText, addBillingWarning, operationWarning, configured, matched, errorMessage, detailVisible, detailRecord, imageLoading, imageError, currentImageUrl, pagination, contentTypes, hasFilters, scopeDescription, columns, formatDate, load, reload, resetFilters, onTableChange, showDetails, closeDetails, onDetailVisibilityChange, loadCurrentImage, handleCurrentImageError, openAddDialog, closeAddDialog, addOCRCache, deleteSingle, deleteSelected, selectedCount, selectionSummary, selectedRowKeys, allRowsSelected, allCurrentPageSelected, hasCurrentPageSelection, rowSelection, onSelectionChange, handleSelectCurrentPage, handleClearCurrentPage, handleSelectAll, handleClearSelection }
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
