export const USERNAME_MAX_LENGTH = 255
export const ACCESS_TOKEN_NAME_MAX_LENGTH = 128

const allowedName = /^[\p{Script=Han}A-Za-z0-9_@]+$/u

export function normalizeRestrictedName(value, maxLength, label) {
  const normalized = String(value ?? '').trim()
  const characterCount = Array.from(normalized).length
  if (!normalized) {
    throw new Error(`${label}不能为空`)
  }
  if (characterCount > maxLength) {
    throw new Error(`${label}长度不能超过${maxLength}个字符`)
  }
  if (!allowedName.test(normalized)) {
    throw new Error(`${label}只能使用中文、英文、数字、下划线和@`)
  }
  return normalized
}

export function limitRestrictedNameLength(value, maxLength) {
  return Array.from(String(value ?? '')).slice(0, maxLength).join('')
}
