import assert from "node:assert/strict";
import test from "node:test";
import {
  TICKET_CREATE_TYPE_GENERAL,
  TICKET_CREATE_TYPE_OPTIONS,
  TICKET_CREATE_TYPE_REFUND,
  buildTicketCreatePayload,
  getRefundDraftValidation,
  isRefundTicketRow,
  parseRefundYuanToCents,
  ticketCreateTypeNeedsRefundFields,
  ticketRowRefundStatus,
} from "../pages/Tickets/createTicketTypeHelpers.mjs";

// 新建工单时选择类型（T-209）的行为测试。
//
// 判据全部是**行为**（选退款后产出的 payload 里有没有退款字段、校验拦不拦得住），
// 不是「源码里有某个字符串」—— 后者改个名字就能蒙过。

// ── 类型清单：只放没有一键动作的两项 ──────────────────────────────────────

test("只提供两个可自选类型，且都不带管理员侧一键动作", () => {
  assert.deepEqual(
    TICKET_CREATE_TYPE_OPTIONS.map((item) => item.value),
    ["general", "refund"],
  );
  // 套利类别一个都不能出现（69-*.md §七.1 红线）。
  for (const forbidden of [
    "registration_bonus",
    "blacklist_appeal",
    "account_rebind",
    "account_closure",
  ]) {
    assert.ok(
      !TICKET_CREATE_TYPE_OPTIONS.some((item) => item.value === forbidden),
      `${forbidden} 不得出现在用户可选类型里（该类别带一键动作）`,
    );
  }
  // 每个选项都要有用户能看懂的说明，否则下拉是三个字母列表。
  for (const item of TICKET_CREATE_TYPE_OPTIONS) {
    assert.ok(item.label && item.label.length > 0);
    assert.ok(item.hint && item.hint.length > 0);
  }
});

test("只有退款类型会切换到退款专属字段", () => {
  assert.equal(ticketCreateTypeNeedsRefundFields(TICKET_CREATE_TYPE_REFUND), true);
  assert.equal(ticketCreateTypeNeedsRefundFields(" REFUND "), true);
  assert.equal(ticketCreateTypeNeedsRefundFields(TICKET_CREATE_TYPE_GENERAL), false);
  assert.equal(ticketCreateTypeNeedsRefundFields(""), false);
  assert.equal(ticketCreateTypeNeedsRefundFields(undefined), false);
});

// ── payload：选退款必须变成退款流程的入参，不是换个字符串 ────────────────

test("选退款类型时 payload 带退款字段且不带普通的标题/正文", () => {
  const { payload, validation } = buildTicketCreatePayload({
    ticketType: TICKET_CREATE_TYPE_REFUND,
    requestedYuan: "25.50",
    reason: "  充错了金额  ",
    refundOrderId: 88,
  });
  assert.equal(validation.message, "");
  assert.equal(payload.ticketType, "refund");
  // 金额必须转成分（后端只认分）。
  assert.equal(payload.refundRequestedCents, 2550);
  // 理由要去掉首尾空白。
  assert.equal(payload.refundReason, "充错了金额");
  assert.equal(payload.refundOrderId, 88);
  // 退款流程不接受 subject/body —— 壳工单的标题与正文由服务端按订单号生成。
  assert.equal("subject" in payload, false);
  assert.equal("body" in payload, false);
});

test("选退款但没指定订单时不传 refundOrderId（后端会归为人工退款）", () => {
  const { payload } = buildTicketCreatePayload({
    ticketType: TICKET_CREATE_TYPE_REFUND,
    requestedYuan: "10",
    reason: "理由",
  });
  assert.equal("refundOrderId" in payload, false);
  assert.equal(payload.refundRequestedCents, 1000);
});

test("普通类型只带标题正文，一个退款字段都不带", () => {
  const { payload, validation } = buildTicketCreatePayload({
    ticketType: TICKET_CREATE_TYPE_GENERAL,
    subject: " 登录不上 ",
    body: " 点登录没反应 ",
    // 用户在普通类型下误填了退款字段：必须被丢弃，不能透传
    // （后端对「普通工单带退款字段」会报参数矛盾）。
    requestedYuan: "99",
    reason: "不该出现",
  });
  assert.equal(validation.message, "");
  assert.equal(payload.ticketType, "general");
  assert.equal(payload.subject, "登录不上");
  assert.equal(payload.body, "点登录没反应");
  assert.equal("refundRequestedCents" in payload, false);
  assert.equal("refundReason" in payload, false);
  assert.equal("refundOrderId" in payload, false);
});

