import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const testDir = dirname(fileURLToPath(import.meta.url));
const pagePath = resolve(testDir, '../pages/AccessTokenManagement/index.vue');

test('AccessTokenManagement page restores copy and show-all controls', async () => {
  const content = await readFile(pagePath, 'utf8');

  assert.match(content, /显示全部/);
  assert.match(content, /隐藏中间部分/);
  assert.match(content, /copyTokenValue\(record\)/);
  assert.match(content, /toggleTokenVisibility\(record\.id\)/);
  assert.match(content, /displayTokenValue\(record\)/);
  assert.match(content, /visibleTokenIds/);
  assert.match(content, /token-value-text--visible/);
  assert.match(content, /white-space:\s*normal/);
  assert.match(content, /:scroll="\{ x: 'max-content' \}"/);
  assert.doesNotMatch(content, /width: 560/);
});

test('AccessTokenManagement page restricts the all-token view to admins and adds usernames', async () => {
  const content = await readFile(pagePath, 'utf8');

  assert.match(content, /显示全部令牌/);
  assert.match(content, /只看我的令牌/);
  assert.match(content, /v-if="isAdmin"/);
  assert.match(content, /showAllTokens/);
  assert.match(content, /buildAccessTokenListParams\(showAllTokens\.value\)/);
  assert.match(content, /title: '用户名'/);
});
