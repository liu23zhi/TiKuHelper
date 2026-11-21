<template>
  <ConsolePanel v-if="canManage" :key="surfaceVersion" class="question-transfer" title="题库导入 / 导出" description="上传文件后先预览识别结果；导入与导出均由服务器异步处理。">
    <div class="transfer-actions">
      <input ref="fileInput" hidden type="file" :accept="QUESTION_IMPORT_ACCEPT" :onChange="bind(handleFileSelected)" />
      <a-button type="primary" :disabled="busy" :onClick="bind(openImportPicker)">导入</a-button>
      <a-button :onClick="bind(() => exportVisible = true)">导出</a-button>
      <a-button :onClick="bind(() => templateVisible = true)">下载模板</a-button>
      <span v-if="selectedCount" class="selection-hint">已选 {{ selectedCount }} 道题，可在导出时选择范围</span>
    </div>

    <section class="task-history" aria-label="题库异步任务">
      <div class="task-heading">
        <h3>异步任务</h3>
        <a-button :loading="taskLoading" :disabled="taskLoading" :onClick="bind(refreshTasks)">刷新</a-button>
      </div>
      <p v-if="taskError" class="task-error" role="alert">{{ taskError }}</p>
      <div class="task-table-scroll">
        <table class="task-table" aria-label="题库异步任务列表">
          <thead><tr><th scope="col">提交时间</th><th scope="col">任务</th><th scope="col">状态</th><th scope="col">结果</th><th scope="col">操作</th></tr></thead>
          <tbody>
            <tr v-if="!tasks.length"><td colspan="5" class="empty-tasks">{{ taskLoading ? '正在读取任务…' : '暂无异步任务' }}</td></tr>
            <tr v-for="task in tasks" :key="task.taskId">
              <td class="task-time">{{ formatTransferSubmittedAt(task.createdAt) }}</td>
              <td>{{ task.type === 'import' ? '题目导入' : `题目导出 · ${task.format || '文件'}` }}</td>
              <td>{{ describeTransferTask(task) }}</td>
              <td class="task-links">
                <a v-if="taskDownloadUrl(task)" :href="taskDownloadUrl(task)" :onClick="guardDownload()">下载结果</a>
                <a v-if="taskErrorReportUrl(task)" :href="taskErrorReportUrl(task)" :onClick="guardDownload()">失败行报告</a>
                <span v-if="!taskDownloadUrl(task) && !taskErrorReportUrl(task)">-</span>
              </td>
              <td><a-button v-if="canDeleteExport(task)" size="small" danger :loading="deletingId === task.taskId" :onClick="bind(deleteExportResult, task)">删除结果</a-button><span v-else>-</span></td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <a-modal v-model:open="previewVisible" title="导入预览" :width="980" :confirm-loading="confirming" ok-text="确认导入" cancel-text="取消" :ok-button-props="{ disabled: !previewId || previewTotal === 0 }" :onOk="bind(confirmImport)" :onCancel="bind(closePreview)">
      <p class="preview-summary">识别到 {{ previewTotal }} 道题目。确认导入后，服务器将在后台写入题库。</p>
      <p v-if="previewError" class="task-error" role="alert">{{ previewError }}</p>
      <div class="preview-table-scroll">
        <a-table :columns="previewColumns" :data-source="previewItems" :pagination="false" :loading="previewLoading" row-key="row" :scroll="{ x: 840 }" size="small">
          <template #bodyCell="{ column, record }">
            <span v-if="column.key === 'question'" class="preview-question">{{ record.question || '-' }}</span>
            <span v-else-if="column.key === 'answer'" class="preview-question">{{ record.answer || '-' }}</span>
            <span v-else>{{ previewCell(record[column.dataIndex]) }}</span>
          </template>
        </a-table>
      </div>
      <a-pagination v-if="previewTotal > previewPageSize" class="preview-pagination" :current="previewPage" :page-size="previewPageSize" :total="previewTotal" :show-size-changer="false" :onChange="bind(loadPreviewPage)" />
    </a-modal>

    <a-modal v-model:open="exportVisible" title="创建导出任务" ok-text="开始导出" cancel-text="取消" :confirm-loading="exporting" :onOk="bind(submitExport)" @cancel="exportError = ''">
      <a-form layout="vertical">
        <a-form-item label="导出格式">
          <a-radio-group v-model:value="exportFormat" class="format-options">
            <a-radio v-for="format in QUESTION_TRANSFER_FORMATS" :key="format.value" :value="format.value">{{ format.label }}</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item label="题目范围">
          <a-radio-group v-model:value="exportScope">
            <a-radio value="all">全部题目</a-radio>
            <a-radio value="selected" :disabled="!selectedIds.length">上方选中的 {{ selectedIds.length }} 道</a-radio>
          </a-radio-group>
        </a-form-item>
      </a-form>
      <p v-if="exportError" class="task-error" role="alert">{{ exportError }}</p>
    </a-modal>

    <a-modal v-model:open="templateVisible" title="下载导入模板" ok-text="下载模板" cancel-text="取消" :onOk="bind(downloadTemplate)">
      <a-form layout="vertical"><a-form-item label="模板格式"><a-select v-model:value="templateFormat" :options="QUESTION_TRANSFER_FORMATS.map(item => ({ value: item.value, label: item.label }))" /></a-form-item></a-form>
      <p v-if="templateError" class="task-error" role="alert">{{ templateError }}</p>
    </a-modal>
  </ConsolePanel>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import Swal from 'sweetalert2'
