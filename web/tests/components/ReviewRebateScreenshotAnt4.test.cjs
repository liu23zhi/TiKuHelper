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

const originals = { adapter: axios.defaults.adapter, create: URL.createObjectURL, revoke: URL.revokeObjectURL }
const base = '/api/admin/review-rebate'
const first = base + '/claims/42/screenshot'
const second = base + '/claims/43/screenshot'
const admin = { id: 101, role: 'admin', status: 'enabled' }
const fixture = new Blob(['synthetic image'], { type: 'image/png' })
const calls = [], created = [], revoked = []
let claims, screenshot, wrapper, router
URL.createObjectURL = blob => {
  created.push(blob)
  return 'blob:http://localhost/review-rebate-' + created.length
}
URL.revokeObjectURL = value => revoked.push(value)
axios.defaults.adapter = async config => {
  if (config.url === base + '/claims') return { status: 200, config, data: { data: claims } }
  if ([base + '/programs', base + '/rules'].includes(config.url)) return { status: 200, config, data: { data: [] } }
  calls.push(config)
  if (![first, second].includes(config.url)) throw new Error('unexpected screenshot target')
  const result = await screenshot(config)
  return { ...result, config }
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
  calls.length = created.length = revoked.length = 0
})
after(() => {
  axios.defaults.adapter = originals.adapter
  URL.createObjectURL = originals.create
  URL.revokeObjectURL = originals.revoke
})

test('admin previews the canonical screenshot in-page with the existing authenticated request and revokes each blob', async () => {
  await open([first, second, ''])
  assert.equal(previewButtons().length, 2)
  assert.match(wrapper.text(), /无图/)
  assert.equal(wrapper.find('a[href="' + first + '"]').exists(), false)
  await show()
  assert.deepEqual(calls.map(({ url, method, withCredentials, responseType }) => ({ url, method, withCredentials, responseType })),
    [{ url: first, method: 'get', withCredentials: true, responseType: 'blob' }])
  assert.deepEqual(created, [fixture])
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
