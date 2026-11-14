import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  RISK_ADVISORY_NOTE,
  RISK_INDEPENDENCE_NOTICE,
  RISK_SETTING_KEYS,
  buildRiskAlertListParams,
  buildRiskEvidenceRows,
  buildRiskWebhookUpdatePayload,
  describeRiskWebhookState,
  formatMentionedInput,
  normalizeRiskWebhookSettings,
  parseMentionedInput,
  parseRiskEvidence,
  readRiskListBody,
  riskRuleLabel,
  riskSeverityMeta,
  validateRiskAlertFilters,
} from '../pages/RiskAdmin/riskAdminHelpers.mjs'

const here = path.dirname(fileURLToPath(import.meta.url))
const webRoot = process.env.TIKU_WEB_SOURCE || path.resolve(here, '../..')

const readSource = relative => readFile(path.join(webRoot, 'src', relative), 'utf8')

// stripComments 去掉 // 行注释与 /* */ 块注释。
//
// 必要性：源码里的**注释**会写明「本文件不调用 /adapter-service/tickets/config」，
// 直接对全文做 doesNotMatch 会把这句声明本身当成违规命中（假红）。
// 但也不能只对注释做断言 —— 所以要剥离注释后只看真实代码。
function stripComments(source) {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*\/\/.*$/gm, '')
    .replace(/\s\/\/\s.*$/gm, '')
}

// ─────────────────────────────────────────────────────────────────────
// C3：两个输入框、值可相同、记录独立
//
// 这一组是**行为断言**：直接执行 riskAdminHelpers.mjs 的真实实现。
// 变异方式（必须让它们红）：
//   ① 给 buildRiskWebhookUpdatePayload 加一行
//      `if (!payload.webhookUrl) payload.webhookUrl = ticketWebhookUrl`
//      → 「空白不提交地址」用例红；
//   ② 把 clearWebhook 分支改成「清空时也带地址」
//      → 「清空不夹带地址」用例红；
//   ③ 把 parseMentionedInput 的去重去掉 → 去重用例红。
// ─────────────────────────────────────────────────────────────────────

test('C3 UI 提示必须是用户要求的原文，不能概括', () => {
  // 逐字断言而不是 includes('独立')：用户点名要这段文案，
  // 「简化成一句话」是本任务最容易出现的走样形态。
  assert.equal(
    RISK_INDEPENDENCE_NOTICE,
    '⚠️ 风控通知与工单通知是两套独立配置。即使填入同一个群机器人地址，也请分别填写 —— 清除其中一个不会影响另一个。',
  )
  // 三个必须出现的语义各自再钉一次（防止有人只改标点之外的措辞也过）。
  assert.ok(RISK_INDEPENDENCE_NOTICE.includes('两套独立配置'))
  assert.ok(RISK_INDEPENDENCE_NOTICE.includes('同一个群机器人地址'))
  assert.ok(RISK_INDEPENDENCE_NOTICE.includes('清除其中一个不会影响另一个'))
})

test('C3 风控与工单用的是两组互不相同的 setting key', () => {
  const keys = Object.values(RISK_SETTING_KEYS)
  assert.equal(new Set(keys).size, keys.length, '六个 key 必须互不相同')
  for (const key of ['enabled', 'webhook', 'mentioned']) {
    assert.ok(RISK_SETTING_KEYS[key].startsWith('wecom_risk_'), `风控 ${key} 必须用 wecom_risk_ 前缀`)
    assert.ok(RISK_SETTING_KEYS['ticket' + key[0].toUpperCase() + key.slice(1)].startsWith('wecom_ticket_'), `工单 ${key} 必须是 wecom_ticket_ 前缀`)
  }
  // 同一语义的两个 key 绝不能相等（那等于复用）。
  assert.notEqual(RISK_SETTING_KEYS.webhook, RISK_SETTING_KEYS.ticketWebhook)
})

test('C3 保存风控配置：空白 = 保持原值，绝不提交空地址', () => {
  const payload = buildRiskWebhookUpdatePayload({ enabled: true, webhookUrl: '   ', mentionedList: '' })
  assert.equal(Object.hasOwn(payload, 'webhookUrl'), false, '空白地址必须整键缺席（后端 *string 语义是「保持原值」）')
  // ⚠️ enabled 必须**恒传**：漏传时后端 *bool 为 nil → 开关永远改不动。
  assert.equal(Object.hasOwn(payload, 'enabled'), true)
  assert.equal(payload.enabled, true)
  assert.deepEqual(payload.mentionedList, [])
  assert.equal(payload.clearWebhook, false)
})

