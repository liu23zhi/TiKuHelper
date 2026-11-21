<template>
  <a-popover v-if="canManage" trigger="click" :title="`${title}导入导出`">
    <template #content>
      <div class="admin-transfer">
        <p>导出全部记录，最多 10 万条。</p>
        <a-space wrap>
          <a-select :value="format" :options="formatOptions" :disabled="busy" :onChange="entry.format" aria-label="导出格式" style="width: 100px" />
          <a-checkbox :checked="masked" :disabled="busy" :onChange="entry.masked">脱敏</a-checkbox>
          <a-button type="primary" :loading="busy" :disabled="busy" :onClick="entry.export">导出全部</a-button>
        </a-space>
        <template v-if="importable">
          <p>{{ importDescription }} 回灌备份请先取消导出脱敏。</p>
          <a-space wrap>
            <a-upload :accept="accept" :show-upload-list="false" :before-upload="entry.import" :disabled="busy">
              <a-button :disabled="busy">选择文件导入</a-button>
            </a-upload>
            <a-button :disabled="busy" :onClick="entry.template">下载 {{ templateFormat.toUpperCase() }} 模板</a-button>
          </a-space>
        </template>
        <RequestFailureAlert v-if="failure" :failure="failure" message="数据任务未完成，请刷新任务状态核对，勿重复提交。" />
        <a-alert v-if="task" :type="task.status === 'failed' ? 'error' : 'info'" show-icon :message="describeTask(task)" class="transfer-status" />
        <a-space v-if="task" wrap class="transfer-links">
          <a-button size="small" :disabled="busy" :onClick="entry.refresh">刷新任务状态</a-button>
          <a v-if="downloadUrl" :href="downloadUrl" :onClick="entry.download">下载结果</a>
          <a v-if="errorReportUrl" :href="errorReportUrl" :onClick="entry.download">下载失败行报告</a>
        </a-space>
      </div>
    </template>
    <a-button :loading="busy">{{ importable ? '导入 / 导出' : '导出' }}</a-button>
  </a-popover>
</template>

<script>
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import RequestFailureAlert from './RequestFailureAlert.vue'
import { getStoredCurrentUser, isAdminUser, useCurrentUserDisplay } from '../utils/currentUser'
import { ensureRequestFailure } from '../utils/requestFailureView.mjs'
import {
  submitExportTask, submitImportTask, fetchAsyncTask, fetchImportTemplate, pollAsyncTask,
  taskPayload, taskDownloadUrl, taskErrorReportUrl, describeTask,
} from '../pages/IntelligentImport/asyncTaskApi.mjs'

