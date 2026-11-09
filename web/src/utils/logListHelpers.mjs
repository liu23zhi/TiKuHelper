export const LOG_MODULE_OPTIONS = [
  { label: '认证', value: 'auth' },
  { label: '题目', value: 'question' },
  { label: '搜题', value: 'search' },
  { label: 'AI 兜底', value: 'ai_fallback' },
  { label: '系统', value: 'system' },
  { label: '验证码', value: 'captcha' },
  { label: '用户管理', value: 'user' },
  { label: '账单管理', value: 'billing' },
  { label: '短信', value: 'sms' },
  { label: '充值', value: 'recharge' },
  { label: '访问令牌', value: 'access_token' },
];

export const LOG_ACTION_OPTIONS = [
  { label: '新增', value: 0 },
  { label: '更新', value: 1 },
  { label: '删除', value: 2 },
  { label: '查询 / 搜题', value: 3 },
  { label: '登录', value: 10 },
  { label: 'AI 兜底', value: 11 },
  { label: '注册', value: 12 },
  { label: '退出', value: 13 },
  { label: '密码重置', value: 14 },
  { label: '手机号修改', value: 15 },
  { label: '资料修改', value: 16 },
  { label: '远程验证码生成', value: 20 },
  { label: '远程验证码校验', value: 21 },
  { label: '远程验证码二验', value: 22 },
  { label: '本地验证码生成', value: 23 },
  { label: '本地验证码校验', value: 24 },
  { label: '发送短信', value: 30 },
  { label: '充值下单', value: 40 },
  { label: '充值到账', value: 41 },
];

const ACCOUNT_AUDIT_MODULES = new Set(['auth', 'user', 'sms', 'recharge', 'access_token']);

export function formatShanghaiLogDate(dateString, separator = ' ') {
  if (!dateString) return '-';
  const date = new Date(dateString);
  if (Number.isNaN(date.getTime())) return '-';

  const parts = new Intl.DateTimeFormat('zh-CN', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
    hourCycle: 'h23',
  }).formatToParts(date).reduce((result, part) => {
    result[part.type] = part.value;
    return result;
  }, {});

  return `${parts.year}-${parts.month}-${parts.day}${separator}${parts.hour}:${parts.minute}:${parts.second}`;
}

export function buildLogUsername(record = {}) {
  const userId = Number(record.userId);
  if (!Number.isInteger(userId) || userId <= 0) return '';
  return typeof record.username === 'string' ? record.username.trim() : '';
}

export function buildLogModuleLabel(value) {
  const normalized = String(value || '').trim().toLowerCase();
  return LOG_MODULE_OPTIONS.find((option) => option.value === normalized)?.label || value || '-';
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
  if (normalizedModule === 'captcha' && normalized === 23) return '本地生成';
  if (normalizedModule === 'captcha' && normalized === 24) return '本地校验';
  return LOG_ACTION_OPTIONS.find((option) => option.value === normalized)?.label || (value ?? '-');
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
  if (normalized === 12 || normalized === 41) return 'green';
  if (normalized === 13) return 'default';
  if (normalized >= 14 && normalized <= 16) return 'blue';
  if (normalized === 23 || normalized === 24 || normalized === 30) return 'cyan';
  if (normalized === 40) return 'gold';
  return 'default';
}

export function buildLogStatusLabel(record = {}) {
  const status = resolveLogStatus(record);
  if (status === 'success' || status === 'succeeded') return '成功';
  if (status === 'failed' || status === 'error') return '失败';
  if (status === 'partial_failure') return '部分失败';
  if (status === 'no_answer') return '无答案';
  if (status === 'unavailable') return '未配置';
  if (status === 'running' || status === 'processing') return '处理中';
  if (status === 'pending' || status === 'queued' || status === 'created') return '等待中';
  if (status === 'timeout') return '超时';
  return buildLogActionLabel(record?.action, record?.module);
}

export function buildLogStatusColor(record = {}) {
  const status = resolveLogStatus(record);
  if (status === 'success' || status === 'succeeded') return 'green';
  if (status === 'failed' || status === 'error' || status === 'timeout') return 'red';
  if (status === 'partial_failure') return 'orange';
  if (status === 'no_answer' || status === 'unavailable') return 'default';
  if (status === 'running' || status === 'processing') return 'blue';
  if (status === 'pending' || status === 'queued' || status === 'created') return 'gold';
  return buildLogActionColor(record?.action);
}

