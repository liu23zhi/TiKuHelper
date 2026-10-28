import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildAISearchTestForm,
  buildAISearchTestPayload,
  buildAISearchTestResult,
  hasAISearchTestResult,
  buildAIToolTestPayload,
  buildAIToolTestPrompt,
  buildAIToolTestResult,
  buildAICompressionToolTestPrompt,
  AI_COMPRESSION_TEST_DEFAULTS,
  AI_COMPRESSION_TEST_HISTORY,
  AI_COMPRESSION_TEST_TARGET,
  buildAIContextCompressionSettings,
  moveAIContextCompressionStrategy,
} from './aiSearchConfigHelpers.mjs';

test('context compression settings preserve disabled and remote values with defaults for missing fields', () => {
  assert.deepEqual(buildAIContextCompressionSettings({}), {
    contextCompressionEnabled: true,
    useRemoteContextCompression: false,
    contextCompressionOrder: ['server', 'remote', 'local'],
    contextCompressionTriggerPercent: 80,
    contextCompressionTargetPercent: 50,
    contextCompressionMaxAttempts: 3,
  });
  assert.deepEqual(buildAIContextCompressionSettings({
    contextCompressionEnabled: false, useRemoteContextCompression: true,
    contextCompressionTriggerPercent: '85', contextCompressionTargetPercent: '45', contextCompressionMaxAttempts: '4',
  }), {
    contextCompressionEnabled: false, useRemoteContextCompression: true,
    contextCompressionOrder: ['server', 'remote', 'local'],
    contextCompressionTriggerPercent: 85, contextCompressionTargetPercent: 45, contextCompressionMaxAttempts: 4,
  });
});

test('compression order preserves a complete custom order without sharing mutable defaults', () => {
  const order = ['local', 'server', 'remote'];
  const settings = buildAIContextCompressionSettings({ contextCompressionOrder: order });
  assert.deepEqual(settings.contextCompressionOrder, order);
  settings.contextCompressionOrder.reverse();
  assert.deepEqual(order, ['local', 'server', 'remote']);
  const defaults = buildAIContextCompressionSettings({});
  defaults.contextCompressionOrder.reverse();
  assert.deepEqual(buildAIContextCompressionSettings({}).contextCompressionOrder, ['server', 'remote', 'local']);
  for (const invalid of [[], ['server', 'local'], ['server', 'server', 'local'], ['server', 'remote', 'unknown'], 'server,remote,local']) {
    assert.throws(() => buildAIContextCompressionSettings({ contextCompressionOrder: invalid }), /顺序/);
  }
});

test('compression strategy moves retain every strategy and ignore invalid drag positions', () => {
  const order = ['server', 'remote', 'local'];
  assert.deepEqual(moveAIContextCompressionStrategy(order, 0, 2), ['remote', 'local', 'server']);
  assert.deepEqual(moveAIContextCompressionStrategy(order, 2, 0), ['local', 'server', 'remote']);
  for (const [from, to] of [[-1, 0], [0, 3], [NaN, 1], [0.5, 1]]) {
    assert.deepEqual(moveAIContextCompressionStrategy(order, from, to), order);
  }
  assert.deepEqual(order, ['server', 'remote', 'local']);
});

test('context compression settings reject invalid budgets instead of silently correcting them', () => {
  for (const override of [
    { contextCompressionTriggerPercent: 49 }, { contextCompressionTriggerPercent: 96 },
    { contextCompressionTargetPercent: 9 }, { contextCompressionTargetPercent: 80 },
    { contextCompressionTriggerPercent: 60, contextCompressionTargetPercent: 60 },
    { contextCompressionMaxAttempts: 0 }, { contextCompressionMaxAttempts: Number.MAX_SAFE_INTEGER + 1 }, { contextCompressionMaxAttempts: 1.5 },
    { contextCompressionTriggerPercent: '' }, { contextCompressionTargetPercent: NaN },
  ]) assert.throws(() => buildAIContextCompressionSettings(override), /压缩/);
});

test('context compression attempts retain positive safe integers above the former limit', () => {
  for (const attempts of [6, 100, Number.MAX_SAFE_INTEGER]) {
    assert.equal(buildAIContextCompressionSettings({ contextCompressionMaxAttempts: String(attempts) }).contextCompressionMaxAttempts, attempts);
  }
});

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
    history: '',
    compressionMode: 'configured',
  });

  assert.deepEqual(buildAISearchTestForm(null), {
    baseUrl: '',
    apiKey: '',
    model: '',
    prompt: '',
    history: '',
    compressionMode: 'configured',
  });
});

