import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { billingTableColumnWidths } from './userPortalHelpers.mjs'

const here = path.dirname(fileURLToPath(import.meta.url))
const pages = [
  path.join(here, '..', 'pages', 'BillingRecordList', 'index.vue'),
  path.join(here, '..', 'pages', 'UserUsage', 'index.vue'),
]

test('shared billing widths match specified content examples plus padding', () => {
  assert.equal(billingTableColumnWidths.id, 104)
  assert.equal(billingTableColumnWidths.username, 170)
  assert.equal(billingTableColumnWidths.subject, 170)
  assert.equal(billingTableColumnWidths.subjectExpanded, 236)
  assert.equal(billingTableColumnWidths.amount, 120)
  assert.equal(billingTableColumnWidths.referenceNo, 300)
  assert.equal(billingTableColumnWidths.accessTokenName, 122)
  assert.equal(billingTableColumnWidths.createdAt, 112)
  assert.equal(billingTableColumnWidths.clientIp, 270)
})

test('both billing tables use shared centered, wrapping columns and conditional project expansion', async () => {
  for (const page of pages) {
    const source = await readFile(page, 'utf8')
    assert.match(source, /table-layout="fixed"/)
    assert.match(source, /:scroll="\{ x: tableScrollX \}"/)
    assert.match(source, /components: \{ OverflowTableText \}/)
    assert.match(source, /<OverflowTableText[\s\S]*?:text="buildBillingRecordListSubjectText\(record\)"/)
    assert.match(source, /:expanded="!!expandedSubjects\[record\.id\]"/)
    assert.match(source, /@toggle="toggleSubject\(record\.id\)"/)
    assert.match(source, /subjectExpanded/)
    assert.match(source, /buildBillingRecordEffectiveMultiplierText\(record\)/)
    assert.match(source, /title: '请求 ID'/)
    assert.match(source, /title: '时间'/)
    assert.match(source, /text-align: center/)
    assert.match(source, /vertical-align: middle/)
    assert.match(source, /overflow-wrap: anywhere/)
  }
})

test('administrative billing details retain the fee popup and use the unified action label', async () => {
  const source = await readFile(pages[0], 'utf8')
  assert.match(source, /@click="handleShowFeeDetail\(record\)">查看详情<\/a-button>/)
  assert.match(source, /buildBillingRecordFeeDetailHtml\(detailRecord\)/)
  assert.match(source, /title: '用户'/)
  assert.match(source, /title: '来源 IP'/)
  assert.match(source, /class="mobile-billing-card__subject">[\s\S]*?<OverflowTableText/)
  assert.match(source, /class="mobile-billing-time">\{\{ formatDateTime\(record\.createdAt\) \}\}/)
  assert.match(source, /\.mobile-billing-time\s*\{\s*white-space:\s*pre-line/)
})
