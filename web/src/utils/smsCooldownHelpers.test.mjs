import test from 'node:test'
import assert from 'node:assert/strict'

import {
  SMS_SEND_COOLDOWN_SECONDS,
  formatSMSCodeButtonText,
} from './smsCooldownHelpers.mjs'

test('formatSMSCodeButtonText shows a 60 second retry countdown', () => {
  assert.equal(SMS_SEND_COOLDOWN_SECONDS, 60)
  assert.equal(formatSMSCodeButtonText(60), '60s后重试')
  assert.equal(formatSMSCodeButtonText(1), '1s后重试')
})

test('formatSMSCodeButtonText returns the send label when ready', () => {
  assert.equal(formatSMSCodeButtonText(0), '发送验证码')
  assert.equal(formatSMSCodeButtonText(-1), '发送验证码')
})
