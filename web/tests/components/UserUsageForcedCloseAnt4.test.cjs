// Real upstream Antd dialog, synthetic accounts and IO; no provider/payment calls.
// Same eight retirement/focus scenarios as the former Swal-specific suite.
// Retirement is asserted at the Vue DOM commit tick, not a fictitious synchronous Swal API.
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(process.env.UI_TEST_HELPER || path.join(root, 'tests/helpers/vue-dom.cjs'))
for (const name of ['DOMParser', 'CustomEvent', 'HTMLTemplateElement', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLSelectElement', 'HTMLTextAreaElement', 'HTMLVideoElement', 'HTMLAudioElement']) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: window[name] })
window.scrollTo = () => {}
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue')
const Antd = require('ant-design-vue'), Swal = require('sweetalert2')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const first = { id: 37, username: 'fixture-first', role: 'user', balance: 9.876543 }, second = { id: 38, username: 'fixture-second', role: 'user', balance: 1.234567 }
const marker = 'SYNTHETIC_FORCED_CLOSE_PRIVATE_SUBJECT'
const record = { id: 9101, type: 'normal_search', direction: 'expense', subject: marker, amount: 0.123456, provider: 'fixture' }
const ok = data => ({ status: 200, data })
const apiPath = path.join(root, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = { getCurrentUser: () => Promise.resolve(ok(user.getStoredCurrentUser())), getBillingRecords: () => Promise.resolve(ok({ data: user.getStoredCurrentUser()?.id === 37 ? [{ ...record }] : [], total: user.getStoredCurrentUser()?.id === 37 ? 1 : 0, page: 1, pageSize: 10 })), getPaymentMethods: () => Promise.resolve(ok([])) }; require.cache[apiPath] = api
const Usage = require(path.join(root, 'src/pages/UserUsage/index.vue'))
const css = document.createElement('style')
css.textContent = '.ant-zoom-leave-active { animation-duration: 0.15s; transition-duration: 0.15s; }'
document.head.appendChild(css)
let wrapper, trigger, safe, popup
const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function open() {
 user.setStoredCurrentUser(first); document.body.style.paddingRight = '7px'
 safe = document.createElement('input'); safe.setAttribute('data-safe-focus', ''); document.body.appendChild(safe)
 wrapper = mount(Usage, { attachTo: document.body, global: { plugins: [Antd], stubs: { transition: false, 'transition-group': false } } }); await settle()
 trigger = document.createElement('button'); trigger.textContent = 'ORIGINAL_FEE_TRIGGER'; wrapper.element.appendChild(trigger); trigger.focus()
 wrapper.vm.handleShowFeeDetail(wrapper.vm.dataSource[0]); await settle(); await pause(25)
 popup = document.querySelector('.billing-detail-dialog [role="dialog"]') || document.querySelector('[role="dialog"].billing-detail-dialog') || [...document.querySelectorAll('[role="dialog"]')].find(node => node.textContent.includes(marker))
 assert.ok(popup?.textContent.includes(marker), 'real upstream detail dialog contains private fixture')
 assert.equal(wrapper.vm.billingDetailOpen, true)
 assert.equal(wrapper.vm.selectedBillingRecord.id, record.id)
}
async function cleanAtCommit() {
 await settle()
 assert.equal(document.body.textContent.includes(marker), false, 'retired account private subject is gone at the Vue DOM commit')
 assert.equal(popup.isConnected, false, 'owned portal detaches, not merely hidden sensitive content')
 assert.equal(document.querySelector('.ant-modal-mask'), null, 'owned mask is unmounted')
 assert.equal(document.body.style.overflow, '', 'owned body scroll lock is released')
 assert.equal(document.body.style.paddingRight, '7px', 'pre-existing body padding is preserved')
 assert.equal(safe.getAttribute('aria-hidden'), null, 'background focus target remains accessible')
 if (wrapper) assert.equal(wrapper.element.parentElement.getAttribute('aria-hidden'), null)
 assert.equal(popup.contains(document.activeElement), false)
 if (wrapper) {
  assert.equal(wrapper.vm.billingDetailOpen, false)
  assert.equal(wrapper.vm.selectedBillingRecord, null)
 }
 console.log('CONTROLLED_DIALOG_RETIRED VueDOMTick owner-private-content-and-mask-removed')
}
afterEach(async () => {
 wrapper?.unmount(); wrapper = undefined; Swal.close(); Antd.Modal.destroyAll(); Antd.message.destroy()
 await settle(); await pause(180); document.body.innerHTML = ''; document.body.style.paddingRight = ''; window.localStorage.clear()
})
test('identity invalidation detaches the animated owned dialog at Vue commit without late returnFocus theft', async () => {
 await open(); let staleFocus = 0, safeBlur = 0
 trigger.addEventListener('focus', () => { staleFocus++ }); safe.addEventListener('blur', () => { safeBlur++ })
 user.setStoredCurrentUser(second); await cleanAtCommit(); safe.focus(); await pause(180)
 assert.equal(document.activeElement, safe); assert.equal(staleFocus, 0); assert.equal(safeBlur, 0)
 assert.equal(trigger.disabled, false); assert.equal(trigger.getAttribute('disabled'), null)
 trigger.focus(); assert.equal(document.activeElement, trigger)
})
test('component unmount detaches owned portal and mask and does not steal outside focus', async () => {
 await open(); wrapper.unmount(); wrapper = undefined; await cleanAtCommit(); safe.focus(); await pause(180); assert.equal(document.activeElement, safe)
})
test('normal owned close preserves ordinary focus return through the real Antd close lifecycle', async () => {
 await open(); wrapper.vm.billingDetailOpen = false; await settle(); await pause(25)
 popup.dispatchEvent(new window.Event('animationend', { bubbles: true })); popup.dispatchEvent(new window.Event('transitionend', { bubbles: true })); await settle(); await pause(220)
 assert.equal(popup.isConnected, false, 'normal close removes the dialog portal; the account may retain its own table rows')
 assert.equal(document.activeElement, trigger)
})
test('a newer popup already owned by another component remains open and unmodified on identity change and unmount', async () => {
 await open(); Swal.fire({ title: 'OTHER_CURRENT_POPUP', text: 'fixture', heightAuto: false, scrollbarPadding: false }); await settle(); await pause(25)
 const other = Swal.getPopup(); user.setStoredCurrentUser(second); wrapper.unmount(); wrapper = undefined; await settle(); await pause(180)
 assert.equal(Swal.getPopup(), other); assert.equal(other.style.getPropertyValue('animation-duration'), '')
 assert.equal(document.body.classList.contains('swal2-shown'), true)
})
test('a new popup after forced retirement keeps body lock and focus after old lifecycle callbacks', async () => {
 await open(); user.setStoredCurrentUser(second); await cleanAtCommit()
 Swal.fire({ title: 'OTHER_NEXT_POPUP', text: 'fixture', heightAuto: false, scrollbarPadding: false }); await settle(); await pause(180)
 const other = Swal.getPopup(); assert.ok(other?.textContent.includes('OTHER_NEXT_POPUP'))
 assert.equal(document.body.classList.contains('swal2-shown'), true); assert.equal(other.contains(document.activeElement), true)
})
test('invalidation during a pending normal close retires the old dialog without a captured stale focus return', async () => {
 await open(); let staleFocus = 0, safeBlur = 0
 trigger.addEventListener('focus', () => { staleFocus++ }); safe.addEventListener('blur', () => { safeBlur++ })
 wrapper.vm.billingDetailOpen = false; await nextTick(); user.setStoredCurrentUser(second); await cleanAtCommit(); safe.focus(); await pause(180)
 assert.equal(document.activeElement, safe); assert.equal(staleFocus, 0); assert.equal(safeBlur, 0)
})
test('after retirement a later normal popup returns focus to its own opener rather than the invalidated trigger', async () => {
 await open(); user.setStoredCurrentUser(second); await cleanAtCommit(); await pause(180)
 const nextTrigger = document.createElement('button'); nextTrigger.textContent = 'NEW_POPUP_TRIGGER'; document.body.appendChild(nextTrigger); nextTrigger.focus()
 Swal.fire({ title: 'LATER_NORMAL_POPUP', heightAuto: false, scrollbarPadding: false }); await settle(); await pause(25)
 Swal.close(); await pause(180); assert.equal(document.activeElement, nextTrigger)
})
test('forced retirement preserves exact pre-existing disabled property and attribute bytes', async () => {
 await open(); trigger.setAttribute('disabled', 'legacy-disabled-value')
 user.setStoredCurrentUser(second); await cleanAtCommit(); await pause(180)
 assert.equal(trigger.disabled, true); assert.equal(trigger.getAttribute('disabled'), 'legacy-disabled-value')
})
