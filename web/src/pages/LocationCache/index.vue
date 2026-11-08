<template>
  <div class="console-page location-page"><div v-if="cacheMounted" :key="cacheKey" ref="cacheSurface" :onKeydownCapture="cacheEntry.guardNative" :onInputCapture="cacheEntry.guardNative" :onChangeCapture="cacheEntry.guardNative" :onClickCapture="cacheEntry.guardNative">
    <ConsolePageHeading :title="pageTitle" description="查看、补全和维护已保存的归属地信息。">
      <template #actions><a-space wrap class="section-actions">
          <a-button :disabled="isRefreshDisabled() || manualSubmitting" :loading="manualSubmitting" :onClick="cacheEntry.bind(openManual)">手动添加</a-button>
          <a-popconfirm title="删除当前已读取筛选范围的全部归属地缓存？" ok-text="全部删除" cancel-text="取消" :open="allDeletePrompt.open" :onOpenChange="cacheEntry.bind(openAllDelete)" :onConfirm="allDeletePrompt.confirm">
            <a-button danger :disabled="!canAct() || loading" :loading="allDeleting">全部删除</a-button>
          </a-popconfirm>
          <a-button :loading="loading" :disabled="!canAct()" :onClick="cacheEntry.bind(load)"><template #icon><ReloadOutlined /></template>刷新列表</a-button>
      </a-space></template>
    </ConsolePageHeading>
    <ConsoleSummaryGrid>
      <a-statistic title="筛选结果" :value="summary.total" suffix="条" />
      <a-statistic title="已有归属地" :value="summary.withAddress" suffix="条" />
      <a-statistic title="待补全" :value="summary.withoutAddress" suffix="条" />
      <a-statistic title="当前选择" :value="selectedCount" suffix="条" />
    </ConsoleSummaryGrid>
    <ConsolePanel title="查询与维护" description="按查询值、归属地、记录来源、归属地状态和查询时间筛选；查询来源仅用于后续重新查询和手动添加。">
      <a-form layout="inline" class="filter-form" :onFinish="cacheEntry.bind(reload)">
        <a-form-item label="关键词">
          <a-input-search :value="keywordInput" v-on="{ 'update:value': cacheEntry.bind(updateKeyword) }" :disabled="!canAct()" allow-clear placeholder="查询值 / 归属地" class="keyword-input" :onSearch="cacheEntry.bind(reload)" />
        </a-form-item>
        <a-form-item label="记录来源">
          <a-select :value="recordSourceInput" :onChange="cacheEntry.bind(updateRecordSource)" :disabled="!canAct()" class="record-source-select">
            <a-select-option value="">全部来源</a-select-option><a-select-option value="default">默认来源</a-select-option>
            <a-select-option v-if="kind === 'ip'" value="commercial">商业增强</a-select-option><a-select-option v-if="kind === 'ip'" value="legacy">历史来源</a-select-option>
          </a-select>
        </a-form-item>
        <a-form-item label="归属地状态">
          <a-select :value="addressStatusInput" :onChange="cacheEntry.bind(updateAddressStatus)" :disabled="!canAct()" class="address-status-select">
            <a-select-option value="">全部状态</a-select-option><a-select-option value="with">已有归属地</a-select-option><a-select-option value="without">待补全</a-select-option>
          </a-select>
        </a-form-item>
        <a-form-item label="查询时间">
          <a-space class="date-filter-space"><a-input :value="fetchedFromInput" :onUpdate:value="cacheEntry.bind(updateFetchedFrom)" type="date" aria-label="查询时间起始" :disabled="!canAct()" /><span>至</span><a-input :value="fetchedToInput" :onUpdate:value="cacheEntry.bind(updateFetchedTo)" type="date" aria-label="查询时间结束" :disabled="!canAct()" /></a-space>
        </a-form-item>
        <a-form-item><a-space><a-button type="primary" html-type="submit" :loading="loading" :disabled="!canAct()">查询</a-button><a-button :onClick="cacheEntry.bind(resetFilters)">重置筛选</a-button></a-space></a-form-item>
        <a-form-item v-if="kind === 'ip'" label="查询来源">
          <a-select :value="querySource" class="query-source-select" :onChange="cacheEntry.bind(setQuerySource)" :disabled="!canAct()">
            <a-select-option value="">普通来源</a-select-option>
            <a-select-option value="commercial" :disabled="configured !== true">商业增强</a-select-option>
          </a-select>
        </a-form-item>
      </a-form>
      <p v-if="kind === 'ip'" class="query-source-hint">重新查询和手动添加使用所选来源，重查成功后同步更新所选记录来源，未选中的其他来源记录保留。</p>
      <a-alert v-if="configured === false" type="warning" show-icon message="未配置 UApiPro 密钥，仅可查看已有缓存，不能重新查询或手动添加。" class="status-alert" />
      <a-alert v-if="listQueryUnconfirmed" type="warning" show-icon message="当前显示上次成功结果；当前筛选或分页尚未确认，列表重新加载成功后才能选择、刷新或删除这些记录。" class="status-alert" />
      <a-alert v-if="errorMessage" type="error" show-icon :message="errorMessage" class="status-alert" />
    </ConsolePanel>
    <ConsolePanel title="归属地记录" class="table-card">
      <a-alert v-if="pagination.total" type="info" show-icon class="selection-alert">
        <template #message>
          <div class="selection-alert-content">
            <span>{{ selectionSummary }}</span>
            <a-button type="primary" size="small" :loading="batchRefreshing" :disabled="!selectedCount || isRefreshDisabled() || !isListCurrent()" :onClick="cacheEntry.bind(refreshSelected)">重新查询所选</a-button>
            <a-button danger size="small" :loading="batchDeleting" :disabled="!selectedCount || !isListCurrent()" :onClick="cacheEntry.bind(deleteSelected)">删除所选</a-button>
            <a-button type="link" size="small" :disabled="allCurrentPageSelected || !isListCurrent()" :onClick="cacheEntry.bind(handleSelectCurrentPage)">全选本页</a-button>
            <a-button type="link" size="small" :disabled="!hasCurrentPageSelection || !isListCurrent()" :onClick="cacheEntry.bind(handleClearCurrentPage)">取消本页全选</a-button>
            <a-button type="link" size="small" :disabled="allRowsSelected || !isListCurrent()" :onClick="cacheEntry.bind(handleSelectAll)">全选全部</a-button>
            <a-button type="link" size="small" :disabled="!selectedCount" :onClick="cacheEntry.bind(handleClearSelection)">取消全部全选</a-button>
          </div>
        </template>
      </a-alert>
      <ConsoleTableShell :label="`${kindLabel}归属地记录`"><a-table table-layout="fixed" :data-source="rows" :columns="columns" :loading="loading" :pagination="pagination" :row-key="recordIdentity" :row-selection="rowSelection" :scroll="{ x: sumTableColumnWidths(columns, 48) }" :onChange="tableEntry.bind(onTableChange)">
        <template #bodyCell="{ column, record }">
          <span v-if="column.key === 'fetchedAt'">{{ formatDate(record.fetchedAt) }}</span>
          <a-space v-else-if="column.key === 'action'" wrap>
            <a-button type="link" :onClick="tableEntry.bind(showDetails, record)">查看详情</a-button>
            <a-button type="link" :loading="!!refreshing[recordIdentity(record)]" :disabled="isRefreshDisabled(record)" :onClick="tableEntry.bind(refresh, record)">重新查询</a-button>
            <a-popconfirm title="删除后下次展示会重新查询，确认删除吗？" ok-text="删除" cancel-text="取消" :open="deletePrompts[recordIdentity(record)]?.open || false" :onOpenChange="tableEntry.bind(openRowDelete, record)" :onConfirm="deletePrompts[recordIdentity(record)]?.confirm || noop">
              <a-button type="link" danger :disabled="!isListCurrent()" :loading="!!removing[recordIdentity(record)]">删除</a-button>
            </a-popconfirm>
          </a-space>
        </template>
      </a-table></ConsoleTableShell>
    </ConsolePanel>
    <a-modal v-if="detailMounted" :key="detailKey" :open="detailVisible" v-on="{ 'update:open': detailEntry.bind(updateDetailOpen) }" :onCancel="detailEntry.bind(cancelDetail)" :afterClose="detailAfterClose" :focusTriggerAfterClose="false" :title="`${kindLabel}归属地详情`" :footer="null" width="760px">
      <div ref="detailSurface"><a-descriptions :column="1" size="small" bordered>
        <a-descriptions-item :label="kindLabel">{{ detailRecord.lookupKey || '-' }}</a-descriptions-item>
        <a-descriptions-item v-if="detailRecord.kind === 'ip'" label="记录来源">{{ sourceLabel(detailRecord.source) }}</a-descriptions-item>
        <a-descriptions-item label="归属地">{{ detailRecord.address || '-' }}</a-descriptions-item>
        <a-descriptions-item label="最近查询时间">{{ formatDate(detailRecord.fetchedAt) }}</a-descriptions-item>
        <a-descriptions-item label="记录更新时间">{{ formatDate(detailRecord.updatedAt) }}</a-descriptions-item>
      </a-descriptions>
      <h4 class="response-title">原始响应</h4>
      <pre class="raw-response">{{ formatRawResponse(detailRecord.rawJson) }}</pre></div>
    </a-modal>
    <a-modal v-if="manualMounted" :key="manualModalKey" :open="manualVisible" v-on="{ 'update:open': manualEntry.bind(updateManualOpen) }" :onCancel="manualEntry.bind(cancelManual)" :afterClose="manualAfterClose" :focusTriggerAfterClose="false" :title="`手动添加${kindLabel}归属地`" :confirm-loading="manualSubmitting" ok-text="查询并保存" cancel-text="取消" :onOk="manualEntry.bind(manualLookup)">
      <a-form ref="manualForm" layout="vertical" :onKeydownCapture="manualEntry.guardNative" :onInputCapture="manualEntry.guardNative" :onChangeCapture="manualEntry.guardNative" :onClickCapture="manualEntry.guardNative">
        <a-form-item :label="kindLabel" required>
          <a-input :value="manualKey" v-on="{ 'update:value': manualEntry.bind(updateManualKey) }" :disabled="manualSubmitting || isRefreshDisabled()" :placeholder="kind === 'phone' ? '请输入 11 位手机号' : '请输入公网 IP 地址'" :onPressEnter="manualEntry.bind(manualLookup)" />
        </a-form-item>
        <a-form-item v-if="kind === 'ip'" label="查询来源">
          <a-select :value="querySource" :onChange="manualEntry.bind(setQuerySource)" :disabled="manualSubmitting || isRefreshDisabled()">
            <a-select-option value="">普通来源</a-select-option>
            <a-select-option value="commercial" :disabled="configured !== true">商业增强</a-select-option>
          </a-select>
        </a-form-item>
      </a-form>
    </a-modal>
  </div></div>
