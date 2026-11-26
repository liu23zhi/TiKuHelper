const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises, DOMWrapper, config } = require('@vue/test-utils')
const { h, nextTick } = require('vue')
const { RouterView } = require('vue-router')
const { message } = require('ant-design-vue')
const axios = require('axios')
const { registerConsoleUI } = require('../../src/registerUI.js')
const user = require('../../src/utils/currentUser.js')

const route = '/agreement-admin'
const base = '/api/admin/agreements'
const admin = { id: 18, role: 'admin', status: 'enabled' }
const member = { id: 27, role: 'user', status: 'enabled' }
const doc = { id: 11, revision: 4, version: 'demo-2', summary: '合成摘要', body: '<strong data-fixture="markup">合成正文</strong>\n第二行' }
const release = { id: 281, draftId: 9, revision: 2, version: 'demo-1', summary: '合成历史摘要', body: '合成历史正文', publishedAt: '2026-11-18T21:20:01Z' }
const listing = row => ({ ...row, body: '' })
const preview = draft => ({ draft, type: 'user_agreement', audience: 'all_devices' })
const ok = data => ({ status: 200, data })
const err = (status, code) => ({ status, data: { code } })
const forbidden = { status: 403, data: { message: '无权限执行该操作' } }
const info = '/api/user/info'
const consent = { deviceId: 'fixture-private-device', userId: 38, version: 'demo-1', clientKind: 'exam_client',
  accepted: true, acceptedAt: '2026-11-27T02:03:04Z', ip: '192.0.2.42' }
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r }); return { promise, resolve } }
const mounted = []
let calls = [], respond = null
axios.defaults.adapter = async config => {
  const c = { path: config.url, method: config.method, params: config.params, data: config.data ? JSON.parse(config.data) : null }
  const r = c.path === '/api/user/review-rebate/availability' ? ok({ enabled: false }) :
    c.path === '/api/user/session' ? ok({ status: 'enabled' }) : await (async () => {
    calls.push(c)
    const response = await respond?.(c)
    if (response) return response
    if (c.method !== 'get') throw new Error('unhandled mutation')
    if (c.path === base + '/drafts') return ok([listing(doc)])
    if (c.path === base + '/drafts/11') return ok(preview(doc))
    if (c.path === base + '/releases') return ok([listing(release)])
    if (c.path === base + '/releases/281') return ok(release)
    if (c.path === base + '/records') return ok([])
    throw new Error('unhandled route')
  })()
  return { ...r, headers: {}, config, request: { memoryOnly: true } }
}
// Install the production registrations rather than the helper's default
// shared-widget registration. The HTTP adapter must precede request creation.
for (const name of ['ConsolePageHeading', 'ConsolePanel', 'ConsoleSummaryGrid', 'ConsoleTableShell']) {
  delete config.global.components[name]
}
const router = require('../../src/router/router.js').default
const Sider = require('../../src/components/SiderMenu/index.vue')
const settle = async () => { await nextTick(); await flushPromises(); await nextTick() }
const action = (w, name) => w.get('[data-action="' + name + '"]')
const modal = () => {
  const dialog = document.querySelector('[role="dialog"]')
  assert.ok(dialog, 'the real Ant Design dialog was mounted')
  return new DOMWrapper(dialog)
}
async function open(actor = admin) {
  user.setStoredCurrentUser(actor)
  const entry = router.resolve(route).matched.at(-1)?.components?.default
  assert.ok(entry, 'the real router should expose the agreement administration component')
  const { default: Page } = await entry()
  const w = mount(Page, { attachTo: document.body, global: { plugins: [{ install: registerConsoleUI }] } })
  mounted.push(w); await settle(); return w
}
const mutateCalls = () => calls.filter(({ method }) => method !== 'get')
const click = async (w, name) => { await action(w, name).trigger('click'); await settle() }
const fill = (w, id, value) => w.get('#agreement-' + id).setValue(value)
async function submit(w, name) { await w.get('form[aria-label="' + name + '"]').trigger('submit'); await settle() }
afterEach(async () => {
  for (const w of mounted.splice(0)) w.unmount()
  user.clearStoredCurrentUser(); message.destroy(); document.body.replaceChildren()
  calls = []; respond = null
  await settle()
})

