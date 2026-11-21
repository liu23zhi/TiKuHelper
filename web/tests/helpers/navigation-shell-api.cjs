/* Navigation mounts the real authenticated shell. Stub its transport, not its
 * API methods: any new endpoint, mutation, body or unexpected read is a failure. */
module.exports = function navigationShellAdapter({ actor, record, deny }) {
  return async config => {
    const method = String(config.method || 'get').toLowerCase(), url = String(config.url)
    record({ method, url })
    if (method !== 'get' || config.data != null || config.params !== undefined) return deny('unexpected-shell-request')
    let data
    if (url === '/api/public/register-status') {
      data = { siteName: 'SYNTHETIC navigation' }
    } else if (url === '/api/user/session' || url === '/api/system/storage-status' || url === '/api/user/review-rebate/availability') {
      if (!(Number.isSafeInteger(actor()?.id) && actor().id > 0)) return deny('anonymous-protected-shell-read')
      if (url !== '/api/system/storage-status' && !config.signal) return deny('uncancellable-protected-read')
      if (url === '/api/user/review-rebate/availability' && ['0', 'disabled', 'banned'].includes(String(actor().status))) return deny('ineligible-menu-availability')
      data = url === '/api/user/session' ? { message: 'ok' } : url === '/api/system/storage-status' ? { driver: 'mysql' } : { enabled: false }
    } else {
      return deny('unexpected-shell-url')
    }
    return { status: 200, data, headers: {}, config, request: { memoryOnly: true } }
  }
}
