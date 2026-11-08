// Entity.Tiku.Type is int32. Normalize only whole decimal integers supplied by
// the editor/legacy JSON; never parse a partial prefix or coerce null to zero.
export function normalizeEditorQuestionType(value) {
  if (typeof value === 'string') {
    const text = value.trim()
    if (!/^[+-]?\d+$/.test(text)) return undefined
    value = Number(text)
  }
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < -2147483648 || value > 2147483647) return undefined
  return value === 0 ? 0 : value
}

// Same objective-question policy as pkg/util.IsObjectiveQuestionType.
// Unknown valid integer types remain free text, including legacy reading types.
export function isObjectiveQuestionType(value) {
  const type = normalizeEditorQuestionType(value)
  return type === 0 || type === 1 || type === 3
}

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
