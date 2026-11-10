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
  assert.deepEqual(build({ id: 2, mode: 'increase', amount: ' 2.123456 ' }), { id: 2, mode: 'increase', amount: 2.123456, bucket: 'principal', showInStatement: true });
  assert.deepEqual(build({ id: 2, mode: 'override', amount: 0, showInStatement: false }), { id: 2, mode: 'override', amount: 0, bucket: 'principal', showInStatement: false });
  assert.equal(build({ id: 2, mode: 'override', amount: -1 }).amount, -1);
});

test('balance adjustments can target the gift bucket', () => {
  const build = helpers.buildUserBalanceAdjustmentPayload;
  // 管理员选赠金桶：payload 必须带上 bucket，后端据此只改 gift_balance。
  assert.deepEqual(
    build({ id: 2, mode: 'increase', amount: 5, bucket: 'gift' }),
    { id: 2, mode: 'increase', amount: 5, bucket: 'gift', showInStatement: true },
  );
  // 空值等价于本金桶，既有调用方不受影响。
  assert.equal(build({ id: 2, mode: 'increase', amount: 1, bucket: '  ' }).bucket, 'principal');
  assert.equal(build({ id: 2, mode: 'increase', amount: 1, bucket: 'principal' }).bucket, 'principal');
  // 未知桶名与「赠送金额为负」都在前端拦下。
  assert.throws(() => build({ id: 2, mode: 'increase', amount: 1, bucket: 'vip' }), /钱包类型/);
  assert.throws(() => build({ id: 2, mode: 'override', amount: -1, bucket: 'gift' }), /赠送金额不能为负数/);
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
