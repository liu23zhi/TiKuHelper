import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
const loginPagePath = path.join(__dirname, '..', 'pages', 'Login', 'index.vue')

test('Login page requests captcha before password login, sms login, and sms sending', async () => {
  const content = await readFile(loginPagePath, 'utf8')

  assert.match(content, /createCaptchaChallenge/)
  assert.match(content, /verifyCaptchaChallenge/)
  assert.match(content, /verifyRemoteCaptcha/)
  assert.match(content, /ensureAuthCaptchaToken/)
  assert.match(content, /CaptchaSceneLoginPassword/)
  assert.match(content, /CaptchaSceneLoginSMS/)
  assert.match(content, /CaptchaSceneSMSSendLogin/)
  assert.match(content, /captchaToken/)
})

test('Login page uses the mainland phone component and constrains sms code length', async () => {
  const content = await readFile(loginPagePath, 'utf8')

  assert.match(content, /<MainlandPhoneInput/)
  assert.match(content, /:maxlength="6"/)
  assert.match(content, /ensurePhoneBeforeSMSCaptcha/)
  assert.match(content, /normalizeSMSCodeInput/)
})
