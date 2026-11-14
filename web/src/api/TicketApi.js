import request from "../utils/request";
import { requireTicketResponse } from "../utils/ticketHelpers.mjs";

async function ticketRequest(config) {
  return requireTicketResponse(await request(config));
}

export function getTickets(params = {}) {
  return ticketRequest({
    url: "/api/tickets",
    method: "get",
    params,
  });
}

export function createTicket(data) {
  return ticketRequest({
    url: "/api/tickets",
    method: "post",
    data,
  });
}

export function getTicket(id) {
  return ticketRequest({
    url: `/api/tickets/${encodeURIComponent(id)}`,
    method: "get",
  });
}

export function addTicketMessage(id, data) {
  return ticketRequest({
    url: `/api/tickets/${encodeURIComponent(id)}/messages`,
    method: "post",
    data,
  });
}

export function updateTicket(id, data) {
  return ticketRequest({
    url: `/api/tickets/${encodeURIComponent(id)}`,
    method: "put",
    data,
  });
}

export function testTicketNotification() {
  return ticketRequest({
    url: "/api/tickets/config/test",
    method: "post",
    timeout: 30_000,
  });
}

export function getTicketNotificationConfig() {
  return ticketRequest({
    url: "/api/tickets/config",
    method: "get",
  });
}

export function saveTicketNotificationConfig(data) {
  return ticketRequest({
    url: "/api/tickets/config",
    method: "put",
    data,
  });
}

export function retryTicketNotification(ticketId, notificationId) {
  return ticketRequest({
    url: `/api/tickets/${encodeURIComponent(
      ticketId
    )}/notifications/${encodeURIComponent(notificationId)}/retry`,
    method: "post",
  });
}

// ---- 工单图片附件 ----
//
// 时序：先上传（本地校验 + 加密落库 + pending），再异步检测。
// 上传成功立刻拿到附件 ID；建单/回复时把 ID 列表带上完成绑定。

export function uploadTicketAttachment(file, ticketId) {
  const form = new FormData();
  form.append("file", file);
  const query = ticketId
    ? `?ticketId=${encodeURIComponent(ticketId)}`
    : "";
  return ticketRequest({
    url: `/api/ticket-attachments${query}`,
    method: "post",
    data: form,
    timeout: 60_000,
  });
}

export function listTicketAttachments(ticketId) {
  return ticketRequest({
    url: `/api/tickets/${encodeURIComponent(ticketId)}/attachments`,
    method: "get",
  });
}

// 用户端读图：敏感图会返回 403 + code=TICKET_ATTACHMENT_HIDDEN（拿不到字节）。
// 调用方**只在 visible=true 时**才调用它。
export function getTicketAttachment(attachmentId) {
  return ticketRequest({
    url: `/api/ticket-attachments/${encodeURIComponent(attachmentId)}`,
    method: "get",
    responseType: "blob",
    timeout: 20_000,
  });
}

// 管理端读图：可拉敏感图，服务端会写审计。
export function getTicketAttachmentAsAdmin(attachmentId) {
  return ticketRequest({
    url: `/api/ticket-attachments/${encodeURIComponent(
      attachmentId
    )}/admin`,
    method: "get",
    responseType: "blob",
    timeout: 20_000,
  });
}
