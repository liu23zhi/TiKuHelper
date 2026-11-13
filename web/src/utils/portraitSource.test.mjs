// 用户画像（整体 + 单用户两个入口）的行为契约 —— T-17 / task_doc/98。
//
// 本文件**零依赖**：不 import vue / echarts / ant-design / jsdom。
// 因此它在完全没有 node_modules 的环境里也能 `node --test` 跑通（本 worktree 的
// web/node_modules 就是空的）。
//
// 四条判据的落点：
//   ② 空数据语义  → 「空数组/null 不得补零」组
//   ④ 口径一致    → 「同一套函数」组（两种视角走同一条代码路径）
//   ① 数据可追溯  → 「每个数字来自后端字段」组（前端不做二次聚合）
//   ③ 权限        → 第 ④ 层（前端可见性）在源码断言组，注意它**不是**安全边界

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  PORTRAIT_EMPTY_HINT,
  PORTRAIT_SCOPE_USER,
  buildPortraitCharts,
  buildPortraitSeries,
  buildPortraitTiles,
  buildPortraitUserOption,
  isPortraitValueMissing,
  normalizePortraitUserId,
  portraitScopeHint,
  readPortraitScalar,
} from './portraitCharts.mjs';

const testDir = path.dirname(fileURLToPath(import.meta.url));
const pageSourcePath = path.join(testDir, '..', 'pages', 'UserPortrait', 'index.vue');
const apiSourcePath = path.join(testDir, '..', 'api', 'UserPortraitApi.js');
const routerSourcePath = path.join(testDir, '..', 'router', 'router.js');
const menuSourcePath = path.join(testDir, '..', 'components', 'SiderMenu', 'index.vue');

// ---------------------------------------------------------------------------
// 组 1：判据 ② —— 空数据是「没有」，不是「0」
// ---------------------------------------------------------------------------

test('画像图表：空数组必须判空，且不得补零', () => {
  const charts = buildPortraitCharts({
    hours: [],
    weekdays: [],
    regions: [],
    billingTypes: [],
  });

  for (const key of ['hours', 'weekdays', 'regions', 'billingTypes']) {
    assert.equal(charts[key].empty, true, `${key} 空数组必须 empty=true`);
    assert.deepEqual(charts[key].labels, [], `${key} 空数组不得补出任何标签`);
    assert.deepEqual(charts[key].values, [], `${key} 空数组不得补出任何数值`);
    // 关键：24 个 0 桶会让饼图/柱图画出「凌晨 0 点 0 次」这类从未发生过的观测。
    assert.notEqual(charts[key].values.length, 24, `${key} 不得补齐成 24 小时`);
    assert.notEqual(charts[key].values.length, 7, `${key} 不得补齐成 7 天`);
  }
});

test('画像图表：null 与 0 必须区分（0 是数据，null 是缺失）', () => {
  assert.equal(isPortraitValueMissing(null), true);
  assert.equal(isPortraitValueMissing(undefined), true);
  // 0 是真实观测值，绝不能当缺失 —— 否则「有账单但金额 0」会显示成「暂无数据」。
  assert.equal(isPortraitValueMissing(0), false);
  assert.equal(isPortraitValueMissing('0'), false);
  assert.equal(isPortraitValueMissing(''), false);

  assert.equal(readPortraitScalar(null), null);
  assert.equal(readPortraitScalar(undefined), null);
  assert.equal(readPortraitScalar(0), 0);
  // 非数值不能静默变成 0（那会把上游字段改名伪装成「数据是 0」）。
  assert.equal(readPortraitScalar('not-a-number'), null);
  assert.equal(readPortraitScalar(NaN), null);
});

