<template>
  <div class="user-page">
    <a-row :gutter="16" class="summary-row">
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="用户总数" :value="summary.total" />
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="管理员" :value="summary.adminCount" />
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="启用账号" :value="summary.enabledCount" />
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="累计余额" prefix="¥" :value="summary.totalBalance" :precision="2" />
        </a-card>
      </a-col>
    </a-row>

    <a-card :bordered="false" class="page-card">
      <template #title>
        <div class="page-title-wrap">
          <div>
            <div class="page-title">用户管理</div>
            <div class="page-subtitle">查看平台全部用户，并创建新的普通用户或管理员用户。</div>
          </div>
        </div>
      </template>
      <template #extra>
        <a-space>
          <a-button @click="handleUserList" :loading="tabLoading">刷新</a-button>
          <a-button type="primary" @click="openCreateModal">新增用户</a-button>
        </a-space>
      </template>

      <a-alert
        type="info"
        show-icon
        class="page-alert"
        message="当前页面展示的是平台全部用户。管理员可以创建用户，并直接编辑角色、状态、昵称与密码。"
      />

      <a-table
        bordered
        :data-source="dataSource"
        :columns="columns"
        :loading="tabLoading"
        :pagination="false"
        row-key="id"
        :scroll="{ x: 1480 }"
      >
        <template #nickname="{ record }">
          <span>{{ record.nickname || '-' }}</span>
        </template>
        <template #role="{ record }">
          <a-tag :color="roleColor(record.role)">{{ roleText(record.role) }}</a-tag>
        </template>
        <template #status="{ record }">
          <a-badge :status="statusBadge(record.status)" :text="statusText(record.status)" />
        </template>
        <template #balance="{ record }">
          <span>¥ {{ formatBalance(record.balance) }}</span>
        </template>
        <template #registerSource="{ record }">
          <a-tag>{{ registerSourceText(record.registerSource) }}</a-tag>
        </template>
        <template #createdAt="{ record }">
          <span>{{ formatDateTime(record.createdAt) }}</span>
        </template>
        <template #lastLoginAt="{ record }">
          <span>{{ formatDateTime(record.lastLoginAt) }}</span>
        </template>
        <template #lastLoginIp="{ record }">
          <span>{{ record.lastLoginIp || '-' }}</span>
        </template>
        <template #operation="{ record }">
          <a-space size="small" wrap>
            <a-button type="link" style="padding: 0" @click="openEditModal(record)">编辑</a-button>
            <a-button type="link" style="padding: 0" @click="openResetPasswordModal(record)">重置密码</a-button>
            <a-popconfirm
              v-if="dataSource.length"
              ok-text="确定"
              cancel-text="取消"
              title="确定删除该用户吗？"
              @confirm="onDelete(record.id)"
            >
              <a-button type="link" danger style="padding: 0">删除</a-button>
            </a-popconfirm>
          </a-space>
        </template>
        <template #emptyText>
          <a-empty description="当前还没有用户记录，点击右上角新增用户即可开始。" />
        </template>
      </a-table>
    </a-card>

    <a-modal
      v-model:visible="visible"
      :title="modalTitle"
      @ok="onSubmit"
      :afterClose="resetFormState"
      ok-text="确定"
      cancel-text="取消"
      :confirm-loading="submitLoading"
    >
      <a-form :model="formState" :label-col="labelCol" :wrapper-col="wrapperCol" layout="horizontal">
        <a-form-item label="用户名" required>
          <a-input v-model:value="formState.username" :disabled="isEditing" placeholder="请输入用户名" />
        </a-form-item>
        <a-form-item v-if="!isEditing" label="密码" required>
          <a-input-password v-model:value="formState.password" placeholder="请输入密码" />
        </a-form-item>
        <a-form-item label="昵称">
          <a-input v-model:value="formState.nickname" placeholder="可选" />
        </a-form-item>
        <a-form-item label="角色">
          <a-select v-model:value="formState.role" :options="roleOptions" />
        </a-form-item>
        <a-form-item label="状态">
          <a-select v-model:value="formState.status" :options="statusOptions" />
        </a-form-item>
        <a-form-item v-if="isEditing" label="余额模式">
          <a-select v-model:value="formState.balanceMode" :options="balanceModeOptions" />
        </a-form-item>
        <a-form-item v-if="isEditing" label="余额数值">
          <a-input v-model:value="formState.balance" placeholder="留空表示不调整余额" />
          <div class="form-hint">可选择增加、减少或覆盖；留空则仅更新基础信息。</div>
        </a-form-item>
        <a-form-item label="创建来源">
          <a-input :value="registerSourceText(formState.registerSource)" disabled />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:visible="resetPasswordVisible"
      title="重置用户密码"
      @ok="submitResetPassword"
      :afterClose="resetResetPasswordForm"
      ok-text="确定"
      cancel-text="取消"
      :confirm-loading="resetPasswordLoading"
    >
      <a-form :model="resetPasswordForm" :label-col="labelCol" :wrapper-col="wrapperCol" layout="horizontal">
        <a-form-item label="目标用户">
          <a-input :value="resetPasswordForm.username || '-'" disabled />
        </a-form-item>
        <a-form-item label="新密码" required>
          <a-input-password v-model:value="resetPasswordForm.password" placeholder="请输入新密码" />
          <div class="form-hint">密码至少需要 8 位，且需包含大小写字母、数字和特殊字符。</div>
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script>
import { getUserList, delUser, addUser, updateUser, resetUserPassword } from '@/api/TikuComponentApi';
import { buildRegisterSourceLabel, buildResetPasswordPayload, buildUserBalanceUpdatePayload } from '@/utils/userPortalHelpers.mjs';
import { message } from 'ant-design-vue';
import { computed, defineComponent, ref, toRaw } from 'vue';

