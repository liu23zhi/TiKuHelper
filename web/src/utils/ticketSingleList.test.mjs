/* 工单列表「单列表」契约（t202）。
 *
 * 用户诉求原话：「所有工单都显示在 工单列表 ，不再单独提供退款工单等工单的
 * 显示按钮，直接默认显示在工单列表。」
 *
 * ── 已核实的事实（决定本测试为什么这样写）─────────────────────────
 *
 *  1. 页面里**没有** tab / segmented / button-group 形式的类型切换器；
 *     唯一「按类型单独提供显示按钮」是页头的「退款工单」按钮
 *     （v-if=canUseTickets + toggleRefunds），它 toggle 出 TicketRefundPanel。
 *
 *  2. 该面板是**全站唯一的退款申请/审核入口**（`git grep TicketRefundPanel`
 *     只有一处引用；`/ticket-refunds` 系列 9 条接口也只被它调用）。
 *     删掉按钮 ≠ 删掉功能，但删掉面板 = 丢失真实能力。
 *
 *  3. 退款记录在独立表 `ticket_refunds`（`entity/ticket_refund.go:17` 注释
 *     「separate from general-purpose tickets」，**无 ticket_id 外键**），
 *     与 `/tickets` 列表是两套数据源 —— 所以「把退款单并进工单列表」在数据层
 *     不成立。而且 `category=refund`（"退款申请"）**从未被任何代码写入过**
 *     （全仓只有常量定义 / 白名单 / 优先级表 / 测试）。
 *
 *  ⇒ 因此本测试的立场是：
 *      · 钉住「按类型的独立入口按钮已消失」（用户要的）
 *      · **同时**钉住「退款功能仍然可达」（防止拿"删功能"冒充"删按钮"）
 *
 * ── 为什么「只断言按钮不存在」是假钉子 ────────────────────────────
 *
 * 如果只有 `assert.doesNotMatch(template, /toggleRefunds/)`，那么把整个
 * `<TicketRefundPanel>` 连同区块一起删掉，测试依然全绿 —— 于是"删掉退款功能"
 * 会被当成"完成了需求"。所以下面必须有**正向**断言：面板存在、区块有标题、
 * 6 个动作入口的文案都在。变异 1（删整块面板）必须让这些正向断言变红。
 *
 * 为什么用源码断言而不是 mount：本仓单跑 mount 型用例成本高且易受环境干扰；
 * 这类"某控件/某文案是否存在"的契约用源码断言更稳，失败信息也更直接。
 */
import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

import { buildTicketListParams } from "./ticketHelpers.mjs";

const testDir = dirname(fileURLToPath(import.meta.url));
const readSrc = (relative) => readFile(resolve(testDir, "..", relative), "utf8");

const pagePath = "pages/Tickets/index.vue";
const panelPath = "pages/Tickets/TicketRefundPanel.vue";

/** 取 <template>…<script 之间的内容（不含 script，避免把 JS 里的字符串当模板断言）。 */
function templateOf(source) {
  const start = source.indexOf("<template>");
  const scriptAt = source.indexOf("\n<script");
  assert.ok(start >= 0, "Tickets 页面缺少 <template>");
  assert.ok(scriptAt > start, "Tickets 页面缺少 <template>…<script> 边界");
  return source.slice(start, scriptAt);
}

function scriptOf(source) {
  const start = source.indexOf("<script");
  const end = source.indexOf("</script>", start);
  assert.ok(start >= 0 && end > start, "Tickets 页面缺少 <script> 块");
  return source.slice(start, end);
}

/** 取退款区块那一段模板（从 data-section="ticket-refund" 到它的 </section>）。 */
function refundSectionOf(template) {
  const start = template.indexOf('data-section="ticket-refund"');
  assert.ok(start >= 0, "找不到 data-section=\"ticket-refund\" 的退款区块");
  // 往回找到包含它的 <section 开始标签
  const tagStart = template.lastIndexOf("<section", start);
  assert.ok(tagStart >= 0, "退款区块外没有 <section> 容器");
  const end = template.indexOf("</section>", tagStart);
  assert.ok(end > tagStart, "退款区块缺少 </section> 结束标签");
  return template.slice(tagStart, end);
}

// ─────────────────────── 正向判据：功能仍然可达 ───────────────────────