export function buildLogStructuredFields(record = {}, requestParamSeparator = ' ') {
  const description = String(record?.description || '').trim();
  const request = String(record?.oldAnswer || '').trim();
  const response = String(record?.newAnswer || '').trim();
  const responseSnapshot = parseLogSnapshot(response);
  const parsedDescription = parseSearchLogDescriptionMap(description);
  const parsedCaptchaDescription = parseCaptchaLogDescriptionMap(description, responseSnapshot);
  const parsedAIFallbackDescription = parseAIFallbackLogDescriptionMap(description);

  if (ACCOUNT_AUDIT_MODULES.has(String(record.module || '').toLowerCase())) {
    const auditRequest = parseLogRequestBody(request);
    const status = resolveLogStatus(record);
    const duration = Number(responseSnapshot.durationMs);
    return {
      summary: description || buildLogActionLabel(record.action, record.module),
      requestId: String(responseSnapshot.requestId || responseSnapshot.orderNo || auditRequest.requestId || auditRequest.orderNo || record.correlationId || '-'),
      mode: responseSnapshot.scene || auditRequest.scene ? `scene=${responseSnapshot.scene || auditRequest.scene}` : '-',
      requestSource: '-',
      answerCount: '-',
      hitProvider: buildAuditProviderLabel(responseSnapshot.provider || auditRequest.provider || responseSnapshot.paymentType || auditRequest.paymentType),
      failureReasons: ['failed', 'error', 'timeout'].includes(status) && responseSnapshot.message ? [String(responseSnapshot.message)] : [],
      billingProvider: responseSnapshot.durationMs != null && Number.isFinite(duration) && duration >= 0 ? `${duration}ms` : '-',
      requestSnapshot: request || '-',
      responseSnapshot: response || '-',
    };
  }

  if (parsedAIFallbackDescription.summary) {
    const fallbackSnapshot = parseLogSnapshot(response);
    const channel = buildHitProviderFields(
      parsedAIFallbackDescription.hitProvider || (parsedAIFallbackDescription.code ? 'ai' : '-'),
      fallbackSnapshot,
      description,
    );
    const errorCode = fallbackSnapshot?.error?.code || parsedAIFallbackDescription.code;
    if (errorCode === 'INSUFFICIENT_BALANCE') {
      channel.errors = ['当前余额不足，AI 兜底已禁用，请充值后再使用'];
    }
    return {
      summary: parsedAIFallbackDescription.summary,
      requestId: parsedAIFallbackDescription.requestId || parsedAIFallbackDescription.taskId || '-',
      mode: buildRequestParamsLabel(request, requestParamSeparator),
      requestSource: 'AI 兜底接口',
      answerCount: parsedAIFallbackDescription.answers || '-',
      hitProvider: buildAIRouteLabel(channel.provider, resolveLogAIRoute(parsedAIFallbackDescription.aiRoute, channel.provider, request)),
      failureReasons: channel.errors,
      billingProvider: buildBillingProviderLabel(parsedAIFallbackDescription.billingProvider),
      requestSnapshot: request || '-',
      responseSnapshot: response || '-',
    };
  }

  if (parsedCaptchaDescription.summary) {
    return {
      summary: parsedCaptchaDescription.summary,
      requestId: parsedCaptchaDescription.captchaId || record.correlationId || '-',
      mode: parsedCaptchaDescription.step || '-',
      requestSource: parsedCaptchaDescription.scene || parsedCaptchaDescription.type || '-',
      answerCount: parsedCaptchaDescription.status || '-',
      hitProvider: parsedCaptchaDescription.remoteValid || parsedCaptchaDescription.result || '-',
      failureReasons: parsedCaptchaDescription.result === 'failed' && responseSnapshot.message ? [String(responseSnapshot.message)] : [],
      billingProvider: parsedCaptchaDescription.durationMs ? `${parsedCaptchaDescription.durationMs}ms` : '-',
      requestSnapshot: request || '-',
      responseSnapshot: response || '-',
    };
  }

  const channel = buildHitProviderFields(parsedDescription.hitProvider, parseLogSnapshot(response), description);
  return {
    summary: parsedDescription.summary || description || '-',
    requestId: parsedDescription.requestId || '-',
    mode: buildRequestParamsLabel(request, requestParamSeparator),
    requestSource: parsedDescription.summary ? '普通搜题接口' : '-',
    answerCount: parsedDescription.answers || '-',
    hitProvider: buildAIRouteLabel(channel.provider, resolveLogAIRoute(parsedDescription.aiRoute, channel.provider, request)),
    failureReasons: channel.errors,
    billingProvider: buildBillingProviderLabel(parsedDescription.billingProvider),
    requestSnapshot: request || '-',
    responseSnapshot: response || '-',
  };
}

