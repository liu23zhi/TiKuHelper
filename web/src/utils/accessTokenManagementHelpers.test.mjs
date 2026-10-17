import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildAccessTokenBalanceAlert,
  buildAccessTokenListParams,
  normalizeAccessTokenName,
  limitAccessTokenNameInput,
} from './accessTokenManagementHelpers.mjs';

test('buildAccessTokenBalanceAlert shows zero and negative balance suspension warning', () => {
  assert.deepEqual(buildAccessTokenBalanceAlert({ balance: -3 }), {
    type: 'warning',
    message: '当前余额不足',
    description: '当前余额不足，系统题库已禁用。仍可在请求中显式传入渠道参数或 AI 参数。',
  });
  assert.deepEqual(buildAccessTokenBalanceAlert({ balance: 0 }), {
    type: 'warning',
    message: '当前余额不足',
    description: '当前余额已不足，搜索服务已暂停。请先充值后再调用。',
  });
});

test('buildAccessTokenBalanceAlert returns null for positive and invalid balances', () => {
  assert.equal(buildAccessTokenBalanceAlert({ balance: 12.5 }), null);
  assert.equal(buildAccessTokenBalanceAlert(null), null);
});

test('buildAccessTokenListParams requests all users only when enabled', () => {
  assert.deepEqual(buildAccessTokenListParams(false), {});
  assert.deepEqual(buildAccessTokenListParams(true), { scope: 'all' });
});

test('normalizeAccessTokenName allows safe names and rejects URLs, punctuation, and overflow', () => {
  assert.equal(normalizeAccessTokenName('  课程令牌_2026@A  '), '课程令牌_2026@A')
  assert.equal(normalizeAccessTokenName('令'.repeat(128)), '令'.repeat(128))
  assert.equal(limitAccessTokenNameInput('令'.repeat(129)), '令'.repeat(128))
  assert.throws(() => normalizeAccessTokenName('https://example.com'), /令牌名称/)
  assert.throws(() => normalizeAccessTokenName('token-name'), /令牌名称/)
  assert.throws(() => normalizeAccessTokenName('令'.repeat(129)), /令牌名称/)
})
