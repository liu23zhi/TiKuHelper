<template>
  <div class="console-page extension-logs-page">
    <ConsolePageHeading title="扩展诊断日志" description="查看各账号、设备和每次启动的诊断事件。扩展不上传页面正文、请求内容或凭据。">
      <template #actions>
        <a-button v-if="authorized" data-action="refresh" :disabled="reading" :loading="reading" :onClick="owned(refresh)">刷新日志</a-button>
      </template>
    </ConsolePageHeading>
    <p v-if="!authorized" role="status">仅当前有效的管理员账号可查看扩展诊断日志。</p>
    <div v-else :key="generation" class="extension-log-panels">
      <ConsolePanel title="诊断事件" :description="historical ? '旧版记录按服务器接收顺序显示，最新在前。' : '来自历史文件和回执库的日志统一按事件发生时间排序；发生时间和接收时间分别显示为北京时间。'">
        <div class="extension-log-form log-source" aria-label="诊断数据来源">
          <a-button :type="historical ? 'default' : 'primary'" data-action="show-receipts" :disabled="reading || exporting" :onClick="owned(selectSource, false)">全部诊断</a-button>
          <a-button :type="historical ? 'primary' : 'default'" data-action="show-history" :disabled="reading || exporting" :onClick="owned(selectSource, true)">历史日志</a-button>
          <a-button v-if="!historical" data-action="export" :disabled="reading || exporting" :loading="exporting" :onClick="owned(download)">下载筛选结果</a-button>
          <p v-if="exportError" role="alert">{{ exportError }}</p>
        </div>
        <form class="extension-log-form filter-form" aria-label="日志筛选" @submit.prevent="submitFilters">
          <label for="extension-log-user">用户 ID<input id="extension-log-user" v-model.trim="draft.user" inputmode="numeric" placeholder="全部账号" /></label>
          <label for="extension-log-device">设备 ID<input id="extension-log-device" v-model.trim="draft.device" placeholder="完整设备 ID" /></label>
          <label for="extension-log-mode">运行方式
            <select id="extension-log-mode" v-model="draft.mode" @change="modeChanged">
              <option value="">全部方式</option><option v-for="[key, text] in modes" :key="key" :value="key">{{ text }}</option>
            </select>
          </label>
          <label v-if="!draft.mode || draft.mode === 'native_browser'" for="extension-log-browser">浏览器 ID<input id="extension-log-browser" v-model.trim="draft.browser" placeholder="完整浏览器 ID" /></label>
          <label for="extension-log-launch">启动 ID<input id="extension-log-launch" v-model.trim="draft.launch" placeholder="完整启动 UUID" /></label>
          <a-button html-type="submit" data-action="filter" :disabled="reading || !!filterError">筛选</a-button>
          <a-button data-action="clear-filter" :disabled="reading || !hasFilter" :onClick="owned(clearFilters)">清除筛选</a-button>
          <p v-if="filterError" class="hint" role="alert">{{ filterError }}</p>
        </form>
        <p v-if="listError" role="alert">{{ listError }}</p>
        <p v-if="reading && !rows.length" role="status">正在读取日志…</p>
        <p v-else-if="hasList && !rows.length && !listError" role="status">暂无扩展诊断日志。</p>
        <div v-if="rows.length" class="extension-log-table" tabindex="0" aria-label="扩展诊断日志表格，可横向滚动">
          <table>
            <caption class="sr-only">{{ historical ? "历史扩展诊断，最新接收的在前" : "统一诊断，按发生时间升序" }}</caption>
            <thead><tr><th scope="col">接收时间</th><th scope="col">发生时间</th><th scope="col">级别</th><th scope="col">事件</th><th scope="col">用户 ID</th><th scope="col">设备 ID</th><th scope="col">运行方式</th><th scope="col">浏览器 ID</th><th scope="col">启动 ID</th><th scope="col">扩展版本</th><th scope="col">耗时</th><th scope="col">数量</th><th scope="col">定位</th></tr></thead>
            <tbody>
              <tr v-for="(entry, index) in rows" :key="index">
                <td><time :datetime="entry.received_at">{{ displayTime(entry.received_at) }}</time></td>
                <td><time :datetime="entry.time">{{ displayTime(entry.time) }}</time><span v-if="entry.clock_regressed">（客户端时钟回拨）</span></td>
                <td>{{ levelLabel(entry.level) }}</td><td>{{ eventLabel(entry.event) }}</td>
                <td>{{ entry.user_id }}</td><td><code>{{ entry.device_id || '—' }}</code></td><td>{{ modeLabel(entry.mode) }}</td>
                <td><code v-if="entry.mode === 'native_browser'">{{ entry.browser_id || '未上报' }}</code><span v-else>不适用</span></td><td><code>{{ entry.launch_id || '—' }}</code></td><td>{{ entry.version }}</td>
                <td>{{ entry.duration_ms == null ? '—' : entry.duration_ms + ' 毫秒' }}</td><td>{{ entry.count ?? '—' }}</td>
                <td><a-button v-if="entry.user_id && entry.device_id && entry.launch_id" type="link" data-action="locate-launch" :disabled="reading" :onClick="owned(locateLaunch, entry)">只看本次启动</a-button></td>
              </tr>
            </tbody>
          </table>
        </div>
        <a-button v-if="cursor" class="older-button" data-action="older" :disabled="reading" :onClick="owned(older)">{{ historical ? "查看更早的日志" : "继续查看" }}</a-button>
      </ConsolePanel>
      <ConsolePanel title="回传与保留设置" description="按账号、设备和启动区分，达到保留期限或容量上限时优先清理最早收到的日志。">
        <p v-if="policyError" role="alert">{{ policyError }}</p>
        <p v-if="policyNotice" role="status">{{ policyNotice }}</p>
        <p v-if="policyReading" role="status">正在读取保留设置…</p>
        <a-button v-else-if="!saved" :onClick="owned(loadPolicy)">重试读取设置</a-button>
        <form v-if="saved" class="extension-log-form" aria-label="回传与保留设置" @submit.prevent="submitPolicy">
          <label class="upload-toggle" for="extension-log-enabled"><input id="extension-log-enabled" v-model="enabled" type="checkbox" :disabled="saving" />启用扩展日志回传</label>
          <p class="hint">默认开启，仅在设备确认当前正式协议后生效；关闭回传不立即删除历史日志。</p>
          <label for="extension-log-retention">保留天数
            <input id="extension-log-retention" v-model.number="days" type="number" min="1" max="7" step="1" required :disabled="saving" />
          </label>
          <label for="extension-log-size">总容量上限（GB）
            <input id="extension-log-size" v-model.number="maxGB" type="number" min="1" max="10" step="1" required :disabled="saving" />
          </label>
          <p class="hint">保留 1–7 天，容量 1–10 GB；1 GB = 10 亿字节。</p>
          <a-button type="primary" html-type="submit" data-action="save" :loading="saving" :disabled="!canSave">保存设置</a-button>
        </form>
      </ConsolePanel>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref, shallowRef, watch } from 'vue'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import { exportDiagnosticReceipts, getExtensionLogPolicy, listDiagnosticReceipts, listExtensionLogs, saveExtensionLogPolicy } from '@/api/ExtensionLogApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity, useCurrentUserRequestScope } from '@/utils/currentUserScope'

