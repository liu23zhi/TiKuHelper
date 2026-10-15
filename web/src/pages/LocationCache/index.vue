<template>
  <div class="location-page">
    <a-card :bordered="false">
      <template #title>{{ pageTitle }}</template>
      <template #extra><a-button :loading="loading" @click="load"><template #icon><ReloadOutlined /></template>刷新列表</a-button></template>
      <a-form layout="inline" class="filter-form" @finish="reload">
        <a-form-item label="关键词">
          <a-input-search v-model:value="keywordInput" allow-clear placeholder="查询值 / 归属地" class="keyword-input" @search="reload" />
        </a-form-item>
        <a-form-item><a-button @click="resetFilters">重置筛选</a-button></a-form-item>
      </a-form>
      <a-alert v-if="configured === false" type="warning" show-icon message="未配置 UApiPro 密钥，已保存的信息仍可查看，暂时无法重新查询。" class="status-alert" />
      <a-alert v-if="errorMessage" type="error" show-icon :message="errorMessage" class="status-alert" />
    </a-card>
    <a-card :bordered="false" class="table-card">
      <a-table :data-source="rows" :columns="columns" :loading="loading" :pagination="pagination" :row-key="recordIdentity" :scroll="{ x: 980 }" @change="onTableChange">
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
  </div>
</template>

<script>
import { computed, defineComponent, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { deleteLocationCache, getLocationCacheList, refreshLocationCache } from '@/api/TikuComponentApi'

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
    const recordIdentity = record => `${record.kind}:${record.lookupKey}:${record.source || 'default'}`
    const sourceLabel = source => source === 'legacy' ? '历史（来源未记录）' : source === 'commercial' ? '商业增强' : '默认'
    const isRefreshDisabled = record => configured.value === false || record.source === 'legacy'
    const detailVisible = ref(false)
    const detailRecord = ref({})
    const pagination = reactive({
      current: 1, pageSize: 20, total: 0, showSizeChanger: true,
      pageSizeOptions: ['10', '20', '50', '100'], showTotal: total => `共 ${total} 条`,
    })
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
      } catch (error) {
        if (generation === loadGeneration) errorMessage.value = requestErrorMessage(error, '获取归属地信息失败')
      } finally {
        if (generation === loadGeneration) loading.value = false
      }
    }
    const reload = () => { keyword.value = keywordInput.value.trim(); pagination.current = 1; load() }
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
      detailVisible.value = false
      load()
    }, { immediate: true })
    return { rows, loading, configured, errorMessage, keywordInput, pagination, columns, formatDate, formatRawResponse, reload, resetFilters, load, onTableChange, remove, refresh, refreshing, removing, recordIdentity, sourceLabel, isRefreshDisabled, detailVisible, detailRecord, showDetails, kindLabel, pageTitle }
  },
})
</script>

<style scoped>
.location-page { display: flex; flex-direction: column; gap: 16px; }
.filter-form { gap: 12px; }
.keyword-input { width: 260px; max-width: 100%; }
.status-alert { margin-top: 16px; }
.table-card { min-height: 420px; }
.response-title { margin: 20px 0 8px; font-size: 14px; }
.raw-response { max-height: 420px; padding: 12px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; background: #f5f7f8; border: 1px solid #e5e7eb; border-radius: 4px; }
@media (max-width: 600px) { .keyword-input { width: 100%; } .filter-form :deep(.ant-form-item) { width: 100%; margin-right: 0; } }
</style>
