function trimText(value) {
  return String(value || '').trim();
}

export const AI_COMPRESSION_TEST_TARGET = 'OpenAI Responses API context compaction';
export const AI_COMPRESSION_TEST_DEFAULTS = Object.freeze({
  tool: 'both',
  target: AI_COMPRESSION_TEST_TARGET,
  compressionMode: 'configured',
});

export const AI_CONTEXT_COMPRESSION_STRATEGIES = Object.freeze([
  Object.freeze({ value: 'server', label: '服务器自动压缩', description: '在正常 AI 请求中由上游自动压缩历史' }),
  Object.freeze({ value: 'remote', label: '独立压缩节点', description: '调用当前供应商的独立压缩接口' }),
  Object.freeze({ value: 'local', label: '本地压缩', description: '由当前模型生成历史摘要' }),
]);

export function aiContextCompressionStrategyText(strategy) {
  return AI_CONTEXT_COMPRESSION_STRATEGIES.find(item => item.value === strategy)?.label || (strategy ? '未知策略' : '未完成');
}

export function moveAIContextCompressionStrategy(order, from, to) {
  const result = [...order];
  if (![from, to].every(index => Number.isInteger(index) && index >= 0 && index < result.length)) return result;
  result.splice(to, 0, result.splice(from, 1)[0]);
  return result;
}

function compressionOrder(value) {
  const expected = AI_CONTEXT_COMPRESSION_STRATEGIES.map(item => item.value);
  if (value === undefined || value === null) return expected;
  if (!Array.isArray(value) || value.length !== expected.length || new Set(value).size !== expected.length || value.some(item => !expected.includes(item))) {
    throw new Error('压缩顺序必须完整包含服务器自动压缩、独立压缩节点和本地压缩，且不能重复');
  }
  return [...value];
}

export const AI_COMPRESSION_TEST_HISTORY = [
  '【合成历史测试数据】仅用于检查模型摘要是否保留历史，不代表真实搜索、浏览结果或事实。',
  '【历史校验】必须保留的唯一标记：HISTORY-KEEP-7F3A。',
  '【关键事实校验】城市=宁波；数量=17；原始来源=https://example.com/history-check。',
  '【待办校验】尚未核验发布日期；状态=待使用网页浏览确认，不得描述为已完成。',
  ...Array.from({ length: 32 }, (_, index) => `历史噪声记录 ${index + 1}：此前仅整理了候选资料，尚未完成在线核验。重复背景：测试内容为合成数据，不能作为现实依据；压缩时可以合并重复背景，但必须保留唯一标记、关键事实、来源地址、未完成事项和它们各自的状态。`),
  '【历史任务】完成联网工具调用后，分别核对历史校验标记、关键事实和待办状态，再总结真实工具返回内容。',
].join('\n');

export function buildAIContextCompressionSettings(config) {
  const integer = (value, fallback) => {
    if (value === undefined || value === null) return fallback;
    if (!['string', 'number'].includes(typeof value) || String(value).trim() === '') return NaN;
    return Number(value);
  };
  const settings = {
    contextCompressionEnabled: config?.contextCompressionEnabled ?? true,
    useRemoteContextCompression: config?.useRemoteContextCompression ?? false,
    contextCompressionOrder: compressionOrder(config?.contextCompressionOrder),
    contextCompressionTriggerPercent: integer(config?.contextCompressionTriggerPercent, 80),
    contextCompressionTargetPercent: integer(config?.contextCompressionTargetPercent, 50),
    contextCompressionMaxAttempts: integer(config?.contextCompressionMaxAttempts, 3),
  };
  const trigger = settings.contextCompressionTriggerPercent;
  const target = settings.contextCompressionTargetPercent;
  const attempts = settings.contextCompressionMaxAttempts;
  if (!Number.isSafeInteger(trigger) || trigger < 50 || trigger > 95) {
    throw new Error('上下文压缩触发比例必须是 50～95 的整数');
  }
  if (!Number.isSafeInteger(target) || target < 10 || target >= trigger) {
    throw new Error('上下文压缩目标比例必须至少为 10，且小于触发比例');
  }
  if (!Number.isSafeInteger(attempts) || attempts < 1) {
    throw new Error('上下文压缩最大尝试次数必须是正整数（含首次），且不能超过安全整数范围');
  }
  return settings;
}