export default {
  name: 'AdminDataTransfer',
  components: { RequestFailureAlert },
  props: {
    resource: { type: String, required: true },
    title: { type: String, default: '数据' },
    importable: { type: Boolean, default: false },
    importDescription: { type: String, default: '支持 CSV / JSON，每次最多 5000 条，相同查询键和来源会更新。' },
    formats: { type: Array, default: () => ['csv', 'json'] },
    accept: { type: String, default: '.csv,.json' },
  },
  emits: ['imported'],
  setup(props, { emit }) {
    const { currentUser, captureUpdateGuard } = useCurrentUserDisplay()
    const validAdmin = user => isAdminUser(user) && Number.isSafeInteger(Number(user?.id)) && Number(user.id) > 0 && !['disabled', '0'].includes(String(user?.status ?? 'enabled'))
    const canManage = computed(() => validAdmin(currentUser.value))
    const format = ref('csv'), masked = ref(true), busy = ref(false)
    const task = ref(null), failure = shallowRef(null), entry = shallowRef({})
    const formatOptions = computed(() => props.formats.filter(value => ['csv', 'json', 'xlsx', 'xml', 'html'].includes(value)).map(value => ({ value, label: value.toUpperCase() })))
    const templateFormat = computed(() => props.accept.includes('.csv') ? 'csv' : props.accept.includes('.xlsx') ? 'xlsx' : 'json')
    const downloadUrl = computed(() => taskDownloadUrl(task.value))
    const errorReportUrl = computed(() => taskErrorReportUrl(task.value))
    let alive = true, generation = 0

    function createEntry() {
      const resource = props.resource, version = generation, isCurrentUser = captureUpdateGuard()
      const owns = () => alive && version === generation && resource === props.resource && isCurrentUser() && validAdmin(getStoredCurrentUser())
      const download = event => { if (!owns()) { event?.preventDefault?.(); event?.stopImmediatePropagation?.() } }
      async function follow(id, imported = false) {
        const result = await pollAsyncTask(id, { shouldStop: () => !owns() })
        if (!owns() || result.stopped) return
        if (result.task) task.value = result.task
        if (result.error) failure.value = ensureRequestFailure(result.error)
        if (result.expired) failure.value = ensureRequestFailure({ code: 'TASK_POLL_EXPIRED' })
        if (imported && result.task?.status === 'succeeded') emit('imported')
      }
      async function submit(type, file) {
        if (!owns() || busy.value) return
        busy.value = true; failure.value = null; task.value = null
        try {
          const response = type === 'import'
            ? await submitImportTask({ resource, file })
            : await submitExportTask({ resource, format: format.value, masked: masked.value })
          if (!owns()) return
          const payload = taskPayload(response)
          if (response?.status !== 202 || !payload?.taskId) throw new Error('invalid task response')
          task.value = payload
          await follow(payload.taskId, type === 'import')
        } catch (error) {
          if (owns()) failure.value = ensureRequestFailure(error)
        } finally { if (owns()) busy.value = false }
      }
      return {
        export: () => submit('export'),
        import: file => { if (props.importable && owns()) void submit('import', file); return false },
        format: value => { if (owns() && !busy.value && formatOptions.value.some(option => option.value === value)) format.value = value },
        masked: event => { if (owns() && !busy.value) masked.value = event?.target?.checked === true },
        download,
        async refresh() {
          if (!owns() || busy.value || !task.value?.taskId) return
          busy.value = true; failure.value = null
          try {
            const response = await fetchAsyncTask(task.value.taskId)
            if (owns()) task.value = taskPayload(response)
          } catch (error) { if (owns()) failure.value = ensureRequestFailure(error) }
          finally { if (owns()) busy.value = false }
        },
        async template() {
          if (!owns() || busy.value || !props.importable) return
          busy.value = true; failure.value = null
          try {
            const selectedFormat = templateFormat.value
            const response = await fetchImportTemplate(resource, selectedFormat)
            if (!owns()) return
            if (response?.status !== 200 || !(response.data instanceof Blob)) throw new Error('invalid template response')
            const url = window.URL.createObjectURL(response.data), link = document.createElement('a')
            try {
              link.href = url; link.download = `${resource}-template.${selectedFormat}`
              document.body.appendChild(link); link.click()
            } finally { link.remove(); window.URL.revokeObjectURL(url) }
          } catch (error) { if (owns()) failure.value = ensureRequestFailure(error) }
          finally { if (owns()) busy.value = false }
        },
      }
    }
    watch([() => props.resource, () => props.formats, () => currentUser.value], () => {
      generation += 1; task.value = null; failure.value = null; busy.value = false
      masked.value = true; format.value = formatOptions.value[0]?.value || 'csv'; entry.value = createEntry()
    }, { immediate: true, flush: 'sync' })
    onBeforeUnmount(() => { alive = false; generation += 1 })
    return { canManage, format, masked, busy, task, failure, entry, formatOptions, templateFormat, downloadUrl, errorReportUrl, describeTask }
  },
}
</script>

<style scoped>
.admin-transfer { width: min(420px, 80vw); }
.admin-transfer p { margin: 0 0 10px; }
.admin-transfer p:not(:first-child), .transfer-status, .transfer-links { margin-top: 12px; }
</style>
