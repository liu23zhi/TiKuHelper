import assert from "node:assert/strict";
import test from "node:test";
import {
  TICKET_ACCOUNT_ISSUE_OPTIONS,
  TICKET_CREATE_FORBIDDEN_TYPES,
  TICKET_CREATE_TYPE_ACCOUNT,
  TICKET_CREATE_TYPE_ACCOUNT_CLOSURE,
  TICKET_CREATE_TYPE_GENERAL,
  TICKET_CREATE_TYPE_OPTIONS,
  TICKET_CREATE_TYPE_REFUND,
  TICKET_WIZARD_STEP_CLOSURE_GUIDE,
  TICKET_WIZARD_STEP_FORM,
  TICKET_WIZARD_STEP_TYPE,
  buildClosureTicketSubject,
  buildTicketCreatePayload,
  getAccountDraftValidation,
  getClosureDraftValidation,
  getRefundDraftValidation,
  getTicketCreateDraftValidation,
  isAccountClosureTicketType,
  isRefundTicketRow,
  normalizeTicketCreateType,
  parseRefundYuanToCents,
  ticketAccountIssueLabel,
  ticketCreateTypeNeedsRefundFields,
  ticketRowRefundStatus,
  ticketWizardCanAdvanceFromType,
  ticketWizardCanSubmit,
  ticketWizardInitialStep,
  ticketWizardNextStep,
} from "../pages/Tickets/createTicketTypeHelpers.mjs";

// 新建工单时选择类型 + 两步向导（T-209 建立，B2 扩到四项）。
//
// 判据全部是**行为**（选退款后产出的 payload 里有没有退款字段、校验拦不拦得住、
// 向导下一步落在哪），不是「源码里有某个字符串」—— 后者改个名字就能蒙过。
//
// ⚠️ 期望值一律写**字面量**（"general" / "account" / "account_closure"），
// 刻意不引用被测常量：否则常量被改回旧值时期望与被测值同步漂移 → 断言恒真。

// ── 判据 T1：类型清单恰好四项白名单，套利项一个都不能有 ─────────────────

test("只提供四个可自选类型，且都不带管理员侧一键动作（T1）", () => {
  assert.deepEqual(
    TICKET_CREATE_TYPE_OPTIONS.map((item) => item.value),
    ["general", "refund", "account", "account_closure"],
  );
  // 套利类别一个都不能出现（69-*.md §七.1 红线）。
  // ⚠️ account_closure **不在**本清单里（B2-2 已加入白名单：它不带一键动作）。
  for (const forbidden of [
    "registration_bonus",
    "blacklist_appeal",
    "account_rebind",
  ]) {
    assert.ok(
      !TICKET_CREATE_TYPE_OPTIONS.some((item) => item.value === forbidden),
      `${forbidden} 不得出现在用户可选类型里（该类别带一键动作）`,
    );
  }
  // 红线清单本身也要覆盖这三项（供页面/测试共用，防止有人只改一处）。
  assert.deepEqual([...TICKET_CREATE_FORBIDDEN_TYPES].sort(), [
    "account_rebind",
    "blacklist_appeal",
    "registration_bonus",
  ]);
  // 每个选项都要有用户能看懂的说明，否则选项列表是三个字母列表。
  for (const item of TICKET_CREATE_TYPE_OPTIONS) {
    assert.ok(item.label && item.label.length > 0, `${item.value} 缺 label`);
    assert.ok(item.hint && item.hint.length > 0, `${item.value} 缺 hint`);
  }
});

test("normalizeTicketCreateType 只认白名单，非法值不兜底成 general", () => {
  for (const value of ["general", "refund", "account", "account_closure"]) {
    assert.equal(normalizeTicketCreateType(value), value);
    assert.equal(normalizeTicketCreateType(` ${value.toUpperCase()} `), value);
  }
  // 非法值返回空串：静默兜底成 general 会让用户以为「注销申请已提交」。
  for (const value of ["registration_bonus", "blacklist_appeal", "account_rebind", "bogus", "", null, undefined]) {
    assert.equal(normalizeTicketCreateType(value), "");
  }
});

