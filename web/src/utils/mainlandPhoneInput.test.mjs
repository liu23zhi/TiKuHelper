import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { computed, defineComponent, effectScope, nextTick, reactive, ref, watch } from 'vue'
import { normalizePhoneInput, normalizePhonePaste } from './captchaHelpers.mjs'

async function createPhoneInput(value = '') {
  const source = await readFile(new URL('../components/MainlandPhoneInput.vue', import.meta.url), 'utf8')
  const script = source.match(/<script>([\s\S]*?)<\/script>/)?.[1]
  const fieldChanges = []
  const fieldBlurs = []
  let injected = 0
  const formItemContext = {
    id: computed(() => 'phone-field'),
    onFieldChange: () => fieldChanges.push(props.value),
    onFieldBlur: () => fieldBlurs.push(props.value),
  }
  const Form = { useInjectFormItemContext: () => { injected += 1; return formItemContext } }
  const component = new Function('computed', 'defineComponent', 'nextTick', 'ref', 'watch', 'Form', 'message', 'MobileOutlined', 'normalizePhoneInput', 'normalizePhonePaste',
    script.replace(/^import .+ from .+\r?\n/gm, '').replace('export default', 'return'))(
    computed, defineComponent, nextTick, ref, watch, Form, { warning: () => warnings.push(true) }, {}, normalizePhoneInput, normalizePhonePaste,
  )
  const props = reactive({ value })
  const emitted = []
  const warnings = []
  const scope = effectScope()
  const input = scope.run(() => component.setup(props, {
    emit: (event, nextValue) => {
      emitted.push([event, nextValue])
      props.value = nextValue
    },
  }))
  return { props, input, emitted, warnings, source, fieldChanges, fieldBlurs, get injected() { return injected }, stop: () => scope.stop() }
}

test('all phone-entry pages use the shared mainland input', async () => {
  for (const name of ['Register', 'Login', 'Profile', 'UserList']) {
    const page = await readFile(new URL(`../pages/${name}/index.vue`, import.meta.url), 'utf8')
    assert.match(page, /<MainlandPhoneInput\s+v-model:value="(?:registerForm|smsLoginForm|phoneForm|formState)\.phone"/)
    assert.doesNotMatch(page, /国际号码请带/)
  }
})

test('the input fixes +86 outside the editable eleven-digit body', async (t) => {
  const context = await createPhoneInput('+86 13800000000')
  t.after(context.stop)
  assert.match(context.source, /addon-before="\+86"/)
  assert.match(context.source, /:maxlength="11"/)
  assert.match(context.source, /inputmode="numeric"/)
  assert.match(context.source, /请输入11位手机号，无需输入\+86/)
  assert.equal(context.input.displayValue.value, '13800000000')
  assert.equal(context.props.value, '13800000000')
})

test('paste is handled before truncation and invalid paste preserves the previous value', async (t) => {
  const context = await createPhoneInput('13900000000')
  t.after(context.stop)
  let prevented = 0
  const event = (text) => ({ target: { value: context.props.value }, clipboardData: { getData: () => text }, preventDefault: () => { prevented += 1 } })
  context.input.handlePaste(event('+86 \t138 0000 0000'))
  await nextTick()
  assert.equal(context.props.value, '13800000000')
  for (const text of ['+1 3800000000', '1380000000a', '138000000000', '１３８００００００００']) {
    const paste = event(text)
    context.input.handlePaste(paste)
    assert.equal(context.props.value, '13800000000')
    assert.equal(paste.target.value, '13800000000')
  }
  assert.equal(prevented, 5)
  assert.equal(context.warnings.length, 4)
})

test('invalid typed characters are rejected and clearing remains possible', async (t) => {
  const context = await createPhoneInput('138000')
  t.after(context.stop)
  for (const text of ['138000a', '138 000', '+86138000', '138000000000']) {
    const input = { target: { value: text } }
    context.input.handleInput(input)
    assert.equal(input.target.value, '138000')
    assert.equal(context.props.value, '138000')
  }
  context.input.handleInput({ target: { value: '' } })
  assert.equal(context.props.value, '')
  assert.equal(context.input.displayValue.value, '')
})

test('unrecognized historical values are not silently cleared', async (t) => {
  const context = await createPhoneInput('+14155552671')
  t.after(context.stop)
  assert.equal(context.props.value, '+14155552671')
  assert.equal(context.emitted.length, 0)
})

test('clearing and re-entering a number validates the committed value after the update', async (t) => {
  const context = await createPhoneInput('13800000000')
  t.after(context.stop)
  context.input.handleInput({ target: { value: '' } })
  assert.deepEqual(context.fieldChanges, [])
  await nextTick()
  assert.deepEqual(context.fieldChanges, [''])
  context.input.handleInput({ target: { value: '13800000000' } })
  assert.deepEqual(context.fieldChanges, [''])
  await nextTick()
  assert.deepEqual(context.fieldChanges, ['', '13800000000'])
  assert.equal(context.injected, 1)
})

test('paste sends one committed change notification and preserves blur validation', async (t) => {
  const context = await createPhoneInput('')
  t.after(context.stop)
  context.input.handlePaste({ target: { value: '' }, clipboardData: { getData: () => '+86 13800000000' }, preventDefault() {} })
  assert.deepEqual(context.fieldChanges, [])
  await nextTick()
  assert.deepEqual(context.fieldChanges, ['13800000000'])
  context.input.handleBlur()
  assert.deepEqual(context.fieldBlurs, ['13800000000'])
})