/**
 * 提取「结果」文本，供详情列直接展示。
 *
 * - 搜题：响应快照里的 answer（选项字母 + 答案文本，缺了再退到 bestAnswer / allAnswer）
 * - AI 兜底：响应快照里的 result.answer
 *
 * 取不到结果时返回空串，由调用方降级（例如退回失败原因或原始快照）。
 *
 * @param {object} record
 * @returns {string}
 */
export function buildLogResultText(record = {}) {
  const module = String(record?.module || '').trim().toLowerCase();
  if (module !== 'search' && module !== 'ai_fallback') {
    return '';
  }
  const snapshot = parseLogSnapshot(String(record?.newAnswer || '').trim());

  if (module === 'ai_fallback') {
    return normalizeResultText(snapshot?.result?.answer);
  }

  const answer = snapshot && typeof snapshot.answer === 'object' && snapshot.answer !== null ? snapshot.answer : {};
  const keyText = normalizeResultText(answer.answerKeyText);
  const text = normalizeResultText(answer.answerText);
  if (keyText && text && keyText !== text) {
    return `${keyText} ${text}`;
  }
  if (text) {
    return text;
  }
  if (keyText) {
    return keyText;
  }
  return joinAnswerList(answer.bestAnswer) || joinAnswerList(flattenAnswerList(answer.allAnswer));
}

function normalizeResultText(value) {
  // 答案文本里的换行压成空格：详情列是一行摘要，完整原文仍在「原始响应」列。
  return String(value ?? '').replace(/\s+/g, ' ').trim();
}

function joinAnswerList(value) {
  if (!Array.isArray(value)) {
    return '';
  }
  const list = value.map((item) => normalizeResultText(item)).filter(Boolean);
  return [...new Set(list)].join(' / ');
}

function flattenAnswerList(value) {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flat(Infinity);
}

function resolveLogStatus(record = {}) {
  const module = String(record.module || '').trim().toLowerCase();
  if (module === 'search' || module === 'ai_fallback' || module === 'captcha' || ACCOUNT_AUDIT_MODULES.has(module)) {
    const snapshot = parseLogSnapshot(record.newAnswer);
    if (typeof snapshot.status === 'string' && snapshot.status.trim()) {
      return snapshot.status.trim().toLowerCase();
    }
  }
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
  const snapshot = parseLogSnapshot(response);
  const captchaFields = parseCaptchaLogDescriptionMap(description, snapshot);

  if (ACCOUNT_AUDIT_MODULES.has(String(record.module || '').trim().toLowerCase()) && Object.keys(snapshot).length) {
    return buildAccountAuditDetail({ ...parseLogRequestBody(request), ...snapshot });
  }

  if (record.module === 'search' || record.module === 'ai_fallback') {
    // 详情列直接给「结果」；失败原因统一在「命中渠道 / 错误原因」列。
    // 确实没有结果时才退回原因（带「失败原因：」前缀，避免与答案混淆）。
    const result = buildLogResultText(record);
    if (result) {
      return `答案：${result}`;
    }
    const reasons = buildLogStructuredFields(record).failureReasons;
    if (reasons.length) {
      return `失败原因：${reasons.join(' / ')}`;
    }
  }

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
      parts.push(`响应：${response}`);
    }
    if (parts.length > 0) {
      return parts.join(' / ');
    }
    if (response) {
      return `响应：${response}`;
    }
    if (request) {
      return `请求：${request}`;
    }
  }

  if (request || response) {
    return response || request;
  }
  return '-';
}

export function buildLogTableDetail(record = {}) {
  if ((record.module === 'search' || record.module === 'ai_fallback') &&
      !buildLogResultText(record) && buildLogStructuredFields(record).failureReasons.length > 0) {
    return '-';
  }
  return buildLogRowDetail(record);
}

