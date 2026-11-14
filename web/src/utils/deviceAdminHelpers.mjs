/**
 * 设备管理页的纯展示 helper（137 设备体系 §9.2）。
 *
 * 为什么单独抽一个 .mjs：本仓的 `npm run test:unit` 跑的是
 * `node --test src/utils/*.test.mjs`，而 `web/node_modules` 在这个 worktree 里
 * 是空的（无 vue），所以**页面逻辑里能被自动验证的部分必须是不依赖 vue 的纯函数**。
 * index.vue 只做「取值 → 交给这里格式化」。
 *
 * 三件事必须在纯函数层钉死（每条都对应一个静默坏掉的形态）：
 *
 *  ① statusText/statusColor：后端返回三种状态（active / revoked / banned），
 *     页面若把未知状态渲染成「使用中」，管理员会以为封禁没生效。
 *  ② banned 的优先级必须最高：一台设备被封禁后它的绑定也被撤销了（D12），
 *     如果只按 revoked_at 渲染成「已解绑」，运营会去重复点封禁。
 *  ③ 永久封禁必须显示「永久」而不是「—」：两者是相反语义
 *     （「没填」vs「永不自动解除」），显示成空会被读成数据缺失。
 */

/** 状态固定顺序（页面按这个顺序给筛选下拉开项，避免顺序随数据抖动）。 */
export const DEVICE_STATUS_ROWS = Object.freeze([
  { key: 'active', label: '使用中', color: 'green', hint: '绑定在用（revoked_at 为空）' },
  { key: 'revoked', label: '已解绑', color: 'default', hint: '绑定已撤销（用户移除 / 管理员解绑 / 闲置清理）' },
  { key: 'banned', label: '封禁中', color: 'red', hint: '设备在全局黑名单里，用户无法自行解除' },
]);

/** 撤销原因的中文文案（后端写的是稳定英文串，见 137 §3.2）。 */
const REVOKE_REASON_TEXT = Object.freeze({
  user_removed: '用户自己移除',
  admin_unbound: '管理员强制解绑',
  idle_10d: '闲置 10 天自动解绑',
  device_banned: '设备被全局封禁',
});

/**
 * 状态的中文文案。
 *
 * 未知状态**原样返回**而不是「未知」：后端加了新状态时，显示原文才能让人去查，
 * 显示「未知」等于把线索删掉（与 scanStateText 同一取舍）。
 */
export function deviceStatusText(status) {
  const key = String(status ?? '').trim().toLowerCase();
  if (!key) return '—';
  const row = DEVICE_STATUS_ROWS.find(candidate => candidate.key === key);
  return row ? row.label : key;
}

/** 状态的 antd tag 颜色。未知状态用 default（不假装正常也不假装违规）。 */
export function deviceStatusColor(status) {
  const key = String(status ?? '').trim().toLowerCase();
  const row = DEVICE_STATUS_ROWS.find(candidate => candidate.key === key);
  return row ? row.color : 'default';
}

/**
 * 撤销原因文案。
 *
 * 空串（未撤销）返回 '—'；未知原因原样返回。
 */
export function revokeReasonText(reason) {
  const key = String(reason ?? '').trim();
  if (!key) return '—';
  return REVOKE_REASON_TEXT[key] || key;
}

/**
 * 到期时间的展示文案。
 *
 * 判据 ③：`expiresAt` 为空 = **永久**，必须显示「永久」。
 * 传 '—' 或空串会让运营把「永久封禁」读成「数据缺失」，进而重复处置。
 */
export function banExpiryText(expiresAt, formatter) {
  if (expiresAt === null || expiresAt === undefined) return '永久';
  const text = String(expiresAt).trim();
  if (!text) return '永久';
  const format = typeof formatter === 'function' ? formatter : defaultDateTimeFormatter;
  const formatted = format(text);
  return formatted || '永久';
}

function defaultDateTimeFormatter(value) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return '';
  return date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' });
}

/**
 * 设备 ID 的短展示（前 10 + 末 6）。
 *
 * 设备 ID 是长随机串，整串会撑破表格列；但**两端都要留**：
 * 前段是设备类型前缀（`exe:`）与序号，末段是同前缀设备的区分位。
 */
export function shortDeviceId(deviceId) {
  const text = String(deviceId ?? '').trim();
  if (!text) return '—';
  if (text.length <= 20) return text;
  return `${text.slice(0, 10)}…${text.slice(-6)}`;
}

/**
 * 读取列表响应的分页数字。
 *
 * 后端 JSON 里 total 可能是 number、也可能是字符串（不同驱动），两种都要吃；
 * 负数/非数字返回 null，让调用方显式走失败分支而不是渲染成 0。
 */
export function readDeviceCount(value) {
  if (value === null || value === undefined) return null;
  if (typeof value === 'boolean') return null;
  if (typeof value !== 'number' && typeof value !== 'string') return null;
  const text = String(value).trim();
  if (text === '') return null;
  const number = Number(text);
  if (!Number.isFinite(number) || number < 0) return null;
  return Math.trunc(number);
}

/**
 * 从一行设备记录派生「是否可解绑」。
 *
 * 三条闸门缺一不可：
 *  - 必须已经有绑定行（id > 0）；
 *  - 必须是「在用」（revoked_at 为空）——已解绑的行再解一次没有语义；
 *  - 表结构必须支持撤销（revocationSupported），否则点了必然 503。
 *
 * 封禁态**不影响**解绑按钮：被封禁的设备也可能还留着未撤销的绑定行
 * （例如先解绑失败、后补封禁），运营需要能补上解绑。
 */
export function canUnbindDevice(record, revocationSupported) {
  if (!revocationSupported) return false;
  const id = Number(record?.id);
  if (!Number.isInteger(id) || id <= 0) return false;
  return record?.revokedAt === null || record?.revokedAt === undefined;
}

/**
 * 从一行设备记录派生「解绑为什么不可用」。
 *
 * 必须给出**具体原因**而不是笼统的「不可用」：
 * 「结构不支持的库」要去跑迁移，「已解绑」不需要任何动作，
 * 两者的下一步完全不同。
 */
export function unbindBlockedHint(record, revocationSupported) {
  if (!revocationSupported) return '当前数据库缺少 revoked_at / revoked_reason 列，需先执行迁移（A1-2）'
  const id = Number(record?.id)
  if (!Number.isInteger(id) || id <= 0) return '该设备没有绑定记录，无可解绑对象'
  return '该绑定已解绑，无需重复操作'
}

/**
 * 从一行设备记录派生「封禁/解封」哪个动作可用。
 *
 * 返回 'unban'（当前已封禁）/ 'ban'（当前未封禁）。
 *
 * ⚠️ 判据用后端给的 `banned` 布尔，**不是** status 字符串：
 * status 是三态派生的，未来若加第四态（例如「永久封禁」单列），
 * 用它判动作会把新态误判成「未封禁」而给出错误的按钮。
 */
export function deviceBanAction(record) {
  return record?.banned === true ? 'unban' : 'ban'
}
