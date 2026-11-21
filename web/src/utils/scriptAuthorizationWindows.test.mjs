import test from 'node:test'
import assert from 'node:assert/strict'
import { validateScriptAuthorizationWindows } from './scriptAuthorizationWindows.mjs'

const now = Date.parse('2026-11-21T10:00:00Z')
const at = minutes => new Date(now + minutes * 60000).toISOString()
const window = (start, end) => ({ startsAt: at(start), expiresAt: at(end) })

test('authorization windows normalize dates and order while allowing adjacent intervals', () => {
  assert.deepEqual(validateScriptAuthorizationWindows([window(120, 180), window(0, 60), window(60, 90)], { now, maxDurationMinutes: 1440 }), {
    windows: [window(0, 60), window(60, 90), window(120, 180)], error: '',
  })
})

test('authorization windows reject empty, incomplete, reversed and overlapping schedules', () => {
  for (const windows of [[], [{ startsAt: at(1), expiresAt: '' }], [window(10, 10)], [window(20, 10)], [window(0, 60), window(59, 80)]]) {
    const result = validateScriptAuthorizationWindows(windows, { now, maxDurationMinutes: 1440 })
    assert.ok(result.error, JSON.stringify(windows))
    assert.deepEqual(result.windows, [])
  }
})

test('authorization windows enforce the per-command limit, future horizon and 32 interval cap', () => {
  assert.equal(validateScriptAuthorizationWindows([window(1, 1441)], { now, maxDurationMinutes: 1440 }).error, '')
  assert.ok(validateScriptAuthorizationWindows([window(1, 1442)], { now, maxDurationMinutes: 1440 }).error)
  assert.equal(validateScriptAuthorizationWindows([window(1, 525601)], { now, maxDurationMinutes: 525600 }).error, '')
  assert.ok(validateScriptAuthorizationWindows([window(1, 525602)], { now, maxDurationMinutes: 525600 }).error)
  assert.ok(validateScriptAuthorizationWindows([window(525601, 525602)], { now, maxDurationMinutes: 1440 }).error)
  assert.equal(validateScriptAuthorizationWindows(Array.from({ length: 32 }, (_, i) => window(i * 2, i * 2 + 1)), { now, maxDurationMinutes: 1440 }).error, '')
  assert.ok(validateScriptAuthorizationWindows(Array.from({ length: 33 }, (_, i) => window(i * 2, i * 2 + 1)), { now, maxDurationMinutes: 1440 }).error)
})

test('authorization windows require a current or future end but retain expired intervals beside a future one', () => {
  const expired = validateScriptAuthorizationWindows([window(-120, -60), window(-30, 0)], { now, maxDurationMinutes: 1440 })
  assert.match(expired.error, /尚未结束/)
  assert.deepEqual(expired.windows, [])
  assert.equal(validateScriptAuthorizationWindows([window(-120, -60), window(60, 120)], { now, maxDurationMinutes: 1440 }).error, '')
})

test('existing RFC3339 boundaries retain nanosecond precision and compare adjacent sub-millisecond windows', () => {
  const windows = [
    { startsAt: '2026-11-21T11:00:31.123456789Z', expiresAt: '2026-11-21T11:00:31.123456790Z' },
    { startsAt: '2026-11-21T11:00:31.123456790Z', expiresAt: '2026-11-21T11:00:59.987654321Z' },
  ]
  assert.deepEqual(validateScriptAuthorizationWindows(windows, { now, maxDurationMinutes: 1440 }), { windows, error: '' })
})
