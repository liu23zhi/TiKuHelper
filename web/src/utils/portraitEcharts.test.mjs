// 用户画像图表层的行为契约（t200「请添加图表等，这样会直观一些」）。
//
// 三组测试，对应任务书的交付要求：
//
//  A 组（图表存在与类型）：三张图分别是柱 / 环 / 折线，且都由同一个入口构建。
//  B 组（空数据语义，最关键）：空数组 → option === null → 页面渲染「暂无数据」。
//  C 组（字段与顺序）：24 桶顺序与后端字段名一致；深色模式不写死颜色。
//
// ⚠️ 本文件不 import echarts：option 构建层是纯函数，能在无 node_modules 的环境里跑
// （CI 的 test:unit 就是这么跑的）。真正 init() 在 PortraitChart.vue 里。

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  PORTRAIT_HOURS_TITLE,
  PORTRAIT_PALETTE_VARS,
  PORTRAIT_TREND_TITLE,
  PORTRAIT_TYPES_TITLE,
  buildPortraitAllOptions,
  buildPortraitHoursOption,
  buildPortraitTrendOption,
  buildPortraitTypesOption,
  portraitSeriesPalette,
  portraitTooltipOption,
  readPortraitColor,
  readPortraitPalette,
} from '../pages/UserPortrait/portraitChartOptions.mjs';

import { buildPortraitCharts } from './portraitCharts.mjs';

const testDir = path.dirname(fileURLToPath(import.meta.url));
const pageSourcePath = path.join(testDir, '..', 'pages', 'UserPortrait', 'index.vue');
const chartComponentPath = path.join(testDir, '..', 'pages', 'UserPortrait', 'PortraitChart.vue');

// 一份「有数据」的调色板：值刻意取 theme-tokens.css 里 light 主题的真实取值，
// 这样断言能证明「颜色确实来自主题」，而不是随便一个非空串。
const LIGHT_PALETTE = {
  text: '#1D1D1F',
  textMuted: '#5F6368',
  border: '#D8DADF',
  surface: '#FFFFFF',
  primary: '#0066CC',
  successFg: '#176B3A',
  warningFg: '#854D0E',
  dangerFg: '#B42318',
  infoFg: '#005CB8',
  tooltipFg: '#FFFFFF',
  tooltipBg: '#252529',
};

// 深色主题的真实取值，用于验证「换主题就换颜色」而不是写死。
const DARK_PALETTE = {
  ...LIGHT_PALETTE,
  text: '#F2F2F7',
  textMuted: '#A1A6AE',
  border: '#3A3A3E',
  surface: '#1C1C1E',
  primary: '#4C9AFF',
};

// ---------------------------------------------------------------------------
// A 组：三张图存在，类型正确
// ---------------------------------------------------------------------------

test('画像图表：三张图都构建出来，且类型分别为柱 / 环 / 折线', () => {
  const charts = buildPortraitCharts({
    hours: [
      { bucket: 9, label: '09:00', count: 3, amount: 1.5 },
      { bucket: 14, label: '14:00', count: 5, amount: 2.5 },
    ],
    billingTypes: [
      { type: 'normal_search', label: '题目消费', direction: 'expense', count: 8, amount: 4 },
    ],
    weekdays: [{ bucket: 5, label: '周五', count: 8, amount: 4 }],
  });

  const options = buildPortraitAllOptions(charts, LIGHT_PALETTE);

  assert.notEqual(options.hours, null, '24 小时时段图必须构建出来');
  assert.notEqual(options.types, null, '类型分布图必须构建出来');
  assert.notEqual(options.trend, null, '趋势图必须构建出来');

  assert.equal(options.hours.series[0].type, 'bar', '① 时段分布必须是柱状图');
  assert.equal(options.types.series[0].type, 'pie', '② 类型分布必须是饼/环图');
  assert.equal(options.trend.series[0].type, 'line', '③ 趋势必须是折线图');

  // 环图（不是实心饼）：radius 是两段式数组。
  assert.ok(Array.isArray(options.types.series[0].radius), '环图必须有内半径（两段式 radius）');
  assert.equal(options.types.series[0].radius.length, 2, '环图 radius 必须是 [内, 外]');
});

test('画像图表：标题常量与页面使用的一致', () => {
  assert.equal(PORTRAIT_HOURS_TITLE, '24 小时时段分布');
  assert.equal(PORTRAIT_TYPES_TITLE, '题目类型分布');
  assert.equal(PORTRAIT_TREND_TITLE, '消费趋势');
});

