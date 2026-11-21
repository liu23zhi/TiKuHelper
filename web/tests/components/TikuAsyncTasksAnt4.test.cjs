const { test } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')

const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
const read = relative => fs.readFileSync(path.join(root, relative), 'utf8')

test('题库传输区使用单一上传入口并在确认后提交导入', () => {
  const panel = read('src/pages/TikuComponent/QuestionTransferPanel.vue')
  assert.equal((panel.match(/type="file"/g) || []).length, 1)
  assert.match(panel, /previewQuestionImport/)
  assert.match(panel, /getQuestionImportPreview/)
  assert.match(panel, /confirmQuestionImport/)
  assert.match(panel, /<a-pagination\b[^\n]*:onChange="bind\(loadPreviewPage\)"/)
  assert.doesNotMatch(panel, /dryRun|仅预览|单批导入上限|单次导出上限/)
})

test('题库传输区提供 JSON、CSV、XLSX、XML、HTML 异步导出和模板入口', () => {
  const panel = read('src/pages/TikuComponent/QuestionTransferPanel.vue')
  const api = read('src/pages/TikuComponent/questionTransferApi.mjs')
  const helpers = read('src/pages/TikuComponent/questionTransferHelpers.mjs')
  for (const format of ['json', 'csv', 'xlsx', 'xml', 'html']) assert.match(api, new RegExp(`["']${format}["']`))
  assert.match(panel, /创建导出任务/)
  assert.match(panel, /下载模板/)
  assert.match(panel, /deleteQuestionExportResult/)
  assert.match(helpers, /成功 \$\{succeeded\}/)
})

test('题库异步任务始终展示提交时间并保留最近五个导出结果', () => {
  const panel = read('src/pages/TikuComponent/QuestionTransferPanel.vue')
  const helpers = read('src/pages/TikuComponent/questionTransferHelpers.mjs')
  assert.match(panel, /异步任务/)
  assert.match(panel, /formatTransferSubmittedAt\(task\.createdAt\)/)
  assert.match(helpers, /slice\(0, 5\)/)
  assert.match(panel, /删除结果/)
})

test('题库异步任务完成文案保留成功数量', () => {
  const helpers = read('src/pages/TikuComponent/questionTransferHelpers.mjs')
  assert.match(helpers, /任务完成：\$\{parts\.join\('，'\)\}/)
})
