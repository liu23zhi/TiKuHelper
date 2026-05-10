import test from 'node:test';
import assert from 'node:assert/strict';

import {
  splitModelNames,
  buildArchitectureModelOptions,
  buildArchitectureModelValues,
  buildArchitectureModelText,
  buildMissingArchitecturePayloads,
  buildAISearchConfigSavePayload,
} from './aiModelArchitectureHelpers.mjs';

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
        inputTokenPrice: 0,
        outputTokenPrice: 0,
        enabled: true,
        sort: 0,
        remark: '',
      },
      {
        name: 'deepseek-chat',
        modelName: 'deepseek-chat',
        inputTokenPrice: 0,
        outputTokenPrice: 0,
        enabled: true,
        sort: 0,
        remark: '',
      },
    ],
  );
});

test('buildAISearchConfigSavePayload writes manualModels from architecture rows instead of textarea draft', () => {
  assert.deepEqual(
    buildAISearchConfigSavePayload(
      {
        enabled: true,
        apiKey: ' sk-test ',
        model: ' gpt-5.4 ',
        prompt: ' 系统 Prompt ',
        remark: ' 备注 ',
        inputTokenPrice: '0.12',
        outputTokenPrice: '0.34',
        manualModelsText: 'manual-only,should-not-win',
      },
      'https://api.example.com/v1',
      [
        { modelName: ' gpt-5.4 ' },
        { modelName: 'gpt-5.4-mini' },
      ],
    ),
    {
      enabled: true,
      baseUrl: 'https://api.example.com/v1',
      apiKey: ' sk-test ',
      model: ' gpt-5.4 ',
      manualModels: ['gpt-5.4', 'gpt-5.4-mini'],
      prompt: ' 系统 Prompt ',
      remark: ' 备注 ',
      temperature: 0,
      topP: 0,
      inputTokenPrice: 0.12,
      outputTokenPrice: 0.34,
    },
  );
});
