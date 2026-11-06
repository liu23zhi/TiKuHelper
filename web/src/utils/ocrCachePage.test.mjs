import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { compile, computed, createSSRApp, defineComponent, h, reactive, ref } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { sumTableColumnWidths } from './tableLayout.mjs'
import { buildAIToolTestPayload } from './aiSearchConfigHelpers.mjs'

const row = { url: 'https://images.example.test/question.png?token=original', urlHash: 'a'.repeat(64), imageSha256: 'b'.repeat(64), contentType: 'image/png', ocrText: '完整识别结果\n第二行', updatedAt: '2026-11-02T00:00:00Z' }
const response = (data = {}) => ({ status: 200, data: { data: [row], total: 1, summary: { matched: 1 }, configured: true, ...data } })
async function createPage(overrides = {}) {
  const source = await readFile(new URL('../pages/OCRCache/index.vue', import.meta.url), 'utf8')
  const script = source.match(/<script>([\s\S]*?)<\/script>/)[1].replace(/^import .*$/gm, '').replace('export default defineComponent', 'return defineComponent')
  const dependencies = {
    computed, defineComponent, reactive, ref, sumTableColumnWidths, buildAIToolTestPayload, OverflowTableText: {},
    onMounted() {}, onBeforeUnmount() {},
    Descriptions: { Item: {} }, message: { success() {}, error() {} },
    Swal: { fire: async () => ({ isConfirmed: true }) },
    getAIOCRCacheEntries: async () => response(),
    getAIOCRCacheStatus: async () => ({ status: 200, data: { entries: 1, configured: true, latestUpdatedAt: row.updatedAt } }),
    Blob,
    getAIOCRCacheImage: async () => ({ status: 200, data: new Blob(['image'], { type: 'image/png' }) }),
    URL: { createObjectURL: () => 'blob:ocr-preview', revokeObjectURL() {} },
    testAIToolCall: async () => ({ status: 200, data: { success: true, ocrCacheEntry: row } }),
    deleteAIOCRCacheBatch: async () => ({ status: 200, data: { deleted: 1 } }),
    ...overrides,
  }
  return new Function(...Object.keys(dependencies), script)(...Object.values(dependencies)).setup()
}

async function renderPage(page) {
  const source = await readFile(new URL('../pages/OCRCache/index.vue', import.meta.url), 'utf8')
  const template = source.match(/<template>([\s\S]*?)<\/template>\s*<script>/)[1]
  const components = Object.fromEntries([...template.matchAll(/<([A-Z][\w-]*|a-[\w-]+)/g)].map(([, name]) => [name, { setup: (_, { slots }) => () => h('div', null, slots.default?.()) }]))
  components['a-table'] = {
    props: ['dataSource', 'columns'],
    setup: (props, { slots }) => () => h('table', null, props.dataSource.map(record => h('tr', null, props.columns.map(column => h('td', { 'data-column': column.key }, slots.bodyCell({ column, record })))))),
  }
  return renderToString(createSSRApp({ components, methods: { sumTableColumnWidths }, setup: () => page, render: compile(template, { prefixIdentifiers: true }) }))
}

test('OCR list and detail show the full original URL as escaped plain text', async () => {
  const url = `https://images.example.test/原始图片.png?token=abc_%&unsafe=<img src=x onerror=alert(1)>&tail=${'x'.repeat(800)}`
  const page = await createPage({ getAIOCRCacheEntries: async () => response({ data: [{ ...row, url }] }) })
  await page.load()
  page.showDetails(page.rows.value[0])
  const html = await renderPage(page)
  const listURL = html.match(/<td data-column="url">([\s\S]*?)<\/td>/)?.[1] || ''
  const detailURL = html.match(/<div[^>]*label="图片 URL"[^>]*><span class="url-value">([\s\S]*?)<\/span><\/div>/)?.[1] || ''
  const escapedURL = url.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
  for (const displayed of [listURL, detailURL]) {
    assert.ok(displayed.includes(escapedURL), 'URL must keep every original character without truncation')
    assert.doesNotMatch(displayed, /<(?:a|img)\b/)
  }
  assert.equal(page.detailRecord.value.url, url)
  assert.ok(html.includes(row.urlHash), 'URL hash remains visible')
})

test('OCR list and detail identify historical records without a stored URL', async () => {
  const page = await createPage({ getAIOCRCacheEntries: async () => response({ data: [{ ...row, url: '' }] }) })
  await page.load()
  page.showDetails(page.rows.value[0])
  const html = await renderPage(page)
  const listURL = html.match(/<td data-column="url">([\s\S]*?)<\/td>/)?.[1] || ''
  const detailURL = html.match(/<div[^>]*label="图片 URL"[^>]*><span class="url-value">([\s\S]*?)<\/span><\/div>/)?.[1] || ''
  assert.match(listURL, /历史记录未保存 URL/)
  assert.match(detailURL, /历史记录未保存 URL/)
})