// ── 判据 T4：注销申请必须先出引导页，不能直接进表单 ──────────────────────

test("向导从「选类型」开始，选完类型才进模板（T3/T6）", () => {
  // 第一步恒为「选类型」：用户点的是「发起工单」而不是某个具体类型。
  assert.equal(ticketWizardInitialStep(), "type");
  assert.equal(ticketWizardInitialStep(), TICKET_WIZARD_STEP_TYPE);

  // 普通类型直接进表单。
  for (const value of ["general", "refund", "account"]) {
    assert.equal(ticketWizardNextStep(value), "form", `${value} 应直接进表单`);
    assert.equal(ticketWizardNextStep(value), TICKET_WIZARD_STEP_FORM);
  }
  // 注销申请**例外**：先进引导页。
  assert.equal(
    ticketWizardNextStep("account_closure"),
    "closure-guide",
    "注销申请必须先出引导页（跳过引导直接进表单会让用户错过自助注销）",
  );
  assert.equal(ticketWizardNextStep("account_closure"), TICKET_WIZARD_STEP_CLOSURE_GUIDE);
  // 大小写/空白不影响分叉。
  assert.equal(ticketWizardNextStep(" ACCOUNT_CLOSURE "), TICKET_WIZARD_STEP_CLOSURE_GUIDE);
});

test("第一步没选类型时不能前进（避免落到空模板）", () => {
  for (const value of ["general", "refund", "account", "account_closure", " Account "]) {
    assert.equal(ticketWizardCanAdvanceFromType(value), true, `${value} 应可前进`);
  }
  for (const value of ["", "   ", "registration_bonus", "blacklist_appeal", "account_rebind", "bogus", null, undefined]) {
    assert.equal(ticketWizardCanAdvanceFromType(value), false, `${value} 不得前进`);
  }
});

test("只有「填模板」步骤允许提交（T4 的行为落点）", () => {
  // 正例：在表单步骤提交是正常路径。
  assert.equal(ticketWizardCanSubmit("form"), true);
  assert.equal(ticketWizardCanSubmit(" FORM "), true);
  // 反例：引导页**绝不能**提交 —— 否则用户在还没看到「推荐自助注销」
  // 的情况下就被建了一张注销工单（判据 T4 的核心）。
  assert.equal(
    ticketWizardCanSubmit("closure-guide"),
    false,
    "注销引导页不得提交（跳过引导直接建单必须被挡住）",
  );
  assert.equal(ticketWizardCanSubmit("type"), false, "选类型页不得提交");
  for (const value of ["", "   ", null, undefined]) {
    assert.equal(ticketWizardCanSubmit(value), false);
  }
});

// ── 判据 T5：「去自助注销」走的是 closure 链路（本节钉住组件契约）────────

test("注销类型识别与固定标题（自助注销是主路径的前提）", () => {
  assert.equal(isAccountClosureTicketType("account_closure"), true);
  assert.equal(isAccountClosureTicketType(" ACCOUNT_CLOSURE "), true);
  for (const value of ["general", "refund", "account", "", null, undefined]) {
    assert.equal(isAccountClosureTicketType(value), false);
  }
  // 标题固定带前缀：管理员按标题分诊时不依赖用户随手写的文案。
  assert.equal(buildClosureTicketSubject(), "【注销申请】账号注销兜底工单");
  assert.match(buildClosureTicketSubject(), /^【注销申请】/);
});

// ── 判据 T3：不同类型出不同模板字段 ──────────────────────────────────────

