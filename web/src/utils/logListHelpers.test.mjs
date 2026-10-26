import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import * as logHelpers from './logListHelpers.mjs';

import {
  buildLogActionColor,
  buildLogActionLabel,
  buildLogDetailSections,
  buildLogLocalQuestionId,
  buildLogModuleLabel,
  buildLogResultText,
  buildLogRowDetail,
  buildLogTableDetail,
  buildLogStatusLabel,
  buildLogStatusColor,
  buildLogStructuredFields,
} from './logListHelpers.mjs';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const logListPagePath = path.join(__dirname, '..', 'pages', 'LogList', 'index.vue');

test('table detail does not repeat a long failure already shown in the channel column', () => {
  const failed = {
    module: 'search',
    description: '搜索失败 requestId=req-1 answers=0',
    newAnswer: JSON.stringify({ failures: [{ provider: 'ai', code: 'UPSTREAM_ERROR', error: '上游错误'.repeat(60) }] }),
  };
  assert.equal(buildLogTableDetail(failed), '-');
  assert.match(buildLogRowDetail(failed), /失败原因/);
  const solved = { ...failed, newAnswer: JSON.stringify({ answer: { answerKey: ['B'], answerKeyText: 'B', answerText: '正确答案' }, failures: [{ provider: 'ai', error: '部分失败' }] }) };
  assert.match(buildLogTableDetail(solved), /答案：B/);
  const legacy = { module: 'search', description: '搜索完成 requestId=old answers=1', newAnswer: '历史渠道答案：B' };
  assert.equal(buildLogTableDetail(legacy), '历史渠道答案：B');
});

test('search failures show provider causes and distinguish partial failure and no answer', () => {
  const record = {
    module: 'search',
    description: '搜索失败 requestId=req-1 mode=ai answers=0 status=failed',
    newAnswer: JSON.stringify({ status: 'failed', failures: [{ provider: 'ai', code: 'UPSTREAM_ERROR', error: 'HTTP 401: invalid api key' }] }),
  };
  assert.equal(buildLogStatusLabel(record), '失败');
  // 渠道名已经在「命中渠道 / 错误原因」列单独展示，这里只保留原因本身。
  assert.equal(buildLogRowDetail(record), '失败原因：[UPSTREAM_ERROR]：HTTP 401: invalid api key');
  assert.equal(buildLogStatusLabel({ ...record, description: '搜索完成', newAnswer: JSON.stringify({ status: 'partial_failure' }) }), '部分失败');
  assert.equal(buildLogStatusColor({ ...record, newAnswer: JSON.stringify({ status: 'partial_failure' }) }), 'orange');
  assert.equal(buildLogStatusLabel({ ...record, newAnswer: JSON.stringify({ status: 'no_answer' }) }), '无答案');
  assert.equal(buildLogStatusLabel({ ...record, newAnswer: JSON.stringify({ status: 'unavailable' }) }), '未配置');
});

test('AI fallback details prefer diagnostic detail without losing the response snapshot', () => {
  const record = {
    module: 'ai_fallback',
    description: 'AI兜底失败 status=failed code=UPSTREAM_ERROR',
    newAnswer: JSON.stringify({ success: false, error: { code: 'UPSTREAM_ERROR', message: 'AI 上游请求失败' }, failureDetail: 'HTTP 429: rate limited' }),
  };
  assert.equal(buildLogRowDetail(record), '失败原因：HTTP 429: rate limited / AI 上游请求失败');
  assert.equal(buildLogStructuredFields(record).responseSnapshot, record.newAnswer);
});

test('search result is shown directly in the detail column', () => {
  const record = {
    module: 'search',
    description: '搜索完成 requestId=req-ok mode=normal use= answers=1 hitProvider=ai billingProvider=ai',
    newAnswer: JSON.stringify({
      question: '题干',
      options: ['A', 'B'],
      type: 0,
      answer: {
        answerKey: ['A'],
        answerKeyText: 'A',
        answerIndex: [0],
        answerText: '有,详细的计划表',
        bestAnswer: ['有,详细的计划表'],
      },
      status: 'success',
    }),
  };

  // 详情列直接给结果：选项字母 + 答案文本。
  assert.equal(buildLogResultText(record), 'A 有,详细的计划表');
  assert.equal(buildLogRowDetail(record), '答案：A 有,详细的计划表');
  // 成功的行不该在「命中渠道 / 错误原因」列出现错误原因。
  assert.deepEqual(buildLogStructuredFields(record).failureReasons, []);
  assert.equal(buildLogStructuredFields(record).hitProvider, 'AI');
});

test('detail column falls back to bestAnswer when answerText is missing', () => {
  const record = {
    module: 'search',
    description: '搜索完成 requestId=req-best mode=normal use= answers=1 hitProvider=local',
    newAnswer: JSON.stringify({
      answer: { bestAnswer: ['第一条', '第二条'], allAnswer: [['第一条'], ['第二条']] },
      status: 'partial_failure',
    }),
  };
  assert.equal(buildLogResultText(record), '第一条 / 第二条');
  assert.equal(buildLogRowDetail(record), '答案：第一条 / 第二条');
});

test('AI fallback result is shown directly in the detail column', () => {
  const record = {
    module: 'ai_fallback',
    description: 'AI兜底成功 status=success answers=1 hitProvider=ai billingProvider=ai',
    newAnswer: JSON.stringify({ success: true, result: { question: '题干', answer: '这是兜底答案' } }),
  };
  assert.equal(buildLogResultText(record), '这是兜底答案');
  assert.equal(buildLogRowDetail(record), '答案：这是兜底答案');
});

