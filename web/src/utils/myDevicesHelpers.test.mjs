import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  DEVICE_STATUS_META,
  deviceLimitSourceText,
  deviceStatusMeta,
  describeLastSeen,
  emptyDeviceLimitState,
  emptyDeviceListState,
  maskDeviceId,
  normalizeDeviceItem,
  normalizeDeviceLimit,
  normalizeDeviceList,
  readInt,
  validateDeviceLimitInput,
} from './myDevicesHelpers.mjs'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
const srcDir = path.join(__dirname, '..')

async function readSource(relativePath) {
  return readFile(path.join(srcDir, relativePath), 'utf8')
}

// 剥注释：对 .vue/.js 做「不得包含 X」的断言前必须先剥，否则注释里的
// 反例说明会被误判成「又写回去了」（本仓已因此交付过一次假红）。
function stripComments(source) {
  return String(source).replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/[^\n]*/g, '')
}

// ─────────────────────── 上限校验（与后端 400 文案同口径） ───────────────────────

test('validateDeviceLimitInput 拒绝超过等级值的输入，文案与后端一字不差', () => {
  const view = { hasUpperBound: true, upperBound: 3, limit: 3 }
  const result = validateDeviceLimitInput('5', view)
  assert.equal(result.ok, false)
  assert.equal(result.error, 'above-level')
  // 后端 controller/device_user.go 的 400 文案：字面量比对，不引用任何常量。
  assert.equal(result.message, '不能超过当前等级上限 3 台')
})

test('validateDeviceLimitInput 允许 0 与等于上界的值', () => {
  const view = { hasUpperBound: true, upperBound: 3 }
  for (const input of ['0', '1', '3', 0, 3]) {
    const result = validateDeviceLimitInput(input, view)
    assert.equal(result.ok, true, `输入 ${input} 应被允许`)
  }
  assert.equal(validateDeviceLimitInput('3', view).value, 3)
})

test('validateDeviceLimitInput 等级不限（无 upperBound）时放行任意正整数', () => {
  const view = { hasUpperBound: false, upperBound: null }
  const result = validateDeviceLimitInput('10', view)
  assert.equal(result.ok, true)
  assert.equal(result.value, 10)
  // 100 台也不该被前端拦（等级不限 → 只要求 N>=0）
  assert.equal(validateDeviceLimitInput('100', view).ok, true)
})

test('validateDeviceLimitInput 拒绝空值/负数/小数/非数字', () => {
  const view = { hasUpperBound: true, upperBound: 3 }
  assert.equal(validateDeviceLimitInput('', view).error, 'required')
  assert.equal(validateDeviceLimitInput('   ', view).error, 'required')
  assert.equal(validateDeviceLimitInput(null, view).error, 'required')
  // 负数：后端也返回 400「设备上限不能为负数」，这里前端先拦（文案可以不同，
  // 但必须**不 ok** —— 否则用户会提交一个必然 400 的请求）。
  assert.equal(validateDeviceLimitInput('-1', view).ok, false)
  assert.equal(validateDeviceLimitInput('1.5', view).error, 'not-integer')
  assert.equal(validateDeviceLimitInput('abc', view).error, 'not-integer')
})

test('validateDeviceLimitInput 的上界必须来自 upperBound 而不是当前生效值', () => {
  // 形态：等级 3 台，用户已自设 1 台。若实现拿 limit(=1) 当上界，
  // 用户就再也改不回 3 台 —— 一个永久性的自我降级锁。
  const view = { hasUpperBound: true, upperBound: 3, limit: 1 }
  assert.equal(validateDeviceLimitInput('3', view).ok, true)
})

// ─────────────────────── 上限视图归一化 ───────────────────────

test('normalizeDeviceLimit 区分「无上界」与「未知」', () => {
  const unlimited = normalizeDeviceLimit({
    limit: 0, source: 'unlimited', unlimited: true,
    override: -1, levelLimit: 0, upperBound: null, editable: true,
  })
  assert.equal(unlimited.unlimited, true)
  assert.equal(unlimited.hasUpperBound, false)
  assert.equal(unlimited.upperBound, null)
  assert.equal(unlimited.sourceText, '等级不限')

  const bounded = normalizeDeviceLimit({
    limit: 2, source: 'user_override', unlimited: false,
    override: 2, levelLimit: 3, upperBound: 3, editable: true,
  })
  assert.equal(bounded.hasUpperBound, true)
  assert.equal(bounded.upperBound, 3)
  assert.equal(bounded.sourceText, '你自己设置的')
})

