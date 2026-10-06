export function buildAccessTokenBalanceAlert(user) {
  const balance = Number(user?.balance);
  if (!Number.isFinite(balance) || balance > 0) {
    return null;
  }

  const description = balance < 0
    ? '当前余额不足，系统题库已禁用。仍可在请求中显式传入渠道参数或 AI 参数。'
    : '当前余额已不足，搜索服务已暂停。请先充值后再调用 /adapter-service/search。';

  return {
    type: 'warning',
    message: '当前余额不足',
    description,
  };
}

export function buildAccessTokenListParams(showAllTokens) {
  return showAllTokens ? { scope: 'all' } : {};
}
