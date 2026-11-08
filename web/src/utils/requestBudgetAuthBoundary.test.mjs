import test from 'node:test'
import assert from 'node:assert/strict'
import path from 'node:path'
import { readFile } from 'node:fs/promises'
import { fileURLToPath, pathToFileURL } from 'node:url'
const utils = process.env.FRONTEND_UTILS_ROOT || path.dirname(fileURLToPath(import.meta.url))
const H = await import(pathToFileURL(path.join(utils, 'requestHelpers.mjs')))
const quotaMessage = '当前 Key 累计额度已用尽，请调整该 Key 的额度上限或更换 Key 后重试。'
const balanceMessage = '当前账户余额不足，请充值后重试。'
const expired = { shouldLogout: true, userMessage: '当前登陆过期，请重新登陆。' }
const quota = data => ({ status: 401, data: { code: 'ACCESS_TOKEN_QUOTA_EXCEEDED', ...data } })
const balance = data => ({ status: 401, data: { code: 'INSUFFICIENT_BALANCE', ...data } })
test('exact quota machinecode without legacy detail preserves the session and explains Key remediation', () => {
  assert.deepEqual(H.buildAuthFailureHandling(quota({})), { shouldLogout: false, userMessage: quotaMessage })
})
test('exact quota machinecode overrides generic auth detail with an actionable Key message', () => {
  const result = H.buildAuthFailureHandling(quota({ detail: '访问令牌鉴权失败', message: 'unauthorized' }))
  assert.deepEqual(result, { shouldLogout: false, userMessage: quotaMessage })
  assert.match(result.userMessage, /调整.*额度上限/)
  assert.match(result.userMessage, /更换 Key/)
})
test('quota denial is not reduced to a wallet recharge instruction even with the old balance text', () => {
  const result = H.buildAuthFailureHandling(quota({ detail: '当前余额不足，系统题库已禁用，请充值后再使用' }))
  assert.deepEqual(result, { shouldLogout: false, userMessage: quotaMessage })
  assert.doesNotMatch(result.userMessage, /充值/)
})
test('exact insufficient balance machinecode alone preserves the session and offers wallet remediation', () => {
  assert.deepEqual(H.buildAuthFailureHandling(balance({})), { shouldLogout: false, userMessage: balanceMessage })
})
test('exact insufficient balance machinecode does not depend on historical detail keywords', () => {
  assert.deepEqual(H.buildAuthFailureHandling(balance({ detail: 'available amount is zero', message: 'access denied' })), { shouldLogout: false, userMessage: balanceMessage })
})
test('unknown malformed codes and free quota prose do not bypass ordinary 401 logout', () => {
  for (const code of [undefined, null, 'QUOTA_EXCEEDED', 'ACCESS_TOKEN_QUOTA_EXCEEDED_EXTRA', 'access_token_quota_exceeded', ' ACCESS_TOKEN_QUOTA_EXCEEDED', 'ACCESS_TOKEN_QUOTA_EXCEEDED\n', 'INSUFFICIENT_BALANCE_EXTRA', ['INSUFFICIENT_BALANCE'], { toString: () => 'INSUFFICIENT_BALANCE' }]) {
    assert.deepEqual(H.buildAuthFailureHandling({ status: 401, data: { code, detail: 'quota exceeded; budget unavailable', message: 'quota limit' } }), expired)
  }
})
test('unknown JWT and expired-session responses still request logout', () => {
  for (const code of ['JWT_EXPIRED', 'TOKEN_EXPIRED', 'UNAUTHORIZED', '']) assert.deepEqual(H.buildAuthFailureHandling({ status: 401, data: { code, detail: 'JWT token expired' } }), expired)
})
test('permission 403 retains established detail instead of a Key budget message', () => {
  assert.deepEqual(H.buildAuthFailureHandling({ status: 403, data: { code: 'ACCESS_TOKEN_QUOTA_EXCEEDED', detail: '当前账号无权修改' } }), { shouldLogout: false, userMessage: '当前账号无权修改' })
})
test('empty permission 403 retains established fallback even when a budget code is present', () => {
  assert.deepEqual(H.buildAuthFailureHandling({ status: 403, data: { code: 'INSUFFICIENT_BALANCE' } }), { shouldLogout: false, userMessage: '当前账号无权执行此操作。' })
})
test('budget-code exception is constrained to numeric HTTP401', () => {
  for (const status of [undefined, 200, 400, 500, '401']) for (const code of ['ACCESS_TOKEN_QUOTA_EXCEEDED', 'INSUFFICIENT_BALANCE']) assert.deepEqual(H.buildAuthFailureHandling({ status, data: { code } }), expired)
})
test('existing explicit legacy balance and captcha behavior stays unchanged', () => {
  for (const detail of ['当前余额不足，系统题库已禁用，请充值后再使用', '当前余额不足，默认账号已停用', '请先完成人机验证', '图形验证码错误']) assert.deepEqual(H.buildAuthFailureHandling({ status: 401, data: { detail } }), { shouldLogout: false, userMessage: detail })
})
test('budget handling does not mutate backend payloads diagnostics or new quota fields', () => {
  const data = Object.freeze({ code: 'ACCESS_TOKEN_QUOTA_EXCEEDED', detail: 'server quota detail', remainingQuota: '0.000000', totalQuotaLimit: '9007199254740993', searchMode: 'deep' })
  const response = Object.freeze({ status: 401, data, headers: Object.freeze({ 'x-request-id': 'a'.repeat(32) }) })
  H.buildAuthFailureHandling(response)
  const payload = H.buildResponsePayload(response)
  assert.deepEqual(payload, { data, status: 401, message: 'server quota detail' })
  assert.equal(payload.data, data)
  assert.equal(H.getTransportRequestId(payload), 'a'.repeat(32))
  assert.deepEqual(Object.keys(payload), ['data', 'status', 'message'])
})
test('unchanged real request consumer clears storage and navigates only inside shouldLogout', async () => {
  const raw = await readFile(path.join(utils, 'request.js'), 'utf8')
  assert.match(raw, /const handling = buildAuthFailureHandling\(response\)/)
  assert.match(raw, /if \(handling\.shouldLogout\) \{\s*clearStoredCurrentUser\(\)\s*window\.location\.href = '\/login'/)
  assert.equal(H.buildAuthFailureHandling(quota({})).shouldLogout, false)
  assert.equal(H.buildAuthFailureHandling(balance({})).shouldLogout, false)
})
