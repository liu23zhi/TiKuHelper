<template>
  <div class="console-page user-exam-tool-page">
    <ConsolePageHeading title="客户端数据库工具" description="考试客户端本地库的在线解密预览与脱机工具。仅管理员可用。" />

    <a-alert
      type="info"
      show-icon
      class="page-alert"
      message="可直接上传加密的 UserExamInfo.db，服务器会解密后只读预览"
      description="明文库也能直接上传。加密库会先用下方配置的密钥解密；解密只在内存与临时文件里进行，请求结束即删除，不落盘留存。不想把库上传到服务器时，也可以下载脱机工具在本机解密。"
    />

    <a-alert v-if="!canManage()" type="warning" show-icon message="当前身份无该页面权限，请使用管理员账号登录。" />

    <template v-if="canManage()">

      <ConsolePanel title="脱机工具（可选）" description="单文件 exe，无需安装 Python 运行库。用于不想上传数据库的场景。">
        <a-descriptions :column="1" size="small" bordered class="tool-descriptions">
          <a-descriptions-item label="状态">
            <a-tag :color="version.available ? 'green' : 'default'">{{ version.available ? '已部署' : '未部署' }}</a-tag>
            <span v-if="!version.available && version.reason" class="hint"> {{ version.reason }}</span>
          </a-descriptions-item>
          <a-descriptions-item label="文件名">{{ version.filename || '—' }}</a-descriptions-item>
          <a-descriptions-item label="版本">{{ version.version || '—' }}</a-descriptions-item>
          <a-descriptions-item label="大小">{{ formatSize(version.sizeBytes) }}</a-descriptions-item>
          <a-descriptions-item label="SHA-256">
            <code class="hash-value">{{ version.sha256 || '—' }}</code>
          </a-descriptions-item>
          <a-descriptions-item label="加密参数">
            {{ version.cipher || '—' }} / legacy={{ version.legacy || '—' }} / kdf_iter={{ version.kdfIter || '—' }}
          </a-descriptions-item>
        </a-descriptions>
        <template #actions>
          <a-space wrap>
            <a-button :loading="loadingVersion" :onClick="loadVersion">刷新状态</a-button>
            <a-button type="primary" :disabled="!version.available" :href="downloadURL">下载工具</a-button>
          </a-space>
        </template>
      </ConsolePanel>

      <ConsolePanel title="使用说明" description="两条路：直接上传（服务端解密）或用脱机工具在本机处理。">
        <p class="hint usage-lead">
          <strong>推荐：直接上传。</strong>把考试客户端的 <code>UserExamInfo.db</code>（加密或明文都行）上传到下面的只读预览即可，
          服务端会用上面的密钥自动解密。明文只在临时文件里存在，请求结束即删除。
        </p>
        <p class="hint usage-lead"><strong>或用脱机工具（不上传数据库）：</strong></p>
        <ol class="usage-steps">
          <li>
            下载上面的 <code>userexam-crypto.exe</code>（单文件，约 15 MB，目标机器<strong>无需</strong>安装 Python）。
          </li>
          <li>
            把口令写进 exe 同目录的 <code>password.txt</code>（一行），或设置环境变量
            <code>USEREXAM_PASSWORD</code>。口令不随 exe 分发。
          </li>
          <li>
            双击 exe 打开图形界面，选择考试客户端的 <code>UserExamInfo.db</code>：普通库自动加密，加密库自动解密；
            转换结果写成同目录的 <code>*.decrypted.db</code> / <code>*.encrypted.db</code>，源文件保持只读不被修改。
          </li>
          <li>
            需要批量或无人值守时用命令行：
            <code>userexam-crypto.exe -i ".\UserExamInfo.db" --json</code>；
            只查内容用 <code>--inspect -i ".\UserExamInfo.decrypted.db"</code>；
            验证随包运行库是否可用用 <code>--selftest</code>。
          </li>
        </ol>
        <p class="hint">
          加密参数固定为 wxSQLite3 AES-256-CBC（legacy=0 / kdf_iter=4001）。工具不是通用解密器：
          口令或格式不匹配时会报错并取消输出，不会生成半成品文件。
        </p>
      </ConsolePanel>

      <ConsolePanel title="只读预览" description="上传 UserExamInfo.db（加密或明文均可）；服务端只读解析 baseInfo 与 macAddress，不落盘留存。">
        <!-- 原生 form，不用 a-form：ant-design-vue 4 的 Form 只在存在 :model 时才
             在提交时 emit('finish')，而本页只有一个文件控件、没有字段级校验，
             用 a-form + :onFinish 会让「解析」按钮被 preventDefault 后什么都不发生。 -->
        <form class="upload-form" @submit.prevent="submit">
          <div class="field">
            <label :for="fieldId('file')">数据库文件（可使用加密库）</label>
            <input :id="fieldId('file')" type="file" accept=".db,.sqlite,.sqlite3" :disabled="uploading" @change="onFileChange" />
            <div class="hint">加密库会用内置口令自动解密。若解不开，通常是这个库不是考试客户端生成的，或来自尚未适配的客户端版本。</div>
          </div>
          <a-space wrap>
            <a-button type="primary" html-type="submit" :loading="uploading" :disabled="!selectedFile">解析</a-button>
            <a-button :disabled="uploading || !result" :onClick="reset">清空结果</a-button>
          </a-space>
        </form>

        <a-alert
          v-if="errorMessage"
          type="error"
          show-icon
          :message="errorMessage"
          class="page-alert"
        >
          <template v-if="errorAction" #description>
            <span>{{ errorAction }}</span>
          </template>
        </a-alert>

        <template v-if="result">
          <a-alert
            v-if="result.decrypted"
            type="success"
            show-icon
            class="page-alert"
            message="该文件是加密库，已由服务器自动解密后预览。"
          />
          <a-descriptions :column="1" size="small" bordered class="result-summary">
            <a-descriptions-item label="已知表">
              {{ Object.keys(result.tables || {}).join('、') || '无' }}
            </a-descriptions-item>
            <a-descriptions-item label="其它表（仅列名，不读取内容）">
              {{ (result.otherTables || []).join('、') || '无' }}
            </a-descriptions-item>
          </a-descriptions>

          <section v-for="name in Object.keys(result.tables || {})" :key="name" class="table-block">
            <h3 class="table-title">{{ name }}<span class="row-count">（{{ (result.tables[name].rows || []).length }} 行）</span></h3>
            <a-table
              :columns="columnsFor(name)"
              :data-source="result.tables[name].rows || []"
              :pagination="false"
              size="small"
              row-key="__rowKey"
              :scroll="{ x: 'max-content' }"
            />
          </section>
        </template>
      </ConsolePanel>
    </template>
  </div>
