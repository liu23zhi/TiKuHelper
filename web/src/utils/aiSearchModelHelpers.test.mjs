import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  splitModelNames,
  filterRemoteModelNames,
  buildModelNameOptions,
  buildModelNameText,
  buildAISearchConfigSavePayload,
  buildAIModelPricingGaps,
  isModelPriced,
  buildAIModelPriceSavePayload,
  formatAIModelContextWindowTokens,
} from './aiSearchModelHelpers.mjs';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const aiSearchConfigPagePath = path.join(__dirname, '..', 'pages', 'AISearchConfig', 'index.vue');

test('model price payload keeps existing prices and explicit context token counts', () => {
  const input = {
    id: 8,
    modelName: ' deepseek-chat ',
    displayName: 'DeepSeek 自定义',
    contextWindowTokens: '131072',
    inputPrice: 7.123456,
    cachedPrice: 0.5,
    cacheWritePrice: 1.2,
    outputPrice: 12,
    multiplier: 0.7,
    remark: '已有价格保持不变',
  };
  assert.deepEqual(buildAIModelPriceSavePayload(input), {
    ...input, modelName: 'deepseek-chat', contextWindowTokens: 131072,
  });
  assert.equal(input.contextWindowTokens, '131072');
  for (const value of [undefined, null, '', '  ', 0, '0']) {
    assert.equal(buildAIModelPriceSavePayload({ ...input, contextWindowTokens: value }).contextWindowTokens, 0);
  }
  assert.equal(buildAIModelPriceSavePayload({ ...input, contextWindowTokens: 1048576 }).contextWindowTokens, 1048576);
});

test('model price payload rejects invalid context windows before submission', () => {
  for (const value of [-1, '-1', 0.5, '131072.5', '128K', '1e6', NaN, Infinity, true, [], {}, Number.MAX_SAFE_INTEGER + 1]) {
    assert.throws(() => buildAIModelPriceSavePayload({ modelName: 'deepseek-chat', contextWindowTokens: value }), /上下文窗口/);
  }
  assert.throws(() => buildAIModelPriceSavePayload({ modelName: ' ' }), /模型名称/);
});

test('model context windows display readable token counts and preserve unknown values', () => {
  assert.equal(formatAIModelContextWindowTokens(131072), '131,072');
  assert.equal(formatAIModelContextWindowTokens('1048576'), '1,048,576');
  for (const value of [0, undefined, null, '', -1, 1.5, '128K', Infinity]) {
    assert.equal(formatAIModelContextWindowTokens(value), '未设置');
  }
});

test('splitModelNames handles commas newlines and trimming', () => {
  assert.deepEqual(
    splitModelNames(' gpt-5.4,gpt-5.4-mini\n gpt-5.3-codex ， deepseek-chat '),
    ['gpt-5.4', 'gpt-5.4-mini', 'gpt-5.3-codex', 'deepseek-chat'],
  );

  assert.deepEqual(splitModelNames(' , ， \n '), []);
});





test('buildModelNameOptions and buildModelNameText derive unique names from editable model text', () => {
  assert.deepEqual(
    buildModelNameOptions(' manual-only, gpt-5.4\nmanual-only，deepseek-chat '),
    [
      { label: 'manual-only', value: 'manual-only' },
      { label: 'gpt-5.4', value: 'gpt-5.4' },
      { label: 'deepseek-chat', value: 'deepseek-chat' },
    ],
  );

  assert.equal(
    buildModelNameText(['manual-only', 'gpt-5.4', 'manual-only', 'deepseek-chat']),
    'manual-only\ngpt-5.4\ndeepseek-chat',
  );
});

test('filterRemoteModelNames removes compact OpenAI and image-series remote models', () => {
  assert.deepEqual(
    filterRemoteModelNames([
      'gpt-5.4',
      'gpt-5.4-openai-compact',
      'GPT-IMAGE-1',
      'deepseek-chat',
      'stable-image-ultra',
      'gpt-5.4-mini',
    ]),
    ['gpt-5.4', 'deepseek-chat', 'gpt-5.4-mini'],
  );
});