test('normalizeDeviceLimit 对缺失字段给显式未知态，不伪造 0', () => {
  const state = normalizeDeviceLimit({})
  assert.equal(state.limit, null)
  assert.equal(state.limitKnown, false)
  assert.equal(state.override, null)
  assert.equal(state.levelLimit, null)
  assert.equal(state.hasUpperBound, false)
  // 空对象不是「读到 0 台」
  assert.notEqual(state.limit, 0)

  const empty = emptyDeviceLimitState()
  assert.equal(empty.loaded, false)
  assert.equal(empty.limitKnown, false)
})

test('deviceLimitSourceText 对未知来源说「未知」而不是「不限」', () => {
  assert.equal(deviceLimitSourceText('user_override'), '你自己设置的')
  assert.equal(deviceLimitSourceText('level'), '等级权益')
  assert.equal(deviceLimitSourceText('level_missing'), '未评级（暂不限制）')
  assert.equal(deviceLimitSourceText('unlimited'), '等级不限')
  assert.equal(deviceLimitSourceText(''), '未知')
  assert.equal(deviceLimitSourceText('stub'), '未知')
})

// ─────────────────────── 设备列表归一化 ───────────────────────

test('normalizeDeviceList 保留三态状态并映射按钮可用性', () => {
  const state = normalizeDeviceList({
    devices: [
      { id: 1, deviceId: 'extension:a', deviceLabel: '在用', status: 'active', removable: true },
      { id: 2, deviceId: 'extension:b', deviceLabel: '已移除', status: 'removed', revokedReason: 'user_removed' },
      { id: 3, deviceId: 'extension:c', deviceLabel: '已拉黑', status: 'blocked', blocked: true, blockId: 7 },
      { deviceId: 'extension:d', status: 'blocked', blocked: true, blockId: 8, blockOnly: true },
    ],
    total: 4, active: 1, blocked: 2, schemaReady: true,
  })
  assert.equal(state.devices.length, 4)
  assert.equal(state.devices[0].statusText, '使用中')
  assert.equal(state.devices[0].removable, true)
  assert.equal(state.devices[1].statusText, '已移除')
  assert.equal(state.devices[1].revokedReason, 'user_removed')
  assert.equal(state.devices[2].statusText, '已拉黑')
  assert.equal(state.devices[2].blockId, 7)
  assert.equal(state.devices[3].blockOnly, true)
  // blockOnly 行的 id 是 0（没有绑定行），但 blockId 必须可用 —— 否则无法解除。
  assert.equal(state.devices[3].id, 0)
  assert.equal(state.devices[3].blockId, 8)
  assert.equal(state.active, 1)
  assert.equal(state.blocked, 2)
})

test('normalizeDeviceList 不把「读取失败」伪装成「0 台设备」', () => {
  const empty = emptyDeviceListState()
  assert.equal(empty.loaded, false)
  const fallback = normalizeDeviceList(null)
  assert.deepEqual(fallback.devices, [])
  assert.equal(fallback.loaded, true)
})

test('normalizeDeviceItem 对 blockId<=0 的拉黑行给出 0（不能用 0 去 DELETE）', () => {
  const item = normalizeDeviceItem({ deviceId: 'extension:x', status: 'blocked', blocked: true, blockId: 0 })
  assert.equal(item.blockId, 0)
  const missing = normalizeDeviceItem({ deviceId: 'extension:y', status: 'blocked' })
  assert.equal(missing.blockId, 0)
})

test('deviceStatusMeta 与 DEVICE_STATUS_META 覆盖三态且未知状态不猜', () => {
  // 期望值写**字面量**：三个 key 与三段文案。用 Object.keys(DEVICE_STATUS_META)
  // 做期望会让「状态枚举被删掉一个」永远绿。
  assert.deepEqual(Object.keys(DEVICE_STATUS_META).sort(), ['active', 'blocked', 'removed'])
  assert.equal(DEVICE_STATUS_META.active.text, '使用中')
  assert.equal(DEVICE_STATUS_META.removed.text, '已移除')
  assert.equal(DEVICE_STATUS_META.blocked.text, '已拉黑')
  assert.equal(deviceStatusMeta('nonsense').text, '未知')
  assert.equal(deviceStatusMeta('').text, '未知')
})

// ─────────────────────── 文案与工具 ───────────────────────

