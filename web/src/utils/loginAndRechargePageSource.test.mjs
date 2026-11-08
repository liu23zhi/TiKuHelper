import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';
import { parse, compileScript } from '@vue/compiler-sfc';
import { ensureRequestFailure, inheritRequestFailureView } from './requestFailureView.mjs';
import { inheritTransportRequestId } from './requestHelpers.mjs';
import { isAdminUser } from './currentUser.js';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const loginPagePath = path.join(__dirname, '..', 'pages', 'Login', 'index.vue');
const userUsagePagePath = path.join(__dirname, '..', 'pages', 'UserUsage', 'index.vue');
const tikuPagePath = path.join(__dirname, '..', 'pages', 'TikuComponent', 'index.vue');
const searchAccountPagePath = path.join(__dirname, '..', 'pages', 'SearchAccountConfig', 'index.vue');
const rechargeOrderListPagePath = path.join(__dirname, '..', 'pages', 'RechargeOrderList', 'index.vue');
const wechatBindPagePath = path.join(__dirname, '..', 'pages', 'WechatBind', 'index.vue');
const routerPath = path.join(__dirname, '..', 'router', 'router.js');
const apiPath = path.join(__dirname, '..', 'api', 'TikuComponentApi.js');
const siderMenuPath = path.join(__dirname, '..', 'components', 'SiderMenu', 'index.vue');

function loadPageMethod(source, name, scope) {
  const { descriptor } = parse(source);
  const compiled = compileScript(descriptor, { id: 'refresh-regression' });
  const options = compiled.scriptAst.find(node => node.type === 'ExportDefaultDeclaration').declaration.arguments[0];
  const methods = options.properties.find(property => property.key?.name === 'methods')?.value;
  const method = methods?.properties.find(property => property.key?.name === name);
  if (!method) {
    const setup = options.properties.find(property => property.key?.name === 'setup');
    const declaration = setup?.body?.body.flatMap(statement => statement.declarations || []).find(item => item.id?.name === name)
      || compiled.scriptAst.flatMap(statement => statement.declarations || []).find(item => item.id?.name === name);
    assert.ok(declaration?.init, `expected page method or setup function ${name}`);
    return vm.runInNewContext(`(${descriptor.script.content.slice(declaration.init.start, declaration.init.end)})`, scope);
  }
  const methodSource = descriptor.script.content.slice(method.start, method.end);
  return vm.runInNewContext(`({${methodSource}})`, scope)[name];
}

