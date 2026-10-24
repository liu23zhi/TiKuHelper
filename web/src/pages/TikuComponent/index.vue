<template>
  <div class="console-page tiku-page">
    <header class="section-heading page-heading">
      <div>
        <h1>题库管理</h1>
        <p class="page-lead">管理本地题目、答案和导入记录。</p>
      </div>
    </header>

    <section class="console-panel filter-panel">
      <a-form layout="inline" class="filter-form" @finish="onSearch">
        <a-form-item label="类型">
          <a-select v-model:value="typeValue" show-search allow-clear placeholder="请选择" :options="typeList"
                    @change="onFilterChange" />
        </a-form-item>
        <a-form-item label="问题">
          <a-input v-model:value="searchValue.question" placeholder="搜索问题" @press-enter="onSearch" />
        </a-form-item>
        <a-form-item label="无答案">
          <a-checkbox v-model:checked="searchValue.onlyShowEmptyAnswer" />
        </a-form-item>
        <a-form-item>
          <a-button type="primary" @click="onSearch" @keyup.enter="onSearch">
            <SearchOutlined />
            搜索
          </a-button>
        </a-form-item>
      </a-form>
    </section>

    <a-layout class="tiku-layout">
    <a-layout-content>
      <a-card :bordered="false" class="console-card console-panel tiku-table-card" :bodyStyle="style">
          <template #title>
            <div class="section-heading table-heading">
              <div>
                <h2>题目列表</h2>
                <p class="section-description">共 {{ page.total }} 道题目</p>
              </div>
              <a-space wrap class="section-actions">
            <a-button type="primary" @click="triggerJsonImport">
              JSON 导入
            </a-button>
            <a-button type="primary" @click="navigateToImport">
              <DownloadOutlined />
              智能导入
            </a-button>
            <a-button @click="handleExportSelected">
              导出选中
            </a-button>
            <a-button danger @click="handleDeleteSelected">
              批量删除
            </a-button>
            <a-button type="primary" @click="showModal({
              type: 0,
              question: '',
              options: '[]',
              answer: '[]'
            }, 2)">
              <FormOutlined/>
              添加
            </a-button>
              </a-space>
            </div>
          </template>
          <a-alert
            v-if="page.total"
            type="info"
            show-icon
            class="selection-alert"
          >
            <template #message>
              <div class="selection-alert-content">
                <span>{{ selectionSummary }}</span>
                <a-button
                  type="link"
                  size="small"
                  :disabled="allCurrentPageSelected"
                  @click="handleSelectCurrentPage"
                >
                  全选本页
                </a-button>
                <a-button
                  type="link"
                  size="small"
                  :disabled="!hasCurrentPageSelection"
                  @click="handleClearCurrentPage"
                >
                  取消本页选择
                </a-button>
                <a-button
                  type="link"
                  size="small"
                  :loading="selectAllLoading"
                  :disabled="allRowsSelected || selectAllLoading"
                  @click="handleSelectAll"
                >
                  全选所有页
                </a-button>
                <a-button
                  type="link"
                  size="small"
                  :disabled="!selectedRowKeys.length"
                  @click="handleClearSelection"
                >
                  取消全部选择
                </a-button>
              </div>
            </template>
          </a-alert>
          <a-table :columns="columns" :data-source="data" :pagination="false" :row-key="record => record.id"
                   :loading="tabLoading" :scroll="{ x: 'max-content' }" :row-selection="rowSelection">
            <template #type="{ record }">
              <a-tag :color="record.type === 0 ? 'green' : record.type === 1 ? 'cyan' : 'orange'">
                {{ quesType[record.type] }}
              </a-tag>
            </template>
            <template #question="{ record }">
              <div class="multiline-cell">{{ record.question || '-' }}</div>
            </template>
            <template #answer="{ record }">
              <div class="multiline-cell answer-cell">
                <a-tag v-for="(value, index) in (record.answer ? JSON.parse(record.answer) : [])" color="blue"
                       :key="index">{{ record.source === 2 ? index : value }}
                </a-tag>
              </div>
            </template>


            <template #sourceName="{ record }">
              <span>
                {{ getSourceNameText(record.source_name) }}
              </span>
            </template>


            <template #action="{ record }">
              <div style="display:flex;justify-content: space-between">
                <a-button type="primary" ghost @click="showModal(record, 1)">编辑</a-button>
                <a-button type="primary" ghost danger @click="deleteQuestion(record.id)">删除</a-button>
              </div>
            </template>
          </a-table>
          <a-pagination @change="onChange" :current="page.pageNo" :total="page.total" show-size-changer
                        :page-size-options="page.pageSizeOptions" :page-size="page.pageSize"
                        @showSizeChange="onShowSizeChange"
                        style="margin-top: 20px; text-align: right;"/>
      </a-card>
      <input
        ref="jsonImportInput"
        type="file"
        accept="application/json,.json"
        style="display: none"
        @change="handleJsonImportChange"
      />
      <a-modal :open="visible" title="详情内容" @ok="confirm" @cancel="visible = false" v-if="visible">
        <a-form :model="formData">
          <a-form-item label="类型">
            <a-select v-model:value="formData.type">
              <a-select-option v-for="(value, key) in quesType" :key="key" :value="parseInt(key)">
                {{ value }}
              </a-select-option>
            </a-select>
          </a-form-item>
          <a-form-item label="问题" class="margin_top">
            <a-textarea v-model:value="formData.question"/>
          </a-form-item>
          <div v-if="/[013]/.test(formData.type)" class="margin_top">
            <a-form-item label="选项">
              <OptionBox :options="formData.options ? JSON.parse(formData.options) : []" :type="formData.type"
                         :answer="answer" ref="optionBox"/>
            </a-form-item>
          </div>
          <div v-else>
            <a-card :bordered="false" style="width: 100%; text-align: start;" :bodyStyle="style">
              <a-tabs default-active-key="1">
                <a-tab-pane tab="默认选项" key="1">
                  <AnswerBox :answer="answer" ref="answerBox"/>
                </a-tab-pane>
                <a-tab-pane tab="提交附件" key="2">
                  <div>
                    <a-upload-dragger name="file" multiple="true" show-upload-list="true"
                                      action='/adapter-service/upload?parentDir=myword' @change="handleChange">
                      <p class="ant-upload-drag-icon">
                        <upload-outlined></upload-outlined>
                      </p>
                      <p class="ant-upload-text">点击或者拖动文件到该区域来上传</p>
                      <p class="ant-upload-hint">请不要上传敏感数据，银行卡号和密码，信用卡号有效期和安全码</p>
                    </a-upload-dragger>
                  </div>

                </a-tab-pane>
              </a-tabs>


            </a-card>
          </div>
        </a-form>
      </a-modal>
    </a-layout-content>
    </a-layout>
  </div>
