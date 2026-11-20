// A4-2：用户管理页「设备」按钮 + 设备管理页按 ?userId= 限定 —— 前后端契约。
//
// 本文件**零依赖**（不 import vue / ant-design / jsdom），与 portraitSource.test.mjs 同款，
// 因此没有 node_modules 也能 `node --test` 跑通。
//
// ## 为什么这个功能需要一条专门的契约测试
//
// 它是一条**跨三层**的链：
//
//   UserList 的「设备」按钮  →  router.push({name:'device-admin', query:{userId}})
//     →  DeviceAdmin 读 route.query.userId 并**每次请求**带上
//       →  后端 ListAdminDeviceBindingsWithDB 的 query.UserID 过滤
//
// 任何一环断掉，表现都是「页面能打开、列表能显示，但内容是**全部用户**的」。
// 这类故障不会报错、不会白屏，只会让管理员看着别人的设备做判断 ——
// 所以每一环都必须有独立判据，不能只测其中一层。
//
// 判据只做**源码级**结构化断言，不复制实现：断言的是「这个语义存在」，
// 而不是「这一行长什么样」。因此变量改名不会让它假红，但删掉功能必红。

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const testDir = path.dirname(fileURLToPath(import.meta.url));
const webSrc = path.join(testDir, '..');

const userListPath = path.join(webSrc, 'pages', 'UserList', 'index.vue');
const deviceAdminPath = path.join(webSrc, 'pages', 'DeviceAdmin', 'index.vue');
const apiPath = path.join(webSrc, 'api', 'DeviceAdminApi.js');
const routerPath = path.join(webSrc, 'router', 'router.js');

/**
 * 剥掉注释，避免把「反例说明」误判成「功能被改了」。
 *
 * ⚠️ 必须同时剥 `/* *\/` 与 `//`：本仓的 Vue 文件里大量用块注释记录取舍，
 * 只剥行注释会让 `<!-- ... -->` 之外的说明混进断言。
 * 模板里的 HTML 注释是另一种语法，单独剥。
 */
function stripComments(source) {
  return source
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*\/\/.*$/gm, '');
}

async function readStripped(file) {
  return stripComments(await readFile(file, 'utf8'));
}

// ---------------------------------------------------------------------------
// 环 1：UserList 的「设备」按钮必须存在，且与「查看」并列（不是替换）
// ---------------------------------------------------------------------------

test('UserList 有「设备」按钮，且它调用 openDevices', async () => {
  const source = await readStripped(userListPath);

  // 按钮的可见文案
  assert.match(source, />\s*设备\s*<\/a-button>/,
    '用户管理页必须有「设备」按钮（A4-2）');
  // 它绑定的处理函数
  assert.match(source, /@click="openDevices\(record\)"/,
    '「设备」按钮必须调用 openDevices(record)');
});

