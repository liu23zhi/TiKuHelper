import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
const registerPagePath = path.join(__dirname, '..', 'pages', 'Register', 'index.vue')

test('Register page requests captcha before sms sending and register submit', async () => {
  const content = await readFile(registerPagePath, 'utf8')

  assert.match(content, /createCaptchaChallenge/)
  assert.match(content, /verifyCaptchaChallenge/)
  assert.match(content, /verifyRemoteCaptcha/)
  assert.match(content, /ensureAuthCaptchaToken/)
  assert.match(content, /CaptchaSceneSMSSendRegister/)
  assert.match(content, /CaptchaSceneRegisterSubmit/)
  assert.match(content, /captchaToken/)
})

test('Register page accepts domestic or international phone input and constrains sms code length', async () => {
  const content = await readFile(registerPagePath, 'utf8')

  assert.doesNotMatch(content, /addonBefore="\+86"/)
  assert.match(content, /:maxlength="20"/)
  assert.match(content, /:maxlength="6"/)
  assert.match(content, /inputmode="tel"/)
  assert.match(content, /normalizePhoneInput/)
  assert.match(content, /normalizeSMSCodeInput/)
})
