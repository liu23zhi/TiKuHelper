import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { buildLogStructuredFields } from './logListHelpers.mjs';

const pagePath = new URL('../pages/LogList/index.vue', import.meta.url);

test('log table has the requested columns, fixed layout, and wrapping centered cells', async () => {
  const source = await readFile(pagePath, 'utf8');
  const titles = [...source.matchAll(/title: '([^']+)'/g)].map((match) => match[1]);
  assert.deepEqual(titles, [
    '时间', '模块', '状态', '用户', '来源 IP', '本地题库 ID', '账单 ID',
    '说明', '命中渠道 / 错误原因', '计费渠道 / 耗时', '详情',
    '请求 ID', '请求参数', '请求接口', '答案数', '原始请求', '原始响应',
  ]);
  assert.match(source, /table-layout="fixed"/);
  assert.match(source, /:scroll="\{ x: tableScrollX \}"/);
  assert.match(source, /text-align:\s*center/);
  assert.match(source, /vertical-align:\s*middle/);
  assert.match(source, /overflow-wrap:\s*anywhere/);
  assert.match(source, /class="mobile-log-time">\{\{ formatDateForTable\(record\.createTime\) \}\}/);
  assert.match(source, /\.mobile-log-time\s*\{\s*white-space:\s*pre-line/);
  assert.doesNotMatch(source, /log-cell-nowrap/);
});

test('log widths account for the named samples and expand entire snapshot columns', async () => {
  const source = await readFile(pagePath, 'utf8');
  assert.match(source, /const logColumnWidths =/);
  assert.match(source, /moduleLabels = \['认证', '题目', '搜题', 'AI 兜底', '系统', '验证码', '用户管理', '账单管理'\]/);
  assert.match(source, /module: Math\.max\(\.\.\.moduleLabels\.map/);
  for (const [key, width] of Object.entries({
    createTime: 112, username: 170, clientIp: 262, qid: 104,
    billingIds: 104, summary: 156, hitProvider: 170,
    billingProvider: 170, detail: 170, requestId: 310,
    mode: 160, requestSource: 124, answerCount: 76,
    requestSnapshot: 170, responseSnapshot: 170,
  })) {
    assert.match(source, new RegExp(`${key}: ${width}(?:,|\\b)`));
  }
  assert.match(source, /\? 236 : logColumnWidths\[field\]/);
  assert.match(source, /expandedColumnWidth\('requestSnapshot'\)/);
  assert.match(source, /expandedColumnWidth\('responseSnapshot'\)/);
  assert.match(source, /Object\.values\(logColumnWidths\)\.reduce\(\(sum, width\) => sum \+ width, 0\)/);
});

test('log request parameters can be rendered one per line without changing detail text', () => {
  const row = { oldAnswer: 'method=POST path=/adapter-service/search query=use=ai contentType=application/json body={"deepThinking":true,"skipCache":false}' };
  assert.equal(buildLogStructuredFields(row).mode, 'use=ai deepThinking=true skipCache=false');
  assert.equal(buildLogStructuredFields(row, '\n').mode, 'use=ai\ndeepThinking=true\nskipCache=false');
});
