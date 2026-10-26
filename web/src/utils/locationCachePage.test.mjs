import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { computed, defineComponent, reactive, ref } from 'vue'

async function createPage(overrides = {}) {
  const source = await readFile(new URL('../pages/LocationCache/index.vue', import.meta.url), 'utf8')
  const script = source.match(/<script>([\s\S]*?)<\/script>/)[1]
    .replace(/^import .*$/gm, '')
    .replace('export default defineComponent', 'return defineComponent')
  const dependencies = {
    computed, defineComponent, reactive, ref,
    watch: () => {},
    globalThis: { localStorage: undefined },
    useRoute: () => ({ meta: { locationKind: 'ip' } }),
    message: { success() {} },
    Swal: { fire: async () => ({ isConfirmed: true }) },
    ReloadOutlined: {},
    deleteLocationCache: async () => ({ status: 200 }),
    deleteLocationCacheBatch: async () => ({ status: 200, data: { deleted: 0 } }),
    refreshLocationCacheBatch: async () => ({ status: 200, data: { configured: true, data: { requested: 0, refreshed: 0, failed: 0, failures: [] } } }),
    getLocationCacheList: async () => ({ status: 200, data: { data: [], configured: true } }),
    manualLookupLocationCache: async () => ({ status: 200, data: { configured: true } }),
    refreshLocationCache: async () => ({ status: 200, data: { configured: true } }),
    ...overrides,
  }
  // Default fixture models an already confirmed configured page. Individual key/loading
  // cases explicitly set false/undefined; production actions stay disabled until true.
  const page = new Function(...Object.keys(dependencies), script)(...Object.values(dependencies)).setup()
  page.configured.value = true
  return page
}

for (const [status, configured, reason] of [
  [502, true, 'UApiPro 密钥无效或已过期，请检查系统设置'],
  [503, false, '尚未配置 UApiPro API 密钥'],
]) {
  test(`location refresh preserves the API reason and configuration from rejected HTTP ${status}`, async () => {
    const page = await createPage({
      refreshLocationCache: async () => {
        throw Object.assign(new Error(`Request failed with status code ${status}`), {
          response: { status, data: { message: reason, configured } },
        })
      },
    })
    await page.refresh({ kind: 'ip', lookupKey: '8.8.8.8', source: '' })
    assert.equal(page.errorMessage.value, `8.8.8.8：${reason}`)
    assert.equal(page.configured.value, configured)
  })
}

test('location refresh retains a useful network error without an HTTP response', async () => {
  const page = await createPage({ refreshLocationCache: async () => { throw new Error('Network Error') } })
  await page.refresh({ kind: 'ip', lookupKey: '8.8.8.8', source: '' })
  assert.equal(page.errorMessage.value, '8.8.8.8：Network Error')
})

for (const [action, requestName, stateName] of [
  ['refresh', 'refreshLocationCache', 'refreshing'],
  ['remove', 'deleteLocationCache', 'removing'],
]) {
  test(`location ${action} tracks default and commercial rows independently`, async () => {
    const pending = new Map()
    const page = await createPage({
      [requestName]: (input) => new Promise(resolve => pending.set(input.source, resolve)),
    })
    const defaultRow = { kind: 'ip', lookupKey: '8.8.8.8', source: '' }
    const commercialRow = { kind: 'ip', lookupKey: '8.8.8.8', source: 'commercial' }
    const defaultTask = page[action](defaultRow)
    if (action === 'refresh') page.setQuerySource('commercial')
    const commercialTask = page[action](commercialRow)
    assert.equal(page[stateName]['ip:8.8.8.8:default'], true)
    assert.equal(page[stateName]['ip:8.8.8.8:commercial'], true)
    pending.get('')({ status: 200, data: { configured: true } })
    await defaultTask
    assert.equal(page[stateName]['ip:8.8.8.8:default'], false)
    assert.equal(page[stateName]['ip:8.8.8.8:commercial'], true)
    pending.get('commercial')({ status: 200, data: { configured: true } })
    await commercialTask
    assert.equal(page[stateName]['ip:8.8.8.8:commercial'], false)
  })
}

test('legacy IP rows disclose their unknown source', async () => {
  const page = await createPage()
  const row = { kind: 'ip', lookupKey: '8.8.8.8', source: 'legacy' }
  assert.equal(page.columns.value.find(column => column.key === 'source').customRender({ record: row }), '历史（来源未记录）')
})

