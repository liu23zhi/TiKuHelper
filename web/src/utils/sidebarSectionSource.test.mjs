/* 侧边栏两个分区（task_doc/123）的契约测试。
 *
 * 为什么读源码而不 mount：本 worktree 的 web/node_modules 是空壳（0 个顶层目录），
 * 无法 require vue / ant-design-vue，任何 mount 型测试都跑不起来。
 * 所以这里做两层断言：
 *   1) 结构层 —— 直接读 SiderMenu/index.vue 源码，钉住模板里「有标题才渲染分区」的结构；
 *   2) 行为层 —— 把源码里 sidebar-section-derivation 标记之间的原文截出来**真实执行**，
 *      而不是在测试里手抄一份等价逻辑（手抄会让测试与实现脱钩，变异永远绿）。
 *
 * 数字全部从源码派生（ADMIN_MENU_ITEMS.length / USER_MENU_ITEMS），不写死 27 / 9：
 * 并行开发会往菜单里加项，写死数字等于给自己埋一个必红的钉子。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const testDir = dirname(fileURLToPath(import.meta.url));
const menuPath = resolve(testDir, '../components/SiderMenu/index.vue');
const source = readFileSync(menuPath, 'utf8');

const REGION_START = '// >>> sidebar-section-derivation';
const REGION_END = '// <<< sidebar-section-derivation';

/** 统计某个标记串在源码里出现的次数（不用 split，避免把串吃掉）。 */
function countOccurrences(haystack, needle) {
  let count = 0;
  for (let at = haystack.indexOf(needle); at !== -1; at = haystack.indexOf(needle, at + needle.length)) count += 1;
  return count;
}

/**
 * 标记必须存在且**各只出现一次**。
 *
 * 这条不是形式主义：下面 evaluateDerivation() 靠 indexOf 截取标记之间的原文再 new Function 执行。
 * 一旦标记被复制一份（重构时整段拷贝）、或被搬到别处，indexOf 只会取到**第一处**，
 * 于是测试执行的是那份已经不生效的旧副本，而运行时真正用的是另一份 —— 二者脱钩后面所有断言都会失真。
 * 更糟的形态是标记被删但恰好截出空串：空函数返回 undefined，部分断言可能"恰好"通过（静默变绿）。
 * 所以把「标记本身的完整性」也变成可红的判据，而不是依赖 indexOf === -1 的偶然行为。
 */
function assertMarkersIntact() {
  assert.equal(countOccurrences(source, REGION_START), 1, `${REGION_START} 必须恰好出现一次`);
  assert.equal(countOccurrences(source, REGION_END), 1, `${REGION_END} 必须恰好出现一次`);
  const start = source.indexOf(REGION_START);
  const end = source.indexOf(REGION_END);
  assert.ok(end > start, '结束标记必须在起始标记之后（顺序不能颠倒）');
  return { start, end };
}

/** 从源码里截出某个 `const NAME = [ ... ]` 数组字面量的原文。 */
function arrayLiteral(name) {
  const match = new RegExp(`const ${name} = \\[([\\s\\S]*?)\\n\\]`).exec(source);
  assert.ok(match, `源码里找不到 const ${name} = [...]`);
  return match[1];
}

/** 把数组字面量原文求值成真实对象数组。图标是组件引用，这里统一置 null。 */
function evaluateItems(name) {
  const body = arrayLiteral(name).replace(/icon:\s*[A-Za-z_$][\w$]*/g, 'icon: null');
  // eslint-disable-next-line no-new-func
  return new Function(`return [${body}]`)();
}

