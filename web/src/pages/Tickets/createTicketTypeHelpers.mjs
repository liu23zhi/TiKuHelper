// 新建工单时选择类型的纯函数集合（T-209 建立，B2 扩到四项 + 两步向导）。
//
// 放在页面目录而不是 web/src/utils/ticketHelpers.mjs：本任务的独占可写范围是
// web/src/pages/Tickets/** 与 web/src/utils/ticket*.test.mjs，utils 下的
// 非测试文件不在范围内（那个文件由其它任务共用，改它容易撞车）。
// 页面与测试都从这里 import，行为完全等价。

import { getTicketDraftValidation, normalizeTicketText } from "../../utils/ticketHelpers.mjs";

// ─────────────────── 新建工单时选择类型 ───────────────────
//
// 用户原话：「新建工单的时候让用户选择工单类型」。
//
// # 为什么只有这四项
//
// 后端 69-*.md §七.1 的红线是「category 不得由用户自选」，理由是套利：
// 用户挑 registration_bonus 就能诱导管理员点「一键补发赠金」。
// 用户现在要选类型，与那条红线并不冲突 —— 把可选项收窄成**没有一键动作**的项：
//
//   general          一般问题   —— 自由提问，无专属动作；
//   refund           退款申请   —— 有专属流程，但审核/打款只有管理员能点；
//   account          账号问题   —— 只是提问（登录异常/绑定问题/封禁申诉）；
//   account_closure  注销申请   —— 只是建单，真正的注销仍要管理员审批。
//
// 判据（后端 service/ticket.go 注释原文）：「用户自选后能不能触发一个管理员侧
// 的一键动作」—— 能，就不许加。因此下面三项**永久不可入表**：
//   registration_bonus（一键补发赠金）、blacklist_appeal（绕过短信验证进解封）、
//   account_rebind（一键换绑，绕过短信验证的身份变更）。
export const TICKET_CREATE_TYPE_GENERAL = "general";
export const TICKET_CREATE_TYPE_REFUND = "refund";
export const TICKET_CREATE_TYPE_ACCOUNT = "account";
export const TICKET_CREATE_TYPE_ACCOUNT_CLOSURE = "account_closure";

// 永久禁入用户可选类型的取值（红线清单，供测试与页面共同引用）。
export const TICKET_CREATE_FORBIDDEN_TYPES = Object.freeze([
  "registration_bonus",
  "blacklist_appeal",
  "account_rebind",
]);

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
  {
    value: TICKET_CREATE_TYPE_ACCOUNT,
    label: "账号问题",
    hint: "登录异常、绑定问题、封禁申诉；请选择问题类型并说明情况",
  },
  {
    value: TICKET_CREATE_TYPE_ACCOUNT_CLOSURE,
    label: "注销申请",
    hint: "注销账号。推荐先用「自助注销」，更快且可随时撤销",
  },
]);

// ──────────────── 两步向导的第一步：只选类型 ────────────────
//
// 用户诉求：列表右上角点「发起工单」→ **先选类型** → 才出对应模板。
// 因此表单里**不再有**类型下拉（那是「一步向导」的形态）。
//
// 这一步的状态机很小，但必须显式建模：如果只用一个布尔（createVisible），
// 「选完类型」和「还没选」无法区分，表单会先闪一下错误模板。

// TICKET_WIZARD_STEP_TYPE 是第一步（选类型），TICKET_WIZARD_STEP_FORM 是第二步。
export const TICKET_WIZARD_STEP_TYPE = "type";
export const TICKET_WIZARD_STEP_FORM = "form";

// ticketWizardInitialStep 返回进入向导时的第一步。
//
// 始终从「选类型」开始：用户点的是「发起工单」而不是「新建一般问题」，
// 直接落到某个模板上等于替他做了选择。
export function ticketWizardInitialStep() {
  return TICKET_WIZARD_STEP_TYPE;
}

// ticketWizardNextStep 返回选定类型后的下一步。
//
// ⚠️ 注销申请**例外**：它必须先经过引导页（T4），所以在第三步之前插入
// TICKET_WIZARD_STEP_CLOSURE_GUIDE。这里刻意让 closure 与其它类型分叉，
// 而不是在表单里加一个 if —— 分叉点在步骤上，页面据此渲染完全不同的组件。
export const TICKET_WIZARD_STEP_CLOSURE_GUIDE = "closure-guide";

