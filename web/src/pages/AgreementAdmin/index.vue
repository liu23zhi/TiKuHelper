<template>
  <div class="console-page agreement-admin">
    <ConsolePageHeading title="用户协议管理" description="编辑协议草稿、发布不可变版本，并查询设备的同意记录。" />
    <p v-if="!authorized" role="status">仅当前有效的管理员账号可管理用户协议。</p>
    <div v-else :key="generation" class="agreement-panels">
      <p v-if="notice" role="status" class="notice">{{ notice }}</p>
      <ConsolePanel title="协议草稿" description="保存草稿不会影响已发布版本或设备。">
        <template #actions>
          <a-button data-action="refresh-drafts" :disabled="drafts.status === 'loading'" :onClick="owned(loadPage, 'drafts', 0)">刷新草稿</a-button>
          <a-button type="primary" data-action="create" :onClick="owned(openDialog, 'create')">新建草稿</a-button>
          <a-button danger data-action="batch-delete" :disabled="!selected.length" :onClick="owned(openDelete, null)">批量删除{{ selected.length ? '（' + selected.length + '）' : '' }}</a-button>
        </template>
        <p v-if="drafts.status === 'error'" role="alert">{{ drafts.message }}</p>
        <p v-else-if="drafts.status === 'loading'" role="status">正在读取草稿…</p>
        <p v-else-if="drafts.status === 'ready' && !drafts.rows.length" role="status">暂无协议草稿。</p>
        <div v-if="drafts.rows.length" class="table-scroll" tabindex="0" aria-label="协议草稿列表">
          <table>
            <thead><tr><th scope="col">选择</th><th scope="col">版本</th><th scope="col">正文摘要</th><th scope="col">草稿修订</th><th scope="col">操作</th></tr></thead>
            <tbody>
              <tr v-for="row in drafts.rows" :key="row.id">
                <td><input v-model="selected" :value="row.id" type="checkbox" :aria-label="'选择草稿 ' + row.version" /></td>
                <td>{{ row.version }}</td><td class="summary">{{ row.summary }}</td><td>{{ row.revision }}</td>
                <td><div class="row-actions">
                  <a-button type="link" data-action="preview" :onClick="owned(openDialog, 'preview', row.id)">预览</a-button>
                  <template v-if="!publishedDrafts.has(row.id)">
                    <a-button type="link" data-action="edit" :onClick="owned(openDialog, 'edit', row.id)">编辑</a-button>
                    <a-button type="link" data-action="publish" :onClick="owned(openDialog, 'publish', row.id)">审阅并发布</a-button>
                  </template>
                  <span v-else>已发布</span>
                  <a-button type="link" danger data-action="delete" :onClick="owned(openDelete, row)">删除草稿</a-button>
                </div></td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="drafts.offset || drafts.hasNext" class="paging" aria-label="草稿分页">
          <a-button v-if="drafts.offset" data-action="previous-drafts" :disabled="drafts.status === 'loading'" :onClick="owned(turnPage, 'drafts', -1)">上一页</a-button>
          <span>第 {{ 1 + drafts.offset / PAGE_SIZE }} 页</span>
          <a-button v-if="drafts.hasNext" data-action="next-drafts" :onClick="owned(turnPage, 'drafts', 1)">下一页</a-button>
        </div>
      </ConsolePanel>

      <ConsolePanel title="发布版本" description="发布后版本和正文不可修改；历史只读。时间均为北京时间。">
        <template #actions>
          <a-button data-action="refresh-releases" :disabled="releases.status === 'loading'" :onClick="owned(loadPage, 'releases', 0)">刷新发布版本</a-button>
        </template>
        <section aria-label="当前发布版本" class="current-release">
          <h3>当前发布版本</h3>
          <p v-if="currentRelease"><strong>{{ currentRelease.version }}</strong> · 发布于 <time :datetime="currentRelease.publishedAt">{{ displayTime(currentRelease.publishedAt) }}</time></p>
          <p v-else-if="headLoaded">尚未发布用户协议。</p>
          <p v-else-if="releases.status === 'loading'" role="status">正在读取当前发布版本…</p>
        </section>
        <p v-if="releases.status === 'error'" role="alert">{{ releases.message }}</p>
        <p v-else-if="releases.status === 'loading'" role="status">正在读取发布历史…</p>
        <div v-if="releases.rows.length" class="table-scroll" tabindex="0" aria-label="发布版本历史">
          <table>
            <thead><tr><th scope="col">版本</th><th scope="col">正文摘要</th><th scope="col">发布时间</th><th scope="col">操作</th></tr></thead>
            <tbody>
              <tr v-for="row in releases.rows" :key="row.id">
                <td>{{ row.version }} <a-tag v-if="row.id === currentRelease?.id" color="blue">当前</a-tag></td>
                <td class="summary">{{ row.summary }}</td>
                <td><time :datetime="row.publishedAt">{{ displayTime(row.publishedAt) }}</time></td>
                <td><a-button type="link" data-action="view-release" :onClick="owned(openDialog, 'release', row.id)">查看已发布协议</a-button></td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="releases.offset || releases.hasNext" class="paging" aria-label="发布历史分页">
          <a-button v-if="releases.offset" data-action="previous-releases" :disabled="releases.status === 'loading'" :onClick="owned(turnPage, 'releases', -1)">上一页</a-button>
          <span>第 {{ 1 + releases.offset / PAGE_SIZE }} 页</span>
          <a-button v-if="releases.hasNext" data-action="next-releases" :onClick="owned(turnPage, 'releases', 1)">下一页</a-button>
        </div>
      </ConsolePanel>

      <ConsolePanel title="同意记录" description="未选择版本时查询当前发布版本。尚未同意表示没有该版本的同意事件，不代表设备拒绝。">
        <template #actions>
          <a-button data-action="refresh-records" :disabled="records.status === 'loading'" :onClick="owned(loadPage, 'records', records.offset)">刷新记录</a-button>
        </template>
        <form aria-label="同意记录筛选" class="record-filter" @submit.prevent="ownedSubmitFilters">
          <label for="agreement-filter-device">设备 ID
            <input id="agreement-filter-device" v-model.trim="filter.deviceId" maxlength="36" placeholder="完整设备 ID" />
          </label>
          <label for="agreement-filter-user">用户 ID
            <input id="agreement-filter-user" v-model.trim="filter.userId" maxlength="10" inputmode="numeric" placeholder="完整用户 ID" :disabled="filter.unbound" />
          </label>
          <label for="agreement-filter-version">协议版本
            <input id="agreement-filter-version" v-model.trim="filter.version" maxlength="48" placeholder="当前发布版本" />
          </label>
          <label for="agreement-filter-client">客户端类型
            <select id="agreement-filter-client" v-model="filter.clientKind">
              <option value="">全部</option>
              <option v-for="[value, label] in clientKinds" :key="value" :value="value">{{ label }}</option>
            </select>
          </label>
          <label for="agreement-filter-accepted">同意情况
            <select id="agreement-filter-accepted" v-model="filter.accepted">
              <option value="">全部</option><option value="true">已同意</option><option value="false">尚未同意</option>
            </select>
          </label>
          <label for="agreement-filter-unbound" class="unbound-check">
            <input id="agreement-filter-unbound" v-model="filter.unbound" type="checkbox" />仅未绑定设备
          </label>
          <div class="row-actions">
            <a-button type="primary" html-type="submit" data-action="apply-record-filters" :disabled="!!filterError || records.status === 'loading'">查询</a-button>
            <a-button data-action="clear-record-filters" :disabled="records.status === 'loading'" :onClick="owned(clearFilters)">清除筛选</a-button>
          </div>
        </form>
        <p v-if="filterError" role="alert">{{ filterError }}</p>
        <p v-if="records.status === 'error'" role="alert">{{ records.message }}</p>
        <p v-else-if="records.status === 'loading'" role="status">正在读取同意记录…</p>
        <p v-else-if="records.message" role="status">{{ records.message }}</p>
        <p v-else-if="records.status === 'ready' && !records.rows.length" role="status">没有符合条件的同意记录。</p>
        <div v-if="records.rows.length" class="table-scroll" tabindex="0" aria-label="协议同意记录列表">
          <table>
            <thead><tr><th scope="col">设备 ID</th><th scope="col">用户 ID</th><th scope="col">客户端类型</th><th scope="col">协议版本</th><th scope="col">同意情况</th><th scope="col">同意时间</th><th scope="col">同意 IP</th></tr></thead>
            <tbody>
              <tr v-for="row in records.rows" :key="row.deviceId">
                <td><code>{{ row.deviceId }}</code></td><td>{{ row.userId ?? '未绑定' }}</td>
                <td>{{ clientKinds.get(row.clientKind) || row.clientKind || '未知' }}</td><td>{{ row.version }}</td>
                <td>{{ row.accepted ? '已同意' : '尚未同意' }}</td>
                <td><time v-if="row.acceptedAt" :datetime="row.acceptedAt">{{ displayTime(row.acceptedAt) }}</time><span v-else>—</span></td>
                <td><code>{{ row.ip || '—' }}</code></td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="records.offset || records.hasNext" class="paging" aria-label="同意记录分页">
          <a-button v-if="records.offset" data-action="previous-records" :disabled="records.status === 'loading'" :onClick="owned(turnPage, 'records', -1)">上一页</a-button>
          <span>第 {{ 1 + records.offset / PAGE_SIZE }} 页</span>
          <a-button v-if="records.hasNext" data-action="next-records" :onClick="owned(turnPage, 'records', 1)">下一页</a-button>
        </div>
      </ConsolePanel>

      <a-modal v-if="dialog" :key="dialog.key" :open="true" :title="titles[dialog.mode]"
        :width="720" :keyboard="!dialog.busy" :closable="!dialog.busy" :mask-closable="false" @cancel="closeDialog">
        <p v-if="dialog.error" role="alert" class="dialog-error">{{ dialog.error }}</p>
        <p v-if="dialog.loading" role="status">正在读取正文…</p>
        <a-button v-if="dialog.canReload" data-action="reload-detail" :onClick="owned(loadDetail, dialog)">
          {{ dialog.mode === 'edit' && dialog.locked ? '放弃本地改动并重新读取草稿' : '重新读取' }}
        </a-button>
        <form v-if="dialog.form" id="agreement-draft-form" aria-label="协议草稿编辑" class="draft-form" @submit.prevent="ownedSubmitDraft">
          <label for="agreement-version">协议版本（必填）
            <input id="agreement-version" v-model="dialog.form.version" required maxlength="48" autocomplete="off" :disabled="dialog.busy" />
          </label>
          <label for="agreement-summary">正文摘要（必填）
            <textarea id="agreement-summary" v-model="dialog.form.summary" required rows="3" :disabled="dialog.busy"></textarea>
          </label>
          <label for="agreement-body">协议正文（必填）
            <textarea id="agreement-body" v-model="dialog.form.body" required rows="9" :disabled="dialog.busy"></textarea>
          </label>
          <p v-if="draftError" role="status">{{ draftError }}</p>
        </form>
        <div v-if="dialog.detail && !dialog.form" class="agreement-detail">
          <dl>
            <dt>协议版本</dt><dd>{{ dialog.detail.version }}</dd>
            <dt>正文摘要</dt><dd class="summary">{{ dialog.detail.summary }}</dd>
            <template v-if="dialog.mode === 'release'">
              <dt>发布时间</dt><dd><time :datetime="dialog.detail.publishedAt">{{ displayTime(dialog.detail.publishedAt) }}</time></dd>
            </template>
            <template v-else>
              <dt>影响范围</dt><dd>全部设备（发布后需同意此版本）</dd>
              <dt>草稿修订</dt><dd>{{ dialog.detail.revision }}</dd>
            </template>
          </dl>
          <h3>协议正文（纯文本）</h3>
          <pre class="agreement-body">{{ dialog.detail.body }}</pre>
          <template v-if="dialog.mode === 'publish'">
            <p>确认后尝试发布；新发布的协议立即生效为当前版本，版本和正文不可修改。</p>
            <label for="agreement-reviewed" class="review-check">
              <input id="agreement-reviewed" v-model="dialog.reviewed" type="checkbox" :disabled="dialog.busy || dialog.locked" />
              我已核对版本、摘要、正文和影响范围。
            </label>
          </template>
        </div>
        <p v-if="dialog.mode === 'delete'">确认删除 {{ dialog.ids.length }} 个草稿{{ dialog.version ? '（' + dialog.version + '）' : '' }}？此操作不会删除已发布版本或同意记录。</p>
        <template #footer>
          <a-button data-action="cancel" :disabled="dialog.busy" :onClick="owned(closeDialog)">
            {{ ['preview', 'release'].includes(dialog.mode) ? '关闭' : '取消' }}
          </a-button>
          <a-button v-if="dialog.form" data-action="save" type="primary" html-type="submit" form="agreement-draft-form"
            :loading="dialog.busy" :disabled="!!draftError || dialog.locked || dialog.busy">保存草稿</a-button>
          <a-button v-if="dialog.mode === 'publish'" data-action="confirm-publish" type="primary" danger
            :loading="dialog.busy" :disabled="!dialog.detail || !dialog.reviewed || dialog.locked || dialog.busy" :onClick="owned(commitDialog, dialog)">确认发布</a-button>
          <a-button v-if="dialog.mode === 'delete'" data-action="confirm-delete" type="primary" danger
            :loading="dialog.busy" :disabled="dialog.locked || dialog.busy" :onClick="owned(commitDialog, dialog)">确认删除</a-button>
        </template>
      </a-modal>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref, shallowRef, watch } from 'vue'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import {
  listAgreementDrafts, getAgreementDraft, saveAgreementDraft, deleteAgreementDraft, deleteAgreementDrafts,
  publishAgreementDraft, listAgreementReleases, getAgreementRelease, listAgreementRecords,
} from '@/api/AgreementAdminApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity, useCurrentUserRequestScope } from '@/utils/currentUserScope'

