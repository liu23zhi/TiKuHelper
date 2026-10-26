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
  normalizePhonePaste,
  isValidMainlandPhone,
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

test('normalizePhoneInput accepts only mainland phone digits and partial typing', () => {
  assert.equal(normalizePhoneInput('13800000000'), '13800000000')
  assert.equal(normalizePhoneInput('138000'), '138000')
  assert.equal(normalizePhoneInput('138000000000'), '')
  assert.equal(normalizePhoneInput('1380000000a'), '')
  assert.equal(normalizePhoneInput('+8613800000000'), '')
  assert.equal(normalizePhoneInput('138 0000 0000'), '')
  assert.equal(normalizePhoneInput(''), '')
})

test('normalizePhonePaste handles a complete optional +86 prefix without truncating invalid input', () => {
  assert.equal(normalizePhonePaste('13800000000'), '13800000000')
  assert.equal(normalizePhonePaste('+86 13800000000'), '13800000000')
  assert.equal(normalizePhonePaste('+8613800000000'), '13800000000')
  assert.equal(normalizePhonePaste('138 0000 0000'), '13800000000')
  assert.equal(normalizePhonePaste('+86 \t138 0000 0000'), '13800000000')
  assert.equal(normalizePhonePaste(' 13800000000 '), '13800000000')
  assert.equal(normalizePhonePaste('+86-138-0000-0000'), '')
  assert.equal(normalizePhonePaste('+1 3800000000'), '')
  assert.equal(normalizePhonePaste('1380000000a'), '')
  assert.equal(normalizePhonePaste('138000000000'), '')
})

test('isValidMainlandPhone validates exactly eleven ASCII digits', () => {
  assert.equal(isValidMainlandPhone('13800000000'), true)
  assert.equal(isValidMainlandPhone(' 13800000000 '), false)
  assert.equal(isValidMainlandPhone('1380000000'), false)
  assert.equal(isValidMainlandPhone('23800000000'), false)
  assert.equal(isValidMainlandPhone('１３８００００００００'), false)
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
