function trimText(value) {
  return String(value || '').trim();
}

export function splitModelNames(text) {
  return String(text || '')
    .split(/\r?\n|,|，/)
    .map(item => trimText(item))
    .filter(Boolean);
}

function modelSourceToText(modelSource) {
  return Array.isArray(modelSource) ? modelSource.join('\n') : modelSource;
}

export function buildModelNameValues(modelSource) {
  const seen = new Set();
  const values = [];

  splitModelNames(modelSourceToText(modelSource)).forEach(modelName => {
    if (seen.has(modelName)) {
      return;
    }
    seen.add(modelName);
    values.push(modelName);
  });

  return values;
}

export function buildModelNameOptions(modelSource) {
  return buildModelNameValues(modelSource).map(modelName => ({
    label: modelName,
    value: modelName,
  }));
}

export function buildModelNameText(modelSource) {
  return buildModelNameValues(modelSource).join('\n');
}

export function filterRemoteModelNames(modelSource) {
  return buildModelNameValues(modelSource).filter(modelName => {
    const normalized = modelName.toLowerCase();
    return !normalized.includes('-openai-compact') && !normalized.includes('image');
  });
}

function normalizeAIModelContextWindowTokens(value) {
  const input = typeof value === 'string' ? value.trim() : value;
  if (input === '' || input === undefined || input === null) return 0;
  if ((typeof input !== 'number' && typeof input !== 'string')
    || (typeof input === 'string' && !/^\d+$/.test(input))) {
    throw new Error('上下文窗口必须是非负整数 Token 数，0 表示未设置');
  }
  const tokens = Number(input);
  if (!Number.isSafeInteger(tokens) || tokens < 0) {
    throw new Error('上下文窗口必须是非负整数 Token 数，且不能超过安全整数范围');
  }
  return tokens;
}

export function formatAIModelContextWindowTokens(value) {
  try {
    const tokens = normalizeAIModelContextWindowTokens(value);
    return tokens > 0 ? tokens.toLocaleString('zh-CN') : '未设置';
  } catch {
    return '未设置';
  }
}

export function buildAIModelPriceSavePayload(formState) {
  const modelName = trimText(formState?.modelName);
  if (!modelName) throw new Error('请填写模型名称');
  return {
    id: formState?.id,
    modelName,
    displayName: trimText(formState?.displayName),
    contextWindowTokens: normalizeAIModelContextWindowTokens(formState?.contextWindowTokens),
    inputPrice: Number(formState?.inputPrice || 0),
    cachedPrice: Number(formState?.cachedPrice || 0),
    cacheWritePrice: Number(formState?.cacheWritePrice || 0),
    outputPrice: Number(formState?.outputPrice || 0),
    multiplier: Number(formState?.multiplier || 1),
    remark: formState?.remark || '',
  };
}

export const DEFAULT_AI_STAGED_TOKEN_MULTIPLIER_POINTS = Object.freeze([
  Object.freeze({ tokens: 0, multiplier: 4 }),
  Object.freeze({ tokens: 3000, multiplier: 2 }),
  Object.freeze({ tokens: 10000, multiplier: 1 }),
  Object.freeze({ tokens: 30000, multiplier: 0.65 }),
  Object.freeze({ tokens: 100000, multiplier: 0.3 }),
]);

function normalizeStagedTokenMultiplierPoints(points) {
  const source = Array.isArray(points) && points.length
    ? points
    : DEFAULT_AI_STAGED_TOKEN_MULTIPLIER_POINTS;
  return source.map(point => ({
    tokens: String(point?.tokens ?? 0),
    multiplier: Number(point?.multiplier ?? 1),
  }));
}

function positiveCount(value, fallback, label) {
  if (value === undefined) return fallback;
  const number = ['number', 'string'].includes(typeof value) && String(value).trim() !== '' ? Number(value) : NaN;
  if (!Number.isSafeInteger(number) || number < 1) {
    throw new Error(`${label}必须是正整数，且不能超过安全整数范围`);
  }
  return number;
}

