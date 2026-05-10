function trimText(value) {
  return String(value || '').trim();
}

export function splitModelNames(text) {
  return String(text || '')
    .split(/\r?\n|,|，/)
    .map(item => trimText(item))
    .filter(Boolean);
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
  return splitModelNames(Array.isArray(modelSource) ? modelSource.join('\n') : modelSource)
    .filter(modelName => !existing.has(modelName))
    .map(modelName => ({
      name: modelName,
      modelName,
      inputTokenPrice: 0,
      outputTokenPrice: 0,
      enabled: true,
      sort: 0,
      remark: '',
    }));
}

export function buildAISearchConfigSavePayload(formState, baseUrl, architectureRows) {
  return {
    enabled: !!formState?.enabled,
    baseUrl,
    apiKey: formState?.apiKey || '',
    model: formState?.model || '',
    manualModels: buildArchitectureModelValues(architectureRows),
    prompt: formState?.prompt || '',
    remark: formState?.remark || '',
    temperature: 0,
    topP: 0,
    inputTokenPrice: Number(formState?.inputTokenPrice || 0),
    outputTokenPrice: Number(formState?.outputTokenPrice || 0),
  };
}
