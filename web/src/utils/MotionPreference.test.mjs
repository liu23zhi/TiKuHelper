import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)
const webRoot = process.env.TIKU_THEME_SOURCE || path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const modulePath = path.join(webRoot, 'src/utils/themeMode.mjs')
const tokensPath = path.join(webRoot, 'src/styles/theme-tokens.css')
const COLOR_QUERY = '(prefers-color-scheme: dark)'
const MOTION_QUERY = '(prefers-reduced-motion: reduce)'
const THEME_KEY = 'tiku.theme-mode.v1'
const MOTION_KEY = 'tiku.motion-mode.v1'

async function implementation() { return import(pathToFileURL(modulePath).href) }

// Minimal contract-shaped fixture: one media list per query, same listener API.
function fixture({ theme = null, motion = null, reduced = false } = {}) {
  const { JSDOM } = require('jsdom')
  const dom = new JSDOM('<!doctype html><html><head></head><body></body></html>', { url: 'http://localhost', runScripts: 'outside-only' })
  const { window } = dom
  if (theme !== null) window.localStorage.setItem(THEME_KEY, theme)
  if (motion !== null) window.localStorage.setItem(MOTION_KEY, motion)
  const lists = { color: { matches: false, media: COLOR_QUERY, listeners: new Set() }, motion: { matches: reduced, media: MOTION_QUERY, listeners: new Set() } }
  const list = key => {
    const entry = lists[key]
    return {
      media: entry.media,
      get matches() { return entry.matches },
      addEventListener: (name, fn) => { assert.equal(name, 'change'); entry.listeners.add(fn) },
      removeEventListener: (name, fn) => { assert.equal(name, 'change'); entry.listeners.delete(fn) },
      addListener: fn => entry.listeners.add(fn),
      removeListener: fn => entry.listeners.delete(fn),
    }
  }
  const storageListeners = new Set()
  const addEvent = window.addEventListener.bind(window)
  const removeEvent = window.removeEventListener.bind(window)
  window.addEventListener = (name, fn, ...rest) => { if (name === 'storage') storageListeners.add(fn); return addEvent(name, fn, ...rest) }
  window.removeEventListener = (name, fn, ...rest) => { if (name === 'storage') storageListeners.delete(fn); return removeEvent(name, fn, ...rest) }
  window.matchMedia = query => {
    if (query === COLOR_QUERY) return list('color')
    if (query === MOTION_QUERY) return list('motion')
    throw new Error('unexpected media query ' + query)
  }
  const options = { getStorage: () => window.localStorage, createMediaQuery: query => window.matchMedia(query), root: window.document.documentElement, eventTarget: window }
  const setReduced = next => { lists.motion.matches = Boolean(next); for (const fn of [...lists.motion.listeners]) fn({ matches: lists.motion.matches, media: MOTION_QUERY }) }
  const setDark = next => { lists.color.matches = Boolean(next); for (const fn of [...lists.color.listeners]) fn({ matches: lists.color.matches, media: COLOR_QUERY }) }
  const close = controller => { controller?.dispose(); dom.window.close() }
  return { window, storage: window.localStorage, options, root: options.root, lists, setReduced, setDark, close }
}

test('motion normalization accepts exactly on/off and defaults everything else to auto', async () => {
  const { normalizeMotionMode } = await implementation()
  for (const value of ['on', 'off', 'auto']) assert.equal(normalizeMotionMode(value), value)
  for (const raw of [null, undefined, '', 'On', ' off', 'auto ', 0, 1, true, {}, { toString() { throw new Error('must not coerce') } }]) assert.equal(normalizeMotionMode(raw), 'auto')
})

test('system reduced motion outranks an in-app motion=on, while explicit off always wins', async () => {
  const { resolveMotion } = await implementation()
  assert.equal(resolveMotion('on', false), 'on')
  assert.equal(resolveMotion('on', true), 'off', 'a medical OS preference must not be overridden by the app switch')
  assert.equal(resolveMotion('off', false), 'off')
  assert.equal(resolveMotion('off', true), 'off')
  assert.equal(resolveMotion('auto', false), 'on')
  assert.equal(resolveMotion('auto', true), 'off')
  assert.equal(resolveMotion('broken', true), 'off')
})

test('storage key is a separate tiku.*.v1 channel so a motion choice never rewrites the theme choice', async () => {
  const { MOTION_STORAGE_KEY } = await implementation()
  assert.equal(MOTION_STORAGE_KEY, MOTION_KEY)
  const f = fixture({ theme: 'light', motion: 'off' })
  const { createThemeController } = await implementation()
  const c = createThemeController(f.options)
  try {
    assert.equal(f.root.getAttribute('data-theme'), 'light')
    assert.equal(f.root.getAttribute('data-motion'), 'off')
    c.setMotion('on')
    assert.equal(f.storage.getItem(MOTION_KEY), 'on')
    assert.equal(f.storage.getItem(THEME_KEY), 'light', 'theme storage must not be touched by a motion change')
    c.setMode('dark')
    assert.equal(f.storage.getItem(THEME_KEY), 'dark')
    assert.equal(f.storage.getItem(MOTION_KEY), 'on', 'theme storage must not be touched by a theme change')
  } finally { f.close(c) }
})

test('data-motion carries the stored choice, not the effective value, so the media override can see on', async () => {
  const { createThemeController } = await implementation()
  const f = fixture({ motion: 'on', reduced: true })
  const c = createThemeController(f.options)
  try {
    assert.equal(f.root.getAttribute('data-motion'), 'on', 'the attribute keeps the user choice for the reduce override to target')
    assert.deepEqual(c.getMotionState(), { mode: 'on', resolved: 'off' })
    c.setMotion('auto')
    assert.equal(f.root.getAttribute('data-motion'), 'auto')
    assert.deepEqual(c.getMotionState(), { mode: 'auto', resolved: 'off' })
  } finally { f.close(c) }
})

