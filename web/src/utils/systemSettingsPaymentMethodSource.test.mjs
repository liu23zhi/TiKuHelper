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
  const panel = await readFile(path.join(__dirname, '..', 'pages', 'SystemSettings', 'PaymentMethodPanel.vue'), 'utf8');

  assert.match(panel, /record\.epayType\s*\|\|\s*'-'/);
  assert.match(content, /ePayType:\s*record\.epayType\s*\|\|\s*''/);
  assert.doesNotMatch(panel, /record\.ePayType\s*\|\|\s*'-'/);
  assert.doesNotMatch(content, /ePayType:\s*record\.ePayType\s*\|\|\s*''/);
  assert.match(content, /:onEdit="paymentPanelEntry\.bind\(openEditPaymentMethodModal\)"/);
  assert.match(content, /:onDelete="paymentPanelEntry\.bind\(handleDeletePaymentMethod\)"/);
  assert.match(content, /import SafePaymentIcon from '@\/components\/SafePaymentIcon\.js'/, 'inline preview binds the strict VNode renderer');
  const registeredComponents = content.match(/components:\s*\{([^}]+)\}/)?.[1] || '';
  for (const component of ['PaymentMethodPanel', 'SafePaymentIcon', 'PaymentMethodIconPicker', 'SystemMetricsPanel']) {
    assert.match(registeredComponents, new RegExp(`\\b${component}\\b`), `${component} remains registered`);
    assert.match(content, new RegExp(`import ${component} from `), `${component} remains imported`);
    assert.match(content, new RegExp(`<${component}(?:\\s|/|>)`), `${component} remains mounted`);
  }
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
  assert.match(content, /newUserBonusAmount: normalizeBonusAmount\(formState\.newUserBonusAmount\)/);
});

test('SystemSettings exposes a configurable minimum recharge amount with a 1 yuan default', async () => {
  const content = await readFile(systemSettingsPagePath, 'utf8');

  assert.match(content, /const DEFAULT_MIN_RECHARGE_AMOUNT = 1/);
  assert.match(content, /formState\.minRechargeAmount/);
  assert.match(content, /充值最低金额/);
  assert.match(content, /默认 1 元/);
  assert.match(content, /minRechargeAmount: DEFAULT_MIN_RECHARGE_AMOUNT/);
  assert.match(content, /formState\.minRechargeAmount = normalizeMinRechargeAmount\(data\.minRechargeAmount\)/);
  assert.match(content, /minRechargeAmount,/);
});

test('SystemSettings exposes a validated byte-based maximum log size with a 5GB default', async () => {
  const content = await readFile(systemSettingsPagePath, 'utf8');

  assert.match(content, /const DEFAULT_MAX_LOG_SIZE_BYTES = 5 \* 1024 \* 1024 \* 1024/);
  assert.match(content, /maxLogSizeValue: DEFAULT_MAX_LOG_SIZE_BYTES \/ LOG_SIZE_UNIT_MAP\.GB\.bytes/);
  assert.match(content, /maxLogSizeUnit: 'GB'/);
  assert.match(content, /const LOG_SIZE_UNITS = \[/);
  assert.match(content, /value: 'KB'/);
  assert.match(content, /value: 'MB'/);
  assert.match(content, /value: 'GB'/);
  assert.match(content, /value: 'TB'/);
  assert.match(content, /const maxLogSize = splitMaxLogSizeBytes\(data\.maxLogSizeBytes\)/);
  assert.match(content, /const maxLogSizeBytes = composeMaxLogSizeBytes\(formState\.maxLogSizeValue, formState\.maxLogSizeUnit\)/);
  assert.match(content, /maxLogSizeBytes,/);
  assert.match(content, /最大日志大小必须是不小于 1MB 的整数/);
  assert.match(content, /日志目录全部日志文件的总占用上限/);
});

test('SystemSettings removes the non-password login derived status block', async () => {
  const content = await readFile(systemSettingsPagePath, 'utf8');

  assert.doesNotMatch(content, /非密码登录/);
  assert.doesNotMatch(content, /由短信验证码登录或微信扫码登录自动决定/);
  assert.doesNotMatch(content, /alternativeLoginEnabled/);
});
