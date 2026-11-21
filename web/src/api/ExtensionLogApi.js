import request from '../utils/request'

const endpoint = '/api/admin/extension-logs'

export const listExtensionLogs = (filters, cursor = '') => request({
  url: endpoint, method: 'get', params: { limit: 50, ...filters, ...(cursor ? { cursor } : {}) },
  retryPolicy: 'single', errorFeedback: 'local',
})
export const getExtensionLogPolicy = () => request({
  url: endpoint + '/settings', method: 'get', retryPolicy: 'single', errorFeedback: 'local',
})
export const saveExtensionLogPolicy = data => request({
  url: endpoint + '/settings', method: 'put', data, retryPolicy: 'single', errorFeedback: 'local',
})
