import test from 'node:test'
import assert from 'node:assert/strict'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { readFile } from 'node:fs/promises'
const utils = process.env.FRONTEND_UTILS_ROOT || path.dirname(fileURLToPath(import.meta.url))
const H = await import(pathToFileURL(path.join(utils, 'requestHelpers.mjs')))
const requireDiagnostics = () => {
  assert.equal(typeof H.getTransportRequestId, 'function', 'transport request ID accessor must exist')
  assert.equal(typeof H.captureTransportRequestError, 'function', 'original-error diagnostic capture must exist')
}
test('integration HTTP retains public WeChat registration isolation', () => {
  assert.equal(H.isPublicWechatRegisterRequest('/adapter-service/public/wechat-register/session'), true)
  assert.equal(H.isPublicWechatRegisterRequest('/adapter-service/user/login'), false)
})
test('integration HTTP ordinary expired 401 still requests logout', () => {
  assert.equal(H.buildAuthFailureHandling({status:401,data:{message:'unauthorized'}}).shouldLogout,true)
})
test('integration HTTP balance/captcha refusals retain the session', () => {
  for (const detail of ['当前余额不足，系统题库已禁用，请充值后再使用','请先完成人机验证'])
    assert.equal(H.buildAuthFailureHandling({status:401,data:{detail}}).shouldLogout,false)
})
test('integration HTTP permission 403 does not clear an authenticated session', () => {
  assert.deepEqual(H.buildAuthFailureHandling({status:403,data:{detail:'当前账号无权修改'}}),{shouldLogout:false,userMessage:'当前账号无权修改'})
})
test('integration HTTP empty 403 uses a safe permission message without logout', () => {
  assert.deepEqual(H.buildAuthFailureHandling({status:403,data:{}}),{shouldLogout:false,userMessage:'当前账号无权执行此操作。'})
})
test('integration HTTP retains upstream payload shape and newly added business fields', () => {
  const data={message:'temporary failure',detail:'可恢复说明',stagePricingRules:[{threshold:'9007199254740993',multiplier:0.75}],effectiveMultiplier:'0.75x'}
  const payload=H.buildResponsePayload({status:503,data})
  assert.deepEqual(payload,{data,status:503,message:'可恢复说明'})
  assert.equal(payload.data,data,'diagnostics never project or discard new upstream data')
})
test('integration HTTP request IDs remain out of enumerable payloads and JSON', () => {
  requireDiagnostics()
  const id='a'.repeat(32),payload=H.buildResponsePayload({status:200,data:{ok:true},headers:{'X-Request-ID':id}})
  assert.equal(H.getTransportRequestId(payload),id)
  assert.deepEqual(Object.keys(payload),['data','status','message'])
  assert.equal(JSON.stringify(payload).includes(id),false)
})
test('integration HTTP fallback ID integer bounds are checked without Number coercion', () => {
  requireDiagnostics()
  for (const id of ['fallback-9223372036854775807-18446744073709551615','fallback--9223372036854775808-0']) {
    const payload=H.buildResponsePayload({status:500,data:{},headers:{get:n=>n==='x-request-id'?id:undefined}})
    assert.equal(H.getTransportRequestId(payload),id)
  }
})
test('integration HTTP malformed duplicate unsafe request IDs never become diagnostics', () => {
  requireDiagnostics()
  for (const id of ['A'.repeat(32),'a'.repeat(31),'bad\nvalue','fallback--0-0','fallback-01-1','fallback-9223372036854775808-0','fallback-0-18446744073709551616'])
    assert.equal(H.getTransportRequestId(H.buildResponsePayload({status:500,data:{},headers:{'x-request-id':id}})),undefined)
  assert.equal(H.getTransportRequestId(H.buildResponsePayload({status:500,data:{},headers:{'x-request-id':'a'.repeat(32),'X-Request-Id':'b'.repeat(32)}})),undefined)
})
test('integration HTTP frozen original errors retain identity and stale IDs are cleared', () => {
  requireDiagnostics()
  let response={headers:{'x-request-id':'a'.repeat(32)}}
  const error=Object.freeze(Object.defineProperty(new Error('safe fixture'), 'response',{get:()=>response}))
  const before=Object.getOwnPropertyDescriptors(error)
  assert.equal(H.captureTransportRequestError(error),error)
  assert.equal(H.getTransportRequestId(error),'a'.repeat(32))
  response={headers:{get(){throw new Error('header accessor fixture')}}}
  assert.equal(H.captureTransportRequestError(error),error)
  assert.equal(H.getTransportRequestId(error),undefined)
  assert.deepEqual(Object.getOwnPropertyDescriptors(error),before)
})
test('integration HTTP final responses do not console-log private backend payloads', async () => {
  const raw=await readFile(path.join(utils,'request.js'),'utf8')
  assert.doesNotMatch(raw,/console\.(?:log|error|warn)\s*\(/)
  assert.match(raw,/await executeWithRetry\(/,'keep upstream retry policy owner')
  assert.match(raw,/toUserRequestError\(/,'keep upstream unknown-mutation outcome handling')
})
