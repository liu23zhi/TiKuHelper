import { test } from 'node:test'
import assert from 'node:assert/strict'
import { buildRequestFailureView, ensureRequestFailure } from './requestFailureView.mjs'
import { buildResponsePayload, captureTransportRequestError } from './requestHelpers.mjs'
const ID = '0123456789abcdef0123456789abcdef'
test('failure classes use fixed public copy, never free-form payload, header body or config', () => {
  const table = [
    [{ status: 401 }, 'unauthorized'], [{ status: 403 }, 'forbidden'], [{ status: 429 }, 'rate-limit'],
    [{ status: 408 }, 'timeout'], [{ code: 'ECONNABORTED' }, 'timeout'], [{ code: 'ETIMEDOUT' }, 'timeout'],
    [{ code: 'ERR_NETWORK' }, 'network'], [{ message: 'Failed to fetch' }, 'network'],
    [{ status: 502 }, 'upstream'], [{ status: 200, data: { error: { code: 'UPSTREAM_ERROR' } } }, 'upstream'],
    [{ status: 200, data: { error: { code: 'NO_ANSWER' } } }, 'no-answer'],
    [{ status: 401, data: { code: 'INSUFFICIENT_BALANCE' } }, 'balance'],
    [{ status: 401, data: { code: 'ACCESS_TOKEN_QUOTA_EXCEEDED' } }, 'quota'],
    [{ status: 200, data: { error: { code: 'AI_UNAVAILABLE' } } }, 'unavailable'],
    [{ code: 'UNKNOWN_OUTCOME' }, 'unknown-outcome'], [{ code: 'ERR_CANCELED' }, 'canceled'],
    [{ status: 200, data: { schemaVersion: 'invalid' } }, 'unknown'],
  ]
  for (const [failure, kind] of table) {
    const body = failure.data || {}
    failure.data = { ...body, message: 'PRIVATE_SERVER_MESSAGE', detail: 'PRIVATE_DETAIL', requestId: ID }
    failure.message ||= 'https://private.invalid/?key=PRIVATE_KEY'
    failure.config = { data: 'PRIVATE_BODY', headers: { Cookie: 'PRIVATE_COOKIE' } }
    const view = buildRequestFailureView(failure)
    assert.equal(view.kind, kind)
    assert.equal(view.diagnosticId, undefined, 'body ID is not trusted transport metadata')
    assert.doesNotMatch(JSON.stringify(view), /PRIVATE_|private\.invalid/)
  }
})
test('validated transport ID stays out of enumerable payload and is reused, never inferred', () => {
  for (const id of [ID, 'fallback-1793200000000000000-1']) {
    const failure = buildResponsePayload({ status: 503, data: {}, headers: { 'x-request-id': id } })
    assert.equal(buildRequestFailureView(failure).diagnosticId, id)
    assert.deepEqual(Object.keys(failure).sort(), ['data', 'message', 'status'])
    assert.equal(JSON.stringify(failure).includes(id), false)
  }
  for (const id of ['BAD_ID', ID + '\n', [ID], '<svg/onload=alert(1)>', ID.toUpperCase()]) {
    const failure = buildResponsePayload({ status: 503, data: {}, headers: { 'x-request-id': id } })
    assert.equal(buildRequestFailureView(failure).diagnosticId, undefined)
  }
})
test('frozen errors, throwing getters and proxies do not leak or replace failure handling', () => {
  const error = Object.freeze(Object.assign(new Error('PRIVATE_ERROR'), { response: { status: 503, headers: { 'X-Request-ID': ID }, data: {} } }))
  captureTransportRequestError(error)
  assert.equal(buildRequestFailureView(error).diagnosticId, ID)
  const proxy = new Proxy({}, { get() { throw new Error('PRIVATE_GETTER') } })
  assert.equal(buildRequestFailureView(proxy).kind, 'unknown')
  assert.doesNotMatch(JSON.stringify(buildRequestFailureView(proxy)), /PRIVATE_/)
  assert.equal(buildRequestFailureView(null), null)
})
test('uncertain, login and spending failures cannot suggest replaying a paid operation', () => {
  for (const failure of [{ code: 'UNKNOWN_OUTCOME' }, { status: 401 }, { data: { code: 'INSUFFICIENT_BALANCE' } }, { data: { code: 'ACCESS_TOKEN_QUOTA_EXCEEDED' } }, { data: { error: { code: 'NO_ANSWER' } } }]) {
    assert.equal(buildRequestFailureView(failure).allowRetry, false)
  }
})

test('malformed rejection/return values retain visible generic failure without fake identity', () => {
  for (const v of [null, undefined, false, 0, 'PRIVATE_PRIMITIVE']) {
    const f = ensureRequestFailure(v)
    assert.equal(buildRequestFailureView(f).kind, 'unknown')
    assert.equal(buildRequestFailureView(f).diagnosticId, undefined)
    assert.doesNotMatch(JSON.stringify(f), /PRIVATE_PRIMITIVE/)
  }
  const original = buildResponsePayload({status:503,data:{},headers:{'x-request-id':ID}})
  assert.equal(ensureRequestFailure(original), original)
  assert.equal(buildRequestFailureView(ensureRequestFailure(original)).diagnosticId,ID)
})
