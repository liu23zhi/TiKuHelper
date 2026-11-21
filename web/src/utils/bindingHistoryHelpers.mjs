// Normalize the admin binding history response while preserving request failure
// as a separate state from a confirmed empty history.
export function readAdminBindingHistory(response) {
  const body = response?.data
  const list = Array.isArray(body?.bindings?.items)
    ? body.bindings.items
    : Array.isArray(body?.items)
      ? body.items
      : Array.isArray(body) ? body : null
  if (response?.status !== 200 || !list) return { ok: false, rows: [] }
  return { ok: true, rows: list.filter(item => item && typeof item === 'object').map(item => ({ ...item })) }
}