test("老客户端不传 ticketType 时按一般问题处理", () => {
  const { payload } = buildTicketCreatePayload({ subject: "标题", body: "正文" });
  assert.equal(payload.ticketType, "general");
});

// ── 校验：前端拦不住就等于让用户「提交了但后端报错」 ──────────────────────

test("退款金额校验挡住空值、非法格式与 0", () => {
  for (const [yuan, field] of [
    ["", "requestedYuan"],
    ["   ", "requestedYuan"],
    ["abc", "requestedYuan"],
    ["1e3", "requestedYuan"],
    ["-5", "requestedYuan"],
    ["0", "requestedYuan"],
    ["0.00", "requestedYuan"],
    ["1.234", "requestedYuan"],
  ]) {
    const result = getRefundDraftValidation({ requestedYuan: yuan, reason: "理由" });
    assert.equal(result.field, field, `${JSON.stringify(yuan)} 应被金额校验挡住`);
    assert.ok(result.message.length > 0);
  }
});

test("退款理由必填且不超 500 字", () => {
  assert.equal(getRefundDraftValidation({ requestedYuan: "1", reason: "" }).field, "reason");
  assert.equal(getRefundDraftValidation({ requestedYuan: "1", reason: "   " }).field, "reason");
  assert.equal(
    getRefundDraftValidation({ requestedYuan: "1", reason: "长".repeat(501) }).field,
    "reason",
  );
  // 500 字符整好通过（边界值，不能少算一个）。
  assert.equal(getRefundDraftValidation({ requestedYuan: "1", reason: "长".repeat(500) }).message, "");
});

test("合法退款表单完全通过校验", () => {
  assert.equal(getRefundDraftValidation({ requestedYuan: "0.01", reason: "理由" }).message, "");
  assert.equal(getRefundDraftValidation({ requestedYuan: "12345.67", reason: "r" }).message, "");
});

test("元转分是精确整数（不走浮点）", () => {
  assert.equal(parseRefundYuanToCents("25.50"), 2550);
  assert.equal(parseRefundYuanToCents("0.01"), 1);
  assert.equal(parseRefundYuanToCents("100"), 10000);
  assert.equal(parseRefundYuanToCents("1.1"), 110);
  // 非法一律 0（后端会把 0 判为金额错误，不会静默当成免费退款）。
  assert.equal(parseRefundYuanToCents(""), 0);
  assert.equal(parseRefundYuanToCents("1.234"), 0);
  assert.equal(parseRefundYuanToCents("abc"), 0);
});

// ── 列表行：退款状态与类型要能区分 ────────────────────────────────────────

test("退款行的真实状态取自 refund 摘要而不是行 status", () => {
  // 行 status 是映射口径（pending_review → open），管理员要看的是真实 7 态。
  const row = {
    category: "refund",
    status: "open",
    refund: { refundId: 7, status: "frozen", requestedCents: 1200 },
  };
  assert.equal(ticketRowRefundStatus(row), "frozen");
  assert.equal(isRefundTicketRow(row), true);

  // 普通工单没有退款状态。
  const general = { category: "general", status: "open" };
  assert.equal(ticketRowRefundStatus(general), "");
  assert.equal(isRefundTicketRow(general), false);
});

test("退款行缺少 refund 摘要时不误判、也不抛异常", () => {
  const broken = { category: "refund", status: "open" };
  assert.equal(ticketRowRefundStatus(broken), "");
  assert.equal(isRefundTicketRow(broken), true);
  assert.equal(ticketRowRefundStatus(null), "");
  assert.equal(isRefundTicketRow(null), false);
});

// ── 结构钉子：页面必须真的接线（否则 helper 再好也不生效）──────────────────

test("新建表单与列表都接上了类型选择与退款展示", async () => {
  const { readFile } = await import("node:fs/promises");
  const page = await readFile(new URL("../pages/Tickets/index.vue", import.meta.url), "utf8");
  // 新建表单要出现类型下拉与退款专属字段。
  assert.match(page, /TICKET_CREATE_TYPE_OPTIONS/, "新建表单必须用类型清单");
  assert.match(page, /ticketCreateTypeNeedsRefundFields/, "必须按类型切换字段");
  assert.match(page, /buildTicketCreatePayload/, "提交必须走统一的 payload 构造");
  // 列表要能显示退款真实状态。
  assert.match(page, /ticketRowRefundStatus/, "列表行必须显示退款真实状态");
  assert.match(page, /isRefundTicketRow/, "列表必须能区分退款行");
});
