// 「我的脚本」通知区的纯函数层（2026-11-17 用户需求）。
//
// 用户原话：「需要在我的脚本添加一个通知区，显示因为没有心跳被撤销令牌等信息」。
//
// # 为什么单独一层 .mjs
//
// `npm run test:unit` 只跑 `src/utils/*.test.mjs`，写在 index.vue 里就没有单测入口。
// 而通知区的判据全是纯计算（该不该显示、显什么级别、文案怎么排），不需要 DOM。
//
// # 三条纪律
//
//  1. **不引用后端的常量字符串**：这里写字面量。后端枚举漂移时前端测试应当红，
//     而不是跟着一起漂移成恒真（本仓踩过「期望值与被测实现共享常量导致断言恒真」）。
//  2. **后端说 none / 没这个字段，前端一律不渲染**。通知区的价值在于稀少而准确，
//     多显示一条假提示会让用户学会无视整个区域。
//  3. **不认识的值按 info 处理，且原样保留 reason**。将来后端加新原因时，
//     旧前端至少能显示「已撤销」，而不是白屏。

/** 后端 ScriptNotice* 三档的字面量镜像（刻意手写）。 */
export const SCRIPT_NOTICE_NONE = 'none';
export const SCRIPT_NOTICE_INFO = 'info';
export const SCRIPT_NOTICE_WARNING = 'warning';

/** 已知原因 → 前端补充说明。未知原因不做映射，保留后端文案。 */
const KNOWN_REASON_HINTS = new Map([
  ['heartbeat_timeout', '客户端心跳中断'],
  ['user_closed', '你手动关闭了脚本'],
  ['admin_revoked', '管理员撤销'],
]);

/**
 * 把后端返回的 revokeNotice 归一化成可直接渲染的视图模型。
 *
 * @param {unknown} raw 后端列表项里的 revokeNotice（可能是 undefined / null / 非法对象）
 * @returns {{visible: boolean, level: string, reason: string, message: string, hint: string}}
 */
export function normalizeScriptRevokeNotice(raw) {
  const hidden = { visible: false, level: SCRIPT_NOTICE_NONE, reason: '', message: '', hint: '' };
  if (!raw || typeof raw !== 'object') return hidden;

  const level = typeof raw.level === 'string' ? raw.level.trim() : '';
  // none / 空 / 无法识别的级别都按「不显示」处理：
  // 猜一个级别去渲染，比不渲染更容易误导用户。
  if (level === SCRIPT_NOTICE_NONE || level === '') return hidden;

  const reason = typeof raw.reason === 'string' ? raw.reason.trim() : '';
  const message = typeof raw.message === 'string' ? raw.message.trim() : '';

  // 已知级别只放行 info / warning；其它值降级为 info（可见但不用警示色）。
  const safeLevel = level === SCRIPT_NOTICE_WARNING ? SCRIPT_NOTICE_WARNING : SCRIPT_NOTICE_INFO;

  return {
    visible: true,
    level: safeLevel,
    reason,
    // 后端没给文案时给一句兜底，避免出现「有个空提示条」。
    message: message || '本次答题令牌已撤销。',
    hint: KNOWN_REASON_HINTS.get(reason) || '',
  };
}

/**
 * 从脚本列表里挑出所有需要展示的通知（用于页面顶部聚合展示）。
 *
 * 判据：只收 visible 的项，且带上脚本名以区分「哪个脚本出问题了」——
 * 同一用户可能有多个脚本，只说「令牌被撤销」而不说是哪个，用户无从下手。
 *
 * @param {Array<unknown>} items 脚本列表
 * @returns {Array<{scriptId: number, scriptName: string, level: string, reason: string, message: string, hint: string}>}
 */
export function collectScriptRevokeNotices(items) {
  if (!Array.isArray(items)) return [];
  const out = [];
  for (const item of items) {
    if (!item || typeof item !== 'object') continue;
    const notice = normalizeScriptRevokeNotice(item.revokeNotice);
    if (!notice.visible) continue;
    const rawId = Number(item.id);
    out.push({
      scriptId: Number.isFinite(rawId) ? rawId : 0,
      scriptName: typeof item.name === 'string' && item.name.trim() ? item.name.trim() : '未命名的脚本',
      level: notice.level,
      reason: notice.reason,
      message: notice.message,
      hint: notice.hint,
    });
  }
  // warning 排前面：那是唯一需要用户行动的形态，不能埋在一堆 info 中间。
  const weight = { warning: 0, info: 1 };
  out.sort((a, b) => (weight[a.level] ?? 2) - (weight[b.level] ?? 2));
  return out;
}

/** 聚合用的 ant-design-vue alert 类型。warning→warning，其余→info。 */
export function scriptNoticeAlertType(level) {
  return level === SCRIPT_NOTICE_WARNING ? 'warning' : 'info';
}
