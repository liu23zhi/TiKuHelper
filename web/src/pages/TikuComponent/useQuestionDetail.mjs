import {computed, onBeforeUnmount, ref, shallowRef, toRaw, watch} from 'vue'
import {getStoredCurrentUser, getCurrentUserLogoutEpoch} from '@/utils/currentUser'
import {normalizeEditorQuestionType, parseQuestionAnswerValues} from '@/utils/questionAnswerDisplay.mjs'

// Page-local detail responsibility. Call after the parent question scope's watch
// and cleanup registration, so its refreshed entry captures the current epoch.
export function useQuestionDetail({
  currentUser, principal, role, canUseQuestions, captureQuestionOwner,
  guardQuestionEvent, fieldId, quesType,
}) {
  // The read-only detail lane has its own captured render entry. In particular,
  // a status/logout ABA must not lend a stale row event the next display scope.
  const questionDetailOpen = ref(false)
  const questionDetailRecord = shallowRef(null)
  const questionDetailOwner = shallowRef(null)
  const questionDetailBody = ref(null)
  const questionDetailKey = ref(0)
  let questionDetailAlive = true
  let questionDetailEpoch = 0
  const detailPrincipal = () => JSON.stringify([principal(), role(), String(currentUser.value?.status ?? '')])
  let questionDetailPrincipal = detailPrincipal()
  let questionDetailLogout = getCurrentUserLogoutEpoch()
  let questionDetailIdentity = principal()
  let questionDetailBlockedPrincipal = ''
  const canViewQuestionDetail = () => canUseQuestions() && currentUser.value?.status !== 'disabled'
    && (!questionDetailBlockedPrincipal || principal() !== questionDetailBlockedPrincipal)
  const makeDetailEntry = () => {
    const ownsPrincipal = captureQuestionOwner()
    const epoch = questionDetailEpoch
    const identity = questionDetailPrincipal
    const logout = questionDetailLogout
    const isCurrent = () => {
      if (!questionDetailAlive) return false
      getStoredCurrentUser()
      return ownsPrincipal() && epoch === questionDetailEpoch && identity === detailPrincipal()
        && logout === getCurrentUserLogoutEpoch() && canViewQuestionDetail()
    }
    return {
      isCurrent,
      guardNative: event => guardQuestionEvent(isCurrent, event),
      bind: (fn, ...args) => (...events) => isCurrent() ? fn(...args, ...events) : undefined,
    }
  }
  const questionDetailEntry = shallowRef(makeDetailEntry())
  const detailPortal = () => questionDetailBody.value?.closest('.ant-drawer')
  const hideDetailPortal = owner => {
    const portal = owner?.dialog || detailPortal()
    if (!portal) return
    portal.hidden = true
    portal.style.display = 'none'
    if (portal.contains(document.activeElement)) document.activeElement.blur?.()
  }
  const retireQuestionDetail = () => {
    questionDetailEpoch += 1
    hideDetailPortal(questionDetailOwner.value)
    questionDetailOpen.value = false
    questionDetailRecord.value = null
    questionDetailOwner.value = null
    questionDetailEntry.value = makeDetailEntry()
  }
  // Registered after the existing page watcher: actor changes first retire the
  // original request epoch, then this new entry captures the fresh page epoch.
  watch(currentUser, () => {
    const next = detailPrincipal(), logout = getCurrentUserLogoutEpoch(), identity = principal()
    // A failed storage removal can leave the logged-out actor in the cache.
    // Do not re-authorize that stale actor by merely adopting the new logout
    // epoch. A real clear/different identity retires the sticky block; ordinary
    // logout -> empty -> login remains usable on this same page mount.
    if (logout !== questionDetailLogout && identity && identity === questionDetailIdentity) {
      questionDetailBlockedPrincipal = identity
    } else if (identity !== questionDetailBlockedPrincipal) {
      questionDetailBlockedPrincipal = ''
    }
    questionDetailIdentity = identity
    if (next === questionDetailPrincipal && logout === questionDetailLogout) return
    questionDetailPrincipal = next
    questionDetailLogout = logout
    retireQuestionDetail()
  }, { flush: 'sync' })
  onBeforeUnmount(() => {
    questionDetailAlive = false
    retireQuestionDetail()
  })
  // This Antd4 DrawerWrapper drops unknown attrs. Project dialog semantics and
  // capture guards onto this instance's real portal, not a source-only prop or
  // a body-only role that would leave the header/close action outside the dialog.
  watch(questionDetailBody, body => {
    const owner = questionDetailOwner.value
    if (!body || !owner?.isCurrent()) return
    const portal = body.closest('.ant-drawer')
    if (!portal) return
    owner.dialog = portal
    portal.setAttribute('role', 'dialog')
    portal.setAttribute('aria-modal', 'true')
    portal.setAttribute('aria-labelledby', fieldId('detail-title-' + questionDetailKey.value))
    portal.addEventListener('click', owner.guardNative, true)
    portal.addEventListener('keydown', owner.guardNative, true)
  }, { flush: 'post' })
  const isDetailElementVisible = element => {
    for (let node = element; node; node = node.parentElement) {
      const style = window.getComputedStyle(node)
      if (node.hidden || node.inert || node.getAttribute('aria-hidden') === 'true'
        || style.display === 'none' || style.visibility === 'hidden') return false
    }
    return true
  }
  const detailHasOtherDialog = own => [...document.querySelectorAll('[role="dialog"]')]
    .some(dialog => dialog !== own && isDetailElementVisible(dialog))
  const openQuestionDetail = (record, event) => {
    const entry = questionDetailEntry.value
    if (!entry.isCurrent() || !record || typeof record !== 'object' || Array.isArray(record)) return
    if (questionDetailOwner.value) hideDetailPortal(questionDetailOwner.value)
    const owner = {
      scopeCurrent: entry.isCurrent,
      opener: event?.currentTarget || document.activeElement,
      dialog: null,
    }
    owner.isCurrent = () => owner.scopeCurrent() && questionDetailOwner.value === owner && questionDetailOpen.value
    owner.guardNative = event => guardQuestionEvent(owner.isCurrent, event)
    owner.close = () => {
      if (owner.isCurrent()) questionDetailOpen.value = false
    }
    owner.afterOpenChange = opened => {
      if (!owner.scopeCurrent() || questionDetailOwner.value !== owner) return
      owner.dialog = detailPortal() || owner.dialog
      const active = document.activeElement
      if (opened) {
        if (!questionDetailOpen.value || !owner.dialog || detailHasOtherDialog(owner.dialog)) return
        if (active && active !== document.body && active !== owner.opener && !owner.dialog.contains(active)) return
        owner.dialog.querySelector('button[data-question-detail-close]')?.focus({ preventScroll: true })
        return
      }
      if (questionDetailOpen.value) return
      questionDetailOpen.value = false
      questionDetailRecord.value = null
      questionDetailOwner.value = null
      const opener = owner.opener
      if (!(opener instanceof HTMLElement) || !opener.isConnected || opener.tabIndex < 0
        || opener.matches(':disabled, [aria-disabled="true"]') || !isDetailElementVisible(opener)
        || detailHasOtherDialog(owner.dialog)) return
      if (active && active !== document.body && active !== opener && !owner.dialog?.contains(active)) return
      opener.focus({ preventScroll: true })
    }
    // Snapshot only: never alias the table, selection cache or editor draft.
    questionDetailRecord.value = JSON.parse(JSON.stringify(toRaw(record)))
    questionDetailKey.value += 1
    questionDetailOwner.value = owner
    questionDetailOpen.value = true
  }
  const formatQuestionDetailValue = value => {
    if (value === undefined) return '未提供'
    if (value === null) return 'null'
    if (value === '') return '（空）'
    return typeof value === 'object' ? JSON.stringify(value, null, 2) : String(value)
  }
  const questionDetailLabels = {
    id: '题目 ID', type: '题目类型', question: '问题', options: '选项', answer: '答案',
    course_name: '课程名称', source_name: '数据来源', extra: '拓展参数', hash: '题目哈希',
  }
  const questionDetailFields = computed(() => {
    const record = questionDetailRecord.value
    if (!record) return []
    const keys = [...Object.keys(questionDetailLabels), ...Object.keys(record).filter(key => !Object.hasOwn(questionDetailLabels, key))]
    return keys.map(key => {
      let text = formatQuestionDetailValue(record[key])
      if (key === 'type' && quesType.value[normalizeEditorQuestionType(record.type)]) {
        text = quesType.value[normalizeEditorQuestionType(record.type)] + '（' + text + '）'
      }
      return { key, label: Object.hasOwn(questionDetailLabels, key) ? questionDetailLabels[key] : '其他字段（' + key + '）', text,
        values: key === 'options' || key === 'answer' ? parseQuestionAnswerValues(record[key]) : null }
    })
  })
  return {
    canViewQuestionDetail, questionDetailEntry, openQuestionDetail,
    questionDetailOpen, questionDetailOwner, questionDetailBody, questionDetailKey,
    questionDetailFields, formatQuestionDetailValue,
  }
}