for (const querySource of ['', 'commercial']) {
  test(`single refresh uses setting ${querySource || 'ordinary'} for every stored source`, async () => {
    const inputs = []
    const page = await createPage({
      refreshLocationCache: async input => { inputs.push(input); return { status: 200 } },
    })
    page.setQuerySource(querySource)
    for (const source of ['', 'commercial', 'legacy']) {
      const row = { kind: 'ip', lookupKey: '8.8.8.8', source }
      assert.equal(page.isRefreshDisabled(row), false)
      await page.refresh(row)
    }
    assert.deepEqual(inputs, Array(3).fill({ kind: 'ip', lookupKey: '8.8.8.8', source: querySource }))
  })
}

test('query source defaults to ordinary and persists a valid browser preference', async () => {
  const values = new Map()
  const globalThis = { localStorage: { getItem: key => values.get(key), setItem: (key, value) => values.set(key, value) } }
  const page = await createPage({ globalThis })
  assert.equal(page.querySource.value, '')
  page.setQuerySource('commercial')
  const reloaded = await createPage({ globalThis })
  assert.equal(reloaded.querySource.value, 'commercial')
  reloaded.setQuerySource('legacy')
  assert.equal(reloaded.querySource.value, '')
  assert.equal((await createPage({ globalThis })).querySource.value, '')
})

test('missing key keeps cache read-only and disables all IP query actions', async () => {
  let input
  const page = await createPage({
    refreshLocationCache: async value => { input = value; return { status: 200 } },
    getLocationCacheList: async () => ({ status: 200, data: { data: [], configured: false } }),
  })
  page.setQuerySource('commercial')
  await page.load()
  assert.equal(page.querySource.value, '')
  page.setQuerySource('commercial')
  assert.equal(page.querySource.value, '')
  assert.equal(page.isRefreshDisabled(), true)
  await page.refresh({ kind: 'ip', lookupKey: '8.8.8.8', source: 'commercial' })
  assert.equal(input, undefined)
})

test('batch refresh applies the chosen source without changing all-page exceptions', async () => {
  let payload
  let confirmation
  const page = await createPage({
    Swal: { fire: async options => { confirmation = options; return { isConfirmed: true } } },
    refreshLocationCacheBatch: async input => { payload = input; return { status: 200 } },
  })
  page.setQuerySource('commercial')
  page.rows.value = [{ kind: 'ip', lookupKey: '8.8.8.8', source: 'legacy' }]
  page.pagination.total = 3
  page.handleSelectAll()
  page.handleClearCurrentPage()
  await page.refreshSelected()
  assert.deepEqual(payload, {
    kind: 'ip', keyword: '', source: '', items: [], all: true,
    except: [{ lookupKey: '8.8.8.8', source: 'legacy' }], querySource: 'commercial',
  })
  assert.match(confirmation.text, /按商业增强重新查询 2 条/)
})

test('missing key blocks batch refresh; configured cancellation still sends nothing', async () => {
  const calls = []
  let confirmed = false
  const page = await createPage({
    Swal: { fire: async () => ({ isConfirmed: confirmed }) },
    refreshLocationCacheBatch: async input => { calls.push(input); return { status: 200 } },
    getLocationCacheList: async () => ({ status: 200, data: { data: [], configured: false } }),
  })
  await page.load()
  page.rows.value = [{ kind: 'ip', lookupKey: '8.8.8.8', source: 'commercial' }]
  page.pagination.total = 1
  page.handleSelectCurrentPage()
  await page.refreshSelected()
  assert.equal(calls.length, 0)
  confirmed = true
  await page.refreshSelected()
  assert.equal(calls.length, 0)
  page.configured.value = true
  confirmed = false
  await page.refreshSelected()
  assert.equal(calls.length, 0)
  confirmed = true
  await page.refreshSelected()
  assert.equal(calls.length, 1)
  assert.equal(calls[0].querySource, '')
  assert.equal(calls[0].items[0].source, 'commercial')
})

test('phone query actions require a key and never send the IP source setting', async () => {
  const calls = []; const batches = []; const manual = []
  const page = await createPage({
    useRoute: () => ({ meta: { locationKind: 'phone' } }),
    refreshLocationCache: async input => { calls.push(input); return { status: 200 } },
    refreshLocationCacheBatch: async input => { batches.push(input); return { status: 200 } },
    manualLookupLocationCache: async input => { manual.push(input); return { status: 200 } },
  })
  page.configured.value = true
  page.setQuerySource('commercial')
  const row = { kind: 'phone', lookupKey: '13800138000', source: '' }
  await page.refresh(row)
  assert.deepEqual(calls, [{ kind: 'phone', lookupKey: '13800138000', source: '' }])
  page.configured.value = false
  assert.equal(page.isRefreshDisabled(row), true)
  await page.refresh(row)
  assert.equal(calls.length, 1)
  page.rows.value = [row]; page.pagination.total = 1; page.handleSelectCurrentPage()
  await page.refreshSelected()
  page.manualKey.value = row.lookupKey
  await page.manualLookup()
  assert.equal(batches.length, 0); assert.equal(manual.length, 0)
  page.configured.value = true
  await page.refreshSelected()
  assert.equal(batches.length, 1); assert.equal(batches[0].querySource, '')
  page.manualKey.value = row.lookupKey
  await page.manualLookup()
  assert.deepEqual(manual, [{ kind: 'phone', lookupKey: row.lookupKey, source: '' }])
})

