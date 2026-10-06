import test from 'node:test';
import assert from 'node:assert/strict';
import * as userPortalHelpers from './userPortalHelpers.mjs';

import {
  buildPortalMenuKeys,
  buildRegisterSourceLabel,
  buildResetPasswordPayload,
  buildBillingRecordAmountText,
  buildBillingRecordFeeDetailHtml,
  buildBillingRecordFeeDetails,
  buildBillingRecordReferenceText,
  buildBillingRecordSourceText,
  buildBillingRecordSubjectText,
  buildBillingRecordTypeText,
  buildPersonalUsageBillingRecordParams,
  buildPersonalUsageQuestionText,
  buildPersonalUsageAnswerSourceText,
  buildUserBalanceUpdatePayload,
  resolveAdapterHomePath,
} from './userPortalHelpers.mjs';

test('resolveAdapterHomePath sends admins to component and users to usage page', () => {
  assert.equal(resolveAdapterHomePath({ role: 'admin' }), '/adapter/component');
  assert.equal(resolveAdapterHomePath({ role: 'user' }), '/adapter/user-usage');
  assert.equal(resolveAdapterHomePath(null), '/adapter/user-usage');
});

test('buildRegisterSourceLabel maps system registrations to initialization wording', () => {
  assert.equal(buildRegisterSourceLabel('system'), '系统初始化创建');
  assert.equal(buildRegisterSourceLabel('public'), '公开注册');
});

test('buildPortalMenuKeys includes personal usage for admin users', () => {
  assert.deepEqual(buildPortalMenuKeys({ role: 'admin' }), [
    'profile',
    'component',
    'import',
    'user-usage',
    'userlist',
    'loglist',
    'recharge-orders',
    'billing-records',
    'search-account-config',
    'ai-search-config',
    'system-settings',
    'access-tokens',
  ]);
  assert.deepEqual(buildPortalMenuKeys({ role: 'user' }), [
    'profile',
    'user-usage',
    'access-tokens',
    'recharge-orders',
    'billing-records',
  ]);
});

test('buildPersonalUsageBillingRecordParams always requests own records', () => {
  assert.deepEqual(buildPersonalUsageBillingRecordParams(3, 20), {
    page: 3,
    pageSize: 20,
    scope: 'self',
  });
});

test('buildPersonalUsageQuestionText prefers explicit question text and falls back cleanly', () => {
  assert.equal(buildPersonalUsageQuestionText({ questionText: '缓存命中题目', providerLabel: '万能题库（缓存）' }), '缓存命中题目');
  assert.equal(buildPersonalUsageQuestionText({ question: '直接题目字段' }), '直接题目字段');
  assert.equal(buildPersonalUsageQuestionText({ providerLabel: '万能题库（缓存）' }), '万能题库（缓存）');
  assert.equal(buildPersonalUsageQuestionText({}), '-');
});

test('buildPersonalUsageAnswerSourceText prefers answer source label before provider label', () => {
  assert.equal(buildPersonalUsageAnswerSourceText({ answerSourceLabel: '万能题库（缓存）', providerLabel: '本地题库' }), '万能题库（缓存）');
  assert.equal(buildPersonalUsageAnswerSourceText({ providerLabel: 'Icodef 题库' }), 'Icodef 题库');
  assert.equal(buildPersonalUsageAnswerSourceText({ provider: 'icodef' }), 'icodef');
  assert.equal(buildPersonalUsageAnswerSourceText({}), '-');
});

test('billing record helpers render recharge and search ledger rows', () => {
  const recharge = {
    type: 'recharge',
    typeLabel: '充值入账',
    direction: 'income',
    subject: '账户充值',
    detail: '账户充值',
    providerLabel: 'epay',
    referenceNo: 'rcg_user_20260621002829_100_ABC12345',
    amount: 1,
  };
  assert.equal(buildBillingRecordTypeText(recharge), '充值入账');
  assert.equal(buildBillingRecordSubjectText(recharge), '账户充值');
  assert.equal(buildBillingRecordSourceText(recharge), '账户充值');
  assert.equal(buildBillingRecordReferenceText(recharge), 'rcg_user_20260621002829_100_ABC12345');
  assert.equal(buildBillingRecordAmountText(recharge), '+¥ 1.00');

  const search = {
    type: 'normal_search',
    typeLabel: '题目消费',
    direction: 'expense',
    questionText: '搜索题目',
    answerSourceLabel: 'Icodef 题库',
    referenceNo: 'req-search',
    amount: 1.25,
  };
  assert.equal(buildBillingRecordTypeText(search), '题目消费');
  assert.equal(buildBillingRecordSubjectText(search), '搜索题目');
  assert.equal(buildBillingRecordSourceText(search), 'Icodef 题库');
  assert.equal(buildBillingRecordReferenceText(search), 'req-search');
  assert.equal(buildBillingRecordAmountText(search), '-¥ 1.25');
});

