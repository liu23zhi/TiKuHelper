const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
global.getComputedStyle = window.getComputedStyle
for (const key of ['DOMParser', 'HTMLVideoElement', 'HTMLAudioElement', 'HTMLInputElement', 'HTMLButtonElement']) global[key] = window[key]
window.scrollTo = () => {}
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue'), Antd = require('ant-design-vue')
const Swal = require('sweetalert2')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const admin = { id: 91, username: 'fixture-admin', role: 'admin', balance: 1.234567 }
const normal = { id: 92, username: 'fixture-user', role: 'user', balance: 1.234567 }
const row = (id = 17) => ({ id, name: 'fixture_token', tokenValue: 'SYNTHETIC_ONLY_TOKEN_123456', totalQuotaLimit: 0.000062, status: 'enabled', totalAmountUsed: 1.234567, remark: 'fixture' })
const ok = data => ({ status: 200, data })
let deferredReads = false, mutationMode = 'hold'
const wrappers = [], reads = [], mutations = [], calls = [], dialogs = []
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = {
 getAccessTokens: payload => { calls.push({ name: 'get', payload }); return deferredReads ? new Promise((resolve, reject) => reads.push({ resolve, reject, payload })) : Promise.resolve(ok([row()])) },
 ...Object.fromEntries(['create', 'update', 'delete'].map(name => [name + 'AccessToken', payload => {
   calls.push({ name, payload: JSON.parse(JSON.stringify(payload)) })
   return mutationMode === 'hold' ? new Promise((resolve, reject) => mutations.push({ resolve, reject, name })) : Promise.resolve(ok({ token: 'SYNTHETIC_CREATED_TOKEN' }))
 }])),
}
require.cache[apiPath] = api
const PAGE = require(path.join(web, 'src/pages/AccessTokenManagement/index.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function page(account = admin, realDialog = false) {
 user.setStoredCurrentUser(account)
 const w = mount(PAGE, { attachTo: document.body, global: { plugins: [Antd] } }); wrappers.push(w)
 if (!realDialog) w.vm.showCreatedTokenDialog = token => { dialogs.push(token); return Promise.resolve() }
 await settle(); return w
}
function edit(w, id = 17) { w.vm.openEditModal(row(id)) }
function create(w, name = 'fixture_created') { w.vm.openCreateModal(); w.vm.formState.name = name; w.vm.formState.totalQuotaLimit = 0.000062 }
function notices() { return [...document.querySelectorAll('.ant-message-notice')].map(x => x.textContent).join('|') }
afterEach(async () => {
 for (const w of wrappers) if (w.exists()) w.unmount()
 wrappers.length = 0; Swal.close(); await settle(); Antd.message.destroy(); Antd.Modal.destroyAll(); document.body.replaceChildren(); user.clearStoredCurrentUser()
 console.log('TOKEN_CASE_LEDGER ' + JSON.stringify({ calls, dialogs }))
 reads.length = mutations.length = calls.length = dialogs.length = 0; deferredReads = false; mutationMode = 'hold'
})
test('two mounted token pages own independent rows, drafts, busy flags and visibility', async () => {
 const a = await page(); edit(a); a.vm.formState.remark = 'OWNER_A_DRAFT'; a.vm.toggleTokenVisibility(17)
 const b = await page()
 const observed = { independentRows: a.vm.dataSource !== b.vm.dataSource, independentDraft: a.vm.formState !== b.vm.formState, aOpen: a.vm.visible, bOpen: b.vm.visible, bRevealed: b.vm.isTokenVisible(17) }
 console.log('TOKEN_OWNER_OBSERVED ' + JSON.stringify(observed))
 assert.deepEqual(observed, { independentRows: true, independentDraft: true, aOpen: true, bOpen: false, bRevealed: false })
 b.vm.openCreateModal(); b.vm.formState.name = 'OWNER_B'; assert.equal(a.vm.formState.remark, 'OWNER_A_DRAFT')
})
test('scope switch clears exposed rows immediately and refuses a late all-token GET and finally', async () => {
 const w = await page(); w.vm.toggleTokenVisibility(17); deferredReads = true
 w.vm.toggleAllTokens(); const all = reads.shift(); assert.deepEqual(all.payload, { scope: 'all' })
 console.log('TOKEN_SCOPE_CLEAR_OBSERVED ' + JSON.stringify({ rows: w.vm.dataSource.length, revealed: w.vm.isTokenVisible(17) }))
 assert.equal(w.vm.dataSource.length, 0); assert.equal(w.vm.isTokenVisible(17), false)
 w.vm.toggleAllTokens(); const mine = reads.shift(); all.resolve(ok([{ ...row(901), username: 'OTHER_OWNER_PRIVATE' }]))
 await settle(); assert.equal(w.vm.tableLoading, true, 'old scope finally cannot release current read')
 assert.equal(w.vm.dataSource.length, 0, 'old all-token rows cannot publish into mine')
 mine.resolve(ok([row(902)])); await settle(); assert.equal(w.vm.dataSource[0].id, 902); assert.equal(w.vm.tableLoading, false); assert.equal(w.vm.isTokenVisible(17), false)
})
for (const outcome of ['success', 'failure']) test('old create ' + outcome + ' cannot close a retargeted draft or release a newer submit', async () => {
 const w = await page(); create(w); w.vm.handleSubmit(); assert.equal(mutations.length, 1)
 w.vm.visible = false; await settle(); create(w, 'fixture_new_editor'); w.vm.formState.remark = 'NEW_DRAFT'; w.vm.handleSubmit(); assert.equal(mutations.length, 2)
 if (outcome === 'success') mutations[0].resolve(ok({ token: 'SYNTHETIC_OLD_SECRET' })); else mutations[0].reject(new Error('SYNTHETIC_OLD_FAILURE'))
 await settle()
 const observed = { open: w.vm.visible, name: w.vm.formState.name, remark: w.vm.formState.remark, busy: w.vm.submitLoading, dialogs: dialogs.length, refreshes: calls.filter(x => x.name === 'get').length, notices: notices() }
 console.log('TOKEN_EDITOR_OBSERVED ' + JSON.stringify(observed))
 assert.deepEqual(observed, { open: true, name: 'fixture_new_editor', remark: 'NEW_DRAFT', busy: true, dialogs: 0, refreshes: 1, notices: '' })
 mutations[1].resolve(ok({ token: 'SYNTHETIC_CURRENT_SECRET' })); await settle(); assert.equal(w.vm.submitLoading, false); assert.equal(w.vm.visible, false); assert.deepEqual(dialogs, ['SYNTHETIC_CURRENT_SECRET'])
})
for (const operation of ['create', 'update', 'delete']) for (const outcome of ['success', 'failure']) test(operation + ' ' + outcome + ' after account change or disposal cannot publish or trigger follow-up IO', async () => {
 const w = await page(); if (operation === 'create') create(w); else edit(w)
 if (operation === 'delete') w.vm.handleDelete(17); else w.vm.handleSubmit()
 assert.equal(mutations.length, 1)
 if (outcome === 'success') { user.setStoredCurrentUser(normal); await settle(); create(w, 'NEW_OWNER_DRAFT') } else w.unmount()
 const before = JSON.stringify({ form: w.vm.formState, open: w.vm.visible, rows: w.vm.dataSource, busy: w.vm.submitLoading }), count = calls.length
 if (outcome === 'success') mutations[0].resolve(ok({ token: 'SYNTHETIC_RETIRED_SECRET' })); else mutations[0].reject(new Error('SYNTHETIC_DISPOSED_FAILURE'))
 await settle(); assert.equal(JSON.stringify({ form: w.vm.formState, open: w.vm.visible, rows: w.vm.dataSource, busy: w.vm.submitLoading }), before); assert.equal(calls.length, count); assert.equal(dialogs.length, 0); assert.equal(notices(), '')
})
test('role downgrade clears all-token view and rejects old administrator response without ordinary-user scope escalation', async () => {
 const w = await page(); deferredReads = true; w.vm.toggleAllTokens(); const old = reads.shift(); user.setStoredCurrentUser({ ...admin, role: 'user' }); await settle()
 assert.equal(w.vm.isAdmin, false); assert.equal(w.vm.showAllTokens, false); assert.equal(reads.length, 1)
 old.resolve(ok([{ ...row(903), username: 'OLD_ADMIN_ONLY' }])); await settle(); assert.equal(w.vm.dataSource.length, 0); assert.equal(w.vm.tableLoading, true)
 reads.shift().resolve(ok([row(904)])); await settle(); const count = calls.length; w.vm.toggleAllTokens(); await settle(); assert.equal(calls.length, count); assert.equal(w.vm.showAllTokens, false); assert.equal(w.vm.dataSource[0].id, 904)
})
test('live create/update keep six-digit quota, whitelist, legacy name and confirmed delete behavior', async () => {
 const w = await page(normal); mutationMode = 'immediate'; create(w); w.vm.handleSubmit(); await settle()
 assert.deepEqual(calls.find(x => x.name === 'create').payload, { name: 'fixture_created', totalQuotaLimit: 0.000062, remark: '' })
 edit(w); w.vm.formState.name = '历史 名称'; w.vm.formState.originalName = '历史 名称'; w.vm.formState.totalAmountUsed = 999; w.vm.handleSubmit(); await settle()
 const payload = calls.find(x => x.name === 'update').payload; assert.equal(payload.name, '历史 名称'); assert.equal(payload.totalQuotaLimit, 0.000062); assert.ok(!Object.hasOwn(payload, 'totalAmountUsed') && !Object.hasOwn(payload, 'tokenValue'))
 const pop = w.findAllComponents(Antd.Popconfirm)[0]; assert.ok(pop); const before = calls.filter(x => x.name === 'delete').length; assert.equal(before, 0); pop.vm.$emit('confirm'); await settle(); assert.equal(calls.filter(x => x.name === 'delete').length, 1)
 console.log('TOKEN_LIVE_OBSERVED ' + JSON.stringify({ quota: payload.totalQuotaLimit, ordinaryScopeAll: w.vm.showAllTokens, deleteAfterConfirm: true }))
})

test('successful same-editor save preserves input typed while the request was pending', async () => {
 const w = await page(); edit(w); w.vm.formState.remark = 'SUBMITTED'; w.vm.handleSubmit(); assert.equal(mutations.length, 1)
 w.vm.formState.remark = 'TYPED_AFTER_SUBMIT'; mutations[0].resolve(ok({})); await settle()
 assert.equal(w.vm.visible, true); assert.equal(w.vm.formState.remark, 'TYPED_AFTER_SUBMIT'); assert.equal(w.vm.submitLoading, false)
 console.log('TOKEN_NEWER_DRAFT_OBSERVED ' + JSON.stringify({ open: w.vm.visible, remark: w.vm.formState.remark, busy: w.vm.submitLoading }))
})
test('native modal Cancel retires a pending create and its old afterClose cannot erase a reopened editor', async () => {
 const w = await page(); create(w); w.vm.handleSubmit(); await settle()
 const modal = [...document.querySelectorAll('.ant-modal')].find(x => x.querySelector('.ant-modal-title')?.textContent.includes('新增令牌'))
 assert.ok(modal); const cancel = [...modal.querySelectorAll('.ant-modal-footer button')].find(x => x.textContent.replace(/\s+/g, '') === '取消'); assert.ok(cancel && !cancel.disabled)
 cancel.click(); assert.equal(w.vm.visible, false); edit(w, 29); w.vm.formState.remark = 'REOPENED_EDITOR'
 w.vm.handleModalAfterClose(); mutations[0].resolve(ok({ token: 'SYNTHETIC_CANCELLED_SECRET' })); await settle()
 assert.equal(w.vm.visible, true); assert.equal(w.vm.formState.id, 29); assert.equal(w.vm.formState.remark, 'REOPENED_EDITOR'); assert.equal(dialogs.length, 0)
})
test('same-view refresh keeps upstream response completion order instead of introducing a latest-wins policy', async () => {
 const w = await page(); deferredReads = true; w.vm.handleLoad(); w.vm.handleLoad(); const first = reads.shift(), second = reads.shift()
 second.resolve(ok([row(22)])); await settle(); assert.equal(w.vm.dataSource[0].id, 22)
 first.resolve(ok([row(21)])); await settle(); assert.equal(w.vm.dataSource[0].id, 21)
})

for (const retirement of ['account', 'role', 'unmount']) test('real Swal token popup synchronously removes owned plaintext on ' + retirement + ' retirement', async () => {
 const w = await page(admin, true); mutationMode = 'immediate'; create(w); w.vm.handleSubmit(); await settle()
 const old = Swal.getPopup(); assert.ok(old); const field = old.querySelector('[data-token-value]'); assert.equal(field.textContent, 'SYNTHETIC_CREATED_TOKEN')
 if (retirement === 'account') user.setStoredCurrentUser(normal)
 else if (retirement === 'role') user.setStoredCurrentUser({ ...admin, role: 'user' })
 else w.unmount()
 const observed = { plaintext: field.textContent, oldStillActive: Swal.getPopup() === old, oldSecretInDOM: document.body.textContent.includes('SYNTHETIC_CREATED_TOKEN') }
 console.log('TOKEN_SWAL_RETIRE_OBSERVED ' + JSON.stringify({ retirement, ...observed }))
 assert.equal(observed.plaintext, '', 'clear the old secret before any exit animation')
 assert.equal(observed.oldSecretInDOM, false)
 await settle(); assert.notEqual(Swal.getPopup(), old)
})
test('same-principal balance refresh retains real owned token dialog and never restores focus to old owner', async () => {
 const w = await page(admin, true); const oldFocus = document.createElement('input'); document.body.appendChild(oldFocus); oldFocus.focus(); w.vm.showCreatedTokenDialog('SYNTHETIC_BALANCE_TOKEN'); await settle(); const old = Swal.getPopup()
 user.setStoredCurrentUser({ ...admin, balance: 0 }); await settle(); assert.equal(Swal.getPopup(), old); assert.equal(old.querySelector('[data-token-value]').textContent, 'SYNTHETIC_BALANCE_TOKEN')
 const input = document.createElement('input'); document.body.appendChild(input); input.focus(); user.setStoredCurrentUser(normal); await settle(); await new Promise(resolve => setTimeout(resolve, 150)); assert.notEqual(Swal.getPopup(), old); assert.notEqual(document.activeElement, oldFocus, 'retired owner cannot receive restored focus')
})
test('retired token owner does not close a newer unrelated popup or rewrite it from a pending clipboard callback', async () => {
 const w = await page(admin, true); let resolveCopy; w.vm.copyTokenText = () => new Promise(resolve => { resolveCopy = resolve })
 w.vm.showCreatedTokenDialog('SYNTHETIC_OLD_COPY_TOKEN'); await settle(); const old = Swal.getPopup(); old.querySelector('.swal2-deny').click(); await settle(); assert.equal(typeof resolveCopy, 'function')
 Swal.fire({ title: 'NEW_UNRELATED_POPUP', html: '<span data-new-popup>NEW_CONTENT</span>', showDenyButton: true, denyButtonText: 'NEW_COPY', returnFocus: false, heightAuto: false }); await settle(); const newer = Swal.getPopup(); assert.notEqual(newer, old)
 user.setStoredCurrentUser(normal); assert.equal(Swal.getPopup(), newer, 'old owner may close only its popup'); assert.ok(document.body.textContent.includes('NEW_CONTENT'))
 resolveCopy(true); await settle(); assert.equal(Swal.getPopup(), newer); assert.equal(newer.querySelector('.swal2-deny').textContent, 'NEW_COPY'); assert.ok(!newer.textContent.includes('已复制到剪贴板')); assert.ok(!newer.textContent.includes('SYNTHETIC_OLD_COPY_TOKEN'))
 console.log('TOKEN_SWAL_REPLACEMENT_OBSERVED ' + JSON.stringify({ replacementRetained: Swal.getPopup() === newer, denyText: newer.querySelector('.swal2-deny').textContent, lateValidationAbsent: !newer.textContent.includes('已复制到剪贴板') }))
})

test('real pending clipboard rejection cannot fall back to copying retired plaintext or annotate a replacement popup', async () => {
 const w = await page(admin, true); let rejectCopy, copiedValue; let fallbackCalls = 0
 const oldClipboard = navigator.clipboard, oldExec = document.execCommand
 Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: value => { copiedValue = value; return new Promise((resolve, reject) => { rejectCopy = reject }) } } })
 document.execCommand = () => { fallbackCalls++; return true }
 try {
  w.vm.showCreatedTokenDialog('SYNTHETIC_PENDING_COPY_TOKEN'); await settle(); const old = Swal.getPopup(); old.querySelector('.swal2-deny').click(); await settle(); assert.equal(copiedValue, 'SYNTHETIC_PENDING_COPY_TOKEN')
  user.setStoredCurrentUser(normal); assert.equal(old.querySelector('[data-token-value]').textContent, '')
  Swal.fire({ title: 'CURRENT_COPY_POPUP', text: 'CURRENT_SAFE_TEXT', showDenyButton: true, denyButtonText: 'CURRENT_COPY', returnFocus: false, heightAuto: false }); await settle(); const newer = Swal.getPopup()
  rejectCopy(new Error('SYNTHETIC_CLIPBOARD_RETIRED')); await settle(); assert.equal(fallbackCalls, 0); assert.equal(Swal.getPopup(), newer); assert.equal(newer.querySelector('.swal2-deny').textContent, 'CURRENT_COPY'); assert.ok(!newer.textContent.includes('复制失败'))
  console.log('TOKEN_SWAL_CLIPBOARD_OBSERVED ' + JSON.stringify({ fallbackCalls, replacementRetained: Swal.getPopup() === newer, denyText: newer.querySelector('.swal2-deny').textContent }))
 } finally { Object.defineProperty(navigator, 'clipboard', { configurable: true, value: oldClipboard }); document.execCommand = oldExec }
})
