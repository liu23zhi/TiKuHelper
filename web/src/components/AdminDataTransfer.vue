<template>
  <section v-if="canManage" :key="renderKey" class="admin-transfer" :aria-label="`${title}数据任务`">
    <div class="transfer-heading"><h2>{{ title }} · 数据任务</h2><a-space wrap>
      <a-button data-transfer-open type="primary" :onClick="entry.open">{{ importable ? '导入 / 导出' : '导出' }}</a-button>
      <a-button data-transfer-refresh :loading="listLoading" :onClick="entry.refresh">刷新任务列表</a-button>
    </a-space></div>
    <p>提交后可关闭弹窗，任务记录会保留在下方；结果文件保留 1 小时。</p>
    <RequestFailureAlert v-if="failure" :failure="failure" message="数据任务未完成，请刷新任务列表核对，勿重复提交。" />
    <p v-if="notice" role="alert">{{ notice }}</p>
    <div class="transfer-history-scroll"><table class="transfer-history" :aria-label="`${title}任务列表`">
      <thead><tr><th>提交时间</th><th>任务</th><th>状态</th><th>脱敏</th><th>结果</th></tr></thead><tbody>
        <tr v-if="!tasks.length"><td colspan="5">{{ listLoading ? '正在读取任务…' : listLoaded ? '暂无数据任务' : '任务列表尚未确认，请刷新核对。' }}</td></tr>
        <tr v-for="row in tasks" :key="row.taskId" :data-transfer-task="row.taskId">
          <td>{{ formatTime(row.createdAt) }}</td><td>{{ row.type === 'import' ? '导入' : '导出' }} · {{ String(row.format || '').toUpperCase() }}<br /><small>{{ row.taskId }}</small></td>
          <td>{{ describeTask(row) }}</td><td>{{ row.type === 'export' ? row.masked ? '是' : '否' : '—' }}</td>
          <td><a-space wrap>
            <a-button v-if="canDownload(row)" size="small" data-transfer-download :loading="downloading === row.taskId" :onClick="entry.downloadRow(row)">下载结果</a-button>
            <a-button v-if="taskErrorReportUrl(row) && !expired(row)" size="small" :onClick="entry.downloadRow(row, true)">下载失败行报告</a-button>
            <span v-if="expired(row)">文件已过期，请重新导出</span><span v-else-if="row.resultDeleted">结果文件已删除</span>
          </a-space></td>
        </tr>
      </tbody></table></div>
    <a-pagination v-if="total > pageSize" :current="page" :page-size="pageSize" :total="total" :show-size-changer="false" :onChange="entry.page" />
    <a-modal :open="visible" :title="`${title}${importable ? '导入 / 导出' : '导出'}`" :footer="null" :mask-closable="false" :onCancel="entry.close">
      <p>导出全部记录，最多 {{ resource === 'sensitive_images' ? '1000' : '10 万' }} 条。{{ resource === 'sensitive_images' ? '脱敏导出只含检测记录，不读取或包含原图；仅在需要原图备份时取消脱敏。' : '默认脱敏；回灌备份前请取消脱敏。' }}</p>
      <a-space wrap>
        <a-select :value="format" :options="formatOptions" :disabled="busy" :onChange="entry.format" aria-label="导出格式" style="width: 120px" />
        <a-checkbox :checked="masked" :disabled="busy" :onChange="entry.masked">脱敏</a-checkbox>
        <a-button data-transfer-submit type="primary" :loading="busy" :disabled="busy" :onClick="entry.export">开始导出</a-button>
      </a-space>
      <template v-if="importable"><p class="transfer-import-description">{{ importDescription }}</p><a-space wrap>
        <a-upload :accept="accept" :show-upload-list="false" :before-upload="entry.import" :disabled="busy"><a-button :disabled="busy" data-transfer-import>选择文件导入</a-button></a-upload>
        <a-button :disabled="busy" :onClick="entry.template">下载 {{ templateFormat.toUpperCase() }} 模板</a-button>
      </a-space></template>
      <p v-if="task" role="status">{{ describeTask(task) }}；任务记录已保留在页面中。</p>
      <a-button class="transfer-done" :onClick="entry.close">关闭，查看任务列表</a-button>
    </a-modal>
  </section>