test('channel column keeps the failure reason when the snapshot has no failures', () => {
  // 历史行：快照里只有 status，失败明细只存在于审计描述里。
  const record = {
    module: 'search',
    description: '搜索失败 requestId=req-old mode=ai answers=0 provider=ai code=UPSTREAM_ERROR error=HTTP401',
    newAnswer: JSON.stringify({ status: 'failed' }),
  };
  const fields = buildLogStructuredFields(record);
  assert.equal(fields.hitProvider, 'AI');
  assert.deepEqual(fields.failureReasons, ['ai [UPSTREAM_ERROR]：HTTP401']);
  // 没有答案时详情列才退回原因，保证信息不丢。
  assert.equal(buildLogRowDetail(record), '失败原因：ai [UPSTREAM_ERROR]：HTTP401');
});

test('ordinary search failure descriptions retain complete error causes', () => {
  const fields = buildLogStructuredFields({
    module: 'search',
    description: '搜索失败 requestId=req-old mode=normal answers=0 provider=wanneng code=UPSTREAM_ERROR error=当前余额不足，请充值后再使用',
    newAnswer: JSON.stringify({ status: 'failed' }),
  });
  assert.equal(fields.hitProvider, 'wanneng');
  assert.deepEqual(fields.failureReasons, ['wanneng [UPSTREAM_ERROR]：当前余额不足，请充值后再使用']);
});

test('local question id falls back to structured search metadata when qid is empty', () => {
  assert.equal(buildLogLocalQuestionId({ qid: 42, description: '搜索完成 localQuestionId=99' }), '42');
  assert.equal(buildLogLocalQuestionId({ qid: 0, description: '搜索完成 localQuestionId=99' }), '99');
  assert.equal(buildLogLocalQuestionId({ qid: 0, description: '普通日志 qid=88 provider=local' }), '88');
  assert.equal(buildLogLocalQuestionId({ qid: 0, oldAnswer: '{"qid":"77","question":"题目"}' }), '77');
  assert.equal(buildLogLocalQuestionId({ qid: 0, description: '搜索完成 requestId=req-1' }), '-');
});

test('LogList puts channel and billing columns right after the summary column', async () => {
  const content = await readFile(logListPagePath, 'utf8');
  const titles = [...content.matchAll(/title: '([^']+)'/g)].map((match) => match[1]);

  const summaryIndex = titles.indexOf('说明');
  assert.ok(summaryIndex >= 0, 'missing summary column');
  assert.equal(titles[summaryIndex + 1], '命中渠道 / 错误原因');
  assert.equal(titles[summaryIndex + 2], '计费渠道 / 耗时');
  assert.ok(titles.indexOf('详情') > summaryIndex + 2, 'detail column should follow the channel columns');
  assert.deepEqual(titles, [
    '时间', '模块', '状态', '用户', '来源 IP', '本地题库 ID', '账单 ID',
    '说明', '命中渠道 / 错误原因', '计费渠道 / 耗时', '详情',
    '请求 ID', '请求参数', '请求接口', '答案数', '原始请求', '原始响应',
  ]);
});