export function ticketWizardNextStep(ticketType) {
  const normalized = normalizeTicketCreateType(ticketType);
  if (normalized === TICKET_CREATE_TYPE_ACCOUNT_CLOSURE) {
    return TICKET_WIZARD_STEP_CLOSURE_GUIDE;
  }
  return TICKET_WIZARD_STEP_FORM;
}

// ticketWizardCanAdvanceFromType 判断第一步能否「下一步」。
//
// 没选类型时不允许前进：否则表单会拿到空类型，落到 general 模板上，
// 用户以为自己在填「注销申请」。
export function ticketWizardCanAdvanceFromType(ticketType) {
  const normalized = normalizeTicketCreateType(ticketType);
  return TICKET_CREATE_TYPE_OPTIONS.some((item) => item.value === normalized);
}

// ticketWizardCanSubmit 判断当前步骤能否提交（判据 T4 的**行为**落点）。
//
// 只有「填模板」这一步允许提交。这条必须是可测的纯函数，而不是页面里的一句
// if —— 否则「跳过引导直接提交」这个回归只能在手工点界面时才发现。
//
// 反例（必须红）：把本函数放宽成「只要在向导里就允许提交」，那么用户在注销
// 引导页点提交（按钮不该存在，但键盘/脚本能触发）就会直接建出一张注销工单，
// 而他从未看到「推荐自助注销」。
export function ticketWizardCanSubmit(step) {
  return String(step ?? "").trim().toLowerCase() === TICKET_WIZARD_STEP_FORM;
}

// normalizeTicketCreateType 是前端侧的类型归一（与后端 normalizeUserTicketType 同规则）。
//
// 非法值返回空串而不是兜底成 general —— 与后端「明确报错而不是静默降级」一致：
// 一个不存在的类型被静默当成普通工单，是最坏的一种「成功」。
export function normalizeTicketCreateType(ticketType) {
  const normalized = String(ticketType ?? "").trim().toLowerCase();
  return TICKET_CREATE_TYPE_OPTIONS.some((item) => item.value === normalized)
    ? normalized
    : "";
}

// ticketCreateTypeNeedsRefundFields 判断选了该类型后表单是否要切换到退款字段。
//
// 这是任务书明确要求的：**不能只是换个 category 字符串**。
// 选退款必须让用户填订单/金额/理由，否则后端无法受理（会直接报错）。
export function ticketCreateTypeNeedsRefundFields(ticketType) {
  return String(ticketType || "").trim().toLowerCase() === TICKET_CREATE_TYPE_REFUND;
}

// ─────────────────── account 类型：问题类型下拉（B2-6）───────────────────
//
// 用户要求：账号问题要分「登录异常 / 绑定问题 / 封禁申诉」三类 + 说明文本域。
//
// 为什么问题类型只在前端做**标题前缀**、不作为独立后端字段：
// ticket 表没有 account_issue_type 列，硬加一列等于改迁移与全部读写路径。
// 而这一项的用途只是让管理员一眼分诊 —— 标题里带分类即可，正文是自由说明。
// ⚠️ 将来若要做成可筛选字段，必须同步加列 + 迁移 + 列表筛选，不能只改前端。
export const TICKET_ACCOUNT_ISSUE_OPTIONS = Object.freeze([
  { value: "login", label: "登录异常", titlePrefix: "【登录异常】" },
  { value: "binding", label: "绑定问题", titlePrefix: "【绑定问题】" },
  { value: "banned", label: "封禁申诉", titlePrefix: "【封禁申诉】" },
]);

export function ticketAccountIssueLabel(issueType) {
  const matched = TICKET_ACCOUNT_ISSUE_OPTIONS.find(
    (item) => item.value === String(issueType || "").trim().toLowerCase(),
  );
  return matched ? matched.label : "";
}

// ─────────────────── account_closure 类型（B2-4）───────────────────
//
// 注销工单是**兜底**：主入口是自助注销（/api/user/closure-request）。
// 引导页的主按钮必须调自助注销，次按钮才是「仍然提交工单」。
//
// 这条顺序是用户明确要求的（138 号 §2.4 用户原话）：
// 「注销工单是用于兜底的，可以引导用户使用自助注销，如果用户仍然继续，才继续。」
// ⇒ 主按钮 primary = 去自助注销；次按钮 default = 仍然提交工单。
export function isAccountClosureTicketType(ticketType) {
  return (
    String(ticketType || "").trim().toLowerCase() ===
    TICKET_CREATE_TYPE_ACCOUNT_CLOSURE
  );
}

