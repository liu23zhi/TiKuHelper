const { test } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
// SweetAlert2 uses the same native jsdom parser as the mounted document.
for (const key of ['DOMParser', 'HTMLVideoElement', 'HTMLAudioElement']) globalThis[key] = window[key]
const { mount, flushPromises, DOMWrapper } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const currentUser = require(path.join(web, 'src/utils/currentUser.js'))

// Synthetic API boundary only: the page, table, checkboxes, buttons and modal are real.
const rows = [
  { id: 101, course_name: '合成课程', type: 0, question: '合成第一页题目', options: '["甲","乙"]', answer: '["甲"]', source: 1, source_name: 'fixture', extra: '' },
  { id: 201, course_name: '合成课程', type: 4, question: '合成第二页题目', options: '[]', answer: '["合成答案","合成答案"]', source: 1, source_name: 'fixture', extra: '' },
]
let activeWrapper
let queries = []
let courseQueries = 0
let courseFailure = false
let courseRows = []
let deferUpdates = false
let pendingUpdates = []
let writes = []
let blobs = []
let downloads = []
let revoked = []
const apiPath = path.join(web, 'src/api/TikuComponentApi.js')
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  getCourses: () => { courseQueries += 1; return courseFailure ? Promise.reject(new Error('SYNTHETIC_COURSES_FAILURE')) : Promise.resolve({ status: 200, data: courseRows }) },
  getQuestions: query => {
    queries.push({ ...query })
    return Promise.resolve({ status: 200, data: { items: [{ ...rows[query.pageNo === 1 ? 1 : 0] }], total: 12 } })
  },
  delQuestions: payload => { writes.push({ action: 'delete', payload }); return Promise.resolve({ status: 200 }) },
  updateQuestions: payload => { writes.push({ action: 'update', payload }); return deferUpdates ? new Promise(resolve => pendingUpdates.push(resolve)) : Promise.resolve({ status: 200 }) },
  createQuestions: () => { throw new Error('Unexpected create request') },
  importQuestions: () => { throw new Error('Unexpected import request') },
}
require.cache[apiPath] = api
window.URL.createObjectURL = blob => { blobs.push(blob); return 'blob:synthetic-question-export' }
window.URL.revokeObjectURL = value => revoked.push(value)
const anchorClick = window.HTMLAnchorElement.prototype.click
window.HTMLAnchorElement.prototype.click = function () {
  downloads.push({ href: this.href, filename: this.download })
}
const pagePath = path.join(web, 'src/pages/TikuComponent/index.vue')
const labels = ['导出选中', '批量编辑课程名', '批量删除']
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 5)); await nextTick() }
function button(wrapper, label) {
  const found = wrapper.findAll('button').find(item => item.text().trim() === label)
  assert.ok(found, 'visible button ' + label)
  return found.element
}
function states(wrapper) { return labels.map(label => button(wrapper, label).disabled) }
function rowCheckbox(wrapper) {
  const found = wrapper.find('tbody input[type="checkbox"]')
  assert.ok(found.exists(), 'actual Antd row checkbox')
  return found.element
}
async function click(element) { element.click(); await settle() }
function witness(name, wrapper, extra = {}) {
  console.log('BULK_DOM_WITNESS ' + JSON.stringify({ name, disabled: states(wrapper), summary: wrapper.find('.selection-alert-content').text(), downloads: downloads.length, blobs: blobs.length, writes: writes.length, dialogs: document.querySelectorAll('[role="dialog"]').length, ...extra }))
}
async function fixture(run, options = {}) {
  currentUser.setStoredCurrentUser({ id: 7301, username: 'synthetic-tiku-admin', role: 'admin' })
  queries = []; writes = []; blobs = []; downloads = []; revoked = []; courseQueries = 0; courseRows = ['合成课程', '另一个课程', '合成课程']; courseFailure = Boolean(options.courseFailure); deferUpdates = Boolean(options.deferUpdates); pendingUpdates = []
  delete require.cache[pagePath]
  const Tiku = require(pagePath)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/component', component: Tiku }] })
  await router.push('/adapter/component'); await router.isReady()
  const wrapper = mount(Tiku, { attachTo: document.body, global: { plugins: [router, Antd], stubs: { transition: false, 'transition-group': false } } })
  activeWrapper = wrapper
  try { await settle(); await run(wrapper) }
  finally {
    // Restore module-level selection through the same visible UI path used by people.
    const clear = wrapper.findAll('button').find(item => item.text().trim() === '取消全部选择')
    if (clear && !clear.element.disabled) await click(clear.element)
    wrapper.unmount(); activeWrapper = undefined; currentUser.clearStoredCurrentUser(); Antd.message.destroy(); await settle(); document.body.innerHTML = ''
  }
}

