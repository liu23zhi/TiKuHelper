import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildLogActionColor,
  buildLogActionLabel,
  buildLogDetailSections,
  buildLogModuleLabel,
  buildLogQidLabel,
  buildLogStructuredFields,
} from './logListHelpers.mjs';

test('buildLogModuleLabel supports search module wording', () => {
  assert.equal(buildLogModuleLabel('search'), '搜题');
  assert.equal(buildLogModuleLabel('auth'), '认证');
  assert.equal(buildLogModuleLabel('unknown'), 'unknown');
});

test('buildLogActionLabel renders search action specially for search module', () => {
  assert.equal(buildLogActionLabel(3, 'search'), '搜题');
  assert.equal(buildLogActionLabel(3, 'question'), '查询');
  assert.equal(buildLogActionLabel(10, 'auth'), '登录');
});

test('buildLogActionColor keeps search action purple and login gold', () => {
  assert.equal(buildLogActionColor(3), 'purple');
  assert.equal(buildLogActionColor(10), 'gold');
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

