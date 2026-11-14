import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import {
  STORAGE_WARNING_DISMISS_KEY,
  buildStorageWarningAnnouncement,
  resolveStorageWarningVisibility,
  shouldRespectDismiss,
  shouldShowStorageWarning,
} from './storageWarning.mjs'

/**
 * SQLite 悬浮警告的可见性判据（task_doc/108:297、109:474/486）。
 *
 * 这一组用例服务的核心风险是「警告该出现时没出现 / 不该消失时消失」。
 * 任务书点名的两种失败形态都在这里被钉住：
 *   ① driver 判定恒 false → SQLite 时不显示（变异 A）
 *   ② 一次误关就永久消失 → 重新登录不重现（dismissed 语义写错）
 */

const SQLITE_STATUS = {
  driver: 'sqlite',
  warning: '当前使用 SQLite 存储，仅供测试用途（已冻结，不再做性能优化）。生产环境请改用 MySQL / MariaDB。',
  production: false,
}

const MYSQL_STATUS = { driver: 'mysql', warning: '', production: true }

test('SQLite 时显示：driver=sqlite 且 warning 非空', () => {
  assert.equal(shouldShowStorageWarning(SQLITE_STATUS), true)
})

test('MySQL 时不显示', () => {
  assert.equal(shouldShowStorageWarning(MYSQL_STATUS), false)
})

test('development 模式的 SQLite 同样显示（文档未区分模式）', () => {
  // 文档说的是「使用 SQLite 就警告」。若按 production 收窄，开发/测试实例
  // 的用户永远看不到——而那正是文档要求提示的场景。
  assert.equal(shouldShowStorageWarning({ ...SQLITE_STATUS, production: false }), true)
  assert.equal(shouldShowStorageWarning({ ...SQLITE_STATUS, production: true }), true)
})

test('warning 为空时不显示（文案由后端给，前端不自己编）', () => {
  assert.equal(shouldShowStorageWarning({ driver: 'sqlite', warning: '', production: false }), false)
  assert.equal(shouldShowStorageWarning({ driver: 'sqlite', warning: '   ', production: false }), false)
  assert.equal(shouldShowStorageWarning({ driver: 'sqlite' }), false)
})

test('status 为 null/非对象时不显示（取数失败路径）', () => {
  assert.equal(shouldShowStorageWarning(null), false)
  assert.equal(shouldShowStorageWarning(undefined), false)
  assert.equal(shouldShowStorageWarning('sqlite'), false)
})

test('关闭记录：空记录视为「没关过」', () => {
  // 写反成「空 = 关过了」会让第一次进入的用户直接看不到警告。
  assert.equal(shouldRespectDismiss('', 'rev-1'), false)
  assert.equal(shouldRespectDismiss('   ', 'rev-1'), false)
  assert.equal(shouldRespectDismiss(null, 'rev-1'), false)
})

test('关闭记录：同代次隐藏', () => {
  assert.equal(shouldRespectDismiss('rev-1', 'rev-1'), true)
})

test('关闭记录：代次不同（重新登录）则不隐藏', () => {
  assert.equal(shouldRespectDismiss('rev-1', 'rev-2'), false)
})

test('当前代次读不到时不隐藏（宁可多显示一次，不能永久静默）', () => {
  assert.equal(shouldRespectDismiss('rev-1', ''), false)
  assert.equal(shouldRespectDismiss('rev-1', null), false)
})

test('合成可见性：六种组合', () => {
  // [status, dismissed, current, want]
  const cases = [
    [SQLITE_STATUS, '', 'rev-1', true, 'SQLite + 没关过 → 显示'],
    [SQLITE_STATUS, 'rev-1', 'rev-1', false, 'SQLite + 本次已关 → 隐藏'],
    [SQLITE_STATUS, 'rev-1', 'rev-2', true, 'SQLite + 重新登录 → 重现'],
    [MYSQL_STATUS, '', 'rev-1', false, 'MySQL + 没关过 → 不显示'],
    [MYSQL_STATUS, '', '', false, 'MySQL → 不显示（与代次无关）'],
    [null, '', 'rev-1', false, '取数失败 → 不显示'],
  ]
  for (const [status, dismissed, current, want, label] of cases) {
    assert.equal(resolveStorageWarningVisibility(status, dismissed, current), want, label)
  }
})

