/* 「双角色文案一致性」审计（T-COPY）。
 *
 * 背景：同一页面在两个角色下会有两个名字（例如管理员「工单管理」/ 用户「工单反馈」），
 * 这本身是设计。真正的事故是**名字对不上**：
 *   - 侧边栏 shared 分区用管理端文案（管理员在「普通用户可见」区里看到自己的视角）—— 已修；
 *   - ⚠️ 修它时把管理端的 ADMIN_MENU_ITEMS 也改掉了 —— 已回滚重做；
 *   - 页面标题写死管理员措辞，普通用户点进去看到「工单管理」—— 已修三处（tickets / billing-records / scripts）；
 *   - 页面标题与菜单文案不同名（user-usage：菜单「个人使用」/ 标题「账户使用」）—— 本轮修。
 *
 * 这个文件把上述关系固化成**判据**，而不是靠人逐个页面肉眼比对。
 *
 * 两条硬约束（踩过才知道）：
 *
 *  1. **不 import vue / @vue/compiler-sfc**。web/node_modules 是空壳，任何 import 它们的测试
 *     都会 ERR_MODULE_NOT_FOUND（既有的 loginAndRechargePageSource.test.mjs 就是这么废掉的）。
 *     所以这里只读源码字符串 + 正则/结构解析。
 *
 *  2. **映射必须从 router.js 派生，不能手抄一张「key → 页面」表**。手抄表在新增页面时会静默漏掉
 *     （新页面没被审计 = 缺陷能从判据下面走过去而没人知道）。下面 routePageMap() 解析
 *     router.js 的 import 别名与 route 对象，页面文件是**算出来的**。
 *     验证手法见变异 d：往 router.js 塞一条假 route，本测试立刻跟着去看那个页面。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const testDir = dirname(fileURLToPath(import.meta.url));
const srcDir = resolve(testDir, '..');
const readSrc = (relative) => readFileSync(resolve(srcDir, relative), 'utf8');

const menuSource = readSrc('components/SiderMenu/index.vue');
const routerSource = readSrc('router/router.js');

// ─────────────────────── 从 SiderMenu 派生菜单数据 ───────────────────────

/** 截出 `const NAME = [ ... ]` 的数组原文。 */
function arrayLiteral(source, name) {
  const match = new RegExp(`const ${name} = \\[([\\s\\S]*?)\\n\\]`).exec(source);
  assert.ok(match, `SiderMenu 里找不到 const ${name} = [...]`);
  return match[1];
}

/** 把菜单数组原文求值成对象数组（图标是组件引用，统一置 null）。 */
function evaluateItems(source, name) {
  const body = arrayLiteral(source, name).replace(/icon:\s*[A-Za-z_$][\w$]*/g, 'icon: null');
  // eslint-disable-next-line no-new-func
  return new Function(`return [${body}]`)();
}

/**
 * 执行 SiderMenu 里「分区派生」那一段原文，拿到 ADMIN_MENU_SECTIONS。
 *
 * 为什么复用那段原文而不是在测试里自己 filter 一遍：shared 分区的文案是
 * `sharedSectionItem` 从 USER_MENU_ITEMS 取的（第 8 轮的修法），
 * 自己在测试里重算等于「测试复制了一份实现」，实现改了测试不红。
 */
function evaluateAdminSections(source, adminItems, userItems) {
  const REGION_START = '// >>> sidebar-section-derivation';
  const REGION_END = '// <<< sidebar-section-derivation';
  const start = source.indexOf(REGION_START);
  const end = source.indexOf(REGION_END);
  assert.ok(start >= 0 && end > start, 'sidebar-section-derivation 标记缺失或顺序颠倒');
  const regionStart = source.indexOf('\n', start);
  const region = source.slice(regionStart + 1, end);
  assert.ok(region.trim().length > 0, '派生区段截出来是空串');
  // eslint-disable-next-line no-new-func
  const run = new Function(
    'ADMIN_MENU_ITEMS',
    'USER_MENU_ITEMS',
    `${region}\nreturn { ADMIN_MENU_SECTIONS };`,
  );
  try {
    return run(adminItems, userItems).ADMIN_MENU_SECTIONS;
  } catch (error) {
    assert.fail(`侧边栏派生区段执行失败（标记还在但内容已失效）：${error.message}`);
  }
}

const ADMIN_MENU_ITEMS = evaluateItems(menuSource, 'ADMIN_MENU_ITEMS');
const USER_MENU_ITEMS = evaluateItems(menuSource, 'USER_MENU_ITEMS');
const ADMIN_MENU_SECTIONS = evaluateAdminSections(menuSource, ADMIN_MENU_ITEMS, USER_MENU_ITEMS);
const sharedSection = ADMIN_MENU_SECTIONS.find((section) => section.key === 'shared');
assert.ok(sharedSection, 'ADMIN_MENU_SECTIONS 里没有 shared 分区');

