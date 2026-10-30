import http from 'node:http'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const dist = fileURLToPath(new URL('../dist/', import.meta.url))
export async function startExtensionUpdateFixture() {
  let config = { autoUpdate: true, intervalMinutes: 360, edgeId: 'lcmeenlnjihhjcjoakekikoaldigonac', chromeId: '' }
  let version = '1.2.3', lastError = '', failRefresh = false
  const requests = []
  const state = () => ({ config, sources: ['edge', 'chrome'].map(source => ({
    source, configured: !!config[`${source}Id`], extensionId: config[`${source}Id`], available: source === 'edge', updating: false,
    lastCheckedAt: '2026-10-30T08:00:00Z', lastError: source === 'edge' ? lastError : '',
    ...(source === 'edge' ? { release: { version, size: 1234567, sha256: 'a'.repeat(64), checkedAt: '2026-10-30T08:00:00Z', updatedAt: '2026-10-30T07:00:00Z' } } : {}),
  })) })
  const server = http.createServer(async (req, res) => {
    try {
      const json = (status, value) => { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(value)) }
      if (req.url.startsWith('/adapter-service/')) {
        const body = Buffer.concat(await Array.fromAsync(req)).toString('utf8')
        requests.push({ url: req.url, method: req.method, body: body ? JSON.parse(body) : undefined })
        if (req.url === '/adapter-service/public/register-status') return json(200, { siteName: '题库管理测试', registerEnabled: false })
        if (req.url === '/adapter-service/extensions/config') {
          if (req.method === 'PUT') config = JSON.parse(body)
          return json(200, state())
        }
        if (req.url === '/adapter-service/extensions/edge/refresh') {
          await new Promise(resolve => setTimeout(resolve, 150))
          if (failRefresh) { lastError = 'C:\\private\\secret.key mock upstream error'; return json(502, { message: lastError }) }
          version = '1.2.4'; lastError = ''; return json(200, state().sources[0].release)
        }
        return json(404, { message: 'mock route not found' })
      }
      const url = new URL(req.url, 'http://127.0.0.1')
      const name = url.pathname.startsWith('/js/') || url.pathname.startsWith('/css/') || url.pathname === '/favicon.ico' ? url.pathname.slice(1) : 'index.html'
      const filename = path.resolve(dist, name)
      if (!filename.startsWith(dist)) { res.writeHead(403); return res.end() }
      let bytes = await readFile(filename)
      if (name === 'index.html') bytes = Buffer.from(bytes.toString().replace('<head>', '<head><script>localStorage.setItem("adapter_current_user", JSON.stringify({id:1,username:"mock-admin",role:"admin"}));</script>'))
      res.writeHead(200, { 'Content-Type': name.endsWith('.js') ? 'application/javascript' : name.endsWith('.css') ? 'text/css' : name.endsWith('.ico') ? 'image/x-icon' : 'text/html' }); res.end(bytes)
    } catch { res.writeHead(500); res.end('mock fixture failed') }
  })
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  return { url: `http://127.0.0.1:${server.address().port}/adapter/extension-updates`, requests, setRefreshFailure(value) { failRefresh = value }, close: () => new Promise(resolve => server.close(resolve)) }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const fixture = await startExtensionUpdateFixture()
  console.log(fixture.url)
  process.on('SIGINT', async () => { await fixture.close(); process.exit(0) })
}
