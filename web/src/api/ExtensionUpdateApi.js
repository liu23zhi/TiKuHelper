import request from '../utils/request'

export function getExtensionUpdateConfig() {
  return request({ url: '/api/extensions/config', method: 'get' })
}

export function saveExtensionUpdateConfig(data) {
  return request({ url: '/api/extensions/config', method: 'put', data })
}

export function refreshExtensionUpdate(source) {
  if (!['edge', 'chrome'].includes(source)) throw new Error('扩展来源无效')
  return request({ url: `/api/extensions/${source}/refresh`, method: 'post', timeout: 120_000 })
}