const ADMIN_BY_KEY = new Map(ADMIN_MENU_ITEMS.map((item) => [item.key, item]));
const USER_BY_KEY = new Map(USER_MENU_ITEMS.map((item) => [item.key, item]));
const SHARED_KEYS = sharedSection.items.map((item) => item.key).sort();

// ─────────────────────── 从 router.js 派生「key → 页面文件」 ───────────────────────

/** 组件变量名 → 页面文件（相对 src/）。同时认 `const X = () => import(...)` 与 `import X from '...'`。 */
function componentFileMap(router) {
  const map = new Map();
  for (const match of router.matchAll(/const\s+([A-Za-z_$][\w$]*)\s*=\s*\(\)\s*=>\s*import\(\s*\/\*[\s\S]*?\*\/\s*'([^']+)'\s*\)/g)) {
    map.set(match[1], match[2]);
  }
  for (const match of router.matchAll(/^import\s+([A-Za-z_$][\w$]*)\s+from\s+'([^']+)'/gm)) {
    map.set(match[1], match[2]);
  }
  return map;
}

/**
 * route name → 页面文件（相对 src/，已归一化）。
 *
 * 只认带 `component:` 的叶子路由：`location-cache` 那种只有 `redirect:` 的别名路由
 * 没有页面，不该参与文案审计（它不是菜单项）。
 */
function routePageMap(router) {
  const components = componentFileMap(router);
  const map = new Map();
  // ⚠️ `\{` 与 `path:` 之间可能夹着注释行（2026-11-14 拆路由时新增了说明注释）。
  // 用 `[^{}]*?` 允许任意非花括号内容，否则带注释的路由会被静默漏掉 ——
  // 那会让「共用 key 映射完整派生」这条自检误报成「router 里没有该路由」。
  const routeRe = /\{[^{}]*?path:\s*'[^']*',\s*name:\s*'([^']+)',\s*component:\s*([A-Za-z_$][\w$]*)/g;
  for (const match of router.matchAll(routeRe)) {
    const file = components.get(match[2]);
    if (file) map.set(match[1], file);
  }
  return map;
}

/** router.js 里的相对路径是相对 src/router/ 写的，这里归一化成 src/ 下的相对路径。 */
function normalizePagePath(routerRelative) {
  const parts = ['router', ...routerRelative.split('/')];
  const out = [];
  for (const part of parts) {
    if (part === '.' || part === '') continue;
    if (part === '..') out.pop();
    else out.push(part);
  }
  return out.join('/');
}

const ROUTE_PAGE = new Map(
  [...routePageMap(routerSource)].map(([name, file]) => [name, normalizePagePath(file)]),
);

// ─────────────────────── 解析 ConsolePageHeading ───────────────────────

/** 取出 `<ConsolePageHeading …>` 整个开始标签（多行；跳过引号内的 `>`）。 */
function findHeadingTag(source) {
  const start = source.indexOf('<ConsolePageHeading');
  if (start < 0) return null;
  let quote = null;
  for (let i = start + '<ConsolePageHeading'.length; i < source.length; i += 1) {
    const ch = source[i];
    if (quote) {
      if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'") {
      quote = ch;
      continue;
    }
    if (ch === '>') return source.slice(start, i + 1);
  }
  return null;
}

/** 从开始标签里读出标题的形态：静态字面量 / 动态表达式 / 缺失。 */
function readHeadingTitle(tag) {
  const dynamic = /\s:title="([^"]*)"/.exec(tag);
  if (dynamic) return { kind: 'dynamic', expr: dynamic[1].trim() };
  const staticTitle = /\stitle="([^"]*)"/.exec(tag);
  if (staticTitle) return { kind: 'static', value: staticTitle[1].trim() };
  return { kind: 'missing' };
}

/**
 * 把动态标题解析成 [isAdmin 为真时的文案, isAdmin 为假时的文案]。
 *
 * 两种写法都要认，因为本仓两种都在用：
 *   ① 内联三元  `:title="isAdmin ? '工单管理' : '工单反馈'"`
 *   ② 具名 computed `:title="pageTitle"` + `const pageTitle = computed(() => (isAdmin.value ? … : …))`
 *
 * 返回 null 表示解析不出来 —— 调用方必须把它当作**失败**，不能当"通过"。
 */
