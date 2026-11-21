// Persist only the retry identifier and quote. Activation codes stay server side.
const memory = new Map()
const keyFor = id => `tiku-script-purchase-intent:${id}`

export function scriptPurchaseIntent(userID, quote) {
  const storageKey = keyFor(userID)
  let existing = memory.get(storageKey)
  try { existing = JSON.parse(localStorage.getItem(storageKey)) || existing } catch { /* storage can be disabled */ }
  if (existing && /^script-activation-purchase-[a-f0-9]{32}$/.test(existing.key) && /^\d+(?:\.\d{1,6})?$/.test(existing.quote)) return existing
  const bytes = new Uint8Array(16)
  globalThis.crypto.getRandomValues(bytes)
  const result = { key: 'script-activation-purchase-' + Array.from(bytes, value => value.toString(16).padStart(2, '0')).join(''), quote: String(quote) }
  memory.set(storageKey, result)
  try { localStorage.setItem(storageKey, JSON.stringify(result)) } catch { /* memory still protects this window */ }
  return result
}

export function clearScriptPurchaseIntent(userID) {
  const key = keyFor(userID)
  memory.delete(key)
  try { localStorage.removeItem(key) } catch { /* storage can be disabled */ }
}
