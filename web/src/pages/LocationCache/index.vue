<template>
  <div class="location-page">
    <a-card :bordered="false">
      <template #title>{{ pageTitle }}</template>
      <template #extra>
        <a-space>
          <a-button @click="manualVisible = true">手动添加</a-button>
          <a-popconfirm title="删除当前类型的全部归属地缓存？" ok-text="全部删除" cancel-text="取消" @confirm="deleteAll">
            <a-button danger :loading="allDeleting">全部删除</a-button>
          </a-popconfirm>
          <a-button :loading="loading" @click="load"><template #icon><ReloadOutlined /></template>刷新列表</a-button>
        </a-space>
      </template>
      <a-form layout="inline" class="filter-form" @finish="reload">
        <a-form-item label="关键词">
          <a-input-search v-model:value="keywordInput" allow-clear placeholder="查询值 / 归属地" class="keyword-input" @search="reload" />
        </a-form-item>
        <a-form-item><a-button @click="resetFilters">重置筛选</a-button></a-form-item>
      </a-form>
      <a-alert v-if="configured === false" type="warning" show-icon message="未配置 UApiPro 密钥，已保存的信息仍可查看，暂时无法重新查询。" class="status-alert" />
      <a-alert v-if="errorMessage" type="error" show-icon :message="errorMessage" class="status-alert" />
      <a-row :gutter="16" class="summary-row">
        <a-col :xs="12" :sm="6"><a-statistic title="筛选结果" :value="summary.total" suffix="条" /></a-col>
        <a-col :xs="12" :sm="6"><a-statistic title="已有归属地" :value="summary.withAddress" suffix="条" /></a-col>
        <a-col :xs="12" :sm="6"><a-statistic title="待补全" :value="summary.withoutAddress" suffix="条" /></a-col>
        <a-col :xs="12" :sm="6"><a-statistic title="当前选择" :value="selectedCount" suffix="条" /></a-col>
      </a-row>
    </a-card>
    <a-card :bordered="false" class="table-card">
      <a-alert v-if="pagination.total" type="info" show-icon class="selection-alert">
        <template #message>
          <div class="selection-alert-content">
            <span>{{ selectionSummary }}</span>
            <a-button danger size="small" :loading="batchDeleting" :disabled="!selectedCount" @click="deleteSelected">删除所选</a-button>
            <a-button type="link" size="small" :disabled="allCurrentPageSelected" @click="handleSelectCurrentPage">全选本页</a-button>
            <a-button type="link" size="small" :disabled="!hasCurrentPageSelection" @click="handleClearCurrentPage">取消本页全选</a-button>
            <a-button type="link" size="small" :disabled="allRowsSelected" @click="handleSelectAll">全选全部</a-button>
            <a-button type="link" size="small" :disabled="!selectedCount" @click="handleClearSelection">取消全部全选</a-button>
          </div>
        </template>
      </a-alert>
      <a-table :data-source="rows" :columns="columns" :loading="loading" :pagination="pagination" :row-key="recordIdentity" :row-selection="rowSelection" :scroll="{ x: 980 }" @change="onTableChange">
        <template #fetchedAt="{ record }">{{ formatDate(record.fetchedAt) }}</template>
        <template #action="{ record }">
          <a-space>
            <a-button type="link" @click="showDetails(record)">详情</a-button>
            <a-button type="link" :loading="!!refreshing[recordIdentity(record)]" :disabled="isRefreshDisabled(record)" @click="refresh(record)">重新查询</a-button>
            <a-popconfirm title="删除后下次展示会重新查询，确认删除吗？" ok-text="删除" cancel-text="取消" @confirm="remove(record)">
              <a-button type="link" danger :loading="!!removing[recordIdentity(record)]">删除</a-button>
            </a-popconfirm>
          </a-space>
        </template>
      </a-table>
    </a-card>
    <a-modal v-model:visible="detailVisible" :title="`${kindLabel}归属地详情`" :footer="null" width="760px">
      <a-descriptions :column="1" size="small" bordered>
        <a-descriptions-item :label="kindLabel">{{ detailRecord.lookupKey || '-' }}</a-descriptions-item>
        <a-descriptions-item v-if="detailRecord.kind === 'ip'" label="查询模式">{{ sourceLabel(detailRecord.source) }}</a-descriptions-item>
        <a-descriptions-item label="归属地">{{ detailRecord.address || '-' }}</a-descriptions-item>
        <a-descriptions-item label="最近查询时间">{{ formatDate(detailRecord.fetchedAt) }}</a-descriptions-item>
        <a-descriptions-item label="记录更新时间">{{ formatDate(detailRecord.updatedAt) }}</a-descriptions-item>
      </a-descriptions>
      <h4 class="response-title">原始响应</h4>
      <pre class="raw-response">{{ formatRawResponse(detailRecord.rawJson) }}</pre>
    </a-modal>
    <a-modal v-model:visible="manualVisible" :title="`手动添加${kindLabel}归属地`" :confirm-loading="manualSubmitting" ok-text="查询并保存" cancel-text="取消" @ok="manualLookup">
      <a-form layout="vertical">
        <a-form-item :label="kindLabel" required>
          <a-input v-model:value="manualKey" :placeholder="kind === 'phone' ? '请输入 11 位手机号' : '请输入公网 IP 地址'" @press-enter="manualLookup" />
        </a-form-item>
        <a-form-item v-if="kind === 'ip'" label="查询模式">
          <a-select v-model:value="manualSource">
            <a-select-option value="">默认</a-select-option>
            <a-select-option value="commercial">商业增强</a-select-option>
          </a-select>
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script>
import { computed, defineComponent, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import Swal from 'sweetalert2'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { deleteLocationCache, deleteLocationCacheBatch, getLocationCacheList, manualLookupLocationCache, refreshLocationCache } from '@/api/TikuComponentApi'

export default defineComponent({
  name: 'LocationCache',
  components: { ReloadOutlined },
  setup() {
    const route = useRoute()
    const kind = computed(() => route.meta.locationKind === 'phone' ? 'phone' : 'ip')
    const kindLabel = computed(() => kind.value === 'phone' ? '手机号' : 'IP')
    const pageTitle = computed(() => `${kindLabel.value} 归属地信息管理`)
    const rows = ref([])
    const loading = ref(false)
    const configured = ref(undefined)
    const errorMessage = ref('')
    const keywordInput = ref('')
    const keyword = ref('')
    const refreshing = reactive({})
    const removing = reactive({})
    const selectedKeys = ref(new Set())
    const deselectedKeys = ref(new Set())
    const selectAllMode = ref(false)
    const batchDeleting = ref(false)
    const allDeleting = ref(false)
    const manualVisible = ref(false)
    const manualSubmitting = ref(false)
    const manualKey = ref('')
    const manualSource = ref('')
    const summary = ref({ total: 0, withAddress: 0, withoutAddress: 0 })
    const recordIdentity = record => `${record.kind}:${record.lookupKey}:${record.source || 'default'}`
    const sourceLabel = source => source === 'legacy' ? '历史（来源未记录）' : source === 'commercial' ? '商业增强' : '默认'
    const isRefreshDisabled = record => configured.value === false || record.source === 'legacy'
    const detailVisible = ref(false)
    const detailRecord = ref({})
    const pagination = reactive({
      current: 1, pageSize: 20, total: 0, showSizeChanger: true,
      pageSizeOptions: ['10', '20', '50', '100'], showTotal: total => `共 ${total} 条`,
    })
    const currentPageKeys = computed(() => rows.value.map(recordIdentity))
    const selectedRowKeys = computed(() => {
      if (selectAllMode.value) {
        return currentPageKeys.value.filter(key => !deselectedKeys.value.has(key))
      }
      return currentPageKeys.value.filter(key => selectedKeys.value.has(key))
    })
    const selectedCount = computed(() => selectAllMode.value
      ? Math.max(0, pagination.total - deselectedKeys.value.size)
      : selectedKeys.value.size)
    const allRowsSelected = computed(() => selectAllMode.value && deselectedKeys.value.size === 0 && pagination.total > 0)
    const allCurrentPageSelected = computed(() => currentPageKeys.value.length > 0 && selectedRowKeys.value.length === currentPageKeys.value.length)
    const hasCurrentPageSelection = computed(() => selectedRowKeys.value.length > 0)
    const selectionSummary = computed(() => `已选择 ${selectedCount.value} 条，共 ${pagination.total} 条`)
    const rowSelection = computed(() => ({
      selectedRowKeys: selectedRowKeys.value,
      onChange: onSelectionChange,
    }))
    const columns = computed(() => [
      { title: kindLabel.value, dataIndex: 'lookupKey', key: 'lookupKey', width: 210 },
      ...(kind.value === 'ip' ? [{ title: '查询模式', key: 'source', customRender: ({ record }) => sourceLabel(record.source), width: 160 }] : []),
      { title: '归属地', dataIndex: 'address', key: 'address', width: 260 },
      { title: '最近查询时间', dataIndex: 'fetchedAt', key: 'fetchedAt', slots: { customRender: 'fetchedAt' }, width: 200 },
      { title: '操作', key: 'action', slots: { customRender: 'action' }, width: 240 },
    ])
    const formatDate = (value) => {
      const date = value ? new Date(value) : null
      return date && !Number.isNaN(date.getTime())
        ? date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' }) : '-'
    }
    const formatRawResponse = (value) => {
      const raw = String(value || '')
      if (!raw) return '-'
      try { return JSON.stringify(JSON.parse(raw), null, 2) } catch { return raw }
    }
    const requestErrorMessage = (error, fallback) => {
      const data = error?.response?.data
      if (typeof data?.configured === 'boolean') configured.value = data.configured
      return data?.message || error?.message || fallback
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
      if (!pagination.total) return
      selectedKeys.value = new Set()
      deselectedKeys.value = new Set()
      selectAllMode.value = true
    }
    const handleClearSelection = () => clearSelectionState()
    function onSelectionChange(keys) {
      const selected = new Set(Array.isArray(keys) ? keys : [])
      if (selectAllMode.value) {
        const next = new Set(deselectedKeys.value)
        currentPageKeys.value.forEach(key => {
          if (selected.has(key)) next.delete(key)
          else next.add(key)
        })
        deselectedKeys.value = next
        return
      }
      const next = new Set(selectedKeys.value)
      currentPageKeys.value.forEach(key => {
        if (selected.has(key)) next.add(key)
        else next.delete(key)
      })
      selectedKeys.value = next
    }
    const selectionItemFromIdentity = (identity) => {
      const prefix = `${kind.value}:`
      if (!identity.startsWith(prefix)) return null
      const body = identity.slice(prefix.length)
      const separator = body.lastIndexOf(':')
      if (separator <= 0) return null
      const source = body.slice(separator + 1)
      return { lookupKey: body.slice(0, separator), source: source === 'default' ? '' : source }
    }
    const buildBatchDeletePayload = () => {
      if (selectAllMode.value) {
        return {
          kind: kind.value,
          keyword: keyword.value,
          source: '',
          items: [],
          all: true,
          except: [...deselectedKeys.value].map(selectionItemFromIdentity).filter(Boolean),
        }
      }
      return {
        kind: kind.value,
        keyword: keyword.value,
        source: '',
        items: [...selectedKeys.value].map(selectionItemFromIdentity).filter(Boolean),
        all: false,
        except: [],
      }
    }
    const deleteSelected = async () => {
      if (!selectedCount.value || batchDeleting.value) return
      const count = selectedCount.value
      const confirmation = await Swal.fire({
        title: '确认删除所选归属地？',
        text: `将删除 ${count} 条记录，删除后下次展示时会重新查询。`,
        icon: 'warning',
        showCancelButton: true,
        confirmButtonText: '删除',
        cancelButtonText: '取消',
        reverseButtons: true,
      })
      if (!confirmation?.isConfirmed) return
      batchDeleting.value = true
      errorMessage.value = ''
      try {
        const res = await deleteLocationCacheBatch(buildBatchDeletePayload())
        if (res?.status !== 200) throw new Error(res?.message || '批量删除失败')
        const deleted = Number(res?.data?.deleted)
        clearSelectionState()
        message.success(Number.isFinite(deleted) ? `已删除 ${deleted} 条` : '已删除')
        await load()
        if (rows.value.length === 0 && pagination.current > 1) {
          pagination.current--
          await load()
        }
      } catch (error) {
        errorMessage.value = requestErrorMessage(error, '批量删除失败')
      } finally {
        batchDeleting.value = false
      }
    }
    const deleteAll = async () => {
      if (allDeleting.value) return
      allDeleting.value = true
      errorMessage.value = ''
      try {
        const res = await deleteLocationCacheBatch({ kind: kind.value, keyword: '', source: '', items: [], all: true, except: [] })
        if (res?.status !== 200) throw new Error(res?.message || '全部删除失败')
        clearSelectionState()
        pagination.current = 1
        message.success(`已删除 ${Number(res?.data?.deleted) || 0} 条`)
        await load()
      } catch (error) {
        errorMessage.value = requestErrorMessage(error, '全部删除失败')
      } finally {
        allDeleting.value = false
      }
    }
    const manualLookup = async () => {
      const lookupKey = manualKey.value.trim()
      if (!lookupKey || manualSubmitting.value) return
      manualSubmitting.value = true
      errorMessage.value = ''
      try {
        const res = await manualLookupLocationCache({ kind: kind.value, lookupKey, source: kind.value === 'ip' ? manualSource.value : '' })
        if (typeof res?.data?.configured === 'boolean') configured.value = res.data.configured
        if (res?.status !== 200) throw new Error(res?.message || '手动查询失败')
        manualVisible.value = false
        manualKey.value = ''
        manualSource.value = ''
        message.success('查询成功，已保存归属地')
        await load()
      } catch (error) {
        errorMessage.value = `${lookupKey}：${requestErrorMessage(error, '手动查询失败')}`
      } finally {
        manualSubmitting.value = false
      }
    }
    const updateSummary = (data) => {
      const source = data?.summary || data?.statistics || {}
      const total = Number(source.total ?? data?.total ?? 0) || 0
      const withAddress = Number(source.withAddress ?? source.resolved ?? source.cached ?? rows.value.filter(row => String(row.address || '').trim()).length) || 0
      const withoutAddress = Number(source.withoutAddress ?? source.unresolved ?? Math.max(0, total - withAddress)) || 0
      summary.value = { total, withAddress, withoutAddress }
    }
    let loadGeneration = 0
    const load = async () => {
      const generation = ++loadGeneration
      loading.value = true
      errorMessage.value = ''
      try {
        const res = await getLocationCacheList({ kind: kind.value, keyword: keyword.value, page: pagination.current, pageSize: pagination.pageSize })
        if (generation !== loadGeneration) return
        if (res?.status !== 200) throw new Error(res?.message || '获取归属地信息失败')
        const data = res.data || {}
        rows.value = Array.isArray(data.data) ? data.data : []
        pagination.total = Number(data.total || 0)
        pagination.current = Number(data.page || pagination.current)
        pagination.pageSize = Number(data.pageSize || pagination.pageSize)
        configured.value = typeof data.configured === 'boolean' ? data.configured : undefined
        updateSummary(data)
      } catch (error) {
        if (generation === loadGeneration) errorMessage.value = requestErrorMessage(error, '获取归属地信息失败')
      } finally {
        if (generation === loadGeneration) loading.value = false
      }
    }
    const reload = () => { clearSelectionState(); keyword.value = keywordInput.value.trim(); pagination.current = 1; load() }
    const resetFilters = () => { keywordInput.value = ''; reload() }
    const onTableChange = (page) => { pagination.current = page.current; pagination.pageSize = page.pageSize; load() }
    const showDetails = (record) => { detailRecord.value = record; detailVisible.value = true }
    const refresh = async (record) => {
      if (isRefreshDisabled(record)) return
      const identity = recordIdentity(record)
      refreshing[identity] = true
      errorMessage.value = ''
      try {
        const res = await refreshLocationCache({ kind: record.kind, lookupKey: record.lookupKey, source: record.source || '' })
        if (typeof res?.data?.configured === 'boolean') configured.value = res.data.configured
        if (res?.status !== 200) throw new Error(res?.message || '重新查询失败')
        message.success('归属地已更新')
        await load()
      } catch (error) {
        errorMessage.value = `${record.lookupKey}：${requestErrorMessage(error, '重新查询失败')}`
      } finally { refreshing[identity] = false }
    }
    const remove = async (record) => {
      const identity = recordIdentity(record)
      removing[identity] = true
      errorMessage.value = ''
      try {
        const res = await deleteLocationCache({ kind: record.kind, lookupKey: record.lookupKey, source: record.source || '' })
        if (res?.status !== 200) throw new Error(res?.message || '删除失败')
        if (rows.value.length === 1 && pagination.current > 1) pagination.current--
        if (selectAllMode.value) {
          const next = new Set(deselectedKeys.value)
          next.delete(identity)
          deselectedKeys.value = next
        } else {
          const next = new Set(selectedKeys.value)
          next.delete(identity)
          selectedKeys.value = next
        }
        message.success('已删除')
        await load()
      } catch (error) {
        errorMessage.value = `${record.lookupKey}：${requestErrorMessage(error, '删除失败')}`
      } finally { removing[identity] = false }
    }
    watch(kind, () => {
      rows.value = []
      configured.value = undefined
      keywordInput.value = ''
      keyword.value = ''
      pagination.current = 1
      clearSelectionState()
      summary.value = { total: 0, withAddress: 0, withoutAddress: 0 }
      detailVisible.value = false
      load()
    }, { immediate: true })
    return { rows, loading, configured, errorMessage, keywordInput, pagination, columns, formatDate, formatRawResponse, reload, resetFilters, load, onTableChange, remove, refresh, refreshing, removing, recordIdentity, sourceLabel, isRefreshDisabled, detailVisible, detailRecord, showDetails, kind, kindLabel, pageTitle, summary, selectedCount, selectedRowKeys, allRowsSelected, allCurrentPageSelected, hasCurrentPageSelection, selectionSummary, rowSelection, handleSelectCurrentPage, handleClearCurrentPage, handleSelectAll, handleClearSelection, batchDeleting, deleteSelected, allDeleting, deleteAll, manualVisible, manualSubmitting, manualKey, manualSource, manualLookup }
  },
})
</script>

<style scoped>
.location-page { display: flex; flex-direction: column; gap: 16px; }
.filter-form { gap: 12px; }
.keyword-input { width: 260px; max-width: 100%; }
.status-alert { margin-top: 16px; }
.summary-row { margin-top: 18px; }
.selection-alert { margin-bottom: 16px; }
.selection-alert-content { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 8px; }
.table-card { min-height: 420px; }
.response-title { margin: 20px 0 8px; font-size: 14px; }
.raw-response { max-height: 420px; padding: 12px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; background: #f5f7f8; border: 1px solid #e5e7eb; border-radius: 4px; }
@media (max-width: 600px) { .keyword-input { width: 100%; } .filter-form :deep(.ant-form-item) { width: 100%; margin-right: 0; } }
</style>
