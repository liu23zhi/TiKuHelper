// 激活码页「是否允许操作」判据的钉子。
//
// 真实事故：用户报「脚本一次性激活码无法生成」。
// 根因不是后端（controller/script_activation_code.go:56-109 只做 requireAdmin +
// Count 1..100 校验，不读价格/escrow/settings；DB 表已建、路由已挂 → 401 而非 404），
// 而在前端 canManage 的判据形态：它用挂载时快照 `route.path` 比较当前 `route.path`。
//
// 这种写法是**一次性快照**：组件实例被 Vue 复用到另一条路由时 route.path 变了而快照不变，
// canManage 恒 false → 整页按钮静默禁用，没有任何报错。
// 仓库里 ScriptManagement 已经踩过同一个坑并改成了 OWN_ROUTE_NAMES 白名单。

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const pagePath = path.join(here, '..', 'pages', 'ScriptActivationCode', 'index.vue');

const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/[^\n]*/g, '');

test('canManage 不得依赖挂载时快照的 route.path', async () => {
  const source = stripComments(await readFile(pagePath, 'utf8'));
  assert.doesNotMatch(
    source,
    /const\s+mountedPath\s*=\s*route\.path/,
    '不得再用 mountedPath 快照 —— 组件实例复用时会静默失效，表现为按钮全灰且无报错',
  );
  assert.doesNotMatch(
    source,
    /route\.path\s*===\s*mountedPath/,
    'canManage 不得比较 route.path === mountedPath',
  );
});

test('canManage 必须用 route.name 白名单判归属（与 ScriptManagement 一致）', async () => {
  const source = stripComments(await readFile(pagePath, 'utf8'));
  assert.match(
    source,
    /OWN_ROUTE_NAMES\s*=\s*Object\.freeze\(\[/,
    '必须有 OWN_ROUTE_NAMES 白名单（照搬 ScriptManagement 的已验证做法）',
  );
  assert.match(source, /const\s+stillOwnRoute\s*=/, '必须有 stillOwnRoute()');
  assert.match(
    source,
    /canManage\s*=\s*\(\)\s*=>\s*alive\s*&&\s*stillOwnRoute\(\)/,
    'canManage 必须基于 stillOwnRoute()，不能基于路径快照',
  );
  // 白名单必须真的包含本页路由名，否则 stillOwnRoute 恒 false（另一种全灰）
  assert.match(
    source,
    /OWN_ROUTE_NAMES[\s\S]{0,200}?['"]script-activation-codes['"]/,
    "白名单必须包含 'script-activation-codes'，否则 canManage 恒 false",
  );
});

test('admin 与账号状态仍是必要条件（换判据不能放宽权限）', async () => {
  const source = stripComments(await readFile(pagePath, 'utf8'));
  assert.match(
    source,
    /canManage[\s\S]{0,160}?isAdminUser\(/,
    'canManage 必须仍要求 isAdminUser —— 这是管理员页，不能因为换判据就放宽',
  );
  assert.match(
    source,
    /canManage[\s\S]{0,240}?disabled/,
    'canManage 必须仍检查账号是否被禁用',
  );
});

test('batchLimit 必须是固定常量（不能来自可能为 0 的设置）', async () => {
  const source = stripComments(await readFile(pagePath, 'utf8'));
  assert.match(
    source,
    /batchLimit\s*=\s*BATCH_LIMIT/,
    'batchLimit 必须直接引用 BATCH_LIMIT 常量；若改成从接口读，未配置时会变 0 → 输入框 max=0 → 无法生成',
  );
});
