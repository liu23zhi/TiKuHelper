import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

// Pure source-switch contract: actual cached module namespaces, no API/network/DB.
// ID-only provenance is not a claim that wrapped Errors gain HTTP classification.
const web = process.env.TIKU_WEB_SOURCE || path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const moduleURL = name => pathToFileURL(path.join(web, 'src/utils', name)).href;
const transport = await import(moduleURL('requestHelpers.mjs'));
const tickets = await import(moduleURL('ticketHelpers.mjs'));
const failureView = await import(moduleURL('requestFailureView.mjs'));
const SERVER = '0123456789abcdef0123456789abcdef';
const OTHER = 'fedcba9876543210fedcba9876543210';
const FORGED = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb';
const MESSAGE = '工单不存在';

function payload(headers = { 'x-request-id': SERVER }, status = 404) {
  return transport.buildResponsePayload({
    status,
    data: { message: MESSAGE, requestId: FORGED, diagnosticId: FORGED },
    headers,
  });
}

function businessError(response) {
  let error;
  try { tickets.requireTicketResponse(response); } catch (caught) { error = caught; }
  assert.ok(error instanceof Error, 'non-2xx keeps the existing thrown Error contract');
  return error;
}

function plainError(error, message) {
  assert.equal(error.constructor, Error);
  assert.equal(error.name, 'Error');
  assert.equal(error.message, message, 'business Error.message remains unchanged');
  assert.equal(error.status, undefined, 'ID-only bridge does not add HTTP status');
  assert.deepEqual(Reflect.ownKeys(error).sort(), ['message', 'stack']);
  for (const name of ['response', 'config', 'data', 'cause', 'diagnosticId', 'requestId']) {
    assert.equal(Object.hasOwn(error, name), false, 'no public/hidden own metadata field: ' + name);
  }
}

function inherit() {
  const fn = transport.inheritTransportRequestId;
  assert.equal(typeof fn, 'function', 'missing inheritTransportRequestId export; internal transfer boundaries not yet exercised');
  return fn;
}

test('actual known404 wrapper keeps Error/message/status/unknown kind and the original validated server ID', () => {
  const response = payload();
  const before = Object.getOwnPropertyDescriptors(response);
  assert.equal(transport.getTransportRequestId(response), SERVER);
  const error = businessError(response);
  plainError(error, MESSAGE);
  assert.deepEqual(Object.getOwnPropertyDescriptors(response), before);
  const view = failureView.buildRequestFailureView(error, '回复请求未完成，请核对后再操作。');
  assert.equal(view.kind, 'unknown', 'known404 remains its actual existing classification');
  assert.equal(transport.getTransportRequestId(error), SERVER, 'wrapper inherits exactly the transport-validated ID');
  assert.equal(view.diagnosticId, SERVER, 'existing failure view receives that same ID');
});

test('all original2xx statuses return the identical payload and body with unchanged diagnostics', () => {
  for (const status of [200, 202, 204, 299]) {
    const response = payload({ 'x-request-id': SERVER }, status);
    const body = response.data, before = Object.getOwnPropertyDescriptors(response);
    assert.equal(tickets.requireTicketResponse(response), response);
    assert.equal(response.data, body);
    assert.deepEqual(Object.getOwnPropertyDescriptors(response), before);
    assert.equal(transport.getTransportRequestId(response), SERVER);
  }
});

test('missing invalid duplicate and nonstring headers cannot promote a forged body ID through the wrapper', () => {
  for (const headers of [null, { 'x-request-id': 'invalid' }, { 'x-request-id': [SERVER] }, { 'x-request-id': SERVER, 'X-Request-ID': OTHER }]) {
    const response = payload(headers);
    assert.equal(transport.getTransportRequestId(response), undefined);
    const error = businessError(response);
    plainError(error, MESSAGE);
    assert.equal(transport.getTransportRequestId(error), undefined);
    assert.equal(failureView.buildRequestFailureView(error).diagnosticId, undefined);
  }
});

