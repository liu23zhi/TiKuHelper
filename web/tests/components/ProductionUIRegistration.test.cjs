const test = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { createApp, resolveComponent, h } = require('vue')
test('production UI registration resolves overlay and structured content components', () => {
  const { registerConsoleUI } = require('../../src/registerUI.js')
  const resolved = []
  const app = createApp({ setup() {
    for (const name of ['a-popover', 'a-descriptions', 'a-descriptions-item', 'a-drawer', 'a-timeline', 'a-timeline-item', 'a-divider', 'a-result', 'a-typography-paragraph']) resolved.push([name, resolveComponent(name)])
    return () => h('div')
  } })
  registerConsoleUI(app)
  const host = document.createElement('div'); app.mount(host)
  try { for (const [name, component] of resolved) assert.notEqual(typeof component, 'string', name + ' must be a registered Vue component') }
  finally { app.unmount() }
})
