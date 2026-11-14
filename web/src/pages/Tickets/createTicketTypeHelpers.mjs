// 新建工单时选择类型的纯函数集合（T-209）。
//
// 放在页面目录而不是 web/src/utils/ticketHelpers.mjs：本任务的独占可写范围是
// web/src/pages/Tickets/** 与 web/src/utils/ticket*.test.mjs，utils 下的
// 非测试文件不在范围内（那个文件由其它任务共用，改它容易撞车）。
// 页面与测试都从这里 import，行为完全等价。

import { getTicketDraftValidation, normalizeTicketText } from "../../utils/ticketHelpers.mjs";

// ─────────────────── 新建工单时选择类型（T-209）───────────────────
//
// 用户原话：「新建工单的时候让用户选择工单类型」。
//
// # 为什么这里只放两项
//
// 后端 69-*.md §七.1 的红线是「category 不得由用户自选」，理由是套利：
// 用户挑 registration_bonus 就能诱导管理员点「一键补发赠金」。
// 用户现在要选类型，与那条红线并不冲突 —— 把可选项收窄成**没有一键动作**的两项：
//
//   general（一般问题）—— 自由提问，无专属动作；
//   refund（退款申请）  —— 有专属流程，但审核/打款只有管理员能点。
//
// 任何将来要加进来的类型，判据是「用户自选后能不能触发管理员侧的一键动作」：
// 能，就不许加。归因类（注册赠金 / 封禁申诉 / 换绑 / 注销）因此不在列表里。
export const TICKET_CREATE_TYPE_GENERAL = "general";
export const TICKET_CREATE_TYPE_REFUND = "refund";

export const TICKET_CREATE_TYPE_OPTIONS = Object.freeze([
  {
    value: TICKET_CREATE_TYPE_GENERAL,
    label: "一般问题",
    hint: "登录、使用、功能异常等常规问题",
  },
  {
    value: TICKET_CREATE_TYPE_REFUND,
    label: "退款申请",
    hint: "充值后需要退款，需要选择订单并填写金额与理由",
  },
]);

// ticketCreateTypeNeedsRefundFields 判断选了该类型后表单是否要切换到退款字段。
//
// 这是任务书明确要求的：**不能只是换个 category 字符串**。
// 选退款必须让用户填订单/金额/理由，否则后端无法受理（会直接报错）。
export function ticketCreateTypeNeedsRefundFields(ticketType) {
  return String(ticketType || "").trim().toLowerCase() === TICKET_CREATE_TYPE_REFUND;
}

// getRefundDraftValidation 是退款类型的表单校验（与普通工单是两套字段）。
//
// 返回 { field, message }，message 为空表示通过。判据与后端
// createRefundTicketWithVerifier 完全一致（金额 > 0、理由必填且不超 500 字），
// 这样用户不会「前端通过、后端报错」。
export function getRefundDraftValidation({ requestedYuan, reason } = {}) {
  const yuan = String(requestedYuan ?? "").trim();
  if (!yuan) return Object.freeze({ field: "requestedYuan", message: "请输入退款金额" });
  // 只接受「正整数或最多两位小数」，避免 1e3 / 0x10 这类被 Number() 吃掉的形式。
  const matched = /^(0|[1-9]\d{0,11})(?:\.(\d{1,2}))?$/.exec(yuan);
  if (!matched) return Object.freeze({ field: "requestedYuan", message: "退款金额格式不正确，最多两位小数" });
  const cents =
    Number(matched[1]) * 100 + Number((matched[2] ?? "").padEnd(2, "0"));
  if (!Number.isSafeInteger(cents) || cents <= 0) {
    return Object.freeze({ field: "requestedYuan", message: "退款金额必须大于 0" });
  }
  const text = String(reason ?? "").replace(/\r\n?/g, "\n").trim();
  if (!text) return Object.freeze({ field: "reason", message: "请填写退款理由" });
  if (Array.from(text).length > 500) {
    return Object.freeze({ field: "reason", message: "退款理由不能超过 500 个字符" });
  }
  return Object.freeze({ field: "", message: "" });
}

// buildTicketCreatePayload 把新建表单整理成后端 CreateTicketInput。
//
// 关键：退款类型下**必须**带 refundRequestedCents / refundReason / refundOrderId，
// 且不传 subject/body（壳工单的标题与正文由服务端按订单号自动生成）。
// 普通类型则相反：只传 subject/body，一个退款字段都不带
// （后端对「普通工单带退款字段」会报错，这是有意的参数矛盾检测）。
export function buildTicketCreatePayload(draft = {}) {
  const ticketType = String(draft.ticketType || TICKET_CREATE_TYPE_GENERAL)
    .trim()
    .toLowerCase();
  const attachmentIds = Array.isArray(draft.attachmentIds)
    ? draft.attachmentIds.filter((id) => Number.isSafeInteger(id) && id > 0)
    : [];

  if (ticketType === TICKET_CREATE_TYPE_REFUND) {
    const validation = getRefundDraftValidation(draft);
    const payload = {
      ticketType: TICKET_CREATE_TYPE_REFUND,
      refundRequestedCents: Number.isSafeInteger(draft.refundRequestedCents)
        ? draft.refundRequestedCents
        : parseRefundYuanToCents(draft.requestedYuan),
      refundReason: normalizeTicketText(draft.reason, 500),
    };
    // 订单可选：不选 = 无单人工退款（后端会落 kind=manual）。
    const orderId = Number(draft.refundOrderId);
    if (Number.isSafeInteger(orderId) && orderId > 0) payload.refundOrderId = orderId;
    if (attachmentIds.length) payload.attachmentIds = attachmentIds;
    return Object.freeze({ payload: Object.freeze(payload), validation });
  }

  return Object.freeze({
    payload: Object.freeze({
      ticketType: TICKET_CREATE_TYPE_GENERAL,
      subject: normalizeTicketText(draft.subject, 120),
      body: normalizeTicketText(draft.body, 10000),
      ...(attachmentIds.length ? { attachmentIds } : {}),
    }),
    validation: getTicketDraftValidation(draft.subject, draft.body),
  });
}

// parseRefundYuanToCents 把「元」文本转成分。非法返回 0（后端会把 0 判为金额错误）。
export function parseRefundYuanToCents(value) {
  const matched = /^(0|[1-9]\d{0,11})(?:\.(\d{1,2}))?$/.exec(String(value ?? "").trim());
  if (!matched) return 0;
  const cents = Number(matched[1]) * 100 + Number((matched[2] ?? "").padEnd(2, "0"));
  return Number.isSafeInteger(cents) ? cents : 0;
}

// ticketRowRefundStatus 取一行的**退款真实状态**（7 态），普通工单返回空串。
//
// 列表的行状态是映射口径（退款 7 态 → 工单 4 态），管理员要看的
// 「冻结中 / 处理中 / 结果待核对」只能从 refund 摘要里拿。
export function ticketRowRefundStatus(ticket) {
  const status = String(ticket?.refund?.status || "").trim().toLowerCase();
  return status;
}

// isRefundTicketRow 判断一行是不是退款工单。
export function isRefundTicketRow(ticket) {
  return (
    String(ticket?.category || "").trim().toLowerCase() === TICKET_CREATE_TYPE_REFUND
  );
}