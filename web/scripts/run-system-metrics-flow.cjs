/* Current Go handler JSON -> real API/request/native Panel. No fallback snapshots. */
const fs = require('node:fs')
const path = require('node:path')
const os = require('node:os')
const crypto = require('node:crypto')
const { spawnSync } = require('node:child_process')
const root = path.resolve(__dirname, '../..')
const sha = value => crypto.createHash('sha256').update(value).digest('hex')

function run(binary, args, env, timeout) {
  const result = spawnSync(binary, args, { cwd: root, env, encoding: 'utf8', timeout, maxBuffer: 4 * 1024 * 1024 })
  if (result.stdout) process.stdout.write(result.stdout)
  if (result.stderr) process.stderr.write(result.stderr)
  if (result.error) throw result.error
  const status = Number.isInteger(result.status) && result.status >= 0 ? result.status : 1
  console.log('METRICS_FLOW_COMMAND ' + JSON.stringify({ binary, args, exit: status }))
  return status
}

function flow(temp) {
    const output = path.join(temp, 'producer.json')
    const env = {
      ...process.env, GOTOOLCHAIN: 'local', GOPROXY: 'off', GOSUMDB: 'off', CGO_ENABLED: '0',
      GOMAXPROCS: process.env.GOMAXPROCS || '4', TIKU_METRICS_FLOW_OUTPUT: output,
      NODE_PATH: path.join(root, 'web/node_modules'),
    }
    const go = env.TIKU_METRICS_FLOW_GO_BINARY || 'go'
    const producer = run(go, ['test', '-p=2', './internal/controller', '-run', '^TestSystemMetricsFlowProducer$', '-count=1', '-v'], env, 120000)
    if (producer !== 0) return producer
    if (!fs.lstatSync(output).isFile() || fs.lstatSync(output).isSymbolicLink()) throw new Error('missing regular actual producer JSON')
    const raw = fs.readFileSync(output)
    const data = JSON.parse(raw)
    if (data.schemaVersion !== 1 || !Array.isArray(data.cases) || data.cases.length !== 3) throw new Error('unsupported producer envelope')
    const names = new Set(data.cases.map(item => item.name))
    if (names.size !== 3 || !['empty', 'populated', 'forbidden'].every(name => names.has(name))) throw new Error('missing actual producer cases')
    for (const item of data.cases) {
      if (item.status !== (item.name === 'forbidden' ? 403 : 200) || !item.body || typeof item.body !== 'object' || !item.headers || typeof item.headers !== 'object') throw new Error('malformed producer response')
    }
    const digest = sha(raw)
    console.log('METRICS_FLOW_ACTUAL_PRODUCER_SHA256 ' + digest)
    const evidence = env.TIKU_METRICS_FLOW_EVIDENCE
    if (evidence) {
      if (!path.isAbsolute(evidence) || !fs.statSync(evidence).isDirectory()) throw new Error('evidence must be an existing absolute directory')
      fs.copyFileSync(output, path.join(evidence, 'producer.json'), fs.constants.COPYFILE_EXCL)
      fs.writeFileSync(path.join(evidence, 'producer.sha256'), digest + '\n', { flag: 'wx' })
    }
    const consumerEnv = { ...env, TIKU_METRICS_FLOW_INPUT: output }
    return run(env.TIKU_METRICS_FLOW_NODE_BINARY || process.execPath, ['--test', path.join(root, 'web/tests/integration/SystemMetricsFlowAnt4.test.cjs')], consumerEnv, 90000)
}

function main() {
  const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'tikuhelper-metrics-flow-'))
  const marker = path.join(temp, '.owned')
  fs.writeFileSync(marker, 'system-metrics-flow owned temporary context\n')
  let status = 1
  try { status = flow(temp) } catch (error) {
    console.error('System metrics flow failed: ' + error.message)
  }
  try {
    if (path.dirname(fs.realpathSync(temp)) !== fs.realpathSync(os.tmpdir()) || !path.basename(temp).startsWith('tikuhelper-metrics-flow-') || fs.readFileSync(marker, 'utf8') !== 'system-metrics-flow owned temporary context\n') throw new Error('refused unsafe metrics temporary cleanup')
    fs.rmSync(temp, { recursive: true, force: false })
  } catch (error) {
    console.error('System metrics cleanup failed: ' + error.message)
    status = 1
  }
  return status
}

try { process.exitCode = main() } catch (error) {
  console.error('System metrics flow failed: ' + error.message)
  process.exitCode = 1
}