test('restricted navigation hides the page from members and direct route is guarded before any API read', async () => {
  const w = await open(member)
  assert.match(w.text(), /仅.*管理员/)
  assert.equal(w.find('table').exists(), false)
  const menu = mount(Sider, { attachTo: document.body, global: { plugins: [router, { install: registerConsoleUI }] }, props: { currentUser: member } })
  mounted.push(menu)
  assert.equal(menu.find('a[href="' + route + '"]').exists(), false)
  await router.push(route)
  assert.equal(router.currentRoute.value.path, user.resolveHomePath(member))
  assert.deepEqual(calls, [])
  user.setStoredCurrentUser(admin)
  await menu.setProps({ currentUser: admin })
  assert.equal(menu.get('a[href="' + route + '"]').text(), '用户协议管理')
  assert.equal(router.resolve(route).name, 'agreement-admin')
})

test('admin browses multiple draft summaries and previews the server body as inert text', async () => {
  respond = c => c.path === base + '/drafts' && ok([listing(doc), listing({ ...doc, id: 12, version: 'demo-3' })])
  const w = await open()
  assert.match(w.get('[aria-label="协议草稿列表"]').text(), /demo-2.*demo-3/)
  assert.equal(calls.some(c => c.path === base + '/drafts/11'), false)
  await w.findAll('[data-action="preview"]')[0].trigger('click'); await settle()
  assert.match(modal().text(), /<strong data-fixture="markup">合成正文<\/strong>/)
  assert.equal(modal().find('strong[data-fixture="markup"]').exists(), false)
  assert.match(modal().text(), /第二行/)
  assert.deepEqual(mutateCalls(), [])
})

test('creating a draft posts only the exact editable fields and refreshes list only after receipt', async () => {
  const input = { version: 'demo-new', summary: '新摘要', body: '新正文', revision: 0 }
  const saved = { ...input, id: 12, revision: 1 }
  let created = false
  respond = c => {
    if (c.path === base + '/drafts' && c.method === 'post') { created = true; return ok(saved) }
    if (c.path === base + '/drafts' && created) return ok([listing(saved), listing(doc)])
    return null
  }
  const w = await open(); await click(w, 'create')
  const dialog = modal()
  assert.ok(action(dialog, 'save').element.disabled)
  await fill(dialog, 'version', input.version); await fill(dialog, 'summary', input.summary); await fill(dialog, 'body', input.body)
  await submit(dialog, '协议草稿编辑')
  assert.deepEqual(mutateCalls().map(({ path, method, data }) => ({ path, method, data })), [{ path: base + '/drafts', method: 'post', data: input }])
  assert.match(w.text(), /草稿已保存/)
  assert.match(w.get('[aria-label="协议草稿列表"]').text(), /demo-new/)
})

test('edit sends the loaded revision; conflict preserves input and blocks replay until an explicit reread', async () => {
  let version = 4
  respond = c => {
    if (c.path === base + '/drafts/11' && c.method === 'put') return c.data.revision === 4 ? err(409, 'version_conflict') : ok({ ...doc, ...c.data, revision: 6 })
    if (c.path === base + '/drafts/11') return ok(preview({ ...doc, revision: version, body: version === 4 ? doc.body : '他人改过的正文' }))
    return null
  }
  const w = await open(); await click(w, 'edit')
  await fill(modal(), 'body', '我的新正文')
  await submit(modal(), '协议草稿编辑')
  assert.match(modal().text(), /版本冲突/)
  assert.equal(modal().get('#agreement-body').element.value, '我的新正文')
  assert.ok(action(modal(), 'save').element.disabled)
  await submit(modal(), '协议草稿编辑')
  assert.equal(mutateCalls().length, 1)
  assert.deepEqual(mutateCalls()[0].data, { version: doc.version, summary: doc.summary, body: '我的新正文', revision: 4 })
  version = 5
  await click(modal(), 'reload-detail')
  assert.equal(modal().get('#agreement-body').element.value, '他人改过的正文')
  await fill(modal(), 'body', '审阅后的新正文')
  await submit(modal(), '协议草稿编辑')
  assert.equal(mutateCalls()[1].data.revision, 5)
  assert.match(w.text(), /草稿已保存/)
})

