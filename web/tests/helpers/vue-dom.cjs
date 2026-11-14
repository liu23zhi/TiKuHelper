const fs = require('node:fs')
const path = require('node:path')
const Module = require('node:module')
const { JSDOM } = require('jsdom')
const { parse, compileScript, compileTemplate } = require('@vue/compiler-sfc')
const { transformSync } = require('@babel/core')

const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost', pretendToBeVisual: true })
// `Document` / `Text` / `Comment` / `DocumentFragment` 是 Vue runtime-dom 的 v-model
// 指令在 beforeUpdate 里做 instanceof 判断时需要的构造函数。只注入 `document`
// 实例不够：此时 `instanceof Document` 会抛 "Document is not defined"，
// 表现为「v-model 输入框在保存后不回显新值」这种与实现无关的假失败。
for (const key of ['window', 'document', 'navigator', 'history', 'location', 'open', 'Node', 'Element', 'HTMLElement', 'SVGElement', 'ShadowRoot', 'Event', 'KeyboardEvent', 'MouseEvent', 'MutationObserver', 'localStorage', 'Document', 'Text', 'Comment', 'DocumentFragment', 'NodeList', 'HTMLInputElement', 'CSSStyleDeclaration']) {
  Object.defineProperty(globalThis, key, { configurable: true, writable: true, value: dom.window[key] })
}
globalThis.getComputedStyle = dom.window.getComputedStyle.bind(dom.window)
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window)
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window)
// jsdom has no pseudo-element geometry; CSS layout is verified separately in the real browser.
const computedStyle = dom.window.getComputedStyle.bind(dom.window)
dom.window.getComputedStyle = element => computedStyle(element)

let width = 1440
let systemDark = false
let reducedMotion = false
const mediaLists = new Set()
function matches(query) {
  return String(query).split(',').some(alternative => alternative.trim().split(/\s+and\s+/).every(part => {
    const clause = part.trim()
    const size = clause.match(/^\((min|max)-width:\s*(\d+)px\)$/)
    if (size) return size[1] === 'min' ? width >= Number(size[2]) : width <= Number(size[2])
    const color = clause.match(/^\(prefers-color-scheme:\s*(dark|light)\)$/)
    if (color) return color[1] === 'dark' ? systemDark : !systemDark
    const motion = clause.match(/^\(prefers-reduced-motion:\s*(reduce|no-preference)\)$/)
    if (motion) return motion[1] === 'reduce' ? reducedMotion : !reducedMotion
    return false
  }))
}
window.matchMedia = query => {
  const listeners = new Set()
  const list = {
    media: String(query), matches: matches(query), listeners,
    addEventListener: (event, fn) => { if (event === 'change') listeners.add(fn) },
    removeEventListener: (event, fn) => { if (event === 'change') listeners.delete(fn) },
    addListener: fn => listeners.add(fn),
    removeListener: fn => listeners.delete(fn),
  }
  mediaLists.add(list)
  return list
}
function updateMedia() {
  for (const list of mediaLists) {
    const value = matches(list.media)
    if (list.matches !== value) {
      list.matches = value
      for (const listener of [...list.listeners]) listener({ matches: value, media: list.media })
    }
  }
}
function setViewport(next) {
  width = next
  Object.defineProperty(window, 'innerWidth', { configurable: true, value: width })
  updateMedia()
}
function setSystemDark(next) { systemDark = Boolean(next); updateMedia() }
function setReducedMotion(next) { reducedMotion = Boolean(next); updateMedia() }
function listenerCount(query) {
  return [...mediaLists].filter(list => list.media === query).reduce((sum, list) => sum + list.listeners.size, 0)
}

const webRoot = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
const src = path.resolve(webRoot, 'src') + path.sep
const originalJS = require.extensions['.js']
function commonJS(source, filename) {
  return transformSync(source, { filename, babelrc: false, configFile: false, plugins: [() => ({ visitor: { ImportDeclaration(importPath) { const value = importPath.node.source.value; if (value.startsWith('@/')) importPath.node.source.value = path.join(webRoot, 'src', value.slice(2)) } } }), require.resolve('@babel/plugin-transform-dynamic-import', { paths: [require.resolve('@vue/babel-preset-app')] }), require.resolve('@babel/plugin-transform-modules-commonjs', { paths: [require.resolve('@vue/babel-preset-app')] })] }).code
}
require.extensions['.js'] = (module, filename) => {
  if (filename.startsWith(src)) module._compile(commonJS(fs.readFileSync(filename, 'utf8'), filename), filename)
  else originalJS(module, filename)
}
require.extensions['.mjs'] = (module, filename) => {
  if (!filename.startsWith(src)) return originalJS(module, filename)
  module._compile(commonJS(fs.readFileSync(filename, 'utf8'), filename), filename)
}
require.extensions['.vue'] = (module, filename) => {
  const { descriptor, errors } = parse(fs.readFileSync(filename, 'utf8'), { filename })
  if (errors.length) throw errors[0]
  const id = 'test-component'
  const script = compileScript(descriptor, { id })
  // Script-only SFCs may deliberately forward an existing render function.
  // Compile a template only when present; never replace that renderer by a stub.
  module._compile(commonJS(script.content, filename), filename)
  const component = module.exports.default
  if (descriptor.template) {
    const template = compileTemplate({ source: descriptor.template.content, filename, id, compilerOptions: { bindingMetadata: script.bindings } })
    if (template.errors.length) throw template.errors[0]
    const templateModule = new Module(filename + '.template', module)
    templateModule.paths = module.paths
    templateModule._compile(commonJS(template.code, filename), filename)
    component.render = templateModule.exports.render
  }
  module.exports = component
}

// Match main.js's real globally registered upstream console primitives.
// Rendering their actual action slots is required for native-button/ownership tests.
const { config } = require('@vue/test-utils')
for (const name of ['ConsolePageHeading', 'ConsolePanel', 'ConsoleSummaryGrid', 'ConsoleTableShell']) {
  config.global.components[name] = require(path.join(webRoot, 'src/components', name + '.vue'))
}
module.exports = { setViewport, setSystemDark, setReducedMotion, listenerCount }