test('zero selected rows: all three native bulk buttons are disabled and native clicks have no export, modal or delete side effects', async () => {
  await fixture(async wrapper => {
    assert.equal(button(wrapper, '导出选中').disabled, true, 'ZERO_SELECTION_EXPORT_NATIVE_DISABLED')
    assert.equal(button(wrapper, '批量删除').disabled, true, 'ZERO_SELECTION_DELETE_NATIVE_DISABLED')
    for (const label of labels) await click(button(wrapper, label))
    witness('zero-selection-native-clicks', wrapper)
    assert.deepEqual(downloads, [])
    assert.deepEqual(blobs, [])
    assert.deepEqual(writes, [])
    assert.equal(document.querySelector('[role="dialog"]'), null)
    assert.deepEqual(states(wrapper), [true, true, true])
  })
})

test('real row checkbox enables every bulk button; cancel-all selection disables them again', async () => {
  await fixture(async wrapper => {
    await click(rowCheckbox(wrapper))
    assert.equal(rowCheckbox(wrapper).checked, true)
    assert.deepEqual(states(wrapper), [false, false, false])
    witness('row-selected', wrapper)
    await click(button(wrapper, '取消全部选择'))
    assert.equal(rowCheckbox(wrapper).checked, false)
    witness('selection-cleared', wrapper)
    assert.deepEqual(states(wrapper), [true, true, true])
  })
})

test('real pagination preserves off-page selection and export payload; search filter reset disables bulk actions', async () => {
  await fixture(async wrapper => {
    await click(rowCheckbox(wrapper))
    const second = wrapper.find('.ant-pagination-item-2')
    assert.ok(second.exists(), 'real second-page pagination item')
    await click(second.element)
    assert.equal(queries.at(-1).pageNo, 1)
    assert.equal(rowCheckbox(wrapper).checked, false)
    assert.deepEqual(states(wrapper), [false, false, false], 'off-page choice stays actionable')
    await click(rowCheckbox(wrapper))
    await click(button(wrapper, '导出选中'))
    assert.equal(downloads.length, 1)
    assert.equal(blobs.length, 1)
    assert.match(downloads[0].filename, /\.json$/)
    assert.equal(blobs[0].type, 'application/json;charset=utf-8')
    const content = JSON.parse(await blobs[0].text())
    assert.equal(content.length, 2)
    assert.deepEqual(content.map(item => item.question), rows.map(item => item.question))
    assert.equal(content[1].answer, rows[1].answer, 'existing export JSON-string format and repeated answers retained')
    assert.deepEqual(revoked, ['blob:synthetic-question-export'])
    await click(button(wrapper, '取消本页选择'))
    assert.deepEqual(states(wrapper), [false, false, false], 'off-page choice survives clearing this page')
    const input = wrapper.find('input[placeholder="搜索问题"]')
    await input.setValue('合成筛选')
    await click(button(wrapper, '搜索'))
    assert.equal(queries.at(-1).question, '合成筛选')
    assert.equal(queries.at(-1).pageNo, 0)
    assert.equal(rowCheckbox(wrapper).checked, false)
    witness('cross-page-export-filter-reset', wrapper, { exportQuestions: content.map(item => item.question), lastQuery: queries.at(-1) })
    assert.deepEqual(states(wrapper), [true, true, true])
  })
})

