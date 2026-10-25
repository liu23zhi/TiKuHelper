import { parseSafePaymentIcon } from './safePaymentIcon.mjs'

export function sortPaymentMethodsForDisplay(rows = []) {
  return [...rows].sort((a, b) => {
    const enabledA = !!a?.enabled;
    const enabledB = !!b?.enabled;

    if (enabledA !== enabledB) {
      return enabledA ? -1 : 1;
    }

    const sortA = Number(a?.sort ?? 0);
    const sortB = Number(b?.sort ?? 0);
    if (sortA !== sortB) {
      return sortA - sortB;
    }

    return Number(a?.id ?? 0) - Number(b?.id ?? 0);
  });
}

export function buildRechargePaymentMethodOptions(rows = []) {
  return sortPaymentMethodsForDisplay(rows)
    .filter((item) => !!item?.enabled)
    .map((item) => ({
      label: String(item?.name || '').trim(),
      value: Number(item?.id || 0),
      iconSVG: String(item?.iconSvg ?? item?.iconSVG ?? '').trim(),
      remark: String(item?.remark || '').trim(),
      sort: Number(item?.sort || 0),
    }))
    .filter((item) => item.label && item.value > 0);
}

export function normalizePaymentMethodForm(form = {}) {
  return {
    id: form.id === undefined || form.id === null || form.id === '' ? undefined : Number(form.id),
    name: String(form.name || '').trim(),
    iconSvg: String(form.iconSvg ?? form.iconSVG ?? '').trim(),
    epayType: String(form.epayType ?? form.ePayType ?? '').trim(),
    enabled: !!form.enabled,
    sort: Number(form.sort || 0),
    remark: String(form.remark || '').trim(),
  };
}

export function buildSearchAccountDefaultParameterHint(record = {}) {
  const provider = String(record.provider || '').trim().toLowerCase();
  if (provider === 'ai') {
    return 'AI 搜索的模型与默认参数请前往 AI 搜索配置页单独维护。';
  }

  if (provider === 'local') {
    return '本地题库不使用默认参数，但调用 search 时必须携带有效访问令牌。';
  }

  const tokenKey = String(record.tokenKey || '').trim();
  if (!tokenKey) {
    return '当前题库无需默认参数。';
  }

  return `参数名：${tokenKey}`;
}

export function buildSearchAccountUnitPriceHint(record = {}) {
  const provider = String(record.provider || '').trim().toLowerCase();
  if (provider === 'ai') {
    return 'AI 搜索价格请前往 AI 搜索配置页单独维护。';
  }

  const flatPrice = Number(record.flatPrice || 0);
  return `单次价格：¥ ${flatPrice.toFixed(8)}`;
}

export function buildSearchAccountConfigSummary(record = {}) {
  const provider = String(record.provider || '').trim().toLowerCase();
  if (provider === 'ai') {
    return 'AI 搜索的模型、Token 与价格请前往 AI 搜索配置页单独维护。';
  }

  return `${buildSearchAccountDefaultParameterHint(record)}｜${buildSearchAccountUnitPriceHint(record)}`;
}

export function buildSearchAccountConfigSavePayloads(rows = []) {
  return (rows || [])
    .filter((item) => String(item?.provider || '').trim().toLowerCase() !== 'ai')
    .map((item) => ({
      provider: String(item?.provider || '').trim(),
      payload: {
        tokenValue: String(item?.tokenValue || '').trim(),
        enabled: !!item?.enabled,
        remark: String(item?.remark || '').trim(),
        flatPrice: Number(item?.flatPrice || 0),
      },
    }))
    .filter((item) => item.provider);
}

/** 充值金额允许的小数位数：支付渠道只支持「分」。 */
export const RECHARGE_DECIMALS = 2

function hasMoreDecimalsThan(amount, decimals) {
  const scale = 10 ** decimals
  // 1e-9 容差，避免浮点表示误差把合法的 12.34 判成超精度。
  return Math.abs(amount * scale - Math.round(amount * scale)) > 1e-9
}