test('画像指标卡：缺失显示 missing，不显示 0', () => {
  const tiles = buildPortraitTiles({
    totals: {
      userCount: 12,
      recordCount: null,
      amount: null,
      consumptionCount: null,
      ticketCount: null,
      activeUserCount: null,
    },
  });

  const byKey = Object.fromEntries(tiles.map(tile => [tile.key, tile]));
  assert.equal(byKey.userCount.missing, false);
  assert.equal(byKey.userCount.value, 12);
  for (const key of ['recordCount', 'amount', 'consumptionCount', 'ticketCount', 'activeUserCount']) {
    assert.equal(byKey[key].missing, true, `${key} 缺失时必须是 missing`);
    assert.equal(byKey[key].value, null, `${key} 缺失时不能是 0`);
  }
});

test('画像指标卡：0 值不被当成缺失', () => {
  const tiles = buildPortraitTiles({
    totals: { recordCount: 0, amount: 0, consumptionCount: 0, ticketCount: 0 },
  });
  const byKey = Object.fromEntries(tiles.map(tile => [tile.key, tile]));
  for (const key of ['recordCount', 'amount', 'consumptionCount', 'ticketCount']) {
    assert.equal(byKey[key].missing, false, `${key}=0 必须渲染成 0，不是「暂无数据」`);
    assert.equal(byKey[key].value, 0);
  }
});

test('画像序列：只有一条 0 值数据时仍要画图（total=0 不等于 empty）', () => {
  const series = buildPortraitSeries([{ bucket: 3, label: '03:00', count: 0, amount: 0 }]);
  assert.equal(series.empty, false, '有一条观测就必须画出来');
  assert.equal(series.total, 0);
  assert.deepEqual(series.values, [0]);
});

test('画像序列：后端返回 null 时按空处理，不抛错', () => {
  for (const input of [null, undefined, 'not-an-array', {}]) {
    const series = buildPortraitSeries(input);
    assert.equal(series.empty, true);
    assert.deepEqual(series.labels, []);
  }
});

test('画像序列：非数值项被跳过，不把 NaN 画进图里', () => {
  const series = buildPortraitSeries([
    { bucket: 1, count: 2 },
    { bucket: 2, count: 'oops' },
    { bucket: 3, count: 5 },
  ], { label: item => `${item.bucket}` });
  assert.deepEqual(series.values, [2, 5]);
  assert.deepEqual(series.labels, ['1', '3']);
  assert.equal(series.total, 7);
});

test('空态文案只有一处定义，页面不得另写一份', async () => {
  assert.equal(PORTRAIT_EMPTY_HINT, '暂无数据');
  const source = await readFile(pageSourcePath, 'utf8');
  // 页面必须引用这个常量，而不是硬编码字符串 —— 否则「改一处漏一处」。
  assert.match(source, /PORTRAIT_EMPTY_HINT/);
});

// ---------------------------------------------------------------------------
// 组 2：判据 ④ —— 整体与单用户走同一套函数
// ---------------------------------------------------------------------------

