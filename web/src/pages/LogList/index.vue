<template>
  <div class="console-page log-page">
    <header class="section-heading page-heading">
      <div>
        <h1>操作日志</h1>
        <p class="page-lead">按关键词、模块和操作类型查看操作记录。</p>
      </div>
      <a-button type="primary" @click="handleLogList">刷新</a-button>
    </header>
    <section class="console-panel filter-panel">
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
    </section>

    <section class="console-panel table-card">
      <a-table
        class="desktop-log-table"
        bordered
        :data-source="dataSource"
        :columns="columns"
        :loading="tabLoading"
        :pagination="pagination"
        row-key="id"
        :scroll="{ x: 1560 }"
        @change="handleTableChange"
      >
        <template #module="{ record }">
          <a-tag color="blue">{{ buildModuleLabel(record.module) }}</a-tag>
        </template>
        <template #status="{ record }">
          <a-tag :color="buildStatusColor(record)">
            {{ buildStatusLabel(record) }}
          </a-tag>
        </template>
        <template #username="{ record }">
          <span>{{ record.username || `用户 #${record.userId || '-'}` }}</span>
        </template>
        <template #summary="{ record }">
          <span>{{ buildStructuredFields(record).summary }}</span>
        </template>
        <template #detail="{ record }">
          <span>{{ buildRowDetail(record) }}</span>
        </template>
        <template #requestId="{ record }">
          <span>{{ buildStructuredFields(record).requestId }}</span>
        </template>
        <template #mode="{ record }">
          <span>{{ buildStructuredFields(record).mode }}</span>
        </template>
        <template #requestSource="{ record }">
          <span>{{ buildStructuredFields(record).requestSource }}</span>
        </template>
        <template #answerCount="{ record }">
          <span>{{ buildStructuredFields(record).answerCount }}</span>
        </template>
        <template #hitProvider="{ record }">
          <div class="log-channel-cell">
            <span class="log-channel-name">{{ buildStructuredFields(record).hitProvider }}</span>
            <div :class="['log-channel-reasons', buildStructuredFields(record).failureReasons.join('\n').length > 150 && !isSnapshotExpanded(record.id, 'failureReasons') ? 'log-channel-reasons--collapsed' : 'log-channel-reasons--expanded']">
              <div
                v-for="(reason, index) in buildStructuredFields(record).failureReasons"
                :key="index"
                class="log-channel-error"
              >{{ reason }}</div>
            </div>
            <a-button
              v-if="buildStructuredFields(record).failureReasons.join('\n').length > 150"
              type="link"
              size="small"
              class="log-snapshot-toggle"
              @click="toggleSnapshotExpanded(record.id, 'failureReasons')"
            >
              {{ isSnapshotExpanded(record.id, 'failureReasons') ? '收起' : '展开' }}
            </a-button>
          </div>
        </template>
        <template #billingProvider="{ record }">
          <span>{{ buildStructuredFields(record).billingProvider }}</span>
        </template>
        <template #requestSnapshot="{ record }">
          <div class="log-snapshot-cell">
            <span :class="['log-snapshot-text', { 'log-snapshot-text--expanded': isSnapshotExpanded(record.id, 'requestSnapshot') }]">
              {{ buildStructuredFields(record).requestSnapshot }}
            </span>
            <a-button
              v-if="shouldShowSnapshotExpand(buildStructuredFields(record).requestSnapshot)"
              type="link"
              size="small"
              class="log-snapshot-toggle"
              @click="toggleSnapshotExpanded(record.id, 'requestSnapshot')"
            >
              {{ isSnapshotExpanded(record.id, 'requestSnapshot') ? '收起' : '展开' }}
            </a-button>
          </div>
        </template>
        <template #responseSnapshot="{ record }">
          <div class="log-snapshot-cell">
            <span :class="['log-snapshot-text', { 'log-snapshot-text--expanded': isSnapshotExpanded(record.id, 'responseSnapshot') }]">
              {{ buildStructuredFields(record).responseSnapshot }}
            </span>
            <a-button
              v-if="shouldShowSnapshotExpand(buildStructuredFields(record).responseSnapshot)"
              type="link"
              size="small"
              class="log-snapshot-toggle"
              @click="toggleSnapshotExpanded(record.id, 'responseSnapshot')"
            >
              {{ isSnapshotExpanded(record.id, 'responseSnapshot') ? '收起' : '展开' }}
            </a-button>
          </div>
        </template>
        <template #clientIp="{ record }">
          <span class="location-value">{{ formatValueWithLocation(record.clientIp, ipLocations[normalizeLocationKey(record.clientIp)]) || '-' }}</span>
        </template>
        <template #qid="{ record }">
          <span>{{ buildLogLocalQuestionId(record) }}</span>
        </template>
        <template #createTime="{ text, record }">
          {{ formatDate(text || record.createTime) }}
        </template>
      </a-table>
      <div class="mobile-log-list">
        <article v-for="record in dataSource" :key="record.id" class="mobile-log-card">
          <header class="mobile-log-card__header">
            <a-tag color="blue">{{ buildModuleLabel(record.module) }}</a-tag>
            <a-tag :color="buildStatusColor(record)">{{ buildStatusLabel(record) }}</a-tag>
          </header>
          <strong>{{ buildStructuredFields(record).summary }}</strong>
          <dl>
            <div><dt>操作人</dt><dd>{{ record.username || `用户 #${record.userId || '-'}` }}</dd></div>
            <div><dt>时间</dt><dd>{{ formatDate(record.createTime) }}</dd></div>
            <div><dt>IP</dt><dd>{{ record.clientIp || '-' }}</dd></div>
          </dl>
          <p>{{ buildRowDetail(record) }}</p>
        </article>
        <a-empty v-if="!dataSource.length" description="当前还没有日志记录" />
      </div>
      <div class="mobile-log-pagination">
        <a-pagination :current="pagination.current" :page-size="pagination.pageSize" :total="pagination.total" size="small" show-less-items @change="handleMobilePageChange" />
      </div>
    </section>
  </div>
