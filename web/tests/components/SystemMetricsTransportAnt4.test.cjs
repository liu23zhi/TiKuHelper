/* Real API wrapper, axios, retry policy and auth handling; only axios adapter is synthetic. No network. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
const axios = require('axios'), Antd = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const create = axios.create
let requests = [], respond
axios.create = config => create({ ...config, adapter: async request => {
  requests.push(request)
  return respond(request)
} })
const { getSystemMetrics } = require(path.join(root, 'src/api/TikuComponentApi.js'))
const admin = { id: 17, role: 'admin', username: 'SYNTHETIC_ADMIN' }
const result = (request, status, data) => ({ config: request, status, statusText: String(status), headers: {}, data })
afterEach(() => { requests = []; user.clearStoredCurrentUser(); Antd.message.destroy() })
test('SM_TRANSPORT real wrapper sends exactly one same-origin GET and preserves raw snapshot shape', async () => {
  user.setStoredCurrentUser(admin)
  const data = { schemaVersion: 1, uptimeSeconds: 0 }
  respond = request => result(request, 200, data)
  const value = await getSystemMetrics()
  assert.equal(requests.length, 1)
  assert.equal(requests[0].url, '/adapter-service/system-metrics')
  assert.equal(requests[0].method, 'get')
  assert.equal(requests[0].retryPolicy, 'single')
  assert.equal(requests[0].withCredentials, true)
  assert.equal(requests[0].data, undefined)
  assert.equal(requests[0].params, undefined)
  assert.equal(value.status, 200); assert.deepEqual(value.data, data)
})
test('SM_TRANSPORT HTTP503 and network errors are single-attempt, with no hidden auto retries', async () => {
  respond = request => result(request, 503, { message: 'SYNTHETIC_UNAVAILABLE' })
  assert.equal((await getSystemMetrics()).status, 503)
  assert.equal(requests.length, 1)
  respond = () => { throw new axios.AxiosError('Network Error', 'ERR_NETWORK') }
  await assert.rejects(getSystemMetrics(), /Network Error/)
  assert.equal(requests.length, 2)
})
test('SM_TRANSPORT real HTTP403 keeps login and is not a success or automatic retry', async () => {
  user.setStoredCurrentUser(admin)
  respond = request => result(request, 403, { message: '当前账号无权执行此操作。' })
  const value = await getSystemMetrics()
  assert.equal(value.status, 403); assert.equal(requests.length, 1)
  assert.deepEqual(user.getStoredCurrentUser(), admin)
  assert.equal(window.location.pathname, '/')
  console.log('SM_TRANSPORT_WITNESS adapter-only GET=1 cookie=true raw-snapshot=true 503/network-single=true 403-login-preserved=true')
})
