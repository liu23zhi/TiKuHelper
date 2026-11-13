<template>
  <div class="console-page sensitive-word-page">
    <a-alert v-if="!canManage" type="warning" show-icon message="当前身份无敏感词检测记录管理权限，请使用管理员账号登录。" />
    <template v-else>
      <ConsolePageHeading title="敏感词检测记录" description="查看按文本哈希缓存的敏感词检测结论，并按命中/未命中/失败筛选。">
        <template #actions>
          <a-button :loading="loading" :disabled="!canAct()" @click="load">刷新列表</a-button>
        </template>
      </ConsolePageHeading>

      <ConsoleSummaryGrid>
        <a-statistic title="命中（全库）" :value="hasLoaded ? counts.hit : '--'" suffix="条" />
        <a-statistic title="未命中（全库）" :value="hasLoaded ? counts.clean : '--'" suffix="条" />
        <a-statistic title="失败（全库）" :value="hasLoaded ? counts.failed : '--'" suffix="条" />
        <a-statistic title="筛选结果" :value="hasLoaded ? total : '--'" suffix="条" />
      </ConsoleSummaryGrid>

      <!-- 说明：这张表只存结论，不存原文。这句必须显式写出来 ——
           否则管理员会以为页面把原文藏起来了。 -->
      <a-alert
        type="info"
        show-icon
        class="cache-hint-alert"
        message="缓存按设计只保存文本哈希与结论，不保存题目/答案原文，因此这里没有原文可看。屏蔽文本（MaskedText）是上游返回的参考值，仅供排障。"
      />

      <ConsolePanel title="筛选" description="关键词匹配文本哈希、屏蔽文本、上游状态与失败原因；检测时间按上海时间筛选，包含结束日期当天。">
        <a-form layout="inline" class="filter-form" @finish="reload">
          <a-form-item label="关键词" :html-for="fieldId('keyword')">
            <a-input
              :id="fieldId('keyword')"
              :value="filters.keyword"
              allow-clear
              placeholder="文本哈希 / 屏蔽文本 / 状态 / 失败原因"
              class="keyword-input"
              :disabled="!canAct()"
              @update:value="value => setFilter('keyword', value)"
            />
          </a-form-item>
          <a-form-item label="检测结论" :html-for="fieldId('state')">
            <a-select
              :id="fieldId('state')"
              :value="filters.state"
              class="state-select"
              :disabled="!canAct()"
              @update:value="value => setFilter('state', value)"
            >
              <a-select-option value="">全部结论</a-select-option>
              <a-select-option v-for="state in stateOptions" :key="state" :value="state">
                {{ stateLabels[state] }}
              </a-select-option>
            </a-select>
          </a-form-item>
          <a-form-item label="检测时间">
            <a-space class="date-filter-space">
              <a-input
                :value="filters.checkedFrom"
                type="date"
                aria-label="检测时间起始日期"
                :disabled="!canAct()"
                @update:value="value => setFilter('checkedFrom', value)"
              />
              <span>至</span>
              <a-input
                :value="filters.checkedTo"
                type="date"
                aria-label="检测时间结束日期"
                :disabled="!canAct()"
                @update:value="value => setFilter('checkedTo', value)"
              />
            </a-space>
          </a-form-item>
          <a-form-item>
            <a-space wrap>
              <a-button type="primary" html-type="submit" :loading="loading" :disabled="!canAct()">查询</a-button>
              <a-button :disabled="!canAct()" @click="resetFilters">重置筛选</a-button>
            </a-space>
          </a-form-item>
        </a-form>
        <a-alert v-if="errorMessage" type="error" show-icon :message="errorMessage" class="status-alert" />
      </ConsolePanel>

      <ConsolePanel title="检测记录" :description="scopeDescription">
        <ConsoleTableShell label="敏感词检测记录">
          <a-table
            table-layout="fixed"
            :data-source="rows"
            :columns="columns"
            :loading="loading"
            :pagination="pagination"
            row-key="textHash"
            :scroll="{ x: tableScrollX }"
            :locale="{ emptyText: errorMessage ? '列表加载失败，请重试' : hasLoaded ? '暂无符合条件的检测记录' : '检测记录尚未读取成功' }"
            @change="onTableChange"
          >
            <template #bodyCell="{ column, record }">
              <a-tag v-if="column.key === 'state'" :color="stateColor(record.state)">
                {{ stateLabels[record.state] || record.state }}
              </a-tag>
              <code v-else-if="column.key === 'textHash'" class="hash-value">{{ record.textHash || '-' }}</code>
              <!-- MaskedText 可看（上游返回的屏蔽后文本），原文不可看（后端不存）。 -->
              <span v-else-if="column.key === 'maskedText'" class="masked-value">{{ record.maskedText || '—' }}</span>
              <span v-else-if="column.key === 'words'">{{ formatWords(record.words) }}</span>
              <span v-else-if="column.key === 'apiStatus'">{{ record.apiStatus || '—' }}</span>
              <span v-else-if="column.key === 'error'" class="error-value">{{ record.error || '—' }}</span>
              <span v-else-if="column.key === 'attemptCount'">{{ record.attemptCount ?? 0 }}</span>
              <span v-else-if="column.key === 'checkedAt'">{{ formatDate(record.checkedAt) }}</span>
              <span v-else>{{ record[column.dataIndex] ?? '-' }}</span>
            </template>
          </a-table>
        </ConsoleTableShell>
      </ConsolePanel>
    </template>
  </div>