const dataSource = ref([])
const visible = ref(false)
const submitLoading = ref(false)
const isEditing = ref(false)
const resetPasswordVisible = ref(false)
const resetPasswordLoading = ref(false)
const formState = ref({
  id: undefined,
  username: '',
  password: '',
  nickname: '',
  perms: '',
  role: 'user',
  status: 'enabled',
  balance: '',
  balanceMode: 'override',
  registerSource: 'admin',
})
const resetPasswordForm = ref({
  id: undefined,
  username: '',
  password: '',
})
const tabLoading = ref(true)

export default defineComponent({
  name: 'UserList',
  setup() {
    const summary = computed(() => {
      const rows = Array.isArray(dataSource.value) ? dataSource.value : []
      return rows.reduce((acc, item) => {
        const role = String(item?.role || '').toLowerCase()
        const status = String(item?.status || '').toLowerCase()
        const balance = Number(item?.balance || 0)
        acc.total += 1
        if (role === 'admin') {
          acc.adminCount += 1
        }
        if (status === 'disabled') {
          acc.disabledCount += 1
        } else {
          acc.enabledCount += 1
        }
        if (Number.isFinite(balance)) {
          acc.totalBalance += balance
        }
        return acc
      }, {
        total: 0,
        adminCount: 0,
        enabledCount: 0,
        disabledCount: 0,
        totalBalance: 0,
      })
    })

    const modalTitle = computed(() => isEditing.value ? '编辑用户' : '新增用户')

    return {
      labelCol: { style: { width: '100px' } },
      wrapperCol: { span: 16 },
      formState,
      resetPasswordForm,
      visible,
      resetPasswordVisible,
      dataSource,
      tabLoading,
      submitLoading,
      resetPasswordLoading,
      summary,
      isEditing,
      modalTitle,
      roleOptions: [
        { label: '普通用户', value: 'user' },
        { label: '管理员', value: 'admin' },
      ],
      statusOptions: [
        { label: '启用', value: 'enabled' },
        { label: '禁用', value: 'disabled' },
      ],
      balanceModeOptions: [
        { label: '覆盖', value: 'override' },
        { label: '增加', value: 'increase' },
        { label: '减少', value: 'decrease' },
      ],
      columns: [
        {
          title: '用户名',
          dataIndex: 'username',
          key: 'username',
          width: 160,
        },
        {
          title: '昵称',
          dataIndex: 'nickname',
          key: 'nickname',
          slots: {
            customRender: 'nickname',
          },
          width: 150,
        },
        {
          title: '角色',
          dataIndex: 'role',
          key: 'role',
          slots: {
            customRender: 'role',
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
          title: '余额',
          dataIndex: 'balance',
          key: 'balance',
          slots: {
            customRender: 'balance',
          },
          width: 120,
        },
        {
          title: '注册来源',
          dataIndex: 'registerSource',
          key: 'registerSource',
          slots: {
            customRender: 'registerSource',
          },
          width: 140,
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
        {
          title: '上次登录时间',
          dataIndex: 'lastLoginAt',
          key: 'lastLoginAt',
          slots: {
            customRender: 'lastLoginAt',
          },
          width: 180,
        },
        {
          title: '上次登录 IP',
          dataIndex: 'lastLoginIp',
          key: 'lastLoginIp',
          slots: {
            customRender: 'lastLoginIp',
          },
          width: 160,
        },
        {
          title: '操作',
          dataIndex: 'operation',
          key: 'operation',
          fixed: 'right',
          slots: {
            customRender: 'operation',
          },
          width: 190,
        },
      ],
    };
  },
  mounted() {
    this.handleUserList()
  },
  methods: {
    formatBalance(value) {
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
    roleText(value) {
      return String(value || '').toLowerCase() === 'admin' ? '管理员' : '普通用户'
    },
    roleColor(value) {
      return String(value || '').toLowerCase() === 'admin' ? 'gold' : 'blue'
    },
    statusText(value) {
      return String(value || '').toLowerCase() === 'disabled' ? '禁用' : '启用'
    },
    statusBadge(value) {
      return String(value || '').toLowerCase() === 'disabled' ? 'error' : 'success'
    },
    registerSourceText(value) {
      return buildRegisterSourceLabel(value)
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
        username: record.username || '',
        password: '',
        nickname: record.nickname || '',
        perms: record.perms || '',
        role: record.role || 'user',
        status: record.status || 'enabled',
        balance: record.balance ?? '',
        balanceMode: 'override',
        registerSource: record.registerSource || 'admin',
      }
      visible.value = true
    },
    openResetPasswordModal(record) {
      resetPasswordForm.value = {
        id: record.id,
        username: record.username || '',
        password: '',
      }
      resetPasswordVisible.value = true
    },
    handleUserList() {
      tabLoading.value = true
      getUserList().then(res => {
        if (res?.status === 200) {
          dataSource.value = Array.isArray(res?.data) ? res.data : []
        } else {
          message.error(res?.message || '获取用户列表失败')
        }
      }).catch(() => {
        message.error('获取用户列表失败')
      }).finally(() => {
        tabLoading.value = false
      })
    },
    onDelete(val) {
      delUser({ id: val }).then(res => {
        if (res?.status === 200) {
          message.success('删除成功')
          this.handleUserList()
        } else {
          message.error(res?.message || '删除失败')
        }
      }).catch(() => {
        message.error('删除失败')
      })
    },
    onSubmit() {
      const payload = {
        ...toRaw(formState).value,
        nickname: String(formState.value.nickname || '').trim(),
        role: String(formState.value.role || 'user').trim(),
        status: String(formState.value.status || 'enabled').trim(),
      }

      if (!isEditing.value) {
        payload.username = String(formState.value.username || '').trim()
        payload.password = String(formState.value.password || '').trim()
        if (!payload.username || !payload.password) {
          message.warning('请输入用户名和密码')
          return
        }
      }

      let editPayload = null
      if (isEditing.value) {
        try {
          editPayload = buildUserBalanceUpdatePayload(formState.value)
        } catch (error) {
          message.warning(error?.message || '请输入有效的余额数值')
          return
        }
      }

      submitLoading.value = true
      const submitter = isEditing.value
        ? updateUser(editPayload)
        : addUser(payload)

      submitter.then(res => {
        if (res?.status === 200) {
          message.success(isEditing.value ? '更新用户成功' : '添加用户成功')
          this.handleUserList()
          this.resetFormState()
          visible.value = false
        } else {
          message.error(res?.message || (isEditing.value ? '更新用户失败' : '添加用户失败'))
        }
      }).catch(() => {
        message.error(isEditing.value ? '更新用户失败' : '添加用户失败')
      }).finally(() => {
        submitLoading.value = false
      })
    },
    submitResetPassword() {
      let payload
      try {
        payload = buildResetPasswordPayload(resetPasswordForm.value.password)
      } catch (error) {
        message.warning(error?.message || '请输入新密码')
        return
      }

      resetPasswordLoading.value = true
      resetUserPassword({
        id: resetPasswordForm.value.id,
        ...payload,
      }).then((res) => {
        if (res?.status === 200) {
          message.success('重置密码成功')
          resetPasswordVisible.value = false
          this.handleUserList()
        } else {
          message.error(res?.message || '重置密码失败')
        }
      }).catch((error) => {
        message.error(error?.response?.data?.message || '重置密码失败')
      }).finally(() => {
        resetPasswordLoading.value = false
      })
    },
    resetFormState() {
      formState.value = {
        id: undefined,
        username: '',
        password: '',
        nickname: '',
        perms: '',
        role: 'user',
        status: 'enabled',
        balance: '',
        balanceMode: 'override',
        registerSource: 'admin',
      };
      isEditing.value = false
    },
    resetResetPasswordForm() {
      resetPasswordForm.value = {
        id: undefined,
        username: '',
        password: '',
      }
    },
  }
});
</script>

<style scoped>
.user-page {
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

.form-hint {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.6;
  color: #6b7280;
}
</style>
