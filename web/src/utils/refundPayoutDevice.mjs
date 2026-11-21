// The server keeps only the public key. A remembered image is unlocked by proving
// possession of this browser's private key against a fresh, one-use challenge.
const storageKey = userId => `tiku.refund.payoutQr.device.${Number(userId)}`
const algorithm = { name: 'ECDSA', namedCurve: 'P-256' }

function subtle() {
  if (!globalThis.crypto?.subtle) throw new Error('当前浏览器无法验证本机身份，请使用 HTTPS 或更新浏览器')
  return globalThis.crypto.subtle
}
export function bytesToBase64(bytes) {
  return btoa(Array.from(new Uint8Array(bytes), byte => String.fromCharCode(byte)).join(''))
}
export function base64ToBytes(value) {
  return Uint8Array.from(atob(value), character => character.charCodeAt(0))
}
export async function createRefundPayoutDeviceKey() {
  const pair = await subtle().generateKey(algorithm, true, ['sign', 'verify'])
  const publicKey = bytesToBase64(await subtle().exportKey('spki', pair.publicKey))
  const privateKey = await subtle().exportKey('jwk', pair.privateKey)
  return { publicKey, privateKey }
}
export function saveRefundPayoutDeviceKey(userId, keyId, privateKey) {
  // Called only after the server has accepted the image and its public key.
  try {
    window.localStorage.setItem(storageKey(userId), JSON.stringify({ keyId, privateKey }))
    window.localStorage.removeItem(`tiku.refund.payoutQr.${Number(userId)}`)
    return true
  } catch { return false }
}
export function loadRefundPayoutDeviceKey(userId, keyId) {
  try {
    window.localStorage.removeItem(`tiku.refund.payoutQr.${Number(userId)}`)
    const saved = JSON.parse(window.localStorage.getItem(storageKey(userId)) || 'null')
    return keyId && saved?.keyId === keyId && saved?.privateKey?.kty === 'EC' ? saved.privateKey : null
  } catch { return null }
}
export function clearRefundPayoutDeviceKey(userId) {
  try {
    window.localStorage.removeItem(storageKey(userId))
    window.localStorage.removeItem(`tiku.refund.payoutQr.${Number(userId)}`)
  } catch { /* Server deletion remains authoritative. */ }
}
export async function signRefundPayoutChallenge(privateKey, challenge) {
  const key = await subtle().importKey('jwk', privateKey, algorithm, false, ['sign'])
  // Web Crypto ECDSA returns IEEE P1363 (raw r || s), as expected by the API.
  return bytesToBase64(await subtle().sign({ name: 'ECDSA', hash: 'SHA-256' }, key, base64ToBytes(challenge)))
}
