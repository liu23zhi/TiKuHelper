import assert from 'node:assert/strict'
import test from 'node:test'
import {
  describeTransferTask,
  formatTransferSubmittedAt,
  limitQuestionTransferTasks,
  QUESTION_IMPORT_ACCEPT,
  QUESTION_TRANSFER_FORMATS,
  transferTaskRows,
} from './questionTransferHelpers.mjs'

test('import accepts common structured question formats, including real XLS and text', () => {
  for (const extension of ['.json', '.csv', '.xlsx', '.xls', '.xml', '.html', '.htm', '.txt']) {
    assert.ok(QUESTION_IMPORT_ACCEPT.includes(extension), `missing ${extension}`)
  }
})

test('exports and templates expose the requested common formats', () => {
  assert.deepEqual(QUESTION_TRANSFER_FORMATS.map(item => item.value), ['json', 'csv', 'xlsx', 'xml', 'html'])
})

test('task rows support list envelopes and retain active rows plus five latest exports', () => {
  const tasks = [
    { type: 'import', taskId: 'active', status: 'running', createdAt: '2026-11-20T09:00:00Z' },
    ...Array.from({ length: 7 }, (_, index) => ({
      type: 'export', taskId: `e${index}`, status: 'succeeded',
      createdAt: `2026-11-${String(20 - index).padStart(2, '0')}T09:00:00Z`,
    })),
  ]
  assert.deepEqual(transferTaskRows({ data: { data: tasks, total: 8 } }), tasks)
  const visible = limitQuestionTransferTasks(tasks)
  assert.ok(visible.some(task => task.taskId === 'active'))
  assert.equal(visible.filter(task => task.type === 'export').length, 5)
  assert.deepEqual(visible.filter(task => task.type === 'export').map(task => task.taskId), ['e0', 'e1', 'e2', 'e3', 'e4'])
})

test('task summaries retain the persistent completion wording and render submitted times', () => {
  assert.equal(describeTransferTask({ status: 'succeeded', succeeded: 350 }), '任务完成：成功 350')
  assert.equal(formatTransferSubmittedAt('2026-11-20T10:06:36Z'), '2026/11/20 18:06:36')
  assert.equal(formatTransferSubmittedAt('bad-date'), '-')
})
