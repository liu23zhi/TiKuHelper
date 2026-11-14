// 脚本激活码管理页的纯逻辑（无 Vue / 无 DOM 依赖，可直接 node --test 覆盖）。
//
// 抽出来的理由：状态展示与批量上限这些是**业务判据**，放在 .vue 的 setup 里
// 就只能靠组件测试间接覆盖；抽成纯函数后能直接断言边界值。

// BATCH_LIMIT 必须与后端 service.MaxAdminScriptActivationCodeBatch 一致。
// 两处不同步时前端会先放行、后端再拒（用户看到的是「输入合法但保存失败」）。
export const BATCH_LIMIT = 100

// STATUS_META 是状态的中文展示与颜色。
//
// 后端返回的 status 有四种：unused / used / disabled / expired。
// expired 是**派生状态**（按 expires_at 实时算），不是库里的持久值 ——
// 这也是为什么前端不能自己按 status 字符串硬编码「是否可用」。
export const STATUS_META = {
  unused: { label: '未使用', color: 'green' },
  used: { label: '已使用', color: 'blue' },
  disabled: { label: '已禁用', color: 'orange' },
  expired: { label: '已过期', color: 'default' },
}

export function statusMeta(status) {
  return STATUS_META[status] || { label: status || '未知', color: 'default' }
}

// normalizeBatchCount 校验并规范化「生成数量」。
//
// 返回 { ok, count, message }。ok=false 时 message 直接可展示给用户。
// 非法输入（NaN / 小数 / <=0 / >上限）一律拒绝而不是静默 clamp ——
// 静默 clamp 会让管理员以为生成了 200 个、实际只有 100 个。
export function normalizeBatchCount(raw) {
  const count = Number(raw)
  if (!Number.isFinite(count)) {
    return { ok: false, count: 0, message: '请输入有效的数量' }
  }
  if (!Number.isInteger(count)) {
    return { ok: false, count: 0, message: '数量必须是整数' }
  }
  if (count < 1) {
    return { ok: false, count: 0, message: '数量至少为 1' }
  }
  if (count > BATCH_LIMIT) {
    return { ok: false, count: 0, message: `单次最多生成 ${BATCH_LIMIT} 个` }
  }
  return { ok: true, count, message: '' }
}

// masksCode 判断列表里的一条记录是否只给到了掩码（而不是全码）。
//
// 详情接口才返回 code；列表接口返回 codeMask。这个函数用于提示语，
// 避免把 codeMask 当全码展示给用户去念。
export function masksCode(item) {
  if (!item) return false
  return typeof item.codeMask === 'string' && item.codeMask.length > 0 && !item.code
}

// buildUsageTimeline 把使用记录整理成时间线文案。
//
// 用户诉求原话是「可以查看脚本一次性激活码的具体情况，例如换取令牌，
// 下载脚本等，方便客服处理工单问题」，所以每条记录都要能一眼读出
// 「什么动作、什么时候、谁、哪个脚本」。
export function buildUsageTimeline(usages) {
  if (!Array.isArray(usages) || usages.length === 0) return []
  const KIND_LABEL = { redeem: '兑换激活码', run: '运行脚本', download: '下载脚本' }
  return usages
    .slice()
    .sort((left, right) => new Date(left?.at || 0) - new Date(right?.at || 0))
    .map(usage => ({
      kind: usage?.kind || '',
      label: KIND_LABEL[usage?.kind] || usage?.kind || '未知动作',
      at: usage?.at || '',
      username: usage?.username || '',
      scriptName: usage?.scriptName || '',
      detail: usage?.detail || '',
    }))
}

// parseExpiryInput 把手填的有效期字符串解析成 RFC3339。
//
// 为什么不用 a-date-picker：本仓没有 dayjs/DatePicker 依赖（见 TikuComponent
// 的注释），引入会动 package.json 并让 CI 的 npm ci 要求 lock 同步。
//
// 语义（与 UserBan 的「空 = 永久」保持一致）：
//   - 空串 / null / undefined → { ok: true, value: null }，即「永不过期」；
//   - 能解析 → 归一化成 ISO 串；
//   - 不能解析 → { ok: false }，**不静默当作永久**。
//     把非法输入当「永久」是最坏的一种宽容：管理员以为自己设了期限，
//     实际生成了一张永不过期的码。
export function parseExpiryInput(raw) {
  if (raw === null || raw === undefined) return { ok: true, value: null, message: '' }
  const text = String(raw).trim()
  if (text === '') return { ok: true, value: null, message: '' }
  const date = new Date(text)
  if (!Number.isFinite(date.getTime())) {
    return { ok: false, value: null, message: '有效期格式无法识别，请用 YYYY-MM-DD HH:mm:ss' }
  }
  return { ok: true, value: date.toISOString(), message: '' }
}

// summarizeUsage 给详情页顶部一句结论：「有没有被用、被谁用、换了什么」。
export function summarizeUsage(detail) {
  if (!detail || !detail.code) {
    return { used: false, text: '暂无数据' }
  }
  if (!detail.hasUsage) {
    return { used: false, text: '该激活码尚未被使用' }
  }
  const timeline = buildUsageTimeline(detail.usages)
  const redeemer = timeline.find(item => item.kind === 'redeem')
  const who = redeemer?.username ? `用户 ${redeemer.username}` : '某用户'
  const script = redeemer?.scriptName ? `脚本「${redeemer.scriptName}」` : '脚本'
  const extra = timeline.filter(item => item.kind !== 'redeem').length
  return {
    used: true,
    text: `已被${who}兑换，绑定${script}${extra > 0 ? `，另有 ${extra} 条后续记录` : ''}`,
  }
}
