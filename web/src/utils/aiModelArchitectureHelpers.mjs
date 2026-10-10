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

export function buildArchitectureModelOptions(rows) {
  const seen = new Set();
  const options = [];

  (Array.isArray(rows) ? rows : []).forEach(row => {
    const modelName = trimText(row?.modelName);
    if (!modelName || seen.has(modelName)) {
      return;
    }
    seen.add(modelName);
    options.push({
      label: modelName,
      value: modelName,
    });
  });

  return options;
}

export function buildArchitectureModelValues(rows) {
  return buildArchitectureModelOptions(rows).map(item => item.value);
}

export function buildArchitectureModelText(rows) {
  return buildArchitectureModelValues(rows).join('\n');
}

export function buildMissingArchitecturePayloads(rows, modelSource) {
  const existing = new Set(buildArchitectureModelValues(rows));
  return buildModelNameValues(modelSource)
    .filter(modelName => !existing.has(modelName))
    .map(modelName => ({
      name: modelName,
      modelName,
      enabled: true,
      sort: 0,
      remark: '',
    }));
}

// 模型单价已迁移到模型价格表，保存 AI 搜索配置时不再提交任何计费字段。
export function buildAISearchConfigSavePayload(formState, baseUrl) {
  const hasEditableModelText = Object.prototype.hasOwnProperty.call(formState || {}, 'manualModelsText');
  const manualModelSource = hasEditableModelText ? formState?.manualModelsText : formState?.manualModels;

  return {
    enabled: !!formState?.enabled,
    baseUrl,
    apiKey: formState?.apiKey || '',
    model: formState?.model || '',
    manualModels: buildModelNameValues(manualModelSource),
    prompt: formState?.prompt || '',
    remark: formState?.remark || '',
    temperature: 0,
    topP: 0,
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
    // 只有启用状态的模型价格才会真正参与计费。
    if (row?.status && String(row.status).toLowerCase() !== 'enabled') {
      return;
    }
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
 * - unresolvedModels：模型列表里没有价格的模型（含当前默认模型）
 * - configuredModel：当前默认模型名
 * - configuredModelPriced：当前默认模型是否已配置价格
 * - hasUnpricedModel：是否需要展示提示
 */
export function buildAIModelPricingGaps(formState, priceRows) {
  const configuredModel = trimText(formState?.model);
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
  }

  return {
    configuredModel,
    configuredModelPriced,
    unresolvedModels,
    hasUnpricedModel: unresolvedModels.length > 0,
  };
}
