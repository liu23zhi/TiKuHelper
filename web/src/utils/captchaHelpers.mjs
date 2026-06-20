function trimText(value) {
  return String(value || '').trim()
}

function onlyDigits(value) {
  return String(value || '').replace(/\D+/g, '')
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
  return onlyDigits(value).slice(0, 11)
}

export function normalizeSMSCodeInput(value) {
  return onlyDigits(value).slice(0, 6)
}

export function ensurePhoneBeforeSMSCaptcha(phone) {
  const normalizedPhone = trimText(phone)
  if (!normalizedPhone) {
    throw new Error('请输入手机号')
  }
  return normalizedPhone
}

export function shouldRetryWithCaptcha(message) {
  const text = trimText(message)
  return text.includes('人机验证') || text.includes('图形验证码')
}