import { message } from 'ant-design-vue'
import { useCurrentUserDisplay } from '@/utils/currentUser'
import { useAdminPanelLease } from '@/utils/useAdminPanelLease'
import { taskDownloadUrl, taskErrorReportUrl } from '../IntelligentImport/asyncTaskApi.mjs'
import {
  confirmQuestionImport,
  createQuestionExportTask,
  deleteQuestionExportResult,
  discardQuestionImportPreview,
  downloadQuestionImportTemplate,
  getQuestionImportPreview,
  listQuestionTransferTasks,
  previewQuestionImport,
} from './questionTransferApi.mjs'
import {
  describeTransferTask,
  formatTransferSubmittedAt,
  limitQuestionTransferTasks,
  QUESTION_IMPORT_ACCEPT,
  QUESTION_TRANSFER_FORMATS,
  transferTaskRows,
} from './questionTransferHelpers.mjs'

const props = defineProps({ selectedQuestionIds: { type: Array, default: () => [] } })
const emit = defineEmits(['imported'])
const selectedIds = computed(() => props.selectedQuestionIds.map(Number).filter(id => Number.isInteger(id) && id > 0))
const selectedCount = computed(() => selectedIds.value.length)
const previewColumns = [
  { title: '行', dataIndex: 'row', key: 'row', width: 60 },
  { title: '类型', dataIndex: 'type', key: 'type', width: 100 },
  { title: '题目', dataIndex: 'question', key: 'question', width: 280 },
  { title: '选项', dataIndex: 'options', key: 'options', width: 190 },
  { title: '答案', dataIndex: 'answer', key: 'answer', width: 150 },
  { title: '课程', dataIndex: 'courseName', key: 'courseName', width: 150 },
]
const fileInput = ref(null)
const busy = ref(false)
const taskLoading = ref(false)
const taskError = ref('')
const tasks = ref([])
const exportVisible = ref(false)
const exportFormat = ref('json')
const exportScope = ref('selected')
const exporting = ref(false)
const exportError = ref('')
const templateVisible = ref(false)
const templateFormat = ref('csv')
const templateError = ref('')
const previewVisible = ref(false)
const previewId = ref('')
const previewTotal = ref(0)
const previewPage = ref(1)
const previewPageSize = 25
const previewItems = ref([])
const previewLoading = ref(false)
const previewError = ref('')
const confirming = ref(false)
const deletingId = ref('')
let pollingTimer = null
let taskLoadVersion = 0
let previewRequestVersion = 0
const terminalStatuses = new Set(['succeeded', 'failed', 'cancelled'])
const pendingImportIds = new Set()
const { currentUser } = useCurrentUserDisplay()
const { canManage, capture, bind, surfaceVersion } = useAdminPanelLease(currentUser, {
  clear: resetTransfer,
  reload: refreshTasks,
})

function resetTransfer() {
  if (pollingTimer) clearTimeout(pollingTimer)
  pollingTimer = null
  taskLoadVersion += 1
  previewRequestVersion += 1
  pendingImportIds.clear()
  tasks.value = []
  previewItems.value = []
  previewPage.value = 1
  previewTotal.value = 0
  for (const state of [busy, taskLoading, exportVisible, exporting, templateVisible, previewVisible, previewLoading, confirming]) state.value = false
  for (const state of [taskError, exportError, templateError, previewError, previewId, deletingId]) state.value = ''
  exportFormat.value = 'json'
  exportScope.value = selectedIds.value.length ? 'selected' : 'all'
  templateFormat.value = 'csv'
}

function guardDownload() {
  const owns = capture()
  return event => { if (!owns()) event.preventDefault() }
}