test('selected rows use the batch refresh endpoint and include legacy rows', async () => {
  let payload
  const page = await createPage({
    refreshLocationCacheBatch: async (input) => {
      payload = input
      return { status: 200, data: { configured: true, data: { requested: 2, refreshed: 2, failed: 0, failures: [] } } }
    },
  })
  page.rows.value = [
    { kind: 'ip', lookupKey: '8.8.8.8', source: 'legacy' },
    { kind: 'ip', lookupKey: '1.1.1.1', source: '' },
  ]
  page.pagination.total = 2
  page.handleSelectCurrentPage()
  await page.refreshSelected()
  assert.deepEqual(payload, {
    kind: 'ip',
    keyword: '',
    source: '',
    items: [
      { lookupKey: '8.8.8.8', source: 'legacy' },
      { lookupKey: '1.1.1.1', source: '' },
    ],
    all: false,
    except: [],
    querySource: '',
  })
  assert.equal(page.selectedCount.value, 0)
})

test('batch refresh keeps partial failure details after reloading the list', async () => {
  const page = await createPage({
    refreshLocationCacheBatch: async () => ({
      status: 200,
      data: { configured: true, data: { requested: 2, refreshed: 1, failed: 1, failures: [{ lookupKey: '8.8.8.8', message: '上游失败' }] } },
    }),
    getLocationCacheList: async () => ({ status: 200, data: { data: [], total: 0, configured: true } }),
  })
  page.rows.value = [{ kind: 'ip', lookupKey: '8.8.8.8', source: '' }]
  page.pagination.total = 1
  page.handleSelectCurrentPage()
  await page.refreshSelected()
  assert.equal(page.errorMessage.value, '已成功重新查询 1 条，1 条失败：8.8.8.8：上游失败')
})

test('legacy IP rows remain deletable with their stored source', async () => {
  let deleted
  const page = await createPage({
    deleteLocationCache: async (input) => {
      deleted = input
      return { status: 200 }
    },
  })
  await page.remove({ kind: 'ip', lookupKey: '8.8.8.8', source: 'legacy' })
  assert.deepEqual(deleted, { kind: 'ip', lookupKey: '8.8.8.8', source: 'legacy' })
})

test('location cache reload sends the active filter and server page', async () => {
  const calls = []
  const page = await createPage({
    getLocationCacheList: async (params) => {
      calls.push(params)
      return {
        status: 200,
        data: {
          data: [],
          total: 42,
          page: 2,
          pageSize: 50,
          configured: true,
          summary: { total: 42, withAddress: 40, withoutAddress: 2 },
        },
      }
    },
  })
  page.keywordInput.value = '  上海  '
  page.pagination.current = 2
  page.pagination.pageSize = 50
  page.reload()
  await new Promise(resolve => setTimeout(resolve, 0))
  assert.deepEqual(calls, [{ kind: 'ip', keyword: '上海', page: 1, pageSize: 50 }])
  assert.deepEqual(page.summary.value, { total: 42, withAddress: 40, withoutAddress: 2 })
})

test('location cache selection supports current page, all pages, and page exceptions', async () => {
  const page = await createPage()
  page.rows.value = [
    { kind: 'ip', lookupKey: '10.0.0.1', source: '' },
    { kind: 'ip', lookupKey: '10.0.0.2', source: '' },
  ]
  page.pagination.total = 5

  page.handleSelectCurrentPage()
  assert.deepEqual(page.selectedRowKeys.value, ['ip:10.0.0.1:default', 'ip:10.0.0.2:default'])
  assert.equal(page.selectedCount.value, 2)

  page.handleSelectAll()
  assert.equal(page.allRowsSelected.value, true)
  assert.equal(page.selectedCount.value, 5)

  page.handleClearCurrentPage()
  assert.deepEqual(page.selectedRowKeys.value, [])
  assert.equal(page.selectedCount.value, 3)

  page.handleSelectCurrentPage()
  assert.deepEqual(page.selectedRowKeys.value, ['ip:10.0.0.1:default', 'ip:10.0.0.2:default'])
  assert.equal(page.selectedCount.value, 5)

  page.handleClearSelection()
  assert.equal(page.allRowsSelected.value, false)
  assert.equal(page.selectedCount.value, 0)
})

