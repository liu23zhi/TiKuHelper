import test from 'node:test'
import assert from 'node:assert/strict'

import {
  buildCaptchaChallengePayload,
  buildCaptchaVerifyPayload,
  ensurePhoneBeforeSMSCaptcha,
  normalizeSMSCodeInput,
  normalizePhoneInput,
  shouldRetryWithCaptcha,
} from './captchaHelpers.mjs'

test('buildCaptchaChallengePayload trims and keeps scene', () => {
  assert.deepEqual(buildCaptchaChallengePayload('  login_password  '), {
    scene: 'login_password',
  })
})

test('buildCaptchaVerifyPayload trims challenge answer and scene', () => {
  assert.deepEqual(buildCaptchaVerifyPayload({ challengeId: '  ch-1 ', scene: ' login_sms ', captchaCode: ' ab12cd ' }), {
    challengeId: 'ch-1',
    scene: 'login_sms',
    captchaCode: 'ab12cd',
  })
})

test('ensurePhoneBeforeSMSCaptcha requires phone before captcha challenge', () => {
  assert.equal(ensurePhoneBeforeSMSCaptcha(' 13800000000 '), '13800000000')
  assert.throws(() => ensurePhoneBeforeSMSCaptcha('   '), /请输入手机号/)
})

test('normalizePhoneInput keeps only digits and caps at 11 characters', () => {
  assert.equal(normalizePhoneInput(' 138-0000-0000abc '), '13800000000')
  assert.equal(normalizePhoneInput('+86 13800000000123'), '86138000000')
})

test('normalizeSMSCodeInput keeps only digits and caps at 6 characters', () => {
  assert.equal(normalizeSMSCodeInput(' 12a3-45 678 '), '123456')
  assert.equal(normalizeSMSCodeInput('abc'), '')
})

test('shouldRetryWithCaptcha only treats captcha failures as retryable challenge flow', () => {
  assert.equal(shouldRetryWithCaptcha('请先完成人机验证'), true)
  assert.equal(shouldRetryWithCaptcha('人机验证已过期，请重新验证'), true)
  assert.equal(shouldRetryWithCaptcha('当前登陆过期，请重新登陆。'), false)
})
