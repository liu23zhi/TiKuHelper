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
