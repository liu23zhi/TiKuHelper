<template>
  <div class="console-page user-page">
    <header class="section-heading page-heading">
      <div>
        <h1>用户管理</h1>
        <p class="page-lead">查看平台全部用户，并创建新的普通用户或管理员用户。</p>
      </div>
      <a-space wrap class="section-actions">
        <a-button @click="handleUserList" :loading="tabLoading">刷新</a-button>
        <a-button type="primary" @click="openCreateModal">新增用户</a-button>
      </a-space>
    </header>
    <a-row :gutter="16" class="summary-row">
      <a-col :xs="12" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="用户总数（筛选结果）" :value="summary.total" />
        </a-card>
      </a-col>
      <a-col :xs="12" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="当前页管理员" :value="summary.adminCount" />
        </a-card>
      </a-col>
      <a-col :xs="12" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="当前页启用账号" :value="summary.enabledCount" />
        </a-card>
      </a-col>
      <a-col :xs="12" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="当前页余额合计" prefix="¥" :value="summary.totalBalance" :precision="6" />
        </a-card>
      </a-col>
    </a-row>

    <a-card :bordered="false" class="page-card">
      <a-alert
        type="info"
        show-icon
        class="page-alert"
        message="当前页面展示的是平台全部用户。管理员可以创建用户，并直接编辑角色、状态、昵称、手机号与微信绑定。"
      />

      <a-form layout="inline" class="filter-form">
        <a-form-item label="关键词">
          <a-input-search
            v-model:value="keywordInput"
            placeholder="用户名 / 昵称 / 手机号 / 微信"
            allow-clear
            style="width: 260px"
            @search="applyKeyword"
            @press-enter="applyKeyword"
          />
        </a-form-item>
        <a-form-item label="角色">
          <a-select
            v-model:value="filters.role"
            allow-clear
            placeholder="全部角色"
            style="width: 140px"
            :options="roleOptions"
            @change="onFilterChange"
          />
        </a-form-item>
        <a-form-item label="状态">
          <a-select
            v-model:value="filters.status"
            allow-clear
            placeholder="全部状态"
            style="width: 140px"
            :options="statusOptions"
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

      <a-table
        class="desktop-user-table"
        bordered
        :data-source="dataSource"
        :columns="columns"
        :loading="tabLoading"
        :pagination="pagination"
        row-key="id"
        :scroll="{ x: 1560 }"
        @change="handleTableChange"
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
        <template #phone="{ record }">
          <span class="location-value">{{ formatValueWithLocation(record.phone, phoneLocations[normalizeLocationKey(record.phone)]) || '-' }}</span>
        </template>
        <template #wechat="{ record }">
          <a-tag :color="record.wechatId ? 'green' : 'default'">
            {{ record.wechatId ? '已绑定' : '未绑定' }}
          </a-tag>
        </template>
        <template #registerSource="{ record }">
          <a-tag>{{ registerSourceText(record.registerSource) }}</a-tag>
        </template>
        <template #phoneBoundAt="{ record }">
          <span>{{ formatDateTime(record.phoneBoundAt) }}</span>
        </template>
        <template #wechatBoundAt="{ record }">
          <span>{{ formatDateTime(record.wechatBoundAt) }}</span>
        </template>
        <template #createdAt="{ record }">
          <span>{{ formatDateTime(record.createdAt) }}</span>
        </template>
        <template #lastLoginAt="{ record }">
          <span>{{ formatDateTime(record.lastLoginAt) }}</span>
        </template>
        <template #lastLoginIp="{ record }">
          <span class="location-value">{{ formatValueWithLocation(record.lastLoginIp, ipLocations[normalizeLocationKey(record.lastLoginIp)]) || '-' }}</span>
        </template>
        <template #registerIp="{ record }">
          <span class="location-value">{{ formatValueWithLocation(record.registerIp, ipLocations[normalizeLocationKey(record.registerIp)]) || '-' }}</span>
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

      <div class="mobile-user-list">
        <article v-for="record in dataSource" :key="record.id" class="mobile-user-card">
          <header class="mobile-user-card__header">
            <div class="mobile-user-card__identity">
              <strong>{{ record.nickname || record.username || `用户 #${record.id}` }}</strong>
              <span>{{ record.username || `ID ${record.id}` }}</span>
            </div>
            <a-space size="small" class="mobile-user-card__actions">
              <a-button type="link" @click="openEditModal(record)">编辑</a-button>
              <a-popconfirm title="确定删除该用户吗？" ok-text="确定" cancel-text="取消" @confirm="onDelete(record.id)">
                <a-button type="link" danger>删除</a-button>
              </a-popconfirm>
            </a-space>
          </header>
          <dl class="mobile-user-card__meta">
            <div><dt>余额</dt><dd>¥ {{ formatBalance(record.balance) }}</dd></div>
            <div><dt>角色</dt><dd>{{ roleText(record.role) }}</dd></div>
            <div><dt>状态</dt><dd>{{ statusText(record.status) }}</dd></div>
            <div><dt>手机号</dt><dd>{{ record.phone || '-' }}</dd></div>
            <div><dt>微信</dt><dd>{{ record.wechatId ? '已绑定' : '未绑定' }}</dd></div>
          </dl>
          <a-button block size="small" @click="openResetPasswordModal(record)">重置密码</a-button>
        </article>
        <a-empty v-if="!dataSource.length" description="当前还没有用户记录" />
      </div>
      <div class="mobile-user-pagination">
        <a-pagination
          :current="pagination.current"
          :page-size="pagination.pageSize"
          :total="pagination.total"
          size="small"
          show-less-items
          @change="handleMobilePageChange"
        />
      </div>
    </a-card>

    <a-modal
      v-model:open="visible"
      :title="modalTitle"
      @ok="onSubmit"
      :afterClose="resetFormState"
      ok-text="确定"
      cancel-text="取消"
      :confirm-loading="submitLoading"
    >
      <a-form :model="formState" :label-col="labelCol" :wrapper-col="wrapperCol" layout="horizontal">
        <a-form-item label="用户名" required>
          <a-input v-model:value="formState.username" :disabled="isEditing" placeholder="请输入用户名" @input="formState.username = limitUsernameInput(formState.username)" />
        </a-form-item>
        <a-form-item v-if="!isEditing" label="密码" required>
          <a-input-password v-model:value="formState.password" placeholder="请输入密码" />
          <div class="form-hint">密码不能为空，最多 72 字节；弱密码提交前会二次确认。</div>
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
        <a-form-item v-if="isEditing" label="当前余额">
          <a-input :value="formatBalance(formState.balance)" readonly prefix="¥" />
          <a-button class="balance-adjust-button" @click="openBalanceModal">修改余额</a-button>
        </a-form-item>
        <a-form-item v-if="isEditing" label="手机号">
          <a-input v-model:value="formState.phone" placeholder="留空表示清空手机号绑定" />
          <div class="form-hint">输入新手机号可直接绑定；留空则清空当前绑定。</div>
        </a-form-item>
        <a-form-item v-if="isEditing" label="重指派同号">
          <a-switch v-model:checked="formState.forceReassignPhone" />
          <div class="form-hint">若该手机号已绑定其他用户，开启后会把旧绑定转移到当前用户。</div>
        </a-form-item>
        <a-form-item v-if="isEditing" label="微信 ID">
          <a-input v-model:value="formState.wechatId" placeholder="留空表示清空微信绑定" />
          <a-space wrap class="wechat-id-actions">
            <a-button size="small" :loading="wechatScanLoading" @click="startWechatScan">扫码获取 ID</a-button>
            <a-button size="small" @click="toggleWechatCodeInput">验证码获取 ID</a-button>
          </a-space>
          <div v-if="wechatScanVisible" class="wechat-id-panel">
            <a-spin v-if="wechatScanLoading" />
            <img v-else-if="wechatScanQrCodeImageData" :src="wechatScanQrCodeImageData" alt="微信换绑二维码" class="wechat-id-qrcode" />
            <div class="form-hint">{{ wechatScanStatusText }}</div>
          </div>
          <div v-if="wechatCodeVisible" class="wechat-id-panel">
            <a-space>
              <a-input
                v-model:value="wechatCode"
                placeholder="请输入用户提供的微信验证码"
                @input="wechatCode = normalizeWechatCodeInput(wechatCode)"
              />
              <a-button :loading="wechatCodeLoading" @click="resolveWechatIDByCode">获取 ID</a-button>
            </a-space>
          </div>
          <div class="form-hint">可手动填写微信 OpenID，也可扫码或使用验证码自动获取；留空保存会清空绑定。</div>
        </a-form-item>
        <a-form-item v-if="isEditing" label="重指派微信">
          <a-switch v-model:checked="formState.forceReassignWechat" />
          <div class="form-hint">若该微信已绑定其他用户，开启后会把旧绑定转移到当前用户。</div>
        </a-form-item>
        <a-form-item v-if="!isEditing" label="启用赠送">
          <a-switch v-model:checked="formState.giftEnabled" />
          <div class="form-hint">默认开启；关闭后，本次创建的新用户不会获得赠送金额。</div>
        </a-form-item>
        <a-form-item v-if="!isEditing && formState.giftEnabled" label="赠送金额">
          <a-input-number
            v-model:value="formState.giftAmount"
            :min="0"
            :precision="6"
            style="width: 220px"
          />
          <div class="form-hint">默认使用系统管理中的新用户赠送金额，可按本次创建单独修改。金额最多支持 6 位小数。</div>
        </a-form-item>
        <a-form-item label="创建来源">
          <a-input :value="registerSourceText(formState.registerSource)" disabled />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="balanceVisible"
      title="修改用户余额"
      ok-text="确认修改"
      cancel-text="取消"
      :confirm-loading="balanceSubmitting"
      :mask-closable="!balanceSubmitting"
      :keyboard="!balanceSubmitting"
      :closable="!balanceSubmitting"
      :cancel-button-props="{ disabled: balanceSubmitting }"
      @ok="submitBalanceAdjustment"
    >
      <a-form layout="vertical" autocomplete="off">
        <a-form-item label="目标用户">
          <a-input :value="`${formState.username}（ID: ${formState.id}）`" readonly />
        </a-form-item>
        <a-form-item label="当前余额">
          <a-input :value="formatBalance(formState.balance)" readonly prefix="¥" />
        </a-form-item>
        <a-form-item label="修改模式" required>
          <a-select v-model:value="balanceForm.mode" :options="balanceModeOptions" :disabled="balanceSubmitting" />
        </a-form-item>
        <a-form-item :label="balanceForm.mode === 'override' ? '覆盖后的余额' : '调整金额'" required>
          <a-input-number
            v-model:value="balanceForm.amount"
            :min="balanceForm.mode === 'override' ? undefined : 0"
            :precision="6"
            :step="1"
            :disabled="balanceSubmitting"
            style="width: 100%"
            placeholder="请输入金额，最多 6 位小数"
          />
        </a-form-item>
        <a-form-item>
          <a-checkbox v-model:checked="balanceForm.showInStatement" :disabled="balanceSubmitting">同步到账单流水，向用户展示本次修改</a-checkbox>
          <div class="form-hint">无论是否勾选，都会记录操作日志及管理员账单明细；可在账单明细中调整流水可见性。</div>
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="resetPasswordVisible"
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
          <div class="form-hint">密码不能为空，最多 72 字节；弱密码提交前会二次确认。</div>
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script>
import {
  getUserList,
  delUser,
  addUser,
  updateUser,
  adjustUserBalance,
  resetUserPassword,
  createWechatLoginQRCode,
  resolveAdminWechatIDByScan,
  resolveAdminWechatIDByCode,
  getSystemSettings,
  getPhoneLocations,
  getIpLocations,
} from '@/api/TikuComponentApi';
import { buildAdminCreateUserPayload, buildRegisterSourceLabel, buildResetPasswordPayload, buildUserProfileUpdatePayload, buildUserBalanceAdjustmentPayload, limitUsernameInput, runAdminPasswordAction } from '@/utils/userPortalHelpers.mjs';
import { formatMoneyValue } from '@/utils/moneyFormat.mjs';
import { buildSearchParams, countActiveFilters, normalizeSearchKeyword } from '@/utils/listSearchHelpers.mjs';
import {
  collectLocationKeys,
  lookupLocationAddresses,
  normalizeLocationKey,
  formatValueWithLocation as formatValueWithLocationBase,
} from '@/utils/locationHelpers.mjs';
import { message } from 'ant-design-vue';
import Swal from 'sweetalert2';
import { computed, defineComponent, reactive, ref } from 'vue';

