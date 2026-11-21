import test from 'node:test'
import assert from 'node:assert/strict'
import { readAdminBindingHistory } from './bindingHistoryHelpers.mjs'

test('nested admin binding response is recognized as successful history', () => {
  const rows = [{ id: 7, state: 'active', valueMasked: '138****0000' }]
  assert.deepEqual(readAdminBindingHistory({ status: 200, data: { bindings: { items: rows } } }), { ok: true, rows })
})

test('a successful empty response is distinct from a failed request', () => {
  assert.deepEqual(readAdminBindingHistory({ status: 200, data: { bindings: { items: [] } } }), { ok: true, rows: [] })
  assert.deepEqual(readAdminBindingHistory({ status: 503, data: { bindings: { items: [] } } }), { ok: false, rows: [] })
  assert.deepEqual(readAdminBindingHistory({ status: 200, data: { bindings: {} } }), { ok: false, rows: [] })
})