test('independent compression tests require history and a model and preserve the selected strategy', () => {
  const base = { mode: 'compression', model: 'gpt-test', history: ' Earlier search results and verified facts. ' };
  for (const compressionMode of ['configured', 'server', 'local', 'remote']) {
    const payload = buildAIToolTestPayload({ ...base, compressionMode }, { baseUrl: 'https://api.example.com/v1', apiKey: 'saved' });
    assert.equal(payload.mode, 'compression');
    assert.equal(payload.history, 'Earlier search results and verified facts.');
    assert.equal(payload.compressionMode, compressionMode);
    assert.equal(payload.baseUrl, 'https://api.example.com/v1');
    assert.equal(payload.tool, '');
    assert.equal(payload.target, undefined);
  }
  assert.throws(() => buildAIToolTestPayload({ mode: 'compression', model: 'gpt-test', history: ' ' }), /历史/);
  assert.throws(() => buildAIToolTestPayload({ mode: 'compression', history: 'evidence' }), /模型/);
});

test('AI compression tests combine a compression history with a selected real tool call', () => {
  const payload = buildAIToolTestPayload({
    mode: 'ai_compression', tool: 'both', target: 'OpenAI context window',
    history: 'Earlier search result summary and verified source. ', model: 'gpt-test', compressionMode: 'remote',
  }, { baseUrl: 'https://api.example.com/v1', apiKey: 'saved', prompt: 'System rules' });
  assert.deepEqual({ mode: payload.mode, tool: payload.tool, target: payload.target, query: payload.query, url: payload.url }, {
    mode: 'ai_compression', tool: 'both', target: 'OpenAI context window', query: 'OpenAI context window', url: '',
  });
  assert.equal(payload.history, 'Earlier search result summary and verified source.');
  assert.equal(payload.compressionMode, 'remote');
  assert.equal(payload.model, 'gpt-test');
  assert.equal(payload.baseUrl, 'https://api.example.com/v1');
  assert.equal(payload.apiKey, 'saved');
  assert.equal(payload.prompt, 'System rules');
  assert.equal(buildAIToolTestPayload({ mode: 'ai', tool: 'web_search', target: 'OpenAI', model: 'gpt-test', compressionMode: 'invalid' }).compressionMode, undefined);
  assert.throws(() => buildAIToolTestPayload({ mode: 'ai_compression', tool: 'both', target: 'OpenAI', model: 'gpt-test', history: '' }), /历史/);
  assert.throws(() => buildAIToolTestPayload({ mode: 'ai_compression', tool: 'other', target: 'OpenAI', model: 'gpt-test', history: 'evidence' }), /工具/);
  assert.throws(() => buildAIToolTestPayload({ mode: 'compression', model: 'gpt-test', history: 'evidence', compressionMode: 'invalid' }), /策略/);
});

test('AI compression history-only verification never includes stale network targets', () => {
  const payload = buildAIToolTestPayload({
    mode: 'ai_compression', tool: 'none', history: 'Historical evidence.', model: 'gpt-test',
    target: 'old target', query: 'old query', url: 'https://example.com/old',
  });
  assert.equal(payload.tool, 'none');
  assert.equal(payload.history, 'Historical evidence.');
  assert.equal(payload.target, undefined);
  assert.equal(payload.query, '');
  assert.equal(payload.url, '');
  assert.equal(buildAIToolTestPayload({ mode: 'ai_compression', history: 'Historical evidence.', model: 'gpt-test' }).tool, 'none');
  assert.doesNotMatch(buildAICompressionToolTestPrompt({ tool: 'none', target: 'old target' }), /old target/);
  assert.throws(() => buildAIToolTestPayload({ mode: 'ai', tool: 'none', target: 'anything', model: 'gpt-test' }), /工具/);
  assert.throws(() => buildAIToolTestPayload({ mode: 'direct', tool: 'none' }), /工具/);
  for (const tool of ['web_search', 'web_read', 'both']) {
    assert.throws(() => buildAIToolTestPayload({ mode: 'ai_compression', tool, target: '', history: 'evidence', model: 'gpt-test' }), /关键词|地址/);
  }
});