test('a reduced-motion media change reaches motion subscribers even while the theme is pinned', async () => {
  const { createThemeController } = await implementation()
  const f = fixture({ theme: 'light', motion: 'auto' })
  const c = createThemeController(f.options)
  const themeSeen = []
  const motionSeen = []
  try {
    c.subscribe(state => themeSeen.push(state))
    c.subscribeMotion(state => motionSeen.push(state))
    assert.equal(f.lists.motion.listeners.size, 1, 'the motion query is observed')
    f.setReduced(true)
    assert.deepEqual(motionSeen, [{ mode: 'auto', resolved: 'off' }])
    assert.deepEqual(themeSeen, [], 'a pinned theme hears nothing from the motion query')
    f.setReduced(false)
    assert.equal(motionSeen.at(-1).resolved, 'on')
    f.setDark(true)
    assert.deepEqual(themeSeen, [], 'explicit light ignores the colour query')
    assert.equal(f.lists.motion.listeners.size, 1)
    assert.equal(f.lists.color.listeners.size, 1)
  } finally { f.close(c) }
})

test('cross-tab motion changes apply to root and storage and ignore unrelated keys', async () => {
  const { createThemeController } = await implementation()
  const f = fixture({ theme: 'dark', motion: 'auto' })
  const c = createThemeController(f.options)
  try {
    f.window.dispatchEvent(new f.window.StorageEvent('storage', { key: MOTION_KEY, newValue: 'off', storageArea: f.storage }))
    assert.equal(f.root.getAttribute('data-motion'), 'off')
    assert.equal(f.root.getAttribute('data-theme'), 'dark')
    f.window.dispatchEvent(new f.window.StorageEvent('storage', { key: MOTION_KEY, newValue: 'nonsense', storageArea: f.storage }))
    assert.equal(f.root.getAttribute('data-motion'), 'auto')
    f.window.dispatchEvent(new f.window.StorageEvent('storage', { key: 'adapter_current_user', newValue: 'off', storageArea: f.storage }))
    assert.equal(f.root.getAttribute('data-motion'), 'auto', 'unrelated keys cannot move the motion state')
  } finally { f.close(c) }
})

test('head bootstrap applies the stored motion choice before Vue mounts', () => {
  const { JSDOM } = require('jsdom')
  const vm = require('node:vm')
  const script = readFileSync(path.join(webRoot, 'public/theme-init.js'), 'utf8')
  for (const [stored, want] of [['off', 'off'], ['on', 'on'], [null, 'auto'], ['broken', 'auto']]) {
    const dom = new JSDOM('<!doctype html><html><head></head><body></body></html>', { url: 'http://localhost', runScripts: 'outside-only' })
    try {
      if (stored !== null) dom.window.localStorage.setItem(MOTION_KEY, stored)
      dom.window.matchMedia = () => ({ matches: false })
      new vm.Script(script).runInContext(dom.getInternalVMContext())
      assert.equal(dom.window.document.documentElement.getAttribute('data-motion'), want, 'stored ' + stored)
    } finally { dom.window.close() }
  }
})

test('theme-tokens.css suppresses motion for data-motion=off on elements and pseudos', () => {
  const css = readFileSync(tokensPath, 'utf8')
  const rule = css.match(/html\[data-motion='off'\][^{]*\{([^}]*)\}/)
  assert.ok(rule, 'html[data-motion=off] rule must exist')
  const selectors = css.slice(css.indexOf("html[data-motion='off']"), css.indexOf('{', css.indexOf("html[data-motion='off']")))
  for (const selector of ['html[data-motion=\'off\'] *', "html[data-motion='off'] *::before", "html[data-motion='off'] *::after"]) assert.ok(selectors.includes(selector), 'missing ' + selector)
  assert.match(rule[1], /animation-duration:\s*\.01ms\s*!important/)
  assert.match(rule[1], /transition-duration:\s*\.01ms\s*!important/)
})

function mediaBlocks(css, header) {
  const blocks = []
  let from = 0
  for (;;) {
    const start = css.indexOf(header, from)
    if (start < 0) return blocks
    let depth = 0
    let end = start
    for (; end < css.length; end++) {
      if (css[end] === '{') depth++
      else if (css[end] === '}') { depth--; if (depth === 0) { end++; break } }
    }
    blocks.push({ start, text: css.slice(start, end) })
    from = end
  }
}

test('theme-tokens.css lets system reduce override an explicit in-app motion=on', () => {
  const css = readFileSync(tokensPath, 'utf8')
  // The file already had an unrelated reduce block (scroll-behavior); the
  // override must be its own block that specifically targets data-motion=on.
  const blocks = mediaBlocks(css, '@media (prefers-reduced-motion: reduce)')
  const override = blocks.find(block => block.text.includes("html[data-motion='on']"))
  assert.ok(override, 'theme-tokens.css needs a reduce block targeting html[data-motion=on]')
  assert.match(override.text, /animation-duration:\s*\.01ms\s*!important/, 'with !important so author animations cannot win')
  assert.match(override.text, /transition-duration:\s*\.01ms\s*!important/)
  for (const pseudo of ["html[data-motion='on'] *::before", "html[data-motion='on'] *::after"]) assert.ok(override.text.includes(pseudo), 'pseudo-elements covered: ' + pseudo)
  assert.ok(css.indexOf("html[data-motion='off']") < override.start, 'the off rule is declared before the override block')
})
