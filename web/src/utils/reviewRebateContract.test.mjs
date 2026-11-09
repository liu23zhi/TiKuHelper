import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

// 评价返现的前后端字段契约。
//
// 这些断言钉住的是「前端实际发给后端 / 从后端读取的名字」。它们不是风格检查：
// 字段名一旦漂移，症状是「功能静默失效」——链接没保存、结论永远 400、
// 列表永远空 —— 都很难从界面上看出来。因此这里按字符串断言，改谁都要过一次这里。
const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
const apiPath = path.join(__dirname, '..', 'api', 'ReviewRebateApi.js')
const userPagePath = path.join(__dirname, '..', 'pages', 'ReviewRebate', 'index.vue')
const adminPagePath = path.join(__dirname, '..', 'pages', 'ReviewRebateAdmin', 'index.vue')

test('ReviewRebateApi submits the canonical claimedReviewUrl form field', async () => {
  const content = await readFile(apiPath, 'utf8')
  // 规范名来自实体 ClaimedReviewURL；老名字 reviewUrl 后端仍兼容，但前端只发规范名。
  assert.match(content, /form\.append\('claimedReviewUrl', link\)/)
  assert.doesNotMatch(content, /form\.append\('reviewUrl'/)
})

test('ReviewRebateApi decision body uses the canonical action field', async () => {
  const content = await readFile(apiPath, 'utf8')
  // 后端 controller 读 `action`；`decision` 只是兼容别名。
  assert.match(content, /data: \{ action: decision, note: String\(note \|\| ''\) \}/)
  assert.doesNotMatch(content, /data: \{ decision, note:/)
})

test('ReviewRebateApi exposes the admin screenshot proxy and score endpoints', async () => {
  const content = await readFile(apiPath, 'utf8')
  // 截图在私有目录、没有可直连 URL，只能走鉴权代理。
  assert.match(content, /claims\/\$\{value\}\/screenshot/)
  assert.match(content, /claims\/\$\{value\}\/score/)
})

test('ReviewRebate admin page unpacks the {data:[...]} programs envelope', async () => {
  const content = await readFile(adminPagePath, 'utf8')
  // GET /admin/review-rebate/programs 返回 {data:[...]}。直接当对象用会让整张表单变空。
  assert.match(content, /Array\.isArray\(data\?\.data\)/)
  // 打分来源必须能显示：stub 是被标出来的占位实现，不是核验结论。
  assert.match(content, /agentScorer/)
  assert.match(content, /占位/)
  // 复核要能看到模型给的理由。
  assert.match(content, /agentRationale/)
})

test('ReviewRebate user page treats a disabled feature as a normal closed state', async () => {
  const content = await readFile(userPagePath, 'utf8')
  // 后端开关关闭时返回 400 + 「未启用」；这不该显示成红色失败告警。
  assert.match(content, /isReviewRebateDisabledResponse/)
  assert.match(content, /未启用/)
  // usage 的条数按 approvedCount（后端同时下发 claimedCount，两者恒等）。
  assert.match(content, /usage\.value\.approvedCount/)
  assert.match(content, /usage\.value\.rejectedCount/)
})