test('publish visibly presents the server preview and needs both review and confirmation; one in-flight POST only', async () => {
  const request = deferred()
  const published = { ...doc, id: 282, draftId: doc.id, publishedAt: '2026-11-27T02:00:00Z' }
  let finished = false
  respond = c => {
    if (c.path === base + '/drafts/11/publish') return request.promise
    if (c.path === base + '/releases' && finished) return ok([listing(published), listing(release)])
    return null
  }
  const w = await open(); await click(w, 'publish')
  assert.match(modal().text(), /demo-2.*合成摘要.*全部设备/)
  assert.match(modal().text(), /第二行/)
  assert.ok(action(modal(), 'confirm-publish').element.disabled)
  assert.deepEqual(mutateCalls(), [])
  await click(modal(), 'cancel')
  assert.deepEqual(mutateCalls(), [])
  await click(w, 'publish'); await modal().get('#agreement-reviewed').setValue(true)
  const confirm = action(modal(), 'confirm-publish')
  await confirm.trigger('click'); await confirm.trigger('click'); await settle()
  assert.ok(confirm.element.disabled)
  assert.equal(mutateCalls().length, 1)
  assert.deepEqual(mutateCalls()[0], { path: base + '/drafts/11/publish', method: 'post', params: undefined, data: { revision: 4 } })
  finished = true; request.resolve(ok(published)); await settle()
  assert.match(w.text(), /协议 demo-2 已发布/)
  assert.match(w.get('[aria-label="当前发布版本"]').text(), /demo-2/)
  assert.equal(w.find('[data-action="publish"]').exists(), false)
  assert.equal(w.find('[data-action="edit"]').exists(), false)
})

test('HTTP 403 on publish closes the page, retires pending reads, and restores only fresh data after a verified retry', async () => {
  const pendingRelease = deferred(), pendingInfo = deferred()
  let historyReads = 0, profileReads = 0, recordReads = 0
  respond = c => {
    if (c.path === base + '/drafts/11/publish') return forbidden
    if (c.path === base + '/releases' && ++historyReads === 2) return pendingRelease.promise
    if (c.path === base + '/records') return ok([{ ...consent, deviceId: ++recordReads === 1 ? consent.deviceId : 'fresh-fixture-device' }])
    if (c.path === info) {
      profileReads++
      return profileReads === 1 ? pendingInfo.promise : profileReads === 2 ? ok({ ...admin, id: 99 }) : ok(admin)
    }
    return null
  }
  const w = await open()
  assert.match(w.text(), /合成摘要.*demo-1.*fixture-private-device.*192\.0\.2\.42/)
  await click(w, 'refresh-releases')
  await click(w, 'publish')
  assert.match(modal().text(), /合成正文/)
  await modal().get('#agreement-reviewed').setValue(true)
  const confirm = action(modal(), 'confirm-publish')
  await confirm.trigger('click'); await settle()
  assert.equal(user.getStoredCurrentUser().role, 'admin', 'no local demotion before the server verifies identity')
  assert.equal(profileReads, 1)
  assert.ok(action(w, 'recheck-access').element.disabled)
  assert.equal(document.querySelector('[role="dialog"]'), null)
  assert.equal(w.find('table').exists(), false)
  assert.doesNotMatch(w.text(), /合成摘要|demo-1|fixture-private-device|192\.0\.2\.42|已发布。/)
  pendingRelease.resolve(ok([listing({ ...release, version: 'late-private-release' })])); await settle()
  pendingInfo.resolve(err(404, 'fixture_profile_unavailable')); await settle()
  assert.match(w.text(), /未能确认/)
  assert.doesNotMatch(w.text(), /late-private-release/)
  assert.equal(w.find('table').exists(), false)
  assert.equal(w.find('[data-action="create"]').exists(), false)
  await click(w, 'recheck-access')
  assert.equal(profileReads, 2)
  assert.equal(user.getStoredCurrentUser().id, admin.id, 'a profile for a different owner cannot reopen the page')
  assert.equal(w.find('table').exists(), false)
  await click(w, 'recheck-access')
  assert.equal(profileReads, 3)
  assert.match(w.text(), /fresh-fixture-device/)
  assert.doesNotMatch(w.text(), /fixture-private-device|late-private-release|已发布。/)
  assert.equal(document.querySelector('[role="dialog"]'), null)
  assert.equal(mutateCalls().length, 1, 'recovery never replays the denied publication')
})