function taskPayload(response) { return response?.data && typeof response.data === 'object' ? response.data : {} }

function openImportPicker() {
  if (!busy.value) fileInput.value?.click()
}

async function handleFileSelected(event) {
  const owns = capture()
  if (!owns() || busy.value) return
  const input = event?.target
  const file = input?.files?.[0]
  if (input) input.value = ''
  if (!file) return
  if (!/\.(json|csv|xlsx|xls|xml|html|htm|txt)$/i.test(file.name)) {
    message.error('暂不支持该文件格式，请选择 JSON、Excel、CSV、XML、HTML 或 TXT 文件。')
    return
  }
  busy.value = true
  previewError.value = ''
  try {
    const response = await previewQuestionImport(file)
    if (!owns()) return
    const data = taskPayload(response)
    previewId.value = String(data.previewId || '')
    previewTotal.value = Number(data.total || 0)
    previewPage.value = Number(data.page || 1)
    previewItems.value = Array.isArray(data.items) ? data.items : []
    if (!previewId.value) throw new Error('服务器未返回预览编号')
    previewVisible.value = true
    await refreshTasks()
  } catch (error) {
    if (owns()) message.error(error?.response?.data?.message || error?.message || '文件解析失败，请检查文件内容后重试。')
  } finally {
    if (owns()) busy.value = false
  }
}

async function loadPreviewPage(page) {
  const owns = capture()
  if (!owns() || !previewId.value || previewLoading.value) return
  const ticket = ++previewRequestVersion
  previewLoading.value = true
  previewError.value = ''
  try {
    const response = await getQuestionImportPreview(previewId.value, page, previewPageSize)
    if (!owns() || ticket !== previewRequestVersion) return
    const data = taskPayload(response)
    previewPage.value = Number(data.page || page)
    previewTotal.value = Number(data.total ?? previewTotal.value)
    previewItems.value = Array.isArray(data.items) ? data.items : []
  } catch (error) {
    if (owns() && ticket === previewRequestVersion) previewError.value = error?.response?.data?.message || '读取预览分页失败，请重试。'
  } finally {
    if (owns() && ticket === previewRequestVersion) previewLoading.value = false
  }
}

function previewCell(value) {
  if (Array.isArray(value)) return value.map(item => typeof item === 'string' ? item : JSON.stringify(item)).join('；') || '-'
  if (value && typeof value === 'object') return JSON.stringify(value)
  return value == null || value === '' ? '-' : String(value)
}

async function closePreview() {
  previewRequestVersion += 1
  const id = previewId.value
  previewVisible.value = false
  previewId.value = ''
  previewItems.value = []
  previewTotal.value = 0
  if (id) {
    try { await discardQuestionImportPreview(id) } catch { /* Temporary previews expire server-side. */ }
  }
}

async function confirmImport() {
  const owns = capture()
  if (!owns() || !previewId.value || confirming.value) return
  confirming.value = true
  previewError.value = ''
  const id = previewId.value
  try {
    const response = await confirmQuestionImport(id)
    if (!owns()) return
    const task = taskPayload(response)
    if (!task.taskId) throw new Error('服务器未返回导入任务编号')
    previewVisible.value = false
    previewId.value = ''
    previewItems.value = []
    message.success('导入任务已提交，完成后可在异步任务列表查看结果。')
    pendingImportIds.add(task.taskId)
    await refreshTasks()
  } catch (error) {
    if (owns()) previewError.value = error?.response?.data?.message || error?.message || '提交导入失败，请重试。'
  } finally {
    if (owns()) confirming.value = false
  }
}

async function submitExport() {
  const owns = capture()
  if (!owns() || exporting.value) return
  const ids = exportScope.value === 'selected' ? selectedIds.value : []
  if (exportScope.value === 'selected' && !ids.length) {
    exportError.value = '请先在题库列表中选择题目，或改为导出全部题目。'
    return
  }
  exporting.value = true
  exportError.value = ''
  try {
    const response = await createQuestionExportTask(exportFormat.value, ids)
    if (!owns()) return
    const task = taskPayload(response)
    if (!task.taskId) throw new Error('服务器未返回导出任务编号')
    exportVisible.value = false
    message.success('导出任务已提交，结果会显示在异步任务列表。')
    mergeTask(task)
    await refreshTasks()
  } catch (error) {
    if (owns()) exportError.value = error?.response?.data?.message || error?.message || '提交导出失败，请稍后重试。'
  } finally {
    if (owns()) exporting.value = false
  }
}

function mergeTask(task) {
  if (!task?.taskId) return
  tasks.value = limitQuestionTransferTasks([task, ...tasks.value.filter(item => item.taskId !== task.taskId)])
}

