export function buildAccessTokenBalanceAlert(user) {
  const balance = Number(user?.balance);
  if (!Number.isFinite(balance) || balance >= 0) {
    return null;
  }

  return {
    type: 'warning',
    message: '当前余额不足',
    description: '当前余额已小于 0，系统默认账号已停用。继续调用 /adapter-service/search 时，请在请求中显式传入渠道参数或 AI 参数，否则请求会直接失败。',
  };
}
