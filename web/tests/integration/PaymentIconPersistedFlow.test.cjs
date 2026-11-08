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

// Dedicated integration command: missing compiled Go fixture is a hard failure,
// never a mock/skip. Only unrelated, explicitly listed GETs use readonly fixtures.
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(process.env.UI_TEST_HELPER || path.resolve(__dirname, '../helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick, h } = require('vue')
const Antd = require('ant-design-vue')
const zhCN = require('ant-design-vue/lib/locale/zh_CN').default
const user = require(path.join(root, 'src/utils/currentUser.js'))
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))
const hash = value => crypto.createHash('sha256').update(value).digest('hex')
const paymentPath = '/adapter-service/payment-methods'
const customSVG = '<svg viewBox="0 0 32 32" width="32" height="32" class="icon" version="1.1" t="1765858760722" p-id="2038" data-name="wechat" aria-label="支付"><path d="M2 3h17v19H2z" fill="#12ab34" stroke="#234567" stroke-width="2" p-id="2039"/></svg>'
const legacySVG = '<svg onload="S02_INERT_LEGACY"/>'
const admin = { id: 92001, username: 'S02_FIXTURE_ADMIN', role: 'admin', status: 1, balance: 20 }
const ordinary = { id: 92002, username: 'S02_FIXTURE_USER', role: 'user', status: 1, balance: 20 }
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function eventually(predicate, label, timeout = 8000) {
  const until = Date.now() + timeout
  while (Date.now() < until) {
    await settle()
    if (predicate()) return
    await sleep(10)
  }
  assert.fail(`Timed out: ${label}`)
}
function sourceHashes() {
  return Object.fromEntries([
    'src/api/TikuComponentApi.js', 'src/utils/currentUser.js', 'src/utils/request.js',
    'src/utils/safePaymentIcon.mjs', 'src/utils/paymentMethodHelpers.mjs',
    'src/components/SafePaymentIcon.js', 'src/components/SafePaymentIcon.vue',
    'src/pages/SystemSettings/index.vue', 'src/pages/SystemSettings/PaymentMethodPanel.vue',
    'src/pages/SystemSettings/components/PaymentMethodIconPicker.vue', 'src/pages/UserUsage/index.vue',
  ].map(relative => [path.join(root, relative), hash(fs.readFileSync(path.join(root, relative)))]))
}
function assertSafeDOM(element, label) {
  assert.ok(element, label)
  assert.equal(element.querySelector('script,foreignObject,iframe,object,embed,image,use,a,style'), null, `${label}: dangerous tag`)
  for (const node of [element, ...element.querySelectorAll('*')]) {
    for (const attr of node.attributes || []) {
      assert.doesNotMatch(attr.name, /^on/i, `${label}: event attribute`)
      assert.doesNotMatch(attr.name, /^(?:href|xlink:href|src)$/i, `${label}: resource attribute`)
      assert.doesNotMatch(attr.value, /(?:javascript:|https?:|data:|url\s*\()/i, `${label}: executable/external resource`)
    }
  }
}
function assertCustomSVG(element, label) {
  assertSafeDOM(element, label)
  const svg = element.querySelector('svg')
  assert.ok(svg, `${label}: actual reconstructed SVG`)
  for (const [key, value] of Object.entries({ viewBox: '0 0 32 32', width: '32', height: '32', class: 'icon', version: '1.1', t: '1765858760722', 'p-id': '2038', 'data-name': 'wechat', 'aria-label': '支付' })) {
    assert.equal(svg.getAttribute(key), value, `${label}: svg ${key}`)
  }
  const shape = svg.querySelector('path')
  assert.ok(shape, `${label}: path`)
  for (const [key, value] of Object.entries({ d: 'M2 3h17v19H2z', fill: '#12ab34', stroke: '#234567', 'stroke-width': '2', 'p-id': '2039' })) {
    assert.equal(shape.getAttribute(key), value, `${label}: path ${key}`)
  }
}
function nativeButton(container, text) {
  // Antd's Chinese autoInsertSpace renders 确定 as 确 定 in the actual button.
  const found = [...container.querySelectorAll('button')].find(button => button.textContent.replace(/\s/g, '') === text.replace(/\s/g, ''))
  assert.ok(found, `native button ${text}`)
  return found
}
async function nativeInput(element, value) {
  assert.ok(element, 'actual input exists')
  element.value = value
  element.dispatchEvent(new window.Event('input', { bubbles: true }))
  element.dispatchEvent(new window.Event('change', { bubbles: true }))
  await settle()
}

test('S02 native Vue/Antd -> production HTTP/controller/service -> isolated SQLite -> both consumers', { timeout: 100000 }, async t => {
  const binary = process.env.TIKU_S02_BRIDGE_BINARY
  assert.ok(binary && path.isAbsolute(binary) && fs.existsSync(binary), 'TIKU_S02_BRIDGE_BINARY must name a real absolute compiled controller.test; run test:integration:s02 (no skip/mock fallback)')
  const evidenceRoot = process.env.TIKU_S02_EVIDENCE_DIR || path.join(os.tmpdir(), 'tiku-s02-native-evidence')
  fs.mkdirSync(evidenceRoot, { recursive: true })
  const evidence = fs.mkdtempSync(path.join(evidenceRoot, 'attempt-'))
  const readyFile = path.join(evidence, 'bridge-ready.json')
  const beforeHashes = sourceHashes()
  const trace = [], snapshots = [], dom = [], fixtureReads = [], trapped = []
  const original = { httpRequest: http.request, httpGet: http.get, httpsRequest: https.request, httpsGet: https.get, fetch: globalThis.fetch, xhr: window.XMLHttpRequest, webSocket: window.WebSocket }
  const requestPath = path.join(root, 'src/utils/request.js')
  const cachedRequest = require.cache[requestPath]
  let ready, actor = 'admin', bridge, exitPromise, exitResult, childOut = '', childErr = '', shutdownRequested = false
  const wrappers = []
  const save = (name, value) => fs.writeFileSync(path.join(evidence, name), typeof value === 'string' ? value : JSON.stringify(value, null, 2) + '\n')
  function trap(kind, detail) { trapped.push({ kind, detail }); throw new Error(`S02 forbidden ${kind}: ${detail}`) }
  async function wire(method, pathname, data, identity = actor) {
    assert.ok(ready, 'real Go bridge ready before HTTP')
    const url = new URL(pathname, ready.url)
    assert.equal(url.origin, new URL(ready.url).origin, 'loopback fixture origin only')
    assert.match(url.hostname, /^(?:127\.0\.0\.1|localhost|\[::1\])$/, 'loopback host only')
    assert.match(url.pathname, /^(?:\/adapter-service\/payment-methods(?:\/\d+)?|\/__fixture__\/(?:snapshot|disable-all|shutdown))$/, 'bounded fixture/payment route only')
    const body = data === undefined ? undefined : JSON.stringify(data)
    const record = { method, path: pathname, actor: identity, request: data === undefined ? null : data }
    trace.push(record)
    return new Promise((resolve, reject) => {
      const request = original.httpRequest.call(http, url, { method, headers: { 'X-S02-Actor': identity, 'X-S02-Nonce': ready.nonce, ...(body ? { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body) } : {}) }, timeout: 5000 }, response => {
        let raw = ''
        response.setEncoding('utf8')
        response.on('data', chunk => { raw += chunk })
        response.on('end', () => {
          record.status = response.statusCode; record.rawResponse = raw
          try { record.response = raw ? JSON.parse(raw) : null } catch (error) { reject(error); return }
          resolve({ status: response.statusCode, data: record.response })
        })
      })
      request.on('timeout', () => request.destroy(new Error('S02 HTTP timeout')))
      request.on('error', error => { record.error = error.message; reject(error) })
      request.end(body)
    })
  }
  async function snapshot(label) {
    const response = await wire('GET', '/__fixture__/snapshot')
    assert.equal(response.status, 200)
    assert.ok(Array.isArray(response.data.rows))
    assert.match(response.data.sha256, /^[a-f0-9]{64}$/)
    snapshots.push({ label, ...response.data })
    return response.data
  }
  function transport(config) {
    const verb = String(config.method || 'get').toUpperCase()
    if (config.url === paymentPath || new RegExp(`^${paymentPath}/\\d+$`).test(config.url)) return wire(verb, config.url, config.data)
    if (verb !== 'GET') return trap('non-payment write', `${verb} ${config.url}`)
    fixtureReads.push({ method: verb, path: config.url })
    switch (config.url) {
      case '/adapter-service/system-settings': return Promise.resolve({ status: 200, data: { minRechargeAmount: 1 } })
      case '/adapter-service/system-metrics': return Promise.resolve({ status: 200, data: { schemaVersion: 1, startedAt: '2026-10-29T00:00:00Z', uptimeSeconds: 1, http: { total: 0, inFlight: 0, failed: 0, latencyCount: 0, latencyTotalMillis: 0, latencyMaxMillis: 0, buckets: [] }, cache: { search: { hits: 0, misses: 0, shared: 0 }, fallback: { hits: 0, misses: 0, shared: 0 } }, billing: { attempts: 0, successes: 0, failures: 0 } } })
      case '/adapter-service/user/info': return Promise.resolve({ status: 200, data: { ...(actor === 'admin' ? admin : ordinary), minRechargeAmount: 1 } })
      case '/adapter-service/billing-records': return Promise.resolve({ status: 200, data: { data: [], total: 0, page: 1, pageSize: 10 } })
      default: return trap('unlisted readonly fixture', config.url)
    }
  }
  function mountPage(relative) {
    const page = require(path.join(root, relative))
    const wrapper = mount({ render: () => h(Antd.ConfigProvider, { locale: zhCN }, { default: () => h(page) }) }, { attachTo: document.body, global: { plugins: [Antd] } })
    wrappers.push(wrapper)
    return wrapper.findComponent(page)
  }
  async function unmountAll() {
    for (const wrapper of wrappers.splice(0)) wrapper.unmount()
    Antd.Modal.destroyAll(); Antd.message.destroy()
    await settle(); document.body.innerHTML = ''
  }
  try {
    save('source-before.json', beforeHashes)
    save('invocation.json', { execPath: process.execPath, argv: process.argv, binary, binarySHA256: hash(fs.readFileSync(binary)), fixtureIdentity: 'X-S02-Actor is test middleware only; no JWT/Cookie/provider qualification', evidence })
    bridge = spawn(binary, ['-test.run=^TestS02PaymentIconNativeBridge$', '-test.count=1', '-test.timeout=90s', '-test.v'], { cwd: evidence, env: { PATH: process.env.PATH, HOME: os.homedir(), TMPDIR: evidence, TIKU_S02_BRIDGE_MODE: 'ui', TIKU_S02_BRIDGE_READY_FILE: readyFile, TIKU_S02_BRIDGE_LOG_DIR: evidence }, stdio: ['ignore', 'pipe', 'pipe'] })
    exitPromise = new Promise((resolve, reject) => { bridge.once('error', reject); bridge.once('exit', (code, signal) => { exitResult = { code, signal }; resolve(exitResult) }) })
    bridge.stdout.on('data', chunk => { childOut += chunk.toString() })
    bridge.stderr.on('data', chunk => { childErr += chunk.toString() })
    await eventually(() => {
      if (exitResult) assert.fail(`Go bridge exited before ready: ${JSON.stringify(exitResult)}\n${childOut}\n${childErr}`)
      return fs.existsSync(readyFile)
    }, 'Go bridge ready JSON', 15000)
    ready = JSON.parse(fs.readFileSync(readyFile, 'utf8'))
    assert.ok(ready.nonce)
    assert.match(new URL(ready.url).hostname, /^(?:127\.0\.0\.1|localhost|\[::1\])$/)
    const requestMock = new Module(requestPath); requestMock.loaded = true; requestMock.exports = transport; require.cache[requestPath] = requestMock
    // Actual API thin functions are loaded from source; no payment response echo.
    http.request = (...args) => trap('uncontrolled HTTP', String(args[0]))
    http.get = (...args) => trap('uncontrolled HTTP', String(args[0]))
    https.request = (...args) => trap('HTTPS/provider', String(args[0]))
    https.get = (...args) => trap('HTTPS/provider', String(args[0]))
    globalThis.fetch = (...args) => trap('fetch/provider', String(args[0]))
    window.XMLHttpRequest = function () { return trap('XHR/provider', 'constructor') }
    window.WebSocket = function () { return trap('WebSocket/provider', 'constructor') }
    const API = require(path.join(root, 'src/api/TikuComponentApi.js'))
    const initial = await snapshot('initial actual temp SQLite')
    assert.equal(initial.rows.find(row => row.id === 1).iconSvg, legacySVG)
    assert.equal(initial.rows.length, 3)
    let created, passed = 0

    await t.test('native catalog/custom preview/Save -> actual POST/GET -> SystemSettings and UserUsage SVG attributes', async () => {
      actor = 'admin'; user.setStoredCurrentUser(admin)
      const settings = mountPage('src/pages/SystemSettings/index.vue')
      await eventually(() => settings.vm.paymentMethodsLoaded && !settings.vm.paymentMethodLoading, 'admin real GET loaded')
      const legacyCell = document.querySelector('tr[data-row-key="1"] .icon-cell')
      assert.ok(legacyCell)
      assert.equal(legacyCell.textContent.trim(), '-', 'admin API-empty legacy placeholder')
      assertSafeDOM(legacyCell, 'admin legacy icon DOM')
      nativeButton(document, '新增支付方式').click(); await settle()
      assert.equal(document.querySelectorAll('.payment-icon-button').length, 17)
      document.querySelector('.payment-icon-button').click(); await settle()
      assert.equal(document.querySelector('.payment-icon-button').getAttribute('aria-pressed'), 'true')
      assert.ok(document.querySelector('.icon-preview svg path'), 'catalog selection native preview')
      await nativeInput(document.querySelector('input[placeholder="例如 支付宝"]'), 'S02 Persisted Custom')
      await nativeInput(document.querySelector('input[placeholder="例如 alipay / wxpay"]'), 'alipay')
      await nativeInput(document.querySelector('textarea[placeholder="可选，直接粘贴 SVG 源码"]'), customSVG)
      assert.equal(document.querySelector('.payment-icon-button').getAttribute('aria-pressed'), 'false')
      assertCustomSVG(document.querySelector('.icon-preview'), 'native custom preview')
      dom.push({ label: 'valid native preview', html: document.querySelector('.icon-preview').outerHTML })
      nativeButton(document.querySelector('.ant-modal-footer'), '确定').click()
      await eventually(() => trace.some(entry => entry.method === 'POST' && entry.path === paymentPath && entry.status === 200) && !settings.vm.paymentMethodVisible && settings.vm.paymentMethods.some(row => row.name === 'S02 Persisted Custom'), 'POST and follow-up GET consumed')
      const post = trace.find(entry => entry.method === 'POST' && entry.path === paymentPath)
      assert.equal(post.request.iconSvg, customSVG)
      created = post.response
      assert.ok(Number(created.id) > 3, 'real inserted ID, not request echo')
      assert.ok(created.createdAt, 'persisted timestamp absent from UI payload')
      assert.equal(post.request.id, undefined)
      const persisted = await snapshot('after native valid POST')
      assert.equal(persisted.count, initial.count + 1)
      assert.equal(persisted.rows.find(row => row.id === created.id).iconSvg, customSVG)
      const dto = await API.getPaymentMethods()
      assert.equal(dto.status, 200)
      assert.equal(dto.data.find(row => row.id === created.id).iconSvg, customSVG)
      assert.equal(dto.data.find(row => row.id === 1).iconSvg, '', 'actual API sanitizes raw legacy')
      const cell = document.querySelector(`tr[data-row-key="${created.id}"] .icon-cell`)
      assertCustomSVG(cell, 'SystemSettings actual GET consumer')
      dom.push({ label: 'SystemSettings actual GET', html: cell.outerHTML })
      await unmountAll()
      actor = 'user'; user.setStoredCurrentUser(ordinary)
      const usage = mountPage('src/pages/UserUsage/index.vue')
      await eventually(() => usage.vm.paymentMethods.some(row => row.id === created.id) && !usage.vm.paymentMethodLoading, 'UserUsage real enabled GET')
      const card = [...document.querySelectorAll('.payment-method-card')].find(node => node.textContent.includes('S02 Persisted Custom'))
      assertCustomSVG(card.querySelector('.safe-payment-icon'), 'UserUsage actual GET consumer')
      const legacyCard = [...document.querySelectorAll('.payment-method-card')].find(node => node.textContent.includes('legacy-dangerous'))
      assert.ok(legacyCard.querySelector('[data-payment-icon-placeholder]'))
      assert.equal(legacyCard.querySelector('svg'), null)
      assertSafeDOM(legacyCard.querySelector('.safe-payment-icon'), 'UserUsage legacy DOM')
      dom.push({ label: 'UserUsage actual GET', html: card.outerHTML }, { label: 'UserUsage legacy placeholder', html: legacyCard.outerHTML })
      await unmountAll()
      passed += 1
    })

    await t.test('unsafe native preview/Save is inert; bypassed POST/PUT are 400 with unchanged DB', async () => {
      actor = 'admin'; user.setStoredCurrentUser(admin)
      const settings = mountPage('src/pages/SystemSettings/index.vue')
      await eventually(() => settings.vm.paymentMethodsLoaded && !settings.vm.paymentMethodLoading, 'admin reread')
      nativeButton(document, '新增支付方式').click(); await settle()
      await nativeInput(document.querySelector('input[placeholder="例如 支付宝"]'), 'Never Persist Unsafe')
      await nativeInput(document.querySelector('input[placeholder="例如 alipay / wxpay"]'), 'alipay')
      const vectors = [legacySVG, '<svg><foreignObject><div>unsafe</div></foreignObject></svg>', '<svg><path fill="url(https://example.invalid/unsafe)" d="M0 0"/></svg>', '<svg><image href="https://example.invalid/a.svg"/></svg>']
      for (const svg of vectors) {
        const before = await snapshot('before rejected new input')
        const beforeWrites = trace.filter(entry => entry.path.startsWith(paymentPath) && entry.method !== 'GET').length
        await nativeInput(document.querySelector('textarea[placeholder="可选，直接粘贴 SVG 源码"]'), svg)
        const preview = document.querySelector('.icon-preview')
        assert.ok(preview.querySelector('[data-payment-icon-placeholder]'))
        assert.equal(preview.querySelector('svg'), null)
        assertSafeDOM(preview, 'unsafe input native preview')
        nativeButton(document.querySelector('.ant-modal-footer'), '确定').click(); await settle()
        assert.equal(trace.filter(entry => entry.path.startsWith(paymentPath) && entry.method !== 'GET').length, beforeWrites, 'UI rejects before dispatch')
        dom.push({ label: 'unsafe native preview', input: svg, html: preview.outerHTML })
        for (const [verb, endpoint] of [['POST', paymentPath], ['PUT', `${paymentPath}/${created.id}`]]) {
          assert.equal((await wire(verb, endpoint, { name: 'Never Persist Unsafe', epayType: 'alipay', iconSvg: svg, enabled: true, sort: 0, remark: '' })).status, 400)
          assert.equal((await snapshot(`after rejected ${verb}`)).sha256, before.sha256, 'no persisted mutation')
        }
      }
      await unmountAll()
      passed += 1
    })

    await t.test('ordinary/anonymous actual POST/PUT/DELETE are 403/401 and preserve all DB rows', async () => {
      assert.ok(created, 'valid insert prerequisite')
      for (const [identity, status] of [['user', 403], ['anonymous', 401]]) {
        for (const [verb, endpoint] of [['POST', paymentPath], ['PUT', `${paymentPath}/${created.id}`], ['DELETE', `${paymentPath}/${created.id}`]]) {
          const before = await snapshot(`before ${identity} ${verb}`)
          assert.equal((await wire(verb, endpoint, verb === 'DELETE' ? undefined : { name: 'Unauthorized Write', epayType: 'alipay', iconSvg: customSVG, enabled: true }, identity)).status, status, `${identity} ${verb}`)
          assert.equal((await snapshot(`after ${identity} ${verb}`)).sha256, before.sha256)
        }
      }
      assert.equal((await wire('GET', paymentPath, undefined, 'anonymous')).status, 401)
      passed += 1
    })

    await t.test('admin includes disabled/user filters; Enabled-only disable-all preserves raw legacy and disabled native order click attempts zero recharge API', async () => {
      actor = 'admin'
      const all = await API.getPaymentMethods()
      assert.equal(all.status, 200)
      assert.ok(all.data.some(row => row.id === 3 && row.enabled === false))
      actor = 'user'
      const enabled = await API.getPaymentMethods()
      assert.equal(enabled.status, 200)
      assert.ok(enabled.data.length > 0)
      assert.equal(enabled.data.some(row => row.id === 3), false)
      assert.ok(enabled.data.every(row => row.enabled))
      const before = await snapshot('before test-only disable-all')
      assert.equal((await wire('POST', '/__fixture__/disable-all')).status, 200)
      const disabled = await snapshot('after test-only disable-all')
      assert.equal(disabled.enabledCount, 0)
      assert.equal(disabled.count, before.count)
      for (let index = 0; index < before.rows.length; index += 1) {
        const { enabled: oldEnabled, ...oldRow } = before.rows[index]
        const { enabled: newEnabled, ...newRow } = disabled.rows[index]
        assert.equal(typeof oldEnabled, 'boolean'); assert.equal(newEnabled, false)
        assert.deepEqual(newRow, oldRow, 'test fixture changes Enabled only')
      }
      assert.equal(disabled.rows.find(row => row.id === 1).iconSvg, legacySVG)
      assert.deepEqual(await API.getPaymentMethods(), { status: 200, data: [] }, 'actual user empty GET wire contract')
      user.setStoredCurrentUser(ordinary)
      const usage = mountPage('src/pages/UserUsage/index.vue')
      await eventually(() => !usage.vm.paymentMethodLoading && fixtureReads.some(row => row.path === '/adapter-service/billing-records'), 'disabled UserUsage settled')
      assert.deepEqual([...usage.vm.paymentMethods], [])
      assert.equal(document.querySelectorAll('.payment-method-card').length, 0)
      assert.match(document.body.textContent, /暂无可用支付方式/)
      const order = nativeButton(document.querySelector('.recharge-actions'), '创建充值订单')
      assert.equal(order.disabled, true)
      const callsBeforeClick = trace.length, fixturesBeforeClick = fixtureReads.length
      order.click(); await settle()
      assert.equal(trace.length, callsBeforeClick, 'disabled native click emits no HTTP, including recharge')
      assert.equal(fixtureReads.length, fixturesBeforeClick)
      assert.equal(trapped.length, 0, 'no recharge/provider/other write attempted')
      dom.push({ label: 'all disabled native order', html: document.querySelector('.recharge-form').outerHTML })
      assert.equal((await snapshot('after disabled native order click')).sha256, disabled.sha256)
      await unmountAll()
      passed += 1
    })
    assert.equal(passed, 4, 'every stateful integration phase actually passed')
    assert.equal(trapped.length, 0)
    assert.ok(trace.some(entry => entry.method === 'GET' && entry.path === paymentPath && entry.status === 200 && Array.isArray(entry.response) && entry.response.length === 0))
    save('completed.json', { subtests: 4, actualHTTPCount: trace.length, forbiddenAttempts: trapped.length, providerCalls: 0, realPaymentCalls: 0, boundary: 'fixture actor header, not production JWT/Cookie authentication; no original/preview DB' })
  } finally {
    await unmountAll(); user.clearStoredCurrentUser()
    if (ready && bridge && !exitResult) {
      shutdownRequested = true
      try { assert.equal((await wire('POST', '/__fixture__/shutdown')).status, 200) } catch (error) { save('shutdown-error.txt', error.stack + '\n') }
    }
    if (bridge && !exitResult) {
      await Promise.race([exitPromise, sleep(6000)])
      if (!exitResult) { bridge.kill('SIGTERM'); await Promise.race([exitPromise, sleep(3000)]) }
    }
    http.request = original.httpRequest; http.get = original.httpGet; https.request = original.httpsRequest; https.get = original.httpsGet
    globalThis.fetch = original.fetch; window.XMLHttpRequest = original.xhr; window.WebSocket = original.webSocket
    if (cachedRequest) require.cache[requestPath] = cachedRequest; else delete require.cache[requestPath]
    const afterHashes = sourceHashes()
    save('source-after.json', afterHashes); save('actual-http-trace.json', trace); save('db-snapshots.json', snapshots); save('dom-evidence.json', dom)
    save('readonly-fixtures.json', fixtureReads); save('forbidden-attempts.json', trapped)
    save('bridge-stdout.txt', childOut); save('bridge-stderr.txt', childErr); save('bridge-exit.json', { ...exitResult, shutdownRequested })
    console.log(`S02_NATIVE_EVIDENCE=${evidence}`)
    assert.deepEqual(afterHashes, beforeHashes, 'production frontend source hashes unchanged')
    assert.deepEqual(exitResult, { code: 0, signal: null }, `bridge exits successfully\n${childOut}\n${childErr}`)
  }
})