defineOptions({ name: 'AgreementAdmin' })

const PAGE_SIZE = 25
const VERSION = /^[a-z0-9][a-z0-9._-]{0,47}$/
const clientKinds = new Map([['exam_client', 'EXE 考试模式'], ['extension', '浏览器扩展'], ['unknown', '未知']])
const titles = { create: '新建协议草稿', edit: '编辑协议草稿', preview: '预览协议草稿', publish: '审阅并发布用户协议', delete: '删除协议草稿', release: '查看已发布协议（只读）' }
const timeFormat = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium', timeZone: 'Asia/Shanghai' })
const utf8 = new TextEncoder()
function displayTime(value) {
  const date = new Date(value)
  return value && Number.isFinite(date.getTime()) ? timeFormat.format(date) : '—'
}
const newList = () => ({ rows: [], offset: 0, hasNext: false, status: '', message: '', requestId: 0 })
const drafts = reactive(newList()), releases = reactive(newList()), records = reactive(newList())
const lists = { drafts, releases, records }
const reads = { drafts: listAgreementDrafts, releases: listAgreementReleases, records: listAgreementRecords }
const readNames = { drafts: '草稿', releases: '发布版本', records: '同意记录' }
const currentRelease = shallowRef(null), headLoaded = ref(false), publishedDrafts = reactive(new Set())
const selected = ref([]), notice = ref(''), dialog = shallowRef(null), generation = ref(0)
let dialogKey = 0
const emptyFilter = () => ({ deviceId: '', userId: '', version: '', clientKind: '', accepted: '', unbound: false })
const filter = reactive(emptyFilter()), applied = shallowRef({})
const filterError = computed(() => {
  if (!filter.unbound && filter.userId && (!/^[1-9][0-9]*$/.test(filter.userId) || Number(filter.userId) > 2147483647)) return '用户 ID 必须为有效正整数。'
  if (filter.version && !VERSION.test(filter.version)) return '请输入有效的协议版本（小写字母、数字、点、下划线或连字符）。'
  return ''
})
const draftError = computed(() => {
  const form = dialog.value?.form
  if (!form) return ''
  if (!VERSION.test(form.version)) return '版本以小写字母或数字开头，最多 48 字符，仅可含小写字母、数字、点、下划线和连字符。'
  if (!form.summary.trim() || utf8.encode(form.summary).length > 8192) return '摘要不能为空，且不得超过 8 KiB（UTF-8）。'
  if (!form.body.trim() || utf8.encode(form.body).length > 256 * 1024) return '正文不能为空，且不得超过 256 KiB（UTF-8）。'
  return ''
})
const { currentUser } = useCurrentUserDisplay()
const logoutEpoch = getCurrentUserLogoutEpoch()
const authorized = computed(() => getCurrentUserLogoutEpoch() === logoutEpoch && Boolean(currentUserIdentity(currentUser.value)) &&
  isAdminUser(currentUser.value) && (currentUser.value?.status || 'enabled') === 'enabled')
