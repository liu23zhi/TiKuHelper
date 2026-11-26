import test from 'node:test'
import assert from 'node:assert/strict'

import {
  DEVICE_STATUS_ROWS,
  banExpiryText,
  canUnbindDevice,
  deviceBanAction,
  deviceStatusColor,
  deviceStatusText,
  readDeviceCount,
  revokeReasonText,
  shortDeviceId,
  unbindBlockedHint,
} from './deviceAdminHelpers.mjs'

// 设备管理页纯展示 helper 的钉子（137 设备体系 §9.2）。
//
// 这些用例跑在 `npm run test:unit`（`node --test src/utils/*.test.mjs`）里，
// 不依赖 vue / node_modules，因此任何环境都能执行 —— 这也是把格式化逻辑
// 抽成纯函数的唯一理由。

test('deviceStatusText 覆盖三态且未知状态原样返回', () => {
  assert.equal(deviceStatusText('active'), '使用中')
  assert.equal(deviceStatusText('revoked'), '已移除')
  assert.equal(deviceStatusText('banned'), '已拉黑')
  // 未知状态原样返回：后端加新态时显示原文才能让人去查，显示「未知」等于删线索。
  assert.equal(deviceStatusText('frozen'), 'frozen')
  assert.equal(deviceStatusText(''), '—')
  assert.equal(deviceStatusText(null), '—')
  // 大小写与空白要吃掉（后端稳定返回小写，但历史数据不一定）。
  assert.equal(deviceStatusText('  BANNED '), '已拉黑')
})

test('deviceStatusColor 与 DEVICE_STATUS_ROWS 一一对应', () => {
  assert.equal(deviceStatusColor('active'), 'green')
  assert.equal(deviceStatusColor('revoked'), 'default')
  assert.equal(deviceStatusColor('banned'), 'red')
  assert.equal(deviceStatusColor('whatever'), 'default')
  // 三态集合必须恰好三个：新增状态时必须显式登记（漏了会退化成 default 色，
  // 而 default 与「已解绑」同色 ⇒ 封禁态在视觉上消失）。
  assert.deepEqual(DEVICE_STATUS_ROWS.map(row => row.key), ['active', 'revoked', 'banned'])
})

test('revokeReasonText 翻译四个稳定原因', () => {
  assert.equal(revokeReasonText('user_removed'), '用户自己移除')
  assert.equal(revokeReasonText('admin_unbound'), '管理员强制解绑')
  assert.equal(revokeReasonText('idle_10d'), '闲置 10 天自动解绑')
  assert.equal(revokeReasonText('device_banned'), '设备被全局封禁')
  assert.equal(revokeReasonText(''), '—')
  assert.equal(revokeReasonText(null), '—')
  assert.equal(revokeReasonText('future_reason'), 'future_reason')
})

test('banExpiryText 把「永久」与「没填」分开', () => {
  // 判据：expiresAt 为空 = 永久。绝不能返回 '—' 或空串 ——
  // 那会被读成「数据缺失」，而「永不自动解除」与「查不到」是相反语义。
  assert.equal(banExpiryText(null), '永久')
  assert.equal(banExpiryText(undefined), '永久')
  assert.equal(banExpiryText(''), '永久')
  assert.equal(banExpiryText('   '), '永久')
  // 有值时用注入的格式化器（页面传 formatDateTime，测试传假实现避免时区依赖）。
  assert.equal(banExpiryText('2026-12-01T00:00:00Z', () => '2026-12-01 08:00'), '2026-12-01 08:00')
  // 格式化失败（非法日期）时不能返回空：空会被读成「没填」= 永久，方向反了。
  assert.equal(banExpiryText('not-a-date', () => ''), '永久')
})

test('shortDeviceId 两端都留', () => {
  assert.equal(shortDeviceId('exe:a1b2'), 'exe:a1b2')
  assert.equal(shortDeviceId('   '), '—')
  assert.equal(shortDeviceId(null), '—')
  const long = 'exe:a1b2c3d4-e5f6-7890-abcd-ef1234567890'
  const short = shortDeviceId(long)
  assert.ok(short.startsWith(long.slice(0, 10)), `必须保留前段以便识别设备类型，got ${short}`)
  assert.ok(short.endsWith(long.slice(-6)), `必须保留末段以便区分同前缀设备，got ${short}`)
  assert.ok(!short.includes('-e5f6-'), '中段必须省略（否则仍撑破列宽）')
})

test('readDeviceCount 只接受非负整数', () => {
  assert.equal(readDeviceCount(0), 0)
  assert.equal(readDeviceCount('12'), 12)
  assert.equal(readDeviceCount(''), null)
  assert.equal(readDeviceCount(-1), null)
  assert.equal(readDeviceCount('abc'), null)
  assert.equal(readDeviceCount(true), null)
  assert.equal(readDeviceCount(null), null)
})

test('canUnbindDevice 三条闸门缺一不可', () => {
  const active = { id: 7, revokedAt: null }
  assert.equal(canUnbindDevice(active, true), true)
  // 结构不支持 → 一律不可解绑（否则点了必然 503）。
  assert.equal(canUnbindDevice(active, false), false)
  // 已解绑 → 没有语义（幂等但无意义）。
  assert.equal(canUnbindDevice({ id: 7, revokedAt: '2026-01-01T00:00:00Z' }, true), false)
  // 没有绑定行 → 无可解绑对象。
  assert.equal(canUnbindDevice({ id: 0, revokedAt: null }, true), false)
  assert.equal(canUnbindDevice({ revokedAt: null }, true), false)
  assert.equal(canUnbindDevice(null, true), false)
  // 封禁态**不影响**解绑按钮：被封禁的设备可能还留着未撤销的绑定行。
  assert.equal(canUnbindDevice({ id: 7, revokedAt: null, banned: true }, true), true)
})

test('unbindBlockedHint 给出不同原因', () => {
  const structural = unbindBlockedHint({ id: 7, revokedAt: null }, false)
  assert.match(structural, /revoked_at/)
  assert.match(structural, /迁移/)
  const already = unbindBlockedHint({ id: 7, revokedAt: 'x' }, true)
  assert.match(already, /已解绑/)
  const noRow = unbindBlockedHint({ id: 0, revokedAt: null }, true)
  assert.match(noRow, /没有绑定记录/)
  // 三种原因必须互不相同：下一步动作完全不同（跑迁移 / 不用管 / 数据异常）。
  assert.notEqual(structural, already)
  assert.notEqual(already, noRow)
})

test('deviceBanAction 用 banned 布尔判，不看 status 字符串', () => {
  assert.equal(deviceBanAction({ banned: true, status: 'banned' }), 'unban')
  assert.equal(deviceBanAction({ banned: false, status: 'active' }), 'ban')
  // 关键对照：status 已是 banned 但布尔为 false（旧后端/派生异常）时，
  // 动作必须按布尔走 —— 用 status 判会把新态误判成「未封禁」而给出错误按钮。
  assert.equal(deviceBanAction({ banned: false, status: 'banned' }), 'ban')
  assert.equal(deviceBanAction({}), 'ban')
  assert.equal(deviceBanAction(null), 'ban')
})
