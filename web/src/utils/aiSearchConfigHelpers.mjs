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

export function buildAIToolTestPayload(form, fallbackConfig) {
  const mode = trimText(form?.mode);
  const tool = trimText(form?.tool);
  if (!['direct', 'ai'].includes(mode)) throw new Error('请选择测试类型');
  if (!['web_search', 'web_read'].includes(tool)) throw new Error('请选择要测试的工具');
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
  if (tool === 'web_search' && !payload.query) throw new Error('请填写搜索词');
  if (tool === 'web_read') {
    let parsed;
    try { parsed = new URL(payload.url); } catch { throw new Error('请填写有效的 HTTP 或 HTTPS 网页地址'); }
    if (!['http:', 'https:'].includes(parsed.protocol)) throw new Error('网页地址仅支持 HTTP 或 HTTPS');
  }
  if (mode === 'ai' && !payload.model) throw new Error('请选择要测试的 AI 模型');
  return payload;
}

function formatTestDiagnostic(value) {
  if (typeof value === 'string') return value;
  if (value === null || value === undefined) return '';
  return JSON.stringify(value, null, 2);
}

export function buildAIToolTestResult(response) {
  const data = response?.data || {};
  return {
    ...buildAISearchTestResult(response),
    success: data.success === true,
    message: trimText(data.message),
    toolResultText: formatTestDiagnostic(data.toolResult),
    traceText: formatTestDiagnostic(data.trace ?? data.usage?.trace),
    usageText: formatTestDiagnostic(data.usage),
    requestId: trimText(data.requestId),
    billingRecord: data.billingRecord && typeof data.billingRecord === 'object' ? data.billingRecord : null,
    billingWarning: trimText(data.billingWarning),
  };
}
