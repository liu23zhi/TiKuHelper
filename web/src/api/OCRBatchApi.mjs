import request from '../utils/request'

function upload(url, file) {
  const data = new FormData()
  data.append('file', file, file.name || 'image.png')
  return request({ url, method: 'post', data, errorFeedback: 'local', timeout: 180000 })
}
export const previewOCRDocument = file => upload('/api/admin/ocr/document-preview', file)
export const uploadOCRImage = file => upload('/api/admin/ocr/image-upload', file)
// URL 批量识别逐张经过现有管理员钱包及工具计费入口。
export const recognizeOCRURL = url => request({ url: '/api/ai-search-config/tool-test', method: 'post', data: { mode: 'direct', tool: 'image_ocr', url }, errorFeedback: 'local', timeout: 180000 })
