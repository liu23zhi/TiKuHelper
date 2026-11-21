// 「工单反馈 / 工单管理各只有一个列表」的结构钉子（2026-11-18 UI 重构）。
//
// 历史：这个文件原本钉的是「退款面板必须常驻在列表下方」——那是 2026-11-14
// 的决定（当时的要求是「所有工单都在同一个列表里看，不要单独的类型入口按钮」，
// 但退款操作仍留在独立面板里）。
//
// 2026-11-18 用户明确升级了要求：
//   「工单反馈只保留一个工单列表，并提供发起工单按钮」「工单管理也只保留一个列表」
// 于是退款工作台（TicketRefundPanel.vue）被删除，退款操作收进工单详情
// （TicketRefundActions.vue）。本文件随之重写，钉的是新架构。
//
// ⚠️ 原文件里最有价值的一条判据必须保留：**删功能不能冒充删按钮**。
// 也就是说，面板可以删，但「提交退款 / 审核 / 打款 / 查看凭证」这些能力
// 必须仍然存在于某个可达位置。下面用「新文件里逐个动作可达」来钉住它。

import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const webRoot = path.join(here, '..', '..')
const read = (rel) => readFile(path.join(webRoot, rel), 'utf8')

const PAGE = 'src/pages/Tickets/index.vue'
const ACTIONS = 'src/pages/Tickets/TicketRefundActions.vue'

test('工单页只有一个工单列表（不再有第二个退款工作台）', async () => {
  const source = await read(PAGE)
  // 页面里只能挂一次「工单列表」面板。
  const listPanels = source.match(/title="工单列表"/g) || []
  assert.equal(listPanels.length, 1, '工单页必须只有一个「工单列表」')
  // 旧的退款工作台组件不能再被挂载。
  assert.doesNotMatch(
    source,
    /<TicketRefundPanel/,
    'TicketRefundPanel 已删除，页面不能再挂载它（否则又是第二套 UI）',
  )
})

test('退款工作台文件确实已删除', async () => {
  await assert.rejects(
    readFile(path.join(webRoot, 'src/pages/Tickets/TicketRefundPanel.vue'), 'utf8'),
    /ENOENT/,
    'TicketRefundPanel.vue 必须已被删除，不能只是不再引用',
  )
})

// 创建入口的路由/角色行为由 TicketsViewModeAnt4.test.cjs 通过真实点击验证：
// 管理员在「工单反馈」可创建，在「工单管理」处理工单。

test('退款操作收进详情：动作组件被详情弹窗挂载且绑定 detail.refund', async () => {
  const source = await read(PAGE)
  const mount = source.match(/<TicketRefundActions[\s\S]{0,300}?\/>/)
  assert.ok(mount, '详情里必须挂载 TicketRefundActions')
  assert.match(mount[0], /detailTicket\.refund/, '数据必须来自 detail.refund（后端新增字段）')
})

// 原判据的延续：删面板不等于删功能。逐个动作必须在新的可达位置。
test('退款四项能力在新的可达位置仍然存在（删功能不能冒充删按钮）', async () => {
  const actions = await read(ACTIONS)
  const page = await read(PAGE)

  // ① 提交退款：走「发起工单 → 退款申请」的向导。
  const helpers = await read('src/pages/Tickets/createTicketTypeHelpers.mjs')
  assert.match(helpers, /退款申请/, '提交入口必须在发起工单的类型向导里')

  // ② 审核通过 / ③ 驳回：在详情动作区。
  assert.match(actions, /reviewTicketRefund/, '审核动作必须仍在（reviewTicketRefund）')
  assert.match(actions, /审核通过/, '必须能审核通过')
  assert.match(actions, /驳回/, '必须能驳回')

  // ④ 打款：在详情动作区。
  assert.match(actions, /processTicketRefund/, '打款动作必须仍在（processTicketRefund）')

  // ⑤ 查看凭证：在详情动作区。
  assert.match(actions, /getTicketRefundProof/, '查看凭证必须仍在')

  // 页面本身仍要有发起入口，否则「提交退款」不可达。
  assert.match(page, /openCreate/, '发起工单的 openCreate 必须仍被绑定')
})

test('通知设置收进弹窗，不再常驻页面', async () => {
  const source = await read(PAGE)
  // 必须有一个弹窗承载通知设置。
  assert.match(
    source,
    /ticket-notification-config-modal|notificationConfigVisible/,
    '通知设置必须以弹窗形式存在',
  )
  // 它不能是 ConsolePanel（常驻面板）的 title —— 弹窗自身带 title 是对的。
  // 判据：ConsolePanel 段落里不得出现该标题。
  for (const panel of source.match(/<ConsolePanel[\s\S]*?<\/ConsolePanel>/g) || []) {
    assert.doesNotMatch(
      panel,
      /企业微信工单提醒/,
      '通知设置不能再作为常驻 ConsolePanel 出现（应改为按钮 + 弹窗）',
    );
  }
})

test('筛选：状态与分类对普通用户也可见（不再只给管理员）', async () => {
  const source = await read(PAGE)
  for (const label of ['状态', '分类']) {
    const item = source.match(
      new RegExp(`<a-form-item([^>]*)label="${label}"`),
    )
    assert.ok(item, `必须存在「${label}」筛选`)
    assert.doesNotMatch(
      item[1],
      /isAdmin/,
      `「${label}」筛选不能只给管理员 —— 普通用户也需要筛自己的单`,
    )
  }
})

test('提供「待处理退款」快捷筛选（原面板的队列价值由筛选承担）', async () => {
  const source = await read(PAGE)
  assert.match(source, /待处理退款/, '必须有「待处理退款」快捷筛选')
  assert.match(source, /toggleRefundPending/, '快捷筛选必须有切换逻辑')
  // 判据只认 category：退款「待处理」横跨 open 与 pending 两个 ticket status，
  // 写死 status=open 会漏掉 processing 那批（最需要看的）。
  const logic = source.match(/const refundPendingActive = computed\([^\n]*\)/)
  assert.ok(logic, '必须有 refundPendingActive 判据')
  assert.match(logic[0], /category/, '判据必须基于 category')
  assert.doesNotMatch(
    logic[0],
    /status/,
    '判据不能带 status —— 退款待处理横跨 open 与 pending，写死会漏单',
  )
})
