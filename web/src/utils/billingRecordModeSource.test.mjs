import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const pages = {
  账单明细: path.join(__dirname, '..', 'pages', 'BillingRecordList', 'index.vue'),
  账单流水: path.join(__dirname, '..', 'pages', 'UserUsage', 'index.vue'),
};

// 两个账单页面都必须展示「模式」列（深度思考 / 普通搜索），否则只有管理端能看到。
test('billing pages render the search mode column', async () => {
  for (const [label, pagePath] of Object.entries(pages)) {
    const content = await readFile(pagePath, 'utf8');
    assert.match(content, /title: '模式'/, `${label} 缺少模式列`);
    assert.match(content, /#bodyCell="\{ column, record, text \}"/, `${label} 缺少现代单元格插槽`);
    assert.match(content, /column.key === 'searchMode'/, `${label} 缺少模式列模板`);
    assert.match(content, /buildBillingRecordModeText\(record\)/, `${label} 没有用共享的模式映射函数`);
    assert.match(content, /buildBillingRecordModeText,/, `${label} 没有暴露 buildBillingRecordModeText`);
  }
});

test('billing detail table merges administrator fields into the detail action', async () => {
  const content = await readFile(pages.账单明细, 'utf8');
  assert.doesNotMatch(content, /title: '操作管理员'/);
  assert.doesNotMatch(content, /title: '余额变化'/);
  assert.doesNotMatch(content, /title: '账单流水展示'/);
  assert.match(content, /<BillingDetailDialog/);
  assert.match(content, /@statement-visibility-change="handleStatementVisibilityChange"/);
  assert.match(content, /:viewer-scope="isAdmin \? 'admin' : 'self'"/);
  assert.doesNotMatch(content, /Swal|document\.querySelector|addEventListener/);
  assert.doesNotMatch(content, /title: '操作管理员'/);
  assert.doesNotMatch(content, /title: '余额变化'/);
  assert.doesNotMatch(content, /title: '账单流水展示'/);
});
