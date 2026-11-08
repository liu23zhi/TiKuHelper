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

  assert.match(content, /<a-input-password\s+:value="record\.tokenValue"/);
  assert.match(content, /'update:value': accountEntry\.bind\(updateConfig, record, 'tokenValue'\)/);
  assert.match(content, /:precision="8"/, 'the existing fixed-price input retains eight-decimal UI precision');
  assert.doesNotMatch(content, /<a-input\s[^>]*(?:v-model:value|:value)="record\.tokenValue"/);
});

test('AISearchConfig API key is hidden until toggled visible', async () => {
  const content = await readFile(aiSearchPagePath, 'utf8');
  // This named child owns the same password control; the parent retains the canonical draft.
  assert.match(content, /import AIConnectionModelSection from '\.\/AIConnectionModelSection\.vue'/);
  assert.match(content, /components:\s*\{[^}]*\bAIConnectionModelSection\b[^}]*\}/);
  const connection = content.match(/<AIConnectionModelSection\b[\s\S]*?\/>/);
  assert.ok(connection, 'parent mounts the imported connection section');
  for (const binding of [
    ':section-id="sectionIds.connection"', ':form-value="formState"',
    ':fetching-models="fetchingModels"', ':model-options="modelOptions"',
    ':price-rows="priceRows"', ':pricing-gaps="pricingGaps"',
    "'update:form-value': aiEntry.bind(updateConnectionForm)", "'test': aiEntry.bind(handleOpenTestDialog)",
    "'price-for-configured-model': aiEntry.bind(openPriceModalForConfiguredModel)",
    "'fetch-models': aiEntry.bind(handleFetchModels)", "'select-model': aiEntry.bind(selectModel)",
  ]) assert.ok(connection[0].includes(binding), binding);
  assert.match(content, /updateConnectionForm\(nextValue\)\s*\{\s*if \(!this\.canUseAIPage\(\)\) return\s*Object\.assign\(this\.formState, nextValue\)/);
  assert.doesNotMatch(content, /<a-input\s+v-model:value="formState\.apiKey"/);

  const child = await readFile(resolve(dirname(aiSearchPagePath), 'AIConnectionModelSection.vue'), 'utf8');
  const password = child.match(/<a-input-password\b[\s\S]*?\/>/);
  assert.ok(password, 'API key remains a password/reveal control, not a plain input');
  assert.match(password[0], /:value="formValue\.apiKey"/);
  assert.match(password[0], /@update:value="updateField\('apiKey', \$event\)"/);
  for (const attribute of ['name="ai-service-api-key"', 'autocomplete="new-password"', 'autocapitalize="none"', ':spellcheck="false"', 'placeholder="请输入 API Key"']) {
    assert.ok(password[0].includes(attribute), attribute);
  }
  assert.doesNotMatch(child, /<a-input\s[^>]*(?:v-model:value|:value)="formValue\.apiKey"/);
  assert.match(child, /updateField\(field, value\)\s*\{\s*this\.\$emit\('update:form-value',\s*\{\s*\.\.\.this\.formValue,\s*\[field\]: value\s*\}\)/);
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
