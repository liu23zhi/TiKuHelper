<template>
  <div class="console-page script-page">
    <ConsolePageHeading title="脚本管理" description="为账号生成短指令；管理员负责上传 EXE，用户可维护自己的脚本参数。">
      <template #actions>
        <a-space wrap class="section-actions">
          <a-button v-if="isAdmin" :type="showAll ? 'primary' : 'default'" @click="toggleScope">
            {{ showAll ? '只看我的脚本' : '显示全部脚本' }}
          </a-button>
          <a-button :loading="loading" @click="loadScripts">刷新</a-button>
          <a-button type="primary" @click="openCreate">新建脚本指令</a-button>
        </a-space>
      </template>
    </ConsolePageHeading>

    <a-alert
      type="info"
      show-icon
      class="page-alert"
      message="每条脚本指令创建时预扣 ¥10"
      description="删除脚本时会幂等释放预扣金额；即使余额为负也允许删除，但欠费期间其它需要余额的功能可能会受限。脚本答题计费可以选择跟随题库或使用独立单价。"
    />

    <ConsolePanel v-if="isAdmin" title="管理员上传 EXE" description="上传可复用的 EXE 版本，用户创建脚本指令时选择版本。">
      <a-space wrap>
        <a-input v-model:value="uploadVersion" placeholder="版本号，例如 1.0.0" style="width: 180px" :disabled="uploading" />
        <a-upload :show-upload-list="false" accept=".exe,application/vnd.microsoft.portable-executable" :custom-request="handleUpload" :disabled="uploading">
          <a-button type="primary" :loading="uploading">
            <UploadOutlined />上传 EXE
          </a-button>
        </a-upload>
        <span v-if="lastUploadKey" class="upload-result">最近上传版本：<code>{{ lastUploadKey }}</code></span>
      </a-space>
      <p class="hint">上传不会自动创建脚本；用户可以在脚本设置中选择已上传版本。</p>
    </ConsolePanel>

    <ConsoleSummaryGrid>
      <a-card :bordered="false" class="summary-card"><a-statistic title="脚本数" :value="summary.total" /></a-card>
      <a-card :bordered="false" class="summary-card"><a-statistic title="启用脚本" :value="summary.enabled" /></a-card>
      <a-card :bordered="false" class="summary-card"><a-statistic title="预扣金额" prefix="¥" :value="summary.reserved" :precision="6" /></a-card>
    </ConsoleSummaryGrid>

    <ConsolePanel title="脚本指令" :description="scopeDescription">
      <ConsoleTableShell label="脚本指令列表">
        <a-table bordered table-layout="fixed" :data-source="rows" :columns="columns" :loading="loading" :pagination="false" row-key="id" :scroll="{ x: tableScrollX }">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'name'">
              <div class="script-name-cell"><strong>{{ record.name }}</strong><a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag></div>
              <p v-if="record.description" class="table-hint">{{ record.description }}</p>
            </template>
            <span v-else-if="column.key === 'owner'">{{ record.ownerName || '-' }}</span>
            <div v-else-if="column.key === 'command'" class="command-cell">
              <code>{{ buildScriptCommand(record) || '请先设置域名和短标识' }}</code>
              <a-button size="small" @click="copyCommand(record)" :disabled="!buildScriptCommand(record)">复制</a-button>
            </div>
            <div v-else-if="column.key === 'billing'">
              <span>{{ buildScriptBillingText(record) }}</span>
              <p class="table-hint">创建时预扣 ¥10，删除时释放</p>
            </div>
            <span v-else-if="column.key === 'preset'">{{ Object.keys(record.presetParams || {}).length }} 个</span>
            <span v-else-if="column.key === 'updatedAt'">{{ formatDateTime(record.updatedAt) }}</span>
            <a-space v-else-if="column.key === 'operation'" wrap>
              <a-button type="link" style="padding: 0" @click="openEdit(record)">编辑</a-button>
              <a-popconfirm title="删除脚本并释放预扣金额？" description="欠费时仍允许删除；删除后会释放这条脚本的预扣。" ok-text="删除" cancel-text="取消" @confirm="handleDelete(record)">
                <a-button type="link" danger style="padding: 0">删除</a-button>
              </a-popconfirm>
            </a-space>
            <template v-else>{{ record[column.dataIndex] ?? '-' }}</template>
          </template>
          <template #emptyText><a-empty description="暂无脚本指令" /></template>
        </a-table>
      </ConsoleTableShell>
    </ConsolePanel>

    <a-modal v-model:open="modalOpen" :title="editing ? '编辑脚本指令' : '新建脚本指令'" width="760px" ok-text="保存" cancel-text="取消" :confirm-loading="saving" @ok="handleSubmit" @cancel="closeModal">
      <a-form layout="vertical" :model="form">
        <div class="form-grid">
          <a-form-item label="脚本名称" required>
            <a-input v-model:value="form.name" maxlength="80" placeholder="例如：超星答题助手" />
          </a-form-item>
          <a-form-item label="脚本域名" required>
            <a-input v-model:value="form.baseDomain" placeholder="例如 example.com" />
          </a-form-item>
          <a-form-item label="协议">
            <a-select v-model:value="form.scheme"><a-select-option value="https">HTTPS</a-select-option><a-select-option value="http">HTTP（仅兼容旧环境）</a-select-option></a-select>
          </a-form-item>
        </div>
        <a-form-item label="泛域名模式">
          <a-switch v-model:checked="form.useWildcardDomain" />
          <span class="switch-description">{{ form.useWildcardDomain ? '优先使用尽可能短的 a.example.com' : '使用 a.example.com/a 兼容非泛域名部署' }}</span>
          <div class="hint">短标识由服务端按最短可用长度自动分配。</div>
        </a-form-item>
        <a-form-item label="EXE 版本" required>
          <a-select v-model:value="form.versionId" allow-clear :disabled="editing" :placeholder="editing ? '版本在创建时绑定' : '选择管理员上传的 EXE 版本'">
            <a-select-option v-for="version in versions" :key="version.id" :value="version.id">
              {{ version.version }} · {{ version.filename || version.fileName || 'script.exe' }}
            </a-select-option>
          </a-select>
          <div class="hint">管理员上传 EXE 后，所有账号都可以选择已发布版本创建自己的指令；已有指令的 EXE 版本在创建时绑定。</div>
        </a-form-item>
        <a-form-item label="说明"><a-textarea v-model:value="form.description" :rows="2" placeholder="可选，说明用途或适用客户端" /></a-form-item>
        <a-form-item label="预置参数">
          <a-textarea v-model:value="form.presetText" :rows="4" placeholder="每行一个 KEY=VALUE，例如\nkey=your-api-key\ncourseId=123" />
          <div class="hint">参数由服务端在脚本执行入口注入；此处只保存键和值，不会写进 PowerShell 命令文本。</div>
        </a-form-item>
        <a-form-item label="启用状态"><a-switch v-model:checked="form.enabled" /></a-form-item>
        <ConsolePanel title="脚本答题计费" description="脚本答题计费与题库计费独立保存；可直接选择跟随题库。" class="billing-panel">
          <a-form-item label="启用脚本答题计费">
            <a-switch v-model:checked="form.billingEnabled" />
            <span class="switch-description">{{ form.billingEnabled ? '启用' : '停用' }}</span>
          </a-form-item>
          <a-form-item v-if="form.billingEnabled" label="跟随题库计费">
            <a-switch v-model:checked="form.billingUseTikuPrice" />
            <span class="switch-description">{{ form.billingUseTikuPrice ? '使用题库当前标准' : '使用脚本独立单价' }}</span>
          </a-form-item>
          <a-form-item v-if="form.billingEnabled && !form.billingUseTikuPrice" label="独立答题单价（元 / 题）">
            <a-input-number v-model:value="form.answerPrice" :min="0" :precision="6" :step="0.01" style="width: 220px" />
          </a-form-item>
        </ConsolePanel>
        <ConsolePanel title="指令预览" description="PowerShell + .NET WebClient 可覆盖 Windows 7 至 Windows 11。" class="preview-panel">
          <div class="command-preview"><code>{{ buildScriptCommand(previewRecord) || '设置域名和短标识后生成' }}</code><a-button size="small" :disabled="!buildScriptCommand(previewRecord)" @click="copyCommand(previewRecord)">复制</a-button></div>
        </ConsolePanel>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { UploadOutlined } from '@ant-design/icons-vue'
