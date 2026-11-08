// S-02 static SVG contract. This is deliberately a small ASCII XML subset,
// not HTML sanitization; bounded inert exporter metadata is permitted. No entities, namespaces other than root xmlns, CSS,
// URLs, text nodes, comments, processing instructions or active elements.
export const PAYMENT_ICON_ERROR = 'SVG 图标不安全或不受支持，请重新选择内置图标或使用静态 SVG'
const elements = {
  svg: ['xmlns', 'viewBox', 'width', 'height', 'focusable', 'aria-hidden', 'role', 'data-icon'],
  g: [], path: ['d'], circle: ['cx', 'cy', 'r'], ellipse: ['cx', 'cy', 'rx', 'ry'],
  rect: ['x', 'y', 'width', 'height', 'rx', 'ry'], line: ['x1', 'y1', 'x2', 'y2'],
  polyline: ['points'], polygon: ['points'],
}
const common = ['fill', 'stroke', 'stroke-width', 'stroke-linecap', 'stroke-linejoin', 'stroke-miterlimit', 'stroke-dasharray', 'stroke-dashoffset', 'opacity', 'fill-opacity', 'stroke-opacity', 'fill-rule', 'clip-rule', 'transform']
const number = '[+-]?(?:[0-9]+(?:\\.[0-9]*)?|\\.[0-9]+)(?:[eE][+-]?[0-9]+)?'
const numeric = new RegExp(`^${number}$(?![\\s\\S])`)
const numbers = new RegExp(`^${number}(?:[ ,\\t\\r\\n]+${number})*$`)
const dimension = new RegExp(`^(${number})(px|em|%)?$(?![\\s\\S])`)
function boundedNumber(value) { return numeric.test(value) && Number.isFinite(Number(value)) && Math.abs(Number(value)) <= 1000000 }
function numericList(value, count) {
  if (!numbers.test(value)) return false
  const values = value.split(/[ ,\t\r\n]+/)
  return (!count || values.length === count) && values.every(boundedNumber)
}