test('C3 保存风控配置：填了新地址就提交，且不夹带工单字段', () => {
  const payload = buildRiskWebhookUpdatePayload({
    enabled: false,
    webhookUrl: '  https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=risk  ',
    mentionedList: 'alice, bob',
  })
  assert.equal(payload.webhookUrl, 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=risk', '必须 trim 后再提交')
  assert.deepEqual(payload.mentionedList, ['alice', 'bob'])
  // 请求体里**不得**出现任何工单通道字段（两个通道的 payload 不允许合流）。
  for (const key of Object.keys(payload)) {
    assert.ok(!/ticket/i.test(key), `风控请求体不得包含工单字段：${key}`)
  }
})

test('C3 清空风控地址：只表达清空意图，不夹带地址', () => {
  const payload = buildRiskWebhookUpdatePayload({
    enabled: true,
    clearWebhook: true,
    webhookUrl: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=stale',
  })
  assert.equal(payload.clearWebhook, true)
  assert.equal(
    Object.hasOwn(payload, 'webhookUrl'),
    false,
    '清空时不得同时提交地址：日志/回放里会出现「清了又填」的歧义记录',
  )
})

test('C3 读回配置：webhookUrl 恒为空，只展示脱敏 preview', () => {
  const settings = normalizeRiskWebhookSettings({
    enabled: true,
    configured: true,
    // 故意模拟「后端误回显真实地址」：页面归一化必须把它丢掉，
    // 否则截图/复制就能带走凭据。
    webhookUrl: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=leaked',
    preview: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=已配置',
    mentionedList: ['a', 'a', 'b'],
  })
  assert.equal(settings.webhookUrl, '', '凭据永不进页面状态')
  assert.equal(settings.preview, 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=已配置')
  assert.deepEqual(settings.mentionedList, ['a', 'b'], '@成员必须去重')
})

test('C3 读回配置：字段类型不对必须报错，不能静默当成「未配置」', () => {
  assert.throws(() => normalizeRiskWebhookSettings(null), /响应无效/)
  assert.throws(() => normalizeRiskWebhookSettings({ enabled: 'true', configured: true }), /响应无效/)
  assert.throws(() => normalizeRiskWebhookSettings({ enabled: true }), /响应无效/)
})

test('C3 状态文案：未配置 / 已配置未启用 / 已启用 三态可区分', () => {
  assert.match(describeRiskWebhookState({ configured: false, enabled: false }), /未配置/)
  const disabled = describeRiskWebhookState({ configured: true, enabled: false, preview: 'https://x?key=已配置' })
  assert.match(disabled, /已配置但未启用/)
  assert.match(disabled, /不会推送|不推送/)
  const enabled = describeRiskWebhookState({ configured: true, enabled: true, preview: 'https://x?key=已配置' })
  assert.match(enabled, /已启用/)
  assert.equal(describeRiskWebhookState(null), '尚未读取配置')
})

// ─────────────────────────────────────────────────────────────────────
// C1：只告警不拦截
// ─────────────────────────────────────────────────────────────────────

test('C1 页面必须有「不做自动处置」的常驻提示', () => {
  assert.ok(RISK_ADVISORY_NOTE.includes('不会封禁'))
  assert.ok(RISK_ADVISORY_NOTE.includes('不会拒绝请求'))
  assert.ok(RISK_ADVISORY_NOTE.includes('不会修改任何用户状态'))
  // 必须指明处置去哪做，否则运营看到告警找不到下一步。
  assert.ok(RISK_ADVISORY_NOTE.includes('黑名单'))
})

test('C1 页面源码里不得出现任何封禁/处置写操作', async () => {
  const page = await readSource('pages/RiskAdmin/index.vue')
  // 风控页只读：出现这些词就意味着有人在告警页接了处置入口。
  const forbidden = [
    /updateUserStatus/i,
    /banUser/i,
    /setUserBan/i,
    /\b封禁账号\b/,
    /blacklistEnabled:\s*next/,
    /adapter-service\/user\/\$\{[^}]*\}\/status/,
  ]
  for (const pattern of forbidden) {
    assert.doesNotMatch(page, pattern, `风控页不得包含处置写操作：${pattern}`)
  }
  // 反向：必须只有**只读**的两个数据源（列表 / 详情）。
  assert.match(page, /getRiskAlerts\(/, '列表读取必须存在')
  assert.match(page, /getRiskAlertDetail\(/, '详情读取必须存在')
})

test('C1 风控 API 模块不碰工单配置接口', async () => {
  const api = stripComments(await readSource('api/RiskAdminApi.js'))
  assert.doesNotMatch(api, /tickets\/config/, '风控 API 模块不得调用工单配置接口')
  assert.doesNotMatch(api, /TicketApi/, '风控 API 模块不得 import 工单 API')
  assert.doesNotMatch(api, /getTicketWecomSettings|updateTicketWecomSettings/, '不得调用工单配置的读写函数')
  // 五条路由都在（判据 ①「5 条路由可调」的前端侧证据）。
  assert.match(api, /const RISK_BASE = '\/adapter-service\/risk'/, '基准路径必须是 /adapter-service/risk')
  assert.match(api, /RISK_BASE \+ '\/alerts'/, '列表路由')
  assert.match(api, /RISK_BASE \+ '\/alerts\/' \+ value/, '详情路由（带 id 校验）')
  assert.match(api, /RISK_BASE \+ '\/webhook'/)
  assert.match(api, /RISK_BASE \+ '\/webhook\/test'/)
  // 「风控为空就回退工单」这类隐式复用不得出现在任何形态里。
  assert.doesNotMatch(api, /\?\?\s*ticket/i)
  assert.doesNotMatch(api, /if\s*\(\s*!?\s*risk\w*\s*\)\s*\{[^}]*ticket/i)
})

test('C3 页面把「两套独立」提示与配置卡片放在一起（漏渲染等于没提示）', async () => {
  const page = await readSource('pages/RiskAdmin/index.vue')
  assert.match(page, /RISK_INDEPENDENCE_NOTICE/, '页面必须引用那段原文常量（硬编码副本会与测试漂移）')
  assert.match(page, /:message="RISK_INDEPENDENCE_NOTICE"/, '原文必须真的渲染进 a-alert')
  assert.match(page, /buildRiskWebhookUpdatePayload\(config\)/, '保存必须走唯一那处 payload 构造')
  // 三个控件缺一不可（96 §4.2 的五个控件里，这两组是 C3 直接依赖的）。
  assert.match(page, /v-on="\{ 'update:checked': toggleConfigEnabled \}"/, '启用开关必须存在')
  assert.match(page, /clearSavedWebhook/, '必须有独立的「清除风控地址」按钮')
  assert.match(page, /testRiskWebhook\(/, '必须有独立的「发送测试消息」按钮')
})

test('判据 ②（改阈值落库）已实现：前端必须提供阈值读写调用，且打在真实存在的路由上', async () => {
  // ⚠️ 这条用例曾经是**反向**的（断言「不得出现 threshold」）。
  //
  // 原因：当时 controller/risk_admin.go 只有 5 个 handler，没有任何 risk_rules
  // 读写接口；阈值 risk_rule_<id>_threshold 是 system_settings 键，而
  // UpdateSystemSettingInput 是固定 struct，既没有该字段也没有通用 key/value 透传口。
  // 页面放输入框会「能填、能点保存、毫无反应」，比缺失更难排查，
  // 所以当时故意不做 UI 并把它钉住。
  //
  // 现在后端补了 GET/PUT/DELETE /risk/thresholds[/:key]，
  // 阈值这条判据从「刻意未实现」变成「必须实现」，用例随之翻转。
  const api = stripComments(await readSource('api/RiskAdminApi.js'))
  // 三个动作缺一不可：读、写、重置。
  assert.match(api, /export function getRiskThresholds\(/, '必须有读取阈值的 API')
  assert.match(api, /export function updateRiskThreshold\(/, '必须有写入阈值的 API')
  assert.match(api, /export function resetRiskThreshold\(/, '必须有重置为默认的 API（否则设错了无法回退）')
  // 路由必须与后端挂的一致（router.go：/risk/thresholds 与 /risk/thresholds/:key）。
  assert.match(api, /RISK_BASE \+ '\/thresholds'/, '读取路由必须是 /risk/thresholds')
  assert.match(api, /RISK_BASE \+ '\/thresholds\/'/, '写入/重置路由必须是 /risk/thresholds/:key')
  // ⚠️ 这里原来有一条 `assert.doesNotMatch(api, /\/risk\/rules/)`，理由写的是
  // 「不得声明后端不存在的 /risk/rules 路由」。**那条断言的理由已经过期**：
  // 路由已于 internal/api/router.go:496-497 挂上（GET /risk/rules +
  // PUT /risk/rules/:ruleId/enabled），handler 在 controller/risk_admin.go。
  // 保留它等于**禁止任何人接这个能力** —— 而且报错会把人引向「后端不存在」
  // 的错误结论。已删除，改为下面的正向断言。
  assert.match(api, /export function getRiskRules\(/, '必须有读取规则状态的 API（否则 Description 与启停到不了用户）')
  assert.match(api, /export function setRiskRuleEnabled\(/, '必须有启停规则的 API')
  // 路由必须与后端挂的一致（router.go:496-497）。
  assert.match(api, /RISK_BASE \+ '\/rules'/, '读取规则路由必须是 /risk/rules')
  assert.match(api, /RISK_BASE \+ '\/rules\/'/, '启停路由必须是 /risk/rules/:ruleId/enabled')

  // 单位必须来自后端 RiskRuleState.Unit，**不得**在页面里内联一份映射。
  // 内联映射是「第二份真相」，与 riskThresholdSpecs 的 Unit 必然漂移。
  assert.doesNotMatch(api, /RULE_THRESHOLD_UNIT/, '不得在前端内联 ruleId → 单位的映射，单位由后端给')

  // 路由白名单同步扩展（服务端实际挂的就是这些）。
  const routes = [...api.matchAll(/RISK_BASE \+ '(\/[\w/]+)'/g)].map(m => m[1])
  assert.deepEqual(
    [...new Set(routes)].sort(),
    ['/alerts', '/alerts/', '/rules', '/rules/', '/thresholds', '/thresholds/', '/webhook', '/webhook/test'],
    '风控 API 模块只允许列表 / 详情 / 配置读写 / 测试发送 / 阈值读写重置这几条路径',
  )
  assert.match(api, /RISK_BASE \+ '\/alerts\/' \+ value/, '详情路径必须是拼接形式（不能写成模板字面量绕过上面的统计）')
})

// ─────────────────────────────────────────────────────────────────────
// 参数校验：与后端 risk_admin.go 逐条对齐
// ─────────────────────────────────────────────────────────────────────

test('筛选校验：userId 必须是正整数（后端 400 的同一判据）', () => {
  assert.equal(validateRiskAlertFilters({ userId: '' }), '')
  assert.equal(validateRiskAlertFilters({ userId: '123' }), '')
  assert.match(validateRiskAlertFilters({ userId: 'abc' }), /正整数/)
  assert.match(validateRiskAlertFilters({ userId: '0' }), /正整数/)
  assert.match(validateRiskAlertFilters({ userId: '-1' }), /正整数/)
  assert.match(validateRiskAlertFilters({ userId: '12.5' }), /正整数/)
  assert.match(validateRiskAlertFilters({ userId: '99999999999' }), /超出范围/)
})

test('筛选校验：notified 只接受 true / false', () => {
  assert.equal(validateRiskAlertFilters({ notified: '' }), '')
  assert.equal(validateRiskAlertFilters({ notified: 'true' }), '')
  assert.equal(validateRiskAlertFilters({ notified: 'false' }), '')
  assert.match(validateRiskAlertFilters({ notified: 'maybe' }), /推送状态/)
  assert.match(validateRiskAlertFilters({ notified: 'TRUE' }), /推送状态/)
})

test('筛选校验：日期格式与范围（起始晚于结束必须拦下）', () => {
  assert.equal(validateRiskAlertFilters({ createdFrom: '2026-11-13', createdTo: '2026-11-13' }), '')
  assert.equal(validateRiskAlertFilters({ createdFrom: '2026-11-12', createdTo: '2026-11-13' }), '')
  assert.match(validateRiskAlertFilters({ createdFrom: '2026-11-13', createdTo: '2026-11-12' }), /晚于/)
  // 2026-02-30 是不存在的日期：纯字符串比较会放过它，必须真校验。
  assert.match(validateRiskAlertFilters({ createdTo: '2026-02-30' }), /格式无效/)
  assert.match(validateRiskAlertFilters({ createdFrom: 'not-a-date', createdTo: '2026-11-13' }), /格式无效/)
  assert.equal(validateRiskAlertFilters({ createdFrom: '2026-11-13T10:00:00+08:00' }), '')
})

test('列表参数：空值必须整键缺席（传空串会拼出恒空结果）', () => {
  const params = buildRiskAlertListParams({}, 1, 20)
  assert.deepEqual(Object.keys(params).sort(), ['page', 'pageSize'])
  // ruleId='' 会让后端拼 `rule_id = ''`，永远是空列表。
  assert.equal(Object.hasOwn(buildRiskAlertListParams({ ruleId: '' }, 1, 20), 'ruleId'), false)
  assert.equal(buildRiskAlertListParams({ ruleId: 'debt_user' }, 1, 20).ruleId, 'debt_user')
  // notified 只透传合法值，'maybe' 不得出现（否则后端 400）。
  assert.equal(Object.hasOwn(buildRiskAlertListParams({ notified: 'maybe' }, 1, 20), 'notified'), false)
  assert.equal(buildRiskAlertListParams({ notified: 'true' }, 1, 20).notified, 'true')
})

test('列表参数：分页必须夹到合法范围', () => {
  assert.deepEqual(
    (({ page, pageSize }) => ({ page, pageSize }))(buildRiskAlertListParams({}, -3, 9999)),
    { page: 1, pageSize: 100 },
  )
  assert.deepEqual(
    (({ page, pageSize }) => ({ page, pageSize }))(buildRiskAlertListParams({}, 2, '50')),
    { page: 2, pageSize: 50 },
  )
  assert.equal(buildRiskAlertListParams({}, 'abc', 'abc').page, 1)
})

// ─────────────────────────────────────────────────────────────────────
// 展示层：证据快照、@成员、规则名
// ─────────────────────────────────────────────────────────────────────

test('证据快照：坏 JSON 不能让详情不可看（与后端同一策略）', () => {
  const parsed = parseRiskEvidence('{not json')
  assert.deepEqual(parsed.evidence, {})
  assert.equal(parsed.unparsable, '{not json')
  const rows = buildRiskEvidenceRows('{not json')
  assert.equal(rows.length, 1)
  assert.match(rows[0].field, /无法解析/)
  assert.equal(rows[0].value, '{not json')
  // 空 Detail 不是坏数据：返回空证据而不是报错。
  assert.deepEqual(buildRiskEvidenceRows(''), [])
})

test('证据快照：字段顺序固定（同一告警两次打开顺序一致）', () => {
  const detail = JSON.stringify({ value: 150, ruleId: 'debt_user', evidenceNote: 'snapshot', threshold: 100, zzz: 1 })
  const rows = buildRiskEvidenceRows(detail)
  assert.deepEqual(rows.map(row => row.field), ['ruleId', 'threshold', 'value', 'evidenceNote', 'zzz'])
  assert.equal(rows.find(row => row.field === 'value').value, '150')
})

test('@成员解析：支持中英文分隔符、去重、超长丢弃', () => {
  assert.deepEqual(parseMentionedInput('a，b;c\nd'), ['a', 'b', 'c', 'd'])
  assert.deepEqual(parseMentionedInput(['a', ' a ', 'b']), ['a', 'b'])
  assert.deepEqual(parseMentionedInput(''), [])
  assert.deepEqual(parseMentionedInput('x'.repeat(65)), [], '单成员超过 64 字符必须丢弃（与后端一致）')
  assert.equal(formatMentionedInput(['a', 'b']), 'a，b')
  // 往返：输入框 → payload → 输入框 必须稳定。
  assert.deepEqual(parseMentionedInput(formatMentionedInput(['a', 'b', 'a'])), ['a', 'b'])
})

test('规则名与级别：未知值回落而不是抛错', () => {
  assert.equal(riskRuleLabel('debt_user'), '单用户欠款超阈值')
  assert.equal(riskRuleLabel('debt_batch'), '单批次欠款超阈值')
  assert.equal(riskRuleLabel('debt_system'), '系统总欠款突增')
  // R1（96 §2.1）：后端 rule_id 必须与这里逐字一致，否则筛选下拉选中后查不到数据。
  assert.equal(riskRuleLabel('multi_account_ip'), '同 IP 多账号')
  // R8 / R11：后端 rule_id 必须与这里逐字一致，否则筛选下拉选中后查不到数据。
  assert.equal(riskRuleLabel('ticket_flood'), '工单刷屏')
  assert.equal(riskRuleLabel('abnormal_register_pattern'), '注册名高度模板化')
  // R12 / R16：后端 rule_id 必须与这里逐字一致。
  assert.equal(riskRuleLabel('token_abuse'), 'access token 滥用')
  assert.equal(riskRuleLabel('offhours_activity'), '凌晨作息异常（需 3 天历史）')
  // R14 / R18：后端 rule_id 必须与这里逐字一致。
  assert.equal(riskRuleLabel('binding_churn'), '频繁换绑')
  assert.equal(riskRuleLabel('wechat_phone_share'), '封禁账号共用号（仅当前绑定）')
  assert.equal(riskRuleLabel('impossible_travel'), '异地瞬移（仅公网 IP · 回溯 7 天）')
  assert.equal(riskRuleLabel('credential_stuffing'), '撞库（仅失败登录 · 30 分钟）')
  assert.equal(riskRuleLabel('login_bruteforce'), '登录爆破（同账号或 IP · 10 分钟）')
  assert.equal(riskRuleLabel('refund_abuse'), '退款滥用（仅成功退款 · 近满额）')
  assert.equal(riskRuleLabel('proxy_ip_login'), '代理IP登录（需配 ASN 名单）')
  assert.equal(riskRuleLabel('register_burst'), '注册突增（同 /24 段 · 仅自助注册）')
  assert.equal(riskRuleLabel('answer_pattern_repeat'), '同题重复（搜题 · 7 天）')
  // R17 label 必须写「搜题」而不是「提交」——两者语义不同（见 Description）。
  assert.match(riskRuleLabel('answer_pattern_repeat'), /搜题/, 'R17 label 必须写明是搜题口径')
  // R5 的 label 必须写明「仅自助注册」——管理员建号不计入，不提示会让运营
  // 在后台批量建号后奇怪「为什么没告警」。
  assert.match(riskRuleLabel('register_burst'), /仅自助注册/, 'R5 label 必须写明只统计自助注册')
  // R10 的 label 必须提示「需配名单」——空名单时规则静默，
  // 不提示会让运营以为规则坏了。
  assert.match(riskRuleLabel('proxy_ip_login'), /需配/, 'R10 label 必须提示需要配置 ASN 名单')
  // R2 与 R9 的 label 必须能区分（97 号 L318 要求「区别于 R2 的同账号多次」）。
  {
    const r2 = riskRuleLabel('login_bruteforce')
    const r9 = riskRuleLabel('credential_stuffing')
    assert.notEqual(r2, r9, 'R2 与 R9 的 label 不能相同')
    assert.match(r2, /10 分钟/, 'R2 label 必须写明 10 分钟窗口')
    assert.match(r9, /30 分钟/, 'R9 label 必须写明 30 分钟窗口')
  }
  // R9 的两条边界必须同时出现在 label 里（前端不展示 Description）。
  {
    const r9 = riskRuleLabel('credential_stuffing')
    assert.match(r9, /失败登录/, 'R9 label 必须写明「仅失败登录」限制')
    assert.match(r9, /30 分钟/, 'R9 label 必须写明 30 分钟窗口')
  }
  // R3 的两条限制必须同时出现在 label 里（前端不展示 Description，
  // 只写其一 = 运营按错误边界理解告警行为）。
  {
    const r3 = riskRuleLabel('impossible_travel')
    assert.match(r3, /公网 IP/, 'R3 label 必须写明「仅公网 IP」限制')
    assert.match(r3, /7 天/, 'R3 label 必须写明「回溯 7 天」限制')
  }
  assert.equal(riskRuleLabel(''), '未知规则')
  assert.equal(riskRuleLabel('future_rule'), 'future_rule', '未知规则要原样显示，便于排错')
  assert.equal(riskSeverityMeta('high').label, '高')
  assert.equal(riskSeverityMeta('MEDIUM').label, '中')
  assert.equal(riskSeverityMeta('weird').color, 'default')
})

// 页面必须能展示**任意** ruleId 的告警，而不只是下拉清单里那几个。
//
// 这是 R1 落地时的实际要求：后端现在会写 rule_id=multi_account_ip 的告警，
// 而后续 17 条规则（R2–R18）落地时**不应该**每次都要改前端才能看到。
// 判据分两层：
//   ① riskRuleLabel 对未登记的 ruleId 原样返回（不返回「未知规则」）；
//   ② 表格列走 riskRuleLabel 派生，而不是渲染一张硬编码的规则清单。
// 变异「把 riskRuleLabel 的兜底改成 return '未知规则'」时 ① 会红。
// R16 的取舍必须在**用户可见文案**里说明，否则会被当成规则坏了。
//
// 判据来源：任务书硬要求 —— 「本规则需要至少 3 天历史数据，新部署的环境首 3 天
// 不会产生告警」。这条只写在代码注释里不算，必须出现在管理员能看到的文案上。
test('R16 的「需 3 天历史」必须在用户可见的规则名里', () => {
  const label = riskRuleLabel('offhours_activity')
  assert.match(label, /3 天/, '规则名必须写明需要 3 天历史，实际：' + label)
  assert.match(label, /历史/, '规则名必须出现「历史」字样，实际：' + label)
  // 反向对照：其它规则名不得带这个提示（否则提示失去定位作用）。
  for (const other of ['debt_user', 'ticket_flood', 'token_abuse']) {
    assert.doesNotMatch(riskRuleLabel(other), /需 3 天历史/, other + ' 不应带 3 天历史提示')
  }
})

test('规则展示对任意 ruleId 通用（新规则不必改前端）', () => {
  // 注意：这里只能列**当前未登记**的 ruleId。若某条规则后来登记了（如 impossible_travel
  // 已在 R3 落地时登记），必须从本列表移除，否则断言会红 —— 这不是测试变弱，是它在正确报警。
  for (const unregistered of ['answer_script', 'bonus_abuse', 'device_farm', 'r99_future']) {
    assert.equal(
      riskRuleLabel(unregistered),
      unregistered,
      `未登记的规则 ${unregistered} 必须原样显示（后端新增规则时前端不应变成「未知规则」）`,
    )
  }
})

test('规则展示：列表列与详情都经 riskRuleLabel 派生，不是硬编码清单', async () => {
  const page = await readSource('pages/RiskAdmin/index.vue')
  // 表格列与详情都必须经 riskRuleLabel 渲染（这样任何 ruleId 都能显示）。
  assert.match(page, /column\.key === 'ruleId'.*riskRuleLabel\(record\.ruleId\)/, '表格列必须经 riskRuleLabel 派生')
  assert.match(page, /riskRuleLabel\(detail\.ruleId\)/, '详情必须经 riskRuleLabel 派生')
  // 不得在页面里内联一份规则名映射（会与 helpers 的那份漂移）。
  const stripped = stripComments(page)
  assert.doesNotMatch(stripped, /debt_user\s*:\s*['"]/, '页面不得内联规则名映射')
})

test('列表响应解析：失败与「真的为空」必须区分开', () => {
  const failed = readRiskListBody({ status: 500, data: { message: 'boom' } })
  assert.equal(failed.ok, false)
  assert.equal(failed.total, null, '失败时 total 必须是 null（0 会被渲染成「空列表」）')
  const empty = readRiskListBody({ status: 200, data: { data: [], total: 0 } })
  assert.equal(empty.ok, true)
  assert.equal(empty.total, 0)
  const ok = readRiskListBody({ status: 200, data: { data: [{ id: 1 }, null, 'x'], total: 3 } })
  assert.deepEqual(ok.items, [{ id: 1 }], '非对象行必须被过滤，不能直接喂给表格')
})