test('画像：整体与单用户是同一套函数，不存在 scope 分支的第二套计算', () => {
  const overallPayload = {
    scope: 'overall',
    targetUserId: 0,
    totals: { userCount: 10, recordCount: 3, amount: 30, consumptionCount: 2 },
    hours: [{ bucket: 9, count: 3, amount: 30 }],
    weekdays: [{ bucket: 5, label: '周五', count: 3, amount: 30 }],
    regions: [{ region: '中国 北京', label: '中国 北京', count: 3, amount: 30, ipCount: 1 }],
    billingTypes: [{ type: 'normal_search', label: '题目消费', direction: 'expense', count: 3, amount: 30 }],
  };
  const singlePayload = {
    scope: 'user',
    targetUserId: 7,
    base: { userId: 7, username: 'alice', nickname: '' },
    // 刻意给出**同样的数字**：两条路径算出来的图必须逐字节相同。
    totals: { recordCount: 3, amount: 30, consumptionCount: 2 },
    hours: [{ bucket: 9, count: 3, amount: 30 }],
    weekdays: [{ bucket: 5, label: '周五', count: 3, amount: 30 }],
    regions: [{ region: '中国 北京', label: '中国 北京', count: 3, amount: 30, ipCount: 1 }],
    billingTypes: [{ type: 'normal_search', label: '题目消费', direction: 'expense', count: 3, amount: 30 }],
  };

  const overallCharts = buildPortraitCharts(overallPayload);
  const singleCharts = buildPortraitCharts(singlePayload);

  // 图数据必须完全一致：证明 buildPortraitCharts 里没有 scope 分支。
  assert.deepEqual(
    { ...overallCharts, hours: { ...overallCharts.hours, title: '' }, weekdays: { ...overallCharts.weekdays, title: '' }, regions: { ...overallCharts.regions, title: '' }, billingTypes: { ...overallCharts.billingTypes, title: '' } },
    { ...singleCharts, hours: { ...singleCharts.hours, title: '' }, weekdays: { ...singleCharts.weekdays, title: '' }, regions: { ...singleCharts.regions, title: '' }, billingTypes: { ...singleCharts.billingTypes, title: '' } },
  );

  // 指标卡也一样（单用户视角下 userCount/activeUserCount 由后端给 null，
  // 前端不因此换算法，只是显示 missing）。因此只比较**两种视角共有**的四个指标。
  const SHARED_TILE_KEYS = ['recordCount', 'amount', 'consumptionCount', 'ticketCount'];
  const pickShared = tiles => tiles
    .filter(tile => SHARED_TILE_KEYS.includes(tile.key))
    .map(tile => [tile.key, tile.value, tile.missing]);
  assert.deepEqual(pickShared(buildPortraitTiles(overallPayload)), pickShared(buildPortraitTiles(singlePayload)));

  // 整体独占的两张卡：整体视角必须有值，单用户视角必须是 missing（不是 0）。
  const overallTiles = Object.fromEntries(buildPortraitTiles(overallPayload).map(tile => [tile.key, tile]));
  const singleTiles = Object.fromEntries(buildPortraitTiles(singlePayload).map(tile => [tile.key, tile]));
  assert.equal(overallTiles.userCount.missing, false);
  assert.equal(overallTiles.userCount.value, 10);
  assert.equal(singleTiles.userCount.missing, true, '单用户视角下 userCount 必须是 missing，不能是 0');
  assert.equal(singleTiles.activeUserCount.missing, true, '单用户视角下 activeUserCount 必须是 missing，不能是 0');

  // 差异只允许出现在文案上。
  assert.match(portraitScopeHint(overallPayload), /整体/);
  assert.match(portraitScopeHint(singlePayload), /alice/);
});

test('画像：前端不做二次聚合（每个数字都直接来自后端字段）', () => {
  // 后端给的 count 已是聚合结果。前端若"顺手再 sum 一次"就会出现双重口径。
  const payload = {
    hours: [
      { bucket: 9, count: 3, amount: 7.5 },
      { bucket: 10, count: 4, amount: 2.5 },
    ],
  };
  const series = buildPortraitSeries(payload.hours, { valueKey: 'count', label: item => `${item.bucket}` });
  // 3 + 4 = 7：这是把两小时的**已有计数**相加，不是重新数账单行。
  assert.equal(series.total, 7);
  assert.deepEqual(series.values, [3, 4]);

  // 金额必须走 amountKey 才拿得到，读 count 时不得到金额。
  const amountSeries = buildPortraitSeries(payload.hours, { valueKey: 'amount', label: item => `${item.bucket}` });
  assert.deepEqual(amountSeries.values, [7.5, 2.5]);
});

// ---------------------------------------------------------------------------
// 组 3：搜索框 / 视角切换的输入归一
// ---------------------------------------------------------------------------

