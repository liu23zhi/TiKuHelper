<template>
  <div class="console-page script-page">
    <a-alert v-if="!canUse()" type="warning" show-icon message="当前身份无法管理脚本，请重新登录。" />
    <div v-else :key="frameKey" ref="pageSurface" :onInputCapture="pageEntry.guardNative" :onChangeCapture="pageEntry.guardNative" :onClickCapture="pageEntry.guardNative" :onKeydownCapture="pageEntry.guardNative">
    <ConsolePageHeading title="脚本管理" description="为账号生成短指令；管理员负责上传 EXE，用户可维护自己的脚本参数。">
      <template #actions>
        <a-space wrap class="section-actions">
          <a-button v-if="isAdmin" :type="showAll ? 'primary' : 'default'" :onClick="pageEntry.bind(toggleScope)">
            {{ showAll ? '只看我的脚本' : '显示全部脚本' }}
          </a-button>
          <a-button :loading="loading" :onClick="pageEntry.bind(loadScripts)">刷新</a-button>
          <a-button type="primary" :onClick="pageEntry.bind(openCreate)">新建脚本指令</a-button>
        </a-space>
      </template>
    </ConsolePageHeading>

    <a-alert
      type="info"
      show-icon
      class="page-alert"
      message="每条脚本指令创建时预扣 ¥10"
      description="删除脚本时会幂等释放预扣金额；即使余额为负也允许删除，但欠费期间其它需要余额的功能可能会受限。普通答题可选择跟随题库或设置独立倍率；实际使用的 AI 模型与工具另行结算。"
    />

    <ConsolePanel v-if="isAdmin" title="管理员上传 EXE" description="上传后自动发布为最新版本；已有脚本会原子切换到新版本，正在下载的旧版本会继续完成。">
      <a-space wrap>
        <a-input :id="fieldId('uploadVersion')" :value="uploadVersion" :onUpdate:value="pageEntry.bind(setUploadVersion)" aria-label="EXE 版本号" placeholder="版本号，例如 1.0.0" style="width: 180px" :disabled="uploading" />
        <a-upload :show-upload-list="false" accept=".exe,application/vnd.microsoft.portable-executable" :custom-request="pageEntry.bind(handleUpload)" :disabled="uploading">
          <a-button type="primary" :loading="uploading">
            <UploadOutlined />上传 EXE
          </a-button>
        </a-upload>
        <span v-if="lastUploadKey" class="upload-result">最近上传版本：<code>{{ lastUploadKey }}</code></span>
      </a-space>
      <p class="hint">上传不会创建脚本。新建脚本默认引用当前版本；发布后所有未删除脚本都会自动使用最新 EXE。</p>
    </ConsolePanel>

    <ConsolePanel v-if="isAdmin" title="脚本可用域名" description="管理员可以限制用户创建脚本时可选择的域名，并控制是否允许泛域名短地址。留空域名列表表示兼容旧配置，允许任意域名。">
      <a-textarea :id="fieldId('domains')" :value="domainText" :onUpdate:value="pageEntry.bind(setDomainText)" aria-label="脚本允许域名" :rows="3" placeholder="每行一个域名，例如 example.com\na.example.net" :disabled="domainSaving || !domainReady" />
      <a-space wrap class="domain-settings-actions">
        <a-switch :checked="domainAllowWildcard" :onUpdate:checked="pageEntry.bind(setDomainWildcard)" aria-label="允许泛域名模式" :disabled="domainSaving || !domainReady" />
        <span class="switch-description">允许泛域名模式（a.example.com）</span>
        <a-button type="primary" :loading="domainSaving" :disabled="!domainReady" :onClick="pageEntry.bind(saveDomainSettings)">保存域名设置</a-button>
      </a-space>
    </ConsolePanel>

    <ConsoleSummaryGrid>
      <a-card :bordered="false" class="summary-card"><a-statistic title="脚本数" :value="summary.total" /></a-card>
      <a-card :bordered="false" class="summary-card"><a-statistic title="启用脚本" :value="summary.enabled" /></a-card>
      <a-card :bordered="false" class="summary-card"><a-statistic title="预扣金额" prefix="¥" :value="summary.reserved" :precision="6" /></a-card>
    </ConsoleSummaryGrid>

    <ConsolePanel title="脚本指令" :description="scopeDescription">
      <a-alert v-if="readError" type="error" show-icon :message="readError" class="page-alert" />
      <ConsoleTableShell label="脚本指令列表">
        <a-table bordered table-layout="fixed" :data-source="rows" :columns="columns" :loading="loading" :pagination="false" row-key="id" :scroll="{ x: tableScrollX }">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'name'">
              <div class="script-name-cell"><strong>{{ record.name }}</strong><a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag></div>
              <p v-if="record.description" class="table-hint">{{ record.description }}</p>
            </template>
            <span v-else-if="column.key === 'owner'">{{ record.ownerName || '-' }}</span>
            <div v-else-if="column.key === 'command'" class="command-cell">
              <code>{{ buildScriptDownloadURL(record) || '尚未配置短地址' }}</code>
              <span v-if="record.scheme === 'http'" class="table-hint">授权指令仅支持 HTTPS</span>
              <a-button size="small" :onClick="pageEntry.bind(copyCommand, record)" :disabled="!mayCopyCommand(record)">复制授权指令</a-button>
            </div>
            <div v-else-if="column.key === 'billing'">
              <span>{{ buildScriptBillingText(record) }}</span>
              <p class="table-hint">创建时预扣 ¥10，删除时释放</p>
            </div>
            <span v-else-if="column.key === 'preset'">{{ Object.keys(record.presetParams || {}).length }} 个</span>
            <span v-else-if="column.key === 'updatedAt'">{{ formatDateTime(record.updatedAt) }}</span>
            <a-space v-else-if="column.key === 'operation'" wrap>
              <a-button type="link" style="padding: 0" :onClick="pageEntry.bind(openEdit, record)">编辑</a-button>
              <a-popconfirm title="删除脚本并释放预扣金额？" description="欠费时仍允许删除；删除后会释放这条脚本的预扣。" ok-text="删除" cancel-text="取消" :onConfirm="pageEntry.bind(handleDelete, record)">
                <a-button type="link" danger style="padding: 0" :disabled="deleting || saving">删除</a-button>
              </a-popconfirm>
            </a-space>
            <template v-else>{{ record[column.dataIndex] ?? '-' }}</template>
          </template>
          <template #emptyText><a-empty :description="listReady ? '暂无脚本指令' : '脚本列表尚未读取成功'" /></template>
        </a-table>
      </ConsoleTableShell>
    </ConsolePanel>

    <a-modal v-if="modalOpen" :key="editorKey" :open="modalOpen" :title="editing ? '编辑脚本指令' : '新建脚本指令'" width="760px" ok-text="保存" cancel-text="取消" :confirm-loading="saving" :closable="!saving" :mask-closable="!saving" :keyboard="!saving" :cancel-button-props="{ disabled: saving }" :onOk="editorEntry.bind(handleSubmit)" :onCancel="editorEntry.bind(closeModal)" :wrap-props="{ 'aria-modal': true }">
      <div ref="modalSurface" :onInputCapture="editorEntry.guardNative" :onChangeCapture="editorEntry.guardNative" :onClickCapture="editorEntry.guardNative" :onKeydownCapture="editorEntry.guardNative">
      <a-form layout="vertical" :model="form" :disabled="saving">
        <div class="form-grid">
          <a-form-item label="脚本名称" :html-for="fieldId('name')" required>
            <a-input :id="fieldId('name')" :value="form.name" :onUpdate:value="editorEntry.bind(setFormField, 'name')" :maxlength="80" placeholder="例如：超星答题助手" />
          </a-form-item>
          <a-form-item label="脚本域名" :html-for="fieldId('baseDomain')" required>
            <a-input :id="fieldId('baseDomain')" :value="form.baseDomain" :onUpdate:value="editorEntry.bind(setFormField, 'baseDomain')" placeholder="例如 example.com" />
          </a-form-item>
          <a-form-item label="协议" :html-for="fieldId('scheme')">
            <a-select :id="fieldId('scheme')" :value="form.scheme" :onUpdate:value="editorEntry.bind(setFormField, 'scheme')"><a-select-option value="https">HTTPS</a-select-option><a-select-option value="http">HTTP（仅兼容旧环境）</a-select-option></a-select>
          </a-form-item>
        </div>
        <a-form-item label="泛域名模式" :html-for="fieldId('useWildcardDomain')">
          <a-switch :id="fieldId('useWildcardDomain')" :checked="form.useWildcardDomain" :onUpdate:checked="editorEntry.bind(setFormField, 'useWildcardDomain')" />
          <span class="switch-description">{{ form.useWildcardDomain ? '优先使用尽可能短的 a.example.com' : '使用 a.example.com/a 兼容非泛域名部署' }}</span>
          <div class="hint">短标识由服务端按最短可用长度自动分配。</div>
        </a-form-item>
        <a-form-item label="EXE 版本" :html-for="fieldId('versionId')" required>
          <a-select :id="fieldId('versionId')" :value="form.versionId" :onUpdate:value="editorEntry.bind(setFormField, 'versionId')" allow-clear disabled :placeholder="editing ? '跟随当前发布版本' : '请先上传 EXE'">
            <a-select-option v-for="version in versions" :key="version.id" :value="version.id">
              {{ version.version }} · {{ version.filename || version.fileName || 'script.exe' }}
            </a-select-option>
          </a-select>
          <div class="hint">此版本由服务端维护。新建脚本使用当前发布版本，管理员上传新 EXE 后会自动切换所有未删除脚本。</div>
        </a-form-item>
        <a-form-item label="说明" :html-for="fieldId('description')"><a-textarea :id="fieldId('description')" :value="form.description" :onUpdate:value="editorEntry.bind(setFormField, 'description')" :rows="2" placeholder="可选，说明用途或适用客户端" /></a-form-item>
        <a-form-item label="预置参数" :html-for="fieldId('presetText')">
          <a-textarea :id="fieldId('presetText')" :value="form.presetText" :onUpdate:value="editorEntry.bind(setFormField, 'presetText')" :rows="4" placeholder="每行一个 KEY=VALUE，例如\nkey=your-api-key\ncourseId=123" />
          <div class="hint">参数由服务端在脚本执行入口注入；此处只保存键和值，不会写进 PowerShell 命令文本。</div>
        </a-form-item>
        <a-form-item label="启用状态" :html-for="fieldId('enabled')"><a-switch :id="fieldId('enabled')" :checked="form.enabled" :onUpdate:checked="editorEntry.bind(setFormField, 'enabled')" /></a-form-item>
        <ConsolePanel title="脚本答题计费" description="此设置仅用于获得普通答案时的脚本费率，不减免实际使用的 AI 模型及工具费用。" class="billing-panel">
          <a-form-item label="启用脚本答题计费" :html-for="fieldId('billingEnabled')">
            <a-switch :id="fieldId('billingEnabled')" :checked="form.billingEnabled" :onUpdate:checked="editorEntry.bind(setFormField, 'billingEnabled')" />
            <span class="switch-description">{{ form.billingEnabled ? '使用脚本费率' : '按原渠道费率' }}</span>
          </a-form-item>
          <a-form-item v-if="form.billingEnabled" label="跟随题库计费" :html-for="fieldId('billingUseTikuPrice')">
            <a-switch :id="fieldId('billingUseTikuPrice')" :checked="form.billingUseTikuPrice" :onUpdate:checked="editorEntry.bind(setFormField, 'billingUseTikuPrice')" />
            <span class="switch-description">{{ form.billingUseTikuPrice ? '使用题库当前标准（1X）' : '使用脚本独立倍率' }}</span>
          </a-form-item>
          <a-form-item v-if="form.billingEnabled && !form.billingUseTikuPrice" label="独立答题倍率（X）" :html-for="fieldId('priceMultiplier')">
            <a-input-number :id="fieldId('priceMultiplier')" :value="form.priceMultiplier" :onUpdate:value="editorEntry.bind(setFormField, 'priceMultiplier')" :min="0" :precision="6" :step="0.1" style="width: 220px" />
            <div class="hint">仅普通答题按题库当前标准乘以该倍率；0X 不影响 AI 模型和工具的实际费用。</div>
          </a-form-item>
        </ConsolePanel>
        <ConsolePanel title="指令预览" description="PowerShell + .NET WebClient 可覆盖 Windows 7 至 Windows 11。" class="preview-panel">
          <div class="command-preview"><code>{{ buildScriptDownloadURL(previewRecord) || '设置合法域名后生成' }}</code></div>
          <p class="hint">此处仅预览地址，不分配短标识。保存后可从列表复制实际授权指令；请勿分享指令。</p>
        </ConsolePanel>
      </a-form>
      </div>
    </a-modal>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, useId, watch } from 'vue'