test('selected course editing uses the real modal and existing update payload without writing on cancel', async () => {
  await fixture(async wrapper => {
    await click(rowCheckbox(wrapper))
    await click(button(wrapper, '批量编辑课程名'))
    assert.ok(document.querySelector('[role="dialog"]'))
    const cancel = [...document.querySelectorAll('.ant-modal-footer button')].find(item => item.textContent.replace(/\s/g, '') === '取消')
    assert.ok(cancel); await click(cancel)
    assert.deepEqual(writes, [])
    await click(button(wrapper, '批量编辑课程名'))
    const input = document.querySelector('.ant-modal-body input')
    assert.ok(input)
    input.value = '合成新课程'
    input.dispatchEvent(new Event('input', { bubbles: true })); await settle()
    const save = [...document.querySelectorAll('.ant-modal-footer button')].find(item => item.textContent.replace(/\s/g, '') === '保存')
    assert.ok(save); await click(save)
    await new Promise(resolve => setTimeout(resolve, 450)); await settle()
    assert.deepEqual(writes, [{ action: 'update', payload: { ...rows[0], course_name: '合成新课程' } }])
    assert.equal(wrapper.vm.bulkCourseVisible, false)
    assert.equal(wrapper.vm.bulkCourseName, '')
    assert.equal(document.querySelector('.ant-modal-wrap').style.display, 'none', 'persistent Antd4 modal wrapper is hidden after save')
    assert.equal(rowCheckbox(wrapper).checked, false)
    witness('course-save-existing-payload', wrapper)
  })
})

test('selected delete retains the existing per-id API payload and clears the actual selection after success', async () => {
  await fixture(async wrapper => {
    await click(rowCheckbox(wrapper))
    await click(button(wrapper, '批量删除'))
    assert.deepEqual(writes, [], 'dangerous delete waits for explicit real confirmation')
    const confirmation = document.querySelector('.swal2-popup .swal2-confirm')
    assert.ok(confirmation, 'actual SweetAlert2 delete confirmation')
    await click(confirmation)
    assert.deepEqual(writes, [{ action: 'delete', payload: { id: 101 } }])
    assert.equal(rowCheckbox(wrapper).checked, false)
    witness('selected-delete-existing-payload', wrapper)
  })
})

process.on('exit', () => { window.HTMLAnchorElement.prototype.click = anchorClick })

function modal() {
  const input = activeWrapper?.findComponent(Antd.AutoComplete)
  const node = input?.exists() ? input.element.closest('[role="dialog"]') : null
  assert.ok(node, 'actual Antd4 bulk-course dialog')
  assert.ok(activeWrapper.vm.bulkCourseOwner?.isCurrent() && activeWrapper.vm.bulkCourseVisible
    && !node.hidden && node.style.display !== 'none' && node.closest('.ant-modal-wrap')?.style.display !== 'none',
  'only the current component-owned visible bulk-course modal is actionable')
  return node
}
function modalButton(label) {
  const node = [...modal().querySelectorAll('button')].find(item => item.textContent.replace(/\s/g, '') === label)
  assert.ok(node, 'actual modal button ' + label)
  return node
}
async function inputCourse(value) {
  let ready = false
  for (let frame = 0; frame < 30; frame += 1) {
    await nextTick(); await flushPromises()
    const control = activeWrapper.findComponent(Antd.AutoComplete)
    const owner = activeWrapper.vm.bulkCourseOwner
    const native = control.exists() ? control.element.querySelector('input') : null
    const component = activeWrapper.findAllComponents(Antd.Modal).find(item => item.props('title') === '批量编辑课程名称' && item.props('open'))
    ready = Boolean(owner?.isCurrent() && native?.isConnected && !native.disabled
      && control.props('value') === activeWrapper.vm.bulkCourseName && native.value === String(control.props('value') ?? '')
      && typeof component?.vm.$.vnode.props.onOk === 'function')
    if (ready) break
    await new Promise(resolve => requestAnimationFrame(resolve))
  }
  assert.ok(ready, 'current AutoComplete input props and current-owner confirm binding are rendered before native input')
  const input = modal().querySelector('input')
  assert.ok(input, 'actual AutoComplete input')
  const before = activeWrapper.vm.bulkCourseName
  input.value = value
  // VTU dispatches one real bubbling input with Vue's supported event timestamp handling.
  await new DOMWrapper(input).trigger('input'); await settle()
  console.log('BULK_INPUT_COMMIT_TRACE ' + JSON.stringify({ before, input: value,
    committed: activeWrapper.vm.bulkCourseName, ownerCurrent: activeWrapper.vm.bulkCourseOwner.isCurrent(),
    controlledValue: activeWrapper.findComponent(Antd.AutoComplete).props('value') }))
  assert.equal(activeWrapper.vm.bulkCourseName, value, 'one current native input reaches the owned course draft before confirm')
}
async function selectTwoPages(wrapper) {
  await click(rowCheckbox(wrapper))
  await click(wrapper.find('.ant-pagination-item-2').element)
  assert.equal(queries.at(-1).pageNo, 1, 'existing zero-based second-page query')
  await click(rowCheckbox(wrapper))
  assert.deepEqual([...wrapper.vm.selectedRowKeys], [101, 201])
}

