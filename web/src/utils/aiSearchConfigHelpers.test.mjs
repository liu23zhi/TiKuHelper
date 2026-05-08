import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildAISearchTestForm,
  buildAISearchTestPayload,
  buildAISearchTestResult,
  hasAISearchTestResult,
} from './aiSearchConfigHelpers.mjs';

test('buildAISearchTestForm seeds the dialog from current AI search config', () => {
  assert.deepEqual(buildAISearchTestForm({
    baseUrl: ' https://api.example.com/v1 ',
    apiKey: ' test-key ',
    model: ' gpt-4o-mini ',
    prompt: ' 系统 Prompt ',
  }), {
    baseUrl: 'https://api.example.com/v1',
    apiKey: 'test-key',
    model: 'gpt-4o-mini',
    prompt: '系统 Prompt',
  });

  assert.deepEqual(buildAISearchTestForm(null), {
    baseUrl: '',
    apiKey: '',
    model: '',
    prompt: '',
  });
});

test('buildAISearchTestPayload prefers explicit dialog overrides and falls back to current config', () => {
  assert.deepEqual(buildAISearchTestPayload({
    baseUrl: ' ',
    apiKey: ' dialog-key ',
    model: ' ',
    prompt: '',
  }, {
    baseUrl: 'https://fallback.example.com/v1',
    apiKey: 'fallback-key',
    model: 'gpt-4o',
    prompt: '默认 Prompt',
  }), {
    baseUrl: 'https://fallback.example.com/v1',
    apiKey: 'dialog-key',
    model: 'gpt-4o',
    prompt: '默认 Prompt',
  });
});

test('buildAISearchTestPayload requires a model after fallback resolution', () => {
  assert.throws(() => buildAISearchTestPayload({
    baseUrl: '',
    apiKey: '',
    model: ' ',
    prompt: '',
  }, {
    model: ' ',
  }), /请选择要测试的 AI 模型/);
});

test('buildAISearchTestResult normalizes response fields for the dialog', () => {
  assert.deepEqual(buildAISearchTestResult({
    data: {
      endpoint: 'https://api.example.com/v1/chat/completions',
      answers: ['你好', '世界'],
      requestBody: '{"hello":true}',
      rawResponse: 'data: [DONE]',
    },
  }), {
    answers: ['你好', '世界'],
    requestBody: 'POST https://api.example.com/v1/chat/completions\n\n{"hello":true}',
    rawResponse: 'data: [DONE]',
  });

  assert.deepEqual(buildAISearchTestResult({ data: null }), {
    answers: [],
    requestBody: '',
    rawResponse: '',
  });
});

test('hasAISearchTestResult detects whether any result block should be shown', () => {
  assert.equal(hasAISearchTestResult({ answers: [], requestBody: '', rawResponse: '' }), false);
  assert.equal(hasAISearchTestResult({ answers: ['hello'], requestBody: '', rawResponse: '' }), true);
  assert.equal(hasAISearchTestResult({ answers: [], requestBody: '{"a":1}', rawResponse: '' }), true);
});
