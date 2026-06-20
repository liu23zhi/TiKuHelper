import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
const captchaDialogPath = path.join(__dirname, 'captchaDialog.mjs')

test('captcha dialog helper defines polished layout and refresh affordances', async () => {
  const content = await readFile(captchaDialogPath, 'utf8')

  assert.match(content, /export async function openCaptchaDialog/)
  assert.match(content, /请输入下方图片中的验证码/)
  assert.match(content, /captcha-dialog-panel/)
  assert.match(content, /captcha-dialog-preview/)
  assert.match(content, /id="captcha-refresh-trigger"/)
  assert.match(content, /刷新验证码<\/button>/)
  assert.match(content, /点击验证码图片也可刷新验证码/)
  assert.match(content, /captchaImage\.addEventListener\('click'/)
  assert.match(content, /captchaRefreshTrigger\.addEventListener\('click'/)
  assert.match(content, /refreshCaptchaChallenge/)
})
