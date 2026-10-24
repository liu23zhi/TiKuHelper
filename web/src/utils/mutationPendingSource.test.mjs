import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

function page(name) {
  return readFileSync(new URL(`../pages/${name}/index.vue`, import.meta.url), 'utf8')
}

test('payment and balance screens distinguish pending from failed mutations', () => {
  const recharge = page('UserUsage')
  const balance = page('UserList')
  assert.match(recharge, /res\?\.status === 202/)
  assert.match(recharge, /orderNo/)
  assert.match(balance, /res\?\.status === 202/)
})

test('SMS pages avoid immediate re-send when delivery is uncertain', () => {
  for (const name of ['Login', 'Register', 'Profile']) {
    const source = page(name)
    assert.match(source, /res\?\.status === 202/)
    assert.match(source, /smsCooldown\.start\(\)/)
  }
})
