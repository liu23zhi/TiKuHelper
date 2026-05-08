export function buildLogModuleLabel(value) {
  const normalized = String(value || '').trim().toLowerCase();
  if (normalized === 'auth') return '认证';
  if (normalized === 'question') return '题目';
  if (normalized === 'system') return '系统';
  if (normalized === 'search') return '搜题';
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
  return value ?? '-';
}

export function buildLogActionColor(value) {
  const normalized = Number(value);
  if (normalized === 0) return 'green';
  if (normalized === 1) return 'blue';
  if (normalized === 2) return 'red';
  if (normalized === 3) return 'purple';
  if (normalized === 10) return 'gold';
  return 'default';
}

export function buildLogStructuredFields(record = {}) {
  const description = String(record?.description || '').trim();
  const request = String(record?.oldAnswer || '').trim();
  const response = String(record?.newAnswer || '').trim();
  const parsedDescription = parseSearchLogDescriptionMap(description);

  return {
    summary: parsedDescription.summary || description || '-',
    requestId: parsedDescription.requestId || '-',
    mode: parsedDescription.mode || '-',
    requestSource: parsedDescription.use || '-',
    answerCount: parsedDescription.answers || '-',
    hitProvider: parsedDescription.hitProvider || '-',
    billingProvider: parsedDescription.billingProvider || '-',
    requestSnapshot: request || '-',
    responseSnapshot: response || '-',
  };
}

export function buildLogDetailSections(record = {}) {
  const sections = [];
  const description = String(record?.description || '').trim();
  const request = String(record?.oldAnswer || '').trim();
  const response = String(record?.newAnswer || '').trim();

  if (description) {
    const parsedSections = parseSearchLogDescription(description);
    sections.push(...parsedSections);
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
    return { key, label, value };
  }).filter((section) => section.value !== '-' || section.key === 'use');
}

function parseSearchLogDescriptionMap(description) {
  const text = String(description || '').trim();
  if (!text.startsWith('搜索完成')) {
    return {};
  }

  const fields = ['requestId', 'mode', 'use', 'answers', 'hitProvider', 'billingProvider', 'localQuestionId', 'anonymous'];
  return fields.reduce((acc, key) => {
    const value = extractDescriptionField(text, key);
    if (value) {
      acc[key] = value;
    }
    return acc;
  }, { summary: '搜索完成' });
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
