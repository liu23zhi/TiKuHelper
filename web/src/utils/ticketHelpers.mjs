import { inheritTransportRequestId } from "./requestHelpers.mjs";
import { inheritRequestFailureView } from "./requestFailureView.mjs";

export const TICKET_STATUS_OPTIONS = Object.freeze([
  { value: "open", label: "待处理", color: "blue" },
  { value: "pending", label: "等待回复", color: "gold" },
  { value: "resolved", label: "已解决", color: "green" },
  { value: "closed", label: "已关闭", color: "default" },
]);

export const TICKET_STATUS_MAP = Object.freeze(
  Object.fromEntries(TICKET_STATUS_OPTIONS.map((item) => [item.value, item]))
);

export function ticketStatusLabel(value) {
  return TICKET_STATUS_MAP[String(value || "").trim()]?.label || "未知状态";
}
export function ticketStatusColor(value) {
  return TICKET_STATUS_MAP[String(value || "").trim()]?.color || "default";
}

export function requireTicketResponse(response) {
  const status = Number(response?.status);
  if (!Number.isFinite(status) || status < 200 || status >= 300) {
    const error = new Error(
      String(response?.message || response?.data?.message || "工单请求失败")
    );
    inheritTransportRequestId(error, response);
    inheritRequestFailureView(error, response);
    throw error;
  }
  return response;
}

export function normalizeTicketListResponse(response) {
  const payload = response?.data?.data || response?.data || {};
  const items = Array.isArray(payload?.items) ? payload.items : [];
  const total = Number(payload?.total);
  const page = Number(payload?.page);
  const pageSize = Number(payload?.pageSize);
  return {
    items,
    total: Number.isFinite(total) && total >= 0 ? total : items.length,
    page: Number.isInteger(page) && page > 0 ? page : 1,
    pageSize: Number.isInteger(pageSize) && pageSize > 0 ? pageSize : 20,
  };
}

export function normalizeTicketDetailResponse(response) {
  const payload = response?.data?.data || response?.data || {};
  const ticket = payload?.ticket || payload;
  return {
    ticket: ticket && typeof ticket === "object" ? ticket : null,
    messages: Array.isArray(payload?.messages) ? payload.messages : [],
    // 附件可能是占位条目（visible=false），前端只按 visible 决定是否拉图。
    attachments: Array.isArray(payload?.attachments) ? payload.attachments : [],
    notification: payload?.notification || null,
  };
}

export function normalizeTicketText(value, maxLength = 10000) {
  const text = String(value ?? "")
    .replace(/\r\n?/g, "\n")
    .trim();
  return Array.from(text).slice(0, maxLength).join("");
}

export function getTicketDraftValidation(subject, body) {
  const normalizedSubject = String(subject ?? "")
    .replace(/\r\n?/g, "\n")
    .trim();
  const normalizedBody = String(body ?? "")
    .replace(/\r\n?/g, "\n")
    .trim();
  if (!normalizedSubject) return Object.freeze({ field: "subject", message: "请输入工单标题" });
  if (Array.from(normalizedSubject).length > 120)
    return Object.freeze({ field: "subject", message: "工单标题不能超过 120 个字符" });
  if (!normalizedBody) return Object.freeze({ field: "body", message: "请输入问题描述" });
  if (Array.from(normalizedBody).length > 10000)
    return Object.freeze({ field: "body", message: "问题描述不能超过 10000 个字符" });
  return Object.freeze({ field: "", message: "" });
}

export function validateTicketDraft(subject, body) {
  return getTicketDraftValidation(subject, body).message;
}

export function buildTicketListParams(filters, page, pageSize) {
  const params = {
    page: Number(page) > 0 ? Number(page) : 1,
    pageSize: Number(pageSize) > 0 ? Math.min(Number(pageSize), 100) : 20,
  };
  const keyword = normalizeTicketText(filters?.keyword, 120);
  if (keyword) params.keyword = keyword;
  const status = String(filters?.status || "").trim();
  if (status) params.status = status;
  for (const key of ["createdFrom", "createdTo", "updatedFrom", "updatedTo"]) {
    const value = String(filters?.[key] || "").trim();
    if (value) params[key] = value;
  }
  return params;
}

export function formatTicketTime(value) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  }).format(date);
}

export function notificationStatusText(notification) {
  const status = String(notification?.status || "")
    .trim()
    .toLowerCase();
  if (status === "sent") return "企业微信已发送";
  if (status === "pending" || status === "sending") return "企业微信提醒排队中";
  if (status === "failed")
    return `企业微信提醒失败${
      notification?.lastError ? `：${notification.lastError}` : ""
    }`;
  if (status === "skipped") return "企业微信提醒未启用";
  return "";
}
