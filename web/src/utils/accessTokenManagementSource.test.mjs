import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import vm from 'node:vm';
import { parse, compileScript } from '@vue/compiler-sfc';
import { computed, effectScope, ref } from 'vue';
import { useTableExpansion } from '../composables/useTableExpansion.mjs';
import { sumTableColumnWidths } from './tableLayout.mjs';

const testDir = dirname(fileURLToPath(import.meta.url));
const pagePath = resolve(testDir, '../pages/AccessTokenManagement/index.vue');

test('AccessTokenManagement page restores copy and show-all controls', async () => {
  const content = await readFile(pagePath, 'utf8');

  assert.match(content, /显示全部/);
  assert.match(content, /隐藏中间部分/);
  assert.match(content, /copyTokenValue\(record\)/);
  assert.match(content, /toggleTokenVisibility\(record\.id\)/);
  assert.match(content, /displayTokenValue\(record\)/);
  assert.match(content, /visibleTokenIds/);
  assert.match(content, /token-value-text--visible/);
  assert.match(content, /white-space:\s*normal/);
  assert.match(content, /table-layout="fixed"/);
  assert.match(content, /:scroll="\{ x: tableScrollX \}"/);
  assert.match(content, /#bodyCell/);
  assert.doesNotMatch(content, /customRender:/);
});

test('AccessTokenManagement keeps fixed widths in both scopes and expands the remark column with its row', async () => {
  const source = await readFile(pagePath, 'utf8');
  const { descriptor } = parse(source);
  const compiled = compileScript(descriptor, { id: 'token-table-regression' });
  const options = compiled.scriptAst.find(node => node.type === 'ExportDefaultDeclaration').declaration.arguments[0];
  const setup = options.properties.find(property => property.key?.name === 'setup');
  const showAllTokens = ref(false);
  const dataSource = ref([{ id: 17, remark: '长备注'.repeat(80) }]);
  const scope = effectScope();
  try {
    const setupSource = descriptor.script.content.slice(setup.start, setup.end);
    const state = scope.run(() => vm.runInNewContext(`({${setupSource}})`, {
      computed, useTableExpansion, sumTableColumnWidths, dataSource, showAllTokens,
      getStoredCurrentUser: () => ({ role: 'admin' }),
      isAdminUser: () => true,
      buildAccessTokenBalanceAlert: () => null,
      tableLoading: ref(false), visible: ref(false), submitLoading: ref(false),
      isEditing: ref(false), visibleTokenIds: ref([]), formState: ref({}),
      limitAccessTokenNameInput: value => value,
    }).setup());

    const widths = () => state.columns.value.map(column => column.width);
    assert.ok(widths().every(width => Number.isFinite(width) && width > 0));
    assert.equal(state.tableScrollX.value, widths().reduce((sum, width) => sum + width, 0));
    assert.equal(state.columns.value.some(column => column.key === 'username'), false);
    const collapsed = state.tableScrollX.value;
    state.remarkExpansion.toggle(dataSource.value[0], 'remark');
    assert.equal(state.columns.value.find(column => column.key === 'remark').width, 236);
    assert.equal(state.tableScrollX.value, collapsed + 66);
    state.remarkExpansion.toggle(dataSource.value[0], 'remark');
    assert.equal(state.tableScrollX.value, collapsed);
    showAllTokens.value = true;
    assert.equal(state.columns.value[0].key, 'username');
    assert.equal(state.tableScrollX.value, collapsed + 160);
  } finally {
    scope.stop();
  }
});

test('AccessTokenManagement page restricts the all-token view to admins and adds usernames', async () => {
  const content = await readFile(pagePath, 'utf8');

  assert.match(content, /显示全部令牌/);
  assert.match(content, /只看我的令牌/);
  assert.match(content, /v-if="isAdmin"/);
  assert.match(content, /showAllTokens/);
  assert.match(content, /buildAccessTokenListParams\(showAllTokens\.value\)/);
  assert.match(content, /title: '用户名'/);
});
