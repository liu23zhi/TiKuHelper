import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { computed, defineComponent, reactive, ref } from 'vue'
import { sumTableColumnWidths } from './tableLayout.mjs'

const row = { urlHash: 'a'.repeat(64), imageSha256: 'b'.repeat(64), contentType: 'image/png', ocrText: '完整识别结果\n第二行', updatedAt: '2026-11-02T00:00:00Z' }
const response = (data = {}) => ({ status: 200, data: { data: [row], total: 1, summary: { matched: 1 }, configured: true, ...data } })
async function createPage(overrides = {}) {
  const source = await readFile(new URL('../pages/OCRCache/index.vue', import.meta.url), 'utf8')
  const script = source.match(/<script>([\s\S]*?)<\/script>/)[1].replace(/^import .*$/gm, '').replace('export default defineComponent', 'return defineComponent')
  const dependencies = {
    computed, defineComponent, reactive, ref, sumTableColumnWidths,
    onMounted() {}, onBeforeUnmount() {},
    Descriptions: { Item: {} }, message: { success() {} },
    Swal: { fire: async () => ({ isConfirmed: true }) },
    getAIOCRCacheEntries: async () => response(),
    clearAIOCRCache: async () => ({ status: 200, data: { deleted: 1 } }),
    ...overrides,
  }
  return new Function(...Object.keys(dependencies), script)(...Object.values(dependencies)).setup()
}

test('OCR filters, pagination and matching count use the same submitted scope', async () => {
  const calls = []
  const page = await createPage({ getAIOCRCacheEntries: async params => { calls.push(params); return response({ total: 60, summary: { matched: 60 } }) } })
  Object.assign(page.filters, { keyword: '  _%中文  ', contentType: 'image/png', updatedFrom: '2026-11-01', updatedTo: '2026-11-02' })
  await page.reload()
  assert.equal(page.matched.value, 60)
  page.filters.keyword = '尚未查询的输入'
  await page.onTableChange({ current: 2, pageSize: 20 })
  assert.deepEqual(calls, [1, 2].map(page => ({ keyword: '_%中文', contentType: 'image/png', updatedFrom: '2026-11-01', updatedTo: '2026-11-02', page, pageSize: 20 })))
  assert.match(page.scopeDescription.value, /_%中文/)
  assert.doesNotMatch(page.scopeDescription.value, /尚未查询/)
  await page.onTableChange({ current: 2, pageSize: 50 })
  assert.equal(calls.at(-1).page, 1)
  assert.equal(calls.at(-1).pageSize, 50)
})

test('OCR rejects reversed dates before any request but accepts the same start and end date', async () => {
  const calls = []
  const page = await createPage({ getAIOCRCacheEntries: async params => { calls.push(params); return response() } })
  Object.assign(page.filters, { updatedFrom: '2026-11-03', updatedTo: '2026-11-02' })
  await page.reload()
  assert.deepEqual(calls, [])
  assert.match(page.errorMessage.value, /起始日期不能晚于结束日期/)
  page.filters.updatedFrom = '2026-11-02'
  await page.reload()
  assert.equal(calls.length, 1)
  assert.equal(calls[0].updatedFrom, calls[0].updatedTo)
  assert.equal(page.errorMessage.value, '')
})

test('OCR deletion uses displayed filters without pagination and ignores unsubmitted edits', async () => {
  const deleted = []
  const confirmations = []
  const page = await createPage({
    clearAIOCRCache: async params => { deleted.push(params); return { status: 200, data: { deleted: 1 } } },
    Swal: { fire: async params => { confirmations.push(params); return { isConfirmed: true } } },
  })
  Object.assign(page.filters, { keyword: '指定题目', contentType: 'image/png', updatedFrom: '2026-11-02', updatedTo: '2026-11-02' })
  await page.reload()
  page.showDetails(row)
  page.filters.keyword = '不该删除的输入'
  await page.clearMatching()
  assert.deepEqual(deleted, [{ keyword: '指定题目', contentType: 'image/png', updatedFrom: '2026-11-02', updatedTo: '2026-11-02' }])
  assert.match(confirmations[0].title, /筛选结果/)
  assert.match(confirmations[0].text, /指定题目/)
  assert.match(confirmations[0].text, /包含其他分页/)
  assert.equal(page.detailVisible.value, false)
  assert.equal(page.pagination.current, 1)
  assert.equal(page.clearing.value, false)
})

