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

test('remote captcha dialog passes TAC asset path to captcha-local loader', async () => {
  const content = await readFile(captchaDialogPath, 'utf8')

  assert.match(content, /tacPath/)
  assert.match(content, /window\.initTAC\(\s*remoteConfig\.tacPath,/)
})

test('remote captcha token exchange failure does not fall back to local captcha', async () => {
  const content = await readFile(captchaDialogPath, 'utf8')

  assert.match(content, /RemoteCaptchaUnavailableError/)
  assert.match(content, /instanceof RemoteCaptchaUnavailableError/)
  assert.match(content, /throw error/)
  assert.doesNotMatch(content, /catch \(_\)/)
})

test('remote captcha user cancellation does not fall back to local captcha', async () => {
  const content = await readFile(captchaDialogPath, 'utf8')

  assert.match(content, /UserCancelledCaptchaError/)
  assert.match(content, /btnCloseFun/)
  assert.match(content, /new UserCancelledCaptchaError\('请先完成人机验证'\)/)
  assert.doesNotMatch(content, /error instanceof RemoteCaptchaUnavailableError \|\| error instanceof UserCancelledCaptchaError/)
})

test('remote captcha uses TAC as the only modal instead of nesting it in SweetAlert', async () => {
  const content = await readFile(captchaDialogPath, 'utf8')
  const remoteDialogStart = content.indexOf('export async function openRemoteCaptchaDialog')
  const localTokenStart = content.indexOf('async function ensureLocalCaptchaToken')
  const remoteDialogSource = content.slice(remoteDialogStart, localTokenStart)

  assert.match(remoteDialogSource, /createRemoteCaptchaMount/)
  assert.match(remoteDialogSource, /removeRemoteCaptchaMount/)
  assert.match(remoteDialogSource, /bindEl: `#\$\{mountId\}`/)
  assert.match(remoteDialogSource, /btnCloseFun:/)
  assert.doesNotMatch(remoteDialogSource, /Swal\.fire/)
  assert.doesNotMatch(remoteDialogSource, /showCancelButton/)
})
