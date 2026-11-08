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


test('upstream SVG entry point delegates to strict VNodes without raw HTML', async () => {
  const wrapper = readFileSync(new URL('../components/SafePaymentIcon.vue', import.meta.url), 'utf8')
  const renderer = readFileSync(new URL('../components/SafePaymentIcon.js', import.meta.url), 'utf8')
  assert.match(wrapper, /SafePaymentIcon.js/)
  assert.doesNotMatch(wrapper + renderer, /v-html|innerHTML|outerHTML/)
  assert.match(renderer, /parseSafePaymentIcon/)
})

test('bounded inert iconfont metadata is accepted without permitting executable lookalikes', () => {
  assert.equal(isSafePaymentIcon('<svg t="1765858760722" class="icon" version="1.1" p-id="2038"><path d="M0 0h1" p-id="2039"/></svg>'), true)
  assert.equal(isSafePaymentIcon('<svg data-name="wechat" aria-label="支付"><path d="M0 0"/></svg>'), true)
  for (const svg of ['<svg data-name="x" onclick="alert(1)"/>', '<svg data-name="x" style="color:red"/>', '<svg data-name="https://evil.invalid"/>']) assert.equal(isSafePaymentIcon(svg), false)
})
