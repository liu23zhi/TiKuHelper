// AI 搜索配置页「UI 错位」的回归钉子。
//
// 用户报的现象：AI 搜索配置页布局错位（未给截图）。
// 实测定性为四个独立问题，前三个是同一个根因链：
//
//  1) AIConnectionModelSection.vue / AIAdvancedSettingsSection.vue 的 <style scoped>
//     只定义了 .hint，把 .console-form-grid / .full-width / .ai-section-heading /
//     .unit-label / .switch-description 都指望父组件 index.vue 的 scoped 样式兜住。
//     —— 这在 Vue SFC 里不可能生效：父 scoped 规则编译成 `.x[data-v-父]`，
//     子组件模板元素带的是 data-v-子。于是
//       · .console-form-grid 没有 display:grid → 两列布局塌成单列；
//       · .full-width 没有 grid-column: 1/-1 → Prompt 文本域、模型列表、
//         分隔线被挤进「一列」；.unit-label 没 margin-left → 「次」「元/次」贴住输入框。
//     这就是用户看到的错位主因。
//  2) index.vue 自身残留一整套同样名字的 scoped 规则，而它的模板一次都没用到
//     （已用正则统计：console-form-grid=0、full-width=0、model-editor=0 …），
//     属于拆分组件时的死代码，且会误导后人以为「父级已经管了」。
//  3) AIPromptSettingsSection.vue 的 .console-form-grid 带了 padding: 24px，
//     与外层 ConsolePanel__body 的 24px 叠加成 48px → 该卡片内容比相邻卡片
//     多缩进一倍，形成横向错位。
//  4) ≤768px 的断点必须把网格降为单列，且三处 section 都要有（原来只有
//     AIPromptSettingsSection 有，另两个没有 → 窄屏右侧溢出）。
//
// 本测试的执行方式：**截取真实 .vue 源码里的 <style scoped> 区段原文**，
// 用真实 CSS 解析（不用 includes 字符串匹配），从而：
//   · 变异「删掉某条规则」→ 解析结果里该选择器消失 → 用例红；
//   · 变异「把类名改错」→ 同样红；
//   · 变异「把 padding 加回来」→ padding 断言红。
// 只用 includes 的话，「选择器名出现在注释里」也会判绿 —— 那是假钉子。

import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const webRoot = process.env.TIKU_WEB_SOURCE
  || path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const pageDir = path.join(webRoot, 'src/pages/AISearchConfig')

const SECTIONS = {
  'AIConnectionModelSection.vue': { gridClass: 'connection-grid' },
  'AIAdvancedSettingsSection.vue': { gridClass: 'execution-grid' },
  'AIPromptSettingsSection.vue': { gridClass: 'execution-grid' },
}

function readSource(file) {
  return readFileSync(path.join(pageDir, file), 'utf8')
}

// extractStyleScoped 精确截出 `<style scoped>` … `</style>` 之间的原文。
//
// 用 ^ 行首锚定的正则而不是 indexOf('<style scoped>')：注释里提到
// “父组件 index.vue 的 <style scoped>”这种散文是正常写法，
// 用裸 indexOf 会把它误当成第二个标记（实测踩到）。行首锚定只认可
// 真正的块级标记，同时也保留了「标记被复制」的检测能力。
function styleTagOffsets(source, file) {
  const re = /^<style scoped>[ \t]*$/gm
  const matches = [...source.matchAll(re)]
  assert.ok(matches.length > 0, `${file}: 缺少行首的 <style scoped> 标记`)
  assert.equal(
    matches.length, 1,
    `${file}: 行首 <style scoped> 出现 ${matches.length} 次 —— 只允许一个样式块，`
    + '多块会让「测的是哪一块」变得不确定',
  )
  return matches[0]
}

function extractStyleScoped(source, file) {
  const match = styleTagOffsets(source, file)
  const bodyStart = match.index + match[0].length
  const closeIndex = source.indexOf('</style>', bodyStart)
  assert.notEqual(closeIndex, -1, `${file}: 缺少 </style> 标记`)
  assert.equal(
    source.indexOf('</style>', closeIndex + 1), -1,
    `${file}: </style> 出现多次 —— 测试可能截到错误的区段`,
  )
  const body = source.slice(bodyStart, closeIndex)
  assert.ok(body.trim().length > 0, `${file}: 样式区段为空，测试会静默放过一切`)
  return { body, startIndex: bodyStart, endIndex: closeIndex }
}