test('T10 cross-page course save is serial and cache-backed; cancel writes nothing; success clears modal/name/selection and refetches once', async () => {
  await fixture(async wrapper => {
    await selectTwoPages(wrapper)
    await click(button(wrapper, '批量编辑课程名'))
    await click(modalButton('取消'))
    assert.deepEqual(writes, [])
    assert.deepEqual([...wrapper.vm.selectedRowKeys], [101, 201])
    assert.equal(queries.length, 2)
    await click(button(wrapper, '批量编辑课程名'))
    await inputCourse('  新课程 literal  ')
    await click(modalButton('保存'))
    assert.deepEqual(writes, [{ action: 'update', payload: { ...rows[0], course_name: '新课程 literal' } }])
    assert.equal(pendingUpdates.length, 1, 'second update cannot start before first resolves')
    assert.equal(queries.length, 2, 'pending writes cannot refetch')
    pendingUpdates.shift()({ status: 200 }); await settle()
    assert.deepEqual(writes, rows.map(payload => ({ action: 'update', payload: { ...payload, course_name: '新课程 literal' } })))
    assert.equal(pendingUpdates.length, 1)
    assert.equal(queries.length, 2)
    pendingUpdates.shift()({ status: 200 }); await settle()
    assert.equal(wrapper.vm.bulkCourseVisible, false)
    assert.equal(wrapper.vm.bulkCourseName, '')
    assert.deepEqual([...wrapper.vm.selectedRowKeys], [])
    assert.equal(queries.length, 3, 'all-success refetches exactly once')
    assert.equal(queries.at(-1).pageNo, 1, 'page query preserved after save')
    assert.ok(document.body.textContent.includes('成功更新 2 条题目的课程名称'))
    assert.equal(courseQueries, 1, 'original course suggestions loaded once')
    witness('T10-serial-cache-backed-success', wrapper, { queries, updates: writes, courseQueries })
  }, { deferUpdates: true })
})

test('T11 empty course and first non-200 preserve modal/name/cross-page selection and stop without speculative refetch', async () => {
  await fixture(async wrapper => {
    await selectTwoPages(wrapper)
    await click(button(wrapper, '批量编辑课程名'))
    await inputCourse('   ')
    await click(modalButton('保存'))
    assert.equal(writes.length, 0)
    assert.ok(document.body.textContent.includes('请输入课程名称'))
    await inputCourse('  失败课程  ')
    await click(modalButton('保存'))
    assert.equal(writes.length, 1)
    assert.deepEqual(writes[0].payload, { ...rows[0], course_name: '失败课程' })
    pendingUpdates.shift()({ status: 500 }); await settle()
    assert.equal(writes.length, 1, 'non-200 stops before off-page second row')
    assert.equal(pendingUpdates.length, 0)
    assert.equal(wrapper.vm.bulkCourseVisible, true)
    assert.equal(wrapper.vm.bulkCourseName, '  失败课程  ')
    assert.deepEqual([...wrapper.vm.selectedRowKeys], [101, 201])
    assert.equal(queries.length, 2, 'failure cannot fetch or clear cached selection')
    assert.ok(document.body.textContent.includes('题目 101 更新课程名称失败'))
    witness('T11-validation-first-failure', wrapper, { queries, updates: writes, draft: wrapper.vm.bulkCourseName })
  }, { deferUpdates: true })
})

test('course candidates use the existing course API and real AutoComplete without duplicate names', async () => {
  await fixture(async wrapper => {
    assert.equal(courseQueries, 1)
    await click(rowCheckbox(wrapper)); await click(button(wrapper, '批量编辑课程名'))
    await inputCourse('合成')
    assert.deepEqual([...wrapper.vm.bulkCourseCandidates].map(x => ({ ...x })), [{ label: '合成课程', value: '合成课程' }])
    assert.ok(document.body.textContent.includes('合成课程'))
    assert.equal(writes.length, 0)
    await click(modalButton('取消'))
    assert.equal(wrapper.vm.bulkCourseName, '')
    assert.deepEqual([...wrapper.vm.selectedRowKeys], [101])
    witness('course-candidates-existing-api', wrapper, { courseQueries })
  })
})

