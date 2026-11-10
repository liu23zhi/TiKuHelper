<template>
  <div class="console-page user-page">
    <ConsolePageHeading title="用户管理" description="查看平台全部用户，并创建新的普通用户或管理员用户。">
      <template #actions>
      <a-space wrap class="section-actions">
        <a-button @click="handleUserList" :loading="tabLoading">刷新</a-button>
        <a-button type="primary" @click="openCreateModal">新增用户</a-button>
      </a-space>
      </template>
    </ConsolePageHeading>
    <ConsoleSummaryGrid>
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="用户总数（筛选结果）" :value="summary.total" />
        </a-card>
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="当前页管理员" :value="summary.adminCount" />
        </a-card>
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="当前页启用账号" :value="summary.enabledCount" />
        </a-card>
        <a-card :bordered="false" class="summary-card">
          <a-statistic title="当前页余额合计" prefix="¥" :value="summary.totalBalance" :precision="6" />
        </a-card>
    </ConsoleSummaryGrid>

    <a-card :bordered="false" class="page-card">
      <a-alert v-if="!canManageUsers" type="warning" show-icon class="page-alert" message="当前账号无用户管理权限。" />
      <RequestFailureAlert
        v-if="canManageUsers && listFailure"
        :key="listFailureContextKey"
        :failure="listFailure"
        message="获取用户列表失败"
        retryable
        :busy="tabLoading"
        :context-key="listFailureContextKey"
        :is-current="isCurrentListFailure"
        @retry="retryUserList"
      />
      <section v-if="canManageUsers && deleteFailure" data-user-delete-feedback class="page-alert">
        <RequestFailureAlert :key="deleteFailureContextKey" :failure="deleteFailure" message="删除用户请求未完成，请先刷新核对结果，勿重复提交。" :context-key="deleteFailureContextKey" :is-current="isCurrentDeleteFailure" />
      </section>
      <a-alert
        type="info"
        show-icon
        class="page-alert"
        message="当前页面展示的是平台全部用户。管理员可以创建用户，并直接编辑角色、状态、昵称、手机号与微信绑定。"
      />

      <a-form layout="inline" class="filter-form">
        <a-form-item label="关键词" :html-for="fieldId('filter-keyword')">
          <a-input-search
            :id="fieldId('filter-keyword')"
            v-model:value="keywordInput"
            placeholder="用户名 / 昵称 / 手机号 / 微信"
            allow-clear
            style="width: 260px"
            @search="applyKeyword"
            @press-enter="applyKeyword"
          />
        </a-form-item>
        <a-form-item label="角色" :html-for="fieldId('filter-role')">
          <a-select
            :id="fieldId('filter-role')"
            v-model:value="filters.role"
            allow-clear
            placeholder="全部角色"
            style="width: 140px"
            :options="roleOptions"
            @change="onFilterChange"
          />
        </a-form-item>
        <a-form-item label="状态" :html-for="fieldId('filter-status')">
          <a-select
            :id="fieldId('filter-status')"
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

      <div class="table-preferences-actions">
        <TableColumnSettings
          :model-value="selectedColumnKeys"
          :options="columnOptions"
          :context-key="columnContextKey"
          :priority-keys="priorityColumnKeys"
          @update:model-value="setSelectedColumnKeys"
          @reset="resetColumns"
        />
      </div>
      <ConsoleTableShell label="用户列表" class="desktop-user-table-shell">
      <a-table
        class="desktop-user-table"
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
        <template #bodyCell="{ column, record }">
          <span v-if="column.key === 'nickname'">{{ record.nickname || '-' }}</span>
          <a-tag v-else-if="column.key === 'role'" :color="roleColor(record.role)">{{ roleText(record.role) }}</a-tag>
          <a-badge v-else-if="column.key === 'status'" :status="statusBadge(record.status)" :text="statusText(record.status)" />
          <span v-else-if="column.key === 'balance'">¥ {{ formatBalance(record.balance) }}</span>
          <span v-else-if="column.key === 'phone'" class="location-value">{{ formatValueWithLocation(record.phone, phoneLocations[normalizeLocationKey(record.phone)]) || '-' }}</span>
          <a-tag v-else-if="column.key === 'wechat'" :color="record.wechatId ? 'green' : 'default'">
            {{ record.wechatId ? '已绑定' : '未绑定' }}
          </a-tag>
          <a-tag v-else-if="column.key === 'registerSource'">{{ registerSourceText(record.registerSource) }}</a-tag>
          <span v-else-if="['phoneBoundAt', 'wechatBoundAt', 'createdAt', 'lastLoginAt'].includes(column.key)" class="user-time-cell">{{ formatDateTime(record[column.key]) }}</span>
          <span v-else-if="['lastLoginIp', 'registerIp'].includes(column.key)" class="location-value">{{ formatValueWithLocation(record[column.key], ipLocations[normalizeLocationKey(record[column.key])]) || '-' }}</span>
          <a-space v-else-if="column.key === 'operation'" size="small" wrap class="user-operation-actions">
            <a-button type="link" style="padding: 0" @click="openEditModal(record)">编辑</a-button>
            <a-button type="link" style="padding: 0" @click="openResetPasswordModal(record)">重置密码</a-button>
            <a-popconfirm
              :key="actionContextKey + ':' + record.id"
              v-if="dataSource.length"
              ok-text="确定"
              cancel-text="取消"
              title="确定删除该用户吗？"
              :onConfirm="deleteConfirmation(record.id)"
            >
              <a-button type="link" danger style="padding: 0">删除</a-button>
            </a-popconfirm>
          </a-space>
          <template v-else>{{ record[column.dataIndex] ?? '-' }}</template>
        </template>
        <template #emptyText>
          <a-empty v-if="!tabLoading && !listFailure && canManageUsers" description="当前还没有用户记录，点击右上角新增用户即可开始。" />
          <p v-else class="form-hint" role="status">{{ !canManageUsers ? '用户列表尚未确认，请使用具有用户管理权限的账号重试。' : tabLoading ? '正在读取用户列表，请稍候。' : '暂时无法读取用户列表，请使用上方重试。' }}</p>
        </template>
      </a-table>
      </ConsoleTableShell>

      <div class="mobile-user-list">
        <article v-for="record in dataSource" :key="record.id" class="mobile-user-card">
          <header class="mobile-user-card__header">
            <div class="mobile-user-card__identity">
              <strong>{{ record.nickname || record.username || `用户 #${record.id}` }}</strong>
              <span>{{ record.username || `ID ${record.id}` }}</span>
            </div>
            <a-space size="small" class="mobile-user-card__actions">
              <a-button type="link" @click="openEditModal(record)">编辑</a-button>
              <a-popconfirm :key="actionContextKey + ':' + record.id" title="确定删除该用户吗？" ok-text="确定" cancel-text="取消" :onConfirm="deleteConfirmation(record.id)">
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
        <template v-if="!dataSource.length">
          <a-empty v-if="!tabLoading && !listFailure && canManageUsers" description="当前还没有用户记录" />
          <p v-else class="form-hint" role="status">{{ !canManageUsers ? '用户列表尚未确认，请使用具有用户管理权限的账号重试。' : tabLoading ? '正在读取用户列表，请稍候。' : '暂时无法读取用户列表，请使用上方重试。' }}</p>
        </template>
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
      :wrap-props="{ 'aria-modal': !balanceVisible }"
      :title="modalTitle"
      @ok="onSubmit"
      :afterClose="afterEditorClose"
      ok-text="确定"
      cancel-text="取消"
      :confirm-loading="submitLoading"
    >
      <section v-if="hasUnsubmittedSavedEdit" data-user-edit-saved-draft class="page-alert" role="status">
        <a-alert type="info" show-icon message="提交的用户资料已保存，当前修改尚未提交。" />
      </section>
      <section v-if="editFailure" :data-user-edit-feedback="editFailureAction === 'edit' ? '' : undefined" :data-user-create-feedback="editFailureAction === 'create' ? '' : undefined" class="page-alert">
        <RequestFailureAlert :key="editFailureContextKey" :failure="editFailure" :message="editFailureAction === 'create' ? '创建用户请求未完成，请核对当前资料后再操作。' : '更新用户请求未完成，请核对当前资料后再操作。'" :busy="submitLoading" :context-key="editFailureContextKey" :is-current="isCurrentEditFailure" />
      </section>
      <a-form :model="formState" :label-col="labelCol" :wrapper-col="wrapperCol" layout="horizontal">
        <a-form-item label="用户名" required :html-for="fieldId('editor-username')" :validate-status="usernameError ? 'error' : undefined">
          <a-input ref="usernameInput" :id="fieldId('editor-username')" v-model:value="formState.username" :disabled="isEditing" :aria-invalid="usernameError ? 'true' : undefined" :aria-describedby="usernameError ? fieldId('editor-username-error') : undefined" placeholder="请输入用户名" @input="formState.username = limitUsernameInput(formState.username)" />
          <template #help><span v-if="usernameError" :id="fieldId('editor-username-error')" role="alert">{{ usernameError }}</span></template>
        </a-form-item>
        <a-form-item v-if="!isEditing" label="密码" required :html-for="fieldId('editor-password')">
          <a-input-password :id="fieldId('editor-password')" v-model:value="formState.password" placeholder="请输入密码" />
          <div class="form-hint">密码不能为空，最多 72 字节；弱密码提交前会二次确认。</div>
        </a-form-item>
        <a-form-item label="昵称" :html-for="fieldId('editor-nickname')">
          <a-input :id="fieldId('editor-nickname')" v-model:value="formState.nickname" placeholder="可选" />
        </a-form-item>
        <a-form-item label="角色" :html-for="fieldId('editor-role')">
          <a-select :id="fieldId('editor-role')" v-model:value="formState.role" :options="roleOptions" />
        </a-form-item>
        <a-form-item label="状态" :html-for="fieldId('editor-status')">
          <a-select :id="fieldId('editor-status')" v-model:value="formState.status" :options="statusOptions" />
        </a-form-item>
        <a-form-item v-if="isEditing" label="当前余额" :html-for="fieldId('editor-current-balance')">
          <a-input :id="fieldId('editor-current-balance')" :value="formatBalance(formState.balance)" readonly prefix="¥" />
          <a-button class="balance-adjust-button" @click="openBalanceModal">修改余额</a-button>
        </a-form-item>
        <a-form-item v-if="isEditing" label="手机号" :html-for="fieldId('editor-phone')">
          <MainlandPhoneInput v-model:value="formState.phone" :id="fieldId('editor-phone')" />
          <div class="form-hint">输入新手机号可直接绑定；留空则清空当前绑定。</div>
        </a-form-item>
        <a-form-item v-if="isEditing" label="重指派同号" :html-for="fieldId('editor-force-reassign-phone')">
          <a-switch :id="fieldId('editor-force-reassign-phone')" v-model:checked="formState.forceReassignPhone" />
          <div class="form-hint">若该手机号已绑定其他用户，开启后会把旧绑定转移到当前用户。</div>
        </a-form-item>
        <a-form-item v-if="isEditing" label="微信 ID" :html-for="fieldId('editor-wechat-id')">
          <a-input :id="fieldId('editor-wechat-id')" v-model:value="formState.wechatId" placeholder="留空表示清空微信绑定" />
          <a-space wrap class="wechat-id-actions">
            <a-button size="small" :loading="wechatScanLoading" @click="startWechatScan">扫码获取 ID</a-button>
            <a-button size="small" @click="toggleWechatCodeInput">验证码获取 ID</a-button>
          </a-space>
          <section v-if="wechatFailure" data-user-wechat-feedback class="page-alert">
            <RequestFailureAlert :key="wechatFailureContextKey" :failure="wechatFailure" :message="wechatFailureMessage" :busy="wechatScanLoading || wechatCodeLoading" :context-key="wechatFailureContextKey" :is-current="isCurrentWechatFailure" />
          </section>
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
        <a-form-item v-if="isEditing" label="重指派微信" :html-for="fieldId('editor-force-reassign-wechat')">
          <a-switch :id="fieldId('editor-force-reassign-wechat')" v-model:checked="formState.forceReassignWechat" />
          <div class="form-hint">若该微信已绑定其他用户，开启后会把旧绑定转移到当前用户。</div>
        </a-form-item>
        <a-form-item v-if="!isEditing" label="启用赠送" :html-for="fieldId('editor-gift-enabled')">
          <a-switch :id="fieldId('editor-gift-enabled')" v-model:checked="formState.giftEnabled" />
          <div class="form-hint">默认开启；关闭后，本次创建的新用户不会获得赠送金额。</div>
        </a-form-item>
        <a-form-item v-if="!isEditing && formState.giftEnabled" label="赠送金额" :html-for="fieldId('editor-gift-amount')">
          <a-input-number
            :id="fieldId('editor-gift-amount')"
            v-model:value="formState.giftAmount"
            :min="0"
            :precision="6"
            style="width: 220px"
          />
          <div class="form-hint">默认使用系统管理中的新用户赠送金额，可按本次创建单独修改。金额最多支持 6 位小数。</div>
        </a-form-item>
        <a-form-item label="创建来源" :html-for="fieldId('editor-register-source')">
          <a-input :id="fieldId('editor-register-source')" :value="registerSourceText(formState.registerSource)" disabled />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="balanceVisible"
      :wrap-props="{ 'aria-modal': true }"
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
      <section v-if="balanceFailure" data-user-balance-feedback class="page-alert">
        <RequestFailureAlert
          :key="balanceFailureContextKey"
          :failure="balanceFailure"
          :message="balanceFailureMessage"
          :busy="balanceSubmitting || balanceReconciling"
          :context-key="balanceFailureContextKey"
          :is-current="isCurrentBalanceFailure"
        />
        <p class="form-hint">刷新只读取当前筛选和分页下的账户信息，不会重新提交余额调整或清空草稿；当前余额相同也不能证明本次调整已入账，请核对账单明细或操作日志。</p>
        <a-button html-type="button" data-action="reconcile-user-balance" :loading="balanceReconciling" :disabled="balanceSubmitting" @click="reconcileUserBalance">刷新账户核对</a-button>
        <p v-if="balanceReconciliation" data-user-balance-reconciliation class="form-hint" role="status">{{ balanceReconciliation }}</p>
        <RequestFailureAlert
          v-if="balanceReconcileFailure"
          :key="'read:' + balanceFailureContextKey"
          :failure="balanceReconcileFailure"
          message="账户信息读取未完成；余额调整结果仍须核对，未重新提交操作。"
          :busy="balanceReconciling"
          :context-key="balanceFailureContextKey"
          :is-current="isCurrentBalanceReconcileFailure"
        />
      </section>
      <a-form layout="vertical" autocomplete="off">
        <a-form-item label="目标用户" :html-for="fieldId('balance-target-user')">
          <a-input :id="fieldId('balance-target-user')" :value="`${formState.username}（ID: ${formState.id}）`" readonly />
        </a-form-item>
        <a-form-item label="当前余额" :html-for="fieldId('balance-current-balance')">
          <a-input :id="fieldId('balance-current-balance')" :value="formatBalance(formState.balance)" readonly prefix="¥" />
          <div class="form-hint">可退本金（充值余额）。赠送金额单独记账，可消费但不可退款。</div>
        </a-form-item>
        <a-form-item label="钱包类型" required :html-for="fieldId('balance-bucket')">
          <a-select :id="fieldId('balance-bucket')" v-model:value="balanceForm.bucket" :options="balanceBucketOptions" :disabled="balanceSubmitting" />
          <div class="form-hint">{{ balanceForm.bucket === 'gift' ? '赠送金额：用户可消费，不可退款，最低为 0。' : '充值本金：可退款，允许为负数（欠款）。' }}</div>
        </a-form-item>
        <a-form-item label="修改模式" required :html-for="fieldId('balance-mode')">
          <a-select :id="fieldId('balance-mode')" v-model:value="balanceForm.mode" :options="balanceModeOptions" :disabled="balanceSubmitting" />
        </a-form-item>
        <a-form-item :label="balanceForm.mode === 'override' ? '覆盖后的金额' : '调整金额'" required :html-for="fieldId('balance-amount')">
          <a-input-number
            :id="fieldId('balance-amount')"
            v-model:value="balanceForm.amount"
            :min="balanceForm.mode === 'override' && balanceForm.bucket === 'gift' ? 0 : (balanceForm.mode === 'override' ? undefined : 0)"
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
      :wrap-props="{ 'aria-modal': true }"
      title="重置用户密码"
      @ok="submitResetPassword"
      :afterClose="afterPasswordClose"
      ok-text="确定"
      cancel-text="取消"
      :confirm-loading="resetPasswordLoading"
    >
      <section v-if="hasUnsubmittedSavedPassword" data-user-password-saved-draft class="page-alert" role="status">
        <a-alert type="info" show-icon message="已提交的新密码已重置，当前修改尚未提交。" />
      </section>
      <section v-if="passwordFailure" data-user-password-feedback class="page-alert">
        <RequestFailureAlert :key="passwordFailureContextKey" :failure="passwordFailure" message="重置密码请求未完成，请核对当前结果后再操作。" :busy="resetPasswordLoading" :context-key="passwordFailureContextKey" :is-current="isCurrentPasswordFailure" />
      </section>
      <a-form :model="resetPasswordForm" :label-col="labelCol" :wrapper-col="wrapperCol" layout="horizontal">
        <a-form-item label="目标用户" :html-for="fieldId('reset-target-user')">
          <a-input :id="fieldId('reset-target-user')" :value="resetPasswordForm.username || '-'" disabled />
        </a-form-item>
        <a-form-item label="新密码" required :html-for="fieldId('reset-password')">
          <a-input-password :id="fieldId('reset-password')" v-model:value="resetPasswordForm.password" placeholder="请输入新密码" />
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
import { buildAdminCreateUserPayload, buildRegisterSourceLabel, buildResetPasswordPayload, buildUserProfileUpdatePayload, buildUserBalanceAdjustmentPayload, formatBillingRecordTime, limitUsernameInput, runAdminPasswordAction } from '@/utils/userPortalHelpers.mjs';
import { formatMoneyValue } from '@/utils/moneyFormat.mjs';
import { ensurePhoneBeforeSMSCaptcha, normalizePhonePaste } from '@/utils/captchaHelpers.mjs';
import MainlandPhoneInput from '@/components/MainlandPhoneInput.vue';
import TableColumnSettings from '@/components/TableColumnSettings.vue';
import RequestFailureAlert from '@/components/RequestFailureAlert.vue';
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs';
import { useTableColumnPreferences } from '@/composables/useTableColumnPreferences.mjs';
import { useScopedSessionPreferences } from '@/composables/useScopedSessionPreferences.mjs';
import { useCurrentUserDisplay, isAdminUser, getCurrentUserLogoutEpoch } from '@/utils/currentUser';
import { normalizeRestrictedName, USERNAME_MAX_LENGTH } from '@/utils/restrictedName.mjs';
import { currentUserIdentity, useCurrentUserRequestScope } from '@/utils/currentUserScope';
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs';
import { buildSearchParams, countActiveFilters, normalizeSearchKeyword } from '@/utils/listSearchHelpers.mjs';
import {
  collectLocationKeys,
  lookupLocationAddresses,
  normalizeLocationKey,
  formatValueWithLocation as formatValueWithLocationBase,
} from '@/utils/locationHelpers.mjs';
import { message } from 'ant-design-vue';
import Swal from 'sweetalert2';
import { computed, defineComponent, getCurrentInstance, markRaw, nextTick, reactive, ref, shallowRef, useId, watch } from 'vue';

