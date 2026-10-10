import test from 'node:test';
import assert from 'node:assert/strict';

import {
  MONEY_DECIMALS,
  formatMoneyValue,
  formatMoneyCny,
  formatMoneyYuanWithSpace,
} from './moneyFormat.mjs';

test('money display keeps up to six decimals so tiny AI charges stay visible', () => {
  // 与后端 MoneyPrecision 对齐
  assert.equal(MONEY_DECIMALS, 6);

  // 小额扣费必须看得见（2 位小数会显示成 0）
  assert.equal(formatMoneyValue(0.000062), '0.000062');
  assert.equal(formatMoneyValue(0.012157), '0.012157');
  assert.equal(formatMoneyValue(49.987843), '49.987843');
});

test('money display trims trailing zeros and the dangling decimal point', () => {
  assert.equal(formatMoneyValue(50), '50');
  assert.equal(formatMoneyValue(12.5), '12.5');
  assert.equal(formatMoneyValue(10.0), '10');
  assert.equal(formatMoneyValue(0), '0');
  assert.equal(formatMoneyValue(8.75), '8.75');
  assert.equal(formatMoneyValue(0.175), '0.175');
  assert.equal(formatMoneyValue(1.230000), '1.23');
});

test('money display rounds to six decimals and never exceeds the precision', () => {
  assert.equal(formatMoneyValue(49.9878432), '49.987843');
  assert.equal(formatMoneyValue(0.0000004), '0');
  assert.equal(formatMoneyValue(0.0000009), '0.000001');
  assert.equal(formatMoneyValue(1.2345678), '1.234568');
  assert.equal(formatMoneyValue(1.2345671), '1.234567');

  // 小数位永远不超过 6 位（半值边界受 float64 表示误差影响，不做断言）。
  for (const value of [0.0000004, 0.0000009, 1.2345678, 99.9999999, -7.1234567]) {
    const text = formatMoneyValue(value);
    const decimals = text.includes('.') ? text.split('.')[1].length : 0;
    assert.ok(decimals <= MONEY_DECIMALS, `${text} 超过 ${MONEY_DECIMALS} 位小数`);
  }
});

test('money display handles negative balances and invalid input', () => {
  assert.equal(formatMoneyValue(-43.25), '-43.25');
  assert.equal(formatMoneyValue(-0.000062), '-0.000062');
  assert.equal(formatMoneyValue(-0), '0');
  assert.equal(formatMoneyValue(Number.NaN), '0');
  assert.equal(formatMoneyValue(Number.POSITIVE_INFINITY), '0');
  assert.equal(formatMoneyValue(undefined), '0');
  assert.equal(formatMoneyValue('not-a-number'), '0');
  assert.equal(formatMoneyValue('12.50'), '12.5');
});

test('money display helpers add the expected currency symbol', () => {
  assert.equal(formatMoneyCny(12.5), '￥12.5');
  assert.equal(formatMoneyCny(0.000062), '￥0.000062');
  assert.equal(formatMoneyYuanWithSpace(12.5), '¥ 12.5');
  assert.equal(formatMoneyYuanWithSpace(0), '¥ 0');
});
