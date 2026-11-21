const test = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { createApp, h } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const { registerPublicUI, installConsoleUIGuard } = require('../../src/publicUI.js')
const { registerConsoleExtras } = require('../../src/registerUI.js')

test('public routes have auth controls without loading tables, and a direct console visit waits for the real widgets', async () => {
  const page = { render: () => h('div') }
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/', name: 'loginlayout', component: page, children: [{ path: 'login', name: 'login', component: page }] },
    { path: '/console', name: 'console', component: page },
    { path: '/another', name: 'another', component: page },
  ] })
  const app = registerPublicUI(createApp(page))
  let loads = 0
  installConsoleUIGuard(app, router, async () => { loads++; return { registerConsoleExtras } })
  await router.push('/login')
  assert.ok(app.component('AForm')); assert.ok(app.component('ARadioGroup'))
  assert.equal(loads, 0); assert.equal(app.component('ATable'), undefined)
  await router.push('/console')
  assert.ok(app.component('ATable')); assert.ok(app.component('ConsoleTableShell'))
  assert.equal(loads, 1)
  await router.push('/another')
  assert.equal(loads, 1, 'install the admin widgets once for the app lifetime')
  router.options.history.destroy()
})
