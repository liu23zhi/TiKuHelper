export const publicContactKinds = Object.freeze([
  Object.freeze({ kind: 'email', label: '邮箱', maxLength: 254 }),
  Object.freeze({ kind: 'wecom', label: '企业微信', maxLength: 100 }),
  Object.freeze({ kind: 'wechat_official', label: '公众号', maxLength: 100 }),
])

export function readAgreementChannels(data) {
  if (!Array.isArray(data?.channels) || data.channels.length > publicContactKinds.length) return null
  const channels = [], seen = new Set()
  for (const row of data.channels) {
    const field = publicContactKinds.find(item => item.kind === row?.kind)
    if (!field || seen.has(field.kind) || typeof row.value !== 'string' || !row.value.trim() ||
        Array.from(row.value).length > field.maxLength) return null
    seen.add(field.kind)
    channels.push({ kind: field.kind, label: field.label, value: row.value })
  }
  return channels
}
