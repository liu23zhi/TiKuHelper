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
import { isAdminUser, getCurrentUserLogoutEpoch } from "./currentUser.js";
import { ensureRequestFailure } from "./requestFailureView.mjs";

const require = createRequire(import.meta.url);
const Vue = require("vue");
const { parse } = require("@vue/compiler-sfc");
const { compile } = require("@vue/compiler-dom");
const { Form } = require("ant-design-vue");

async function loadTicketApi(request) {
  const source = (
    await readFile(new URL("../api/TicketApi.js", import.meta.url), "utf8")
  )
    .replace(/import[\s\S]*?from ["'][^"']+["'];?\r?\n/g, "")
    .replace(/export function/g, "function");
  return new Function(
    "request",
    "requireTicketResponse",
    `${source}\nreturn {createTicket, addTicketMessage, updateTicket, testTicketNotification, getTicketNotificationConfig, saveTicketNotificationConfig};`
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
  const { fixtureActor = { id: 88001, role: "admin", status: "enabled" }, ...apiOverrides } = overrides;
  const source = await readFile(
    new URL("../pages/Tickets/index.vue", import.meta.url),
    "utf8"
  );
  const script = parse(source).descriptor.scriptSetup.content.replace(
    /import[\s\S]*?from ["'][^"']+["'];?\r?\n/g,
    ""
  );
  const route = Vue.reactive({ params: {} });
  const currentUser = Vue.shallowRef({ ...fixtureActor });
  const unmount = [];
  // Business-unit constructor only: real scope source/capture predicates with
  // explicit synthetic identity. Native owner/logout qualification is separate.
  const ownerSource = (await readFile(new URL("./currentUserScope.js", import.meta.url), "utf8"))
    .replace(/^import .*$/gm, "").replace(/export function/g, "function");
  const useCurrentUserRequestScope = new Function("watch", "onMounted", "onBeforeUnmount", "isAdminUser", "getCurrentUserLogoutEpoch", `${ownerSource}\nreturn useCurrentUserRequestScope;`)(Vue.watch, () => {}, callback => unmount.push(callback), isAdminUser, getCurrentUserLogoutEpoch);
  const dependencies = {
    ...Vue,
    ...ticketHelpers,
    ensureRequestFailure,
    defineOptions: () => {},
    onMounted: () => {},
    onBeforeUnmount: callback => unmount.push(callback),
    useRoute: () => route,
    useRouter: () => ({ push() {}, replace() {} }),
    useCurrentUserDisplay: () => ({ currentUser }),
    useCurrentUserRequestScope,
    isAdminUser,
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
    ...apiOverrides,
  };
  const scope = Vue.effectScope();
  const state = scope.run(() =>
    new Function(
      ...Object.keys(dependencies),
      `${script}\nreturn {loadDetail, loadTickets, submitReply, saveStatus, detailTicket, detailStatus, messages, replyBody, tickets, notificationConfig, configSaving, configTesting, saveNotificationConfig, testNotification, loadNotificationConfig, pageEntry, configLoaded, canManage};`
    )(...Object.values(dependencies))
  );
  return { ...state, close: () => { unmount.forEach(callback => callback()); scope.stop(); } };
}

async function mountNotificationForm(page) {
  const source = await readFile(
    new URL("../pages/Tickets/index.vue", import.meta.url),
    "utf8"
  );
  const findForm = (node) => {
    if (
      node.tag === "a-form" &&
      node.props.some((prop) => prop.exp?.content === "saveNotificationConfig" || prop.exp?.content === "pageEntry.bind(saveNotificationConfig)")
    )
      return node;
    return node.children?.map(findForm).find(Boolean);
  };
  const form = findForm(parse(source).descriptor.template.ast);
  assert.ok(form, "notification settings form exists");
  assert.ok(form.props.some(prop => prop.arg?.content === "model" && prop.exp?.content === "notificationConfig"), "real AntForm has its actual notificationConfig model");
  const render = new Function(
    "Vue",
    compile(form.loc.source, { mode: "function", prefixIdentifiers: true }).code
  )(Vue);
  const node = (type, text = "") => ({
    type,
    text,
    children: [],
    props: {},
    parent: null,
  });
  const renderer = Vue.createRenderer({
    createElement: (type) => node(type),
    createText: (text) => node("#text", text),
    createComment: (text) => node("#comment", text),
    setText: (target, text) => (target.text = text),
    setElementText: (target, text) => (target.text = text),
    parentNode: (target) => target.parent,
    nextSibling: (target) =>
      target.parent?.children[target.parent.children.indexOf(target) + 1],
    insert: (target, parent, anchor = null) => {
      target.parent = parent;
      const index = parent.children.indexOf(anchor);
      parent.children.splice(index < 0 ? parent.children.length : index, 0, target);
    },
    remove: (target) => {
      const index = target.parent?.children.indexOf(target);
      if (index >= 0) target.parent.children.splice(index, 1);
    },
    patchProp: (target, key, previous, next) => (target.props[key] = next),
  });
  const app = renderer.createApp({ setup: () => page, render });
  // Keep the actual form's validation and submit handling; other controls need no DOM.
  app.component("a-form", Form);
  for (const tag of ["a-form-item", "a-switch", "a-input", "a-button", "a-space"])
    app.component(tag, {
      setup: (_, { slots }) => () => Vue.h("div", {}, slots.default?.()),
    });
  const container = node("root");
  app.mount(container);
  const nativeForm = container.children.find((child) => child.type === "form");
  return {
    async submit() {
      nativeForm.props.onSubmit({ preventDefault() {}, stopPropagation() {} });
      await new Promise((resolve) => setImmediate(resolve));
    },
    text() {
      const collect = target => [target.text, ...target.children.map(collect)].join(" ");
      return collect(container);
    },
    close: () => app.unmount(),
  };
}

test("notification form submits settings and reloads the saved configuration", async () => {
  const saved = [];
  const successes = [];
  let reloads = 0;
  const page = await loadTicketPage({
    saveTicketNotificationConfig: async (payload) => saved.push(payload),
    getTicketNotificationConfig: async () => {
      reloads++;
      return {
        data: {
          data: { enabled: true, configured: true, webhookUrl: "", preview: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=***", mentionedList: ["alice", "bob"] },
        },
      };
    },
    message: { success: (value) => successes.push(value), error() {} },
  });
  const form = await mountNotificationForm(page);
  try {
    await page.loadNotificationConfig();
    const initialReads = reloads;
    assert.equal(page.notificationConfig.webhookUrl, "", "protected read never prefills a saved secret or masked preview");
    assert.match(form.text(), /已配置企业微信机器人/, "configured state uses a safe public status preview");
    assert.doesNotMatch(form.text(), /qyapi\.weixin\.qq\.com|key=test|key=\*\*\*/, "neither raw address nor masked URL becomes visible form text");
    Object.assign(page.notificationConfig, {
      enabled: true,
      webhookUrl: " https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test ",
      mentionedList: "alice, bob",
    });
    await form.submit();
    assert.deepEqual(saved, [
      {
        enabled: true,
        clearWebhook: false,
        mentionedList: ["alice", "bob"],
        webhookUrl: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test",
      },
    ]);
    assert.equal(reloads - initialReads, 1, "one actual saved-config readback after the required initial GET");
    assert.equal(successes.length, 1);
    assert.equal(page.notificationConfig.webhookUrl, "", "successful save/readback clears the entered secret, preserving blank-retain policy");
    assert.match(form.text(), /已配置企业微信机器人/);
    assert.doesNotMatch(form.text(), /key=test/);
    assert.equal(page.notificationConfig.configured, true);
    assert.equal(page.configSaving.value, false);
  } finally {
    form.close();
    page.close();
  }
});

test("notification form keeps its draft and reports a failed save", async () => {
  const failures = [];
  const successes = [];
  let reloads = 0;
  const page = await loadTicketPage({
    saveTicketNotificationConfig: async () => {
      throw new Error("保存失败，请稍后重试");
    },
    getTicketNotificationConfig: async () => { reloads++; return { data: { data: { enabled: false, configured: false, mentionedList: [] } } }; },
    message: {
      success: (value) => successes.push(value),
      error: (value) => failures.push(value),
    },
  });
  const form = await mountNotificationForm(page);
  try {
    await page.loadNotificationConfig();
    const initialReads = reloads;
    Object.assign(page.notificationConfig, {
      enabled: true,
      webhookUrl: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=draft",
      mentionedList: "alice,bob",
    });
    const draft = { ...page.notificationConfig };
    await form.submit();
    assert.deepEqual({ ...page.notificationConfig }, draft);
    assert.deepEqual(failures, ["企业微信提醒设置保存失败，请核对配置后重试。"]);
    assert.equal(successes.length, 0);
    assert.equal(reloads - initialReads, 0, "failed save never performs a success readback");
    assert.equal(page.configSaving.value, false);
  } finally {
    form.close();
    page.close();
  }
});

test("notification form can retain or explicitly clear an existing webhook", async () => {
  const saved = [];
  const page = await loadTicketPage({
    saveTicketNotificationConfig: async (payload) => saved.push(payload),
    getTicketNotificationConfig: async () => ({
      data: {
        data: { enabled: false, configured: saved.length < 2, mentionedList: [] },
      },
    }),
  });
  const form = await mountNotificationForm(page);
  try {
    await page.loadNotificationConfig();
    assert.equal(page.notificationConfig.configured, true, "initial protected GET confirms an existing secret before retain/clear");
    assert.equal(page.notificationConfig.webhookUrl, "", "blank input is the actual retain control");
    page.notificationConfig.configured = true;
    await form.submit();
    page.notificationConfig.clearWebhook = true;
    await form.submit();
    assert.deepEqual(saved, [
      { enabled: false, clearWebhook: false, mentionedList: [] },
      { enabled: false, clearWebhook: true, mentionedList: [] },
    ]);
    assert.equal(page.notificationConfig.configured, false);
    assert.equal(page.notificationConfig.clearWebhook, false);
  } finally {
    form.close();
    page.close();
  }
});

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
    await assert.rejects(api.getTicketNotificationConfig(), /服务端失败原因/);
    await assert.rejects(api.saveTicketNotificationConfig({}), /服务端失败原因/);
  }
});

test("notification settings API uses the protected config PUT endpoint", async () => {
  const requests = [];
  const api = await loadTicketApi(async (config) => {
    requests.push(config);
    return { status: 200, data: { data: { configured: true } } };
  });
  const payload = { enabled: true, mentionedList: ["alice"] };
  await api.saveTicketNotificationConfig(payload);
  assert.deepEqual(requests, [
    { url: "/adapter-service/tickets/config", method: "put", data: payload },
  ]);
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