export function buildAISearchTestForm(config) {
  return {
    baseUrl: trimText(config?.baseUrl),
    apiKey: trimText(config?.apiKey),
    model: trimText(config?.model),
    prompt: trimText(config?.prompt),
    history: trimText(config?.history),
    compressionMode: ['configured', 'server', 'local', 'remote'].includes(trimText(config?.compressionMode))
      ? trimText(config?.compressionMode) : 'configured',
  };
}

export function buildAISearchTestPayload(form, fallbackConfig) {
  const payload = {
    baseUrl: trimText(form?.baseUrl) || trimText(fallbackConfig?.baseUrl),
    apiKey: trimText(form?.apiKey) || trimText(fallbackConfig?.apiKey),
    model: trimText(form?.model) || trimText(fallbackConfig?.model),
    prompt: trimText(form?.prompt) || trimText(fallbackConfig?.prompt),
  };

  if (!payload.model) {
    throw new Error('请选择要测试的 AI 模型');
  }

  return payload;
}

export function buildAISearchTestResult(response) {
  const data = response?.data || {};
  const endpoint = trimText(data?.endpoint);
  const requestBody = trimText(data?.requestBody);
  return {
    answers: Array.isArray(data?.answers) ? data.answers.map(item => trimText(item)).filter(Boolean) : [],
    requestBody: endpoint && requestBody ? `POST ${endpoint}\n\n${requestBody}` : requestBody,
    rawResponse: trimText(data?.rawResponse),
  };
}

export function hasAISearchTestResult(result) {
  return Boolean(
    (Array.isArray(result?.answers) && result.answers.length > 0)
      || trimText(result?.requestBody)
      || trimText(result?.rawResponse)
  );
}