test('HTTP 403 loading a draft clears every list even when the profile still reports administrator', async () => {
  respond = c => {
    if (c.path === base + '/drafts/11') return forbidden
    if (c.path === base + '/records') return ok([consent])
    if (c.path === info) return ok(admin)
    return null
  }
  const w = await open()
  await click(w, 'preview')
  assert.equal(document.querySelector('[role="dialog"]'), null)
  assert.equal(w.find('table').exists(), false)
  assert.equal(calls.filter(c => c.path === info).length, 1, '403 starts only one automatic profile check')
  assert.equal(calls.filter(c => c.path === base + '/drafts').length, 1, 'admin profile alone cannot cause a background reopen loop')
  assert.match(w.text(), /拒绝.*重新检查/)
  assert.deepEqual(mutateCalls(), [])
})

test('a failed review cannot publish; a stale revision requires reread and a fresh acknowledgement', async () => {
  let reviewFails = true
  let revision = 4
  respond = c => {
    if (c.path === base + '/drafts/11') return reviewFails ? err(503, 'unavailable') : ok(preview({ ...doc, revision }))
    if (c.path === base + '/drafts/11/publish') return err(409, 'version_conflict')
    return null
  }
  const w = await open(); await click(w, 'publish')
  assert.match(modal().text(), /正文读取失败/)
  assert.ok(action(modal(), 'confirm-publish').element.disabled)
  assert.equal(modal().find('#agreement-reviewed').exists(), false)
  assert.deepEqual(mutateCalls(), [])
  reviewFails = false; await click(modal(), 'reload-detail')
  await modal().get('#agreement-reviewed').setValue(true); await click(modal(), 'confirm-publish')
  assert.match(modal().text(), /版本冲突/)
  assert.ok(action(modal(), 'confirm-publish').element.disabled)
  revision = 5; await click(modal(), 'reload-detail')
  assert.ok(action(modal(), 'confirm-publish').element.disabled)
  assert.match(modal().text(), /草稿修订5/)
  assert.equal(mutateCalls().length, 1)
})

test('a lost publication response locks the confirmation without re-POSTing or claiming success', async () => {
  respond = c => {
    if (c.path === base + '/drafts/11/publish') throw new Error('Network Error')
    return null
  }
  const w = await open(); await click(w, 'publish')
  await modal().get('#agreement-reviewed').setValue(true); await click(modal(), 'confirm-publish')
  assert.match(modal().text(), /结果未确认.*先刷新/)
  assert.ok(modal().get('#agreement-reviewed').element.disabled)
  assert.ok(action(modal(), 'confirm-publish').element.disabled)
  assert.equal(mutateCalls().length, 1)
  assert.doesNotMatch(w.text(), /已发布。/)
})