test('播报文本包含后端文案，不显示时为空串', () => {
  assert.match(buildStorageWarningAnnouncement(SQLITE_STATUS), /SQLite/)
  assert.match(buildStorageWarningAnnouncement(SQLITE_STATUS), /MySQL \/ MariaDB/)
  assert.equal(buildStorageWarningAnnouncement(MYSQL_STATUS), '')
})

test('localStorage 键是带代次的专用键（不与其它键冲突）', () => {
  assert.equal(STORAGE_WARNING_DISMISS_KEY, 'adapter_storage_warning_dismissed_revision')
})

// ───────── 源码级接线钉子 ─────────
//
// 逻辑文件测通了，不代表「它被挂上去了」。以下断言防的是
// 「组件存在、逻辑正确、但布局里没挂」——那正是本仓多次出现的「只缺接线」。

const readWeb = relative => readFile(new URL(`../${relative}`, import.meta.url), 'utf8')

test('布局真的挂载了 StorageWarning，并把它放在 PageHeader 之前', async () => {
  const layout = await readWeb('layout/TikuAdapterLayout.vue')
  assert.match(layout, /<StorageWarning[\s\S]*?\/>/, '布局必须挂载 <StorageWarning />')
  const warningAt = layout.indexOf('<StorageWarning')
  const headerAt = layout.indexOf('<PageHeader')
  assert.ok(warningAt > 0, '找不到挂载点')
  assert.ok(headerAt > 0, '找不到 PageHeader')
  assert.ok(warningAt < headerAt, '悬浮警告应挂在 PageHeader 之前（浮层先渲染）')
  // 必须把后端响应与代次都传下去：不传 session-revision 就无法表达「重新登录重现」。
  assert.match(layout, /:status="storageStatus"/)
  assert.match(layout, /:session-revision="storageWarningRevision"/)
})

test('布局从 SystemStatusApi 取数（而不是自己 fetch）', async () => {
  const layout = await readWeb('layout/TikuAdapterLayout.vue')
  assert.match(layout, /import \{ getStorageStatus \} from '\.\.\/api\/SystemStatusApi'/)
  assert.match(layout, /getStorageStatus\(\)/)
})

test('组件文案不写死中文警告（必须来自后端）', async () => {
  const component = await readWeb('components/StorageWarning.vue')
  // 组件里唯一允许出现的中文是按钮与 aria-label；警告正文必须来自 props。
  assert.ok(!component.includes('仅供测试用途'), '警告正文不得在组件里硬编码')
  assert.ok(!component.includes('MySQL / MariaDB'), '行动指引不得在组件里硬编码')
  assert.match(component, /\{\{ warning \}\}/, '正文必须绑定计算属性 warning')
})

test('组件样式不含颜色字面量（依赖 theme-tokens 以支持深色模式）', async () => {
  const component = await readWeb('components/StorageWarning.vue')
  const style = component.slice(component.indexOf('<style'))
  // ⚠️ 必须先剥注释：本组件的样式块里有一段解释「浅色 #854D0E on #FFF5DA、
  // 深色 #F4CF83 on #3A2E17」的说明，不剥注释会把反例说明误判成违规。
  const code = style.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/[^\n]*/g, '')
  assert.ok(!/#[0-9a-fA-F]{3,8}\b/.test(code), '样式里不得写十六进制颜色')
  // rgba 也算字面量：写死 rgba(0,0,0,.16) 的阴影在深色主题下几乎不可见。
  assert.ok(!/rgba?\(\s*\d/.test(code), '样式里不得写 rgba() 颜色字面量')
  assert.match(code, /var\(--warning-bg\)/)
  assert.match(code, /var\(--warning-fg\)/)
  assert.match(code, /var\(--warning-border\)/)
  // 阴影也必须走令牌（深浅两套主题的阴影强度不同）。
  assert.match(code, /var\(--shadow-color/)
})

test('组件是悬浮而非内嵌（position: fixed）', async () => {
  const component = await readWeb('components/StorageWarning.vue')
  const style = component.slice(component.indexOf('<style'))
  assert.match(style, /\.storage-warning \{[^}]*position: fixed/, '用户要求的是悬浮警告')
})