test('LogList uses fixed widths and horizontal scrolling while allowing wraps', async () => {
  const content = await readFile(logListPagePath, 'utf8');
  const columnsStart = content.indexOf('    const columns = computed(() => [');
  const columnsEnd = content.indexOf('\n      ])', columnsStart);
  assert.ok(columnsStart >= 0 && columnsEnd > columnsStart, 'missing log columns definition');

  const columnsSource = content.slice(columnsStart, columnsEnd);
  const columnBlocks = [...columnsSource.matchAll(/title: '([^']+)'([\s\S]*?)(?=\n {8}\},|$)/g)];
  const widths = Object.fromEntries(columnBlocks.map(([, title, body]) => {
    const width = body.match(/width:\s*(logColumnWidths\.\w+|expandedColumnWidth\('[^']+'\))/);
    assert.ok(width, `missing width for ${title}`);
    return [title, width[1]];
  }));

  assert.equal(Object.keys(widths).length, 17, 'all log columns should be included');
  assert.equal(widths['详情'], "expandedColumnWidth('detail')");
  assert.equal(widths['请求参数'], 'logColumnWidths.mode');
  assert.equal(widths['原始请求'], "expandedColumnWidth('requestSnapshot')");
  assert.equal(widths['原始响应'], "expandedColumnWidth('responseSnapshot')");
  for (const title of [
    '模块', '状态', '用户', '本地题库 ID', '说明',
    '命中渠道 / 错误原因', '计费渠道 / 耗时', '请求 ID', '请求接口', '答案数',
  ]) {
    const column = columnBlocks.find(([, columnTitle]) => columnTitle === title);
    assert.match(column?.[2] || '', /width:\s*(?:logColumnWidths\.|expandedColumnWidth\()/, `${title} needs a fixed width`);
  }
  assert.match(content, /:scroll="\{ x: tableScrollX \}"/);
  assert.match(content, /table-layout="fixed"/);
  assert.match(content, /sumTableColumnWidths\(columns\.value\)/);
  assert.doesNotMatch(content, /log-cell-nowrap/);
});

test('LogList formats timestamp and IP as centered two-line cells', async () => {
  assert.equal(logHelpers.formatShanghaiLogDate('2026-10-24T18:18:59Z', '\n'), '2026-10-25\n02:18:59');
  assert.equal(logHelpers.formatShanghaiLogDate('2026-10-24T18:18:59Z'), '2026-10-25 02:18:59');
  assert.equal(logHelpers.formatShanghaiLogDate('invalid', '\n'), '-');
  const content = await readFile(logListPagePath, 'utf8');
  assert.match(content, /class="log-time-cell"[^>]*>\{\{ formatDateForTable\(/);
  assert.match(content, /class="location-value"/);
  assert.match(content, /\.log-time-cell[\s\S]*?white-space:\s*pre-wrap/);
  assert.match(content, /\.location-value[\s\S]*?white-space:\s*pre-wrap/);
  assert.match(content, /<ConsoleTableShell/);
  const styles = await readFile(new URL('../styles/console.css', import.meta.url), 'utf8');
  assert.match(styles, /\.console-table-shell \.ant-table-tbody > tr > td\s*\{[^}]*text-align:\s*center;[^}]*vertical-align:\s*middle/);
});

test('LogList shows a resolved username only for an identified user', async () => {
  assert.equal(logHelpers.buildLogUsername({ userId: 17, username: 'anonymous' }), 'anonymous');
  assert.equal(logHelpers.buildLogUsername({ userId: 17, username: 'member' }), 'member');
  assert.equal(logHelpers.buildLogUsername({ userId: 0, username: 'anonymous' }), '');
  assert.equal(logHelpers.buildLogUsername({ userId: 17, username: '' }), '');
  assert.equal(logHelpers.buildLogUsername({ userId: 17 }), '');
  assert.equal(logHelpers.buildLogUsername({ username: 'anonymous' }), '');
  assert.equal(logHelpers.buildLogUsername({ userId: 'unknown', username: 'anonymous' }), '');
  const content = await readFile(logListPagePath, 'utf8');
  assert.equal(content.match(/buildLogUsername\(record\)/g)?.length, 2, 'desktop and mobile rows use the same rule');
  assert.doesNotMatch(content, /record\.username\s*\|\|\s*`用户 #/);
});

test('LogList raw snapshots preview three lines and can expand', async () => {
  const content = await readFile(logListPagePath, 'utf8');
  const preview = await readFile(new URL('../components/OverflowTableText.vue', import.meta.url), 'utf8');
  assert.match(preview, /-webkit-line-clamp:\s*var\(--preview-lines\)/);
  assert.match(preview, /overflow:\s*hidden/);
  assert.match(preview, /scrollHeight\s*>\s*element\.clientHeight/);
  assert.match(content, /toggleSnapshotExpanded\(record\.id, 'requestSnapshot'\)/);
  assert.match(content, /toggleSnapshotExpanded\(record\.id, 'responseSnapshot'\)/);
});

test('buildLogModuleLabel supports search module wording', () => {
  assert.equal(buildLogModuleLabel('search'), '搜题');
  assert.equal(buildLogModuleLabel('ai_fallback'), 'AI 兜底');
  assert.equal(buildLogModuleLabel('auth'), '认证');
  assert.equal(buildLogModuleLabel('captcha'), '验证码');
  assert.equal(buildLogModuleLabel('sms'), '短信');
  assert.equal(buildLogModuleLabel('recharge'), '充值');
  assert.equal(buildLogModuleLabel('access_token'), '访问令牌');
  assert.equal(buildLogModuleLabel('unknown'), 'unknown');
});

test('buildLogActionLabel renders search action specially for search module', () => {
  assert.equal(buildLogActionLabel(3, 'search'), '搜题');
  assert.equal(buildLogActionLabel(11, 'ai_fallback'), 'AI 兜底');
  assert.equal(buildLogActionLabel(3, 'question'), '查询');
  assert.equal(buildLogActionLabel(10, 'auth'), '登录');
  assert.equal(buildLogActionLabel(20, 'captcha'), '远程生成');
  assert.equal(buildLogActionLabel(21, 'captcha'), '远程校验');
  assert.equal(buildLogActionLabel(22, 'captcha'), '远程二验');
  assert.equal(buildLogActionLabel(23, 'captcha'), '本地生成');
  assert.equal(buildLogActionLabel(24, 'captcha'), '本地校验');
  for (const [action, label] of [[12, '注册'], [13, '退出'], [14, '密码重置'], [15, '手机号修改'], [16, '资料修改'], [30, '发送短信'], [40, '充值下单'], [41, '充值到账']]) {
    assert.equal(buildLogActionLabel(action, 'auth'), label);
    assert.ok(logHelpers.LOG_ACTION_OPTIONS.some((option) => option.value === action && option.label === label));
  }
});

test('account audit modules use snapshot status without treating successful messages as failures', () => {
  for (const module of ['auth', 'user', 'sms', 'recharge', 'access_token']) {
    for (const [status, label, color] of [['success', '成功', 'green'], ['failed', '失败', 'red'], ['pending', '等待中', 'gold']]) {
      const record = {
        module, action: 1, description: '安全操作摘要',
        newAnswer: JSON.stringify({ status, message: '操作结果', requestId: 'request-1', durationMs: 187, provider: 'smsbao', scene: 'login_sms' }),
      };
      assert.equal(buildLogStatusLabel(record), label);
      assert.equal(buildLogStatusColor(record), color);
      assert.equal(buildLogStructuredFields(record).requestId, 'request-1');
      assert.equal(buildLogStructuredFields(record).billingProvider, '187ms');
      assert.equal(buildLogStructuredFields(record).hitProvider, '短信宝');
      assert.equal(buildLogStructuredFields(record).mode, 'scene=login_sms');
      assert.deepEqual(buildLogStructuredFields(record).failureReasons, status === 'failed' ? ['操作结果'] : []);
    }
  }
});

test('recharge audit rows expose order correlation and a readable safe summary', () => {
  const record = {
    module: 'recharge', action: 41, description: '充值到账',
    newAnswer: JSON.stringify({ status: 'success', message: '充值已到账', orderNo: 'rcg_order_1', amount: 10, paymentType: 'alipay' }),
  };
  assert.equal(buildLogStructuredFields(record).summary, '充值到账');
  assert.equal(buildLogStructuredFields(record).requestId, 'rcg_order_1');
  assert.equal(buildLogStructuredFields(record).hitProvider, '支付宝');
  assert.equal(buildLogRowDetail(record), '充值已到账');
});

test('SMS audits read scene and provider from the actual safe request snapshot', () => {
  const record = {
    module: 'sms', action: 30, description: '短信发送成功',
    oldAnswer: JSON.stringify({ scene: 'login', phone: '138****0000', provider: 'smsbao' }),
    newAnswer: JSON.stringify({ status: 'success', message: '短信发送成功', durationMs: 187, smsRecordId: 12 }),
  };
  const fields = buildLogStructuredFields(record);
  assert.equal(fields.mode, 'scene=login');
  assert.equal(fields.hitProvider, '短信宝');
  assert.equal(fields.billingProvider, '187ms');
  assert.equal(buildLogStatusLabel(record), '成功');
  assert.equal(buildLogRowDetail(record), '短信发送成功');
  assert.equal(fields.requestSnapshot, record.oldAnswer);
  assert.equal(fields.responseSnapshot, record.newAnswer);
  assert.deepEqual(fields.failureReasons, []);
});

test('account change details accept before in request and after in response without rendering secrets', () => {
  const record = {
    module: 'access_token', action: 1, description: '更新访问令牌成功',
    oldAnswer: JSON.stringify({ before: { name: '原令牌', totalQuotaLimit: 5, tokenValue: 'secret-old' } }),
    newAnswer: JSON.stringify({ status: 'success', after: { name: '新令牌', totalQuotaLimit: 10, tokenValue: 'secret-new' } }),
  };
  assert.equal(buildLogRowDetail(record), '变更前：名称：原令牌，总额度：5\n变更后：名称：新令牌，总额度：10');
  assert.doesNotMatch(buildLogRowDetail(record), /secret|tokenValue/);
  assert.equal(buildLogStructuredFields({ ...record, oldAnswer: JSON.stringify({ orderNo: 'request-order', provider: 'smsbao', scene: 'old' }), newAnswer: JSON.stringify({ status: 'success', requestId: 'result-request', provider: 'alipay', scene: 'new' }) }).requestId, 'result-request');
  assert.equal(buildLogStructuredFields({ ...record, oldAnswer: JSON.stringify({ orderNo: 'request-order' }), newAnswer: JSON.stringify({ status: 'success' }) }).requestId, 'request-order');
});

test('account change details preserve readable changes without exposing unrecognized secret fields', () => {
  const record = {
    module: 'user', description: '资料修改',
    newAnswer: JSON.stringify({
      status: 'success', message: '资料已更新',
      before: { nickname: '旧昵称', enabled: false, password: 'secret-old' },
      after: { nickname: '新昵称', enabled: true, password: 'secret-new', token: 'secret-token' },
    }),
  };
  assert.equal(buildLogRowDetail(record), '资料已更新\n变更前：昵称：旧昵称，启用：否\n变更后：昵称：新昵称，启用：是');
  assert.doesNotMatch(buildLogRowDetail(record), /secret|password|token/);
  assert.equal(buildLogRowDetail({ module: 'auth', newAnswer: '历史登录结果' }), '历史登录结果');
});

test('local captcha audit snapshots share status, correlation and duration rendering', () => {
  const record = {
    module: 'captcha', action: 24, description: '本地验证码校验', correlationId: 'captcha-local',
    newAnswer: JSON.stringify({ status: 'success', scene: 'register', captchaId: 'captcha-local', durationMs: 0 }),
  };
  assert.equal(buildLogStatusLabel(record), '成功');
  const fields = buildLogStructuredFields(record);
  assert.equal(fields.summary, '本地验证码校验成功');
  assert.equal(fields.requestId, 'captcha-local');
  assert.equal(fields.mode, 'local_check');
  assert.equal(fields.requestSource, 'register');
  assert.equal(fields.billingProvider, '0ms');
  assert.match(buildLogRowDetail(record), /场景：register/);
  assert.doesNotMatch(buildLogRowDetail(record), /HTTP success/);
  const failed = { ...record, newAnswer: JSON.stringify({ status: 'failed', message: '验证码无效' }) };
  assert.equal(buildLogStatusLabel(failed), '失败');
  assert.equal(buildLogStructuredFields(failed).requestId, 'captcha-local');
  assert.deepEqual(buildLogStructuredFields(failed).failureReasons, ['验证码无效']);
});

test('expandable detail text retains the complete answer and legacy response', () => {
  const text = '需要完整显示的答案'.repeat(100);
  assert.equal(buildLogTableDetail({ module: 'search', newAnswer: JSON.stringify({ answer: { answerText: text } }) }), `答案：${text}`);
  assert.equal(buildLogTableDetail({ module: 'question', newAnswer: text }), text);
});

test('buildLogActionColor keeps search action purple and login gold', () => {
  assert.equal(buildLogActionColor(3), 'purple');
  assert.equal(buildLogActionColor(10), 'gold');
  assert.equal(buildLogActionColor(20), 'cyan');
  assert.equal(buildLogActionColor(21), 'geekblue');
  assert.equal(buildLogActionColor(22), 'volcano');
});

test('buildLogStatusLabel renders success and failure states for AI fallback and search logs', () => {
  assert.equal(buildLogStatusLabel({
    module: 'ai_fallback',
    description: 'AI兜底完成 status=success stage=result mode=async requestId=req-1',
  }), '成功');
  assert.equal(buildLogStatusLabel({
    module: 'ai_fallback',
    description: 'AI兜底失败 status=failed stage=result mode=async requestId=req-2 code=NO_ANSWER',
  }), '失败');
  assert.equal(buildLogStatusLabel({
    module: 'search',
    description: '搜索完成 requestId=req-3 mode=normal answers=1',
  }), '成功');
  assert.equal(buildLogStatusLabel({
    module: 'search',
    description: '搜索失败 detail=当前余额不足 method=POST path=/adapter-service/search',
  }), '失败');
});

test('buildLogDetailSections exposes structured search summary with raw request and response blocks', () => {
  assert.deepEqual(buildLogDetailSections({
    description: '搜索完成 requestId=952e69ad-71aa-419b-8ccc-9599574d81ff mode=normal use= answers=4 hitProvider=icodef billingProvider=icodef localQuestionId=321 anonymous=true',
    oldAnswer: 'method=POST body={"question":"测试"}',
    newAnswer: '{"answer":["A"]}',
  }), [
    {
      key: 'requestId',
      label: '请求 ID',
      value: '952e69ad-71aa-419b-8ccc-9599574d81ff',
    },
    {
      key: 'requestParams',
      label: '请求参数',
      value: '-',
    },
    {
      key: 'answers',
      label: '答案数',
      value: '4',
    },
    {
      key: 'hitProvider',
      label: '命中渠道',
      value: 'icodef',
    },
    {
      key: 'billingProvider',
      label: '计费渠道',
      value: 'Icodef 题库',
    },
    {
      key: 'localQuestionId',
      label: '本地题库 ID',
      value: '321',
    },
    {
      key: 'anonymous',
      label: '匿名请求',
      value: '是',
    },
    {
      key: 'request',
      label: '原始请求',
      value: 'method=POST body={"question":"测试"}',
    },
    {
      key: 'response',
      label: '原始响应',
      value: '{"answer":["A"]}',
    },
  ]);
});


test('buildLogDetailSections keeps anonymous false and explicit request source in structured sections', () => {
  assert.deepEqual(buildLogDetailSections({
    description: '搜索完成 requestId=req-1 mode=normal use=local,icodef answers=1 hitProvider=local billingProvider=local anonymous=false',
    oldAnswer: 'method=POST path=/adapter-service/search query=use=local%2Cicodef contentType=application/json body={}',
  }), [
    {
      key: 'requestId',
      label: '请求 ID',
      value: 'req-1',
    },
    {
      key: 'requestParams',
      label: '请求参数',
      value: 'use=local,icodef',
    },
    {
      key: 'answers',
      label: '答案数',
      value: '1',
    },
    {
      key: 'hitProvider',
      label: '命中渠道',
      value: 'local',
    },
    {
      key: 'billingProvider',
      label: '计费渠道',
      value: '本地题库',
    },
    {
      key: 'anonymous',
      label: '匿名请求',
      value: '否',
    },
    {
      key: 'request',
      label: '原始请求',
      value: 'method=POST path=/adapter-service/search query=use=local%2Cicodef contentType=application/json body={}',
    },
  ]);
});

test('buildLogDetailSections keeps non-search descriptions as plain text snapshots only', () => {
  assert.deepEqual(buildLogDetailSections({ oldAnswer: 'before', newAnswer: 'after' }), [
    {
      key: 'request',
      label: '原始请求',
      value: 'before',
    },
    {
      key: 'response',
      label: '原始响应',
      value: 'after',
    },
  ]);
});

test('buildLogStructuredFields splits search description and snapshots into single-line columns', () => {
  assert.deepEqual(buildLogStructuredFields({
    description: '搜索完成 requestId=952e69ad-71aa-419b-8ccc-9599574d81ff mode=normal use= answers=4 hitProvider=icodef billingProvider=icodef localQuestionId=321 anonymous=true',
    oldAnswer: 'method=POST body={"question":"测试"}',
    newAnswer: '{"answer":["A"]}',
  }), {
    summary: '搜索完成',
    requestId: '952e69ad-71aa-419b-8ccc-9599574d81ff',
    mode: '-',
    requestSource: '普通搜题接口',
    answerCount: '4',
    hitProvider: 'icodef',
    failureReasons: [],
    billingProvider: 'Icodef 题库',
    requestSnapshot: 'method=POST body={"question":"测试"}',
    responseSnapshot: '{"answer":["A"]}',
  });
});

test('buildLogStructuredFields maps billing provider codes to Chinese labels', () => {
  assert.equal(buildLogStructuredFields({
    description: '搜索完成 requestId=req-ai mode=normal use= answers=1 hitProvider=ai billingProvider=ai',
  }).billingProvider, 'AI 搜索');

  assert.equal(buildLogStructuredFields({
    description: '搜索完成 requestId=req-mixed mode=normal use=icodef answers=1 hitProvider=ai billingProvider=icodef,ai',
  }).billingProvider, 'Icodef 题库、AI 搜索');

  assert.equal(buildLogStructuredFields({
    description: '搜索完成 requestId=req-tools mode=normal use=ai answers=1 hitProvider=ai billingProvider=ai,ai_search,web_read,custom_provider',
  }).billingProvider, 'AI 搜索、联网搜索、浏览网页、custom_provider');

  assert.equal(buildLogStructuredFields({
    description: '搜索完成 requestId=req-failed mode=normal use= answers=1 hitProvider=ai billingProvider=billing_persist_failed',
  }).billingProvider, '计费落账失败');
});

test('buildLogStructuredFields shows request parameters and channel failure details', () => {
  const fields = buildLogStructuredFields({
    description: '搜索失败 requestId=req-failed mode=ai use=ai answers=0',
    oldAnswer: 'method=POST path=/adapter-service/search query=use=ai contentType=application/json body={}',
    newAnswer: JSON.stringify({
      status: 'failed',
      failures: [{ provider: 'ai', code: 'UPSTREAM_ERROR', error: '对方服务器异常' }],
    }),
  });

  assert.equal(fields.mode, 'use=ai');
  assert.equal(fields.requestSource, '普通搜题接口');
  assert.equal(fields.hitProvider, 'AI');
  assert.deepEqual(fields.failureReasons, ['[UPSTREAM_ERROR]：对方服务器异常']);
});

test('request parameters reflect only safe parameters actually present in request snapshots', () => {
  const cases = [
    {
      description: '搜索完成 mode=normal use= answers=1',
      request: 'method=POST path=/adapter-service/search query= contentType=application/json body={"question":"测试"}',
      expected: '-',
    },
    {
      description: '搜索完成 mode=normal use=other answers=1',
      request: 'method=POST path=/adapter-service/search query=key=private-key&model=test-model&mode=ai&token=private-token&url=https%3A%2F%2Fprivate.example%2F%3Fsecret%3Dprivate-secret&use=ai contentType=application/json body={"question":"测试","deepThinking":true,"skipCache":false}',
      expected: 'use=ai mode=ai model=test-model deepThinking=true skipCache=false',
    },
    {
      description: 'AI兜底完成 status=success stage=completed mode=async deepThinking=true',
      request: '{"question":"测试","deepThinking":true,"skipCache":true}',
      expected: 'deepThinking=true skipCache=true',
    },
    {
      description: 'AI兜底失败 status=failed stage=billing_precheck mode=sync',
      request: '{"question":"测试"}',
      expected: '-',
    },
    {
      description: '搜索完成 mode=ai use=ai answers=1',
      request: '',
      expected: '-',
    },
  ];
  for (const tc of cases) {
    assert.equal(buildLogStructuredFields({ description: tc.description, oldAnswer: tc.request }).mode, tc.expected);
  }
});

test('channel failures retain complete long diagnostic text separately from the provider', () => {
  const detail = `AI联网搜题第1轮失败: HTTP 502 ${'完整上游错误'.repeat(100)}`;
  const fields = buildLogStructuredFields({
    description: '搜索失败 mode=ai answers=0',
    newAnswer: JSON.stringify({ failures: [{ provider: 'ai', code: 'UPSTREAM_ERROR', error: detail }] }),
  });
  assert.equal(fields.hitProvider, 'AI');
  assert.deepEqual(fields.failureReasons, [`[UPSTREAM_ERROR]：${detail}`]);
});

test('buildLogStructuredFields keeps non-search logs on summary and snapshot columns', () => {
  assert.deepEqual(buildLogStructuredFields({
    description: '用户登录成功',
    oldAnswer: 'before',
    newAnswer: 'after',
  }), {
    summary: '用户登录成功',
    requestId: '-',
    mode: '-',
    requestSource: '-',
    answerCount: '-',
    hitProvider: '-',
    failureReasons: [],
    billingProvider: '-',
    requestSnapshot: 'before',
    responseSnapshot: 'after',
  });
});

test('buildLogStructuredFields parses AI fallback failures for list columns', () => {
  assert.deepEqual(buildLogStructuredFields({
    description: 'AI兜底失败 status=failed stage=billing_precheck mode=async requestId=req-ai-fallback taskId=task-1 code=INSUFFICIENT_BALANCE message=当前余额不足',
    oldAnswer: '{"question":"1+1=?","type":0,"options":["1","2"]}',
    newAnswer: '{"success":false,"error":{"code":"INSUFFICIENT_BALANCE","message":"当前余额不足"}}',
  }), {
    summary: 'AI兜底失败',
    requestId: 'req-ai-fallback',
    mode: '-',
    requestSource: 'AI 兜底接口',
    answerCount: '-',
    hitProvider: 'AI 普通搜索',
    failureReasons: ['当前余额不足，AI 兜底已禁用，请充值后再使用'],
    billingProvider: '-',
    requestSnapshot: '{"question":"1+1=?","type":0,"options":["1","2"]}',
    responseSnapshot: '{"success":false,"error":{"code":"INSUFFICIENT_BALANCE","message":"当前余额不足"}}',
  });
});

test('buildLogStructuredFields summarizes failed search logs', () => {
  assert.equal(buildLogStructuredFields({
    description: '搜索失败 detail=当前余额不足，系统题库已禁用，请充值后再使用 method=POST path=/adapter-service/search clientIp=192.0.2.1 authorization=Bearer tok...1234',
  }).summary, '搜索失败');
});

test('buildLogDetailSections exposes structured captcha remote status fields', () => {
  assert.deepEqual(buildLogDetailSections({
    description: '远程验证码校验 step=remote_check result=failed status=403 remoteCode=403 captchaId=captcha-1 durationMs=25 hasKey=true',
    oldAnswer: 'method=POST path=/check body={"id":"captcha-1"}',
    newAnswer: '{"code":403,"message":"bad track"}',
  }), [
    {
      key: 'step',
      label: '远程步骤',
      value: 'remote_check',
    },
    {
      key: 'result',
      label: '结果',
      value: '失败',
    },
    {
      key: 'status',
      label: 'HTTP 状态',
      value: '403',
    },
    {
      key: 'remoteCode',
      label: '远程返回码',
      value: '403',
    },
    {
      key: 'remoteValid',
      label: '远程有效',
      value: '-',
    },
    {
      key: 'scene',
      label: '场景',
      value: '-',
    },
    {
      key: 'captchaId',
      label: '验证码 ID',
      value: 'captcha-1',
    },
    {
      key: 'type',
      label: '验证码类型',
      value: '-',
    },
    {
      key: 'durationMs',
      label: '耗时(ms)',
      value: '25',
    },
    {
      key: 'hasKey',
      label: '已配置密钥',
      value: '是',
    },
    {
      key: 'request',
      label: '原始请求',
      value: 'method=POST path=/check body={"id":"captcha-1"}',
    },
    {
      key: 'response',
      label: '原始响应',
      value: '{"code":403,"message":"bad track"}',
    },
  ]);
});

test('buildLogStructuredFields summarizes captcha logs for table columns', () => {
  assert.deepEqual(buildLogStructuredFields({
    description: '远程验证码二验 step=remote_check2 result=success status=200 remoteValid=true scene=login_sms captchaId=captcha-2 durationMs=13 hasKey=false',
    oldAnswer: 'method=GET path=/check2 id=captcha-2',
    newAnswer: 'true',
  }), {
    summary: '远程验证码二验成功',
    requestId: 'captcha-2',
    mode: 'remote_check2',
    requestSource: 'login_sms',
    answerCount: '200',
    hitProvider: 'true',
    failureReasons: [],
    billingProvider: '13ms',
    requestSnapshot: 'method=GET path=/check2 id=captcha-2',
    responseSnapshot: 'true',
  });
});

test('buildLogRowDetail exposes compact remote captcha diagnostics for visible table column', () => {
  assert.equal(buildLogRowDetail({
    description: '远程验证码二验 step=remote_check2 result=success status=200 remoteValid=true scene=login_sms captchaId=captcha-2 durationMs=13 hasKey=false',
    oldAnswer: 'method=GET path=/check2 query=id%3Dcaptcha-2 hasKey=false',
    newAnswer: 'true',
  }), 'HTTP 200 / 有效：是 / 场景：login_sms / ID：captcha-2 / 耗时：13ms');

  assert.equal(buildLogRowDetail({
    description: '远程验证码校验 step=remote_check result=failed remoteCode=403 captchaId=captcha-3 hasKey=true',
    newAnswer: '{"code":403,"message":"bad track"}',
  }), '远程码：403 / ID：captcha-3 / 响应：{"code":403,"message":"bad track"}');
});

test('LogList page renders visible detail column for structured diagnostics', async () => {
  const content = await readFile(logListPagePath, 'utf8');

  assert.match(content, /#bodyCell="\{ column, record, text \}"/);
  assert.match(content, /column.key === 'detail'/);
  assert.match(content, /buildRowDetail\(record\)/);
  assert.match(content, /buildLogRowDetail/);
  assert.match(content, /title: '详情'/);
  assert.match(content, /:text="buildTableDetail\(record\)"/);
  assert.match(content, /title: '请求参数'/);
  assert.match(content, /title: '请求接口'/);
  assert.match(content, /title: '命中渠道 \/ 错误原因'/);
});

test('LogList page renders derived status instead of the raw action column', async () => {
  const content = await readFile(logListPagePath, 'utf8');

  assert.match(content, /title: '状态'/);
  assert.match(content, /column.key === 'status'/);
  assert.match(content, /buildStatusLabel\(record\)/);
  assert.doesNotMatch(content, /title: '操作'/);
});

test('LogList separates channel and complete error text with an expandable preview', async () => {
  const content = await readFile(logListPagePath, 'utf8');
  const source = content.match(/<template v-else-if="column.key === 'hitProvider'">([\s\S]*?)<\/template>/)?.[1];
  assert.ok(source, 'missing channel cell template');
  assert.match(source, /class="log-channel-name"/);
  assert.match(source, /class="log-channel-error"/);
  assert.match(source, /failureReasons\.join\('\\n'\)/);
  assert.match(source, /toggleSnapshotExpanded\(record\.id, 'hitProvider'\)/);
  assert.doesNotMatch(source, /> \/ \{\{ reason \}\}/);
  const detail = '完整错误'.repeat(100);
  const fields = buildLogStructuredFields({
    description: '搜索失败 mode=ai answers=0',
    newAnswer: JSON.stringify({ failures: [{ provider: 'ai', code: 'UPSTREAM_ERROR', error: detail }] }),
  });
  assert.ok(fields.failureReasons[0].includes(detail));
});

test('LogList formats timestamps in Shanghai time', async () => {
  const content = await readFile(logListPagePath, 'utf8');

  assert.match(content, /formatShanghaiLogDate\(dateString/);
  assert.match(content, /formatShanghaiLogDate\(dateString, '\\n'\)/);
});

test('search outcome summaries retain structured request fields', () => {
  for (const summary of ['搜索完成，部分渠道失败', '搜索完成，未找到答案', '搜索未执行，服务未配置或未启用']) {
    const fields = buildLogStructuredFields({
      description: `${summary} requestId=request-1 mode=normal use=ai answers=0 hitProvider=- billingProvider=-`,
      oldAnswer: 'method=POST path=/adapter-service/search query=use=ai contentType=application/json body={}',
    });
    assert.equal(fields.summary, summary);
    assert.equal(fields.requestId, 'request-1');
    assert.equal(fields.mode, 'use=ai');
    assert.equal(fields.requestSource, '普通搜题接口');
    assert.equal(fields.answerCount, '0');
  }
});

test('AI route labels and unauthorized search detail are visible in channel errors', () => {
  const deepMcp = buildLogStructuredFields({
    module: 'search',
    description: '搜索失败 requestId=req-mcp mode=ai answers=0 aiRoute=deep_mcp hitProvider=ai',
    newAnswer: JSON.stringify({ status: 'failed', error: { message: '服务器内部错误，搜题失败。' } }),
  });
  assert.equal(deepMcp.hitProvider, 'AI 深度思考 MCP编排');
  assert.deepEqual(deepMcp.failureReasons, ['服务器内部错误，搜题失败。']);

  const ordinary = buildLogStructuredFields({
    module: 'search',
    description: '搜索失败 detail=当前余额不足，请充值后再使用 provider= code=INSUFFICIENT_BALANCE error=当前余额不足，请充值后再使用',
    newAnswer: JSON.stringify({ detail: '当前余额不足，请充值后再使用' }),
  });
  assert.ok(ordinary.failureReasons.some((reason) => reason.includes('当前余额不足')));
});

test('ordinary balance rejection shows one readable reason without changing its source', () => {
  const message = '当前余额不足，系统题库已禁用，请充值后再使用';
  const record = {
    module: 'search',
    description: `搜索失败 provider= code=INSUFFICIENT_BALANCE error=${message}`,
    newAnswer: JSON.stringify({
      code: 'INSUFFICIENT_BALANCE',
      message: '访问令牌鉴权失败',
      detail: message,
      failures: [{ provider: '', code: 'INSUFFICIENT_BALANCE', error: message }],
    }),
  };
  const fields = buildLogStructuredFields(record);
  assert.equal(fields.hitProvider, '-');
  assert.deepEqual(fields.failureReasons, [message]);
  assert.equal(fields.responseSnapshot, record.newAnswer);
  assert.equal(buildLogTableDetail(record), '-');
});

test('AI fallback balance rejection uses its own message and restores the requested route', () => {
  for (const [deepThinking, label] of [[false, 'AI 普通搜索'], [true, 'AI 深度思考']]) {
    const record = {
      module: 'ai_fallback',
      description: 'AI兜底失败 status=failed code=INSUFFICIENT_BALANCE',
      oldAnswer: JSON.stringify({ question: '题目', deepThinking }),
      newAnswer: JSON.stringify({
        success: false,
        error: { code: 'INSUFFICIENT_BALANCE', message: '当前余额不足，系统题库已禁用，请充值后再使用' },
        failureDetail: '当前余额不足，系统题库已禁用，请充值后再使用',
      }),
    };
    const fields = buildLogStructuredFields(record);
    assert.equal(fields.hitProvider, label);
    assert.deepEqual(fields.failureReasons, ['当前余额不足，AI 兜底已禁用，请充值后再使用']);
    assert.equal(fields.responseSnapshot, record.newAnswer);
  }
});

test('ordinary AI rejection restores its route from the unauthorized request snapshot', () => {
  for (const [deepThinking, label] of [[false, 'AI 普通搜索'], [true, 'AI 深度思考']]) {
    const message = '当前余额不足，系统题库已禁用，请充值后再使用';
    const fields = buildLogStructuredFields({
      module: 'search',
      description: '搜索失败 provider=ai code=INSUFFICIENT_BALANCE',
      oldAnswer: JSON.stringify({ method: 'POST', path: '/adapter-service/search', body: JSON.stringify({ deepThinking }) }),
      newAnswer: JSON.stringify({ detail: message, failures: [{ provider: 'ai', code: 'INSUFFICIENT_BALANCE', error: message }] }),
    });
    assert.equal(fields.hitProvider, label);
    assert.deepEqual(fields.failureReasons, [message]);
  }
});

test('balance failure descriptions are readable without reclassifying another question bank as AI', () => {
  const message = '上游账号余额不足';
  const fields = buildLogStructuredFields({
    module: 'search',
    description: `搜索失败 provider=wanneng code=INSUFFICIENT_BALANCE error=${message}`,
    newAnswer: '{}',
  });
  assert.equal(fields.hitProvider, 'wanneng');
  assert.deepEqual(fields.failureReasons, [message]);
});

test('duplicate response fields do not hide distinct provider failures', () => {
  const record = {
    module: 'search',
    description: '搜索失败',
    newAnswer: JSON.stringify({
      failures: [
        { provider: 'ai', code: 'UPSTREAM_ERROR', error: 'HTTP 503' },
        { provider: 'wanneng', code: 'UPSTREAM_ERROR', error: 'HTTP 503' },
      ],
      error: { message: 'HTTP 503' },
      detail: 'HTTP 503',
    }),
  };
  const fields = buildLogStructuredFields(record);
  assert.deepEqual(fields.failureReasons, ['ai [UPSTREAM_ERROR]：HTTP 503', 'wanneng [UPSTREAM_ERROR]：HTTP 503']);
  assert.equal(fields.responseSnapshot, record.newAnswer);
});

test('explicit AI route takes priority over the requested deep-thinking flag', () => {
  const fields = buildLogStructuredFields({
    module: 'ai_fallback',
    description: 'AI兜底失败 hitProvider=ai aiRoute=deep_mcp code=INSUFFICIENT_BALANCE',
    oldAnswer: JSON.stringify({ deepThinking: true }),
    newAnswer: JSON.stringify({ error: { code: 'INSUFFICIENT_BALANCE', message: '当前余额不足' } }),
  });
  assert.equal(fields.hitProvider, 'AI 深度思考 MCP编排');
  assert.deepEqual(fields.failureReasons, ['当前余额不足，AI 兜底已禁用，请充值后再使用']);
});
