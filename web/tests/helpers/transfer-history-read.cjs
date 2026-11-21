// A list page can also mount the independent admin export panel. During a
// principal/filter test, stub only its opt-in list read; all writes and all
// other transports keep the fixture's normal network-deny behavior.
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
module.exports = function fixtureTransferHistory(web, resources) {
  const page = () => ({ status: 200, data: { data: [], total: 0 } })
  function replaceRead(rel, replacements) {
    const file = path.join(web, rel), original = require(file), m = new Module(file)
    m.loaded = true; m.exports = { ...original, ...replacements }; require.cache[file] = m
  }
  replaceRead('src/pages/IntelligentImport/asyncTaskApi.mjs', { listAsyncTasks: async ({ resource, page: current, pageSize }) => {
    assert.ok(resources.includes(resource), 'unexpected transfer resource: ' + resource)
    assert.ok(current >= 1 && pageSize > 0)
    return page()
  } })
  if (resources.includes('questions')) {
    replaceRead('src/pages/TikuComponent/questionTransferApi.mjs', { listQuestionTransferTasks: async () => page() })
  }
}