</template>

<script>
import { computed, defineComponent, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, watch } from 'vue'
import { onBeforeRouteLeave, useRoute } from 'vue-router'
import { Descriptions, message, Modal } from 'ant-design-vue'
import { useCurrentUserDisplay, isAdminUser } from '@/utils/currentUser'
import { useUnsavedDraft } from '@/utils/useUnsavedDraft'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'
import { deleteLocationCache, deleteLocationCacheBatch, getLocationCacheList, manualLookupLocationCache, refreshLocationCache, refreshLocationCacheBatch } from '@/api/TikuComponentApi'

export default defineComponent({
  name: 'LocationCache',
  methods: { sumTableColumnWidths },
  components: { ReloadOutlined, ADescriptions: Descriptions, ADescriptionsItem: Descriptions.Item },
  setup() {
    const route = useRoute()
    const kind = computed(() => route.meta.locationKind === 'phone' ? 'phone' : 'ip')
    const kindLabel = computed(() => kind.value === 'phone' ? '手机号' : 'IP')
    const pageTitle = computed(() => `${kindLabel.value} 归属地信息管理`)
    const rows = ref([])
    const loading = ref(false)
    const configured = ref(undefined)
    const querySource = ref('')
    const recordSource = ref('')
    const addressStatus = ref('')
    const fetchedFrom = ref('')
    const fetchedTo = ref('')
    const querySourceStorageKey = 'tiku.location.querySource'
    try {
      if (globalThis.localStorage?.getItem(querySourceStorageKey) === 'commercial') querySource.value = 'commercial'
    } catch { /* Storage may be unavailable; ordinary source remains the default. */ }
    const setQuerySource = value => {
      if (!canAct()) return
      const next = value === 'commercial' && configured.value === true ? 'commercial' : ''
      // A non-sensitive future-query preference, not a list/identity change.
      // Already issued operations keep their captured payload and busy identity.
      if (next !== querySource.value) renewEntry()
      querySource.value = next
      try { globalThis.localStorage?.setItem(querySourceStorageKey, querySource.value) } catch { /* Keep the page setting usable. */ }
    }
    const updateConfigured = value => {
      if (typeof value !== 'boolean') return
      configured.value = value
      if (!value) {
        querySource.value = ''
        try { globalThis.localStorage?.setItem(querySourceStorageKey, '') } catch { /* optional preference */ }
      }
    }
    const errorMessage = ref('')
    const keywordInput = ref('')
    const keyword = ref('')
    // Draft controls apply only on Query, just like keywordInput/keyword.
    const recordSourceInput = ref(''), addressStatusInput = ref(''), fetchedFromInput = ref(''), fetchedToInput = ref('')
    const refreshing = reactive({})
    const removing = reactive({})
    const selectedKeys = ref(new Set())
    const deselectedKeys = ref(new Set())
    const selectAllMode = ref(false)
    const batchRefreshing = ref(false)
    const batchDeleting = ref(false)
    const allDeleting = ref(false)
    const manualVisible = ref(false)
    const manualSubmitting = ref(false)
    const manualKey = ref('')
    const summary = ref({ total: 0, withAddress: 0, withoutAddress: 0 })
    const recordIdentity = record => `${record.kind}:${record.lookupKey}:${record.source || 'default'}`
    const sourceLabel = source => source === 'legacy' ? '历史（来源未记录）' : source === 'commercial' ? '商业增强' : '普通来源'
    const isRefreshDisabled = record => !canAct() || configured.value !== true || Boolean(record && !isListCurrent())
    const detailVisible = ref(false)
    const detailRecord = ref({})
    const pagination = reactive({
      current: 1, pageSize: 20, total: 0, showSizeChanger: true,
      pageSizeOptions: ['10', '20', '50', '100'], showTotal: total => `共 ${total} 条`,
    })

    // The visible records belong to the last successful list query. A failed
    // new filter/page must not turn old rows into authority for a new payload.
    const lastSuccessfulListQuery = ref(null)
    const listQueryFingerprint = () => JSON.stringify([kind.value, keyword.value, recordSource.value, addressStatus.value, fetchedFrom.value, fetchedTo.value, pagination.current, pagination.pageSize])
    const isListCurrent = () => lastSuccessfulListQuery.value !== null && lastSuccessfulListQuery.value === listQueryFingerprint()
    const listQueryUnconfirmed = computed(() => lastSuccessfulListQuery.value !== null && !isListCurrent())

    const { currentUser } = useCurrentUserDisplay()
    const principal = () => {
      const id = currentUser.value?.id
      if (typeof id !== 'number' && (typeof id !== 'string' || !/^[0-9]+$/.test(id.trim()))) return ''
      const value = Number(id)
      return Number.isSafeInteger(value) && value > 0 && value <= 2147483647 ? `id:${value}` : ''
    }
    const role = () => String(currentUser.value?.role || '').trim().toLowerCase()
    let alive = true, mounted = false, epoch = 0, queryVersion = 0, renderVersion = 0
    const cacheSurface = ref(null), cacheMounted = ref(true), cacheKey = ref(0), cacheDraining = ref(false)
    const manualMounted = ref(false), manualModalKey = ref(0), manualForm = ref(null)
    const detailMounted = ref(false), detailKey = ref(0), detailSurface = ref(null)
    const canManageCache = () => alive && Boolean(principal()) && isAdminUser(currentUser.value)
    const canAct = () => canManageCache() && !cacheDraining.value
    const captureOwner = () => {
      const ticket = epoch, identity = principal(), privilege = role(), type = kind.value
      return () => alive && ticket === epoch && identity === principal() && privilege === role() && type === kind.value && canManageCache()
    }
    const captureQuery = () => {
      const owns = captureOwner(), version = queryVersion
      return () => owns() && version === queryVersion && !cacheDraining.value
    }
    const makeEntry = (extra = () => true, queryBound = true) => {
      const owns = queryBound ? captureQuery() : captureOwner(), version = renderVersion
      const isCurrent = () => owns() && version === renderVersion && !cacheDraining.value && extra()
      return {
        isCurrent,
        guardNative(event) { if (!isCurrent()) { event.preventDefault(); event.stopImmediatePropagation() } },
        bind: (fn, ...args) => (...events) => isCurrent() ? fn(...args, ...events) : undefined,
      }
    }
    const cacheEntry = shallowRef(makeEntry()), tableEntry = shallowRef(makeEntry())
    const manualEntry = shallowRef(makeEntry(() => false)), detailEntry = shallowRef(makeEntry(() => false))
    const manualAfterClose = shallowRef(null), detailAfterClose = shallowRef(null)
    let manualOwner = null, detailOwner = null, manualDrain = Promise.resolve()
    const renewEntry = () => {
      renderVersion += 1; cacheEntry.value = makeEntry(); tableEntry.value = makeEntry(() => !loading.value)
      if (manualOwner) manualEntry.value = makeEntry(manualOwner.isCurrent, false)
      if (detailOwner) detailEntry.value = makeEntry(detailOwner.isCurrent, false)
    }
    const rowOwners = new WeakMap()
    const recordCurrent = record => canAct() && record?.kind === kind.value && (!rowOwners.has(record) || rowOwners.get(record)())
    const prompts = new Map()
    const closePrompt = name => {
      const prompt = prompts.get(name)
      if (!prompt) return
      prompts.delete(name); prompt.resolve(false); prompt.modal?.destroy()
    }
    const confirmOwned = (name, options, owns) => {
      if (!owns()) return Promise.resolve(false)
      if (prompts.has(name)) return prompts.get(name).promise
      const prompt = { modal: null, resolve: null, promise: null }
      prompt.promise = new Promise(resolve => { prompt.resolve = resolve })
      prompts.set(name, prompt)
      const settle = allow => {
        if (prompts.get(name) !== prompt) return
        prompts.delete(name); prompt.resolve(Boolean(allow && owns())); prompt.modal?.destroy()
      }
      prompt.modal = Modal.confirm({ ...options, autoFocusButton: 'cancel', focusTriggerAfterClose: false,
        onOk: () => settle(true), onCancel: () => settle(false) })
      return prompt.promise
    }
    const actions = new Map()
    const beginAction = (name, busy) => {
      if (!canAct() || actions.has(name)) return null
      const owner = captureQuery(), actor = captureOwner(), ticket = { busy }
      ticket.owns = () => owner() && actions.get(name) === ticket
      // A query change retires painting, not an already issued call. Keep its
      // duplicate guard/busy identity until real completion, then release only
      // this actor's ticket; a replacement actor can never be touched here.
      ticket.finish = () => { if (actor() && actions.get(name) === ticket) { actions.delete(name); busy(false) } }
      actions.set(name, ticket); busy(true)
      return ticket
    }
    const clearActions = () => {
      for (const action of actions.values()) action.busy(false)
      actions.clear()
      for (const key of Object.keys(refreshing)) delete refreshing[key]
      for (const key of Object.keys(removing)) delete removing[key]
    }
    const hidePrivateSurface = form => {
      const element = form?.$el || form
      if (!(element instanceof HTMLElement)) return
      const surface = element.closest('[role="dialog"]') || element
      surface.hidden = true; surface.style.display = 'none'
      for (const input of surface.querySelectorAll('input, textarea')) {
        input.value = ''; input.setAttribute('value', '')
        if (input.tagName === 'INPUT' && input.type !== 'checkbox' && input.type !== 'radio') input.type = 'password'
      }
      // Erase retained private text synchronously without removing native inputs:
      // Antd may still have an accepted setSelection queued against those refs.
      for (const element of surface.querySelectorAll('[data-row-key], [title]')) {
        element.removeAttribute('data-row-key'); element.removeAttribute('title')
      }
      const walker = document.createTreeWalker(surface, 4 /* SHOW_TEXT */)
      while (walker.nextNode()) walker.currentNode.data = ''
      if (surface.contains(document.activeElement)) document.activeElement.blur?.()
    }
    const scrubRecord = record => {
      if (record && typeof record === 'object') for (const field of Object.keys(record)) record[field] = ''
    }
    const draftState = reactive({ manual: computed(() => manualVisible.value ? manualKey.value : null) })
    const { currentConfigSnapshot, savedConfigSnapshot, hasUnsavedChanges, requestRouteLeave: requestDraftLeave } = useUnsavedDraft(draftState, { focusTriggerAfterClose: false })
    const markDraftSaved = () => { savedConfigSnapshot.value = currentConfigSnapshot.value }
    const retireManual = () => {
      const owner = manualOwner
      manualOwner = null; manualEntry.value = makeEntry(() => false); manualAfterClose.value = null
      closePrompt('manual'); hidePrivateSurface(manualForm.value)
      manualVisible.value = false; manualKey.value = ''; manualSubmitting.value = actions.has('manualLookup'); markDraftSaved()
      manualDrain = nextTick().then(() => nextTick()).then(() => {
        if (!manualOwner && (!owner || owner.key === manualModalKey.value)) manualMounted.value = false
      }).then(() => nextTick())
      return manualDrain
    }
    const retireDetail = () => {
      detailOwner = null; detailEntry.value = makeEntry(() => false); detailAfterClose.value = null
      hidePrivateSurface(detailSurface.value); detailVisible.value = false
      scrubRecord(detailRecord.value); detailRecord.value = {}; detailMounted.value = false
    }
    const openManual = async () => {
      if (isRefreshDisabled() || manualSubmitting.value || manualVisible.value) return
      const owns = captureOwner()
      await manualDrain
      if (!owns() || isRefreshDisabled()) return
      manualKey.value = ''
      const owner = { key: ++manualModalKey.value, owns }
      owner.isCurrent = () => owns() && manualOwner === owner && owner.key === manualModalKey.value && manualVisible.value
      manualOwner = owner; manualMounted.value = true; manualVisible.value = true
      manualEntry.value = makeEntry(owner.isCurrent, false); markDraftSaved()
      manualAfterClose.value = () => { if (manualOwner === owner && !manualVisible.value) retireManual() }
    }
    const cancelManual = async () => {
      const owner = manualOwner
      if (!owner?.isCurrent()) return
      if (hasUnsavedChanges.value && !await confirmOwned('manual', { title: '有未保存的查询值，确认关闭？',
        content: '关闭将清除当前未保存的手动添加内容。', okText: '放弃更改', cancelText: '保留编辑' }, owner.isCurrent)) return
      if (owner.isCurrent()) retireManual()
    }
    const updateManualOpen = value => { if (!value) return cancelManual() }
    const updateManualKey = value => { if (manualOwner?.isCurrent() && !manualSubmitting.value) manualKey.value = String(value ?? '') }
    const cancelDetail = () => { if (detailOwner?.isCurrent()) retireDetail() }
    const updateDetailOpen = value => { if (!value) cancelDetail() }
    const noop = () => {}
    const deletePrompts = reactive({}), allDeletePrompt = ref({ open: false, confirm: noop })
    const closeDeletePrompts = () => {
      for (const key of Object.keys(deletePrompts)) delete deletePrompts[key]
      allDeletePrompt.value = { open: false, confirm: noop }
    }
    const openRowDelete = (record, open) => {
      if (!recordCurrent(record) || !isListCurrent() || loading.value) return
      const identity = recordIdentity(record)
      if (!open) { if (deletePrompts[identity]) deletePrompts[identity].open = false; return }
      const entry = tableEntry.value, owns = () => entry.isCurrent(), original = { ...record }
      deletePrompts[identity] = { open: true, confirm: () => {
        if (!owns()) return
        if (deletePrompts[identity]) deletePrompts[identity].open = false
        return remove(original)
      } }
    }
    const openAllDelete = open => {
      if (!canAct() || !isListCurrent() || loading.value) return
      if (!open) { allDeletePrompt.value.open = false; return }
      const entry = cacheEntry.value, owns = () => entry.isCurrent()
      allDeletePrompt.value = { open: true, confirm: () => {
        if (!owns()) return
        allDeletePrompt.value.open = false
        return deleteAll()
      } }
    }
    const beginQuery = () => {
      queryVersion += 1; loadGeneration += 1; loading.value = false
      for (const name of [...prompts.keys()]) closePrompt(name)
      closeDeletePrompts(); renewEntry()
    }
    const updateKeyword = value => { if (canAct()) keywordInput.value = String(value ?? '') }
    const updateRecordSource = value => { if (canAct()) recordSourceInput.value = String(value ?? '') }
    const updateAddressStatus = value => { if (canAct()) addressStatusInput.value = String(value ?? '') }
    const updateFetchedFrom = value => { if (canAct()) fetchedFromInput.value = String(value ?? '') }
    const updateFetchedTo = value => { if (canAct()) fetchedToInput.value = String(value ?? '') }
    const assertResponse = (res, fallback) => {
      if (res?.status === 200) return
      const error = new Error(fallback)
      error.response = { status: res?.status, data: { configured: res?.data?.configured, message: res?.message, code: res?.data?.code } }
      throw error
    }

    const currentPageKeys = computed(() => rows.value.map(recordIdentity))
    const selectedRowKeys = computed(() => {
      if (selectAllMode.value) {
        return currentPageKeys.value.filter(key => !deselectedKeys.value.has(key))
      }
      return currentPageKeys.value.filter(key => selectedKeys.value.has(key))
    })
    const selectedCount = computed(() => selectAllMode.value
      ? Math.max(0, pagination.total - deselectedKeys.value.size)
      : selectedKeys.value.size)
    const allRowsSelected = computed(() => selectAllMode.value && deselectedKeys.value.size === 0 && pagination.total > 0)
    const allCurrentPageSelected = computed(() => currentPageKeys.value.length > 0 && selectedRowKeys.value.length === currentPageKeys.value.length)
    const hasCurrentPageSelection = computed(() => selectedRowKeys.value.length > 0)
    const selectionSummary = computed(() => `已选择 ${selectedCount.value} 条，共 ${pagination.total} 条`)
    const rowSelection = computed(() => ({
      columnWidth: 48,
      selectedRowKeys: selectedRowKeys.value,
      onChange: tableEntry.value.bind(onSelectionChange),
      getCheckboxProps: () => ({ disabled: !canAct() || !isListCurrent() || loading.value }),
    }))
    const columns = computed(() => [
      { title: kindLabel.value, dataIndex: 'lookupKey', key: 'lookupKey', width: 210 },
      ...(kind.value === 'ip' ? [{ title: '记录来源', key: 'source', customRender: ({ record }) => sourceLabel(record.source), width: 160 }] : []),
      { title: '归属地', dataIndex: 'address', key: 'address', width: 260 },
      { title: '最近查询时间', dataIndex: 'fetchedAt', key: 'fetchedAt', width: 200 },
      { title: '操作', key: 'action', width: 260 },
    ])
    const formatDate = (value) => {
      const date = value ? new Date(value) : null
      return date && !Number.isNaN(date.getTime())
        ? date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' }) : '-'
    }
    const formatRawResponse = (value) => {
      const raw = String(value || '')
      if (!raw) return '-'
      try { return JSON.stringify(JSON.parse(raw), null, 2) } catch { return raw }
    }
    const safeReasons = new Set([
      '尚未配置 UApiPro API 密钥', '上游响应没有可识别的归属地信息',
      '归属地类型或查询值不合法', '只能查询与当前账号关联的 IP',
      'UApiPro 密钥无效或已过期，请检查系统设置', 'UApiPro 积分或配额不足',
      'UApiPro 密钥没有此接口的访问权限', 'UApiPro 查询频率受限，请稍后重试',
      'UApiPro 未找到对应的归属地信息', 'UApiPro 网络请求失败，请稍后重试',
      'UApiPro 服务异常，请稍后重试', '归属地查询或保存失败，请检查服务日志',
    ])
    const fixedReason = value => safeReasons.has(value) ? value : '查询失败'
    const requestErrorMessage = (error, fallback) => {
      const data = error?.response?.data
      updateConfigured(data?.configured)
      if (data?.configured === false) return '未配置 UApiPro 密钥，不能重新查询或手动添加。'
      if (safeReasons.has(data?.message)) return fixedReason(data.message)
      const status = Number(error?.response?.status || error?.status)
      if (status === 401) return '登录状态已失效，请重新登录。'
      if (status === 403) return '当前账号无权执行此操作。'
      if (status === 429) return '请求过于频繁，请稍后重试。'
      if (status === 400 || status === 422) return fallback + '，请检查查询值和来源后重试。'
      return fallback + '，当前数据与未保存的编辑仍保留，请重试。'
    }
    const clearSelectionState = () => {
      selectedKeys.value = new Set()
      deselectedKeys.value = new Set()
      selectAllMode.value = false
    }
    const handleSelectCurrentPage = () => {
      if (!canAct() || !isListCurrent() || loading.value) return
      if (!currentPageKeys.value.length) return
      if (selectAllMode.value) {
        const next = new Set(deselectedKeys.value)
        currentPageKeys.value.forEach(key => next.delete(key))
        deselectedKeys.value = next
        return
      }
      const next = new Set(selectedKeys.value)
      currentPageKeys.value.forEach(key => next.add(key))
      selectedKeys.value = next
    }
    const handleClearCurrentPage = () => {
      if (!canAct() || !isListCurrent() || loading.value) return
      if (!currentPageKeys.value.length) return
      if (selectAllMode.value) {
        const next = new Set(deselectedKeys.value)
        currentPageKeys.value.forEach(key => next.add(key))
        deselectedKeys.value = next
        return
      }
      const next = new Set(selectedKeys.value)
      currentPageKeys.value.forEach(key => next.delete(key))
      selectedKeys.value = next
    }
    const handleSelectAll = () => {
      if (!canAct() || !isListCurrent() || loading.value) return
      if (!pagination.total) return
      selectedKeys.value = new Set()
      deselectedKeys.value = new Set()
      selectAllMode.value = true
    }
    const handleClearSelection = () => { if (canAct()) clearSelectionState() }
    function onSelectionChange(keys) {
      if (!canAct() || !isListCurrent() || loading.value) return
      const selected = new Set(Array.isArray(keys) ? keys : [])
      if (selectAllMode.value) {
        const next = new Set(deselectedKeys.value)
        currentPageKeys.value.forEach(key => {
          if (selected.has(key)) next.delete(key)
          else next.add(key)
        })
        deselectedKeys.value = next
        return
      }
      const next = new Set(selectedKeys.value)
      currentPageKeys.value.forEach(key => {
        if (selected.has(key)) next.add(key)
        else next.delete(key)
      })
      selectedKeys.value = next
    }
    const selectionItemFromIdentity = (identity) => {
      const prefix = `${kind.value}:`
      if (!identity.startsWith(prefix)) return null
      const body = identity.slice(prefix.length)
      const separator = body.lastIndexOf(':')
      if (separator <= 0) return null
      const source = body.slice(separator + 1)
      return { lookupKey: body.slice(0, separator), source: source === 'default' ? '' : source }
    }
    const buildFilterParams = () => {
      const params = { kind: kind.value, keyword: keyword.value }
      if (recordSource.value) params.source = recordSource.value
      if (addressStatus.value) params.addressStatus = addressStatus.value
      if (fetchedFrom.value) params.fetchedFrom = fetchedFrom.value
      if (fetchedTo.value) params.fetchedTo = fetchedTo.value
      return params
    }
    const buildBatchDeletePayload = () => {
      const filters = buildFilterParams()
      if (selectAllMode.value) {
        return {
          ...filters,
          source: recordSource.value,
          items: [],
          all: true,
          except: [...deselectedKeys.value].map(selectionItemFromIdentity).filter(Boolean),
        }
      }
      return {
        ...filters,
        source: recordSource.value,
        items: [...selectedKeys.value].map(selectionItemFromIdentity).filter(Boolean),
        all: false,
        except: [],
      }
    }
    const buildBatchRefreshPayload = () => ({
      ...buildBatchDeletePayload(),
      querySource: kind.value === 'ip' ? querySource.value : '',
    })
    const selectedDetailForRefresh = (payload) => {
      if (!detailVisible.value || !detailOwner?.isCurrent() || detailRecord.value.kind !== payload.kind) return null
      const record = detailRecord.value
      const matchesRecord = item => item.lookupKey === record.lookupKey && (item.source || '') === (record.source || '')
      const filter = String(payload.keyword || '').trim().toLowerCase()
      const wantedSource = payload.source === 'default' ? '' : payload.source
      const matchesFilter = (!payload.source || (record.source || '') === wantedSource)
        && (!filter || [record.lookupKey, record.address].some(value => String(value || '').toLowerCase().includes(filter)))
      const selected = payload.all ? matchesFilter && !payload.except.some(matchesRecord) : payload.items.some(matchesRecord)
      return selected ? { ...record } : null
    }
    const updateRefreshedDetail = (original, refreshed) => {
      if (!original || !detailOwner?.isCurrent() || recordIdentity(detailRecord.value) !== recordIdentity(original)) return
      if (refreshed) detailRecord.value = { ...refreshed }
      else retireDetail()
    }
    const refreshSelected = async () => {
      if (!selectedCount.value || batchRefreshing.value || isRefreshDisabled() || !isListCurrent()) return
      if (!canAct()) return
      const action = beginAction('refreshSelected', value => { batchRefreshing.value = value })
      if (!action) return
      const owns = action.owns
      const count = selectedCount.value
      const payload = buildBatchRefreshPayload()
      const confirmation = await confirmOwned('refreshSelected', {
        title: '确认重新查询所选归属地？',
        content: `将${kind.value === 'ip' ? `按${sourceLabel(payload.querySource)}` : ''}重新查询 ${count} 条记录。`,
        okText: '重新查询',
        cancelText: '取消',
      }, owns)
      if (!confirmation || !owns()) { action.finish(); return }
      errorMessage.value = ''
      try {
        if (!owns()) return
        const res = await refreshLocationCacheBatch(payload)
        if (!owns()) return
        updateConfigured(res?.data?.configured)
        assertResponse(res, '批量重新查询失败')
        const summary = res?.data?.data || {}
        const refreshed = Number(summary.refreshed) || 0
        const failed = Number(summary.failed) || 0
        clearSelectionState()
        let failureMessage = ''
        if (failed > 0) {
          const detail = Array.isArray(summary.failures) && summary.failures.length
            ? `：${summary.failures.slice(0, 3).map(item => `${item.lookupKey || '-'}（${sourceLabel(item.source)}）：${fixedReason(item.message)}`).join('；')}`
            : ''
          failureMessage = `已成功重新查询 ${refreshed} 条，${failed} 条失败${detail}`
        } else {
          message.success(`已重新查询 ${refreshed} 条`)
        }
        if (!owns()) return
        const loaded = await load(owns)
        if (!owns()) return
        if (!loaded) {
          if (failureMessage) errorMessage.value = failureMessage + '；' + errorMessage.value
          return
        }
        const detailToRefresh = selectedDetailForRefresh(payload)
        const detailFailed = detailToRefresh && Array.isArray(summary.failures) && summary.failures.some(item =>
          item.lookupKey === detailToRefresh.lookupKey && (item.source || '') === (detailToRefresh.source || ''))
        if (detailToRefresh && !detailFailed) {
          const refreshedDetail = rows.value.find(row => row.kind === payload.kind && row.lookupKey === detailToRefresh.lookupKey && (row.source || '') === payload.querySource)
          updateRefreshedDetail(detailToRefresh, refreshedDetail)
        }
        if (failureMessage) errorMessage.value = failureMessage
      } catch (error) {
        if (!owns()) return
        errorMessage.value = requestErrorMessage(error, '批量重新查询失败')
      } finally {
        action.finish()
      }
    }
    const deleteSelected = async () => {
      if (!selectedCount.value || batchDeleting.value || !isListCurrent()) return
      if (!canAct()) return
      const action = beginAction('deleteSelected', value => { batchDeleting.value = value })
      if (!action) return
      const owns = action.owns
      const count = selectedCount.value
      const payload = buildBatchDeletePayload()
      const confirmation = await confirmOwned('deleteSelected', {
        title: '确认删除所选归属地？',
        content: `将删除 ${count} 条记录，删除后下次展示时会重新查询。`,
        okText: '删除',
        cancelText: '取消',
      }, owns)
      if (!confirmation || !owns()) { action.finish(); return }
      errorMessage.value = ''
      try {
        if (!owns()) return
        const res = await deleteLocationCacheBatch(payload)
        if (!owns()) return
        assertResponse(res, '批量删除失败')
        const deleted = Number(res?.data?.deleted)
        clearSelectionState()
        message.success(Number.isFinite(deleted) ? `已删除 ${deleted} 条` : '已删除')
        if (!owns()) return
        const loaded = await load(owns)
        if (!owns() || !loaded) return
        if (rows.value.length === 0 && pagination.current > 1) {
          pagination.current--
          if (!owns()) return
          const loaded = await load(owns)
          if (!owns() || !loaded) return
        }
      } catch (error) {
        if (!owns()) return
        errorMessage.value = requestErrorMessage(error, '批量删除失败')
      } finally {
        action.finish()
      }
    }
    const deleteAll = async () => {
      if (allDeleting.value || !isListCurrent()) return
      if (!canAct()) return
      const action = beginAction('deleteAll', value => { allDeleting.value = value })
      if (!action) return
      const owns = action.owns
      errorMessage.value = ''
      try {
        if (!owns()) return
        const res = await deleteLocationCacheBatch({ ...buildFilterParams(), source: recordSource.value, items: [], all: true, except: [] })
        if (!owns()) return
        assertResponse(res, '全部删除失败')
        clearSelectionState()
        pagination.current = 1
        message.success(`已删除 ${Number(res?.data?.deleted) || 0} 条`)
        if (!owns()) return
        const loaded = await load(owns)
        if (!owns() || !loaded) return
      } catch (error) {
        if (!owns()) return
        errorMessage.value = requestErrorMessage(error, '全部删除失败')
      } finally {
        action.finish()
      }
    }
    const manualLookup = async () => {
      const lookupKey = manualKey.value.trim()
      const dialog = manualOwner
      if (!lookupKey || manualSubmitting.value || isRefreshDisabled()) return
      if (!canAct()) return
      const action = beginAction('manualLookup', value => { manualSubmitting.value = value })
      if (!action) return
      const owns = action.owns

      errorMessage.value = ''
      try {
        if (!owns()) return
        if (dialog && !dialog.isCurrent()) return
        const res = await manualLookupLocationCache({ kind: kind.value, lookupKey, source: kind.value === 'ip' ? querySource.value : '' })
        if (!owns() || (dialog && !dialog.isCurrent())) return
        updateConfigured(res?.data?.configured)
        assertResponse(res, '手动查询失败')
        retireManual()
        message.success('查询成功，已保存归属地')
        if (!owns()) return
        const loaded = await load(owns)
        if (!owns() || !loaded) return
      } catch (error) {
        if (!owns()) return
        if (dialog && !dialog.isCurrent()) return
        errorMessage.value = `${lookupKey}：${requestErrorMessage(error, '手动查询失败')}`
      } finally {
        action.finish()
      }
    }
    const updateSummary = (data) => {
      const source = data?.summary || data?.statistics || {}
      const total = Number(source.total ?? data?.total ?? 0) || 0
      const withAddress = Number(source.withAddress ?? source.resolved ?? source.cached ?? rows.value.filter(row => String(row.address || '').trim()).length) || 0
      const withoutAddress = Number(source.withoutAddress ?? source.unresolved ?? Math.max(0, total - withAddress)) || 0
      summary.value = { total, withAddress, withoutAddress }
    }
    let loadGeneration = 0
    const load = async parentOwns => {
      if (!canAct() || (typeof parentOwns === 'function' && !parentOwns())) return false
      if (typeof parentOwns !== 'function') beginQuery()
      const owns = captureQuery()
      const generation = ++loadGeneration
      loading.value = true
      errorMessage.value = ''
      try {
        const res = await getLocationCacheList({ ...buildFilterParams(), page: pagination.current, pageSize: pagination.pageSize })
        if (!owns() || generation !== loadGeneration) return false
        assertResponse(res, '获取归属地信息失败')
        if (res?.data?.configured === false) updateConfigured(false)
        if (!Array.isArray(res?.data?.data) || !res.data.data.every(row => row && typeof row === 'object' && row.kind === kind.value && typeof row.lookupKey === 'string') || typeof res?.data?.total !== 'number' || !Number.isSafeInteger(res.data.total) || res.data.total < 0) throw new Error('Invalid list')
        const data = res.data || {}
        rows.value = data.data
        const rowOwner = captureOwner()
        rows.value.forEach(row => rowOwners.set(row, rowOwner))
        pagination.total = Number(data.total || 0)
        pagination.current = Number(data.page || pagination.current)
        pagination.pageSize = Number(data.pageSize || pagination.pageSize)
        configured.value = typeof data.configured === 'boolean' ? data.configured : undefined
        updateConfigured(data.configured)
        updateSummary(data)
        lastSuccessfulListQuery.value = listQueryFingerprint()
        closeDeletePrompts(); renewEntry()
        return true
      } catch (error) {
        if (owns() && generation === loadGeneration) errorMessage.value = requestErrorMessage(error, '获取归属地信息失败')
        return false
      } finally {
        if (owns() && generation === loadGeneration) loading.value = false
      }
    }
    const reload = () => {
      if (!canAct()) return
      clearSelectionState(); keyword.value = keywordInput.value.trim()
      recordSource.value = recordSourceInput.value; addressStatus.value = addressStatusInput.value
      fetchedFrom.value = fetchedFromInput.value; fetchedTo.value = fetchedToInput.value
      pagination.current = 1; return load()
    }
    const resetFilters = () => { keywordInput.value = ''; recordSourceInput.value = ''; addressStatusInput.value = ''; fetchedFromInput.value = ''; fetchedToInput.value = ''; return reload() }
    const onTableChange = (page) => { if (!canAct()) return; pagination.current = page.current; pagination.pageSize = page.pageSize; load() }
    const showDetails = record => {
      if (!recordCurrent(record)) return
      retireDetail()
      const owns = captureOwner(), owner = { key: ++detailKey.value }
      owner.isCurrent = () => owns() && detailOwner === owner && detailVisible.value
      detailOwner = owner; detailRecord.value = { ...record }; detailVisible.value = true; detailMounted.value = true
      detailEntry.value = makeEntry(owner.isCurrent, false)
      detailAfterClose.value = () => { if (detailOwner === owner && !detailVisible.value) retireDetail() }
    }
    const refresh = async (record) => {
      if (isRefreshDisabled(record)) return
      if (!recordCurrent(record)) return
      const identity = recordIdentity(record)
      const original = { ...record }
      const source = record.kind === 'ip' ? querySource.value : ''
      const action = beginAction('refresh:' + identity, value => { refreshing[identity] = value })
      if (!action) return
      const owns = action.owns
      errorMessage.value = ''
      try {
        if (!owns()) return
        const res = await refreshLocationCache({ kind: record.kind, lookupKey: record.lookupKey, source, recordSource: record.kind === 'phone' ? '' : record.source || '' })
        if (!owns()) return
        updateConfigured(res?.data?.configured)
        assertResponse(res, '重新查询失败')
        if ((original.source || '') !== source) clearSelectionState()
        message.success('归属地已更新')
        if (!owns()) return
        const loaded = await load(owns)
        if (!owns() || !loaded) return
        const refreshed = res?.data?.data || rows.value.find(row => row.kind === original.kind && row.lookupKey === original.lookupKey && (row.source || '') === source)
        updateRefreshedDetail(original, refreshed)
      } catch (error) {
        if (!owns()) return
        errorMessage.value = `${record.lookupKey}：${requestErrorMessage(error, '重新查询失败')}`
      } finally { action.finish() }
    }
    const remove = async (record) => {
      if (!recordCurrent(record) || !isListCurrent()) return
      const identity = recordIdentity(record)
      const action = beginAction('remove:' + identity, value => { removing[identity] = value })
      if (!action) return
      const owns = action.owns
      errorMessage.value = ''
      try {
        if (!owns()) return
        const res = await deleteLocationCache({ kind: record.kind, lookupKey: record.lookupKey, source: record.kind === 'phone' ? '' : record.source || '' })
        if (!owns()) return
        assertResponse(res, '删除失败')
        if (rows.value.length === 1 && pagination.current > 1) pagination.current--
        if (selectAllMode.value) {
          const next = new Set(deselectedKeys.value)
          next.delete(identity)
          deselectedKeys.value = next
        } else {
          const next = new Set(selectedKeys.value)
          next.delete(identity)
          selectedKeys.value = next
        }
        message.success('已删除')
        if (!owns()) return
        const loaded = await load(owns)
        if (!owns() || !loaded) return
      } catch (error) {
        if (!owns()) return
        errorMessage.value = `${record.lookupKey}：${requestErrorMessage(error, '删除失败')}`
      } finally { action.finish() }
    }
    const retireScope = (resume = true) => {
      epoch += 1; queryVersion += 1; loadGeneration += 1; cacheDraining.value = true
      hidePrivateSurface(cacheSurface.value); retireManual(); retireDetail()
      for (const name of [...prompts.keys()]) closePrompt(name)
      requestDraftLeave({ path: '/login' }); closeDeletePrompts(); clearActions()
      rows.value.forEach(scrubRecord); rows.value = []
      configured.value = undefined; keywordInput.value = ''; keyword.value = ''; lastSuccessfulListQuery.value = null
      recordSource.value = ''; addressStatus.value = ''; fetchedFrom.value = ''; fetchedTo.value = ''
      recordSourceInput.value = ''; addressStatusInput.value = ''; fetchedFromInput.value = ''; fetchedToInput.value = ''
      pagination.current = 1; pagination.total = 0; clearSelectionState()
      summary.value = { total: 0, withAddress: 0, withoutAddress: 0 }; errorMessage.value = ''; loading.value = false
      markDraftSaved(); renewEntry()
      const ticket = epoch
      return nextTick().then(() => nextTick()).then(async () => {
        if (!alive || ticket !== epoch) return
        cacheMounted.value = false
        await nextTick()
        if (!alive || ticket !== epoch || !resume) return
        cacheKey.value += 1; cacheMounted.value = true; cacheDraining.value = false; renewEntry()
        if (mounted && canManageCache()) await load()
      })
    }
    const requestRouteLeave = async to => {
      const owns = captureOwner(), allow = await requestDraftLeave(to)
      if (!allow || !owns()) return false
      // This guard may be followed by another guard that cancels navigation.
      // Only a committed unmount/kind change retires the current private state.
      await nextTick()
      await nextTick()
      return owns()
    }
    onBeforeRouteLeave(requestRouteLeave)
    watch(() => [principal(), role(), kind.value], (next, previous) => {
      if (next.some((value, index) => value !== previous[index])) retireScope()
    }, { flush: 'sync' })
    onMounted(() => { mounted = true; if (canManageCache()) load() })
    onBeforeUnmount(() => { alive = false; mounted = false; retireScope(false) })
    return { recordSource, addressStatus, fetchedFrom, fetchedTo, recordSourceInput, addressStatusInput, fetchedFromInput, fetchedToInput, updateRecordSource, updateAddressStatus, updateFetchedFrom, updateFetchedTo, buildFilterParams, isListCurrent, listQueryUnconfirmed, cacheSurface, cacheMounted, cacheKey, cacheDraining, cacheEntry, tableEntry, canManageCache, canAct, noop, updateKeyword, manualMounted, manualModalKey, manualForm, manualEntry, manualAfterClose, openManual, cancelManual, updateManualOpen, updateManualKey, detailMounted, detailKey, detailSurface, detailEntry, detailAfterClose, cancelDetail, updateDetailOpen, deletePrompts, allDeletePrompt, openRowDelete, openAllDelete, hasUnsavedChanges, requestRouteLeave, rows, loading, configured, querySource, setQuerySource, errorMessage, keywordInput, pagination, columns, formatDate, formatRawResponse, reload, resetFilters, load, onTableChange, remove, refresh, refreshing, removing, recordIdentity, sourceLabel, isRefreshDisabled, detailVisible, detailRecord, showDetails, kind, kindLabel, pageTitle, summary, selectedCount, selectedRowKeys, allRowsSelected, allCurrentPageSelected, hasCurrentPageSelection, selectionSummary, rowSelection, handleSelectCurrentPage, handleClearCurrentPage, handleSelectAll, handleClearSelection, batchRefreshing, refreshSelected, batchDeleting, deleteSelected, allDeleting, deleteAll, manualVisible, manualSubmitting, manualKey, manualLookup }
  },
})
</script>

<style scoped>
.keyword-input { width: 280px; max-width: 100%; }
.query-source-select { width: 160px; }
.query-source-hint { margin: 14px 0 0; color: var(--console-muted); font-size: 13px; line-height: 1.7; }
.status-alert { margin-top: 16px; }
.selection-alert { margin-bottom: 20px; }
.selection-alert-content { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.response-title { margin: 20px 0 8px; font-size: 14px; }
.raw-response { max-height: 420px; padding: 16px; margin: 0; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--console-text); background: var(--console-subtle); border: 1px solid var(--console-border); border-radius: 8px; }
@media (max-width: 600px) { .keyword-input { width: 100%; } .filter-form :deep(.ant-form-item) { width: 100%; } .filter-form :deep(.ant-form-item-control) { flex: 1; } }
</style>