/** 执行源码里「分区派生」那一段原文，返回它导出的东西。 */
function evaluateDerivation(adminItems, userItems) {
  const { start, end } = assertMarkersIntact();
  // 起始标记之后同一行还有说明文字，从下一个换行开始截，否则会把说明当成代码执行。
  const regionStart = source.indexOf('\n', start);
  assert.ok(regionStart > start, 'sidebar-section-derivation 起始标记后必须换行');
  const region = source.slice(regionStart + 1, end);
  // 截出的空串/半截片段会让 new Function 变成空函数，静默返回 undefined —— 必须显式挡住。
  assert.ok(region.trim().length > 0, '派生区段截出来是空串（标记都在但没有内容）');
  assert.ok(region.includes('ADMIN_MENU_SECTIONS'), '派生区段里没有 ADMIN_MENU_SECTIONS');
  assert.ok(region.includes('USER_MENU_SECTIONS'), '派生区段里没有 USER_MENU_SECTIONS');
  // eslint-disable-next-line no-new-func
  const run = new Function(
    'ADMIN_MENU_ITEMS',
    'USER_MENU_ITEMS',
    `${region}
    return { USER_MENU_KEYS, ADMIN_SECTION_TITLES, ADMIN_MENU_SECTIONS, USER_MENU_SECTIONS };`,
  );
  let result;
  try {
    result = run(adminItems, userItems);
  } catch (error) {
    // 别让裸 ReferenceError 直接冒出来：报错必须指向「区段被改坏」，否则下游只会看到
    // 'USER_MENU_KEYS is not defined' 这种看不出根因的信息。
    assert.fail(`派生区段执行失败（标记还在但内容已不是有效代码）：${error.message}`);
  }
  // 执行成功但返回值残缺同样是静默失败，逐个钉住。
  for (const name of ['USER_MENU_KEYS', 'ADMIN_SECTION_TITLES', 'ADMIN_MENU_SECTIONS', 'USER_MENU_SECTIONS']) {
    assert.ok(result?.[name] !== undefined, `派生区段没有产出 ${name}（标记或内容被改坏了）`);
  }
  assert.ok(Array.isArray(result.ADMIN_MENU_SECTIONS), 'ADMIN_MENU_SECTIONS 必须是数组');
  return result;
}

const ADMIN_MENU_ITEMS = evaluateItems('ADMIN_MENU_ITEMS');
const USER_MENU_ITEMS = evaluateItems('USER_MENU_ITEMS');
const { ADMIN_SECTION_TITLES, ADMIN_MENU_SECTIONS, USER_MENU_SECTIONS } =
  evaluateDerivation(ADMIN_MENU_ITEMS, USER_MENU_ITEMS);

const keysOf = (items) => items.map((item) => item.key);
const sorted = (list) => [...list].sort();
const adminKeys = keysOf(ADMIN_MENU_ITEMS);
const userKeys = keysOf(USER_MENU_ITEMS);

/** 分区标题必须是「有内容」的字符串 —— 空字符串会让 v-if 走进无标题分支，等于没做分区。 */
const titledSections = ADMIN_MENU_SECTIONS.filter((section) => section.title);

/** 管理员侧「普通用户可见」组。 */
const sharedSection = ADMIN_MENU_SECTIONS.find((section) => section.key === 'shared');
/** 管理员侧「仅管理员可见」组。 */
const adminOnlySection = ADMIN_MENU_SECTIONS.find((section) => section.key === 'admin-only');

