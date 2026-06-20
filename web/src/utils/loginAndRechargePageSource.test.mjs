import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const loginPagePath = path.join(__dirname, '..', 'pages', 'Login', 'index.vue');
const userUsagePagePath = path.join(__dirname, '..', 'pages', 'UserUsage', 'index.vue');
const wechatBindPagePath = path.join(__dirname, '..', 'pages', 'WechatBind', 'index.vue');
const routerPath = path.join(__dirname, '..', 'router', 'router.js');

test('Login page only renders sms login mode when public sms login is enabled', async () => {
  const content = await readFile(loginPagePath, 'utf8');

  assert.match(content, /<a-radio-button\s+v-if="publicAuthSettings\.smsLoginEnabled"\s+value="sms"/);
  assert.match(content, /<a-radio-button\s+v-if="publicAuthSettings\.wechatLoginEnabled"\s+value="wechat"/);
  assert.doesNotMatch(content, /<a-radio-button\s+value="sms"[^>]*:disabled="!publicAuthSettings\.smsLoginEnabled"/);
});

test('Login page supports wechat QR login and need-bind redirect', async () => {
  const content = await readFile(loginPagePath, 'utf8');

  assert.match(content, /createWechatLoginQRCode/);
  assert.match(content, /getWechatLoginStatus/);
  assert.match(content, /loginMode\.value === 'wechat'/);
  assert.match(content, /wechatQrCodeImageData/);
  assert.match(content, /need_bind/);
  assert.match(content, /\/wechat-bind/);
});

test('Wechat bind page reads bindToken and submits existing account credentials', async () => {
  const content = await readFile(wechatBindPagePath, 'utf8');

  assert.match(content, /name: 'WechatBind'/);
  assert.match(content, /route\.query\.bindToken/);
  assert.match(content, /bindWechatLogin/);
  assert.match(content, /username/);
  assert.match(content, /password/);
  assert.match(content, /finishLogin/);
});

test('Router exposes wechat bind route under login layout', async () => {
  const content = await readFile(routerPath, 'utf8');

  assert.match(content, /WechatBind/);
  assert.match(content, /path: 'wechat-bind'/);
  assert.match(content, /name: 'wechat-bind'/);
});

test('UserUsage page removes payment method count and exposes quick recharge amounts with inline recharge action', async () => {
  const content = await readFile(userUsagePagePath, 'utf8');
  const extraTemplateMatch = content.match(/<template #extra>([\s\S]*?)<\/template>/);

  assert.ok(extraTemplateMatch, 'expected recharge card extra template to exist');
  assert.doesNotMatch(content, /title="可用支付方式"/);
  assert.match(content, /const rechargeQuickAmounts = \[10, 20, 50, 100, 200, 500\]/);
  assert.match(content, /v-for="amount in rechargeQuickAmounts"/);
  assert.match(content, /class="recharge-quick-amounts"/);
  assert.match(content, /class="payment-method-grid"/);
  assert.match(content, /selectedPaymentMethodId/);
  assert.match(content, /selectPaymentMethod/);
  assert.match(content, /class="recharge-actions"/);
  assert.match(content, /@click="applyRechargeQuickAmount\(amount\)"/);
  assert.doesNotMatch(content, /buildPaymentMethodSwalHtml/);
  assert.doesNotMatch(content, /Swal\.fire\(\{\s*title: '请选择支付方式'/);
  assert.doesNotMatch(extraTemplateMatch[1], /handleOpenRechargeDialog/);
});