function clear() {
  generation.value++
  for (const list of Object.values(lists)) Object.assign(list, newList())
  notice.value = ''; dialog.value = null; selected.value = []
  currentRelease.value = null; headLoaded.value = false; publishedDrafts.clear()
  Object.assign(filter, emptyFilter()); applied.value = {}
}
function open() {
  if (!authorized.value) return
  for (const type of Object.keys(lists)) loadPage(type, 0)
}
const scope = useCurrentUserRequestScope(currentUser, { clear, reset() {}, reload: open })
onMounted(open)
// The shared scope handles identity, role and logout. Invalidate status-only changes too.
watch(currentUser, (next, previous) => {
  if (currentUserIdentity(next) !== currentUserIdentity(previous) || isAdminUser(next) !== isAdminUser(previous) ||
      (next?.status || 'enabled') === (previous?.status || 'enabled')) return
  clear(); open()
}, { flush: 'sync' })
function capture() {
  const isOwner = scope.captureDataOwner(), version = generation.value
  return () => isOwner() && generation.value === version && authorized.value
}
function owned(fn, ...args) {
  const live = capture()
  return () => { if (live()) return fn(...args) }
}
const ownedSubmitFilters = computed(() => owned(submitFilters))
const ownedSubmitDraft = computed(() => owned(commitDialog, dialog.value))