test('batch and individual deletion target drafts only and the history dialog is strictly read-only', async () => {
  let rows = [doc, { ...doc, id: 12, version: 'demo-3' }, { ...doc, id: 9, version: release.version }]
  respond = c => {
    if (c.path === base + '/drafts/batch-delete' && c.method === 'post') {
      rows = rows.filter(row => ![11, 12].includes(row.id))
      return ok([11, 12])
    }
    if (c.path === base + '/drafts/9' && c.method === 'delete') { rows = []; return ok({ deleted: 9 }) }
    if (c.path === base + '/drafts') return ok(rows.map(listing))
    return null
  }
  const w = await open()
  await w.get('[aria-label="选择草稿 demo-2"]').setValue(true)
  await w.get('[aria-label="选择草稿 demo-3"]').setValue(true)
  await click(w, 'batch-delete')
  assert.match(modal().text(), /2 个草稿.*已发布版本.*同意记录/)
  assert.deepEqual(mutateCalls(), [])
  await click(modal(), 'confirm-delete')
  assert.deepEqual(mutateCalls()[0], { path: base + '/drafts/batch-delete', method: 'post', params: undefined, data: { ids: [11, 12] } })
  await click(w, 'delete'); await click(modal(), 'confirm-delete')
  assert.equal(mutateCalls()[1].method, 'delete'); assert.equal(mutateCalls()[1].path, base + '/drafts/9')
  assert.match(w.text(), /暂无协议草稿/)
  await click(w, 'view-release')
  assert.match(modal().text(), /合成历史正文/)
  assert.equal(modal().get('time').attributes('datetime'), release.publishedAt)
  assert.equal(modal().find('input, textarea, [data-action="confirm-delete"], [data-action="confirm-publish"]').exists(), false)
  assert.match(w.get('[aria-label="当前发布版本"]').text(), /demo-1/)
  assert.equal(mutateCalls().length, 2)
})

test('consent records use exact synchronous filters, keep false, preserve historical timestamps and represent unbound and no-event separately', async () => {
  const acceptedAt = '2026-11-27T02:03:04Z'
  respond = c => c.path === base + '/records' && ok(c.params.accepted === 'false'
    ? [{ deviceId: 'fixture-2', userId: null, version: 'demo-1', clientKind: 'unknown', accepted: false, acceptedAt: null, ip: null }]
    : [{ deviceId: 'fixture-1', userId: 38, version: 'demo-1', clientKind: 'exam_client', accepted: true, acceptedAt, ip: '192.0.2.42' }])
  const w = await open(), table = () => w.get('[aria-label="协议同意记录列表"]')
  assert.match(table().text(), /fixture-1.*38.*考试模式.*demo-1.*已同意.*192\.0\.2\.42/)
  assert.equal(table().get('time').attributes('datetime'), acceptedAt)
  await fill(w, 'filter-device', 'fixture-2'); await fill(w, 'filter-unbound', true); await fill(w, 'filter-version', 'demo-1')
  await fill(w, 'filter-client', 'unknown'); await fill(w, 'filter-accepted', 'false')
  await submit(w, '同意记录筛选')
  assert.deepEqual(calls.filter(c => c.path === base + '/records').at(-1).params,
    { limit: 26, offset: 0, deviceId: 'fixture-2', userId: 'unbound', version: 'demo-1', clientKind: 'unknown', accepted: 'false' })
  assert.match(table().text(), /fixture-2.*未绑定.*尚未同意.*—.*—/)
  assert.equal(table().find('time').exists(), false)
  assert.ok(w.get('#agreement-filter-user').element.disabled)
  await fill(w, 'filter-unbound', false); await fill(w, 'filter-user', '38'); await fill(w, 'filter-accepted', 'true'); await submit(w, '同意记录筛选')
  assert.equal(calls.filter(c => c.path === base + '/records').at(-1).params.userId, '38')
  await click(w, 'clear-record-filters')
  assert.deepEqual(calls.filter(c => c.path === base + '/records').at(-1).params, { limit: 26, offset: 0 })
})

