const { test } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const os = require('node:os')
const crypto = require('node:crypto')
const Module = require('node:module')
const http = require('node:http')
const https = require('node:https')
const { spawn } = require('node:child_process')

// Real Vue/Antd/upload lifecycle and parser API. The only replacements are
// loopback-only browser transports, fixture identity, router and a write trap.
// jsdom is not a Chromium layout, real Cookie/JWT or document-recognition test.
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(process.env.UI_TEST_HELPER || path.resolve(__dirname, '../helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick, h } = require('vue')
const Antd = require('ant-design-vue')
const zhCN = require('ant-design-vue/lib/locale/zh_CN').default
const axios = require('axios')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const hash = value => crypto.createHash('sha256').update(value).digest('hex')
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))
const A = { id: 93001, role: 'admin', username: 'S03_FIXTURE_A' }
const B = { id: 93002, role: 'admin', username: 'S03_FIXTURE_B' }
const CRC = 'S03+中/value%'
const sourceFiles = ['src/pages/IntelligentImport/index.vue', 'src/api/api.js', 'src/api/TikuComponentApi.js', 'src/utils/currentUser.js', 'src/utils/requestRetry.mjs', 'src/utils/useUnsavedDraft.js', 'src/utils/uitls.js']
const sourceHashes = () => Object.fromEntries(sourceFiles.map(name => [path.join(root, name), hash(fs.readFileSync(path.join(root, name)))]))
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function eventually(predicate, label, timeout = 8000) {
  const end = Date.now() + timeout
  while (Date.now() < end) { await settle(); if (predicate()) return; await sleep(10) }
  assert.fail('Timed out: ' + label)
}
function button(container, text) {
  const found = [...container.querySelectorAll('button')].find(node => node.textContent.replace(/\s/g, '') === text.replace(/\s/g, ''))
  assert.ok(found, 'actual button ' + text)
  return found
}
function mockModule(filename, exports) {
  const previous = require.cache[filename]
  const module = new Module(filename); module.loaded = true; module.exports = exports; require.cache[filename] = module
  return () => { if (previous) require.cache[filename] = previous; else delete require.cache[filename] }
}