</template>

<script>
import {computed, defineComponent, ref} from 'vue';
import {useRouter} from 'vue-router';
import Swal from 'sweetalert2'
import {
  getQuestions,
  updateQuestions,
  delQuestions,
  createQuestions,
  importQuestions,
} from '@/api/TikuComponentApi'
import {
  FormOutlined,
  DownloadOutlined,
  SearchOutlined,
  UploadOutlined
} from '@ant-design/icons-vue';
import OptionBox from '../../components/OptionBox/index.vue'
import AnswerBox from '../../components/AnswerBox/index.vue'
import {questionType} from "@/utils/uitls";
import {message} from "ant-design-vue";
import {
  buildQuestionBulkActionIds,
  buildQuestionExportPayload,
  buildQuestionImportDryRunPayload,
  buildQuestionImportExecutePayload,
  buildQuestionImportSummary,
  buildQuestionSelectionSummary,
  buildQuestionSourceLabel,
  normalizeQuestionTypeFilter,
  parseQuestionImportText,
} from '@/utils/topicOverviewHelpers.mjs';

const style = {
  padding: "0 0 24px"
}
const fileList = ref([])
const tabLoading = ref(true)
const data = ref([])
const columns = [
  {
    title: 'ID',
    dataIndex: 'id',
    key: 'id',
    width: 90
  },
  {
    title: '类型',
    key: 'type',
    dataIndex: 'type',
    width: 80,
    slots: {
      customRender: 'type',
    },
  },
  {
    title: '问题',
    width: 520,
    dataIndex: 'question',
    key: 'question',
    slots: {
      customRender: 'question',
    },
  },
  {
    title: '答案',
    dataIndex: 'answer',
    key: 'answer',
    width: 340,
    slots: {
      customRender: 'answer',
    },
  },
  {
    title: '数据来源',
    dataIndex: 'source_name',
    key: 'source_name',
    width: 160,
    slots: {
      customRender: 'sourceName',
    },
  },
  {
    title: '拓展参数',
    dataIndex: 'extra',
    key: 'extra',
    width: 300,
  },
  {
    title: '操作',
    fixed: 'right',
    key: 'action',
    width: 180,
    slots: {
      customRender: 'action',
    },
  },
]
const page = ref({
  pageSizeOptions: ['10', '20', '50', '100'],
  pageNo: 1,
  pageSize: 10,
  total: 0
})