</template>
<script>
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import RequestFailureAlert from './RequestFailureAlert.vue'
import { getStoredCurrentUser, isAdminUser, useCurrentUserDisplay } from '../utils/currentUser'
import { ensureRequestFailure } from '../utils/requestFailureView.mjs'
import { submitExportTask, submitImportTask, listAsyncTasks, fetchImportTemplate, downloadAsyncTask,
  taskPayload, taskDownloadUrl, taskErrorReportUrl, describeTask } from '../pages/IntelligentImport/asyncTaskApi.mjs'
import { formatTransferSubmittedAt } from '../pages/TikuComponent/questionTransferHelpers.mjs'
export default {
  name: 'AdminDataTransfer', components: { RequestFailureAlert },
  props: {
    resource: { type: String, required: true }, title: { type: String, default: '数据' }, importable: { type: Boolean, default: false },
    importDescription: { type: String, default: '支持 CSV / JSON，每次最多 5000 条，相同查询键和来源会更新。' },
    formats: { type: Array, default: () => ['csv', 'json'] }, accept: { type: String, default: '.csv,.json' },
  }, emits: ['imported'],
  setup(props, { emit }) {
    const { currentUser, captureUpdateGuard } = useCurrentUserDisplay()
    const validAdmin = user => isAdminUser(user) && Number.isSafeInteger(Number(user?.id)) && Number(user.id) > 0 && !['disabled', 'banned', '0'].includes(String(user?.status ?? 'enabled').trim().toLowerCase())
    const canManage = computed(() => validAdmin(currentUser.value))
    const format = ref('csv'), masked = ref(true), visible = ref(false), busy = ref(false), renderKey = ref(0)
    const task = ref(null), tasks = ref([]), failure = shallowRef(null), entry = shallowRef({}), notice = ref('')
    const listLoading = ref(false), listLoaded = ref(false), downloading = ref(''), page = ref(1), total = ref(0), pageSize = 10
    const formatOptions = computed(() => props.formats.filter(value => ['csv', 'json', 'xlsx', 'xml', 'html'].includes(value)).map(value => ({ value, label: value.toUpperCase() })))
    const templateFormat = computed(() => props.accept.includes('.csv') ? 'csv' : props.accept.includes('.xlsx') ? 'xlsx' : 'json')
    const downloadUrl = computed(() => taskDownloadUrl(task.value)), errorReportUrl = computed(() => taskErrorReportUrl(task.value))
    const expired = row => Boolean(row.expiresAt) && Date.parse(row.expiresAt) <= Date.now()
    const canDownload = row => Boolean(taskDownloadUrl(row)) && !row.resultDeleted && !expired(row)
    let alive = true, generation = 0, listVersion = 0, timer = null
    const pendingImports = new Set()
    function stopPolling() { if (timer) clearTimeout(timer); timer = null }
    function createEntry() {
      const resource = props.resource, version = generation, isCurrentUser = captureUpdateGuard()
      const owns = () => alive && version === generation && resource === props.resource && isCurrentUser() && validAdmin(getStoredCurrentUser())
      function merge(row) { task.value = row; if (page.value === 1) tasks.value = [row, ...tasks.value.filter(value => value.taskId !== row.taskId)].slice(0, pageSize) }
      function observe(rows) {
        for (const row of rows) {
          if (!pendingImports.has(row.taskId) || !['succeeded', 'failed', 'cancelled'].includes(row.status)) continue
          pendingImports.delete(row.taskId); if (row.status === 'succeeded') emit('imported')
        }
        if (task.value) task.value = rows.find(row => row.taskId === task.value.taskId) || task.value
      }
      function schedule() {
        stopPolling()
        if (owns() && tasks.value.some(row => ['pending', 'running'].includes(row.status))) timer = setTimeout(() => { if (owns()) void refresh() }, 1500)
      }
      async function refresh() {
        if (!owns()) return
        const read = ++listVersion; listLoading.value = true
        try {
          const response = await listAsyncTasks({ resource, page: page.value, pageSize })
          if (!owns() || read !== listVersion) return
          const body = taskPayload(response)
          if (response?.status !== 200 || !Array.isArray(body?.data)) throw response
          tasks.value = body.data.filter(row => row?.resource === resource); total.value = Number(body.total) || 0; listLoaded.value = true
          observe(tasks.value); schedule()
        } catch (error) { if (owns() && read === listVersion) failure.value = ensureRequestFailure(error) }
        finally { if (owns() && read === listVersion) listLoading.value = false }
      }
      async function submit(type, file) {
        if (!owns() || busy.value) return
        busy.value = true; failure.value = null
        try {
          const response = type === 'import' ? await submitImportTask({ resource, file }) : await submitExportTask({ resource, format: format.value, masked: masked.value })
          if (!owns()) return
          const row = taskPayload(response)
          if (response?.status !== 202 || !row?.taskId) throw response
          page.value = 1; merge(row); if (type === 'import') pendingImports.add(row.taskId)
          await refresh()
        } catch (error) { if (owns()) failure.value = ensureRequestFailure(error) }
        finally { if (owns()) busy.value = false }
      }
      async function saveFile(response, filename) {
        if (!owns()) return
        if (response?.status !== 200 || !(response.data instanceof Blob)) throw response
        const url = window.URL.createObjectURL(response.data), link = document.createElement('a')
        try { link.href = url; link.download = filename; document.body.appendChild(link); link.click() }
        finally { link.remove(); window.URL.revokeObjectURL(url) }
      }
      return {
        open: () => { if (owns()) visible.value = true }, close: () => { if (owns()) visible.value = false }, export: () => submit('export'),
        import: file => { if (props.importable && owns()) void submit('import', file); return false },
        format: value => { if (owns() && !busy.value && formatOptions.value.some(option => option.value === value)) format.value = value },
        masked: event => { if (owns() && !busy.value) masked.value = event?.target?.checked === true },
        refresh: () => { if (owns()) { failure.value = null; return refresh() } }, page: value => { if (owns()) { page.value = value; void refresh() } },
        download: event => { if (!owns()) { event?.preventDefault?.(); event?.stopImmediatePropagation?.() } },
        downloadRow(row, errors = false) { return async () => {
          if (!owns() || downloading.value || row.resource !== resource || (errors ? !taskErrorReportUrl(row) : !canDownload(row))) return
          downloading.value = row.taskId; failure.value = null
          try { await saveFile(await downloadAsyncTask(row.taskId, errors), `${resource}-${row.taskId}${errors ? '-errors.csv' : '.' + (row.format || 'csv')}`) }
          catch (error) { if (owns()) {
            failure.value = ensureRequestFailure(error)
            if (error?.status === 410) { notice.value = '结果文件已过期或已清理，请重新导出。'; row.resultDeleted = true }
          } }
          finally { if (owns()) downloading.value = '' }
        } },
        async template() {
          if (!owns() || busy.value || !props.importable) return
          busy.value = true; failure.value = null
          try { await saveFile(await fetchImportTemplate(resource, templateFormat.value), `${resource}-template.${templateFormat.value}`) }
          catch (error) { if (owns()) failure.value = ensureRequestFailure(error) }
          finally { if (owns()) busy.value = false }
        },
      }
    }
    watch([() => props.resource, () => props.formats.join('|'), () => currentUser.value], () => {
      generation += 1; renderKey.value += 1; listVersion += 1; stopPolling(); pendingImports.clear()
      task.value = null; tasks.value = []; failure.value = null; notice.value = ''; busy.value = false; visible.value = false
      listLoading.value = false; listLoaded.value = false; downloading.value = ''; page.value = 1; total.value = 0
      masked.value = true; format.value = formatOptions.value[0]?.value || 'csv'; entry.value = createEntry()
      if (canManage.value) void entry.value.refresh()
    }, { immediate: true, flush: 'sync' })
    onBeforeUnmount(() => { alive = false; generation += 1; stopPolling() })
    return { canManage, renderKey, format, masked, visible, busy, task, tasks, failure, notice, entry, formatOptions, templateFormat,
      downloadUrl, errorReportUrl, describeTask, listLoading, listLoaded, downloading, page, total, pageSize,
      expired, canDownload, taskErrorReportUrl, formatTime: formatTransferSubmittedAt }
  },
}
</script>
<style scoped>
.admin-transfer { width: 100%; min-width: 0; margin-top: 24px; padding: 20px; border: 1px solid var(--console-border, #ddd); border-radius: 8px; }
.transfer-heading { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 12px; }
.transfer-heading h2 { margin: 0; font-size: 18px; }
.transfer-history-scroll { overflow-x: auto; }
.transfer-history { width: 100%; border-collapse: collapse; }
.transfer-history th, .transfer-history td { padding: 10px; text-align: left; border-bottom: 1px solid var(--console-border, #ddd); overflow-wrap: anywhere; }
.transfer-history small { color: var(--console-muted, #666); }
.transfer-import-description, .transfer-done { margin-top: 20px; }
</style>
