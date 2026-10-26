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

  assert.match(content, /<a-form-item\s+v-if="!isEditing"\s+label="密码"\s+required>[\s\S]*弱密码/);
  assert.match(content, /buildAdminCreateUserPayload/);
  assert.match(content, /runAdminPasswordAction/);
  assert.match(content, /Swal\.fire/);
  assert.match(content, /confirmWeakPassword/);
  assert.doesNotMatch(content, /async confirmWeakPassword\(action, password\)/);
  assert.match(content, /async confirmWeakPassword\(action\)/);
  assert.match(content, /撞库/);
  assert.doesNotMatch(content, /message\.warning\('请输入用户名和密码'\)/);
});

test('UserList password forms state that weak passwords require confirmation', async () => {
  const content = await readFile(userListPagePath, 'utf8');
  assert.match(content, /弱密码.*二次确认/);
  assert.match(content, /async onSubmit\(\)/);
  assert.match(content, /async submitResetPassword\(\)/);
});

test('UserList table exposes the user id column first', async () => {
  const content = await readFile(userListPagePath, 'utf8');

  assert.match(content, /title: 'ID',\s+dataIndex: 'id',\s+key: 'id',/);
});

test('UserList follows fixed, centered table layout with two-line Shanghai times and locations', async () => {
  const content = await readFile(userListPagePath, 'utf8');

  assert.match(content, /table-layout="fixed"/);
  assert.match(content, /:scroll="\{ x: tableScrollX \}"/);
  assert.match(content, /const userTableScrollX = Object\.values\(userTableColumnWidths\)\.reduce/);
  assert.match(content, /tableScrollX: userTableScrollX/);
  assert.match(content, /width: userTableColumnWidths\.(?:username|lastLoginIp|operation)/);
  assert.match(content, /return formatBillingRecordTime\(value\)/);
  assert.match(content, /class="user-time-cell"/);
  assert.match(content, /\.user-time-cell\s*\{[^}]*white-space:\s*pre-wrap/s);
  assert.match(content, /\.location-value\s*\{[^}]*white-space:\s*pre-wrap[^}]*text-align:\s*center/s);
  assert.match(content, /\.desktop-user-table \.ant-table-thead > tr > th[\s\S]*text-align:\s*center;[\s\S]*vertical-align:\s*middle;[\s\S]*overflow-wrap:\s*anywhere;/);
});

test('UserList edit modal exposes wechat binding and force reassign controls', async () => {
  const content = await readFile(userListPagePath, 'utf8');

  assert.match(content, /label="微信 ID"[\s\S]*v-model:value="formState\.wechatId"/);
  assert.match(content, /label="重指派微信"[\s\S]*v-model:checked="formState\.forceReassignWechat"/);
  assert.match(content, /wechatId: record\.wechatId \|\| ''/);
});

test('UserList edit modal can resolve wechat id by scan or verification code', async () => {
  const content = await readFile(userListPagePath, 'utf8');

  assert.match(content, /扫码获取 ID/);
  assert.match(content, /验证码获取 ID/);
  assert.match(content, /startWechatScan/);
  assert.match(content, /resolveAdminWechatIDByScan/);
  assert.match(content, /resolveAdminWechatIDByCode/);
  assert.match(content, /formState\.value\.wechatId = .*wechatId/);
});

test('UserList create modal exposes editable new-user bonus controls', async () => {
  const content = await readFile(userListPagePath, 'utf8');

  assert.match(content, /label="启用赠送"[\s\S]*v-model:checked="formState\.giftEnabled"/);
  assert.match(content, /v-if="!isEditing && formState\.giftEnabled"[\s\S]*v-model:value="formState\.giftAmount"/);
  assert.match(content, /giftEnabled: true/);
  assert.match(content, /this\.userListContext\.formState\.value\.giftAmount = this\.userListContext\.systemNewUserBonusAmount\.value/);
});
