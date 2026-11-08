// Page-local editor state and unchanged Options-API methods. Parent retains
// the one page authority, shared retirement order and original lifecycle hooks.
import {computed, nextTick, ref, shallowRef, watch} from 'vue'
import {message, Upload} from 'ant-design-vue'
import {createQuestions, updateQuestions} from '@/api/TikuComponentApi'
import {buildRequestFailureView, ensureRequestFailure} from '@/utils/requestFailureView.mjs'
import {normalizeEditorQuestionType, parseQuestionAnswerValues, isObjectiveQuestionType} from '@/utils/questionAnswerDisplay.mjs'

export function useQuestionEditor({ captureQuestionOwner, fieldId, fetchData, tabLoading }) {
    const fileList = ref([])
    const formData = ref({})
    const answer = ref([])
    const visible = ref(false)
    const questionAction = ref(2) // 1是编辑 2是添加
    const questionDialogOwner = shallowRef(null)
    const questionSubmitLoading = ref(false)
    const questionUploads = new Map()
    const questionDialogKey = ref(0)
    const questionDialogMounted = ref(false)
    const questionDialogForm = ref(null)
    const questionDialogAfterClose = shallowRef(null)
    const questionDialogDraining = ref(false)
    let questionDialogDrain = Promise.resolve()
    let questionDialogDrainToken = null
    const awaitQuestionDialogDrain = () => questionDialogDrain
    let questionFocusOwner = null
    let questionFocusAlive = true
    const captureQuestionDialogOpener = (event) => {
      const owner = {
        isCurrent: captureQuestionOwner(),
        opener: event?.currentTarget || document.activeElement,
        dialog: null,
        epoch: ++questionDialogKey.value,
      }
      questionFocusOwner = owner
      questionDialogMounted.value = true
      questionDialogAfterClose.value = () => {
        if (!questionFocusAlive || !owner.isCurrent() || questionFocusOwner !== owner
          || owner.epoch !== questionDialogKey.value || visible.value) return
        questionFocusOwner = null
        questionDialogMounted.value = false
        const opener = owner.opener
        const active = document.activeElement
        const isVisible = (element) => {
          for (let node = element; node; node = node.parentElement) {
            const style = window.getComputedStyle(node)
            if (node.hidden || node.inert || node.getAttribute('aria-hidden') === 'true'
              || style.display === 'none' || style.visibility === 'hidden') return false
          }
          return true
        }
        // Another portal can be opening before its enter transition takes focus.
        if ([...document.querySelectorAll('[role="dialog"]')]
          .some(dialog => dialog !== owner.dialog && isVisible(dialog))) return
        // A later user action or another dialog owns any outside focus.
        if (active && active !== document.body && active !== opener
          && !owner.dialog?.contains(active)) return
        if (!(opener instanceof HTMLElement) || !opener.isConnected
          || opener.tabIndex < 0 || opener.matches(':disabled, [aria-disabled="true"]')
          || !isVisible(opener)) return
        opener.focus({ preventScroll: true })
      }
      nextTick(() => {
        if (owner.isCurrent() && questionFocusOwner === owner) {
          owner.dialog = questionDialogForm.value?.$el?.closest('[role="dialog"]')
        }
      })
    }
    const questionTypeControl = ref(null)
    const questionTypeValidationOwner = shallowRef(null)
    const questionTypeError = computed(() => questionTypeValidationOwner.value?.isCurrent()
      && normalizeEditorQuestionType(formData.value.type) === undefined
      ? '题型必须为32位整数，请重新选择。' : '')
    // Antd4 Select keeps extra ARIA attrs on its outer div, not its combobox.
    // Project only this field's owned error onto the real native input.
    let questionTypeProjection = null
    const releaseQuestionTypeProjection = () => {
      const projection = questionTypeProjection
      questionTypeProjection = null
      if (!projection) return
      const { input, errorId, previousInvalid } = projection
      const remaining = (input.getAttribute('aria-describedby') || '').split(/\s+/).filter(id => id && id !== errorId)
      if (remaining.length) input.setAttribute('aria-describedby', remaining.join(' '))
      else input.removeAttribute('aria-describedby')
      if (input.getAttribute('aria-invalid') === 'true') {
        if (previousInvalid === null) input.removeAttribute('aria-invalid')
        else input.setAttribute('aria-invalid', previousInvalid)
      }
    }
    const syncQuestionTypeProjection = () => {
      releaseQuestionTypeProjection()
      const owner = questionTypeValidationOwner.value
      const control = questionTypeControl.value
      const form = questionDialogForm.value?.$el
      const input = control?.$el?.querySelector('input[role="combobox"]')
      const errorId = fieldId('editor-type-error-' + questionDialogKey.value)
      if (!questionTypeError.value || !owner?.isCurrent() || !input || !form?.contains(input)
        || ![...form.querySelectorAll('[id]')].some(node => node.id === errorId)) return
      const previousInvalid = input.getAttribute('aria-invalid')
      const descriptions = new Set((input.getAttribute('aria-describedby') || '').split(/\s+/).filter(Boolean))
      descriptions.add(errorId)
      input.setAttribute('aria-describedby', [...descriptions].join(' '))
      input.setAttribute('aria-invalid', 'true')
      questionTypeProjection = { input, errorId, previousInvalid }
    }
    watch([questionTypeError, questionTypeControl], syncQuestionTypeProjection, { flush: 'post' })
    const clearQuestionTypeError = () => {
      questionTypeValidationOwner.value = null
      releaseQuestionTypeProjection()
    }
    const showQuestionTypeError = (dialog) => {
      if (!dialog?.isCurrent()) return
      questionTypeValidationOwner.value = dialog
      const control = questionTypeControl.value
      nextTick(() => {
        if (dialog.isCurrent() && questionTypeControl.value === control && questionTypeError.value) control?.focus?.()
      })
    }
    const questionWriteFailure = shallowRef(null)
    const questionWriteFailureContext = ref(0)
    const questionWriteFailureMessage = ref('')
    const questionWriteFailureOwner = shallowRef(null)
    const questionWriteCheck = shallowRef(null)
    const clearQuestionWriteFailure = () => {
      questionWriteFailure.value = null
      questionWriteFailureOwner.value = null
      questionWriteFailureMessage.value = ''
      questionWriteCheck.value = null
      questionWriteFailureContext.value += 1
    }
    const isCurrentQuestionWriteFailure = (failure, context) => Boolean(
      failure && failure === questionWriteFailure.value && context === questionWriteFailureContext.value
      && questionWriteFailureOwner.value?.isCurrent(),
    )
    const questionWriteUnconfirmed = computed(() => isCurrentQuestionWriteFailure(
      questionWriteFailure.value, questionWriteFailureContext.value,
    ) && buildRequestFailureView(questionWriteFailure.value)?.kind === 'unknown-outcome')
    const recordQuestionWriteFailure = (dialog, value, editing) => {
      if (!dialog?.isCurrent()) return
      const failure = ensureRequestFailure(value)
      const context = ++questionWriteFailureContext.value
      questionWriteFailureOwner.value = dialog
      questionWriteFailure.value = failure
      questionWriteFailureMessage.value = buildRequestFailureView(failure)?.kind === 'unknown-outcome'
        ? (editing ? '修改结果尚未确认' : '创建结果尚未确认')
        : (editing ? '修改失败' : '创建失败')
      // This is only the existing read-only list request, not a write receipt or
      // permission to replay the mutation. A captured old button retains its lane.
      questionWriteCheck.value = () => {
        if (isCurrentQuestionWriteFailure(failure, context) && !tabLoading.value) return fetchData()
      }
    }
    const retireQuestionEditorDialog = () => {
      if (questionDialogMounted.value) {
        const dialog = questionDialogForm.value?.$el?.closest('[role="dialog"]') || questionFocusOwner?.dialog
        // Retire authority and sensitive visibility synchronously, but let Antd's
        // already accepted native input nextTick finish before disposing its ref.
        if (dialog) {
          dialog.hidden = true
          dialog.style.display = 'none'
          if (dialog.contains(document.activeElement)) document.activeElement.blur?.()
        }
        const token = {}
        const key = questionDialogKey.value
        questionDialogDrainToken = token
        questionDialogDraining.value = true
        questionDialogDrain = nextTick().then(() => nextTick()).then(() => {
          if (questionDialogDrainToken !== token) return
          if (questionDialogKey.value === key && !visible.value) {
            questionDialogMounted.value = false
            questionDialogKey.value += 1
          }
          questionDialogDraining.value = false
        })
      }
      questionFocusOwner = null
      questionDialogOwner.value = null
      questionSubmitLoading.value = false
    }
    const deactivateQuestionEditorFocus = () => {
      questionFocusAlive = false
    }
    return {
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
    }
}