async function refreshTasks() {
  const owns = capture()
  if (!owns()) return
  const ticket = ++taskLoadVersion
  taskLoading.value = true
  try {
    const response = await listQuestionTransferTasks()
    if (!owns() || ticket !== taskLoadVersion) return
    const rows = transferTaskRows(response)
    for (const task of rows) {
      if (!pendingImportIds.has(task?.taskId) || !terminalStatuses.has(String(task?.status || '').toLowerCase())) continue
      pendingImportIds.delete(task.taskId)
      if (String(task.status).toLowerCase() === 'succeeded') emit('imported')
    }
    tasks.value = limitQuestionTransferTasks(rows)
    taskError.value = ''
    schedulePolling()
  } catch (error) {
    if (owns() && ticket === taskLoadVersion) taskError.value = error?.response?.data?.message || '任务列表读取失败，请稍后重试。'
  } finally {
    if (owns() && ticket === taskLoadVersion) taskLoading.value = false
  }
}

function schedulePolling() {
  if (pollingTimer) clearTimeout(pollingTimer)
  if (!tasks.value.some(task => ['pending', 'running'].includes(String(task.status || '').toLowerCase()))) return
  pollingTimer = setTimeout(bind(refreshTasks), 1800)
}

function canDeleteExport(task) {
  return task?.type === 'export' && terminalStatuses.has(String(task.status || '').toLowerCase())
    && Boolean(taskDownloadUrl(task))
}

async function deleteExportResult(task) {
  const owns = capture()
  if (!owns()) return
  const result = await Swal.fire({
    title: '删除导出结果？',
    text: '只会删除此任务的结果文件，任务记录仍会保留。',
    icon: 'warning', showCancelButton: true, confirmButtonText: '删除结果', cancelButtonText: '取消',
  })
  if (!owns() || !result.isConfirmed || deletingId.value) return
  deletingId.value = task.taskId
  try {
    await deleteQuestionExportResult(task.taskId)
    if (!owns()) return
    message.success('导出结果已删除。')
    await refreshTasks()
  } catch (error) {
    if (owns()) message.error(error?.response?.data?.message || '删除导出结果失败，请重试。')
  } finally {
    if (owns()) deletingId.value = ''
  }
}

async function downloadTemplate() {
  const owns = capture(), format = templateFormat.value
  if (!owns()) return
  templateError.value = ''
  try {
    const response = await downloadQuestionImportTemplate(format)
    if (!owns()) return
    if (response?.status !== 200) throw new Error(response?.message || '模板下载失败，请稍后重试。')
    const blob = response?.data instanceof Blob ? response.data : response
    const url = window.URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `题库导入模板.${format}`
    document.body.appendChild(link)
    link.click()
    link.remove()
    window.URL.revokeObjectURL(url)
    templateVisible.value = false
  } catch (error) {
    if (owns()) templateError.value = error?.response?.data?.message || '模板下载失败，请稍后重试。'
  }
}

watch(selectedIds, ids => {
  if (!ids.length && exportScope.value === 'selected') exportScope.value = 'all'
})
</script>

<style scoped>
.question-transfer { margin-top: 20px; }
.transfer-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.selection-hint, .preview-summary { color: var(--console-muted); }
.task-history { margin-top: 20px; padding-top: 18px; border-top: 1px solid var(--console-border); }
.task-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.task-heading h3 { margin: 0; font-size: 16px; }
.task-table-scroll, .preview-table-scroll { min-width: 0; overflow-x: auto; }
.task-table { width: 100%; margin-top: 12px; border-collapse: collapse; font-size: 13px; }
.task-table th, .task-table td { padding: 9px 10px; text-align: left; vertical-align: top; border-bottom: 1px solid var(--console-border); overflow-wrap: anywhere; }
.task-table th { color: var(--console-muted); font-weight: 600; white-space: nowrap; }
.task-time { min-width: 174px; white-space: nowrap; }
.task-links { display: flex; flex-wrap: wrap; gap: 10px; }
.empty-tasks { text-align: center !important; color: var(--console-muted); padding: 18px !important; }
.task-error { color: #b42318; margin: 10px 0; }
.preview-question { display: block; max-height: 84px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; }
.preview-pagination { margin-top: 14px; display: flex; justify-content: flex-end; }
.format-options { display: flex; flex-wrap: wrap; gap: 8px 16px; }
@media (max-width: 680px) {
  .task-table th, .task-table td { padding: 8px 7px; }
  .task-time { min-width: 150px; }
  .task-table { min-width: 720px; }
}
</style>