test('buildAISearchConfigSavePayload persists the editable model list', () => {
  assert.deepEqual(
    buildAISearchConfigSavePayload(
      {
        enabled: true,
        apiKey: ' sk-test ',
        model: ' gpt-5.4 ',
        prompt: ' 系统 Prompt ',
        remark: ' 备注 ',
        manualModelsText: 'manual-only,gpt-5.4\nmanual-only',
        searchPricePerCall: 0.125,
      },
      'https://api.example.com/v1',
    ),
    {
      enabled: true,
      baseUrl: 'https://api.example.com/v1',
      apiKey: ' sk-test ',
      model: ' gpt-5.4 ',
      backupModel: '',
      manualModels: ['manual-only', 'gpt-5.4'],
      prompt: ' 系统 Prompt ',
      remark: ' 备注 ',
      temperature: 0,
      topP: 0,
      maxAISearchModelCalls: 4,
      maxAISearches: 3,
      maxAISearchRetries: 3,
      searchPricePerCall: 0.125,
      tiku_search_price_per_call: 0,
      maxAIWebReads: 5,
      webReadPricePerCall: 0,
      enableAIStagedTokenBilling: false,
      smoothAIStagedTokenMultiplier: false,
    },
  );
});

test('buildAISearchConfigSavePayload persists the optional backup model', () => {
  const payload = buildAISearchConfigSavePayload({
    enabled: true,
    model: 'primary-model',
    backupModel: 'backup-model',
    manualModelsText: 'primary-model\nbackup-model',
  }, 'https://api.example.com/v1');

  assert.equal(payload.backupModel, 'backup-model');
});

test('buildAISearchConfigSavePayload defaults the web search unit price to zero', () => {
  const payload = buildAISearchConfigSavePayload({ enabled: true }, 'https://api.example.com/v1');
  assert.equal(payload.searchPricePerCall, 0);
  assert.equal(payload.tiku_search_price_per_call, 0);
  assert.equal(payload.maxAIWebReads, 5);
  assert.equal(payload.webReadPricePerCall, 0);
});

test('buildAISearchConfigSavePayload sends the configured local Tiku search price using the API field name', () => {
  const payload = buildAISearchConfigSavePayload({ tikuSearchPricePerCall: 0.025 }, 'https://api.example.com/v1');
  assert.equal(payload.tiku_search_price_per_call, 0.025);
});

test('buildAISearchConfigSavePayload preserves web read limits and pricing', () => {
  const payload = buildAISearchConfigSavePayload({
    enabled: true,
    maxAIWebReads: 8,
    webReadPricePerCall: 0.02,
  }, 'https://api.example.com/v1');
  assert.equal(payload.maxAIWebReads, 8);
  assert.equal(payload.webReadPricePerCall, 0.02);
});

test('buildAISearchConfigSavePayload preserves an explicitly configured retry budget', () => {
  assert.equal(
    buildAISearchConfigSavePayload({ enabled: true, maxAISearchRetries: 1 }, 'https://api.example.com/v1').maxAISearchRetries,
    1,
  );
});

test('AI search count settings retain configured values above the former limits', () => {
  const configured = { maxAISearchModelCalls: 101, maxAISearches: 102, maxAISearchRetries: 103, maxAIWebReads: 104 };
  const payload = buildAISearchConfigSavePayload(configured, 'https://api.example.com/v1');
  for (const [field, value] of Object.entries(configured)) assert.equal(payload[field], value);
});

test('AI search count settings reject nonpositive, fractional and unsafe values before saving', () => {
  for (const field of ['maxAISearchModelCalls', 'maxAISearches', 'maxAISearchRetries', 'maxAIWebReads']) {
    for (const value of [0, -1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1, '', null, true]) {
      assert.throws(() => buildAISearchConfigSavePayload({ [field]: value }, ''), /正整数/);
    }
  }
});

