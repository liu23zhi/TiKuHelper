import request from "../utils/request";
import { requireTicketResponse } from "../utils/ticketHelpers.mjs";

async function ticketRequest(config) {
  return requireTicketResponse(await request(config));
}

export function getTickets(params = {}) {
  return ticketRequest({
    url: "/adapter-service/tickets",
    method: "get",
    params,
  });
}

export function createTicket(data) {
  return ticketRequest({
    url: "/adapter-service/tickets",
    method: "post",
    data,
  });
}

export function getTicket(id) {
  return ticketRequest({
    url: `/adapter-service/tickets/${encodeURIComponent(id)}`,
    method: "get",
  });
}

export function addTicketMessage(id, data) {
  return ticketRequest({
    url: `/adapter-service/tickets/${encodeURIComponent(id)}/messages`,
    method: "post",
    data,
  });
}

export function updateTicket(id, data) {
  return ticketRequest({
    url: `/adapter-service/tickets/${encodeURIComponent(id)}`,
    method: "put",
    data,
  });
}

export function testTicketNotification() {
  return ticketRequest({
    url: "/adapter-service/tickets/config/test",
    method: "post",
    timeout: 30_000,
  });
}

export function getTicketNotificationConfig() {
  return ticketRequest({
    url: "/adapter-service/tickets/config",
    method: "get",
  });
}

export function saveTicketNotificationConfig(data) {
  return ticketRequest({
    url: "/adapter-service/tickets/config",
    method: "put",
    data,
  });
}

export function retryTicketNotification(ticketId, notificationId) {
  return ticketRequest({
    url: `/adapter-service/tickets/${encodeURIComponent(
      ticketId
    )}/notifications/${encodeURIComponent(notificationId)}/retry`,
    method: "post",
  });
}
