// 充值记录「发起退款」入口的契约判据（源码级，零依赖）。
//
// 用户诉求：「在充值记录提供一个按钮，用于快捷发起退款工单」，且已选定
// 「弹小窗直接提交，不跳页」。
//
// 这组用例钉三件事，都是**不看代码就会做错**的判断：
//   ① 可退性只认后端（/ticket-refunds/eligible-orders），前端不得自己推
//   ② 提交字段必须与 CreateTicketRefundInput 对齐（kind/orderId/requestedCents/reason）
//   ③ kind 固定 original —— 从充值记录发起天然是原路退

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const pagePath = path.join(here, '..', 'pages', 'RechargeOrderList', 'index.vue');

async function read() {
  return readFile(pagePath, 'utf8');
}

test('① 可退性来自后端 eligible-orders，不在前端自己推', async () => {
  const source = await read();
  assert.match(
    source,
    /getEligibleTicketRefundOrders\(/,
    '必须调用 /ticket-refunds/eligible-orders 判断可退性 —— 退款有金额上限、时间窗口、' +
      '是否已退过等规则，前端复刻必然漂移，且漂移方向是「显示按钮但点了报错」',
  );
  // 反面判据：不得用「状态=paid 且金额>0」之类的前端本地规则决定
  assert.doesNotMatch(
    source,
    /canRefundRow[^\n]*record\.status\s*===\s*['"]paid['"]/,
    '不得用前端本地状态判断可退性',
  );
});

test('② 提交字段与 CreateTicketRefundInput 对齐', async () => {
  const source = await read();
  const call = source.match(/createTicketRefund\(\{[\s\S]*?\}\)/);
  assert.ok(call, '必须调用 createTicketRefund');
  const body = call[0];
  for (const field of ['kind', 'orderId', 'requestedCents', 'reason']) {
    assert.match(body, new RegExp(`${field}\\s*:`), `提交体必须含 ${field} 字段（后端契约）`);
  }
});

test('③ kind 固定 original（充值记录发起 = 原路退回）', async () => {
  const source = await read();
  const call = source.match(/createTicketRefund\(\{[\s\S]*?\}\)/);
  assert.ok(call);
  assert.match(
    call[0],
    /kind:\s*["']original["']/,
    'kind 必须是 original —— manual 是人工退款，没有 orderId 语义，不该从充值记录入口进来',
  );
});

test('④ 操作列存在且按钮受 canRefundRow 控制', async () => {
  const source = await read();
  assert.match(source, /key:\s*['"]action['"]/, '必须新增「操作」列');
  assert.match(source, /v-if="canRefundRow\(record\)"/, '按钮必须只在可退时渲染');
  assert.match(source, /openRefundModal\(record\)/, '按钮必须打开退款小窗');
});

test('⑤ 金额校验在前端拦一次（避免无意义请求）', async () => {
  const source = await read();
  assert.match(source, /cents\s*>\s*refundTarget\.paidCents/, '必须拦「退款金额超过实付」');
  assert.match(source, /退款金额必须是大于 0 的数字/, '必须拦非正数金额');
});
