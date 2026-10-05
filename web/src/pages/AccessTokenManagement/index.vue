<template>
  <div class="token-page">
    <a-row :gutter="16" class="summary-row">
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="令牌总数" :value="summary.total" />
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="启用令牌" :value="summary.enabledCount" />
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="已禁用令牌" :value="summary.disabledCount" />
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="累计已消费" prefix="¥" :value="summary.totalAmountUsed" :precision="2" />
        </a-card>
      </a-col>
    </a-row>

    <a-card :bordered="false" class="page-card">
      <template #title>
        <div class="page-title-wrap">
          <div>
            <div class="page-title">令牌管理</div>
            <div class="page-subtitle">为当前账号创建多个访问令牌，并按令牌维度维护状态、备注与累计额度上限。</div>
          </div>
        </div>
      </template>
      <template #extra>
        <a-space>
          <a-button @click="handleLoad" :loading="tableLoading">刷新</a-button>
          <a-button type="primary" @click="openCreateModal">新增令牌</a-button>
        </a-space>
      </template>

      <a-alert
        type="info"
        show-icon
        class="page-alert"
        message="访问令牌用于 /adapter-service/search 的 Authorization: Basic xxxx 认证。令牌默认隐藏中间部分；如需核对完整值，可点击右侧按钮直接查看，也可直接复制。"
      />

      <a-alert
        v-if="balanceAlert"
        :type="balanceAlert.type"
        show-icon
        class="page-alert"
        :message="balanceAlert.message"
        :description="balanceAlert.description"
      />

      <a-table
        bordered
        :data-source="dataSource"
        :columns="columns"
        :loading="tableLoading"
        :pagination="false"
        row-key="id"
        :scroll="{ x: 'max-content' }"
      >
        <template #tokenValue="{ record }">
          <div class="token-value-cell">
            <a-typography-paragraph
              copyable
              class="token-value-text"
              :class="{ 'token-value-text--visible': isTokenVisible(record.id) }"
            >
              <code>{{ displayTokenValue(record) }}</code>
            </a-typography-paragraph>
            <a-button class="token-copy-button" shape="round" size="small" @click="copyTokenValue(record)">
              复制
            </a-button>
            <a-button class="token-toggle-button" shape="round" size="small" @click="toggleTokenVisibility(record.id)">
              {{ isTokenVisible(record.id) ? '隐藏中间部分' : '显示全部' }}
            </a-button>
          </div>
        </template>
        <template #status="{ record }">
          <a-badge :status="statusBadge(record.status)" :text="statusText(record.status)" />
        </template>
        <template #totalQuotaLimit="{ record }">
          <span>{{ quotaText(record.totalQuotaLimit) }}</span>
        </template>
        <template #totalAmountUsed="{ record }">
          <span>¥ {{ formatAmount(record.totalAmountUsed) }}</span>
        </template>
        <template #lastUsedAt="{ record }">
          <span>{{ formatDateTime(record.lastUsedAt) }}</span>
        </template>
        <template #lastUsedIp="{ record }">
          <span>{{ record.lastUsedIp || '-' }}</span>
        </template>
        <template #remark="{ record }">
          <span>{{ record.remark || '-' }}</span>
        </template>
        <template #operation="{ record }">
          <a-space size="small">
            <a-button type="link" style="padding: 0" @click="openEditModal(record)">编辑</a-button>
            <a-popconfirm
              ok-text="确定"
              cancel-text="取消"
              title="确定删除该令牌吗？"
              @confirm="handleDelete(record.id)"
            >
              <a-button type="link" danger style="padding: 0">删除</a-button>
            </a-popconfirm>
          </a-space>
        </template>
        <template #emptyText>
          <a-empty description="当前还没有访问令牌，可点击右上角新增令牌。" />
        </template>
      </a-table>
    </a-card>

    <a-modal
      v-model:visible="visible"
      :title="modalTitle"
      @ok="handleSubmit"
      :afterClose="resetFormState"
      ok-text="确定"
      cancel-text="取消"
      :confirm-loading="submitLoading"
    >
      <a-form :model="formState" :label-col="labelCol" :wrapper-col="wrapperCol" layout="horizontal">
        <a-form-item label="令牌名称" required>
          <a-input v-model:value="formState.name" placeholder="请输入令牌名称" />
        </a-form-item>
        <a-form-item v-if="isEditing" label="状态">
          <a-select v-model:value="formState.status" :options="statusOptions" />
        </a-form-item>
        <a-form-item label="累计额度上限">
          <a-input-number
            v-model:value="formState.totalQuotaLimit"
            :min="0"
            :precision="2"
            style="width: 100%"
            placeholder="0 表示不单独限制"
          />
          <div class="hint">设置为 0 或留空时，表示不单独限制该令牌，消费直接受用户余额约束。</div>
        </a-form-item>
        <a-form-item label="备注">
          <a-textarea v-model:value="formState.remark" :rows="4" placeholder="可选，例如调用来源、用途说明等" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script>