// Applied keywords may contain private identifiers: bounded tab-memory only, never storage.
const userListKeywordSessions = new Map()
let userListKeywordEpoch = getCurrentUserLogoutEpoch()
const maxKeywordSessions = 8
function emptyUserForm() {
  return { id: undefined, username: '', password: '', nickname: '', perms: '', role: 'user', status: 'enabled', balance: '', registerSource: 'admin', phone: '', phoneBoundAt: '', forceReassignPhone: false, wechatId: '', wechatBoundAt: '', forceReassignWechat: false, giftEnabled: true, giftAmount: '' }
}

// Only fixed, exact backend rejection pairs are public copy; never echo arbitrary
// response/Error messages. Keep the original object for transport diagnostics.
function balanceAdjustmentFailureMessage(failure) {
  try {
    if (failure?.code === 'UNKNOWN_OUTCOME') return '网络中断，操作结果未确认。请先刷新核对，勿重复提交。'
    const status = failure?.response?.status ?? failure?.status
    if (status === 202) return '余额调整状态待核对，请刷新账户信息确认，勿重复提交'
    if (status === 200) return '未收到有效的余额调整回执，操作结果尚未确认。请先核对，勿重复提交。'
    const detail = failure?.response?.data?.message ?? failure?.data?.message ?? failure?.message
    const publicReasons = {
      400: ['用户参数错误', '请输入余额调整金额', '幂等键无效', '余额调整参数无效：余额调整模式错误', '余额调整参数无效：金额必须为最多 6 位小数的有效余额', '余额调整参数无效：添加或减少金额不能为负数', '余额调整参数无效：余额或变动金额超出存储范围'],
      403: ['仅管理员可修改余额或账单流水可见性'],
      404: ['用户或账单不存在'],
      409: ['幂等键已用于不同请求', '余额已发生变化，请刷新后重试'],
      500: ['修改失败，未保存任何变更，请稍后重试'],
    }
    return publicReasons[status]?.find(reason => reason === detail) || '余额调整请求未完成，请先核对结果，勿重复提交。'
  } catch { return '余额调整请求未完成，请先核对结果，勿重复提交。' }
}

