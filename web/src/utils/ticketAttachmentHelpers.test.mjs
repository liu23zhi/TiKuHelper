import assert from "node:assert/strict";
import test from "node:test";

import {
  groupTicketAttachmentsByMessage,
  isTicketAttachmentVisible,
  pickTicketAttachmentIds,
  TICKET_ATTACHMENT_MAX_BYTES,
  ticketAttachmentPlaceholder,
  validateTicketAttachmentFile,
} from "./ticketAttachmentHelpers.mjs";

test("placeholder text prefers the server-provided reason", () => {
  assert.equal(
    ticketAttachmentPlaceholder({
      visible: false,
      hiddenReason: "服务端给的提示",
      scanState: "sensitive",
    }),
    "服务端给的提示"
  );
});

test("placeholder text falls back to scanState wording when reason is missing", () => {
  assert.equal(
    ticketAttachmentPlaceholder({ visible: false, scanState: "sensitive" }),
    "此图片包含敏感内容，已隐藏"
  );
  assert.equal(
    ticketAttachmentPlaceholder({ visible: false, scanState: "pending" }),
    "图片正在进行安全检测，暂不显示"
  );
  assert.equal(
    ticketAttachmentPlaceholder({ visible: false, scanState: "failed" }),
    "图片未通过安全检测，已隐藏"
  );
  // 未知状态同样不能放行，必须落到「未通过安全检测」。
  assert.equal(
    ticketAttachmentPlaceholder({ visible: false, scanState: "weird" }),
    "图片未通过安全检测，已隐藏"
  );
});

test("placeholder text is empty for visible attachments", () => {
  assert.equal(
    ticketAttachmentPlaceholder({ visible: true, scanState: "approved" }),
    ""
  );
});

test("isTicketAttachmentVisible requires an explicit server-side true", () => {
  assert.equal(isTicketAttachmentVisible({ visible: true }), true);
  assert.equal(isTicketAttachmentVisible({ visible: false }), false);
  // 关键：不能因为缺字段就当成可见（fail-closed）。
  assert.equal(isTicketAttachmentVisible({}), false);
  assert.equal(isTicketAttachmentVisible(null), false);
  // 字符串 "true" 也不行：服务端下发的是布尔 true。
  assert.equal(isTicketAttachmentVisible({ visible: "true" }), false);
  // scanState=approved 但没有 visible 也不放行（判据唯一）。
  assert.equal(isTicketAttachmentVisible({ scanState: "approved" }), false);
});

test("validateTicketAttachmentFile rejects non-images, oversize, and empty files", () => {
  assert.equal(validateTicketAttachmentFile(null), "请选择图片文件");
  assert.equal(
    validateTicketAttachmentFile({ name: "a.txt", type: "text/plain", size: 10 }),
    "仅支持 PNG / JPEG 图片"
  );
  assert.equal(
    validateTicketAttachmentFile({ name: "a.png", type: "image/png", size: 0 }),
    "图片内容为空"
  );
  assert.equal(
    validateTicketAttachmentFile({
      name: "a.png",
      type: "image/png",
      size: TICKET_ATTACHMENT_MAX_BYTES + 1,
    }),
    "图片不能超过 2 MiB"
  );
  assert.equal(
    validateTicketAttachmentFile({ name: "a.png", type: "image/png", size: 2048 }),
    ""
  );
  assert.equal(
    validateTicketAttachmentFile({ name: "a.jpeg", type: "image/jpeg", size: 2048 }),
    ""
  );
  // 扩展名可用但 MIME 为空（某些浏览器）也接受，最终由服务端按真实魔数判定。
  assert.equal(
    validateTicketAttachmentFile({ name: "a.PNG", type: "", size: 2048 }),
    ""
  );
});

test("pickTicketAttachmentIds keeps only positive integer ids", () => {
  assert.deepEqual(
    pickTicketAttachmentIds([
      { id: 3 },
      { id: "4" },
      { id: 0 },
      { id: -1 },
      { id: 1.5 },
      null,
      { id: null },
    ]),
    [3, 4]
  );
  assert.deepEqual(pickTicketAttachmentIds(null), []);
  assert.deepEqual(pickTicketAttachmentIds([]), []);
});

test("groupTicketAttachmentsByMessage groups by message id and keeps unbound ones", () => {
  const groups = groupTicketAttachmentsByMessage([
    { id: 1, messageId: 10 },
    { id: 2, messageId: 10 },
    { id: 3, messageId: 11 },
    { id: 4, messageId: null },
    { id: 5 },
  ]);
  assert.equal(groups.get("10").length, 2);
  assert.equal(groups.get("11").length, 1);
  // 未绑定到消息的（null / 缺失）统一落到 "ticket" 组。
  assert.equal(groups.get("ticket").length, 2);
  assert.equal(groupTicketAttachmentsByMessage(null).size, 0);
});
