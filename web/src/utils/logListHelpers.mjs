export function buildLogModuleLabel(value) {
  const normalized = String(value || '').trim().toLowerCase();
  if (normalized === 'auth') return '认证';
  if (normalized === 'question') return '题目';
  if (normalized === 'system') return '系统';
  if (normalized === 'search') return '搜题';
  if (normalized === 'ai_fallback') return 'AI 兜底';
  if (normalized === 'captcha') return '验证码';
  return value || '-';
}

export function buildLogActionLabel(value, module) {
  const normalized = Number(value);
  const normalizedModule = String(module || '').trim().toLowerCase();
  if (normalized === 0) return '新增';
  if (normalized === 1) return '更新';
  if (normalized === 2) return '删除';
  if (normalized === 3) return normalizedModule === 'search' ? '搜题' : '查询';
  if (normalized === 10) return '登录';
  if (normalizedModule === 'ai_fallback' && normalized === 11) return 'AI 兜底';
  if (normalizedModule === 'captcha' && normalized === 20) return '远程生成';
  if (normalizedModule === 'captcha' && normalized === 21) return '远程校验';
  if (normalizedModule === 'captcha' && normalized === 22) return '远程二验';
  return value ?? '-';
}

export function buildLogActionColor(value) {
  const normalized = Number(value);
  if (normalized === 0) return 'green';
  if (normalized === 1) return 'blue';
  if (normalized === 2) return 'red';
  if (normalized === 3) return 'purple';
  if (normalized === 10) return 'gold';
  if (normalized === 11) return 'orange';
  if (normalized === 20) return 'cyan';
  if (normalized === 21) return 'geekblue';
  if (normalized === 22) return 'volcano';
  return 'default';
}

export function buildLogStatusLabel(record = {}) {
  const status = resolveLogStatus(record);
  if (status === 'success' || status === 'succeeded') return '成功';
  if (status === 'failed' || status === 'error') return '失败';
  if (status === 'running' || status === 'processing') return '处理中';
  if (status === 'pending' || status === 'queued' || status === 'created') return '等待中';
  if (status === 'timeout') return '超时';
  return buildLogActionLabel(record?.action, record?.module);
}

export function buildLogStatusColor(record = {}) {
  const status = resolveLogStatus(record);
  if (status === 'success' || status === 'succeeded') return 'green';
  if (status === 'failed' || status === 'error' || status === 'timeout') return 'red';
  if (status === 'running' || status === 'processing') return 'blue';
  if (status === 'pending' || status === 'queued' || status === 'created') return 'gold';
  return buildLogActionColor(record?.action);
}

export function buildLogStructuredFields(record = {}) {
  const description = String(record?.description || '').trim();
  const request = String(record?.oldAnswer || '').trim();
  const response = String(record?.newAnswer || '').trim();
  const parsedDescription = parseSearchLogDescriptionMap(description);
  const parsedCaptchaDescription = parseCaptchaLogDescriptionMap(description);
  const parsedAIFallbackDescription = parseAIFallbackLogDescriptionMap(description);

  if (parsedAIFallbackDescription.summary) {
    return {
      summary: parsedAIFallbackDescription.summary,
      requestId: parsedAIFallbackDescription.requestId || parsedAIFallbackDescription.taskId || '-',
      mode: parsedAIFallbackDescription.mode || '-',
      requestSource: parsedAIFallbackDescription.stage || '-',
      answerCount: parsedAIFallbackDescription.answers || '-',
      hitProvider: parsedAIFallbackDescription.code || parsedAIFallbackDescription.hitProvider || '-',
      billingProvider: buildBillingProviderLabel(parsedAIFallbackDescription.billingProvider),
      requestSnapshot: request || '-',
      responseSnapshot: response || '-',
    };
  }

  if (parsedCaptchaDescription.summary) {
    return {
      summary: parsedCaptchaDescription.summary,
      requestId: parsedCaptchaDescription.captchaId || '-',
      mode: parsedCaptchaDescription.step || '-',
      requestSource: parsedCaptchaDescription.scene || parsedCaptchaDescription.type || '-',
      answerCount: parsedCaptchaDescription.status || '-',
      hitProvider: parsedCaptchaDescription.remoteValid || parsedCaptchaDescription.result || '-',
      billingProvider: parsedCaptchaDescription.durationMs ? `${parsedCaptchaDescription.durationMs}ms` : '-',
      requestSnapshot: request || '-',
      responseSnapshot: response || '-',
    };
  }

  return {
    summary: parsedDescription.summary || description || '-',
    requestId: parsedDescription.requestId || '-',
    mode: parsedDescription.mode || '-',
    requestSource: parsedDescription.use || '-',
    answerCount: parsedDescription.answers || '-',
    hitProvider: parsedDescription.hitProvider || '-',
    billingProvider: buildBillingProviderLabel(parsedDescription.billingProvider),
    requestSnapshot: request || '-',
    responseSnapshot: response || '-',
  };
}