test('location cache template exposes bulk selection actions and summary', async () => {
  const page = await readFile(new URL('../pages/LocationCache/index.vue', import.meta.url), 'utf8')
  assert.match(page, /row-selection/)
  assert.match(page, /全选本页/)
  assert.match(page, /全选全部/)
  assert.match(page, /取消本页全选/)
  assert.match(page, /取消全部全选/)
  assert.match(page, /withAddress/)
  assert.match(page, /withoutAddress/)
  assert.match(page, /手动添加/)
  assert.match(page, /全部删除/)
  assert.match(page, /manualLookupLocationCache/)
  assert.match(page, /label="查询来源"/)
  assert.match(page, /value="">普通来源/)
  assert.match(page, /value="commercial" :disabled="configured !== true"/)
})

test('location cache manual lookup posts the active kind and persists result', async () => {
  let input
  const page = await createPage({
    manualLookupLocationCache: async (value) => {
      input = value
      return { status: 200, data: { configured: true } }
    },
  })
  page.manualKey.value = ' 8.8.8.8 '
  page.setQuerySource('commercial')
  await page.manualLookup()
  assert.deepEqual(input, { kind: 'ip', lookupKey: '8.8.8.8', source: 'commercial' })
  assert.equal(page.manualVisible.value, false)
})

test('location cache quick delete scopes all=true to the active kind', async () => {
  let input
  const page = await createPage({
    deleteLocationCacheBatch: async (value) => {
      input = value
      return { status: 200, data: { deleted: 2 } }
    },
  })
  await page.deleteAll()
  assert.deepEqual(input, { kind: 'ip', keyword: '', source: '', items: [], all: true, except: [] })
})

test('location cache deletes explicit cross-page selections through the batch API', async () => {
  let payload
  const page = await createPage({
    deleteLocationCacheBatch: async (input) => {
      payload = input
      return { status: 200, data: { deleted: 2 } }
    },
  })
  page.rows.value = [
    { kind: 'ip', lookupKey: '2001:db8::1', source: '' },
    { kind: 'ip', lookupKey: '8.8.8.8', source: 'commercial' },
  ]
  page.pagination.total = 2
  page.handleSelectCurrentPage()
  await page.deleteSelected()
  assert.deepEqual(payload, {
    kind: 'ip',
    keyword: '',
    source: '',
    items: [
      { lookupKey: '2001:db8::1', source: '' },
      { lookupKey: '8.8.8.8', source: 'commercial' },
    ],
    all: false,
    except: [],
  })
  assert.equal(page.selectedCount.value, 0)
})

test('location cache sends filter scope and exceptions when deleting all pages', async () => {
  let payload
  const page = await createPage({
    deleteLocationCacheBatch: async (input) => {
      payload = input
      return { status: 200, data: { deleted: 3 } }
    },
  })
  page.rows.value = [
    { kind: 'ip', lookupKey: '10.0.0.1', source: '' },
    { kind: 'ip', lookupKey: '10.0.0.2', source: '' },
  ]
  page.pagination.total = 5
  page.handleSelectAll()
  page.handleClearCurrentPage()
  await page.deleteSelected()
  assert.deepEqual(payload, {
    kind: 'ip',
    keyword: '',
    source: '',
    items: [],
    all: true,
    except: [
      { lookupKey: '10.0.0.1', source: '' },
      { lookupKey: '10.0.0.2', source: '' },
    ],
  })
  assert.equal(page.selectedCount.value, 0)
})

test('missing-key status message never promises visitor lookups', async () => {
  const source = await readFile(new URL('../pages/LocationCache/index.vue', import.meta.url), 'utf8')
  assert.match(source, /未配置 UApiPro 密钥，仅可查看已有缓存，不能重新查询或手动添加/)
  assert.doesNotMatch(source, /仍可查询手机号|仅可使用普通来源/)
})

test('unknown configuration cannot start a query before the key status is known', async () => {
  let calls = 0
  const page = await createPage({ refreshLocationCache: async () => { calls++; return { status: 200 } } })
  page.configured.value = undefined
  assert.equal(page.isRefreshDisabled(), true)
  await page.refresh({ kind: 'ip', lookupKey: '8.8.8.8', source: '' })
  assert.equal(calls, 0)
})
