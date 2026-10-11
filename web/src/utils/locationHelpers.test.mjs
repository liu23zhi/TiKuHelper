import assert from 'node:assert/strict'
import test from 'node:test'

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
