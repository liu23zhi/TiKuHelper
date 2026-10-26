import { limitRestrictedNameLength, normalizeRestrictedName, USERNAME_MAX_LENGTH } from './restrictedName.mjs'
import { isValidMainlandPhone } from './captchaHelpers.mjs'

export { USERNAME_MAX_LENGTH }

export function normalizeUsername(value) {
  return normalizeRestrictedName(value, USERNAME_MAX_LENGTH, '用户名')
}

export function limitUsernameInput(value) {
  return limitRestrictedNameLength(value, USERNAME_MAX_LENGTH)
}

function trimText(value) {
  return String(value || '').trim()
}

function requireMainlandPhone(value) {
  const phone = trimText(value)
  if (!phone) {
    throw new Error('请输入手机号')
  }
  if (!isValidMainlandPhone(phone)) {
    throw new Error('请输入11位手机号')
  }
  return phone
}

export function buildPublicSMSCodePayload(formValue, scene) {
  const captchaToken = trimText(formValue?.captchaToken)
  return {
    phone: requireMainlandPhone(formValue?.phone),
    scene: trimText(scene),
    ...(captchaToken ? { captchaToken } : {}),
  }
}

export function buildSMSLoginPayload(formValue) {
  const phone = requireMainlandPhone(formValue?.phone)
  const smsCode = trimText(formValue?.smsCode)
  const captchaToken = trimText(formValue?.captchaToken)
  if (!smsCode) {
    throw new Error('请输入验证码')
  }
  if (!captchaToken) {
    throw new Error('请先完成人机验证')
  }
  return { phone, smsCode, captchaToken }
}

export function buildPublicRegisterPayload(formValue) {
  const username = normalizeUsername(formValue?.username)
  const phone = requireMainlandPhone(formValue?.phone)
  const smsCode = trimText(formValue?.smsCode)
  const captchaToken = trimText(formValue?.captchaToken)
  if (!smsCode) {
    throw new Error('请输入验证码')
  }
  if (!captchaToken) {
    throw new Error('请先完成人机验证')
  }
  return {
    username,
    password: trimText(formValue?.password),
    nickname: trimText(formValue?.nickname),
    phone,
    smsCode,
    captchaToken,
  }
}
