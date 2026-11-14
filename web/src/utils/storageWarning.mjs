/**
 * SQLite 悬浮警告的纯逻辑（可 node 直测，不依赖 DOM）。
 *
 * 依据：task_doc/108:297「当使用 Sqlite 启动时候，需要给与警告」、
 * 109:474「SQLite | 冻结（启动警告 + 文档说明）」、
 * 109:486 Q7-a「警告形态」——按用户原话「悬浮警告」取 WARN + 前端悬浮横幅。
 *
 * # 为什么逻辑要单独成文件
 *
 * 「关掉之后重新登录还要再出现」这条要求，判断依据是**会话代次**，不是时间戳。
 * 时间戳方案有两个必然失败的模式：① 关掉后 5 分钟自动重现已关的警告（用户会认为关不掉）；
 * ② 关机/换设备后时间戳对不上而永久消失。会话代次（sessionRevision）恰好表达
 * 「这是新的一次登录」，与本需求语义完全一致，且本仓已有一套现成的代次实现
 * （utils/currentUser.js 的 advanceCurrentSessionRevision，登录拿到 jwt 时推进）。
 *
 * 把它抽成 .mjs 的另一个理由：这里的分支（可见/不可见 × 已关/未关 × 同代次/跨代次）
 * 是 6 种组合，塞进 .vue 就只能靠读代码推断，写不了钉子。
 */

/**
 * 关闭记录的 localStorage 键。
 *
 * 存的内容形如 `<sessionRevision>` —— **只存代次，不存时间**。
 * 这样「判定」退化成一次字符串比较，没有时钟依赖。
 */
export const STORAGE_WARNING_DISMISS_KEY = 'adapter_storage_warning_dismissed_revision'

/**
 * 悬浮警告的显示判据。
 *
 * 三条同时成立才显示：
 *  1. driver 是 sqlite —— 这是**唯一**的显示条件，不看 production。
 *     （生产 + sqlite 是最危险组合，但 development + sqlite 同样需要提示，
 *       文档说的是「使用 SQLite 就警告」，没有区分模式。）
 *  2. warning 非空 —— 文案由后端下发，前端不自己编。空文案意味着后端
 *     认为无需告知，此时前端不该凭 driver 猜一句话出来。
 *  3. 未被本次会话关掉 —— 见 shouldRespectDismiss。
 *
 * 返回的可显示性**不做**「production 才显示」的加强：那是把需求收窄。
 */
export function shouldShowStorageWarning(status) {
  if (!status || typeof status !== 'object') return false
  if (status.driver !== 'sqlite') return false
  const warning = typeof status.warning === 'string' ? status.warning.trim() : ''
  return warning !== ''
}

/**
 * 判断关闭记录是否仍然有效（有效 = 应该继续隐藏）。
 *
 * 两个入参都是字符串：
 *  - dismissedRevision：关闭当时记下的会话代次（读不到就是空串）
 *  - currentRevision：当前的会话代次
 *
 * **空值语义（这里是最容易写反的地方）**：
 *  - dismissedRevision 为空 → 没有关闭记录 → 不隐藏（返回 false）。
 *    不能把它当成「关过了」—— 那会让第一次进入的用户直接看不到警告。
 *  - currentRevision 为空 → 无法判定代次。此时选择**不隐藏**：
 *    宁可多显示一次，也不要因为读取不到存储就让警告永久消失
 *    （「一次误关就永久消失」正是任务书点名要避免的失败形态）。
 *  - 两者相等 → 同一次会话内关的 → 隐藏（返回 true）。
 *  - 两者不等 → 已经重新登录（代次被推进）→ 不隐藏（返回 false）。
 */
export function shouldRespectDismiss(dismissedRevision, currentRevision) {
  const dismissed = typeof dismissedRevision === 'string' ? dismissedRevision.trim() : ''
  const current = typeof currentRevision === 'string' ? currentRevision.trim() : ''
  if (!dismissed) return false
  if (!current) return false
  return dismissed === current
}

/**
 * 把「该不该显示」与「是否已关闭」合成最终可见性。
 *
 * 拆成三步而不是一个大 if：出问题时能一眼看出是「不该显示」还是「被关掉了」。
 */
export function resolveStorageWarningVisibility(status, dismissedRevision, currentRevision) {
  if (!shouldShowStorageWarning(status)) return false
  return !shouldRespectDismiss(dismissedRevision, currentRevision)
}

/**
 * 悬浮警告的无障碍播报文本。
 *
 * role="status" 需要一段稳定的纯文本；把后端文案与行动指引分开，
 * 便于读屏先读结论再读建议。
 */
export function buildStorageWarningAnnouncement(status) {
  if (!shouldShowStorageWarning(status)) return ''
  const warning = String(status.warning).trim()
  return `存储警告：${warning}`
}
