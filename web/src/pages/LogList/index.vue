<template>
  <div class="log-page">
    <a-card :bordered="false" class="filter-card">
      <a-form layout="inline" class="filter-form">
        <a-form-item label="模块">
          <a-select
            v-model:value="filters.module"
            allow-clear
            placeholder="全部模块"
            style="width: 180px"
            :options="moduleOptions"
            @change="onFilterChange"
          />
        </a-form-item>
        <a-form-item label="操作类型">
          <a-select
            v-model:value="filters.action"
            allow-clear
            placeholder="全部操作"
            style="width: 180px"
            :options="actionOptions"
            @change="onFilterChange"
          />
        </a-form-item>
        <a-form-item>
          <a-space>
            <a-button type="primary" @click="handleLogList">刷新</a-button>
            <a-button @click="resetFilters">重置筛选</a-button>
          </a-space>
        </a-form-item>
      </a-form>
      <a-alert
        type="info"
        show-icon
        class="page-alert"
        :message="isAdmin ? '当前显示全站审计日志，可按模块与操作类型筛选。搜题日志会拆分展示请求来源、请求 ID、命中渠道、计费渠道以及原始请求 / 响应快照。' : '当前仅显示你自己的操作日志，可按模块与操作类型筛选。搜题日志会拆分展示请求来源、请求 ID、命中渠道、计费渠道以及原始请求 / 响应快照。'"
      />
    </a-card>

    <a-card :bordered="false" class="table-card">
      <a-table
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
        <template #action="{ record }">
          <a-tag :color="buildActionColor(record.action)">
            {{ buildActionLabel(record.action, record.module) }}
          </a-tag>
        </template>
        <template #username="{ record }">
          <span>{{ record.username || `用户 #${record.userId || '-'}` }}</span>
        </template>
        <template #summary="{ record }">
          <span>{{ buildStructuredFields(record).summary }}</span>
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
          <span>{{ buildStructuredFields(record).hitProvider }}</span>
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
          <span>{{ record.clientIp || '-' }}</span>
        </template>
        <template #qid="{ record }">
          <span>{{ buildLogQidLabel(record.qid) }}</span>
        </template>
        <template #createTime="{ text, record }">
          {{ formatDate(text || record.createTime) }}
        </template>
      </a-table>
    </a-card>
  </div>
</template>

<script>
import { getLogList } from '@/api/TikuComponentApi';
import { defineComponent, ref, reactive } from 'vue';
import { getStoredCurrentUser, isAdminUser } from '@/utils/currentUser';
import {
  buildLogActionColor,
  buildLogActionLabel,
  buildLogModuleLabel,
  buildLogQidLabel,
  buildLogStructuredFields,
} from '@/utils/logListHelpers.mjs';
const dataSource = ref([])
const tabLoading = ref(true)
const currentUser = ref(getStoredCurrentUser())
const filters = reactive({
  module: undefined,
  action: undefined,
})
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
      tabLoading,
      pagination,
      filters,
      expandedSnapshots,
      isAdmin: isAdminUser(currentUser.value),
      moduleOptions: [
        { label: '认证', value: 'auth' },
        { label: '题目', value: 'question' },
        { label: '搜题', value: 'search' },
        { label: '系统', value: 'system' },
        { label: '验证码', value: 'captcha' },
      ],
      actionOptions: [
        { label: '新增', value: 0 },
        { label: '更新', value: 1 },
        { label: '删除', value: 2 },
        { label: '查询 / 搜题', value: 3 },
        { label: '登录', value: 10 },
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
          title: '操作',
          dataIndex: 'action',
          key: 'action',
          slots: {
            customRender: 'action',
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
          title: '请求 ID',
          dataIndex: 'requestId',
          key: 'requestId',
          slots: {
            customRender: 'requestId',
          },
          width: 220,
        },
        {
          title: '模式',
          dataIndex: 'mode',
          key: 'mode',
          slots: {
            customRender: 'mode',
          },
          width: 110,
        },
        {
          title: '请求来源',
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
          title: '命中渠道',
          dataIndex: 'hitProvider',
          key: 'hitProvider',
          slots: {
            customRender: 'hitProvider',
          },
          width: 140,
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
    buildActionLabel(value, module) {
      return buildLogActionLabel(value, module)
    },
    buildActionColor(value) {
      return buildLogActionColor(value)
    },
    buildStructuredFields(record) {
      return buildLogStructuredFields(record)
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
    handleLogList() {
      tabLoading.value = true
      getLogList({
        page: pagination.current,
        pageSize: pagination.pageSize,
        module: filters.module || undefined,
        action: filters.action === undefined ? undefined : filters.action,
      }).then(res => {
        if (res?.status == 200) {
          dataSource.value = res?.data?.data || []
          pagination.total = res?.data?.total || 0
          pagination.current = res?.data?.page || 1
          pagination.pageSize = res?.data?.pageSize || 10
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
    formatDate(dateString) {
      if (!dateString) return '-'
      const date = new Date(dateString)
      if (isNaN(date.getTime())) return '-'

      const year = date.getFullYear()
      const month = String(date.getMonth() + 1).padStart(2, '0')
      const day = String(date.getDate()).padStart(2, '0')
      const hours = String(date.getHours()).padStart(2, '0')
      const minutes = String(date.getMinutes()).padStart(2, '0')
      const seconds = String(date.getSeconds()).padStart(2, '0')

      return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}`
    }
  }
});

</script>

<style scoped>
.log-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.filter-card,
.table-card {
  border-radius: 20px;
}

.filter-form {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 0;
  margin-bottom: 12px;
}

.page-alert {
  margin-top: 4px;
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
