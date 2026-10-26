export function parseQuestionAnswerValues(value) {
  if (value == null || value === '') return []
  if (Array.isArray(value)) return value
  if (typeof value !== 'string') return [value]
  try {
    const parsed = JSON.parse(value)
    if (parsed == null) return []
    return Array.isArray(parsed) ? parsed : [parsed]
  } catch {
    return [value]
  }
}

function answerValueText(value) {
  if (value == null) return ''
  if (typeof value !== 'object') return String(value)
  try { return JSON.stringify(value) } catch { return String(value) }
}

export function buildQuestionAnswerText(record) {
  return parseQuestionAnswerValues(record?.answer)
    .map((value, index) => record?.source === 2 ? String(index) : answerValueText(value))
    .join('\n')
}
