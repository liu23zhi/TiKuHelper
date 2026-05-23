function trimText(value) {
  return String(value || '').trim()
}

export function buildPublicSMSCodePayload(formValue, scene) {
  return {
    phone: trimText(formValue?.phone),
    scene: trimText(scene),
  }
}

export function buildSMSLoginPayload(formValue) {
  const phone = trimText(formValue?.phone)
  const smsCode = trimText(formValue?.smsCode)
  if (!phone) {
    throw new Error('请输入手机号')
  }
  if (!smsCode) {
    throw new Error('请输入验证码')
  }
  return { phone, smsCode }
}

export function buildPublicRegisterPayload(formValue) {
  const phone = trimText(formValue?.phone)
  const smsCode = trimText(formValue?.smsCode)
  if (!phone) {
    throw new Error('请输入手机号')
  }
  if (!smsCode) {
    throw new Error('请输入验证码')
  }
  return {
    username: trimText(formValue?.username),
    password: trimText(formValue?.password),
    nickname: trimText(formValue?.nickname),
    phone,
    smsCode,
  }
}
