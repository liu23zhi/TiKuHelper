// The token page reads the current user's level after a successful list load.
// Do not let that independent read escape a token-only, no-network fixture.
const path = require('node:path'), Module = require('node:module')
const levelApi = { getMyUserLevel: async () => ({ status: 200, data: { data: { deviceLimit: 3 } } }) }
exports.levelApi = levelApi
exports.install = web => {
  const filename = path.join(web, 'src/api/UserLevelApi.js'), mod = new Module(filename)
  mod.loaded = true; mod.exports = levelApi; require.cache[filename] = mod
}
