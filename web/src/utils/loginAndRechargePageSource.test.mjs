import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const loginPagePath = path.join(__dirname, '..', 'pages', 'Login', 'index.vue');
const userUsagePagePath = path.join(__dirname, '..', 'pages', 'UserUsage', 'index.vue');

test('Login page only renders sms login mode when public sms login is enabled', async () => {
  const content = await readFile(loginPagePath, 'utf8');

  assert.match(content, /<a-radio-button\s+v-if="publicAuthSettings\.smsLoginEnabled"\s+value="sms"/);
  assert.doesNotMatch(content, /<a-radio-button\s+value="sms"[^>]*:disabled="!publicAuthSettings\.smsLoginEnabled"/);
});

test('UserUsage page removes payment method count and exposes quick recharge amounts with inline recharge action', async () => {
  const content = await readFile(userUsagePagePath, 'utf8');
  const extraTemplateMatch = content.match(/<template #extra>([\s\S]*?)<\/template>/);

  assert.ok(extraTemplateMatch, 'expected recharge card extra template to exist');
  assert.doesNotMatch(content, /title="可用支付方式"/);
  assert.match(content, /const rechargeQuickAmounts = \[10, 20, 50, 100, 200, 500\]/);
  assert.match(content, /v-for="amount in rechargeQuickAmounts"/);
  assert.match(content, /class="recharge-quick-amounts"/);
  assert.match(content, /class="recharge-actions"/);
  assert.match(content, /@click="applyRechargeQuickAmount\(amount\)"/);
  assert.doesNotMatch(extraTemplateMatch[1], /handleOpenRechargeDialog/);
});