test('OCR detail fetches the stored image on demand and renders the returned blob', async () => {
  const calls = []
  const page = await createPage({ getAIOCRCacheImage: async hash => { calls.push(hash); return { status: 200, data: new Blob(['image'], { type: 'image/png' }) } } })
  page.showDetails(row)
  await page.loadCurrentImage()
  assert.deepEqual(calls, [row.urlHash])
  assert.equal(page.currentImageUrl.value, 'blob:ocr-preview')
  assert.equal(page.imageLoading.value, false)
  const html = await renderPage(page)
  assert.match(html, /获取当前图片/)
  assert.match(html, /<img[^>]+src="blob:ocr-preview"/)
})

test('OCR detail reports image fetch failure and never requests an unsaved historical URL', async () => {
  let calls = 0
  const page = await createPage({
    getAIOCRCacheImage: async () => { calls++; return { status: 502, data: new Blob([JSON.stringify({ message: '图片源暂不可用' })], { type: 'application/json' }) } },
  })
  page.showDetails(row)
  await page.loadCurrentImage()
  assert.equal(calls, 1)
  assert.match(page.imageError.value, /图片源暂不可用/)
  assert.equal(page.imageLoading.value, false)
  assert.equal(page.currentImageUrl.value, '')
  page.showDetails({ ...row, url: '' })
  await page.loadCurrentImage()
  assert.equal(calls, 1)
  assert.match(page.imageError.value, /没有保存图片 URL/)
})

test('OCR detail releases the temporary image URL when closed or switched', async () => {
  const revoked = []
  const page = await createPage({ URL: { createObjectURL: () => 'blob:ocr-preview', revokeObjectURL: value => revoked.push(value) } })
  page.showDetails(row)
  await page.loadCurrentImage()
  page.showDetails({ ...row, urlHash: 'next-row' })
  assert.deepEqual(revoked, ['blob:ocr-preview'])
  await page.loadCurrentImage()
  page.closeDetails()
  assert.deepEqual(revoked, ['blob:ocr-preview', 'blob:ocr-preview'])
  assert.equal(page.currentImageUrl.value, '')
})

test('OCR detail ignores a late image response after it closes', async () => {
  let finishRequest
  const created = []
  const page = await createPage({
    getAIOCRCacheImage: () => new Promise(resolve => { finishRequest = resolve }),
    URL: { createObjectURL: () => { created.push('created'); return 'blob:ocr-preview' }, revokeObjectURL() {} },
  })
  page.showDetails(row)
  const pending = page.loadCurrentImage()
  page.closeDetails()
  finishRequest({ status: 200, data: new Blob(['image'], { type: 'image/png' }) })
  await pending
  assert.deepEqual(created, [])
  assert.equal(page.currentImageUrl.value, '')
  assert.equal(page.imageLoading.value, false)
})

test('OCR cache manual add runs the existing direct OCR flow and reloads after success', async () => {
  const calls = []
  const listCalls = []
  const page = await createPage({
    testAIToolCall: async body => { calls.push(body); return { status: 200, data: { success: true, ocrCacheEntry: { ...row, url: body.url } } } },
    getAIOCRCacheEntries: async params => { listCalls.push(params); return response() },
  })
  page.openAddDialog()
  page.addImageURL.value = 'https://images.example.test/manual.png?token=abc'
  assert.equal(page.addForm.url, page.addImageURL.value)
  await page.addOCRCache()
  assert.deepEqual(calls, [{ mode: 'direct', tool: 'image_ocr', url: 'https://images.example.test/manual.png?token=abc' }])
  assert.equal(page.addVisible.value, false)
  assert.equal(page.adding.value, false)
  assert.equal(page.addImageURL.value, '')
  assert.equal(page.detailVisible.value, true)
  assert.equal(page.detailRecord.value.url, 'https://images.example.test/manual.png?token=abc')
  assert.equal(listCalls.length, 1)
  const html = await renderPage(page)
  assert.match(html, /手动添加/)
  assert.match(html, /识别并添加/)
  assert.match(html, /计入当前管理员账单/)
})

test('OCR cache manual add retains the URL and explains OCR failure', async () => {
  let calls = 0
  const page = await createPage({ testAIToolCall: async () => { calls++; return { status: 200, data: { success: false, message: '工具调用测试失败：图片 OCR 失败' } } } })
  page.openAddDialog()
  page.addImageURL.value = 'https://images.example.test/manual.png'
  await page.addOCRCache()
  assert.equal(calls, 1)
  assert.equal(page.addVisible.value, true)
  assert.equal(page.addImageURL.value, 'https://images.example.test/manual.png')
  assert.match(page.addError.value, /图片 OCR 失败/)
  assert.equal(page.adding.value, false)
})

