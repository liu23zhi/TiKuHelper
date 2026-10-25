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
  return new Function(...Object.keys(dependencies), script)(...Object.values(dependencies)).setup()
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

test('legacy IP rows cannot issue refresh queries', async () => {
  let refreshCalls = 0
  const page = await createPage({
    refreshLocationCache: async () => {
      refreshCalls++
      return { status: 200, data: { configured: true } }
    },
  })
  const row = { kind: 'ip', lookupKey: '8.8.8.8', source: 'legacy' }
  await page.refresh(row)
  assert.equal(refreshCalls, 0)
  assert.equal(Object.keys(page.refreshing).length, 0)
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
  page.manualSource.value = 'commercial'
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