test('buildAISearchConfigSavePayload always sends staged billing toggles with false defaults', () => {
  const payload = buildAISearchConfigSavePayload({ enabled: true }, 'https://api.example.com/v1');

  assert.equal(payload.enableAIStagedTokenBilling, false);
  assert.equal(payload.smoothAIStagedTokenMultiplier, false);
});

test('buildAISearchConfigSavePayload preserves explicitly enabled staged billing toggles', () => {
  const payload = buildAISearchConfigSavePayload({
    enabled: true,
    enableAIStagedTokenBilling: true,
    smoothAIStagedTokenMultiplier: true,
  }, 'https://api.example.com/v1');

  assert.equal(payload.enableAIStagedTokenBilling, true);
  assert.equal(payload.smoothAIStagedTokenMultiplier, true);
});

test('buildAISearchConfigSavePayload persists editable staged multiplier points', () => {
  const points = [
    { tokens: '0', multiplier: 4 },
    { tokens: '3000', multiplier: 2.5 },
    { tokens: '100000', multiplier: 0.3 },
  ];
  const payload = buildAISearchConfigSavePayload({
    enabled: true,
    stagedTokenMultiplierPoints: points,
  }, 'https://api.example.com/v1');

  assert.deepEqual(payload.stagedTokenMultiplierPoints, points);
});

test('buildAISearchConfigSavePayload does not send legacy model pricing fields', () => {
  const payload = buildAISearchConfigSavePayload({ enabled: true }, 'https://api.example.com/v1');

  for (const field of ['exchangeRate', 'useGlobalBillingMultiplier', 'globalBillingMultiplier', 'inputTokenPrice', 'outputTokenPrice']) {
    assert.ok(!(field in payload), `expected save payload not to contain ${field}`);
  }
});

