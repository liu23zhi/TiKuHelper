import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const read = (file) => readFile(path.join(root, file), 'utf8');

test('compact viewport starts with navigation collapsed and gives an explicit expand action', async () => {
  const [layout, menu] = await Promise.all([
    read('layout/TikuAdapterLayout.vue'),
    read('components/SiderMenu/index.vue'),
  ]);
  assert.match(layout, /window\.matchMedia\(['"]\(max-width:\s*960px\)['"]\)/);
  assert.match(menu, /collapsed\s*\?\s*['"]展开侧边栏['"]\s*:\s*['"]折叠侧边栏['"]/);
  assert.match(menu, /\.sider-menu-wrap\s*\{[^}]*min-height:\s*auto/s);
});

test('user management renders readable mobile user cards with usable mobile pagination', async () => {
  const source = await read('pages/UserList/index.vue');
  assert.match(source, /class="mobile-user-list"/);
  assert.match(source, /class="mobile-user-card"/);
  assert.match(source, /class="mobile-user-pagination"/);
  assert.match(source, /class="desktop-user-table"/);
  assert.match(source, /@media\s*\(max-width:\s*640px\)/);
  assert.match(source, /<a-col\s+:xs="12"\s+:sm="12"\s+:lg="6"/);
});

test('billing records render mobile detail cards with statement controls inside details', async () => {
  const source = await read('pages/BillingRecordList/index.vue');
  assert.match(source, /class="mobile-billing-list"/);
  assert.match(source, /class="mobile-billing-card"/);
  assert.match(source, /class="desktop-billing-table"/);
  assert.doesNotMatch(source, /class="mobile-billing-visibility"/);
  assert.match(source, /billing-statement-toggle/);
  assert.match(source, /handleShowFeeDetail/);
  assert.match(source, /\bisAdmin,\s*pagination/);
  assert.match(source, /@media\s*\(max-width:\s*640px\)/);
  assert.match(source, /<a-col\s+:xs="12"\s+:sm="12"\s+:lg="6"/);
  assert.match(source, /\.mobile-billing-pagination\s*\{[^}]*display:\s*flex/s);
});

test('audit log cards keep operator, time and summary visible without a wide table', async () => {
  const source = await read('pages/LogList/index.vue');
  assert.match(source, /class="mobile-log-list"/);
  assert.match(source, /class="mobile-log-card"/);
  assert.match(source, /class="desktop-log-table"/);
  assert.match(source, /操作人/);
  assert.match(source, /@media\s*\(max-width:\s*640px\)/);
  assert.match(source, /\.mobile-log-pagination\s*\{[^}]*display:\s*flex/s);
});
