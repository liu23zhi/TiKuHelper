export function buildImportReviewItems(paper = [], submissionReason = '') {
  return paper.flatMap((item, index) => {
    const reasons = []
    if (typeof item.content !== 'string' || !item.content.trim()) reasons.push('题干为空')
    if (item.answer === undefined) reasons.push('缺少答案字段')
    else if (!Array.isArray(item.answer) || item.answer.some(answer => typeof answer === 'string' && !answer.trim())) reasons.push('答案格式不完整')
    else if (item.options?.length && item.answer.some(answer => typeof answer !== 'string' || !/^[A-Z]$/.test(answer) || answer.charCodeAt(0) - 65 >= item.options.length)) reasons.push('答案与选项不匹配')
    if (item.hintMsg) reasons.push(String(item.hintMsg))
    if (submissionReason) reasons.push(submissionReason)
    return reasons.length ? [{ index: index + 1, item, reason: reasons.join('；') }] : []
  })
}

export function buildImportReviewReport(items = []) {
  return ['智能导入待核对题目', '仅列出待校对题目或提交结果未逐题确认的题目，请核对题库后再决定是否重试。', '',
    ...items.map(({ index, item, reason }) => [
      `第 ${index} 题｜待核对原因：${reason}`,
      `【${item.typeAlias || '未识别题型'}】`,
      item.content || '[空题干]',
      ...(item.options || []).map((option, optionIndex) => `${option.name ?? String.fromCharCode(65 + optionIndex)}. ${option.value}`),
      item.answer === undefined ? '答案：[缺少答案字段]' : '答案：' + JSON.stringify(item.answer),
      ...['course_name', 'source_name', 'extra'].filter(field => item[field] !== undefined).map(field => `${({ course_name: '课程', source_name: '来源', extra: '备注' })[field]}：${item[field]}`),
    ].join('\n')),
  ].join('\n\n')
}
