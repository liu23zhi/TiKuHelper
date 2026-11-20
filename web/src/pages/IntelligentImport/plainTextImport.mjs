export function plainTextToParserHtml(value) {
  if (typeof value !== 'string' || !value.trim()) throw new Error('文本不能为空')
  const escapeHtml = text => text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  return value.split(/\r\n|\r|\n/).map(line => `<p>${escapeHtml(line)}</p>`).join('')
}
