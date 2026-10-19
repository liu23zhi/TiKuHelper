import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildQuestionBulkActionIds,
  buildQuestionCourseNameCandidates,
  buildQuestionExportPayload,
  buildQuestionImportDryRunPayload,
  buildQuestionImportExecutePayload,
  buildQuestionImportSummary,
  buildQuestionSelectionSummary,
  buildQuestionSourceLabel,
  normalizeQuestionTypeFilter,
  parseQuestionImportText,
} from './topicOverviewHelpers.mjs';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const topicOverviewPagePath = path.join(__dirname, '..', 'pages', 'TikuComponent', 'index.vue');

test('normalizeQuestionTypeFilter converts cleared values to the unfiltered sentinel', () => {
  assert.equal(normalizeQuestionTypeFilter(null), -1);
  assert.equal(normalizeQuestionTypeFilter(undefined), -1);
  assert.equal(normalizeQuestionTypeFilter(''), -1);
  assert.equal(normalizeQuestionTypeFilter('0'), 0);
  assert.equal(normalizeQuestionTypeFilter(3), 3);
});

test('buildQuestionCourseNameCandidates prefers matching existing names without duplicates', () => {
  assert.deepEqual(buildQuestionCourseNameCandidates('A', [
    { label: 'AB', value: 'AB' },
    { label: 'A1', value: 'A1' },
    { label: 'AB', value: 'AB' },
    { label: '数学', value: '数学' },
  ]), [
    { label: 'AB', value: 'AB' },
    { label: 'A1', value: 'A1' },
  ]);

  assert.deepEqual(buildQuestionCourseNameCandidates('   ', [
    { label: 'AB', value: 'AB' },
  ]), []);
});

test('buildQuestionCourseNameCandidates keeps AutoComplete-compatible label-value options', () => {
  assert.deepEqual(buildQuestionCourseNameCandidates('高', [
    { label: '高等数学', value: '高等数学' },
    { label: '线性代数', value: '线性代数' },
  ]), [
    { label: '高等数学', value: '高等数学' },
  ]);
});

test('buildQuestionBulkActionIds keeps unique positive integer ids in order', () => {
  assert.deepEqual(buildQuestionBulkActionIds([3, '7', 3, 0, null, 'bad', 12]), [3, 7, 12]);
});

test('buildQuestionExportPayload serializes selected question rows for download', () => {
  const result = buildQuestionExportPayload([
    {
      id: 11,
      type: 1,
      course_name: '数学',
      question: '2 + 2 = ?',
      options: '["3","4"]',
      answer: '["4"]',
      plat: 2,
      extra: '基础题',
    },
  ]);

  assert.equal(result.filename, 'questions-export.json');
  assert.match(result.content, /"id": 11/);
  assert.match(result.content, /"course_name": "数学"/);
  assert.match(result.content, /"question": "2 \+ 2 = \?"/);
});

test('buildQuestionSourceLabel trims provider source names and falls back to manual import', () => {
  assert.equal(buildQuestionSourceLabel('  万能题库  '), '万能题库');
  assert.equal(buildQuestionSourceLabel('   '), '人工导入');
  assert.equal(buildQuestionSourceLabel(null), '人工导入');
});

test('parseQuestionImportText returns question items from a JSON array', () => {
  const result = parseQuestionImportText('[{"id":7,"question":"测试题"}]');

  assert.deepEqual(result, [{ id: 7, question: '测试题' }]);
});

test('parseQuestionImportText rejects non-array JSON payloads', () => {
  assert.throws(() => parseQuestionImportText('{"id":7}'), /JSON 文件内容必须是题目数组/);
  assert.throws(() => parseQuestionImportText('bad-json'), /JSON 文件解析失败/);
});

test('buildQuestionImportDryRunPayload wraps parsed items in dry-run mode', () => {
  assert.deepEqual(buildQuestionImportDryRunPayload([{ id: 1 }]), {
    dryRun: true,
    overwrite: false,
    items: [{ id: 1 }],
  });
});

test('buildQuestionImportExecutePayload keeps overwrite choice for final import', () => {
  assert.deepEqual(buildQuestionImportExecutePayload([{ id: 9 }], true), {
    overwrite: true,
    items: [{ id: 9 }],
  });
  assert.deepEqual(buildQuestionImportExecutePayload([{ id: 9 }], false), {
    overwrite: false,
    items: [{ id: 9 }],
  });
});

test('buildQuestionImportSummary describes dry-run collisions clearly', () => {
  assert.equal(
    buildQuestionImportSummary({ existingIds: [7, 9], created: 0, updated: 0, skipped: 0 }),
    '检测到 2 条题目已存在（ID: 7, 9）。选择“是”将直接覆盖，选择“否”只导入不存在的数据。'
  );
  assert.equal(
    buildQuestionImportSummary({ existingIds: [], created: 3, updated: 0, skipped: 0 }),
    '共准备导入 3 条题目，未发现 ID 冲突。'
  );
});

test('buildQuestionSelectionSummary renders selected and visible totals', () => {
  assert.equal(buildQuestionSelectionSummary(0, 10), '已选择 0 项，共 10 项');
  assert.equal(buildQuestionSelectionSummary(3, 10), '已选择 3 项，共 10 项');
});

test('Topic overview exposes select-all and clear-selection controls', async () => {
  const content = await readFile(topicOverviewPagePath, 'utf8');

  assert.match(content, /buildQuestionSelectionSummary/);
  assert.match(content, /class="selection-alert"/);
  assert.match(content, /handleSelectAll/);
  assert.match(content, /handleSelectCurrentPage/);
  assert.match(content, /handleClearCurrentPage/);
  assert.match(content, /handleClearSelection/);
  assert.match(content, /loadAllFilteredQuestions/);
  assert.match(content, /preserveSelectedRowKeys:\s*true/);
  assert.match(content, /page\.total/);
  assert.match(content, /全选本页/);
  assert.match(content, /取消本页选择/);
  assert.match(content, /全选所有页/);
  assert.match(content, /取消全部选择/);
});
