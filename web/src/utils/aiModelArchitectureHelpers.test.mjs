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
  buildArchitectureModelOptions,
  buildArchitectureModelValues,
  buildArchitectureModelText,
  buildMissingArchitecturePayloads,
  buildAISearchConfigSavePayload,
} from './aiModelArchitectureHelpers.mjs';

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

test('buildArchitectureModelOptions derives unique model names from architecture rows', () => {
  assert.deepEqual(
    buildArchitectureModelOptions([
      { modelName: ' gpt-5.4 ' },
      { modelName: 'gpt-5.4-mini' },
      { modelName: 'gpt-5.4' },
      { modelName: '' },
      {},
      { modelName: 'deepseek-chat' },
    ]),
    [
      { label: 'gpt-5.4', value: 'gpt-5.4' },
      { label: 'gpt-5.4-mini', value: 'gpt-5.4-mini' },
      { label: 'deepseek-chat', value: 'deepseek-chat' },
    ],
  );
});

test('buildArchitectureModelValues and buildArchitectureModelText mirror architecture order', () => {
  const rows = [
    { modelName: 'gpt-5.4' },
    { modelName: 'gpt-5.4-mini' },
    { modelName: 'gpt-5.3-codex' },
  ];

  assert.deepEqual(buildArchitectureModelValues(rows), ['gpt-5.4', 'gpt-5.4-mini', 'gpt-5.3-codex']);
  assert.equal(buildArchitectureModelText(rows), 'gpt-5.4\ngpt-5.4-mini\ngpt-5.3-codex');
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

test('buildMissingArchitecturePayloads only returns rows missing from existing architectures', () => {
  assert.deepEqual(
    buildMissingArchitecturePayloads(
      [
        { modelName: 'gpt-5.4' },
        { modelName: 'gpt-5.4-mini' },
      ],
      'gpt-5.4,gpt-5.4-mini,gpt-5.3-codex\ndeepseek-chat',
    ),
    [
      {
        name: 'gpt-5.3-codex',
        modelName: 'gpt-5.3-codex',
        enabled: true,
        sort: 0,
        remark: '',
      },
      {
        name: 'deepseek-chat',
        modelName: 'deepseek-chat',
        enabled: true,
        sort: 0,
        remark: '',
      },
    ],
  );
});

test('buildAISearchConfigSavePayload persists editable model list instead of architecture rows', () => {
  assert.deepEqual(
    buildAISearchConfigSavePayload(
      {
        enabled: true,
        apiKey: ' sk-test ',
        model: ' gpt-5.4 ',
        prompt: ' 系统 Prompt ',
        remark: ' 备注 ',
        manualModelsText: 'manual-only,gpt-5.4\nmanual-only',
      },
      'https://api.example.com/v1',
      [
        { modelName: 'stale-from-architecture' },
        { modelName: 'gpt-5.4-mini' },
      ],
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
    },
  );
});

test('buildAISearchConfigSavePayload stops sending any billing fields', () => {
  const payload = buildAISearchConfigSavePayload({ enabled: true }, 'https://api.example.com/v1');

  for (const field of ['exchangeRate', 'useGlobalBillingMultiplier', 'globalBillingMultiplier', 'inputTokenPrice', 'outputTokenPrice']) {
    assert.ok(!(field in payload), `expected save payload not to contain ${field}`);
  }
});

test('AISearchConfig page bills from the model price table and has no multiplier UI', async () => {
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

  // 倍率与汇率相关 UI 必须彻底消失。
  assert.doesNotMatch(content, /formState\.exchangeRate/);
  assert.doesNotMatch(content, /formState\.globalBillingMultiplier/);
  assert.doesNotMatch(content, /formState\.useGlobalBillingMultiplier/);
  assert.doesNotMatch(content, /billingMultiplier/);
  assert.doesNotMatch(content, /formatMultiplier/);
  assert.doesNotMatch(content, /单模型倍率/);
  assert.doesNotMatch(content, /全局倍率/);
  assert.doesNotMatch(content, /label="汇率"/);
  assert.doesNotMatch(content, /美元/);

  assert.match(content, /architecture-table-wrap/);
  assert.match(content, /@media \(max-width: 768px\)/);
  assert.doesNotMatch(content, /syncModelTextFromArchitectures/);
  assert.doesNotMatch(content, /每条记录直接绑定模型名，并维护计费倍率、启停状态与排序/);
});
