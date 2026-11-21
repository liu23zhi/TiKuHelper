// The real questions page mounts its transfer panel alongside the editor, table and drawer.
 // Those isolated UI fixtures only supply its precise initial read(s); writes and other APIs
 // still use the original transport and remain trapped by each fixture's no-network checks.
const assert = require('node:assert/strict')
const { isDeepStrictEqual } = require('node:util')
const { join } = require('node:path')
const Module = require('node:module')
module.exports = function isolateQuestionTransferReads(web) {
  const file = require.resolve(join(web, 'src/pages/IntelligentImport/asyncTaskApi.mjs'))
  const actual = require(file)
  const expected = [
    { type: 'import', resource: 'questions', pageSize: 20 }, { type: 'export', resource: 'questions', pageSize: 20 },
    { status: 'pending', resource: 'questions', pageSize: 1 }, { status: 'running', resource: 'questions', pageSize: 1 },
  ]
  const mock = new Module(file)
  mock.loaded = true
  mock.exports = { ...actual, async listAsyncTasks(params) {
    assert.ok(expected.some(shape => isDeepStrictEqual(shape, params)), 'unexpected question transfer read: ' + JSON.stringify(params))
    return { status: 200, data: { data: [], total: 0 } }
  } }
  require.cache[file] = mock
}