test('compression results normalize strategy, token counts, fallback and summary without losing diagnostics', () => {
  const result = buildAIToolTestResult({ data: {
    success: true, message: '完成压缩', requestId: 'cmp-1', answers: ['答案'],
    compression: {
      success: true, strategy: 'local', beforeTokens: 2500, afterTokens: 410,
      summary: '保留答案线索、来源网址和工具返回状态。', remoteFallback: true,
    },
    trace: [{ stage: '本地历史摘要' }], usage: { totalTokens: 120 },
    billingRecord: { id: 9 }, billingWarning: '账单读取提示',
  } });
  assert.deepEqual(result.compression, {
    success: true, strategy: 'local', beforeTokens: 2500, afterTokens: 410,
    summary: '保留答案线索、来源网址和工具返回状态。', remoteFallback: true, error: '', attemptedStrategies: [], fallbackReason: '',
  });
  assert.equal(result.success, true);
  assert.equal(result.requestId, 'cmp-1');
  assert.equal(result.traceText, '[\n  {\n    "stage": "本地历史摘要"\n  }\n]');
  assert.equal(result.billingRecord.id, 9);
  assert.equal(result.billingWarning, '账单读取提示');
  assert.equal(buildAIToolTestResult({ data: { compression: { strategy: 'unknown', beforeTokens: -1, afterTokens: '2' } } }).compression.strategy, '');
  assert.equal(buildAIToolTestResult({ data: {} }).compression, null);
});