function validateToolTestURL(value) {
  let parsed;
  try { parsed = new URL(value); } catch { throw new Error('请填写有效的 HTTP 或 HTTPS 网页地址'); }
  if (!/^https?:\/\//i.test(value) || !['http:', 'https:'].includes(parsed.protocol) || !parsed.hostname) {
    throw new Error('网页地址仅支持完整 HTTP 或 HTTPS 地址');
  }
  const authority = value.match(/^https?:\/\/([^/?#]*)/i)?.[1] || '';
  if (authority.includes('@') || parsed.username || parsed.password || parsed.hash) throw new Error('网页地址不能包含凭据或片段');
  if ([...value].length > 2048) throw new Error('网页地址不能超过 2048 个字符');
}

function resolveAIToolTestTarget(form) {
  const tool = trimText(form?.tool);
  if (!['web_search', 'web_read', 'both'].includes(tool)) throw new Error('请选择要测试的工具');
  let target = trimText(form?.target);
  if (!Object.prototype.hasOwnProperty.call(form || {}, 'target')) {
    target = tool === 'web_read' ? trimText(form?.url)
      : tool === 'both' ? trimText(form?.url) || trimText(form?.query) : trimText(form?.query);
  }
  if (!target) throw new Error('请填写关键词或网页地址');
  const isURL = target.includes('://');
  if (isURL || tool === 'web_read') validateToolTestURL(target);
  else if ([...target].length > 120) throw new Error('关键词不能超过 120 个字符');
  return { target, isURL };
}

export function buildAIToolTestPrompt(form) {
  const { target, isURL } = resolveAIToolTestTarget(form);
  const tool = trimText(form?.tool);
  let instruction;
  if (tool === 'web_read') {
    instruction = '请调用一次 web_read 读取该网页，等待正文返回后简述内容。';
  } else if (tool === 'both') {
    instruction = isURL
      ? '请先调用一次 web_read 读取该网页，再根据网页主题调用一次 web_search 核对相关信息，最后综合说明。'
      : '请先调用一次 web_search 搜索该关键词，再从搜索结果中选择一个相关网页调用一次 web_read，等待正文返回后综合说明。';
  } else {
    instruction = isURL
      ? '请根据该网页地址提炼关键词，调用一次 web_search，根据实际搜索结果简述相关信息。'
      : '请调用一次 web_search，根据实际搜索结果简述相关信息。';
  }
  return `这是 AI 工具调用测试。用户输入：${JSON.stringify(target)}。${instruction}必须实际调用所选工具；失败或无结果时如实说明，不得编造。`;
}

export function buildAICompressionToolTestPrompt(form) {
  return `${buildAIToolTestPrompt(form)}工具调用结束后，请根据压缩后的历史逐项回报【历史校验】标记、【关键事实校验】字段值及【待办校验】内容，并区分尚未完成与已完成。只从压缩后的历史提取这些内容；缺少或无法确认的字段须明确写“历史字段缺失”，不得从本提示、网页或工具结果推断。最后另行简述真实工具返回结果。`;
}

export function buildAIToolTestPayload(form, fallbackConfig) {
  const mode = trimText(form?.mode);
  const tool = trimText(form?.tool);
  if (!['direct', 'ai', 'compression', 'ai_compression'].includes(mode)) throw new Error('请选择测试类型');
  if (['ai', 'ai_compression'].includes(mode) && !['web_search', 'web_read', 'both'].includes(tool)) throw new Error('请选择要测试的工具');
  if (mode === 'direct' && !['web_search', 'web_read'].includes(tool)) throw new Error('请选择要测试的工具');
  const connection = buildAISearchTestForm(form);
  const fallback = buildAISearchTestForm(fallbackConfig);
  const compressionMode = trimText(form?.compressionMode) || 'configured';
  const payload = {
    mode, tool,
    query: trimText(form?.query),
    url: trimText(form?.url),
    baseUrl: connection.baseUrl || fallback.baseUrl,
    apiKey: connection.apiKey || fallback.apiKey,
    model: connection.model || fallback.model,
    prompt: connection.prompt || fallback.prompt,
  };
  if (mode === 'compression' || mode === 'ai_compression') {
    if (!['configured', 'server', 'local', 'remote'].includes(compressionMode)) throw new Error('请选择有效的压缩策略');
    payload.history = trimText(form?.history);
    payload.compressionMode = compressionMode;
  }
  if (mode === 'compression') {
    if (!payload.history) throw new Error('请填写待压缩历史');
  } else if (mode === 'ai' || mode === 'ai_compression') {
    const { target, isURL } = resolveAIToolTestTarget(form);
    payload.target = target;
    payload.query = isURL ? '' : target;
    payload.url = isURL ? target : '';
    if (mode === 'ai_compression' && !payload.history) throw new Error('请填写待压缩历史');
  } else if (tool === 'web_read') {
    validateToolTestURL(payload.url);
  } else {
    if (!payload.query) throw new Error('请填写搜索词');
    if ([...payload.query].length > 120) throw new Error('搜索词不能超过 120 个字符');
  }
  if (['ai', 'compression', 'ai_compression'].includes(mode) && !payload.model) throw new Error('请选择要测试的 AI 模型');
  return payload;
}

function formatTestDiagnostic(value) {
  if (typeof value === 'string') return value;
  if (value === null || value === undefined) return '';
  return JSON.stringify(value, null, 2);
}

function isDiagnosticObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function diagnosticText(value) {
  return typeof value === 'string' ? value.trim() : '';
}

function diagnosticCount(value) {
  return Number.isInteger(value) && value >= 0 ? value : null;
}

function diagnosticHref(value) {
  if (!/^https?:\/\//i.test(value) || [...value].some(char => char.charCodeAt(0) < 32 || char.charCodeAt(0) === 127)) return '';
  try {
    const parsed = new URL(value);
    const authority = value.match(/^https?:\/\/([^/?#]*)/i)?.[1] || '';
    if (!parsed.hostname || parsed.username || parsed.password || authority.includes('@')) return '';
    return value;
  } catch { return ''; }
}

function buildAIToolTestCalls(value) {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item, index) => {
    if (!isDiagnosticObject(item) || !['web_search', 'web_read'].includes(item.tool)) return [];
    const isSearch = item.tool === 'web_search';
    const payload = isDiagnosticObject(item.result) ? item.result : {};
    const error = diagnosticText(item.error) || (isSearch ? '' : diagnosticText(payload.error));
    const results = isSearch && Array.isArray(payload.results) ? payload.results.filter(isDiagnosticObject).map(result => {
      const url = diagnosticText(result.url);
      return {
        title: diagnosticText(result.title), url, href: diagnosticHref(url),
        snippet: diagnosticText(result.snippet), domain: diagnosticText(result.domain), source: diagnosticText(result.source),
      };
    }).filter(result => result.title || result.url || result.snippet || result.domain || result.source) : [];
    const sources = isSearch && Array.isArray(payload.sources) ? payload.sources.filter(isDiagnosticObject).map(source => ({
      name: diagnosticText(source.name), status: diagnosticText(source.status), resultCount: diagnosticCount(source.result_count),
    })).filter(source => source.name || source.status || source.resultCount !== null) : [];
    const target = diagnosticText(isSearch ? item.query : item.url);
    const markdown = !isSearch && typeof payload.result?.markdown === 'string' ? payload.result.markdown : '';
    const taskStatus = isSearch ? '' : diagnosticText(payload.status).toLowerCase();
    let status = 'unknown';
    if (error) status = 'error';
    else if (isSearch) status = results.length > 0 ? 'success' : 'empty';
    else if (taskStatus === 'completed') status = markdown.trim() ? 'success' : 'empty';
    else if (['queued', 'pending', 'processing'].includes(taskStatus)) status = 'pending';
    else if (['failed', 'timeout', 'cancelled'].includes(taskStatus)) status = 'error';
    const statusText = {
      success: '调用成功', error: '调用失败', empty: isSearch ? '无搜索结果' : '未返回网页正文',
      pending: '等待完成', unknown: '状态未知',
    }[status];
    return [{
      key: `${item.tool}-${index}`, tool: item.tool,
      title: isSearch ? '联网搜索' : '网页读取', targetLabel: isSearch ? '搜索关键词' : '网页地址',
      target, targetHref: isSearch ? '' : diagnosticHref(target), status, statusText,
      statusColor: { success: 'green', error: 'red', empty: 'orange', pending: 'orange', unknown: 'default' }[status],
      error, results, resultCount: results.length, totalResults: isSearch ? diagnosticCount(payload.total_results) : null, sources,
      markdown, taskId: isSearch ? '' : diagnosticText(payload.task_id), taskStatus,
      taskMessage: isSearch ? '' : diagnosticText(payload.message),
      rawResponse: typeof item.rawResponse === 'string' ? item.rawResponse : '',
    }];
  });
}

export function buildAIToolTestResult(response) {
  const data = response?.data || {};
  const compression = isDiagnosticObject(data.compression) ? {
    success: data.compression.success === true,
    strategy: ['server', 'remote', 'local'].includes(diagnosticText(data.compression.strategy)) ? diagnosticText(data.compression.strategy) : '',
    beforeTokens: diagnosticCount(data.compression.beforeTokens),
    afterTokens: diagnosticCount(data.compression.afterTokens),
    summary: diagnosticText(data.compression.summary),
    remoteFallback: data.compression.remoteFallback === true,
    attemptedStrategies: Array.isArray(data.compression.attemptedStrategies)
      ? data.compression.attemptedStrategies.filter(item => ['server', 'remote', 'local'].includes(item)) : [],
    fallbackReason: diagnosticText(data.compression.fallbackReason),
    error: diagnosticText(data.compression.error),
  } : null;
  return {
    ...buildAISearchTestResult(response),
    success: data.success === true,
    message: trimText(data.message),
    testPrompt: trimText(data.testPrompt),
    toolCalls: buildAIToolTestCalls(data.toolResult),
    toolResultText: formatTestDiagnostic(data.toolResult),
    traceText: formatTestDiagnostic(data.trace ?? data.usage?.trace),
    usageText: formatTestDiagnostic(data.usage),
    requestId: trimText(data.requestId),
    billingRecord: data.billingRecord && typeof data.billingRecord === 'object' ? data.billingRecord : null,
    billingWarning: trimText(data.billingWarning),
    compression,
  };
}
