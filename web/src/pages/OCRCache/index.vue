<template>
  <div class="console-page ocr-cache-page">
    <ConsolePageHeading title="OCR 缓存管理" description="查看和维护已保存的图片识别结果。">
      <template #actions>
        <a-button :loading="loading" :disabled="clearing" @click="load">刷新列表</a-button>
      </template>
    </ConsolePageHeading>
    <ConsoleSummaryGrid>
      <a-statistic title="筛选结果" :value="matched" suffix="条" />
      <a-statistic title="当前页" :value="rows.length" suffix="条" />
      <div class="provider-status"><span>识别服务</span><a-tag :color="configured ? 'green' : 'default'">{{ configured === undefined ? (loading ? '加载中' : '状态未知') : configured ? 'UAPI 已配置' : '未配置有效 UAPI 密钥' }}</a-tag></div>
    </ConsoleSummaryGrid>
    <ConsolePanel title="筛选缓存" description="关键词匹配识别文本、URL 哈希、图片哈希或图片类型；更新时间按上海时间筛选，包含结束日期当天。">
      <a-form :model="filters" layout="inline" class="filter-form" @finish="reload">
        <a-form-item label="关键词">
          <a-input v-model:value="filters.keyword" allow-clear placeholder="识别文本 / URL 哈希 / 图片哈希" class="keyword-input" :disabled="clearing" />
        </a-form-item>
        <a-form-item label="图片类型">
          <a-select v-model:value="filters.contentType" class="content-type-select" :disabled="clearing">
            <a-select-option value="">全部类型</a-select-option>
            <a-select-option v-for="type in contentTypes" :key="type" :value="type">{{ type }}</a-select-option>
          </a-select>
        </a-form-item>
        <a-form-item label="更新时间">
          <a-space class="date-filter-space">
            <a-input v-model:value="filters.updatedFrom" type="date" aria-label="更新时间起始日期" :disabled="clearing" />
            <span>至</span>
            <a-input v-model:value="filters.updatedTo" type="date" aria-label="更新时间结束日期" :disabled="clearing" />
          </a-space>
        </a-form-item>
        <a-form-item>
          <a-space wrap>
            <a-button type="primary" html-type="submit" :loading="loading" :disabled="clearing">查询</a-button>
            <a-button :disabled="clearing" @click="resetFilters">重置筛选</a-button>
          </a-space>
        </a-form-item>
      </a-form>
      <a-alert v-if="errorMessage" type="error" show-icon :message="errorMessage" class="status-alert" />
    </ConsolePanel>
    <ConsolePanel title="识别结果" :description="scopeDescription">
      <template #actions>
        <a-button danger :loading="clearing" :disabled="loading || !!errorMessage || !matched" @click="clearMatching">{{ hasFilters ? '清空筛选结果' : '清空全部缓存' }}</a-button>
      </template>
      <p class="cache-hint">仅保存识别文本与哈希，不保存原始图片或网址。命中缓存仍按现有搜题计费口径处理；清空不会取消正在执行的请求，旧请求的结果不会重新写回缓存。</p>
      <ConsoleTableShell label="OCR 缓存记录">
        <a-table table-layout="fixed" :data-source="rows" :columns="columns" :loading="loading" :pagination="pagination" row-key="urlHash" :scroll="{ x: sumTableColumnWidths(columns) }" :locale="{ emptyText: errorMessage ? '列表加载失败，请重试' : '暂无符合条件的 OCR 缓存' }" @change="onTableChange">
          <template #bodyCell="{ column, record }">
            <span v-if="column.key === 'ocrText'" class="text-preview">{{ record.ocrText || '-' }}</span>
            <code v-else-if="column.key === 'urlHash' || column.key === 'imageSha256'" class="hash-value">{{ record[column.key] || '-' }}</code>
            <span v-else-if="column.key === 'updatedAt'">{{ formatDate(record.updatedAt) }}</span>
            <a-button v-else-if="column.key === 'action'" type="link" @click="showDetails(record)">查看全文</a-button>
          </template>
        </a-table>
      </ConsoleTableShell>
    </ConsolePanel>
    <a-modal v-model:open="detailVisible" title="OCR 缓存详情" :footer="null" width="760px">
      <a-descriptions :column="1" size="small" bordered>
        <a-descriptions-item label="URL 哈希"><code class="hash-value">{{ detailRecord.urlHash }}</code></a-descriptions-item>
        <a-descriptions-item label="图片 SHA-256"><code class="hash-value">{{ detailRecord.imageSha256 }}</code></a-descriptions-item>
        <a-descriptions-item label="图片类型">{{ detailRecord.contentType || '-' }}</a-descriptions-item>
        <a-descriptions-item label="更新时间（上海）">{{ formatDate(detailRecord.updatedAt) }}</a-descriptions-item>
      </a-descriptions>
      <h3 class="response-title">识别全文</h3>
      <pre class="ocr-text">{{ detailRecord.ocrText || '-' }}</pre>
    </a-modal>
  </div>
</template>

