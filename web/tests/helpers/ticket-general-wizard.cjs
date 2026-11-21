// Exercise the actual type-selection step before reusing the existing general form assertions.
const assert = require('node:assert/strict')
function modalForStep(step) {
  const nodes = [...document.querySelectorAll('[data-ticket-step="' + step + '"]')].filter(n => n.isConnected && n.closest('.ant-modal')?.getAttribute('aria-hidden') !== 'true' && n.closest('.ant-modal-wrap')?.style.display !== 'none')
  assert.equal(nodes.length, 1, 'one live ticket ' + step + ' step')
  return nodes[0].closest('.ant-modal')
}
async function openGeneralTicket(root, settle) {
  const open = [...root.querySelectorAll('button')].filter(b => b.textContent.replace(/\s/g, '') === '发起工单')
  assert.equal(open.length, 1); assert.equal(open[0].disabled, false); open[0].click(); await settle()
  const modal = modalForStep('type'), choice = modal.querySelector('button[data-ticket-type-option="general"]')
  assert.ok(choice); choice.click(); await settle(); assert.equal(choice.getAttribute('aria-checked'), 'true')
  const next = [...modal.querySelectorAll('button')].find(b => b.textContent.replace(/\s/g, '') === '下一步')
  assert.ok(next && !next.disabled); next.click(); await settle()
  return modalForStep('form')
}
// Ant's dialog is teleported; keep ownership scoped to the actual Tickets
// wrapper when multiple pages/dialogs (or a retiring previous actor) coexist.
function notificationForm(page) {
  const Form = require('ant-design-vue').Form
  return page.findAllComponents(Form).find(w => {
    const modal = w.element.closest('.ant-modal')
    return modal && modal.querySelector('.ant-modal-title')?.textContent.trim() === '企业微信工单提醒'
      && modal.closest('.ant-modal-wrap')?.style.display !== 'none' && w.element.isConnected
  })
}
async function openNotificationSettings(page, settle) {
  const button = [...page.element.querySelectorAll('button')].find(b => b.textContent.replace(/\s/g,'') === '提醒设置')
  assert.ok(button && !button.disabled, 'actual authorized notification settings entry')
  button.click(); await settle()
  const form = notificationForm(page)
  assert.ok(form, 'owned real notification AntForm in its dialog')
  return form.element
}
module.exports = { openNotificationSettings, notificationForm, openGeneralTicket, currentGeneralTicket: () => modalForStep('form') }
