/* Exam-client DB tool (administrator-only) front-end contract.
 *
 * What must survive future edits:
 *  1. Non-admins never see the tool, never read its version, never upload;
 *  2. The download URL is the real admin route (a typo here silently 404s in the browser,
 *     which no unit test of the API module would catch);
 *  3. The 16 MiB client-side cap matches the server cap, and 413/400 map to actionable text;
 *  4. The usage instructions name the actual build artifact (userexam-crypto.exe) — the
 *     page previously said "single-file exe" while the backend served a differently named
 *     file, so a literal drift here is a real delivery bug;
 *  5. The page never asks the server to decrypt: no request may carry a password.
 *
 * Boundaries mocked: the UserExamToolApi module only. Vue, Ant Design Vue, vue-router and
 * the currentUser helpers are real.
 */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(root, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(root, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))

const ADMIN = { id: 91601, role: 'admin', username: 'SYNTHETIC_EXAM_ADMIN', status: 'enabled' }
const PLAIN = { id: 91602, role: 'user', username: 'SYNTHETIC_EXAM_USER', status: 'enabled' }

const calls = { version: [], inspect: [], downloadURL: [] }
let versionPayload = null
let inspectHandler = null
const wrappers = []
const notices = []

const ok = data => ({ status: 200, data })

const apiPath = path.join(root, 'src/api/UserExamToolApi.js')
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  getUserExamToolVersion() {
    calls.version.push(true)
    return Promise.resolve(versionPayload)
  },
  getUserExamToolDownloadURL() {
    calls.downloadURL.push(true)
    return '/adapter-service/admin/userexam/tool'
  },
  inspectUserExamDatabase(file) {
    calls.inspect.push(file)
    return inspectHandler ? inspectHandler(file) : Promise.resolve(ok({ tables: {}, otherTables: [], rowCounts: {} }))
  },
}
require.cache[apiPath] = api

for (const kind of ['success', 'error', 'warning']) {
  const original = Antd.message[kind].bind(Antd.message)
  Antd.message[kind] = (...args) => { notices.push({ kind, text: String(args[0]?.content || args[0] || '') }); return original(...args) }
}

const Page = require(path.join(root, 'src/pages/UserExamTool/index.vue'))

async function settle() { await nextTick(); await flushPromises(); await nextTick() }

async function mountPage(actor) {
  user.setStoredCurrentUser(actor)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/admin/user-exam-tool', component: Page }] })
  router.push('/adapter/admin/user-exam-tool')
  await router.isReady()
  const wrapper = mount(Page, { global: { plugins: [Antd, router] } })
  wrappers.push(wrapper)
  await settle()
  return wrapper
}

function fileInput(wrapper) { return wrapper.find('input[type="file"]') }

function attachFile(wrapper, name, size) {
  const input = fileInput(wrapper).element
  const file = new File([new Uint8Array(1)], name, { type: 'application/octet-stream' })
  Object.defineProperty(file, 'size', { configurable: true, value: size })
  Object.defineProperty(input, 'files', { configurable: true, value: [file] })
  return { input, file }
}

afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) if (wrapper.exists()) wrapper.unmount()
  await settle()
  calls.version.length = 0
  calls.inspect.length = 0
  calls.downloadURL.length = 0
  notices.length = 0
  versionPayload = null
  inspectHandler = null
  user.clearStoredCurrentUser()
  Antd.message.destroy()
  document.body.innerHTML = ''
})

test('non-admin sees the refusal and never calls the admin endpoints', async () => {
  const wrapper = await mountPage(PLAIN)
  assert.equal(calls.version.length, 0, 'non-admin must not read the tool version')
  assert.equal(fileInput(wrapper).exists(), false, 'non-admin must not get an upload control')
  assert.match(wrapper.text(), /无该页面权限/)
  assert.doesNotMatch(wrapper.text(), /下载工具/)
  console.log('EXAM_TOOL_GATE_OK non-admin: no version read, no upload control, refusal shown')
})

