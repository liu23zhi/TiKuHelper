import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const systemSettingsPagePath = path.join(__dirname, '..', 'pages', 'SystemSettings', 'index.vue');

test('SystemSettings payment method management reads backend epayType field consistently', async () => {
  const content = await readFile(systemSettingsPagePath, 'utf8');

  assert.match(content, /record\.epayType\s*\|\|\s*'-'/);
  assert.match(content, /ePayType:\s*record\.epayType\s*\|\|\s*''/);
  assert.doesNotMatch(content, /record\.ePayType\s*\|\|\s*'-'/);
  assert.doesNotMatch(content, /ePayType:\s*record\.ePayType\s*\|\|\s*''/);
});

test('SystemSettings exposes wechat captcha and smsbao credential settings', async () => {
  const content = await readFile(systemSettingsPagePath, 'utf8');

  assert.match(content, /formState\.wechatLoginEnabled/);
  assert.match(content, /formState\.wechatScanLoginEnabled/);
  assert.match(content, /formState\.wechatCodeLoginEnabled/);
  assert.match(content, /formState\.wechatDefaultLoginMode/);
  assert.match(content, /formState\.wechatCodeStaticQrcodeUrl/);
  assert.match(content, /微信扫描/);
  assert.match(content, /微信验证码/);
  assert.match(content, /默认微信方式/);
  assert.match(content, /验证码二维码/);
  assert.match(content, /formState\.wechatApiBaseUrl/);
  assert.match(content, /formState\.wechatApiBearerToken/);
  assert.match(content, /formState\.captchaProvider/);
  assert.match(content, /formState\.captchaApiBaseUrl/);
  assert.match(content, /formState\.captchaApiKey/);
  assert.match(content, /formState\.captchaType/);
  assert.match(content, /formState\.smsbaoCredentialType/);
  assert.match(content, /formState\.smsbaoRegionMode/);
});

test('SystemSettings removes the non-password login derived status block', async () => {
  const content = await readFile(systemSettingsPagePath, 'utf8');

  assert.doesNotMatch(content, /非密码登录/);
  assert.doesNotMatch(content, /由短信验证码登录或微信扫码登录自动决定/);
  assert.doesNotMatch(content, /alternativeLoginEnabled/);
});
