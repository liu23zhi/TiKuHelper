const { test, after, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const { createServer } = require('node:http')
const { once } = require('node:events')
const { JSDOM } = require('jsdom')
require('../helpers/vue-dom.cjs')
const axios = require('axios')

// The RED implementation uses Axios's normal redirect-following transport.
// Give that adapter a loopback base URL in Node; production uses a relative URL.
const original = { window: global.window, adapter: axios.defaults.adapter }
const nodeHttp = axios.getAdapter('http')
axios.defaults.adapter = async config => {
  const response = await nodeHttp({ ...config, baseURL: window.location.origin, proxy: false })
  // Model XHR's real responseType behavior for the RED path in Node.
  if (config.responseType === 'blob') {
    response.data = new Blob([response.data], { type: response.headers.get('content-type') })
  }
  return response
}
const { getReviewRebateScreenshot } = require('../../src/api/ReviewRebateApi.js')
const { clearStoredCurrentUser, getStoredCurrentUser, setStoredCurrentUser } = require('../../src/utils/currentUser.js')
const route = '/api/admin/review-rebate/claims/42/screenshot'
const admin = { id: 1, role: 'admin', status: 'enabled' }

let windowFixture
async function listen(t, handler) {
  const server = createServer(handler).listen(0, '127.0.0.1')
  await once(server, 'listening')
  t.after(async () => {
    server.closeAllConnections()
    await new Promise(resolve => server.close(resolve))
  })
  return 'http://127.0.0.1:' + server.address().port
}
function switchOrigin(origin) {
  windowFixture = new JSDOM('', { url: origin })
  global.window = windowFixture.window
}
afterEach(() => {
  if (windowFixture) {
    clearStoredCurrentUser()
    windowFixture.window.close()
    windowFixture = undefined
  }
  global.window = original.window
})
after(() => { axios.defaults.adapter = original.adapter })

test('the validated screenshot request rejects redirects before any same- or cross-origin follow-up', async t => {
  let sameTargetHits = 0, foreignTargetHits = 0, initialHits = 0
  const foreign = await listen(t, (req, res) => { foreignTargetHits++; res.end('unexpected redirect') })
  let target = '/same-target'
  let redirect = 302
  const origin = await listen(t, (req, res) => {
    if (req.url !== route) { sameTargetHits++; res.end('unexpected redirect'); return }
    initialHits++
    res.writeHead(redirect, { Location: target }).end()
  })
  switchOrigin(origin)
  setStoredCurrentUser(admin)
  const results = await Promise.allSettled([getReviewRebateScreenshot(42, route)])
  target = foreign + '/foreign-target'
  redirect = 307
  results.push(...await Promise.allSettled([getReviewRebateScreenshot(42, route)]))
  assert.equal(initialHits, 2)
  assert.equal(sameTargetHits, 0)
  assert.equal(foreignTargetHits, 0)
  assert.deepEqual(results.map(result => result.status), ['rejected', 'rejected'])
})

test('a wire-format 403 JSON ban response reaches the unchanged shared request handler', async t => {
  const requestID = '12341234123412341234123412341234'
  const origin = await listen(t, (req, res) => {
    assert.equal(req.url, route)
    res.writeHead(403, { 'Content-Type': 'application/json; charset=utf-8', 'X-Request-ID': requestID })
    res.end(JSON.stringify({ code: 'ACCOUNT_BANNED', message: 'fixture ban' }))
  })
  switchOrigin(origin)
  setStoredCurrentUser(admin)
  const result = await getReviewRebateScreenshot(42, route)
  assert.equal(result.status, 403)
  assert.equal(getStoredCurrentUser().status, 'banned')
  assert.equal(result.data.code, 'ACCOUNT_BANNED')
  const { getTransportRequestId } = require('../../src/utils/requestHelpers.mjs')
  assert.equal(getTransportRequestId(result), requestID)
})