test('OCR cache manual add validates before requesting and prevents duplicate submission', { timeout: 5000 }, async () => {
  let finishRequest
  let calls = 0
  const page = await createPage({ testAIToolCall: () => { calls++; return new Promise(resolve => { finishRequest = resolve }) } })
  page.openAddDialog()
  for (const url of ['', 'file:///image.png', 'https://example.com/image.png#fragment', 'https://user:pass@example.com/image.png']) {
    page.addImageURL.value = url
    await page.addOCRCache()
    assert.equal(calls, 0)
    assert.ok(page.addError.value)
  }
  page.addImageURL.value = 'https://images.example.test/manual.png'
  const pending = page.addOCRCache()
  await page.addOCRCache()
  page.closeAddDialog()
  assert.equal(calls, 1)
  assert.equal(page.addVisible.value, true)
  assert.equal(page.adding.value, true)
  finishRequest({ status: 200, data: { success: true, ocrCacheEntry: row } })
  await pending
})

test('OCR recognition alone never reports manual cache addition success', async () => {
  const page = await createPage({ testAIToolCall: async () => ({ status: 200, data: {
    success: true, requestId: 'tool-test-not-saved', ocrCacheEntry: null, ocrCacheWarning: '识别成功，但缓存未保存，请先刷新核对。',
    toolResult: [{ tool: 'image_ocr', text: '实际识别文字' }], billingWarning: '账单已排队',
  } }) })
  page.openAddDialog()
  page.addImageURL.value = 'https://images.example.test/manual.png'
  await page.addOCRCache()
  assert.equal(page.addVisible.value, true)
  assert.match(page.addError.value, /缓存未保存/)
  assert.equal(page.addRequestId.value, 'tool-test-not-saved')
  assert.equal(page.addRecognizedText.value, '实际识别文字')
  assert.equal(page.addBillingWarning.value, '账单已排队')
})

test('OCR cache manual add preserves billing warnings and submitted filters after saving', async () => {
  const scopes = []
  const page = await createPage({
    testAIToolCall: async () => ({ status: 200, data: { success: true, ocrCacheEntry: row, requestId: 'tool-test-saved', billingWarning: '账单已排队' } }),
    getAIOCRCacheEntries: async scope => { scopes.push(scope); return response() },
  })
  page.filters.keyword = '已提交筛选'
  await page.reload()
  page.filters.keyword = '未提交输入'
  page.openAddDialog()
  page.addImageURL.value = row.url
  await page.addOCRCache()
  assert.equal(scopes.at(-1).keyword, '已提交筛选')
  assert.equal(page.filters.keyword, '未提交输入')
  assert.equal(page.addVisible.value, false)
  assert.match(page.operationWarning.value, /账单已排队/)
  assert.match(page.operationWarning.value, /tool-test-saved/)
})

test('OCR cache manual add keeps the original signed URL when the diagnostic response redacts it', async () => {
  const page = await createPage({ testAIToolCall: async () => ({ status: 200, data: { success: true, ocrCacheEntry: { ...row, url: 'https://images.example.test/image.png?token=[REDACTED]' } } }) })
  page.openAddDialog()
  page.addImageURL.value = row.url
  await page.addOCRCache()
  assert.equal(page.detailRecord.value.url, row.url)
  assert.equal(page.detailRecord.value.urlHash, row.urlHash)
})

test('OCR cache manual add retains network uncertainty without retrying the OCR request', async () => {
  let calls = 0
  const page = await createPage({ testAIToolCall: async () => { calls++; throw new Error('网络中断，操作结果未确认。请先刷新核对，勿重复提交。') } })
  page.openAddDialog()
  page.addImageURL.value = row.url
  await page.addOCRCache()
  assert.equal(calls, 1)
  assert.equal(page.addImageURL.value, row.url)
  assert.equal(page.addVisible.value, true)
  assert.match(page.addError.value, /先刷新核对，勿重复提交/)
  assert.equal(page.adding.value, false)
})

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

test('OCR select-all deletion uses displayed filters without pagination', async () => {
  const deleted = []
  const confirmations = []
  const page = await createPage({
    deleteAIOCRCacheBatch: async params => { deleted.push(params); return { status: 200, data: { deleted: 1 } } },
    Swal: { fire: async params => { confirmations.push(params); return { isConfirmed: true } } },
  })
  Object.assign(page.filters, { keyword: '指定题目', contentType: 'image/png', updatedFrom: '2026-11-02', updatedTo: '2026-11-02' })
  await page.reload()
  page.showDetails(row)
  page.handleSelectAll()
  page.filters.keyword = '不该删除的输入'
  await page.deleteSelected()
  assert.deepEqual(deleted, [{ keyword: '指定题目', contentType: 'image/png', updatedFrom: '2026-11-02', updatedTo: '2026-11-02', items: [], all: true, except: [] }])
  assert.match(confirmations[0].title, /所选 OCR 缓存/)
  assert.match(confirmations[0].text, /1 条已选择的缓存/)
  assert.equal(page.detailVisible.value, false)
  assert.equal(page.pagination.current, 1)
  assert.equal(page.deleting.value, false)
})

