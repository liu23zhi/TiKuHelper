import test from 'node:test'
import assert from 'node:assert/strict'
import { buildProfileBalanceDisplay } from './profileBalance.mjs'

test('Profile display sums the displayed six-place amounts, allowing principal debt but not gift debt', () => {
  assert.deepEqual(buildProfileBalanceDisplay({ balance: 90.123456, giftBalance: 5.876544 }, 'ready'), { principal: '¥ 90.123456', gift: '¥ 5.876544', total: '¥ 96' })
  assert.deepEqual(buildProfileBalanceDisplay({ balance: '-999999999999.999999', giftBalance: '999999999999.999998' }, 'ready'), { principal: '¥ -999999999999.999999', gift: '¥ 999999999999.999998', total: '¥ -0.000001' })
  assert.deepEqual(buildProfileBalanceDisplay({ balance: '999999999999.999999', giftBalance: '999999999999.999999' }, 'ready'), { principal: '¥ 999999999999.999999', gift: '¥ 999999999999.999999', total: '¥ 1999999999999.999998' })
  for (const giftBalance of [-1e-7, -0.000001, '-0.000001', Infinity, false, [], 'none', '  ', '0x1', '0.0000001']) {
    assert.deepEqual(buildProfileBalanceDisplay({ balance: 0, giftBalance }, 'ready'), { principal: '¥ 0', gift: '余额数据异常', total: '余额数据异常' })
  }
})

test('Profile display has distinct unavailable states and never adds a partial total', () => {
  assert.deepEqual(buildProfileBalanceDisplay({ balance: 2, giftBalance: 0 }, 'loading'), { principal: '余额加载中…', gift: '余额加载中…', total: '余额加载中…' })
  assert.deepEqual(buildProfileBalanceDisplay({ balance: 2, giftBalance: 0 }, 'failed'), { principal: '余额加载失败', gift: '余额加载失败', total: '余额加载失败' })
  for (const giftBalance of [null, undefined, '']) {
    assert.deepEqual(buildProfileBalanceDisplay({ balance: 2, giftBalance }, 'ready'), { principal: '¥ 2', gift: '余额数据缺失', total: '余额数据缺失' })
  }
  assert.deepEqual(buildProfileBalanceDisplay({ balance: {}, giftBalance: null }, 'ready'), { principal: '余额数据异常', gift: '余额数据缺失', total: '余额数据异常' })
})
