import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const web = process.env.TIKU_WEB_SOURCE || path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const read = relative => readFileSync(path.join(web, relative), 'utf8');

const page = () => read('src/pages/Whitelist/index.vue');
const api = () => read('src/api/WhitelistApi.js');

// 白名单页与 API 的契约钉子（T-04 / T-05）。
//
// 为什么这些断言值得写：白名单是本轮唯一「放行」语义的功能，它的失效方向与
// 黑名单相反 —— 黑名单坏掉是「该拦的没拦」，白名单坏掉是「该放行的仍被拦」，
// 表现为「我明明加了规则，还是 403」，从现象完全看不出是哪一层漏了。
// 所以这里钉的是三件最容易静默断掉的事：
//   ① 页面与 API 必须指向 /adapter-service/whitelist（不是 blacklist）；
//   ② 删除必须带 reason（否则后端 400，页面表现成「删不掉」）；
//   ③ 页面不得请求黑名单的命中记录接口（白名单不写命中行，接口也不存在）。

test('白名单 API 指向 /adapter-service/whitelist 且覆盖六个端点', () => {
  const source = api();

  // 六个端点：列表 / 新增 / 启停 / 删除 / 读文本域 / 写文本域。
  for (const [fn, url, method] of [
    ['getWhitelistEntries', '/adapter-service/whitelist', 'get'],
    ['createWhitelistEntry', '/adapter-service/whitelist', 'post'],
    ['setWhitelistEntryEnabled', '/adapter-service/whitelist/', 'put'],
    ['deleteWhitelistEntry', '/adapter-service/whitelist/', 'delete'],
    ['getWhitelistExemptIPs', '/adapter-service/whitelist/exempt-ips', 'get'],
    ['updateWhitelistExemptIPs', '/adapter-service/whitelist/exempt-ips', 'put'],
  ]) {
    assert.match(source, new RegExp(`export function ${fn}\\b`), `缺少导出 ${fn}`);
    assert.ok(source.includes(url), `${fn} 必须打到 ${url}`);
    assert.match(source, new RegExp(`method: '${method}'`), `${fn} 必须用 ${method.toUpperCase()}`);
  }

  // 启停必须是 ${id}/enabled 子路径 + 显式 enabled 字段（后端缺字段会 400）。
  assert.match(source, /url: `\/adapter-service\/whitelist\/\$\{value\}\/enabled`/,
    '启停必须打到 /whitelist/:id/enabled');
  assert.match(source, /data: \{ enabled \}/, '启停必须显式提交 enabled（后端缺字段返回 400）');
  assert.match(source, /if \(typeof enabled !== 'boolean'\) throw new Error\('缺少目标状态'\)/,
    '前端也必须拒绝无边界的启停请求');
});

test('白名单删除必须带 reason（后端 400 会让页面表现成「删不掉」）', () => {
  const source = api();
  // reason 走 query 参数，且长度校验与后端一致（去空白 ≥ 2 字）。
  assert.match(source, /params: \{ reason: trimmed \}/, '删除必须把 reason 作为查询参数提交');
  assert.match(source, /if \(trimmed\.length < 2\) throw new Error\('请填写删除原因'\)/,
    '删除原因必须在前端也做长度校验，与后端 minRuleDeleteReasonRunes=2 对齐');
  // 不得给默认值兜底：审计里出现「无原因」等于没有审计。
  assert.doesNotMatch(source, /reason:\s*trimmed\s*\|\|/, '不得为 reason 提供默认值兜底');
});

test('白名单页面不得请求黑名单命中记录（白名单不写命中行，接口也不存在）', () => {
  const source = page();
  assert.doesNotMatch(source, /getBlacklistHits/,
    '白名单页不得调用 getBlacklistHits —— 白名单命中不写 blacklist_hits，后端也没有该接口');
  assert.doesNotMatch(source, /\/adapter-service\/blacklist/,
    '白名单页不得请求 /adapter-service/blacklist（会变成读写黑名单）');
  // 也不该出现「命中记录」这个 tab：那是黑名单的解释数据。
  assert.doesNotMatch(source, /tab="命中记录"/, '白名单页不应有「命中记录」tab');
});