import { onBeforeRouteLeave, useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import { UploadOutlined } from '@ant-design/icons-vue'
import { getCurrentUserLogoutEpoch, getStoredCurrentUser, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { useUnsavedDraft } from '@/utils/useUnsavedDraft'
import { createScript, deleteScript, getScripts, getScriptDomainSettings, getScriptVersions, updateScript, updateScriptDomainSettings, uploadScriptVersion } from '@/api/TikuComponentApi'
import { buildScriptBillingText, buildScriptCommand, buildScriptDownloadURL, buildScriptListParams, buildScriptSavePayload, formatPresetParams, normalizeScriptRecord, normalizeScriptRows } from '@/utils/scriptManagementHelpers.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

defineOptions({ name: 'ScriptManagement' })

const route = useRoute(), mountedPath = route.path
const { currentUser } = useCurrentUserDisplay()
const fieldPrefix = 'script-' + useId(), fieldId = field => fieldPrefix + '-' + field
const pageSurface = ref(null), modalSurface = ref(null), frameKey = ref(0), editorKey = ref(0), draining = ref(false)
const deleting = ref(false), listReady = ref(false), readError = ref('')
const domainReady = ref(false)
let alive = true, mounted = false, epoch = 0, readTicket = 0, domainRevision = 0, retry = null
const principal = () => {
  const user = currentUser.value, id = Number(user?.id)
  return Number.isSafeInteger(id) && id > 0 && !['disabled', '0'].includes(String(user?.status ?? 'enabled'))
    ? JSON.stringify([id, String(user.role || '').trim().toLowerCase(), user.status ?? '']) : ''
}
let observedPrincipal = principal(), observedLogout = getCurrentUserLogoutEpoch(), authorizedLogout = observedLogout
const canUse = () => alive && route.path === mountedPath && authorizedLogout === getCurrentUserLogoutEpoch() && Boolean(principal())
const canAct = () => canUse() && !draining.value
function captureOwner() {
  const ticket = epoch, identity = principal(), logout = getCurrentUserLogoutEpoch()
  return () => {
    getStoredCurrentUser()
    return canAct() && epoch === ticket && identity === principal() && logout === getCurrentUserLogoutEpoch()
  }
}
function makeEntry(extra = () => true) {
  const owns = captureOwner(), frame = frameKey.value
  const isCurrent = () => owns() && frame === frameKey.value && extra()
  return {
    isCurrent,
    guardNative(event) { if (!isCurrent()) { event.preventDefault(); event.stopImmediatePropagation() } },
    bind: (fn, ...args) => (...events) => isCurrent() ? fn(...args, ...events) : undefined,
  }
}
const pageEntry = computed(() => { frameKey.value; return makeEntry() })
const editorEntry = shallowRef(makeEntry(() => false))
function hidePrivate(element) {
  if (!(element instanceof HTMLElement)) return
  element.style.visibility = 'hidden'; element.setAttribute('aria-hidden', 'true')
  if (element.contains(document.activeElement)) document.activeElement.blur?.()
  element.querySelectorAll('input,textarea').forEach(node => { node.value = ''; node.defaultValue = ''; node.removeAttribute('value') })
  const texts = document.createTreeWalker(element, 4)
  while (texts.nextNode()) texts.currentNode.nodeValue = ''
}
const isAdmin = computed(() => isAdminUser(currentUser.value))
const showAll = ref(false)
const rows = ref([])
const loading = ref(false)
const uploading = ref(false)
const uploadVersion = ref('')
const versions = ref([])
const saving = ref(false)
const modalOpen = ref(false)
const editing = ref(false)
const lastUploadKey = ref('')
const summary = reactive({ total: 0, enabled: 0, reserved: 0 })
const domainText = ref('')
const domainAllowWildcard = ref(true)
const domainSaving = ref(false)

const emptyForm = () => ({ id: undefined, name: '', description: '', versionId: undefined, slug: 'a', scheme: 'https', baseDomain: '', useWildcardDomain: true, enabled: true, presetText: '', billingEnabled: true, billingUseTikuPrice: true, priceMultiplier: 1 })
const form = reactive(emptyForm())
const draft = useUnsavedDraft(form, { focusTriggerAfterClose: false })
const previewRecord = computed(() => normalizeScriptRecord({ ...form, presetParams: form.presetText }))
const scopeDescription = computed(() => isAdmin.value && showAll.value ? '当前显示全部用户的脚本；删除和编辑仍由服务端校验权限。' : '当前只显示自己的脚本。')
const columns = computed(() => [
  { title: '脚本', key: 'name', dataIndex: 'name', width: 220 },
  ...(isAdmin.value && showAll.value ? [{ title: '用户', key: 'owner', dataIndex: 'ownerName', width: 150 }] : []),
  { title: '短指令', key: 'command', dataIndex: 'command', width: 430 },
  { title: '预置参数', key: 'preset', dataIndex: 'preset', width: 100 },
  { title: '答题计费', key: 'billing', dataIndex: 'billing', width: 180 },
  { title: '更新时间', key: 'updatedAt', dataIndex: 'updatedAt', width: 150 },
  { title: '操作', key: 'operation', dataIndex: 'operation', width: 130 },
])
const tableScrollX = computed(() => sumTableColumnWidths(columns.value))

function resetForm(record) {
  const fields = emptyForm()
  if (record) {
    const normalized = normalizeScriptRecord(record)
    for (const name of Object.keys(fields)) if (Object.hasOwn(normalized, name)) fields[name] = normalized[name]
    fields.presetText = formatPresetParams(normalized.presetParams)
  }
  Object.assign(form, fields)
  draft.savedConfigSnapshot.value = draft.currentConfigSnapshot.value
}

function setFormField(name, value) { if (modalOpen.value && !saving.value && Object.hasOwn(form, name)) form[name] = value }
function setUploadVersion(value) { if (isAdmin.value && !uploading.value) uploadVersion.value = value }
function setDomainText(value) { if (isAdmin.value && !domainSaving.value) { domainRevision++; domainText.value = value } }
function setDomainWildcard(value) { if (isAdmin.value && !domainSaving.value) { domainRevision++; domainAllowWildcard.value = value } }

function startEditor(record) {
  if (!canAct() || modalOpen.value || saving.value || deleting.value) return
  editing.value = Boolean(record)
  resetForm(record)
  if (!record && versions.value[0]?.id) form.versionId = versions.value[0].id
  draft.savedConfigSnapshot.value = draft.currentConfigSnapshot.value
  const key = ++editorKey.value
  editorEntry.value = makeEntry(() => modalOpen.value && key === editorKey.value)
  modalOpen.value = true
}
function openCreate() { startEditor() }
function openEdit(record) { if (rows.value.includes(record)) startEditor(record) }
function retireEditor() {
  hidePrivate(modalSurface.value)
  editorKey.value += 1; editorEntry.value = makeEntry(() => false); modalOpen.value = false; saving.value = false; editing.value = false
  draft.cancelPendingLeave(); resetForm()
}
async function closeModal() {
  if (saving.value || !editorEntry.value.isCurrent()) return
  const owns = editorEntry.value.isCurrent
  if (await draft.requestRouteLeave({ path: mountedPath }) && owns()) retireEditor()
}

function extractSummary(response, dataRows) {
  const source = response?.data?.summary || {}
  summary.total = Number(source.total ?? dataRows.length) || 0
  summary.enabled = Number(source.enabled ?? dataRows.filter((row) => row.enabled).length) || 0
  summary.reserved = Number(source.reserved ?? source.reservedAmount ?? dataRows.reduce((sum, row) => sum + row.reservedFee, 0)) || 0
}

async function loadScripts() {
  if (!canAct()) return
  const owns = captureOwner(), ticket = ++readTicket
  const scope = buildScriptListParams(isAdmin.value, showAll.value)
  const current = () => owns() && ticket === readTicket
  loading.value = true; readError.value = ''
  try {
    const response = await getScripts(scope)
    if (!current()) return
    if (response?.status !== 200) { readError.value = '读取脚本列表失败，请重试。'; return }
    const normalized = normalizeScriptRows(response)
    rows.value = normalized
    extractSummary(response, normalized)
    listReady.value = true
  } catch {
    if (current()) readError.value = '读取脚本列表失败，请重试。'
  } finally {
    if (current()) loading.value = false
  }
}

function toggleScope() {
  if (!canAct() || !isAdmin.value || modalOpen.value || deleting.value) return
  showAll.value = !showAll.value
  rows.value = []; listReady.value = false
  Object.assign(summary, { total: 0, enabled: 0, reserved: 0 })
  return loadScripts()
}

async function handleUpload({ file, onSuccess, onError }) {
  if (!canAct() || !isAdmin.value || uploading.value) return
  const owns = captureOwner()
  if (!String(file?.name || '').toLowerCase().endsWith('.exe')) {
    const error = new Error('只允许上传 .exe 文件')
    onError?.(error)
    message.error(error.message)
    return
  }
  if (!String(uploadVersion.value || '').trim()) {
    const error = new Error('请先填写 EXE 版本号')
    onError?.(error)
    message.warning(error.message)
    return
  }
  uploading.value = true
  try {
    const response = await uploadScriptVersion(file, uploadVersion.value)
    if (!owns()) return
    if (response?.status !== 200) throw new Error('上传 EXE 失败，请刷新核对版本。')
    const data = response.data || {}
    const uploaded = { ...data, filename: data.filename || file.name }
    if (!uploaded.id) throw new Error('上传成功但服务端没有返回版本信息')
    versions.value = [uploaded, ...versions.value.filter((item) => item.id !== uploaded.id)]
    lastUploadKey.value = uploaded.version || uploaded.filename || ''
    if (!modalOpen.value && !saving.value) openCreate()
    onSuccess?.(data)
    message.success('EXE 上传成功，已发布为最新版本')
  } catch {
    if (owns()) {
      const error = new Error('上传 EXE 结果未确认，请刷新版本后再试。')
      onError?.(error); message.error(error.message)
    }
  } finally { if (owns()) uploading.value = false }
}

function createRetryKey(payload) {
  const signature = JSON.stringify(payload)
  if (retry?.signature === signature) return retry.key
  const bytes = new Uint8Array(16)
  globalThis.crypto.getRandomValues(bytes)
  const key = 'script-create-' + Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('')
  retry = { signature, key }
  return key
}

async function handleSubmit() {
  const owns = editorEntry.value.isCurrent
  if (!owns() || saving.value || deleting.value) return
  const payload = buildScriptSavePayload({ ...form, presetParams: form.presetText })
  if (!payload.name) return message.warning('请输入脚本名称')
  if (!payload.baseDomain || !buildScriptDownloadURL(payload)) return message.warning('请输入有效的脚本域名')
  if (!payload.versionId) return message.warning('请选择 EXE 版本')
  const rawMultiplier = Number(form.priceMultiplier)
  if (payload.billingEnabled && !payload.billingUseTikuPrice && (form.priceMultiplier === null || !Number.isFinite(rawMultiplier) || rawMultiplier < 0)) return message.warning('请输入有效的非负答题倍率')
  const isEditing = editing.value, sentSnapshot = draft.currentConfigSnapshot.value
  saving.value = true
  try {
    const response = isEditing ? await updateScript(payload) : await createScript(payload, createRetryKey(payload))
    if (!owns()) return
    if (response?.status !== 200) { message.error('保存脚本未完成，请刷新列表核对后重试。'); return }
    if (!isEditing) retry = null
    draft.savedConfigSnapshot.value = sentSnapshot
    const sameDraft = draft.currentConfigSnapshot.value === sentSnapshot
    if (sameDraft) retireEditor()
    message.success(isEditing ? '脚本已更新' : '脚本已创建，已预扣 ¥10')
    await loadScripts()
  } catch {
    if (owns()) message.error('保存结果未确认，请刷新核对；如重试，请保留原表单。')
  } finally { if (owns()) saving.value = false }
}

async function handleDelete(record) {
  if (!canAct() || !rows.value.includes(record) || deleting.value || saving.value) return
  const owns = captureOwner()
  deleting.value = true
  try {
    const response = await deleteScript({ id: record.id })
    if (!owns()) return
    if (response?.status !== 200) { message.error('删除脚本未完成，请刷新列表核对。'); return }
    message.success('脚本已删除，预扣金额已释放')
    await loadScripts()
  } catch {
    if (owns()) message.error('删除脚本结果未确认，请刷新列表核对。')
  } finally { if (owns()) deleting.value = false }
}

async function copyText(value) {
  if (!value || !canAct()) return
  const owns = captureOwner()
  try {
    if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(value)
    else {
      const input = document.createElement('textarea'), opener = document.activeElement
      input.value = value; input.style.position = 'fixed'; input.style.opacity = '0'
      input.setAttribute('aria-hidden', 'true'); input.tabIndex = -1
      document.body.appendChild(input)
      try {
        input.focus(); input.select()
        if (!document.execCommand('copy')) throw new Error('Clipboard copy unavailable')
      } finally {
        input.value = ''; input.remove()
        if (owns() && opener instanceof HTMLElement && opener.isConnected) opener.focus({ preventScroll: true })
      }
    }
    if (owns()) message.success('指令已复制')
  } catch {
    if (owns()) message.error('复制失败，请手动选择指令')
  }
}

function mayCopyCommand(record) {
  return String(record.ownerId ?? '') === String(currentUser.value?.id ?? '')
    && Boolean(currentUser.value?.id) && record.scheme === 'https' && Boolean(buildScriptCommand(record))
}
function copyCommand(record) {
  if (rows.value.includes(record) && mayCopyCommand(record)) return copyText(buildScriptCommand(record))
}
function formatDateTime(value) {
  if (!value) return '-'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString('zh-CN', { hour12: false })
}

async function loadVersions() {
  if (!canAct()) return
  const owns = captureOwner()
  try {
    const response = await getScriptVersions()
    if (!owns()) return
    if (response?.status !== 200) { message.warning('EXE 版本未读取成功，请刷新页面。'); return }
    versions.value = Array.isArray(response.data) ? response.data : (response.data?.items || [])
    if (modalOpen.value && !editing.value && !form.versionId && versions.value[0]?.id) {
      const unchanged = !draft.hasUnsavedChanges.value
      form.versionId = versions.value[0].id
      if (unchanged) draft.savedConfigSnapshot.value = draft.currentConfigSnapshot.value
    }
  } catch {
    if (owns()) message.warning('EXE 版本未读取成功，请刷新页面。')
  }
}

async function loadDomainSettings() {
  if (!canAct() || !isAdmin.value) return
  const owns = captureOwner(), ticket = domainRevision
  try {
    const response = await getScriptDomainSettings()
    if (!owns() || ticket !== domainRevision) return
    if (response?.status !== 200) { message.warning('域名设置未读取成功，请刷新页面。'); return }
    domainText.value = Array.isArray(response.data?.domains) ? response.data.domains.join('\n') : ''
    domainAllowWildcard.value = response.data?.allowWildcard !== false
    domainReady.value = true
  } catch {
    if (owns() && ticket === domainRevision) message.warning('域名设置未读取成功，请刷新页面。')
  }
}

async function saveDomainSettings() {
  if (!canAct() || !isAdmin.value || !domainReady.value || domainSaving.value) return
  const owns = captureOwner(), ticket = ++domainRevision
  domainSaving.value = true
  try {
    const domains = domainText.value.split(/\r?\n/).map(value => value.trim()).filter(Boolean)
    const response = await updateScriptDomainSettings({ domains, allowWildcard: domainAllowWildcard.value })
    if (!owns() || ticket !== domainRevision) return
    if (response?.status !== 200) { message.error('保存域名设置未完成，请刷新核对。'); return }
    domainText.value = Array.isArray(response.data?.domains) ? response.data.domains.join('\n') : domainText.value
    domainAllowWildcard.value = response.data?.allowWildcard !== false
    message.success('脚本域名设置已保存')
  } catch {
    if (owns() && ticket === domainRevision) message.error('域名设置保存结果未确认，请刷新核对。')
  } finally { if (owns() && ticket === domainRevision) domainSaving.value = false }
}

function startReads() {
  if (!canAct()) return
  void loadVersions(); void loadDomainSettings(); void loadScripts()
}
function retireScope() {
  const ticket = ++epoch
  readTicket += 1; domainRevision += 1; retry = null; draining.value = true
  hidePrivate(pageSurface.value); retireEditor()
  showAll.value = false; rows.value = []; versions.value = []
  Object.assign(summary, { total: 0, enabled: 0, reserved: 0 })
  readError.value = ''; listReady.value = false; loading.value = false; deleting.value = false
  domainText.value = ''; domainAllowWildcard.value = true; domainReady.value = false; domainSaving.value = false
  lastUploadKey.value = ''; uploadVersion.value = ''; uploading.value = false
  void nextTick().then(() => nextTick()).then(() => {
    if (!alive || epoch !== ticket) return
    frameKey.value += 1; draining.value = false
    if (mounted) startReads()
  })
}

watch(() => [principal(), getCurrentUserLogoutEpoch()], ([next, logout]) => {
  if (next === observedPrincipal && logout === observedLogout) return
  if (logout === observedLogout && next && next !== observedPrincipal) authorizedLogout = logout
  observedPrincipal = next; observedLogout = logout
  retireScope()
}, { flush: 'sync' })
watch(() => route.fullPath, retireScope, { flush: 'sync' })
onBeforeRouteLeave(to => to.path === '/login' || !modalOpen.value || (!saving.value && draft.requestRouteLeave(to)))
onMounted(() => { mounted = true; startReads() })
onBeforeUnmount(() => { alive = false; mounted = false; retireScope() })
</script>

<style scoped>
.page-alert { margin-bottom: 20px; }
.hint, .table-hint { margin: 8px 0 0; color: var(--console-muted); font-size: 12px; line-height: 1.7; overflow-wrap: anywhere; }
.upload-result { max-width: 100%; overflow-wrap: anywhere; }
.domain-settings-actions { margin-top: 12px; }
.script-name-cell, .command-cell { display: flex; align-items: center; gap: 8px; min-width: 0; }
.script-name-cell { flex-wrap: wrap; }
.command-cell { align-items: flex-start; flex-direction: column; }
.command-cell code, .command-preview code { display: block; width: 100%; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--console-text); }
.form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 16px; }
.switch-description { margin-left: 10px; color: var(--console-muted); }
.billing-panel, .preview-panel { margin-top: 16px; }
.command-preview { display: flex; align-items: flex-start; gap: 12px; padding: 12px; border: 1px solid var(--console-border); border-radius: 8px; background: var(--console-surface-muted, #f8faf9); }
@media (max-width: 700px) { .form-grid { grid-template-columns: 1fr; } .command-preview { flex-direction: column; } }
</style>
