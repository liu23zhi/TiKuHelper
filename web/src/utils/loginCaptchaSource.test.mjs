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
  assert.match(content, /CaptchaSceneLoginPassword/)
  assert.match(content, /CaptchaSceneLoginSMS/)
  assert.match(content, /CaptchaSceneSMSSendLogin/)
  assert.match(content, /captchaToken/)
})

test('Login page shows fixed +86 prefix and constrains phone and sms code length', async () => {
  const content = await readFile(loginPagePath, 'utf8')

  assert.match(content, /addonBefore="\+86"/)
  assert.match(content, /:maxlength="11"/)
  assert.match(content, /:maxlength="6"/)
  assert.match(content, /inputmode="numeric"/)
  assert.match(content, /normalizePhoneInput/)
  assert.match(content, /normalizeSMSCodeInput/)
})
