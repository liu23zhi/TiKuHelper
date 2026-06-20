import test from 'node:test'
import assert from 'node:assert/strict'

import {
  buildCaptchaChallengePayload,
  buildCaptchaVerifyPayload,
  buildRemoteCaptchaConfig,
  ensurePhoneBeforeSMSCaptcha,
  normalizeCaptchaType,
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

test('normalizePhoneInput keeps digits and a leading plus for international numbers', () => {
  assert.equal(normalizePhoneInput(' 138-0000-0000abc '), '13800000000')
  assert.equal(normalizePhoneInput('+1 415-555-2671abc'), '+14155552671')
  assert.equal(normalizePhoneInput('++1 415 555'), '+1415555')
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

test('remote captcha helpers normalize type and build TAC urls', () => {
  assert.equal(normalizeCaptchaType('slider'), 'SLIDER')
  assert.equal(normalizeCaptchaType('滑块验证码'), 'SLIDER')
  assert.equal(normalizeCaptchaType('点选验证码'), 'WORD_IMAGE_CLICK')
  assert.equal(normalizeCaptchaType('unknown'), 'RANDOM')
  assert.deepEqual(buildRemoteCaptchaConfig('https://captcha.api.zelly.cn/', 'SLIDER'), {
    loaderUrl: 'https://captcha.api.zelly.cn/loader.js',
    tacPath: 'https://captcha.api.zelly.cn/tac',
    requestCaptchaDataUrl: '/adapter-service/public/captcha/remote-gen?type=SLIDER',
    validCaptchaUrl: '/adapter-service/public/captcha/remote-check',
  })
})