function hasBalanceReadback(value) {
  return typeof value === 'number' ? Number.isFinite(value)
    : typeof value === 'string' && /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)$/.test(value.trim()) && Number.isFinite(Number(value))
}

function createUserListState() {
  const usernameError = ref('')
  const usernameInput = ref(null)
  let usernameErrorSeq = 0
  function clearUsernameError() { usernameError.value = ''; usernameErrorSeq += 1 }
  function showUsernameError(error, editorOwner) {
    const detail = error?.message || '请检查用户名'
    const seq = ++usernameErrorSeq
    const logout = getCurrentUserLogoutEpoch()
    const username = formState.value.username
    const control = usernameInput.value
    const active = document.activeElement
    usernameError.value = detail
    nextTick(() => {
      if (!editorOwner.isAlive() || logout !== getCurrentUserLogoutEpoch() || !visible.value || isEditing.value
        || seq !== usernameErrorSeq || usernameError.value !== detail || usernameInput.value !== control
        || formState.value.username !== username || document.activeElement !== active) return
      control?.focus?.({ preventScroll: true })
    })
  }
  const savedEditSnapshot = ref(null)
  const hasUnsubmittedSavedEdit = computed(() => visible.value && isEditing.value && savedEditSnapshot.value !== null
    && JSON.stringify(buildUserProfileUpdatePayload(formState.value)) !== savedEditSnapshot.value)
  function clearSavedEditSnapshot() { savedEditSnapshot.value = null }
  const editFailure = shallowRef(null)
  const editFailureAction = ref('edit')
  const editFailureContextKey = ref(0)
  let editDraftLease = {}
  let createDraftLease = {}
  let editFailureOwner = () => false
  function clearEditFailure() {
    editFailure.value = null
    editFailureAction.value = 'edit'
    editFailureOwner = () => false
    editFailureContextKey.value += 1
  }
  function retireEditDraft() { editDraftLease = {}; clearEditFailure() }
  function captureEditDraftOwner() {
    const editor = owner.capture('editor'), draft = editDraftLease
    return () => editor.isAlive() && draft === editDraftLease && visible.value && isEditing.value
  }
  function captureCreateDraftOwner() {
    const editor = owner.capture('editor'), draft = createDraftLease
    return () => editor.isAlive() && draft === createDraftLease && visible.value && !isEditing.value
  }
  function showEditFailure(failure, owns, action = 'edit') {
    if (!owns()) return
    editFailureAction.value = action
    editFailureOwner = owns
    editFailure.value = ensureRequestFailure(failure)
  }
  function isCurrentEditFailure(failure, context) {
    return editFailureOwner() && visible.value && (editFailureAction.value === 'edit' ? isEditing.value : !isEditing.value) && Boolean(failure)
      && failure === editFailure.value && context === editFailureContextKey.value
  }
  const passwordFailure = shallowRef(null)
  const passwordFailureContextKey = ref(0)
  let passwordDraftLease = {}
  let passwordFailureOwner = () => false
  function clearPasswordFailure() {
    passwordFailure.value = null
    passwordFailureOwner = () => false
    passwordFailureContextKey.value += 1
  }
  function retirePasswordDraft() { passwordDraftLease = {}; clearPasswordFailure() }
  function capturePasswordDraftOwner() {
    const password = owner.capture('password'), draft = passwordDraftLease
    return () => password.isAlive() && draft === passwordDraftLease && resetPasswordVisible.value
  }
  function showPasswordFailure(failure, owns) {
    if (!owns()) return
    passwordFailureOwner = owns
    passwordFailure.value = ensureRequestFailure(failure)
  }
  function isCurrentPasswordFailure(failure, context) {
    return passwordFailureOwner() && resetPasswordVisible.value && Boolean(failure)
      && failure === passwordFailure.value && context === passwordFailureContextKey.value
  }
  // Keep the submitted raw draft only in a private equality closure, never a serialized snapshot.
  const savedPasswordMatcher = shallowRef(null)
  const hasUnsubmittedSavedPassword = computed(() => resetPasswordVisible.value && savedPasswordMatcher.value !== null
    && !savedPasswordMatcher.value())
  function capturePasswordSnapshot() {
    const rawPassword = resetPasswordForm.value.password
    return targetId => resetPasswordForm.value.id === targetId && resetPasswordForm.value.password === rawPassword
  }
  function rememberSavedPasswordSnapshot(matcher) { savedPasswordMatcher.value = matcher }
  function clearSavedPasswordSnapshot() { savedPasswordMatcher.value = null }
  let alive = true
  let wechatLease = {}
  let editorLease = {}, balanceLease = {}, passwordLease = {}
  let balanceDraftLease = {}, balanceFailureOwner = () => false
  let capturePrincipal = () => () => true
  let allowedPrincipal = () => true
  let listRequestVersion = 0
  let passwordPopup = null
  const ownedTimers = new Set()
  const owner = {
    isAlive: () => alive && allowedPrincipal(),
    bindPrincipal(capture, allowed) { capturePrincipal = capture; allowedPrincipal = allowed },
    capture(dialog) {
      const ownsPrincipal = capturePrincipal()
      const editor = editorLease, balance = balanceLease, password = passwordLease
      const isAlive = () => alive && allowedPrincipal() && ownsPrincipal()
        && (!['editor', 'balance'].includes(dialog) || editor === editorLease)
        && (dialog !== 'balance' || balance === balanceLease)
        && (dialog !== 'password' || password === passwordLease)
      return { ...owner, isAlive, isWechatLeaseCurrent: lease => isAlive() && owner.isWechatLeaseCurrent(lease) }
    },
    captureListRequest() {
      const isAlive = owner.capture().isAlive
      const version = ++listRequestVersion
      return () => isAlive() && version === listRequestVersion
    },
    trackPasswordPopup(popup, dialog) {
      const record = { popup, dialog }
      passwordPopup = record
      return () => { if (passwordPopup === record) passwordPopup = null }
    },
    invalidateDialog(dialog) {
      if (dialog === 'editor') { editorLease = {}; balanceLease = {}; clearUsernameError(); retireEditDraft(); createDraftLease = {}; clearSavedEditSnapshot(); owner.invalidateWechatFlow() }
      if (dialog === 'balance') balanceLease = {}
      if (dialog === 'password') { passwordLease = {}; retirePasswordDraft(); clearSavedPasswordSnapshot() }
      if (passwordPopup?.dialog === dialog) {
        if (passwordPopup.popup && Swal.getPopup() === passwordPopup.popup) Swal.close()
        passwordPopup = null
      }
    },
    retire() {
      listRequestVersion += 1
      owner.invalidateDialog('editor')
      owner.invalidateDialog('password')
    },
    captureWechatLease: () => wechatLease,
    isWechatLeaseCurrent: lease => alive && lease === wechatLease,
    clearTimer() {
      if (wechatScanTimer !== null) {
        clearInterval(wechatScanTimer)
        ownedTimers.delete(wechatScanTimer)
        wechatScanTimer = null
      }
    },
    invalidateWechatFlow() {
      if (!alive) return
      wechatLease = {}
      ownedTimers.forEach(timer => clearInterval(timer))
      ownedTimers.clear()
      wechatScanTimer = null
    },
    schedulePoll(callback, lease) {
      if (!alive || lease !== wechatLease) return
      const timer = setInterval(() => {
        if (alive && lease === wechatLease) callback()
      }, 2500)
      ownedTimers.add(timer)
      wechatScanTimer = timer
    },
    dispose() {
      if (!alive) return
      owner.retire()
      alive = false
    },
  }
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
async function enrichColumns(rows, isCurrent = owner.capture().isAlive) {
  if (!isCurrent()) return
  const phones = collectLocationKeys(rows, 'phone')
  const ips = collectLocationKeys(rows, ['lastLoginIp', 'registerIp'])

  const tasks = []
  if (phones.length) {
    tasks.push(
      lookupLocationAddresses('phone', phones, getPhoneLocations)
        .then((addresses) => { if (isCurrent()) mergeLocations(phoneLocations, addresses) }),
    )
  }
  if (ips.length) {
    tasks.push(
      lookupLocationAddresses('ip', ips, getIpLocations)
        .then((addresses) => { if (isCurrent()) mergeLocations(ipLocations, addresses) }),
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
const balanceForm = reactive({ mode: 'increase', amount: null, bucket: 'principal', showInStatement: true })
const balanceFailure = shallowRef(null)
const balanceFailureContextKey = ref(0)
const balanceFailureLogoutEpoch = ref(null)
const balanceFailureMessage = computed(() => balanceAdjustmentFailureMessage(balanceFailure.value))
const balanceReconciling = ref(false)
const balanceReconcileFailure = shallowRef(null)
const balanceReconciliation = ref('')
function clearBalanceFailure() {
  balanceFailure.value = null
  balanceFailureLogoutEpoch.value = null
  balanceFailureOwner = () => false
  balanceFailureContextKey.value += 1
  balanceReconciling.value = false
  balanceReconcileFailure.value = null
  balanceReconciliation.value = ''
}
function retireBalanceDraft() {
  balanceDraftLease = {}
  clearBalanceFailure()
}
function captureBalanceDialogOwner() {
  const owns = owner.capture('balance').isAlive
  const target = formState.value.id, logout = getCurrentUserLogoutEpoch()
  return () => owns() && logout === getCurrentUserLogoutEpoch() && visible.value && balanceVisible.value
    && isEditing.value && target === formState.value.id
}
function captureBalanceDraftOwner() {
  const owns = captureBalanceDialogOwner(), draft = balanceDraftLease
  return () => owns() && draft === balanceDraftLease
}
function showBalanceFailure(failure, owns) {
  if (!owns()) return
  balanceFailureOwner = owns
  balanceFailureLogoutEpoch.value = getCurrentUserLogoutEpoch()
  balanceFailure.value = ensureRequestFailure(failure)
}
function isCurrentBalanceFailure(failure, context) {
  return balanceFailureOwner() && getCurrentUserLogoutEpoch() === balanceFailureLogoutEpoch.value
    && Boolean(failure) && failure === balanceFailure.value && context === balanceFailureContextKey.value
}
function isCurrentBalanceReconcileFailure(failure, context) {
  return isCurrentBalanceFailure(balanceFailure.value, context) && Boolean(failure) && failure === balanceReconcileFailure.value
}
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
const wechatFailure = shallowRef(null)
const wechatFailureContextKey = ref(0)
const wechatFailureKind = ref('qr')
let wechatFailureOwner = () => false
const wechatFailureMessage = computed(() => wechatFailureKind.value === 'code'
  ? '获取微信 ID 请求未完成，请核对验证码后再操作。'
  : '二维码创建请求未完成，请核对后再操作。')
function clearWechatFailure() {
  wechatFailure.value = null
  wechatFailureOwner = () => false
  wechatFailureContextKey.value += 1
}
function captureWechatFailureOwner(capturedOwner, lease) {
  const context = wechatFailureContextKey.value
  return () => capturedOwner.isWechatLeaseCurrent(lease) && context === wechatFailureContextKey.value
}
function showWechatFailure(failure, owns, kind) {
  if (!owns()) return
  wechatFailureOwner = owns
  wechatFailureKind.value = kind
  wechatFailure.value = ensureRequestFailure(failure)
}
function isCurrentWechatFailure(failure, context) {
  return wechatFailureOwner() && Boolean(failure)
    && failure === wechatFailure.value && context === wechatFailureContextKey.value
}
watch(wechatCode, clearWechatFailure, { flush: 'sync' })
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
const actionContextKey = ref(0)
// Keep the original transport object: its validated diagnostic ID lives in a WeakMap.
const listFailure = shallowRef(null)
const listFailureContextKey = ref(0)
function clearListFailure() {
  listFailure.value = null
  listFailureContextKey.value += 1
}
const deleteFailure = shallowRef(null)
const deleteFailureContextKey = ref(0)
let deleteFailureOwner = () => false
function clearDeleteFailure() {
  deleteFailure.value = null
  deleteFailureOwner = () => false
  deleteFailureContextKey.value += 1
}
function showDeleteFailure(failure, owns) {
  if (!owns()) return
  deleteFailureOwner = owns
  deleteFailure.value = ensureRequestFailure(failure)
}
function isCurrentDeleteFailure(failure, context) {
  return deleteFailureOwner() && Boolean(failure)
    && failure === deleteFailure.value && context === deleteFailureContextKey.value
}
watch(() => [filters.keyword, filters.role, filters.status, pagination.current, pagination.pageSize], () => {
  clearListFailure()
  clearDeleteFailure()
}, { flush: 'sync' })

function clearLocations() {
  Object.keys(phoneLocations).forEach(key => { delete phoneLocations[key] })
  Object.keys(ipLocations).forEach(key => { delete ipLocations[key] })
}
function clearPrincipalState() {
  owner.retire()
  retireBalanceDraft()
  clearListFailure()
  clearDeleteFailure()
  actionContextKey.value += 1
  dataSource.value = []
  pagination.total = 0
  pagination.current = 1
  tabLoading.value = false
  clearLocations()
  visible.value = balanceVisible.value = resetPasswordVisible.value = false
  submitLoading.value = balanceSubmitting.value = resetPasswordLoading.value = passwordConfirmationPending.value = false
  formState.value = emptyUserForm()
  resetPasswordForm.value = { id: undefined, username: '', password: '' }
  Object.assign(balanceForm, { mode: 'increase', amount: null, bucket: 'principal', showInStatement: true })
  systemNewUserBonusAmount.value = 0
  isEditing.value = false
  filters.keyword = ''; keywordInput.value = ''
  resetWechatResolveState()
}

const userTableColumnWidths = Object.freeze({
  id: 88,
  username: 170,
  nickname: 170,
  role: 104,
  status: 104,
  balance: 132,
  phone: 262,
  wechat: 104,
  phoneBoundAt: 112,
  wechatBoundAt: 112,
  registerSource: 124,
  createdAt: 112,
  lastLoginAt: 112,
  lastLoginIp: 262,
  registerIp: 262,
  operation: 190,
})
const userTableScrollX = Object.values(userTableColumnWidths).reduce((sum, width) => sum + width, 0)

function stopWechatScanTimer() {
  owner.clearTimer()
}

function resetWechatResolveState() {
  stopWechatScanTimer()
  clearWechatFailure()
  wechatScanVisible.value = false
  wechatScanLoading.value = false
  wechatScanQrCodeImageData.value = ''
  wechatScanLoginToken.value = ''
  wechatScanStatusText.value = ''
  wechatCodeVisible.value = false
  wechatCode.value = ''
  wechatCodeLoading.value = false
}

  watch(() => JSON.stringify(buildUserProfileUpdatePayload(formState.value)), () => { if (isEditing.value) retireEditDraft() }, { flush: 'sync' })
  // Raw create inputs only retire an opaque ticket; never validate or serialize passwords here.
  watch(() => [formState.value.username, formState.value.password, formState.value.nickname, formState.value.role, formState.value.status, formState.value.registerSource, formState.value.giftEnabled, formState.value.giftAmount], () => {
    createDraftLease = {}
    if (!isEditing.value) clearEditFailure()
  }, { flush: 'sync' })
  // Raw target/password edits retire only this feedback ticket, never serialize the password.
  watch(() => [resetPasswordForm.value.id, resetPasswordForm.value.password], retirePasswordDraft, { flush: 'sync' })
  watch(() => formState.value.username, () => {
    if (!usernameError.value) return
    usernameErrorSeq += 1
    if (isEditing.value) { clearUsernameError(); return }
    try {
      normalizeRestrictedName(formState.value.username, USERNAME_MAX_LENGTH, '用户名')
      clearUsernameError()
    } catch (error) {
      usernameError.value = error?.message || '请检查用户名'
    }
  }, { flush: 'sync' })
  watch(visible, next => {
    if (!next) { owner.invalidateDialog('editor'); retireBalanceDraft(); submitLoading.value = passwordConfirmationPending.value = false; resetWechatResolveState() }
  }, { flush: 'sync' })
  watch(balanceVisible, next => {
    retireBalanceDraft()
    if (!next) { owner.invalidateDialog('balance'); balanceSubmitting.value = false }
  }, { flush: 'sync' })
  watch(resetPasswordVisible, next => {
    if (!next) { owner.invalidateDialog('password'); resetPasswordLoading.value = passwordConfirmationPending.value = false }
  }, { flush: 'sync' })
  // Draft/target retirement clears feedback, not an already-issued write's busy
  // state. Its dialog/principal owner alone releases that state in finally.
  watch(() => [balanceForm.mode, balanceForm.amount, balanceForm.bucket, balanceForm.showInStatement, formState.value.id], retireBalanceDraft, { flush: 'sync' })
  watch(() => formState.value.id, () => {
    owner.invalidateWechatFlow()
    resetWechatResolveState()
  }, { flush: 'sync' })
  return { owner, usernameError, usernameInput, clearUsernameError, showUsernameError, savedEditSnapshot, hasUnsubmittedSavedEdit, clearSavedEditSnapshot, editFailure, editFailureAction, editFailureContextKey, clearEditFailure, retireEditDraft, captureEditDraftOwner, captureCreateDraftOwner, showEditFailure, isCurrentEditFailure, passwordFailure, passwordFailureContextKey, clearPasswordFailure, capturePasswordDraftOwner, showPasswordFailure, isCurrentPasswordFailure, hasUnsubmittedSavedPassword, capturePasswordSnapshot, rememberSavedPasswordSnapshot, clearSavedPasswordSnapshot, balanceFailure, balanceFailureContextKey, balanceFailureMessage, balanceReconciling, balanceReconcileFailure, balanceReconciliation, clearBalanceFailure, retireBalanceDraft, captureBalanceDialogOwner, captureBalanceDraftOwner, showBalanceFailure, isCurrentBalanceFailure, isCurrentBalanceReconcileFailure, actionContextKey, listFailure, listFailureContextKey, clearListFailure, deleteFailure, deleteFailureContextKey, clearDeleteFailure, showDeleteFailure, isCurrentDeleteFailure, clearLocations, clearPrincipalState, dataSource, phoneLocations, ipLocations, formatValueWithLocation, mergeLocations, enrichColumns, filters, keywordInput, pagination, visible, submitLoading, passwordConfirmationPending, balanceVisible, balanceSubmitting, balanceForm, isEditing, resetPasswordVisible, resetPasswordLoading, wechatFailure, wechatFailureContextKey, wechatFailureMessage, clearWechatFailure, captureWechatFailureOwner, showWechatFailure, isCurrentWechatFailure, wechatScanVisible, wechatScanLoading, wechatScanQrCodeImageData, wechatScanLoginToken, wechatScanStatusText, wechatCodeVisible, wechatCode, wechatCodeLoading, systemNewUserBonusAmount, wechatScanTimer, formState, resetPasswordForm, tabLoading, userTableColumnWidths, userTableScrollX, stopWechatScanTimer, resetWechatResolveState }
}

export default defineComponent({
  name: 'UserList',
  components: { MainlandPhoneInput, TableColumnSettings, RequestFailureAlert },
  setup() {
    // Stable UI-only IDs keep labels unique across dialogs and sibling UserList instances.
    const fieldIdPrefix = `user-list-${useId()}`
    const fieldId = field => `${fieldIdPrefix}-${field}`
    if (userListKeywordEpoch !== getCurrentUserLogoutEpoch()) {
      userListKeywordSessions.clear()
      userListKeywordEpoch = getCurrentUserLogoutEpoch()
    }
    const userListContext = markRaw(createUserListState())
    const { dataSource, pagination, isEditing, formState, resetPasswordForm, visible, resetPasswordVisible, phoneLocations, ipLocations, formatValueWithLocation, tabLoading, filters, keywordInput, submitLoading, balanceVisible, balanceSubmitting, balanceForm, resetPasswordLoading, wechatScanVisible, wechatScanLoading, wechatScanQrCodeImageData, wechatScanLoginToken, wechatScanStatusText, wechatCodeVisible, wechatCode, wechatCodeLoading, systemNewUserBonusAmount, userTableColumnWidths } = userListContext

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
    const allColumns = computed(() => [
        {
          title: 'ID',
          dataIndex: 'id',
          key: 'id',
          width: userTableColumnWidths.id,
        },
        {
          title: '用户名',
          dataIndex: 'username',
          key: 'username',
          width: userTableColumnWidths.username,
        },
        {
          title: '昵称',
          dataIndex: 'nickname',
          key: 'nickname',
          width: userTableColumnWidths.nickname,
        },
        {
          title: '角色',
          dataIndex: 'role',
          key: 'role',
          width: userTableColumnWidths.role,
        },
        {
          title: '状态',
          dataIndex: 'status',
          key: 'status',
          width: userTableColumnWidths.status,
        },
        {
          title: '余额',
          dataIndex: 'balance',
          key: 'balance',
          width: userTableColumnWidths.balance,
        },
        {
          title: '手机号',
          dataIndex: 'phone',
          key: 'phone',
          width: userTableColumnWidths.phone,
        },
        {
          title: '手机号绑定时间',
          dataIndex: 'phoneBoundAt',
          key: 'phoneBoundAt',
          width: userTableColumnWidths.phoneBoundAt,
        },
        {
          title: '微信绑定',
          dataIndex: 'wechatId',
          key: 'wechat',
          width: userTableColumnWidths.wechat,
        },
        {
          title: '微信绑定时间',
          dataIndex: 'wechatBoundAt',
          key: 'wechatBoundAt',
          width: userTableColumnWidths.wechatBoundAt,
        },
        {
          title: '注册来源',
          dataIndex: 'registerSource',
          key: 'registerSource',
          width: userTableColumnWidths.registerSource,
        },
        {
          title: '创建时间',
          dataIndex: 'createdAt',
          key: 'createdAt',
          width: userTableColumnWidths.createdAt,
        },
        {
          title: '上次登录时间',
          dataIndex: 'lastLoginAt',
          key: 'lastLoginAt',
          width: userTableColumnWidths.lastLoginAt,
        },
        {
          title: '上次登录 IP',
          dataIndex: 'lastLoginIp',
          key: 'lastLoginIp',
          width: userTableColumnWidths.lastLoginIp,
        },
        {
          title: '注册 IP',
          dataIndex: 'registerIp',
          key: 'registerIp',
          width: userTableColumnWidths.registerIp,
        },
        {
          title: '操作',
          dataIndex: 'operation',
          key: 'operation',
          fixed: 'right',
          width: userTableColumnWidths.operation,
        },
    ])
    const { currentUser } = useCurrentUserDisplay()
    const instance = getCurrentInstance()
    const canManageUsers = computed(() => Boolean(currentUserIdentity(currentUser.value)) && isAdminUser(currentUser.value))
    const principalScope = computed(() => {
      const identity = currentUserIdentity(currentUser.value)
      return identity ? `${identity}:${isAdminUser(currentUser.value) ? 'admin' : 'user'}` : ''
    })
    let keywordScope = principalScope.value
    let retiringKeyword = false
    const { captureDataOwner, canLoadForCurrentUser } = useCurrentUserRequestScope(currentUser, {
      clear() {
        retiringKeyword = true
        try { userListContext.clearPrincipalState() } finally { retiringKeyword = false }
      },
      reset() { pagination.current = 1 },
      reload() {
        const ownsPrincipal = captureDataOwner()
        // Scope preferences restore synchronously after owner retirement. Read once,
        // after that restoration, and never submit anything on a principal transition.
        nextTick(() => {
          if (!ownsPrincipal() || !canManageUsers.value) return
          instance.proxy.handleUserList()
          instance.proxy.loadNewUserBonusDefault()
        })
      },
    })
    userListContext.owner.bindPrincipal(captureDataOwner, () => canLoadForCurrentUser() && canManageUsers.value)
    let observedBalanceLogoutEpoch = getCurrentUserLogoutEpoch()
    watch(() => [currentUser.value, getCurrentUserLogoutEpoch()], () => {
      const logout = getCurrentUserLogoutEpoch()
      if (logout !== observedBalanceLogoutEpoch) { userListContext.retireBalanceDraft(); userListContext.clearUsernameError() }
      observedBalanceLogoutEpoch = logout
    }, { flush: 'sync' })
    watch(() => instance.proxy.$route?.fullPath, () => userListContext.retireBalanceDraft(), { flush: 'sync' })
    const reconcileUserBalance = computed(() => {
      const failure = userListContext.balanceFailure.value, context = userListContext.balanceFailureContextKey.value
      const owns = userListContext.captureBalanceDraftOwner()
      const isCurrent = () => owns() && userListContext.isCurrentBalanceFailure(failure, context)
      return async () => {
        if (!isCurrent() || balanceSubmitting.value || userListContext.balanceReconciling.value) return
        const target = formState.value.id
        const currentParams = () => buildSearchParams({ keyword: filters.keyword, role: filters.role, status: filters.status }, pagination.current, pagination.pageSize)
        const params = currentParams(), query = JSON.stringify(params)
        const queryChanged = () => JSON.stringify(currentParams()) !== query
        const changedQueryHint = '筛选或分页已变化，本次读取未用于账户核对，请重新读取。'
        userListContext.balanceReconciling.value = true
        userListContext.balanceReconcileFailure.value = null
        userListContext.balanceReconciliation.value = ''
        try {
          // A separate guarded GET: no mutation, no list latest-wins ticket or
          // pagination changes, and no interpretation as an adjustment receipt.
          const res = await getUserList(params)
          if (!isCurrent()) return
          if (queryChanged()) {
            userListContext.balanceReconciliation.value = changedQueryHint
            return
          }
          if (res?.status !== 200 || !Array.isArray(res?.data?.data)) {
            userListContext.balanceReconcileFailure.value = ensureRequestFailure(res)
            return
          }
          const row = res.data.data.find(item => item && String(item.id) === String(target))
          if (row && hasBalanceReadback(row.balance)) {
            formState.value.balance = row.balance
            userListContext.balanceReconciliation.value = `已只读刷新当前账户余额：¥ ${formatMoneyValue(row.balance)}。这不是本次调整的入账凭据，仍须核对账单明细或操作日志。`
          } else {
            userListContext.balanceReconciliation.value = '当前筛选和分页未返回目标用户的有效余额，尚未完成账户核对；不会据此确认本次调整成功。'
          }
        } catch (error) {
          if (!isCurrent()) return
          if (queryChanged()) userListContext.balanceReconciliation.value = changedQueryHint
          else userListContext.balanceReconcileFailure.value = ensureRequestFailure(error)
        } finally {
          if (isCurrent()) userListContext.balanceReconciling.value = false
        }
      }
    })
    const isCurrentListFailure = (failure, context) => userListContext.owner.isAlive()
      && Boolean(failure) && failure === userListContext.listFailure.value && context === userListContext.listFailureContextKey.value
    const retryUserList = computed(() => {
      const failure = userListContext.listFailure.value
      const context = userListContext.listFailureContextKey.value
      const owns = userListContext.owner.capture().isAlive
      return (entryFailure = failure, entryContext = context) => {
        if (owns() && entryFailure === failure && entryContext === context && isCurrentListFailure(entryFailure, entryContext)) instance.proxy.handleUserList()
      }
    })
    const columnScope = computed(() => {
      const identity = currentUser.value?.id ? 'id:' + currentUser.value.id : ''
      return identity ? `${identity}:${isAdminUser(currentUser.value) ? 'admin' : 'user'}` : ''
    })
    const priorityColumnKeys = ['id', 'username', 'nickname', 'role', 'status', 'balance', 'operation']
    const { visibleColumns, selectedKeys, columnOptions, scopeKey: columnContextKey, setSelectedKeys, resetColumns } = useTableColumnPreferences({
      pageKey: 'user-list', columns: allColumns, scope: columnScope, requiredKeys: ['id', 'username', 'operation'], priorityKeys: priorityColumnKeys,
    })
    // Only enum/number facets enter sessionStorage; applied keyword restoration
    // uses the separate bounded principal-scoped memory above.
    useScopedSessionPreferences({
      pageKey: 'user-list', scope: columnScope,
      fields: {
        role: { get: () => filters.role, set: value => { filters.role = value }, validate: value => ['user', 'admin'].includes(value) },
        status: { get: () => filters.status, set: value => { filters.status = value }, validate: value => ['enabled', 'disabled'].includes(value) },
        pageSize: { get: () => pagination.pageSize, set: value => { pagination.pageSize = value }, validate: value => [10, 20, 50, 100].includes(value) },
      },
    })
    watch(principalScope, next => {
      keywordScope = next
      if (!next) userListKeywordSessions.clear()
      const keyword = next ? userListKeywordSessions.get(next) || '' : ''
      filters.keyword = keyword; keywordInput.value = keyword; pagination.current = 1
    }, { immediate: true, flush: 'sync' })
    watch(() => filters.keyword, keyword => {
      if (retiringKeyword || !keywordScope || keywordScope !== principalScope.value) return
      userListKeywordSessions.delete(keywordScope)
      if (keyword) userListKeywordSessions.set(keywordScope, keyword)
      while (userListKeywordSessions.size > maxKeywordSessions) userListKeywordSessions.delete(userListKeywordSessions.keys().next().value)
    }, { flush: 'sync' })


    return {
      fieldId,
      usernameError: userListContext.usernameError,
      usernameInput: userListContext.usernameInput,
      hasUnsubmittedSavedEdit: userListContext.hasUnsubmittedSavedEdit,
      editFailure: userListContext.editFailure,
      editFailureAction: userListContext.editFailureAction,
      editFailureContextKey: userListContext.editFailureContextKey,
      isCurrentEditFailure: userListContext.isCurrentEditFailure,
      passwordFailure: userListContext.passwordFailure,
      passwordFailureContextKey: userListContext.passwordFailureContextKey,
      isCurrentPasswordFailure: userListContext.isCurrentPasswordFailure,
      hasUnsubmittedSavedPassword: userListContext.hasUnsubmittedSavedPassword,
      userListContext,
      balanceFailure: userListContext.balanceFailure,
      balanceFailureContextKey: userListContext.balanceFailureContextKey,
      balanceFailureMessage: userListContext.balanceFailureMessage,
      balanceReconciling: userListContext.balanceReconciling,
      balanceReconcileFailure: userListContext.balanceReconcileFailure,
      balanceReconciliation: userListContext.balanceReconciliation,
      isCurrentBalanceFailure: userListContext.isCurrentBalanceFailure,
      isCurrentBalanceReconcileFailure: userListContext.isCurrentBalanceReconcileFailure,
      reconcileUserBalance,
      actionContextKey: userListContext.actionContextKey,
      listFailure: userListContext.listFailure,
      listFailureContextKey: userListContext.listFailureContextKey,
      deleteFailure: userListContext.deleteFailure,
      deleteFailureContextKey: userListContext.deleteFailureContextKey,
      isCurrentDeleteFailure: userListContext.isCurrentDeleteFailure,
      retryUserList,
      isCurrentListFailure,
      canManageUsers,
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
      tableScrollX: computed(() => sumTableColumnWidths(visibleColumns.value)),
      filters,
      keywordInput,
      activeFilterCount: computed(() => countActiveFilters({ keyword: filters.keyword, role: filters.role, status: filters.status })),
      submitLoading,
      balanceVisible,
      balanceSubmitting,
      balanceForm,
      resetPasswordLoading,
      wechatFailure: userListContext.wechatFailure,
      wechatFailureContextKey: userListContext.wechatFailureContextKey,
      wechatFailureMessage: userListContext.wechatFailureMessage,
      isCurrentWechatFailure: userListContext.isCurrentWechatFailure,
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
      // 钱包桶：本金可退可为负，赠金不可退且下限 0（用户 2026-11-09 明确要求）。
      balanceBucketOptions: [
        { label: '充值本金（可退款）', value: 'principal' },
        { label: '赠送金额（不可退款）', value: 'gift' },
      ],
      columns: visibleColumns,
      selectedColumnKeys: selectedKeys,
      columnOptions,
      columnContextKey,
      priorityColumnKeys,
      setSelectedColumnKeys: setSelectedKeys,
      resetColumns,
    };
  },
  mounted() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

    this.handleUserList()
    this.loadNewUserBonusDefault()
  },
  beforeUnmount() {
    this.userListContext.owner.dispose()
  },
  methods: {
    formatBalance(value) {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      return formatMoneyValue(value || 0)
    },
    formatDateTime(value) {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      return formatBillingRecordTime(value)
    },
    roleText(value) {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      return String(value || '').toLowerCase() === 'admin' ? '管理员' : '普通用户'
    },
    roleColor(value) {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      return String(value || '').toLowerCase() === 'admin' ? 'gold' : 'blue'
    },
    statusText(value) {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      return String(value || '').toLowerCase() === 'disabled' ? '禁用' : '启用'
    },
    statusBadge(value) {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      return String(value || '').toLowerCase() === 'disabled' ? 'error' : 'success'
    },
    registerSourceText(value) {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      return buildRegisterSourceLabel(value)
    },
    normalizeWechatCodeInput(value) {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      return String(value || '').replace(/\D/g, '').slice(0, 12)
    },
    toggleWechatCodeInput() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      this.userListContext.clearWechatFailure()
      this.userListContext.wechatCodeVisible.value = !this.userListContext.wechatCodeVisible.value
      if (!this.userListContext.wechatCodeVisible.value) {
        this.userListContext.wechatCode.value = ''
      }
    },
    stopWechatScan() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      this.userListContext.stopWechatScanTimer()
      this.userListContext.wechatScanLoading.value = false
    },
    async startWechatScan() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return
      const wechatLease = owner.captureWechatLease()
      if (!owner.isWechatLeaseCurrent(wechatLease)) return

      this.stopWechatScan()
      this.userListContext.resetWechatResolveState()
      this.userListContext.wechatScanVisible.value = true
      this.userListContext.wechatScanLoading.value = true
      this.userListContext.wechatScanStatusText.value = '正在创建二维码'
      const ownsFailure = this.userListContext.captureWechatFailureOwner(owner, wechatLease)
      try {
        const res = await createWechatLoginQRCode({ errorFeedback: 'local' })
        if (!owner.isWechatLeaseCurrent(wechatLease)) return

        if (res?.status !== 200) {
          throw ensureRequestFailure(res)
        }
        this.userListContext.wechatScanQrCodeImageData.value = res?.data?.qrcodeImageData || ''
        this.userListContext.wechatScanLoginToken.value = res?.data?.loginToken || ''
        if (!this.userListContext.wechatScanQrCodeImageData.value || !this.userListContext.wechatScanLoginToken.value) {
          throw new Error('二维码创建失败')
        }
        this.userListContext.wechatScanStatusText.value = '请使用需要绑定的微信扫码'
        owner.schedulePoll(() => this.pollWechatScanID(), wechatLease)
        this.pollWechatScanID()
      } catch (error) {
        if (!owner.isWechatLeaseCurrent(wechatLease)) return

        if (!ownsFailure()) return
        this.userListContext.showWechatFailure(error, ownsFailure, 'qr')
        this.userListContext.wechatScanStatusText.value = '二维码创建请求未完成，请核对后再操作。'
        message.error(this.userListContext.wechatScanStatusText.value)
      } finally {
        if (owner.isWechatLeaseCurrent(wechatLease)) {


        this.userListContext.wechatScanLoading.value = false

        }
      }
    },
    async pollWechatScanID() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return
      const wechatLease = owner.captureWechatLease()
      if (!owner.isWechatLeaseCurrent(wechatLease)) return

      if (!this.userListContext.wechatScanLoginToken.value) {
        return
      }
      try {
        const res = await resolveAdminWechatIDByScan({ loginToken: this.userListContext.wechatScanLoginToken.value })
        if (!owner.isWechatLeaseCurrent(wechatLease)) return

        if (res?.status !== 200) {
          this.userListContext.wechatScanStatusText.value = '扫码状态查询未完成，请稍后核对。'
          return
        }
        const status = String(res?.data?.status || '').trim().toLowerCase()
        if (status === 'pending') {
          this.userListContext.wechatScanStatusText.value = '等待微信扫码'
          return
        }
        if (status === 'scanned') {
          this.userListContext.wechatScanStatusText.value = '已扫码，请确认'
          return
        }
        if (status === 'success' && res?.data?.wechatId) {
          this.userListContext.formState.value.wechatId = String(res.data.wechatId || '').trim()
          this.userListContext.clearWechatFailure()
          this.userListContext.wechatScanStatusText.value = '已获取微信 ID，请保存用户'
          message.success('微信 ID 已填入')
          this.stopWechatScan()
          this.userListContext.wechatScanVisible.value = false
          return
        }
        if (status === 'expired') {
          this.userListContext.wechatScanStatusText.value = '二维码已过期，请重新获取'
          this.stopWechatScan()
        }
      } catch (error) {
        if (!owner.isWechatLeaseCurrent(wechatLease)) return

        this.userListContext.wechatScanStatusText.value = '扫码状态查询未完成，请稍后核对。'
      }
    },
    async resolveWechatIDByCode() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return
      const wechatLease = owner.captureWechatLease()
      if (!owner.isWechatLeaseCurrent(wechatLease)) return

      if (!this.userListContext.wechatCode.value) {
        message.warning('请输入微信验证码')
        return
      }
      this.userListContext.clearWechatFailure()
      const ownsFailure = this.userListContext.captureWechatFailureOwner(owner, wechatLease)
      this.userListContext.wechatCodeLoading.value = true
      try {
        const res = await resolveAdminWechatIDByCode({ code: this.userListContext.wechatCode.value })
        if (!owner.isWechatLeaseCurrent(wechatLease)) return

        if (res?.status !== 200) {
          if (ownsFailure()) {
            this.userListContext.showWechatFailure(res, ownsFailure, 'code')
            message.error('获取微信 ID 请求未完成，请核对验证码后再操作。')
          }
          return
        }
        this.userListContext.formState.value.wechatId = String(res?.data?.wechatId || '').trim()
        if (!this.userListContext.formState.value.wechatId) {
          message.error('未获取到微信 ID')
          return
        }
        this.userListContext.clearWechatFailure()
        this.userListContext.wechatCodeVisible.value = false
        this.userListContext.wechatCode.value = ''
        message.success('微信 ID 已填入')
      } catch (error) {
        if (!owner.isWechatLeaseCurrent(wechatLease)) return

        if (!ownsFailure()) return
        this.userListContext.showWechatFailure(error, ownsFailure, 'code')
        message.error('获取微信 ID 请求未完成，请核对验证码后再操作。')
      } finally {
        if (owner.isWechatLeaseCurrent(wechatLease)) {


        this.userListContext.wechatCodeLoading.value = false

        }
      }
    },
    openCreateModal() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      this.resetFormState()
      this.userListContext.formState.value.giftAmount = this.userListContext.systemNewUserBonusAmount.value
      this.userListContext.isEditing.value = false
      this.userListContext.visible.value = true
    },
    openEditModal(record) {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return
      owner.invalidateDialog('editor')
      this.userListContext.submitLoading.value = false
      this.userListContext.passwordConfirmationPending.value = false
      this.userListContext.balanceVisible.value = false
      this.userListContext.balanceSubmitting.value = false

      this.userListContext.resetWechatResolveState()
      this.userListContext.isEditing.value = true
      this.userListContext.formState.value = {
        id: record.id,
        username: record.username || '',
        password: '',
        nickname: record.nickname || '',
        perms: record.perms || '',
        role: record.role || 'user',
        status: record.status || 'enabled',
        balance: record.balance ?? '',
        registerSource: record.registerSource || 'admin',
        phone: normalizePhonePaste(record.phone) || record.phone || '',
        phoneBoundAt: record.phoneBoundAt || '',
        forceReassignPhone: false,
        wechatId: record.wechatId || '',
        wechatBoundAt: record.wechatBoundAt || '',
        forceReassignWechat: false,
      }
      this.userListContext.visible.value = true
    },
    openResetPasswordModal(record) {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return
      owner.invalidateDialog('password')
      this.userListContext.resetPasswordLoading.value = false
      this.userListContext.passwordConfirmationPending.value = false

      this.userListContext.resetPasswordForm.value = {
        id: record.id,
        username: record.username || '',
        password: '',
      }
      this.userListContext.resetPasswordVisible.value = true
    },
    openBalanceModal() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return
      owner.invalidateDialog('balance')
      this.userListContext.retireBalanceDraft()
      this.userListContext.balanceSubmitting.value = false

      this.userListContext.balanceForm.mode = 'increase'
      this.userListContext.balanceForm.amount = null
      this.userListContext.balanceForm.bucket = 'principal'
      this.userListContext.balanceForm.showInStatement = true
      this.userListContext.balanceVisible.value = true
    },
    async submitBalanceAdjustment() {
      const owns = this.userListContext.captureBalanceDialogOwner()
      const ownsDraft = this.userListContext.captureBalanceDraftOwner()
      if (!owns()) return

      if (this.userListContext.balanceSubmitting.value) return
      let payload
      try {
        payload = buildUserBalanceAdjustmentPayload({ id: this.userListContext.formState.value.id, ...this.userListContext.balanceForm })
      } catch (error) {
        if (!owns()) return
        message.warning(error?.message || '请检查余额调整参数')
        return
      }
      this.userListContext.clearBalanceFailure()
      this.userListContext.balanceSubmitting.value = true
      try {
        const res = await adjustUserBalance(payload)
        if (!owns()) return

        if (res?.status === 202) {
          if (ownsDraft()) {
            this.userListContext.showBalanceFailure(res, ownsDraft)
            message.warning('余额调整状态待核对，请刷新账户信息确认，勿重复提交')
          }
          this.handleUserList()
          return
        }
        if (res?.status !== 200 || !hasBalanceReadback(res?.data?.balance)) {
          if (ownsDraft()) {
            this.userListContext.showBalanceFailure(res, ownsDraft)
            message.error(balanceAdjustmentFailureMessage(res))
          }
          return
        }
        this.userListContext.formState.value.balance = res.data.balance
        this.userListContext.balanceVisible.value = false
        message.success('余额修改成功，已记录日志与账单明细')
        this.handleUserList()
      } catch (error) {
        if (!owns() || !ownsDraft()) return
        this.userListContext.showBalanceFailure(error, ownsDraft)
        message.error(balanceAdjustmentFailureMessage(error))
      } finally {
        // A changed draft must not release this already-issued request early.
        if (owns()) this.userListContext.balanceSubmitting.value = false
      }
    },
    // 点「搜索」或回车时才把输入框内容变成生效的筛选条件。
    applyKeyword() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      this.userListContext.filters.keyword = normalizeSearchKeyword(this.userListContext.keywordInput.value)
      this.userListContext.keywordInput.value = this.userListContext.filters.keyword
      this.userListContext.pagination.current = 1
      this.handleUserList()
    },
    onFilterChange() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      this.userListContext.pagination.current = 1
      this.handleUserList()
    },
    resetFilters() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      this.userListContext.filters.keyword = ''
      this.userListContext.keywordInput.value = ''
      this.userListContext.filters.role = undefined
      this.userListContext.filters.status = undefined
      this.userListContext.pagination.current = 1
      this.handleUserList()
    },
    handleTableChange(paginationData) {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      this.userListContext.pagination.current = paginationData.current
      this.userListContext.pagination.pageSize = paginationData.pageSize
      this.handleUserList()
    },
    handleMobilePageChange(current, pageSize) {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      this.userListContext.pagination.current = current
      this.userListContext.pagination.pageSize = pageSize
      this.handleUserList()
    },
    handleUserList() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return
      const isCurrent = owner.captureListRequest()

      this.userListContext.clearListFailure()
      this.userListContext.clearDeleteFailure()
      const failureContext = this.userListContext.listFailureContextKey.value
      this.userListContext.tabLoading.value = true
      return getUserList(buildSearchParams({
        keyword: this.userListContext.filters.keyword,
        role: this.userListContext.filters.role,
        status: this.userListContext.filters.status,
      }, this.userListContext.pagination.current, this.userListContext.pagination.pageSize)).then(res => {
        if (!isCurrent()) return

        if (res?.status === 200) {
          // 接口现在返回 { data, total, page, pageSize }。
          const payload = res?.data || {}
          this.userListContext.dataSource.value = Array.isArray(payload.data) ? payload.data : []
          this.userListContext.pagination.total = Number(payload.total || 0)
          this.userListContext.pagination.current = Number(payload.page || 1)
          this.userListContext.pagination.pageSize = Number(payload.pageSize || 10)
          // 归属地单独批量拉取（失败静默）：列表接口本身不依赖它。
          this.userListContext.clearLocations()
          this.userListContext.enrichColumns(this.userListContext.dataSource.value, isCurrent)
        } else {
          if (failureContext === this.userListContext.listFailureContextKey.value) this.userListContext.listFailure.value = ensureRequestFailure(res)
        }
      }).catch(error => {
        if (!isCurrent()) return

        if (failureContext === this.userListContext.listFailureContextKey.value) this.userListContext.listFailure.value = ensureRequestFailure(error)
      }).finally(() => {
        if (!isCurrent()) return

        this.userListContext.tabLoading.value = false
      })
    },
    deleteConfirmation(val) {
      const owner = this.userListContext.owner.capture()
      return () => { if (owner.isAlive()) return this.onDelete(val, owner.isAlive) }
    },
    onDelete(val, ownsConfirmation = () => true) {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive() || !ownsConfirmation()) return
      if (!this.userListContext.dataSource.value.some(row => row.id === val)) return

      this.userListContext.clearDeleteFailure()
      const failureContext = this.userListContext.deleteFailureContextKey.value
      const ownsFailure = () => owner.isAlive() && failureContext === this.userListContext.deleteFailureContextKey.value
      delUser({ id: val }).then(res => {
        if (!owner.isAlive()) return

        if (res?.status === 200) {
          message.success('删除成功')
          this.handleUserList()
        } else if (ownsFailure()) {
          this.userListContext.showDeleteFailure(res, ownsFailure)
          message.error('删除请求未完成，请先核对结果，勿重复提交。')
        }
      }).catch(error => {
        if (!ownsFailure()) return

        this.userListContext.showDeleteFailure(error, ownsFailure)
        message.error('删除请求未完成，请先核对结果，勿重复提交。')
      })
    },
    async confirmWeakPassword(action) {
      const dialog = action === 'create' ? 'editor' : 'password'
      const owner = this.userListContext.owner.capture(dialog)
      if (!owner.isAlive()) return

      const pendingConfirmation = Swal.fire({
        title: '确认使用弱密码？',
        text: `当前密码未达到推荐强度。推荐至少 8 位，并同时包含大写字母、小写字母、数字和特殊字符。弱密码更容易被猜测、撞库或重复使用；密码仍不能超过 bcrypt 支持的 72 字节。确认继续${action === 'create' ? '创建用户' : '重置密码'}吗？`,
        icon: 'warning',
        showCancelButton: true,
        confirmButtonText: action === 'create' ? '仍然创建' : '仍然重置',
        cancelButtonText: '返回修改',
        reverseButtons: true,
      })
      const release = owner.trackPasswordPopup(Swal.getPopup(), dialog)
      try {
        const confirmation = await pendingConfirmation
        return owner.isAlive() && confirmation?.isConfirmed === true
      } finally { release() }
    },
    async onSubmit() {
      const owner = this.userListContext.owner.capture('editor')
      if (!owner.isAlive()) return

      if (this.userListContext.submitLoading.value || this.userListContext.passwordConfirmationPending.value) return
      let createPayload = null
      if (!this.userListContext.isEditing.value) {
        try {
          createPayload = buildAdminCreateUserPayload(this.userListContext.formState.value)
        } catch (error) {
        if (!owner.isAlive()) return

          try {
            normalizeRestrictedName(this.userListContext.formState.value.username, USERNAME_MAX_LENGTH, '用户名')
            this.userListContext.clearUsernameError()
          } catch (usernameError) {
            this.userListContext.showUsernameError(usernameError, owner)
          }
          message.warning(error?.message || '请完善新增用户必填项')
          return
        }
      }

      let editPayload = null
      if (this.userListContext.isEditing.value) {
        try {
          if (String(this.userListContext.formState.value.phone || '').trim()) {
            this.userListContext.formState.value.phone = ensurePhoneBeforeSMSCaptcha(this.userListContext.formState.value.phone)
          }
          editPayload = buildUserProfileUpdatePayload(this.userListContext.formState.value)
        } catch (error) {
        if (!owner.isAlive()) return

          message.warning(error?.message || '请检查用户资料')
          return
        }
      }

      const submittedEditSnapshot = editPayload ? JSON.stringify(editPayload) : null
      const ownsEditDraft = this.userListContext.isEditing.value ? this.userListContext.captureEditDraftOwner() : null
      const ownsCreateDraft = createPayload ? this.userListContext.captureCreateDraftOwner() : null
      const submit = async () => {
        if (!owner.isAlive()) return

        if (ownsEditDraft) { this.userListContext.clearEditFailure(); this.userListContext.clearSavedEditSnapshot() }
        else if (ownsCreateDraft) this.userListContext.clearEditFailure()
        this.userListContext.submitLoading.value = true
        try {
          const res = await (this.userListContext.isEditing.value ? updateUser(editPayload) : addUser(createPayload))
        if (!owner.isAlive()) return

          if (res?.status !== 200) {
            if (ownsEditDraft) this.userListContext.showEditFailure(res, ownsEditDraft)
            else if (ownsCreateDraft) this.userListContext.showEditFailure(res, ownsCreateDraft, 'create')
            message.error(this.userListContext.isEditing.value ? '更新用户请求未完成，请核对当前资料后再操作。' : '创建用户请求未完成，请核对当前资料后再操作。')
            return
          }
          message.success(this.userListContext.isEditing.value ? '更新用户成功' : '添加用户成功')
          this.handleUserList()
          if (submittedEditSnapshot !== null && JSON.stringify(buildUserProfileUpdatePayload(this.userListContext.formState.value)) !== submittedEditSnapshot) {
            this.userListContext.savedEditSnapshot.value = submittedEditSnapshot
          } else {
            this.resetFormState()
            this.userListContext.visible.value = false
          }
        } catch (error) {
        if (!owner.isAlive()) return

          if (ownsEditDraft) this.userListContext.showEditFailure(error, ownsEditDraft)
          else if (ownsCreateDraft) this.userListContext.showEditFailure(error, ownsCreateDraft, 'create')
          message.error(this.userListContext.isEditing.value ? '更新用户请求未完成，请核对当前资料后再操作。' : '创建用户请求未完成，请核对当前资料后再操作。')
        } finally {
        if (owner.isAlive()) {


          this.userListContext.submitLoading.value = false

        }
      }
      }

      if (createPayload) {
        this.userListContext.passwordConfirmationPending.value = true
        try {
          await runAdminPasswordAction(createPayload.password, submit, () => { if (!owner.isAlive()) return; return this.confirmWeakPassword('create', createPayload.password); })
        if (!owner.isAlive()) return

        } catch (error) {
        if (!owner.isAlive()) return

          message.error(error?.message || '创建用户失败')
        } finally {
        if (owner.isAlive()) {


          this.userListContext.passwordConfirmationPending.value = false

        }
      }
        return
      }
      await submit()
        if (!owner.isAlive()) return

    },
    async submitResetPassword() {
      const owner = this.userListContext.owner.capture('password')
      if (!owner.isAlive()) return

      if (this.userListContext.resetPasswordLoading.value || this.userListContext.passwordConfirmationPending.value) return
      let payload
      try {
        payload = buildResetPasswordPayload(this.userListContext.resetPasswordForm.value.password)
      } catch (error) {
        if (!owner.isAlive()) return

        message.warning(error?.message || '请输入新密码')
        return
      }
      const ownsPasswordDraft = this.userListContext.capturePasswordDraftOwner()
      const matchesSubmittedPassword = this.userListContext.capturePasswordSnapshot()
      const submit = async () => {
        if (!owner.isAlive()) return

        const submittedTargetId = this.userListContext.resetPasswordForm.value.id
        this.userListContext.clearPasswordFailure()
        this.userListContext.clearSavedPasswordSnapshot()
        this.userListContext.resetPasswordLoading.value = true
        try {
          const res = await resetUserPassword({ id: submittedTargetId, ...payload })
        if (!owner.isAlive()) return

          if (res?.status !== 200) {
            this.userListContext.showPasswordFailure(res, ownsPasswordDraft)
            message.error('重置密码请求未完成，请核对当前结果后再操作。')
            return
          }
          message.success('重置密码成功')
          if (matchesSubmittedPassword(submittedTargetId)) this.userListContext.resetPasswordVisible.value = false
          else this.userListContext.rememberSavedPasswordSnapshot(() => matchesSubmittedPassword(submittedTargetId))
          this.handleUserList()
        } catch (error) {
        if (!owner.isAlive()) return

          this.userListContext.showPasswordFailure(error, ownsPasswordDraft)
          message.error('重置密码请求未完成，请核对当前结果后再操作。')
        } finally {
        if (owner.isAlive()) {


          this.userListContext.resetPasswordLoading.value = false

        }
      }
      }

      this.userListContext.passwordConfirmationPending.value = true
      try {
        await runAdminPasswordAction(payload.password, submit, () => { if (!owner.isAlive()) return; return this.confirmWeakPassword('reset', payload.password); })
        if (!owner.isAlive()) return

      } catch (error) {
        if (!owner.isAlive()) return

        message.error(error?.message || '重置密码失败')
      } finally {
        if (owner.isAlive()) {


        this.userListContext.passwordConfirmationPending.value = false

        }
      }
    },
    afterEditorClose() {
      if (!this.userListContext.visible.value) this.resetFormState()
    },
    afterPasswordClose() {
      if (!this.userListContext.resetPasswordVisible.value) this.resetResetPasswordForm()
    },
    resetFormState() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return
      owner.invalidateDialog('editor')
      this.userListContext.submitLoading.value = false
      this.userListContext.passwordConfirmationPending.value = false
      this.userListContext.balanceVisible.value = false
      this.userListContext.balanceSubmitting.value = false

      this.userListContext.formState.value = {
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
      this.userListContext.resetWechatResolveState()
      this.userListContext.isEditing.value = false
    },
    resetResetPasswordForm() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return
      owner.invalidateDialog('password')
      this.userListContext.resetPasswordLoading.value = false
      this.userListContext.passwordConfirmationPending.value = false

      this.userListContext.resetPasswordForm.value = {
        id: undefined,
        username: '',
        password: '',
      }
    },
    loadNewUserBonusDefault() {
      const owner = this.userListContext.owner.capture()
      if (!owner.isAlive()) return

      getSystemSettings().then((res) => {
        if (!owner.isAlive()) return

        if (res?.status === 200) {
          this.userListContext.systemNewUserBonusAmount.value = Number(res?.data?.newUserBonusAmount || 0)
        }
      }).catch(() => {
        if (!owner.isAlive()) return
})
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

.table-preferences-actions {
  display: flex;
  justify-content: flex-end;
  margin-bottom: 12px;
}

.filter-form {
  margin: 12px 0 16px;
  display: flex;
  flex-wrap: wrap;
  gap: 8px 0;
  padding: 16px;
  border: 1px solid var(--console-border, #dce5e1);
  border-radius: 12px;
  background: var(--console-surface-secondary, #f8faf9);
}

.user-page .page-card {
  border: 1px solid var(--console-border, #dce5e1);
  border-radius: var(--console-radius, 12px);
}

.page-alert {
  margin-bottom: 16px;
}

.filter-form :deep(.ant-form-item) {
  margin-bottom: 0;
}

.user-page :deep(.ant-table) {
  font-size: 14px;
}

.user-page :deep(.desktop-user-table .ant-table-thead > tr > th),
.user-page :deep(.desktop-user-table .ant-table-tbody > tr > td) {
  text-align: center;
  vertical-align: middle;
  white-space: normal;
  overflow-wrap: anywhere;
}

.user-page :deep(.desktop-user-table .ant-tag) {
  max-width: 100%;
  margin-inline-end: 0;
  white-space: normal;
  overflow-wrap: anywhere;
}

.user-time-cell {
  display: block;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.user-operation-actions {
  justify-content: center;
}

.mobile-user-list,
.mobile-user-pagination {
  display: none;
}

.location-value {
  display: block;
  white-space: pre-wrap;
  line-height: 1.5;
  text-align: center;
  overflow-wrap: anywhere;
}

.wechat-id-actions {
  margin-top: 8px;
}

.wechat-id-panel {
  margin-top: 10px;
  padding: 12px;
  border: 1px solid var(--console-border, #dce5e1);
  border-radius: 12px;
  background: var(--console-surface-secondary, #f8faf9);
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
  color: var(--console-muted, #62766e);
}

@media (max-width: 640px) {
  .user-page :deep(.ant-card-body),
  .user-page .page-card :deep(.ant-card-body) {
    padding: 12px;
  }

  .desktop-user-table-shell {
    display: none;
  }

  .mobile-user-list {
    display: grid;
    gap: 10px;
  }

  .mobile-user-card {
    padding: 12px;
    border: 1px solid var(--console-border, #dce5e1);
    border-radius: 12px;
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
    color: var(--console-muted, var(--text-muted));
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
    border-top: 1px solid var(--console-border, var(--border));
    border-bottom: 1px solid var(--console-border, var(--border));
  }

  .mobile-user-card__meta div {
    min-width: 0;
  }

  .mobile-user-card__meta dt {
    color: var(--console-muted, var(--text-muted));
    font-size: 11px;
  }

  .mobile-user-card__meta dd {
    margin: 2px 0 0;
    overflow-wrap: anywhere;
    color: var(--console-text, var(--text));
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
