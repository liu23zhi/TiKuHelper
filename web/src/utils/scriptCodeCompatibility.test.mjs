import test from 'node:test'
import assert from 'node:assert/strict'
import { isRedeemableScriptCode } from './scriptManagementHelpers.mjs'
test('accepts generated admin and purchased activation codes but rejects masks and malformed secrets', () => {
  assert.equal(isRedeemableScriptCode('TKA-4e7181dbd65cb10581f367434bd6e35a'), true)
  assert.equal(isRedeemableScriptCode('sac_' + 'a'.repeat(64)), true)
  for (const code of ['****e35a', 'TKA-abc', 'sac_short', '', 'TKA-' + 'z'.repeat(32)]) assert.equal(isRedeemableScriptCode(code), false)
})
