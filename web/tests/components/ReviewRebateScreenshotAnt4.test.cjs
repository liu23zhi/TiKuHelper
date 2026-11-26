const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises, config } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const axios = require('axios')
const { registerConsoleUI } = require('../../src/registerUI.js')
const { setStoredCurrentUser, getStoredCurrentUser, clearStoredCurrentUser } = require('../../src/utils/currentUser.js')

for (const name of ['ConsolePageHeading', 'ConsolePanel', 'ConsoleSummaryGrid', 'ConsoleTableShell']) delete config.global.components[name]

const originals = { adapter: axios.defaults.adapter, create: URL.createObjectURL, revoke: URL.revokeObjectURL, fetch: globalThis.fetch }
const base = '/api/admin/review-rebate'
const first = base + '/claims/42/screenshot'
const second = base + '/claims/43/screenshot'
const admin = { id: 101, role: 'admin', status: 'enabled' }
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==', 'base64')
const fixture = new Blob([png], { type: 'image/png' })
const calls = [], created = [], revoked = [], managementReads = []
let claims, screenshot, wrapper, router
URL.createObjectURL = blob => {
  created.push(blob)
  return 'blob:http://localhost/review-rebate-' + created.length
}
URL.revokeObjectURL = value => revoked.push(value)
axios.defaults.adapter = async config => {
  managementReads.push(config.url)
  if (config.url === base + '/claims') return { status: 200, config, data: { data: claims } }
  if ([base + '/programs', base + '/rules'].includes(config.url)) return { status: 200, config, data: { data: [] } }
  throw new Error('unexpected non-preview adapter dispatch')
}
globalThis.fetch = async (url, options) => {
  const target = new URL(url)
  assert.equal(target.origin, window.location.origin)
  assert.ok([first, second].includes(target.pathname))
  calls.push({ url: target.pathname, ...options })
  const result = await screenshot(options)
  return new Response(result.data, { status: result.status })
}
const Review = require('../../src/pages/ReviewRebateAdmin/index.vue')

async function open(rows, actor = admin) {
  claims = rows.map((screenshotUrl, index) => ({ id: 42 + index, userId: 102, source: 'edge', status: 'rejected', screenshotUrl }))
  screenshot = async () => ({ status: 200, data: fixture })
  setStoredCurrentUser(actor)
  router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/review-rebate-admin', component: Review }] })
  await router.push('/review-rebate-admin')
  wrapper = mount(Review, { attachTo: document.body, global: { plugins: [router, { install: registerConsoleUI }] } })
  await settle()
}

async function settle() {
  await nextTick()
  await flushPromises()
  await nextTick()
}

const previewButtons = () => wrapper.findAll('button').filter(button => button.text().replace(/\s/g, '') === '查看截图')
const dialog = () => document.querySelector('[role="dialog"]')
const image = () => dialog()?.querySelector('img[alt="提交截图"]')
async function show(index = 0) {
  const button = previewButtons()[index]
  assert.ok(button, 'available screenshot should have an in-page preview button, not a navigation link')
  await button.trigger('click')
  await settle()
}
async function close() {
  const button = dialog()?.querySelector('button[aria-label="Close"]')
  assert.ok(button, 'the preview is dismissible')
  button.click()
  await settle()
}

afterEach(async () => {
  wrapper?.unmount()
  wrapper = undefined
  router?.options.history.destroy()
  clearStoredCurrentUser()
  await settle()
  document.body.replaceChildren()
  calls.length = created.length = revoked.length = managementReads.length = 0
})
after(() => {
  axios.defaults.adapter = originals.adapter
  URL.createObjectURL = originals.create
  URL.revokeObjectURL = originals.revoke
  globalThis.fetch = originals.fetch
})

test('admin previews the canonical screenshot in-page with the existing authenticated request and revokes each blob', async () => {
  await open([first, second, ''])
  assert.equal(previewButtons().length, 2)
  assert.match(wrapper.text(), /无图/)
  assert.equal(wrapper.find('a[href="' + first + '"]').exists(), false)
  await show()
  assert.deepEqual(calls.map(({ url, credentials, redirect }) => ({ url, credentials, redirect })),
    [{ url: first, credentials: 'same-origin', redirect: 'error' }])
  assert.equal(created.length, 1)
  assert.deepEqual(Buffer.from(await created[0].arrayBuffer()), png)
  assert.equal(image()?.getAttribute('src'), 'blob:http://localhost/review-rebate-1')
  await show(1)
  assert.equal(image()?.getAttribute('src'), 'blob:http://localhost/review-rebate-2')
  assert.deepEqual(revoked, ['blob:http://localhost/review-rebate-1'])
  await close()
  assert.ok(!image())
  assert.deepEqual(revoked, ['blob:http://localhost/review-rebate-1', 'blob:http://localhost/review-rebate-2'])
})

