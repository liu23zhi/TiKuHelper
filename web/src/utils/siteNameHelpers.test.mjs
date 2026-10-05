import test from 'node:test'
import assert from 'node:assert/strict'

import { DEFAULT_SITE_NAME, normalizeSiteName } from './siteNameHelpers.mjs'

test('normalizeSiteName defaults the web title to 题库', () => {
  assert.equal(DEFAULT_SITE_NAME, '题库')
  assert.equal(normalizeSiteName(''), '题库')
  assert.equal(normalizeSiteName('   '), '题库')
  assert.equal(normalizeSiteName('  我的题库  '), '我的题库')
})
