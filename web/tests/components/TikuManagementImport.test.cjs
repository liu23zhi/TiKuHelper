const { test } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')

const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
const read = relative => fs.readFileSync(path.join(root, relative), 'utf8')

test('题库管理在页面页头右侧提供列设置，并展示题库摘要', () => {
  const page = read('src/pages/TikuComponent/index.vue')
  const heading = page.match(/<ConsolePageHeading[\s\S]*?<\/ConsolePageHeading>/)?.[0] || ''
  assert.match(heading, /TableColumnSettings/, '列设置应在页面级页头操作区')
  assert.match(page, /筛选结果题数/)
  assert.match(page, /当前页答案覆盖率/)
})

test('题库管理的筛选字段透传到已有查询接口', () => {
  const page = read('src/pages/TikuComponent/index.vue')
  const reads = read('src/pages/TikuComponent/useQuestionReads.mjs')
  const selection = read('src/pages/TikuComponent/useQuestionSelection.mjs')
  for (const label of ['题型', '题干', '课程', '来源', '答案状态']) assert.ok(page.includes(label), `缺少${label}筛选`)
  assert.match(reads, /courseName:\s*searchValue\.value\.courseName/)
  assert.match(selection, /courseName:\s*searchValue\.value\.courseName/)
  assert.match(reads, /source:\s*parseInt\(searchValue\.value\.source\)/)
  assert.match(reads, /onlyShowEmptyAnswer:\s*searchValue\.value\.onlyShowEmptyAnswer/)
})

test('题库管理使用单一上传入口，预览分页后确认异步导入', () => {
  const page = read('src/pages/TikuComponent/index.vue')
  const panel = read('src/pages/TikuComponent/QuestionTransferPanel.vue')
  const helpers = read('src/pages/TikuComponent/questionTransferHelpers.mjs')
  assert.match(page, /QuestionTransferPanel/)
  assert.equal((panel.match(/type="file"/g) || []).length, 1)
  assert.match(panel, /预览识别结果/)
  assert.match(panel, /:pagination="false"/)
  assert.match(panel, /<a-pagination\b[^\n]*:onChange="bind\(loadPreviewPage\)"/)
  assert.match(panel, /confirmQuestionImport/)
  assert.match(panel, /创建导出任务/)
  assert.match(panel, /describeTransferTask\(task\)/)
  assert.match(panel, /{{ formatTransferSubmittedAt\(task\.createdAt\) }}/)
  assert.match(panel, /删除结果/)
  assert.match(panel, /下载模板/)
  assert.match(panel, /选中的 \{\{ selectedIds.length \}\} 道/)
  assert.match(helpers, /\.xls/)
  assert.doesNotMatch(page + panel, /仅预览（dryRun：只校验不写库）|单批导入上限|单次导出上限/)
  assert.doesNotMatch(page, /导出 JSON/)
})

test('智能导入只提供 Word 和纯文本输入并保留校对导入工作区', () => {
  const page = read('src/pages/IntelligentImport/index.vue')
  assert.match(page, /accept="\.docx,\.txt"/)
  assert.equal((page.match(/<a-upload\b/g) || []).length, 1, 'DOCX 和 TXT 共用一个上传入口')
  assert.match(page, /上传文件/)
  assert.doesNotMatch(page, /上传 \.txt 文件/)
  assert.match(page, /textarea/)
  assert.match(page, /contenteditable/)
  assert.match(page, /确认导入/)
  for (const removed of ['submitImportTask', 'submitExportTask', 'asyncTaskPanel', 'dryRun', '.xlsx']) {
    assert.ok(!page.includes(removed), `智能导入不应保留 ${removed}`)
  }
})