function resolveLogStatus(record = {}) {
  const description = String(record?.description || '').trim();
  const aiFallbackFields = parseAIFallbackLogDescriptionMap(description);
  if (aiFallbackFields.status) {
    return String(aiFallbackFields.status).trim().toLowerCase();
  }

  const searchSummary = description.split(/\s+/)[0] || '';
  if (searchSummary === '搜索完成') return 'success';
  if (searchSummary === '搜索失败') return 'failed';

  const captchaFields = parseCaptchaLogDescriptionMap(description);
  if (captchaFields.result) {
    return String(captchaFields.result).trim().toLowerCase();
  }
  return '';
}

export function buildLogRowDetail(record = {}) {
  const description = String(record?.description || '').trim();
  const request = String(record?.oldAnswer || '').trim();
  const response = String(record?.newAnswer || '').trim();
  const captchaFields = parseCaptchaLogDescriptionMap(description);

  if (captchaFields.summary) {
    const parts = [];
    if (captchaFields.status) {
      parts.push(`HTTP ${captchaFields.status}`);
    }
    if (captchaFields.remoteCode) {
      parts.push(`远程码：${captchaFields.remoteCode}`);
    }
    if (captchaFields.remoteValid) {
      parts.push(`有效：${formatBooleanText(captchaFields.remoteValid)}`);
    }
    if (captchaFields.scene) {
      parts.push(`场景：${captchaFields.scene}`);
    }
    if (captchaFields.type) {
      parts.push(`类型：${captchaFields.type}`);
    }
    if (captchaFields.captchaId) {
      parts.push(`ID：${captchaFields.captchaId}`);
    }
    if (captchaFields.durationMs) {
      parts.push(`耗时：${captchaFields.durationMs}ms`);
    }
    if (captchaFields.result === 'failed' && response) {
      parts.push(`响应：${truncateInlineText(response, 96)}`);
    }
    if (parts.length > 0) {
      return parts.join(' / ');
    }
    if (response) {
      return `响应：${truncateInlineText(response, 96)}`;
    }
    if (request) {
      return `请求：${truncateInlineText(request, 96)}`;
    }
  }

  if (request || response) {
    return truncateInlineText(response || request, 120);
  }
  return '-';
}

export function buildLogDetailSections(record = {}) {
  const sections = [];
  const description = String(record?.description || '').trim();
  const request = String(record?.oldAnswer || '').trim();
  const response = String(record?.newAnswer || '').trim();

  if (description) {
    const parsedSections = parseCaptchaLogDescription(description);
    if (parsedSections.length > 0) {
      sections.push(...parsedSections);
    } else {
      sections.push(...parseSearchLogDescription(description));
    }
  }

  if (request) {
    sections.push({
      key: 'request',
      label: '原始请求',
      value: request,
    });
  }

  if (response) {
    sections.push({
      key: 'response',
      label: '原始响应',
      value: response,
    });
  }

  return sections;
}

export function buildLogQidLabel(value) {
  const normalized = Number(value);
  return Number.isFinite(normalized) && normalized > 0 ? String(normalized) : '-';
}

function formatBooleanText(value) {
  if (value === 'true') return '是';
  if (value === 'false') return '否';
  return value || '-';
}

function truncateInlineText(value, limit) {
  const text = String(value || '').trim();
  if (!text) {
    return '-';
  }
  if (text.length <= limit) {
    return text;
  }
  return `${text.slice(0, Math.max(0, limit - 3))}...`;
}

function parseSearchLogDescription(description) {
  const fieldMap = parseSearchLogDescriptionMap(description);
  const fields = [
    ['requestId', '请求 ID'],
    ['mode', '模式'],
    ['use', '请求来源'],
    ['answers', '答案数'],
    ['hitProvider', '命中渠道'],
    ['billingProvider', '计费渠道'],
    ['localQuestionId', '本地题库 ID'],
    ['anonymous', '匿名请求'],
  ];

  return fields.map(([key, label]) => {
    let value = fieldMap[key] || '-';
    if (key === 'anonymous') {
      if (value === 'true') value = '是';
      if (value === 'false') value = '否';
    }
    if (key === 'billingProvider') {
      value = buildBillingProviderLabel(value);
    }
    return { key, label, value };
  }).filter((section) => section.value !== '-' || section.key === 'use');
}

