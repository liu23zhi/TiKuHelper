import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildRechargeOrderPayload,
  sanitizeRechargeAmountInput,
  shouldBlockRechargeAmountKey,
  buildRechargePaymentMethodOptions,
  buildPaymentMethodSwalHtml,
  buildSearchAccountConfigSummary,
  buildSearchAccountDefaultParameterHint,
  buildSearchAccountUnitPriceHint,
  buildSearchAccountConfigSavePayloads,
  normalizePaymentMethodForm,
  sortPaymentMethodsForDisplay,
} from './paymentMethodHelpers.mjs';

test('sortPaymentMethodsForDisplay keeps enabled methods first and sorts by sort then id', () => {
  const rows = [
    { id: 9, name: '微信支付', enabled: false, sort: 1 },
    { id: 3, name: '支付宝', enabled: true, sort: 10 },
    { id: 1, name: '银行卡', enabled: true, sort: 1 },
    { id: 2, name: '云闪付', enabled: true, sort: 1 },
  ];

  const result = sortPaymentMethodsForDisplay(rows).map((item) => item.id);

  assert.deepEqual(result, [1, 2, 3, 9]);
});

test('buildRechargePaymentMethodOptions only returns enabled methods', () => {
  const rows = [
    { id: 1, name: '支付宝', iconSVG: '<svg />', enabled: true, sort: 2 },
    { id: 2, name: '微信支付', iconSVG: '<svg />', enabled: false, sort: 1 },
  ];

  const result = buildRechargePaymentMethodOptions(rows);

  assert.deepEqual(result, [
    {
      label: '支付宝',
      value: 1,
      iconSVG: '<svg />',
      remark: '',
      sort: 2,
    },
  ]);
});

test('normalizePaymentMethodForm preserves backend epayType naming and trims fields', () => {
  const result = normalizePaymentMethodForm({
    id: '9',
    name: '  易支付支付宝  ',
    iconSvg: '  <svg />  ',
    epayType: '  alipay  ',
    enabled: 1,
    sort: '12',
    remark: '  推荐  ',
  });

  assert.deepEqual(result, {
    id: 9,
    name: '易支付支付宝',
    iconSvg: '<svg />',
    epayType: 'alipay',
    enabled: true,
    sort: 12,
    remark: '推荐',
  });
});

test('normalizePaymentMethodForm also accepts legacy camel-cased frontend field names', () => {
  const result = normalizePaymentMethodForm({
    id: '7',
    name: '  支付宝  ',
    iconSVG: '  <svg />  ',
    ePayType: '  alipay  ',
    enabled: 0,
    sort: '8',
    remark: '  常用渠道  ',
  });

  assert.deepEqual(result, {
    id: 7,
    name: '支付宝',
    iconSvg: '<svg />',
    epayType: 'alipay',
    enabled: false,
    sort: 8,
    remark: '常用渠道',
  });
});

test('buildRechargeOrderPayload validates amount and payment method selection', () => {
  assert.deepEqual(buildRechargeOrderPayload(' 12.30 ', '7'), {
    amount: 12.3,
    paymentMethodId: 7,
  });

  assert.throws(() => buildRechargeOrderPayload('', '7'), /充值金额/);
  assert.throws(() => buildRechargeOrderPayload('12.3', 0), /支付方式/);
});

test('recharge amount only accepts up to two decimals', () => {
  // 支付渠道只支持「分」，两位小数以内必须通过。
  for (const amount of ['10', '25.5', '25.05', '0.01', '199.99']) {
    assert.doesNotThrow(() => buildRechargeOrderPayload(amount, 1), `expected ${amount} to be accepted`);
  }

  // 超过两位小数必须被拒绝，而不是静默四舍五入。
  for (const amount of ['12.345', '0.001', '99.999']) {
    assert.throws(
      () => buildRechargeOrderPayload(amount, 1),
      /两位小数/,
      `expected ${amount} to be rejected`,
    );
  }
});

test('sanitizeRechargeAmountInput strips characters and caps decimals while typing', () => {
  // 超出的位数在输入阶段就被丢掉，用户敲不进第三位。
  assert.equal(sanitizeRechargeAmountInput('12.345'), '12.34');
  assert.equal(sanitizeRechargeAmountInput('12.3'), '12.3');
  assert.equal(sanitizeRechargeAmountInput('12'), '12');
  assert.equal(sanitizeRechargeAmountInput('199.999'), '199.99');

  // 非法字符与非数字前缀被清除。
  assert.equal(sanitizeRechargeAmountInput('abc'), '');
  assert.equal(sanitizeRechargeAmountInput('-12.5'), '12.5');
  assert.equal(sanitizeRechargeAmountInput('1e5'), '15');
  assert.equal(sanitizeRechargeAmountInput('12.3.4'), '12.34');
  assert.equal(sanitizeRechargeAmountInput('¥ 25.50'), '25.50');
  assert.equal(sanitizeRechargeAmountInput(' 1 2 . 5 '), '12.5');

  // 中间态必须保留，否则光标与后续输入会跳。
  assert.equal(sanitizeRechargeAmountInput(''), '');
  assert.equal(sanitizeRechargeAmountInput(null), '');
  assert.equal(sanitizeRechargeAmountInput('.'), '.');
  assert.equal(sanitizeRechargeAmountInput('12.'), '12.');

  // 支持自定义位数。
  assert.equal(sanitizeRechargeAmountInput('12.3456', 3), '12.345');
  assert.equal(sanitizeRechargeAmountInput('12.34', 0), '12');
});