test('OCR bulk deletion requires selection and cancellation sends no POST', async () => {
  const deleted = []
  let confirmed = false
  const page = await createPage({
    deleteAIOCRCacheBatch: async params => { deleted.push(params); return { status: 200, data: { deleted: 1 } } },
    Swal: { fire: async params => { assert.match(params.title, /所选 OCR 缓存/); return { isConfirmed: confirmed } } },
  })
  await page.load()
  await page.deleteSelected()
  assert.deepEqual(deleted, [])
  page.handleSelectAll()
  await page.deleteSelected()
  assert.deepEqual(deleted, [])
  confirmed = true
  await page.deleteSelected()
  assert.deepEqual(deleted, [{ items: [], all: true, except: [] }])
})

test('OCR explicit selection remains selected across pages and deletes only selected hashes', async () => {
  const deleted = []
  const secondRow = { ...row, urlHash: 'b'.repeat(64), ocrText: '第二条' }
  const thirdRow = { ...row, urlHash: 'c'.repeat(64), ocrText: '第三条' }
  const page = await createPage({
    getAIOCRCacheEntries: async params => params.page === 2
      ? response({ data: [thirdRow], total: 3, summary: { matched: 3 } })
      : response({ data: [row, secondRow], total: 3, summary: { matched: 3 } }),
    deleteAIOCRCacheBatch: async params => { deleted.push(params); return { status: 200, data: { deleted: 2 } } },
  })
  await page.load()
  page.onSelectionChange([row.urlHash])
  await page.onTableChange({ current: 1, pageSize: 1 })
  await page.onTableChange({ current: 2, pageSize: 1 })
  page.onSelectionChange([thirdRow.urlHash])
  assert.equal(page.selectedCount.value, 2)
  await page.deleteSelected()
  assert.deepEqual(deleted, [{ items: [row.urlHash, thirdRow.urlHash], all: false, except: [] }])
  assert.equal(page.selectedCount.value, 0)
})

test('OCR select-all uses the filtered scope and transmits deselected exceptions', async () => {
  const deleted = []
  const secondRow = { ...row, urlHash: 'b'.repeat(64), ocrText: '第二条' }
  const page = await createPage({
    getAIOCRCacheEntries: async () => response({ data: [row, secondRow], total: 60, summary: { matched: 60 } }),
    deleteAIOCRCacheBatch: async params => { deleted.push(params); return { status: 200, data: { deleted: 59 } } },
  })
  page.filters.keyword = '题目'
  await page.reload()
  page.handleSelectAll()
  page.onSelectionChange([row.urlHash])
  assert.equal(page.selectedCount.value, 59)
  await page.deleteSelected()
  assert.deepEqual(deleted, [{ keyword: '题目', items: [], all: true, except: [secondRow.urlHash] }])
})

test('OCR row deletion sends only the row hash and clears its detail', async () => {
  const deleted = []
  const page = await createPage({
    deleteAIOCRCacheBatch: async params => { deleted.push(params); return { status: 200, data: { deleted: 1 } } },
  })
  await page.load()
  page.showDetails(row)
  await page.deleteSingle(row)
  assert.deepEqual(deleted, [{ items: [row.urlHash], all: false, except: [] }])
  assert.equal(page.detailVisible.value, false)
  assert.equal(page.deleting.value, false)
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
    deleteAIOCRCacheBatch: () => { throw new Error('列表失败不应删除') },
  })
  await page.load()
  fail = true
  page.filters.keyword = '下一次查询'
  await page.reload()
  assert.equal(page.errorMessage.value, '缓存数据库暂不可用')
  assert.deepEqual(page.rows.value, [])
  assert.equal(page.matched.value, 0)
  await page.deleteSelected()
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
  assert.match(api, /export function getAIOCRCacheImage\(urlHash\)[\s\S]*?responseType: 'blob'/)
  assert.match(api, /ocr-cache\/entries\/\$\{encodeURIComponent\(urlHash\)\}\/image/)
  assert.match(api, /export function deleteAIOCRCacheBatch\(data\)[\s\S]*?ocr-cache\/batch-delete/)
  assert.match(page, /a-form :model="filters"/)
  assert.match(page, /:row-selection="rowSelection"/)
  assert.doesNotMatch(page, /v-html/)
})
