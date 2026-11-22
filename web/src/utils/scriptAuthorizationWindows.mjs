// Date-specific windows use the same boundaries as the server. Adjacent windows
// are valid; every interval shares the authorization's one usage counter.
function timePoint(value) {
  const raw = String(value || '')
  const milliseconds = raw ? Date.parse(raw) : NaN
  // Date retains milliseconds only. Keep the remaining RFC3339 nanoseconds for
  // comparisons and preserve untouched API boundaries in the outgoing payload.
  const fraction = raw.match(/\.(\d{1,9})(?:Z|[+-]\d{2}:\d{2})?$/)?.[1] || ''
  const subMillisecond = Number(fraction.slice(3).padEnd(6, '0'))
  const rfc3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(raw)
  return { milliseconds, subMillisecond, value: rfc3339 ? raw : Number.isFinite(milliseconds) ? new Date(milliseconds).toISOString() : '' }
}
function compareTime(a, b) { return a.milliseconds - b.milliseconds || a.subMillisecond - b.subMillisecond }

export function validateScriptAuthorizationWindows(entries, { now = Date.now(), maxDurationMinutes = 1440 } = {}) {
  const fail = error => ({ windows: [], error })
  if (!Array.isArray(entries) || !entries.length) return fail('请至少添加一个生效时段。')
  if (entries.length > 32) return fail('最多可以设置 32 个生效时段。')
  const windows = []
  for (const [index, entry] of entries.entries()) {
    const start = timePoint(entry?.startsAt), end = timePoint(entry?.expiresAt)
    if (!Number.isFinite(start.milliseconds) || !Number.isFinite(end.milliseconds)) return fail(`请填写时段 ${index + 1} 的开始与结束时间。`)
    if (compareTime(end, start) <= 0) return fail(`时段 ${index + 1} 的结束时间必须晚于开始时间。`)
    if (compareTime(end, { ...start, milliseconds: start.milliseconds + maxDurationMinutes * 60000 }) > 0) return fail(`时段 ${index + 1} 超过最长时长（${maxDurationMinutes === 1440 ? '24 小时' : '365 天'}）。`)
    if (compareTime(start, { milliseconds: now + 365 * 86400000, subMillisecond: 0 }) > 0) return fail(`时段 ${index + 1} 的开始时间不能晚于未来 365 天。`)
    windows.push({ startsAt: start.value, expiresAt: end.value, start, end })
  }
  windows.sort((a, b) => compareTime(a.start, b.start))
  if (windows.some((window, index) => index > 0 && compareTime(window.start, windows[index - 1].end) < 0)) return fail('生效时段不能重叠，可以首尾相接。')
  if (!windows.some(window => compareTime(window.end, { milliseconds: now, subMillisecond: 0 }) > 0)) return fail('请至少设置一个尚未结束的生效时段。')
  return { windows: windows.map(({ startsAt, expiresAt }) => ({ startsAt, expiresAt })), error: '' }
}