test('shouldBlockRechargeAmountKey blocks the third decimal digit and a second dot', () => {
  // 已有两位小数时，数字键被拦截。
  assert.equal(shouldBlockRechargeAmountKey('12.34', '5'), true);
  assert.equal(shouldBlockRechargeAmountKey('12.3', '5'), false);
  assert.equal(shouldBlockRechargeAmountKey('12', '5'), false);
  assert.equal(shouldBlockRechargeAmountKey('12.', '5'), false);

  // 只允许一个小数点。
  assert.equal(shouldBlockRechargeAmountKey('12', '.'), false);
  assert.equal(shouldBlockRechargeAmountKey('12.', '.'), true);
  assert.equal(shouldBlockRechargeAmountKey('12.3', '.'), true);

  // 控制键与退格不能被拦截，否则用户无法编辑。
  for (const key of ['Backspace', 'Delete', 'ArrowLeft', 'ArrowRight', 'Tab', 'Enter', 'Home', 'End']) {
    assert.equal(shouldBlockRechargeAmountKey('12.34', key), false, `${key} should not be blocked`);
  }
});

test('sanitized recharge input stays accepted by the payload builder', () => {
  // 输入框产出的值必须始终能通过提交校验，两端规则不会互相矛盾。
  for (const raw of ['12.345', '0.999', '99.999', '1.005', '25.50']) {
    const sanitized = sanitizeRechargeAmountInput(raw);
    assert.doesNotThrow(
      () => buildRechargeOrderPayload(sanitized, 1),
      `expected sanitized ${raw} -> ${sanitized} to pass validation`,
    );
  }
});

test('buildPaymentMethodSwalHtml renders icon, label and id marker', () => {
  const html = buildPaymentMethodSwalHtml([
    {
      label: '支付宝',
      value: 3,
      iconSVG: '<svg><circle /></svg>',
      remark: '推荐使用',
    },
  ]);

  assert.match(html, /data-payment-method-id="3"/);
  assert.match(html, /支付宝/);
  assert.match(html, /推荐使用/);
  assert.match(html, /<svg><circle><\/circle><\/svg>/);
});

test('buildSearchAccountDefaultParameterHint keeps AI guidance in the default parameter column', () => {
  assert.equal(
    buildSearchAccountDefaultParameterHint({
      provider: 'ai',
    }),
    'AI 搜索的模型与默认参数请前往 AI 搜索配置页单独维护。'
  );
});

test('buildSearchAccountDefaultParameterHint explains local provider access token requirement', () => {
  assert.equal(
    buildSearchAccountDefaultParameterHint({
      provider: 'local',
      tokenKey: '',
    }),
    '本地题库不使用默认参数，但调用 search 时必须携带有效访问令牌。'
  );
});

test('buildSearchAccountDefaultParameterHint does not expose the default token value', () => {
  assert.equal(
    buildSearchAccountDefaultParameterHint({
      provider: 'icodef',
      tokenKey: 'icodefToken',
      tokenValue: 'secret-token-value',
    }),
    '参数名：icodefToken'
  );
});

test('buildSearchAccountUnitPriceHint keeps AI guidance in the unit price column', () => {
  assert.equal(
    buildSearchAccountUnitPriceHint({
      provider: 'ai',
    }),
    'AI 搜索价格请前往 AI 搜索配置页单独维护。'
  );
});

test('buildSearchAccountUnitPriceHint formats local search unit price independently', () => {
  assert.equal(
    buildSearchAccountUnitPriceHint({
      provider: 'local',
      flatPrice: 0.6,
    }),
    '单次价格：¥ 0.60000000'
  );
});

test('buildSearchAccountConfigSummary keeps backward-compatible merged text for normal providers', () => {
  assert.equal(
    buildSearchAccountConfigSummary({
      provider: 'baidu',
      tokenKey: 'token',
      tokenValue: 'abc123',
      flatPrice: 0.125,
    }),
    '参数名：token｜单次价格：¥ 0.12500000'
  );
});

test('buildSearchAccountConfigSavePayloads skips ai provider and normalizes editable rows', () => {
  const result = buildSearchAccountConfigSavePayloads([
    {
      provider: 'baidu',
      tokenValue: '  abc123  ',
      enabled: 1,
      remark: '  默认账号  ',
      flatPrice: '0.125',
    },
    {
      provider: 'ai',
      tokenValue: 'should-skip',
      enabled: true,
      remark: 'skip me',
      flatPrice: 9,
    },
    {
      provider: 'icodef',
      tokenValue: ' system-token ',
      enabled: false,
      remark: '',
      flatPrice: 2,
    },
  ]);

  assert.deepEqual(result, [
    {
      provider: 'baidu',
      payload: {
        tokenValue: 'abc123',
        enabled: true,
        remark: '默认账号',
        flatPrice: 0.125,
      },
    },
    {
      provider: 'icodef',
      payload: {
        tokenValue: 'system-token',
        enabled: false,
        remark: '',
        flatPrice: 2,
      },
    },
  ]);
});
