export const QUESTION_TRANSFER_FORMATS = [
  { value: 'json', label: 'JSON' },
  { value: 'csv', label: 'CSV' },
  { value: 'xlsx', label: 'Excel (.xlsx)' },
  { value: 'xml', label: 'XML' },
  { value: 'html', label: 'HTML' },
]

export const QUESTION_IMPORT_ACCEPT = '.json,.csv,.xlsx,.xls,.xml,.html,.htm,.txt'

export function transferTaskRows(response) {
  const body = response?.data
  if (Array.isArray(body?.data)) return body.data
  if (Array.isArray(body?.items)) return body.items
  if (Array.isArray(body)) return body
  return []
}

export function limitQuestionTransferTasks(tasks) {
  const sorted = [...(Array.isArray(tasks) ? tasks : [])].sort((a, b) => {
    return new Date(b?.createdAt || 0).getTime() - new Date(a?.createdAt || 0).getTime()
  })
  const active = sorted.filter(task => ['pending', 'running'].includes(String(task?.status || '').toLowerCase()))
  const imports = sorted.filter(task => task?.type === 'import' && !active.includes(task)).slice(0, 20)
  const exports = sorted.filter(task => task?.type === 'export' && !active.includes(task)).slice(0, 5)
  return [...active, ...imports, ...exports].sort((a, b) => {
    return new Date(b?.createdAt || 0).getTime() - new Date(a?.createdAt || 0).getTime()
  })
}

export function formatTransferSubmittedAt(value) {
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) return '-'
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).format(date)
}

export function describeTransferTask(task) {
  const status = String(task?.status || '').toLowerCase()
  if (status === 'pending') return '任务排队中'
  if (status === 'running') return task?.total > 0 ? `任务执行中（${task.processed || 0}/${task.total}）` : '任务执行中'
  if (status === 'succeeded') {
    const succeeded = Number.isFinite(task?.succeeded) ? task.succeeded : 0
    const parts = [`成功 ${succeeded}`]
    if (task?.skipped) parts.push(`跳过 ${task.skipped}`)
    if (task?.failed) parts.push(`失败 ${task.failed}`)
    return `任务完成：${parts.join('，')}`
  }
  if (status === 'failed') return task?.errorMessage || task?.message || '任务失败，请核对文件后重试。'
  if (status === 'cancelled') return '任务已取消'
  return task?.message || '状态未知'
}