function parseLogSnapshot(value) {
  try {
    const parsed = JSON.parse(String(value || ''));
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {};
  } catch {
    return {};
  }
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
      sections.push(...parseSearchLogDescription(description, request));
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

/**
 * 操作日志「项目」列的展示文本。
 *
 * 口径只有一个来源：后端在列表响应里派生的 `project`（方案 B，读取时派生、不落库），
 * 前端不再二次推导 —— 同一语义只允许一份实现（AGENTS.md 第 13 条），
 * 否则口径一改就会两处不一致。
 *
 * 后端保证该字段永不为空字符串；这里仍做兜底，兼容尚未升级的后端与手工构造的行。
 *
 * @param {object} record
 * @returns {string}
 */
export function buildLogProjectText(record = {}) {
  const project = String(record?.project ?? '').trim();
  if (project) {
    return project;
  }
  // 旧后端没有 project 字段：只降级到行内已有的 qid，不复制完整派生表。
  const qid = buildLogLocalQuestionId(record);
  return qid === '-' ? '-' : `题目 #${qid}`;
}

export function buildLogQidLabel(value) {
  const normalized = Number(value);
  return Number.isFinite(normalized) && normalized > 0 ? String(normalized) : '-';
}

export function buildLogLocalQuestionId(record = {}) {
  const direct = buildLogQidLabel(record?.qid);
  if (direct !== '-') {
    return direct;
  }
  const description = String(record?.description || '');
  for (const key of ['localQuestionId', 'qid']) {
    const match = description.match(new RegExp(`(?:^|\\s)${key}=(\\d+)`));
    const value = buildLogQidLabel(match?.[1]);
    if (value !== '-') {
      return value;
    }
  }
  const request = String(record?.oldAnswer || '');
  try {
    const parsed = JSON.parse(request);
    const parsedValue = buildLogQidLabel(parsed?.qid ?? parsed?.localQuestionId);
    if (parsedValue !== '-') {
      return parsedValue;
    }
  } catch (_) {
    // 非 JSON 请求快照继续走文本兜底。
  }
  const requestMatch = request.match(/(?:["']?qid["']?\s*[:=]\s*["']?)(\d+)/i);
  return buildLogQidLabel(requestMatch?.[1]);
}

function formatBooleanText(value) {
  if (value === 'true') return '是';
  if (value === 'false') return '否';
  return value || '-';
}

function buildAuditProviderLabel(value) {
  const provider = String(value || '').trim();
  return ({ smsbao: '短信宝', alipay: '支付宝', wxpay: '微信支付', wechat: '微信支付' })[provider.toLowerCase()] || provider || '-';
}

function buildAccountAuditDetail(snapshot) {
  const labels = {
    username: '用户名', nickname: '昵称', phone: '手机号', name: '名称',
    balance: '余额', amount: '金额', quota: '额度', limit: '限额',
    totalQuotaLimit: '总额度', perMinuteLimit: '每分钟限额', perDayLimit: '每日限额',
    enabled: '启用', status: '状态', role: '角色', remark: '备注',
    expiresAt: '过期时间', tokenId: '令牌 ID', ownerId: '所属用户 ID', targetUserId: '目标用户 ID',
  };
  const formatValue = (value) => typeof value === 'boolean' ? (value ? '是' : '否') : String(value ?? '-');
  const formatChanges = (value) => {
    if (value === null || value === undefined) return '';
    if (typeof value !== 'object') return formatValue(value);
    return Object.entries(labels)
      .filter(([key]) => Object.hasOwn(value, key) && (value[key] === null || typeof value[key] !== 'object'))
      .map(([key, label]) => `${label}：${formatValue(value[key])}`).join('，');
  };
  const parts = snapshot.message ? [String(snapshot.message)] : [];
  for (const [key, label] of [['before', '变更前'], ['after', '变更后']]) {
    const text = formatChanges(snapshot[key]);
    if (text) parts.push(`${label}：${text}`);
  }
  if (!parts.length) {
    const text = formatChanges(snapshot);
    if (text) parts.push(text);
  }
  return parts.join('\n') || '-';
}

function parseSearchLogDescription(description, request) {
  const fieldMap = parseSearchLogDescriptionMap(description);
  const fields = [
    ['requestId', '请求 ID'],
    ['requestParams', '请求参数'],
    ['answers', '答案数'],
    ['hitProvider', '命中渠道'],
    ['billingProvider', '计费渠道'],
    ['localQuestionId', '本地题库 ID'],
    ['anonymous', '匿名请求'],
  ];

  return fields.map(([key, label]) => {
    let value = key === 'requestParams' ? buildRequestParamsLabel(request) : (fieldMap[key] || '-');
    if (key === 'anonymous') {
      if (value === 'true') value = '是';
      if (value === 'false') value = '否';
    }
    if (key === 'billingProvider') {
      value = buildBillingProviderLabel(value);
    }
    return { key, label, value };
  }).filter((section) => section.value !== '-' || section.key === 'requestParams');
}

function buildRequestParamsLabel(request, separator = ' ') {
  const raw = String(request || '').trim();
  const query = new URLSearchParams(raw.match(/(?:^|\s)query=([^\s]*)/)?.[1] || '');
  const body = parseLogRequestBody(raw);
  const parts = [];
  // Request snapshots also contain credentials and task text. Only expose
  // known non-secret routing controls, never arbitrary query/body fields.
  for (const key of ['use', 'mode', 'model', 'noRecord']) {
    for (const value of query.getAll(key)) {
      parts.push(`${key}=${value}`);
    }
  }
  for (const key of ['deepThinking', 'skipCache']) {
    if (typeof body[key] === 'boolean') {
      parts.push(`${key}=${body[key]}`);
    }
  }
  return parts.join(separator) || '-';
}

function parseLogRequestBody(request) {
  const raw = String(request || '').trim();
  const snapshot = parseLogSnapshot(raw.startsWith('{') ? raw : raw.match(/(?:^|\s)body=(\{[\s\S]*\})$/)?.[1]);
  if (typeof snapshot.body === 'string') return parseLogSnapshot(snapshot.body);
  if (snapshot.body && typeof snapshot.body === 'object') return snapshot.body;
  return snapshot;
}

function resolveLogAIRoute(route, provider, request) {
  if (route || !String(provider).split(/[、,]/).some((value) => /^ai$/i.test(value.trim()))) return route;
  const body = parseLogRequestBody(request);
  if (typeof body.deepThinking === 'boolean') return body.deepThinking ? 'deep' : 'normal';
  // SearchRequest omits deepThinking=false when serializing audit snapshots.
  return Object.hasOwn(body, 'question') && !Object.hasOwn(body, 'deepThinking') ? 'normal' : '';
}

function buildHitProviderFields(provider, snapshot = {}, description = '') {
  let normalizedProvider = String(provider || '').trim();
  if (normalizedProvider === '-') {
    normalizedProvider = '';
  }
  const errors = [];
  const errorMessages = new Set();
  const addError = (message, formatted = message, deduplicateMessage = true) => {
    const text = String(message || '').trim();
    const display = String(formatted || '').trim();
    if (text && (!deduplicateMessage || !errorMessages.has(text)) && !errors.includes(display)) {
      errorMessages.add(text);
      errors.push(display);
    }
  };
  // 描述串里的 `provider=... code=... error=...` 是审计写入口留下的原文，
  // 快照没有明细时（历史行 / 被裁剪过的快照）靠它兜底。
  const descriptionFailures = parseDescriptionFailureReasons(description);

  if (Array.isArray(snapshot?.failures)) {
    if (!normalizedProvider) {
      const failureProviders = [...new Set(snapshot.failures
        .map((failure) => String(failure?.provider || '').trim())
        .filter(Boolean))];
      normalizedProvider = failureProviders.join('、');
    }
    snapshot.failures.forEach((failure) => {
      const error = String(failure?.error || '').trim();
      if (!error) {
        return;
      }
      const failureProvider = String(failure?.provider || '').trim();
      const code = String(failure?.code || '').trim();
      const prefix = failureProvider && failureProvider !== normalizedProvider ? `${failureProvider}` : '';
      const codeText = code && code !== 'INSUFFICIENT_BALANCE' ? ` [${code}]` : '';
      addError(error, `${prefix}${codeText}${prefix || codeText ? '：' : ''}${error}`.trim(), false);
    });
  }
  if (!normalizedProvider) {
    normalizedProvider = [...new Set(descriptionFailures.map((failure) => failure.provider).filter(Boolean))].join('、');
  }

  addError(snapshot?.failureDetail);
  addError(snapshot?.error?.message);
  addError(snapshot?.detail || snapshot?.message);

  if (errors.length === 0) {
    for (const failure of descriptionFailures) {
      const text = failure.code === 'INSUFFICIENT_BALANCE' && failure.error
        ? `${failure.provider && failure.provider !== normalizedProvider ? `${failure.provider}：` : ''}${failure.error}`
        : failure.text;
      addError(failure.error || text, text, false);
    }
  }

  return {
    provider: normalizedProvider.split(/[、,]/).map((value) => value === 'ai' ? 'AI' : value).join('、') || '-',
    errors: [...new Set(errors)],
  };
}

function buildAIRouteLabel(provider, route) {
  const normalizedProvider = String(provider || '').trim();
  const normalizedRoute = String(route || '').trim().toLowerCase();
  if (!normalizedRoute) {
    return normalizedProvider || '-';
  }
  const routeLabel = {
    normal: '普通搜索',
    deep: '深度思考',
    deep_native: '深度思考 原生工具',
    deep_mcp: '深度思考 MCP编排',
  }[normalizedRoute];
  if (!routeLabel) {
    return normalizedProvider || '-';
  }
  const providerLabel = normalizedProvider && normalizedProvider !== '-' ? normalizedProvider : 'AI';
  if (/^ai$/i.test(providerLabel)) {
    return `AI ${routeLabel}`;
  }
  return `${providerLabel} ${routeLabel}`;
}

/**
 * 从审计描述里解析失败原因三元组（`provider=ai code=UPSTREAM_ERROR error=xxx`）。
 *
 * 描述被截断到 255 字符，error 也是以空白结尾的 token，
 * 因此这里只当作「快照没有明细时」的兜底。
 *
 * @returns {Array<{provider: string, code: string, text: string}>}
 */
function parseDescriptionFailureReasons(description) {
  const text = String(description || '');
  const reasons = [];
  const pattern = /provider=([^\s;]+)\s+code=([^\s;]*)\s+error=(.*?)(?=\s+provider=[^\s;]+\s+code=|$)/g;
  let match = pattern.exec(text);
  while (match !== null) {
    const provider = match[1] === '-' ? '' : match[1];
    const code = match[2] === '-' ? '' : match[2];
    const error = match[3].trim() === '-' ? '' : match[3].trim();
    if (error || code) {
      const codeText = code ? ` [${code}]` : '';
      reasons.push({
        provider,
        code,
        error,
        text: `${provider}${codeText}${provider || codeText ? '：' : ''}${error}`.trim(),
      });
    }
    match = pattern.exec(text);
  }
  return reasons;
}

function buildBillingProviderLabel(value) {
  const raw = String(value || '').trim();
  if (!raw || raw === '-') {
    return '-';
  }

  const labelMap = {
    ai: 'AI 搜索',
    ai_search: '联网搜索',
    web_read: '浏览网页',
    ai_ocr: '图片 OCR',
    script_answer: '脚本答题',
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
  if (!/^(搜索完成|搜索失败|搜索未执行)(?:，|$)/.test(summary)) {
    return {};
  }

  const fields = ['requestId', 'mode', 'use', 'answers', 'hitProvider', 'billingProvider', 'localQuestionId', 'anonymous', 'aiRoute'];
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

  const fields = ['status', 'stage', 'mode', 'requestId', 'taskId', 'code', 'message', 'answers', 'hitProvider', 'billingProvider', 'aiRoute'];
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

function parseCaptchaLogDescriptionMap(description, snapshot = {}) {
  const text = String(description || '').trim();
  if (!/^(远程|本地)验证码/.test(text)) {
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

  for (const key of fields) {
    if (!parsed[key] && snapshot[key] !== undefined && snapshot[key] !== null && typeof snapshot[key] !== 'object') {
      parsed[key] = String(snapshot[key]);
    }
  }
  if (typeof snapshot.status === 'string' && /^(success|succeeded|failed|error|pending|running)$/.test(snapshot.status)) {
    parsed.result ||= snapshot.status;
    if (parsed.status === snapshot.status) delete parsed.status;
  }
  if (text.startsWith('本地验证码') && !parsed.step) {
    parsed.step = text.startsWith('本地验证码生成') ? 'local_generate' : 'local_check';
  }

  if (!parsed.step && !parsed.result) {
    return {};
  }

  const resultText = parsed.result === 'success' ? '成功' : (parsed.result === 'failed' ? '失败' : '');
  const summaryBase = text.split(/\s+/)[0] || '验证码';
  return {
    summary: resultText && !summaryBase.endsWith(resultText) ? `${summaryBase}${resultText}` : summaryBase,
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