test('records distinguish empty/current-not-published/version-not-found from failure and do not leak late rows after demotion', async () => {
  let mode = 'not_published'
  const late = deferred()
  respond = c => {
    if (c.path === base + '/releases') return ok(mode === 'not_published' ? [] : [listing(release)])
    if (c.path !== base + '/records') return null
    if (mode === 'not_published' || mode === 'not_found') return err(404, 'not_found')
    if (mode === 'empty') return ok([])
    if (mode === 'failure') return err(503, 'unavailable')
    return late.promise
  }
  const w = await open()
  assert.match(w.text(), /尚未发布用户协议/)
  assert.match(w.text(), /当前没有发布版本，暂无同意记录/)
  mode = 'not_found'
  await fill(w, 'filter-version', 'not-found'); await submit(w, '同意记录筛选')
  assert.match(w.text(), /未找到指定的发布版本/)
  mode = 'empty'; await click(w, 'clear-record-filters')
  assert.match(w.text(), /没有符合条件的同意记录/)
  mode = 'failure'; await click(w, 'refresh-records')
  assert.match(w.text(), /同意记录读取失败/)
  assert.doesNotMatch(w.text(), /没有符合条件的同意记录/)
  mode = 'late'; await click(w, 'refresh-records')
  user.setStoredCurrentUser({ ...admin, role: 'user' }); await settle()
  late.resolve(ok([{ deviceId: 'late-private', accepted: false }])); await settle()
  assert.equal(w.find('table').exists(), false)
  assert.doesNotMatch(w.text(), /late-private/)
})

test('paged lists use bounded provider offsets; current version stays anchored to the first history page', async () => {
  const full = Array.from({ length: 26 }, (_, i) => listing({ ...release, id: 281 - i, version: i ? 'old-' + i : 'current-demo' }))
  respond = c => c.path === base + '/releases' && ok(c.params.offset ? [full[25]] : full)
  const w = await open()
  assert.match(w.get('[aria-label="当前发布版本"]').text(), /current-demo/)
  await click(w, 'next-releases')
  assert.match(w.get('[aria-label="发布版本历史"]').text(), /old-25/)
  assert.match(w.get('[aria-label="当前发布版本"]').text(), /current-demo/)
  assert.equal(w.find('[data-action="next-releases"]').exists(), false)
  assert.deepEqual(calls.filter(c => c.path === base + '/releases').map(c => c.params), [{ limit: 26, offset: 0 }, { limit: 26, offset: 25 }])
})

test('live administrator demotion removes the real RouterView and returns to the personal home', async () => {
  user.setStoredCurrentUser(admin)
  assert.equal(router.resolve(route).name, 'agreement-admin')
  await router.push(route)
  const shell = mount({ render: () => h(RouterView) }, { attachTo: document.body, global: { plugins: [router, { install: registerConsoleUI }] } })
  mounted.push(shell); await settle()
  assert.match(shell.text(), /合成摘要/)
  user.setStoredCurrentUser({ ...admin, role: 'user' }); await settle()
  assert.doesNotMatch(shell.text(), /合成摘要/)
  assert.equal(router.currentRoute.value.path, user.resolveHomePath(member))
})

test('server-confirmed demotion after a list 403 sends the live RouterView to personal home', async () => {
  const profile = deferred()
  let revokeOnRefresh = false
  respond = c => {
    if (c.path === base + '/drafts' && revokeOnRefresh) return forbidden
    if (c.path === base + '/records') return ok([consent])
    if (c.path === info) return profile.promise
    return null
  }
  user.setStoredCurrentUser(admin)
  await router.push(route)
  const shell = mount({ render: () => h(RouterView) }, { attachTo: document.body, global: { plugins: [router, { install: registerConsoleUI }] } })
  mounted.push(shell); await settle()
  assert.match(shell.text(), /fixture-private-device/)
  revokeOnRefresh = true
  await click(shell, 'refresh-drafts')
  assert.equal(user.getStoredCurrentUser().role, 'admin')
  assert.equal(router.currentRoute.value.path, route)
  assert.doesNotMatch(shell.text(), /fixture-private-device|合成历史摘要/)
  assert.equal(shell.find('[data-action="create"]').exists(), false)
  profile.resolve(ok({ ...admin, role: 'user' })); await settle()
  assert.equal(user.getStoredCurrentUser().role, 'user')
  assert.equal(router.currentRoute.value.path, user.resolveHomePath(member))
  assert.deepEqual(mutateCalls(), [])
})