// 模型单价已迁移到模型价格表，保存 AI 搜索配置时不再提交任何计费字段。
export function buildAISearchConfigSavePayload(formState, baseUrl) {
  const hasEditableModelText = Object.prototype.hasOwnProperty.call(formState || {}, 'manualModelsText');
  const manualModelSource = hasEditableModelText ? formState?.manualModelsText : formState?.manualModels;
  const hasStagedPoints = Object.prototype.hasOwnProperty.call(formState || {}, 'stagedTokenMultiplierPoints');

  return {
    enabled: !!formState?.enabled,
    baseUrl,
    apiKey: formState?.apiKey || '',
    model: formState?.model || '',
    backupModel: formState?.backupModel || '',
    manualModels: buildModelNameValues(manualModelSource),
    prompt: formState?.prompt || '',
    remark: formState?.remark || '',
    temperature: 0,
    topP: 0,
    maxAISearchModelCalls: positiveCount(formState?.maxAISearchModelCalls, 4, '模型最大调用次数'),
    maxAISearches: positiveCount(formState?.maxAISearches, 3, '最大搜索次数'),
    maxAISearchRetries: positiveCount(formState?.maxAISearchRetries, 3, '每轮重试次数'),
    searchPricePerCall: Number(formState?.searchPricePerCall || 0),
    maxAIWebReads: positiveCount(formState?.maxAIWebReads, 5, '网页读取次数'),
    webReadPricePerCall: Number(formState?.webReadPricePerCall || 0),
    ...(Object.prototype.hasOwnProperty.call(formState || {}, 'imageOCRPricePerCall')
      ? { imageOCRPricePerCall: Number(formState?.imageOCRPricePerCall || 0) }
      : {}),
    enableAIStagedTokenBilling: !!formState?.enableAIStagedTokenBilling,
    smoothAIStagedTokenMultiplier: !!formState?.smoothAIStagedTokenMultiplier,
    ...(hasStagedPoints ? { stagedTokenMultiplierPoints: normalizeStagedTokenMultiplierPoints(formState?.stagedTokenMultiplierPoints) } : {}),
  };
}

function normalizePricingKey(value) {
  return trimText(value)
    .toLowerCase()
    .replace(/[\s\u3000\-_（）()*]/g, '');
}

function buildPricedModelKeys(priceRows) {
  const keys = new Set();
  (Array.isArray(priceRows) ? priceRows : []).forEach(row => {
    // 价格表没有状态概念：每一行都参与计费。
    // 模型 ID 与显示名称都可以作为绑定名，因此两个键都要登记。
    const modelKey = normalizePricingKey(row?.modelName);
    if (modelKey) {
      keys.add(modelKey);
    }
    const displayKey = normalizePricingKey(row?.displayName);
    if (displayKey) {
      keys.add(displayKey);
    }
  });
  return keys;
}

export function isModelPriced(modelName, priceRows) {
  const key = normalizePricingKey(modelName);
  return Boolean(key) && buildPricedModelKeys(priceRows).has(key);
}

/**
 * 汇总 AI 设置页需要提示的「缺价格」情况。
 * - unresolvedModels：模型列表、默认模型或备用模型中没有价格的模型
 * - configuredModel：当前默认模型名
 * - configuredModelPriced：当前默认模型是否已配置价格
 * - hasUnpricedModel：是否需要展示提示
 */
export function buildAIModelPricingGaps(formState, priceRows) {
  const configuredModel = trimText(formState?.model);
  const backupModel = trimText(formState?.backupModel);
  const pricedKeys = buildPricedModelKeys(priceRows);

  const unresolvedModels = [];
  const seen = new Set();
  buildModelNameValues(formState?.manualModelsText ?? formState?.manualModels).forEach(modelName => {
    const key = normalizePricingKey(modelName);
    if (!key || pricedKeys.has(key) || seen.has(key)) {
      return;
    }
    seen.add(key);
    unresolvedModels.push(modelName);
  });

  const configuredKey = normalizePricingKey(configuredModel);
  const configuredModelPriced = Boolean(configuredKey) && pricedKeys.has(configuredKey);
  if (configuredKey && !configuredModelPriced && !seen.has(configuredKey)) {
    unresolvedModels.unshift(configuredModel);
    seen.add(configuredKey);
  }

  const backupKey = normalizePricingKey(backupModel);
  const backupModelPriced = Boolean(backupKey) && pricedKeys.has(backupKey);
  if (backupKey && !backupModelPriced && !seen.has(backupKey)) {
    unresolvedModels.splice(configuredKey && !configuredModelPriced ? 1 : 0, 0, backupModel);
    seen.add(backupKey);
  }

  return {
    configuredModel,
    configuredModelPriced,
    backupModel,
    backupModelPriced,
    unresolvedModels,
    hasUnpricedModel: unresolvedModels.length > 0,
  };
}