function buildBillingProviderLabel(value) {
  const raw = String(value || '').trim();
  if (!raw || raw === '-') {
    return '-';
  }

  const labelMap = {
    ai: 'AI 搜索',
    local: '本地题库',
    icodef: 'Icodef 题库',
    wanneng: '万能题库',
    tikuhai: '题库海',
    enncy: 'Enncy 言溪题库',
    aidian: '爱点题库',
    lemon: '柠檬题库',
    billing_persist_failed: '计费落账失败',
    billing_db_unavailable: '计费数据库不可用',
    billing_charge_resolve_failed: '计费计算失败',
    billing_zero_amount: '零金额未计费',
  };

  return raw
    .split(',')
    .map((part) => String(part || '').trim())
    .filter(Boolean)
    .map((part) => labelMap[part.toLowerCase()] || part)
    .join('、');
}

function parseSearchLogDescriptionMap(description) {
  const text = String(description || '').trim();
  const summary = text.split(/\s+/)[0] || '';
  if (summary !== '搜索完成' && summary !== '搜索失败') {
    return {};
  }

  const fields = ['requestId', 'mode', 'use', 'answers', 'hitProvider', 'billingProvider', 'localQuestionId', 'anonymous'];
  return fields.reduce((acc, key) => {
    const value = extractDescriptionField(text, key);
    if (value) {
      acc[key] = value;
    }
    return acc;
  }, { summary });
}

function parseAIFallbackLogDescriptionMap(description) {
  const text = String(description || '').trim();
  if (!text.startsWith('AI兜底')) {
    return {};
  }

  const fields = ['status', 'stage', 'mode', 'requestId', 'taskId', 'code', 'message', 'answers', 'hitProvider', 'billingProvider'];
  return fields.reduce((acc, key) => {
    const value = extractDescriptionField(text, key);
    if (value) {
      acc[key] = value;
    }
    return acc;
  }, { summary: text.split(/\s+/)[0] || 'AI兜底' });
}

function parseCaptchaLogDescription(description) {
  const fieldMap = parseCaptchaLogDescriptionMap(description);
  if (!fieldMap.summary) {
    return [];
  }

  const fields = [
    ['step', '远程步骤'],
    ['result', '结果'],
    ['status', 'HTTP 状态'],
    ['remoteCode', '远程返回码'],
    ['remoteValid', '远程有效'],
    ['scene', '场景'],
    ['captchaId', '验证码 ID'],
    ['type', '验证码类型'],
    ['durationMs', '耗时(ms)'],
    ['hasKey', '已配置密钥'],
  ];

  return fields.map(([key, label]) => {
    let value = fieldMap[key] || '-';
    if (key === 'result') {
      if (value === 'success') value = '成功';
      if (value === 'failed') value = '失败';
    }
    if (key === 'remoteValid' || key === 'hasKey') {
      if (value === 'true') value = '是';
      if (value === 'false') value = '否';
    }
    return { key, label, value };
  });
}

function parseCaptchaLogDescriptionMap(description) {
  const text = String(description || '').trim();
  if (!text.startsWith('远程验证码')) {
    return {};
  }

  const fields = ['step', 'result', 'status', 'remoteCode', 'remoteValid', 'scene', 'captchaId', 'type', 'durationMs', 'hasKey'];
  const parsed = fields.reduce((acc, key) => {
    const value = extractDescriptionField(text, key);
    if (value) {
      acc[key] = value;
    }
    return acc;
  }, {});

  if (!parsed.step && !parsed.result) {
    return {};
  }

  const resultText = parsed.result === 'success' ? '成功' : (parsed.result === 'failed' ? '失败' : '');
  const summaryBase = text.split(/\s+/)[0] || '远程验证码';
  return {
    summary: `${summaryBase}${resultText}`,
    ...parsed,
  };
}

function extractDescriptionField(description, key) {
  const normalizedKey = String(key || '').trim();
  if (!normalizedKey) {
    return '';
  }

  const pattern = new RegExp(`(?:^|\\s)${normalizedKey}=([^\\s]*)`);
  const match = String(description || '').match(pattern);
  if (!match) {
    return '';
  }

  const value = String(match[1] || '').trim();
  return value || '-';
}
