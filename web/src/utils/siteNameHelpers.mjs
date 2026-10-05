export const DEFAULT_SITE_NAME = '题库'

export function normalizeSiteName(value) {
  const name = String(value || '').trim()
  return name || DEFAULT_SITE_NAME
}
