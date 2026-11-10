/*
 * Decorative accent contrast check (45 §0.27.3 / 78 §6.6).
 *
 * Reads the real light/dark token blocks from src/styles/theme-tokens.css and
 * measures every --accent-* value against the surfaces it may be painted on:
 *   canvas  = --bg          (light #F5F5F7 / dark #111113 -- we also check the
 *                            authored dark surface because accents sit on cards)
 *   surface = --surface     (light #FFFFFF / dark #1C1C1E)
 *   raised  = --surface-raised (light #FFFFFF / dark #242426)
 *
 * Thresholds: >=4.5:1 on the theme canvas (body text), >=3:1 on the card
 * surfaces (icons / labels / chart series are non-text graphics).
 * Exit code 1 when any measurement fails, so this doubles as a CI gate.
 */
const fs = require('node:fs')
const path = require('node:path')

const cssPath = path.resolve(__dirname, '../src/styles/theme-tokens.css')
const css = fs.readFileSync(cssPath, 'utf8')

const THEME_CANVAS = { light: '#F5F5F7', dark: '#1C1C1E' }
const CARD_SURFACES = { light: ['#FFFFFF'], dark: ['#1C1C1E', '#242426'] }

function rgb(hex) {
  const m = hex.trim().replace('#', '').match(/../g)
  if (!m || m.length !== 3) throw new Error('not a hex color: ' + hex)
  return m.map(x => parseInt(x, 16) / 255)
}
function luminance(hex) {
  const [r, g, b] = rgb(hex).map(v => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4))
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
function contrast(a, b) {
  const x = luminance(a), y = luminance(b)
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05)
}
function section(theme) {
  const m = css.match(new RegExp("html\\[data-theme='" + theme + "'\\]\\s*\\{([^}]+)\\}"))
  if (!m) throw new Error('missing token block for theme ' + theme)
  return m[1]
}
function tokens(body) {
  const out = {}
  for (const m of body.matchAll(/--([a-z0-9-]+)\s*:\s*([^;]+);/g)) out[m[1]] = m[2].trim()
  return out
}

let failed = 0
const report = []
for (const theme of ['light', 'dark']) {
  const map = tokens(section(theme))
  const accents = Object.keys(map).filter(k => k.startsWith('accent-')).sort()
  if (accents.length !== 4) {
    failed++
    console.error(`FAIL ${theme}: expected 4 decorative tokens, found ${accents.length} (${accents.join(', ')})`)
  }
  for (const key of accents) {
    const value = map[key]
    if (/var\(/.test(value)) {
      failed++
      console.error(`FAIL ${theme} --${key}: decorative tokens must be literal colors, not aliases (${value})`)
      continue
    }
    const canvas = contrast(value, THEME_CANVAS[theme])
    if (canvas < 4.5) {
      failed++
      console.error(`FAIL ${theme} --${key} ${value} on canvas ${THEME_CANVAS[theme]} = ${canvas.toFixed(3)}:1 (< 4.5 text)`)
    }
    const surfaces = CARD_SURFACES[theme].map(bg => ({ bg, ratio: contrast(value, bg) }))
    for (const s of surfaces) {
      if (s.ratio < 3) {
        failed++
        console.error(`FAIL ${theme} --${key} ${value} on surface ${s.bg} = ${s.ratio.toFixed(3)}:1 (< 3.0 graphic)`)
      }
    }
    report.push({
      theme,
      token: '--' + key,
      value,
      checks: [{ background: THEME_CANVAS[theme], kind: 'canvas', ratio: Number(canvas.toFixed(3)) }]
        .concat(surfaces.map(s => ({ background: s.bg, kind: 'surface', ratio: Number(s.ratio.toFixed(3)) }))),
    })
  }
  // Decorative family must never collide with a semantic value: a shared hex
  // makes "category color" and "status color" indistinguishable to the user.
  const semantic = ['success-fg', 'warning-fg', 'danger-fg', 'info-fg']
    .filter(k => map[k] && /^#[0-9a-fA-F]{6}$/.test(map[k]))
    .map(k => ({ key: k, value: map[k].toLowerCase() }))
  for (const key of accents) {
    const hit = semantic.find(s => s.value === map[key].toLowerCase())
    if (hit) {
      failed++
      console.error(`FAIL ${theme} --${key} ${map[key]} is identical to semantic --${hit.key}`)
    }
  }
}

console.log(JSON.stringify({ scope: 'src/styles/theme-tokens.css light+dark decorative accents; WCAG 2.1 relative luminance', thresholds: { canvas: 4.5, surface: 3 }, measurements: report, failures: failed }, null, 2))
process.exitCode = failed ? 1 : 0
