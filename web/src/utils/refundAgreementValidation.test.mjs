// 承诺书签署与退回方式的校验判据（2026-11-19 用户要求）。
//
// 用户在界面上的完整要求是：「用户点击勾选我已阅读并同意协议的框框的时候弹出弹窗，
// 然后需要用户阅读10s，并且强制需要滑动到最底下……该协议每次都要签署，不带记忆。」
//
// 阅读时长与滚动到底属于弹窗内部行为（需要浏览器事件），这里钉的是**提交闸门**：
// 没有签名就绝不能提交人工退款 —— 那是把钱打给一个只有用户口头承诺的账户。

import test from 'node:test';
import assert from 'node:assert/strict';
import { getRefundDraftValidation, buildTicketCreatePayload } from '../pages/Tickets/createTicketTypeHelpers.mjs';

const base = {
  requestedYuan: '10.00',
  reason: '充错了',
  refundMethod: 'manual',
  refundPayoutQrAttachmentId: 9,
};

test('人工退回必须签署承诺书', () => {
  const missing = getRefundDraftValidation({ ...base });
  assert.equal(missing.field, 'refundAgreement', '没签名时必须指向承诺书字段');
  assert.match(missing.message, /承诺书/);

  const zero = getRefundDraftValidation({ ...base, refundSignatureAttachmentId: 0 });
  assert.equal(zero.field, 'refundAgreement', '0 不算签名');

  const ok = getRefundDraftValidation({ ...base, refundSignatureAttachmentId: 9 });
  assert.equal(ok.message, '', `签名后应通过，实际：${ok.message}`);
});

test('原路退回不要求承诺书（那条路不用收款码）', () => {
  const result = getRefundDraftValidation({
    requestedYuan: '10.00',
    reason: '充错了',
    refundMethod: 'original',
    refundAllocations: [{ orderId: 1, cents: 1000 }],
  });
  assert.equal(result.message, '', `原路退回不该被承诺书挡住，实际：${result.message}`);
});

test('签名图随 payload 一起提交', () => {
  const { payload } = buildTicketCreatePayload({
    ticketType: 'refund',
    ...base,
    refundSignatureAttachmentId: 42,
  });
  assert.equal(payload.refundSignatureAttachmentId, 42, '签名必须真的发出去，否则后端存不到');
  assert.equal(payload.refundMethod, 'manual');
});

test('没有签名图时不发该字段（而不是发 0）', () => {
  const { payload } = buildTicketCreatePayload({
    ticketType: 'refund',
    requestedYuan: '10.00',
    reason: '充错了',
    refundMethod: 'original',
    refundAllocations: [{ orderId: 1, cents: 1000 }],
  });
  // 发 0 会被后端当成「给了但无效」，直接不发才是「没有」。
  assert.equal('refundSignatureAttachmentId' in payload, false);
});