defineOptions({ name: 'ExtensionDiagnosticLogs' })

const labels = new Map(Object.entries({
  'session.start': '本次启动（新文件）', 'extension.ready': '扩展就绪', 'settings.saved': '设置已保存', 'sync.success': '同步成功', 'sync.failed': '同步失败',
  'network.timeout': '网络超时', 'network.offline': '网络离线', 'runtime.error': '运行错误',
}))
const modes = new Map(Object.entries({ extension: '普通浏览器扩展', native_browser: '原生浏览器运行', native_exam: '原生考试运行' }))
const modeLabel = mode => modes.get(mode) || '未标注'
const levels = new Map(Object.entries({ debug: '调试', info: '信息', warn: '警告', error: '错误' }))
const eventLabel = code => labels.get(code) || '未识别事件'
const levelLabel = code => levels.get(code) || '未知'
const clock = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium', timeZone: 'Asia/Shanghai', hour12: false })
function displayTime(value) {
  const date = new Date(value)
  return value && Number.isFinite(date.getTime()) ? clock.format(date) : '—'
}
const { currentUser } = useCurrentUserDisplay()
const logout = getCurrentUserLogoutEpoch()
const authorized = computed(() => getCurrentUserLogoutEpoch() === logout && Boolean(currentUserIdentity(currentUser.value)) &&
  isAdminUser(currentUser.value) && String(currentUser.value?.status || 'enabled') === 'enabled')