test('admin reads the version, and the download button points at the real admin route', async () => {
  versionPayload = ok({
    available: true, filename: 'userexam-crypto.exe', version: '1.1.0',
    sha256: 'a'.repeat(64), sizeBytes: 15498901,
    cipher: 'aes256cbc', legacy: '0', kdfIter: 4001,
  })
  const wrapper = await mountPage(ADMIN)
  assert.equal(calls.version.length, 1)
  const text = wrapper.text()
  assert.match(text, /已部署/)
  assert.match(text, /userexam-crypto\.exe/)
  assert.match(text, /14\.78 MB/, 'size must be rendered in MB')
  assert.match(text, /aes256cbc \/ legacy=0 \/ kdf_iter=4001/, 'cipher params must be shown verbatim')
  const download = wrapper.findAll('a').find(node => /下载工具/.test(node.text()))
  assert.ok(download, 'download button exists')
  assert.equal(download.attributes('href'), '/adapter-service/admin/userexam/tool')
  assert.equal(download.attributes('disabled'), undefined)
  console.log('EXAM_TOOL_VERSION_OK admin: version read, cipher params shown, download href is the admin route')
})

test('download stays disabled when the server has no deployed tool', async () => {
  versionPayload = ok({ available: false, filename: 'userexam-crypto.exe', reason: '服务端未部署脱机工具（未配置 USEREXAM_TOOL_PATH）' })
  const wrapper = await mountPage(ADMIN)
  assert.match(wrapper.text(), /未部署/)
  assert.match(wrapper.text(), /USEREXAM_TOOL_PATH/, 'the reason must reach the operator')
  const download = wrapper.findAll('a').find(node => /下载工具/.test(node.text()))
  assert.ok(download)
  assert.notEqual(download.attributes('disabled'), undefined, 'download must be disabled when unavailable')
  console.log('EXAM_TOOL_UNDEPLOYED_OK unavailable tool: reason shown, download disabled')
})

test('the usage instructions name the real artifact and the real flags', async () => {
  versionPayload = ok({ available: true, filename: 'userexam-crypto.exe', cipher: 'aes256cbc', legacy: '0', kdfIter: 4001 })
  const wrapper = await mountPage(ADMIN)
  // Scope to the instructions block: the version panel also renders the filename, so a
  // page-wide match would pass even if the instructions named a nonexistent artifact.
  const usage = wrapper.find('.usage-steps')
  assert.equal(usage.exists(), true, 'the usage instructions block must exist')
  const text = usage.text()
  assert.match(text, /userexam-crypto\.exe/, 'instructions must name the real build artifact')
  assert.doesNotMatch(text, /userexam_crypto\.exe/, 'instructions must not use the spec-less underscore name')
  assert.match(text, /password\.txt/, 'key provisioning must be documented')
  assert.match(text, /USEREXAM_PASSWORD/)
  assert.match(text, /--json/, 'the CLI form must be documented')
  assert.match(text, /--inspect/, 'the read-only CLI form must be documented')
  assert.match(text, /--selftest/, 'the packaged-runtime self test must be documented')
  assert.match(text, /无需/, 'the no-install promise must be stated')
  console.log('EXAM_TOOL_USAGE_OK usage block names artifact, password.txt, USEREXAM_PASSWORD, --json, --inspect, --selftest')
})

test('client-side size cap mirrors the server cap and rejects oversized files before upload', async () => {
  versionPayload = ok({ available: true, filename: 'userexam-crypto.exe', cipher: 'aes256cbc', legacy: '0', kdfIter: 4001 })
  const wrapper = await mountPage(ADMIN)

  attachFile(wrapper, 'UserExamInfo.decrypted.db', 16 * 1024 * 1024 + 1)
  fileInput(wrapper).trigger('change')
  await settle()
  assert.equal(calls.inspect.length, 0, 'oversized file must not be uploaded')
  assert.match(wrapper.text(), /文件超过 16 MiB/)

  const exact = attachFile(wrapper, 'UserExamInfo.decrypted.db', 16 * 1024 * 1024)
  fileInput(wrapper).trigger('change')
  await settle()
  await wrapper.find('form').trigger('submit')
  await settle()
  assert.equal(calls.inspect.length, 1, 'a file exactly at 16 MiB is allowed')
  assert.equal(calls.inspect[0], exact.file)
  console.log('EXAM_TOOL_SIZE_CAP_OK 16MiB+1 rejected pre-upload, exactly 16MiB accepted')
})