test("只有退款类型会切换到退款专属字段", () => {
  assert.equal(ticketCreateTypeNeedsRefundFields(TICKET_CREATE_TYPE_REFUND), true);
  assert.equal(ticketCreateTypeNeedsRefundFields(" REFUND "), true);
  assert.equal(ticketCreateTypeNeedsRefundFields(TICKET_CREATE_TYPE_GENERAL), false);
  assert.equal(ticketCreateTypeNeedsRefundFields(TICKET_CREATE_TYPE_ACCOUNT), false);
  assert.equal(ticketCreateTypeNeedsRefundFields(TICKET_CREATE_TYPE_ACCOUNT_CLOSURE), false);
  assert.equal(ticketCreateTypeNeedsRefundFields(""), false);
  assert.equal(ticketCreateTypeNeedsRefundFields(undefined), false);
});

test("account 的问题类型是三项固定枚举，不是自由文本（B2-6）", () => {
  assert.deepEqual(
    TICKET_ACCOUNT_ISSUE_OPTIONS.map((item) => item.value),
    ["login", "binding", "banned"],
  );
  assert.deepEqual(
    TICKET_ACCOUNT_ISSUE_OPTIONS.map((item) => item.label),
    ["登录异常", "绑定问题", "封禁申诉"],
  );
  assert.equal(ticketAccountIssueLabel("login"), "登录异常");
  assert.equal(ticketAccountIssueLabel(" BANNED "), "封禁申诉");
  assert.equal(ticketAccountIssueLabel("other"), "");
  assert.equal(ticketAccountIssueLabel(""), "");
});

// ── payload：选退款必须变成退款流程的入参，不是换个字符串 ────────────────

test("选退款类型时 payload 带退款字段且不带普通的标题/正文", () => {
  const { payload, validation } = buildTicketCreatePayload({
    ticketType: TICKET_CREATE_TYPE_REFUND,
    requestedYuan: "25.50",
    reason: "  充错了金额  ",
    refundMethod: "original",
    refundAllocations: [{ orderId: 88, cents: 2550 }],
  });
  assert.equal(validation.message, "");
  assert.equal(payload.ticketType, "refund");
  // 金额必须转成分（后端只认分）。
  assert.equal(payload.refundRequestedCents, 2550);
  // 理由要去掉首尾空白。
  assert.equal(payload.refundReason, "充错了金额");
  // 多选后是数组（2026-11-18）：后端按「一笔订单一张退款单」展开。
  // 2026-11-19：改为上传每笔各自的金额 —— 只有 id 表达不出 7.5+2.5。
  assert.deepEqual(payload.refundOrderAllocations, [{ orderId: 88, cents: 2550 }]);
  // 退款流程不接受 subject/body —— 壳工单的标题与正文由服务端按订单号生成。
  assert.equal("subject" in payload, false);
  assert.equal("body" in payload, false);
});

test("选退款但没指定订单时不传 refundOrderIds（后端会归为人工退款）", () => {
  const { payload } = buildTicketCreatePayload({
    ticketType: TICKET_CREATE_TYPE_REFUND,
    requestedYuan: "10",
    reason: "理由",
  });
  // 无订单 = 人工退款：不带分摊，也不带旧的 refundOrderIds。
  assert.equal("refundOrderAllocations" in payload, false);
  assert.equal("refundOrderIds" in payload, false);
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
  // 无订单 = 人工退款：不带分摊，也不带旧的 refundOrderIds。
  assert.equal("refundOrderAllocations" in payload, false);
  assert.equal("refundOrderIds" in payload, false);
});

test("老客户端不传 ticketType 时按一般问题处理", () => {
  const { payload } = buildTicketCreatePayload({ subject: "标题", body: "正文" });
  assert.equal(payload.ticketType, "general");
});

