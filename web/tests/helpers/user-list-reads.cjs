// Keep the native user editor and its no-I/O guard. It reads the selected user's active ban
// when opened. Intercept only that documented lookup, at the point at which the component
// normally imports the API, so the fixture's real Axios configuration and all writes survive.
const assert = require('node:assert/strict')
const Module = require('node:module')
const path = require('node:path')
const ban = path.join(process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..'), 'src/api/UserBanApi')
const previous = Module._load
Module._load = function (request, parent, main) {
  const actual = previous.call(this, request, parent, main)
  if (request === ban || request === ban + '.js' || request === '@/api/UserBanApi' || request === '@/api/UserBanApi.js') return { ...actual, async getUserBans(params) {
    assert.ok(Number.isSafeInteger(params?.userId) && params.userId > 0)
    assert.deepEqual(params, { userId: params.userId, active: 'true', page: 1, pageSize: 1 })
    return { status: 200, data: { data: [], total: 0 } }
  } }
  return actual
}
