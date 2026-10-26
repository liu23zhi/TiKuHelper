// Bounded fallback for still-unassociated legacy Antd form-items.
// Native ids/names/ARIA are authoritative. Values, checked state and rules are never written.
const installations = new WeakMap()
let sequence = 0

export function installLegacyFormFallback(root) {
  if (!root?.ownerDocument) return () => {}
  let shared = installations.get(root)
  if (shared) {
    shared.references += 1
    return releaseHandle(shared)
  }
  const doc = root.ownerDocument
  const labels = new Map()
  const controls = new WeakMap()
  const owned = new Map()
  const protectedAttributes = new WeakMap()
  let disposed = false
  let scheduled = false
  const dirty = new Set()
  const uniqueId = kind => {
    let id
    do { id = 'tiku-legacy-' + kind + '-' + sequence++ } while (doc.getElementById(id))
    return id
  }
  const writeOwned = (element, name, value, allowBlank = false) => {
    let protectedNames = protectedAttributes.get(element)
    if (protectedNames?.has(name)) return false
    let attrs = owned.get(element)
    let state = attrs?.get(name)
    const current = element.getAttribute(name)
    if (!state) {
      if (current !== null && !(allowBlank && current === '')) return false
      if (!attrs) { attrs = new Map(); owned.set(element, attrs) }
      state = { original: current, last: current }
      attrs.set(name, state)
    } else if (current !== state.last) {
      // A native/caller update takes ownership permanently; never fight Vue.
      if (!protectedNames) { protectedNames = new Set(); protectedAttributes.set(element, protectedNames) }
      protectedNames.add(name)
      attrs.delete(name)
      return false
    }
    if (value === null) {
      if (current !== null) element.removeAttribute(name)
    } else if (current !== value) element.setAttribute(name, value)
    state.last = value
    return true
  }
  const releaseAttribute = (element, name) => {
    const attrs = owned.get(element)
    const state = attrs?.get(name)
    if (!state) return
    if (element.getAttribute(name) === state.last) {
      if (state.original === null) element.removeAttribute(name)
      else element.setAttribute(name, state.original)
    }
    attrs.delete(name)
  }
  const restoreElement = element => {
    for (const name of [...(owned.get(element)?.keys() || [])]) releaseAttribute(element, name)
    owned.delete(element)
  }
  const detachLabel = label => {
    const state = labels.get(label)
    if (!state) return
    label.removeEventListener('click', state.click)
    labels.delete(label)
    restoreElement(label)
    restoreElement(state.control)
    controls.delete(state.control)
  }
  const ensureId = (element, kind) => {
    if (!element.id) writeOwned(element, 'id', uniqueId(kind), true)
    return element.id
  }
  const refreshItem = item => {
    if (disposed || !root.contains(item)) return
    const label = [...item.querySelectorAll('.ant-form-item-label > label')]
      .find(node => node.closest('.ant-form-item') === item)
    if (!label) return
    const groups = [...item.querySelectorAll('.ant-radio-group, .ant-checkbox-group')]
      .filter(node => node.closest('.ant-form-item') === item)
    const candidates = [...item.querySelectorAll('input:not([type="hidden"]), textarea, select, button[role="switch"]')]
      .filter(node => node.closest('.ant-form-item') === item)
    const legacyFor = label.getAttribute('for') || label.getAttribute('html-for')
    const group = groups.find(node => node.id && node.id === legacyFor) || (groups.length === 1 ? groups[0] : null)
    const control = group || candidates.find(node => node.id && node.id === legacyFor) || (candidates.length === 1 ? candidates[0] : null)
    if (!control) { detachLabel(label); return }
    const previousLabel = labels.get(label)
    if (previousLabel && previousLabel.control !== control) detachLabel(label)
    let state = controls.get(control)
    const labelledBy = control.getAttribute('aria-labelledby')
    const explicitName = control.hasAttribute('aria-label') ||
      (control.hasAttribute('aria-labelledby') && (!state || labelledBy !== state.labelledBy))
    const nativeLabel = !group && label.control === control
    if (explicitName || (!state && nativeLabel)) {
      detachLabel(label)
      return
    }
    if (!state) { state = { labelledBy: null }; controls.set(control, state) }
    let labelState = labels.get(label)
    if (!labelState) {
      labelState = { control, nativeFor: false, click: event => {
        const current = labels.get(label)
        if (!current || current.nativeFor || current.control.disabled) return
        event.preventDefault()
        const target = current.control.matches('.ant-radio-group, .ant-checkbox-group')
          ? (current.control.querySelector('input:checked:not([disabled])') ||
             current.control.querySelector('input:not([disabled])')) : current.control
        target?.focus()
      } }
      labels.set(label, labelState)
      label.addEventListener('click', labelState.click)
    }
    labelState.control = control
    if (explicitName) {
      releaseAttribute(control, 'aria-labelledby')
      state.labelledBy = null
    } else {
      const id = ensureId(control, 'control')
      const nativeLabelable = ['INPUT', 'TEXTAREA', 'SELECT', 'BUTTON'].includes(control.tagName)
      const nativeFor = nativeLabelable && id && doc.getElementById(id) === control &&
        (label.control === control || writeOwned(label, 'for', id, true))
      labelState.nativeFor = !!nativeFor
      if (!nativeFor) {
        const labelId = ensureId(label, 'label')
        if (labelId && writeOwned(control, 'aria-labelledby', labelId)) state.labelledBy = labelId
        if (group && !group.hasAttribute('role')) {
          writeOwned(group, 'role', group.classList.contains('ant-radio-group') ? 'radiogroup' : 'group')
        }
      }
    }
    const invalid = item.classList.contains('ant-form-item-has-error')
    writeOwned(control, 'aria-invalid', invalid ? 'true' : null)
    writeOwned(control, 'aria-required', label.classList.contains('ant-form-item-required') ? 'true' : null)
    const help = [...item.querySelectorAll('.ant-form-item-explain')]
      .find(node => node.closest('.ant-form-item') === item)
    const errorId = invalid && help?.textContent.trim() ? ensureId(help, 'error') : null
    writeOwned(control, 'aria-describedby', errorId)
  }
  const prune = () => {
    for (const label of [...labels.keys()]) if (!root.contains(label)) detachLabel(label)
    for (const element of owned.keys()) if (!root.contains(element)) restoreElement(element)
  }
  const collectContext = node => {
    const element = node?.nodeType === 1 ? node : node?.parentElement
    const item = element?.closest?.('.ant-form-item')
    if (item) dirty.add(item)
  }
  const collectAdded = node => {
    collectContext(node)
    if (node?.nodeType !== 1 && node?.nodeType !== 11) return
    if (node.matches?.('.ant-form-item')) dirty.add(node)
    for (const item of node.querySelectorAll?.('.ant-form-item') || []) dirty.add(item)
  }
  const observer = new doc.defaultView.MutationObserver(records => {
    let removed = false
    for (const record of records) {
      collectContext(record.target)
      if (record.type === 'childList') {
        for (const node of record.addedNodes) collectAdded(node)
        removed ||= [...record.removedNodes].some(node => node.nodeType === 1 &&
          (node.matches('.ant-form-item') || !!node.querySelector('.ant-form-item')))
      }
    }
    if ((!dirty.size && !removed) || scheduled || disposed) return
    scheduled = true
    Promise.resolve().then(() => {
      scheduled = false
      if (disposed) return
      prune()
      const items = [...dirty]; dirty.clear()
      items.forEach(refreshItem)
    })
  })
  shared = {
    references: 1,
    stop() {
      if (disposed) return
      disposed = true
      observer.disconnect()
      labels.forEach((state, label) => label.removeEventListener('click', state.click))
      labels.clear()
      for (const element of [...owned.keys()]) restoreElement(element)
      dirty.clear()
      installations.delete(root)
    },
  }
  installations.set(root, shared)
  root.querySelectorAll('.ant-form-item').forEach(refreshItem)
  observer.observe(root, {
    childList: true, subtree: true, characterData: true, attributes: true,
    attributeFilter: ['class', 'id', 'for', 'html-for', 'disabled', 'aria-label',
      'aria-labelledby', 'aria-describedby', 'aria-invalid', 'aria-required'],
  })
  return releaseHandle(shared)
}

function releaseHandle(shared) {
  let stopped = false
  return () => {
    if (stopped) return
    stopped = true
    shared.references -= 1
    if (!shared.references) shared.stop()
  }
}
