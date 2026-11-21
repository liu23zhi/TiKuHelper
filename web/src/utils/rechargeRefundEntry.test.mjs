// 充值记录页的**职责边界**契约判据（源码级，零依赖）。
//
// 2026-11-19 用户定案：
//   充值记录 = **管理员专属的运营视图**，只做「改状态」，不提供退款入口；
//   普通用户看自己的入账在「个人使用 → 账单流水」。
//
// 这组用例钉的是「边界」而不是「实现细节」，因为边界错了的后果是：
//   · 普通用户在侧边栏看到运营页（越权可见）；
//   · 只从菜单删掉但没登记 ADMIN_ONLY_ROUTE_NAMES → 直接敲 URL 仍能进；
//   · 两个地方各放一个退款入口 → 两条口径不同的退款路径。
//
// 断言方式说明：这里读源码做结构断言，是因为这些边界**没有可断言的行为出口**
// （菜单渲染要跑整页、路由守卫要跑 router）。为防假钉子，每条用例都同时钉
// 「应当存在」与「不应当存在」两面，只钉一面的断言在重命名后会静默通过。

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const repoWeb = path.join(here, '..', '..');
const pagePath = path.join(repoWeb, 'src', 'pages', 'RechargeOrderList', 'index.vue');
const siderPath = path.join(repoWeb, 'src', 'components', 'SiderMenu', 'index.vue');
const routerPath = path.join(repoWeb, 'src', 'router', 'router.js');
const apiPath = path.join(repoWeb, 'src', 'api', 'TikuComponentApi.js');

const read = (p) => readFile(p, 'utf8');

test('① 充值记录不再有退款入口（退款只走工单反馈）', async () => {
  const source = await read(pagePath);
  // 应当存在：状态修改入口
  assert.match(source, /openStatusModal/, '必须有「修改状态」入口');
  // 不应存在：任何退款入口残留
  for (const banned of [
    'getEligibleTicketRefundOrders',
    'createTicketRefund',
    'openRefundModal',
    'submitRefund',
    'canRefundRow',
  ]) {
    assert.ok(
      !source.includes(banned),
      `充值记录不得残留退款逻辑 ${banned} —— 退款申请统一走「工单反馈 → 退款申请」，` +
        '两处并存会产生两条口径不同的退款路径',
    );
  }
});

test('② 改状态只调 updateRechargeOrderStatus，不在前端复刻合法迁移', async () => {
  const page = await read(pagePath);
  const api = await read(apiPath);

  // API 侧：必须存在 PATCH /recharge-orders/:id/status
  assert.match(api, /updateRechargeOrderStatus/, 'API 必须提供 updateRechargeOrderStatus');
  assert.match(
    api,
    /recharge-orders\/\$\{[^}]+\}\/status/,
    '请求路径必须是 /api/recharge-orders/:id/status',
  );
  assert.match(api, /method:\s*['"]patch['"]/, '改状态必须是 PATCH');

  // 页面侧：必须调用它
  assert.match(page, /updateRechargeOrderStatus\(/, '页面必须调用 updateRechargeOrderStatus');

  // 反面判据：不得在前端判断「哪些迁移合法」。
  // 合法迁移由后端 allowedRechargeStatusTransition 决定（refunded→paid 被拒），
  // 前端复刻一份必然漂移，漂移方向是「按钮能点但请求 400」。
  assert.doesNotMatch(
    page,
    /next\s*===\s*['"]paid['"][\s\S]{0,80}current\s*===\s*['"]refunded['"]/,
    '前端不得自行校验状态迁移合法性',
  );
});

test('③ 状态白名单三值，且筛选与弹窗共用同一份', async () => {
  const page = await read(pagePath);
  // 白名单必须覆盖 refunded —— 否则「已退款」的订单既筛不出来也改不回去看
  for (const value of ['pending', 'paid', 'refunded']) {
    assert.ok(
      page.includes(`value: '${value}'`) || page.includes(`value: "${value}"`),
      `状态白名单必须包含 ${value}`,
    );
  }
  // 共用判据：statusOptions 直接引用 STATUS_OPTIONS，不得另起一份字面量数组。
  // 两份字面量的漂移后果是「已退款的订单筛不出来」（历史上就是这样漏的）。
  assert.match(
    page,
    /statusOptions:\s*STATUS_OPTIONS/,
    '筛选下拉必须复用 STATUS_OPTIONS，不得另写一份（两份必然漂移）',
  );
});

test('④ 充值记录在普通用户菜单里不存在，且已登记管理员专属', async () => {
  const sider = await read(siderPath);
  const router = await read(routerPath);

  // 菜单：普通用户那份里不得出现 recharge-orders
  const userMenuStart = sider.indexOf('const USER_MENU_ITEMS');
  assert.ok(userMenuStart > 0, '必须能找到 USER_MENU_ITEMS');
  const userMenu = sider.slice(userMenuStart);
  assert.ok(
    !userMenu.includes("key: 'recharge-orders'"),
    '普通用户菜单不得出现「充值记录」—— 它是管理员运营视图',
  );
  // 管理员那份里必须仍在
  const adminMenuStart = sider.indexOf('const ADMIN_MENU_ITEMS');
  assert.ok(adminMenuStart > 0, '必须能找到 ADMIN_MENU_ITEMS');
  assert.ok(
    sider.slice(adminMenuStart, userMenuStart).includes("key: 'recharge-orders'"),
    '管理员菜单必须保留「充值记录」',
  );

  // 路由守卫：只从菜单删掉不够，必须登记进管理员专属，
  // 否则普通用户直接敲 URL 仍能打开（菜单只是不显示入口）。
  const adminOnlyStart = router.indexOf('const ADMIN_ONLY_ROUTE_NAMES');
  assert.ok(adminOnlyStart > 0, '必须能找到 ADMIN_ONLY_ROUTE_NAMES');
  const adminOnly = router.slice(adminOnlyStart, adminOnlyStart + 2000);
  assert.ok(
    adminOnly.includes("'recharge-orders'"),
    'recharge-orders 必须登记进 ADMIN_ONLY_ROUTE_NAMES —— 否则直接敲 URL 仍能进',
  );
});