const historical = ref(false), exporting = ref(false), exportError = ref('')
const rows = shallowRef([]), cursor = ref(''), snapshot = ref(''), reading = ref(false), hasList = ref(false), listError = ref('')
const saved = shallowRef(null), enabled = ref(false), days = ref(null), maxGB = ref(null), saving = ref(false), policyReading = ref(false)
const blankFilter = () => ({ user: '', device: '', launch: '', mode: '', browser: '' })
const draft = reactive(blankFilter()), applied = shallowRef({})
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const filterError = computed(() => {
  if (draft.user && (!/^[1-9][0-9]*$/.test(draft.user) || Number(draft.user) > 2147483647)) return '用户 ID 必须是有效正整数。'
  if (draft.launch && !uuid.test(draft.launch)) return '启动 ID 必须是完整的 UUID。'
  return ''
})
const hasFilter = computed(() => draft.user || draft.device || draft.launch || draft.mode || draft.browser || Object.keys(applied.value).length)
const policyError = ref(''), policyNotice = ref(''), generation = ref(0)
const valid = (n, max) => Number.isInteger(n) && n >= 1 && n <= max
const canSave = computed(() => authorized.value && saved.value && !saving.value && valid(days.value, 7) && valid(maxGB.value, 10) &&
  (enabled.value !== saved.value.enabled || days.value !== saved.value.retention_days || maxGB.value !== saved.value.max_gb))
