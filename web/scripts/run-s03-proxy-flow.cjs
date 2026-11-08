/* Required fixed-proxy local HTTP and native-page integration runner. Never installs
 * dependencies, contacts a provider, or uses the preview/original database. */
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { spawnSync } = require('node:child_process')
const root = path.resolve(__dirname, '../..')
const owned = fs.mkdtempSync(path.join(os.tmpdir(), 'tikuhelper-s03-'))
const binary = path.join(owned, process.platform === 'win32' ? 'controller.test.exe' : 'controller.test')
let status = 1
try {
  const go = process.env.TIKU_S03_GO_BINARY || 'go'
  const compile = spawnSync(go, ['test', '-c', '-p=2', '-o', binary, './internal/controller'], {
    cwd: root, stdio: 'inherit', timeout: 240000,
    env: { ...process.env, GOTOOLCHAIN: 'local', GOPROXY: 'off', GOSUMDB: 'off', GOMAXPROCS: process.env.GOMAXPROCS || '4' },
  })
  if (compile.error) throw compile.error
  if (compile.signal) throw new Error('S03 bridge compilation terminated: ' + compile.signal)
  if (compile.status !== 0) {
    status = compile.status || 1
  } else {
    const native = spawnSync(process.execPath, ['--test', 'web/tests/integration/IntelligentImportProxyFlow.test.cjs'], {
      cwd: root, stdio: 'inherit', timeout: 240000,
      env: { ...process.env, TIKU_S03_BRIDGE_BINARY: binary },
    })
    if (native.error) throw native.error
    if (native.signal) throw new Error('S03 native integration terminated: ' + native.signal)
    status = Number.isInteger(native.status) ? native.status : 1
  }
} catch (error) {
  process.stderr.write('S03 integration runner failed: ' + String(error.message) + '\n')
  status = 1
} finally {
  // Only this exact mkdtemp-owned directory may be removed, in one native API.
  const resolved = path.resolve(owned)
  if (path.dirname(resolved) !== path.resolve(os.tmpdir()) || !path.basename(resolved).startsWith('tikuhelper-s03-')) {
    process.stderr.write('S03 refused unsafe temporary cleanup target\n')
    status = status || 1
  } else {
    try { fs.rmSync(resolved, { recursive: true, force: true }) } catch (error) {
      process.stderr.write('S03 temporary cleanup failed: ' + String(error.message) + '\n')
      status = status || 1
    }
  }
}
process.exitCode = status
