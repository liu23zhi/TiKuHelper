import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
const srcDir = path.join(__dirname, '..')

async function readSource(relativePath) {
  return readFile(path.join(srcDir, relativePath), 'utf8')
}

test('Profile page displays account status, phone change, and wechat bind/unbind flows', async () => {
  const content = await readSource(path.join('pages', 'Profile', 'index.vue'))
  const apiContent = await readSource(path.join('api', 'TikuComponentApi.js'))

  assert.match(content, /name: 'UserProfile'/)
  assert.match(content, /个人中心/)
  assert.match(content, /smsLoginEnabled/)
  assert.match(content, /管理员尚未启用手机号，当前无法更换手机号。/)
  assert.match(content, /管理员尚未开启微信，当前无法绑定。/)
  assert.match(content, /phoneBoundAt/)
  assert.match(content, /wechatBoundAt/)
  assert.match(content, /微信ID：\{\{ currentUser\.wechatId \}\}/)
  assert.match(content, /wechat-binding-id--empty">未绑定/)
  assert.match(content, /sendUserPhoneChangeSMSCode/)
  assert.match(content, /updateCurrentUserPhone/)
  assert.match(content, /bindCurrentUserWechat/)
  assert.match(content, /bindCurrentUserWechatByCode/)
  assert.match(content, /unbindCurrentUserWechat/)
  assert.match(content, /wechatBindSubMode/)
  assert.match(content, /wechatScanLoginEnabled/)
  assert.match(content, /wechatCodeLoginEnabled/)
  assert.match(content, /wechatCodeStaticQrcodeUrl/)
  assert.match(content, /value="scan"/)
  assert.match(content, /value="code"/)
  assert.match(content, /submitWechatCodeBind/)
  assert.match(apiContent, /export function bindCurrentUserWechatByCode/)
  assert.match(apiContent, /url: `\/adapter-service\/user\/wechat\/bind-code`/)
})

test('Profile route and menu are available to both admins and regular users', async () => {
  const routerContent = await readSource(path.join('router', 'router.js'))
  const menuContent = await readSource(path.join('components', 'SiderMenu', 'index.vue'))

  assert.match(routerContent, /Profile/)
  assert.match(routerContent, /path: 'profile'/)
  assert.match(menuContent, /key: 'profile'/)
  assert.match(menuContent, /label: '个人中心'/)
})

test('Profile page keeps scan QR manual while using realtime status lifecycle', async () => {
  const content = await readSource(path.join('pages', 'Profile', 'index.vue'))

  assert.match(content, /createWechatQrStatusController/)
  assert.doesNotMatch(content, /void startWechatBind\(\)/)
  assert.match(content, /@click="startWechatBind"/)
  assert.match(content, /onRemaining/)
  assert.match(content, /purpose: 'bind'/)
  assert.match(content, /实时连接/)
  assert.match(content, /轮询中/)
  assert.match(content, /二维码已过期/)
})

test('User management displays wechat binding state and binding time', async () => {
  const content = await readSource(path.join('pages', 'UserList', 'index.vue'))

  assert.match(content, /title: '微信绑定'/)
  assert.match(content, /title: '微信绑定时间'/)
  assert.match(content, /record\.wechatId/)
  assert.match(content, /record\.wechatBoundAt/)
})

test('System settings exposes a configurable site name', async () => {
  const content = await readSource(path.join('pages', 'SystemSettings', 'index.vue'))

  assert.match(content, /label="网站名称"/)
  assert.match(content, /formState\.siteName/)
})

test('Login and register pages start a 60 second SMS countdown after a successful send', async () => {
  const loginContent = await readSource(path.join('pages', 'Login', 'index.vue'))
  const registerContent = await readSource(path.join('pages', 'Register', 'index.vue'))

  for (const content of [loginContent, registerContent]) {
    assert.match(content, /useSmsCooldown/)
    assert.match(content, /startSmsCooldown/)
    assert.match(content, /smsCooldownText/)
    assert.match(content, /:disabled="[^"]*smsCooldownActive/)
  }
})

test('Register page keeps optional WeChat registration lazy and isolated per tab', async () => {
  const registerContent = await readSource(path.join('pages', 'Register', 'index.vue'))
  const apiContent = await readSource(path.join('api', 'TikuComponentApi.js'))

  assert.match(registerContent, /createWechatRegisterSession/)
  assert.match(registerContent, /createWechatRegisterQRCode/)
  assert.match(registerContent, /getWechatRegisterStatus/)
  assert.match(registerContent, /stageWechatRegisterCode/)
  assert.match(registerContent, /releaseWechatRegisterFlow/)
  assert.match(registerContent, /registrationFlowToken/)
  assert.match(registerContent, /onMounted/)
  assert.match(registerContent, /status === 409|status === 410|response\?\.status/)
  assert.match(apiContent, /url: `\/adapter-service\/public\/wechat-register\/session`/)
  assert.match(apiContent, /url: `\/adapter-service\/public\/wechat-register\/qrcode`/)
  assert.match(apiContent, /url: `\/adapter-service\/public\/wechat-register\/status`/)
  assert.match(apiContent, /url: `\/adapter-service\/public\/wechat-register\/code`/)
  assert.match(apiContent, /url: `\/adapter-service\/public\/wechat-register\/release`/)
})

test('Topic overview removes the course name table column while keeping bulk editing', async () => {
  const content = await readSource(path.join('pages', 'TikuComponent', 'index.vue'))

  assert.doesNotMatch(content, /title: '课程名称'/)
  assert.match(content, /批量编辑课程名称/)
  assert.match(content, /buildQuestionBulkCourseUpdatePayload/)
})
