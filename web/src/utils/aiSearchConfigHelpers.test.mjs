import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildAISearchTestForm,
  buildAISearchTestPayload,
  buildAISearchTestResult,
  hasAISearchTestResult,
  buildAIToolTestPayload,
  buildAIToolTestResult,
} from './aiSearchConfigHelpers.mjs';

test('buildAISearchTestForm seeds the dialog from current AI search config', () => {
  assert.deepEqual(buildAISearchTestForm({
    baseUrl: ' https://api.example.com/v1 ',
    apiKey: ' test-key ',
    model: ' gpt-4o-mini ',
    prompt: ' 系统 Prompt ',
  }), {
    baseUrl: 'https://api.example.com/v1',
    apiKey: 'test-key',
    model: 'gpt-4o-mini',
    prompt: '系统 Prompt',
  });

  assert.deepEqual(buildAISearchTestForm(null), {
    baseUrl: '',
    apiKey: '',
    model: '',
    prompt: '',
  });
});

test('buildAISearchTestPayload prefers explicit dialog overrides and falls back to current config', () => {
  assert.deepEqual(buildAISearchTestPayload({
    baseUrl: ' ',
    apiKey: ' dialog-key ',
    model: ' ',
    prompt: '',
  }, {
    baseUrl: 'https://fallback.example.com/v1',
    apiKey: 'fallback-key',
    model: 'gpt-4o',
    prompt: '默认 Prompt',
  }), {
    baseUrl: 'https://fallback.example.com/v1',
    apiKey: 'dialog-key',
    model: 'gpt-4o',
    prompt: '默认 Prompt',
  });
});

test('buildAISearchTestPayload requires a model after fallback resolution', () => {
  assert.throws(() => buildAISearchTestPayload({
    baseUrl: '',
    apiKey: '',
    model: ' ',
    prompt: '',
  }, {
    model: ' ',
  }), /请选择要测试的 AI 模型/);
});

test('buildAISearchTestResult normalizes response fields for the dialog', () => {
  assert.deepEqual(buildAISearchTestResult({
    data: {
      endpoint: 'https://api.example.com/v1/chat/completions',
      answers: ['你好', '世界'],
      requestBody: '{"hello":true}',
      rawResponse: 'data: [DONE]',
    },
  }), {
    answers: ['你好', '世界'],
    requestBody: 'POST https://api.example.com/v1/chat/completions\n\n{"hello":true}',
    rawResponse: 'data: [DONE]',
  });

  assert.deepEqual(buildAISearchTestResult({ data: null }), {
    answers: [],
    requestBody: '',
    rawResponse: '',
  });
});

test('hasAISearchTestResult detects whether any result block should be shown', () => {
  assert.equal(hasAISearchTestResult({ answers: [], requestBody: '', rawResponse: '' }), false);
  assert.equal(hasAISearchTestResult({ answers: ['hello'], requestBody: '', rawResponse: '' }), true);
  assert.equal(hasAISearchTestResult({ answers: [], requestBody: '{"a":1}', rawResponse: '' }), true);
});

test('direct tool tests validate the selected input without requiring a model', () => {
  const payload = buildAIToolTestPayload({ mode: 'direct', tool: 'web_search', query: ' OpenAI ' });
  assert.equal(payload.query, 'OpenAI');
  assert.equal(payload.model, '');
  assert.throws(() => buildAIToolTestPayload({ mode: 'direct', tool: 'web_search', query: ' ' }), /搜索词/);
  assert.throws(() => buildAIToolTestPayload({ mode: 'direct', tool: 'web_read', url: 'file:///private' }), /HTTP/);
  assert.equal(buildAIToolTestPayload({ mode: 'direct', tool: 'web_read', url: 'https://example.com/' }).url, 'https://example.com/');
});

test('AI tool tests require a model and resolve current connection overrides', () => {
  assert.throws(() => buildAIToolTestPayload({ mode: 'ai', tool: 'web_search', query: 'OpenAI' }), /模型/);
  const payload = buildAIToolTestPayload({ mode: 'ai', tool: 'web_read', url: 'https://example.com/', apiKey: ' override ' }, {
    model: 'gpt-test', baseUrl: 'https://api.example.com/v1', apiKey: 'saved', prompt: 'prompt',
  });
  assert.equal(payload.model, 'gpt-test');
  assert.equal(payload.apiKey, 'override');
  assert.equal(payload.baseUrl, 'https://api.example.com/v1');
  assert.throws(() => buildAIToolTestPayload({ mode: 'unknown', tool: 'web_search', query: 'q' }), /测试类型/);
  assert.throws(() => buildAIToolTestPayload({ mode: 'direct', tool: 'unknown', query: 'q' }), /工具/);
});

test('AI tests accept one target for either a single tool or both tools', () => {
  for (const tool of ['web_search', 'both']) {
    const payload = buildAIToolTestPayload({ mode: 'ai', tool, target: ' OpenAI 最新动态 ', model: 'gpt-test' });
    assert.equal(payload.target, 'OpenAI 最新动态');
    assert.equal(payload.tool, tool);
  }
  const payload = buildAIToolTestPayload({ mode: 'ai', tool: 'web_read', target: ' https://example.com/article ', model: 'gpt-test' });
  assert.equal(payload.target, 'https://example.com/article');
  const legacy = buildAIToolTestPayload({ mode: 'ai', tool: 'both', query: 'OpenAI', url: 'https://example.com/article', model: 'gpt-test' });
  assert.equal(legacy.target, 'https://example.com/article');
  assert.throws(() => buildAIToolTestPayload({ mode: 'direct', tool: 'both', query: 'OpenAI' }), /工具/);
});

