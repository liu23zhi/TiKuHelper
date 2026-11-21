<template>
  <div v-if="canManage" class="ocr-batch-entry">
    <a-button :onClick="entry.open">批量识别</a-button>
    <a-modal :open="visible" title="批量识别图片" :width="1050" wrap-class-name="ocr-batch-dialog" :footer="null" :mask-closable="false" :closable="!busy" :onCancel="entry.close">
      <div class="ocr-batch-content">
      <a-alert type="warning" show-icon message="识别按当前 OCR 单价逐张计费，费用扣除当前管理员余额；命中已有缓存也按现有计费规则处理。上传后先查看清单，点击开始才会识别。" />
      <p>TXT：每行一个图片 URL；Markdown 可直接提取文中的图片 URL，最多 50 条、1 MiB。DOCX / DOC：提取内嵌图片并去重，文档最多 10 MiB、50 张，每张最多 5 MiB；旧版二进制 DOC 请先另存为 DOCX。</p>
      <a-space wrap>
        <a-upload accept=".txt,.md,.markdown,.doc,.docx" :show-upload-list="false" :before-upload="entry.upload" :disabled="busy || preparing"><a-button :loading="preparing" :disabled="busy || preparing">选择 TXT / Markdown / Word</a-button></a-upload>
        <a-button type="primary" :disabled="busy || preparing || !pendingCount" :loading="busy" :onClick="entry.start">{{ busy ? '识别中' : `开始识别（${pendingCount} 张，逐张计费）` }}</a-button>
        <a-button v-if="busy" :disabled="stopRequested" :onClick="entry.stop">{{ stopRequested ? '当前图片完成后停止' : '停止后续识别' }}</a-button>
      </a-space>
      <p v-if="validation" role="alert">{{ validation }}</p>
      <RequestFailureAlert v-if="failure" :failure="failure" message="批量处理未完成，请按请求编号核对账单和缓存，勿重复识别结果未确认的图片。" />
      <p v-if="items.length">清单 {{ items.length }} 张，已完成 {{ completedCount }} 张。{{ notice }}</p>
      <a-table :data-source="items" :columns="columns" row-key="id" size="small" table-layout="fixed" :pagination="{ pageSize: 10 }" :scroll="{ x: 1300 }">
        <template #bodyCell="{ column, record }">
          <span v-if="column.key === 'status'">{{ statusLabel(record.status) }}</span>
          <div v-else-if="column.key === 'result'" class="ocr-batch-result">
            <div>{{ record.text || '-' }}</div>
            <p v-if="record.warning" role="alert">{{ record.warning }}</p>
          </div>
        </template>
      </a-table>
      </div>
    </a-modal>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import RequestFailureAlert from './RequestFailureAlert.vue'
import { getStoredCurrentUser, isAdminUser, useCurrentUserDisplay } from '../utils/currentUser'
import { ensureRequestFailure } from '../utils/requestFailureView.mjs'
import { parseOCRBatchURLs, readOCRBatchText, imageFileFromOCRDocument } from '../utils/ocrBatchHelpers.mjs'
import { previewOCRDocument, recognizeOCRURL, uploadOCRImage } from '../api/OCRBatchApi.mjs'

