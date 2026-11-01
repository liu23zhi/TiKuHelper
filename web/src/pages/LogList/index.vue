<template>
  <div class="console-page log-page">
    <ConsolePageHeading title="操作日志" description="按关键词、模块和操作类型查看操作记录。">
      <template #actions><a-button type="primary" :loading="tabLoading" @click="handleLogList">刷新</a-button></template>
    </ConsolePageHeading>
    <ConsolePanel class="filter-panel">
      <a-form layout="inline" class="filter-form">
        <a-form-item label="关键词">
          <a-input-search
            v-model:value="keywordInput"
            placeholder="说明 / 用户名 / IP"
            allow-clear
            style="width: 240px"
            @search="applyKeyword"
            @press-enter="applyKeyword"
          />
        </a-form-item>
        <a-form-item label="模块">
          <a-select
            v-model:value="filters.module"
            allow-clear
            placeholder="全部模块"
            style="width: 160px"
            :options="moduleOptions"
            @change="onFilterChange"
          />
        </a-form-item>
        <a-form-item label="操作类型">
          <a-select
            v-model:value="filters.action"
            allow-clear
            placeholder="全部操作"
            style="width: 160px"
            :options="actionOptions"
            @change="onFilterChange"
          />
        </a-form-item>
        <a-form-item label="用户 ID">
          <a-input-number v-model:value="filters.userId" :min="1" placeholder="精确用户" style="width: 130px" @change="onFilterChange" />
        </a-form-item>
        <a-form-item label="题目 ID">
          <a-input-number v-model:value="filters.qid" :min="1" placeholder="精确题目" style="width: 130px" @change="onFilterChange" />
        </a-form-item>
        <a-form-item label="客户端 IP">
          <a-input v-model:value="filters.clientIp" allow-clear placeholder="精确 IP" style="width: 160px" @press-enter="onFilterChange" @blur="onFilterChange" />
        </a-form-item>
        <a-form-item label="开始日期">
          <a-input v-model:value="filters.createdFrom" type="date" style="width: 160px" @change="onFilterChange" />
        </a-form-item>
        <a-form-item label="结束日期">
          <a-input v-model:value="filters.createdTo" type="date" style="width: 160px" @change="onFilterChange" />
        </a-form-item>
        <a-form-item>
          <a-space>
            <a-button @click="resetFilters">重置筛选</a-button>
            <a-tag v-if="activeFilterCount > 0" color="blue">已启用 {{ activeFilterCount }} 个筛选条件</a-tag>
          </a-space>
        </a-form-item>
      </a-form>
      <a-alert
        type="info"
        show-icon
        class="page-alert"
        :message="isAdmin ? '当前显示全站审计日志，可按关键词（说明 / 用户名 / 客户端 IP）、模块与操作类型筛选。搜题日志会拆分展示请求接口、请求参数、请求 ID、命中渠道及错误原因、计费渠道以及原始请求 / 响应快照。' : '当前仅显示你自己的操作日志，可按关键词（说明 / 用户名 / 客户端 IP）、模块与操作类型筛选。搜题日志会拆分展示请求接口、请求参数、请求 ID、命中渠道及错误原因、计费渠道以及原始请求 / 响应快照。'"
      />
    </ConsolePanel>

    <ConsolePanel class="table-card">
      <ConsoleTableShell label="操作日志记录">
        <a-table
          class="desktop-log-table"
          bordered
          :data-source="dataSource"
          :columns="columns"
          :loading="tabLoading"
          :pagination="pagination"
          row-key="id"
          table-layout="fixed"
          :scroll="{ x: tableScrollX }"
          @change="handleTableChange"
        >
          <template #bodyCell="{ column, record, text }">
            <template v-if="column.key === 'module'">
              <a-tag color="blue">{{ buildModuleLabel(record.module) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'status'">
              <a-tag :color="buildStatusColor(record)">
                {{ buildStatusLabel(record) }}
              </a-tag>
            </template>
            <template v-else-if="column.key === 'username'">
              <span>{{ buildLogUsername(record) }}</span>
            </template>
            <template v-else-if="column.key === 'summary'">
              <span>{{ buildStructuredFields(record).summary }}</span>
            </template>
            <template v-else-if="column.key === 'detail'">
              <OverflowTableText
                :text="buildTableDetail(record)"
                :expanded="isSnapshotExpanded(record.id, 'detail')"
                @toggle="toggleSnapshotExpanded(record.id, 'detail')"
              />
            </template>
            <template v-else-if="column.key === 'requestId'">
              <span>{{ buildStructuredFields(record).requestId }}</span>
            </template>
            <template v-else-if="column.key === 'mode'">
              <span class="log-params-cell">{{ buildStructuredFields(record, '\n').mode }}</span>
            </template>
            <template v-else-if="column.key === 'requestSource'">
              <span>{{ buildStructuredFields(record).requestSource }}</span>
            </template>
            <template v-else-if="column.key === 'answerCount'">
              <span>{{ buildStructuredFields(record).answerCount }}</span>
            </template>
            <template v-else-if="column.key === 'hitProvider'">
              <div class="log-channel-cell">
                <span class="log-channel-name">{{ buildStructuredFields(record).hitProvider }}</span>
                <OverflowTableText
                  v-if="buildStructuredFields(record).failureReasons.length"
                  class="log-channel-error"
                  :text="buildStructuredFields(record).failureReasons.join('\n')"
                  :expanded="isSnapshotExpanded(record.id, 'hitProvider')"
                  @toggle="toggleSnapshotExpanded(record.id, 'hitProvider')"
                />
              </div>
            </template>
            <template v-else-if="column.key === 'billingProvider'">
              <span>{{ buildStructuredFields(record).billingProvider }}</span>
            </template>
            <template v-else-if="column.key === 'requestSnapshot'">
              <OverflowTableText
                :text="buildStructuredFields(record).requestSnapshot"
                :expanded="isSnapshotExpanded(record.id, 'requestSnapshot')"
                @toggle="toggleSnapshotExpanded(record.id, 'requestSnapshot')"
              />
            </template>
            <template v-else-if="column.key === 'responseSnapshot'">
              <OverflowTableText
                :text="buildStructuredFields(record).responseSnapshot"
                :expanded="isSnapshotExpanded(record.id, 'responseSnapshot')"
                @toggle="toggleSnapshotExpanded(record.id, 'responseSnapshot')"
              />
            </template>
            <template v-else-if="column.key === 'clientIp'">
              <span class="location-value">{{ formatValueWithLocation(record.clientIp, ipLocations[normalizeLocationKey(record.clientIp)]) || '-' }}</span>
            </template>
            <template v-else-if="column.key === 'qid'">
              <span>{{ buildLogLocalQuestionId(record) }}</span>
            </template>
            <template v-else-if="column.key === 'billingIds'">
              <span class="log-billing-ids">{{ record.billingIds?.length ? record.billingIds.join('\n') : '-' }}</span>
            </template>
            <template v-else-if="column.key === 'createTime'">
              <span class="log-time-cell">{{ formatDateForTable(text || record.createTime) }}</span>
            </template>
            <template v-else>{{ text }}</template>
          </template>
        </a-table>
      </ConsoleTableShell>
      <div class="mobile-log-list">
        <article v-for="record in dataSource" :key="record.id" class="mobile-log-card">
          <header class="mobile-log-card__header">
            <a-tag color="blue">{{ buildModuleLabel(record.module) }}</a-tag>
            <a-tag :color="buildStatusColor(record)">{{ buildStatusLabel(record) }}</a-tag>
          </header>
          <strong>{{ buildStructuredFields(record).summary }}</strong>
          <dl class="mobile-log-card__meta">
            <div><dt>操作人</dt><dd>{{ buildLogUsername(record) }}</dd></div>
            <div><dt>时间</dt><dd class="mobile-log-time">{{ formatDateForTable(record.createTime) }}</dd></div>
            <div><dt>IP</dt><dd>{{ record.clientIp || '-' }}</dd></div>
          </dl>
          <p class="mobile-log-card__detail">{{ buildRowDetail(record) }}</p>
        </article>
        <a-empty v-if="!dataSource.length" description="当前还没有日志记录" />
      </div>
      <div class="mobile-log-pagination">
        <a-pagination :current="pagination.current" :page-size="pagination.pageSize" :total="pagination.total" size="small" show-less-items @change="handleMobilePageChange" />
      </div>
    </ConsolePanel>
  </div>
</template>

<script>
import { getLogList, getIpLocations } from '@/api/TikuComponentApi';
import { message } from 'ant-design-vue';
import { defineComponent, ref, reactive, computed } from 'vue';
import OverflowTableText from '@/components/OverflowTableText.vue';
import { useTableExpansion } from '@/composables/useTableExpansion.mjs';
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs';
import { getStoredCurrentUser, isAdminUser } from '@/utils/currentUser';
import { buildSearchParams, countActiveFilters, normalizeSearchKeyword } from '@/utils/listSearchHelpers.mjs';
import {
  collectLocationKeys,
  lookupLocationAddresses,
  normalizeLocationKey,
  formatValueWithLocation as formatValueWithLocationBase,
} from '@/utils/locationHelpers.mjs';
import {
  LOG_MODULE_OPTIONS,
  LOG_ACTION_OPTIONS,
  buildLogModuleLabel,
  buildLogUsername,
  buildLogLocalQuestionId,
  buildLogQidLabel,
  buildLogRowDetail,
  buildLogTableDetail,
  buildLogStatusColor,
  buildLogStatusLabel,
  buildLogStructuredFields,
  formatShanghaiLogDate,
} from '@/utils/logListHelpers.mjs';
const dataSource = ref([])
const tabLoading = ref(true)
const currentUser = ref(getStoredCurrentUser())

// 归属地映射：key 为归一化后的 IP，value 为展示文本。
// 渲染时按行取值，无需改写 dataSource 的行对象。
const ipLocations = reactive({})

function formatValueWithLocation(rawValue, address) {
  return formatValueWithLocationBase(rawValue, address)
}

// enrichClientIps 批量拉取本页来源 IP 的归属地。
//
// 失败静默：归属地只是展示增强，未配置密钥或上游异常时对应列退化为只显示原始 IP。
function enrichClientIps(rows) {
  const ips = collectLocationKeys(rows, 'clientIp')
  if (!ips.length) {
    return
  }
  lookupLocationAddresses('ip', ips, getIpLocations)
    .then((addresses) => {
      Object.keys(addresses).forEach((key) => {
        ipLocations[normalizeLocationKey(key)] = addresses[key]
      })
    })
}
const filters = reactive({
  keyword: '',
  module: undefined,
  action: undefined,
  userId: undefined,
  qid: undefined,
  clientIp: '',
  createdFrom: '',
  createdTo: '',
})
// 关键词输入框的即时值：只有点「搜索」或回车才生效，避免每敲一个字就请求一次。
const keywordInput = ref('')
const pagination = reactive({
  current: 1,
  pageSize: 10,
  total: 0,
  showSizeChanger: true,
  showQuickJumper: true,
  showTotal: (total) => `共 ${total} 条`,
  pageSizeOptions: ['10', '20', '50', '100']
})
const moduleLabels = LOG_MODULE_OPTIONS.map((option) => option.label)
const logColumnWidths = {
  createTime: 112, // 2026-10-25 plus cell padding
  module: Math.max(...moduleLabels.map((label) => [...label].reduce((width, character) => width + (character.codePointAt(0) > 127 ? 13 : 7), 0))) + 48,
  status: 76,
  username: 170,
  clientIp: 262, // （中国 新疆 乌鲁木齐 中国教育网）
  qid: 104,
  billingIds: 104,
  summary: 156, // 远程验证码二验成功
  hitProvider: 170,
  billingProvider: 170,
  detail: 170,
  requestId: 310, // UUID request ID
  mode: 160, // deepThinking=true
  requestSource: 124,
  answerCount: 76,
  requestSnapshot: 170,
  responseSnapshot: 170,
}
export default defineComponent({
  name: 'LogList',
  components: { OverflowTableText },
  setup() {
    const snapshots = useTableExpansion(dataSource)
    const expandedColumnWidth = (field) => snapshots.columnWidth(field, logColumnWidths[field], 236)
    const columns = computed(() => [
        {
          title: '时间',
          dataIndex: 'createTime',
          key: 'createTime',
          width: logColumnWidths.createTime,
        },
        {
          title: '模块',
          dataIndex: 'module',
          key: 'module',
          width: logColumnWidths.module,
        },
        {
          title: '状态',
          dataIndex: 'status',
          key: 'status',
          width: logColumnWidths.status,
        },
        {
          title: '用户',
          dataIndex: 'username',
          key: 'username',
          width: logColumnWidths.username,
        },
        {
          title: '来源 IP',
          dataIndex: 'clientIp',
          key: 'clientIp',
          width: logColumnWidths.clientIp,
        },
        {
          title: '本地题库 ID',
          dataIndex: 'qid',
          key: 'qid',
          width: logColumnWidths.qid,
        },
        {
          title: '账单 ID',
          dataIndex: 'billingIds',
          key: 'billingIds',
          width: logColumnWidths.billingIds,
        },
        {
          title: '说明',
          dataIndex: 'summary',
          key: 'summary',
          width: logColumnWidths.summary,
        },
        {
          // 紧跟「说明」：搜题失败时的原因必须第一眼看到（详见 logListHelpers 的 buildHitProviderFields）。
          title: '命中渠道 / 错误原因',
          dataIndex: 'hitProvider',
          key: 'hitProvider',
          width: expandedColumnWidth('hitProvider'),
        },
        {
          title: '计费渠道 / 耗时',
          dataIndex: 'billingProvider',
          key: 'billingProvider',
          width: logColumnWidths.billingProvider,
        },
        {
          // 详情列直接给「结果」（答案），不再放错误原因。
          title: '详情',
          dataIndex: 'detail',
          key: 'detail',
          width: expandedColumnWidth('detail'),
        },
        {
          title: '请求 ID',
          dataIndex: 'requestId',
          key: 'requestId',
          width: logColumnWidths.requestId,
        },
        {
          title: '请求参数',
          dataIndex: 'mode',
          key: 'mode',
          width: logColumnWidths.mode,
        },
        {
          title: '请求接口',
          dataIndex: 'requestSource',
          key: 'requestSource',
          width: logColumnWidths.requestSource,
        },
        {
          title: '答案数',
          dataIndex: 'answerCount',
          key: 'answerCount',
          width: logColumnWidths.answerCount,
        },
        {
          title: '原始请求',
          dataIndex: 'requestSnapshot',
          key: 'requestSnapshot',
          width: expandedColumnWidth('requestSnapshot'),
        },
        {
          title: '原始响应',
          dataIndex: 'responseSnapshot',
          key: 'responseSnapshot',
          width: expandedColumnWidth('responseSnapshot'),
        },
      ])
    const tableScrollX = computed(() => sumTableColumnWidths(columns.value))

    return {
      dataSource,
      // 归属地展示：映射 + 取值/格式化函数（模板按行取值）。
      ipLocations,
      normalizeLocationKey,
      formatValueWithLocation,
      tabLoading,
      pagination,
      filters,
      keywordInput,
      activeFilterCount: computed(() => countActiveFilters({ keyword: filters.keyword, module: filters.module, action: filters.action, userId: filters.userId, qid: filters.qid, clientIp: filters.clientIp, createdFrom: filters.createdFrom, createdTo: filters.createdTo })),
      isSnapshotExpanded: snapshots.isExpanded,
      toggleSnapshotExpanded: snapshots.toggle,
      tableScrollX,
      isAdmin: isAdminUser(currentUser.value),
      moduleOptions: LOG_MODULE_OPTIONS,
      actionOptions: LOG_ACTION_OPTIONS,
      columns,

    };
  },
  mounted() {
    this.handleLogList()
  },
  methods: {
    buildLogUsername,
    buildModuleLabel(value) {
      return buildLogModuleLabel(value)
    },
    buildStatusLabel(record) {
      return buildLogStatusLabel(record)
    },
    buildStatusColor(record) {
      return buildLogStatusColor(record)
    },
    buildStructuredFields(record, separator = ' ') {
      return buildLogStructuredFields(record, separator)
    },
    buildRowDetail(record) {
      return buildLogRowDetail(record)
    },
    buildTableDetail(record) {
      return buildLogTableDetail(record)
    },
    buildLogQidLabel(value) {
      return buildLogQidLabel(value)
    },
    buildLogLocalQuestionId(record) {
      return buildLogLocalQuestionId(record)
    },
    // 点「搜索」或回车时才把输入框内容变成生效的筛选条件。
    applyKeyword() {
      filters.keyword = normalizeSearchKeyword(keywordInput.value)
      keywordInput.value = filters.keyword
      pagination.current = 1
      this.handleLogList()
    },
    handleLogList() {
      tabLoading.value = true
      getLogList(buildSearchParams({
        keyword: filters.keyword,
        module: filters.module,
        action: filters.action === undefined ? undefined : filters.action,
        userId: filters.userId,
        qid: filters.qid,
        clientIp: filters.clientIp,
        createdFrom: filters.createdFrom,
        createdTo: filters.createdTo,
      }, pagination.current, pagination.pageSize)).then(res => {
        if (res?.status == 200) {
          dataSource.value = res?.data?.data || []
          pagination.total = res?.data?.total || 0
          pagination.current = res?.data?.page || 1
          pagination.pageSize = res?.data?.pageSize || 10
          // 来源 IP 归属地单独批量拉取（失败静默）。
          enrichClientIps(dataSource.value)
        }
      }).catch(() => {
        message.error('获取操作日志失败')
      }).finally(() => {
        tabLoading.value = false
      })
    },
    onFilterChange() {
      pagination.current = 1
      this.handleLogList()
    },
    resetFilters() {
      filters.keyword = ''
      keywordInput.value = ''
      filters.module = undefined
      filters.action = undefined
      filters.userId = undefined
      filters.qid = undefined
      filters.clientIp = ''
      filters.createdFrom = ''
      filters.createdTo = ''
      pagination.current = 1
      this.handleLogList()
    },
    handleTableChange(paginationData) {
      pagination.current = paginationData.current
      pagination.pageSize = paginationData.pageSize
      this.handleLogList()
    },
    handleMobilePageChange(current, pageSize) {
      pagination.current = current
      pagination.pageSize = pageSize
      this.handleLogList()
    },
    formatDateForTable(dateString) {
      return formatShanghaiLogDate(dateString, '\n')
    }
  }
});

</script>

<style scoped>
.filter-form { display: flex; flex-wrap: wrap; gap: 12px 0; }
.filter-form :deep(.ant-form-item) { margin-bottom: 0; }
.filter-panel :deep(.ant-alert) { margin-top: 16px; }
.log-params-cell, .log-billing-ids, .log-time-cell, .location-value { white-space: pre-wrap; overflow-wrap: anywhere; }
.log-time-cell { font-variant-numeric: tabular-nums; }
.log-channel-cell { display: grid; gap: 8px; text-align: center; }
.log-channel-name { white-space: pre-wrap; overflow-wrap: anywhere; }
.log-channel-error { color: var(--console-danger, #c13e41); }
.mobile-log-list, .mobile-log-pagination { display: none; }
@media (max-width: 640px) {
  .filter-form { display: block; }
  .filter-form :deep(.ant-form-item) { margin: 0 0 12px; }
  .filter-form :deep(.ant-form-item-control), .filter-form :deep(.ant-input-search), .filter-form :deep(.ant-select) { width: 100% !important; }
  .desktop-log-table { display: none; }
  .mobile-log-list { display: grid; gap: 12px; }
  .mobile-log-card { padding: 16px; border: 1px solid var(--console-border); border-radius: 8px; background: var(--console-surface); min-width: 0; }
  .mobile-log-card__header { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
  .mobile-log-card__meta { display: grid; gap: 8px; margin: 16px 0 0; }
  .mobile-log-card__meta > div { display: grid; grid-template-columns: 4em minmax(0, 1fr); gap: 12px; }
  .mobile-log-card__meta dt { color: var(--console-muted); }
  .mobile-log-card__meta dd { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
  .mobile-log-time { white-space: pre-line; }
  .mobile-log-card__detail { margin: 16px 0 0; white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.7; }
  .mobile-log-pagination { display: flex; justify-content: center; margin-top: 16px; }
}
</style>
