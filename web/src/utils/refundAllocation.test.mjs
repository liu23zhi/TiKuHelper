// 退款分摊算法的契约判据（纯函数，零依赖）。
//
// 用户 2026-11-19 的原话逐条对应到下面的用例：
//   · 「假如我要退款10块钱，但是支付记录有两笔，每笔都7.5」          → 用例 1
//   · 「最旧的那笔全额退回，然后最新的部分退款」                      → 用例 1、6
//   · 「两笔的退款金额已经达到了申请退款金额，所以不能选择第三笔」    → 用例 2
//   · 「用户可以修改退款金额，以选择第三笔」                          → 用例 3
//   · 「避免覆盖用户手动修改的金额」                                  → 用例 3、4、5

import test from 'node:test';
import assert from 'node:assert/strict';
// 注意路径：实现在 pages/Tickets/（跟页面走），测试在 utils/。
// 这是本仓库的惯例（参见 ticketCreateType.test.mjs 导入
// ../pages/Tickets/createTicketTypeHelpers.mjs），也是因为 test:unit 只收 src/utils/*.test.mjs。
import { computeRefundAllocations, refundAllocationTotals } from '../pages/Tickets/refundAllocation.mjs';

// 两笔各 7.5 元。较旧的 paidAt 更小。
const older = { id: 1, paidCents: 750, paidAt: '2026-01-01T00:00:00Z' };
const newer = { id: 2, paidCents: 750, paidAt: '2026-02-01T00:00:00Z' };
const third = { id: 3, paidCents: 750, paidAt: '2026-03-01T00:00:00Z' };
const orders = [older, newer, third];

test('① 退 10 元、两笔各 7.5：旧的先全额，新的部分', () => {
  // 用户先选第一笔。
  let selections = computeRefundAllocations({
    orders,
    requestedCents: 1000,
    selections: [{ orderId: 1, cents: 0, manual: false }],
  });
  assert.equal(selections.length, 1);
  assert.equal(selections[0].cents, 750, '一笔才 7.5，不够 10，所以先全额');

  // 再选第二笔：按新旧规则补足剩余。
  selections = computeRefundAllocations({
    orders,
    requestedCents: 1000,
    selections: [...selections, { orderId: 2, cents: 0, manual: false }],
  });
  assert.deepEqual(
    selections.map(item => [item.orderId, item.cents]),
    [[1, 750], [2, 250]],
    '较旧的 7.5 全额 + 较新的 2.5 部分 = 10',
  );
});

test('② 两笔已达申请金额：第三笔分不到钱（前端据此禁选）', () => {
  const selections = computeRefundAllocations({
    orders,
    requestedCents: 1000,
    selections: [
      { orderId: 1, cents: 0, manual: false },
      { orderId: 2, cents: 0, manual: false },
      { orderId: 3, cents: 0, manual: false },
    ],
  });
  const totals = refundAllocationTotals(1000, selections);
  assert.equal(totals.allocated, 1000, '三笔合计不得超过申请金额');
  assert.equal(totals.remaining, 0, '剩余为 0 → 组件的 canToggleRefundOrder 会禁掉未选中的行');
  assert.equal(selections[2].cents, 0, '第三笔应当分到 0');
});

test('③ 用户把第一笔改小 → 腾出额度，第二笔相应变大', () => {
  const selections = computeRefundAllocations({
    orders,
    requestedCents: 1000,
    selections: [
      { orderId: 1, cents: 500, manual: true }, // 用户手改成 5 元
      { orderId: 2, cents: 0, manual: false },
    ],
  });
  assert.deepEqual(
    selections.map(item => [item.orderId, item.cents]),
    [[1, 500], [2, 500]],
    '手改的 5 元保留，剩余 5 元给下一笔',
  );
  assert.equal(refundAllocationTotals(1000, selections).remaining, 0);
});

test('④ 手改过的行不会被任何重算覆盖（最容易写错的一条）', () => {
  const modified = { orderId: 1, cents: 123, manual: true };
  // 反复重算：只要 manual 为真，金额就不该变。
  for (const requested of [1000, 300, 5000]) {
    const selections = computeRefundAllocations({
      orders,
      requestedCents: requested,
      selections: [modified, { orderId: 2, cents: 0, manual: false }],
    });
    const first = selections.find(item => item.orderId === 1);
    assert.equal(first.cents, 123, `requested=${requested} 时手改值被覆盖了`);
    assert.equal(first.manual, true, 'manual 标记必须保留');
  }
});

test('⑤ 单笔不超过该笔订单实付', () => {
  const selections = computeRefundAllocations({
    orders,
    requestedCents: 100000, // 远超两笔之和
    selections: [{ orderId: 1, cents: 0, manual: false }],
  });
  assert.equal(selections[0].cents, 750, '只能退到该笔实付为止，不能超额');
});

test('⑥ 按 paidAt 升序，与传入顺序无关；同刻按 id 稳定', () => {
  const orderInput = [
    { id: 9, paidCents: 300, paidAt: '2026-05-01T00:00:00Z' },
    { id: 1, paidCents: 500, paidAt: '2026-01-01T00:00:00Z' },
    { id: 5, paidCents: 400, paidAt: '2026-03-01T00:00:00Z' },
  ];
  const selections = computeRefundAllocations({
    orders: orderInput,
    requestedCents: 1000,
    // 故意用乱序传入
    selections: [
      { orderId: 9, cents: 0, manual: false },
      { orderId: 1, cents: 0, manual: false },
      { orderId: 5, cents: 0, manual: false },
    ],
  });
  assert.deepEqual(
    selections.map(item => item.orderId),
    [1, 5, 9],
    '必须先给最旧的那笔',
  );
  // 500 + 400 + 剩余 100 = 1000
  assert.deepEqual(selections.map(item => item.cents), [500, 400, 100]);
});

test('⑦ 空选择与零金额不产生假数据', () => {
  assert.deepEqual(
    computeRefundAllocations({ orders, requestedCents: 1000, selections: [] }),
    [],
  );
  const selections = computeRefundAllocations({
    orders,
    requestedCents: 0,
    selections: [{ orderId: 1, cents: 0, manual: false }],
  });
  assert.equal(selections[0].cents, 0, '申请金额为 0 时不该分配出正数');
});

test('⑧ 汇总与分配一致（已分配 + 剩余 = 申请）', () => {
  const selections = computeRefundAllocations({
    orders,
    requestedCents: 1000,
    selections: [
      { orderId: 1, cents: 0, manual: false },
      { orderId: 2, cents: 0, manual: false },
    ],
  });
  const totals = refundAllocationTotals(1000, selections);
  assert.equal(totals.allocated + totals.remaining, 1000);
});