const typeValue = ref(null)
const typeList = ref([])
const formData = ref({})
const answer = ref([])
const visible = ref(false)
const action = ref(2) // 1是编辑 2是添加
const searchValue = ref({})
const quesType = ref(questionType)
const selectedRowKeys = ref([])
const jsonImportInput = ref(null)
const selectAllLoading = ref(false)
const questionCache = new Map()

const rememberQuestionRows = (items = []) => {
  for (const item of items || []) {
    const id = Number(item?.id || 0)
    if (id > 0) {
      questionCache.set(id, item)
    }
  }
}

const resetSelectionState = () => {
  selectedRowKeys.value = []
  questionCache.clear()
}

export default defineComponent({
  name: 'TikuComponent',
  setup() {
    const router = useRouter();
    const navigateToImport = () => {
      router.push('/adapter/import');
    };
    const buildQuestionQuery = (pageNo, pageSize) => ({
      type: normalizeQuestionTypeFilter(typeValue.value),
      pageSize,
      pageNo,
      question: searchValue.value.question || '',
      source: parseInt(searchValue.value.source) || 0,
      extra: searchValue.value.extra || '',
      onlyShowEmptyAnswer: searchValue.value.onlyShowEmptyAnswer || false
    })
    const loadAllFilteredQuestions = async () => {
      const batchSize = 100
      const first = await getQuestions(buildQuestionQuery(0, batchSize))
      if (first?.status !== 200) {
        throw new Error(first?.message || '获取全部题目失败')
      }
      const total = Number(first?.data?.total || 0)
      const items = Array.isArray(first?.data?.items) ? [...first.data.items] : []
      const totalPages = Math.ceil(total / batchSize)
      for (let pageNo = 1; pageNo < totalPages; pageNo += 1) {
        const response = await getQuestions(buildQuestionQuery(pageNo, batchSize))
        if (response?.status !== 200) {
          throw new Error(response?.message || '获取全部题目失败')
        }
        if (Array.isArray(response?.data?.items)) {
          items.push(...response.data.items)
        }
      }
      return items.slice(0, total)
    }
    const selectedQuestions = computed(() => {
      const selectedIds = buildQuestionBulkActionIds(selectedRowKeys.value)
      if (!selectedIds.length) {
        return []
      }
      return selectedIds.map(id => questionCache.get(id)).filter(Boolean)
    })
    const currentPageIds = computed(() => buildQuestionBulkActionIds(
      (data.value || []).map(item => item?.id),
    ))
    const allCurrentPageSelected = computed(() => (
      currentPageIds.value.length > 0
      && currentPageIds.value.every(id => selectedRowKeys.value.includes(id))
    ))
    const hasCurrentPageSelection = computed(() => (
      currentPageIds.value.some(id => selectedRowKeys.value.includes(id))
    ))
    const allRowsSelected = computed(() => {
      const total = Number(page.value.total || 0)
      return total > 0 && selectedRowKeys.value.length === total
    })
    const selectionSummary = computed(() => buildQuestionSelectionSummary(
      selectedRowKeys.value.length,
      page.value.total || 0,
    ))
    const handleSelectCurrentPage = () => {
      rememberQuestionRows(data.value)
      selectedRowKeys.value = buildQuestionBulkActionIds([
        ...selectedRowKeys.value,
        ...currentPageIds.value,
      ])
    }
    const handleClearCurrentPage = () => {
      const currentPageIdSet = new Set(currentPageIds.value)
      selectedRowKeys.value = selectedRowKeys.value.filter(id => !currentPageIdSet.has(id))
    }
    const handleSelectAll = async () => {
      if (allRowsSelected.value || selectAllLoading.value) {
        return
      }
      selectAllLoading.value = true
      try {
        const rows = await loadAllFilteredQuestions()
        rememberQuestionRows(rows)
        selectedRowKeys.value = buildQuestionBulkActionIds(rows.map(item => item?.id))
        if (!rows.length) {
          resetSelectionState()
        }
      } catch (error) {
        message.error(error?.message || '全选失败')
      } finally {
        selectAllLoading.value = false
      }
    }
    const handleClearSelection = () => {
      resetSelectionState()
    }
    const rowSelection = computed(() => ({
      selectedRowKeys: selectedRowKeys.value,
      preserveSelectedRowKeys: true,
      onChange: (keys) => {
        selectedRowKeys.value = buildQuestionBulkActionIds(keys)
      },
    }))
    return {
      tabLoading,
      typeList,
      typeValue,
      answer,
      page,
      data,
      columns,
      quesType,
      visible,
      formData,
      style,
      searchValue,
      selectedRowKeys,
      selectedQuestions,
      rowSelection,
      selectionSummary,
      allRowsSelected,
      allCurrentPageSelected,
      hasCurrentPageSelection,
      selectAllLoading,
      handleSelectCurrentPage,
      handleClearCurrentPage,
      handleSelectAll,
      handleClearSelection,
      jsonImportInput,
      labelCol: {span: 8},
      wrapperCol: {span: 14},
      navigateToImport,
      fileList,
    }
  },
  components: {
    UploadOutlined,
    OptionBox,
    AnswerBox,
    FormOutlined,
    SearchOutlined,
    DownloadOutlined
  },
  mounted() {
    this.getType()
    this.fetchData()
  },
  methods: {
    async onFilterChange() {
      // Reset page number when any filter changes and fetch data
      this.page.pageNo = 1
      resetSelectionState()
      await this.fetchData()
    },
    handleChange(info) {
      const status = info.file.status;
      if (status === 'done') {
        message.success(`${info.file.name} 上传成功`);
        fileList.value.push(info.file.response)
        this.$refs.answerBox.pushAnswer(info.file.response)
      } else if (status === 'error') {
        // 处理上传失败
        message.error(`${info.file.name} 上传失败`);
      }
    },
    typeObjects(types) {
      return Object.entries(types).map(([key, value]) => ({
        value: parseInt(key),
        label: value
      }));
    },
    getType() {
      const arr = this.typeObjects(questionType)
      typeList.value = arr
    },
    async fetchData() {
      fileList.value = []
      tabLoading.value = true
      getQuestions({
        type: normalizeQuestionTypeFilter(typeValue.value),
        pageSize: page.value.pageSize,
        pageNo: page.value.pageNo - 1,
        question: searchValue.value.question || '',
        source: parseInt(searchValue.value.source) || 0,
        extra: searchValue.value.extra || '',
        onlyShowEmptyAnswer: searchValue.value.onlyShowEmptyAnswer || false
      }).then(res => {
        data.value = Array.isArray(res?.data?.items) ? res.data.items : []
        rememberQuestionRows(data.value)
        tabLoading.value = false
        this.page.total = res?.data?.total
      })

    },
    onSearch() {
      // Reset page number when search filters change
      this.page.pageNo = 1
      resetSelectionState()
      this.fetchData()
    },
    onShowSizeChange(current, pageSize) {
      this.page.pageSize = pageSize
      this.page.pageNo = 1
      this.fetchData()
    },

    onChange(page) {
      this.page.pageNo = page
      this.fetchData()
    },

    showModal(record, act) {
      if (record.options === '[]' || record.options === '') {
        record.options = '[]'
      }
      if (record.answer === '[]' || record.answer === '') {
        record.answer = '[]'
      }

      formData.value = record
      answer.value = formData.value.answer ? JSON.parse(formData.value.answer) : []
      action.value = act
      fileList.value = []
      visible.value = true
    },

    async deleteQuestion(id) {
      fileList.value = []
      await delQuestions({id: id}).then(res => {
        res.status == 200 ? message.success(`删除成功`) : message.error(`删除失败`)
      })
      visible.value = false
      selectedRowKeys.value = selectedRowKeys.value.filter(item => Number(item) !== Number(id))
      await this.fetchData()
    },

    getSourceNameText(sourceName) {
      return buildQuestionSourceLabel(sourceName)
    },

    triggerJsonImport() {
      const input = this.$refs.jsonImportInput
      if (input) {
        input.click()
      }
    },

    async handleJsonImportChange(event) {
      const input = event?.target
      const file = input?.files?.[0]
      if (!file) {
        return
      }

      try {
        const content = await file.text()
        const items = parseQuestionImportText(content)
        if (!items.length) {
          message.warning('JSON 文件中没有可导入的题目')
          return
        }

        const dryRunResponse = await importQuestions(buildQuestionImportDryRunPayload(items))
        if (dryRunResponse?.status !== 200) {
          message.error(dryRunResponse?.message || '导入预检失败')
          return
        }

        const dryRunResult = dryRunResponse?.data || {}
        let overwrite = false

        if (Array.isArray(dryRunResult.existingIds) && dryRunResult.existingIds.length) {
          const confirmResult = await Swal.fire({
            icon: 'warning',
            title: '检测到重复题目',
            text: buildQuestionImportSummary(dryRunResult),
            showDenyButton: true,
            showCancelButton: true,
            confirmButtonText: '是，直接覆盖',
            denyButtonText: '否，只导入不存在的数据',
            cancelButtonText: '取消',
            heightAuto: false,
            scrollbarPadding: false,
          })

          if (confirmResult.isDismissed) {
            return
          }
          overwrite = Boolean(confirmResult.isConfirmed)
        }

        const executeResponse = await importQuestions(buildQuestionImportExecutePayload(items, overwrite))
        if (executeResponse?.status !== 200) {
          message.error(executeResponse?.message || '导入失败')
          return
        }

        const executeResult = executeResponse?.data || {}
        const summary = `导入完成：新增 ${Number(executeResult.created || 0)} 条，覆盖 ${Number(executeResult.updated || 0)} 条，跳过 ${Number(executeResult.skipped || 0)} 条。`
        await Swal.fire({
          icon: 'success',
          title: 'JSON 导入成功',
          text: summary,
          confirmButtonText: '我知道了',
          heightAuto: false,
          scrollbarPadding: false,
        })
        await this.fetchData()
      } catch (error) {
        message.error(error?.message || 'JSON 导入失败')
      } finally {
        if (input) {
          input.value = ''
        }
      }
    },

    handleExportSelected() {
      const rows = this.selectedQuestions
      if (!rows.length) {
        message.warning('请先选择需要导出的题目')
        return
      }
      const payload = buildQuestionExportPayload(rows)
      const blob = new Blob([payload.content], { type: 'application/json;charset=utf-8' })
      const objectUrl = window.URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = objectUrl
      link.download = payload.filename
      document.body.appendChild(link)
      link.click()
      document.body.removeChild(link)
      window.URL.revokeObjectURL(objectUrl)
      message.success(`已导出 ${rows.length} 条题目`)
    },

    async handleDeleteSelected() {
      const ids = buildQuestionBulkActionIds(this.selectedRowKeys)
      if (!ids.length) {
        message.warning('请先选择需要删除的题目')
        return
      }
      for (const id of ids) {
        const res = await delQuestions({ id })
        if (res?.status !== 200) {
          message.error(`题目 ${id} 删除失败`)
          return
        }
      }
      message.success(`成功删除 ${ids.length} 条题目`)
      resetSelectionState()
      await this.fetchData()
    },


    async confirm() {
      formData.value.type = parseInt(formData.value.type)

      if (/[0|1|3]/.test(formData.value.type)) {
        const childComponentData = this.$refs.optionBox.getData()
        Object.assign(formData.value, childComponentData)
      } else {
        const textAns = this.$refs.answerBox.getData()
        console.log('textAns==>', textAns)
        formData.value.answer = textAns
      }
      let response;
      const successMessage = action.value === 1 ? '修改成功' : '创建成功';
      const errorMessage = action.value === 1 ? '修改失败' : '创建失败';
      if (action.value === 1) {
        response = await updateQuestions(formData.value)
      } else if (action.value === 2) {
        response = await createQuestions([formData.value])
      }
      if (response.status === 200) {
        message.success(successMessage);
      } else {
        message.error(errorMessage);
      }
      fileList.value = []
      visible.value = false
      await this.fetchData()
    },
  }
})
</script>
<style scoped>
.console-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
  color: var(--console-text, #1f2937);
}

