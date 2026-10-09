import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const testDir = dirname(fileURLToPath(import.meta.url));
const layoutPath = resolve(testDir, '../layout/TikuAdapterLayout.vue');
const menuPath = resolve(testDir, '../components/SiderMenu/index.vue');

test('sidebar provides an accessible collapse toggle wired to the layout', async () => {
  const [layout, menu] = await Promise.all([
    readFile(layoutPath, 'utf8'),
    readFile(menuPath, 'utf8'),
  ]);

  assert.match(layout, /siderCollapsed/);
  assert.match(layout, /@toggle="toggleSider"/);
  assert.match(menu, /aria-expanded/);
  assert.match(menu, /aria-controls="portal-navigation"/);
  assert.match(menu, /MenuFoldOutlined/);
  assert.match(menu, /MenuUnfoldOutlined/);
});