test('UserList 的 openDevices 在用户列表弹出指定用户设备', async () => {
  const source = await readStripped(userListPath);

  const method = source.match(/openDevices\s*\(record\)\s*\{[\s\S]*?\n\s{4}\},/);
  assert.ok(method, '必须定义 openDevices(record) 方法');
  const body = method[0];

  assert.match(body, /deviceTarget\.value\s*=\s*\{\s*id/,
    'openDevices 必须绑定目标用户供弹窗显示');
  assert.match(body, /deviceVisible\.value\s*=\s*true/,
    'openDevices 必须在当前页面打开设备弹窗');
  assert.match(body, /this\.loadUserDevices\(\)/,
    '打开设备弹窗后必须读取该用户设备');

  // 非法 id 必须早退，不能带着 NaN 跳过去。
  assert.match(body, /Number\.isSafeInteger\(id\)/,
    'openDevices 必须校验 id 是安全整数（否则会带着 NaN 请求后端）');
});

test('「设备」与「查看」「画像」是三个并列入口，谁都没被替换', async () => {
  const source = await readStripped(userListPath);

  // 三个 handler 必须同时存在。
  for (const handler of ['openEditModal', 'openBindingsModal', 'openPortrait', 'openDevices']) {
    assert.ok(source.includes(`${handler}(record)`),
      `操作列必须仍保留 ${handler} —— A4-2 是**并列新增**，不得替换任何既有入口`);
  }
  // 「查看」是绑定历史，「设备」是槽位列表：两者的 handler 不能是同一个。
  assert.doesNotMatch(source, /openBindingsModal\s*\(record\)"?\s*>\s*设备/,
    '「设备」不得复用绑定历史的 openBindingsModal（语义不同）');
});

// ---------------------------------------------------------------------------
// 环 2：DeviceAdmin 必须读 route.query.userId，且**每次请求**都带上
// ---------------------------------------------------------------------------

test('DeviceAdmin 从 route.query 解析 userId，非法值按 0 处理', async () => {
  const source = await readStripped(deviceAdminPath);

  const decl = source.match(/const\s+\w*[Ss]copedUserId\w*\s*=\s*computed\([\s\S]*?\n\s*\}\)/);
  assert.ok(decl, 'DeviceAdmin 必须从 route.query.userId 派生出 scopedUserId');
  const body = decl[0];

  assert.match(body, /route\.query\.userId/,
    'scopedUserId 必须读 route.query.userId');
  // 非法值降级为 0（不过滤）而不是抛错：手改 URL 不该让整页报错。
  assert.match(body, /Number\.isSafeInteger/,
    'scopedUserId 必须用 Number.isSafeInteger 校验（否则 "abc" 会变成 NaN 传下去）');
  assert.match(body, /\?\s*parsed\s*:\s*0/,
    'scopedUserId 对非法值必须返回 0（不过滤），不能返回 NaN/undefined');
});

test('userId 必须随**每次**请求带上，不是只在挂载时用一次', async () => {
  const source = await readStripped(deviceAdminPath);

  // 关键：这条断言在 buildParams（每次请求都会调）里，而不是 onMounted 里。
  const buildParams = source.match(/const\s+buildParams\s*=\s*\(\)\s*=>\s*\{[\s\S]*?\n\s{4}\}/);
  assert.ok(buildParams, '必须存在 buildParams()');
  assert.match(buildParams[0], /params\.userId\s*=/,
    'userId 必须在 buildParams() 里赋值 —— ' +
    '只放在 onMounted 会让翻页/刷新时丢掉限定，第 2 页变回「全部用户的设备」');
});