test("account 类型的标题带固定前缀、正文是用户说明、不带退款字段（B2-1/B2-6）", () => {
  const { payload, validation } = buildTicketCreatePayload({
    ticketType: TICKET_CREATE_TYPE_ACCOUNT,
    issueType: "banned",
    body: "  12 月 1 日账号被限制登录  ",
    // 误填的普通标题/退款字段都必须被丢弃。
    subject: "不该出现",
    reason: "不该出现",
    requestedYuan: "9.9",
  });
  assert.equal(validation.message, "");
  assert.equal(payload.ticketType, "account");
  // 前缀来自白名单固定文案，不是用户输入拼接（防冒充「封禁申诉」）。
  assert.equal(payload.subject, "【封禁申诉】账号问题");
  assert.equal(payload.body, "12 月 1 日账号被限制登录");
  assert.equal("refundRequestedCents" in payload, false);
  assert.equal("refundReason" in payload, false);
  // 问题类型不进 payload（后端没有这个字段）。
  assert.equal("issueType" in payload, false);
});

test("account_closure 类型的标题固定生成、用户只填原因（B2-2）", () => {
  const { payload, validation } = buildTicketCreatePayload({
    ticketType: TICKET_CREATE_TYPE_ACCOUNT_CLOSURE,
    body: "  不再使用该账号  ",
    subject: "不该出现",
  });
  assert.equal(validation.message, "");
  assert.equal(payload.ticketType, "account_closure");
  assert.equal(payload.subject, "【注销申请】账号注销兜底工单");
  assert.equal(payload.body, "不再使用该账号");
  assert.equal("refundRequestedCents" in payload, false);
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
  assert.equal(getRefundDraftValidation({
    requestedYuan: "1",
    reason: "",
    refundMethod: "manual",
    refundPayoutQrAttachmentId: 9,
  }).field, "reason");
  assert.equal(getRefundDraftValidation({
    requestedYuan: "1",
    reason: "   ",
    refundMethod: "manual",
    refundPayoutQrAttachmentId: 9,
  }).field, "reason");
  assert.equal(
    getRefundDraftValidation({
      requestedYuan: "1",
      reason: "长".repeat(501),
      refundMethod: "manual",
      refundPayoutQrAttachmentId: 9,
    }).field,
    "reason",
  );
  // 500 字符整好通过（边界值，不能少算一个）。
  assert.equal(getRefundDraftValidation({
    requestedYuan: "1",
    reason: "长".repeat(500),
    refundMethod: "manual",
    refundPayoutQrAttachmentId: 9,
  }).message, "");
});

test("合法退款表单完全通过校验", () => {
  assert.equal(
    getRefundDraftValidation({
      requestedYuan: "0.01",
      reason: "理由",
      refundMethod: "original",
      refundAllocations: [{ orderId: 1, cents: 1 }],
    }).message,
    "",
  );
  assert.equal(
    getRefundDraftValidation({
      requestedYuan: "12345.67",
      reason: "r",
      refundMethod: "manual",
      refundPayoutQrAttachmentId: 9,
    }).message,
    "",
  );
});

test("account 校验：问题类型必选、说明必填（B2-6）", () => {
  assert.equal(getAccountDraftValidation({ issueType: "", body: "说明" }).field, "issueType");
  assert.equal(getAccountDraftValidation({ issueType: "other", body: "说明" }).field, "issueType");
  assert.equal(getAccountDraftValidation({ issueType: "login", body: "   " }).field, "body");
  assert.equal(getAccountDraftValidation({ issueType: "login", body: "说明" }).message, "");
  assert.equal(
    getAccountDraftValidation({ issueType: "login", body: "长".repeat(10001) }).field,
    "body",
  );
  // 10000 字符整好通过（与后端 TicketBodyMaxLength 对齐）。
  assert.equal(getAccountDraftValidation({ issueType: "login", body: "长".repeat(10000) }).message, "");
});

test("account_closure 校验：原因必填", () => {
  assert.equal(getClosureDraftValidation({ body: "" }).field, "body");
  assert.equal(getClosureDraftValidation({ body: "   " }).field, "body");
  assert.equal(getClosureDraftValidation({ body: "不再使用" }).message, "");
  assert.equal(getClosureDraftValidation({ body: "长".repeat(10001) }).field, "body");
});

