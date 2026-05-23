import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const userListPagePath = path.join(__dirname, '..', 'pages', 'UserList', 'index.vue');

test('UserList create-user modal explains the password rule and uses the dedicated create payload helper', async () => {
  const content = await readFile(userListPagePath, 'utf8');

  assert.match(content, /<a-form-item\s+v-if="!isEditing"\s+label="密码"\s+required>[\s\S]*密码至少需要 8 位，且需包含大小写字母、数字和特殊字符。/);
  assert.match(content, /buildAdminCreateUserPayload/);
  assert.doesNotMatch(content, /message\.warning\('请输入用户名和密码'\)/);
});