test('S03 actual upload/parse/reparse UI -> loopback production proxy -> local upstream, failure recovery and owner retirement', { timeout: 150000 }, async t => {
  const binary = process.env.TIKU_S03_BRIDGE_BINARY
  assert.ok(binary && path.isAbsolute(binary) && fs.existsSync(binary), 'TIKU_S03_BRIDGE_BINARY must be a real absolute compiled controller.test; no skip or mock fallback')
  const evidenceRoot = process.env.TIKU_S03_EVIDENCE_DIR || path.join(os.tmpdir(), 'tiku-s03-native-evidence')
  fs.mkdirSync(evidenceRoot, { recursive: true })
  const evidence = fs.mkdtempSync(path.join(evidenceRoot, 'attempt-'))
  const readyFile = path.join(evidence, 'bridge-ready.json')
  const save = (name, data) => fs.writeFileSync(path.join(evidence, name), typeof data === 'string' ? data : JSON.stringify(data, null, 2) + '\n')
  const beforeHashes = sourceHashes(), trace = [], adapters = [], snapshots = [], dom = [], traps = [], nativeErrors = [], notices = []
  const originals = { httpRequest: http.request, httpGet: http.get, httpsRequest: https.request, httpsGet: https.get, fetch: globalThis.fetch, windowFetch: window.fetch, xhr: window.XMLHttpRequest, globalXHR: globalThis.XMLHttpRequest, formData: globalThis.FormData, blob: globalThis.Blob, file: globalThis.File, webSocket: window.WebSocket, axiosCreate: axios.create }
  const restoreModules = [], wrappers = []
  let ready, bridge, exited, exitPromise, childOut = '', childErr = '', shutdown = false, passed = 0, fileNumber = 0
  function trap(kind, detail) { traps.push({ kind, detail }); throw new Error('S03 forbidden ' + kind + ': ' + detail) }
  async function wire(method, pathname, body, headers = {}) {
    assert.ok(ready, 'Go bridge ready')
    const url = new URL(pathname, ready.url)
    assert.equal(url.origin, new URL(ready.url).origin)
    assert.match(url.hostname, /^(?:127\.0\.0\.1|localhost|\[::1\])$/)
    assert.match(url.pathname, /^(?:\/sqp\/api\/(?:upload|parse)|\/__fixture__\/(?:mode|snapshot|release|shutdown))$/)
    const fixture = url.pathname.startsWith('/__fixture__/')
    const record = { method, path: url.pathname, rawQuery: url.search.slice(1), body: body === undefined ? null : String(body), headers, actor: user.getStoredCurrentUser()?.id || null, fixture }
    trace.push(record)
    return new Promise((resolve, reject) => {
      const req = originals.httpRequest.call(http, url, { method, headers: { 'X-S03-Nonce': ready.nonce, 'X-S03-Actor': 'admin', Authorization: 'S03_SYNTHETIC_AUTH', 'Proxy-Authorization': 'S03_SYNTHETIC_PROXY_AUTH', Cookie: 'S03_SYNTHETIC_COOKIE=private', ...headers, ...(body === undefined ? {} : { 'Content-Length': Buffer.byteLength(body) }) }, timeout: 8000 }, response => {
        let raw = ''; response.setEncoding('utf8')
        response.on('data', chunk => { raw += chunk })
        response.on('end', () => {
          record.status = response.statusCode; record.response = raw
          let data = raw
          try { if (raw) data = JSON.parse(raw) } catch { /* actual non-JSON is preserved for the production API */ }
          resolve({ status: response.statusCode, statusText: response.statusMessage, data, raw, headers: response.headers })
        })
      })
      req.on('timeout', () => req.destroy(new Error('S03 loopback HTTP timeout')))
      req.on('error', error => { record.error = error.message; reject(error) })
      req.end(body)
    })
  }
  const mode = async value => { const res = await wire('POST', '/__fixture__/mode', JSON.stringify({ mode: value }), { 'Content-Type': 'application/json' }); assert.equal(res.status, 200) }
  const snapshot = async label => { const res = await wire('GET', '/__fixture__/snapshot'); assert.equal(res.status, 200); snapshots.push({ label, ...res.data }); return res.data }
  function businessTrace() { return trace.filter(item => !item.fixture) }
  function assertSafePage(page) {
    assert.doesNotMatch(page.text(), /S03_SYNTHETIC_(?:AUTH|PROXY_AUTH|COOKIE)|S03_PRIVATE_UPSTREAM_TOKEN|S03_SYNTHETIC_AUDIT_FAILURE|mooc2-ans|https?:\/\//)
    assert.equal(page.find('script, iframe, object, embed').exists(), false)
  }
  function adapter(config) {
    assert.equal(config.baseURL, '/sqp/api')
    assert.equal(config.url, '/parse')
    assert.equal(config.timeout, 60000)
    assert.equal(config.retryPolicy, 'single')
    const method = String(config.method).toUpperCase(), url = '/sqp/api/parse' + (config.params ? '?' + new URLSearchParams(config.params).toString() : '')
    const headers = Object.fromEntries(Object.entries(config.headers.toJSON()).filter(([, value]) => value !== undefined))
    const record = { method, url, data: config.data, headers, retryPolicy: config.retryPolicy, timeout: config.timeout }
    adapters.push(record)
    return wire(method, url, config.data, headers).then(response => ({ ...response, config })).catch(error => { throw new axios.AxiosError(error.message, 'ERR_NETWORK', config) })
  }
  function blobBytes(blob) {
    return new Promise((resolve, reject) => { const reader = new window.FileReader(); reader.onload = () => resolve(Buffer.from(reader.result)); reader.onerror = reject; reader.readAsArrayBuffer(blob) })
  }
  // Antd's unmodified vc-upload constructs this XHR and browser FormData. This
  // adapter serializes its actual entries, then delivers real multipart bytes.
  // It does not synthesize Upload callbacks, CRC, parse calls or parser results.
  class LoopbackUploadXHR {
    constructor() { this.upload = {}; this.headers = {}; this.withCredentials = false; this.aborted = false }
    open(method, url) { this.method = method.toUpperCase(); this.url = url; assert.equal(this.method, 'POST'); assert.equal(url, '/sqp/api/upload') }
    setRequestHeader(name, value) { this.headers[name] = value }
    async send(form) {
      try {
        assert.ok(form instanceof window.FormData, 'Antd creates actual browser FormData')
        const boundary = 'S03_NATIVE_MULTIPART_' + (++fileNumber), chunks = [], entries = []
        for (const [name, value] of form.entries()) {
          assert.equal(name, 'file')
          assert.ok(value instanceof window.Blob, 'native uploaded file remains Blob')
          const bytes = await blobBytes(value)
          entries.push({ name, filename: value.name, type: value.type, bytes: bytes.length, sha256: hash(bytes) })
          chunks.push(Buffer.from('--' + boundary + '\r\nContent-Disposition: form-data; name="' + name + '"; filename="' + value.name + '"\r\nContent-Type: ' + (value.type || 'application/octet-stream') + '\r\n\r\n'), bytes, Buffer.from('\r\n'))
        }
        chunks.push(Buffer.from('--' + boundary + '--\r\n'))
        assert.equal(entries.length, 1)
        const bytes = Buffer.concat(chunks)
        adapters.push({ method: 'POST', url: this.url, transport: 'Antd XHR FormData loopback serializer', entries, bytes: bytes.length })
        const response = await wire(this.method, this.url, bytes, { ...this.headers, 'Content-Type': 'multipart/form-data; boundary=' + boundary })
        if (this.aborted) return
        this.status = response.status; this.responseText = response.raw; this.response = response.raw
        this.onload?.()
      } catch (error) { if (!this.aborted) this.onerror?.(error) }
    }
    abort() { this.aborted = true }
  }
  async function mountPage(actor = A) {
    user.setStoredCurrentUser(actor)
    const Page = require(path.join(root, 'src/pages/IntelligentImport/index.vue'))
    const outer = mount({ render: () => h(Antd.ConfigProvider, { locale: zhCN }, { default: () => h(Page) }) }, { attachTo: document.body, global: { plugins: [Antd], config: { errorHandler: error => nativeErrors.push(String(error?.stack || error)) } } })
    wrappers.push(outer); await settle()
    return outer.findComponent(Page)
  }
  async function unmountAll() {
    for (const wrapper of wrappers.splice(0)) wrapper.unmount()
    Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); document.body.replaceChildren()
  }
  async function uploadFile(page, label, confirm = false) {
    const count = businessTrace().length
    const input = page.get('input[type=file]').element
    const value = new window.File(['S03_SYNTHETIC_DOCX_BYTES'], 'S03_' + label + '.docx', { type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document' })
    Object.defineProperty(input, 'files', { configurable: true, value: [value] })
    input.dispatchEvent(new window.Event('change', { bubbles: true }))
    if (confirm) { await eventually(() => Boolean(document.querySelector('.ant-modal-confirm')), 'native upload discard dialog'); button(document.querySelector('.ant-modal-confirm'), '放弃更改').click() }
    await eventually(() => businessTrace().slice(count).some(item => item.path === '/sqp/api/upload' && item.status !== undefined) && !page.vm.loading, 'native upload settled')
    await settle()
  }
  function edit(page, text) {
    const editor = page.get('.compileContent [contenteditable]').element
    editor.innerHTML = '<p class="lines">' + text + '</p>'
    editor.dispatchEvent(new window.Event('input', { bubbles: true }))
    return editor
  }
  async function reparse(page) {
    const count = businessTrace().length
    button(page.element, '重新识别').click()
    await eventually(() => businessTrace().length === count + 1 && businessTrace().at(-1).status !== undefined && !page.vm.loading, 'native reparse settled')
  }
  try {
    save('source-before.json', beforeHashes)
    save('invocation.json', { binary, binarySHA256: hash(fs.readFileSync(binary)), execPath: process.execPath, argv: process.argv, evidence })
    bridge = spawn(binary, ['-test.run=^TestS03ProxyNativeBridge$', '-test.count=1', '-test.timeout=145s', '-test.v'], { cwd: evidence, env: { PATH: process.env.PATH, HOME: os.homedir(), TMPDIR: evidence, TIKU_S03_BRIDGE_MODE: 'ui', TIKU_S03_BRIDGE_READY_FILE: readyFile }, stdio: ['ignore', 'pipe', 'pipe'] })
    exitPromise = new Promise((resolve, reject) => { bridge.once('error', reject); bridge.once('exit', (code, signal) => { exited = { code, signal }; resolve(exited) }) })
    bridge.stdout.on('data', bytes => { childOut += bytes }); bridge.stderr.on('data', bytes => { childErr += bytes })
    await eventually(() => { if (exited) assert.fail('Go bridge exited before ready: ' + JSON.stringify(exited) + '\n' + childOut + childErr); return fs.existsSync(readyFile) }, 'Go bridge ready', 15000)
    ready = JSON.parse(fs.readFileSync(readyFile, 'utf8')); assert.ok(ready.nonce)
    restoreModules.push(mockModule(require.resolve('vue-router'), { useRouter: () => ({ push: value => trap('unexpected navigation', String(value)) }), onBeforeRouteLeave: () => {} }))
    restoreModules.push(mockModule(path.join(root, 'src/api/TikuComponentApi.js'), { createQuestions: payload => trap('question write', JSON.stringify(payload)) }))
    axios.create = config => originals.axiosCreate({ ...config, adapter })
    require(path.join(root, 'src/api/api.js'))
    axios.create = originals.axiosCreate
    http.request = (...args) => trap('uncontrolled HTTP', String(args[0])); http.get = (...args) => trap('uncontrolled HTTP', String(args[0]))
    https.request = (...args) => trap('HTTPS/provider', String(args[0])); https.get = (...args) => trap('HTTPS/provider', String(args[0]))
    globalThis.fetch = window.fetch = (...args) => trap('fetch/provider', String(args[0]))
    window.WebSocket = function () { return trap('WebSocket/provider', 'constructor') }
    window.XMLHttpRequest = globalThis.XMLHttpRequest = LoopbackUploadXHR
    globalThis.FormData = window.FormData; globalThis.Blob = window.Blob; globalThis.File = window.File
    for (const kind of ['error', 'warning', 'success', 'info']) {
      const original = Antd.message[kind]; Antd.message[kind] = (...args) => { notices.push({ kind, text: String(args[0]) }); return original(...args) }
      restoreModules.push(() => { Antd.message[kind] = original })
    }

    await t.test('actual file input -> Antd multipart upload -> CRC query -> parser paper, then native reparse form body', async () => {
      await mode('success'); const page = await mountPage()
      await uploadFile(page, 'THREE_WIRES')
      assert.equal(page.vm.UploadId, CRC); assert.equal(page.vm.data.length, 5); assert.equal(page.vm.parseReady, true)
      assert.equal(page.vm.data[0].options[0].value, 0)
      assert.deepEqual([...page.vm.data[1].answer], [0, 0]); assert.deepEqual([...page.vm.data[2].answer], ['重复', '重复']); assert.deepEqual([...page.vm.data[3].answer], ['同文', '同文']); assert.deepEqual([...page.vm.data[4].answer], [])
      const upload = businessTrace()[0], get = businessTrace()[1]
      assert.equal(upload.method, 'POST'); assert.match(upload.headers['Content-Type'], /^multipart\/form-data; boundary=/)
      assert.ok(upload.body.includes('name="file"; filename="S03_THREE_WIRES.docx"')); assert.ok(upload.body.includes('S03_SYNTHETIC_DOCX_BYTES'))
      assert.equal(get.method, 'GET'); assert.equal(new URLSearchParams(get.rawQuery).get('file'), CRC)
      const typed = 'S03_NATIVE_HTML & + 中文'
      edit(page, typed); await reparse(page)
      const post = businessTrace().at(-1)
      assert.equal(post.method, 'POST'); assert.equal(post.path, '/sqp/api/parse'); assert.match(post.headers['Content-Type'], /^application\/x-www-form-urlencoded/)
      const html = new URLSearchParams(post.body).get('html')
      assert.ok(html.startsWith('<p>S03_NATIVE_HTML &amp; + 中文</p>'), 'actual HTML entity encoding survives form serialization')
      const decoded = document.createElement('div'); decoded.innerHTML = html
      assert.ok(decoded.textContent.includes(typed), 'decoded actual HTML retains ampersand, plus and Unicode')
      assert.equal(page.vm.editorDirty, false); assert.equal(page.vm.parseReady, true); assertSafePage(page)
      const current = await snapshot('three actual page wires')
      assert.equal(current.violations.length, 0)
      assert.equal(current.traces.length, 3)
      assert.equal(current.upstreamRequests.length, 3, 'all three forwarded wires reach real local upstream HTTP')
      for (const [index, forward] of current.traces.entries()) {
        const incoming = businessTrace()[index], upstream = current.upstreamRequests[index]
        for (const field of ['method', 'path', 'rawQuery']) { assert.equal(forward[field], incoming[field], 'forward ' + field); assert.equal(upstream[field], incoming[field], 'upstream ' + field) }
        assert.equal(forward.body, incoming.body || ''); assert.equal(upstream.body, incoming.body || '')
        assert.equal(forward.contentType, incoming.headers['Content-Type'] || ''); assert.equal(upstream.contentType, forward.contentType)
        assert.equal(forward.host, 'mooc2-ans.chaoxing.com'); assert.equal(forward.scheme, 'http')
        assert.equal(forward.authorization, ''); assert.equal(forward.proxyAuthorization, ''); assert.equal(forward.cookie, 'UID=1')
        assert.equal(upstream.authorization, ''); assert.equal(upstream.proxyAuthorization, ''); assert.equal(upstream.cookie, 'UID=1')
      }
      dom.push({ label: 'native success', html: page.element.outerHTML }); await unmountAll(); passed++
    })

    for (const [fault, expected] of [['closed', 503], ['unauth', 401], ['timeout', 504], ['busy', 429], ['upstream5xx', 502], ['audit-upstream-error', 502]]) {
      await t.test('native ' + fault + ' reparse fails once, preserves actual edited DOM/draft, then explicit recovery succeeds', async () => {
        await mode('success'); const page = await mountPage(); await uploadFile(page, 'FAULT_' + fault)
        const previous = JSON.stringify(page.vm.data), text = 'S03_PRESERVED_' + fault
        edit(page, text); await settle(); assert.equal(page.vm.editorDirty, true); assert.equal(page.vm.hasUnsavedChanges, true)
        const before = businessTrace().length; await mode(fault); await reparse(page)
        assert.equal(businessTrace().length, before + 1, 'no automatic POST replay')
        assert.equal(businessTrace().at(-1).status, expected)
        assert.equal(JSON.stringify(page.vm.data), previous); assert.ok(page.get('.compileContent').text().includes(text)); assert.equal(page.vm.editorDirty, true); assert.equal(page.vm.hasUnsavedChanges, true); assert.equal(page.vm.parseReady, false)
        assert.equal(page.vm.importError, '重新识别失败，当前题目与校对编辑仍保留，请重试。'); assertSafePage(page)
        button(page.element, '确认导入').click(); await settle()
        assert.equal(businessTrace().length, before + 1, 'failed parse cannot create questions or replay parser POST')
        dom.push({ label: fault + ' preserved draft', html: page.element.outerHTML })
        await mode('success'); await reparse(page)
        assert.equal(businessTrace().length, before + 2, 'only explicit recovery POST')
        assert.equal(page.vm.importError, ''); assert.equal(page.vm.editorDirty, false); assert.equal(page.vm.parseReady, true)
        const current = await snapshot(fault + ' recovered'); assert.equal(current.active, 0); assert.equal(current.violations.length, 0)
        await unmountAll(); passed++
      })
    }

    await t.test('actual upload failure preserves approved existing draft and GET failure does not replace it', async () => {
      await mode('success'); const page = await mountPage(); await uploadFile(page, 'UPLOAD_BASELINE')
      edit(page, 'S03_UPLOAD_DRAFT'); await settle(); const rows = JSON.stringify(page.vm.data)
      await mode('upstream5xx'); const before = businessTrace().length
      await uploadFile(page, 'UPLOAD_FAIL', true)
      assert.equal(businessTrace().length, before + 1); assert.equal(businessTrace().at(-1).path, '/sqp/api/upload'); assert.equal(businessTrace().at(-1).status, 502)
      assert.equal(page.vm.importError, '文件上传失败，当前题目与校对编辑仍保留，请重新上传。'); assert.ok(page.get('.compileContent').text().includes('S03_UPLOAD_DRAFT')); assert.equal(JSON.stringify(page.vm.data), rows)
      const getBefore = businessTrace().length; await page.vm.getParseFile(); await settle()
      assert.equal(businessTrace().length, getBefore + 1); assert.equal(businessTrace().at(-1).method, 'GET'); assert.equal(page.vm.importError, '识别文件失败，当前题目与校对编辑仍保留，请重试。')
      assert.ok(page.get('.compileContent').text().includes('S03_UPLOAD_DRAFT')); assert.equal(page.vm.hasUnsavedChanges, true)
      await mode('success'); await page.vm.getParseFile(); await settle(); assert.equal(page.vm.parseReady, true); assert.equal(page.vm.importError, '')
      assertSafePage(page); await snapshot('upload and GET recovered'); await unmountAll(); passed++
    })

    await t.test('audit callback panic after completed 200 is not relabeled failure and proxy slot is reusable', async () => {
      await mode('success'); const page = await mountPage(); await uploadFile(page, 'AUDIT_SUCCESS')
      edit(page, 'S03_AUDIT_200'); await mode('auditpanic'); await reparse(page)
      assert.equal(businessTrace().at(-1).status, 200); assert.equal(page.vm.importError, ''); assert.equal(page.vm.parseReady, true)
      const current = await snapshot('audit panic after response'); assert.equal(current.active, 0); assert.equal(current.violations.length, 0)
      await mode('success'); edit(page, 'S03_AFTER_AUDIT'); await reparse(page); assert.equal(page.vm.parseReady, true)
      await unmountAll(); passed++
    })

    await t.test('actual pending A HTTP response and old native entry cannot publish after B owns a new parsed draft', async () => {
      await mode('success'); const page = await mountPage(); await uploadFile(page, 'OWNER_A')
      edit(page, 'S03_A_PRIVATE_EDIT'); const oldButton = page.findAllComponents(Antd.Button).find(item => item.text().replace(/\s/g, '') === '重新识别').vm.$.vnode.props.onClick
      await mode('deferred'); const before = businessTrace().length; button(page.element, '重新识别').click()
      await eventually(() => businessTrace().length === before + 1 && page.vm.loading, 'A HTTP pending')
      let held = false
      for (let attempt = 0; attempt < 100; attempt++) {
        const current = await wire('GET', '/__fixture__/snapshot')
        if (current.data.upstreamRequests.some(item => item.mode === 'deferred')) { held = true; break }
        await sleep(10)
      }
      assert.equal(held, true, 'A request must actually reach the deferred local upstream before changing identity/mode')
      user.setStoredCurrentUser(B); await settle()
      assert.equal(page.vm.data.length, 0); assert.equal(page.text().includes('S03_A_PRIVATE_EDIT'), false)
      await mode('success'); await uploadFile(page, 'OWNER_B'); assert.equal(page.vm.UploadId, CRC)
      const state = JSON.stringify({ data: page.vm.data, loading: page.vm.loading, error: page.vm.importError, file: page.vm.UploadId }), count = businessTrace().length
      oldButton(); await settle(); assert.equal(businessTrace().length, count, 'retired rendered entry cannot borrow B')
      const released = await wire('POST', '/__fixture__/release', '{}', { 'Content-Type': 'application/json' }); assert.equal(released.status, 200)
      await eventually(() => businessTrace()[before].status !== undefined, 'old A HTTP response delivered')
      await settle(); assert.equal(JSON.stringify({ data: page.vm.data, loading: page.vm.loading, error: page.vm.importError, file: page.vm.UploadId }), state, 'old A completion and finally cannot change B')
      assertSafePage(page); await snapshot('owner retired after late HTTP'); await unmountAll(); passed++
    })

    const final = await snapshot('final')
    assert.equal(passed, 10, 'all ten native phases must finish, not just parent cleanup')
    assert.equal(final.violations.length, 0); assert.equal(final.active, 0); assert.equal(traps.length, 0); assert.equal(nativeErrors.length, 0)
    assert.doesNotMatch(JSON.stringify(notices), /S03_PRIVATE_UPSTREAM_TOKEN|S03_SYNTHETIC_AUDIT_FAILURE|S03_SYNTHETIC_(?:AUTH|PROXY_AUTH|COOKIE)/, 'no actual upstream/audit sentinel or fixture credential in any native notice')
    assert.deepEqual(sourceHashes(), beforeHashes, 'all bound production sources unchanged')
    const response = await wire('POST', '/__fixture__/shutdown', '{}', { 'Content-Type': 'application/json' }); assert.equal(response.status, 200); shutdown = true
    const result = await Promise.race([exitPromise, sleep(5000).then(() => { throw new Error('bridge did not exit after shutdown') })]); assert.deepEqual(result, { code: 0, signal: null })
    save('NATIVE_RESULT.json', { status: 'PASS', phases: passed, businessRequests: businessTrace().length, traceEntries: trace.length, sourceHashes: beforeHashes, bridgeExit: result, traps, nativeErrors, boundaries: ['Actual production Vue/Antd/API and production Go proxy over loopback HTTP; parser is synthetic only-local upstream', 'Fixture header auth is not production JWT/Cookie qualification', 'Antd vc-upload actual File input and FormData are serialized by bounded test XHR transport; no browser layout/device/reader claim', 'No question write, database, real document parser, real provider or payment call', 'Audit panic after transmitted HTTP200 remains200; only existing Recovery is observed, no production audit policy changed'] })
    console.log('S03_EVIDENCE ' + evidence)
  } finally {
    await unmountAll()
    if (bridge && !exited) {
      if (ready && !shutdown) { try { await wire('POST', '/__fixture__/shutdown', '{}', { 'Content-Type': 'application/json' }) } catch { /* failing run still preserves real cleanup/error evidence */ } }
      if (!exited) await Promise.race([exitPromise.catch(() => {}), sleep(1000)])
      if (!exited) { bridge.kill('SIGKILL'); await exitPromise.catch(() => {}) }
    }
    save('trace.json', trace); save('axios-and-upload-adapters.json', adapters); save('snapshots.json', snapshots); save('dom.json', dom); save('notices.json', notices); save('traps.json', traps); save('native-errors.json', nativeErrors); save('bridge.stdout', childOut); save('bridge.stderr', childErr); save('bridge.exit.json', exited || null); save('source-after.json', sourceHashes())
    for (const restore of restoreModules.reverse()) restore()
    axios.create = originals.axiosCreate; http.request = originals.httpRequest; http.get = originals.httpGet; https.request = originals.httpsRequest; https.get = originals.httpsGet
    globalThis.fetch = originals.fetch; window.fetch = originals.windowFetch; window.XMLHttpRequest = originals.xhr; globalThis.XMLHttpRequest = originals.globalXHR; globalThis.FormData = originals.formData; globalThis.Blob = originals.blob; globalThis.File = originals.file; window.WebSocket = originals.webSocket
    user.clearStoredCurrentUser()
  }
})
