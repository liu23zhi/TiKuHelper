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
// Explicit longhands exercise the real SweetAlert hasCssAnimation/animationend branch.
// jsdom does not advance CSS frames; normal animation completion is dispatched by each test.
const css = document.createElement('style'); css.textContent = '.swal2-popup.swal2-hide { animation-duration: 0.15s; transition-duration: 0s; }'; document.head.appendChild(css)
let wrapper, trigger, safe, popup, container, mountHost
const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function open() {
 user.setStoredCurrentUser(first); document.body.style.paddingRight = '7px'
 safe = document.createElement('input'); safe.setAttribute('data-safe-focus', ''); document.body.appendChild(safe)
 wrapper = mount(Usage, { attachTo: document.body, global: { plugins: [Antd] } }); await settle()
 mountHost = wrapper.element.parentElement
 trigger = document.createElement('button'); trigger.textContent = 'ORIGINAL_FEE_TRIGGER'; wrapper.element.appendChild(trigger); trigger.focus()
 wrapper.vm.handleShowFeeDetail(wrapper.vm.dataSource[0]); await settle(); await pause(25)
 popup = Swal.getPopup(); container = Swal.getContainer(); assert.ok(popup?.textContent.includes(marker)); assert.equal(document.body.classList.contains('swal2-shown'), true); assert.equal(mountHost.getAttribute('aria-hidden'), 'true')
}
function endAnimation(node = Swal.getPopup()) { node?.dispatchEvent(new window.Event('animationend')) }
function cleanNow() {
 assert.equal(popup.isConnected, false, 'owned private popup must detach in the invalidation call, not after animationend')
 assert.equal(container.isConnected, false, 'backdrop/container must detach synchronously')
 assert.equal(document.querySelector('.swal2-container'), null)
 assert.equal(document.body.classList.contains('swal2-shown'), false); assert.equal(document.documentElement.classList.contains('swal2-shown'), false)
 assert.equal(document.body.style.paddingRight, '7px')
 const aria = { mountHostConnected: mountHost.isConnected, mountHostAria: mountHost.getAttribute('aria-hidden'), safeConnected: safe.isConnected, safeAria: safe.getAttribute('aria-hidden') }
 console.log('FORCED_CLOSE_LIVE_ARIA ' + JSON.stringify(aria))
 if (mountHost.isConnected) assert.equal(mountHost.getAttribute('aria-hidden'), null)
 assert.equal(safe.getAttribute('aria-hidden'), null, 'still-connected background content must be exposed again')
 assert.equal(document.querySelector('[data-previous-aria-hidden]'), null, 'no live SweetAlert ARIA restoration marker remains')
 assert.equal(popup.contains(document.activeElement), false, 'no focus remains in the removed popup')
}
afterEach(async () => { wrapper?.unmount(); wrapper = undefined; Swal.close(); endAnimation(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); await pause(140); document.body.innerHTML = ''; document.body.style.paddingRight = ''; window.localStorage.clear() })
test('identity invalidation immediately detaches animated owned popup and releases body/ARIA/focus without late returnFocus theft', async () => {
 await open(); let staleFocus = 0; let safeBlur = 0; trigger.addEventListener('focus', () => { staleFocus++ }); safe.addEventListener('blur', () => { safeBlur++ })
 user.setStoredCurrentUser(second); cleanNow(); safe.focus(); await settle(); await pause(140)
 assert.equal(document.activeElement, safe); assert.equal(staleFocus, 0); assert.equal(safeBlur, 0, 'invalidated returnFocus must not even blur the new focus owner'); assert.equal(Swal.getPopup(), null); assert.equal(trigger.disabled, false); assert.equal(trigger.getAttribute('disabled'), null); trigger.focus(); assert.equal(document.activeElement, trigger, 'owned trigger must be restored after library lifecycle cleanup')
})
test('component unmount immediately detaches animated popup and releases its backdrop/body/ARIA/focus', async () => {
 await open(); wrapper.unmount(); wrapper = undefined; cleanNow(); safe.focus(); await pause(140); assert.equal(document.activeElement, safe)
})
test('normal owned close still waits for its real hide animation and retains ordinary focus return', async () => {
 await open(); Swal.close(); assert.equal(popup.isConnected, true); assert.equal(popup.classList.contains('swal2-hide'), true); assert.ok(parseFloat(window.getComputedStyle(popup).getPropertyValue('animation-duration')) > 0)
 endAnimation(popup); cleanNow(); await pause(140); assert.equal(document.activeElement, trigger)
})
test('a newer popup already owned by another component is neither closed nor restyled during identity change/unmount', async () => {
 await open(); Swal.fire({ title: 'OTHER_CURRENT_POPUP', text: 'fixture', heightAuto: false, scrollbarPadding: false }); await settle(); await pause(25)
 const other = Swal.getPopup(); user.setStoredCurrentUser(second); wrapper.unmount(); wrapper = undefined; await settle(); await pause(140)
 assert.equal(Swal.getPopup(), other); assert.equal(other.style.getPropertyValue('animation-duration'), ''); assert.equal(document.body.classList.contains('swal2-shown'), true)
 Swal.close(); assert.equal(other.isConnected, true); endAnimation(other); await pause(140); assert.equal(document.querySelector('.swal2-container'), null)
})
test('a new popup opened immediately after forced cleanup keeps its body lock and focus after old lifecycle callbacks', async () => {
 await open(); user.setStoredCurrentUser(second); cleanNow(); Swal.fire({ title: 'OTHER_NEXT_POPUP', text: 'fixture', heightAuto: false, scrollbarPadding: false }); await settle(); await pause(140)
 const other = Swal.getPopup(); assert.ok(other?.textContent.includes('OTHER_NEXT_POPUP')); assert.equal(document.body.classList.contains('swal2-shown'), true); assert.equal(other.contains(document.activeElement), true)
 Swal.close(); assert.equal(other.isConnected, true); endAnimation(other)
})
test('identity invalidation while normal hide is already pending completes its existing cleanup and suppresses captured old returnFocus', async () => {
 await open(); let staleFocus = 0; let safeBlur = 0; trigger.addEventListener('focus', () => { staleFocus++ }); safe.addEventListener('blur', () => { safeBlur++ })
 Swal.close(); assert.equal(popup.isConnected, true); assert.equal(popup.classList.contains('swal2-hide'), true)
 user.setStoredCurrentUser(second); cleanNow(); safe.focus(); await settle(); await pause(140)
 assert.equal(document.activeElement, safe); assert.equal(staleFocus, 0); assert.equal(safeBlur, 0, 'already-hide captured returnFocus must not blur the new focus owner'); assert.equal(trigger.disabled, false); assert.equal(trigger.getAttribute('disabled'), null); trigger.focus(); assert.equal(document.activeElement, trigger); assert.equal(document.querySelector('.swal2-container'), null)
})
test('after forced lifecycle cleanup a later normal popup returns focus to its own trigger, not the invalidated old trigger', async () => {
 await open(); user.setStoredCurrentUser(second); cleanNow(); await pause(140)
 const nextTrigger = document.createElement('button'); nextTrigger.textContent = 'NEW_POPUP_TRIGGER'; document.body.appendChild(nextTrigger); nextTrigger.focus()
 Swal.fire({ title: 'LATER_NORMAL_POPUP', heightAuto: false, scrollbarPadding: false }); await settle(); await pause(25); const other = Swal.getPopup(); Swal.close(); assert.equal(other.isConnected, true); endAnimation(other); await pause(140)
 assert.equal(document.activeElement, nextTrigger); assert.equal(document.querySelector('.swal2-container'), null)
})

test('forced focus protection restores the original disabled property and exact attribute value', async () => {
 await open(); trigger.setAttribute('disabled', 'legacy-disabled-value'); assert.equal(trigger.disabled, true)
 user.setStoredCurrentUser(second); cleanNow(); await pause(140)
 assert.equal(trigger.disabled, true); assert.equal(trigger.getAttribute('disabled'), 'legacy-disabled-value')
})
