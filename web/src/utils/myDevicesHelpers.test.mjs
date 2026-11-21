import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  DEVICE_STATUS_META,
  describeLastSeen,
  formatDeviceUsageTime,
  deviceIPText,
  deviceLimitMaximumText,
  deviceLimitSourceText,
  deviceStatusMeta,
  emptyDeviceLimitState,
  emptyDeviceListState,
  maskDeviceId,
  normalizeDeviceItem,
  normalizeDeviceLimit,
  normalizeDeviceList,
  readInt,
  validateDeviceLabel,
  validateDeviceLimitInput,
  validateDeviceExpiryInput,
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

test('设备闲置时间只允许管理员范围内的整数，0 由独立开关控制', () => {
  for (const input of ['', -1, 1.5, 'abc', 11]) assert.equal(validateDeviceExpiryInput(input, 10, false).ok, false)
  assert.equal(validateDeviceExpiryInput(0, 10, false).ok, false)
  assert.equal(validateDeviceExpiryInput(0, 10, true).ok, true)
  assert.equal(validateDeviceExpiryInput(3, 10, false).value, 3)
})

test('已移除设备才显示删除记录，页面提供个人闲置移除设置', async () => {
  assert.equal(normalizeDeviceItem({id: 1, status: 'removed', deletable: true}).deletable, true)
  assert.equal(normalizeDeviceItem({id: 1, status: 'active', deletable: true}).deletable, false)
  assert.equal(normalizeDeviceItem({id: 1, status: 'blocked', blocked: true, deletable: true}).deletable, false)
  const page = stripComments(await readSource(path.join('pages', 'MyDevices', 'index.vue')))
  for (const expected of ['record.deletable', 'deleteRemovedDevice', 'expiryLoaded', 'saveExpiry', 'validateDeviceExpiryInput', '设备闲置自动移除']) assert.ok(page.includes(expected), expected)
  const api = stripComments(await readSource(path.join('api', 'TikuComponentApi.js')))
  for (const expected of ['deleteMyRemovedDevice', 'getMyDeviceExpiry', 'updateMyDeviceExpiry', '/api/user/device-expiry']) assert.ok(api.includes(expected), expected)
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

test('可设置最高数量独立于当前生效上限', () => {
  const unlimitedLevelWithOverride = normalizeDeviceLimit({
    limit: 1, source: 'user_override', unlimited: false,
    override: 1, levelLimit: 0, upperBound: null, editable: true,
  })
  assert.equal(unlimitedLevelWithOverride.limit, 1)
  assert.equal(deviceLimitMaximumText(unlimitedLevelWithOverride), '不限')

  const cappedLevelWithOverride = normalizeDeviceLimit({
    limit: 1, source: 'user_override', unlimited: false,
    override: 1, levelLimit: 3, upperBound: 3, editable: true,
  })
  assert.equal(cappedLevelWithOverride.limit, 1)
  assert.equal(deviceLimitMaximumText(cappedLevelWithOverride), '3 台')
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
  // 列表列：设备标签、最近使用时间、状态
  assert.match(code, /deviceLabel/)
  assert.match(code, /最近使用时间/)
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

// ─────────────────── 来源 IP ───────────────────

test('normalizeDeviceItem 回传 lastIp，缺失时为显式空串而不是伪造值', () => {
  const withIp = normalizeDeviceItem({ id: 7, deviceId: 'd', lastIp: '203.0.113.7' })
  assert.equal(withIp.lastIp, '203.0.113.7')

  // 缺失 / 非字符串 → 空串。**不能是** '0.0.0.0'、'未知' 或 undefined：
  // 前两者是伪造事实，undefined 会让 v-if 与展示分支都错。
  assert.equal(normalizeDeviceItem({ id: 7, deviceId: 'd' }).lastIp, '')
  assert.equal(normalizeDeviceItem({ id: 7, deviceId: 'd', lastIp: null }).lastIp, '')
  assert.equal(normalizeDeviceItem({ id: 7, deviceId: 'd', lastIp: 123 }).lastIp, '')
})

test('normalizeDeviceItem 独立保留首次与最近使用信息，不用最近 IP 伪造历史', () => {
  const view = normalizeDeviceItem({ firstIp: '172.18.0.1', lastIp: '203.0.113.7', firstSeenAt: '2026-11-01T01:02:03Z', lastSeenAt: '2026-11-20T01:02:03Z' })
  assert.equal(view.firstIp, '172.18.0.1')
  assert.equal(view.lastIp, '203.0.113.7')
  assert.equal(view.firstSeenAt, '2026-11-01T01:02:03Z')
  assert.equal(view.lastSeenAt, '2026-11-20T01:02:03Z')
  assert.equal(normalizeDeviceItem({ lastIp: '203.0.113.7' }).firstIp, '')
})

test('设备使用时间显示完整北京时间，不使用相对时间', () => {
  assert.equal(formatDeviceUsageTime('2026-11-01T01:02:03Z'), '2026-11-01 09:02:03')
  assert.equal(formatDeviceUsageTime('2026-11-01T09:02:03+08:00'), '2026-11-01 09:02:03')
  assert.equal(formatDeviceUsageTime(null), '未记录')
  assert.equal(formatDeviceUsageTime('not-a-date'), '未知时间')
})

test('deviceIPText 把空值翻成「未记录」，有值时原样显示', () => {
  assert.equal(deviceIPText(''), '未记录')
  assert.equal(deviceIPText('   '), '未记录')
  assert.equal(deviceIPText(null), '未记录')
  assert.equal(deviceIPText(undefined), '未记录')
  // IPv6 不能被截断或格式化（用户要能拿它去比对）。
  assert.equal(deviceIPText('2001:db8::1'), '2001:db8::1')
  assert.equal(deviceIPText('192.168.1.7'), '192.168.1.7')
})

// ─────────────────── 重命名 ───────────────────

test('normalizeDeviceItem 的 renamable 只对真实绑定行为 true', () => {
  // 正常绑定行：可改名。
  assert.equal(normalizeDeviceItem({ id: 12, deviceId: 'd' }).renamable, true)
  // 孤儿拉黑行（id=0，只有拉黑记录没有绑定行）：不可改名。
  // 若判成 true，用户点「重命名」会拿到 404 —— 按钮应该根本不出现。
  assert.equal(normalizeDeviceItem({ id: 0, deviceId: 'd', blockOnly: true, blockId: 3 }).renamable, false)
})

test('validateDeviceLabel 允许空串（= 恢复默认显示）', () => {
  // 这是关键：把空串判成非法会逼用户「随便填一个字符来清空」，造出脏数据。
  assert.deepEqual(validateDeviceLabel(''), { ok: true, value: '', error: '', message: '' })
  assert.equal(validateDeviceLabel('   ').ok, true)
  assert.equal(validateDeviceLabel(null).ok, true)
  assert.equal(validateDeviceLabel(undefined).ok, true)
})

test('validateDeviceLabel 拒绝换行/制表符/NUL，文案与后端一字不差', () => {
  const expected = '设备名称不能包含换行或控制字符'
  for (const bad of ['第一行\n第二行', '名字\t带制表符', '名字\r回车', '名字\x00']) {
    const result = validateDeviceLabel(bad)
    assert.equal(result.ok, false, `应当拒绝 ${JSON.stringify(bad)}`)
    assert.equal(result.message, expected)
  }
})

test('validateDeviceLabel 按字符（码点）计长度，不按 UTF-16 码元', () => {
  // 64 个汉字：恰好允许；65 个：拒绝。
  assert.equal(validateDeviceLabel('名'.repeat(64)).ok, true)
  const tooLong = validateDeviceLabel('名'.repeat(65))
  assert.equal(tooLong.ok, false)
  assert.equal(tooLong.message, '设备名称最多 64 个字符')

  // ⚠️ 这一段是「按码点 vs 按 .length」的**变异靶子**，数字必须选对。
  //
  // 33 个 emoji：码点 33（合法），`.length` 66（超限）。
  //   → 按码点实现：通过；按 .length 实现：拒绝。两者结论相反 ⇒ 变异必红。
  //
  // 曾用 32 个 emoji 写过这条，`.length` 恰好 64 —— 不超限，按两种实现都通过，
  // 是**恒真的假钉子**（实测变异后仍全绿）。选边界值时必须让两种口径**跨过**阈值，
  // 而不是恰好落在阈值上。
  const emoji = '🛋'.repeat(33)
  assert.equal(Array.from(emoji).length, 33, '前置：码点数必须是 33')
  assert.equal(emoji.length, 66, '前置：UTF-16 码元数必须是 66（超限）')
  assert.equal(validateDeviceLabel(emoji).ok, true, '33 个 emoji（码点 33）必须通过')
})

test('validateDeviceLabel 去首尾空白后再落 value', () => {
  const result = validateDeviceLabel('  家里的笔记本  ')
  assert.equal(result.ok, true)
  assert.equal(result.value, '家里的笔记本')
})

// ─────────────────── 页面与 API 接线 ───────────────────

test('我的设备页展示首次与最近 IP/时间、查询归属地并保留重命名', async () => {
  const page = await readSource(path.join('pages', 'MyDevices', 'index.vue'))
  const code = stripComments(page)
  // 两个时间与两个 IP 均独立显示；归属地继续走统一查询与格式化层。
  for (const label of ['首次使用 IP', '最近使用 IP', '首次使用时间', '最近使用时间']) assert.ok(code.includes(label))
  assert.match(code, /formatDeviceUsageTime/)
  assert.doesNotMatch(code, /describeLastSeen/)
  assert.match(code, /collectLocationKeys\(rows, \['firstIp', 'lastIp'\]\)/)
  assert.match(code, /lookupLocationAddresses\('ip', ips, getMyLocations, 'ip'\)/)
  assert.match(code, /formatValueWithLocation/)
  assert.match(code, /lastIp/)
  // 重命名：按钮 + 弹窗 + 提交函数。
  assert.match(code, /重命名/)
  assert.match(code, /openRename/)
  assert.match(code, /submitRename/)
  // 必须用 renamable 控制按钮可见性（孤儿拉黑行 id=0，点了必然 404）。
  assert.match(code, /record\.renamable/)
  // 弹窗要告诉用户「留空 = 恢复默认」，否则用户不知道该敲什么才能清空。
  assert.match(code, /未命名的设备/)
})

test('页面不自己写设备名校验（必须走 myDevicesHelpers 的单一口径）', async () => {
  const page = await readSource(path.join('pages', 'MyDevices', 'index.vue'))
  const code = stripComments(page)
  // 反例形态：在页面里直接拼那句 400 文案 = 第二份口径。
  assert.doesNotMatch(code, /设备名称不能包含换行或控制字符/)
  assert.doesNotMatch(code, /设备名称最多 \$\{?64/)
  // 必须真的调用纯函数。
  assert.match(code, /validateDeviceLabel/)
})

test('重命名接口路径与后端一致（PUT /user/devices/:id/label）', async () => {
  const api = await readSource(path.join('api', 'TikuComponentApi.js'))
  const code = stripComments(api)
  assert.match(code, /export function renameMyDevice/)
  assert.ok(code.includes('/api/user/devices/'), '缺少设备路径前缀')
  assert.ok(code.includes("'/label'"), '缺少 /label 子路径')
  // 必须是 put：用 post 会命中 405/404（后端只注册了 PUT）。
  const renameBlock = code.slice(code.indexOf('export function renameMyDevice'))
  assert.match(renameBlock.slice(0, 400), /method: 'put'/)
})
