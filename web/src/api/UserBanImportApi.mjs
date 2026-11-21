import request from '../utils/request'

const basePath = '/api/admin/user-bans/import'
function previewPath(id) {
  if (typeof id !== 'string' || !/^[a-zA-Z0-9_-]{8,128}$/.test(id)) throw new Error('无效的封禁预览编号')
  return `${basePath}/${id}`
}
export function previewUserBanImport(file) {
  if (!file) throw new Error('请选择 CSV / JSON 文件')
  const data = new FormData()
  data.append('file', file, file.name || 'bans.csv')
  return request({ url: `${basePath}/preview`, method: 'post', data, errorFeedback: 'local' })
}
export function confirmUserBanImport(id) {
  return request({ url: `${previewPath(id)}/confirm`, method: 'post', data: { confirmed: true }, errorFeedback: 'local' })
}
export function deleteUserBanImportPreview(id) {
  return request({ url: previewPath(id), method: 'delete', errorFeedback: 'local' })
}
