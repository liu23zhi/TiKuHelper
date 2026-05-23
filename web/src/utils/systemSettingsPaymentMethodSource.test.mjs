import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const systemSettingsPagePath = path.join(__dirname, '..', 'pages', 'SystemSettings', 'index.vue');

test('SystemSettings payment method management reads backend epayType field consistently', async () => {
  const content = await readFile(systemSettingsPagePath, 'utf8');

  assert.match(content, /record\.epayType\s*\|\|\s*'-'/);
  assert.match(content, /ePayType:\s*record\.epayType\s*\|\|\s*''/);
  assert.doesNotMatch(content, /record\.ePayType\s*\|\|\s*'-'/);
  assert.doesNotMatch(content, /ePayType:\s*record\.ePayType\s*\|\|\s*''/);
});
