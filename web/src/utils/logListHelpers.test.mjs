import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  buildLogActionColor,
  buildLogActionLabel,
  buildLogDetailSections,
  buildLogModuleLabel,
  buildLogRowDetail,
  buildLogStatusLabel,
  buildLogStatusColor,
  buildLogStructuredFields,
} from './logListHelpers.mjs';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const logListPagePath = path.join(__dirname, '..', 'pages', 'LogList', 'index.vue');

test('search failures show provider causes and distinguish partial failure and no answer', () => {
  const record = {
    module: 'search',
    description: '搜索失败 requestId=req-1 mode=ai answers=0 status=failed',
    newAnswer: JSON.stringify({ status: 'failed', failures: [{ provider: 'ai', code: 'UPSTREAM_ERROR', error: 'HTTP 401: invalid api key' }] }),
  };
  assert.equal(buildLogStatusLabel(record), '失败');
  assert.equal(buildLogRowDetail(record), 'ai [UPSTREAM_ERROR]：HTTP 401: invalid api key');
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
  assert.equal(buildLogRowDetail(record), 'HTTP 429: rate limited');
  assert.equal(buildLogStructuredFields(record).responseSnapshot, record.newAnswer);
});

test('buildLogModuleLabel supports search module wording', () => {
  assert.equal(buildLogModuleLabel('search'), '搜题');
  assert.equal(buildLogModuleLabel('ai_fallback'), 'AI 兜底');
  assert.equal(buildLogModuleLabel('auth'), '认证');
  assert.equal(buildLogModuleLabel('captcha'), '验证码');
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
      value: 'mode=normal',
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
    mode: 'mode=normal',
    requestSource: '普通搜题接口',
    answerCount: '4',
    hitProvider: 'icodef',
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
    description: '搜索完成 requestId=req-failed mode=normal use= answers=1 hitProvider=ai billingProvider=billing_persist_failed',
  }).billingProvider, '计费落账失败');
});

test('buildLogStructuredFields shows request parameters and channel failure details', () => {
  const fields = buildLogStructuredFields({
    description: '搜索失败 requestId=req-failed mode=ai use=ai answers=0',
    newAnswer: JSON.stringify({
      status: 'failed',
      failures: [{ provider: 'ai', code: 'UPSTREAM_ERROR', error: '对方服务器异常' }],
    }),
  });

  assert.equal(fields.mode, 'use=ai');
  assert.equal(fields.requestSource, '普通搜题接口');
  assert.equal(fields.hitProvider, 'ai（错误：[UPSTREAM_ERROR]：对方服务器异常）');
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
    mode: 'mode=async stage=billing_precheck',
    requestSource: 'AI 兜底接口',
    answerCount: '-',
    hitProvider: 'ai（错误：当前余额不足）',
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

  assert.match(content, /<template #detail="\{ record \}">/);
  assert.match(content, /buildRowDetail\(record\)/);
  assert.match(content, /buildLogRowDetail/);
  assert.match(content, /title: '详情'/);
  assert.match(content, /customRender: 'detail'/);
  assert.match(content, /title: '请求参数'/);
  assert.match(content, /title: '请求接口'/);
  assert.match(content, /title: '命中渠道 \/ 错误原因'/);
});

test('LogList page renders derived status instead of the raw action column', async () => {
  const content = await readFile(logListPagePath, 'utf8');

  assert.match(content, /title: '状态'/);
  assert.match(content, /<template #status="\{ record \}">/);
  assert.match(content, /buildStatusLabel\(record\)/);
  assert.doesNotMatch(content, /title: '操作'/);
});

test('LogList formats timestamps in Shanghai time', async () => {
  const content = await readFile(logListPagePath, 'utf8');

  assert.match(content, /timeZone:\s*'Asia\/Shanghai'/);
  assert.match(content, /new Intl\.DateTimeFormat\('zh-CN'/);
  assert.doesNotMatch(content, /date\.getHours\(\)/);
});

test('search outcome summaries retain structured request fields', () => {
  for (const summary of ['搜索完成，部分渠道失败', '搜索完成，未找到答案', '搜索未执行，服务未配置或未启用']) {
    const fields = buildLogStructuredFields({
      description: `${summary} requestId=request-1 mode=normal use=ai answers=0 hitProvider=- billingProvider=-`,
    });
    assert.equal(fields.summary, summary);
    assert.equal(fields.requestId, 'request-1');
    assert.equal(fields.mode, 'use=ai');
    assert.equal(fields.requestSource, '普通搜题接口');
    assert.equal(fields.answerCount, '0');
  }
});