test("按类型分派的校验：同一 draft 不同类型给出不同 field", () => {
  // 同一份空 draft：general 报 subject、account 报 issueType、
  // account_closure 报 body、refund 报 requestedYuan。
  //
  // 这条钉住「页面不应自己维护第二张类型→校验映射表」：
  // 如果页面绕过分派各自调用，某类型就会漏校验。
  const empty = { subject: "", body: "", requestedYuan: "", reason: "", issueType: "" };
  assert.equal(getTicketCreateDraftValidation({ ...empty, ticketType: "general" }).field, "subject");
  assert.equal(getTicketCreateDraftValidation({ ...empty, ticketType: "refund" }).field, "requestedYuan");
  assert.equal(getTicketCreateDraftValidation({ ...empty, ticketType: "account" }).field, "issueType");
  assert.equal(
    getTicketCreateDraftValidation({ ...empty, ticketType: "account_closure" }).field,
    "body",
  );
  // 非法/缺省类型退回 general 那套（老客户端行为不变）。
  assert.equal(getTicketCreateDraftValidation(empty).field, "subject");
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

test("列表页接上两步向导、注销引导与自助注销入口", async () => {
  const { readFile } = await import("node:fs/promises");
  const page = await readFile(new URL("../pages/Tickets/index.vue", import.meta.url), "utf8");
  const profile = await readFile(new URL("../pages/Profile/index.vue", import.meta.url), "utf8");
  const api = await readFile(new URL("../api/TikuComponentApi.js", import.meta.url), "utf8");
  const guide = await readFile(new URL("../pages/Tickets/ClosureGuide.vue", import.meta.url), "utf8");

  // 两步向导：类型清单 + 步骤状态机 + 按类型分派的 payload/校验。
  assert.match(page, /TICKET_CREATE_TYPE_OPTIONS/, "向导第一步必须用类型清单");
  assert.match(page, /createStep/, "必须由步骤状态驱动向导（不能靠多个布尔）");
  assert.match(page, /ticketWizardNextStep/, "第二步落点必须走 helper 分叉");
  assert.match(page, /buildTicketCreatePayload/, "提交必须走统一的 payload 构造");
  assert.match(page, /getTicketCreateDraftValidation/, "校验必须按类型分派到同一份实现");
  // 注销引导必须被渲染（而不是只 import）。
  assert.match(page, /<ClosureGuide/, "注销类型必须先渲染引导组件");
  // 列表要能显示退款真实状态。
  assert.match(page, /ticketRowRefundStatus/, "列表行必须显示退款真实状态");
  assert.match(page, /isRefundTicketRow/, "列表必须能区分退款行");

  // 个人中心必须挂自助注销入口（B2-5）。
  //
  // ⚠️ 断言的是**渲染**（`<ClosureGuide`），不是词面出现：
  // import 语句、components 注册里都有 "ClosureGuide"，只用 /ClosureGuide/
  // 做断言的话，把模板里那一行删掉测试仍然全绿（实测踩过 —— 这是假钉子）。
  assert.match(profile, /<ClosureGuide\b/, "个人中心必须真的渲染自助注销组件（不只是 import）");
  assert.match(profile, /data-section="account-closure"/, "个人中心必须有稳定的区块标记");

  // 引导页主按钮=自助注销、次按钮=仍然提交工单（顺序不能反）。
  //
  // 同样断言**元素**而不是词面：两个 data-closure-action 都必须以属性形式出现。
  assert.match(guide, /<a-button[\s\S]{0,200}data-closure-action="self-service"/, "必须有「去自助注销」按钮元素");
  assert.match(guide, /<a-button[\s\S]{0,200}data-closure-action="fallback"/, "必须有「仍然提交工单」按钮元素");
  // 主按钮必须带 type="primary"（视觉上也是主按钮，而不只是顺序在前）。
  const selfButtonBlock = guide.slice(
    Math.max(0, guide.indexOf('data-closure-action="self-service"') - 300),
    guide.indexOf('data-closure-action="self-service"'),
  );
  assert.match(selfButtonBlock, /type="primary"/, "「去自助注销」必须是 primary 主按钮");
  // 主按钮必须排在次按钮之前。
  const selfAt = guide.indexOf('data-closure-action="self-service"');
  const fallbackAt = guide.indexOf('data-closure-action="fallback"');
  assert.ok(selfAt > 0 && fallbackAt > selfAt, "主按钮（自助注销）必须排在次按钮之前");

  // 三条 closure 接口必须真的指向后端已存在的路由。
  assert.match(api, /\/api\/user\/closure-status/, "必须调用 closure-status");
  assert.match(api, /\/api\/user\/closure-request/, "必须调用 closure-request");
  assert.match(api, /\/api\/user\/closure-cancel/, "必须调用 closure-cancel");
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
    refundMethod: "original",
    refundAllocations: [{ orderId: 88, cents: 2550 }],
  });
  assert.equal(validation.message, "");
  assert.equal(payload.ticketType, "refund");
  // 金额必须转成分（后端只认分）。
  assert.equal(payload.refundRequestedCents, 2550);
  // 理由要去掉首尾空白。
  assert.equal(payload.refundReason, "充错了金额");
  // 多选后是数组（2026-11-18）：后端按「一笔订单一张退款单」展开。
  // 2026-11-19：改为上传每笔各自的金额 —— 只有 id 表达不出 7.5+2.5。
  assert.deepEqual(payload.refundOrderAllocations, [{ orderId: 88, cents: 2550 }]);
  // 退款流程不接受 subject/body —— 壳工单的标题与正文由服务端按订单号生成。
  assert.equal("subject" in payload, false);
  assert.equal("body" in payload, false);
});

