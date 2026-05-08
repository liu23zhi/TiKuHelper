import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildAccessTokenBalanceAlert,
} from './accessTokenManagementHelpers.mjs';

test('buildAccessTokenBalanceAlert shows negative-balance default-account suspension warning', () => {
  assert.deepEqual(buildAccessTokenBalanceAlert({ balance: -3 }), {
    type: 'warning',
    message: '当前余额不足',
    description: '当前余额已小于 0，系统默认账号已停用。继续调用 /adapter-service/search 时，请在请求中显式传入渠道参数或 AI 参数，否则请求会直接失败。',
  });
});

test('buildAccessTokenBalanceAlert returns null for non-negative balances', () => {
  assert.equal(buildAccessTokenBalanceAlert({ balance: 0 }), null);
  assert.equal(buildAccessTokenBalanceAlert({ balance: 12.5 }), null);
  assert.equal(buildAccessTokenBalanceAlert(null), null);
});
