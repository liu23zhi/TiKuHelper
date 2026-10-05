export const SMS_SEND_COOLDOWN_SECONDS = 60

export function formatSMSCodeButtonText(remainingSeconds) {
  const seconds = Math.max(0, Math.ceil(Number(remainingSeconds) || 0))
  return seconds > 0 ? `${seconds}s后重试` : '发送验证码'
}