test('untrusted, mismatched, decorated and legacy destinations never dispatch credentials', async () => {
  for (const target of ['https://foreign.invalid' + first, '//foreign.invalid' + first, second, first + '?x=1', first + '\n', first.replace(/^\/api/, '/adapter-service')]) {
    if (!wrapper) await open([target])
    else {
      claims[0].screenshotUrl = target
      await wrapper.vm.loadClaims()
      await settle()
    }
    assert.equal(wrapper.find('a[href="' + target.trim() + '"]').exists(), false)
    if (previewButtons().length) await show()
    assert.equal(calls.length, 0)
    assert.ok(!image())
  }
})

test('permission, not found and network errors show distinct local messages without creating a blob', async () => {
  await open([first])
  for (const [status, expected] of [[403, /无权/], [404, /不存在/], [500, /加载失败/], [0, /网络/], [200, /不是受支持的图片/]]) {
    screenshot = async () => {
      if (!status) throw new Error('Network Error')
      return { status, data: new Blob(['not an image'], { type: 'text/html' }) }
    }
    await show()
    assert.match(dialog()?.textContent || '', expected)
    assert.ok(!image())
    assert.equal(created.length, 0)
    await close()
  }
})

test('a late response is discarded after close and principal change; active blobs retire on unmount', async () => {
  await open([first])
  let finish
  screenshot = () => new Promise(resolve => { finish = resolve })
  await show()
  assert.match(dialog()?.textContent || '', /正在加载/)
  await close()
  assert.equal(calls.at(-1).signal.aborted, true)
  finish({ status: 200, data: fixture })
  await settle()
  assert.equal(created.length, 0)

  await show()
  setStoredCurrentUser({ id: 102, role: 'user', status: 'enabled' })
  await settle()
  assert.equal(calls.at(-1).signal.aborted, true)
  finish({ status: 200, data: fixture })
  await settle()
  assert.equal(created.length, 0)
  assert.equal(previewButtons().length, 0)

  setStoredCurrentUser(admin)
  screenshot = async () => ({ status: 200, data: fixture })
  await settle()
  await show()
  assert.ok(image())
  wrapper.unmount()
  wrapper = undefined
  await settle()
  assert.deepEqual(revoked, ['blob:http://localhost/review-rebate-1'])
})

test('401 reuses the existing session retirement flow and does not keep a preview', async () => {
  await open([first])
  screenshot = async () => ({ status: 401, data: new Blob(['expired'], { type: 'application/json' }) })
  await show()
  assert.equal(getStoredCurrentUser(), null)
  assert.equal(previewButtons().length, 0)
  assert.ok(!image())
  assert.equal(created.length, 0)
})

test('current PNG decode failure revokes only its own Blob, not a replacement preview', async () => {
  await open([first, second])
  await show()
  const retiredImage = image()
  assert.ok(retiredImage)
  screenshot = async () => ({ status: 200, data: new Blob(['invalid PNG'], { type: 'image/png' }) })
  await show(1)
  const currentImage = image()
  assert.ok(currentImage)
  retiredImage.dispatchEvent(new Event('error'))
  await settle()
  assert.equal(image(), currentImage)
  assert.deepEqual(revoked, ['blob:http://localhost/review-rebate-1'])

  currentImage.dispatchEvent(new Event('error'))
  await settle()
  assert.ok(!image(), 'do not leave a broken image for a decode error')
  assert.match(dialog()?.textContent || '', /无法解码/)
  assert.deepEqual(revoked, ['blob:http://localhost/review-rebate-1', 'blob:http://localhost/review-rebate-2'])
  await close()
  assert.equal(revoked.length, 2)
})

test('a screenshot ban retires the page without reading more administrator data', async () => {
  await open([first])
  const initialReads = managementReads.length
  screenshot = async () => ({
    status: 403,
    data: new Blob(['{"code":"ACCOUNT_BANNED"}'], { type: 'application/json' }),
  })
  await show()
  assert.equal(getStoredCurrentUser()?.status, 'banned')
  assert.equal(previewButtons().length, 0)
  assert.ok(!image())
  assert.equal(managementReads.length, initialReads, 'do not replay admin list requests under the banned session')
})