test('server compression diagnostics preserve ordered attempts and failure reason', () => {
  const compression = buildAIToolTestResult({ data: { compression: {
    success: true, strategy: 'server', attemptedStrategies: ['local', 'server'], fallbackReason: '本地摘要失败',
    beforeTokens: 4000, afterTokens: 500,
  } } }).compression;
  assert.equal(compression.strategy, 'server');
  assert.deepEqual(compression.attemptedStrategies, ['local', 'server']);
  assert.equal(compression.fallbackReason, '本地摘要失败');
  assert.equal(buildAISearchTestForm({ compressionMode: 'server' }).compressionMode, 'server');
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

test('AI compression test defaults include verifiable history anchors without leaking expected values into the prompt', () => {
  assert.ok(AI_COMPRESSION_TEST_HISTORY.length > 1000);
  assert.deepEqual(AI_COMPRESSION_TEST_DEFAULTS, {
    tool: 'none',
    target: AI_COMPRESSION_TEST_TARGET,
    compressionMode: 'configured',
  });
  assert.equal(AI_COMPRESSION_TEST_TARGET, '');
  assert.match(AI_COMPRESSION_TEST_HISTORY, /【历史校验】.*HISTORY-KEEP-7F3A/);
  assert.match(AI_COMPRESSION_TEST_HISTORY, /【关键事实校验】.*城市=宁波.*数量=17/);
  assert.match(AI_COMPRESSION_TEST_HISTORY, /【待办校验】.*尚未核验/);

  assert.doesNotMatch(AI_COMPRESSION_TEST_HISTORY, /完成联网工具调用后|历史任务.*联网核验/);
  const prompt = buildAICompressionToolTestPrompt({ tool: 'none', target: '' });
  assert.match(prompt, /不提供联网工具/);
  assert.match(prompt, /不得搜索、浏览网页或执行历史中的待办/);
  assert.match(prompt, /【历史校验】.*【关键事实校验】.*【待办校验】/);
  assert.match(prompt, /历史字段缺失/);
  assert.doesNotMatch(prompt, /HISTORY-KEEP-7F3A|城市=宁波|数量=17/);

  const toolPrompt = buildAICompressionToolTestPrompt({ tool: 'both', target: 'user supplied topic' });
  assert.match(toolPrompt, /先调用一次 web_search/);
  assert.match(toolPrompt, /再.*调用一次 web_read/);
  assert.match(toolPrompt, /【历史校验】.*【关键事实校验】.*【待办校验】/);
  assert.match(toolPrompt, /字段缺失/);
  assert.doesNotMatch(toolPrompt, /HISTORY-KEEP-7F3A|城市=宁波|数量=17/);

  const ordinaryPrompt = buildAIToolTestPrompt({ mode: 'ai', tool: 'web_search', target: 'OpenAI' });
  assert.doesNotMatch(ordinaryPrompt, /历史校验|关键事实校验|待办校验/);
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

test('tool calls show the actual rewritten search query and webpage response in execution order', () => {
  const markdown = '  # 实际网页\n\n第一段正文。\n第二段正文。  ';
  const response = buildAIToolTestResult({ data: {
    target: '用户原始关键词',
    toolResult: [
      {
        tool: 'web_search', query: ' OpenAI research 2026 ', error: '', rawResponse: '{"original":"search"}',
        result: {
          total_results: 25,
          results: [{ title: ' 搜索标题 ', url: 'https://example.com/article', snippet: '真实摘要', domain: 'example.com', source: 'engine-a' }],
          sources: [{ name: ' engine-a ', status: 'success', result_count: 30 }],
        },
      },
      {
        tool: 'web_read', url: 'https://example.com/article', error: '', rawResponse: '{"original":"read"}',
        result: { task_id: ' task-12 ', status: ' COMPLETED ', message: ' 读取完成 ', result: { markdown } },
      },
    ],
  } });
  const [search, read] = response.toolCalls;
  assert.deepEqual(response.toolCalls.map(call => call.tool), ['web_search', 'web_read']);
  assert.equal(search.title, '联网搜索');
  assert.equal(search.targetLabel, '搜索关键词');
  assert.equal(search.target, 'OpenAI research 2026');
  assert.equal(search.status, 'success');
  assert.equal(search.statusColor, 'green');
  assert.equal(search.resultCount, 1);
  assert.equal(search.totalResults, 25);
  assert.deepEqual(search.results[0], { title: '搜索标题', url: 'https://example.com/article', href: 'https://example.com/article', snippet: '真实摘要', domain: 'example.com', source: 'engine-a' });
  assert.deepEqual(search.sources, [{ name: 'engine-a', status: 'success', resultCount: 30 }]);
  assert.equal(read.title, '网页读取');
  assert.equal(read.targetLabel, '网页地址');
  assert.equal(read.target, 'https://example.com/article');
  assert.equal(read.taskId, 'task-12');
  assert.equal(read.taskStatus, 'completed');
  assert.equal(read.taskMessage, '读取完成');
  assert.equal(read.status, 'success');
  assert.equal(read.markdown, markdown);
  assert.equal(read.rawResponse, '{"original":"read"}');
  assert.notEqual(search.key, read.key);
});

test('tool call order follows recorded invocations instead of grouped usage or requested tool order', () => {
  const response = buildAIToolTestResult({ data: {
    usage: { searchCalls: [{ query: 'later' }], webReadCalls: [{ url: 'https://example.com/first' }] },
    toolResult: [
      { tool: 'web_read', url: 'https://example.com/first', result: { status: 'completed', result: { markdown: '# First' } } },
      { tool: 'web_search', query: 'later', result: { results: [{ title: 'Later' }] } },
      { tool: 'web_search', query: 'later', result: { results: [{ title: 'Another actual call' }] } },
    ],
  } });
  assert.deepEqual(response.toolCalls.map(call => call.target), ['https://example.com/first', 'later', 'later']);
  assert.equal(new Set(response.toolCalls.map(call => call.key)).size, 3);
});

test('failed invocations retain partial results without showing a successful status', () => {
  const response = buildAIToolTestResult({ data: { toolResult: [
    { tool: 'web_search', query: 'partial', error: ' 搜索上游失败 ', result: { results: [{ title: 'Partial search result' }] } },
    { tool: 'web_read', url: 'https://example.com/partial', error: ' 读取中断 ', result: { task_id: 'partial-task', status: 'completed', result: { markdown: '# Partial body' }, error: '次要错误' } },
  ] } });
  assert.deepEqual(response.toolCalls.map(call => call.status), ['error', 'error']);
  assert.deepEqual(response.toolCalls.map(call => call.error), ['搜索上游失败', '读取中断']);
  assert.equal(response.toolCalls[0].results[0].title, 'Partial search result');
  assert.equal(response.toolCalls[1].markdown, '# Partial body');
  assert.equal(response.toolCalls[1].taskId, 'partial-task');
});

test('webpage success requires a completed state and nonempty body while terminal errors remain failures', () => {
  for (const scenario of [
    { taskStatus: 'completed', markdown: '# Body', status: 'success' },
    { taskStatus: 'completed', markdown: '  ', status: 'empty' },
    { taskStatus: 'completed', status: 'empty' },
    { taskStatus: 'queued', status: 'pending' },
    { taskStatus: 'pending', status: 'pending' },
    { taskStatus: 'processing', status: 'pending' },
    { taskStatus: 'failed', markdown: '# Ignore as success', status: 'error' },
    { taskStatus: 'timeout', status: 'error' },
    { taskStatus: 'cancelled', status: 'error' },
    { taskStatus: 'unexpected', markdown: '# Cannot imply success', status: 'unknown' },
    { taskStatus: '', markdown: '# Cannot imply success', status: 'unknown' },
  ]) {
    const response = buildAIToolTestResult({ data: { toolResult: [{
      tool: 'web_read', url: 'https://example.com/',
      result: { status: scenario.taskStatus, result: { markdown: scenario.markdown } },
    }] } });
    assert.equal(response.toolCalls[0].status, scenario.status, JSON.stringify(scenario));
  }
  const response = buildAIToolTestResult({ data: { toolResult: [{
    tool: 'web_read', result: { status: 'failed', error: ' 原始任务错误 ', message: '失败说明' },
  }] } });
  assert.equal(response.toolCalls[0].error, '原始任务错误');
  assert.equal(response.toolCalls[0].taskMessage, '失败说明');
});

test('search result links only permit complete HTTP URLs without credentials', () => {
  const urls = [
    'https://example.com/a?x=1&y=2#section', 'http://example.com/',
    'javascript:alert(1)', 'data:text/html,test', '//example.com/a',
    'https://user:pass@example.com/a', 'https://@example.com/a', '/relative', 'https://example.com/\nunsafe',
  ];
  const response = buildAIToolTestResult({ data: { toolResult: [{
    tool: 'web_search', query: 'urls', result: { results: urls.map(url => ({ title: 'Result', url })) },
  }] } });
  assert.deepEqual(response.toolCalls[0].results.map(item => item.url), urls);
  assert.deepEqual(response.toolCalls[0].results.map(item => item.href), [urls[0], urls[1], '', '', '', '', '', '', '']);
});

test('large multiline webpage content is preserved without truncation or guessed summaries', () => {
  const markdown = `# ${'标题'.repeat(1000)}\n\n${'完整正文\n'.repeat(1000)}`;
  const response = buildAIToolTestResult({ data: { toolResult: [{
    tool: 'web_read', url: 'https://example.com/long', result: { status: 'completed', result: { markdown } },
  }] } });
  assert.equal(response.toolCalls[0].markdown, markdown);
});

test('missing or legacy tool results never invent calls from an AI answer or usage trace', () => {
  for (const toolResult of [undefined, null, [], { error: 'old response' }, '# Legacy Markdown']) {
    const response = buildAIToolTestResult({ data: {
      toolResult, answers: ['我已经搜索并浏览网页'],
      usage: { searchCalls: [{ query: 'claimed query' }], webReadCalls: [{ url: 'https://example.com' }] },
    } });
    assert.deepEqual(response.toolCalls, []);
    assert.equal(response.toolResultText, toolResult == null ? '' : typeof toolResult === 'string' ? toolResult : JSON.stringify(toolResult, null, 2));
  }
});

test('malformed diagnostic entries are ignored safely while recognized empty calls stay visible', () => {
  const response = buildAIToolTestResult({ data: { toolResult: [
    null, 'invalid', 7, [], { tool: 'other_tool' },
    { tool: 'web_search', query: {}, result: { total_results: '25', results: [null, 5, [], {}, { title: ' ' }, { title: 'Real' }], sources: [null, {}, { name: 'Source', result_count: -1 }] } },
    { tool: 'web_read', url: {}, result: null },
  ] } });
  assert.equal(response.toolCalls.length, 2);
  assert.equal(response.toolCalls[0].target, '');
  assert.equal(response.toolCalls[0].resultCount, 1);
  assert.equal(response.toolCalls[0].totalResults, null);
  assert.deepEqual(response.toolCalls[0].sources, [{ name: 'Source', status: '', resultCount: null }]);
  assert.equal(response.toolCalls[1].status, 'unknown');
  assert.equal(response.toolCalls[1].target, '');
  const emptySearch = buildAIToolTestResult({ data: { toolResult: [{ tool: 'web_search', result: { total_results: 0, results: [] } }] } }).toolCalls[0];
  assert.equal(emptySearch.status, 'empty');
  assert.equal(emptySearch.totalResults, 0);
});