test("退款区块常驻且有明确分界（标题 / 说明 / 稳定标记）", async () => {
  const template = templateOf(await readSrc(pagePath));
  const section = refundSectionOf(template);

  // 稳定标记：测试与将来迁移都靠它定位，不靠 DOM 结构猜。
  assert.match(template, /data-section="ticket-refund"/, "退款区块必须有 data-section 稳定标记");

  // 分界要求在 captain 复核时明确追加：不能静默铺一块面板在列表下面。
  assert.match(section, /退款申请与审核/, "退款区块必须有可读标题「退款申请与审核」");
  assert.match(
    section,
    /退款走独立流程，不进入上方工单列表/,
    "退款区块必须有一句说明，明确它不进工单列表（否则用户分不清两块数据）",
  );
  // 标题要能被读屏关联（aria-labelledby + 对应 id）。
  assert.match(section, /aria-labelledby="ticket-refund-heading"/, "区块应通过 aria-labelledby 关联标题");
  assert.match(section, /id="ticket-refund-heading"/, "标题必须有对应 id");

  // 面板本身必须在这块里（不是被挪到别处，也不是被删掉）。
  assert.match(section, /<TicketRefundPanel/, "退款面板必须渲染在这个有分界的区块内");
  // 常驻：不再由「是否点开」的开关控制。
  assert.doesNotMatch(section, /refundsVisible/, "退款区块不应再受「是否点开」的状态控制");
});

test("退款面板的 6 个动作入口都仍然存在（删功能不能冒充删按钮）", async () => {
  const template = templateOf(await readSrc(pagePath));
  const section = refundSectionOf(template);
  const panel = await readSrc(panelPath);

  // 面板必须真的被渲染 —— 否则下面所有"功能可达"都是空谈。
  assert.match(section, /<TicketRefundPanel/, "退款面板必须仍然渲染");
  assert.match(section, /:is-admin="isAdmin"/, "面板必须仍按角色区分用户/审核者视图");
  assert.match(section, /:capture-owner="capturePage"/, "面板必须仍受页面 owner 生命周期约束");

  // 6 个动作入口（文案级断言，删掉任一入口都会红）：
  //   1. 提交申请（用户）2. 审核通过（管理员）3. 驳回（管理员）
  //   4. 提交原路退款（管理员）5. 撤销未提交退款并解冻（管理员）6. 记录打款（管理员）
  const entries = [
    [/提交人工退款申请|提交原路退款申请/, "提交退款申请（用户侧）"],
    [/审核通过/, "审核通过（管理员）"],
    [/驳回/, "驳回（管理员）"],
    [/提交原路退款/, "提交原路退款（管理员）"],
    [/撤销未提交退款并解冻/, "撤销未提交退款并解冻（管理员）"],
    [/记录打款/, "记录打款/对账（管理员）"],
  ];
  for (const [pattern, label] of entries) {
    assert.match(
      panel,
      pattern,
      `退款功能缺失：找不到「${label}」入口。删掉退款面板会让本测试与"无按钮"断言一起变绿——所以这条必须存在`,
    );
  }

  // 用户侧的凭证上传与查看也必须还在（这是退款申请的必要环节）。
  assert.match(panel, /上传收款码或凭证/, "用户侧上传凭证入口必须保留");
  assert.match(panel, /查看受限凭证/, "凭证查看入口必须保留");

  // 角色切换必须仍生效（reviewer = props.isAdmin），否则普通用户会看到审核按钮。
  assert.match(panel, /const reviewer = props\.isAdmin/, "面板仍必须以 isAdmin 决定是否显示审核动作");
});

test("用户视图（viewMode='self'）的功能不因删除入口按钮而退化", async () => {
  const source = await readSrc(pagePath);
  const template = templateOf(source);
  const script = scriptOf(source);

  // 视图模式仍由 route.meta.viewMode 决定（2026-11-14 的改动，不能回退）。
  assert.match(script, /meta\??\.viewMode/, "必须仍由 route.meta.viewMode 决定视图模式");
  assert.match(script, /"self"|'self'/, "必须仍识别 viewMode === 'self'");
  assert.match(script, /"admin"|'admin'/, "必须仍识别 viewMode === 'admin'");

  // 用户侧建单入口保留（仅非管理员显示）。
  assert.match(
    template,
    /v-if="canUseTickets\(\) && !isAdmin"[\s\S]{0,80}提交工单/,
    "用户视图的「提交工单」入口必须保留",
  );

  // 退款区块对用户也可见（canUseTickets 只校验登录+启用，不要求管理员）。
  const section = refundSectionOf(template);
  assert.match(
    section,
    /v-if="canUseTickets\(\)"/,
    "退款区块必须对普通用户可见（否则用户就失去了退款申请入口）",
  );
  assert.doesNotMatch(
    section,
    /v-if="canManage\(\)"/,
    "退款区块不能收窄成仅管理员可见——那会让普通用户无法申请退款",
  );

  // 管理员专属区块（提醒设置）的守卫不变。
  assert.match(template, /v-if="canManage\(\) && configLoaded"/, "管理员提醒设置区块守卫不应改变");

  // 列表数据源未变。
  assert.match(script, /getTickets\(buildTicketListParams\(filters, pagination\.current, pagination\.pageSize\)\)/);
});

// ─────────────────────── 反向判据：按类型入口按钮已移除 ───────────────────────

