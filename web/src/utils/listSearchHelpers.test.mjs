import test from 'node:test';
import assert from 'node:assert/strict';

import {
  SEARCH_KEYWORD_MAX_LENGTH,
  normalizeSearchKeyword,
  buildSearchParams,
  shouldResetPage,
  countActiveFilters,
} from './listSearchHelpers.mjs';

test('normalizeSearchKeyword trims and caps the keyword', () => {
  assert.equal(normalizeSearchKeyword('  牛顿第二定律  '), '牛顿第二定律');
  assert.equal(normalizeSearchKeyword(''), '');
  assert.equal(normalizeSearchKeyword('   '), '');
  assert.equal(normalizeSearchKeyword(null), '');
  assert.equal(normalizeSearchKeyword(undefined), '');

  const long = 'a'.repeat(SEARCH_KEYWORD_MAX_LENGTH + 50);
  assert.equal(normalizeSearchKeyword(long).length, SEARCH_KEYWORD_MAX_LENGTH);
});

test('buildSearchParams drops empty filters and always sends pagination', () => {
  assert.deepEqual(buildSearchParams({}, 1, 10), { page: 1, pageSize: 10 });

  assert.deepEqual(
    buildSearchParams({ keyword: '  alice  ', role: '', status: null, type: undefined, page: 2 }, 3, 20),
    { page: 3, pageSize: 20, keyword: 'alice' },
  );

  // 非字符串的合法值也会带上。
  assert.deepEqual(buildSearchParams({ enabled: false, limit: 5 }), {
    page: 1,
    pageSize: 10,
    enabled: false,
    limit: 5,
  });

  // 非法页码回落到默认值。
  assert.deepEqual(buildSearchParams({}, 0, -1), { page: 1, pageSize: 10 });
  assert.deepEqual(buildSearchParams({}, 'x', 'y'), { page: 1, pageSize: 10 });
});

test('shouldResetPage compares effective filters only', () => {
  // 未变化（含空值差异）不应重置页码。
  assert.equal(shouldResetPage({ keyword: 'a', role: '' }, { keyword: 'a', role: null }), false);
  // trim 后相同也不重置。
  assert.equal(shouldResetPage({ keyword: 'a' }, { keyword: '  a  ' }), false);
  // 真正变化要重置。
  assert.equal(shouldResetPage({ keyword: 'a' }, { keyword: 'b' }), true);
  assert.equal(shouldResetPage({ keyword: '' }, { keyword: 'newton' }), true);
  assert.equal(shouldResetPage({ role: 'admin' }, { role: 'user' }), true);
  // 键顺序不影响判断。
  assert.equal(shouldResetPage({ a: '1', b: '2' }, { b: '2', a: '1' }), false);
});

test('countActiveFilters counts only effective values', () => {
  assert.equal(countActiveFilters({}), 0);
  assert.equal(countActiveFilters({ keyword: '', role: null, status: undefined }), 0);
  assert.equal(countActiveFilters({ keyword: 'x', role: '', status: 'enabled' }), 2);
  assert.equal(countActiveFilters({ enabled: false }), 1);
});