// buildClosureTicketSubject 生成注销工单的标题。
//
// 注销工单的标题不该让用户自由填写：管理员按标题分诊时，
// 「【注销申请】」前缀比用户随手写的「我要注销一下」可读得多。
export function buildClosureTicketSubject() {
  return "【注销申请】账号注销兜底工单";
}

// ─────────────────── 表单校验（按类型分三套）───────────────────

// getAccountDraftValidation 是 account 类型的校验。
//
// 三件事必须拦：没选问题类型 / 说明为空 / 说明超长。
// 说明上限与后端 TicketBodyMaxLength 一致（10000），但账号问题不需要那么长，
// 前端收窄到 2000 只是输入体验，不作为安全边界。
export function getAccountDraftValidation({ issueType, body } = {}) {
  if (!ticketAccountIssueLabel(issueType)) {
    return Object.freeze({ field: "issueType", message: "请选择问题类型" });
  }
  const text = String(body ?? "").replace(/\r\n?/g, "\n").trim();
  if (!text) return Object.freeze({ field: "body", message: "请填写问题说明" });
  if (Array.from(text).length > 10000) {
    return Object.freeze({ field: "body", message: "问题说明不能超过 10000 个字符" });
  }
  return Object.freeze({ field: "", message: "" });
}

// getClosureDraftValidation 是 account_closure 类型的校验。
//
// 注销工单只需要正文：标题由 buildClosureTicketSubject 生成（带固定前缀）。
// 正文必填是因为「我要注销」三个字不足以让管理员判断该不该放行。
export function getClosureDraftValidation({ body } = {}) {
  const text = String(body ?? "").replace(/\r\n?/g, "\n").trim();
  if (!text) return Object.freeze({ field: "body", message: "请说明注销原因" });
  if (Array.from(text).length > 10000) {
    return Object.freeze({ field: "body", message: "注销说明不能超过 10000 个字符" });
  }
  return Object.freeze({ field: "", message: "" });
}

// getTicketCreateDraftValidation 按类型派发到对应校验（页面只调这一个）。
export function getTicketCreateDraftValidation(draft = {}) {
  const type = normalizeTicketCreateType(draft.ticketType);
  if (type === TICKET_CREATE_TYPE_REFUND) {
    return getRefundDraftValidation({
      requestedYuan: draft.requestedYuan,
      reason: draft.reason,
    });
  }
  if (type === TICKET_CREATE_TYPE_ACCOUNT) {
    return getAccountDraftValidation({ issueType: draft.issueType, body: draft.body });
  }
  if (type === TICKET_CREATE_TYPE_ACCOUNT_CLOSURE) {
    return getClosureDraftValidation({ body: draft.body });
  }
  return getTicketDraftValidation(draft.subject, draft.body);
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

  // account（账号问题）：问题类型不进后端字段，只作为标题前缀让管理员分诊
  // （tickets 表没有 issue_type 列，硬加一列要连带改迁移与全部读写路径）。
  if (ticketType === TICKET_CREATE_TYPE_ACCOUNT) {
    // 标题前缀只用**白名单里的固定文案**，不拼接用户输入的问题类型，
    // 否则用户可以自己填前缀冒充「封禁申诉」。
    const matchedIssue = TICKET_ACCOUNT_ISSUE_OPTIONS.find(
      (item) => item.value === String(draft.issueType || "").trim().toLowerCase(),
    );
    return Object.freeze({
      payload: Object.freeze({
        ticketType: TICKET_CREATE_TYPE_ACCOUNT,
        subject: normalizeTicketText(
          `${matchedIssue ? matchedIssue.titlePrefix : ""}账号问题`,
          120,
        ),
        body: normalizeTicketText(draft.body, 10000),
        ...(attachmentIds.length ? { attachmentIds } : {}),
      }),
      validation: getAccountDraftValidation({
        issueType: draft.issueType,
        body: draft.body,
      }),
    });
  }

  // account_closure（注销申请）：标题固定生成，用户只填正文。
  if (ticketType === TICKET_CREATE_TYPE_ACCOUNT_CLOSURE) {
    return Object.freeze({
      payload: Object.freeze({
        ticketType: TICKET_CREATE_TYPE_ACCOUNT_CLOSURE,
        subject: normalizeTicketText(buildClosureTicketSubject(), 120),
        body: normalizeTicketText(draft.body, 10000),
        ...(attachmentIds.length ? { attachmentIds } : {}),
      }),
      validation: getClosureDraftValidation({ body: draft.body }),
    });
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