async function loadPage(type, offset) {
  if (!authorized.value || !scope.canLoadForCurrentUser() || offset < 0 || offset > 1000000) return
  const list = lists[type], owner = capture(), requestId = ++list.requestId
  const live = () => owner() && list.requestId === requestId
  const params = { limit: PAGE_SIZE + 1, offset, ...(type === 'records' ? applied.value : {}) }
  Object.assign(list, { status: 'loading', message: '', rows: [], offset, hasNext: false })
  if (type === 'drafts') selected.value = []
  if (type === 'releases' && offset === 0) { currentRelease.value = null; headLoaded.value = false }
  try {
    const result = await reads[type](params)
    if (!live()) return
    if (type === 'records' && result?.status === 404 && result.data?.code === 'not_found') {
      list.status = 'ready'
      list.message = params.version ? '未找到指定的发布版本。' : '当前没有发布版本，暂无同意记录。'
      return
    }
    if (result?.status !== 200 || !Array.isArray(result.data)) {
      list.status = 'error'
      list.message = result?.status === 403 ? '当前账号无权读取' + readNames[type] + '。' : readNames[type] + '读取失败，请刷新重试。'
      return
    }
    if (type === 'releases') {
      // Publish appends one release and advances head in the same transaction;
      // a retry returning an existing release does not move head.
      for (const row of result.data) publishedDrafts.add(row.draftId)
      if (offset === 0) { currentRelease.value = result.data[0] ?? null; headLoaded.value = true }
    }
    list.rows = result.data.slice(0, PAGE_SIZE)
    list.hasNext = result.data.length > PAGE_SIZE && offset + PAGE_SIZE <= 1000000
    list.status = 'ready'
  } catch {
    if (live()) {
      list.status = 'error'
      list.message = readNames[type] + '读取失败，请刷新重试。'
    }
  }
}
function turnPage(type, direction) {
  const list = lists[type]
  if (list.status === 'loading' || (direction === 1 && !list.hasNext)) return
  loadPage(type, list.offset + direction * PAGE_SIZE)
}
function submitFilters() {
  if (filterError.value || records.status === 'loading') return
  const { unbound, ...query } = filter
  if (unbound) query.userId = 'unbound'
  applied.value = Object.fromEntries(Object.entries(query).filter(([, value]) => value !== ''))
  loadPage('records', 0)
}
function clearFilters() {
  Object.assign(filter, emptyFilter())
  submitFilters()
}

