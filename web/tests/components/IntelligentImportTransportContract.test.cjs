/* Real parser API, axios, retry policy and failure classifier. Only the axios adapter is synthetic; zero sockets/providers. */
const { test, beforeEach, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const { inspect } = require('node:util')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
const axios = require('axios')
const create = axios.create
let requests = [], respond
axios.create = config => create({ ...config, adapter: async request => {
  requests.push(request)
  assert.equal(typeof respond, 'function', 'adapter has no network fallback')
  return respond(request)
} })
const { parseFile, reParseFile } = require(path.join(root, 'src/api/api.js'))
const { buildRequestFailureView } = require(path.join(root, 'src/utils/requestFailureView.mjs'))
axios.create = create

const SECRET = 'SYNTHETIC_SECRET_DO_NOT_DISPLAY'
const CONNECTION = 'https://synthetic-token:synthetic-password@provider.invalid/private'
const QUESTION = 'SYNTHETIC_PRIVATE_QUESTION'
const consoleMethods = ['log', 'info', 'warn', 'error', 'debug']
let consoleCalls, originals
const result = (config, status, data) => ({ config, status, statusText: String(status), headers: {}, data })
const methods = [
  { label: 'GET parseFile', invoke: () => parseFile({ file: 'SYNTHETIC_CRC' }), kind: 'get' },
  { label: 'POST reParseFile', invoke: () => reParseFile({ html: '<p>SYNTHETIC_HTML & +</p>' }), kind: 'post' },
]
function assertSingleRequest() {
  assert.equal(requests.length, 1, 'one invocation must make exactly one adapter call')
  assert.equal(requests[0].retryPolicy, 'single')
  assert.equal(requests[0].timeout, 60_000)
  assert.equal(requests[0].baseURL, '/sqp/api')
  assert.equal(requests[0].url, '/parse')
}
function assertSafe(error) {
  assert.ok(error instanceof Error, 'a failed transport rejects with an Error')
  const text = inspect(error, { depth: 8, showHidden: true })
  for (const sensitive of [SECRET, CONNECTION, QUESTION]) assert.ok(!text.includes(sensitive), 'safe error must not retain upstream messages, connection details or paper content')
  assert.equal(consoleCalls.length, 0, 'transport does not echo raw responses or errors to console')
}
beforeEach(() => {
  requests = []; consoleCalls = []; originals = {}
  for (const key of consoleMethods) {
    originals[key] = console[key]
    console[key] = (...args) => consoleCalls.push({ key, args })
  }
})
afterEach(() => { for (const key of consoleMethods) console[key] = originals[key] })
after(() => { axios.create = create })

test('IMPORT_TRANSPORT HTTP503 with plausible paper is rejected, never mistaken for parser success', async () => {
  const body = { paper: [{ content: QUESTION, typeAlias: '单选题', answer: ['A'], options: [{ value: 'one' }] }], message: SECRET }
  respond = config => result(config, 503, body)
  const settled = await Promise.allSettled([parseFile({ file: 'SYNTHETIC_CRC' })])
  assert.equal(settled[0].status, 'rejected', 'non-200 paper cannot be accepted')
  assertSafe(settled[0].reason)
  assertSingleRequest()
})
test('IMPORT_TRANSPORT non-200 rawmessage is never printed, even when it looks like a valid paper response', async () => {
  respond = config => result(config, 503, { paper: [{ content: QUESTION }], message: SECRET })
  await parseFile({ file: 'SYNTHETIC_CRC' }).catch(() => {})
  assert.equal(consoleCalls.length, 0, 'raw upstream message must not be console logged')
  assertSingleRequest()
})
for (const method of methods) {
  test(`IMPORT_TRANSPORT ${method.label} HTTP200 returns exact untouched raw-body object, including nested arrays`, async () => {
    const body = { paper: [{ content: QUESTION, typeAlias: '多选题', answer: ['B', 'A', 'A'], options: [{ value: '甲' }, { value: '乙' }] }], extension: { preserved: true } }
    const before = structuredClone(body)
    respond = config => result(config, 200, body)
    const actual = await method.invoke()
    assert.strictEqual(actual, body)
    assert.strictEqual(actual.paper, body.paper)
    assert.strictEqual(actual.paper[0].answer, body.paper[0].answer)
    assert.deepEqual(actual, before)
    assertSingleRequest()
    assert.equal(requests[0].method, method.kind)
    if (method.kind === 'get') {
      assert.deepEqual(requests[0].params, { file: 'SYNTHETIC_CRC' })
      assert.equal(requests[0].data, undefined)
    } else {
      assert.equal(requests[0].params, undefined)
      assert.equal(typeof requests[0].data, 'string')
      assert.deepEqual([...new URLSearchParams(requests[0].data)], [['html', '<p>SYNTHETIC_HTML & +</p>']])
      assert.match(requests[0].headers.get('Content-Type'), /^application\/x-www-form-urlencoded\b/i)
    }
    assert.equal(consoleCalls.length, 0)
  })
  test(`IMPORT_TRANSPORT ${method.label} HTTP200 missing paper is left for page validation, not silently wrapped`, async () => {
    const body = { extension: { parserVersion: 'SYNTHETIC' } }
    respond = config => result(config, 200, body)
    assert.strictEqual(await method.invoke(), body)
    assertSingleRequest()
  })
  for (const status of [201, 204, 206, 299, 301, 304, 400, 401, 403, 408, 429, 500, 503, 599]) {
    test(`IMPORT_TRANSPORT ${method.label} HTTP${status} rejects safely once and preserves status classification`, async () => {
      respond = config => result(config, status, { paper: [{ content: QUESTION }], message: `${SECRET} ${CONNECTION}` })
      let error
      await assert.rejects(method.invoke(), failure => { error = failure; return true })
      assertSingleRequest()
      assertSafe(error)
      assert.equal(error.name, 'RequestError')
      assert.equal(error.response?.status ?? error.status, status)
      const expected = status === 401 ? 'unauthorized' : status === 403 ? 'forbidden' : status === 429 ? 'rate-limit' : status === 408 ? 'timeout' : status >= 500 ? 'upstream' : 'unknown'
      assert.equal(buildRequestFailureView(error).kind, expected)
    })
  }
  for (const [code, kind] of [['ERR_NETWORK', 'network'], ['ECONNABORTED', 'timeout'], ['ETIMEDOUT', 'timeout']]) {
    test(`IMPORT_TRANSPORT ${method.label} ${code} rejects safely once with existing retry-outcome semantics`, async () => {
      respond = config => { throw new axios.AxiosError(`${SECRET} ${CONNECTION} ${QUESTION}`, code, config) }
      let error
      await assert.rejects(method.invoke(), failure => { error = failure; return true })
      assertSingleRequest()
      assertSafe(error)
      assert.equal(buildRequestFailureView(error).kind, method.kind === 'post' ? 'unknown-outcome' : kind)
      if (method.kind === 'post') {
        assert.equal(error.code, 'UNKNOWN_OUTCOME')
        assert.equal(error.cause?.name, 'RequestError')
      } else {
        assert.equal(error.code, code)
        assert.equal(error.name, 'RequestError')
      }
    })
  }
  test(`IMPORT_TRANSPORT ${method.label} HTTP failure message is fixed rather than upstream-controlled`, async () => {
    const failures = []
    for (const message of ['FIRST_SYNTHETIC_RAW_DETAIL', 'SECOND_DIFFERENT_RAW_DETAIL']) {
      respond = config => result(config, 503, { paper: [{ content: QUESTION }], message })
      await assert.rejects(method.invoke(), failure => { failures.push(failure); return true })
    }
    assert.equal(requests.length, 2)
    assert.equal(failures[0].message, failures[1].message)
    assert.ok(failures[0].message.length > 0)
    assert.ok(!/FIRST_SYNTHETIC|SECOND_DIFFERENT/.test(failures[0].message))
    assert.equal(consoleCalls.length, 0)
  })
}