test('画像图表：柱图数值直接来自后端 count，不做二次聚合', () => {
  const charts = buildPortraitCharts({
    hours: [
      { bucket: 9, count: 3, amount: 1.5 },
      { bucket: 10, count: 4, amount: 2.5 },
    ],
  });
  const options = buildPortraitAllOptions(charts, LIGHT_PALETTE);
  assert.deepEqual(options.hours.series[0].data, [3, 4], '柱高必须等于后端 count 原值');
});

// ---------------------------------------------------------------------------
// B 组：空数据语义（任务书点名要钉住的一条）
// ---------------------------------------------------------------------------

test('空数据：空数组不画图，option 必须是 null（页面才好渲染「暂无数据」）', () => {
  const charts = buildPortraitCharts({ hours: [], billingTypes: [], weekdays: [], regions: [] });
  const options = buildPortraitAllOptions(charts, LIGHT_PALETTE);

  assert.equal(options.hours, null, '小时桶为空时不得返回 option（否则会画出全 0 柱状图）');
  assert.equal(options.types, null, '类型为空时不得返回 option（否则会画出空环）');
  assert.equal(options.trend, null, '趋势为空时不得返回 option');
});

test('空数据：直接把空 series 喂给各构建函数也返回 null（不依赖上层判断）', () => {
  const emptySeries = { labels: [], values: [], total: 0, empty: true };
  assert.equal(buildPortraitHoursOption(emptySeries, LIGHT_PALETTE), null);
  assert.equal(buildPortraitTypesOption(emptySeries, LIGHT_PALETTE), null);
  assert.equal(buildPortraitTrendOption(emptySeries, LIGHT_PALETTE), null);
});

test('空数据：null / undefined / 非法输入一律安全返回 null，不抛错', () => {
  for (const bad of [null, undefined, 'not-an-object', 42]) {
    const options = buildPortraitAllOptions(bad, LIGHT_PALETTE);
    assert.equal(options.hours, null);
    assert.equal(options.types, null);
    assert.equal(options.trend, null);
  }
  assert.equal(buildPortraitHoursOption(null, LIGHT_PALETTE), null);
  assert.equal(buildPortraitTypesOption(undefined, LIGHT_PALETTE), null);
  assert.equal(buildPortraitTrendOption(null, LIGHT_PALETTE), null);
});

test('空数据：全 0 值序列仍要画图（「有分类且值为 0」不等于「没有数据」）', () => {
  // 这是空态语义的反面：三条类型分类，但次数都是 0。
  // 后端会返回非空数组（确实有这三个 type 的行），所以必须画图。
  const zeroSeries = { labels: ['甲', '乙', '丙'], values: [0, 0, 0], total: 0, empty: false };
  const option = buildPortraitTypesOption(zeroSeries, LIGHT_PALETTE);
  assert.notEqual(option, null, '有分类但全为 0 时必须画图，不能显示「暂无数据」');
  assert.equal(option.series[0].data.length, 3);
});

test('空数据：页面用空态占位而不是画图（组件层证据）', async () => {
  const source = await readFile(chartComponentPath, 'utf8');
  // 组件必须在 options 为假值时渲染 a-empty，而不是无条件渲染 canvas。
  // 注意：v-if 与 a-empty 在**同一行**（自闭合标签），所以不跨行匹配。
  assert.match(source, /<a-empty\s+v-if="!options"/, 'options 为空时必须渲染 a-empty 占位');
  assert.match(source, /v-else[\s\S]*?portrait-chart__canvas/, '有数据时才渲染画布容器');
  // 空态文案必须来自统一常量，不能各写一份。
  assert.match(source, /PORTRAIT_EMPTY_HINT/);
  // 空数据时要销毁旧实例，否则切到空视角会看到上一个视角的残图。
  assert.match(source, /disposeChart\(\)/, '空数据分支必须销毁旧实例');
});

// ---------------------------------------------------------------------------
// C 组：24 桶顺序 / 字段名 / 主题色
// ---------------------------------------------------------------------------

test('24 桶：横轴顺序与后端返回的 bucket 升序一致，标签为 HH:00', () => {
  // 后端 ORDER BY bucket，且只返回有数据的桶。
  const charts = buildPortraitCharts({
    hours: [
      { bucket: 0, count: 1 },
      { bucket: 7, count: 2 },
      { bucket: 13, count: 3 },
      { bucket: 23, count: 4 },
    ],
  });
  const option = buildPortraitHoursOption(charts.hours, LIGHT_PALETTE);

  assert.deepEqual(option.xAxis.data, ['00:00', '07:00', '13:00', '23:00'], '横轴顺序必须与后端 bucket 升序一致');
  assert.deepEqual(option.series[0].data, [1, 2, 3, 4]);
  // 关键：不补齐成 24 格。补齐会把「没发生」画成「0 次」。
  assert.equal(option.xAxis.data.length, 4, '不得补齐成 24 个格子');
});