import { getStoredCurrentUser, isAdminUser } from '@/utils/currentUser'
import { createScript, deleteScript, getScripts, getScriptVersions, updateScript, uploadScriptVersion } from '@/api/TikuComponentApi'
import { buildScriptBillingText, buildScriptCommand, buildScriptListParams, buildScriptSavePayload, formatPresetParams, normalizeScriptRecord, normalizeScriptRows } from '@/utils/scriptManagementHelpers.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

defineOptions({ name: 'ScriptManagement' })

const currentUser = ref(getStoredCurrentUser())
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

const emptyForm = () => ({ id: undefined, name: '', description: '', versionId: undefined, slug: 'a', scheme: 'https', baseDomain: '', useWildcardDomain: true, enabled: true, presetText: '', billingEnabled: true, billingUseTikuPrice: true, answerPrice: 0 })
const form = reactive(emptyForm())
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
  Object.assign(form, emptyForm())
  if (record) {
    const normalized = normalizeScriptRecord(record)
    Object.assign(form, normalized, { presetText: formatPresetParams(normalized.presetParams) })
  }
}

function openCreate() {
  editing.value = false
  resetForm()
  modalOpen.value = true
}

function openEdit(record) {
  editing.value = true
  resetForm(record)
  modalOpen.value = true
}

