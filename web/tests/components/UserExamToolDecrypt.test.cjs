/* 考试客户端库「服务器解密」前端契约（2026-11-14 用户拍板的新方向）。
 *
 * 与 UserExamToolPrincipalAnt4.test.cjs 的分工：那个文件守的是「管理员门禁 /
 * 16 MiB 上限 / 尺寸与文案」这些既有权衡；本文件只守本次新增的两件事：
 *
 *  1. **页面明确说明可以直接上传加密库**（旧文案「服务端不做解密」已作废，
 *     再出现就是给用户错误指引，让他白跑一趟本机解密）；
 *  2. **解密密钥面板存在且不回显密钥** —— 管理员必须能确认「改的值生效了」，
 *     但页面/请求里都不能出现密钥原文（除了用户主动输入那一次 PUT）。
 *
 * 另外钉住「加密库解密成功」时页面必须显示「已自动解密」，否则管理员
 * 无法分辨「这个库本来是明文」还是「服务器帮我解开了」。
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

const ADMIN = { id: 91701, role: 'admin', username: 'SYNTHETIC_DECRYPT_ADMIN', status: 'enabled' }

const calls = { version: [], inspect: [], decryptKey: [], updateKey: [] }
let keyState = null
let inspectHandler = null
let updateHandler = null
const wrappers = []

const ok = data => ({ status: 200, data })

const apiPath = path.join(root, 'src/api/UserExamToolApi.js')
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  getUserExamToolVersion() {
    calls.version.push(true)
    return Promise.resolve(ok({
      available: true, filename: 'userexam-crypto.exe', version: '1.1.0',
      sha256: 'a'.repeat(64), sizeBytes: 1, cipher: 'aes256cbc', legacy: '0', kdfIter: 4001,
    }))
  },
  getUserExamToolDownloadURL() { return '/api/admin/userexam/tool' },
  inspectUserExamDatabase(file) {
    calls.inspect.push(file)
    return inspectHandler ? inspectHandler(file) : Promise.resolve(ok({ tables: {}, otherTables: [], rowCounts: {} }))
  },
  getUserExamDecryptKey() {
    calls.decryptKey.push(true)
    return Promise.resolve(ok(keyState || {
      configured: false, usingDefault: true,
      keyHint: 'aaaaaaaa', defaultKeyHint: 'aaaaaaaa',
      message: '当前使用内置默认密钥（开箱即用）；如需更安全，请改成自己的密钥。',
    }))
  },
  updateUserExamDecryptKey(key) {
    calls.updateKey.push(key)
    if (updateHandler) return updateHandler(key)
    return Promise.resolve(ok({
      configured: true, usingDefault: false,
      keyHint: 'bbbbbbbb', defaultKeyHint: 'aaaaaaaa',
      message: '当前使用管理员配置的密钥。',
    }))
  },
}
require.cache[apiPath] = api

const Page = require(path.join(root, 'src/pages/UserExamTool/index.vue'))

async function settle() { await nextTick(); await flushPromises(); await nextTick() }

async function mountPage() {
  user.setStoredCurrentUser(ADMIN)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/admin/user-exam-tool', component: Page }] })
  router.push('/adapter/admin/user-exam-tool')
  await router.isReady()
  const wrapper = mount(Page, { global: { plugins: [Antd, router] } })
  wrappers.push(wrapper)
  await settle()
  return wrapper
}

afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) if (wrapper.exists()) wrapper.unmount()
  await settle()
  calls.version.length = 0
  calls.inspect.length = 0
  calls.decryptKey.length = 0
  calls.updateKey.length = 0
  keyState = null
  inspectHandler = null
  updateHandler = null
  user.clearStoredCurrentUser()
  Antd.message.destroy()
  document.body.innerHTML = ''
})

// ─────────────────── ① 文案：明确支持加密库 ───────────────────

test('页面明确告知可直接上传加密库，且不再声称服务端不解密', async () => {
  const wrapper = await mountPage()
  const text = wrapper.text()

  // 正向：必须出现「可直接上传加密」「服务器会解密」的意思。
  assert.match(text, /可直接上传加密/, '页面必须明确告知可以直接上传加密库')
  assert.match(text, /服务器会解密/, '必须说明服务端负责解密')

  // 反向：旧口径「服务端不做解密」必须彻底消失 —— 它会直接误导用户
  // 去本机解密（白跑一趟），是本次需求要消灭的核心问题。
  assert.doesNotMatch(text, /服务端不做解密/, '旧的「服务端不做解密」文案必须移除')
  // 反向：不能再把「已解密」当成上传前提。
  assert.doesNotMatch(text, /上传已解密的/, '上传不应再要求预先解密')
  console.log('EXAM_DECRYPT_COPY_OK 页面已声明支持加密库，旧口径已移除')
})

test('加密库解密成功后页面显示「已自动解密」标记', async () => {
  const wrapper = await mountPage()
  inspectHandler = () => Promise.resolve(ok({
    tables: { baseInfo: { columns: ['schoolId'], rows: [{ schoolId: '196586' }] } },
    otherTables: [], rowCounts: { baseInfo: 1 },
    decrypted: true,
  }))

  const input = wrapper.find('input[type="file"]').element
  const file = new File([new Uint8Array(1)], 'UserExamInfo.db')
  Object.defineProperty(input, 'files', { configurable: true, value: [file] })
  await wrapper.find('input[type="file"]').trigger('change')
  await wrapper.find('form').trigger('submit')
  await settle()

  const text = wrapper.text()
  assert.match(text, /已由服务器自动解密/, '解密成功时必须明确标记，否则管理员分不清本来是否明文')
  console.log('EXAM_DECRYPT_BADGE_OK decrypted=true 时显示自动解密提示')
})

test('明文库（decrypted=false）不显示「已自动解密」', async () => {
  const wrapper = await mountPage()
  inspectHandler = () => Promise.resolve(ok({
    tables: { baseInfo: { columns: ['schoolId'], rows: [{ schoolId: '1' }] } },
    otherTables: [], rowCounts: { baseInfo: 1 },
    decrypted: false,
  }))
  const input = wrapper.find('input[type="file"]').element
  Object.defineProperty(input, 'files', { configurable: true, value: [new File([new Uint8Array(1)], 'x.db')] })
  await wrapper.find('input[type="file"]').trigger('change')
  await wrapper.find('form').trigger('submit')
  await settle()

  assert.doesNotMatch(wrapper.text(), /已由服务器自动解密/, '明文库不应被标成「已解密」')
  console.log('EXAM_DECRYPT_BADGE_PLAIN_OK decrypted=false 时不显示自动解密提示')
})

// ② 产品已移除浏览器密钥设置（efa4cea3）；必须验证真实页面不会读、写或泄露它。
test('挂载只读取工具版本，浏览器既不请求也不显示运维密钥状态或指纹', async () => {
  keyState = { configured: true, keyHint: 'SYNTHETIC_PRIVATE_HINT', message: 'SYNTHETIC_PRIVATE_KEY_STATUS' }
  const wrapper = await mountPage()
  assert.equal(calls.version.length, 1)
  assert.equal(calls.decryptKey.length, 0)
  assert.equal(calls.updateKey.length, 0)
  assert.doesNotMatch(wrapper.text(), /SYNTHETIC_PRIVATE_HINT|SYNTHETIC_PRIVATE_KEY_STATUS/)
})

test('管理页没有密钥输入或保存控制，仍提供只读文件选择', async () => {
  const wrapper = await mountPage()
  assert.ok(wrapper.find('input[type="file"]').exists())
  assert.equal(wrapper.find('input[type="password"]').exists(), false)
  assert.equal(wrapper.findAll('button').some(button => /保存密钥/.test(button.text())), false)
  assert.doesNotMatch(wrapper.text(), /下方配置的密钥|上面的密钥/)
  assert.equal(calls.updateKey.length, 0)
})

test('文件解析失败给出可读的来源或版本提示，不要求改密钥，也不触发密钥读写', async () => {
  const wrapper = await mountPage()
  inspectHandler = () => Promise.resolve({ status: 400, data: { message: 'SYNTHETIC_INVALID_FILE', reason: 'wrong-key' } })
  const input = wrapper.find('input[type="file"]').element
  Object.defineProperty(input, 'files', { configurable: true, value: [new File([new Uint8Array(1)], 'sample.db')] })
  await wrapper.find('input[type="file"]').trigger('change')
  await wrapper.find('form').trigger('submit')
  await settle()
  assert.equal(calls.inspect.length, 1)
  assert.match(wrapper.text(), /SYNTHETIC_INVALID_FILE/)
  assert.match(wrapper.text(), /尚未适配的客户端版本|文件已损坏/)
  assert.equal(calls.decryptKey.length, 0)
  assert.equal(calls.updateKey.length, 0)
})