test('派生区段标记存在且各只出现一次（标记被删/被复制会让行为断言静默失真）', () => {
  const { start, end } = assertMarkersIntact();
  assert.ok(start >= 0 && end > start);

  // 标记必须在 <script> 里（不能在模板/样式里），否则截出来的不是可执行 JS。
  const scriptStart = source.indexOf('<script>');
  const scriptEnd = source.indexOf('</script>');
  assert.ok(scriptStart >= 0 && scriptEnd > scriptStart, '源码缺少 <script> 块');
  assert.ok(start > scriptStart && end < scriptEnd, '派生区段必须整体位于 <script> … </script> 之间');

  // 区段必须真的包住两个分区定义，而不是夹在中间某处。
  const region = source.slice(start, end);
  assert.match(region, /const ADMIN_MENU_SECTIONS = \[/);
  assert.match(region, /const USER_MENU_SECTIONS = \[/);
  // 派生必须基于 USER_MENU_KEYS 过滤（这是「不手抄」的唯一判据）。
  assert.match(region, /ADMIN_MENU_ITEMS\.filter\(\(item\) =>\s+USER_MENU_KEYS\.has\(item\.key\)\)/);
  assert.match(region, /ADMIN_MENU_ITEMS\.filter\(\(item\) =>\s+!USER_MENU_KEYS\.has\(item\.key\)\)/);
  // 手抄清单的特征：区段里出现字面量对象数组给分区赋值（而不是 filter 派生）。
  assert.doesNotMatch(region, /items:\s*\[\s*\{/, '分区 items 不能是手写字面量数组，必须 filter 派生');
});

test('管理员侧边栏有且只有两个带标题的分区，标题文案是用户决策原文', () => {
  assert.equal(ADMIN_MENU_SECTIONS.length, 2, '管理员必须恰好两个分区');
  assert.equal(titledSections.length, 2, '两个分区都必须有非空标题（标题为空 = 退化成无标题列表）');

  const titles = titledSections.map((section) => section.title);
  assert.deepEqual(titles, ['普通用户可见', '仅管理员可见']);
  // 字面量同时钉住常量表，避免 ADMIN_SECTION_TITLES 被改名后模板与数据一起漂移。
  assert.equal(ADMIN_SECTION_TITLES.shared, '普通用户可见');
  assert.equal(ADMIN_SECTION_TITLES.adminOnly, '仅管理员可见');
  assert.notEqual(titles[0], titles[1], '两个分区标题不能相同，否则用户分不清');
});

test('两个分区合计与 ADMIN_MENU_ITEMS 完全一致：不增、不减、不重', () => {
  const combined = [...sharedSection.items, ...adminOnlySection.items];

  // 判据①：合计条数 = ADMIN_MENU_ITEMS.length（不写死 27，允许并行新增菜单项）。
  assert.equal(combined.length, ADMIN_MENU_ITEMS.length);

  // 不增不减：key 集合相等（不是子集、也不是超集）。
  assert.deepEqual(sorted(keysOf(combined)), sorted(adminKeys));

  // 不重：两个分区之间没有重复 key。
  const duplicates = keysOf(sharedSection.items).filter((key) => keysOf(adminOnlySection.items).includes(key));
  assert.deepEqual(duplicates, [], `分区之间出现重复 key: ${duplicates.join(', ')}`);

  // 组内保持 ADMIN_MENU_ITEMS 的相对顺序（filter 是稳序的，不得重排或倒序）。
  // 注意：分组合并后整体顺序必然与 ADMIN_MENU_ITEMS 不同（普通项被提到前面），这是分组的目的，
  // 所以这里只钉「各自相对顺序」，不钉拼接后的全局顺序。
  for (const section of ADMIN_MENU_SECTIONS) {
    const sectionKeys = keysOf(section.items);
    const preservedOrder = adminKeys.filter((key) => sectionKeys.includes(key));
    assert.deepEqual(sectionKeys, preservedOrder, `分区 ${section.key} 打乱了 ADMIN_MENU_ITEMS 的相对顺序`);
  }

  // 两组首尾相接即完整覆盖：既不能有项既不在前组也不在后组，也不能有项同时进两组。
  assert.equal(new Set(combined.map((item) => item.key)).size, ADMIN_MENU_ITEMS.length);
});

test('「普通用户可见」组与 USER_MENU_ITEMS 集合相等（不是子集），且从 USER_MENU_ITEMS 派生', () => {
  const sharedKeys = keysOf(sharedSection.items);

  // 判据④：集合相等。用交集表述是因为两个数组本身不是同一集合：
  // USER_MENU_ITEMS 里有 my-referral / review-rebate 两条普通用户专属路由，
  // 它们不在 ADMIN_MENU_ITEMS 里（管理员通过「赠送记录」「评价返现管理」看聚合页）。
  // 若写成「shared ⊆ USER_MENU_ITEMS」就漏掉了「管理员组里多塞了非普通项」这一半。
  const expectedShared = adminKeys.filter((key) => userKeys.includes(key));
  assert.deepEqual(sorted(sharedKeys), sorted(expectedShared), 'shared 组必须恰好等于 ADMIN ∩ USER 的 key 集');
  // 反向也要成立：ADMIN 里每一个属于 USER 的 key 都必须真的出现在 shared 组里（不能漏项）。
  for (const key of userKeys) {
    if (adminKeys.includes(key)) {
      assert.ok(sharedKeys.includes(key), `USER_MENU_ITEMS 的 ${key} 在 ADMIN_MENU_ITEMS 里，却没进 shared 组`);
    }
  }
  // 任何多出来的项都必须能在 USER_MENU_ITEMS 里找到 —— 这条是「多塞了普通项之外的东西」的防线。
  const strays = sharedKeys.filter((key) => !userKeys.includes(key));
  assert.deepEqual(strays, [], `shared 组混入了非普通用户可见项: ${strays.join(', ')}`);

  // 管理员侧不得出现「管理员没有该路由」的普通项：USER_MENU_ITEMS 里那两条普通用户专属
  // （我的邀请 / 评价返现）在 router.js 里不是 admin-only 路由，但它们根本不在 ADMIN_MENU_ITEMS 里，
  // 所以 shared 组一个都不能有。这条不会因将来「新增普通用户页面」而误红，只钉真正的泄漏方向。
  for (const exclusive of ['my-referral', 'review-rebate']) {
    assert.ok(!sharedKeys.includes(exclusive), `普通用户专属路由 ${exclusive} 泄漏进了管理员 shared 组`);
    assert.ok(!adminKeys.includes(exclusive), `ADMIN_MENU_ITEMS 不该包含普通用户专属路由 ${exclusive}`);
  }

  // 派生而非手抄：shared 组的 key 与顺序都必须来自 ADMIN_MENU_ITEMS。
  //
  // ⚠️ 这里**不能**再断言「item 是 ADMIN_MENU_ITEMS 里的同一个对象引用」。
  // shared 组的语义是「普通用户可见」，所以文案必须取 USER_MENU_ITEMS 的；
  // 用管理端对象会让分区里出现「工单管理」「账单明细」「脚本管理」——管理员看到的
  // 是自己的视角，而分区标题却写着「普通用户可见」，标题就成了假话。
  // （这是用户实测报出的缺陷，2026-11-13 修。）
  //
  // 「防手抄」这条防线改为断言下列三条，强度不减：
  //   a) key 顺序与 ADMIN_MENU_ITEMS 过滤结果逐项相同（不能重排、不能增删）；
  //   b) 除 label 外其余字段（icon 等）仍来自管理端原对象；
  //   c) label 必须逐项等于 USER_MENU_ITEMS 的文案。
  const expectedSharedOrder = ADMIN_MENU_ITEMS.filter((item) => userKeys.includes(item.key));
  assert.deepEqual(keysOf(sharedSection.items), keysOf(expectedSharedOrder), 'shared 组的 key 顺序必须与管理端过滤结果一致');
  const USER_BY_KEY = new Map(USER_MENU_ITEMS.map((item) => [item.key, item]));
  for (const item of sharedSection.items) {
    const adminItem = ADMIN_MENU_ITEMS.find((candidate) => candidate.key === item.key);
    assert.equal(item.icon, adminItem.icon, `shared 组的 ${item.key} 图标必须来自 ADMIN_MENU_ITEMS 原对象`);
    assert.equal(
      item.label,
      USER_BY_KEY.get(item.key).label,
      `shared 组的 ${item.key} 文案必须是普通用户看到的那个（${USER_BY_KEY.get(item.key).label}），不是管理端的（${adminItem.label}）`,
    );
  }
  // 逐条钉住三处已知的双文案，防止有人把它改回管理端措辞。
  const labelOf = (key) => sharedSection.items.find((item) => item.key === key).label;
  assert.equal(labelOf('tickets'), '工单反馈');
  assert.equal(labelOf('billing-records'), '我的账单');
  assert.equal(labelOf('scripts'), '我的脚本');
  // 对照组：只有这三处不同，其余文案两侧本来就相同，不得被顺手改掉。
  assert.equal(labelOf('profile'), '个人中心');
  assert.equal(labelOf('access-tokens'), '令牌管理');
});

test('「仅管理员可见」组是剩余项，且不含任何普通用户可见项', () => {
  const adminOnlyKeys = keysOf(adminOnlySection.items);

  // 判据①的另一半：剩余 = ADMIN − USER。
  assert.deepEqual(sorted(adminOnlyKeys), sorted(adminKeys.filter((key) => !userKeys.includes(key))));
  assert.equal(adminOnlyKeys.length, ADMIN_MENU_ITEMS.length - sharedSection.items.length);

  // 反向防线：管理员独占组里一个普通项都不能有。
  const leaked = adminOnlyKeys.filter((key) => userKeys.includes(key));
  assert.deepEqual(leaked, [], `管理员独占组混入了普通用户可见项: ${leaked.join(', ')}`);
});

test('普通用户仍只有一个无标题分区：不出现任何分区标题', () => {
  // 判据②：普通用户路径不分组、无标题。
  assert.equal(USER_MENU_SECTIONS.length, 1);
  const [only] = USER_MENU_SECTIONS;
  assert.equal(only.title, '', '普通用户分区不能有标题');
  assert.deepEqual(titledSectionsOf(USER_MENU_SECTIONS), []);
  assert.equal(only.items, USER_MENU_ITEMS, '普通用户分区必须直接用 USER_MENU_ITEMS，不做任何重排或过滤');
  assert.deepEqual(keysOf(only.items), userKeys);

  function titledSectionsOf(sections) {
    return sections.filter((section) => section.title);
  }
});

test('模板按标题决定是否渲染分区标题（缺标题即退化为扁平列表）', () => {
  // 注意不能找第一个 '</template>'：模板内部还有 <template #title> 与 v-else 的嵌套模板。
  const templateStart = source.indexOf('<template>');
  const templateEnd = source.indexOf('</template>\n<script');
  assert.ok(templateStart >= 0, '源码缺少 <template> 块');
  assert.ok(templateEnd > templateStart, '源码缺少 <template>…</template> 到 <script> 的边界');
  const template = source.slice(templateStart, templateEnd);

  // 只有一个 v-for 遍历 menuSections，菜单项由分区驱动渲染。
  assert.match(template, /v-for="section in menuSections" :key="section\.key"/);

  // 分区容器受 section.title 守卫：标题为空时不会渲染空标题节点。
  assert.match(template, /<a-menu-item-group v-if="section\.title"/);
  assert.equal((template.match(/<a-menu-item-group/g) || []).length, 1, '模板里应恰好一个分区容器');
  assert.match(template, /<template #title>\s*<span class="menu-section-title">\{\{ section\.title \}\}<\/span>/);

  // 无标题分支必须存在，否则普通用户会看到空分区壳。
  assert.match(template, /v-else[\s\S]*?v-for="item in section\.items"/);

  // 分区必须真的挂在菜单里，且分区标题与菜单项仍然只有一套渲染逻辑（两处 a-menu-item 同构）。
  const itemOccurrences = (template.match(/<a-menu-item v-for="item in section\.items"/g) || []).length;
  assert.equal(itemOccurrences, 2, '两个分支各渲染一次菜单项');
  assert.match(template, /:href="'\/adapter\/' \+ item\.key"/);

  // 分区标题有样式（标题不是裸文本，且折叠态隐藏）。
  const style = source.slice(source.indexOf('<style'));
  assert.match(style, /\.menu-section-title|\.ant-menu-item-group-title/);
  assert.match(style, /\.menu\.ant-menu-inline-collapsed :deep\(\.ant-menu-item-group-title\) \{ display: none; \}/);

  // 标题常量必须被模板可达（setup 暴露 menuSections）。
  assert.match(source, /return \{[\s\S]*menuSections,[\s\S]*\}/);
});

test('分区不影响权限判定与选中态：仍以 visibleMenuItems 为准', () => {
  // 红线：不动 isAdminUser 那套权限判定；分区只是渲染分组。
  assert.match(source, /const isAdmin = computed\(\(\) => isAdminUser\(props\.currentUser\)\)/);
  assert.match(source, /const visibleMenuItems = computed\(\(\) => \(isAdmin\.value \? ADMIN_MENU_ITEMS : USER_MENU_ITEMS\)\)/);
  assert.match(source, /const menuSections = computed\(\(\) => \(isAdmin\.value \? ADMIN_MENU_SECTIONS : USER_MENU_SECTIONS\)\)/);

  // 选中态 / 点击 / watch 仍读 visibleMenuItems，不读分区（否则分区一变选中就丢）。
  const setup = source.slice(source.indexOf('const syncSelectedKeys'), source.indexOf('const handleSelect'));
  assert.match(setup, /visibleMenuItems\.value/);
  assert.doesNotMatch(setup, /menuSections/);
  const clickHandler = source.slice(source.indexOf('const handleSelect'), source.indexOf('const handleLinkClick'));
  assert.match(clickHandler, /visibleMenuItems\.value\.some/);
  assert.doesNotMatch(clickHandler, /menuSections/);

  // 普通用户的可见项集合 = 分区渲染出来的项集合；管理员的可见项集合 = 两个分区的并集。
  const userRendered = USER_MENU_SECTIONS.flatMap((section) => keysOf(section.items));
  assert.deepEqual(userRendered, userKeys);
  // 管理员侧只比集合（分组会把普通项提前，顺序按设计会变；顺序另有「组内相对顺序」用例钉住）。
  const adminRendered = ADMIN_MENU_SECTIONS.flatMap((section) => keysOf(section.items));
  assert.deepEqual(sorted(adminRendered), sorted(adminKeys));
});