// Vue binds these methods to the parent instance; keep this.$refs live.
export const questionEditorMethods = {
    beforeQuestionUpload(file, dialog = this.questionDialogOwner) {
      if (!dialog?.isCurrent()) return Upload.LIST_IGNORE
      this.questionUploads.set(file.uid || file, dialog)
      return true
    },
    handleChange(info, dialog = this.questionDialogOwner) {
      const file = info?.file
      if (!dialog?.isCurrent() || this.questionUploads.get(file?.uid || file?.originFileObj || file) !== dialog) return
      if (file.status === 'done') {
        message.success(`${file.name} 上传成功`)
        this.fileList.push(file.response)
        this.$refs.answerBox?.pushAnswer(file.response)
        this.questionUploads.delete(file.uid || file.originFileObj || file)
      } else if (file.status === 'error') {
        message.error(`${file.name} 上传失败`)
        this.questionUploads.delete(file.uid || file.originFileObj || file)
      }
    },
    showModal(record, act, event) {
      const ownsPrincipal = this.captureQuestionOwner()
      if (!ownsPrincipal()) return
      if (this.questionDialogDraining) {
        const draft = JSON.parse(JSON.stringify(record || {}))
        this.awaitQuestionDialogDrain().then(() => {
          if (ownsPrincipal()) this.showModal(draft, act, event)
        })
        return
      }
      this.clearQuestionTypeError()
      this.clearQuestionWriteFailure()
      this.captureQuestionDialogOpener(event)
      const owner = {
        epoch: this.questionDialogKey,
        ownsPrincipal,
        pending: false,
        isCurrent: () => ownsPrincipal() && this.questionDialogOwner === owner && this.visible,
        bind: (fn, ...args) => (...events) => owner.isCurrent() ? fn(...args, ...events) : undefined,
        guardNative: event => this.guardQuestionEvent(owner.isCurrent, event),
        beforeUpload: file => this.beforeQuestionUpload(file, owner),
      }
      this.questionDialogOwner = owner
      this.questionSubmitLoading = false
      // Editing is a draft. Unsubmitted changes must never alias table/cache rows.
      this.formData = JSON.parse(JSON.stringify(record || {}))
      const normalizedType = normalizeEditorQuestionType(this.formData.type)
      if (normalizedType !== undefined) this.formData.type = normalizedType
      if (!this.formData.options) this.formData.options = '[]'
      if (!this.formData.answer) this.formData.answer = '[]'
      this.answer = parseQuestionAnswerValues(this.formData.answer)
      this.questionAction = act
      this.fileList = []
      this.visible = true
    },
    closeQuestionModal(dialog = this.questionDialogOwner) {
      if (!dialog?.isCurrent()) return
      this.clearQuestionTypeError()
      this.clearQuestionWriteFailure()
      this.visible = false
      this.questionUploads.clear()
    },
    async confirm(dialog = this.questionDialogOwner) {
      if (!dialog?.isCurrent() || dialog.pending || this.questionWriteUnconfirmed) return
      const isCurrent = dialog.isCurrent
      const type = normalizeEditorQuestionType(this.formData.type)
      if (type === undefined) {
        this.showQuestionTypeError(dialog)
        message.warning('题型必须为32位整数，请重新选择。')
        return
      }
      const payload = { ...this.formData, type }
      try {
        const editor = isObjectiveQuestionType(type) ? this.$refs.optionBox : this.$refs.answerBox
        if (typeof editor?.getData !== 'function') throw new Error('editor not ready')
        if (isObjectiveQuestionType(type)) Object.assign(payload, editor.getData())
        else payload.answer = editor.getData()
      } catch {
        if (isCurrent()) message.error('题目编辑器尚未就绪，请稍后重试。')
        return
      }
      if (!isCurrent()) return
      const editing = this.questionAction === 1
      const snapshot = JSON.parse(JSON.stringify(payload))
      this.clearQuestionTypeError()
      this.clearQuestionWriteFailure()
      dialog.pending = true
      this.questionSubmitLoading = true
      try {
        const response = editing ? await updateQuestions(snapshot) : await createQuestions([snapshot])
        if (!isCurrent()) return
        if (response?.status !== 200) {
          this.recordQuestionWriteFailure(dialog, response, editing)
          return
        }
        message.success(editing ? '修改成功' : '创建成功')
        this.fileList = []
        this.visible = false
        this.questionUploads.clear()
        await this.fetchData()
      } catch (error) {
        this.recordQuestionWriteFailure(dialog, error, editing)
      } finally {
        if (this.questionDialogOwner === dialog && dialog.ownsPrincipal()) {
          dialog.pending = false
          this.questionSubmitLoading = false
        }
      }
    },
}
