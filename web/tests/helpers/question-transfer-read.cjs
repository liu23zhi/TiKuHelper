// The real questions page mounts its transfer panel alongside the editor, table and drawer.
 // Those isolated UI fixtures only supply its precise initial read(s); writes and other APIs
 // still use the original transport and remain trapped by each fixture's no-network checks.
const assert = require('node:assert/strict')
const { join } = require('node:path')
const Module = require('node:module')
module.exports = function isolateQuestionTransferReads(web) {
  const file = require.resolve(join(web, 'src/pages/TikuComponent/questionTransferApi.mjs'))
  const actual = require(file)
  const mock = new Module(file)
  mock.loaded = true
  mock.exports = { ...actual, async listQuestionTransferTasks(...args) {
    assert.deepEqual(args, [], 'only the default readonly question-transfer history query is expected')
    return { status: 200, data: { data: [], total: 0 } }
  } }
  require.cache[file] = mock
}
