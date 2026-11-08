import {ref, shallowRef} from 'vue'
import {message} from 'ant-design-vue'
import {getCourses as requestCourses, getQuestions as requestQuestions} from '@/api/TikuComponentApi'
import {ensureRequestFailure} from '@/utils/requestFailureView.mjs'
import {normalizeQuestionTypeFilter} from '@/utils/topicOverviewHelpers.mjs'

// Page-local read publication. The parent still owns the actor epoch, retirement
// and mounted reload order; construction neither reads nor starts a watcher.
export function useQuestionReads({
  data, courses, tabLoading, page, typeValue, searchValue,
  questionRequestOwner, captureQuestionOwner, questionEntry, rememberQuestionRows,
}) {
  const listFailure = shallowRef(null)
  const listFailureContext = ref(0)
  const isCurrentListFailure = (failure, contextKey) => questionEntry.value.isCurrent()
    && Boolean(failure) && failure === listFailure.value && contextKey === listFailureContext.value
  const getCourses = async (items) => {
    const owner = questionRequestOwner
    const ownsPrincipal = captureQuestionOwner()
    if (!ownsPrincipal()) return
    const version = ++owner.courseVersion
    const isCurrent = () => ownsPrincipal() && version === owner.courseVersion
    if (items) {
      courses.value = items.map(value => ({ value, label: value }))
      return
    }
    try {
      const response = await requestCourses()
      if (!isCurrent()) return
      if (Array.isArray(response?.data) && response.data.length) {
        courses.value = response.data.map(value => ({ value, label: value }))
      }
    } catch {
      if (isCurrent()) message.error('获取课程列表失败')
    }
  }
  const fetchData = async () => {
    const owner = questionRequestOwner
    const ownsPrincipal = captureQuestionOwner()
    if (!ownsPrincipal()) return
    const version = ++owner.listVersion
    listFailure.value = null
    const failureContext = ++listFailureContext.value
    const isCurrent = () => ownsPrincipal() && version === owner.listVersion
    const ownsFailure = () => isCurrent() && failureContext === listFailureContext.value
    tabLoading.value = true
    try {
      const res = await requestQuestions({
        type: normalizeQuestionTypeFilter(typeValue.value),
        pageSize: page.value.pageSize,
        pageNo: page.value.pageNo - 1,
        question: searchValue.value.question || '',
        source: parseInt(searchValue.value.source) || 0,
        extra: searchValue.value.extra || '',
        onlyShowEmptyAnswer: searchValue.value.onlyShowEmptyAnswer || false,
      })
      if (!isCurrent()) return
      if (res?.status !== 200) {
        if (ownsFailure()) listFailure.value = ensureRequestFailure(res)
        return
      }
      data.value = Array.isArray(res?.data?.items) ? res.data.items : []
      rememberQuestionRows(data.value)
      page.value.total = res?.data?.total
    } catch (error) {
      if (ownsFailure()) listFailure.value = ensureRequestFailure(error)
    } finally {
      if (isCurrent()) tabLoading.value = false
    }
  }
  return {
    getCourses, fetchData, listFailure, listFailureContext,
    isCurrentListFailure,
  }
}
