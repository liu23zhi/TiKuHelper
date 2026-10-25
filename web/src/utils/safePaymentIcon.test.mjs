import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { isSafePaymentIcon, parseSafePaymentIcon } from './safePaymentIcon.mjs'
import { buildPaymentMethodSwalHtml } from './paymentMethodHelpers.mjs'
const vectors = JSON.parse(readFileSync(new URL('./paymentIconContract.json', import.meta.url), 'utf8'))
test('S02 shared finite SVG contract, bounds, XML bypasses and all 17 builtins', () => {
  for (const item of vectors) assert.equal(isSafePaymentIcon(item.svg), item.valid, item.id)
  assert.equal(vectors.filter(item => item.id.startsWith('builtin:')).length, 17)
  assert.equal(parseSafePaymentIcon('<svg/>').tag, 'svg')
})
test('S02 deprecated Swal builder reconstructs safe SVG and escapes all text', () => {
  const html = buildPaymentMethodSwalHtml([{ iconSVG: '<svg onload="INERT_ONLY"/>', label: '<img onerror="INERT_ONLY">', remark: '<script>INERT_ONLY</script>', value: '1" onclick="INERT_ONLY' }])
  assert.doesNotMatch(html, /<svg|<img|<script|data-payment-method-id="1"/)
  assert.match(html, /&lt;img/)
  assert.match(buildPaymentMethodSwalHtml([{ iconSVG: '<svg><path d="M0 0h1"/></svg>' }]), /<svg><path d="M0 0h1"><\/path><\/svg>/)
})