const validId = value => Number.isSafeInteger(value) && value > 0
const validContent = row => VERSION.test(row?.version || '') && typeof row.summary === 'string' && typeof row.body === 'string'
function openDialog(mode, id) {
  if (dialog.value || (mode !== 'create' && !validId(id))) return
  if (['edit', 'publish'].includes(mode) && publishedDrafts.has(id)) return
  const target = dialog.value = reactive({
    key: ++dialogKey, mode, id, detail: null, busy: false, error: '', reviewed: false,
    loading: false, locked: false, canReload: false,
    form: mode === 'create' ? { version: '', summary: '', body: '', revision: 0 } : null,
  })
  notice.value = ''
  if (mode !== 'create') loadDetail(target)
}
function openDelete(row) {
  if (dialog.value) return
  const ids = row ? [row.id] : drafts.rows.map(row => row.id).filter(id => selected.value.includes(id))
  if (!ids.length || !ids.every(validId)) return
  notice.value = ''
  dialog.value = reactive({ key: ++dialogKey, mode: 'delete', ids, batch: !row, version: row?.version, busy: false, locked: false, error: '' })
}
function closeDialog() {
  if (!dialog.value?.busy) dialog.value = null
}
async function loadDetail(target) {
  if (!authorized.value || dialog.value !== target || target.busy || target.loading) return
  const owner = capture(), live = () => owner() && dialog.value === target
  const isRelease = target.mode === 'release'
  target.loading = true; target.error = ''; target.canReload = false; target.locked = false
  target.detail = null; target.form = null; target.reviewed = false
  try {
    const result = await (isRelease ? getAgreementRelease(target.id) : getAgreementDraft(target.id))
    if (!live()) return
    const detail = isRelease ? result.data : result.data?.draft
    if (result.status !== 200 || detail?.id !== target.id || !validContent(detail) ||
        (isRelease ? !detail.publishedAt : !validId(detail.revision) || result.data.type !== 'user_agreement' || result.data.audience !== 'all_devices')) {
      target.error = result.status === 403 ? '当前账号无权读取协议。' : result.status === 404 ? '该协议已不存在；请刷新列表。' : '协议正文读取失败，请重试。'
      target.canReload = result.status !== 404
      return
    }
    target.detail = detail
    if (target.mode === 'edit') {
      const { version, summary, body, revision } = detail
      target.form = { version, summary, body, revision }
    }
  } catch {
    if (live()) { target.error = '协议正文读取失败，请重试。'; target.canReload = true }
  } finally {
    if (live()) target.loading = false
  }
}
const unknownMutation = '操作结果未确认。请取消当前操作，先刷新列表核对，勿重复提交。'
function lockUnconfirmed(target) {
  target.error = unknownMutation
  target.locked = true
}
async function commitDialog(target) {
  if (!authorized.value || !target || dialog.value !== target || target.busy || target.locked || target.loading) return
  const { mode, id, form, detail } = target
  const saving = mode === 'create' || mode === 'edit'
  if (saving ? !form || draftError.value : mode === 'publish' ? !detail || !target.reviewed : mode !== 'delete') return
  const owner = capture(), live = () => owner() && dialog.value === target
  const { ids, batch } = target
  const operation = saving ? '保存草稿' : mode === 'publish' ? '发布协议' : '删除草稿'
  target.busy = true; target.reviewed = false; target.error = ''
  try {
    const result = await (saving ? saveAgreementDraft(id, { ...form }) :
      mode === 'publish' ? publishAgreementDraft(id, detail.revision) :
        batch ? deleteAgreementDrafts(ids) : deleteAgreementDraft(ids[0]))
    if (!live()) return
    const { status, data } = result
    if (status !== 200) {
      if (status === 409) {
        target.error = '版本冲突：草稿修订已改变、协议已发布或版本号已使用。请核对后再操作。'
        target.locked = true; target.canReload = mode === 'edit' || mode === 'publish'
      } else if (status === 403) target.error = '当前账号无权' + operation + '。'
      else if (status === 404) { target.error = '目标草稿已不存在；请刷新列表。'; target.locked = true }
      else if (status === 400) target.error = '服务端未接受本次提交，请检查填写内容。'
      else lockUnconfirmed(target)
      return
    }
    const recorded = saving ? validId(data?.id) && (!id || id === data.id) && data.revision === form.revision + 1 :
      mode === 'publish' ? validId(data?.id) && data.draftId === id && data.revision === detail.revision &&
        data.version === detail.version && Boolean(data.publishedAt) :
        batch ? Array.isArray(data) && data.length === ids.length && ids.every(id => data.includes(id)) : data?.deleted === ids[0]
    if (!recorded) { lockUnconfirmed(target); return }
    notice.value = saving ? '草稿已保存。' : mode === 'publish' ? '协议 ' + data.version + ' 已发布。' : '已删除 ' + ids.length + ' 个草稿。'
    dialog.value = null
    loadPage('drafts', 0)
    if (mode === 'publish') {
      publishedDrafts.add(id)
      loadPage('releases', 0)
      if (!applied.value.version) loadPage('records', 0)
    }
  } catch {
    if (live()) lockUnconfirmed(target)
  } finally {
    if (live()) target.busy = false
  }
}
</script>

