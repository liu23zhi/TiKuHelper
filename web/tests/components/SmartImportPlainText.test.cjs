const { test } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(root, 'tests/helpers/vue-dom.cjs'))

test('纯文本按段落转为安全的解析 HTML，保留换行并转义标签字符', () => {
  const helperPath = path.join(root, 'src/pages/IntelligentImport/plainTextImport.mjs')
  assert.ok(fs.existsSync(helperPath), '应提供纯文本解析适配器')
  const { plainTextToParserHtml } = require(helperPath)
  assert.equal(plainTextToParserHtml('1. <题目>\n答案：A & B'), '<p>1. &lt;题目&gt;</p><p>答案：A &amp; B</p>')
  assert.equal(plainTextToParserHtml('甲\r\n\r\n乙'), '<p>甲</p><p></p><p>乙</p>')
  assert.throws(() => plainTextToParserHtml(' \n '), /文本不能为空/)
})

test('待核对下载区分缺答案与显式空答案，并标注无法逐题确认的提交结果', () => {
  const helperPath = path.join(root, 'src/pages/IntelligentImport/importReviewReport.mjs')
  assert.ok(fs.existsSync(helperPath), '应提供 Word/文本题目的校对报告')
  const { buildImportReviewItems, buildImportReviewReport } = require(helperPath)
  const paper = [
    { content: '已含答案', typeAlias: '简答题', answer: ['重复', '重复'] },
    { content: '缺答案题目', typeAlias: '简答题', course_name: '课程原文', extra: '备注原文' },
    { content: '显式空答案', typeAlias: '简答题', answer: [] },
  ]
  const missing = buildImportReviewItems(paper)
  assert.equal(missing.length, 1)
  const report = buildImportReviewReport(missing)
  assert.match(report, /第 2 题/)
  assert.match(report, /缺少答案字段/)
  assert.match(report, /缺答案题目/)
  assert.match(report, /课程原文/)
  assert.doesNotMatch(report, /已含答案|显式空答案/)
  const uncertain = buildImportReviewItems(paper, '提交结果未逐题确认')
  assert.equal(uncertain.length, 3)
  assert.match(buildImportReviewReport(uncertain), /提交结果未逐题确认/)
  assert.match(buildImportReviewReport(uncertain), /\["重复","重复"\]/)
})
