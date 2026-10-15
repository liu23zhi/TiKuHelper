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
} from './aiSearchModelHelpers.mjs';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const aiSearchConfigPagePath = path.join(__dirname, '..', 'pages', 'AISearchConfig', 'index.vue');

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
      manualModels: ['manual-only', 'gpt-5.4'],
      prompt: ' 系统 Prompt ',
      remark: ' 备注 ',
      temperature: 0,
      topP: 0,
      maxAISearchModelCalls: 4,
      maxAISearches: 3,
      maxAISearchRetries: 3,
      searchPricePerCall: 0.125,
    },
  );
});

test('buildAISearchConfigSavePayload defaults the web search unit price to zero', () => {
  const payload = buildAISearchConfigSavePayload({ enabled: true }, 'https://api.example.com/v1');
  assert.equal(payload.searchPricePerCall, 0);
});

test('buildAISearchConfigSavePayload preserves an explicitly configured retry budget', () => {
  assert.equal(
    buildAISearchConfigSavePayload({ enabled: true, maxAISearchRetries: 1 }, 'https://api.example.com/v1').maxAISearchRetries,
    1,
  );
});

test('buildAISearchConfigSavePayload stops sending any billing fields', () => {
  const payload = buildAISearchConfigSavePayload({ enabled: true }, 'https://api.example.com/v1');

  for (const field of ['exchangeRate', 'useGlobalBillingMultiplier', 'globalBillingMultiplier', 'inputTokenPrice', 'outputTokenPrice']) {
    assert.ok(!(field in payload), `expected save payload not to contain ${field}`);
  }
});

test('AISearchConfig page bills from the model price table', async () => {
  const content = await readFile(aiSearchConfigPagePath, 'utf8');

  assert.match(content, /buildModelNameOptions\(formState\.manualModelsText\)/);
  assert.match(content, /formState\.manualModelsText = buildModelNameText\(data\.manualModels/);
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
  assert.match(content, /multiplier: Number\(this\.priceForm\.multiplier \|\| 1\)/);
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
  assert.match(content, /price-table-wrap/);
  assert.match(content, /@media \(max-width: 768px\)/);
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
  assert.match(content, /label="模型最大调用次数"/);
  assert.match(content, /label="最大搜索次数"/);
  assert.match(content, /label="每轮重试次数"/);
  assert.match(content, /每一轮提示中都会告知模型剩余调用次数/);
  assert.match(content, /每一轮模型调用前都会告知剩余搜索次数/);
  assert.match(content, /模型最大调用次数必须是/);
  assert.match(content, /最大搜索次数必须是/);
  assert.match(content, /每轮重试次数必须是/);
  assert.match(helperContent, /maxAISearchModelCalls: Number\(formState\?\.maxAISearchModelCalls \|\| 4\)/);
  assert.match(helperContent, /maxAISearches: Number\(formState\?\.maxAISearches \|\| 3\)/);
  assert.match(helperContent, /maxAISearchRetries: Number\(formState\?\.maxAISearchRetries \?\? 3\)/);
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

test('AI search config exposes the UAPI search engine capability endpoint', async () => {
  const apiPath = path.join(__dirname, '..', 'api', 'TikuComponentApi.js');
  const pageContent = await readFile(aiSearchConfigPagePath, 'utf8');
  const apiContent = await readFile(apiPath, 'utf8');

  assert.match(apiContent, /export function getUAPISearchEngines\(\)/);
  assert.match(apiContent, /\/adapter-service\/uapi\/search\/engines/);
  assert.match(pageContent, /getUAPISearchEngines/);
  assert.match(pageContent, /查看联网搜索能力/);
});