function clear() {
  generation.value++
  historical.value = exporting.value = false; exportError.value = ""
  rows.value = []; cursor.value = ''; snapshot.value = ''; hasList.value = reading.value = false; listError.value = ''
  Object.assign(draft, blankFilter()); applied.value = {}; enabled.value = false
  saved.value = days.value = maxGB.value = null; saving.value = policyReading.value = false; policyError.value = policyNotice.value = ''
}
const scope = useCurrentUserRequestScope(currentUser, { clear, reset() {}, reload: open })
function capture() {
  const isOwner = scope.captureDataOwner(), version = generation.value
  return () => isOwner() && version === generation.value && authorized.value
}
function owned(fn, ...preset) {
  const isCurrent = capture()
  return (...args) => { if (isCurrent()) return fn(...preset, ...args) }
}
function open() { if (authorized.value) { refresh(); loadPolicy() } }
onMounted(open)
// Identity and role transitions are already owned by the shared scope. Only
// a status-only change (e.g. enabled → banned) needs its own invalidation.
watch(currentUser, (next, previous) => {
  if (currentUserIdentity(next) !== currentUserIdentity(previous) || isAdminUser(next) !== isAdminUser(previous) ||
      (next?.status || 'enabled') === (previous?.status || 'enabled')) return
  clear(); open()
}, { flush: 'sync' })
async function loadPage(append) {
  if (!authorized.value || !scope.canLoadForCurrentUser() || reading.value || (append && !cursor.value)) return
  const live = capture()
  reading.value = true; listError.value = ''
  try {
    const list = historical.value ? listExtensionLogs : listDiagnosticReceipts
    const result = await list(applied.value, append ? cursor.value : '')
    if (!live()) return
    if (result?.status !== 200 || !Array.isArray(result.data?.entries)) throw new Error()
    rows.value = append ? [...rows.value, ...result.data.entries] : result.data.entries
    cursor.value = result.data.next_cursor || ''; snapshot.value = historical.value ? '' : result.data.snapshot || ''; hasList.value = true
  } catch {
    if (live()) listError.value = '日志读取失败，请刷新日志或重试查看下一页。'
  } finally { if (live()) reading.value = false }
}
const refresh = () => loadPage(false)
const older = () => loadPage(true)
function applyFilter() {
  if (!authorized.value || filterError.value || reading.value) return
  applied.value = { ...(draft.user && { user_id: Number(draft.user) }), ...(draft.device && { device_id: draft.device }), ...(draft.launch && { launch_id: draft.launch }),
    ...(draft.mode && { mode: draft.mode }), ...(draft.browser && { browser_id: draft.browser }) }
  // A cursor belongs to its exact filter. Never show an old selection as the new one on error.
  rows.value = []; cursor.value = ''; snapshot.value = ''; hasList.value = false; refresh()
}
const submitFilters = computed(() => owned(applyFilter))
function modeChanged() { if (draft.mode && draft.mode !== 'native_browser') draft.browser = '' }
function clearFilters() { Object.assign(draft, blankFilter()); applyFilter() }
function selectSource(history) {
  if (historical.value === history || reading.value || exporting.value) return
  historical.value = history; exportError.value = ''
  Object.assign(draft, blankFilter()); applyFilter()
}
async function download() {
  if (!authorized.value || exporting.value || historical.value || !scope.canLoadForCurrentUser()) return
  const live = capture()
  exporting.value = true; exportError.value = ''
  try {
    const result = await exportDiagnosticReceipts(applied.value, snapshot.value)
    if (!live()) return
    if (result?.status !== 200 || !(result.data instanceof Blob)) throw new Error()
    const url = URL.createObjectURL(result.data)
    try {
      const link = document.createElement('a')
      link.href = url; link.download = 'diagnostic-receipts.jsonl'; link.click()
    } finally { setTimeout(() => URL.revokeObjectURL(url), 1000) }
  } catch { if (live()) exportError.value = '日志导出失败。请缩小筛选范围或刷新后重试。' }
  finally { if (live()) exporting.value = false }
}
function locateLaunch(entry) {
  Object.assign(draft, { user: String(entry.user_id), device: entry.device_id, launch: entry.launch_id,
    mode: modes.has(entry.mode) ? entry.mode : '', browser: entry.mode === 'native_browser' ? entry.browser_id || '' : '' })
  applyFilter()
}
function setPolicy(value) {
  if (typeof value?.enabled !== 'boolean' || !valid(value?.retention_days, 7) || !valid(value?.max_gb, 10)) throw new Error()
  saved.value = value; enabled.value = value.enabled; days.value = value.retention_days; maxGB.value = value.max_gb
}
async function loadPolicy() {
  if (!authorized.value || !scope.canLoadForCurrentUser() || policyReading.value || saving.value) return
  const live = capture()
  policyReading.value = true; policyError.value = policyNotice.value = ''
  try {
    const result = await getExtensionLogPolicy()
    if (!live()) return
    if (result?.status !== 200) throw new Error()
    setPolicy(result.data)
  } catch { if (live()) policyError.value = '保留设置读取失败，请重试。' }
  finally { if (live()) policyReading.value = false }
}
const submitPolicy = computed(() => owned(async () => {
  if (!canSave.value) return
  const live = capture()
  saving.value = true; policyError.value = policyNotice.value = ''
  try {
    const result = await saveExtensionLogPolicy({ enabled: enabled.value, retention_days: days.value, max_gb: maxGB.value })
    if (!live()) return
    if (result?.status !== 200) throw new Error()
    setPolicy(result.data); policyNotice.value = '设置已保存。'
  } catch { if (live()) policyError.value = '保留设置保存失败，修改内容尚未保存。' }
  finally { if (live()) saving.value = false }
}))
</script>

<style scoped>
.extension-log-panels { display: grid; gap: 1rem; }
.extension-log-table { overflow: auto; }
.extension-log-table table { width: 100%; border-collapse: collapse; text-align: left; }
.extension-log-table th, .extension-log-table td { padding: .55rem .7rem; white-space: nowrap; border-bottom: 1px solid var(--console-border, #d9d9d9); }
.extension-log-form { display: flex; flex-wrap: wrap; align-items: end; gap: .7rem 1rem; }
.extension-log-form label { display: grid; gap: .25rem; }
.extension-log-form input, .extension-log-form select { color: inherit; background: transparent; border: 1px solid var(--console-border, #aaa); border-radius: .4rem; padding: .4rem .55rem; }
.extension-log-form .upload-toggle { display: flex; align-items: center; gap: .5rem; }
.filter-form, .log-source { margin-bottom: 1rem; }
.hint { flex-basis: 100%; margin: 0; }
.older-button { margin-top: .75rem; }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
</style>
