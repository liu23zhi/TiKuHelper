function trimText(value) {
  return String(value || '').trim();
}

export function buildAISearchTestForm(config) {
  return {
    baseUrl: trimText(config?.baseUrl),
    apiKey: trimText(config?.apiKey),
    model: trimText(config?.model),
    prompt: trimText(config?.prompt),
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

export function buildAIToolTestPayload(form, fallbackConfig) {
  const mode = trimText(form?.mode);
  const tool = trimText(form?.tool);
  if (!['direct', 'ai'].includes(mode)) throw new Error('请选择测试类型');
  if (!(mode === 'ai' ? ['web_search', 'web_read', 'both'] : ['web_search', 'web_read']).includes(tool)) throw new Error('请选择要测试的工具');
  const connection = buildAISearchTestForm(form);
  const fallback = buildAISearchTestForm(fallbackConfig);
  const payload = {
    mode, tool,
    query: trimText(form?.query),
    url: trimText(form?.url),
    baseUrl: connection.baseUrl || fallback.baseUrl,
    apiKey: connection.apiKey || fallback.apiKey,
    model: connection.model || fallback.model,
    prompt: connection.prompt || fallback.prompt,
  };
  if (mode === 'ai') {
    const { target, isURL } = resolveAIToolTestTarget(form);
    payload.target = target;
    payload.query = isURL ? '' : target;
    payload.url = isURL ? target : '';
  } else if (tool === 'web_read') {
    validateToolTestURL(payload.url);
  } else {
    if (!payload.query) throw new Error('请填写搜索词');
    if ([...payload.query].length > 120) throw new Error('搜索词不能超过 120 个字符');
  }
  if (mode === 'ai' && !payload.model) throw new Error('请选择要测试的 AI 模型');
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
  };
}