test("页头不再有按工单类型单独显示的按钮（退款工单 toggle 已移除）", async () => {
  const source = await readSrc(pagePath);
  const template = templateOf(source);
  const script = scriptOf(source);

  // 模板里不得再有 toggle 型分类按钮的状态与处理器。
  assert.doesNotMatch(template, /refundsVisible/, "模板里不应再有「退款工单」按钮的展开状态");
  assert.doesNotMatch(template, /toggleRefunds/, "模板里不应再有切换退款面板的按钮处理器");
  assert.doesNotMatch(template, /收起退款工单/, "模板里不应再出现「收起退款工单」这句按钮文案");
  // 脚本里也不得残留（残留 = 死代码，且按钮随时可能被加回来）。
  assert.doesNotMatch(script, /refundsVisible/, "脚本里不应残留 refundsVisible 状态");
  assert.doesNotMatch(script, /function toggleRefunds/, "脚本里不应残留 toggleRefunds 处理器");

  // 页头操作区恰好 2 个按钮：刷新 + 提交工单。
  // 这条独立于上面的字符串断言：即使有人用别的名字塞回第三个按钮，这里也会红。
  const headingStart = template.indexOf("<ConsolePageHeading");
  const headingEnd = template.indexOf("</ConsolePageHeading>");
  assert.ok(headingStart >= 0 && headingEnd > headingStart, "找不到 ConsolePageHeading 区块");
  const heading = template.slice(headingStart, headingEnd);
  const buttonCount = [...heading.matchAll(/<a-button\b/g)].length;
  assert.equal(
    buttonCount,
    2,
    `页头操作区应恰好 2 个按钮（刷新 / 提交工单），实际 ${buttonCount} 个 —— 多出来的很可能就是被删掉的按类型入口`,
  );
  assert.match(heading, />刷新</, "页头必须保留「刷新」");
  assert.match(heading, />提交工单</, "页头必须保留「提交工单」（用户视图建单入口）");

  // 不要用"加一个 Tab 来分类"绕过用户诉求。
  for (const control of ["<a-tabs", "<a-tab-pane", "<a-segmented", "<a-button-group"]) {
    assert.equal(
      template.includes(control),
      false,
      `工单页不应出现按类型切换的控件 ${control}（所有工单在同一个列表里）`,
    );
  }
});

test("工单列表默认请求不带类型过滤（零值 = 全部，后端语义已核实）", () => {
  // 默认筛选：全是空串（clearOwned 里 Object.assign 的初值）。
  const defaults = {
    keyword: "",
    status: "",
    category: "",
    createdFrom: "",
    createdTo: "",
    updatedFrom: "",
    updatedTo: "",
  };
  const params = buildTicketListParams(defaults, 1, 20);

  // 默认请求不得带 category / status / origin —— 让后端返回全量。
  assert.deepEqual(params, { page: 1, pageSize: 20 }, "默认列表请求只应带分页参数");
  assert.equal("category" in params, false, "默认请求不得带 category（否则不再是「全部类型」）");
  assert.equal("status" in params, false, "默认请求不得带 status（状态筛选默认「全部」）");
  assert.equal("origin" in params, false, "默认请求不得带 origin");

  // 只有显式选择时才带 —— 证明"默认"不是靠后端猜出来的。
  assert.deepEqual(
    buildTicketListParams({ ...defaults, category: "refund" }, 1, 20),
    { page: 1, pageSize: 20, category: "refund" },
    "显式选择分类时才下发 category",
  );
  // 分类值归一化（后端只认小写枚举），避免"筛了却没生效"。
  assert.equal(
    buildTicketListParams({ ...defaults, category: " REFUND " }, 1, 20).category,
    "refund",
    "category 必须 trim + 小写，否则后端 normalizeTicketCategory 会判定分类无效",
  );
});

test("状态与分类筛选都被保留（用户要的是不按类型分类，不是不要筛选）", async () => {
  const source = await readSrc(pagePath);
  const template = templateOf(source);
  const script = scriptOf(source);

  assert.match(template, /v-if="isAdmin" label="状态"/, "管理视图必须保留状态筛选");
  assert.match(template, /placeholder="全部状态"/, "状态筛选默认应为「全部状态」");
  assert.match(template, /:options="statusOptions"/, "状态筛选项必须绑定 statusOptions");

  // 分类仍作为**筛选下拉**保留（只是不再有独立入口按钮）。
  assert.match(template, /v-if="isAdmin" label="分类"/, "分类应作为筛选下拉保留");
  assert.match(template, /placeholder="全部类别"/, "分类筛选默认应为「全部类别」");

  // 默认值必须为空串 = 全部。
  assert.match(
    script,
    /category: "",\s*\n\s*createdFrom: ""/s,
    "filters.category 的初值必须为空串（= 全部类型）",
  );
});