const dataSource = ref([])

// 归属地映射：key 为归一化后的手机号 / IP，value 为展示文本。
// 列表渲染时按行取值，因此**不需要**改写 dataSource 里的行对象。
const phoneLocations = reactive({})
const ipLocations = reactive({})

function formatValueWithLocation(rawValue, address) {
  return formatValueWithLocationBase(rawValue, address)
}

// 把归属地结果写进响应式映射；重复查询同一批 key 直接覆盖，无需清理。
function mergeLocations(target, addresses) {
  Object.keys(addresses).forEach((key) => {
    target[normalizeLocationKey(key)] = addresses[key]
  })
}

// enrichLocations 拉取本页手机号 / IP 的归属地。
//
// 失败必须静默：归属地只是展示增强，未配置密钥或上游异常时
// 对应列退化为只显示原始值，不能打扰正在管理用户的流程。
async function enrichColumns(rows) {
  const phones = collectLocationKeys(rows, 'phone')
  const ips = collectLocationKeys(rows, ['lastLoginIp', 'registerIp'])

  const tasks = []
  if (phones.length) {
    tasks.push(
      lookupLocationAddresses('phone', phones, getPhoneLocations)
        .then((addresses) => mergeLocations(phoneLocations, addresses)),
    )
  }
  if (ips.length) {
    tasks.push(
      lookupLocationAddresses('ip', ips, getIpLocations)
        .then((addresses) => mergeLocations(ipLocations, addresses)),
    )
  }
  await Promise.all(tasks)
}
// 已生效的筛选条件；keywordInput 只是输入框的即时值。
const filters = reactive({
  keyword: '',
  role: undefined,
  status: undefined,
})
const keywordInput = ref('')
const pagination = reactive({
  current: 1,
  pageSize: 10,
  total: 0,
  showSizeChanger: true,
  showQuickJumper: true,
  showTotal: (total) => `共 ${total} 条`,
  pageSizeOptions: ['10', '20', '50', '100'],
})
const visible = ref(false)
const submitLoading = ref(false)
const passwordConfirmationPending = ref(false)
const balanceVisible = ref(false)
const balanceSubmitting = ref(false)
const balanceForm = reactive({ mode: 'increase', amount: null, showInStatement: true })
const isEditing = ref(false)
const resetPasswordVisible = ref(false)
const resetPasswordLoading = ref(false)
const wechatScanVisible = ref(false)
const wechatScanLoading = ref(false)
const wechatScanQrCodeImageData = ref('')
const wechatScanLoginToken = ref('')
const wechatScanStatusText = ref('')
const wechatCodeVisible = ref(false)
const wechatCode = ref('')
const wechatCodeLoading = ref(false)
const systemNewUserBonusAmount = ref(0)
let wechatScanTimer = null
const formState = ref({
  id: undefined,
  username: '',
  password: '',
  nickname: '',
  perms: '',
  role: 'user',
  status: 'enabled',
  balance: '',
  registerSource: 'admin',
  phone: '',
  phoneBoundAt: '',
  forceReassignPhone: false,
  wechatId: '',
  wechatBoundAt: '',
  forceReassignWechat: false,
  giftEnabled: true,
  giftAmount: '',
})
const resetPasswordForm = ref({
  id: undefined,
  username: '',
  password: '',
})
const tabLoading = ref(true)

