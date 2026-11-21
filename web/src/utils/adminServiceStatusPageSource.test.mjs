import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const webRoot = path.resolve(here, '../..')
const api = fs.readFileSync(path.join(webRoot, 'src/api/AdminServiceStatusApi.js'), 'utf8')
const page = fs.readFileSync(path.join(webRoot, 'src/pages/AdminServiceStatus/index.vue'), 'utf8')

test('管理员详情页面只调用受保护的详细状态 API，并复用进程诊断指标', () => {
  assert.match(api, /url:\s*'\/api\/admin\/service-status'/)
  assert.match(page, /getAdminServiceStatus\(\)/)
  assert.match(page, /SystemMetricsPanel/)
  assert.match(page, /upstreamFailures/)
  assert.match(page, /authFailures/)
  assert.match(page, /当前进程/)
})

test('管理员详情页面检查管理员身份，并在卸载时废弃迟到响应', () => {
  assert.match(page, /isAdminUser\(currentUser\.value\)/)
  assert.match(page, /useCurrentUserRequestScope/)
  assert.match(page, /onBeforeUnmount\(clear\)/)
  assert.match(page, /const isCurrent = \(\) => owns\(\) && ticket === requestVersion && canView\.value/)
})
