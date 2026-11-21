// 旧默认协议的落款是展示占位，不属于用户签署的条款。历史快照保留原文，
// 展示时由真实签名图像和服务端记录的时间替代这两行。
export function refundAgreementBody(text) {
  return String(text || '').replace(/\r\n?/g, '\n')
    .replace(/\n*申请人（以电子签名方式确认）：见签名图像\s*\n签署日期：提交本次退款申请之日\s*$/u, '').trim()
}