<style scoped>
.agreement-panels { display: grid; gap: 20px; min-width: 0; }
.record-filter, .row-actions, .paging { display: flex; flex-wrap: wrap; gap: 8px; }
.record-filter { align-items: start; gap: 12px; margin-bottom: 16px; }
.record-filter label { width: min(100%, 180px); }
.record-filter .row-actions { align-self: end; }
.record-filter label, .draft-form label { display: grid; gap: 4px; }
.current-release > p { color: var(--console-muted); }
.record-filter input:not([type='checkbox']), .record-filter select, .draft-form input, .draft-form textarea {
  width: 100%; padding: 6px 8px; color: var(--console-text); background: var(--console-surface);
  border: 1px solid var(--console-border); border-radius: 6px;
}
.record-filter .unbound-check { display: flex; align-items: center; align-self: end; width: auto; min-height: 32px; }
.draft-form { display: grid; gap: 12px; }
.table-scroll { overflow-x: auto; }
table { width: 100%; border-collapse: collapse; }
th, td { text-align: start; padding: 8px 12px; border-bottom: 1px solid var(--console-border); }
th { white-space: nowrap; }
.paging { align-items: center; justify-content: end; margin-top: 16px; }
.summary { overflow-wrap: anywhere; white-space: pre-wrap; min-width: 12ch; }
.agreement-detail dl { display: grid; grid-template-columns: auto 1fr; gap: 6px 12px; margin: 12px 0; }
.agreement-detail dd { margin: 0; overflow-wrap: anywhere; }
.agreement-body { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 40vh; overflow: auto; padding: 12px; border: 1px solid var(--console-border); border-radius: 6px; }
.review-check { display: inline-flex; align-items: start; gap: 8px; min-height: 32px; }
.review-check input { margin-top: 4px; }
.dialog-error { color: var(--ant-color-error, #b42318); }
.notice { margin: 0; }
</style>