test('ID transfer accepts a genuinely captured source and frozen Error, and clears a reused target without own-property changes', () => {
  const transfer = inherit();
  const source = new Error('synthetic transport source');
  source.response = { status: 404, headers: { 'x-request-id': SERVER } };
  assert.equal(transport.captureTransportRequestError(source), source);
  const target = Object.freeze(new Error('synthetic target'));
  const frozenBefore = Object.getOwnPropertyDescriptors(target);
  assert.equal(transfer(target, source), target);
  assert.equal(transport.getTransportRequestId(target), SERVER);
  assert.deepEqual(Object.getOwnPropertyDescriptors(target), frozenBefore);
  assert.equal(Object.isFrozen(target), true);
  plainError(target, 'synthetic target');
  const reused = new Error('synthetic reused target');
  const reusedBefore = Object.getOwnPropertyDescriptors(reused);
  assert.equal(transfer(reused, payload()), reused);
  assert.equal(transport.getTransportRequestId(reused), SERVER);
  assert.equal(transfer(reused, payload(null)), reused);
  assert.equal(transport.getTransportRequestId(reused), undefined, 'missing new source metadata retires the old target ID');
  assert.deepEqual(Object.getOwnPropertyDescriptors(reused), reusedBefore);
});

test('same target/source reads the existing validated WeakMap entry before deleting old target metadata', () => {
  const transfer = inherit();
  const same = Object.freeze(payload());
  const before = Object.getOwnPropertyDescriptors(same);
  assert.equal(transfer(same, same), same);
  assert.equal(transport.getTransportRequestId(same), SERVER);
  assert.deepEqual(Object.getOwnPropertyDescriptors(same), before);
});

test('primitive targets/sources and a throwing-property source are safe no-ops with no property reads', () => {
  const transfer = inherit(), source = payload();
  for (const value of [undefined, null, false, 0, 1n, 'synthetic', Symbol('synthetic')]) {
    assert.equal(transfer(value, source), value);
    assert.equal(transport.getTransportRequestId(value), undefined);
    const target = new Error('synthetic target');
    transfer(target, source);
    transfer(target, value);
    assert.equal(transport.getTransportRequestId(target), undefined);
  }
  let reads = 0;
  const throwing = Object.create(null);
  for (const name of ['headers', 'response', 'data', 'status', 'requestId', 'diagnosticId']) {
    Object.defineProperty(throwing, name, { get() { reads += 1; throw new Error('unexpected source property read'); } });
  }
  Object.freeze(throwing);
  const target = new Error('synthetic target');
  transfer(target, source);
  assert.equal(transfer(target, throwing), target);
  assert.equal(transport.getTransportRequestId(target), undefined);
  assert.equal(reads, 0, 'transfer consults the existing private WeakMap only');
  plainError(target, 'synthetic target');
});

test('transport-looking properties are not reauthenticated and existing source metadata outranks later properties', () => {
  const transfer = inherit(), target = new Error('synthetic target');
  const fake = { headers: { 'x-request-id': SERVER }, response: { headers: { 'x-request-id': SERVER } }, data: { requestId: SERVER }, requestId: SERVER };
  transfer(target, fake);
  assert.equal(transport.getTransportRequestId(target), undefined);
  const source = payload();
  let reads = 0;
  for (const name of ['headers', 'response', 'requestId', 'diagnosticId']) {
    Object.defineProperty(source, name, { get() { reads += 1; return { 'x-request-id': OTHER }; } });
  }
  Object.freeze(source);
  transfer(target, source);
  assert.equal(transport.getTransportRequestId(target), SERVER);
  assert.equal(reads, 0, 'even a validated source is not recaptured from arbitrary properties');
  transfer(target, fake);
  assert.equal(transport.getTransportRequestId(target), undefined);
  plainError(target, 'synthetic target');
});