test('?userId= 变化时必须重新取数（同一 path 的 query 变化不会触发 route.path 的 watch）', async () => {
  const source = await readStripped(deviceAdminPath);

  // ① 必须存在以 scopedUserId 为源的 watch。
  const guards = [...source.matchAll(/watch\(\s*(\w*[Ss]copedUserId\w*)\s*,/g)];
  assert.ok(guards.length > 0,
    '必须有一条以 scopedUserId 为源的 watch —— ' +
    '否则「从用户 A 的设备跳到用户 B 的设备」不会重新取数，' +
    'B 的面板里会继续显示 A 的设备（而标题已写成 B）');

  const decl = source.match(/watch\(\s*\w*[Ss]copedUserId\w*\s*,[\s\S]*?\n\s{4}\}\)/) ||
    source.match(/watch\(\s*\w*[Ss]copedUserId\w*\s*,[\s\S]*?\n\s{4}\}/);
  assert.ok(decl, 'scopedUserId 的 watch 必须能定位到函数体');
  const fn = decl[0];

  // ② loadRows() 必须在。
  const loadIdx = fn.indexOf('loadRows()');
  assert.ok(loadIdx > 0, 'scopedUserId 变化后必须重新 loadRows()（清空但不重取 = 空白页）');

  // ③ loadRows() 必须是**可达的** —— 它不能排在一个无条件的 `return` 之后。
  //
  // 为什么 ③ 必需：只写 ② 时，把 watch 体改成 `() => { return; … loadRows() }`
  // 测试**仍然全绿**（实测）—— 正则找得到 loadRows 字样，但它在 return 之后
  // 永不执行。这是「断言只要字符串存在」的假钉子形态。
  //
  // 实现：把每个 `if (...)` 连同它后面的整行一起跳过 ——
  // `if (cond) return` 是**有条件的**提前退出，属正常写法；
  // 只有「不依赖任何条件的 return」才会把后面的 loadRows() 屏蔽掉。
  //
  // ⚠️ 必须手工做括号平衡，不能用 `if\s*\([^)]*\)`：条件里本身含括号
  // （`if (!alive || !canManage()) return`），`[^)]*` 会在第一个 `)` 处停下、
  // 剥不掉 → 把有条件的 return 误判成无条件的（实测踩到）。
  // 这与项目记忆里「[^)]* 无法匹配带括号的目标」是同一类坑。
  const before = fn.slice(0, loadIdx);
  let stripped = '';
  for (let i = 0; i < before.length; i += 1) {
    if (before[i] === 'i' && /^if\s*\(/.test(before.slice(i))) {
      // 找到配对的 `)`
      let depth = 0;
      let j = before.indexOf('(', i);
      for (; j < before.length; j += 1) {
        if (before[j] === '(') depth += 1;
        else if (before[j] === ')') { depth -= 1; if (depth === 0) break; }
      }
      // 再吞掉同一行剩下的部分（`return` 就落在这里）
      let k = j + 1;
      while (k < before.length && before[k] !== '\n') k += 1;
      i = k;
      continue;
    }
    stripped += before[i];
  }
  assert.doesNotMatch(stripped, /\breturn\b/,
    'loadRows() 之前不得有无条件的 return —— 否则 watch 触发后直接退出、永远不重取');
});

test('按账号限定时，列表说明必须显式写出来（作用域收窄不能是隐形的）', async () => {
  const source = await readStripped(deviceAdminPath);

  const scopeLabel = source.match(/const\s+scopeLabel\s*=\s*computed\([\s\S]*?\n\s{4}\}\)/) ||
    source.match(/const\s+scopeLabel\s*=\s*computed\([\s\S]*?\n\s+\}\)/);
  assert.ok(scopeLabel, '必须存在 scopeLabel');
  assert.match(scopeLabel[0], /[Ss]copedUserId/,
    'scopeLabel 必须包含「仅显示账号 #N」这类措辞 —— ' +
    '否则「只看到 2 台」与「真的只有 2 台」看起来一样，管理员会据此误判');
});

// ---------------------------------------------------------------------------
// 环 3：前端 API 会把 userId 传给后端
// ---------------------------------------------------------------------------

test('DeviceAdminApi.getAdminDevices 把 params 原样透传给后端', async () => {
  const source = await readStripped(apiPath);

  const fn = source.match(/export\s+function\s+getAdminDevices\s*\([\s\S]*?\n\}/);
  assert.ok(fn, '必须存在 getAdminDevices');
  // 必须以 params 整体传递，而不是手挑字段 ——
  // 手挑字段会让「加了 userId 但 API 层没透传」变成静默失效
  // （与 t34 的 bucket 漏透传同型）。
  assert.match(fn[0], /params/,
    'getAdminDevices 必须透传 params（手挑字段会让新增的 userId 静默丢失）');
});

// ---------------------------------------------------------------------------
// 路由：device-admin 必须仍登记为管理员专属
// ---------------------------------------------------------------------------

test('device-admin 路由存在，且仍在管理员专属名单里', async () => {
  const source = await readStripped(routerPath);

  assert.match(source, /name:\s*'device-admin'/,
    'router.js 必须登记 device-admin 路由（openDevices 依赖它）');

  // 管理员专属：普通用户不该能直接打开设备管理页。
  const adminOnly = source.match(/const ADMIN_ONLY_ROUTE_NAMES = new Set\(\[([\s\S]*?)\]\)/);
  assert.ok(adminOnly, '必须存在 ADMIN_ONLY_ROUTE_NAMES');
  assert.match(adminOnly[0], /'device-admin'/,
    'device-admin 必须在 ADMIN_ONLY_ROUTE_NAMES 里 —— 它是管理员页，A4-2 不得顺带放开权限');
});
