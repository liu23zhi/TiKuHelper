function trimText(value) {
  return String(value || '').trim()
}

export function buildPublicSMSCodePayload(formValue, scene) {
  const captchaToken = trimText(formValue?.captchaToken)
  return {
    phone: trimText(formValue?.phone),
    scene: trimText(scene),
    ...(captchaToken ? { captchaToken } : {}),
  }
}

export function buildSMSLoginPayload(formValue) {
  const phone = trimText(formValue?.phone)
  const smsCode = trimText(formValue?.smsCode)
  const captchaToken = trimText(formValue?.captchaToken)
  if (!phone) {
    throw new Error('请输入手机号')
  }
  if (!smsCode) {
    throw new Error('请输入验证码')
  }
  if (!captchaToken) {
    throw new Error('请先完成人机验证')
  }
  return { phone, smsCode, captchaToken }
}

export function buildPublicRegisterPayload(formValue) {
  const phone = trimText(formValue?.phone)
  const smsCode = trimText(formValue?.smsCode)
  const captchaToken = trimText(formValue?.captchaToken)
  if (!phone) {
    throw new Error('请输入手机号')
  }
  if (!smsCode) {
    throw new Error('请输入验证码')
  }
  if (!captchaToken) {
    throw new Error('请先完成人机验证')
  }
  return {
    username: trimText(formValue?.username),
    password: trimText(formValue?.password),
    nickname: trimText(formValue?.nickname),
    phone,
    smsCode,
    captchaToken,
  }
}
