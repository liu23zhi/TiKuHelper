import request from '@/utils/request'

const IMPORT_PREVIEWS_PATH = '/api/import/previews'
const EXPORT_TASKS_PATH = '/api/export/tasks'
const IMPORT_TEMPLATE_PATH = '/api/import/template'
const FORMATS = new Set(['json', 'csv', 'xlsx', 'xml', 'html'])

export function previewQuestionImport(file) {
  if (!file) throw new Error('请选择要导入的文件')
  const form = new FormData()
  form.append('resource', 'questions')
  form.append('file', file, file.name || 'questions.csv')
  return request({ errorFeedback: 'local', url: IMPORT_PREVIEWS_PATH, method: 'post', data: form })
}

export function getQuestionImportPreview(previewId, page, pageSize) {
  return request({ errorFeedback: 'local', url: `${IMPORT_PREVIEWS_PATH}/${encodeURIComponent(previewId)}`, method: 'get', params: { page, pageSize } })
}

export function confirmQuestionImport(previewId) {
  return request({ errorFeedback: 'local', url: `${IMPORT_PREVIEWS_PATH}/${encodeURIComponent(previewId)}/confirm`, method: 'post' })
}

export function discardQuestionImportPreview(previewId) {
  return request({ errorFeedback: 'local', url: `${IMPORT_PREVIEWS_PATH}/${encodeURIComponent(previewId)}`, method: 'delete' })
}

export function createQuestionExportTask(format, questionIds) {
  if (!FORMATS.has(format)) throw new Error('导出格式不受支持')
  const payload = { resource: 'questions', format, masked: true }
  if (Array.isArray(questionIds) && questionIds.length) payload.questionIds = questionIds
  return request({ errorFeedback: 'local', url: EXPORT_TASKS_PATH, method: 'post', data: payload })
}

export function listQuestionTransferTasks() {
  return request({ errorFeedback: 'local', url: EXPORT_TASKS_PATH, method: 'get', params: { resource: 'questions', page: 1, pageSize: 100 } })
}

export function deleteQuestionExportResult(taskId) {
  return request({ errorFeedback: 'local', url: `${EXPORT_TASKS_PATH}/${encodeURIComponent(taskId)}`, method: 'delete' })
}

export function downloadQuestionImportTemplate(format = 'csv') {
  if (!FORMATS.has(format)) throw new Error('模板格式不受支持')
  return request({ errorFeedback: 'local', url: IMPORT_TEMPLATE_PATH, method: 'get', params: { resource: 'questions', format }, responseType: 'blob' })
}
