import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const loginPagePath = path.join(__dirname, '..', 'pages', 'Login', 'index.vue');
const userUsagePagePath = path.join(__dirname, '..', 'pages', 'UserUsage', 'index.vue');
const rechargeOrderListPagePath = path.join(__dirname, '..', 'pages', 'RechargeOrderList', 'index.vue');
const wechatBindPagePath = path.join(__dirname, '..', 'pages', 'WechatBind', 'index.vue');
const routerPath = path.join(__dirname, '..', 'router', 'router.js');
const apiPath = path.join(__dirname, '..', 'api', 'TikuComponentApi.js');
const siderMenuPath = path.join(__dirname, '..', 'components', 'SiderMenu', 'index.vue');

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

test('Login page supports wechat scan and code login modes from public settings', async () => {
  const content = await readFile(loginPagePath, 'utf8');
  const apiContent = await readFile(apiPath, 'utf8');

  assert.match(apiContent, /export function loginWechatByCode/);
  assert.match(apiContent, /url: `\/adapter-service\/public\/wechat-login\/code`/);
  assert.match(content, /wechatLoginSubMode/);
  assert.match(content, /wechatScanLoginEnabled/);
  assert.match(content, /wechatCodeLoginEnabled/);
  assert.match(content, /wechatDefaultLoginMode/);
  assert.match(content, /wechatCodeStaticQrcodeUrl/);
  assert.match(content, /value="scan"/);
  assert.match(content, /value="code"/);
  assert.match(content, /loginWechatByCode/);
  assert.match(content, /wechatCodeForm\.code/);
  assert.match(content, /submitWechatCodeLogin/);
});

test('Login page uses automatic websocket-first QR lifecycle with fallback', async () => {
  const content = await readFile(loginPagePath, 'utf8');

  assert.match(content, /createWechatQrStatusController/);
  assert.match(content, /startWechatLogin/);
  assert.match(content, /onRemaining/);
  assert.match(content, /实时连接/);
  assert.match(content, /轮询中/);
  assert.match(content, /二维码已过期/);
  assert.doesNotMatch(content, /setInterval\(\(\) => \{\s*void pollWechatLoginStatus/);
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

test('Router keeps recharge result callback page public without adapter auth layout', async () => {
  const content = await readFile(routerPath, 'utf8');

  assert.match(content, /path: '\/adapter\/recharge\/result'/);
  assert.match(content, /name: 'recharge-result'/);
  assert.match(content, /PUBLIC_ADAPTER_ROUTE_NAMES/);
  assert.match(content, /PUBLIC_ADAPTER_ROUTE_NAMES\.has\(String\(to\.name \|\| ''\)\)/);
  assert.doesNotMatch(content, /path: 'recharge\/result'/);
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

test('Recharge order list page is routed, menu-visible, and backed by recharge order API', async () => {
  const apiContent = await readFile(apiPath, 'utf8');
  const routerContent = await readFile(routerPath, 'utf8');
  const menuContent = await readFile(siderMenuPath, 'utf8');
  const pageContent = await readFile(rechargeOrderListPagePath, 'utf8');

  assert.match(apiContent, /export function getRechargeOrders/);
  assert.match(apiContent, /url: `\/adapter-service\/recharge-orders`/);
  assert.match(routerContent, /RechargeOrderList/);
  assert.match(routerContent, /path: 'recharge-orders'/);
  assert.match(menuContent, /key: 'recharge-orders'/);
  assert.match(menuContent, /label: '充值记录'/);
  assert.match(pageContent, /name: 'RechargeOrderList'/);
  assert.match(pageContent, /管理员可查看全部充值记录/);
  assert.match(pageContent, /普通用户只能查看自己的充值记录/);
  assert.match(pageContent, /getRechargeOrders/);
});

test('Recharge order list page renders server-side total and current-page amount summaries', async () => {
  const pageContent = await readFile(rechargeOrderListPagePath, 'utf8');

  assert.match(pageContent, /充值总数（筛选结果）/);
  assert.match(pageContent, /当前页条数/);
  assert.match(pageContent, /累计充值金额/);
  assert.match(pageContent, /当前页累计充值金额/);
  assert.match(pageContent, /summary\.totalAmount/);
  assert.match(pageContent, /summary\.currentPageAmount/);
  assert.match(pageContent, /data\?\.summary\?\.totalAmount/);
  assert.match(pageContent, /data\?\.summary\?\.currentPageAmount/);
});

test('UserUsage page renders qrcode payment results without forcing a cashier redirect', async () => {
  const content = await readFile(userUsagePagePath, 'utf8');

  assert.match(content, /paymentResultVisible/);
  assert.match(content, /paymentQRCodeDataUrl/);
  assert.match(content, /QRCode\.toDataURL/);
  assert.match(content, /data\?\.payType === 'qrcode'/);
  assert.match(content, /data\?\.qrcodeContent/);
  assert.match(content, /<a-modal[\s\S]*扫码支付/);
  assert.match(content, /class="payment-qrcode-image"/);
  assert.match(content, /openPaymentJumpFallback/);
  assert.doesNotMatch(content, /res\.data\?\.payUrl\)[\s\S]{0,120}window\.open\(res\.data\.payUrl/);
});

test('Billing pages render mixed ledger fields instead of search-only columns', async () => {
  const billingContent = await readFile(path.join(__dirname, '..', 'pages', 'BillingRecordList', 'index.vue'), 'utf8');
  const usageContent = await readFile(userUsagePagePath, 'utf8');

  for (const content of [billingContent, usageContent]) {
    assert.match(content, /buildBillingRecordTypeText/);
    assert.match(content, /buildBillingRecordSubjectText/);
    assert.match(content, /buildBillingRecordSourceText/);
    assert.match(content, /buildBillingRecordReferenceText/);
    assert.match(content, /buildBillingRecordAmountText/);
    assert.match(content, /title: '类型'/);
    assert.match(content, /title: '项目'/);
    assert.match(content, /title: '金额'/);
    assert.match(content, /title: '来源\/渠道'/);
    assert.match(content, /title: '关联单号'/);
    assert.doesNotMatch(content, /title: '题目'/);
    assert.doesNotMatch(content, /title: '消费金额'/);
  }
});

test('Billing record list renders the billing owner username', async () => {
  const content = await readFile(path.join(__dirname, '..', 'pages', 'BillingRecordList', 'index.vue'), 'utf8');

  assert.match(content, /#username="\{ record \}"/);
  assert.match(content, /record\.username/);
  assert.match(content, /title: '用户名'/);
  assert.match(content, /if \(!isAdmin\.value\) \{\s+return baseColumns/);
});

test('Billing record list opens a SweetAlert fee breakdown with provider costs', async () => {
  const content = await readFile(path.join(__dirname, '..', 'pages', 'BillingRecordList', 'index.vue'), 'utf8');

  assert.match(content, /import Swal from 'sweetalert2'/);
  assert.match(content, /显示详情/);
  assert.match(content, /Swal\.fire/);
  assert.match(content, /buildBillingRecordFeeDetailHtml/);
  assert.match(content, /handleShowFeeDetail/);
  assert.match(content, /billing-fee-card/);
  assert.match(content, /billing-fee-item/);
  assert.match(content, /billing-fee-total/);
});

test('Personal billing flow does not render the owner username column', async () => {
  const content = await readFile(userUsagePagePath, 'utf8');

  assert.doesNotMatch(content, /#username="\{ record \}"/);
  assert.doesNotMatch(content, /title: '用户名'/);
});
