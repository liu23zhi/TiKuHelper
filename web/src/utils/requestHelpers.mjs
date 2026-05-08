function trimText(value) {
  return String(value || '').trim();
}

export function buildAuthFailureHandling(response) {
  const detail = trimText(response?.data?.detail);
  if (detail.includes('余额不足') && detail.includes('默认账号已停用')) {
    return {
      shouldLogout: false,
      userMessage: detail,
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
