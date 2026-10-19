<template>
  <div class="console-page token-page">
    <header class="section-heading page-heading">
      <div>
        <h1>令牌管理</h1>
        <p class="page-lead">为当前账号创建多个访问令牌，并按令牌维度维护状态、备注与累计额度上限。</p>
      </div>
      <a-space wrap class="section-actions">
        <a-button
          v-if="isAdmin"
          :type="showAllTokens ? 'primary' : 'default'"
          @click="toggleAllTokens"
        >
          {{ showAllTokens ? '只看我的令牌' : '显示全部令牌' }}
        </a-button>
        <a-button @click="handleLoad" :loading="tableLoading">刷新</a-button>
        <a-button type="primary" @click="openCreateModal">新增令牌</a-button>
      </a-space>
    </header>
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
          <a-statistic title="累计已消费" prefix="¥" :value="summary.totalAmountUsed" :precision="6" />
        </a-card>
      </a-col>
    </a-row>

    <a-card :bordered="false" class="page-card">
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
        <template #username="{ record }">
          <span>{{ record.username || '-' }}</span>
        </template>
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
          <a-input v-model:value="formState.name" placeholder="请输入令牌名称" @input="formState.name = limitAccessTokenNameInput(formState.name)" />
        </a-form-item>
        <a-form-item v-if="isEditing" label="状态">
          <a-select v-model:value="formState.status" :options="statusOptions" />
        </a-form-item>
        <a-form-item label="累计额度上限">
          <a-input-number
            v-model:value="formState.totalQuotaLimit"
            :min="0"
            :precision="6"
            style="width: 100%"
            placeholder="0 表示不单独限制"
          />
          <div class="hint">设置为 0 或留空时，表示不单独限制该令牌，消费直接受用户余额约束。金额最多支持 6 位小数。</div>
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
import { getStoredCurrentUser, isAdminUser } from '@/utils/currentUser';
import {
  buildAccessTokenBalanceAlert,
  buildAccessTokenListParams,
  normalizeAccessTokenName,
  limitAccessTokenNameInput,
} from '@/utils/accessTokenManagementHelpers.mjs';
import { formatMoneyValue, formatMoneyYuanWithSpace } from '@/utils/moneyFormat.mjs';