test('describeLastSeen 三态：无时间/无法解析/正常相对时间', () => {
  const now = new Date('2026-11-15T00:00:00Z')
  assert.equal(describeLastSeen('', now), '从未活跃')
  assert.equal(describeLastSeen(null, now), '从未活跃')
  assert.equal(describeLastSeen('not-a-date', now), '未知时间')
  assert.equal(describeLastSeen('2026-11-14T23:59:30Z', now), '刚刚')
  assert.equal(describeLastSeen('2026-11-14T23:30:00Z', now), '30 分钟前')
  assert.equal(describeLastSeen('2026-11-14T20:00:00Z', now), '4 小时前')
  assert.equal(describeLastSeen('2026-11-05T00:00:00Z', now), '10 天前')
})

test('readInt 只接受整数，不把 "1.5"/"" 读成 0', () => {
  assert.equal(readInt(3), 3)
  assert.equal(readInt('3'), 3)
  assert.equal(readInt('-1'), -1)
  assert.equal(readInt('1.5'), null)
  assert.equal(readInt(''), null)
  assert.equal(readInt(null), null)
  assert.equal(readInt(undefined), null)
  assert.equal(readInt('abc'), null)
})

test('maskDeviceId 截断长指纹但保留首尾（便于人工比对）', () => {
  assert.equal(maskDeviceId('short'), 'short')
  const long = 'extension:0123456789abcdef0123456789abcdef'
  const masked = maskDeviceId(long)
  assert.ok(masked.length < long.length)
  assert.ok(masked.startsWith('extension:0123'))
  assert.ok(masked.endsWith('abcdef'))
})

// ─────────────────────── 页面 / API / 路由的接线契约 ───────────────────────

test('我的设备页具备列表/移除/拉黑/解除拉黑/调整上限五件事', async () => {
  const page = await readSource(path.join('pages', 'MyDevices', 'index.vue'))
  const code = stripComments(page)
  // 列表列：设备标签、最近活跃、状态
  assert.match(code, /deviceLabel/)
  assert.match(code, /最近活跃/)
  assert.match(code, /statusText/)
  // 四个动作
  assert.match(code, /removeDevice/)
  assert.match(code, /blockDevice/)
  assert.match(code, /unblockDevice/)
  assert.match(code, /saveDeviceLimit/)
  // 上限输入框的上下界（0 下界 + upperBound 上界）
  assert.match(code, /:min="0"/)
  assert.match(code, /:max=/)
  // 纯函数层必须被复用（否则上下界校验会与后端漂移）
  assert.match(code, /validateDeviceLimitInput/)
  assert.match(code, /normalizeDeviceList/)
  assert.match(code, /normalizeDeviceLimit/)
})

test('页面不自己写上限校验（必须走 myDevicesHelpers 的单一口径）', async () => {
  const page = await readSource(path.join('pages', 'MyDevices', 'index.vue'))
  const code = stripComments(page)
  // 反例形态：在页面里直接拼那句 400 文案 = 第二份口径。
  assert.doesNotMatch(code, /不能超过当前等级上限/)
})

test('TikuComponentApi 暴露六个设备方法且路径与后端一致', async () => {
  const api = await readSource(path.join('api', 'TikuComponentApi.js'))
  const code = stripComments(api)
  const expectations = [
    [/export function listMyDevices/, '/api/user/devices'],
    [/export function removeMyDevice/, '/api/user/devices/'],
    [/export function blockMyDevice/, 'block'],
    [/export function unblockMyDevice/, '/api/user/devices/blocks/'],
    [/export function getMyDeviceLimit/, '/api/user/device-limit'],
    [/export function updateMyDeviceLimit/, '/api/user/device-limit'],
  ]
  for (const [pattern, fragment] of expectations) {
    assert.match(code, pattern)
    assert.ok(code.includes(fragment), `缺少路径片段 ${fragment}`)
  }
})

test('router.js 注册 my-devices 路由且**不**放进 ADMIN_ONLY_ROUTE_NAMES', async () => {
  const routerCode = await readSource(path.join('router', 'router.js'))
  assert.match(routerCode, /path: 'my-devices'/)
  assert.match(routerCode, /name: 'my-devices'/)
  // 普通用户必须能访问：把它登记成管理员专属等于让功能只有管理员看得到。
  const adminOnlyBlock = routerCode.slice(
    routerCode.indexOf('const ADMIN_ONLY_ROUTE_NAMES = new Set(['),
    routerCode.indexOf('const PUBLIC_ADAPTER_ROUTE_NAMES'),
  )
  assert.ok(adminOnlyBlock.length > 0, '未能定位 ADMIN_ONLY_ROUTE_NAMES 段落')
  assert.doesNotMatch(adminOnlyBlock, /'my-devices'/)
})