// parseRules 把 CSS 文本解析成 [{selectors:[...], declarations:{prop:value}}]。
// 只处理本文件用到的形态（无 @import / 无嵌套、@media 单独收集），
// 足够分辨「这条规则到底在不在」，比字符串 includes 硬得多。
function stripComments(css) {
  return css.replace(/\/\*[\s\S]*?\*\//g, '')
}

function splitDeclarations(block) {
  const out = {}
  for (const raw of block.split(';')) {
    const decl = raw.trim()
    if (!decl) continue
    const colon = decl.indexOf(':')
    if (colon <= 0) continue
    const prop = decl.slice(0, colon).trim().toLowerCase()
    const value = decl.slice(colon + 1).trim().replace(/\s+/g, ' ')
    if (prop) out[prop] = value
  }
  return out
}

function parseRules(css) {
  const clean = stripComments(css)
  const rules = []
  let index = 0
  while (index < clean.length) {
    const open = clean.indexOf('{', index)
    if (open === -1) break
    const header = clean.slice(index, open).trim()
    // 找到配对的右花括号（支持一层 @media 嵌套）
    let depth = 0
    let cursor = open
    let close = -1
    for (; cursor < clean.length; cursor += 1) {
      if (clean[cursor] === '{') depth += 1
      else if (clean[cursor] === '}') {
        depth -= 1
        if (depth === 0) { close = cursor; break }
      }
    }
    if (close === -1) break
    const inner = clean.slice(open + 1, close)
    if (header.startsWith('@media')) {
      // 收集 media 里的规则，并打上 media 标记
      for (const rule of parseRules(inner)) rules.push({ ...rule, media: header })
    } else if (header.startsWith('@')) {
      // 其它 at-rule 忽略
    } else {
      const selectors = header.split(',').map(s => s.trim()).filter(Boolean)
      rules.push({ selectors, declarations: splitDeclarations(inner), media: null })
    }
    index = close + 1
  }
  return rules
}

// findRule 找「在指定 media 上下文下、选择器精确等于 selector」的规则。
// media === null 表示只在顶层找；media === 'any' 表示不限。
function findRule(rules, selector, media = 'any') {
  return rules.find(rule => rule.selectors.includes(selector)
    && (media === 'any' || (media === null ? rule.media === null : rule.media === media)))
}

function declarationOf(rules, selector, prop, media = 'any') {
  const rule = findRule(rules, selector, media)
  if (!rule) return undefined
  return rule.declarations[prop]
}

// ─────────────────────── 用例 ───────────────────────

for (const [file, meta] of Object.entries(SECTIONS)) {
  test(`${file}: 自己定义了两列网格（不依赖父组件的 scoped 样式）`, () => {
    const { body } = extractStyleScoped(readSource(file), file)
    const rules = parseRules(body)
    const display = declarationOf(rules, '.console-form-grid', 'display', null)
    assert.equal(
      display, 'grid',
      `${file}: .console-form-grid 必须在本组件 <style scoped> 顶层声明 display: grid。`
      + '父组件 index.vue 的 scoped 规则对子组件不生效 —— 缺这条就是整页塌成单列的错位根因。',
    )
    const columns = declarationOf(rules, '.console-form-grid', 'grid-template-columns', null)
    assert.equal(
      columns, 'repeat(2, minmax(0, 1fr))',
      `${file}: 两列网格必须用 minmax(0, 1fr)，否则长内容（模型 ID / Prompt）会把列撑破`,
    )
    assert.ok(
      readSource(file).includes(meta.gridClass),
      `${file}: 模板里应保留 ${meta.gridClass} 标记类`,
    )
  })

  test(`${file}: .full-width 跨满整行（Prompt 文本域 / 模型列表不能被挤成一列）`, () => {
    const { body } = extractStyleScoped(readSource(file), file)
    const rules = parseRules(body)
    const span = declarationOf(rules, '.full-width', 'grid-column', null)
    assert.equal(
      span, '1 / -1',
      `${file}: .full-width 必须声明 grid-column: 1 / -1。`
      + '缺这条时 Prompt 文本域、模型列表等本应占满整行的表单项只占一列宽 —— 这是用户报的错位之一。',
    )
  })

  test(`${file}: .ai-section-heading 自带内边距与分隔线`, () => {
    const { body } = extractStyleScoped(readSource(sourceName(file)), file)
    const rules = parseRules(body)
    const padding = declarationOf(rules, '.ai-section-heading', 'padding', null)
    assert.ok(
      padding, `${file}: .ai-section-heading 必须声明 padding，否则标题贴住卡片边框`,
    )
    const borderBottom = declarationOf(rules, '.ai-section-heading', 'border-bottom', null)
    assert.ok(
      borderBottom, `${file}: .ai-section-heading 必须声明 border-bottom，否则标题与内容区没有分隔`,
    )
  })

  test(`${file}: ≤768px 断点把网格降为单列（窄屏不溢出）`, () => {
    const { body } = extractStyleScoped(readSource(sourceName(file)), file)
    const rules = parseRules(body)
    const media768 = rules.filter(rule => rule.media && /max-width:\s*768px/.test(rule.media))
    assert.ok(media768.length > 0, `${file}: 缺少 @media (max-width: 768px) 断点`)
    const single = media768.find(rule => rule.selectors.includes('.console-form-grid')
      && rule.declarations['grid-template-columns'] === 'minmax(0, 1fr)')
    assert.ok(
      single,
      `${file}: 768px 断点内必须把 .console-form-grid 降为 minmax(0, 1fr) 单列，`
      + '否则两列在窄屏会横向溢出',
    )
  })
}

function sourceName(file) {
  return file
}

test('三个 section 的 .console-form-grid 不能带 padding（会与 ConsolePanel__body 的 24px 叠加）', () => {
  for (const file of Object.keys(SECTIONS)) {
    const { body } = extractStyleScoped(readSource(file), file)
    const rules = parseRules(body)
    const padding = declarationOf(rules, '.console-form-grid', 'padding', null)
    assert.equal(
      padding, undefined,
      `${file}: .console-form-grid 不得声明 padding。外层 ConsolePanel__body 已经给 24px，`
      + '再叠一层会让本卡片比相邻卡片多缩进一倍 —— 这是横向错位的直接原因（原值 padding: 24px）。',
    )
  }
})

test('index.vue 不再残留对子组件无效的同名布局规则（死代码会误导后人）', () => {
  const file = 'index.vue'
  const source = readSource(file)
  const { body } = extractStyleScoped(source, file)
  const rules = parseRules(body)
  // 这些类只出现在子 section 的模板里；写在父组件的 scoped 样式里永远不生效。
  const forbidden = ['.console-form-grid', '.full-width', '.unit-label',
    '.switch-description', '.model-editor', '.fetch-btn', '.model-tags']
  for (const selector of forbidden) {
    const rule = findRule(rules, selector)
    assert.equal(
      rule, undefined,
      `${file}: 不应再定义 ${selector} —— 它只被子组件模板使用，`
      + '父组件 scoped 规则对他们不生效，留着会让人误以为布局已经由父级兜住。',
    )
  }
})

test('index.vue 用到的布局类仍在（避免误删本地有效的规则）', () => {
  const file = 'index.vue'
  const source = readSource(file)
  const { body } = extractStyleScoped(source, file)
  const rules = parseRules(body)
  // 这些类索引模板里确实在用，删了会真的坏。
  for (const selector of ['.ocr-cache-grid', '.staged-points-editor', '.pricing-grid', '.ai-config-form']) {
    const rule = findRule(rules, selector)
    assert.ok(rule, `${file}: ${selector} 在模板中使用，规则不能被删`)
  }
})

test('index.vue 的 .ocr-cache-grid 在 768px 断点降为单列', () => {
  const file = 'index.vue'
  const { body } = extractStyleScoped(readSource(file), file)
  const rules = parseRules(body)
  const media768 = rules.filter(rule => rule.media && /max-width:\s*768px/.test(rule.media))
  assert.ok(media768.length > 0, `${file}: 缺少 768px 断点`)
  const single = media768.find(rule => rule.selectors.includes('.ocr-cache-grid')
    && rule.declarations['grid-template-columns'] === 'minmax(0, 1fr)')
  assert.ok(single, `${file}: 768px 断点内 .ocr-cache-grid 必须降为单列`)
})

test('三个 section 都用 ConsolePanel 的结构（console-panel + ai-section 类）', () => {
  for (const file of Object.keys(SECTIONS)) {
    const source = readSource(file)
    const styleIndex = source.indexOf('<style scoped>')
    const template = source.slice(0, styleIndex)
    assert.match(
      template,
      /class="console-panel ai-section"/,
      `${file}: 根元素应保持 console-panel ai-section 两个类（全局 console.css 提供边框与圆角）`,
    )
    assert.match(
      template,
      /class="section-heading ai-section-heading"/,
      `${file}: 标题区应保持 section-heading ai-section-heading 两个类`,
    )
  }
})

test('AIAdvancedSettingsSection / AIConnectionModelSection 的 .unit-label 与 .switch-description 有定义或无需定义', () => {
  // unit-label 只在 AIAdvancedSettingsSection 用；switch-description 只在 AIConnectionModelSection 用。
  const advSource = readSource('AIAdvancedSettingsSection.vue')
  const advStyle = parseRules(extractStyleScoped(advSource, 'AIAdvancedSettingsSection.vue').body)
  assert.ok(
    findRule(advStyle, '.unit-label'),
    'AIAdvancedSettingsSection.vue: .unit-label 必须在本组件定义 —— '
    + '否则「次」「元/次」没有 margin-left，会直接贴在输入框右边，形成基线错位。',
  )
  assert.equal(
    declarationOf(advStyle, '.unit-label', 'margin-left'),
    '8px',
    'AIAdvancedSettingsSection.vue: .unit-label 的 margin-left 应为 8px（与既有视觉一致）',
  )

  const connSource = readSource('AIConnectionModelSection.vue')
  const connStyle = parseRules(extractStyleScoped(connSource, 'AIConnectionModelSection.vue').body)
  assert.ok(
    findRule(connStyle, '.switch-description'),
    'AIConnectionModelSection.vue: .switch-description 必须在本组件定义 —— '
    + '否则「已启用 AI 搜索」文案紧贴开关，缺少间距。',
  )
  assert.equal(
    declarationOf(connStyle, '.switch-description', 'margin-left'),
    '12px',
    'AIConnectionModelSection.vue: .switch-description 的 margin-left 应为 12px',
  )
})
