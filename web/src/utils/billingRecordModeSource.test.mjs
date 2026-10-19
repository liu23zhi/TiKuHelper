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
    assert.match(content, /customRender: 'searchMode'|customRender: \(\{ record \}\) => buildBillingRecordModeText\(record\)/, `${label} 的模式列没有渲染逻辑`);
    assert.match(content, /<template #searchMode="\{ record \}">|customRender: \(\{ record \}\) => buildBillingRecordModeText\(record\)/, `${label} 缺少模式列模板`);
    assert.match(content, /buildBillingRecordModeText\(record\)/, `${label} 没有用共享的模式映射函数`);
    assert.match(content, /buildBillingRecordModeText,/, `${label} 没有暴露 buildBillingRecordModeText`);
  }
});