<script>
import { computed, defineComponent, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { Descriptions, message } from 'ant-design-vue'
import Swal from 'sweetalert2'
import { getAIOCRCacheEntries, clearAIOCRCache } from '@/api/TikuComponentApi'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

const emptyFilters = () => ({ keyword: '', contentType: '', updatedFrom: '', updatedTo: '' })
const requestError = (error, fallback) => error?.response?.data?.message || error?.message || fallback

export default defineComponent({
  name: 'OCRCache',
  components: { ADescriptions: Descriptions, ADescriptionsItem: Descriptions.Item },
  methods: { sumTableColumnWidths },
  setup() {
    const rows = ref([])
    const filters = reactive(emptyFilters())
    const appliedFilters = ref({})
    const loading = ref(false)
    const clearing = ref(false)
    const configured = ref(undefined)
    const matched = ref(0)
    const errorMessage = ref('')
    const detailVisible = ref(false)
    const detailRecord = ref({})
    const pagination = reactive({ current: 1, pageSize: 20, total: 0, showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'] })
    const contentTypes = computed(() => [...new Set(['image/png', 'image/jpeg', 'image/webp', 'image/gif', 'image/bmp', 'image/tiff', ...rows.value.map(row => row.contentType), filters.contentType].filter(Boolean))])
    const hasFilters = computed(() => Object.keys(appliedFilters.value).length > 0)
    const scopeDescription = computed(() => {
      const scope = appliedFilters.value
      return [scope.keyword && `关键词：${scope.keyword}`, scope.contentType && `类型：${scope.contentType}`, scope.updatedFrom && `起始：${scope.updatedFrom}`, scope.updatedTo && `结束：${scope.updatedTo}`].filter(Boolean).join('；') || '当前范围：全部 OCR 缓存。'
    })
    const columns = [
      { title: '识别文本', dataIndex: 'ocrText', key: 'ocrText', width: 320 },
      { title: '图片类型', dataIndex: 'contentType', key: 'contentType', width: 140 },
      { title: 'URL 哈希', dataIndex: 'urlHash', key: 'urlHash', width: 210 },
      { title: '图片 SHA-256', dataIndex: 'imageSha256', key: 'imageSha256', width: 210 },
      { title: '更新时间（上海）', dataIndex: 'updatedAt', key: 'updatedAt', width: 180 },
      { title: '操作', key: 'action', width: 100 },
    ]
    const formatDate = value => {
      const date = value ? new Date(value) : null
      return date && Number.isFinite(date.getTime()) ? date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' }) : '-'
    }
    let loadGeneration = 0
    const load = async () => {
      if (clearing.value) return
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
      if (clearing.value) return
      if (filters.updatedFrom && filters.updatedTo && filters.updatedFrom > filters.updatedTo) {
        errorMessage.value = '起始日期不能晚于结束日期'
        return
      }
      appliedFilters.value = Object.fromEntries(Object.entries(filters).map(([key, value]) => [key, String(value || '').trim()]).filter(([, value]) => value))
      pagination.current = 1
      rows.value = []
      matched.value = 0
      pagination.total = 0
      return load()
    }
    const resetFilters = () => { if (!clearing.value) { Object.assign(filters, emptyFilters()); return reload() } }
    const onTableChange = page => {
      if (clearing.value) return
      pagination.current = page.pageSize === pagination.pageSize ? page.current : 1
      pagination.pageSize = page.pageSize
      return load()
    }
    const showDetails = record => { detailRecord.value = record; detailVisible.value = true }
    const clearMatching = async () => {
      if (loading.value || clearing.value || errorMessage.value || !matched.value) return
      const scope = { ...appliedFilters.value }
      const filtered = Object.keys(scope).length > 0
      clearing.value = true
      try {
        const result = await Swal.fire({
          title: filtered ? '确认清空筛选结果？' : '确认清空全部 OCR 缓存？',
          text: `${scopeDescription.value} 将删除当前范围内的 ${matched.value} 条缓存，包含其他分页。正在执行的请求不会被取消。`,
          icon: 'warning', showCancelButton: true, confirmButtonText: '确认清空', cancelButtonText: '取消',
        })
        if (!result.isConfirmed) return
        const res = await clearAIOCRCache(scope)
        if (res?.status !== 200) throw new Error(res?.message || '清空 OCR 缓存失败')
        detailVisible.value = false
        message.success(`已清空 ${Number(res.data?.deleted || 0)} 条 OCR 缓存`)
        pagination.current = 1
        clearing.value = false
        await load()
      } catch (error) {
        errorMessage.value = requestError(error, '清空 OCR 缓存失败')
      } finally { clearing.value = false }
    }
    onMounted(load)
    onBeforeUnmount(() => { loadGeneration++ })
    return { rows, filters, loading, clearing, configured, matched, errorMessage, detailVisible, detailRecord, pagination, contentTypes, hasFilters, scopeDescription, columns, formatDate, load, reload, resetFilters, onTableChange, showDetails, clearMatching }
  },
})
</script>

<style scoped>
.keyword-input { width: 300px; max-width: 100%; }
.content-type-select { width: 170px; }
.filter-form { row-gap: 16px; }
.status-alert { margin-top: 16px; }
.provider-status { display: flex; flex-direction: column; align-items: flex-start; gap: 12px; color: var(--console-muted); }
.cache-hint { margin: 0 0 20px; color: var(--console-muted); line-height: 1.7; }
.hash-value { overflow-wrap: anywhere; white-space: normal; }
.text-preview { display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; white-space: pre-wrap; overflow-wrap: anywhere; }
.response-title { margin: 20px 0 8px; font-size: 14px; }
.ocr-text { max-height: 440px; padding: 16px; margin: 0; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--console-text); background: var(--console-subtle); border: 1px solid var(--console-border); border-radius: var(--console-radius); }
@media (max-width: 600px) {
  .keyword-input, .content-type-select { width: 100%; }
  .filter-form :deep(.ant-form-item) { width: 100%; margin-inline-end: 0; }
  .filter-form :deep(.ant-form-item-control) { flex: 1; min-width: 0; }
  .date-filter-space { display: flex; flex-wrap: wrap; }
  .date-filter-space :deep(.ant-space-item) { min-width: 0; }
}
</style>
