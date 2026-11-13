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
  const routeRe = /\{\s*path:\s*'[^']*',\s*name:\s*'([^']+)',\s*component:\s*([A-Za-z_$][\w$]*)/g;
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

// 每个共用 key 的页面标题信息只算一次。
const HEADINGS = new Map(SHARED_KEYS.map((key) => [key, readPageHeading(key)]));

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

test('判据②：共用页面的标题必须按角色切换，或与两侧共同文案完全同名', () => {
  const violations = [];

  for (const key of SHARED_KEYS) {
    const adminLabel = labelOf(ADMIN_BY_KEY.get(key));
    const userLabel = labelOf(USER_BY_KEY.get(key));
    const labelsDiffer = adminLabel !== userLabel;
    const heading = HEADINGS.get(key);

    if (heading.kind === 'static') {
      // 静态标题：只有当两侧文案本来就相同才被允许，且必须就是那个文案。
      if (labelsDiffer) {
        violations.push(
          `${key} 的页面标题写死成「${heading.value}」，但两侧菜单文案不同`
          + `（管理员「${adminLabel}」/ 用户「${userLabel}」）—— 普通用户会看到管理员措辞。`
          + ` 文件：${heading.file}`,
        );
        continue;
      }
      if (heading.value !== userLabel) {
        violations.push(
          `${key} 的页面标题是「${heading.value}」，但两侧菜单都叫「${userLabel}」——`
          + ` 用户从菜单点进来会以为点错了。文件：${heading.file}`,
        );
      }
      continue;
    }

    // 动态标题：必须真的能按 isAdmin 分出两支，且两支文案与两侧菜单逐一对齐。
    if (!heading.branches) {
      violations.push(
        `${key} 的标题是动态表达式 \`:title="${heading.expr}"\`，但解析不出 isAdmin 三元分支，`
        + ` 无法证明它会按角色切换。文件：${heading.file}`,
      );
      continue;
    }
    if (!labelsDiffer) {
      // 两侧同名却写成动态标题不是错误，但分支必须都是同一个文案（否则某一侧会被叫错名）。
      const [adminBranch, userBranch] = heading.branches;
      if (adminBranch !== userLabel || userBranch !== userLabel) {
        violations.push(
          `${key} 两侧菜单都叫「${userLabel}」，页面却按角色显示「${adminBranch}」/「${userBranch}」。`
          + ` 文件：${heading.file}`,
        );
      }
      continue;
    }
    const [adminBranch, userBranch] = heading.branches;
    // 顺序也是契约：`isAdmin ? A : B` 里 A 是管理员文案、B 是用户文案。写反了就是"管理员看到用户措辞"。
    if (adminBranch !== adminLabel || userBranch !== userLabel) {
      violations.push(
        `${key} 的标题分支与菜单文案对不上：页面显示「${adminBranch}」/「${userBranch}」，`
        + `菜单是「${adminLabel}」/「${userLabel}」（顺序为 isAdmin 真 / 假）。文件：${heading.file}`,
      );
    }
  }

  assert.deepEqual(violations, [], `双角色文案不一致：\n- ${violations.join('\n- ')}`);
});

test('判据③（反向防线）：所有静态标题都必须落在「两侧同名」的 key 集合里', () => {
  // 判据② 是「已登记的共用 key 逐个检查」，本判据反过来扫：**任何**页面只要是静态标题，
  // 它对应的 key 就不允许是「两侧文案不同」的。两条一起，新增页面无论有没有进我的清单都会被拦。
  //
  // 扫的对象是 router.js 派生出来的全部路由，不是菜单列表 ——
  // 菜单漏登记一个新页面时，这条依然能看到它。
  const differingKeys = new Set(
    [...ADMIN_BY_KEY.keys()].filter((key) => {
      const userItem = USER_BY_KEY.get(key);
      return userItem && labelOf(userItem) !== labelOf(ADMIN_BY_KEY.get(key));
    }),
  );
  const sameNameKeys = new Set(
    [...ADMIN_BY_KEY.keys()].filter((key) => {
      const userItem = USER_BY_KEY.get(key);
      return userItem && labelOf(userItem) === labelOf(ADMIN_BY_KEY.get(key));
    }),
  );

  const violations = [];
  for (const [key, heading] of HEADINGS) {
    if (heading.kind !== 'static') continue;
    if (differingKeys.has(key)) {
      violations.push(
        `${key}：菜单文案两侧不同（管理员「${labelOf(ADMIN_BY_KEY.get(key))}」/ 用户「${labelOf(USER_BY_KEY.get(key))}」），`
        + `但页面标题是静态的「${heading.value}」。文件：${heading.file}`,
      );
    }
    if (!sameNameKeys.has(key) && !differingKeys.has(key)) {
      // 共用 key 必然落进上面两个集合之一；落到这里说明菜单/路由数据本身不一致。
      violations.push(`${key} 既不在「两侧同名」也不在「两侧不同名」集合里，菜单数据异常`);
    }
  }
  assert.deepEqual(violations, [], `静态标题落在需要区分的 key 上：\n- ${violations.join('\n- ')}`);

  // 反向自检：本仓当前确实存在「两侧不同名」的 key，否则这条判据就成了空转（永远绿的假钉子）。
  assert.ok(differingKeys.size >= 3, `「两侧不同名」的 key 只剩 ${differingKeys.size} 个，判据③可能已空转`);
  // 且这些 key 的页面标题都必须是动态的（判据② 已逐个断言，这里再钉一次集合级别的事实）。
  for (const key of differingKeys) {
    if (!SHARED_KEYS.includes(key)) continue;
    assert.equal(
      HEADINGS.get(key).kind,
      'dynamic',
      `${key} 两侧文案不同，页面标题必须是按 isAdmin 切换的动态标题`,
    );
  }
});

test('三个双文案入口与四个同名入口的当前状态被显式钉住', () => {
  // 显式断言「本仓此刻的事实」，任何人改文案都会撞到这条并被迫同步两侧。
  // 注意：这里写**字面量**而不是拿 ADMIN/USER 的 label 互相比 ——
  // 两侧同时被改成同一个错字的漂移，只有字面量拦得住。
  const pairs = {
    tickets: ['工单管理', '工单反馈'],
    'billing-records': ['账单明细', '我的账单'],
    scripts: ['脚本管理', '我的脚本'],
  };
  for (const [key, [adminLabel, userLabel]] of Object.entries(pairs)) {
    assert.equal(labelOf(ADMIN_BY_KEY.get(key)), adminLabel, `${key} 管理员文案漂移`);
    assert.equal(labelOf(USER_BY_KEY.get(key)), userLabel, `${key} 用户文案漂移`);
    const heading = HEADINGS.get(key);
    assert.equal(heading.kind, 'dynamic', `${key} 必须是动态标题`);
    assert.deepEqual(heading.branches, [adminLabel, userLabel], `${key} 标题分支与字面量不一致：${heading.file}`);
  }

  const sameName = {
    profile: '个人中心',
    // user-usage 是本轮修的：标题从「账户使用」改成与菜单一致的「个人使用」。
    'user-usage': '个人使用',
    'recharge-orders': '充值记录',
    'access-tokens': '令牌管理',
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