test('AI targets validate URL safety and count keyword Unicode characters', () => {
  const ai = { mode: 'ai', tool: 'both', model: 'gpt-test' };
  assert.equal(buildAIToolTestPayload({ ...ai, target: '中'.repeat(120) }).target.length, 120);
  assert.throws(() => buildAIToolTestPayload({ ...ai, target: '中'.repeat(121) }), /120/);
  assert.equal([...buildAIToolTestPayload({ ...ai, target: '😀'.repeat(120) }).target].length, 120);
  assert.equal(buildAIToolTestPayload({ ...ai, target: 'OpenAI: API' }).target, 'OpenAI: API');
  assert.throws(() => buildAIToolTestPayload({ ...ai, target: ' ' }), /关键词|地址/);
  assert.throws(() => buildAIToolTestPayload({ ...ai, target: ' ', query: 'stale keyword', url: 'https://example.com/' }), /关键词|地址/);
  assert.throws(() => buildAIToolTestPayload({ ...ai, tool: 'web_read', target: 'OpenAI' }), /HTTP|地址/);
  for (const target of ['ftp://example.com/a', 'https://user:pass@example.com/a', 'https://@example.com/a', 'https://example.com/a#section']) {
    assert.throws(() => buildAIToolTestPayload({ ...ai, target }), /HTTP|地址|凭据|片段/);
  }
  const url = `https://example.com/${'a'.repeat(200)}`;
  assert.equal(buildAIToolTestPayload({ ...ai, target: url }).target, url);
  assert.throws(() => buildAIToolTestPayload({ ...ai, target: `https://example.com/${'a'.repeat(2048)}` }), /2048/);
});

test('generated AI prompts request only the selected tool and use the exact input', async () => {
  const { buildAIToolTestPrompt } = await import('./aiSearchConfigHelpers.mjs');
  assert.equal(typeof buildAIToolTestPrompt, 'function');
  const search = buildAIToolTestPrompt({ tool: 'web_search', target: ' OpenAI <新闻> ' });
  assert.match(search, /用户输入："OpenAI <新闻>"/);
  assert.match(search, /调用一次 web_search/);
  assert.doesNotMatch(search, /web_read/);
  const read = buildAIToolTestPrompt({ tool: 'web_read', target: 'https://example.com/a?x=1&y=2' });
  assert.match(read, /调用一次 web_read/);
  assert.match(read, /等待正文返回/);
  assert.doesNotMatch(read, /web_search/);
  const urlSearch = buildAIToolTestPrompt({ tool: 'web_search', target: 'https://example.com/' });
  assert.match(urlSearch, /网页地址提炼关键词/);
});

test('combined AI prompts choose search then read for keywords and read then search for URLs', async () => {
  const { buildAIToolTestPrompt } = await import('./aiSearchConfigHelpers.mjs');
  assert.equal(typeof buildAIToolTestPrompt, 'function');
  assert.match(buildAIToolTestPrompt({ tool: 'both', target: 'OpenAI' }), /先调用一次 web_search.*再.*调用一次 web_read/);
  assert.match(buildAIToolTestPrompt({ tool: 'both', target: 'https://example.com/' }), /先调用一次 web_read.*再.*调用一次 web_search/);
  assert.throws(() => buildAIToolTestPrompt({ tool: 'both', target: '' }), /关键词|地址/);
});

test('failed tool tests retain diagnostics and the actual billing record', () => {
  const record = { id: 12, feeDetails: [{ provider: 'ai', cacheHitRate: 0.1 }] };
  const result = buildAIToolTestResult({ data: {
    success: false, message: '工具未返回有效结果', answers: [' 诊断答案 '],
    toolResult: { error: 'empty' }, usage: { trace: [{ type: 'model' }] },
    requestBody: '{"tool_choice":"required"}', rawResponse: 'raw', requestId: ' req-12 ',
    billingRecord: record, billingWarning: ' 账单提示 ',
  } });
  assert.equal(result.success, false);
  assert.equal(result.message, '工具未返回有效结果');
  assert.deepEqual(result.answers, ['诊断答案']);
  assert.equal(JSON.parse(result.toolResultText).error, 'empty');
  assert.deepEqual(JSON.parse(result.traceText), [{ type: 'model' }]);
  assert.equal(result.billingRecord, record);
  assert.equal(result.requestId, 'req-12');
  assert.equal(result.billingWarning, '账单提示');
});

test('tool test results preserve Markdown content and explicit empty trace', () => {
  const result = buildAIToolTestResult({ data: {
    success: true, toolResult: '# Page\n\nContent', trace: [], usage: { trace: [{ type: 'model' }] }, testPrompt: ' 实际发送的提示词 ',
  } });
  assert.equal(result.toolResultText, '# Page\n\nContent');
  assert.equal(result.traceText, '[]');
  assert.equal(result.billingRecord, null);
  assert.equal(result.testPrompt, '实际发送的提示词');
});
