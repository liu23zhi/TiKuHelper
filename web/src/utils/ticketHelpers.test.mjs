import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import {
  buildTicketListParams,
  normalizeTicketDetailResponse,
  normalizeTicketListResponse,
  requireTicketResponse,
  ticketStatusColor,
  ticketStatusLabel,
  validateTicketDraft,
} from "./ticketHelpers.mjs";
import * as ticketHelpers from "./ticketHelpers.mjs";

const require = createRequire(import.meta.url);
const Vue = require("vue");
const { parse } = require("@vue/compiler-sfc");

async function loadTicketApi(request) {
  const source = (
    await readFile(new URL("../api/TicketApi.js", import.meta.url), "utf8")
  )
    .replace(/import[\s\S]*?from ["'][^"']+["'];?\r?\n/g, "")
    .replace(/export function/g, "function");
  return new Function(
    "request",
    "requireTicketResponse",
    `${source}\nreturn {createTicket, addTicketMessage, updateTicket, testTicketNotification};`
  )(request, requireTicketResponse);
}

function deferred() {
  let resolve;
  const promise = new Promise((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

async function loadTicketPage(overrides = {}) {
  const source = await readFile(
    new URL("../pages/Tickets/index.vue", import.meta.url),
    "utf8"
  );
  const script = parse(source).descriptor.scriptSetup.content.replace(
    /import[\s\S]*?from ["'][^"']+["'];?\r?\n/g,
    ""
  );
  const route = Vue.reactive({ params: {} });
  const dependencies = {
    ...Vue,
    ...ticketHelpers,
    defineOptions: () => {},
    onMounted: () => {},
    onBeforeUnmount: () => {},
    useRoute: () => route,
    useRouter: () => ({ push() {}, replace() {} }),
    getStoredCurrentUser: () => ({ role: "admin" }),
    isAdminUser: () => true,
    message: { success() {}, error() {}, warning() {} },
    getTickets: async () => ({ data: { data: { items: [] } } }),
    getTicket: async (id) => ({
      data: { data: { ticket: { id, status: "open" }, messages: [] } },
    }),
    addTicketMessage: async () => ({}),
    updateTicket: async () => ({}),
    createTicket: async () => ({}),
    getTicketNotificationConfig: async () => ({}),
    retryTicketNotification: async () => ({}),
    saveTicketNotificationConfig: async () => ({}),
    testTicketNotification: async () => ({}),
    ...overrides,
  };
  const scope = Vue.effectScope();
  const state = scope.run(() =>
    new Function(
      ...Object.keys(dependencies),
      `${script}\nreturn {loadDetail, loadTickets, submitReply, saveStatus, detailTicket, detailStatus, messages, replyBody, tickets};`
    )(...Object.values(dependencies))
  );
  return { ...state, close: () => scope.stop() };
}

test("ticket API rejects resolved HTTP failures instead of reporting mutation success", async () => {
  for (const status of [400, 500]) {
    const api = await loadTicketApi(async () => ({
      status,
      message: "服务端失败原因",
    }));
    await assert.rejects(api.createTicket({}), /服务端失败原因/);
    await assert.rejects(api.addTicketMessage(1, {}), /服务端失败原因/);
    await assert.rejects(api.updateTicket(1, {}), /服务端失败原因/);
    await assert.rejects(api.testTicketNotification(), /服务端失败原因/);
  }
});

test("late ticket detail and reply responses cannot replace the newly opened ticket", async () => {
  const firstDetail = deferred();
  const pendingReply = deferred();
  const page = await loadTicketPage({
    getTicket: async (id) =>
      id === 1
        ? firstDetail.promise
        : {
            data: {
              data: {
                ticket: { id, status: "open" },
                messages: [{ id: 20, body: "工单二" }],
              },
            },
          },
    addTicketMessage: async () => pendingReply.promise,
  });
  try {
    const first = page.loadDetail(1);
    await page.loadDetail(2);
    firstDetail.resolve({
      data: { data: { ticket: { id: 1 }, messages: [] } },
    });
    await first;
    assert.equal(page.detailTicket.value.id, 2);
    page.replyBody.value = "回复二";
    const reply = page.submitReply();
    await page.loadDetail(3);
    pendingReply.resolve({
      data: {
        data: {
          ticket: { id: 2, status: "pending" },
          messages: [{ id: 21, body: "回复二" }],
        },
      },
    });
    await reply;
    assert.equal(page.detailTicket.value.id, 3);
    assert.equal(page.messages.value[0].body, "工单二");
  } finally {
    page.close();
  }
});

test("direct ticket routes open detail and stale lists do not overwrite a newer filter response", async () => {
  const first = deferred();
  let calls = 0;
  const page = await loadTicketPage({
    useRoute: () => Vue.reactive({ params: { id: "12" } }),
    getTickets: async () =>
      ++calls === 1
        ? first.promise
        : { data: { data: { items: [{ id: 2 }], total: 1 } } },
  });
  try {
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(page.detailTicket.value.id, "12");
    const older = page.loadTickets();
    await page.loadTickets();
    first.resolve({ data: { data: { items: [{ id: 1 }], total: 1 } } });
    await older;
    assert.deepEqual(
      page.tickets.value.map((ticket) => ticket.id),
      [2]
    );
  } finally {
    page.close();
  }
});

test("late administrator status response does not change another ticket", async () => {
  const pending = deferred();
  const page = await loadTicketPage({
    updateTicket: async () => pending.promise,
  });
  try {
    await page.loadDetail(1);
    page.detailStatus.value = "resolved";
    const saving = page.saveStatus();
    await page.loadDetail(2);
    pending.resolve({
      data: { data: { ticket: { id: 1, status: "resolved" }, messages: [] } },
    });
    await saving;
    assert.equal(page.detailTicket.value.id, 2);
    assert.equal(page.detailStatus.value, "open");
  } finally {
    page.close();
  }
});

test("ticket helpers normalize statuses, drafts and paged responses", () => {
  assert.equal(requireTicketResponse({ status: 200 }).status, 200);
  for (const status of [400, 403, 404, 429, 500]) {
    assert.throws(
      () => requireTicketResponse({ status, message: "服务端返回的原因" }),
      /服务端返回的原因/
    );
  }
  assert.equal(ticketStatusLabel("open"), "待处理");
  assert.equal(ticketStatusColor("resolved"), "green");
  assert.equal(validateTicketDraft("标题", "正文"), "");
  assert.match(validateTicketDraft("", "正文"), /标题/);
  assert.match(validateTicketDraft("标题", ""), /问题描述/);
  assert.match(validateTicketDraft("a".repeat(121), "正文"), /120/);
  assert.deepEqual(
    buildTicketListParams({ keyword: "  登录  ", status: "open" }, 2, 200),
    { page: 2, pageSize: 100, keyword: "登录", status: "open" }
  );
  assert.deepEqual(
    normalizeTicketListResponse({
      data: { data: { items: [{ id: 1 }], total: 3, page: 1, pageSize: 20 } },
    }),
    { items: [{ id: 1 }], total: 3, page: 1, pageSize: 20 }
  );
  assert.deepEqual(
    normalizeTicketDetailResponse({
      data: { data: { ticket: { id: 1 }, messages: [] } },
    }).ticket,
    { id: 1 }
  );
});

test("ticket page is wired for users and administrators with pre-wrapped text", async () => {
  const [router, menu, page] = await Promise.all([
    readFile(new URL("../router/router.js", import.meta.url), "utf8"),
    readFile(
      new URL("../components/SiderMenu/index.vue", import.meta.url),
      "utf8"
    ),
    readFile(new URL("../pages/Tickets/index.vue", import.meta.url), "utf8"),
  ]);
  assert.match(router, /path: 'tickets\/:id\?'[\s\S]*name: 'tickets'/);
  assert.match(menu, /key: 'tickets', label: '工单管理'/);
  assert.match(menu, /key: 'tickets', label: '工单反馈'/);
  assert.match(page, /white-space: pre-wrap/);
  assert.match(page, /getTicketNotificationConfig/);
  assert.match(page, /testTicketNotification/);
  assert.match(page, /route\.params\.id/);
  assert.match(page, /mobile-ticket-pagination/);
});
