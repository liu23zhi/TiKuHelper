import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
const web = process.env.TIKU_WEB_SOURCE || path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const { buildResponsePayload, captureTransportRequestError, getTransportRequestId } = await import(pathToFileURL(path.join(web,'src/utils/requestHelpers.mjs')).href);
const VALID = ["0123456789abcdef0123456789abcdef", "ffffffffffffffffffffffffffffffff", "fallback-0-0", "fallback-1792256000000000000-1", "fallback--1-1", "fallback-9223372036854775807-18446744073709551615", "fallback--9223372036854775808-0"];
const INVALID = ["", "0123456789ABCDEF0123456789ABCDEF", "0123456789abcdef0123456789abcde", "0123456789abcdef0123456789abcdef0", " 0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef ", "0123456789abcdef0123456789abcdef,11111111111111111111111111111111", "fallback--0-1", "fallback-00-1", "fallback-1-00", "fallback-+1-1", "fallback-1-+1", "fallback-1--1", "fallback-9223372036854775808-1", "fallback--9223372036854775809-1", "fallback-1-18446744073709551616", "fallback-1-1\n", "fallback-1-1\r", "0123456789abcdef0123456789abcdef\n", "0123456789abcdef0123456789abcdef\r", "fallback-１-1", "fallback-1-１", "not-a-server-id", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"];
const SERVER = VALID[0], CLIENT = '11111111111111111111111111111111', BUSINESS = '22222222222222222222222222222222';
const payloadWith = value => buildResponsePayload({ status: 200, data: { requestId: BUSINESS }, headers: { 'x-request-id': value } });

test('transport diagnostic accessor accepts exact current normal and bounded canonical fallback formats', () => {
  for (const id of VALID) assert.equal(getTransportRequestId(payloadWith(id)), id);
});
test('transport diagnostic accessor rejects aliases, bounds, arbitrary strings and nonstring header values', () => {
  for (const id of [...INVALID, null, undefined, 123, [SERVER], { id: SERVER }]) assert.equal(getTransportRequestId(payloadWith(id)), undefined);
});
test('transport diagnostic WeakMap leaves fulfilled payload keys, JSON, body reference and getter idempotence unchanged', () => {
  const data = { requestId: BUSINESS }, response = { status: 403, data, headers: { 'X-Request-ID': SERVER } };
  const value = buildResponsePayload(response), snapshot = Object.getOwnPropertyDescriptors(value);
  assert.deepEqual(Object.keys(value), ['data','status','message']);
  assert.equal(JSON.stringify(value), JSON.stringify({ data, status: 403, message: '' }));
  assert.equal(value.data, data);
  for (let i=0;i<5;i++) assert.equal(getTransportRequestId(value), SERVER);
  assert.deepEqual(Object.getOwnPropertyDescriptors(value), snapshot);
  for (const unknown of [undefined,null,1,'x',{},()=>{}]) assert.equal(getTransportRequestId(unknown), undefined);
});
test('transport diagnostic capture preserves frozen Error identity cause descriptors and returnable header failures', () => {
  const cause = Object.freeze({ kind: 'synthetic-cause' });
  const error = new Error('synthetic', { cause }); error.response = { headers: { 'x-request-id': SERVER } }; Object.freeze(error);
  const before = Object.getOwnPropertyDescriptors(error);
  assert.equal(captureTransportRequestError(error), error); assert.equal(getTransportRequestId(error), SERVER);
  assert.equal(error.cause, cause); assert.deepEqual(Object.getOwnPropertyDescriptors(error), before); assert.equal(Object.isFrozen(error), true);
  const throwing = new Error('synthetic'); throwing.response = { get headers() { throw new Error('synthetic diagnostic accessor failure'); } };
  assert.equal(captureTransportRequestError(throwing), throwing); assert.equal(getTransportRequestId(throwing), undefined);
});
test('transport diagnostic source controls ignore valid config request and business IDs and clear reused-error metadata', () => {
  const value = buildResponsePayload({ status: 200, data: { requestId: BUSINESS }, config: { headers: { 'x-request-id': CLIENT } }, request: { headers: { 'x-request-id': CLIENT } } });
  assert.equal(getTransportRequestId(value), undefined); assert.equal(value.data.requestId, BUSINESS);
  const error = new Error('synthetic'); error.config = { headers: { 'x-request-id': CLIENT } }; error.request = { headers: { 'x-request-id': CLIENT } };
  assert.equal(captureTransportRequestError(error), error); assert.equal(getTransportRequestId(error), undefined);
  error.response = { headers: { 'x-request-id': SERVER } }; captureTransportRequestError(error); assert.equal(getTransportRequestId(error), SERVER);
  error.response = { headers: {} }; captureTransportRequestError(error); assert.equal(getTransportRequestId(error), undefined);
  error.response = { headers: { 'x-request-id': SERVER, 'X-Request-ID': CLIENT } }; captureTransportRequestError(error); assert.equal(getTransportRequestId(error), undefined);
  error.response = { headers: { 'x-request-id': SERVER } }; captureTransportRequestError(error);
  Object.defineProperty(error, 'response', { configurable: true, get() { throw new Error('synthetic response accessor failure'); } });
  assert.equal(captureTransportRequestError(error), error); assert.equal(getTransportRequestId(error), undefined);
});
test('transport diagnostic AxiosHeaders-like read is exact bounded and never affects payload when a getter throws', () => {
  const data = { requestId: BUSINESS };
  const value = buildResponsePayload({ status: 200, data, headers: { get(name) { assert.equal(name,'x-request-id'); return SERVER; } } });
  assert.equal(getTransportRequestId(value), SERVER); assert.equal(value.data, data);
  const other = buildResponsePayload({ status: 200, data, headers: { get() { throw new Error('synthetic'); } } });
  assert.equal(getTransportRequestId(other), undefined); assert.equal(other.data, data);
});