test('24 桶：后端字段名 bucket / count / amount 与前端读取一致', async () => {
  // 用后端 struct 的真实 json tag 反查，防止前端改了字段名却没人发现。
  const backendPath = path.join(testDir, '..', '..', '..', 'internal', 'service', 'user_portrait.go');
  const backend = await readFile(backendPath, 'utf8');
  assert.match(backend, /type PortraitTimeBucket struct[\s\S]*?Bucket\s+int\s+`json:"bucket"`[\s\S]*?Label\s+string\s+`json:"label"`[\s\S]*?Count\s+int64\s+`json:"count"`[\s\S]*?Amount\s+float64\s+`json:"amount"`/, '后端 PortraitTimeBucket 的 json 字段名必须是 bucket/label/count/amount');

  // 前端确实读的是 count（默认 valueKey），并用 bucket 拼 HH:00 标签。
  const charts = buildPortraitCharts({ hours: [{ bucket: 5, count: 9 }] });
  assert.equal(charts.hours.values[0], 9, '前端必须读 count 字段');
  assert.equal(charts.hours.labels[0], '05:00', '前端必须用 bucket 拼出 HH:00');
});

test('24 桶：时区换算必须走 datetime(+8 hours)，不得退回 substr 截偏移', async () => {
  // ⚠️ 这条测试的口径曾经写错过，记录在此避免重犯：
  //
  // 最初画像实现是 `substr(created_at, 1, 19)`，理由是「created_at 存的是
  // `+08:00` 带偏移文本，截掉偏移再取小时」。该理由只在**所有行恰好是 +08:00**
  // 时成立。实测本机库是 `2026-05-08 13:15:29.939932211+00:00`（UTC），
  // substr 把 UTC 小时当本地小时 → 52/53 行的小时值错，整张时段分布左移 8 小时。
  //
  // 修法见提交 9ff629d：SQLite 分支改用 `datetime(created_at, '+8 hours')`。
  // 不用 'localtime'，因为那依赖进程所在机器的时区（CI 跑 UTC、开发机跑 CST，
  // 同一份数据得出不同分布，比偏移更难查）。
  //
  // 图表会把这个偏移放大成「峰值出现在凌晨」的假象，所以必须钉住。
  const backendPath = path.join(testDir, '..', '..', '..', 'internal', 'service', 'user_portrait.go');
  const backend = await readFile(backendPath, 'utf8');
  // ⚠️ 反向断言前必须剥掉 Go 注释：该文件的注释里**正是把错误写法当反例解释**
  // （例如「为什么 `strftime('%H', created_at)` 不行」），
  // 不剥注释会把这些说明性引用误判成「实现又改回去了」。
  const backendCode = backend
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/\/\/[^\n]*/g, '');

  assert.match(
    backendCode,
    /strftime\('%H',\s*datetime\(created_at,\s*'\+8 hours'\)\)/,
    '小时桶必须用 datetime(created_at, \'+8 hours\') 做时区换算',
  );
  assert.match(
    backendCode,
    /strftime\('%w',\s*datetime\(created_at,\s*'\+8 hours'\)\)/,
    '星期桶同样必须做时区换算（跨日界会算错星期）',
  );
  // 反向断言：两种错误写法都不得出现在**代码**里。
  assert.doesNotMatch(
    backendCode,
    /strftime\('%H',\s*created_at\)/,
    '不得直接 strftime(created_at) —— SQLite 会按 UTC 解析，偏 8 小时',
  );
  assert.doesNotMatch(
    backendCode,
    /strftime\('%H',\s*datetime\(created_at,\s*'localtime'\)\)/,
    '不得用 localtime —— CI(UTC) 与开发机(CST) 会得出不同分布',
  );
  // substr 截偏移的写法也不得回来（只在所有行都是 +08:00 时才碰巧正确）。
  assert.doesNotMatch(
    backendCode,
    /strftime\('%H',\s*substr\(created_at/,
    '不得退回 substr(created_at,1,19) —— 那个写法只在所有行都是 +08:00 时才碰巧正确',
  );
});

test('主题：颜色全部取自调色板，切换深色后 option 里的颜色必须跟着变', () => {
  const charts = buildPortraitCharts({
    hours: [{ bucket: 9, count: 1 }],
    billingTypes: [{ type: 'normal_search', label: '题目消费', count: 1, amount: 1 }],
  });

  const light = buildPortraitAllOptions(charts, LIGHT_PALETTE);
  const dark = buildPortraitAllOptions(charts, DARK_PALETTE);

  assert.equal(light.hours.series[0].itemStyle.color, '#0066CC');
  assert.equal(dark.hours.series[0].itemStyle.color, '#4C9AFF', '深色模式下柱色必须换成深色主题的 primary');
  assert.notEqual(light.hours.series[0].itemStyle.color, dark.hours.series[0].itemStyle.color);

  // 坐标轴文字与分隔线也要跟着换（否则深色底上是浅色字，看不见）。
  assert.notEqual(
    light.hours.yAxis.axisLabel.color,
    dark.hours.yAxis.axisLabel.color,
    '坐标轴文字颜色必须跟随主题',
  );
  assert.notEqual(
    light.hours.yAxis.splitLine.lineStyle.color,
    dark.hours.yAxis.splitLine.lineStyle.color,
    '分隔线颜色必须跟随主题',
  );
});

test('主题：源文件里不得出现写死的十六进制颜色（必须走 CSS 变量）', async () => {
  const optionSource = await readFile(path.join(testDir, '..', 'pages', 'UserPortrait', 'portraitChartOptions.mjs'), 'utf8');
  // 去掉注释后再查，避免注释里的示例值误判。
  // 必须同时剥掉块注释（/* … */）与行注释（// …）：只剥行注释会漏掉 JSDoc 里的
  // `{ text: '#1D1D1F' }` 这种参数示例，把「注释举例」误报成「写死颜色」。
  const codeOnly = optionSource
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/\/\/[^\n]*/g, '');
  const hexLiterals = codeOnly.match(/#[0-9a-fA-F]{3,8}\b/g) || [];
  assert.deepEqual(hexLiterals, [], `option 构建层不得写死颜色，实际发现：${hexLiterals.join(', ')}`);

  // 颜色键必须来自集中定义的映射表（不散落 var(--...)）。
  assert.ok(Object.keys(PORTRAIT_PALETTE_VARS).length >= 8, '调色板映射表必须覆盖主要语义色');
  assert.equal(PORTRAIT_PALETTE_VARS.primary, '--primary');
  assert.equal(PORTRAIT_PALETTE_VARS.textMuted, '--text-muted');
});

test('主题：readPortraitPalette 从 CSS 变量读值，且读失败不抛错', () => {
  const reads = [];
  const palette = readPortraitPalette({
    element: { id: 'root' },
    readStyle: (el, prop) => { reads.push([el.id, prop]); return ' #ABCDEF '; },
  });
  // 每个键都读了对应变量，且值被 trim。
  assert.equal(palette.primary, '#ABCDEF');
  assert.equal(palette.textMuted, '#ABCDEF');
  assert.ok(reads.some(([, prop]) => prop === '--primary'));
  assert.ok(reads.length >= Object.keys(PORTRAIT_PALETTE_VARS).length);

  // 读样式抛错时不能把整页拖崩：返回空串，由 fallback 兜。
  const broken = readPortraitPalette({
    element: {},
    readStyle: () => { throw new Error('boom'); },
  });
  assert.equal(broken.primary, '');

  // 没有元素（SSR / 测试环境）也不能抛。
  assert.equal(readPortraitPalette({ element: null }).primary, '');
  assert.equal(readPortraitPalette(undefined).primary, '');
});

test('主题：缺色时用 fallback，不产生 undefined 塞进 option', () => {
  assert.equal(readPortraitColor({ primary: '' }, 'primary', '#000'), '#000');
  assert.equal(readPortraitColor({}, 'primary', '#000'), '#000');
  assert.equal(readPortraitColor(null, 'primary', ''), '');
  assert.equal(readPortraitColor({ primary: '#123' }, 'primary', '#000'), '#123');

  // 空调色板下也必须给出字符串（echarts 拿到 undefined 会静默用默认色，
  // 深色模式下默认色往往看不清）。
  const tooltip = portraitTooltipOption({});
  assert.equal(typeof tooltip.backgroundColor, 'string');
  assert.equal(typeof tooltip.textStyle.color, 'string');
});

test('主题：环图多色序列只用语义色，不引入新色', () => {
  const series = portraitSeriesPalette(LIGHT_PALETTE);
  assert.ok(series.length >= 4, '环图至少需要 4 个可区分色');
  for (const color of series) {
    assert.ok(
      Object.values(LIGHT_PALETTE).includes(color),
      `环图颜色 ${color} 必须来自主题调色板，不得是自造色`,
    );
  }
});

// ---------------------------------------------------------------------------
// D 组：移动端 / 无障碍 / 接线
// ---------------------------------------------------------------------------

test('移动端：容器不写死像素宽度，且用 min-width:0 防溢出', async () => {
  const source = await readFile(chartComponentPath, 'utf8');
  assert.match(source, /width:\s*100%/, '画布宽度必须 100% 自适应');
  assert.match(source, /min-width:\s*0/, '必须有 min-width:0，否则 grid 子项被长标签撑出横向滚动条');
  // 高度用固定值 + 媒体查询缩小，不能说死成一个大 px 值导致窄屏过高。
  assert.match(source, /@media \(max-width:\s*480px\)/, '必须有窄屏媒体查询');
  // option 层不得塞固定宽度（echarts 会按它画，不跟随容器）。
  const optionSource = await readFile(path.join(testDir, '..', 'pages', 'UserPortrait', 'portraitChartOptions.mjs'), 'utf8');
  assert.doesNotMatch(optionSource, /width:\s*\d{3,}\b/, 'option 里不得写死像素宽度');
});

test('移动端：窄屏下坐标轴标签抽稀，避免 24 个标签重叠', () => {
  const many = buildPortraitCharts({
    hours: Array.from({ length: 24 }, (_, hour) => ({ bucket: hour, count: hour })),
  });
  const option = buildPortraitHoursOption(many.hours, LIGHT_PALETTE);
  assert.equal(option.xAxis.axisLabel.hideOverlap, true, '重叠标签必须自动隐藏');
  assert.equal(option.xAxis.axisLabel.interval, 'auto', '桶数 > 12 时必须抽稀标签');
});

test('无障碍：画布必须有 role=img 与 aria-label（canvas 对读屏不可见）', async () => {
  const source = await readFile(chartComponentPath, 'utf8');
  assert.match(source, /role="img"/);
  assert.match(source, /:aria-label="ariaLabel"/);
  // 页面必须给每张图传 ariaLabel，否则 aria-label 为空等于没有。
  const page = await readFile(pageSourcePath, 'utf8');
  const labels = page.match(/ariaLabel:\s*'[^']+'/g) || [];
  assert.ok(labels.length >= 3, `页面必须给三张图都传 ariaLabel，实际 ${labels.length} 个`);
});

test('接线：页面用 PortraitChart 渲染三张图，且空态由组件统一处理', async () => {
  const page = await readFile(pageSourcePath, 'utf8');
  assert.match(page, /import PortraitChart from '\.\/PortraitChart\.vue'/, '必须引入图表组件');
  assert.match(page, /components:\s*\{[^}]*PortraitChart/, '必须注册组件');
  assert.match(page, /<PortraitChart[\s\S]*?:options="chart\.options"/, '必须把 option 传给组件');
  assert.match(page, /buildPortraitAllOptions/, '必须走统一的 option 构建入口');
  // 页面不得自己再写一份空态判断（会与组件里的判断漂移）。
  assert.doesNotMatch(page, /chart\.empty/, '页面的图表区不得自己判断 empty（空态由 PortraitChart 统一处理）');
});

test('接线：echarts 必须动态 import，不进首屏 bundle', async () => {
  const source = await readFile(chartComponentPath, 'utf8');
  assert.match(source, /await import\('echarts\/core'\)/, '必须动态 import，避免 echarts 打进首屏 chunk');
  assert.doesNotMatch(source, /^import .*from 'echarts/m, '不得静态 import echarts');
  // 必须按需注册，不全量 import 'echarts'（体积差一个量级）。
  assert.match(source, /echarts\.use\(/, '必须按需 use() 注册图表与组件');
  assert.doesNotMatch(source, /from 'echarts'$/, '不得整包 import echarts');
});

test('接线：趋势图口径如实标注后端无按天字段', async () => {
  const page = await readFile(pageSourcePath, 'utf8');
  // 这是一条「如实告知」的判据：后端没有按天聚合，页面必须说清楚，
  // 不能画一条折线就让人以为是「今日 vs 昨日」。
  assert.match(page, /暂无按天聚合字段/, '页面必须标注趋势图不是按天对比');
});
