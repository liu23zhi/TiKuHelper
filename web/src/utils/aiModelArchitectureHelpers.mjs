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

function normalizePositiveNumber(value) {
  const amount = Number(value);
  return Number.isFinite(amount) && amount > 0 ? amount : 1;
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
      billingMultiplier: 1,
      enabled: true,
      sort: 0,
      remark: '',
    }));
}

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
    exchangeRate: normalizePositiveNumber(formState?.exchangeRate),
    useGlobalBillingMultiplier: formState?.useGlobalBillingMultiplier !== false,
    globalBillingMultiplier: normalizePositiveNumber(formState?.globalBillingMultiplier),
  };
}
