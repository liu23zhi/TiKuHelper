import test from 'node:test'
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { createRequire } from 'node:module'
import vm from 'node:vm'

const require = createRequire(import.meta.url)
const { JSDOM } = require('jsdom')
const webRoot = process.env.TIKU_THEME_SOURCE || path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const modulePath = path.join(webRoot, 'src/utils/themeMode.mjs')
const bootstrapPath = path.join(webRoot, 'public/theme-init.js')
const KEY = 'tiku.theme-mode.v1'
async function implementation() {
  assert.ok(existsSync(modulePath), 'U-02 contract missing: themeMode.mjs must exist')
  return import(pathToFileURL(modulePath).href)
}
function fixture({ mode = null, dark = false, legacy = false } = {}) {
  const dom = new JSDOM('<!doctype html><html><head></head><body></body></html>', { url: 'http://localhost', runScripts: 'outside-only' })
  const { window } = dom
  if (mode !== null) window.localStorage.setItem(KEY, mode)
  const storage = window.localStorage
  const mediaListeners = new Set()
  const storageListeners = new Set()
  const media = { matches: dark, media: '(prefers-color-scheme: dark)' }
  const add = fn => mediaListeners.add(fn)
  const remove = fn => mediaListeners.delete(fn)
  if (legacy) { media.addListener = add; media.removeListener = remove }
  else { media.addEventListener = (name, fn) => { assert.equal(name, 'change'); add(fn) }; media.removeEventListener = (name, fn) => { assert.equal(name, 'change'); remove(fn) } }
  const addEvent = window.addEventListener.bind(window)
  const removeEvent = window.removeEventListener.bind(window)
  window.addEventListener = (name, fn, ...rest) => { if (name === 'storage') storageListeners.add(fn); return addEvent(name, fn, ...rest) }
  window.removeEventListener = (name, fn, ...rest) => { if (name === 'storage') storageListeners.delete(fn); return removeEvent(name, fn, ...rest) }
  window.matchMedia = query => { assert.equal(query, '(prefers-color-scheme: dark)'); return media }
  const options = { getStorage: () => window.localStorage, createMediaQuery: query => window.matchMedia(query), root: window.document.documentElement, eventTarget: window }
  const mediaChange = dark => { media.matches = dark; for (const fn of [...mediaListeners]) fn({ matches: dark, media: media.media }) }
  const storageChange = (key, newValue, area = storage) => window.dispatchEvent(new window.StorageEvent('storage', { key, newValue, storageArea: area }))
  const close = controller => { controller?.dispose(); dom.window.close() }
  return { dom, window, storage, media, options, mediaChange, storageChange, mediaListeners, storageListeners, root: options.root, close }
}
function assertRoot(f, mode, resolved) {
  assert.equal(f.root.getAttribute('data-theme-mode'), mode)
  assert.equal(f.root.getAttribute('data-theme'), resolved)
  assert.equal(f.root.style.colorScheme, resolved)
  assert.equal(f.root.style.backgroundColor, resolved === 'dark' ? 'rgb(17, 17, 19)' : 'rgb(245, 245, 247)')
}
function boot(f) {
  assert.ok(existsSync(bootstrapPath), 'U-02 contract missing: synchronous theme-init.js must exist')
  new vm.Script(readFileSync(bootstrapPath, 'utf8'), { filename: bootstrapPath }).runInContext(f.dom.getInternalVMContext())
}

