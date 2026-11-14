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
  getUserExamToolDownloadURL() { return '/adapter-service/admin/userexam/tool' },
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

// ─────────────────── ② 密钥面板 ───────────────────

test('密钥面板显示状态与弱指纹，且页面上不出现密钥字段', async () => {
  keyState = {
    configured: false, usingDefault: true,
    keyHint: '11112222', defaultKeyHint: '11112222',
    message: '当前使用内置默认密钥（开箱即用）；如需更安全，请改成自己的密钥。',
  }
  const wrapper = await mountPage()
  await settle()

  assert.equal(calls.decryptKey.length, 1, '挂载时必须读取一次密钥状态')
  const text = wrapper.text()
  assert.match(text, /使用内置默认密钥/, '未配置时必须明示正在用默认密钥')
  assert.match(text, /11112222/, '必须展示当前密钥指纹，供管理员确认改动生效')

  // 页面不得出现「密钥原文」——只允许 placeholder/指纹这类非敏感信息。
  assert.doesNotMatch(text, /tiku-userexam-default-key-2026/, '页面不得显示内置默认密钥原文')
  console.log('EXAM_DECRYPT_KEY_PANEL_OK 状态与指纹可见，密钥原文不出现')
})

test('保存密钥只把用户输入提交一次，且不在页面上回显', async () => {
  const wrapper = await mountPage()
  const keyInput = wrapper.find('input[type="password"]')
  assert.equal(keyInput.exists(), true, '必须提供密钥输入框')

  await keyInput.setValue('MY-NEW-KEY-123')
  // 找到「保存密钥」按钮并点击
  const buttons = wrapper.findAll('button')
  const saveButton = buttons.find(button => /保存密钥/.test(button.text()))
  assert.ok(saveButton, '必须存在「保存密钥」按钮')
  await saveButton.trigger('click')
  await settle()

  assert.equal(calls.updateKey.length, 1, '保存必须恰好提交一次')
  assert.equal(calls.updateKey[0], 'MY-NEW-KEY-123', '提交的必须是用户输入的值')
  // 提交后输入框必须被清空，页面上不留明文密钥。
  assert.equal(wrapper.find('input[type="password"]').element.value, '')
  assert.doesNotMatch(wrapper.text(), /MY-NEW-KEY-123/, '保存后页面不得回显密钥')
  console.log('EXAM_DECRYPT_KEY_SAVE_OK 只提交一次、清空输入、不回显')
})

test('密钥超长时展示后端的可读错误，不清空用户输入', async () => {
  const wrapper = await mountPage()
  updateHandler = () => Promise.resolve({
    status: 400,
    data: { message: '解密密钥不合法：长度不得超过 32 字节（SQLite3MC 会在此处截断）' },
  })
  await wrapper.find('input[type="password"]').setValue('x'.repeat(40))
  const saveButton = wrapper.findAll('button').find(button => /保存密钥/.test(button.text()))
  await saveButton.trigger('click')
  await settle()

  assert.match(wrapper.text(), /32 字节/, '必须把后端的长度限制原样告诉用户')
  // 失败时不应清空输入（否则用户要重打一遍）。
  assert.equal(wrapper.find('input[type="password"]').element.value, 'x'.repeat(40))
  console.log('EXAM_DECRYPT_KEY_INVALID_OK 400 文案透出，输入保留')
})