</template>

<script>
import { computed, defineComponent, reactive, ref, useId } from 'vue'
import {
  SENSITIVE_WORD_CACHE_STATES,
  SENSITIVE_WORD_CACHE_STATE_LABELS,
  getSensitiveWordCacheEntries,
  getSensitiveWordCacheStatus,
} from '@/api/SensitiveWordApi'
import {
  buildSensitiveWordCacheListParams,
  formatSensitiveWordCacheDate,
  formatSensitiveWordCacheWords,
  sensitiveWordCacheStateColor,
} from '@/utils/sensitiveWordCacheHelpers.mjs'
import { useCurrentUserDisplay, isAdminUser } from '@/utils/currentUser'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

export default defineComponent({
  name: 'SensitiveWordCachePage',
  // ConsoleTableShell / ConsolePanel / ConsoleSummaryGrid / ConsolePageHeading
  // 都已在 main.js 里 app.component 全局注册，这里不重复注册。
  setup() {
    const fieldIdPrefix = 'sensitive-word-' + useId()
    const fieldId = field => fieldIdPrefix + '-' + field
    const { currentUser } = useCurrentUserDisplay()
    const rows = ref([])
    const loading = ref(false)
    const hasLoaded = ref(false)
    const errorMessage = ref('')
    const total = ref(0)
    const counts = reactive({ hit: 0, clean: 0, failed: 0 })
    const pagination = reactive({ current: 1, pageSize: 20, total: 0, showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'] })
    const emptyFilters = () => ({ keyword: '', state: '', checkedFrom: '', checkedTo: '' })
    const filters = reactive(emptyFilters())
    const appliedFilters = ref({})
    let loadSequence = 0
    let alive = true

    const canManage = computed(() => isAdminUser(currentUser.value))
    const canAct = () => alive && canManage.value && !loading.value
    // retire 由 beforeUnmount 调用：置存活标记为 false，让在途响应不再写状态。
    const retire = () => { alive = false }

    const stateOptions = SENSITIVE_WORD_CACHE_STATES
    const stateLabels = SENSITIVE_WORD_CACHE_STATE_LABELS
    const stateColor = sensitiveWordCacheStateColor
    const formatWords = formatSensitiveWordCacheWords
    const formatDate = formatSensitiveWordCacheDate

    const scopeDescription = computed(() => {
      if (!hasLoaded.value) return '尚未读取。'
      const parts = []
      if (appliedFilters.value.state) parts.push(`结论：${stateLabels[appliedFilters.value.state] || appliedFilters.value.state}`)
      if (appliedFilters.value.keyword) parts.push(`关键词：${appliedFilters.value.keyword}`)
      if (appliedFilters.value.checkedFrom || appliedFilters.value.checkedTo) {
        parts.push(`时间：${appliedFilters.value.checkedFrom || '不限'} 至 ${appliedFilters.value.checkedTo || '不限'}`)
      }
      return parts.length ? `当前筛选 — ${parts.join('；')}` : '当前筛选 — 全部检测记录'
    })

    const columns = computed(() => [
      { title: '检测结论', dataIndex: 'state', key: 'state', width: 110 },
      { title: '文本哈希', dataIndex: 'textHash', key: 'textHash', width: 260 },
      { title: '屏蔽文本（参考）', dataIndex: 'maskedText', key: 'maskedText', width: 280 },
      { title: '命中词', dataIndex: 'words', key: 'words', width: 200 },
      { title: '上游状态', dataIndex: 'apiStatus', key: 'apiStatus', width: 140 },
      { title: '失败原因', dataIndex: 'error', key: 'error', width: 220 },
      { title: '失败次数', dataIndex: 'attemptCount', key: 'attemptCount', width: 110 },
      { title: '检测时间（上海）', dataIndex: 'checkedAt', key: 'checkedAt', width: 190 },
    ])
    const tableScrollX = computed(() => sumTableColumnWidths(columns.value))

    const setFilter = (key, value) => { filters[key] = value }
    const resetFilters = () => {
      Object.assign(filters, emptyFilters())
      appliedFilters.value = {}
      pagination.current = 1
      return load()
    }

    const load = async () => {
      if (!alive || !canManage.value) return
      const sequence = ++loadSequence
      const current = () => alive && sequence === loadSequence
      loading.value = true
      errorMessage.value = ''
      try {
        const params = buildSensitiveWordCacheListParams(filters, pagination.current, pagination.pageSize)
        const res = await getSensitiveWordCacheEntries(params, { errorFeedback: 'local' })
        if (!current()) return
        if (res?.status !== 200) {
          errorMessage.value = res?.message || '获取敏感词检测记录失败，请重试。'
          return
        }
        const data = res?.data || {}
        rows.value = Array.isArray(data.data) ? data.data : []
        total.value = Number(data.total || 0)
        pagination.total = total.value
        if (Number.isInteger(data.page) && data.page > 0) pagination.current = data.page
        if (Number.isInteger(data.pageSize) && data.pageSize > 0) pagination.pageSize = data.pageSize
        appliedFilters.value = { ...filters }
        hasLoaded.value = true
        // 三个统计卡片是**全库分布**，数据源是 status 接口而不是 entries。
        //
        // entries 响应里的 data.states 是**当前筛选条件下**的计数：一旦 state=hit，
        // 它就变成 hit=筛选命中数 / clean=0 / failed=0，三个数字失去「分布」语义。
        // 而「筛选结果」那张卡片已经用 total 表达了同一件事，所以这里刻意分开：
        // counts 取全库（status），total 取筛选（entries）。
        //
        // status 是旁路统计，失败**不能**让整页报错 —— 列表已经拿到了，只是三个
        // 数字暂时不准；保留上一次的值比清成 0 更诚实。
        try {
          const statusRes = await getSensitiveWordCacheStatus({ errorFeedback: 'local' })
          if (current() && statusRes?.status === 200) {
            const states = statusRes?.data?.states || {}
            counts.hit = Number(states.hit || 0)
            counts.clean = Number(states.clean || 0)
            counts.failed = Number(states.failed || 0)
          }
        } catch (statusError) {
          // 静默：统计失败不影响列表可用性。
        }
      } catch (error) {
        if (!current()) return
        errorMessage.value = error?.message || '获取敏感词检测记录失败，请重试。'
      } finally {
        if (current()) loading.value = false
      }
    }

    const reload = () => { pagination.current = 1; return load() }
    const onTableChange = (page) => {
      pagination.current = page?.pageSize === pagination.pageSize ? page.current : 1
      pagination.pageSize = page?.pageSize || pagination.pageSize
      return load()
    }

    // 首屏加载交给 mounted（见下方 Options API 部分），这里不重复发请求。
    return {
      fieldId, rows, loading, hasLoaded, errorMessage, total, counts, pagination, filters,
      canManage, canAct, stateOptions, stateLabels, stateColor, formatWords, formatDate,
      scopeDescription, columns, tableScrollX, setFilter, resetFilters, load, reload, onTableChange,
      retire,
    }
  },
  mounted() {
    // 管理员进入页面即读一次；非管理员由模板的告警提示，不发请求。
    if (this.canManage) this.load()
  },
  beforeUnmount() {
    // 卸载后不再写任何状态（避免把旧响应写进已销毁的实例）。
    this.retire()
  },
})
</script>

<style scoped>
.sensitive-word-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}

.cache-hint-alert {
  margin-bottom: 0;
}

.filter-form {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
}

.keyword-input {
  width: 280px;
}

.state-select {
  width: 160px;
}

.status-alert {
  margin-top: 12px;
}

.hash-value {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  overflow-wrap: anywhere;
}

.masked-value,
.error-value {
  overflow-wrap: anywhere;
}

.error-value {
  color: var(--console-danger, #c13e41);
}

@media (max-width: 640px) {
  .keyword-input,
  .state-select {
    width: 100%;
  }
}
</style>
