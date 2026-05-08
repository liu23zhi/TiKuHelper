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
      iconSVG: String(item?.iconSVG || '').trim(),
      remark: String(item?.remark || '').trim(),
      sort: Number(item?.sort || 0),
    }))
    .filter((item) => item.label && item.value > 0);
}

export function normalizePaymentMethodForm(form = {}) {
  return {
    id: form.id === undefined || form.id === null || form.id === '' ? undefined : Number(form.id),
    name: String(form.name || '').trim(),
    iconSVG: String(form.iconSVG || '').trim(),
    ePayType: String(form.ePayType || '').trim(),
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

  const tokenValue = String(record.tokenValue || '').trim() || '-';
  return `参数名：${tokenKey}｜默认 Token：${tokenValue}`;
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

export function buildRechargeOrderPayload(amountValue, paymentMethodId) {
  const amount = Number(String(amountValue || '').trim());
  const methodId = Number(paymentMethodId || 0);

  if (!Number.isFinite(amount) || amount <= 0) {
    throw new Error('充值金额必须大于 0');
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
  return options.map((item) => {
    const icon = item.iconSVG
      ? `<span class="payment-option-icon">${item.iconSVG}</span>`
      : '<span class="payment-option-icon payment-option-icon--placeholder">¥</span>'
    const remark = item.remark
      ? `<div class="payment-option-remark">${item.remark}</div>`
      : ''
    return `
      <button type="button" class="payment-option" data-payment-method-id="${item.value}">
        <div class="payment-option-main">
          ${icon}
          <div>
            <div class="payment-option-title">${item.label}</div>
            ${remark}
          </div>
        </div>
        <span class="payment-option-arrow">选择</span>
      </button>
    `
  }).join('')
}
