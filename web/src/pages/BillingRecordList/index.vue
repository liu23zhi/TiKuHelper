<template>
  <div class="billing-page">
    <a-row :gutter="16" class="summary-row">
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="账单总数" :value="summary.total" />
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="当前页条数" :value="summary.currentPageCount" />
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="累计消费金额" prefix="¥" :value="summary.totalAmount" :precision="2" />
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="答案来源数" :value="summary.providerCount" />
        </a-card>
      </a-col>
    </a-row>

    <a-card :bordered="false" class="page-card">
      <template #title>
        <div class="page-title-wrap">
          <div>
            <div class="page-title">{{ pageTitle }}</div>
            <div class="page-subtitle">{{ pageSubtitle }}</div>
          </div>
        </div>
      </template>
      <template #extra>
        <a-button @click="handleLoad" :loading="tableLoading">刷新</a-button>
      </template>

      <a-alert
        type="info"
        show-icon
        class="page-alert"
        :message="alertMessage"
      />

      <a-table
        bordered
        :data-source="dataSource"
        :columns="columns"
        :loading="tableLoading"
        :pagination="pagination"
        @change="handleTableChange"
        row-key="id"
        :scroll="{ x: 980 }"
      >
        <template #questionText="{ record }">
          <span class="multiline-cell">{{ buildPersonalUsageQuestionText(record) }}</span>
        </template>
        <template #answerSourceLabel="{ record }">
          <span class="multiline-cell">{{ buildPersonalUsageAnswerSourceText(record) }}</span>
        </template>
        <template #amount="{ record }">
          <span>¥ {{ formatAmount(record.amount) }}</span>
        </template>
        <template #accessTokenName="{ record }">
          <span>{{ record.accessTokenName || '-' }}</span>
        </template>
        <template #createdAt="{ record }">
          <span>{{ formatDateTime(record.createdAt) }}</span>
        </template>
        <template #emptyText>
          <a-empty description="当前还没有账单记录。" />
        </template>
      </a-table>
    </a-card>
  </div>
</template>

<script>
import { computed, defineComponent, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { getBillingRecords } from '@/api/TikuComponentApi'
import { getStoredCurrentUser, isAdminUser } from '@/utils/currentUser'
import {
  buildPersonalUsageAnswerSourceText,
  buildPersonalUsageQuestionText,
} from '@/utils/userPortalHelpers.mjs'

const dataSource = ref([])
const tableLoading = ref(false)
const pagination = reactive({
  current: 1,
  pageSize: 10,
  total: 0,
  showSizeChanger: true,
  showQuickJumper: true,
  showTotal: (total) => `共 ${total} 条`,
  pageSizeOptions: ['10', '20', '50', '100'],
})

export default defineComponent({
  name: 'BillingRecordList',
  setup() {
    const currentUser = computed(() => getStoredCurrentUser())
    const isAdmin = computed(() => isAdminUser(currentUser.value))

    const summary = computed(() => {
      const rows = Array.isArray(dataSource.value) ? dataSource.value : []
      const answerSources = new Set()
      return rows.reduce((acc, item) => {
        const amount = Number(item?.amount || 0)
        const answerSourceKey = String(item?.answerSourceLabel || item?.providerLabel || item?.provider || '').trim()
        acc.currentPageCount += 1
        if (answerSourceKey) {
          answerSources.add(answerSourceKey)
        }
        if (Number.isFinite(amount)) {
          acc.totalAmount += amount
        }
        acc.providerCount = answerSources.size
        return acc
      }, {
        total: Number(pagination.total || 0),
        currentPageCount: 0,
        totalAmount: 0,
        providerCount: 0,
      })
    })

    const pageTitle = computed(() => (isAdmin.value ? '账单明细' : '我的账单'))
    const pageSubtitle = computed(() => (
      isAdmin.value
        ? '查看平台内全部账单记录，聚焦题目、消费金额、答案来源、访问令牌与请求时间。'
        : '查看当前账号名下的账单记录，聚焦题目、消费金额、答案来源、访问令牌与请求时间。'
    ))
    const alertMessage = computed(() => (
      isAdmin.value
        ? '管理员可查看全部账单。'
        : '这里只展示当前账号自己的账单记录，便于快速查看每次消费对应的题目、答案来源、令牌与时间。'
    ))

    return {
      dataSource,
      tableLoading,
      pagination,
      summary,
      pageTitle,
      pageSubtitle,
      alertMessage,
      buildPersonalUsageAnswerSourceText,
      buildPersonalUsageQuestionText,
      columns: [
        {
          title: '账单 ID',
          dataIndex: 'id',
          key: 'id',
          width: 90,
        },
        {
          title: '题目',
          dataIndex: 'questionText',
          key: 'questionText',
          width: 320,
          slots: {
            customRender: 'questionText',
          },
        },
        {
          title: '消费金额',
          dataIndex: 'amount',
          key: 'amount',
          width: 120,
          slots: {
            customRender: 'amount',
          },
        },
        {
          title: '答案来源',
          dataIndex: 'answerSourceLabel',
          key: 'answerSourceLabel',
          width: 200,
          slots: {
            customRender: 'answerSourceLabel',
          },
        },
        {
          title: '访问令牌名称',
          dataIndex: 'accessTokenName',
          key: 'accessTokenName',
          width: 180,
          slots: {
            customRender: 'accessTokenName',
          },
        },
        {
          title: '请求时间',
          dataIndex: 'createdAt',
          key: 'createdAt',
          width: 180,
          slots: {
            customRender: 'createdAt',
          },
        },
      ],
    }
  },
  mounted() {
    this.handleLoad()
  },
  methods: {
    handleLoad() {
      tableLoading.value = true
      getBillingRecords({
        page: pagination.current,
        pageSize: pagination.pageSize,
      }).then((res) => {
        if (res?.status === 200) {
          dataSource.value = Array.isArray(res?.data?.data) ? res.data.data : []
          pagination.total = Number(res?.data?.total || 0)
          pagination.current = Number(res?.data?.page || 1)
          pagination.pageSize = Number(res?.data?.pageSize || 10)
        } else {
          message.error(res?.message || '获取账单列表失败')
        }
      }).catch(() => {
        message.error('获取账单列表失败')
      }).finally(() => {
        tableLoading.value = false
      })
    },
    handleTableChange(paginationData) {
      pagination.current = paginationData.current
      pagination.pageSize = paginationData.pageSize
      this.handleLoad()
    },
    formatAmount(value) {
      const amount = Number(value || 0)
      return Number.isFinite(amount) ? amount.toFixed(2) : '0.00'
    },
    formatDateTime(value) {
      if (!value) {
        return '-'
      }
      const date = new Date(value)
      if (Number.isNaN(date.getTime())) {
        return value
      }
      return date.toLocaleString()
    },
  },
})
</script>

<style scoped>
.billing-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.summary-row {
  margin-bottom: 0;
}

.summary-card {
  border-radius: 18px;
}

.page-card {
  border-radius: 20px;
}

.page-title-wrap {
  display: flex;
  align-items: center;
}

.page-title {
  font-size: 22px;
  font-weight: 700;
  color: #1f2937;
}

.page-subtitle {
  margin-top: 6px;
  color: #6b7280;
  font-size: 13px;
}

.page-alert {
  margin-bottom: 16px;
}

.multiline-cell {
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.6;
}
</style>
