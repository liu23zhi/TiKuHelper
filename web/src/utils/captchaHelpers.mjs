function trimText(value) {
  return String(value || '').trim()
}

function onlyDigits(value) {
  return String(value || '').replace(/\D+/g, '')
}

const MAINLAND_PHONE_PATTERN = /^1[0-9]{10}$/

function trimTrailingSlash(value) {
  return trimText(value).replace(/\/+$/, '')
}

export function buildCaptchaChallengePayload(scene) {
  return {
    scene: trimText(scene),
  }
}

export function buildCaptchaVerifyPayload({ challengeId, scene, captchaCode }) {
  return {
    challengeId: trimText(challengeId),
    scene: trimText(scene),
    captchaCode: trimText(captchaCode),
  }
}

export function normalizePhoneInput(value) {
  const text = String(value ?? '')
  if (!/^\d*$/.test(text) || text.length > 11) {
    return ''
  }
  return text
}

export function normalizePhonePaste(value) {
  const text = String(value ?? '').trim()
  const body = text.startsWith('+86') ? text.slice(3) : text
  if (!/^[0-9\s]+$/.test(body)) return ''
  const digits = body.replace(/\s/g, '')
  return isValidMainlandPhone(digits) ? digits : ''
}

export function isValidMainlandPhone(value) {
  return typeof value === 'string' && MAINLAND_PHONE_PATTERN.test(value)
}

export function normalizeSMSCodeInput(value) {
  return onlyDigits(value).slice(0, 6)
}

export function ensurePhoneBeforeSMSCaptcha(phone) {
  const normalizedPhone = trimText(phone)
  if (!normalizedPhone) {
    throw new Error('请输入手机号')
  }
  if (!isValidMainlandPhone(normalizedPhone)) {
    throw new Error('请输入11位手机号')
  }
  return normalizedPhone
}

export function shouldRetryWithCaptcha(message) {
  const text = trimText(message)
  return text.includes('人机验证') || text.includes('图形验证码')
}

export function normalizeCaptchaType(value) {
  const normalized = trimText(value).toUpperCase()
  const aliases = {
    随机: 'RANDOM',
    滑块: 'SLIDER',
    滑块验证: 'SLIDER',
    滑块验证码: 'SLIDER',
    旋转: 'ROTATE',
    旋转验证: 'ROTATE',
    旋转验证码: 'ROTATE',
    滑动还原: 'CONCAT',
    滑动还原验证码: 'CONCAT',
    文字点选: 'WORD_IMAGE_CLICK',
    点选: 'WORD_IMAGE_CLICK',
    点选验证码: 'WORD_IMAGE_CLICK',
    文字点选验证码: 'WORD_IMAGE_CLICK',
  }
  if (aliases[trimText(value)]) {
    return aliases[trimText(value)]
  }
  return ['RANDOM', 'SLIDER', 'ROTATE', 'CONCAT', 'WORD_IMAGE_CLICK'].includes(normalized) ? normalized : 'RANDOM'
}

export function buildRemoteCaptchaConfig(baseUrl, captchaType) {
  const normalizedBaseUrl = trimTrailingSlash(baseUrl || 'https://captcha.api.zelly.cn')
  const normalizedType = normalizeCaptchaType(captchaType)
  return {
    loaderUrl: `${normalizedBaseUrl}/loader.js`,
    tacPath: `${normalizedBaseUrl}/tac`,
    requestCaptchaDataUrl: `/api/public/captcha/remote-gen?type=${encodeURIComponent(normalizedType)}`,
    validCaptchaUrl: '/api/public/captcha/remote-check',
  }
}