test('OCR whole-cache clear is explicit and cancellation sends no DELETE', async () => {
  const deleted = []
  let confirmed = false
  const page = await createPage({
    clearAIOCRCache: async params => { deleted.push(params); return { status: 200, data: { deleted: 1 } } },
    Swal: { fire: async params => { assert.match(params.title, /全部 OCR 缓存/); return { isConfirmed: confirmed } } },
  })
  await page.load()
  await page.clearMatching()
  assert.deepEqual(deleted, [])
  confirmed = true
  await page.clearMatching()
  assert.deepEqual(deleted, [{}])
})

test('OCR stale response cannot replace the newer search results or summary', async () => {
  const pending = []
  const page = await createPage({ getAIOCRCacheEntries: () => new Promise(resolve => pending.push(resolve)) })
  const first = page.load()
  page.filters.keyword = '新筛选'
  const second = page.reload()
  pending[1](response({ data: [{ ...row, ocrText: '新的结果' }], total: 3, summary: { matched: 3 } }))
  await second
  pending[0](response({ total: 99, summary: { matched: 99 } }))
  await first
  assert.equal(page.rows.value[0].ocrText, '新的结果')
  assert.equal(page.matched.value, 3)
  assert.equal(page.pagination.total, 3)
  assert.equal(page.loading.value, false)
})

test('OCR failed list never enables deletion against stale results and keeps the server reason', async () => {
  let fail = false
  const page = await createPage({
    getAIOCRCacheEntries: async () => { if (fail) throw { response: { data: { message: '缓存数据库暂不可用' } } }; return response() },
    clearAIOCRCache: () => { throw new Error('列表失败不应删除') },
  })
  await page.load()
  fail = true
  page.filters.keyword = '下一次查询'
  await page.reload()
  assert.equal(page.errorMessage.value, '缓存数据库暂不可用')
  assert.deepEqual(page.rows.value, [])
  assert.equal(page.matched.value, 0)
  await page.clearMatching()
  assert.equal(page.errorMessage.value, '缓存数据库暂不可用')
})

test('OCR reset clears every filter and detail preserves full plain text with Shanghai time', async () => {
  const calls = []
  const page = await createPage({ getAIOCRCacheEntries: async params => { calls.push(params); return response() } })
  Object.assign(page.filters, { keyword: 'x', contentType: 'image/gif', updatedFrom: '2026-11-01', updatedTo: '2026-11-02' })
  await page.resetFilters()
  assert.deepEqual(calls, [{ page: 1, pageSize: 20 }])
  assert.equal(page.hasFilters.value, false)
  const fullText = `${'识别文本'.repeat(3000)}\n<script>alert(1)</script>`
  page.showDetails({ ...row, ocrText: fullText })
  assert.equal(page.detailRecord.value.ocrText, fullText)
  assert.equal(page.detailVisible.value, true)
  assert.match(page.formatDate(row.updatedAt), /8:00:00|08:00:00/)
  assert.equal(page.formatDate('invalid'), '-')
})

test('OCR cache management is linked only from the administrator menu and protected route', async () => {
  const [router, menu, api, page] = await Promise.all(['../router/router.js', '../components/SiderMenu/index.vue', '../api/TikuComponentApi.js', '../pages/OCRCache/index.vue'].map(path => readFile(new URL(path, import.meta.url), 'utf8')))
  assert.match(router, /path: 'ocr-cache',\s*name: 'ocr-cache',\s*component: OCRCache/)
  assert.match(router.match(/const ADMIN_ONLY_ROUTE_NAMES[\s\S]*?\]\)/)[0], /'ocr-cache'/)
  assert.match(menu.match(/const ADMIN_MENU_ITEMS[\s\S]*?\n\]/)[0], /key: 'ocr-cache', label: 'OCR 缓存管理'/)
  assert.doesNotMatch(menu.match(/const USER_MENU_ITEMS[\s\S]*?\n\]/)[0], /ocr-cache/)
  assert.match(api, /export function getAIOCRCacheEntries\(params\)[\s\S]*?ocr-cache\/entries/)
  assert.match(api, /export function clearAIOCRCache\(params\)[\s\S]*?method: 'delete',\s*params/)
  assert.match(page, /a-form :model="filters"/)
  assert.doesNotMatch(page, /v-html/)
})
