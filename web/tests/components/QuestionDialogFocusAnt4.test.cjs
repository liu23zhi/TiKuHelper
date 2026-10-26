/* Focus-only component contract; real Antd4 in jsdom, no Chromium or live persistence. */
const { test } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const apiPath = path.join(web, 'src/api/TikuComponentApi.js')
const api = new Module(apiPath)
const writes = [], reads = []
const record = () => ({ id: 701, type: 0, question: 'SYNTHETIC_FOCUS_ONLY', options: '[]', answer: '[]', source: 0 })
api.loaded = true
api.exports = new Proxy({
  getCourses: () => Promise.resolve({ status: 200, data: [] }),
  getQuestions: () => Promise.resolve({ status: 200, data: { items: [record()], total: 1 } }),
}, { get(target, name) {
  if (name === '__esModule') return false
  if (target[name]) return (...args) => { reads.push({ name, args }); return target[name](...args) }
  return (...args) => { writes.push({ name, args }); return Promise.resolve({ status: 500, message: 'NO_PERSISTENCE_OR_LIVE_IO' }) }
} })
require.cache[apiPath] = api
const Tiku = require(path.join(web, 'src/pages/TikuComponent/index.vue'))
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 25)); await nextTick() }
function visible(el) { for (let n = el; n && n.nodeType === 1; n = n.parentElement) if (n.hidden || n.style.display === 'none' || n.getAttribute('aria-hidden') === 'true') return false; return true }
function dialogs() { return [...document.querySelectorAll('[role="dialog"]')].filter(visible) }
async function fixture(run) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: Tiku }] })
  await router.push('/'); await router.isReady()
  const w = mount(Tiku, { attachTo: document.body, global: { plugins: [router, Antd], stubs: { transition: false, 'transition-group': false } } })
  try { await settle(); await run(w) }
  finally { w.vm.visible = false; w.unmount(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); router.options.history.destroy(); document.body.replaceChildren(); assert.deepEqual(writes, [], 'all APIs mocked; zero mutations') }
}
function button(w, text) { const b = w.findAll('button').find(b => b.text().replace(/\s+/g, '') === text); assert.ok(b, 'fixture button ' + text); return b.element }
async function open(w, text = '添加') { const opener = button(w, text); assert.notEqual(document.activeElement, opener, 'native click without any opener pre-focus'); opener.click(); for (let i = 0; i < 65; i++) { await settle(); if (dialogs()[0]?.contains(document.activeElement)) break } assert.equal(dialogs().length, 1); assert.ok(dialogs()[0].contains(document.activeElement), 'real Antd modal owns focus before close'); return opener }
function modal(w) { const m = w.findAllComponents(Antd.Modal).find(m => m.props('title') === '详情内容'); assert.ok(m); return m }
function afterClose(w) { const callback = modal(w).props('afterClose'); assert.equal(typeof callback, 'function', 'owned public afterClose callback required'); return callback }
async function close() { const cancel = [...document.querySelectorAll('.ant-modal-footer button')].find(b => ['Cancel', '取消'].includes(b.textContent.replace(/\s+/g, '')) && visible(b)); assert.ok(cancel); cancel.click(); for (let i = 0; i < 65; i++) { await settle(); if (!dialogs().length) break } await new Promise(resolve => setTimeout(resolve, 100)); await settle(); assert.equal(dialogs().length, 0) }
function outside() { const b = document.createElement('button'); b.textContent = 'NEW_USER_FOCUS'; document.body.appendChild(b); return b }
function report(name, extra = {}) { console.log('QUESTION_FOCUS ' + JSON.stringify({ name, ...extra, writes: writes.length, nativeChromium: false })) }
for (const [name, entry] of [['QF01 native Add click', '添加'], ['QF02 native row Edit click', '编辑']]) {
  test(name + ' returns exact clicked opener after real modal closes', () => fixture(async w => { const opener = await open(w, entry); await close(); const restored = document.activeElement === opener; report(name, { restored, active: document.activeElement?.tagName }); assert.equal(restored, true) }))
}
test('QF03 no-event showModal preserves suitable active fallback', () => fixture(async w => { const fallback = outside(); fallback.focus(); w.vm.showModal(record(), 1); await settle(); await close(); report('QF03', { restored: document.activeElement === fallback }); assert.equal(document.activeElement, fallback) }))
test('QF04 close then reopen and late old afterClose cannot steal or close new dialog', () => fixture(async w => {
  await open(w); const stale = afterClose(w); w.vm.visible = false; await nextTick(); const edit = button(w, '编辑'); edit.click(); await settle(); const active = document.activeElement; stale(); await settle(); report('QF04', { dialogCount: dialogs().length, preserved: document.activeElement === active }); assert.equal(dialogs().length, 1); assert.equal(document.activeElement, active); await close(); assert.equal(document.activeElement, edit)
}))
test('QF05 external new user focus is not stolen during close', () => fixture(async w => { await open(w); const next = outside(); const cancel = document.querySelector('.ant-modal-footer button'); cancel.click(); next.focus(); await closeIfOpen(); report('QF05', { preserved: document.activeElement === next }); assert.equal(document.activeElement, next) }))
async function closeIfOpen() { for (let i = 0; i < 65; i++) { await settle(); if (!dialogs().length) break } await new Promise(resolve => setTimeout(resolve, 100)); await settle(); assert.equal(dialogs().length, 0) }
test('QF06 unmount invalidates captured afterClose', () => fixture(async w => { await open(w); const stale = afterClose(w); w.unmount(); const next = outside(); next.focus(); stale(); await settle(); report('QF06', { preserved: document.activeElement === next }); assert.equal(document.activeElement, next) }))
test('QF07 detached opener is never focused', () => fixture(async w => { const opener = await open(w); let attempted = 0; const original = opener.focus; opener.focus = () => { attempted++ }; opener.remove(); await close(); opener.focus = original; report('QF07', { attempted }); assert.equal(attempted, 0) }))
test('QF08 disabled opener is never focused', () => fixture(async w => { const opener = await open(w); opener.disabled = true; let attempted = 0; const original = opener.focus; opener.focus = () => { attempted++ }; await close(); opener.focus = original; report('QF08', { attempted }); assert.equal(attempted, 0) }))
test('QF09 hidden opener is never focused', () => fixture(async w => { const opener = await open(w); opener.style.display = 'none'; let attempted = 0; const original = opener.focus; opener.focus = () => { attempted++ }; await close(); opener.focus = original; report('QF09', { attempted }); assert.equal(attempted, 0) }))
test('QF10 obsolete callback after owner completion cannot re-focus', () => fixture(async w => { await open(w); const stale = afterClose(w); await close(); const next = outside(); next.focus(); stale(); await settle(); report('QF10', { preserved: document.activeElement === next }); assert.equal(document.activeElement, next) }))
test('QF11 opening while focused elsewhere does not bounce through native prior active element on close', () => fixture(async w => { const prior = outside(); prior.focus(); let focuses = 0; prior.addEventListener('focus', () => focuses++); const opener = await open(w); await close(); report('QF11', { priorFocusBounces: focuses, restored: document.activeElement === opener }); assert.equal(focuses, 0); assert.equal(document.activeElement, opener) }))

