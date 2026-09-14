function trimText(value) {
  return String(value || '').trim();
}

export function buildAuthFailureHandling(response) {
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

  return {
    shouldLogout: true,
    userMessage: '当前登陆过期，请重新登陆。',
  };
}

export function buildResponsePayload(response) {
  const detail = trimText(response?.data?.detail);
  const message = trimText(response?.data?.message);

  return {
    data: response?.data,
    status: response?.status,
    message: detail || message,
  };
}