test('billing record fee helpers render per-provider and total charges', () => {
  const record = {
    subject: '搜索答案，题目：混合题目',
    direction: 'expense',
    amount: 2,
    feeDetails: [
      { provider: 'icodef', providerLabel: 'Icodef 题库', amount: 1.25 },
      { provider: 'ai', providerLabel: 'AI 搜索', amount: 0.75 },
    ],
  };

  assert.deepEqual(buildBillingRecordFeeDetails(record), record.feeDetails);

  const html = buildBillingRecordFeeDetailHtml(record);
  assert.match(html, /搜索答案，题目：混合题目/);
  assert.match(html, /Icodef 题库/);
  assert.match(html, /-¥ 1\.25/);
  assert.match(html, /AI 搜索/);
  assert.match(html, /-¥ 0\.75/);
  assert.match(html, /合计/);
  assert.match(html, /-¥ 2\.00/);
});

test('billing record fee helpers fall back to the row provider for legacy records', () => {
  assert.deepEqual(buildBillingRecordFeeDetails({
    providerLabel: 'AI 搜索',
    amount: 0.06,
  }), [
    {
      provider: '',
      providerLabel: 'AI 搜索',
      amount: 0.06,
    },
  ]);
});

test('buildResetPasswordPayload requires non-empty password', () => {
  assert.deepEqual(buildResetPasswordPayload('  New12345!  '), { password: 'New12345!' });
  assert.throws(() => buildResetPasswordPayload(''), /请输入新密码/);
});

test('buildAdminCreateUserPayload trims fields and keeps admin-created defaults', () => {
  assert.equal(typeof userPortalHelpers.buildAdminCreateUserPayload, 'function');
  assert.deepEqual(userPortalHelpers.buildAdminCreateUserPayload({
    username: '  new-user  ',
    password: '  Abc12345!  ',
    nickname: '  测试用户  ',
    role: ' admin ',
    status: ' disabled ',
    registerSource: '  ',
  }), {
    username: 'new-user',
    password: 'Abc12345!',
    nickname: '测试用户',
    role: 'admin',
    status: 'disabled',
    registerSource: 'admin',
  });
});

test('buildUserBalanceUpdatePayload trims fields and keeps edit mode data', () => {
  assert.deepEqual(buildUserBalanceUpdatePayload({
    id: 9,
    nickname: '  管理员修改  ',
    role: ' admin ',
    status: ' disabled ',
    phone: ' 13800000000 ',
    forceReassignPhone: true,
    balance: ' 12.50 ',
    balanceMode: 'increase',
  }), {
    id: 9,
    nickname: '管理员修改',
    role: 'admin',
    status: 'disabled',
    phone: '13800000000',
    forceReassignPhone: true,
    balance: 12.5,
    balanceMode: 'increase',
  });
});

test('buildUserBalanceUpdatePayload omits balance when empty but keeps phone clearing payload', () => {
  assert.deepEqual(buildUserBalanceUpdatePayload({
    id: 10,
    nickname: 'test',
    role: 'user',
    status: 'enabled',
    phone: '   ',
    forceReassignPhone: false,
    balance: '   ',
    balanceMode: 'override',
  }), {
    id: 10,
    nickname: 'test',
    role: 'user',
    status: 'enabled',
    phone: '',
    forceReassignPhone: false,
  });
});

test('buildUserBalanceUpdatePayload rejects invalid balance values', () => {
  assert.throws(() => buildUserBalanceUpdatePayload({
    id: 11,
    nickname: 'test',
    role: 'user',
    status: 'enabled',
    balance: 'abc',
    balanceMode: 'decrease',
  }), /请输入有效的余额数值/);
});
