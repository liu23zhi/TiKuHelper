import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const testDir = dirname(fileURLToPath(import.meta.url));
const controllerPath = resolve(testDir, '../../../internal/controller/search_account_config.go');
const aiControllerPath = resolve(testDir, '../../../internal/controller/ai_search_config.go');
const componentApiPath = resolve(testDir, '../api/TikuComponentApi.js');
const searchAccountPagePath = resolve(testDir, '../pages/SearchAccountConfig/index.vue');
const aiSearchPagePath = resolve(testDir, '../pages/AISearchConfig/index.vue');

test('SearchAccountConfig API returns raw token values for default parameters', async () => {
  const content = await readFile(controllerPath, 'utf8');

  assert.match(content, /c\.JSON\(http\.StatusOK, configs\)/);
  assert.match(content, /c\.JSON\(http\.StatusOK, config\)/);
  assert.doesNotMatch(content, /RedactSearchAccountConfig/);
});

test('SearchAccountConfig default parameter is hidden until toggled visible', async () => {
  const content = await readFile(searchAccountPagePath, 'utf8');

  assert.match(content, /<a-input-password\s+v-model:value="record\.tokenValue"/);
  assert.doesNotMatch(content, /<a-input\s+v-model:value="record\.tokenValue"/);
});

test('AISearchConfig API key is hidden until toggled visible', async () => {
  const content = await readFile(aiSearchPagePath, 'utf8');

  assert.match(content, /<a-input-password\s+v-model:value="formState\.apiKey"/);
  assert.doesNotMatch(content, /<a-input\s+v-model:value="formState\.apiKey"/);
});

test('AISearchConfig API returns raw apiKey for the reveal control', async () => {
  const content = await readFile(aiControllerPath, 'utf8');

  assert.match(content, /c\.JSON\(http\.StatusOK, config\)/);
  assert.doesNotMatch(content, /RedactAISearchConfigView/);
});

test('AISearchConfig test request does not impose the global client timeout', async () => {
  const content = await readFile(componentApiPath, 'utf8');
  const functionSource = content.match(/export function testAISearchConfig\([\s\S]*?(?=export function updateAISearchConfig)/);

  assert.ok(functionSource, 'testAISearchConfig API function should exist');
  assert.match(functionSource[0], /timeout:\s*0/);
});