</template>

<script>
import { getLogList, getIpLocations } from '@/api/TikuComponentApi';
import { defineComponent, ref, reactive, computed } from 'vue';
import { getStoredCurrentUser, isAdminUser } from '@/utils/currentUser';
import { buildSearchParams, countActiveFilters, normalizeSearchKeyword } from '@/utils/listSearchHelpers.mjs';
import {
  collectLocationKeys,
  lookupLocationAddresses,
  normalizeLocationKey,
  formatValueWithLocation as formatValueWithLocationBase,
} from '@/utils/locationHelpers.mjs';
import {
  buildLogModuleLabel,
  buildLogLocalQuestionId,
  buildLogQidLabel,
  buildLogRowDetail,
  buildLogStatusColor,
  buildLogStatusLabel,
  buildLogStructuredFields,
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
const expandedSnapshots = reactive({})

export default defineComponent({
  name: 'LogList',
  setup() {
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
      activeFilterCount: computed(() => countActiveFilters({ keyword: filters.keyword, module: filters.module, action: filters.action })),
      expandedSnapshots,
      isAdmin: isAdminUser(currentUser.value),
      moduleOptions: [
        { label: '认证', value: 'auth' },
        { label: '题目', value: 'question' },
        { label: '搜题', value: 'search' },
        { label: 'AI 兜底', value: 'ai_fallback' },
        { label: '系统', value: 'system' },
        { label: '验证码', value: 'captcha' },
        { label: '用户管理', value: 'user' },
        { label: '账单管理', value: 'billing' },
      ],
      actionOptions: [
        { label: '新增', value: 0 },
        { label: '更新', value: 1 },
        { label: '删除', value: 2 },
        { label: '查询 / 搜题', value: 3 },
        { label: '登录', value: 10 },
        { label: 'AI 兜底', value: 11 },
        { label: '远程验证码生成', value: 20 },
        { label: '远程验证码校验', value: 21 },
        { label: '远程验证码二验', value: 22 },
      ],
      columns: [
        {
          title: '时间',
          dataIndex: 'createTime',
          key: 'createTime',
          slots: {
            customRender: 'createTime',
          },
          width: 180,
        },
        {
          title: '模块',
          dataIndex: 'module',
          key: 'module',
          slots: {
            customRender: 'module',
          },
          width: 120,
        },
        {
          title: '状态',
          dataIndex: 'status',
          key: 'status',
          slots: {
            customRender: 'status',
          },
          width: 120,
        },
        {
          title: '用户',
          dataIndex: 'username',
          key: 'username',
          slots: {
            customRender: 'username',
          },
          width: 160,
        },
        {
          title: '来源 IP',
          dataIndex: 'clientIp',
          key: 'clientIp',
          slots: {
            customRender: 'clientIp',
          },
          width: 160,
        },
        {
          title: '本地题库 ID',
          dataIndex: 'qid',
          key: 'qid',
          slots: {
            customRender: 'qid',
          },
          width: 140,
        },
        {
          title: '说明',
          dataIndex: 'summary',
          key: 'summary',
          slots: {
            customRender: 'summary',
          },
          width: 220,
        },
        {
          // 紧跟「说明」：搜题失败时的原因必须第一眼看到（详见 logListHelpers 的 buildHitProviderFields）。
          title: '命中渠道 / 错误原因',
          dataIndex: 'hitProvider',
          key: 'hitProvider',
          slots: {
            customRender: 'hitProvider',
          },
          width: 200,
        },
        {
          title: '计费渠道',
          dataIndex: 'billingProvider',
          key: 'billingProvider',
          slots: {
            customRender: 'billingProvider',
          },
          width: 140,
        },
        {
          // 详情列直接给「结果」（答案），不再放错误原因。
          title: '详情',
          dataIndex: 'detail',
          key: 'detail',
          slots: {
            customRender: 'detail',
          },
          width: 320,
        },
        {
          title: '请求 ID',
          dataIndex: 'requestId',
          key: 'requestId',
          slots: {
            customRender: 'requestId',
          },
          width: 220,
        },
        {
          title: '请求参数',
          dataIndex: 'mode',
          key: 'mode',
          slots: {
            customRender: 'mode',
          },
          width: 110,
        },
        {
          title: '请求接口',
          dataIndex: 'requestSource',
          key: 'requestSource',
          slots: {
            customRender: 'requestSource',
          },
          width: 180,
        },
        {
          title: '答案数',
          dataIndex: 'answerCount',
          key: 'answerCount',
          slots: {
            customRender: 'answerCount',
          },
          width: 100,
        },
        {
          title: '原始请求',
          dataIndex: 'requestSnapshot',
          key: 'requestSnapshot',
          slots: {
            customRender: 'requestSnapshot',
          },
          width: 260,
        },
        {
          title: '原始响应',
          dataIndex: 'responseSnapshot',
          key: 'responseSnapshot',
          slots: {
            customRender: 'responseSnapshot',
          },
          width: 260,
        },
      ],
    };
  },
  mounted() {
    this.handleLogList()
  },
  methods: {
    buildModuleLabel(value) {
      return buildLogModuleLabel(value)
    },
    buildStatusLabel(record) {
      return buildLogStatusLabel(record)
    },
    buildStatusColor(record) {
      return buildLogStatusColor(record)
    },
    buildStructuredFields(record) {
      return buildLogStructuredFields(record)
    },
    buildRowDetail(record) {
      return buildLogRowDetail(record)
    },
    buildSnapshotExpandKey(recordId, field) {
      return `${recordId || 'unknown'}:${field}`
    },
    shouldShowSnapshotExpand(value) {
      const text = String(value || '').trim()
      return text !== '' && text !== '-' && text.length > 48
    },
    isSnapshotExpanded(recordId, field) {
      return !!this.expandedSnapshots[this.buildSnapshotExpandKey(recordId, field)]
    },
    toggleSnapshotExpanded(recordId, field) {
      const key = this.buildSnapshotExpandKey(recordId, field)
      this.expandedSnapshots[key] = !this.expandedSnapshots[key]
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
      }, pagination.current, pagination.pageSize)).then(res => {
        if (res?.status == 200) {
          dataSource.value = res?.data?.data || []
          pagination.total = res?.data?.total || 0
          pagination.current = res?.data?.page || 1
          pagination.pageSize = res?.data?.pageSize || 10
          // 来源 IP 归属地单独批量拉取（失败静默）。
          enrichClientIps(dataSource.value)
        }
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
    formatDate(dateString) {
      if (!dateString) return '-'
      const date = new Date(dateString)
      if (isNaN(date.getTime())) return '-'

      const parts = new Intl.DateTimeFormat('zh-CN', {
        timeZone: 'Asia/Shanghai',
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        hour12: false,
      }).formatToParts(date).reduce((result, part) => {
        result[part.type] = part.value
        return result
      }, {})

      return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}:${parts.second}`
    }
  }
});

</script>

<style scoped>
.log-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}

.page-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}

.page-heading h1 {
  margin: 0;
  color: var(--console-text, #1f2937);
  font-size: 20px;
  font-weight: 650;
}

.page-lead {
  margin: 4px 0 0;
  color: var(--console-muted, #64748b);
  font-size: 13px;
}

.console-panel {
  min-width: 0;
  padding: 16px;
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: var(--console-radius, 12px);
  background: var(--console-surface, #fff);
}

.filter-form {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 16px;
}

.filter-form :deep(.ant-form-item) {
  margin: 0;
}

.filter-form :deep(.ant-input),
.filter-form :deep(.ant-select) {
  max-width: 100%;
}

.page-alert {
  margin-top: 4px;
}

.table-card {
  overflow: hidden;
}

.log-page :deep(.ant-table) {
  font-size: 13px;
}

.mobile-log-list,
.mobile-log-pagination {
  display: none;
}

@media (max-width: 640px) {
  .console-panel {
    padding: 12px;
  }

  .filter-form,
  .filter-form :deep(.ant-form-item) {
    width: 100%;
  }

  .desktop-log-table {
    display: none;
  }

  .mobile-log-list {
    display: grid;
    gap: 10px;
  }

  .mobile-log-card {
    display: grid;
    gap: 8px;
    padding: 12px;
    border: 1px solid var(--console-border, #e5e7eb);
    border-radius: 8px;
    background: var(--console-surface, #fff);
  }

  .mobile-log-card__header {
    display: flex;
    justify-content: space-between;
    gap: 8px;
  }

  .mobile-log-card strong,
  .mobile-log-card p,
  .mobile-log-card dd {
    overflow-wrap: anywhere;
  }

  .mobile-log-card dl {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
    margin: 0;
    padding: 8px 0;
    border-top: 1px solid var(--console-border, #e5e7eb);
    border-bottom: 1px solid var(--console-border, #e5e7eb);
  }

  .mobile-log-card dt {
    color: var(--console-muted, #64748b);
    font-size: 11px;
  }

  .mobile-log-card dd {
    margin: 2px 0 0;
    font-size: 12px;
  }

  .mobile-log-card p {
    margin: 0;
    color: var(--console-muted, #64748b);
    font-size: 12px;
    line-height: 1.6;
  }

  .mobile-log-pagination {
    display: flex;
    justify-content: flex-end;
    margin-top: 12px;
  }
}

.log-channel-cell {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 4px;
  min-width: 0;
  word-break: break-word;
}

.location-value {
  display: inline-block;
  white-space: pre-line;
  line-height: 1.5;
  text-align: left;
}

.log-channel-name {
  font-weight: 500;
}

.log-channel-reasons {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 3;
  overflow: hidden;
}

.log-channel-reasons--expanded {
  display: block;
  overflow: visible;
}

.log-channel-error {
  color: #a61d24;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.log-snapshot-cell {
  display: flex;
  align-items: flex-start;
  gap: 4px;
  width: 100%;
}

.log-snapshot-text {
  display: inline-block;
  flex: 1;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.log-snapshot-text--expanded {
  white-space: normal;
  overflow: visible;
  text-overflow: clip;
  word-break: break-all;
}

.log-snapshot-toggle {
  flex: none;
  padding-inline: 0;
  height: auto;
  line-height: 1.4;
}
</style>
