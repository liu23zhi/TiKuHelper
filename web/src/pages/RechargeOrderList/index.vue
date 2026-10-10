<template>
  <div class="recharge-order-page">
    <a-card :bordered="false" class="page-card">
      <template #title>
        <div class="page-title-wrap">
          <div>
            <div class="page-title">充值记录</div>
            <div class="page-subtitle">
              {{ isAdmin ? '管理员可查看全部充值记录。' : '普通用户只能查看自己的充值记录。' }}
            </div>
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
        :message="isAdmin ? '管理员可查看全部充值记录，包含用户、订单号、支付方式、金额和支付状态。' : '普通用户只能查看自己的充值记录，支付结果以异步通知入账为准。'"
      />

      <a-table
        bordered
        :data-source="dataSource"
        :columns="columns"
        :loading="tableLoading"
        :pagination="pagination"
        row-key="id"
        :scroll="{ x: 1180 }"
        @change="handleTableChange"
      >
        <template #username="{ record }">
          <span>{{ record.username || `用户 #${record.userId || '-'}` }}</span>
        </template>
        <template #amount="{ record }">
          <span>¥ {{ formatAmount(record.amount) }}</span>
        </template>
        <template #status="{ record }">
          <a-tag :color="buildStatusColor(record.status)">
            {{ buildStatusLabel(record.status) }}
          </a-tag>
        </template>
        <template #paidAt="{ record }">
          <span>{{ formatDateTime(record.paidAt) }}</span>
        </template>
        <template #createdAt="{ record }">
          <span>{{ formatDateTime(record.createdAt) }}</span>
        </template>
        <template #emptyText>
          <a-empty description="当前还没有充值记录。" />
        </template>
      </a-table>
    </a-card>
  </div>
</template>

<script>
import { computed, defineComponent, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { getRechargeOrders } from '@/api/TikuComponentApi'
import { getStoredCurrentUser, isAdminUser } from '@/utils/currentUser'

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
  name: 'RechargeOrderList',
  setup() {
    const currentUser = computed(() => getStoredCurrentUser())
    const isAdmin = computed(() => isAdminUser(currentUser.value))

    return {
      dataSource,
      tableLoading,
      pagination,
      isAdmin,
      columns: [
        {
          title: '订单号',
          dataIndex: 'orderNo',
          key: 'orderNo',
          width: 240,
        },
        {
          title: '用户',
          dataIndex: 'username',
          key: 'username',
          slots: {
            customRender: 'username',
          },
          width: 150,
        },
        {
          title: '金额',
          dataIndex: 'amount',
          key: 'amount',
          slots: {
            customRender: 'amount',
          },
          width: 120,
        },
        {
          title: '支付方式',
          dataIndex: 'paymentMethodNameSnapshot',
          key: 'paymentMethodNameSnapshot',
          width: 150,
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
          title: '第三方单号',
          dataIndex: 'thirdPartyOrderNo',
          key: 'thirdPartyOrderNo',
          width: 220,
        },
        {
          title: '支付时间',
          dataIndex: 'paidAt',
          key: 'paidAt',
          slots: {
            customRender: 'paidAt',
          },
          width: 180,
        },
        {
          title: '创建时间',
          dataIndex: 'createdAt',
          key: 'createdAt',
          slots: {
            customRender: 'createdAt',
          },
          width: 180,
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
      getRechargeOrders({
        page: pagination.current,
        pageSize: pagination.pageSize,
      }).then((res) => {
        if (res?.status === 200) {
          dataSource.value = Array.isArray(res?.data?.data) ? res.data.data : []
          pagination.total = Number(res?.data?.total || 0)
          pagination.current = Number(res?.data?.page || 1)
          pagination.pageSize = Number(res?.data?.pageSize || 10)
        } else {
          message.error(res?.message || '获取充值记录失败')
        }
      }).catch(() => {
        message.error('获取充值记录失败')
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
      // 充值金额固定 2 位小数（支付渠道只支持「分」），与余额的 6 位精度区分开。
      const amount = Number(value)
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
    buildStatusLabel(value) {
      const normalized = String(value || '').trim().toLowerCase()
      if (normalized === 'paid') return '已支付'
      if (normalized === 'pending') return '待支付'
      if (normalized === 'closed') return '已关闭'
      return value || '-'
    },
    buildStatusColor(value) {
      const normalized = String(value || '').trim().toLowerCase()
      if (normalized === 'paid') return 'green'
      if (normalized === 'pending') return 'gold'
      if (normalized === 'closed') return 'default'
      return 'blue'
    },
  },
})
</script>

<style scoped>
.recharge-order-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.page-card {
  border-radius: 20px;
}

.page-title-wrap {
  display: flex;
  align-items: center;
}

.page-title {
  color: #1f2937;
  font-size: 22px;
  font-weight: 700;
}

.page-subtitle {
  margin-top: 6px;
  color: #6b7280;
  font-size: 13px;
}

.page-alert {
  margin-bottom: 16px;
}
</style>