test('server rejections map to actionable messages instead of a generic failure', async () => {
  versionPayload = ok({ available: true, filename: 'userexam-crypto.exe', cipher: 'aes256cbc', legacy: '0', kdfIter: 4001 })
  const wrapper = await mountPage(ADMIN)
  attachFile(wrapper, 'UserExamInfo.db', 4096)
  fileInput(wrapper).trigger('change')
  await settle()

  const errorText = () => {
    const alert = wrapper.find('.ant-alert-error')
    return alert.exists() ? alert.text() : ''
  }

  inspectHandler = () => Promise.resolve({ status: 413, data: { message: '数据库文件超过只读预览允许的大小' } })
  await wrapper.find('form').trigger('submit')
  await settle()
  assert.match(errorText(), /超过只读预览允许的大小/)

  // A 400 must reach the operator verbatim: the old code fell through to
  // 「解析失败，请重试」for everything except 413, which hides the actual fix
  // (decrypt the file with the offline tool first).
  inspectHandler = () => Promise.resolve({ status: 400, data: { status: 400, message: '该文件不是已解密的 SQLite 数据库；请先用脱机工具解密' } })
  await wrapper.find('form').trigger('submit')
  await settle()
  assert.match(errorText(), /请先用脱机工具解密/, 'the encrypted-upload rejection must point at the offline tool')
  assert.doesNotMatch(errorText(), /解析失败，请重试/, 'a 400 must not degrade into the generic message')

  // A body without a message must still not crash and must stay generic.
  inspectHandler = () => Promise.resolve({ status: 400, data: {} })
  await wrapper.find('form').trigger('submit')
  await settle()
  assert.match(errorText(), /不是已解密的 SQLite 数据库/)
  console.log('EXAM_TOOL_ERROR_MAP_OK 413 and 400 both surface the server message verbatim; bare 400 falls back safely')
})

test('previewing a plaintext database renders both known tables and names unknown ones', async () => {
  versionPayload = ok({ available: true, filename: 'userexam-crypto.exe', cipher: 'aes256cbc', legacy: '0', kdfIter: 4001 })
  const wrapper = await mountPage(ADMIN)
  attachFile(wrapper, 'UserExamInfo.decrypted.db', 12288)
  fileInput(wrapper).trigger('change')
  await settle()

  inspectHandler = () => Promise.resolve(ok({
    tables: {
      baseInfo: { columns: ['Token', 'schoolId'], rows: [{ Token: 'synthetic-token', schoolId: '196586' }] },
      macAddress: { columns: ['Address', 'Flag'], rows: [{ Address: '', Flag: 'wsh' }] },
    },
    otherTables: ['unrelated'],
    rowCounts: { baseInfo: 1, macAddress: 1 },
  }))
  await wrapper.find('form').trigger('submit')
  await settle()

  const text = wrapper.text()
  assert.match(text, /baseInfo/)
  assert.match(text, /macAddress/)
  assert.match(text, /196586/)
  assert.match(text, /unrelated/, 'unknown tables are listed by name')
  assert.equal(wrapper.findAll('table').length >= 2, true, 'each known table gets a rendered table')
  console.log('EXAM_TOOL_PREVIEW_OK baseInfo/macAddress rendered, unknown table named only')
})

test('the page never sends a password anywhere: no request payload carries the key', async () => {
  versionPayload = ok({ available: true, filename: 'userexam-crypto.exe', cipher: 'aes256cbc', legacy: '0', kdfIter: 4001 })
  const wrapper = await mountPage(ADMIN)
  attachFile(wrapper, 'UserExamInfo.decrypted.db', 12288)
  fileInput(wrapper).trigger('change')
  await settle()
  await wrapper.find('form').trigger('submit')
  await settle()

  const submitted = calls.inspect[0]
  assert.equal(submitted instanceof File, true, 'only the file itself is submitted')
  assert.equal(Object.keys(submitted).some(key => /password|key|passphrase/i.test(key)), false)
  const serialized = JSON.stringify(Object.keys(wrapper.vm.$data ?? {}))
  assert.doesNotMatch(serialized, /password/i, 'the page holds no password field at all')
  console.log('EXAM_TOOL_NO_KEY_OK only the file leaves the browser; no password field exists')
})