function deferredRequest() {
  let resolve;
  let reject;
  const promise = new Promise((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

test('Login page only renders sms login mode when public sms login is enabled', async () => {
  const content = await readFile(loginPagePath, 'utf8');

  assert.match(content, /<a-radio-button\s+v-if="publicAuthSettings\.smsLoginEnabled"\s+value="sms"/);
  assert.match(content, /<a-radio-button\s+v-if="publicAuthSettings\.wechatLoginEnabled"\s+value="wechat"/);
  assert.doesNotMatch(content, /<a-radio-button\s+value="sms"[^>]*:disabled="!publicAuthSettings\.smsLoginEnabled"/);
});

test('Login page shows a SweetAlert and registration action for an unregistered SMS phone', async () => {
  const content = await readFile(loginPagePath, 'utf8');

  assert.match(content, /isUnregisteredPhoneLoginMessage/);
  assert.match(content, /title: '手机号未注册'/);
  assert.match(content, /text: '该手机号未绑定账号，请先注册。'/);
  assert.match(content, /confirmButtonText: '前往注册'/);
  assert.match(content, /showUnregisteredPhoneAlert/);
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
  assert.match(content, /route\.query\??\.bindToken/);
  assert.match(content, /String\(route\.query\??\.bindToken \|\| ''\)\.trim\(\)/);
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
  const headingMatch = content.match(/<ConsolePageHeading[^>]*>([\s\S]*?)<\/ConsolePageHeading>/);

  assert.ok(headingMatch, 'expected page-level refresh heading to exist');
  assert.match(headingMatch[1], /@click="handleRefreshAll" :loading="pageRefreshing">刷新/);
  assert.doesNotMatch(content, /<template #extra>[\s\S]*?handleRefreshAll/);
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
  assert.doesNotMatch(headingMatch[1], /handleOpenRechargeDialog/);
});

test('Tiku page refresh preserves current filters and page and clears loading after failures', async () => {
  const content = await readFile(tikuPagePath, 'utf8');
  assert.match(content, /<ConsolePageHeading[\s\S]*?:onClick="questionEntry\.bind\(fetchData\)" :loading="tabLoading">刷新[\s\S]*?<\/ConsolePageHeading>/);
  for (const failure of [false, 'network', 'http']) {
    const request = deferredRequest();
    let params;
    let apiCalls = 0;
    const errors = [];
    const scope = {
      ensureRequestFailure,
      fileList: { value: [] }, tabLoading: { value: false }, data: { value: [] },
      typeValue: { value: 2 }, page: { value: { pageNo: 3, pageSize: 20 } },
      searchValue: { value: { question: '当前筛选', source: '2', extra: '课程', onlyShowEmptyAnswer: true } },
      normalizeQuestionTypeFilter: value => value, rememberQuestionRows: () => {},
      getQuestions: value => { apiCalls += 1; params = value; return request.promise; },
      message: { error: value => errors.push(value) },
    };
    // Keep the real owner and permission functions for this refresh-only fixture.
    const currentUser = { value: { id: 7301, username: 'synthetic-tiku-admin', role: 'admin' } };
    const principal = () => 'id:7301';
    const role = () => currentUser.value.role;
    const questionRequestOwner = { active: true, epoch: 0, listVersion: 0, selectionVersion: 0 };
    const canUseQuestions = loadPageMethod(content, 'canUseQuestions', { principal, currentUser, isAdminUser });
    const captureQuestionOwner = loadPageMethod(content, 'captureQuestionOwner', { questionRequestOwner, principal, role, canUseQuestions });
    const context = { listFailure: null, listFailureContext: 0, questionRequestOwner, captureQuestionOwner, rememberQuestionRows: scope.rememberQuestionRows };
    for (const name of ['fileList', 'tabLoading', 'data', 'typeValue', 'page', 'searchValue']) {
      Object.defineProperty(context, name, { get: () => scope[name].value, set: value => { scope[name].value = value; } });
    }
    // The delivered page delegates this exact read transaction to a composable.
    // Execute that source, with its real captured owner, rather than inventing
    // a removed Options-API method or weakening the permission checks.
    const readsSource = await readFile(path.join(__dirname, '..', 'pages', 'TikuComponent', 'useQuestionReads.mjs'), 'utf8');
    const useReads = vm.runInNewContext(readsSource.replace(/^import .*$/gm, '').replace('export function useQuestionReads', 'function useQuestionReads') + '\nuseQuestionReads', {
      ...scope, ref: value => ({ value }), shallowRef: value => ({ value }),
      requestQuestions: scope.getQuestions,
    });
    const reads = useReads({ ...scope, courses: { value: [] }, questionRequestOwner, captureQuestionOwner,
      questionEntry: { value: { isCurrent: captureQuestionOwner() } } });
    Object.defineProperty(context, 'listFailure', { get: () => reads.listFailure.value });
    Object.defineProperty(context, 'listFailureContext', { get: () => reads.listFailureContext.value });
    const refresh = reads.fetchData();
    assert.equal(scope.tabLoading.value, true);
    assert.equal(params.pageNo, 2);
    assert.equal(params.question, '当前筛选');
    assert.equal(params.source, 2);
    assert.equal(params.type, 2);
    assert.equal(params.pageSize, 20);
    assert.equal(params.extra, '课程');
    assert.equal(params.onlyShowEmptyAnswer, true);
    assert.equal(apiCalls, 1, 'refresh issues the original single GET with unchanged filters');
    const expectedFailure = failure === 'network' ? new Error('offline') : failure === 'http' ? { status: 503, message: 'offline' } : null;
    if (failure === 'network') request.reject(expectedFailure);
    else if (failure === 'http') request.resolve(expectedFailure);
    else request.resolve({ status: 200, data: { items: [{ id: 7 }], total: 41 } });
    await refresh;
    assert.equal(scope.tabLoading.value, false);
    assert.equal(scope.page.value.pageNo, 3);
    assert.equal(context.listFailure, expectedFailure, 'current read failures persist as the original Error/response instead of disappearing');
    assert.equal(context.listFailureContext, 1, 'the current diagnostic entry owns this request');
    assert.equal(errors.length, 0, 'persistent inline failure does not duplicate the old error toast');
    if (!failure) assert.equal(scope.page.value.total, 41);
  }
});

test('UserUsage refresh waits for balance, records and payment methods including failed balance requests', async () => {
  const content = await readFile(userUsagePagePath, 'utf8');
  assert.match(content, /pageRefreshing = computed\(\(\) => currentUserLoading\.value \|\| tableLoading\.value \|\| paymentMethodLoading\.value\)/);
  for (const failure of [false, true]) {
    const request = deferredRequest();
    const scope = {
      currentUserLoading: { value: false }, balanceRefreshError: { value: '' }, currentUser: { value: { balance: 1 } },
      balanceFailure: { value: null }, balanceFailureContextKey: { value: 0 }, balanceFailureOwner: () => false, ensureRequestFailure,
      active: true, refreshSequence: 0, canLoadForCurrentUser: () => true, captureUpdateGuard: () => () => true, captureDataOwner: () => () => true,
      minRechargeAmount: { value: 1 }, rechargeAmount: { value: 1 },
      getCurrentUser: () => request.promise, normalizeMinRechargeAmount: Number, setStoredCurrentUser: value => { scope.currentUser.value = value; },
    };
    for (const name of ['clearBalanceFailure', 'showBalanceFailure']) scope[name] = loadPageMethod(content, name, scope);
    const refresh = loadPageMethod(content, 'handleRefreshCurrentUser', scope)();
    assert.equal(scope.currentUserLoading.value, true);
    if (failure) request.reject(new Error('offline'));
    else request.resolve({ status: 200, data: { balance: 12 } });
    await refresh;
    assert.equal(scope.currentUserLoading.value, false);
    assert.equal(scope.currentUser.value.balance, failure ? 1 : 12);
    assert.equal(Boolean(scope.balanceRefreshError.value), failure, 'failed balance refresh remains explicitly recoverable');
  }
  const requests = [deferredRequest(), deferredRequest(), deferredRequest()];
  const names = ['handleRefreshCurrentUser', 'handleLoadBillingRecords', 'handleLoadPaymentMethods'];
  const calls = [];
  const context = Object.fromEntries(names.map((name, index) => [name, () => { calls.push(name); return requests[index].promise; }]));
  let completed = false;
  const refresh = loadPageMethod(content, 'handleRefreshAll', {}).call(context).then(() => { completed = true; });
  assert.deepEqual(calls, names);
  requests[1].resolve();
  requests[2].resolve();
  await Promise.resolve();
  assert.equal(completed, false);
  requests[0].resolve();
  await refresh;
  assert.equal(completed, true);
});

test('SearchAccountConfig refresh fetches real configuration with loading separate from save', async () => {
  const content = await readFile(searchAccountPagePath, 'utf8');
  assert.match(content, /:loading="tabLoading" :disabled="!canManageConfigs\(\) \|\| accountDraining \|\| savingAll" :onClick="accountEntry\.bind\(handleSearchAccountConfigs\)">刷新/);
  assert.match(content, /:loading="savingAll" :disabled="!canManageConfigs\(\) \|\| accountDraining \|\| !configsLoaded \|\| tabLoading \|\| savingAll" :onClick="accountEntry\.bind\(onSaveAll\)"/);
  for (const failure of [false, true]) {
    const request = deferredRequest();
    const errors = [];
    let apiCalls = 0;
    const scope = {
      tabLoading: { value: false }, dataSource: { value: [] },
      savingAll: { value: false }, accountDraining: { value: false },
      configsLoaded: { value: false }, configsError: { value: '' },
      saveFailure: { value: null }, saveFailureText: { value: '' }, saveFailureOwner: () => false, saveFailureContextKey: { value: 0 },
      readFailure: { value: null }, readFailureOwner: () => false, readFailureContextKey: { value: 0 },
      inheritTransportRequestId, inheritRequestFailureView,
      currentUser: { value: { id: 7301, username: 'synthetic-default-account-admin', role: 'admin' } },
      alive: true, epoch: 0, readVersion: 0, isAdminUser,
      getSearchAccountConfigs: () => { apiCalls += 1; return request.promise; },
      message: { error: value => errors.push(value) },
      // This refresh-only VM has no rendered entries; native ownership is covered by the actual Antd cases.
      renewEntry: () => {},
    };
    scope.currentConfigSnapshot = { get value() { return JSON.stringify(scope.dataSource.value); } };
    scope.savedConfigSnapshot = { value: scope.currentConfigSnapshot.value };
    scope.hasUnsavedChanges = { get value() { return scope.currentConfigSnapshot.value !== scope.savedConfigSnapshot.value; } };
    // Execute the real mount-local stable-principal, permission, read-lease and normalizer functions.
    for (const name of ['principal', 'role', 'canManageConfigs', 'captureOwner', 'normalizeConfigs', 'clearSaveFailure', 'clearReadFailure', 'captureReadFailureOwner', 'showReadFailure', 'projectSaveFailure', 'reportReadFailure', 'loadConfigs']) {
      scope[name] = loadPageMethod(content, name, scope);
    }
    const refresh = loadPageMethod(content, 'handleSearchAccountConfigs', scope)();
    assert.equal(scope.tabLoading.value, true);
    if (failure) request.reject(new Error('offline'));
    else request.resolve({ status: 200, data: [{ provider: 'ai' }] });
    await refresh;
    assert.equal(scope.tabLoading.value, false);
    assert.equal(errors.length, failure ? 1 : 0);
    assert.equal(apiCalls, 1, 'refresh retains the original single real GET');
    assert.equal(scope.configsLoaded.value, !failure, 'only a successful current read opens the save gate');
    if (!failure) assert.equal(scope.dataSource.value[0].provider, 'ai');
  }
});

test('UserUsage page exposes the configurable minimum recharge amount and validates before submitting', async () => {
  const content = await readFile(userUsagePagePath, 'utf8');

  assert.match(content, /const minRechargeAmount = ref\(1\)/);
  assert.match(content, /:min="(?:minRechargeAmount|Math\.max\(0\.01, minRechargeAmount\))"/);
  assert.match(content, /最低充值金额：¥/);
  assert.match(content, /hasOwnProperty\.call\(res\.data, 'minRechargeAmount'\)/);
  assert.match(content, /充值金额不能低于 ¥/);
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
  assert.match(pageContent, /const body = res\?\.data/);
  assert.match(pageContent, /readNonnegativeNumber\(body\.summary\?\.totalAmount\)/);
  assert.match(pageContent, /readNonnegativeNumber\(body\.summary\?\.currentPageAmount\)/);
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
    assert.match(content, /title: '请求 ID'/);
    assert.doesNotMatch(content, /title: '题目'/);
    assert.doesNotMatch(content, /title: '消费金额'/);
  }
  assert.match(billingContent, /buildBillingRecordEffectiveMultiplierText/);
  assert.match(billingContent, /title: '最终倍率'/);
  assert.doesNotMatch(usageContent, /buildBillingRecordEffectiveMultiplierText/);
  assert.doesNotMatch(usageContent, /title: '最终倍率'/);
});

test('Billing record list renders the billing owner username', async () => {
  const content = await readFile(path.join(__dirname, '..', 'pages', 'BillingRecordList', 'index.vue'), 'utf8');

  assert.match(content, /column.key === 'username'/);
  assert.match(content, /record\.username/);
  assert.match(content, /title: '用户'/);
  assert.match(content, /if \(!isAdmin\.value\) \{\s+return baseColumns/);
});

test('Billing record list opens the shared Vue fee breakdown with provider costs', async () => {
  const content = await readFile(path.join(__dirname, '..', 'pages', 'BillingRecordList', 'index.vue'), 'utf8');
  const detail = await readFile(path.join(__dirname, '..', 'components', 'BillingDetailDialog.vue'), 'utf8');

  assert.match(content, /查看详情/);
  assert.match(content, /<BillingDetailDialog/);
  assert.match(content, /handleShowFeeDetail/);
  assert.match(detail, /billing-detail__fee-item/);
  assert.match(detail, /billing-detail__total/);
  assert.doesNotMatch(content, /Swal|document\.querySelector/);
});

test('Billing fee item styles use the shared theme without global popup selectors', async () => {
  const content = await readFile(path.join(__dirname, '..', 'components', 'BillingDetailDialog.vue'), 'utf8');

  assert.match(content, /\.billing-detail__fee-item\s*\{[^}]*border:\s*1px solid var\(--console-border\)/);
  assert.doesNotMatch(content, /:global\(/);
});

test('Billing statement visibility uses the controlled Ant switch and ID event', async () => {
  const content = await readFile(path.join(__dirname, '..', 'components', 'BillingDetailDialog.vue'), 'utf8');

  assert.match(content, /<a-switch[\s\S]*?:checked="detail.showInStatement"[\s\S]*?:loading="statementSaving"/);
  assert.match(content, /v-if="detail.canEditStatement"/);
  assert.match(content, /emit\('statement-visibility-change', \{ id: detail.value.id, showInStatement \}\)/);
  assert.doesNotMatch(content, /addEventListener|setAttribute|translateX\(18px\)/);
});

test('Personal billing flow does not render the owner username column', async () => {
  const content = await readFile(userUsagePagePath, 'utf8');

  assert.doesNotMatch(content, /#username="\{ record \}"/);
  assert.doesNotMatch(content, /title: '用户名'/);
});