test('normalization rejects malformed/untrusted persisted values instead of coercing them', async () => {
  const { normalizeThemeMode } = await implementation()
  for (const mode of ['light', 'dark', 'system']) assert.equal(normalizeThemeMode(mode), mode)
  for (const raw of [null, undefined, '', 'Dark', ' dark', 'system ', 'null', 0, {}, { toString() { throw new Error('must not coerce') } }]) assert.equal(normalizeThemeMode(raw), 'system')
})
test('explicit choices take precedence and only system resolves the OS preference', async () => {
  const { resolveTheme } = await implementation()
  for (const [mode, dark, want] of [['light', false, 'light'], ['light', true, 'light'], ['dark', false, 'dark'], ['dark', true, 'dark'], ['system', false, 'light'], ['system', true, 'dark'], ['invalid', true, 'dark']]) assert.equal(resolveTheme(mode, dark), want)
})
for (const [stored, dark, mode, resolved] of [
  [null, false, 'system', 'light'], [null, true, 'system', 'dark'],
  ['light', false, 'light', 'light'], ['light', true, 'light', 'light'],
  ['dark', false, 'dark', 'dark'], ['dark', true, 'dark', 'dark'],
  ['system', false, 'system', 'light'], ['system', true, 'system', 'dark'],
  ['broken', false, 'system', 'light'], ['broken', true, 'system', 'dark'],
]) test(`initial storage=${stored} OS=${dark ? 'dark' : 'light'} applies ${mode}/${resolved} to the real root`, async () => {
  const { createThemeController } = await implementation()
  const f = fixture({ mode: stored, dark }); const c = createThemeController(f.options)
  try { assert.deepEqual(c.getState(), { mode, resolved }); assertRoot(f, mode, resolved) } finally { f.close(c) }
})
test('system media changes update root/subscribers without overwriting persisted mode', async () => {
  const { createThemeController } = await implementation(); const f = fixture({ mode: 'system' }); const c = createThemeController(f.options); const seen = []
  try { c.subscribe(state => seen.push(state)); f.mediaChange(true); assert.deepEqual(seen, [{ mode: 'system', resolved: 'dark' }]); assertRoot(f, 'system', 'dark'); assert.equal(f.storage.getItem(KEY), 'system') } finally { f.close(c) }
})
for (const fixed of ['light', 'dark']) test(`${fixed} ignores later system changes`, async () => {
  const { createThemeController } = await implementation(); const f = fixture({ mode: fixed }); const c = createThemeController(f.options); const seen = []
  try { c.subscribe(state => seen.push(state)); f.mediaChange(true); f.mediaChange(false); assert.deepEqual(seen, []); assertRoot(f, fixed, fixed) } finally { f.close(c) }
})
test('same-tab changes update state/root immediately, persist only the theme key, and normalize invalid input', async () => {
  const { createThemeController } = await implementation(); const f = fixture({ dark: true }); f.storage.setItem('adapter_current_user', 'private-display-fixture'); const c = createThemeController(f.options)
  try { c.setMode('light'); assertRoot(f, 'light', 'light'); assert.equal(f.storage.getItem(KEY), 'light'); c.setMode('invalid'); assertRoot(f, 'system', 'dark'); assert.equal(f.storage.getItem(KEY), 'system'); assert.equal(f.storage.getItem('adapter_current_user'), 'private-display-fixture') } finally { f.close(c) }
})
test('localStorage property getter failures do not prevent system initialization or in-memory choice', async () => {
  const { createThemeController } = await implementation(); const f = fixture({ dark: true }); Object.defineProperty(f.window, 'localStorage', { configurable: true, get() { throw new f.window.DOMException('denied', 'SecurityError') } }); const c = createThemeController(f.options)
  try { assertRoot(f, 'system', 'dark'); assert.doesNotThrow(() => c.setMode('light')); assertRoot(f, 'light', 'light') } finally { f.close(c) }
})
test('getItem failures fall back to system and setItem failures retain the in-memory selection', async () => {
  const { createThemeController } = await implementation(); const f = fixture({ dark: true }); f.options.getStorage = () => ({ getItem() { throw new Error('denied') }, setItem() { throw new Error('quota') } }); const c = createThemeController(f.options)
  try { assert.deepEqual(c.getState(), { mode: 'system', resolved: 'dark' }); assert.doesNotThrow(() => c.setMode('light')); assertRoot(f, 'light', 'light') } finally { f.close(c) }
})
for (const fixed of ['system', 'dark']) test(`matchMedia exceptions use light fallback without discarding ${fixed}`, async () => {
  const { createThemeController } = await implementation(); const f = fixture({ mode: fixed }); f.options.createMediaQuery = () => { throw new Error('not supported') }; const c = createThemeController(f.options)
  try { assertRoot(f, fixed, fixed === 'dark' ? 'dark' : 'light') } finally { f.close(c) }
})
test('missing browser capabilities safely initialize to system/light', async () => {
  const { createThemeController } = await implementation(); const c = createThemeController({ getStorage: () => null, createMediaQuery: () => null, root: null, eventTarget: null })
  try { assert.deepEqual(c.getState(), { mode: 'system', resolved: 'light' }); c.setMode('dark'); assert.deepEqual(c.getState(), { mode: 'dark', resolved: 'dark' }) } finally { c.dispose() }
})
for (const legacy of [false, true]) test(`${legacy ? 'legacy' : 'modern'} media registration is paired with cleanup and no callback after disposal`, async () => {
  const { createThemeController } = await implementation(); const f = fixture({ legacy }); const c = createThemeController(f.options); const seen = []
  try { c.subscribe(state => seen.push(state)); assert.equal(f.mediaListeners.size, 1); assert.equal(f.storageListeners.size, 1); c.dispose(); c.dispose(); assert.equal(f.mediaListeners.size, 0); assert.equal(f.storageListeners.size, 0); f.mediaChange(true); f.storageChange(KEY, 'dark'); c.setMode('dark'); assertRoot(f, 'system', 'light'); assert.deepEqual(seen, []); assert.equal(f.storage.getItem(KEY), null) } finally { f.close(c) }
})
test('unsubscribe stops updates and returned state cannot mutate the controller', async () => {
  const { createThemeController } = await implementation(); const f = fixture(); const c = createThemeController(f.options); const seen = []
  try { const stop = c.subscribe(state => seen.push(state)); const snapshot = c.getState(); snapshot.mode = 'dark'; assert.equal(c.getState().mode, 'system'); c.setMode('dark'); stop(); stop(); c.setMode('light'); assert.deepEqual(seen, [{ mode: 'dark', resolved: 'dark' }]) } finally { f.close(c) }
})
test('mode changes notify even when resolved color stays equal, unchanged state does not notify', async () => {
  const { createThemeController } = await implementation(); const f = fixture({ dark: true }); const c = createThemeController(f.options); const seen = []
  try { c.subscribe(state => seen.push(state)); c.setMode('dark'); c.setMode('dark'); assert.deepEqual(seen, [{ mode: 'dark', resolved: 'dark' }]) } finally { f.close(c) }
})
test('cross-tab theme changes, invalid values and deletion resolve without echo writes', async () => {
  const { createThemeController } = await implementation(); const f = fixture({ dark: true }); const c = createThemeController(f.options)
  try { f.storageChange(KEY, 'light'); assertRoot(f, 'light', 'light'); assert.equal(f.storage.getItem(KEY), null); f.storageChange(KEY, 'broken'); assertRoot(f, 'system', 'dark'); f.storageChange(KEY, 'dark'); f.storageChange(KEY, null); assertRoot(f, 'system', 'dark') } finally { f.close(c) }
})
test('unrelated/sessionStorage events cannot change the theme or authentication storage', async () => {
  const { createThemeController } = await implementation(); const f = fixture({ mode: 'light' }); f.storage.setItem('adapter_current_user', 'fixture'); const c = createThemeController(f.options)
  try { f.storageChange('adapter_current_user', 'dark'); f.storageChange(KEY, 'dark', f.window.sessionStorage); assertRoot(f, 'light', 'light'); assert.equal(f.storage.getItem('adapter_current_user'), 'fixture') } finally { f.close(c) }
})
test('storage clear re-reads only the saved preference and follows the current system', async () => {
  const { createThemeController } = await implementation(); const f = fixture({ mode: 'light', dark: true }); const c = createThemeController(f.options)
  try { f.storage.removeItem(KEY); f.storageChange(null, null); assertRoot(f, 'system', 'dark') } finally { f.close(c) }
})
test('unmount during a subscriber callback prevents late delivery to remaining subscribers', async () => {
  const { createThemeController } = await implementation(); const f = fixture(); const c = createThemeController(f.options); const late = []
  try { c.subscribe(() => c.dispose()); c.subscribe(state => late.push(state)); c.setMode('dark'); assert.deepEqual(late, []); assert.equal(f.mediaListeners.size, 0) } finally { f.close(c) }
})
for (const [stored, dark, mode, resolved] of [
  [null, false, 'system', 'light'], [null, true, 'system', 'dark'],
  ['light', true, 'light', 'light'], ['dark', false, 'dark', 'dark'],
  ['system', true, 'system', 'dark'], ['invalid', true, 'system', 'dark'],
]) test(`bootstrap storage=${stored} OS=${dark ? 'dark' : 'light'} applies expected state without listeners`, async () => {
  const f = fixture({ mode: stored, dark })
  try { boot(f); assertRoot(f, mode, resolved); assert.equal(f.mediaListeners.size, 0); assert.equal(f.storageListeners.size, 0); const { createThemeController } = await implementation(); const c = createThemeController(f.options); assert.deepEqual(c.getState(), { mode, resolved }); c.dispose() } finally { f.close() }
})
test('bootstrap tolerates blocked localStorage getter and absent matchMedia', () => {
  const f = fixture()
  try { Object.defineProperty(f.window, 'localStorage', { get() { throw new Error('denied') } }); f.window.matchMedia = undefined; assert.doesNotThrow(() => boot(f)); assertRoot(f, 'system', 'light') } finally { f.close() }
})
test('controller takeover re-reads OS/storage instead of trusting stale bootstrap attributes', async () => {
  const { createThemeController } = await implementation(); const f = fixture({ mode: 'dark' })
  try { boot(f); assertRoot(f, 'dark', 'dark'); f.storage.setItem(KEY, 'light'); f.media.matches = true; const c = createThemeController(f.options); assertRoot(f, 'light', 'light'); c.dispose() } finally { f.close() }
})
test('HTML loads the same-origin bootstrap synchronously in head before app content', () => {
  const html = readFileSync(path.join(webRoot, 'public/index.html'), 'utf8'); const dom = new JSDOM(html)
  try { const script = dom.window.document.head.querySelector('script'); assert.ok(script, 'head has no early theme script'); assert.equal(script.getAttribute('src'), '<%= BASE_URL %>theme-init.js'); assert.equal(script.hasAttribute('async'), false); assert.equal(script.hasAttribute('defer'), false); assert.notEqual(script.getAttribute('type'), 'module'); assert.equal(dom.window.document.body.querySelector('script'), null) } finally { dom.window.close() }
})
