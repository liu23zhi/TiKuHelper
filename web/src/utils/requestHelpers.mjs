function trimText(value) {
  return String(value || '').trim();
}

export function isPublicWechatRegisterRequest(url) {
  return typeof url === 'string' && url.includes('/adapter-service/public/wechat-register/')
}

export function buildAuthFailureHandling(response) {
  // Exact HTTP401 business codes are spending denials, not expired web sessions.
  // Do not infer this exception from free-form quota text or a partial code match.
  if (response?.status === 401 && response?.data?.code === 'ACCESS_TOKEN_QUOTA_EXCEEDED') {
    return {
      shouldLogout: false,
      userMessage: '当前 Key 累计额度已用尽，请调整该 Key 的额度上限或更换 Key 后重试。',
    };
  }
  if (response?.status === 401 && response?.data?.code === 'INSUFFICIENT_BALANCE') {
    return {
      shouldLogout: false,
      userMessage: '当前账户余额不足，请充值后重试。',
    };
  }
  const detail = trimText(response?.data?.detail);
  if (detail.includes('余额不足') && (detail.includes('默认账号已停用') || detail.includes('系统题库已禁用'))) {
    return {
      shouldLogout: false,
      userMessage: detail,
    };
  }
  if (detail.includes('人机验证') || detail.includes('图形验证码')) {
    return {
      shouldLogout: false,
      userMessage: detail,
    };
  }

  const message = trimText(response?.data?.message);
  if (message.includes('人机验证') || message.includes('图形验证码')) {
    return {
      shouldLogout: false,
      userMessage: message,
    };
  }

  if (response?.status === 403) {
    return {
      shouldLogout: false,
      userMessage: detail || message || '当前账号无权执行此操作。',
    };
  }

  return {
    shouldLogout: true,
    userMessage: '当前登陆过期，请重新登陆。',
  };
}

export function buildResponsePayload(response) {
  const detail = trimText(response?.data?.detail);
  const message = trimText(response?.data?.message);

  const payload = {
    data: response?.data,
    status: response?.status,
    message: detail || message,
  };
  rememberTransportRequestId(payload, response);
  return payload;
}

// Transport-only diagnostics live outside enumerable payloads and Error objects.
const transportRequestIds = new WeakMap();

function isTransportObject(value) {
  return value !== null && (typeof value === 'object' || typeof value === 'function');
}

function canonicalUnsignedWithin(value, maximum) {
  return /^(?:0|[1-9][0-9]*)$/.test(value)
    && (value.length < maximum.length || (value.length === maximum.length && value <= maximum));
}

function validTransportRequestId(value) {
  if (typeof value !== 'string' || value.length > 50) return undefined;
  if (value.length === 32 && /^[0-9a-f]{32}$/.test(value)) return value;
  const match = /^fallback-(-?(?:0|[1-9][0-9]*))-(0|[1-9][0-9]*)$/.exec(value);
  if (!match || match[0] !== value || match[1] === '-0') return undefined;
  const negative = match[1][0] === '-';
  const digits = negative ? match[1].slice(1) : match[1];
  const timeLimit = negative ? '9223372036854775808' : '9223372036854775807';
  if (!canonicalUnsignedWithin(digits, timeLimit)
    || !canonicalUnsignedWithin(match[2], '18446744073709551615')) return undefined;
  return value;
}

function rememberTransportRequestId(value, response) {
  if (!isTransportObject(value)) return;
  // A reused Error must not retain diagnostics from an older HTTP response.
  transportRequestIds.delete(value);
  try {
    const headers = response?.headers;
    let headerValue;
    if (headers && typeof headers.get === 'function') {
      headerValue = headers.get('x-request-id');
    } else if (headers && typeof headers === 'object') {
      const names = Object.keys(headers).filter(name => name.toLowerCase() === 'x-request-id');
      if (names.length !== 1) return;
      headerValue = headers[names[0]];
    }
    const id = validTransportRequestId(headerValue);
    if (id !== undefined) transportRequestIds.set(value, id);
  } catch {
    // Auxiliary diagnostics cannot replace a business response or original error.
  }
}

export function getTransportRequestId(value) {
  return isTransportObject(value) ? transportRequestIds.get(value) : undefined;
}

export function captureTransportRequestError(error) {
  if (isTransportObject(error)) transportRequestIds.delete(error);
  try {
    rememberTransportRequestId(error, error?.response);
  } catch {
    // Preserve even unusual/frozen Error objects and returnable accessor failures.
  }
  return error;
}
