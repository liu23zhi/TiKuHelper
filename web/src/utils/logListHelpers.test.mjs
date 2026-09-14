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
  buildLogQidLabel,
  buildLogRowDetail,
  buildLogStructuredFields,
} from './logListHelpers.mjs';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const logListPagePath = path.join(__dirname, '..', 'pages', 'LogList', 'index.vue');

test('buildLogModuleLabel supports search module wording', () => {
  assert.equal(buildLogModuleLabel('search'), '搜题');
  assert.equal(buildLogModuleLabel('auth'), '认证');
  assert.equal(buildLogModuleLabel('captcha'), '验证码');
  assert.equal(buildLogModuleLabel('unknown'), 'unknown');
});

test('buildLogActionLabel renders search action specially for search module', () => {
  assert.equal(buildLogActionLabel(3, 'search'), '搜题');
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
      key: 'mode',
      label: '模式',
      value: 'normal',
    },
    {
      key: 'use',
      label: '请求来源',
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
      value: 'icodef',
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
      key: 'mode',
      label: '模式',
      value: 'normal',
    },
    {
      key: 'use',
      label: '请求来源',
      value: 'local,icodef',
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
      value: 'local',
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
    mode: 'normal',
    requestSource: '-',
    answerCount: '4',
    hitProvider: 'icodef',
    billingProvider: 'icodef',
    requestSnapshot: 'method=POST body={"question":"测试"}',
    responseSnapshot: '{"answer":["A"]}',
  });
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
});