.page-heading,
.table-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}

.page-heading h1,
.table-heading h2 {
  margin: 0;
  color: var(--console-text, #1f2937);
  font-size: 20px;
  font-weight: 650;
}

.page-lead,
.section-description {
  margin: 4px 0 0;
  color: var(--console-muted, #64748b);
  font-size: 13px;
}

.console-panel {
  padding: 16px;
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: var(--console-radius, 12px);
  background: var(--console-surface, #fff);
}

.filter-form {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 16px;
}

.filter-form :deep(.ant-form-item) {
  margin: 0;
}

.filter-form :deep(.ant-input),
.filter-form :deep(.ant-select) {
  width: 220px;
  max-width: 100%;
}

.filter-form :deep(.ant-select:first-child) {
  width: 180px;
}

.tiku-layout,
.tiku-layout :deep(.ant-layout-content),
.tiku-table-card {
  min-width: 0;
  width: 100%;
  background: transparent;
}

.tiku-table-card :deep(.ant-card-head-title) {
  min-width: 0;
}

.ant-form-item {
  margin-bottom: 0;
}

.selection-alert {
  margin: 12px 0;
}

.selection-alert-content {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.multiline-cell {
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.6;
}

.answer-cell {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

@media (max-width: 640px) {
  .console-panel {
    padding: 12px;
  }

  .filter-form,
  .filter-form :deep(.ant-form-item),
  .filter-form :deep(.ant-input),
  .filter-form :deep(.ant-select) {
    width: 100%;
  }

  .section-actions,
  .section-actions :deep(.ant-space-item),
  .section-actions :deep(.ant-btn) {
    max-width: 100%;
  }
}
</style>
