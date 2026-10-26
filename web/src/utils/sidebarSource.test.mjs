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
  assert.match(menu, /折叠侧边栏/);
  assert.match(menu, /展开侧边栏/);
  assert.match(menu, /margin-top:\s*auto/);
});

test('sidebar collapse toggle shows an icon like the menu items do', async () => {
  const menu = await readFile(menuPath, 'utf8');

  // 与菜单项一致：从同一图标包引入折叠/展开图标。
  assert.match(menu, /MenuFoldOutlined/);
  assert.match(menu, /MenuUnfoldOutlined/);
  assert.match(menu, /from '@ant-design\/icons-vue'/);

  // 图标必须真正渲染进按钮：折叠时用展开图标，展开时用折叠图标。
  assert.match(menu, /:is="collapsed \? MenuUnfoldOutlined : MenuFoldOutlined"/);
  assert.match(menu, /class="collapse-button-icon"/);

  // A compact rail keeps the explicit Chinese action available on every viewport.
  assert.match(menu, /class="collapse-button-label">\{\{ collapsed \? '展开' : '折叠侧边栏' \}\}<\/span>/);
  assert.match(menu, /\.collapse-button\.is-collapsed \.collapse-button-label\s*\{[^}]*display:\s*inline/);
  assert.match(menu, /:title="collapsed \? '展开侧边栏' : '折叠侧边栏'"/);

  // setup() 必须把图标暴露给模板，否则 _ctx 解析不到。
  assert.match(menu, /return \{[\s\S]*MenuFoldOutlined,[\s\S]*MenuUnfoldOutlined,[\s\S]*\}/);

  // 折叠态样式：图标居中。
  assert.match(menu, /\.collapse-button\.is-collapsed/);
  assert.match(menu, /\.collapse-button-icon\s*\{[^}]*font-size/);
});