test("选退款但没指定订单时不传 refundOrderIds（后端会归为人工退款）", () => {
  const { payload } = buildTicketCreatePayload({
    ticketType: TICKET_CREATE_TYPE_REFUND,
    requestedYuan: "10",
    reason: "理由",
  });
  // 无订单 = 人工退款：不带分摊，也不带旧的 refundOrderIds。
  assert.equal("refundOrderAllocations" in payload, false);
  assert.equal("refundOrderIds" in payload, false);
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
  // 无订单 = 人工退款：不带分摊，也不带旧的 refundOrderIds。
  assert.equal("refundOrderAllocations" in payload, false);
  assert.equal("refundOrderIds" in payload, false);
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
  assert.equal(getRefundDraftValidation({
    requestedYuan: "1",
    reason: "",
    refundMethod: "manual",
    refundPayoutQrAttachmentId: 9,
  }).field, "reason");
  assert.equal(getRefundDraftValidation({
    requestedYuan: "1",
    reason: "   ",
    refundMethod: "manual",
    refundPayoutQrAttachmentId: 9,
  }).field, "reason");
  assert.equal(
    getRefundDraftValidation({
      requestedYuan: "1",
      reason: "长".repeat(501),
      refundMethod: "manual",
      refundPayoutQrAttachmentId: 9,
    }).field,
    "reason",
  );
  // 500 字符整好通过（边界值，不能少算一个）。
  assert.equal(getRefundDraftValidation({
    requestedYuan: "1",
    reason: "长".repeat(500),
    refundMethod: "manual",
    refundPayoutQrAttachmentId: 9,
  }).message, "");
});

test("合法退款表单完全通过校验", () => {
  assert.equal(
    getRefundDraftValidation({
      requestedYuan: "0.01",
      reason: "理由",
      refundMethod: "original",
      refundAllocations: [{ orderId: 1, cents: 1 }],
    }).message,
    "",
  );
  assert.equal(
    getRefundDraftValidation({
      requestedYuan: "12345.67",
      reason: "r",
      refundMethod: "manual",
      refundPayoutQrAttachmentId: 9,
    }).message,
    "",
  );
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
