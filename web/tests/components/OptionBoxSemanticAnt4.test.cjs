 'use strict'
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const { createRequire } = require('node:module')
const webRoot = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
assert.ok(webRoot && path.isAbsolute(webRoot), 'U04_FIXTURE_INFRA: an absolute source checkout is required')
const webRequire = createRequire(path.join(webRoot, 'package.json'))
// Unmodified qualified real SFC compiler + jsdom setup; not a replacement render/stub.
webRequire(path.join(webRoot, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = webRequire('@vue/test-utils')
const { nextTick } = webRequire('vue')
const { Button, Row, Col, Input, Tooltip } = webRequire('ant-design-vue')
const OptionBox = webRequire(path.join(webRoot, 'src/components/OptionBox/index.vue'))
const wrappers = []
const INPUT = Object.freeze({ options: Object.freeze(['甲', '乙']), answer: Object.freeze(['甲']), type: 0 })
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
function open(options, answer) {
  const w = mount(OptionBox, { attachTo: document.body, props: { options, answer, type: INPUT.type }, global: { plugins: [Button, Row, Col, Input, Tooltip] } })
  wrappers.push(w)
  return w
}
afterEach(async () => { for (const w of wrappers.splice(0).reverse()) w.unmount(); await settle(); document.body.innerHTML = '' })
function actualData(w) {
  const value = w.vm.getData()
  assert.deepEqual(Object.keys(value), ['options', 'answer'], 'U04_FIXTURE_INFRA: existing getData public shape changed')
  assert.equal(typeof value.options, 'string')
  assert.equal(typeof value.answer, 'string')
  return { raw: value, options: JSON.parse(value.options), answer: JSON.parse(value.answer) }
}
// Closed DOM-name forms only: valid aria-labelledby, aria-label, or plain visible native-button text.
// This is not a general AccName implementation or a native assistive-technology measurement.
function explicitDOMName(el) {
  const labelledby = (el.getAttribute('aria-labelledby') || '').trim()
  if (labelledby) {
    const ids = labelledby.split(/\s+/)
    const matches = ids.map(id => [...document.querySelectorAll('[id]')].filter(node => node.id === id))
    return { source: 'aria-labelledby', valid: matches.every(nodes => nodes.length === 1), value: matches.flat().map(node => node.textContent.trim()).join(' ').trim() }
  }
  const label = (el.getAttribute('aria-label') || '').trim()
  if (label) return { source: 'aria-label', valid: true, value: label }
  function plain(node) {
    if (node.nodeType === 3) return node.textContent
    if (node.nodeType !== 1 || node.getAttribute('aria-hidden') === 'true' || node.hasAttribute('hidden') || node.tagName.toLowerCase() === 'svg') return ''
    return [...node.childNodes].map(plain).join(' ')
  }
  return { source: 'plain DOM text', valid: true, value: plain(el).replace(/\s+/g, ' ').trim() }
}
function snapshot(w) {
  const targets = w.findAll('.CheckStyle')
  assert.equal(targets.length, 2, 'U04_FIXTURE_INFRA: fixed two actual toggle targets must be rendered')
  return targets.map(target => {
    const el = target.element
    el.focus() // Own jsdom DOM focus probe only; never Tab, Space, Enter or a fake key-to-click bridge.
    return { tag: el.tagName, nativeButton: el instanceof window.HTMLButtonElement, type: el.getAttribute('type'), name: explicitDOMName(el), pressed: el.getAttribute('aria-pressed'), tabIndex: el.tabIndex, focusedByDOMFocus: document.activeElement === el, classes: [...el.classList] }
  })
}
test('OptionBox real SFC answer toggles expose named native pressed buttons without changing legacy data', async () => {
  const w = open([...INPUT.options], [...INPUT.answer]); await settle()
  const originalProps = { options: [...INPUT.options], answer: [...INPUT.answer], type: INPUT.type }
  const stages = []
  function record(name, expectedAnswers) {
    const data = actualData(w)
    assert.deepEqual(data.options, [...INPUT.options], 'U04_FIXTURE_INFRA: click positive control changed option content/order')
    assert.deepEqual(data.answer, expectedAnswers, 'U04_FIXTURE_INFRA: click positive control changed legacy toggle semantics')
    assert.equal(data.raw.options, JSON.stringify(INPUT.options))
    assert.equal(data.raw.answer, JSON.stringify(expectedAnswers))
    stages.push({ name, data: data.raw, targets: snapshot(w) })
  }
  record('initial_A_selected', ['甲'])
  await w.findAll('.CheckStyle')[1].trigger('click'); await settle()
  record('B_click_preserves_type0_multi_selection', ['甲', '乙'])
  await w.findAll('.CheckStyle')[0].trigger('click'); await settle()
  record('A_click_deselects_only_A', ['乙'])
  await w.findAll('.CheckStyle')[1].trigger('click'); await settle()
  record('B_click_leaves_no_answers', [])
  await w.findAll('.CheckStyle')[0].trigger('click'); await settle()
  record('A_click_restores_initial_answer', ['甲'])
  assert.deepEqual(w.props('options'), originalProps.options)
  assert.deepEqual(w.props('answer'), originalProps.answer)
  assert.equal(w.props('type'), originalProps.type)
  // Separate content-preservation positive control: strings stay literal data, not sanitizer/rich-text policy.
  const rich = ['<strong>甲</strong>&nbsp;', '<span>乙 &amp; 二</span>']
  const rw = open([...rich], [rich[0]]); await settle()
  assert.deepEqual(actualData(rw).raw, { options: JSON.stringify(rich), answer: JSON.stringify([rich[0]]) })
  assert.deepEqual(rw.findAll('input').map(input => input.element.value), rich)
  await rw.findAll('.CheckStyle')[1].trigger('click'); await settle()
  assert.deepEqual(actualData(rw).raw, { options: JSON.stringify(rich), answer: JSON.stringify(rich) })
  assert.deepEqual(rw.props('options'), rich)
  const violations = []
  const expectedPressed = [['true', 'false'], ['true', 'true'], ['false', 'true'], ['false', 'false'], ['true', 'false']]
  const initialNames = stages[0].targets.map(target => target.name.value)
  for (const [si, stage] of stages.entries()) for (const [oi, target] of stage.targets.entries()) {
    const key = oi === 0 ? 'A' : 'B'
    const at = `${stage.name}/${key}`
    if (!target.nativeButton || target.tag !== 'BUTTON') violations.push(`${at}: not a native button`)
    if (target.type !== 'button') violations.push(`${at}: type must be button (no form submission)`)
    if (!target.name.valid || !target.name.value.includes(key) || !target.name.value.includes('答案')) violations.push(`${at}: name must identify the option and answer-toggle purpose`)
    if (target.name.value !== initialNames[oi]) violations.push(`${at}: toggle name changed with selection`)
    if (target.pressed !== expectedPressed[si][oi]) violations.push(`${at}: aria-pressed differs from observed getData selection`)
    if (target.tabIndex < 0 || !target.focusedByDOMFocus) violations.push(`${at}: native focusability unavailable`)
  }
  if (initialNames[0] === initialNames[1]) violations.push('two toggle names must distinguish A and B')
  const observation = { schema: 'U04_OPTIONBOX_NATIVE_DOM_BASELINE_OBSERVATION_V1', input: INPUT, observation_complete: true, click_getData_positive_controls_passed: true, type0_multi_selection_preserved: true, literal_rich_option_data_preserved: true, stages, rich_positive_control: actualData(rw).raw, semantic_violations: violations, semantic_contract_satisfied: violations.length === 0, native_keyboard_browser_exercised: false, browser_Tab_Space_Enter_qualified: false, computed_CSS_or_focus_ring_qualified: false }
  console.log('U04_OPTIONBOX_DOM_OBSERVATION ' + JSON.stringify(observation))
  assert.equal(violations.length, 0, 'U04_OPTIONBOX_SEMANTIC_CONTRACT: ' + violations.join('; '))
})
