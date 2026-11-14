<template>
  <div class="console-page tiku-page" :onInputCapture="questionEntry.guardNative"
       :onChangeCapture="questionEntry.guardNative" :onClickCapture="questionEntry.guardNative">
    <ConsolePageHeading title="题库管理" description="管理本地题目、答案和导入记录。">
      <template #actions>
      <a-button :disabled="!canUseQuestions()" :onClick="questionEntry.bind(fetchData)" :loading="tabLoading">刷新</a-button>
      <a-button :disabled="!canUseQuestions()" :onClick="questionEntry.bind(triggerJsonImport)">JSON 导入</a-button>
      <a-button :disabled="!canUseQuestions()" :onClick="questionEntry.bind(navigateToImport)"><DownloadOutlined />智能导入</a-button>
      <a-button :disabled="!canUseQuestions() || !selectedRowKeys.length" :onClick="questionEntry.bind(handleExportSelected)">导出选中</a-button>
      <a-button :disabled="!canUseQuestions() || !selectedRowKeys.length" :onClick="questionEntry.bind(openBulkCourseModal)">批量编辑课程名</a-button>
      <a-button danger :disabled="!canUseQuestions() || !selectedRowKeys.length" :onClick="questionEntry.bind(handleDeleteSelected)">批量删除</a-button>
      <a-button type="primary" :disabled="!canUseQuestions()" :onClick="questionEntry.bind(showModal, { type: 0, question: '', options: '[]', answer: '[]' }, 2)"><FormOutlined />添加</a-button>
      </template>
    </ConsolePageHeading>
    <a-alert v-if="!canUseQuestions()" type="warning" show-icon
             message="当前身份无题库管理权限，请使用管理员账号登录。" />

    <section class="console-panel filter-panel">
      <a-form layout="inline" class="filter-form" :onFinish="questionEntry.bind(onSearch)">
        <a-form-item label="类型" :html-for="fieldId('filter-type')">
          <a-select :id="fieldId('filter-type')" :disabled="!canUseQuestions()" :value="typeValue" v-on="{ 'update:value': questionEntry.bind(value => typeValue = value) }" show-search allow-clear placeholder="请选择" :options="typeList"
                    :onChange="questionEntry.bind(onFilterChange)" />
        </a-form-item>
        <a-form-item label="问题" :html-for="fieldId('filter-question')">
          <a-input :id="fieldId('filter-question')" :disabled="!canUseQuestions()" :value="searchValue.question" v-on="{ 'update:value': questionEntry.bind(value => searchValue.question = value) }" placeholder="搜索问题" :onPressEnter="questionEntry.bind(onSearch)" />
        </a-form-item>
        <a-form-item label="无答案" :html-for="fieldId('filter-empty-answer')">
          <a-checkbox :id="fieldId('filter-empty-answer')" :disabled="!canUseQuestions()" :checked="searchValue.onlyShowEmptyAnswer" v-on="{ 'update:checked': questionEntry.bind(value => searchValue.onlyShowEmptyAnswer = value) }" />
        </a-form-item>
        <a-form-item>
          <a-button type="primary" :disabled="!canUseQuestions()" :onClick="questionEntry.bind(onSearch)" :onKeyup="questionEntry.bind(event => event.key === 'Enter' && onSearch())">
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
            </div>
          </template>
          <RequestFailureAlert v-if="listFailure" :failure="listFailure" message="获取题目列表失败"
            :retryable="true" :busy="tabLoading" :context-key="listFailureContext" :is-current="isCurrentListFailure" :onRetry="questionEntry.bind(guardedRetryListFailure)" />
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
                  :onClick="questionEntry.bind(handleSelectCurrentPage)"
                >
                  全选本页
                </a-button>
                <a-button
                  type="link"
                  size="small"
                  :disabled="!hasCurrentPageSelection"
                  :onClick="questionEntry.bind(handleClearCurrentPage)"
                >
                  取消本页选择
                </a-button>
                <a-button
                  type="link"
                  size="small"
                  :loading="selectAllLoading"
                  :disabled="allRowsSelected || selectAllLoading"
                  :onClick="questionEntry.bind(handleSelectAll)"
                >
                  全选所有页
                </a-button>
                <a-button
                  type="link"
                  size="small"
                  :disabled="!selectedRowKeys.length"
                  :onClick="questionEntry.bind(handleClearSelection)"
                >
                  取消全部选择
                </a-button>
              </div>
            </template>
          </a-alert>
          <TableColumnSettings :model-value="selectedColumnKeys" :options="columnOptions"
            :context-key="columnScopeKey" :priority-keys="columnPriorityKeys"
            v-on="{ 'update:modelValue': questionEntry.bind(setSelectedColumnKeys), reset: questionEntry.bind(resetColumns) }" />
          <ConsoleTableShell label="题库管理">
          <a-table :columns="visibleColumns" :data-source="data" :pagination="false" :row-key="record => record.id" table-layout="fixed"
                   :loading="tabLoading" :scroll="{ x: tableScrollX }" :row-selection="rowSelection">
            <template #bodyCell="{ column, record }">
              <a-tag v-if="column.key === 'type'" :color="record.type === 0 ? 'green' : record.type === 1 ? 'cyan' : 'orange'">
                {{ quesType[record.type] }}
              </a-tag>
              <OverflowTableText v-else-if="['question', 'answer', 'extra'].includes(column.key)"
                :text="column.key === 'answer' ? buildQuestionAnswerText(record) : record[column.key]"
                :expanded="isExpanded(record, column.key)" :onToggle="questionEntry.bind(toggleExpanded, record, column.key)" />
              <span v-else-if="column.key === 'source_name'">{{ getSourceNameText(record.source_name) }}</span>
              <div v-else-if="column.key === 'action'" class="row-actions">
                <a-button :disabled="!canViewQuestionDetail()" :onClick="questionDetailEntry.bind(openQuestionDetail, record)">详情</a-button>
                <a-button type="primary" ghost :onClick="questionEntry.bind(showModal, record, 1)">编辑</a-button>
                <a-button type="primary" ghost danger :onClick="questionEntry.bind(deleteQuestion, record.id)">删除</a-button>
              </div>
            </template>
            <template #emptyText>
              <a-empty v-if="!tabLoading && !listFailure && canUseQuestions()" description="暂无数据" />
              <p v-else class="hint" role="status">
                {{ !canUseQuestions() ? '题目列表尚未确认，请使用管理员账号登录。' : tabLoading ? '正在读取题目列表，请稍候。' : '暂时无法读取题目列表，请使用上方重试。' }}
              </p>
            </template>
          </a-table>
          </ConsoleTableShell>
          <a-pagination :onChange="questionEntry.bind(onChange)" :current="page.pageNo" :total="page.total" show-size-changer
                        :page-size-options="page.pageSizeOptions" :page-size="page.pageSize"
                        :onShowSizeChange="questionEntry.bind(onShowSizeChange)"
                        style="margin-top: 20px; text-align: right;"/>
      </a-card>
      <a-drawer v-if="questionDetailOwner" :key="questionDetailKey" :open="questionDetailOpen"
                :width="'min(720px, 100vw)'" :closable="false" :autofocus="false" :destroy-on-close="true"
                :body-style="{ overflowY: 'auto', overflowX: 'hidden' }"
                :onClose="questionDetailOwner.close" :onAfterOpenChange="questionDetailOwner.afterOpenChange">
        <template #title><span :id="fieldId('detail-title-' + questionDetailKey)">题目详情</span></template>
        <template #extra><a-button data-question-detail-close :onClick="questionDetailOwner.close">关闭详情</a-button></template>
        <div ref="questionDetailBody" class="question-detail-content">
          <p class="question-detail-hint">仅查看，不修改题目。</p>
          <section v-for="field in questionDetailFields" :key="field.key" class="question-detail-field">
            <h3>{{ field.label }}</h3>
            <template v-if="field.values">
              <ol v-if="field.values.length" class="question-detail-values">
                <li v-for="(value, index) in field.values" :key="index">{{ formatQuestionDetailValue(value) }}</li>
              </ol>
              <p v-else class="question-detail-hint">暂无{{ field.label }}</p>
              <details class="question-detail-raw">
                <summary>原始内容</summary>
                <pre>{{ field.text }}</pre>
              </details>
            </template>
            <pre v-else>{{ field.text }}</pre>
          </section>
        </div>
      </a-drawer>
      <a-modal
        :open="bulkCourseVisible"
        :wrap-props="{ 'aria-modal': true }"
        title="批量编辑课程名称"
        ok-text="保存"
        cancel-text="取消"
        :focusTriggerAfterClose="false"
        :afterClose="bulkCourseOwner && bulkCourseOwner.afterClose"
        :onOk="bulkCourseOwner && bulkCourseOwner.bind(handleBulkCourseSubmit, bulkCourseOwner)"
        :onCancel="bulkCourseOwner && bulkCourseOwner.bind(closeBulkCourseModal, bulkCourseOwner)"
      >
        <a-form ref="bulkCourseDialogForm" :onInputCapture="bulkCourseOwner && bulkCourseOwner.guardNative"
                :onChangeCapture="bulkCourseOwner && bulkCourseOwner.guardNative">
          <a-form-item label="已选题目">
            <span>{{ selectedRowKeys.length }} 条</span>
          </a-form-item>
          <a-form-item label="课程名称" :html-for="fieldId('bulk-course')">
            <a-auto-complete
              :id="fieldId('bulk-course')"
              :value="bulkCourseName" v-on="{ 'update:value': bulkCourseOwner && bulkCourseOwner.bind(value => bulkCourseName = value) }"
              :options="bulkCourseCandidates"
              placeholder="请输入新的课程名称"
              style="width: 100%"
            >
              <a-input :aria-describedby="fieldId('bulk-course-hint')" />
            </a-auto-complete>
            <div :id="fieldId('bulk-course-hint')" class="bulk-course-hint">支持自由输入；若已存在相近课程名，会在下方显示候选项供快速填充。</div>
          </a-form-item>
        </a-form>
      </a-modal>
      <input
        :key="'json-input-' + questionEntry.epoch" ref="jsonImportInput"
        type="file"
        accept="application/json,.json"
        style="display: none"
        :onChange="questionEntry.bind(handleJsonImportChange)"
      />
      <a-modal :key="questionDialogKey" :open="visible" title="详情内容" :wrap-props="{ 'aria-modal': true }" :onOk="questionDialogOwner && questionDialogOwner.bind(confirm, questionDialogOwner)"
               :onCancel="questionDialogOwner && questionDialogOwner.bind(closeQuestionModal, questionDialogOwner)" v-if="questionDialogMounted"
               :confirmLoading="questionSubmitLoading" :ok-button-props="{ disabled: questionWriteUnconfirmed }"
               :focusTriggerAfterClose="false" :afterClose="questionDialogAfterClose">
        <section v-if="questionWriteFailure" data-question-write-feedback>
          <RequestFailureAlert :failure="questionWriteFailure" :message="questionWriteFailureMessage"
            :context-key="questionWriteFailureContext" :is-current="isCurrentQuestionWriteFailure" />
          <a-button html-type="button" data-question-write-check :onClick="questionWriteCheck"
            :loading="tabLoading" :disabled="!isCurrentQuestionWriteFailure(questionWriteFailure, questionWriteFailureContext) || tabLoading">仅刷新列表核对</a-button>
          <p v-if="questionWriteUnconfirmed" class="hint">这里只读取当前筛选的题目列表，不能确认完整写入结果。请关闭弹窗核对，勿直接重复提交；重新打开编辑是一次新的手动操作。</p>
        </section>
        <a-form ref="questionDialogForm" :model="formData"
                :onInputCapture="questionDialogOwner && questionDialogOwner.guardNative"
                :onChangeCapture="questionDialogOwner && questionDialogOwner.guardNative">
          <a-form-item label="类型" :html-for="fieldId('editor-type-' + questionDialogKey)"
                       :validate-status="questionTypeError ? 'error' : undefined">
            <a-select ref="questionTypeControl" :id="fieldId('editor-type-' + questionDialogKey)" :value="formData.type"
                      v-on="{ 'update:value': questionDialogOwner && questionDialogOwner.bind(value => formData.type = value) }">
              <a-select-option v-for="(value, key) in quesType" :key="key" :value="parseInt(key)">
                {{ value }}
              </a-select-option>
            </a-select>
            <template v-if="questionTypeError" #help>
              <span :id="fieldId('editor-type-error-' + questionDialogKey)" role="alert">{{ questionTypeError }}</span>
            </template>
          </a-form-item>
          <a-form-item label="问题" :html-for="fieldId('editor-question-' + questionDialogKey)" class="margin_top">
            <a-textarea :id="fieldId('editor-question-' + questionDialogKey)" :value="formData.question" v-on="{ 'update:value': questionDialogOwner && questionDialogOwner.bind(value => formData.question = value) }"/>
          </a-form-item>
          <div v-if="isObjectiveQuestionType(formData.type)" class="margin_top">
            <a-form-item label="选项">
              <OptionBox :options="parseQuestionAnswerValues(formData.options)" :type="normalizeEditorQuestionType(formData.type)"
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
                                      action='/api/upload?parentDir=myword' :before-upload="questionDialogOwner && questionDialogOwner.beforeUpload" :onChange="questionDialogOwner && questionDialogOwner.bind(handleChange)">
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
import {computed, defineComponent, onBeforeUnmount, ref, shallowRef, watch, useId} from 'vue';
import {useRouter} from 'vue-router';
import Swal from 'sweetalert2'
import {
  updateQuestions,
  delQuestions,
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
import {useQuestionDetail} from './useQuestionDetail.mjs'
import {useQuestionSelection} from './useQuestionSelection.mjs'
import {useQuestionReads} from './useQuestionReads.mjs'
import {useQuestionEditor, questionEditorMethods} from './useQuestionEditor.mjs'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import TableColumnSettings from '@/components/TableColumnSettings.vue'
import { useTableColumnPreferences } from '@/composables/useTableColumnPreferences.mjs'
import OverflowTableText from '@/components/OverflowTableText.vue'
import { useCurrentUserDisplay, isAdminUser } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { useTableExpansion } from '@/composables/useTableExpansion.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'
import { buildQuestionAnswerText, parseQuestionAnswerValues, isObjectiveQuestionType, normalizeEditorQuestionType } from '@/utils/questionAnswerDisplay.mjs'
import { ARCHIVE_REASON_PRESETS, normalizeArchiveReason, validateArchiveReason } from '@/utils/questionArchiveReason.mjs'
import {questionType} from "@/utils/uitls";
import {message, Drawer} from "ant-design-vue";
import {
  buildQuestionBulkActionIds,
  buildQuestionBulkCourseUpdatePayload,
  buildQuestionCourseNameCandidates,
  buildQuestionExportPayload,
  buildQuestionImportDryRunPayload,
  buildQuestionImportExecutePayload,
  buildQuestionImportSummary,
  buildQuestionSourceLabel,
  parseQuestionImportText,
} from '@/utils/topicOverviewHelpers.mjs';

const style = {
  padding: "24px"
}
export default defineComponent({
  name: 'TikuComponent',
  setup() {
    const fieldIdPrefix = 'tiku-fields-' + useId()
    const fieldId = field => fieldIdPrefix + '-' + field
    const tabLoading = ref(false)
    const data = ref([])
    const page = ref({
      pageSizeOptions: ['10', '20', '50', '100'],
      pageNo: 1,
      pageSize: 10,
      total: 0
    })

    const typeValue = ref(null)
    const typeList = ref([])
    const courses = ref([])
    const bulkCourseVisible = ref(false)
    const bulkCourseName = ref('')
    const searchValue = ref({})
    const quesType = ref(questionType)
    const jsonImportInput = ref(null)

    const router = useRouter();
    const { currentUser } = useCurrentUserDisplay()
    const principal = () => currentUserIdentity(currentUser.value)
    const role = () => String(currentUser.value?.role || '').trim().toLowerCase()
    const canUseQuestions = () => Boolean(principal()) && isAdminUser(currentUser.value)
    const questionRequestOwner = { active: true, epoch: 0, listVersion: 0, courseVersion: 0, selectionVersion: 0, reload: null }
    const captureQuestionOwner = () => {
      const epoch = questionRequestOwner.epoch
      const identity = principal()
      const privilege = role()
      return () => questionRequestOwner.active && epoch === questionRequestOwner.epoch
        && Boolean(identity) && identity === principal() && privilege === role() && canUseQuestions()
    }
    const makeEntry = () => {
      const isCurrent = captureQuestionOwner()
      return { epoch: questionRequestOwner.epoch, isCurrent,
        guardNative: event => guardQuestionEvent(isCurrent, event),
        bind: (fn, ...args) => (...events) => isCurrent() ? fn(...args, ...events) : undefined }
    }
    const guardQuestionEvent = (isCurrent, event) => {
      if (isCurrent()) return
      event.preventDefault()
      event.stopImmediatePropagation()
    }
    // Each render closes over this entry, so a native event on old DOM in the same
    // tick cannot borrow the next principal's authority before Vue patches it.
    const questionEntry = shallowRef(makeEntry())
    const {
      resetSelectionState, rememberQuestionRows, selectedRowKeys, selectedQuestions,
      rowSelection, selectionSummary, allRowsSelected, allCurrentPageSelected,
      hasCurrentPageSelection, selectAllLoading, handleSelectCurrentPage,
      handleClearCurrentPage, handleSelectAll, handleClearSelection,
    } = useQuestionSelection({
      data, page, typeValue, searchValue, questionEntry,
      captureQuestionOwner, questionRequestOwner,
    })
    const bulkCourseOwner = shallowRef(null)
    const jsonImportOwner = shallowRef(null)
    const questionPrompts = new Set()
    const {
      getCourses, fetchData, listFailure, listFailureContext,
      isCurrentListFailure,
    } = useQuestionReads({
      data, courses, tabLoading, page, typeValue, searchValue,
      questionRequestOwner, captureQuestionOwner, questionEntry, rememberQuestionRows,
    })
    const { isExpanded, toggle: toggleExpanded, columnWidth } = useTableExpansion(data)
    const columns = computed(() => [
      { title: 'ID', dataIndex: 'id', key: 'id', width: 90 },
      { title: '类型', dataIndex: 'type', key: 'type', width: 100 },
      { title: '问题', dataIndex: 'question', key: 'question', width: columnWidth('question') },
      { title: '答案', dataIndex: 'answer', key: 'answer', width: columnWidth('answer') },
      { title: '数据来源', dataIndex: 'source_name', key: 'source_name', width: 160 },
      { title: '拓展参数', dataIndex: 'extra', key: 'extra', width: columnWidth('extra') },
      { title: '操作', key: 'action', fixed: 'right', width: 250 },
    ].map(column => ({ ...column, align: 'center' })))
    const columnPriorityKeys = ['id', 'type', 'question', 'answer', 'action']
    const { visibleColumns, selectedKeys: selectedColumnKeys, columnOptions, scopeKey: columnScopeKey,
      setSelectedKeys: setSelectedColumnKeys, resetColumns } = useTableColumnPreferences({
      pageKey: 'TikuComponent', columns,
      scope: () => currentUser.value?.id ? `id:${currentUser.value.id}:role:${currentUser.value.role}:view:questions` : '',
      requiredKeys: ['id', 'question', 'answer', 'action'], priorityKeys: columnPriorityKeys,
    })
    const tableScrollX = computed(() => sumTableColumnWidths(visibleColumns.value, 48))
    const {
      fileList, formData, answer, visible, questionAction,
      questionDialogOwner, questionSubmitLoading, questionUploads,
      questionDialogKey, questionDialogMounted, questionDialogForm,
      questionDialogAfterClose, questionDialogDraining, awaitQuestionDialogDrain,
      captureQuestionDialogOpener, questionTypeControl, questionTypeError,
      clearQuestionTypeError, showQuestionTypeError,
      questionWriteFailure, questionWriteFailureContext, questionWriteFailureMessage,
      questionWriteCheck, questionWriteUnconfirmed, isCurrentQuestionWriteFailure,
      clearQuestionWriteFailure, recordQuestionWriteFailure,
      retireQuestionEditorDialog, deactivateQuestionEditorFocus,
    } = useQuestionEditor({ captureQuestionOwner, fieldId, fetchData, tabLoading })
    const closeQuestionPrompts = () => {
      for (const prompt of questionPrompts) {
        // Never dismiss an unrelated page's later SweetAlert.
        if (prompt.popup && Swal.getPopup?.() === prompt.popup) Swal.close()
      }
      questionPrompts.clear()
    }
    const showQuestionPrompt = async (options, isCurrent) => {
      if (!isCurrent()) return null
      const prompt = { popup: null }
      questionPrompts.add(prompt)
      try {
        const result = Swal.fire({ ...options, returnFocus: false })
        prompt.popup = Swal.getPopup?.()
        const response = await result
        return isCurrent() ? response : null
      } finally {
        questionPrompts.delete(prompt)
      }
    }
    const retireQuestionScope = () => {
      clearQuestionTypeError()
      clearQuestionWriteFailure()
      questionRequestOwner.epoch += 1
      questionRequestOwner.listVersion += 1
      questionRequestOwner.courseVersion += 1
      listFailure.value = null
      listFailureContext.value += 1
      retireQuestionEditorDialog()
      bulkCourseOwner.value = null
      jsonImportOwner.value = null
      questionDialogAfterClose.value = null
      visible.value = false
      bulkCourseVisible.value = false
      bulkCourseName.value = ''
      formData.value = {}
      searchValue.value = {}
      typeValue.value = null
      answer.value = []
      fileList.value = []
      questionUploads.clear()
      if (jsonImportInput.value) jsonImportInput.value.value = ''
      data.value = []
      courses.value = []
      page.value.pageNo = 1
      page.value.total = 0
      tabLoading.value = false
      resetSelectionState()
      closeQuestionPrompts()
      questionEntry.value = makeEntry()
    }
    watch(() => [principal(), role()], (next, previous) => {
      if (next[0] === previous[0] && next[1] === previous[1]) return
      retireQuestionScope()
      if (canUseQuestions()) questionRequestOwner.reload?.()
    }, { flush: 'sync' })
    onBeforeUnmount(() => {
      questionRequestOwner.active = false
      questionRequestOwner.reload = null
      deactivateQuestionEditorFocus()
      retireQuestionScope()
    })
    const {
      canViewQuestionDetail, questionDetailEntry, openQuestionDetail,
      questionDetailOpen, questionDetailOwner, questionDetailBody, questionDetailKey,
      questionDetailFields, formatQuestionDetailValue,
    } = useQuestionDetail({
      currentUser, principal, role, canUseQuestions, captureQuestionOwner,
      guardQuestionEvent, fieldId, quesType,
    })
    const navigateToImport = () => {
      if (!questionEntry.value.isCurrent()) return
      router.push('/import');
    };
    const bulkCourseCandidates = computed(() => buildQuestionCourseNameCandidates(
      bulkCourseName.value,
      courses.value,
    ))
    return {
      fieldId, questionTypeControl, questionTypeError, clearQuestionTypeError, showQuestionTypeError,
      canViewQuestionDetail, questionDetailEntry, openQuestionDetail,
      questionDetailOpen, questionDetailOwner, questionDetailBody, questionDetailKey,
      questionDetailFields, formatQuestionDetailValue,
      questionRequestOwner, questionEntry, captureQuestionOwner, canUseQuestions, guardQuestionEvent,
      questionDialogOwner, questionSubmitLoading, bulkCourseOwner, jsonImportOwner, questionUploads,
      showQuestionPrompt, resetSelectionState, rememberQuestionRows,
      getCourses, fetchData,
      listFailure, listFailureContext, isCurrentListFailure,
      questionWriteFailure, questionWriteFailureContext, questionWriteFailureMessage,
      questionWriteCheck, questionWriteUnconfirmed, isCurrentQuestionWriteFailure,
      clearQuestionWriteFailure, recordQuestionWriteFailure,
      questionDialogKey,
      questionDialogMounted,
      questionDialogForm,
      questionDialogAfterClose,
      questionDialogDraining, awaitQuestionDialogDrain,
      captureQuestionDialogOpener,
      tabLoading,
      typeList,
      typeValue,
      answer,
      page,
      data,
      columns, visibleColumns, selectedColumnKeys, columnOptions, columnScopeKey,
      columnPriorityKeys, setSelectedColumnKeys, resetColumns,
      tableScrollX,
      isExpanded,
      toggleExpanded,
      buildQuestionAnswerText,
      parseQuestionAnswerValues, isObjectiveQuestionType, normalizeEditorQuestionType,
      quesType,
      visible,
      formData,
      questionAction,
      style,
      searchValue,
      selectedRowKeys,
      selectedQuestions,
      courses,
      bulkCourseVisible,
      bulkCourseName,
      bulkCourseCandidates,
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
    ADrawer: Drawer,
    UploadOutlined,
    OptionBox,
    AnswerBox,
    FormOutlined,
    SearchOutlined,
    DownloadOutlined,
    RequestFailureAlert, TableColumnSettings, OverflowTableText
  },
  mounted() {
    this.questionRequestOwner.reload = () => {
      this.getCourses()
      this.fetchData()
    }
    this.getType()
    this.questionRequestOwner.reload()
  },
  methods: {
    guardedRetryListFailure(failure, contextKey) {
      if (this.isCurrentListFailure(failure, contextKey)) this.fetchData()
    },
    async onFilterChange() {
      if (!this.questionEntry.isCurrent()) return
      this.page.pageNo = 1
      this.resetSelectionState()
      await this.fetchData()
    },
    ...questionEditorMethods,
    typeObjects(types) {
      return Object.entries(types).map(([key, value]) => ({ value: parseInt(key), label: value }))
    },
    getType() {
      this.typeList = this.typeObjects(questionType)
    },
    onSearch() {
      if (!this.questionEntry.isCurrent()) return
      this.page.pageNo = 1
      this.resetSelectionState()
      this.fetchData()
    },
    onShowSizeChange(current, pageSize) {
      if (!this.questionEntry.isCurrent()) return
      this.page.pageSize = pageSize
      this.page.pageNo = 1
      this.fetchData()
    },
    onChange(page) {
      if (!this.questionEntry.isCurrent()) return
      this.page.pageNo = page
      this.fetchData()
    },
    async deleteQuestion(id) {
      const isCurrent = this.captureQuestionOwner()
      if (!isCurrent()) return
      // 删除原因（Q77-6 定案 C：预设下拉 + 可自定义输入）。
      //
      // 用 Swal 原生的 input + inputOptions 做「下拉可选、也能手填」，
      // 而不是在页面里另插 a-select：① 本仓没有 dayjs/DatePicker 那套依赖；
      // ② 弹窗是 Swal 的 DOM，混入 AntD 控件需要额外挂载与卸载逻辑；
      // ③ 追加独立的 form-item 会踩「位置选择器取第一个」的既有陷阱。
      //
      // 校验放在 preConfirm 里（按**字符**数，与后端 archiveReasonMinLength 一致），
      // 不合法时 Swal 保持打开并显示同一句文案 —— 不发请求。
      const result = await this.showQuestionPrompt({
        icon: 'warning',
        title: '删除题目',
        text: '确定删除这道题目？归档后可在归档列表恢复。',
        input: 'text',
        inputOptions: {
          // 预设与后端 archiveReasonCode（tiku_archive.go:33-45）逐字对齐。
          presets: ARCHIVE_REASON_PRESETS,
        },
        inputPlaceholder: '请填写删除原因（至少 2 个字符），或从下拉中选择',
        inputValidator: value => validateArchiveReason(value),
        showCancelButton: true,
        confirmButtonText: '确定删除',
        cancelButtonText: '取消',
        heightAuto: false,
        scrollbarPadding: false,
      }, isCurrent)
      if (!isCurrent() || !result?.isConfirmed) return
      const reason = normalizeArchiveReason(result.value)
      try {
        const res = await delQuestions({ id }, reason)
        if (!isCurrent()) return
        if (res?.status !== 200) {
          // 固定文案，不回显后端的 message：既有 TF02 用例钉的就是这一条
          // （「失败时页面必须出现『删除失败』」），且后端 message 可能含原因校验细节。
          message.error('删除失败')
          return
        }
        message.success('已归档')
        this.selectedRowKeys = this.selectedRowKeys.filter(item => Number(item) !== Number(id))
        await this.fetchData()
      } catch {
        if (isCurrent()) message.error('删除失败')
      }
    },
    getSourceNameText(sourceName) {
      return buildQuestionSourceLabel(sourceName)
    },
    triggerJsonImport() {
      const ownsPrincipal = this.captureQuestionOwner()
      if (!ownsPrincipal()) return
      const input = this.$refs.jsonImportInput
      if (!input) return
      const owner = { input, isCurrent: () => ownsPrincipal() && this.jsonImportOwner === owner }
      this.jsonImportOwner = owner
      input.value = ''
      input.click()
    },
    async handleJsonImportChange(event) {
      const input = event?.target
      const file = input?.files?.[0]
      const ownsPrincipal = this.captureQuestionOwner()
      if (!ownsPrincipal() || !file) return
      // Native changes arrive through the captured render entry. The input owner
      // additionally covers a late file picker and each subsequent async stage.
      let owner = this.jsonImportOwner
      if (!owner) {
        owner = { input, isCurrent: () => ownsPrincipal() && this.jsonImportOwner === owner }
        this.jsonImportOwner = owner
      }
      if (owner.input !== input || !owner.isCurrent()) return
      const isCurrent = owner.isCurrent
      try {
        const content = await file.text()
        if (!isCurrent()) return
        const items = parseQuestionImportText(content)
        if (!items.length) {
          message.warning('JSON 文件中没有可导入的题目')
          return
        }
        const dryRunResponse = await importQuestions(buildQuestionImportDryRunPayload(items))
        if (!isCurrent()) return
        if (dryRunResponse?.status !== 200) {
          message.error(dryRunResponse?.message || '导入预检失败')
          return
        }
        const dryRunResult = dryRunResponse?.data || {}
        let overwrite = false
        if (Array.isArray(dryRunResult.existingIds) && dryRunResult.existingIds.length) {
          const confirmResult = await this.showQuestionPrompt({
            icon: 'warning', title: '检测到重复题目', text: buildQuestionImportSummary(dryRunResult),
            showDenyButton: true, showCancelButton: true, confirmButtonText: '是，直接覆盖',
            denyButtonText: '否，只导入不存在的数据', cancelButtonText: '取消',
            heightAuto: false, scrollbarPadding: false,
          }, isCurrent)
          if (!isCurrent() || !confirmResult || confirmResult.isDismissed) return
          overwrite = Boolean(confirmResult.isConfirmed)
        }
        if (!isCurrent()) return
        const executeResponse = await importQuestions(buildQuestionImportExecutePayload(items, overwrite))
        if (!isCurrent()) return
        if (executeResponse?.status !== 200) {
          message.error(executeResponse?.message || '导入失败')
          return
        }
        const executeResult = executeResponse?.data || {}
        const summary = `导入完成：新增 ${Number(executeResult.created || 0)} 条，覆盖 ${Number(executeResult.updated || 0)} 条，跳过 ${Number(executeResult.skipped || 0)} 条。`
        await this.showQuestionPrompt({
          icon: 'success', title: 'JSON 导入成功', text: summary, confirmButtonText: '我知道了',
          heightAuto: false, scrollbarPadding: false,
        }, isCurrent)
        if (isCurrent()) await this.fetchData()
      } catch (error) {
        if (isCurrent()) message.error(error?.message || 'JSON 导入失败')
      } finally {
        if (isCurrent()) {
          if (input) input.value = ''
          this.jsonImportOwner = null
        }
      }
    },
    openBulkCourseModal(event) {
      const ownsPrincipal = this.captureQuestionOwner()
      if (!ownsPrincipal()) return
      if (!this.selectedRowKeys.length) {
        message.warning('请先选择需要批量修改的题目')
        return
      }
      const owner = {
        epoch: this.questionRequestOwner.epoch,
        opener: event?.currentTarget || document.activeElement,
        dialog: null,
        isCurrent: () => ownsPrincipal() && this.bulkCourseOwner === owner && this.bulkCourseVisible,
        bind: (fn, ...args) => (...events) => owner.isCurrent() ? fn(...args, ...events) : undefined,
        guardNative: event => this.guardQuestionEvent(owner.isCurrent, event),
        afterClose: () => {
          if (!ownsPrincipal() || this.bulkCourseOwner !== owner || this.bulkCourseVisible) return
          const opener = owner.opener
          const active = document.activeElement
          // A later portal may be opening before its enter transition takes focus.
          if ([...document.querySelectorAll('[role="dialog"]')].some(dialog => {
            if (dialog === owner.dialog) return false
            for (let node = dialog; node; node = node.parentElement) {
              const style = window.getComputedStyle(node)
              if (node.hidden || node.inert || node.getAttribute('aria-hidden') === 'true'
                || style.display === 'none' || style.visibility === 'hidden') return false
            }
            return true
          })) return
          if (!(opener instanceof HTMLElement) || !opener.isConnected || opener.tabIndex < 0
            || opener.matches(':disabled, [aria-disabled="true"]')
            || (active && active !== document.body && active !== opener && !owner.dialog?.contains(active))) return
          for (let node = opener; node; node = node.parentElement) {
            const style = window.getComputedStyle(node)
            if (node.hidden || node.inert || node.getAttribute('aria-hidden') === 'true'
              || style.display === 'none' || style.visibility === 'hidden') return
          }
          opener.focus({ preventScroll: true })
        },
      }
      this.bulkCourseOwner = owner
      this.bulkCourseName = ''
      this.bulkCourseVisible = true
      this.$nextTick(() => {
        if (!owner.isCurrent()) return
        const form = this.$refs.bulkCourseDialogForm?.$el
        const dialog = form?.closest('[role="dialog"]')
        owner.dialog = dialog
        if (!form?.isConnected || !(dialog instanceof HTMLElement) || !dialog.isConnected
          || !dialog.hasAttribute('tabindex') || dialog.matches(':disabled, [aria-disabled="true"]')) return
        const isVisible = element => {
          for (let node = element; node; node = node.parentElement) {
            const style = window.getComputedStyle(node)
            if (node.hidden || node.inert || node.getAttribute('aria-hidden') === 'true'
              || style.display === 'none' || style.visibility === 'hidden') return false
          }
          return true
        }
        if (!isVisible(dialog) || [...document.querySelectorAll('[role="dialog"]')]
          .some(other => other !== dialog && isVisible(other))) return
        const active = document.activeElement
        // Do not replace an existing own control or a later foreground action.
        if (dialog.contains(active) || (active !== document.body && active !== owner.opener)) return
        dialog.focus({ preventScroll: true })
      })
    },
    closeBulkCourseModal(dialog = this.bulkCourseOwner) {
      if (!dialog?.isCurrent()) return
      this.bulkCourseVisible = false
      this.bulkCourseName = ''
    },
    async handleBulkCourseSubmit(dialog = this.bulkCourseOwner) {
      if (!dialog?.isCurrent()) return
      const isCurrent = dialog.isCurrent
      let payload
      try {
        payload = buildQuestionBulkCourseUpdatePayload(this.selectedRowKeys, this.bulkCourseName)
      } catch (error) {
        if (isCurrent()) message.warning(error?.message || '请输入课程名称')
        return
      }
      const rows = this.selectedQuestions.map(item => ({ ...item }))
      try {
        for (const item of rows) {
          if (!isCurrent()) return
          const response = await updateQuestions({ ...item, course_name: payload.courseName })
          if (!isCurrent()) return
          if (response?.status !== 200) {
            message.error(`题目 ${item.id} 更新课程名称失败`)
            return
          }
        }
        if (!isCurrent()) return
        message.success(`成功更新 ${payload.ids.length} 条题目的课程名称`)
        if (String(this.bulkCourseName || '').trim() === payload.courseName) {
          this.closeBulkCourseModal(dialog)
          this.resetSelectionState()
        }
        await this.fetchData()
      } catch {
        if (isCurrent()) message.error('批量更新课程名称失败')
      }
    },
    handleExportSelected() {
      const isCurrent = this.captureQuestionOwner()
      if (!isCurrent()) return
      const rows = this.selectedQuestions
      if (!rows.length) {
        message.warning('请先选择需要导出的题目')
        return
      }
      const payload = buildQuestionExportPayload(rows)
      const blob = new Blob([payload.content], { type: 'application/json;charset=utf-8' })
      if (!isCurrent()) return
      const objectUrl = window.URL.createObjectURL(blob)
      const link = document.createElement('a')
      try {
        link.href = objectUrl
        link.download = payload.filename
        document.body.appendChild(link)
        if (!isCurrent()) return
        link.click()
        if (isCurrent()) message.success(`已导出 ${rows.length} 条题目`)
      } finally {
        link.remove()
        window.URL.revokeObjectURL(objectUrl)
      }
    },
    async handleDeleteSelected() {
      const isCurrent = this.captureQuestionOwner()
      if (!isCurrent()) return
      const ids = buildQuestionBulkActionIds(this.selectedRowKeys)
      if (!ids.length) {
        message.warning('请先选择需要删除的题目')
        return
      }
      const result = await this.showQuestionPrompt({
        icon: 'warning', title: '批量删除题目', text: `确定删除已选的 ${ids.length} 道题目？此操作不可撤销。`,
        showCancelButton: true, confirmButtonText: '确定删除', cancelButtonText: '取消',
        heightAuto: false, scrollbarPadding: false,
      }, isCurrent)
      if (!isCurrent() || !result?.isConfirmed) return
      try {
        for (const id of ids) {
          if (!isCurrent()) return
          const res = await delQuestions({ id })
          if (!isCurrent()) return
          if (res?.status !== 200) {
            message.error(`题目 ${id} 删除失败`)
            return
          }
        }
        if (!isCurrent()) return
        message.success(`成功删除 ${ids.length} 条题目`)
        this.resetSelectionState()
        await this.fetchData()
      } catch {
        if (isCurrent()) message.error('批量删除失败')
      }
    },
  }
})
</script>
<style scoped>
.filter-panel {
  padding: 16px;
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
.tiku-layout :deep(.ant-layout-content) {
  min-width: 0;
  width: 100%;
  background: transparent;
}

.selection-alert {
  margin: 0 0 16px;
}

.selection-alert-content {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.bulk-course-hint { margin-top: 8px; color: var(--console-muted); font-size: 12px; }
.row-actions {
  display: flex;
  justify-content: center;
  flex-wrap: wrap;
  gap: 8px;
}


.question-detail-content,
.question-detail-field { min-width: 0; }
.question-detail-field + .question-detail-field { margin-top: 20px; }
.question-detail-field h3 { margin: 0 0 8px; font-size: 14px; color: var(--console-text); }
.question-detail-field pre,
.question-detail-values li {
  margin: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font: inherit;
  color: var(--console-text);
}
.question-detail-values { margin: 0; padding-inline-start: 24px; }
.question-detail-values li + li { margin-top: 8px; }
.question-detail-hint,
.question-detail-raw { color: var(--console-muted); font-size: 12px; }
.question-detail-raw { margin-top: 12px; }
.question-detail-raw summary { cursor: pointer; padding-block: 4px; }
.question-detail-raw pre { margin-top: 8px; }

@media (max-width: 640px) {
  .filter-form,
  .filter-form :deep(.ant-form-item),
  .filter-form :deep(.ant-input),
  .filter-form :deep(.ant-select) {
    width: 100%;
  }

}
</style>