export default {
  name: 'OCRBatchRecognition',
  components: { RequestFailureAlert },
  emits: ['completed'],
  setup(_, { emit }) {
    const { currentUser, captureUpdateGuard } = useCurrentUserDisplay()
    const validAdmin = user => isAdminUser(user) && Number.isSafeInteger(Number(user?.id)) && Number(user.id) > 0 && !['disabled', '0'].includes(String(user?.status ?? 'enabled'))
    const canManage = computed(() => validAdmin(currentUser.value))
    const visible = ref(false), busy = ref(false), preparing = ref(false), stopRequested = ref(false), items = ref([]), validation = ref(''), notice = ref('')
    const failure = shallowRef(null), entry = shallowRef({})
    const pendingCount = computed(() => items.value.filter(item => item.status === 'pending').length)
    const completedCount = computed(() => items.value.filter(item => item.status === 'saved' || item.status === 'recognized').length)
    const columns = [
      { title: '图片 / 来源', key: 'name', dataIndex: 'name', width: 330 },
      { title: '状态', key: 'status', width: 150 },
      { title: '请求编号', key: 'requestId', dataIndex: 'requestId', width: 260 },
      { title: '识别结果 / 提示', key: 'result', width: 500 },
    ]
    const statusLabel = value => ({ pending: '待识别', running: '正在识别', saved: '已识别并保存', recognized: '已识别，保存待核对', failed: '识别失败', unknown: '结果未确认' }[value] || value)
    let alive = true, generation = 0
    function reset() { visible.value = false; busy.value = false; preparing.value = false; stopRequested.value = false; items.value = []; validation.value = ''; notice.value = ''; failure.value = null }
    function makeEntry() {
      const version = generation, isCurrentUser = captureUpdateGuard()
      const owns = () => alive && version === generation && isCurrentUser() && validAdmin(getStoredCurrentUser())
      return {
        open() { if (owns() && !busy.value) { visible.value = true } },
        close() { if (owns() && !busy.value && !preparing.value) { reset(); generation += 1; entry.value = makeEntry() } },
        stop() { if (owns() && busy.value) stopRequested.value = true },
        upload(file) {
          if (!owns() || busy.value || preparing.value) return false
          preparing.value = true; failure.value = null; validation.value = ''; notice.value = ''; items.value = []
          void (async () => {
            try {
              const extension = String(file.name || '').split('.').pop().toLowerCase()
              let records
              if (['txt', 'md', 'markdown'].includes(extension)) {
                if (file.size > 1024 * 1024) throw new Error('TXT 文件不能超过 1 MiB')
                const urls = parseOCRBatchURLs(await readOCRBatchText(file), { extract: extension !== 'txt' })
                records = urls.map((url, index) => ({ id: index + 1, name: url, url, status: 'pending' }))
              } else if (['doc', 'docx'].includes(extension)) {
                if (file.size > 10 * 1024 * 1024) throw new Error('DOCX 文档不能超过 10 MiB')
                const response = await previewOCRDocument(file)
                if (!owns()) return
                if (response?.status !== 200 || !Array.isArray(response.data?.images) || !response.data.images.length || response.data.images.length > 50) throw new Error('文档图片预览无效')
                records = response.data.images.map((image, index) => ({ id: index + 1, name: image.name, file: imageFileFromOCRDocument(image), status: 'pending' }))
              } else { throw new Error('请选择 TXT、Markdown 或 Word 文件；旧版 DOC 请先另存为 DOCX') }
              if (owns()) { items.value = records; generation += 1; preparing.value = false; entry.value = makeEntry() }
            } catch (error) {
              if (owns()) { validation.value = extensionError(error); failure.value = ensureRequestFailure(error) }
            } finally { if (owns()) preparing.value = false }
          })()
          return false
        },
        async start() {
          if (!owns() || busy.value || preparing.value || !pendingCount.value) return
          busy.value = true; stopRequested.value = false; failure.value = null; notice.value = ''
          try {
            for (const item of items.value) {
              if (!owns() || stopRequested.value) break
              if (item.status !== 'pending') continue
              item.status = 'running'
              try {
                const response = item.file ? await uploadOCRImage(item.file) : await recognizeOCRURL(item.url)
                if (!owns()) return
                const result = response?.data
                if (response?.status !== 200 || typeof result?.success !== 'boolean' || !result.requestId) throw new Error('invalid OCR result')
                item.requestId = result.requestId
                item.text = Array.isArray(result.toolResult) ? (result.toolResult.find(value => value?.tool === 'image_ocr')?.text || '') : ''
                item.warning = [result.billingWarning, result.ocrCacheWarning].filter(Boolean).join(' ')
                item.status = result.success ? (result.ocrCacheEntry?.urlHash ? 'saved' : 'recognized') : 'failed'
                if (!result.success && !item.text && !item.warning) item.warning = '识别失败，请按请求编号核对账单和原始请求结果。'
              } catch (error) {
                if (!owns()) return
                item.status = 'unknown'; item.warning = '请求结果未确认，请先核对账单和缓存。'
                failure.value = ensureRequestFailure(error); stopRequested.value = true
              }
            }
          } finally {
            if (owns()) {
              busy.value = false; notice.value = stopRequested.value ? '已停止后续识别，待处理图片可再次点击开始。' : '本批处理已结束。'
              generation += 1; entry.value = makeEntry(); emit('completed')
            }
          }
        },
      }
    }
    function extensionError(error) {
      // 本地解析器的固定提示可展示；服务端或网络错误统一由失败组件呈现。
      const text = error instanceof Error ? error.message : ''
      return /^(TXT|DOCX|第 \d+ 行|单次最多|请选择|文档图片|文档中|无法读取 TXT)/.test(text) ? text : '无法读取文件，请核对格式或重试预览。'
    }
    watch(() => currentUser.value, () => { generation += 1; reset(); entry.value = makeEntry() }, { immediate: true, flush: 'sync' })
    onBeforeUnmount(() => { alive = false; generation += 1 })
    return { canManage, visible, busy, preparing, stopRequested, items, validation, notice, failure, entry, pendingCount, completedCount, columns, statusLabel }
  },
}
</script>

<style scoped>
.ocr-batch-result { white-space: pre-wrap; overflow-wrap: anywhere; }
.ocr-batch-content { display:flex; flex-direction:column; gap:16px; min-width:0; }
.ocr-batch-content p { margin:0; line-height:1.7; }
.ocr-batch-content :deep(.ant-table-wrapper) { width:100%; min-width:0; }
.ocr-batch-content :deep(td) { white-space:normal; overflow-wrap:anywhere; }
</style>