function stopWechatScanTimer() {
  if (wechatScanTimer !== null) {
    clearInterval(wechatScanTimer)
    wechatScanTimer = null
  }
}

function resetWechatResolveState() {
  stopWechatScanTimer()
  wechatScanVisible.value = false
  wechatScanLoading.value = false
  wechatScanQrCodeImageData.value = ''
  wechatScanLoginToken.value = ''
  wechatScanStatusText.value = ''
  wechatCodeVisible.value = false
  wechatCode.value = ''
  wechatCodeLoading.value = false
}

export default defineComponent({
  name: 'UserList',
  setup() {
    // 用户列表改为服务端分页后，这里的统计只覆盖「当前页」，
    // 因此标题里明确标注范围，避免被误读成全站合计。
    const summary = computed(() => {
      const rows = Array.isArray(dataSource.value) ? dataSource.value : []
      return rows.reduce((acc, item) => {
        const role = String(item?.role || '').toLowerCase()
        const status = String(item?.status || '').toLowerCase()
        const balance = Number(item?.balance || 0)
        acc.currentPageCount += 1
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
        // 用户总数来自服务端的筛选结果总数，不受分页影响。
        total: Number(pagination.total || 0),
        currentPageCount: 0,
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
      limitUsernameInput,
      resetPasswordForm,
      visible,
      resetPasswordVisible,
      dataSource,
      // 归属地展示：映射 + 取值/格式化函数（模板按行取值，不改写行对象）。
      phoneLocations,
      ipLocations,
      normalizeLocationKey,
      formatValueWithLocation,
      tabLoading,
      pagination,
      filters,
      keywordInput,
      activeFilterCount: computed(() => countActiveFilters({ keyword: filters.keyword, role: filters.role, status: filters.status })),
      submitLoading,
      balanceVisible,
      balanceSubmitting,
      balanceForm,
      resetPasswordLoading,
      wechatScanVisible,
      wechatScanLoading,
      wechatScanQrCodeImageData,
      wechatScanLoginToken,
      wechatScanStatusText,
      wechatCodeVisible,
      wechatCode,
      wechatCodeLoading,
  systemNewUserBonusAmount,
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
        { label: '添加', value: 'increase' },
        { label: '减少', value: 'decrease' },
        { label: '覆盖', value: 'override' },
      ],
      columns: [
        {
          title: 'ID',
          dataIndex: 'id',
          key: 'id',
          width: 80,
        },
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
          title: '手机号',
          dataIndex: 'phone',
          key: 'phone',
          slots: {
            customRender: 'phone',
          },
          width: 150,
        },
        {
          title: '微信绑定',
          dataIndex: 'wechatId',
          key: 'wechat',
          slots: {
            customRender: 'wechat',
          },
          width: 120,
        },
        {
          title: '绑定时间',
          dataIndex: 'phoneBoundAt',
          key: 'phoneBoundAt',
          slots: {
            customRender: 'phoneBoundAt',
          },
          width: 180,
        },
        {
          title: '微信绑定时间',
          dataIndex: 'wechatBoundAt',
          key: 'wechatBoundAt',
          slots: {
            customRender: 'wechatBoundAt',
          },
          width: 180,
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
          title: '注册 IP',
          dataIndex: 'registerIp',
          key: 'registerIp',
          slots: {
            customRender: 'registerIp',
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
    this.loadNewUserBonusDefault()
  },
  beforeUnmount() {
    stopWechatScanTimer()
  },
  methods: {
    formatBalance(value) {
      return formatMoneyValue(value || 0)
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
    normalizeWechatCodeInput(value) {
      return String(value || '').replace(/\D/g, '').slice(0, 12)
    },
    toggleWechatCodeInput() {
      wechatCodeVisible.value = !wechatCodeVisible.value
      if (!wechatCodeVisible.value) {
        wechatCode.value = ''
      }
    },
    stopWechatScan() {
      stopWechatScanTimer()
      wechatScanLoading.value = false
    },
    async startWechatScan() {
      this.stopWechatScan()
      resetWechatResolveState()
      wechatScanVisible.value = true
      wechatScanLoading.value = true
      wechatScanStatusText.value = '正在创建二维码'
      try {
        const res = await createWechatLoginQRCode()
        if (res?.status !== 200) {
          throw new Error(res?.message || '二维码创建失败')
        }
        wechatScanQrCodeImageData.value = res?.data?.qrcodeImageData || ''
        wechatScanLoginToken.value = res?.data?.loginToken || ''
        if (!wechatScanQrCodeImageData.value || !wechatScanLoginToken.value) {
          throw new Error('二维码创建失败')
        }
        wechatScanStatusText.value = '请使用需要绑定的微信扫码'
        wechatScanTimer = setInterval(() => {
          this.pollWechatScanID()
        }, 2500)
        this.pollWechatScanID()
      } catch (error) {
        wechatScanStatusText.value = error?.message || '二维码创建失败'
        message.error(wechatScanStatusText.value)
      } finally {
        wechatScanLoading.value = false
      }
    },
    async pollWechatScanID() {
      if (!wechatScanLoginToken.value) {
        return
      }
      try {
        const res = await resolveAdminWechatIDByScan({ loginToken: wechatScanLoginToken.value })
        if (res?.status !== 200) {
          wechatScanStatusText.value = res?.message || '扫码状态查询失败'
          return
        }
        const status = String(res?.data?.status || '').trim().toLowerCase()
        if (status === 'pending') {
          wechatScanStatusText.value = '等待微信扫码'
          return
        }
        if (status === 'scanned') {
          wechatScanStatusText.value = '已扫码，请确认'
          return
        }
        if (status === 'success' && res?.data?.wechatId) {
          formState.value.wechatId = String(res.data.wechatId || '').trim()
          wechatScanStatusText.value = '已获取微信 ID，请保存用户'
          message.success('微信 ID 已填入')
          this.stopWechatScan()
          wechatScanVisible.value = false
          return
        }
        if (status === 'expired') {
          wechatScanStatusText.value = '二维码已过期，请重新获取'
          this.stopWechatScan()
        }
      } catch (error) {
        wechatScanStatusText.value = error?.message || '扫码状态查询失败'
      }
    },
    async resolveWechatIDByCode() {
      if (!wechatCode.value) {
        message.warning('请输入微信验证码')
        return
      }
      wechatCodeLoading.value = true
      try {
        const res = await resolveAdminWechatIDByCode({ code: wechatCode.value })
        if (res?.status !== 200) {
          message.error(res?.message || '获取微信 ID 失败')
          return
        }
        formState.value.wechatId = String(res?.data?.wechatId || '').trim()
        if (!formState.value.wechatId) {
          message.error('未获取到微信 ID')
          return
        }
        wechatCodeVisible.value = false
        wechatCode.value = ''
        message.success('微信 ID 已填入')
      } catch (error) {
        message.error(error?.message || '获取微信 ID 失败')
      } finally {
        wechatCodeLoading.value = false
      }
    },
    openCreateModal() {
      this.resetFormState()
      formState.value.giftAmount = systemNewUserBonusAmount.value
      isEditing.value = false
      visible.value = true
    },
    openEditModal(record) {
      resetWechatResolveState()
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
        registerSource: record.registerSource || 'admin',
        phone: record.phone || '',
        phoneBoundAt: record.phoneBoundAt || '',
        forceReassignPhone: false,
        wechatId: record.wechatId || '',
        wechatBoundAt: record.wechatBoundAt || '',
        forceReassignWechat: false,
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
    openBalanceModal() {
      balanceForm.mode = 'increase'
      balanceForm.amount = null
      balanceForm.showInStatement = true
      balanceVisible.value = true
    },
    async submitBalanceAdjustment() {
      if (balanceSubmitting.value) return
      let payload
      try {
        payload = buildUserBalanceAdjustmentPayload({ id: formState.value.id, ...balanceForm })
      } catch (error) {
        message.warning(error?.message || '请检查余额调整参数')
        return
      }
      balanceSubmitting.value = true
      try {
        const res = await adjustUserBalance(payload)
        if (res?.status === 202) {
          message.warning('余额调整状态待核对，请刷新账户信息确认，勿重复提交')
          this.handleUserList()
          return
        }
        if (res?.status !== 200) throw new Error(res?.message || '修改余额失败')
        formState.value.balance = res.data.balance
        balanceVisible.value = false
        message.success('余额修改成功，已记录日志与账单明细')
        this.handleUserList()
      } catch (error) {
        message.error(error?.response?.data?.message || error?.message || '修改余额失败')
      } finally {
        balanceSubmitting.value = false
      }
    },
    // 点「搜索」或回车时才把输入框内容变成生效的筛选条件。
    applyKeyword() {
      filters.keyword = normalizeSearchKeyword(keywordInput.value)
      keywordInput.value = filters.keyword
      pagination.current = 1
      this.handleUserList()
    },
    onFilterChange() {
      pagination.current = 1
      this.handleUserList()
    },
    resetFilters() {
      filters.keyword = ''
      keywordInput.value = ''
      filters.role = undefined
      filters.status = undefined
      pagination.current = 1
      this.handleUserList()
    },
    handleTableChange(paginationData) {
      pagination.current = paginationData.current
      pagination.pageSize = paginationData.pageSize
      this.handleUserList()
    },
    handleMobilePageChange(current, pageSize) {
      pagination.current = current
      pagination.pageSize = pageSize
      this.handleUserList()
    },
    handleUserList() {
      tabLoading.value = true
      getUserList(buildSearchParams({
        keyword: filters.keyword,
        role: filters.role,
        status: filters.status,
      }, pagination.current, pagination.pageSize)).then(res => {
        if (res?.status === 200) {
          // 接口现在返回 { data, total, page, pageSize }。
          const payload = res?.data || {}
          dataSource.value = Array.isArray(payload.data) ? payload.data : []
          pagination.total = Number(payload.total || 0)
          pagination.current = Number(payload.page || 1)
          pagination.pageSize = Number(payload.pageSize || 10)
          // 归属地单独批量拉取（失败静默）：列表接口本身不依赖它。
          enrichColumns(dataSource.value)
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
    async confirmWeakPassword(action) {
      const confirmation = await Swal.fire({
        title: '确认使用弱密码？',
        text: `当前密码未达到推荐强度。推荐至少 8 位，并同时包含大写字母、小写字母、数字和特殊字符。弱密码更容易被猜测、撞库或重复使用；密码仍不能超过 bcrypt 支持的 72 字节。确认继续${action === 'create' ? '创建用户' : '重置密码'}吗？`,
        icon: 'warning',
        showCancelButton: true,
        confirmButtonText: action === 'create' ? '仍然创建' : '仍然重置',
        cancelButtonText: '返回修改',
        reverseButtons: true,
      })
      return confirmation?.isConfirmed === true
    },
    async onSubmit() {
      if (submitLoading.value || passwordConfirmationPending.value) return
      let createPayload = null
      if (!isEditing.value) {
        try {
          createPayload = buildAdminCreateUserPayload(formState.value)
        } catch (error) {
          message.warning(error?.message || '请完善新增用户必填项')
          return
        }
      }

      let editPayload = null
      if (isEditing.value) {
        try {
          editPayload = buildUserProfileUpdatePayload(formState.value)
        } catch (error) {
          message.warning(error?.message || '请检查用户资料')
          return
        }
      }

      const submit = async () => {
        submitLoading.value = true
        try {
          const res = await (isEditing.value ? updateUser(editPayload) : addUser(createPayload))
          if (res?.status !== 200) {
            message.error(res?.message || (isEditing.value ? '更新用户失败' : '添加用户失败'))
            return
          }
          message.success(isEditing.value ? '更新用户成功' : '添加用户成功')
          this.handleUserList()
          this.resetFormState()
          visible.value = false
        } catch (error) {
          message.error(error?.response?.data?.message || (isEditing.value ? '更新用户失败' : '添加用户失败'))
        } finally {
          submitLoading.value = false
        }
      }

      if (createPayload) {
        passwordConfirmationPending.value = true
        try {
          await runAdminPasswordAction(createPayload.password, submit, () => this.confirmWeakPassword('create', createPayload.password))
        } catch (error) {
          message.error(error?.message || '创建用户失败')
        } finally {
          passwordConfirmationPending.value = false
        }
        return
      }
      await submit()
    },
    async submitResetPassword() {
      if (resetPasswordLoading.value || passwordConfirmationPending.value) return
      let payload
      try {
        payload = buildResetPasswordPayload(resetPasswordForm.value.password)
      } catch (error) {
        message.warning(error?.message || '请输入新密码')
        return
      }
      const submit = async () => {
        resetPasswordLoading.value = true
        try {
          const res = await resetUserPassword({ id: resetPasswordForm.value.id, ...payload })
          if (res?.status !== 200) {
            message.error(res?.message || '重置密码失败')
            return
          }
          message.success('重置密码成功')
          resetPasswordVisible.value = false
          this.handleUserList()
        } catch (error) {
          message.error(error?.response?.data?.message || '重置密码失败')
        } finally {
          resetPasswordLoading.value = false
        }
      }

      passwordConfirmationPending.value = true
      try {
        await runAdminPasswordAction(payload.password, submit, () => this.confirmWeakPassword('reset', payload.password))
      } catch (error) {
        message.error(error?.message || '重置密码失败')
      } finally {
        passwordConfirmationPending.value = false
      }
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
        registerSource: 'admin',
        phone: '',
        phoneBoundAt: '',
        forceReassignPhone: false,
        wechatId: '',
        wechatBoundAt: '',
        forceReassignWechat: false,
        giftEnabled: true,
        giftAmount: '',
      };
      resetWechatResolveState()
      isEditing.value = false
    },
    resetResetPasswordForm() {
      resetPasswordForm.value = {
        id: undefined,
        username: '',
        password: '',
      }
    },
    loadNewUserBonusDefault() {
      getSystemSettings().then((res) => {
        if (res?.status === 200) {
          systemNewUserBonusAmount.value = Number(res?.data?.newUserBonusAmount || 0)
        }
      }).catch(() => {})
    },
  }
});
</script>

<style scoped>
.balance-adjust-button {
  margin-top: 8px;
}

.user-page {
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

.filter-form {
  margin: 12px 0 16px;
  display: flex;
  flex-wrap: wrap;
  gap: 8px 0;
}

.summary-row {
  margin-bottom: 0;
}

.user-page .page-card {
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: var(--console-radius, 12px);
}

.page-title-wrap {
  display: flex;
  align-items: center;
}

.page-title {
  font-size: 20px;
  font-weight: 650;
  color: var(--console-text, #1f2937);
}

.page-subtitle {
  margin-top: 6px;
  color: var(--console-muted, #64748b);
  font-size: 13px;
}

.page-alert {
  margin-bottom: 16px;
}

.filter-form {
  padding: 16px;
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: 8px;
  background: var(--console-canvas, #f5f7fb);
}

.filter-form :deep(.ant-form-item) {
  margin-bottom: 0;
}

.user-page :deep(.ant-table) {
  font-size: 13px;
}

.mobile-user-list,
.mobile-user-pagination {
  display: none;
}

.location-value {
  display: inline-block;
  white-space: pre-line;
  line-height: 1.5;
  text-align: left;
}

.wechat-id-actions {
  margin-top: 8px;
}

.wechat-id-panel {
  margin-top: 10px;
  padding: 12px;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  background: #f9fafb;
}

.wechat-id-qrcode {
  display: block;
  width: 220px;
  max-width: 100%;
  margin: 0 auto 8px;
  border-radius: 10px;
}

.form-hint {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.6;
  color: #6b7280;
}

@media (max-width: 640px) {
  .user-page :deep(.ant-card-body),
  .user-page .page-card :deep(.ant-card-body) {
    padding: 12px;
  }

  .desktop-user-table {
    display: none;
  }

  .mobile-user-list {
    display: grid;
    gap: 10px;
  }

  .mobile-user-card {
    padding: 12px;
    border: 1px solid var(--console-border, #e5e7eb);
    border-radius: 8px;
    background: var(--console-surface, #fff);
  }

  .mobile-user-card__header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 8px;
  }

  .mobile-user-card__identity {
    display: grid;
    gap: 3px;
    min-width: 0;
  }

  .mobile-user-card__identity strong,
  .mobile-user-card__identity span {
    overflow-wrap: anywhere;
  }

  .mobile-user-card__identity span {
    color: var(--console-muted, #64748b);
    font-size: 12px;
  }

  .mobile-user-card__actions {
    flex-shrink: 0;
  }

  .mobile-user-card__actions :deep(.ant-btn) {
    padding-inline: 4px;
  }

  .mobile-user-card__meta {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
    margin: 12px 0;
    padding: 10px 0;
    border-top: 1px solid var(--console-border, #e5e7eb);
    border-bottom: 1px solid var(--console-border, #e5e7eb);
  }

  .mobile-user-card__meta div {
    min-width: 0;
  }

  .mobile-user-card__meta dt {
    color: var(--console-muted, #64748b);
    font-size: 11px;
  }

  .mobile-user-card__meta dd {
    margin: 2px 0 0;
    overflow-wrap: anywhere;
    color: var(--console-text, #1f2937);
    font-size: 13px;
  }

  .mobile-user-pagination {
    display: flex;
    justify-content: flex-end;
    margin-top: 12px;
  }

  .filter-form,
  .filter-form :deep(.ant-form-item),
  .filter-form :deep(.ant-input-search),
  .filter-form :deep(.ant-select) {
    width: 100%;
  }
}
</style>