test('failed course suggestions still allow free text save with unchanged selection/cache behavior', async () => {
  await fixture(async wrapper => {
    assert.equal(courseQueries, 1)
    assert.ok(document.body.textContent.includes('获取课程列表失败'))
    await click(rowCheckbox(wrapper)); await click(button(wrapper, '批量编辑课程名'))
    await inputCourse('  自由输入课程  ')
    assert.deepEqual([...wrapper.vm.bulkCourseCandidates], [])
    await click(modalButton('保存'))
    assert.deepEqual(writes, [{ action: 'update', payload: { ...rows[0], course_name: '自由输入课程' } }])
    assert.equal(wrapper.vm.bulkCourseVisible, false)
    assert.deepEqual([...wrapper.vm.selectedRowKeys], [])
    assert.equal(queries.length, 2)
    witness('course-list-failure-free-text-success', wrapper, { updates: writes, courseQueries })
  }, { courseFailure: true })
})

test('public course handlers guard empty selection without writing or opening a modal', async () => {
  await fixture(async wrapper => {
    assert.equal(typeof wrapper.vm.openBulkCourseModal, 'function', 'PUBLIC_EMPTY_SELECTION_GUARD')
    wrapper.vm.openBulkCourseModal(); await settle()
    assert.equal(wrapper.vm.bulkCourseVisible, false)
    wrapper.vm.bulkCourseName = '不会写入'
    await wrapper.vm.handleBulkCourseSubmit(); await settle()
    assert.equal(writes.length, 0)
    assert.equal(queries.length, 1)
    assert.ok(document.body.textContent.includes('请先选择需要批量修改的题目'))
    witness('public-course-empty-selection-guard', wrapper)
  })
})


test('T21 remote empty course response retains previously loaded options without touching selection or draft', async () => {
  await fixture(async wrapper => {
    await click(rowCheckbox(wrapper)); await click(button(wrapper, '批量编辑课程名'))
    await inputCourse('待保存课程')
    const before = [...wrapper.vm.courses].map(x => ({ ...x }))
    assert.ok(before.length > 0)
    courseRows = []
    await wrapper.vm.getCourses(); await settle()
    assert.equal(courseQueries, 2, 'remote refresh is exactly one existing API read')
    assert.deepEqual([...wrapper.vm.courses].map(x => ({ ...x })), before, 'T21_REMOTE_EMPTY_RETAINS_EXISTING_OPTIONS')
    assert.deepEqual([...wrapper.vm.selectedRowKeys], [101])
    assert.equal(wrapper.vm.bulkCourseVisible, true)
    assert.equal(wrapper.vm.bulkCourseName, '待保存课程')
    assert.deepEqual(writes, [])
    console.log('T21_COURSE_WITNESS ' + JSON.stringify({ name: 'remote-empty-retains-options', courseQueries, courses: wrapper.vm.courses, selected: wrapper.vm.selectedRowKeys, draft: wrapper.vm.bulkCourseName, writes }))
  })
})

test('T21 direct course arrays replace options and explicit empty array clears them with zero API reads', async () => {
  await fixture(async wrapper => {
    assert.equal(courseQueries, 1)
    await wrapper.vm.getCourses(['直接课程', '直接课程2']); await settle()
    assert.equal(courseQueries, 1, 'T21_DIRECT_ARRAY_ZERO_API')
    assert.deepEqual([...wrapper.vm.courses].map(x => ({ ...x })), [{ value: '直接课程', label: '直接课程' }, { value: '直接课程2', label: '直接课程2' }])
    await wrapper.vm.getCourses([]); await settle()
    assert.equal(courseQueries, 1, 'explicit empty array is not a remote refresh')
    assert.deepEqual([...wrapper.vm.courses], [])
    assert.deepEqual(writes, [])
    console.log('T21_COURSE_WITNESS ' + JSON.stringify({ name: 'direct-array-and-explicit-empty-no-remote', courseQueries, courses: wrapper.vm.courses, writes }))
  })
})
