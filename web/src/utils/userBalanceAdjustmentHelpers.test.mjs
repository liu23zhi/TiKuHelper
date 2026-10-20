import assert from 'node:assert/strict';
import test from 'node:test';
import * as helpers from './userPortalHelpers.mjs';

test('profile edits omit the read-only balance even when it is stale', () => {
  const payload = helpers.buildUserProfileUpdatePayload({ id: 2, nickname: ' name ', role: 'user', status: 'enabled', balance: 123, balanceMode: 'override' });
  assert.equal(payload.nickname, 'name');
  assert.equal(Object.hasOwn(payload, 'balance'), false);
  assert.equal(Object.hasOwn(payload, 'balanceMode'), false);
});

test('balance adjustments default to visible and preserve an unchecked checkbox', () => {
  const build = helpers.buildUserBalanceAdjustmentPayload;
  assert.deepEqual(build({ id: 2, mode: 'increase', amount: ' 2.123456 ' }), { id: 2, mode: 'increase', amount: 2.123456, showInStatement: true });
  assert.deepEqual(build({ id: 2, mode: 'override', amount: 0, showInStatement: false }), { id: 2, mode: 'override', amount: 0, showInStatement: false });
  assert.equal(build({ id: 2, mode: 'override', amount: -1 }).amount, -1);
});

test('balance adjustments reject missing, invalid, excess-precision and negative delta inputs', () => {
  for (const amount of [null, undefined, '', ' ', 'abc', Infinity, NaN, 0.0000001, '1.1234567', 1e13]) {
    assert.throws(() => helpers.buildUserBalanceAdjustmentPayload({ id: 2, mode: 'increase', amount }));
  }
  for (const mode of ['increase', 'decrease', 'multiply']) {
    assert.throws(() => helpers.buildUserBalanceAdjustmentPayload({ id: 2, mode, amount: -1 }));
  }
});

test('administrative balance adjustments have clear fallback labels', () => {
  assert.equal(helpers.buildBillingRecordTypeText({ type: 'balance_adjustment' }), '管理员调整余额');
});