function resolveTitleBranches(source, expr) {
  const inline = /^(?:this\.)?isAdmin(?:\.value)?\s*\?\s*'([^']*)'\s*:\s*'([^']*)'$/.exec(expr);
  if (inline) return [inline[1], inline[2]];
  if (/^[A-Za-z_$][\w$]*$/.test(expr)) {
    const decl = new RegExp(`const\\s+${expr}\\s*=`).exec(source);
    if (decl) {
      const rest = source.slice(decl.index);
      // 只在本声明之后、下一个顶层声明之前找，避免抓到隔壁变量的三元。
      const boundary = rest.search(/\n\s{0,2}(?:const|function|watch|onMounted|onBeforeUnmount)\s/);
      const scope = boundary > 0 ? rest.slice(0, boundary) : rest;
      const ternary = /isAdmin(?:\.value)?\s*\?\s*'([^']*)'\s*:\s*'([^']*)'/.exec(scope);
      if (ternary) return [ternary[1], ternary[2]];
    }
  }
  return null;
}

/** 读某个路由 key 的页面标题信息（文件不存在 / 无标题都显式返回，不静默跳过）。 */
function readPageHeading(key) {
  const relative = ROUTE_PAGE.get(key);
  if (!relative) return { key, kind: 'no-route', reason: `router.js 里没有 name=${key} 且带 component 的 route` };
  let source;
  try {
    source = readSrc(relative);
  } catch {
    return { key, kind: 'no-file', file: relative, reason: `路由指向的页面文件不存在：${relative}` };
  }
  const tag = findHeadingTag(source);
  if (!tag) return { key, kind: 'no-heading', file: relative, reason: '页面里没有 ConsolePageHeading' };
  const title = readHeadingTitle(tag);
  if (title.kind === 'static') return { key, kind: 'static', file: relative, value: title.value };
  if (title.kind === 'dynamic') {
    const branches = resolveTitleBranches(source, title.expr);
    return { key, kind: 'dynamic', file: relative, expr: title.expr, branches };
  }
  return { key, kind: 'no-title', file: relative, reason: 'ConsolePageHeading 上没有 title / :title' };
}

// 每个 key 的页面标题信息只算一次。
//
// ⚠️ 覆盖 SHARED_KEYS ∪ 四个管理端拆出来的 key（ticket-admin / script-admin /
// billing-admin / token-admin）。后者不在 shared 分区里（设计如此：它们属于「仅管理员可见」），
// 但标题同样要审计 —— 否则「管理端入口点进去标题对不对」没有任何判据。
const AUDIT_KEYS = [...new Set([...SHARED_KEYS, 'ticket-admin', 'script-admin', 'billing-admin', 'token-admin'])];
const HEADINGS = new Map(AUDIT_KEYS.map((key) => [key, readPageHeading(key)]));

const labelOf = (item) => String(item?.label ?? '').trim();

// ─────────────────────── 用例 ───────────────────────

test('共用 key 的映射能从 router.js 完整派生（不手抄，且不漏项）', () => {
  // 自检：每个共用 key 都必须在 router.js 里找到带 component 的路由，并且页面文件真的存在。
  // 这条挡住「新增共用页面但审计没覆盖」—— 那是手抄映射表的典型失效形态。
  const missing = SHARED_KEYS.filter((key) => !ROUTE_PAGE.has(key));
  assert.deepEqual(missing, [], `这些共用 key 在 router.js 里找不到页面路由：${missing.join(', ')}`);

  for (const key of SHARED_KEYS) {
    const heading = HEADINGS.get(key);
    assert.ok(
      ['static', 'dynamic'].includes(heading.kind),
      `${key} 的页面标题无法审计（${heading.kind}）：${heading.reason || ''} ${heading.file || ''}`.trim(),
    );
  }

  // 派生本身要健康：至少覆盖本仓当前已知的 7 个共用项，否则说明解析器退化了（例如 router 写法变了）。
  assert.ok(SHARED_KEYS.length >= 7, `共用项只解析出 ${SHARED_KEYS.length} 个，解析器可能已失效`);
});

test('判据①：管理员 shared 分区的文案必须等于普通用户看到的文案', () => {
  // 这是「从侧边栏扩展到页面标题」的起点：shared 分区本身就是「普通用户可见」的声明，
  // 里面出现管理端措辞，分区标题就是假话（第 8 轮用户实测报出的缺陷）。
  for (const item of sharedSection.items) {
    const userItem = USER_BY_KEY.get(item.key);
    assert.ok(userItem, `shared 分区的 ${item.key} 不在 USER_MENU_ITEMS 里，分区语义被破坏`);
    assert.equal(
      labelOf(item),
      labelOf(userItem),
      `shared 分区「${item.key}」用了管理端文案「${labelOf(item)}」，应该是用户端「${labelOf(userItem)}」`,
    );
  }
});

test('判据②：每个共用页面按**入口**决定标题，不由角色决定', () => {
  // ⚠️ 2026-11-14 模型变更（用户实测报出的缺陷驱动）：
  //
  // 旧模型：一个 key + 一个路由，标题按 isAdmin 三元切换。
  //   缺陷：管理员点「工单反馈」看到的是「工单管理」的页面，且管理端入口无法单独选中。
  //
  // 新模型：同一页面拆成两个路由、两个 key，标题由 route.meta.viewMode 决定：
  //   tickets（viewMode='self'）↔ ticket-admin（viewMode='admin'）
  //   scripts（'self'）        ↔ script-admin（'admin'）
  //   billing-records（'self'）↔ billing-admin（'admin'）
  //
  // 所以判据从「同一 key 的标题必须按角色分两支」改成
  // 「每个路由的标题必须与**它自己那个 key** 的菜单文案一致」——
  // 这才是「点菜单进去看到的标题与菜单一致」的真正判据。
  const violations = [];

  for (const key of SHARED_KEYS) {
    const userLabel = labelOf(USER_BY_KEY.get(key));
    const heading = HEADINGS.get(key);
    if (heading.kind !== 'static') {
      // 允许动态标题，但必须是 viewMode 派生的那两支之一，不能是裸 isAdmin 三元
      // （裸三元会让「self 入口」在管理员下也显示管理文案，正是原缺陷）。
      if (heading.kind === 'dynamic' && heading.branches) {
        // 顺序契约：`isAdmin ? A : B` 里 B（非管理员分支）必须是用户端文案。
        const whenNot = heading.branches[1];
        if (whenNot !== userLabel) {
          violations.push(
            `${key} 的标题非管理员分支是「${whenNot}」，但菜单写的是「${userLabel}」`,
          );
        }
        continue;
      }
      violations.push(
        `${key} 的标题无法审计（${heading.kind}）：${heading.reason || ''} ${heading.file || ''}`.trim(),
      );
      continue;
    }
    if (heading.value !== userLabel) {
      violations.push(
        `${key} 的页面标题写死成「${heading.value}」，但它的菜单文案是「${userLabel}」`
        + ` —— 用户从菜单点进来会以为点错了。文件：${heading.file}`,
      );
    }
  }

  assert.deepEqual(violations, [], violations.join('\n'));
});


test('判据③（反向防线）：拆开后的双入口，两侧 key 各自单文案且相邻路由同名', () => {
  // ⚠️ 模型已变（2026-11-14，用户实测报出的缺陷驱动）：
  //
  // 旧：一个 key + 一个路由，标题按 isAdmin 三元切换 —— 管理员点「工单反馈」
  //     看到的却是管理视图，且管理端入口无法单独选中。
  // 新：**拆路由**。同一页面两个 name、两个 key：
  //     tickets（viewMode='self'）↔ ticket-admin（viewMode='admin'）
  //     scripts（'self'）        ↔ script-admin（'admin'）
  //     billing-records（'self'）↔ billing-admin（'admin'）
  //
  // ⇒ 判据从「同一 key 的标题必须分两支」改为「每个 key 的标题必须与它自己的
  // 菜单文案一致」，并且**拆开后不能再有同一个 key 出现在两个分区**。
  const pairs = [
    ['tickets', 'ticket-admin', '工单反馈', '工单管理'],
    ['scripts', 'script-admin', '我的脚本', '脚本管理'],
    ['billing-records', 'billing-admin', '我的账单', '账单明细'],
    // 令牌：用户侧只列自己的（我的令牌），管理侧列全站（令牌管理）。
    ['my-tokens', 'token-admin', '我的令牌', '令牌管理'],
  ];

  for (const [userKey, adminKey, userLabel, adminLabel] of pairs) {
    // ① 两侧 key 互不相同（这就是「拆开」的定义）。
    assert.notEqual(userKey, adminKey, `双入口必须拆成两个 key：${userKey}`);

    // ② 用户端 key 只在 USER_MENU_ITEMS，管理端 key 只在 ADMIN_MENU_ITEMS。
    assert.ok(USER_BY_KEY.has(userKey), `${userKey} 必须在 USER_MENU_ITEMS`);
    assert.ok(ADMIN_BY_KEY.has(userKey), `${userKey} 也应在 ADMIN_MENU_ITEMS（shared 分区要用）`);
    assert.ok(ADMIN_BY_KEY.has(adminKey), `${adminKey} 必须在 ADMIN_MENU_ITEMS`);
    assert.ok(!USER_BY_KEY.has(adminKey), `${adminKey} 是管理端专属，不该出现在 USER_MENU_ITEMS`);

    // ③ 文案字面量钉住（防止两侧同时被改成同一个错字）。
    assert.equal(labelOf(USER_BY_KEY.get(userKey)), userLabel, `${userKey} 用户文案漂移`);
    assert.equal(labelOf(ADMIN_BY_KEY.get(adminKey)), adminLabel, `${adminKey} 管理文案漂移`);

    // ④ 两个路由都必须存在，且页面标题与各自菜单文案一致。
    //
    // 注意：标题**允许**是 `isAdmin ? '工单管理' : '工单反馈'` 这种动态形式 ——
    // 新模型下 `isAdmin` 已由 route.meta.viewMode 驱动（不是裸角色判断），
    // 所以同一个组件在两个入口下会渲染出各自正确的标题。
    // 这里断言的是「标题的两个分支覆盖了两侧文案」，而不是「标题必须是静态」。
    for (const [key, expected] of [[userKey, userLabel], [adminKey, adminLabel]]) {
      const heading = HEADINGS.get(key);
      assert.ok(heading, `${key} 没有页面标题信息`);
      assert.notEqual(heading.kind, 'no-route', `${key} 在 router.js 里找不到路由`);
      if (heading.kind === 'static') {
        assert.equal(heading.value, expected,
          `${key} 页面标题应为「${expected}」，实际「${heading.value}」。文件：${heading.file}`);
      } else {
        // 动态标题：两侧文案都必须出现在分支里（顺序为 isAdmin 真/假）。
        assert.ok(Array.isArray(heading.branches), `${key} 的动态标题解析不出分支：${heading.file}`);
        assert.ok(heading.branches.includes(expected),
          `${key} 的动态标题分支 ${JSON.stringify(heading.branches)} 里没有「${expected}」。文件：${heading.file}`);
      }
    }
  }

  // 反向自检：拆开的三对必须让 ADMIN_MENU_ITEMS 里同时存在两组 key，
  // 否则「普通用户可见」分区会少掉用户端入口（那正是第一次修法失败的原因）。
  for (const [userKey, adminKey] of pairs) {
    assert.ok(ADMIN_BY_KEY.has(userKey) && ADMIN_BY_KEY.has(adminKey),
      `${userKey} / ${adminKey} 必须同时出现在 ADMIN_MENU_ITEMS，否则管理员侧少一个入口`);
  }
});

test('四个同名入口的当前状态被显式钉住', () => {
  const sameName = {
    profile: '个人中心',
    // user-usage 是第 8 轮修的：标题从「账户使用」改成与菜单一致的「个人使用」。
    'user-usage': '个人使用',
    'recharge-orders': '充值记录',
    // access-tokens 原来在这里（两侧同名「令牌管理」）。2026-11-14 按用户诉求
    // 拆成 my-tokens（我的令牌）/ token-admin（令牌管理）双入口后，它不再
    // 「两侧同名」，改由下面的双入口用例断言 —— 继续留在这里会互相矛盾。
  };
  for (const [key, label] of Object.entries(sameName)) {
    assert.equal(labelOf(ADMIN_BY_KEY.get(key)), label, `${key} 管理员文案漂移`);
    assert.equal(labelOf(USER_BY_KEY.get(key)), label, `${key} 用户文案漂移`);
    const heading = HEADINGS.get(key);
    assert.equal(heading.kind, 'static', `${key} 两侧同名，标题应为静态字符串`);
    assert.equal(heading.value, label, `${key} 页面标题与菜单不一致：${heading.file}`);
  }
});

test('user-usage 的菜单文案在两侧相同，且与页面标题同名（本轮修复的回归钉子）', () => {
  // 用户原话的第三处：「菜单叫个人使用，点进去标题是账户使用」。
  // 这条单独钉一次，失败信息直接指出应该叫什么，避免下游只看到一句正则不匹配。
  assert.equal(labelOf(ADMIN_BY_KEY.get('user-usage')), '个人使用');
  assert.equal(labelOf(USER_BY_KEY.get('user-usage')), '个人使用');
  const heading = HEADINGS.get('user-usage');
  assert.equal(heading.kind, 'static', 'user-usage 两侧同名，不该按角色区分标题');
  assert.equal(
    heading.value,
    '个人使用',
    `user-usage 页面标题应为「个人使用」（与两侧菜单一致），实际是「${heading.value}」。文件：${heading.file}`,
  );
  assert.notEqual(
    heading.value,
    '账户使用',
    'user-usage 的旧标题「账户使用」会让用户以为点错了入口',
  );
});
