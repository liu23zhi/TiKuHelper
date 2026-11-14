import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { buildLogStructuredFields, LOG_MODULE_OPTIONS } from './logListHelpers.mjs';

const pagePath = new URL('../pages/LogList/index.vue', import.meta.url);

test('log table has the requested columns, fixed layout, and wrapping centered cells', async () => {
  const source = await readFile(pagePath, 'utf8');
  const titles = [...source.matchAll(/title: '([^']+)'/g)].map((match) => match[1]);
  // 「敏感词」是**只打标记**的附加列：紧跟「状态」，不改变任何既有列的顺序与语义。
  // 「项目」紧跟「说明」（#36 派生列）；#31 的 squash 曾整体覆盖此期望值把它删掉。
  assert.deepEqual(titles, [
    '时间', '模块', '状态', '敏感词', '用户', '来源 IP', '本地题库 ID', '账单 ID',
    '说明', '项目', '命中渠道 / 错误原因', '计费渠道 / 耗时', '详情',
    '请求 ID', '请求参数', '请求接口', '答案数', '原始请求', '原始响应',
  ]);
  assert.match(source, /table-layout="fixed"/);
  assert.match(source, /:scroll="\{ x: tableScrollX \}"/);
  assert.match(source, /<ConsoleTableShell/);
  assert.match(source, /#bodyCell="\{ column, record, text \}"/);
  assert.match(source, /overflow-wrap:\s*anywhere/);
  assert.match(source, /class="mobile-log-time">\{\{ formatDateForTable\(record\.createTime\) \}\}/);
  assert.match(source, /\.mobile-log-time\s*\{\s*white-space:\s*pre-line/);
  assert.doesNotMatch(source, /log-cell-nowrap/);
});

test('log widths account for the named samples and expand entire detail and snapshot columns', async () => {
  const source = await readFile(pagePath, 'utf8');
  assert.match(source, /const logColumnWidths =/);
  assert.match(source, /moduleLabels = LOG_MODULE_OPTIONS\.map\(\(option\) => option\.label\)/);
  assert.match(source, /moduleOptions: LOG_MODULE_OPTIONS/);
  assert.deepEqual(LOG_MODULE_OPTIONS.slice(-3).map((option) => option.label), ['短信', '充值', '访问令牌']);
  assert.match(source, /module: Math\.max\(\.\.\.moduleLabels\.map/);
  for (const [key, width] of Object.entries({
    createTime: 112, username: 170, clientIp: 262, qid: 104,
    billingIds: 104, summary: 156, project: 170, hitProvider: 170,
    billingProvider: 170, detail: 170, requestId: 310,
    mode: 160, requestSource: 124, answerCount: 76,
    requestSnapshot: 170, responseSnapshot: 170, sensitiveFlag: 96,
  })) {
    assert.match(source, new RegExp(`${key}: ${width}(?:,|\\b)`));
  }
  assert.match(source, /snapshots\.columnWidth\(field, logColumnWidths\[field\], 236\)/);
  assert.match(source, /expandedColumnWidth\('detail'\)/);
  assert.match(source, /expandedColumnWidth\('requestSnapshot'\)/);
  assert.match(source, /expandedColumnWidth\('responseSnapshot'\)/);
  assert.match(source, /expandedColumnWidth\('hitProvider'\)/);
  assert.match(source, /expandedColumnWidth\('project'\)/);
  assert.match(source, /:columns="visibleColumns"/);
  assert.match(source, /useTableColumnPreferences\(\{\s*pageKey: 'LogList', columns,/);
  assert.match(source, /sumTableColumnWidths\(visibleColumns\.value\)/);
  assert.match(source, /useTableExpansion\(dataSource\)/);
});

test('log detail previews use measured overflow and preserve expand/collapse state per row', async () => {
  const source = await readFile(pagePath, 'utf8');
  // 详情列现在分两支：未命中敏感词的行走 OverflowTableText（测量溢出 + 逐行展开），
  // 命中行走 SensitiveText（默认遮挡，管理员可展开原文）。
  // 两个分支都必须存在：只断言其中一支会让「所有日志都被当敏感处理」这种回归溜过去。
  assert.match(source, /<template v-else-if="column.key === 'detail'">/);
  assert.match(source, /<OverflowTableText\s+v-else\s+:text="buildTableDetail\(record\)"\s+:expanded="isSnapshotExpanded\(record\.id, 'detail'\)"\s+@toggle="toggleSnapshotExpanded\(record\.id, 'detail'\)"/);
  assert.match(source, /<SensitiveText\s+v-if="isSensitiveRow\(record\)"\s+:text="sensitiveTextFor\(record\.id, 'detail', buildTableDetail\(record\)\)"/);
  // 未命中行不能被 SensitiveText 无条件包住：那会让普通日志也走进敏感分支。
  assert.doesNotMatch(source, /<SensitiveText\s+v-if="true"/);

  const componentSource = await readFile(new URL('../components/OverflowTableText.vue', import.meta.url), 'utf8');
  const script = componentSource.match(/<script>([\s\S]*?)<\/script>/)[1]
    .replace(/import[^;]+;/, '')
    .replace('export default', 'return');
  const mounted = [];
  const unmounted = [];
  const watchers = [];
  let resizeCallback;
  let observedElement;
  let disconnected = false;
  const component = new Function('nextTick', 'onBeforeUnmount', 'onMounted', 'ref', 'watch', 'useId', 'computed', 'ResizeObserver', script)(
    (callback) => callback ? Promise.resolve(callback()) : Promise.resolve(),
    (callback) => unmounted.push(callback),
    (callback) => mounted.push(callback),
    (value) => ({ value }),
    (getter, callback) => watchers.push({ getter, callback }),
    () => 'overflow-log-test',
    (getter) => ({ get value() { return getter(); } }),
    class {
      constructor(callback) { resizeCallback = callback; }
      observe(element) { observedElement = element; }
      disconnect() { disconnected = true; }
    },
  );
  const props = { text: '三行内的完整日志', expanded: false, lines: component.props.lines.default };
  const state = component.setup(props);
  const element = { clientHeight: 72, scrollHeight: 72 };
  state.content.value = element;
  mounted.forEach((callback) => callback());
  assert.equal(props.lines, 3);
  assert.equal(observedElement, element);
  assert.equal(state.overflowing.value, false, '完整显示时不出现查看详情');

  element.scrollHeight = 96;
  resizeCallback();
  assert.equal(state.overflowing.value, true, '容器变窄后重新判断三行溢出');
  props.expanded = true;
  await watchers[0].callback();
  assert.match(componentSource, /v-if="expanded \|\| overflowing"/);
  assert.match(componentSource, /__content--expanded\s*\{[^}]*-webkit-line-clamp:\s*unset;[^}]*overflow:\s*visible;/);
  props.expanded = false;
  props.text = '短日志';
  element.scrollHeight = 72;
  await watchers[0].callback();
  assert.equal(state.overflowing.value, false, '收起或内容变短后恢复完整预览');
  unmounted.forEach((callback) => callback());
  assert.equal(disconnected, true);
});

test('log request parameters can be rendered one per line without changing detail text', () => {
  const row = { oldAnswer: 'method=POST path=/api/search query=use=ai contentType=application/json body={"deepThinking":true,"skipCache":false}' };
  assert.equal(buildLogStructuredFields(row).mode, 'use=ai deepThinking=true skipCache=false');
  assert.equal(buildLogStructuredFields(row, '\n').mode, 'use=ai\ndeepThinking=true\nskipCache=false');
});

test('LogList marks the failed query explicitly instead of presenting stale rows as a current success', async () => {
  const source = await readFile(pagePath, 'utf8');
  assert.match(source, /import RequestFailureAlert from '@\/components\/RequestFailureAlert\.vue'/);
  assert.match(source, /import \{ ensureRequestFailure \} from '@\/utils\/requestFailureView\.mjs'/);
  const requestStart = source.indexOf('handleLogList() {');
  const catchStart = source.indexOf('}).catch((error) => {', requestStart);
  assert.ok(requestStart >= 0 && catchStart > requestStart, 'current list read and original Error branch remain present');
  const requestBody = source.slice(requestStart, source.indexOf('onFilterChange() {', requestStart));
  const catchBody = source.slice(catchStart, source.indexOf('onFilterChange() {', requestStart));
  assert.match(catchBody, /if \(!owns\(\)\) return/);
  assert.match(catchBody, /dataSource\.value = \[\]/);
  assert.match(catchBody, /pagination\.total = 0/);
  assert.match(catchBody, /loadError\.value = '获取操作日志失败，请重试。'/);
  assert.match(catchBody, /this\.listFailure = ensureRequestFailure\(error\)/, 'retain original errors for transport diagnostics');
  assert.match(requestBody, /this\.listFailure = ensureRequestFailure\(res\)/, 'non200 preserves the original response object');
  assert.doesNotMatch(requestBody, /message\.error/, 'persistent inline state replaces duplicate read-error toast');
  assert.match(requestBody, /getLogList\(buildSearchParams\(\{[\s\S]*?keyword: filters\.keyword,[\s\S]*?module: filters\.module,[\s\S]*?action: filters\.action[\s\S]*?pagination\.current, pagination\.pageSize\)\)/);
  assert.match(catchBody, /finally\(\(\) => \{\s*if \(!owns\(\)\) return\s*tabLoading\.value = false/);
  assert.match(source, /class="log-read-error"/);
  assert.match(source, /!tabLoading && !loadError/);
  assert.match(source, /<RequestFailureAlert v-if="listFailure" :failure="listFailure"[\s\S]*?message="获取操作日志失败，请重试。" :retryable="true" :busy="tabLoading"[\s\S]*?@retry="guardedRetryListFailure"/);
  assert.match(source, /guardedRetryListFailure\(failure, contextKey\) \{\s*if \(this\.isCurrentListFailure\(failure, contextKey\)\) this\.handleLogList\(\)/);
  assert.match(source, /const captureListRequest = \(\) => \{[\s\S]*?listFailure\.value = null[\s\S]*?listFailureContext\.value \+= 1/, 'new query retires the prior inline error and stale actions');
});
