import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const testDir = dirname(fileURLToPath(import.meta.url));
const controllerPath = resolve(testDir, '../../../internal/controller/search_account_config.go');

test('SearchAccountConfig API returns raw token values for default parameters', async () => {
  const content = await readFile(controllerPath, 'utf8');

  assert.match(content, /c\.JSON\(http\.StatusOK, configs\)/);
  assert.match(content, /c\.JSON\(http\.StatusOK, config\)/);
  assert.doesNotMatch(content, /RedactSearchAccountConfig/);
});
