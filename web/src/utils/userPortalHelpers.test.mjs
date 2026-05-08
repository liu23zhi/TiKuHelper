import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildPortalMenuKeys,
  buildRegisterSourceLabel,
  buildResetPasswordPayload,
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
    'component',
    'import',
    'user-usage',
    'userlist',
    'loglist',
    'billing-records',
    'search-account-config',
    'ai-search-config',
    'system-settings',
    'access-tokens',
  ]);
  assert.deepEqual(buildPortalMenuKeys({ role: 'user' }), [
    'user-usage',
    'access-tokens',
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

test('buildUserBalanceUpdatePayload trims fields and keeps edit mode data', () => {
  assert.deepEqual(buildUserBalanceUpdatePayload({
    id: 9,
    nickname: '  管理员修改  ',
    role: ' admin ',
    status: ' disabled ',
    balance: ' 12.50 ',
    balanceMode: 'increase',
  }), {
    id: 9,
    nickname: '管理员修改',
    role: 'admin',
    status: 'disabled',
    balance: 12.5,
    balanceMode: 'increase',
  });
});

test('buildUserBalanceUpdatePayload omits balance when empty', () => {
  assert.deepEqual(buildUserBalanceUpdatePayload({
    id: 10,
    nickname: 'test',
    role: 'user',
    status: 'enabled',
    balance: '   ',
    balanceMode: 'override',
  }), {
    id: 10,
    nickname: 'test',
    role: 'user',
    status: 'enabled',
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
