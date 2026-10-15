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
    ReloadOutlined: {},
    deleteLocationCache: async () => ({ status: 200 }),
    getLocationCacheList: async () => ({ status: 200, data: { data: [], configured: true } }),
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