test('画像用户搜索框：非法 id 一律回到整体视角，不抛错', () => {
  assert.equal(normalizePortraitUserId(''), '');
  assert.equal(normalizePortraitUserId(null), '');
  assert.equal(normalizePortraitUserId(undefined), '');
  assert.equal(normalizePortraitUserId(0), '');
  assert.equal(normalizePortraitUserId(-3), '');
  assert.equal(normalizePortraitUserId('abc'), '');
  assert.equal(normalizePortraitUserId('1.5'), '');
  assert.equal(normalizePortraitUserId(7), 7);
  assert.equal(normalizePortraitUserId('7'), 7);
  assert.equal(normalizePortraitUserId('  42 '), 42);
});

test('画像用户搜索框：候选项只做展示归一，不做权限判断', () => {
  assert.equal(buildPortraitUserOption(null), null);
  assert.equal(buildPortraitUserOption({ id: 0 }), null);
  assert.equal(buildPortraitUserOption({ id: 'x' }), null);

  assert.deepEqual(buildPortraitUserOption({ id: 3, username: 'bob', nickname: '小李' }), {
    value: 3,
    label: '小李（#3）',
    username: 'bob',
  });
  // 没有昵称/用户名时用 #id 兜底，不留空标签。
  assert.deepEqual(buildPortraitUserOption({ id: 4 }), { value: 4, label: '#4', username: '' });
});