/**
 * 把充值输入框的原始文本整理成合法金额（供 a-input-number 的 parser 使用）。
 *
 * 目的是在「输入过程中」就挡住非法内容，而不是等失焦才收拢：
 * - 只保留数字与一个小数点（去掉字母、负号、多个小数点、空格等）；
 * - 小数部分最多保留 decimals 位，多敲的位直接丢弃；
 * - 保留用户正在输入的中间态（"" / "." / "12."），否则光标与输入会跳。
 */
export function sanitizeRechargeAmountInput(rawValue, decimals = RECHARGE_DECIMALS) {
  const maxDecimals = Number.isInteger(decimals) && decimals >= 0 ? decimals : RECHARGE_DECIMALS
  const digitsAndDots = String(rawValue ?? '').replace(/[^\d.]/g, '')
  if (!digitsAndDots) {
    return ''
  }

  const firstDotIndex = digitsAndDots.indexOf('.')
  if (firstDotIndex < 0) {
    return digitsAndDots
  }

  const integerPart = digitsAndDots.slice(0, firstDotIndex)
  // 第二个及之后的小数点直接丢弃。
  const decimalPart = digitsAndDots.slice(firstDotIndex + 1).replace(/\./g, '')
  if (maxDecimals === 0) {
    return integerPart
  }

  const cappedDecimals = decimalPart.slice(0, maxDecimals)
  // 保留 "12." 这种中间态，用户才能继续输入小数。
  return `${integerPart}.${cappedDecimals}`
}

/** 充值输入框的展示格式化：不做千分位，避免光标位置跳动。 */
export function formatRechargeAmountInput(rawValue, decimals = RECHARGE_DECIMALS) {
  return sanitizeRechargeAmountInput(rawValue, decimals)
}

/**
 * 判断某个按键是否会让金额超出 decimals 位小数，用于在 keydown 阶段直接拦截。
 * 只有在「已经存在小数点且小数位已达上限」时，才阻止继续输入数字或小数点。
 */
export function shouldBlockRechargeAmountKey(currentValue, key, decimals = RECHARGE_DECIMALS) {
  const maxDecimals = Number.isInteger(decimals) && decimals >= 0 ? decimals : RECHARGE_DECIMALS
  const text = String(currentValue ?? '')

  if (key === '.') {
    return text.includes('.')
  }
  if (!/^\d$/.test(String(key ?? ''))) {
    return false
  }

  const dotIndex = text.indexOf('.')
  if (dotIndex < 0) {
    return false
  }
  return text.length - dotIndex - 1 >= maxDecimals
}

export function buildRechargeOrderPayload(amountValue, paymentMethodId) {
  const amount = Number(String(amountValue || '').trim());
  const methodId = Number(paymentMethodId || 0);

  if (!Number.isFinite(amount) || amount <= 0) {
    throw new Error('充值金额必须大于 0');
  }
  // 第三方支付渠道只支持「分」，充值金额限制为 2 位小数（余额仍是 6 位精度）。
  if (hasMoreDecimalsThan(amount, RECHARGE_DECIMALS)) {
    throw new Error('充值金额最多支持两位小数');
  }
  if (!Number.isInteger(methodId) || methodId <= 0) {
    throw new Error('请选择有效的支付方式');
  }

  return {
    amount,
    paymentMethodId: methodId,
  };
}

export function buildPaymentMethodSwalHtml(options = []) {
  const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]))
  const serialize = node => `<${node.tag}${Object.entries(node.attrs).map(([key, value]) => ` ${key}="${escape(value)}"`).join('')}>${node.children.map(serialize).join('')}</${node.tag}>`
  return options.map((item) => {
    const safeIcon = parseSafePaymentIcon(item.iconSVG)
    const icon = safeIcon
      ? `<span class="payment-option-icon">${serialize(safeIcon)}</span>`
      : '<span class="payment-option-icon payment-option-icon--placeholder">¥</span>'
    const remark = item.remark
      ? `<div class="payment-option-remark">${escape(item.remark)}</div>`
      : ''
    return `
      <button type="button" class="payment-option" data-payment-method-id="${escape(item.value)}">
        <div class="payment-option-main">
          ${icon}
          <div>
            <div class="payment-option-title">${escape(item.label)}</div>
            ${remark}
          </div>
        </div>
        <span class="payment-option-arrow">选择</span>
      </button>
    `
  }).join('')
}