test('QF12 closing question and opening bulk dialog cannot bounce focus back to old opener', () => fixture(async w => {
  const checkbox = w.find('tbody input[type="checkbox"]'); assert.ok(checkbox.exists()); checkbox.element.click(); await settle()
  const opener = await open(w); const stale = afterClose(w); let oldOpenerBounces = 0
  opener.addEventListener('focus', () => oldOpenerBounces++)
  const question = dialogs().find(d => d.textContent.includes('详情内容'))
  const cancel = [...question.querySelectorAll('.ant-modal-footer button')].find(b => ['Cancel', '取消'].includes(b.textContent.replace(/\s+/g, '')))
  assert.ok(cancel); cancel.click(); button(w, '批量编辑课程名').click()
  let bulk
  for (let i = 0; i < 65; i++) { await settle(); bulk = dialogs().find(d => d.textContent.includes('批量编辑课程名称')); if (bulk?.contains(document.activeElement)) break }
  assert.ok(bulk?.contains(document.activeElement), 'new real bulk modal acquired focus')
  const active = document.activeElement; stale(); await settle()
  report('QF12', { oldOpenerBounces, preserved: document.activeElement === active, bulkVisible: w.vm.bulkCourseVisible })
  assert.equal(document.activeElement, active); assert.equal(w.vm.bulkCourseVisible, true); assert.equal(oldOpenerBounces, 0)
}))