import { computed, defineComponent, ref } from 'vue';
import { message } from 'ant-design-vue';
import Swal from 'sweetalert2';
import {
  getAccessTokens,
  createAccessToken,
  updateAccessToken,
  deleteAccessToken,
} from '@/api/TikuComponentApi';
import { getStoredCurrentUser } from '@/utils/currentUser';
import { buildAccessTokenBalanceAlert } from '@/utils/accessTokenManagementHelpers.mjs';

const dataSource = ref([])
const tableLoading = ref(true)
const visible = ref(false)
const submitLoading = ref(false)
const isEditing = ref(false)
const visibleTokenIds = ref([])
const formState = ref({
  id: undefined,
  name: '',
  status: 'enabled',
  totalQuotaLimit: 0,
  remark: '',
})

function normalizeMoneyInput(value) {
  const amount = Number(value || 0)
  if (!Number.isFinite(amount) || amount < 0) {
    return 0
  }
  return amount
}

export default defineComponent({
  name: 'AccessTokenManagement',
  setup() {
    const summary = computed(() => {
      const rows = Array.isArray(dataSource.value) ? dataSource.value : []
      return rows.reduce((acc, item) => {
        const status = String(item?.status || '').toLowerCase()
        const amountUsed = Number(item?.totalAmountUsed || 0)
        acc.total += 1
        if (status === 'disabled') {
          acc.disabledCount += 1
        } else {
          acc.enabledCount += 1
        }
        if (Number.isFinite(amountUsed)) {
          acc.totalAmountUsed += amountUsed
        }
        return acc
      }, {
        total: 0,
        enabledCount: 0,
        disabledCount: 0,
        totalAmountUsed: 0,
      })
    })

    const modalTitle = computed(() => isEditing.value ? '编辑令牌' : '新增令牌')
    const currentUser = computed(() => getStoredCurrentUser())
    const balanceAlert = computed(() => buildAccessTokenBalanceAlert(currentUser.value))

    return {
      labelCol: { style: { width: '110px' } },
      wrapperCol: { span: 16 },
      dataSource,
      tableLoading,
      visible,
      submitLoading,
      isEditing,
      visibleTokenIds,
      formState,
      summary,
      modalTitle,
      balanceAlert,
      statusOptions: [
        { label: '启用', value: 'enabled' },
        { label: '禁用', value: 'disabled' },
      ],
      columns: [
        {
          title: '令牌名称',
          dataIndex: 'name',
          key: 'name',
          width: 180,
        },
        {
          title: '访问令牌',
          dataIndex: 'tokenValue',
          key: 'tokenValue',
          slots: {
            customRender: 'tokenValue',
          },
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
          title: '累计额度上限',
          dataIndex: 'totalQuotaLimit',
          key: 'totalQuotaLimit',
          slots: {
            customRender: 'totalQuotaLimit',
          },
          width: 150,
        },
        {
          title: '累计已消费',
          dataIndex: 'totalAmountUsed',
          key: 'totalAmountUsed',
          slots: {
            customRender: 'totalAmountUsed',
          },
          width: 140,
        },
        {
          title: '最近使用时间',
          dataIndex: 'lastUsedAt',
          key: 'lastUsedAt',
          slots: {
            customRender: 'lastUsedAt',
          },
          width: 180,
        },
        {
          title: '最近使用 IP',
          dataIndex: 'lastUsedIp',
          key: 'lastUsedIp',
          slots: {
            customRender: 'lastUsedIp',
          },
          width: 150,
        },
        {
          title: '备注',
          dataIndex: 'remark',
          key: 'remark',
          slots: {
            customRender: 'remark',
          },
          width: 200,
        },
        {
          title: '操作',
          dataIndex: 'operation',
          key: 'operation',
          fixed: 'right',
          slots: {
            customRender: 'operation',
          },
          width: 120,
        },
      ],
    }
  },
  mounted() {
    this.handleLoad()
  },
  methods: {
    formatAmount(value) {
      const amount = Number(value || 0)
      return Number.isFinite(amount) ? amount.toFixed(2) : '0.00'
    },
    statusText(value) {
      return String(value || '').toLowerCase() === 'disabled' ? '禁用' : '启用'
    },
    statusBadge(value) {
      return String(value || '').toLowerCase() === 'disabled' ? 'error' : 'success'
    },
    quotaText(value) {
      const amount = Number(value || 0)
      if (!Number.isFinite(amount) || amount <= 0) {
        return '未限制'
      }
      return `¥ ${amount.toFixed(2)}`
    },
    isTokenVisible(tokenId) {
      return visibleTokenIds.value.includes(tokenId)
    },
    toggleTokenVisibility(tokenId) {
      if (this.isTokenVisible(tokenId)) {
        visibleTokenIds.value = visibleTokenIds.value.filter(id => id !== tokenId)
        return
      }
      visibleTokenIds.value = [...visibleTokenIds.value, tokenId]
    },
    displayTokenValue(record) {
      const rawValue = String(record?.tokenValue || '').trim()
      if (!rawValue) {
        return '-'
      }
      if (this.isTokenVisible(record?.id)) {
        return rawValue
      }
      if (rawValue.length <= 14) {
        return rawValue
      }
      return `${rawValue.slice(0, 8)}******${rawValue.slice(-6)}`
    },
    copyTokenValue(record) {
      const rawValue = String(record?.tokenValue || '').trim()
      if (!rawValue) {
        message.warning('当前令牌为空，无法复制')
        return
      }
      if (navigator?.clipboard?.writeText) {
        navigator.clipboard.writeText(rawValue).then(() => {
          message.success('访问令牌已复制')
        }).catch(() => {
          message.error('复制访问令牌失败')
        })
        return
      }
      message.warning('当前浏览器不支持直接复制，请手动复制')
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
    handleLoad() {
      tableLoading.value = true
      getAccessTokens().then(res => {
        if (res?.status === 200) {
          dataSource.value = Array.isArray(res?.data) ? res.data : []
        } else {
          message.error(res?.message || '获取访问令牌失败')
        }
      }).catch(() => {
        message.error('获取访问令牌失败')
      }).finally(() => {
        tableLoading.value = false
      })
    },
    openCreateModal() {
      this.resetFormState()
      isEditing.value = false
      visible.value = true
    },
    openEditModal(record) {
      isEditing.value = true
      formState.value = {
        id: record.id,
        name: record.name || '',
        status: record.status || 'enabled',
        totalQuotaLimit: Number(record.totalQuotaLimit || 0),
        remark: record.remark || '',
      }
      visible.value = true
    },
    handleSubmit() {
      const payload = {
        id: formState.value.id,
        name: String(formState.value.name || '').trim(),
        status: String(formState.value.status || 'enabled').trim(),
        totalQuotaLimit: normalizeMoneyInput(formState.value.totalQuotaLimit),
        remark: String(formState.value.remark || '').trim(),
      }

      if (!payload.name) {
        message.warning('请输入令牌名称')
        return
      }

      submitLoading.value = true
      const submitter = isEditing.value
        ? updateAccessToken(payload)
        : createAccessToken({
          name: payload.name,
          totalQuotaLimit: payload.totalQuotaLimit,
          remark: payload.remark,
        })

      submitter.then(res => {
        if (res?.status === 200) {
          if (isEditing.value) {
            message.success('更新令牌成功')
          } else {
            message.success('创建令牌成功')
            const plainToken = String(res?.data?.token || '').trim()
            if (plainToken) {
              Swal.fire({
                icon: 'info',
                title: '请立即保存访问令牌',
                text: plainToken,
                confirmButtonText: '我已保存',
                heightAuto: false,
                scrollbarPadding: false,
              })
            }
          }
          this.handleLoad()
          visible.value = false
        } else {
          message.error(res?.message || (isEditing.value ? '更新令牌失败' : '创建令牌失败'))
        }
      }).catch(() => {
        message.error(isEditing.value ? '更新令牌失败' : '创建令牌失败')
      }).finally(() => {
        submitLoading.value = false
      })
    },
    handleDelete(id) {
      deleteAccessToken({ id }).then(res => {
        if (res?.status === 200) {
          message.success('删除令牌成功')
          this.handleLoad()
        } else {
          message.error(res?.message || '删除令牌失败')
        }
      }).catch(() => {
        message.error('删除令牌失败')
      })
    },
    resetFormState() {
      formState.value = {
        id: undefined,
        name: '',
        status: 'enabled',
        totalQuotaLimit: 0,
        remark: '',
      }
      isEditing.value = false
    },
  },
})
</script>

<style scoped>
.token-page {
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

.hint {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.6;
  color: #6b7280;
}

.token-value-cell {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: nowrap;
}

.token-value-text {
  margin-bottom: 0;
  word-break: break-all;
  flex: 1 1 auto;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.token-value-text--visible {
  white-space: normal;
  overflow: visible;
  text-overflow: clip;
}

.token-copy-button,
.token-toggle-button {
  flex: 0 0 auto;
  border-radius: 999px;
}
</style>