test('AISearchConfig page bills from the model price table', async () => {
  const content = await readFile(aiSearchConfigPagePath, 'utf8');

  assert.match(content, /buildModelNameOptions\(formState\.manualModelsText\)/);
  assert.match(content, /formState\.manualModelsText = buildModelNameText\(data\.manualModels/);
  assert.match(content, /formState\.backupModel = data\.backupModel/);
  assert.match(content, /label="备用模型"/);
  assert.match(content, /openPriceModalForModel/);
  assert.match(content, /filterRemoteModelNames\(remoteModels\)/);
  assert.match(content, /formState\.manualModelsText = buildModelNameText\(filteredRemoteModels\)/);

  // 价格表是唯一计费入口。
  assert.match(content, /模型价格表/);
  assert.match(content, /priceRows/);
  assert.match(content, /priceColumns/);
  assert.match(content, /loadAIModelPrices/);
  assert.match(content, /handleSaveModelPrice/);
  assert.match(content, /handleDeleteModelPrice/);
  assert.match(content, /handleResetModelPrices/);
  assert.match(content, /缓存输入/);
  // 新增的缓存写入单价列。
  assert.match(content, /缓存写入（￥\/M）/);
  assert.match(content, /cacheWritePrice/);

  // 旧的「架构倍率 / 全局倍率 / 汇率」体系必须彻底消失。
  assert.doesNotMatch(content, /formState\.exchangeRate/);
  assert.doesNotMatch(content, /formState\.globalBillingMultiplier/);
  assert.doesNotMatch(content, /formState\.useGlobalBillingMultiplier/);
  assert.doesNotMatch(content, /billingMultiplier/);
  assert.doesNotMatch(content, /单模型倍率/);
  assert.doesNotMatch(content, /全局倍率/);
  assert.doesNotMatch(content, /label="汇率"/);

  // 新的「模型价格表倍率」列：默认 1，可编辑。
  assert.match(content, /title: '倍率'/);
  assert.match(content, /dataIndex: 'multiplier'/);
  assert.match(content, /priceForm\.multiplier = 1/);
  assert.match(content, /buildAIModelPriceSavePayload\(this\.priceForm\)/);
  assert.match(content, /formatMultiplier/);

  // 价格表的排序列已移除。
  const priceColumnsBlock = content.slice(
    content.indexOf('const priceColumns = ['),
    content.indexOf('const loadConfig'),
  );
  assert.ok(priceColumnsBlock, 'expected priceColumns block to exist');
  assert.doesNotMatch(priceColumnsBlock, /sort/, '价格表不应再有排序列');
  assert.match(priceColumnsBlock, /dataIndex: 'multiplier'/);
  assert.match(priceColumnsBlock, /dataIndex: 'cacheWritePrice'/);
  // 状态列与状态开关都已移除，价格行全部参与计费。
  assert.doesNotMatch(priceColumnsBlock, /status/, '价格表不应再有状态列');
  assert.doesNotMatch(content, /priceForm\.enabled/);
  assert.doesNotMatch(content, /label="状态"/);

  // 模型 ID 与显示名称分别成列。
  assert.match(priceColumnsBlock, /title: '模型 ID'/);
  assert.match(priceColumnsBlock, /title: '显示名称'/);

  // 价格表必须用人民币符号，不能出现美元计价（列头 $/M 或 $数字；模板字面量里的 $ 不算）。
  assert.doesNotMatch(content, /美元/);
  assert.doesNotMatch(content, /\$\/M/, '价格表列头不应出现美元符号');
  assert.doesNotMatch(content, /\$\d/, '价格展示不应出现美元符号');
  assert.doesNotMatch(content, /return '\$/, '价格格式化不应返回美元符号');
  assert.match(content, /输入（￥\/M）/);
  assert.match(content, /缓存输入（￥\/M）/);
  assert.match(content, /输出（￥\/M）/);
  // 金额格式化复用统一的 6 位精度模块，不再各页手写 toFixed。
  assert.match(content, /import \{ formatMoneyCny, formatMoneyValue \} from '@\/utils\/moneyFormat\.mjs'/);
  assert.match(content, /return formatMoneyCny\(amount\)/);
  assert.doesNotMatch(content, /toFixed\(/);

  // 模型绑定已彻底移除：卡片、弹窗、接口与同步逻辑都不应残留。
  assert.doesNotMatch(content, /模型绑定/);
  assert.doesNotMatch(content, /architecture/i);
  assert.doesNotMatch(content, /Architecture/);
  assert.match(content, /<ConsoleTableShell label="模型价格表">/);
  assert.match(content, /table-layout="fixed"[\s\S]*?:scroll="\{ x: sumTableColumnWidths\(priceColumns\) \}"/);
  assert.match(content, /@media \(max-width: 768px\)/);
});

test('AISearchConfig model price multiplier accepts positive fractional values below one', async () => {
  const content = await readFile(aiSearchConfigPagePath, 'utf8');
  assert.match(content, /v-model:value="priceForm\.multiplier"[^>]*:min="0\.0001"/);
  assert.match(content, /v-model:value="priceForm\.multiplier"[^>]*:precision="4"/);
});

test('AISearchConfig page exposes configurable model and search budgets', async () => {
  const content = await readFile(aiSearchConfigPagePath, 'utf8');
  const helperContent = await readFile(path.join(__dirname, 'aiSearchModelHelpers.mjs'), 'utf8');

  assert.match(content, /maxAISearchModelCalls: DEFAULT_MAX_AI_SEARCH_MODEL_CALLS/);
  assert.match(content, /maxAISearches: DEFAULT_MAX_AI_SEARCHES/);
  assert.match(content, /formState\.maxAISearchModelCalls = normalizeAISearchBudget\(data\.maxAISearchModelCalls/);
  assert.match(content, /formState\.maxAISearches = normalizeAISearchBudget\(data\.maxAISearches/);
  assert.match(content, /maxAISearchRetries: DEFAULT_MAX_AI_SEARCH_RETRIES/);
  assert.match(content, /formState\.maxAISearchRetries = normalizeAISearchRetries\(data\.maxAISearchRetries/);
  assert.match(content, /tikuSearchPricePerCall: 0/);
  assert.match(content, /formState\.tikuSearchPricePerCall = Number\(data\.tiku_search_price_per_call/);
  assert.match(content, /label="本地题库检索单价（元\/次）"/);
  assert.match(content, /本地题库检索单价必须是大于或等于 0 的有效金额/);
  assert.match(helperContent, /tiku_search_price_per_call: Number\(formState\?\.tikuSearchPricePerCall/);
  assert.match(content, /maxAIWebReads: DEFAULT_MAX_AI_WEB_READS/);
  assert.match(content, /formState\.maxAIWebReads = normalizeAIWebReads\(data\.maxAIWebReads/);
  assert.match(content, /webReadPricePerCall: 0/);
  assert.match(content, /formState\.webReadPricePerCall = Number\(data\.webReadPricePerCall/);
  assert.match(content, /网页读取次数/);
  assert.match(content, /网页读取单价（元\/次）/);
  assert.doesNotMatch(content, /网页读取工具（web_read）/);
  assert.match(content, /label="模型最大调用次数"/);
  assert.match(content, /label="最大搜索次数"/);
  assert.match(content, /label="每轮重试次数"/);
  assert.match(content, /每一轮提示中都会告知模型剩余调用次数/);
  assert.match(content, /每一轮模型调用前都会告知剩余搜索次数/);
  assert.match(content, /模型最大调用次数必须是/);
  assert.match(content, /最大搜索次数必须是/);
  assert.match(content, /每轮重试次数必须是/);
  assert.match(helperContent, /maxAISearchModelCalls: positiveCount\(formState\?\.maxAISearchModelCalls, 4/);
  assert.match(helperContent, /maxAISearches: positiveCount\(formState\?\.maxAISearches, 3/);
  assert.match(helperContent, /maxAISearchRetries: positiveCount\(formState\?\.maxAISearchRetries, 3/);
});

test('AISearchConfig count inputs and loading do not impose arbitrary upper bounds', async () => {
  const content = await readFile(aiSearchConfigPagePath, 'utf8');
  for (const field of ['maxAISearchModelCalls', 'maxAISearches', 'maxAISearchRetries', 'maxAIWebReads', 'contextCompressionMaxAttempts']) {
    const input = content.match(new RegExp(`<a-input-number\\s+v-model:value="formState\\.${field}"[^>]*>`));
    assert.ok(input, `missing ${field} input`);
    assert.doesNotMatch(input[0], /:max=/);
  }
  assert.doesNotMatch(content, /MAX_AI_SEARCH_BUDGET|MAX_AI_SEARCH_RETRIES = 10|MAX_AI_WEB_READS = 20|Math\.min\(parsed/);
});

test('AISearchConfig page loads and exposes staged billing toggles', async () => {
  const content = await readFile(aiSearchConfigPagePath, 'utf8');

  assert.match(content, /enableAIStagedTokenBilling: false/);
  assert.match(content, /smoothAIStagedTokenMultiplier: false/);
  assert.match(content, /formState\.enableAIStagedTokenBilling = !!data\.enableAIStagedTokenBilling/);
  assert.match(content, /formState\.smoothAIStagedTokenMultiplier = !!data\.smoothAIStagedTokenMultiplier/);
  assert.match(content, /v-model:checked="formState\.enableAIStagedTokenBilling"/);
  assert.match(content, /v-model:checked="formState\.smoothAIStagedTokenMultiplier"/);
  assert.match(content, /:disabled="!formState\.enableAIStagedTokenBilling"/);
  assert.match(content, /label="启用阶段计费"/);
  assert.match(content, /label="平滑变化倍率"/);
  assert.match(content, /阶段倍率价格表/);
  assert.match(content, /stagedTokenMultiplierPoints/);
  assert.match(content, /新增阶段/);
  assert.match(content, /恢复默认阶段表/);
});

const pricedRows = [
  { modelName: 'gpt-5.5', displayName: 'GPT-5.5' },
  { modelName: 'gpt-6-luna', displayName: 'GPT-6 Luna' },
  { modelName: 'daybreak-red', displayName: 'Daybreak Red' },
];

test('isModelPriced matches model ID and display name, ignoring case and separators', () => {
  // 模型 ID 与显示名称都能命中同一行。
  assert.equal(isModelPriced('gpt-5.5', pricedRows), true);
  assert.equal(isModelPriced('GPT-5.5', pricedRows), true);
  assert.equal(isModelPriced('  GPT 5.5 ', pricedRows), true);
  assert.equal(isModelPriced('gpt-6-luna', pricedRows), true);
  assert.equal(isModelPriced('GPT-6 Luna', pricedRows), true);
  assert.equal(isModelPriced('GPT-6Luna', pricedRows), true);
  assert.equal(isModelPriced('unknown-model', pricedRows), false);
  assert.equal(isModelPriced('', pricedRows), false);
  assert.equal(isModelPriced('gpt-5.5', undefined), false);
});

test('isModelPriced treats every price row as billable regardless of a status field', () => {
  // 价格表不再有状态：即便历史数据里带着 status，也不影响是否计费。
  assert.equal(isModelPriced('Daybreak Red', [{ modelName: 'daybreak-red', displayName: 'Daybreak Red', status: 'disabled' }]), true);
});

test('buildAIModelPricingGaps reports the configured model when it has no price', () => {
  const gaps = buildAIModelPricingGaps(
    { model: 'my-private-model', manualModelsText: 'GPT-5.5\nmy-private-model' },
    pricedRows,
  );

  assert.equal(gaps.configuredModel, 'my-private-model');
  assert.equal(gaps.configuredModelPriced, false);
  assert.equal(gaps.hasUnpricedModel, true);
  assert.deepEqual(gaps.unresolvedModels, ['my-private-model']);
});

test('buildAIModelPricingGaps reports an unpriced backup model separately', () => {
  const gaps = buildAIModelPricingGaps(
    { model: 'GPT-5.5', backupModel: 'backup-model', manualModelsText: 'GPT-5.5' },
    pricedRows,
  );

  assert.equal(gaps.configuredModelPriced, true);
  assert.equal(gaps.backupModel, 'backup-model');
  assert.equal(gaps.backupModelPriced, false);
  assert.deepEqual(gaps.unresolvedModels, ['backup-model']);
});

test('buildAIModelPricingGaps lists both configured and extra unpriced models', () => {
  const gaps = buildAIModelPricingGaps(
    { model: 'unpriced-default', manualModelsText: 'GPT-5.5\nunpriced-default\nunpriced-extra' },
    pricedRows,
  );

  assert.equal(gaps.configuredModelPriced, false);
  assert.deepEqual(gaps.unresolvedModels, ['unpriced-default', 'unpriced-extra']);
});

test('buildAIModelPricingGaps warns about unpriced manual models even when the default is priced', () => {
  const gaps = buildAIModelPricingGaps(
    { model: 'GPT-5.5', manualModelsText: 'GPT-5.5\nGPT-6 Luna\nmy-private-model' },
    pricedRows,
  );

  assert.equal(gaps.configuredModelPriced, true);
  assert.equal(gaps.hasUnpricedModel, true);
  assert.deepEqual(gaps.unresolvedModels, ['my-private-model']);
});

test('buildAIModelPricingGaps reports nothing when everything is priced', () => {
  const gaps = buildAIModelPricingGaps({ model: 'GPT-5.5', manualModelsText: 'GPT-5.5,GPT-6 Luna' }, pricedRows);

  assert.equal(gaps.configuredModelPriced, true);
  assert.equal(gaps.hasUnpricedModel, false);
  assert.deepEqual(gaps.unresolvedModels, []);
});

test('buildAIModelPricingGaps handles an empty configuration', () => {
  const gaps = buildAIModelPricingGaps({}, []);

  assert.equal(gaps.configuredModel, '');
  assert.equal(gaps.configuredModelPriced, false);
  assert.equal(gaps.hasUnpricedModel, false);
  assert.deepEqual(gaps.unresolvedModels, []);
});

test('AISearchConfig page surfaces unpriced-model hints', async () => {
  const content = await readFile(aiSearchConfigPagePath, 'utf8');

  assert.match(content, /buildAIModelPricingGaps/);
  assert.match(content, /pricingGaps/);
  assert.match(content, /当前默认模型还没有配置价格/);
  assert.match(content, /模型列表中有模型还没有配置价格/);
  assert.match(content, /openPriceModalForConfiguredModel/);
  assert.match(content, /去设置价格/);
  assert.match(content, /未定价/);
  assert.match(content, /hint-warning/);
});

test('TikuComponentApi exposes the AI pricing-status endpoint', async () => {
  const apiPath = path.join(__dirname, '..', 'api', 'TikuComponentApi.js');
  const content = await readFile(apiPath, 'utf8');

  assert.match(content, /export function getAISearchConfigPricingStatus\(\)/);
  assert.match(content, /\/adapter-service\/ai-search-config\/pricing-status/);
});

test('TikuComponentApi exposes OCR cache status and management endpoints', async () => {
  const apiPath = path.join(__dirname, '..', 'api', 'TikuComponentApi.js');
  const content = await readFile(apiPath, 'utf8');

  assert.match(content, /export function getAIOCRCacheStatus\(\)/);
  assert.match(content, /export function deleteAIOCRCacheBatch\(data\)/);
  assert.match(content, /\/adapter-service\/ai-search-config\/ocr-cache/);
  assert.match(content, /ocr-cache\/batch-delete/);
  assert.match(content, /method: 'post'/);
});

test('AISearchConfig page exposes OCR cache management controls', async () => {
  const content = await readFile(aiSearchConfigPagePath, 'utf8');

  assert.match(content, /图片 OCR 缓存/);
  assert.match(content, /loadAIOCRCacheStatus/);
  assert.doesNotMatch(content, /handleClearAIOCRCache/);
  assert.doesNotMatch(content, /清空缓存/);
  assert.match(content, /管理缓存条目/);
});

test('AI search config exposes the UAPI search engine capability endpoint', async () => {
  const apiPath = path.join(__dirname, '..', 'api', 'TikuComponentApi.js');
  const pageContent = await readFile(aiSearchConfigPagePath, 'utf8');
  const apiContent = await readFile(apiPath, 'utf8');

  assert.match(apiContent, /export function getUAPISearchEngines\(\)/);
  assert.match(apiContent, /\/adapter-service\/uapi\/search\/engines/);
  assert.match(pageContent, /getUAPISearchEngines/);
  assert.match(pageContent, /查看深度思考能力/);
  assert.doesNotMatch(pageContent, /查看联网搜索能力/);
  assert.match(pageContent, /getUAPIUsageStatus/);
  assert.match(apiContent, /export function getUAPIUsageStatus\(params\)/);
  assert.match(apiContent, /\/adapter-service\/uapi\/status\/usage/);
});

test('capability usage is clearly labeled as provider endpoint statistics', async () => {
  const content = await readFile(path.join(__dirname, '..', 'pages', 'AISearchConfig', 'AISearchCapabilityDialog.vue'), 'utf8');
  assert.match(content, /UAPI Pro 服务商端点统计/);
  assert.match(content, /不是本服务本地的调用次数/);
  assert.match(content, /服务商累计调用/);
});

test('AI capability modal shows provider statistics for used search, web Markdown and OCR endpoints', async () => {
  const pageContent = await readFile(aiSearchConfigPagePath, 'utf8');
  const dialog = await readFile(path.join(__dirname, '..', 'pages', 'AISearchConfig', 'AISearchCapabilityDialog.vue'), 'utf8');

  assert.match(pageContent, /const usedUsageRows = computed\(\(\) =>/);
  assert.match(pageContent, /item\.count > 0/);
  assert.match(pageContent, /usedCapabilityCards/);
  assert.match(pageContent, /联网搜索 API/);
  assert.match(pageContent, /网页转 Markdown API/);
  assert.match(pageContent, /图片 OCR API/);
  assert.match(dialog, /服务商暂未返回端点统计/);
  assert.match(pageContent, /:rows="usedUsageRows"/);
  assert.match(pageContent, /:cards="usedCapabilityCards"/);
});
