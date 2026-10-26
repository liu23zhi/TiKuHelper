import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const stylesheetPath = path.join(__dirname, '..', 'styles', 'console.css');

test('console stylesheet keeps SweetAlert height override without breaking scrollbar compensation', async () => {
  const content = await readFile(stylesheetPath, 'utf8');

  assert.match(content, /body\.swal2-height-auto\s*\{[\s\S]*height:\s*auto\s*!important;/);
  assert.doesNotMatch(content, /body\.swal2-shown[\s\S]*padding-right:\s*0\s*!important;/);
  assert.doesNotMatch(content, /body\.swal2-shown[\s\S]*overflow-y:\s*auto\s*!important;/);
});