const dataSource = ref([])
const tableLoading = ref(true)
const visible = ref(false)
const submitLoading = ref(false)
const isEditing = ref(false)
const visibleTokenIds = ref([])
const showAllTokens = ref(false)
const formState = ref({
  id: undefined,
  name: '',
  originalName: '',
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
    const isAdmin = computed(() => isAdminUser(currentUser.value))
    const balanceAlert = computed(() => buildAccessTokenBalanceAlert(currentUser.value))
    const baseColumns = [
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
    ]
    const columns = computed(() => {
      if (!showAllTokens.value) {
        return baseColumns
      }
      return [
        {
          title: '用户名',
          dataIndex: 'username',
          key: 'username',
          slots: {
            customRender: 'username',
          },
          width: 160,
        },
        ...baseColumns,
      ]
    })

    return {
      labelCol: { style: { width: '110px' } },
      wrapperCol: { span: 16 },
      dataSource,
      tableLoading,
      visible,
      submitLoading,
      isEditing,
      visibleTokenIds,
      showAllTokens,
      formState,
      summary,
      modalTitle,
      isAdmin,
      balanceAlert,
      statusOptions: [
        { label: '启用', value: 'enabled' },
        { label: '禁用', value: 'disabled' },
      ],
      columns,
      limitAccessTokenNameInput,
    }
  },
  mounted() {
    this.handleLoad()
  },
  methods: {
    formatAmount(value) {
      return formatMoneyValue(value || 0)
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
      return formatMoneyYuanWithSpace(amount)
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
      this.copyTokenText(rawValue).then((copied) => {
        if (copied) {
          message.success('访问令牌已复制')
          return
        }
        message.error('复制访问令牌失败，请手动复制')
      })
    },
    async copyTokenText(rawValue) {
      const value = String(rawValue || '').trim()
      if (!value) {
        return false
      }
      if (navigator?.clipboard?.writeText) {
        try {
          await navigator.clipboard.writeText(value)
          return true
        } catch (error) {
          // Fall through to the legacy copy path for non-secure or restricted contexts.
        }
      }

      const textarea = document.createElement('textarea')
      textarea.value = value
      textarea.setAttribute('readonly', '')
      textarea.style.position = 'fixed'
      textarea.style.top = '-9999px'
      textarea.style.opacity = '0'
      document.body.appendChild(textarea)
      textarea.select()
      textarea.setSelectionRange(0, value.length)

      try {
        return document.execCommand('copy')
      } catch (error) {
        return false
      } finally {
        document.body.removeChild(textarea)
      }
    },
    showCreatedTokenDialog(plainToken) {
      return Swal.fire({
        icon: 'success',
        title: '令牌创建成功',
        html: `
          <div class="token-created-dialog">
            <div class="token-created-dialog__notice">
              <span class="token-created-dialog__notice-mark">!</span>
              <span>请妥善保存访问令牌，避免泄露。</span>
            </div>
            <div class="token-created-dialog__field">
              <div class="token-created-dialog__label">访问令牌</div>
              <code class="token-created-dialog__value" data-token-value></code>
            </div>
          </div>
        `,
        showDenyButton: true,
        confirmButtonText: '我已保存',
        denyButtonText: '复制令牌',
        reverseButtons: true,
        buttonsStyling: false,
        allowOutsideClick: false,
        focusConfirm: false,
        heightAuto: false,
        scrollbarPadding: false,
        customClass: {
          popup: 'token-created-popup',
          title: 'token-created-popup__title',
          htmlContainer: 'token-created-popup__content',
          actions: 'token-created-popup__actions',
          confirmButton: 'token-created-popup__confirm',
          denyButton: 'token-created-popup__copy',
        },
        didOpen: () => {
          const tokenElement = Swal.getHtmlContainer()?.querySelector('[data-token-value]')
          if (tokenElement) {
            tokenElement.textContent = plainToken
          }
        },
        preDeny: async () => {
          const copied = await this.copyTokenText(plainToken)
          const denyButton = Swal.getDenyButton()
          if (copied) {
            if (denyButton) {
              denyButton.textContent = '已复制'
            }
            Swal.showValidationMessage('已复制到剪贴板，请妥善保存')
          } else {
            if (denyButton) {
              denyButton.textContent = '重新复制'
            }
            Swal.showValidationMessage('复制失败，请选中令牌后手动复制')
          }
          return false
        },
      })
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
      getAccessTokens(buildAccessTokenListParams(showAllTokens.value)).then(res => {
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
    toggleAllTokens() {
      if (!this.isAdmin) {
        return
      }
      showAllTokens.value = !showAllTokens.value
      this.handleLoad()
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
        originalName: record.name || '',
        status: record.status || 'enabled',
        totalQuotaLimit: Number(record.totalQuotaLimit || 0),
        remark: record.remark || '',
      }
      visible.value = true
    },
    handleSubmit() {
      let name
      const rawName = String(formState.value.name || '').trim()
      const unchangedLegacyName = isEditing.value && rawName === String(formState.value.originalName || '').trim()
      if (unchangedLegacyName) {
        name = rawName
      } else {
        try {
          name = normalizeAccessTokenName(rawName)
        } catch (error) {
          message.warning(error?.message || '令牌名称不符合要求')
          return
        }
      }
      const payload = {
        id: formState.value.id,
        name,
        status: String(formState.value.status || 'enabled').trim(),
        totalQuotaLimit: normalizeMoneyInput(formState.value.totalQuotaLimit),
        remark: String(formState.value.remark || '').trim(),
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
              this.showCreatedTokenDialog(plainToken)
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
        originalName: '',
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
  max-width: 680px;
  margin: 4px 0 0;
  color: var(--console-muted, #64748b);
  font-size: 13px;
}

.summary-row {
  margin-bottom: 0;
}

.summary-card {
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: 8px;
}

.page-card {
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: var(--console-radius, 12px);
}

.page-alert {
  margin-bottom: 16px;
}

.token-page :deep(.ant-table) {
  font-size: 13px;
}

.filter-form {
  padding: 16px;
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: 8px;
  background: var(--console-canvas, #f5f7fb);
}

@media (max-width: 640px) {
  .filter-form,
  .filter-form :deep(.ant-form-item) {
    width: 100%;
  }

  .filter-form :deep(.ant-input),
  .filter-form :deep(.ant-input-search),
  .filter-form :deep(.ant-select) {
    width: 100%;
  }
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
}
</style>

<style>
.token-created-popup {
  width: min(34rem, calc(100vw - 2rem));
  padding: 28px 28px 24px;
  border-radius: 12px;
  box-shadow: 0 24px 70px rgba(15, 23, 42, 0.2);
}

.token-created-popup__title {
  color: #172033;
  font-size: 23px;
  font-weight: 700;
  letter-spacing: -0.02em;
}

.token-created-popup__content {
  margin-top: 16px;
}

.token-created-dialog {
  color: #334155;
  text-align: left;
}

.token-created-dialog__notice {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin-bottom: 16px;
  padding: 12px 14px;
  border: 1px solid #fde68a;
  border-radius: 8px;
  background: #fffbeb;
  color: #92400e;
  font-size: 13px;
  line-height: 1.6;
}

.token-created-dialog__notice-mark {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  margin-top: 1px;
  border-radius: 50%;
  background: #f59e0b;
  color: #ffffff;
  font-size: 12px;
  font-weight: 800;
}

.token-created-dialog__field {
  padding: 14px;
  border: 1px solid #dbe4ee;
  border-radius: 8px;
  background: #f8fafc;
}

.token-created-dialog__label {
  margin-bottom: 8px;
  color: #64748b;
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.token-created-dialog__value {
  display: block;
  padding: 14px 16px;
  border-radius: 12px;
  background: #0f172a;
  color: #e2e8f0;
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
  font-size: 14px;
  line-height: 1.7;
  text-align: left;
  word-break: break-all;
  user-select: all;
}

.token-created-popup__actions {
  width: 100%;
  gap: 10px;
  margin-top: 20px;
}

.token-created-popup__copy,
.token-created-popup__confirm {
  min-width: 126px;
  height: 42px;
  padding: 0 20px;
  border-radius: 8px;
  font-size: 14px;
  font-weight: 600;
  transition: border-color 0.2s ease, background 0.2s ease, box-shadow 0.2s ease, transform 0.2s ease;
}

.token-created-popup__copy {
  border: 1px solid #1677ff;
  background: #1677ff;
  color: #ffffff;
  box-shadow: 0 8px 20px rgba(22, 119, 255, 0.24);
}

.token-created-popup__copy:hover {
  border-color: #0958d9;
  background: #0958d9;
  box-shadow: 0 10px 24px rgba(22, 119, 255, 0.3);
  transform: translateY(-1px);
}

.token-created-popup__confirm {
  border: 1px solid #cbd5e1;
  background: #ffffff;
  color: #475569;
}

.token-created-popup__confirm:hover {
  border-color: #94a3b8;
  background: #f8fafc;
  color: #1e293b;
}

.token-created-popup__copy:focus-visible,
.token-created-popup__confirm:focus-visible {
  outline: 3px solid rgba(22, 119, 255, 0.2);
  outline-offset: 2px;
}

@media (max-width: 575px) {
  .token-created-popup {
    width: calc(100vw - 24px);
    padding: 24px 18px 18px;
    border-radius: 12px;
  }

  .token-created-popup__actions {
    flex-wrap: nowrap;
  }

  .token-created-popup__copy,
  .token-created-popup__confirm {
    flex: 1 1 0;
    min-width: 0;
    padding: 0 12px;
  }
}
</style>
