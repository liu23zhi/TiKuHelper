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

test('SystemSettings exposes the default new-user bonus amount', async () => {
  const content = await readFile(systemSettingsPagePath, 'utf8');

  assert.match(content, /formState\.newUserBonusAmount/);
  assert.match(content, /新用户赠送金额/);
  assert.match(content, /新用户注册或后台创建成功后自动计入余额/);
  assert.match(content, /newUserBonusAmount: normalizeBonusAmount\(this\.formState\.newUserBonusAmount\)/);
});

test('SystemSettings exposes a validated byte-based maximum log size with a 5GB default', async () => {
  const content = await readFile(systemSettingsPagePath, 'utf8');

  assert.match(content, /const DEFAULT_MAX_LOG_SIZE_BYTES = 5 \* 1024 \* 1024 \* 1024/);
  assert.match(content, /maxLogSizeBytes: DEFAULT_MAX_LOG_SIZE_BYTES/);
  assert.match(content, /formState\.maxLogSizeBytes = normalizeMaxLogSizeBytes\(data\.maxLogSizeBytes\)/);
  assert.match(content, /maxLogSizeBytes,/);
  assert.match(content, /最大日志大小必须是不小于/);
  assert.match(content, /日志目录全部日志文件的总占用上限/);
});

test('SystemSettings removes the non-password login derived status block', async () => {
  const content = await readFile(systemSettingsPagePath, 'utf8');

  assert.doesNotMatch(content, /非密码登录/);
  assert.doesNotMatch(content, /由短信验证码登录或微信扫码登录自动决定/);
  assert.doesNotMatch(content, /alternativeLoginEnabled/);
});
