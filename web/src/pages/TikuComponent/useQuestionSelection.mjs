import {computed, ref, shallowReactive} from 'vue'
import {message} from 'ant-design-vue'
import {getQuestions} from '@/api/TikuComponentApi'
import {buildQuestionBulkActionIds, buildQuestionSelectionSummary, normalizeQuestionTypeFilter} from '@/utils/topicOverviewHelpers.mjs'

// Page-local selection/cache responsibility. The parent retains its request
// authority and lifecycle; invoke after creating questionEntry and before its
// retirement/watch registration. Creating this lane performs no request.
export function useQuestionSelection({
  data, page, typeValue, searchValue, questionEntry,
  captureQuestionOwner, questionRequestOwner,
}) {
  const selectedRowKeys = ref([])
  const selectAllLoading = ref(false)
  const questionCache = shallowReactive(new Map())
  let questionSelectionVersion = 0

  const invalidatePendingSelection = () => {
    questionSelectionVersion += 1
    selectAllLoading.value = false
  }

  const rememberQuestionRows = (items = []) => {
    for (const item of items || []) {
      const id = Number(item?.id || 0)
      if (id > 0) {
        questionCache.set(id, item)
      }
    }
  }

  const resetSelectionState = () => {
    invalidatePendingSelection()
    selectedRowKeys.value = []
    questionCache.clear()
  }


  const buildQuestionQuery = (pageNo, pageSize) => ({
    type: normalizeQuestionTypeFilter(typeValue.value),
    pageSize,
    pageNo,
    question: searchValue.value.question || '',
    source: parseInt(searchValue.value.source) || 0,
    extra: searchValue.value.extra || '',
    onlyShowEmptyAnswer: searchValue.value.onlyShowEmptyAnswer || false
  })
  const loadAllFilteredQuestions = async (isCurrent) => {
    if (!isCurrent()) return null
    const batchSize = 100
    const query = buildQuestionQuery(0, batchSize)
    const first = await getQuestions(query)
    if (!isCurrent()) return null
    if (first?.status !== 200) {
      throw new Error(first?.message || '获取全部题目失败')
    }
    const total = Number(first?.data?.total || 0)
    const items = Array.isArray(first?.data?.items) ? [...first.data.items] : []
    const totalPages = Math.ceil(total / batchSize)
    for (let pageNo = 1; pageNo < totalPages; pageNo += 1) {
      if (!isCurrent()) return null
      const response = await getQuestions({ ...query, pageNo })
      if (!isCurrent()) return null
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
    if (!questionEntry.value.isCurrent()) return
    invalidatePendingSelection()
    rememberQuestionRows(data.value)
    selectedRowKeys.value = buildQuestionBulkActionIds([
      ...selectedRowKeys.value,
      ...currentPageIds.value,
    ])
  }
  const handleClearCurrentPage = () => {
    if (!questionEntry.value.isCurrent()) return
    invalidatePendingSelection()
    const currentPageIdSet = new Set(currentPageIds.value)
    selectedRowKeys.value = selectedRowKeys.value.filter(id => !currentPageIdSet.has(id))
  }
  const handleSelectAll = async () => {
    const ownsPrincipal = captureQuestionOwner()
    if (!ownsPrincipal()) return
    if (allRowsSelected.value || selectAllLoading.value) {
      return
    }
    const version = ++questionSelectionVersion
    questionRequestOwner.selectionVersion = version
    const isCurrent = () => ownsPrincipal() && version === questionSelectionVersion
    selectAllLoading.value = true
    try {
      const rows = await loadAllFilteredQuestions(isCurrent)
      if (!isCurrent()) return
      rememberQuestionRows(rows)
      selectedRowKeys.value = buildQuestionBulkActionIds(rows.map(item => item?.id))
      if (!rows.length) {
        resetSelectionState()
      }
    } catch (error) {
      if (isCurrent()) message.error(error?.message || '全选失败')
    } finally {
      if (isCurrent()) selectAllLoading.value = false
    }
  }
  const handleClearSelection = () => {
    if (!questionEntry.value.isCurrent()) return
    resetSelectionState()
  }
  const rowSelection = computed(() => {
    const entry = questionEntry.value
    return {
      columnWidth: 48,
      selectedRowKeys: selectedRowKeys.value,
      preserveSelectedRowKeys: true,
      onChange: (keys) => {
        if (!entry.isCurrent()) return
        invalidatePendingSelection()
        rememberQuestionRows(data.value)
        selectedRowKeys.value = buildQuestionBulkActionIds(keys)
      },
    }
  })
  return {
    resetSelectionState, rememberQuestionRows, selectedRowKeys, selectedQuestions,
    rowSelection, selectionSummary, allRowsSelected, allCurrentPageSelected,
    hasCurrentPageSelection, selectAllLoading, handleSelectCurrentPage,
    handleClearCurrentPage, handleSelectAll, handleClearSelection,
  }
}