function closeModal() {
  if (!saving.value) modalOpen.value = false
}

function extractSummary(response, dataRows) {
  const source = response?.data?.summary || {}
  summary.total = Number(source.total ?? dataRows.length) || 0
  summary.enabled = Number(source.enabled ?? dataRows.filter((row) => row.enabled).length) || 0
  summary.reserved = Number(source.reserved ?? source.reservedAmount ?? dataRows.reduce((sum, row) => sum + row.reservedFee, 0)) || 0
}

async function loadScripts() {
  loading.value = true
  try {
    const response = await getScripts(buildScriptListParams(isAdmin.value, showAll.value))
    if (response?.status !== 200) throw new Error(response?.message || '获取脚本列表失败')
    const normalized = normalizeScriptRows(response)
    rows.value = normalized
    extractSummary(response, normalized)
  } catch (error) {
    message.error(error?.message || '获取脚本列表失败')
  } finally {
    loading.value = false
  }
}

function toggleScope() {
  showAll.value = !showAll.value
  loadScripts()
}

async function handleUpload({ file, onSuccess, onError }) {
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
    if (response?.status !== 200) throw new Error(response?.message || '上传 EXE 失败')
    const data = response.data || {}
    const uploaded = { ...data, filename: data.filename || file.name }
    if (!uploaded.id) throw new Error('上传成功但服务端没有返回版本信息')
    versions.value = [uploaded, ...versions.value.filter((item) => item.id !== uploaded.id)]
    lastUploadKey.value = uploaded.version || uploaded.filename || ''
    openCreate()
    form.versionId = uploaded.id
    onSuccess?.(data)
    message.success('EXE 上传成功，已填入新脚本表单')
  } catch (error) {
    onError?.(error)
    message.error(error?.message || '上传 EXE 失败')
  } finally {
    uploading.value = false
  }
}

async function handleSubmit() {
  const payload = buildScriptSavePayload({ ...form, presetParams: form.presetText })
  if (!payload.name) return message.warning('请输入脚本名称')
  if (!payload.baseDomain) return message.warning('请输入脚本域名')
  if (!payload.versionId) return message.warning('请选择 EXE 版本')
  if (payload.billingEnabled && !payload.billingUseTikuPrice && payload.answerPrice < 0) return message.warning('独立答题单价不能小于 0')
  saving.value = true
  try {
    const response = editing.value ? await updateScript(payload) : await createScript(payload)
    if (response?.status !== 200) throw new Error(response?.message || '保存脚本失败')
    message.success(editing.value ? '脚本已更新' : '脚本已创建，已预扣 ¥10')
    modalOpen.value = false
    await loadScripts()
  } catch (error) {
    message.error(error?.message || '保存脚本失败')
  } finally {
    saving.value = false
  }
}

async function handleDelete(record) {
  try {
    const response = await deleteScript({ id: record.id })
    if (response?.status !== 200) throw new Error(response?.message || '删除脚本失败')
    message.success('脚本已删除，预扣金额已释放')
    await loadScripts()
  } catch (error) {
    message.error(error?.message || '删除脚本失败')
  }
}

async function copyText(value) {
  if (!value) return
  try {
    if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(value)
    else {
      const input = document.createElement('textarea')
      input.value = value; input.style.position = 'fixed'; input.style.opacity = '0'
      document.body.appendChild(input); input.focus(); input.select(); document.execCommand('copy'); input.remove()
    }
    message.success('指令已复制')
  } catch (_) {
    message.error('复制失败，请手动选择指令')
  }
}

function copyCommand(record) { return copyText(buildScriptCommand(record)) }
function formatDateTime(value) {
  if (!value) return '-'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString('zh-CN', { hour12: false })
}

async function loadVersions() {
  try {
    const response = await getScriptVersions()
    if (response?.status === 200) versions.value = Array.isArray(response.data) ? response.data : (response.data?.items || [])
  } catch (_) {
    // The list page remains usable when version metadata is temporarily unavailable.
  }
}

onMounted(() => {
  loadVersions()
  loadScripts()
})
</script>

<style scoped>
.page-alert { margin-bottom: 20px; }
.hint, .table-hint { margin: 8px 0 0; color: var(--console-muted); font-size: 12px; line-height: 1.7; overflow-wrap: anywhere; }
.upload-result { max-width: 100%; overflow-wrap: anywhere; }
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
