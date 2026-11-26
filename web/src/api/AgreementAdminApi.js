import request from '../utils/request'

const BASE = '/api/admin/agreements'
const send = (method, path, { params, data } = {}) => request({
  method, url: BASE + path, params, data, errorFeedback: 'local', retryPolicy: 'single',
})

export const listAgreementDrafts = params => send('get', '/drafts', { params })
export const getAgreementDraft = id => send('get', '/drafts/' + id)
export const saveAgreementDraft = (id, data) => send(id ? 'put' : 'post', id ? '/drafts/' + id : '/drafts', { data })
export const deleteAgreementDraft = id => send('delete', '/drafts/' + id)
export const deleteAgreementDrafts = ids => send('post', '/drafts/batch-delete', { data: { ids } })
export const publishAgreementDraft = (id, revision) => send('post', '/drafts/' + id + '/publish', { data: { revision } })
export const listAgreementReleases = params => send('get', '/releases', { params })
export const getAgreementRelease = id => send('get', '/releases/' + id)
export const listAgreementRecords = params => send('get', '/records', { params })