// Parse only static geometry numbers. Bounded tokens also prevent enormous
// exponents/invalid command-only paths from crossing the render boundary.
function validPath(value) {
  const numericStart = new RegExp(`^${number}`)
  const sizes = { m: 2, l: 2, h: 1, v: 1, c: 6, s: 4, q: 4, t: 2, a: 7, z: 0 }
  let rest = value, first = true
  const skip = () => { rest = rest.replace(/^[ ,\t\r\n]+/, '') }
  while (rest) {
    skip(); if (!rest) break
    const command = rest[0].toLowerCase(), size = sizes[command]
    if (size === undefined || (first && command !== 'm')) return false
    first = false; rest = rest.slice(1); const values = []
    for (;;) {
      skip(); if (!rest || /^[MmLlHhVvCcSsQqTtAaZz]/.test(rest)) break
      if (!size) return false
      const offset = values.length % size
      let token
      // SVG arc flags are single characters and may be packed (e.g. 016.72).
      if (command === 'a' && (offset === 3 || offset === 4)) {
        if (!/^[01]/.test(rest)) return false
        token = rest[0]
      } else { const match = rest.match(numericStart); if (!match) return false; token = match[0] }
      if (!boundedNumber(token) || (command === 'a' && offset < 2 && Number(token) < 0)) return false
      values.push(token); rest = rest.slice(token.length)
    }
    if (size && (!values.length || values.length % size)) return false
  }
  return !first
}
function validTransform(value) {
  const token = /(matrix|translate|scale|rotate|skewX|skewY)\(([^()]*)\)[ \t\r\n]*/g
  let previous = 0, count = 0, match
  const arities = { matrix: [6], translate: [1, 2], scale: [1, 2], rotate: [1, 3], skewX: [1], skewY: [1] }
  while ((match = token.exec(value))) {
    if (match.index !== previous || !numericList(match[2]) || !arities[match[1]].includes(match[2].split(/[ ,\t\r\n]+/).length)) return false
    previous = token.lastIndex; count++
  }
  return count > 0 && previous === value.length
}
function isInertMetadata(key, value) {
  if (['t', 'p-id'].includes(key)) return /^[0-9]{1,20}$(?![\s\S])/.test(value)
  if (key === 'version') return /^[0-9]{1,4}(?:[.][0-9]{1,4})?$(?![\s\S])/.test(value)
  if (key === 'class') return /^[a-zA-Z0-9_ -]{0,128}$(?![\s\S])/.test(value)
  if (['data-name', 'aria-label'].includes(key)) {
    return value.length <= 80 && /^[a-zA-Z0-9_ \u3400-\u9fff-]*$(?![\s\S])/.test(value)
  }
  return false
}
function validAttribute(tag, key, value) {
  if (isInertMetadata(key, value)) return true
  if (!elements[tag].includes(key) && !common.includes(key)) return false
  if (key === 'xmlns') return tag === 'svg' && value === 'http://www.w3.org/2000/svg'
  if (key === 'viewBox') return numericList(value, 4)
  if (key === 'width' || key === 'height') { const m = value.match(dimension); return !!m && boundedNumber(m[1]) && Number(m[1]) >= 0 }
  if (key === 'aria-hidden' || key === 'focusable') return value === 'true' || value === 'false'
  if (key === 'role') return value === 'img'
  if (key === 'data-icon') return /^[a-z0-9-]{1,64}$(?![\s\S])/.test(value)
  if (key === 'fill' || key === 'stroke') return /^(?:none|currentColor|transparent|black|white|red|green|blue|gray|grey|#[0-9a-fA-F]{3}|#[0-9a-fA-F]{4}|#[0-9a-fA-F]{6}|#[0-9a-fA-F]{8})$(?![\s\S])/.test(value)
  if (key === 'd') return validPath(value)
  if (key === 'points' || key === 'stroke-dasharray') return numericList(value)
  if (key === 'fill-rule' || key === 'clip-rule') return value === 'nonzero' || value === 'evenodd'
  if (key === 'stroke-linecap') return ['butt', 'round', 'square'].includes(value)
  if (key === 'stroke-linejoin') return ['miter', 'round', 'bevel'].includes(value)
  if (key === 'transform') return validTransform(value)
  if (key.includes('opacity')) return boundedNumber(value) && Number(value) >= 0 && Number(value) <= 1
  return boundedNumber(value)
}
function hasOnlyPaymentIconASCII(value) {
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index)
    if (code !== 9 && code !== 10 && code !== 13 && (code < 32 || (code > 126 && (code < 0x3400 || code > 0x9fff)))) return false
  }
  return true
}
function paymentIconBytes(value) {
  let bytes = 0
  for (const character of value) {
    const code = character.codePointAt(0)
    bytes += code <= 0x7f ? 1 : code <= 0x7ff ? 2 : code <= 0xffff ? 3 : 4
  }
  return bytes
}
export function parseSafePaymentIcon(raw) {
  if (typeof raw !== 'string' || paymentIconBytes(raw) > 16384 || !hasOnlyPaymentIconASCII(raw) || /[&!?]/.test(raw)) return null
  const source = raw.trim()
  if (!source) return null // caller distinguishes optional empty from unsafe nonempty
  let position = 0, root = null, count = 0
  const stack = []
  while (position < source.length) {
    const whitespace = source.slice(position).match(/^[ \t\r\n]+/)
    if (whitespace) { position += whitespace[0].length; continue }
    const close = source.slice(position).match(/^<\/([A-Za-z][A-Za-z0-9-]*)>/)
    if (close) {
      if (!stack.length || stack.pop().tag !== close[1]) return null
      position += close[0].length; continue
    }
    const opening = source.slice(position).match(/^<([A-Za-z][A-Za-z0-9-]*)/)
    if (!opening || !Object.prototype.hasOwnProperty.call(elements, opening[1])) return null
    const tag = opening[1]
    if ((!root && tag !== 'svg') || (root && (!stack.length || tag === 'svg')) || (stack.length && !['svg', 'g'].includes(stack.at(-1).tag))) return null
    position += opening[0].length
    const attrs = Object.create(null)
    let attributeCount = 0
    for (;;) {
      const rest = source.slice(position)
      const end = rest.match(/^[ \t\r\n]*(\/?)>/)
      if (end) {
        if (++count > 256 || stack.length + 1 > 16) return null
        const node = { tag, attrs, children: [] }
        if (!root) root = node
        else stack.at(-1).children.push(node)
        if (!end[1]) stack.push(node)
        position += end[0].length
        break
      }
      const attr = rest.match(/^[ \t\r\n]+([A-Za-z][A-Za-z0-9-]*)[ \t\r\n]*=[ \t\r\n]*(?:"([^"<>]*)"|'([^'<>]*)')/)
      if (!attr) return null
      const key = attr[1], value = attr[2] ?? attr[3]
      if (++attributeCount > 24 || value.length > 8192 || Object.prototype.hasOwnProperty.call(attrs, key) || !validAttribute(tag, key, value)) return null
      attrs[key] = value
      position += attr[0].length
    }
  }
  return root && !stack.length ? root : null
}
export function isSafePaymentIcon(raw) {
  return typeof raw === 'string' && paymentIconBytes(raw) <= 16384 && hasOnlyPaymentIconASCII(raw) && (!raw.trim() || !!parseSafePaymentIcon(raw))
}
