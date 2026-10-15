import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'
import * as locationHelpers from './locationHelpers.mjs'

import {
  buildLocationLookupParams,
  collectLocationKeys,
  extractLocationAddresses,
  formatValueWithLocation,
} from './locationHelpers.mjs'

test('formatValueWithLocation renders the documented phone shape', () => {
  assert.equal(
    formatValueWithLocation('18287719599', '云南玉溪 移动'),
    '18287719599 （云南玉溪 移动）',
  )
})

test('formatValueWithLocation renders the ip shape', () => {
  assert.equal(
    formatValueWithLocation('1.2.3.4', '广东深圳 电信'),
    '1.2.3.4 （广东深圳 电信）',
  )
})

test('formatValueWithLocation omits the parentheses when there is no location', () => {
  // 未配置密钥、查询失败、内网 IP 都走这条路径：只显示原始值，不留空括号。
  assert.equal(formatValueWithLocation('18287719599', ''), '18287719599')
  assert.equal(formatValueWithLocation('18287719599', null), '18287719599')
  assert.equal(formatValueWithLocation('18287719599', '   '), '18287719599')
})

test('formatValueWithLocation returns empty for missing raw values', () => {
  assert.equal(formatValueWithLocation('', '云南玉溪 移动'), '')
  assert.equal(formatValueWithLocation(null, '云南玉溪 移动'), '')
  assert.equal(formatValueWithLocation(undefined, undefined), '')
})

test('formatValueWithLocation trims both sides', () => {
  assert.equal(
    formatValueWithLocation('  18287719599  ', '  云南玉溪 移动  '),
    '18287719599 （云南玉溪 移动）',
  )
})

test('extractLocationAddresses reads the nested data map', () => {
  const addresses = extractLocationAddresses({
    data: {
      '18287719599': { address: '云南玉溪 移动', province: '云南' },
      '1.2.3.4': { address: '广东深圳 电信' },
    },
  })

  assert.deepEqual(addresses, {
    '18287719599': '云南玉溪 移动',
    '1.2.3.4': '广东深圳 电信',
  })
})

test('extractLocationAddresses tolerates malformed payloads', () => {
  assert.deepEqual(extractLocationAddresses(undefined), {})
  assert.deepEqual(extractLocationAddresses(null), {})
  assert.deepEqual(extractLocationAddresses({}), {})
  assert.deepEqual(extractLocationAddresses({ data: null }), {})
  assert.deepEqual(extractLocationAddresses({ data: [] }), {})
  // 空 address 不应产出条目，否则前端会显示空括号。
  assert.deepEqual(extractLocationAddresses({ data: { a: { address: '  ' } } }), {})
})

test('extractLocationAddresses reads axios-wrapped and personal-location responses', () => {
  assert.deepEqual(extractLocationAddresses({ status: 200, data: { data: { ' 2001:DB8::1 ': { address: '上海' } } } }), { '2001:db8::1': '上海' })
  assert.deepEqual(extractLocationAddresses({ status: 200, data: { ip: { '8.8.8.8': { address: '美国' } } } }, 'ip'), { '8.8.8.8': '美国' })
})

test('collectLocationKeys normalizes and combines multiple IP columns before deduplicating', () => {
  assert.deepEqual(collectLocationKeys([
    { lastLoginIp: ' 2001:DB8::1 ', registerIp: '2001:db8::1' },
    { lastLoginIp: '8.8.8.8', registerIp: '1.1.1.1' },
  ], ['lastLoginIp', 'registerIp']), ['2001:db8::1', '8.8.8.8', '1.1.1.1'])
})

test('lookupLocationAddresses splits at 100 and keeps successful chunks when one fails', async () => {
  assert.equal(typeof locationHelpers.lookupLocationAddresses, 'function')
  const requests = []
  const keys = Array.from({ length: 201 }, (_, index) => `2001:DB8::${index}`)
  const addresses = await locationHelpers.lookupLocationAddresses('ip', [...keys, keys[0]], async (params) => {
    const batch = params.ips.split(',')
    requests.push(batch)
    if (requests.length === 2) throw new Error('临时网络故障')
    return { status: 200, data: { data: Object.fromEntries(batch.map((key) => [key, { address: '上海' }])) } }
  })
  assert.deepEqual(requests.map((batch) => batch.length), [100, 100, 1])
  assert.equal(Object.keys(addresses).length, 101)
  assert.equal(addresses['2001:db8::0'], '上海')
  assert.equal(addresses['2001:db8::200'], '上海')
})

test('lookupLocationAddresses reads personal IP maps and does not request an empty batch', async () => {
  assert.equal(typeof locationHelpers.lookupLocationAddresses, 'function')
  assert.deepEqual(await locationHelpers.lookupLocationAddresses('ip', [], () => { throw new Error('不应调用') }), {})
  assert.deepEqual(await locationHelpers.lookupLocationAddresses('ip', ['8.8.8.8'], async (params) => {
    assert.deepEqual(params, { ips: '8.8.8.8' })
    return { status: 200, data: { ip: { '8.8.8.8': { address: '美国' } } } }
  }, 'ip'), { '8.8.8.8': '美国' })
})

test('location management has separate protected routes and refresh/raw response controls', async () => {
  const page = await readFile(new URL('../pages/LocationCache/index.vue', import.meta.url), 'utf8')
  const router = await readFile(new URL('../router/router.js', import.meta.url), 'utf8')
  const menu = await readFile(new URL('../components/SiderMenu/index.vue', import.meta.url), 'utf8')
  const api = await readFile(new URL('../api/TikuComponentApi.js', import.meta.url), 'utf8')
  assert.match(page, /route\.meta\.locationKind/)
  assert.match(page, /refreshLocationCache/)
  assert.match(page, /rawJson/)
  assert.match(page, /configured/)
  assert.match(router, /name: 'ip-location-cache'/)
  assert.match(router, /name: 'phone-location-cache'/)
  assert.match(menu, /IP 归属地信息管理/)
  assert.match(menu, /手机号归属地信息管理/)
  assert.match(api, /export function getMyLocations\(params/)
  assert.match(api, /\/location-cache\/refresh/)
})

test('collectLocationKeys deduplicates and skips empty values', () => {
  const keys = collectLocationKeys(
    [
      { phone: '13800138000' },
      { phone: '13800138000' },
      { phone: ' 13900139000 ' },
      { phone: '' },
      { phone: null },
      {},
    ],
    'phone',
  )

  assert.deepEqual(keys, ['13800138000', '13900139000'])
})

test('collectLocationKeys tolerates non-array input', () => {
  assert.deepEqual(collectLocationKeys(undefined, 'phone'), [])
  assert.deepEqual(collectLocationKeys([], 'phone'), [])
  assert.deepEqual(collectLocationKeys([{ phone: 'x' }], ''), [])
})

test('buildLocationLookupParams builds single-value-free bulk params', () => {
  assert.deepEqual(buildLocationLookupParams('phone', ['13800138000']), { phones: '13800138000' })
  assert.deepEqual(buildLocationLookupParams('phone', ['1', '2']), { phones: '1,2' })
  assert.deepEqual(buildLocationLookupParams('ip', ['8.8.8.8']), { ips: '8.8.8.8' })
  assert.equal(buildLocationLookupParams('phone', []), null)
  assert.equal(buildLocationLookupParams('phone', undefined), null)
})
