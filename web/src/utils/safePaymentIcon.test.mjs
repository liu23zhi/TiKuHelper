import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

test('payment pages render icons only through the shared sanitized icon component', async () => {
  const [adminPage, userPage, component] = await Promise.all([
    readFile(path.join(root, 'pages/SystemSettings/PaymentMethodPanel.vue'), 'utf8'),
    readFile(path.join(root, 'pages/UserUsage/index.vue'), 'utf8'),
    readFile(path.join(root, 'components/SafePaymentIcon.vue'), 'utf8'),
  ])

  assert.match(adminPage, /<SafePaymentIcon[^>]*:icon-svg="record\.iconSvg"/)
  assert.match(userPage, /<SafePaymentIcon[^>]*:icon-svg="method\.iconSvg"/)
  assert.doesNotMatch(adminPage, /v-html/)
  assert.doesNotMatch(userPage, /v-html/)
  assert.match(component, /v-html="safeSvg"/)
  assert.match(component, /allowlisted and serialized by the service/)
});
