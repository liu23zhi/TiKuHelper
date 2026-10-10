/**
 * 金额展示工具。
 *
 * 后端余额与账务金额统一保留 6 位小数（见 pkg/util/money.go 的 MoneyPrecision），
 * 因为 AI 单次计费可能只有 0.000062 元这种量级，用 2 位小数会被抹成 0。
 * 展示规则：最多 6 位小数，去掉末尾多余的 0，既简洁又能看到小额扣费。
 */

/** 与后端 MoneyPrecision 保持一致。 */
export const MONEY_DECIMALS = 6;

/** 把金额格式化为「最多 6 位小数、去尾零」的纯数字字符串（不带货币符号）。 */
export function formatMoneyValue(value) {
  const amount = Number(value);
  if (!Number.isFinite(amount)) {
    return '0';
  }
  // toFixed 天然完成 6 位四舍五入；随后去掉末尾的 0 与孤立的小数点。
  const fixed = amount.toFixed(MONEY_DECIMALS);
  const trimmed = fixed.replace(/0+$/, '').replace(/\.$/, '');
  // 避免出现 "-0"。
  return trimmed === '-0' || trimmed === '' ? '0' : trimmed;
}

/** 人民币展示，固定带 ￥ 前缀。 */
export function formatMoneyCny(value) {
  return `￥${formatMoneyValue(value)}`;
}

/** 人民币展示，带 ¥ 前缀与空格分隔（与既有账单页文案保持一致）。 */
export function formatMoneyYuanWithSpace(value) {
  return `¥ ${formatMoneyValue(value)}`;
}