test('白名单页面说明优先级：白名单 > 黑名单，且命中不留痕', () => {
  const source = page();
  // 这是本功能存在的理由，页面上必须写清楚，否则运营会把白名单当成「降低优先级」。
  assert.match(source, /优先级\s*白名单\s*>\s*黑名单/, '页面必须写明优先级 白名单 > 黑名单');
  assert.match(source, /直接放行且完全不查黑名单/, '页面必须写明命中后完全不查黑名单');
  assert.match(source, /不会在黑名单命中记录里留痕/, '页面必须写明命中不留 blacklist_hits 行');
  // 白名单没有总开关：页面不能照抄黑名单那个 switch（会渲染一个不存在的行为）。
  assert.doesNotMatch(source, /toggleSwitch/, '白名单没有总开关，页面不应有开关切换函数');
  assert.doesNotMatch(source, /blacklistEnabled/, '白名单页不得读写 blacklist_enabled');
  assert.match(source, /白名单没有总开关/, '页面必须说明为什么没有总开关');
});

test('白名单页面覆盖规则表与按 IP 文本域两层，并显示解析结果', () => {
  const source = page();
  // 两层白名单（表 + blacklist_exempt_ips）都必须有管理入口。
  assert.match(source, /tab="白名单规则"/, '缺少规则表 tab');
  assert.match(source, /tab="按 IP 白名单"/, '缺少按 IP 文本域 tab（T-05）');
  assert.match(source, /updateWhitelistExemptIPs/, '文本域必须能保存');
  assert.match(source, /getWhitelistExemptIPs/, '文本域必须能读取');
  // 解析结果必须显示：只回原文的话，全角逗号之类会静默少解析几项。
  assert.match(source, /已识别 \{\{ exemptCount \}\} 项/, '必须显示服务端解析出的项数');
  assert.match(source, /exemptItems\.join/, '必须显示解析出的具体网段');
  // count 与 items 不一致时必须报错，而不是让「已识别 N 项」说谎。
  assert.match(source, /exemptError\.value = '按 IP 白名单解析结果与汇总数量不一致/,
    'count 与 items 不一致必须明确报错');
});

test('白名单页面只允许 ip 粒度（其它粒度没有判定落点）', () => {
  const source = page();
  assert.match(source, /const kindOptions = \[\{ label: 'IP', value: 'ip' \}\]/,
    '白名单只支持 ip 粒度，选项必须只有一项');
  assert.doesNotMatch(source, /value: 'phone'/, '不应提供手机号粒度（中间件看不到手机号）');
  assert.doesNotMatch(source, /value: 'wechat'/, '不应提供微信粒度');
  assert.doesNotMatch(source, /value: 'region'/, '不应提供地区粒度');
});

test('白名单页面：写入成功后必须刷新列表（否则界面与实际不一致）', () => {
  const source = page();
  // 新增 / 启停 / 删除三条写路径都要在锁释放后 reload。
  const reloads = source.match(/if \(reloadAfter && owns\(\)\) await loadEntries\(\)/g) || [];
  assert.ok(reloads.length >= 3,
    `新增/启停/删除三条写路径都必须刷新列表，实际匹配 ${reloads.length} 处`);
  // 文本域保存后必须重新读取（用服务端解析结果覆盖本地输入）。
  assert.match(source, /if \(readAfter && owns\(\)\) await loadExemptIPs\(\)/,
    '文本域保存后必须重新读取服务端解析结果');
});

test('白名单页面渲染脱敏值，不渲染 pattern 原文', () => {
  const source = page();
  // 列定义写 valueMasked，不写 pattern（与黑名单同约定：列表不泄漏规则边界）。
  assert.match(source, /title: '脱敏值', dataIndex: 'valueMasked'/, '列表列必须用脱敏值');
  assert.doesNotMatch(source, /title: '[^']*', dataIndex: 'pattern'/, '列表不得直接渲染 pattern 原文');
  // 表格单元格用 record.valueMasked。
  assert.match(source, /record\.valueMasked \|\| '—'/, '单元格必须渲染脱敏值');
});