test('画像视角文案：单用户视角要能看出是谁', () => {
  assert.match(portraitScopeHint({ scope: 'user', targetUserId: 9, base: { username: 'carol' } }), /carol/);
  assert.match(portraitScopeHint({ scope: 'user', targetUserId: 9, base: { username: 'carol' } }), /#9/);
  assert.match(portraitScopeHint({ scope: 'user', targetUserId: 9 }), /#9/);
  assert.match(portraitScopeHint({ scope: PORTRAIT_SCOPE_USER }), /单用户画像/);
  assert.match(portraitScopeHint({}), /整体/);
  assert.match(portraitScopeHint(null), /整体/);
});

// ---------------------------------------------------------------------------
// 组 4：结构要求 —— 两个入口，一个页面
// ---------------------------------------------------------------------------

test('画像页必须同时支持两个入口（默认整体画像 + 可切单用户）', async () => {
  const source = await readFile(pageSourcePath, 'utf8');

  // ① 默认视角必须是整体画像（用户决策：「主要」是整体的）。
  assert.match(source, /PORTRAIT_SCOPE_OVERALL/);
  assert.match(source, /getPortraitOverview/);
  // ② 用户搜索框存在，选中后切到该用户。
  assert.match(source, /getUserPortrait/);
  assert.match(source, /a-select|a-auto-complete/);
  // ③ 一个页面两种视角：不得出现第二个页面组件。
  assert.doesNotMatch(source, /UserPortraitDetail/);
});

test('画像页承载单用户视角时接受路由参数但默认整体', async () => {
  const source = await readFile(pageSourcePath, 'utf8');
  // 单用户画像从用户管理弹窗跳过来时带 userId 查询参数；缺省即整体。
  assert.match(source, /normalizePortraitUserId/);
  assert.match(source, /\$route|useRoute/);
});

test('画像接口客户端只封装两条只读 GET，全部 errorFeedback: local', async () => {
  const source = await readFile(apiSourcePath, 'utf8');

  // 后端契约原文（internal/api/router.go 由 captain 接线）：
  assert.match(source, /\/adapter-service\/admin\/portrait\/overview/);
  assert.match(source, /\/adapter-service\/admin\/users\/\$\{[^}]+\}\/portrait/);
  // 只读：不得出现 post/put/delete。
  assert.doesNotMatch(source, /method:\s*'post'/);
  assert.doesNotMatch(source, /method:\s*'put'/);
  assert.doesNotMatch(source, /method:\s*'delete'/);
  // 页面自己管失败态与重试，不能在被页面接管的请求上再弹全局错误提示。
  // 只数 `request({...})` 调用体里的，避免把注释/文档里的字样也算进去。
  const callBlocks = source.match(/request\(\{[\s\S]*?\}\)/g) || [];
  assert.equal(callBlocks.length, 2, '必须正好两条 request 调用');
  for (const block of callBlocks) {
    assert.match(block, /errorFeedback:\s*'local'/, '每条接口都必须 errorFeedback: local');
    assert.match(block, /method:\s*'get'/, '每条接口都必须是只读 GET');
  }
});

// ---------------------------------------------------------------------------
// 组 6：不改坏既有入口 —— 用户管理的「查看」仍是绑定历史（本任务最高风险点）
// ---------------------------------------------------------------------------

const userListPath = path.join(testDir, '..', 'pages', 'UserList', 'index.vue');

test('用户管理：「查看」按钮仍是绑定历史弹窗，画像按钮是并列新增', async () => {
  const source = await readFile(userListPath, 'utf8');

  // ① 既有的「查看」按钮必须仍然调 openBindingsModal —— 那是绑定历史
  // （GET /admin/users/:id/bindings），语义与画像完全不同。换成画像就是功能回退。
  assert.match(
    source,
    /@click="openBindingsModal\(record\)">查看<\/a-button>/,
    '「查看」按钮必须仍然调用 openBindingsModal（绑定历史），不得被画像替换',
  );
  // ② 画像必须是**另一个**按钮，文案为「画像」。
  assert.match(
    source,
    /@click="openPortrait\(record\)">画像<\/a-button>/,
    '必须新增独立的「画像」按钮（不得占用「查看」）',
  );
  // ③ 两个按钮必须并存于同一个操作列（顺序无关，但都要在同一段 a-space 里）。
  const operationBlock = source.match(/v-else-if="column\.key === 'operation'"[\s\S]*?<\/a-space>/);
  assert.notEqual(operationBlock, null, '操作列必须存在');
  assert.match(operationBlock[0], /openEditModal/, '编辑按钮不得被移除');
  assert.match(operationBlock[0], /openBindingsModal/, '绑定历史按钮不得被移除');
  assert.match(operationBlock[0], /openPortrait/, '画像按钮必须存在');
  assert.match(operationBlock[0], /openResetPasswordModal/, '重置密码按钮不得被移除');
  // ④ 删除按钮不得被移除。
  assert.match(operationBlock[0], /deleteConfirmation\(record\.id\)/, '删除按钮不得被移除');
});

test('用户管理：画像入口跳转到同一页面并带 userId（不另开弹窗）', async () => {
  const source = await readFile(userListPath, 'utf8');

  const method = source.match(/openPortrait\(record\)\s*\{([\s\S]*?)\n {4}\},/);
  assert.notEqual(method, null, 'openPortrait 方法必须存在');
  // 必须跳转到 user-portrait 路由并带 userId 查询参数（一个页面的两种视角）。
  assert.match(method[1], /name:\s*'user-portrait'/, '必须跳到 user-portrait 路由');
  assert.match(method[1], /userId:\s*String\(id\)/, '必须带 userId 查询参数以切到该用户视角');
  // 不得在这里另开弹窗（否则同一份画像会渲染两份）。
  assert.doesNotMatch(method[1], /Visible\.value\s*=\s*true/, '画像不得在本页另开弹窗');
  // 非法 id 要先拦住，不要跳到 /NaN。
  assert.match(method[1], /Number\.isSafeInteger\(id\)/, '必须校验 id');
});

test('用户管理：绑定历史弹窗的加载与关闭逻辑未被触碰', async () => {
  const source = await readFile(userListPath, 'utf8');

  // 这三条是绑定历史弹窗的完整数据流，任何一条被删都会让「查看」静默失效。
  assert.match(source, /async openBindingsModal\(record\)/, 'openBindingsModal 必须保留');
  assert.match(source, /async reloadBindings\(\)/, 'reloadBindings 必须保留');
  assert.match(source, /closeBindingsModal\(\)\s*\{/, 'closeBindingsModal 必须保留');
  assert.match(source, /getAdminUserBindings/, '绑定历史接口调用必须保留');
  assert.match(source, /bindingsVisible/, '绑定历史弹窗状态必须保留');
});

// ---------------------------------------------------------------------------
// 组 7：两个入口的最终接线（由 captain 完成，当前预期为红）
// ---------------------------------------------------------------------------
//
// ⚠️ 下面两条当前是**预期的红**：`web/src/router/router.js` 与 `SiderMenu/index.vue`
// 在本任务的可写范围之外（captain 独占，避免与其它成员撞车）。接线由 captain 完成，
// 失败信息里直接打印需要粘贴的原文，加完后自动转绿，不需要再改测试。
// 这与 Go 侧的 TestPortraitRoutesBindBothMiddlewares 是同一个设计。
// ---------------------------------------------------------------------------

test('画像路由必须登记为管理员专属（直接敲 URL 会被跳转）', async () => {
  const source = await readFile(routerSourcePath, 'utf8');

  const MISSING = [
    'web/src/router/router.js 需要加（两处）：',
    '',
    "1) 顶部懒加载声明（放在 UserList 那一段旁边）：",
    "   const UserPortrait = () => import(/* webpackChunkName: \"route-user-portrait\" */ '../pages/UserPortrait/index.vue')",
    '',
    '2) /adapter 的子路由（放在 userlist 那条之后）：',
    '   {',
    "     path: 'user-portrait',",
    "     name: 'user-portrait',",
    '     component: UserPortrait,',
    '   },',
    '',
    '3) ADMIN_ONLY_ROUTE_NAMES 里加一行（否则普通用户直接敲 URL 能进页面）：',
    "   'user-portrait',",
  ].join('\n');

  assert.match(source, /'user-portrait'/, MISSING);
  const adminOnlyBlock = source.match(/const ADMIN_ONLY_ROUTE_NAMES = new Set\(\[([\s\S]*?)\]\)/);
  assert.notEqual(adminOnlyBlock, null, 'ADMIN_ONLY_ROUTE_NAMES 必须存在');
  assert.match(
    adminOnlyBlock[1],
    /'user-portrait'/,
    `user-portrait 必须列进 ADMIN_ONLY_ROUTE_NAMES，否则普通用户敲 URL 能进页面。\n\n${MISSING}`,
  );
});

test('画像菜单项只出现在管理员菜单里', async () => {
  const source = await readFile(menuSourcePath, 'utf8');

  const MISSING = [
    'web/src/components/SiderMenu/index.vue 的 ADMIN_MENU_ITEMS 里需要加一行',
    '（放在 userlist 那条之后；USER_MENU_ITEMS 绝不能加）：',
    '',
    "   { key: 'user-portrait', label: '用户画像', icon: PieChartOutlined },",
  ].join('\n');

  const adminBlock = source.match(/const ADMIN_MENU_ITEMS = \[([\s\S]*?)\n\]/);
  const userBlock = source.match(/const USER_MENU_ITEMS = \[([\s\S]*?)\n\]/);
  assert.notEqual(adminBlock, null, 'ADMIN_MENU_ITEMS 必须存在');
  assert.notEqual(userBlock, null, 'USER_MENU_ITEMS 必须存在');

  // 普通用户菜单里绝不能出现：98 §0 要求画像仅管理员可见。
  // 先断言这一条（它不依赖接线是否完成），避免接线没做时被前面的 red 掩盖。
  assert.doesNotMatch(userBlock[1], /user-portrait/, '普通用户菜单不得出现画像项（98 §0：画像仅管理员可见）');

  assert.match(adminBlock[1], /key:\s*'user-portrait'/, `管理员菜单必须有「用户画像」项。\n\n${MISSING}`);
  assert.match(adminBlock[1], /用户画像/, '菜单文案应为「用户画像」');
});