</template>

<script>
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import { isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { getUserExamToolVersion, getUserExamToolDownloadURL, inspectUserExamDatabase } from '@/api/UserExamToolApi'

const MAX_UPLOAD_BYTES = 16 * 1024 * 1024

// 解密失败的四种原因 → 给管理员看的「下一步动作」。
//
// ⚠️ 这四条必须各不相同：2026-11-14 之前的实现把「加密库」一律报成
// 「请先用脱机工具解密」，用户完全无法判断该改密钥、该换文件、还是该改方案。
// reason 由后端给出（machine-readable），前端只负责映射成动作。
const DECRYPT_ERROR_ACTIONS = {
  'wrong-key': '内置口令解不开这个库。常见原因：它不是考试客户端生成的，或来自尚未适配的客户端版本；也可能文件已损坏。',
  'key-missing': '服务端没有可用的解密口令，需要运维在服务端配置。请联系管理员处理。',
  'unsupported-cipher': '该库的加密方案不是 SQLite3MC aes256cbc（例如 SQLCipher 或旧 legacy 方案）。请用脱机工具确认参数，或先用脱机工具在本机解密后再上传明文库。',
  'not-database': '请确认上传的是考试客户端的 UserExamInfo.db（而非其它文件）。',
}

export default {
  name: 'UserExamToolPage',
  components: { ConsolePageHeading, ConsolePanel },
  setup() {
    const route = useRoute()
    const { currentUser } = useCurrentUserDisplay()
    const pageId = 'user-exam-tool'
    const fieldId = field => `${pageId}-${field}`
    const mountedPath = route.path

    // 与其它管理页一致：身份、角色、状态、路由任一变化都不再允许操作。
    const canManage = () => route.path === mountedPath && isAdminUser(currentUser.value)
      && !['disabled', '0'].includes(String(currentUser.value?.status ?? 'enabled'))

    const version = reactive({
      available: false, filename: '', version: '', sha256: '', sizeBytes: 0,
      cipher: '', legacy: '', kdfIter: 0, reason: '',
    })
    const loadingVersion = ref(false)
    const uploading = ref(false)
    const selectedFile = ref(null)
    const result = ref(null)
    const errorMessage = ref('')
    // errorAction 是「这次失败该做什么」的一句指引（按后端 reason 映射）。
    // 与 errorMessage 分开：message 说「是什么问题」，action 说「怎么解决」。
    const errorAction = ref('')


    const downloadURL = computed(() => getUserExamToolDownloadURL())

    const formatSize = bytes => {
      const value = Number(bytes)
      if (!Number.isFinite(value) || value <= 0) return '—'
      if (value < 1024) return `${value} B`
      if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
      return `${(value / 1024 / 1024).toFixed(2)} MB`
    }

    const applyVersion = data => {
      version.available = data?.available === true
      version.filename = data?.filename || ''
      version.version = data?.version || ''
      version.sha256 = data?.sha256 || ''
      version.sizeBytes = Number(data?.sizeBytes) || 0
      version.cipher = data?.cipher || ''
      version.legacy = data?.legacy || ''
      version.kdfIter = Number(data?.kdfIter) || 0
      version.reason = data?.reason || ''
    }

    const loadVersion = async () => {
      if (!canManage()) return
      loadingVersion.value = true
      try {
        const response = await getUserExamToolVersion()
        if (response?.status === 200) applyVersion(response.data)
        else if (response?.status === 403) errorMessage.value = '当前账号没有该权限'
        else errorMessage.value = '读取工具状态失败，请重试'
      } catch {
        errorMessage.value = '读取工具状态失败，请重试'
      } finally {
        loadingVersion.value = false
      }
    }

    const onFileChange = event => {
      errorMessage.value = ''
      errorAction.value = ''
      const file = event?.target?.files?.[0] || null
      if (file && file.size > MAX_UPLOAD_BYTES) {
        selectedFile.value = null
        errorMessage.value = '文件超过 16 MiB，无法预览'
        event.target.value = ''
        return
      }
      selectedFile.value = file
    }

    const submit = async () => {
      if (!canManage() || !selectedFile.value || uploading.value) return
      uploading.value = true
      errorMessage.value = ''
      errorAction.value = ''
      try {
        const response = await inspectUserExamDatabase(selectedFile.value)
        if (response?.status === 200) {
          result.value = response.data
          message.success(response.data?.decrypted ? '已自动解密并解析完成' : '解析完成')
        } else if (response?.status === 413) {
          errorMessage.value = '文件超过只读预览允许的大小'
        } else if (response?.status === 400) {
          const data = response.data || {}
          errorMessage.value = data.message || '该文件无法解析'
          // 四种原因各给一条可执行指引（缺一条用户就不知道该干什么）。
          errorAction.value = DECRYPT_ERROR_ACTIONS[data.reason] || ''
        } else {
          errorMessage.value = response.data?.message || '解析失败，请重试'
        }
      } catch {
        errorMessage.value = '解析失败，请重试'
      } finally {
        uploading.value = false
      }
    }

    const reset = () => {
      result.value = null
      errorMessage.value = ''
      errorAction.value = ''
      selectedFile.value = null
      const input = document.getElementById(fieldId('file'))
      if (input) input.value = ''
    }

    // 行主键用下标，避免依赖库里是否存在唯一列。
    const columnsFor = name => {
      const table = result.value?.tables?.[name]
      const columns = (table?.columns || []).map(column => ({
        title: column,
        dataIndex: column,
        key: column,
        ellipsis: true,
      }))
      return columns
    }

    onMounted(() => {
      loadVersion()
    })

    return {
      canManage, fieldId, version, loadingVersion, uploading, selectedFile,
      result, errorMessage, errorAction, downloadURL, formatSize, loadVersion, onFileChange,
      submit, reset, columnsFor,
    }
  },
}
</script>

<style scoped>
.user-exam-tool-page .page-alert { margin-bottom: 16px; }
.tool-descriptions, .result-summary { margin-bottom: 16px; }
.hash-value { word-break: break-all; font-size: 12px; }
.table-block { margin-top: 20px; }
.table-title { font-size: 14px; font-weight: 600; margin-bottom: 8px; }
.row-count { font-weight: 400; color: rgba(0, 0, 0, 0.45); }
.hint { color: rgba(0, 0, 0, 0.45); font-size: 12px; }
.usage-steps { margin: 0 0 12px; padding-left: 20px; color: var(--console-text); font-size: 13px; line-height: 1.9; }
.usage-steps code { padding: 1px 4px; border-radius: 3px; background: rgba(0, 0, 0, 0.05); font-size: 12px; word-break: break-all; }
.upload-form .field { display: flex; flex-direction: column; gap: 6px; margin-bottom: 16px; }
.key-field { display: flex; flex-direction: column; gap: 6px; margin: 12px 0; max-width: 520px; }
.key-field input { padding: 4px 11px; border: 1px solid #d9d9d9; border-radius: 6px; font-size: 13px; }
.key-alert { margin-top: 12px; }
.usage-lead { margin: 0 0 10px; line-height: 1.8; }
.upload-form label { font-size: 13px; color: var(--console-text); }
</style>
