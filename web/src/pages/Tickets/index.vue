<template>
  <div class="console-page tickets-page">
    <ConsolePageHeading
      :title="isAdmin ? '工单管理' : '工单反馈'"
      :description="
        isAdmin
          ? '集中处理用户反馈、回复工单并查看企业微信提醒状态。'
          : '提交问题、补充说明并跟进处理进度。'
      "
    >
      <template #actions
        ><a-button :loading="listLoading" :disabled="!canUseTickets()" :onClick="pageEntry.bind(refreshPage)">刷新</a-button
        ><a-button v-if="canUseTickets() && !isAdmin" type="primary" :onClick="pageEntry.bind(openCreate)"
          >提交工单</a-button
        ></template
      >
    </ConsolePageHeading>
    <section v-if="actionFailures.pageRead.state.failure" data-ticket-page-read-feedback>
      <RequestFailureAlert :key="actionFailures.pageRead.state.context" :failure="actionFailures.pageRead.state.failure" :message="actionFailures.pageRead.state.message" :context-key="actionFailures.pageRead.state.context" :is-current="actionFailures.pageRead.isCurrent" closable :onClose="closePageReadFailure" />
    </section>
    <a-alert
      v-else-if="errorMessage"
      type="error"
      show-icon
      :message="errorMessage"
      closable
      :onClose="pageEntry.bind(clearError)"
    />
    <section v-if="actionFailures.detailRead.state.failure" data-ticket-detail-read-feedback>
      <RequestFailureAlert :key="actionFailures.detailRead.state.context" :failure="actionFailures.detailRead.state.failure" :message="actionFailures.detailRead.state.message" :context-key="actionFailures.detailRead.state.context" :is-current="actionFailures.detailRead.isCurrent" closable :onClose="actionFailures.detailRead.close" />
    </section>
    <a-alert v-if="canManage() && !configLoaded && !configLoading" type="warning" show-icon message="提醒设置尚未确认，请刷新后重试；未确认前不会保存或发送测试。" />
    <ConsolePanel
      v-if="canManage() && configLoaded"
      :key="pageEpoch"
      title="企业微信工单提醒"
      description="提醒以异步队列发送，保存后可发送测试消息。Webhook 地址不会回显完整内容。"
      class="settings-panel"
    >
      <section v-if="actionFailures.configSave.state.failure" data-ticket-config-save-feedback>
      <RequestFailureAlert :key="actionFailures.configSave.state.context" :failure="actionFailures.configSave.state.failure" :message="actionFailures.configSave.state.message" :context-key="actionFailures.configSave.state.context" :is-current="actionFailures.configSave.isCurrent" :busy="configSaving" closable :onClose="actionFailures.configSave.close" />
    </section>
      <section v-if="actionFailures.notificationTest.state.failure" data-ticket-notification-test-feedback>
      <RequestFailureAlert :key="actionFailures.notificationTest.state.context" :failure="actionFailures.notificationTest.state.failure" :message="actionFailures.notificationTest.state.message" :context-key="actionFailures.notificationTest.state.context" :is-current="actionFailures.notificationTest.isCurrent" :busy="configTesting" closable :onClose="actionFailures.notificationTest.close" />
    </section>
      <a-form
        :model="notificationConfig"
        :disabled="!configLoaded || !canManage()"
        layout="vertical"
        :onFinish="pageEntry.bind(saveNotificationConfig)"
      >
        <div class="config-grid">
          <a-form-item label="启用提醒"
            ><a-switch
              :checked="notificationConfig.enabled" :onUpdate:checked="pageEntry.bind(setConfig, 'enabled')"
              checked-children="开启"
              un-checked-children="关闭"
          /></a-form-item>
          <a-form-item
            label="Webhook 地址"
            help="只接受企业微信机器人 HTTPS 地址；留空表示保持已保存地址。"
          >
            <a-input
              :value="notificationConfig.webhookUrl" :onUpdate:value="pageEntry.bind(setConfig, 'webhookUrl')"
              type="url"
              allow-clear
              placeholder="输入新地址以替换已保存地址"
            />
            <div
              v-if="notificationConfig.configured"
              class="config-address-actions"
            >
              <span>{{
                notificationConfig.clearWebhook
                  ? "保存后将清除当前地址"
                  : "已配置企业微信机器人"
              }}</span>
              <a-button
                v-if="!notificationConfig.clearWebhook"
                type="link"
                danger
                size="small"
                :onClick="pageEntry.bind(clearSavedWebhook)"
                >清除已保存地址</a-button
              >
              <a-button
                v-else
                type="link"
                size="small"
                :onClick="pageEntry.bind(keepSavedWebhook)"
                >保留已保存地址</a-button
              >
            </div>
          </a-form-item>
          <a-form-item label="@成员 ID" help="多个成员用逗号分隔，可填写 @all。"
            ><a-input
              :value="notificationConfig.mentionedList" :onUpdate:value="pageEntry.bind(setConfig, 'mentionedList')"
              allow-clear
              placeholder="成员 ID，可选"
          /></a-form-item>
        </div>
        <a-space
          ><a-button type="primary" html-type="submit" :loading="configSaving" :disabled="!configLoaded || !canManage() || configTesting"
            >保存提醒设置</a-button
          ><a-button
            :loading="configTesting"
            :disabled="!configLoaded || !canManage() || configSaving"
            :onClick="pageEntry.bind(testNotification)"
            >发送测试消息</a-button
          ><span v-if="notificationConfig.configured" class="config-hint"
            >当前已配置 Webhook</span
          ></a-space
        >
      </a-form>
    </ConsolePanel>
    <ConsolePanel
      title="工单列表"
      :description="
        isAdmin
          ? '关键词可匹配工单标题、编号、用户和对话内容。'
          : '仅显示当前账号创建的工单。'
      "
      class="list-panel"
    >
      <a-form layout="inline" class="filter-form" :onSubmit="pageEntry.bind(preventSubmit, loadTickets)">
        <a-form-item label="关键词"
          ><a-input-search
            :value="keywordInput" :onUpdate:value="pageEntry.bind(setKeyword)"
            allow-clear
            placeholder="标题 / 工单号 / 内容"
            style="width: 280px"
            :onSearch="pageEntry.bind(applyFilters)"
            :onPressEnter="pageEntry.bind(applyFilters)"
        /></a-form-item>
        <a-form-item v-if="isAdmin" label="状态"
          ><a-select
            :value="filters.status" :onUpdate:value="pageEntry.bind(setFilter, 'status')"
            allow-clear
            placeholder="全部状态"
            style="width: 150px"
            :options="statusOptions"
            :onChange="pageEntry.bind(applyFilters)"
        /></a-form-item>
        <a-form-item v-if="isAdmin" label="创建日期"
          ><a-input
            :value="filters.createdFrom" :onUpdate:value="pageEntry.bind(setFilter, 'createdFrom')"
            type="date"
            aria-label="创建日期起始"
            :onChange="pageEntry.bind(applyFilters)"
        /></a-form-item>
        <a-form-item v-if="isAdmin"
          ><a-input
            :value="filters.createdTo" :onUpdate:value="pageEntry.bind(setFilter, 'createdTo')"
            type="date"
            aria-label="创建日期截止"
            :onChange="pageEntry.bind(applyFilters)"
        /></a-form-item>
        <a-form-item v-if="isAdmin" label="更新日期"
          ><a-input
            :value="filters.updatedFrom" :onUpdate:value="pageEntry.bind(setFilter, 'updatedFrom')"
            type="date"
            aria-label="更新日期起始"
            :onChange="pageEntry.bind(applyFilters)"
        /></a-form-item>
        <a-form-item v-if="isAdmin"
          ><a-input
            :value="filters.updatedTo" :onUpdate:value="pageEntry.bind(setFilter, 'updatedTo')"
            type="date"
            aria-label="更新日期截止"
            :onChange="pageEntry.bind(applyFilters)"
        /></a-form-item>
        <a-form-item
          ><a-button :onClick="pageEntry.bind(resetFilters)">重置</a-button></a-form-item
        >
      </a-form>
      <div class="mobile-ticket-list">
        <a-card
          v-for="ticket in tickets"
          :key="ticket.id"
          class="ticket-card"
          size="small"
          :onClick="pageEntry.bind(openDetail, ticket.id)"
          ><div class="ticket-card-heading">
            <strong>{{ ticket.subject || `工单 #${ticket.id}` }}</strong
            ><a-tag :color="ticketStatusColor(ticket.status)">{{
              ticketStatusLabel(ticket.status)
            }}</a-tag>
          </div>
          <dl class="ticket-card-meta">
            <div v-if="isAdmin">
              <dt>用户</dt>
              <dd>{{ ticket.userName || `用户 #${ticket.userId || "-"}` }}</dd>
            </div>
            <div>
              <dt>最近更新</dt>
              <dd>
                {{ formatTicketTime(ticket.lastMessageAt || ticket.updatedAt) }}
              </dd>
            </div>
          </dl></a-card
        ><a-empty
          v-if="!listLoading && tickets.length === 0"
          description="暂无工单"
        />
        <a-pagination
          class="mobile-ticket-pagination"
          :current="pagination.current"
          :page-size="pagination.pageSize"
          :total="pagination.total"
          :show-size-changer="false"
          size="small"
          :onChange="pageEntry.bind(handleMobilePageChange)"
        />
      </div>
      <ConsoleTableShell label="工单列表" class="desktop-ticket-table"
        ><a-table
          bordered
          row-key="id"
          :data-source="tickets"
          :columns="columns"
          :loading="listLoading"
          :pagination="pagination"
          :scroll="{ x: isAdmin ? 760 : 620 }"
          :onChange="pageEntry.bind(handleTableChange)"
          ><template #bodyCell="{ column, record }"
            ><span v-if="column.key === 'subject'" class="ticket-subject">{{
              record.subject || `工单 #${record.id}`
            }}</span
            ><span v-else-if="column.key === 'userName'">{{
              record.userName || `用户 #${record.userId || "-"}`
            }}</span
            ><a-tag
              v-else-if="column.key === 'status'"
              :color="ticketStatusColor(record.status)"
              >{{ ticketStatusLabel(record.status) }}</a-tag
            ><span v-else-if="column.key === 'updatedAt'" class="time-cell">{{
              formatTicketTime(record.lastMessageAt || record.updatedAt)
            }}</span
            ><a-button
              v-else-if="column.key === 'action'"
              type="link"
              :onClick="pageEntry.bind(openDetail, record.id)"
              >查看详情</a-button
            ></template
          ><template #emptyText
            ><a-empty description="暂无工单" /></template></a-table
      ></ConsoleTableShell>
    </ConsolePanel>
    <a-modal
      :key="pageEpoch + ':' + createVersion" :open="createVisible" :onUpdate:open="createEntry.bind(setCreateVisible)"
      title="提交工单"
      :confirm-loading="createLoading"
      ok-text="提交"
      cancel-text="取消"
      :onOk="createEntry.bind(submitTicket)"
      ><section v-if="actionFailures.create.state.failure" data-ticket-create-feedback>
      <RequestFailureAlert :key="actionFailures.create.state.context" :failure="actionFailures.create.state.failure" :message="actionFailures.create.state.message" :context-key="actionFailures.create.state.context" :is-current="actionFailures.create.isCurrent" :busy="createLoading" closable :onClose="actionFailures.create.close" />
    </section><a-form layout="vertical"
        ><a-alert v-if="createSaved" type="info" show-icon message="提交的工单已保存，当前修改尚未提交。" /><a-form-item label="标题" required :validate-status="createValidationMessage('subject') ? 'error' : undefined"
          ><a-input
            :value="draft.subject" :onUpdate:value="createEntry.bind(setDraft, 'subject')"
            :aria-invalid="createValidationMessage('subject') ? 'true' : undefined" :aria-describedby="createValidationMessage('subject') ? createValidationErrorId('subject') : undefined"
            :maxlength="120"
            show-count
            placeholder="请简要描述问题" /><template #help><span v-if="createValidationMessage('subject')" :id="createValidationErrorId('subject')" role="alert">{{ createValidationMessage('subject') }}</span></template></a-form-item
        ><a-form-item label="问题描述" required :validate-status="createValidationMessage('body') ? 'error' : undefined"
          ><a-textarea
            :value="draft.body" :onUpdate:value="createEntry.bind(setDraft, 'body')"
            :aria-invalid="createValidationMessage('body') ? 'true' : undefined" :aria-describedby="createValidationMessage('body') ? createValidationErrorId('body') : undefined"
            :maxlength="10000"
            show-count
            :rows="8"
            placeholder="请提供复现步骤、错误提示和期望结果" /><template #help><span v-if="createValidationMessage('body')" :id="createValidationErrorId('body')" role="alert">{{ createValidationMessage('body') }}</span></template></a-form-item></a-form
    ></a-modal>
    <a-modal
      :key="pageEpoch + ':' + detailRequestVersion" :open="detailVisible" :onUpdate:open="detailEntry.bind(setDetailVisible)"
      :wrap-props="{ 'aria-modal': true }"
      :focus-trigger-after-close="false"
      :title="detailTitle"
      :footer="null"
      width="820px"
      :onCancel="detailEntry.bind(closeDetail)"
      ><a-spin :spinning="detailLoading"
        ><template v-if="detailTicket"
          ><div class="detail-summary">
            <a-tag :color="ticketStatusColor(detailTicket.status)">{{
              ticketStatusLabel(detailTicket.status)
            }}</a-tag
            ><span v-if="isAdmin">{{
              detailTicket.userName || `用户 #${detailTicket.userId || "-"}`
            }}</span
            ><span>创建于 {{ formatTicketTime(detailTicket.createdAt) }}</span>
          </div>
          <a-alert
            v-if="notificationText"
            :type="notification?.status === 'failed' ? 'warning' : 'info'"
            show-icon
            :message="notificationText"
            class="notification-alert"
            ><template #description
              ><a-button
                v-if="notification?.status === 'failed' && notification?.id"
                type="link"
                size="small"
                :onClick="detailEntry.bind(retryNotification)"
                >重试提醒</a-button
              ></template
            ></a-alert
          >
          <div class="message-thread">
            <article
              v-for="item in messages"
              :key="item.id"
              class="message-item"
              :class="{ 'is-admin': item.authorRole === 'admin' }"
            >
              <header>
                <strong>{{
                  item.authorName ||
                  (item.authorRole === "admin" ? "管理员" : "用户")
                }}</strong
                ><span>{{ formatTicketTime(item.createdAt) }}</span>
              </header>
              <p>{{ item.body }}</p>
            </article>
          </div>
          <section v-if="actionFailures.retry.state.failure" data-ticket-notification-retry-feedback>
      <RequestFailureAlert :key="actionFailures.retry.state.context" :failure="actionFailures.retry.state.failure" :message="actionFailures.retry.state.message" :context-key="actionFailures.retry.state.context" :is-current="actionFailures.retry.isCurrent" :busy="retryLoading" closable :onClose="actionFailures.retry.close" />
    </section>
          <div v-if="isAdmin" class="detail-actions">
            <label :for="detailStatusId">工单状态</label>
            <a-select
              :id="detailStatusId"
              :value="detailStatus" :onUpdate:value="detailEntry.bind(setDetailStatus)"
              style="width: 150px"
              :options="statusOptions"
              :onChange="detailEntry.bind(chooseStatus)"
            /><a-button :loading="statusSaving" :onClick="detailEntry.bind(saveStatus)"
              >更新状态</a-button
            >
          </div>
          <section v-if="actionFailures.status.state.failure" data-ticket-status-feedback>
      <RequestFailureAlert :key="actionFailures.status.state.context" :failure="actionFailures.status.state.failure" :message="actionFailures.status.state.message" :context-key="actionFailures.status.state.context" :is-current="actionFailures.status.isCurrent" :busy="statusSaving" closable :onClose="actionFailures.status.close" />
    </section>
          <section v-if="replyFailure" data-ticket-reply-feedback>
            <RequestFailureAlert :key="replyFailureContextKey" :failure="replyFailure" message="回复请求未完成，请核对当前工单后再操作。" :busy="replyLoading" :context-key="replyFailureContextKey" :is-current="isCurrentReplyFailure" />
          </section>
          <a-form
            layout="vertical"
            class="reply-form"
            :onSubmit="detailEntry.bind(preventSubmit, submitReply)"
            ><a-form-item label="追加回复"
              ><a-textarea
                :value="replyBody" :onUpdate:value="detailEntry.bind(setReply)"
                :maxlength="10000"
                show-count
                :rows="5"
                placeholder="请输入回复内容" /></a-form-item
            ><a-button
              type="primary"
              :loading="replyLoading"
              :disabled="!replyBody.trim()"
              :onClick="detailEntry.bind(submitReply)"
              >发送回复</a-button
            ></a-form
          ></template
        ></a-spin
      ></a-modal
    >
  </div>
</template>

<script setup>
defineOptions({ name: "TicketsPage" });

import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  reactive,
  ref,
  shallowRef,
  shallowReactive,
  useId,
  watch,
} from "vue";
import { useRoute, useRouter } from "vue-router";
import { message } from "ant-design-vue";
import RequestFailureAlert from "@/components/RequestFailureAlert.vue";
import { ensureRequestFailure } from "@/utils/requestFailureView.mjs";
import {
  addTicketMessage,
  createTicket,
  getTicket,
  getTickets,
  getTicketNotificationConfig,
  retryTicketNotification,
  saveTicketNotificationConfig,
  testTicketNotification,
  updateTicket,
} from "@/api/TicketApi";
import { useCurrentUserDisplay, isAdminUser } from "@/utils/currentUser";
import { useCurrentUserRequestScope } from "@/utils/currentUserScope";
import {
  buildTicketListParams,
  formatTicketTime,
  normalizeTicketDetailResponse,
  normalizeTicketListResponse,
  notificationStatusText,
  ticketStatusColor,
  ticketStatusLabel,
  TICKET_STATUS_OPTIONS,
  getTicketDraftValidation,
} from "@/utils/ticketHelpers.mjs";

const detailStatusId = `tickets-${useId()}-detail-status`;
const { currentUser } = useCurrentUserDisplay();
const route = useRoute();
const router = useRouter();
let listRequestVersion = 0;
let detailRequestVersion = 0;
let detailFocusEntry = null, detailFocusVersion = 0;
function retireDetailFocus() { detailFocusEntry = null; detailFocusVersion += 1; }
const isAdmin = computed(() => isAdminUser(currentUser.value));
const tickets = ref([]);
const listLoading = ref(false);
const errorMessage = ref("");
const filters = reactive({
  keyword: "",
  status: "",
  createdFrom: "",
  createdTo: "",
  updatedFrom: "",
  updatedTo: "",
});
const keywordInput = ref("");
const pagination = reactive({
  current: 1,
  pageSize: 20,
  total: 0,
  showSizeChanger: true,
  showQuickJumper: true,
  pageSizeOptions: ["10", "20", "50", "100"],
  showTotal: (total) => `共 ${total} 条`,
});
const statusOptions = TICKET_STATUS_OPTIONS.map(({ value, label }) => ({
  value,
  label,
}));
const columns = computed(() => {
  const base = [
    { title: "编号", dataIndex: "id", key: "id", width: 90 },
    { title: "标题", dataIndex: "subject", key: "subject", width: 300 },
  ];
  if (isAdmin.value)
    base.push({
      title: "用户",
      dataIndex: "userName",
      key: "userName",
      width: 160,
    });
  base.push(
    { title: "状态", dataIndex: "status", key: "status", width: 120 },
    { title: "最近更新", dataIndex: "updatedAt", key: "updatedAt", width: 180 },
    { title: "操作", key: "action", width: 100 }
  );
  return base;
});
const createVisible = ref(false);
const createLoading = ref(false);
const draft = reactive({ subject: "", body: "" });
const createValidation = shallowRef(null);
const createValidationErrorId = field => `${detailStatusId}-create-${field}-error`;
function createValidationMessage(field) { return createValidation.value?.field === field ? createValidation.value.message : ""; }
function retireCreateValidation() { createValidation.value = null; }
function retireStaleCreateValidation() {
  const previous = createValidation.value;
  if (!previous) return;
  const current = getTicketDraftValidation(draft.subject, draft.body);
  if (current.field !== previous.field || current.message !== previous.message) retireCreateValidation();
}
const detailVisible = ref(false);
const detailLoading = ref(false);
const detailTicket = ref(null);
const messages = ref([]);
const notification = ref(null);
const replyBody = ref("");
const replyLoading = ref(false);
const replyFailure = shallowRef(null);
const replyFailureContextKey = ref(0);
let replyDraftLease = {}, replyFailureOwner = () => false;
function clearReplyFailure() {
  replyFailure.value = null;
  replyFailureOwner = () => false;
  replyFailureContextKey.value += 1;
}
function retireReplyDraft() { replyDraftLease = {}; clearReplyFailure(); }
function captureReplyDraftOwner() {
  const owns = captureDetail(), draft = replyDraftLease, id = detailTicket.value?.id;
  return () => owns() && draft === replyDraftLease && detailTicket.value?.id === id;
}
function showReplyFailure(error, owns) {
  if (!owns()) return;
  replyFailureOwner = owns;
  replyFailure.value = ensureRequestFailure(error);
}
function isCurrentReplyFailure(error, context) {
  return replyFailureOwner() && detailVisible.value && Boolean(error)
    && error === replyFailure.value && context === replyFailureContextKey.value;
}
const detailStatus = ref("");
const statusWasChosen = ref(false);
const statusSaving = ref(false);
const configSaving = ref(false);
const configTesting = ref(false);
const notificationConfig = reactive({
  enabled: false,
  webhookUrl: "",
  mentionedList: "",
  configured: false,
  clearWebhook: false,
});
const configLoaded = ref(false), configLoading = ref(false), configDirty = ref(false), retryLoading = ref(false), createSaved = ref(false);
const pageEpoch = ref(0);
let active = true, createVersion = 0, configReadVersion = 0, configDraftVersion = 0;
// Slots keep the original failure identity. Only caller-owned predicates decide
// admission; no global begin ticket cancels concurrent list/config reads.
function makeFailureSlot(defaultMessage) {
  const state = shallowReactive({ failure: null, context: 0, message: defaultMessage });
  let owns = () => false, kind = "";
  function clear() { state.failure = null; state.context += 1; state.message = defaultMessage; owns = () => false; kind = ""; }
  function clearKind(expected) { if (kind === expected) clear(); }
  function show(error, current, message = defaultMessage, domain = "") {
    if (!current()) return;
    state.context += 1; state.message = message; owns = current; kind = domain;
    state.failure = ensureRequestFailure(error);
  }
  function isCurrent(error, context) { return owns() && Boolean(error) && error === state.failure && context === state.context; }
  function close(error, context) { if (!isCurrent(error, context)) return false; clear(); return true; }
  return { state, clear, clearKind, show, isCurrent, close };
}
const actionFailures = {
  pageRead: makeFailureSlot("工单列表加载失败"),
  detailRead: makeFailureSlot("工单详情加载失败"),
  create: makeFailureSlot("工单提交失败"),
  status: makeFailureSlot("工单状态更新失败"),
  retry: makeFailureSlot("提醒重试失败"),
  configSave: makeFailureSlot("企业微信提醒设置保存失败，请核对配置后重试。"),
  notificationTest: makeFailureSlot("测试消息发送失败，请核对已保存配置后重试。"),
};
let createFailureDraft = {}, statusFailureDraft = {}, retryFailureDraft = {}, savedNotificationVersion = 0;
function retireActionFailures() {
  Object.values(actionFailures).forEach(slot => slot.clear());
  createFailureDraft = {}; statusFailureDraft = {}; retryFailureDraft = {}; savedNotificationVersion += 1;
}
function retireDetailActionFailures() {
  actionFailures.detailRead.clear(); actionFailures.status.clear(); actionFailures.retry.clear();
  statusFailureDraft = {}; retryFailureDraft = {};
}
function closePageReadFailure(error, context) { if (actionFailures.pageRead.close(error, context)) errorMessage.value = ""; }

function canUseTickets() {
  const id = Number(currentUser.value?.id);
  return active && Number.isInteger(id) && id > 0 && id <= 2147483647
    && String(currentUser.value?.status || "enabled") === "enabled";
}
function canManage() { return canUseTickets() && isAdmin.value; }
function clearOwned() {
  retireDetailFocus();
  retireCreateValidation();
  retireActionFailures();
  retireReplyDraft();
  listRequestVersion += 1; detailRequestVersion += 1; createVersion += 1; configReadVersion += 1; configDraftVersion += 1; pageEpoch.value += 1;
  tickets.value = []; pagination.total = 0; pagination.current = 1;
  listLoading.value = detailLoading.value = createLoading.value = replyLoading.value = statusSaving.value = configSaving.value = configTesting.value = retryLoading.value = configLoading.value = false;
  createVisible.value = detailVisible.value = configLoaded.value = createSaved.value = configDirty.value = false;
  detailTicket.value = null; messages.value = []; notification.value = null; replyBody.value = ""; detailStatus.value = ""; statusWasChosen.value = false;
  draft.subject = ""; draft.body = ""; errorMessage.value = "";
  Object.assign(notificationConfig, { enabled: false, webhookUrl: "", mentionedList: "", configured: false, clearWebhook: false });
  keywordInput.value = ""; Object.assign(filters, { keyword: "", status: "", createdFrom: "", createdTo: "", updatedFrom: "", updatedTo: "" });
}
function reloadOwned() {
  if (!canUseTickets()) return;
  loadTickets(); if (canManage()) loadNotificationConfig();
  if (route.params.id) loadDetail(String(route.params.id));
}
function refreshPage() { loadTickets(); if (canManage()) loadNotificationConfig(); }
const scope = useCurrentUserRequestScope(currentUser, { clear: clearOwned, reset() {}, reload: reloadOwned });
function capturePage(admin = false) {
  const owns = scope.captureDataOwner(), epoch = pageEpoch.value;
  return () => owns() && epoch === pageEpoch.value && canUseTickets() && (!admin || canManage());
}
function captureCreate() {
  const owns = capturePage(), version = createVersion;
  return () => owns() && version === createVersion && createVisible.value;
}
function captureDetail() {
  const owns = capturePage(), version = detailRequestVersion;
  return () => owns() && version === detailRequestVersion && detailVisible.value;
}
function entry(capture) { return { bind(action, ...args) {
  const owns = capture(); return (...events) => { if (owns()) return action(...args, ...events); };
} }; }
const pageEntry = entry(capturePage), createEntry = entry(captureCreate), detailEntry = entry(captureDetail);
function setKeyword(value) { if (canUseTickets()) keywordInput.value = value; }
function setFilter(key, value) { if (canUseTickets()) filters[key] = value; }
function setConfig(key, value) {
  if (!canManage() || !configLoaded.value) return;
  notificationConfig[key] = value;
  configDirty.value = true;
  if (key === "webhookUrl") notificationConfig.clearWebhook = false;
}
function clearSavedWebhook() { if (canManage() && configLoaded.value) { notificationConfig.clearWebhook = true; notificationConfig.webhookUrl = ""; configDirty.value = true; } }
function keepSavedWebhook() { if (canManage() && configLoaded.value) { notificationConfig.clearWebhook = false; configDirty.value = true; } }
function setDraft(key, value) { if (createVisible.value) draft[key] = value; }
function setCreateVisible(value) { createVisible.value = value; }
function setDetailVisible(value) { if (!value) closeDetail(); }
function setReply(value) { replyBody.value = value; }
function setDetailStatus(value) { if (canManage()) detailStatus.value = value; }
function chooseStatus() { if (canManage()) statusWasChosen.value = true; }
function clearError() { errorMessage.value = ""; }
function preventSubmit(action, event) { event?.preventDefault(); return action(); }
watch(() => [notificationConfig.enabled, notificationConfig.webhookUrl, notificationConfig.mentionedList, notificationConfig.clearWebhook], () => { configDraftVersion += 1; actionFailures.configSave.clear(); }, { flush: "sync" });
watch([() => draft.subject, () => draft.body], () => { createFailureDraft = {}; actionFailures.create.clear(); }, { flush: "sync" });
watch([() => detailTicket.value?.id, detailStatus], () => { statusFailureDraft = {}; actionFailures.status.clear(); }, { flush: "sync" });
watch([() => detailTicket.value?.id, () => notification.value?.id, () => notification.value?.status], () => { retryFailureDraft = {}; actionFailures.retry.clear(); }, { flush: "sync" });
// Compare raw inputs only, without serializing reply text or affecting issued-write busy state.
watch([replyBody, () => detailTicket.value?.id, () => isAdmin.value && statusWasChosen.value ? detailStatus.value : undefined], retireReplyDraft, { flush: "sync" });
watch([() => draft.subject, () => draft.body], retireStaleCreateValidation, { flush: "sync" });
watch(createVisible, next => { if (!next) { retireCreateValidation(); createVersion += 1; createLoading.value = false; createSaved.value = false; draft.subject = ""; draft.body = ""; } }, { flush: "sync" });
// Focus leases also retire for same-id non-admin role ABA and unexpected routes.
watch(() => String(currentUser.value?.role ?? ""), retireDetailFocus, { flush: "sync" });
watch(() => route.fullPath, path => {
  if (path !== "/adapter/tickets" && path !== detailFocusEntry?.path) retireDetailFocus();
}, { flush: "sync" });
watch(() => currentUser.value?.status, () => { clearOwned(); reloadOwned(); }, { flush: "sync" });
onBeforeUnmount(() => { active = false; retireReplyDraft(); });
onBeforeUnmount(retireActionFailures);
onBeforeUnmount(retireCreateValidation);
const detailTitle = computed(() => detailTicket.value?.subject || "工单详情");
const notificationText = computed(() =>
  notificationStatusText(notification.value)
);
function responseMessage(error, fallback) {
  return (
    String(
      error?.message || error?.response?.data?.message || fallback
    ).trim() || fallback
  );
}
async function loadTickets() {
  if (!canUseTickets()) return;
  const owns = capturePage(), version = ++listRequestVersion;
  actionFailures.pageRead.clear();
  listLoading.value = true; errorMessage.value = "";
  try {
    const result = normalizeTicketListResponse(await getTickets(buildTicketListParams(filters, pagination.current, pagination.pageSize)));
    if (!owns() || version !== listRequestVersion) return;
    tickets.value = result.items; pagination.total = result.total;
  } catch (error) {
    if (owns() && version === listRequestVersion) {
      errorMessage.value = responseMessage(error, "工单列表加载失败");
      actionFailures.pageRead.show(error, () => owns() && version === listRequestVersion, "工单列表加载失败", "list");
    }
  } finally { if (owns() && version === listRequestVersion) listLoading.value = false; }
}
function applyFilters() {
  filters.keyword = keywordInput.value.trim();
  pagination.current = 1;
  loadTickets();
}
function resetFilters() {
  keywordInput.value = "";
  filters.keyword = "";
  filters.status = "";
  filters.createdFrom = "";
  filters.createdTo = "";
  filters.updatedFrom = "";
  filters.updatedTo = "";
  pagination.current = 1;
  loadTickets();
}
function handleTableChange(nextPagination) {
  pagination.current = nextPagination.current;
  pagination.pageSize = nextPagination.pageSize;
  loadTickets();
}
function handleMobilePageChange(page) {
  pagination.current = page;
  loadTickets();
}
function openCreate() {
  if (!canUseTickets()) return;
  retireCreateValidation();
  actionFailures.create.clear(); createFailureDraft = {};
  createVersion += 1; createLoading.value = false; createSaved.value = false;
  draft.subject = "";
  draft.body = "";
  createVisible.value = true;
}
async function submitTicket() {
  if (!canUseTickets() || !createVisible.value || createLoading.value) return;
  const validation = getTicketDraftValidation(draft.subject, draft.body);
  if (validation.message) { createValidation.value = validation; message.warning(validation.message); return; }
  retireCreateValidation();
  const owns = captureCreate(), subject = draft.subject, body = draft.body;
  const failureDraft = createFailureDraft, ownsFailure = () => owns() && failureDraft === createFailureDraft;
  actionFailures.create.clear();
  createLoading.value = true; createSaved.value = false;
  try {
    await createTicket({ subject: subject.trim(), body: body.trim() });
    if (!owns()) return;
    message.success("工单已提交");
    if (draft.subject === subject && draft.body === body) createVisible.value = false;
    else createSaved.value = true;
    pagination.current = 1; await loadTickets();
  } catch (error) { if (owns()) { actionFailures.create.show(error, ownsFailure); message.error(responseMessage(error, "工单提交失败")); } }
  finally { if (owns()) createLoading.value = false; }
}
function openDetail(id, event) {
  if (!canUseTickets()) return;
  retireDetailFocus();
  const path = `/adapter/tickets/${encodeURIComponent(id)}`, opener = event?.currentTarget;
  // Retain only the native opener, safe owner predicate and scalar route/id, never the event/row.
  const focus = opener instanceof HTMLElement && opener.isConnected && opener.tabIndex >= 0
    ? { opener, owns: capturePage(), path, id: String(id) } : null;
  detailFocusEntry = focus;
  return router.push(path).then(failure => {
    if (failure && detailFocusEntry === focus) retireDetailFocus();
    return failure;
  }, error => {
    if (detailFocusEntry === focus) retireDetailFocus();
    throw error;
  });
}
async function loadDetail(id) {
  if (!canUseTickets()) return;
  detailFocusVersion += 1;
  if (detailFocusEntry?.id !== String(id)) detailFocusEntry = null;
  const owns = capturePage();
  retireDetailActionFailures();
  retireReplyDraft();
  replyLoading.value = statusSaving.value = retryLoading.value = false;
  const version = ++detailRequestVersion;
  detailVisible.value = true;
  detailLoading.value = true;
  detailTicket.value = null;
  messages.value = [];
  notification.value = null;
  replyBody.value = "";
  statusWasChosen.value = false;
  try {
    const result = normalizeTicketDetailResponse(await getTicket(id));
    if (!owns() || version !== detailRequestVersion) return;
    detailTicket.value = result.ticket;
    messages.value = result.messages;
    notification.value = result.notification;
    detailStatus.value = result.ticket?.status || "";
  } catch (error) {
    if (!owns() || version !== detailRequestVersion) return;
    message.error(responseMessage(error, "工单详情加载失败"));
    detailVisible.value = false;
    actionFailures.detailRead.show(error, () => owns() && version === detailRequestVersion && String(route.params.id || "") === String(id));
  } finally {
    if (owns() && version === detailRequestVersion) detailLoading.value = false;
  }
}
function closeDetail() {
  if (!canUseTickets()) return;
  let focus = detailFocusEntry;
  retireDetailFocus();
  const ticket = detailFocusVersion, create = createVersion;
  retireDetailActionFailures();
  retireReplyDraft();
  ++detailRequestVersion;
  replyLoading.value = statusSaving.value = retryLoading.value = false;
  replyBody.value = ""; messages.value = []; notification.value = null;
  detailVisible.value = false;
  detailTicket.value = null;
  // The original key/version retirement unmounts the old Modal before afterClose.
  // Use one owned continuation after navigation/disposal, not the dead vendor callback.
  return router.replace("/adapter/tickets").then(async failure => {
    if (failure || !focus) return failure;
    await nextTick(); await nextTick();
    if (!focus.owns() || ticket !== detailFocusVersion || create !== createVersion
      || route.fullPath !== "/adapter/tickets" || route.params.id || detailVisible.value || createVisible.value) return;
    const opener = focus.opener;
    if (!(opener instanceof HTMLElement) || !opener.isConnected || opener.tabIndex < 0
      || opener.matches(':disabled, [aria-disabled="true"]')) return;
    const activeElement = document.activeElement;
    if (activeElement !== document.body && activeElement !== opener) return;
    const isVisible = element => {
      for (let node = element; node; node = node.parentElement) {
        const style = window.getComputedStyle(node);
        if (node.hidden || node.inert || node.getAttribute("aria-hidden") === "true"
          || style.display === "none" || style.visibility === "hidden") return false;
      }
      return true;
    };
    if (!isVisible(opener) || [...document.querySelectorAll('[role="dialog"]')].some(isVisible)) return;
    if (activeElement !== opener) opener.focus({ preventScroll: true });
  }).finally(() => { focus = null; });
}
async function submitReply() {
  if (!canUseTickets() || replyLoading.value || !detailVisible.value) return;
  const owns = captureDetail();
  const rawBody = replyBody.value;
  const submittedStatus = detailStatus.value;
  const body = String(replyBody.value || "").trim();
  if (!body) return;
  const ticketId = detailTicket.value?.id;
  const version = detailRequestVersion;
  if (!ticketId) return;
  const ownsDraft = captureReplyDraftOwner();
  clearReplyFailure();
  replyLoading.value = true;
  try {
    const payload = { body };
    if (isAdmin.value && statusWasChosen.value)
      payload.status = detailStatus.value;
    const result = normalizeTicketDetailResponse(
      await addTicketMessage(ticketId, payload)
    );
    if (
      owns() && version === detailRequestVersion &&
      detailTicket.value?.id === ticketId
    ) {
      detailTicket.value = result.ticket || detailTicket.value;
      messages.value = result.messages.length
        ? result.messages
        : [
            ...messages.value,
            {
              body,
              authorRole: isAdmin.value ? "admin" : "user",
              authorName: isAdmin.value
                ? "管理员"
                : currentUser.value?.nickname || currentUser.value?.username,
              createdAt: new Date().toISOString(),
              id: `local-${Date.now()}`,
            },
          ];
      notification.value = result.notification || notification.value;
      if (detailStatus.value === submittedStatus) {
        detailStatus.value = detailTicket.value.status;
        statusWasChosen.value = false;
      }
      if (replyBody.value === rawBody) replyBody.value = "";
    }
    if (!owns()) return;
    await loadTickets();
    if (owns()) message.success("回复已发送");
  } catch (error) {
    if (owns()) {
      showReplyFailure(error, ownsDraft);
      message.error(responseMessage(error, "回复发送失败"));
    }
  } finally {
    if (owns()) replyLoading.value = false;
  }
}
async function saveStatus() {
  if (!canManage() || statusSaving.value || !detailVisible.value) return;
  const owns = captureDetail();
  if (!detailTicket.value || !detailStatus.value) return;
  const ticketId = detailTicket.value.id;
  const status = detailStatus.value;
  const version = detailRequestVersion;
  const failureDraft = statusFailureDraft, ownsFailure = () => owns() && failureDraft === statusFailureDraft && detailTicket.value?.id === ticketId;
  actionFailures.status.clear();
  statusSaving.value = true;
  try {
    const result = normalizeTicketDetailResponse(
      await updateTicket(ticketId, { status })
    );
    if (
      owns() && version === detailRequestVersion &&
      detailTicket.value?.id === ticketId
    ) {
      detailTicket.value = result.ticket || {
        ...detailTicket.value,
        status,
      };
      if (detailStatus.value === status) {
        detailStatus.value = detailTicket.value.status;
        statusWasChosen.value = false;
      }
      messages.value = result.messages.length
        ? result.messages
        : messages.value;
      notification.value = result.notification || notification.value;
    }
    if (!owns()) return;
    await loadTickets();
    if (owns()) message.success("工单状态已更新");
  } catch (error) {
    if (owns()) { actionFailures.status.show(error, ownsFailure); message.error(responseMessage(error, "工单状态更新失败")); }
  } finally {
    if (owns()) statusSaving.value = false;
  }
}
async function retryNotification() {
  if (!canManage() || retryLoading.value || !detailVisible.value) return;
  const owns = captureDetail();
  if (!detailTicket.value?.id || !notification.value?.id) return;
  const ticketId = detailTicket.value.id;
  const notificationId = notification.value.id;
  const version = detailRequestVersion;
  const failureDraft = retryFailureDraft, ownsFailure = () => owns() && failureDraft === retryFailureDraft && detailTicket.value?.id === ticketId && notification.value?.id === notificationId;
  actionFailures.retry.clear();
  retryLoading.value = true;
  try {
    await retryTicketNotification(ticketId, notificationId);
    if (
      owns() && version === detailRequestVersion &&
      detailTicket.value?.id === ticketId
    ) {
      notification.value = {
        ...notification.value,
        status: "pending",
        lastError: "",
      };
    }
    if (owns()) message.success("提醒已加入重试队列");
  } catch (error) {
    if (owns()) { actionFailures.retry.show(error, ownsFailure); message.error(responseMessage(error, "提醒重试失败")); }
  } finally { if (owns()) retryLoading.value = false; }
}
async function loadNotificationConfig(preserveDraft = configDirty.value) {
  if (!canManage() || configLoading.value) return;
  const owns = capturePage(true), version = ++configReadVersion, draftVersion = configDraftVersion;
  actionFailures.pageRead.clearKind("config");
  configLoading.value = true;
  try {
    const response = await getTicketNotificationConfig();
    if (!owns() || version !== configReadVersion) return;
    const data = response?.data?.data || response?.data;
    if (!data || typeof data !== "object" || Array.isArray(data)
      || typeof data.enabled !== "boolean" || typeof data.configured !== "boolean") throw new Error("invalid settings response");
    notificationConfig.configured = Boolean(data.configured);
    if (!preserveDraft && draftVersion === configDraftVersion) {
      notificationConfig.enabled = Boolean(data.enabled);
      notificationConfig.webhookUrl = ""; notificationConfig.clearWebhook = false;
      notificationConfig.mentionedList = Array.isArray(data.mentionedList) ? data.mentionedList.join(",") : String(data.mentionedList || "").trim();
      configDirty.value = false;
    }
    configLoaded.value = true;
    savedNotificationVersion += 1; actionFailures.notificationTest.clear();
  } catch (error) {
    if (owns() && version === configReadVersion) {
      errorMessage.value = "企业微信配置加载失败，请重试。";
      actionFailures.pageRead.show(error, () => owns() && version === configReadVersion, "企业微信配置加载失败，请重试。", "config");
    }
  } finally { if (owns() && version === configReadVersion) configLoading.value = false; }
}
async function saveNotificationConfig() {
  if (!canManage() || !configLoaded.value || configSaving.value || configTesting.value) return;
  const owns = capturePage(true), draftVersion = configDraftVersion;
  const ownsFailure = () => owns() && configLoaded.value && draftVersion === configDraftVersion;
  actionFailures.configSave.clear(); actionFailures.notificationTest.clear();
  configSaving.value = true;
  try {
    const payload = {
      enabled: notificationConfig.enabled,
      clearWebhook: notificationConfig.clearWebhook,
      mentionedList: notificationConfig.mentionedList
        .split(",")
        .map((item) => item.trim())
        .filter(Boolean),
    };
    if (notificationConfig.webhookUrl.trim())
      payload.webhookUrl = notificationConfig.webhookUrl.trim();
    await saveTicketNotificationConfig(payload);
    if (!owns()) return;
    const changed = draftVersion !== configDraftVersion;
    if (!changed) notificationConfig.webhookUrl = "";
    savedNotificationVersion += 1; actionFailures.notificationTest.clear();
    message.success("企业微信提醒设置已保存");
    await loadNotificationConfig(changed);
  } catch (error) {
    if (owns()) { actionFailures.configSave.show(error, ownsFailure); message.error("企业微信提醒设置保存失败，请核对配置后重试。"); }
  } finally {
    if (owns()) configSaving.value = false;
  }
}
async function testNotification() {
  if (!canManage() || !configLoaded.value || configSaving.value || configTesting.value) return;
  const owns = capturePage(true);
  const savedVersion = savedNotificationVersion, ownsFailure = () => owns() && configLoaded.value && savedVersion === savedNotificationVersion;
  actionFailures.notificationTest.clear();
  configTesting.value = true;
  try {
    await testTicketNotification();
    if (owns()) message.success("测试消息已发送");
  } catch (error) {
    if (owns()) { actionFailures.notificationTest.show(error, ownsFailure); message.error("测试消息发送失败，请核对已保存配置后重试。"); }
  } finally {
    if (owns()) configTesting.value = false;
  }
}
onMounted(() => { loadTickets(); if (canManage()) loadNotificationConfig(); });
onBeforeUnmount(() => {
  ++listRequestVersion;
  ++detailRequestVersion;
});
watch(
  () => route.params.id,
  (id) => {
    retireDetailActionFailures();
    if (id && canUseTickets()) loadDetail(String(id));
    else {
      ++detailRequestVersion;
      detailVisible.value = false;
      detailLoading.value = false;
      detailTicket.value = null;
    }
  },
  { immediate: true }
);
</script>

<style scoped>
.tickets-page {
  display: flex;
  flex-direction: column;
  gap: 18px;
  min-width: 0;
}
.config-grid {
  display: grid;
  grid-template-columns: 180px minmax(280px, 1fr) minmax(220px, 0.7fr);
  gap: 16px;
}
.config-hint {
  color: var(--console-muted);
  font-size: 13px;
}
.filter-form {
  margin-bottom: 18px;
}
.ticket-subject {
  display: block;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}
.time-cell {
  white-space: pre-wrap;
}
.mobile-ticket-list {
  display: none;
}
.config-address-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: 6px;
  color: var(--console-muted);
  font-size: 12px;
}
.detail-summary {
  display: flex;
  flex-wrap: wrap;
  gap: 10px 16px;
  align-items: center;
  color: var(--console-muted);
  font-size: 13px;
}
.notification-alert {
  margin-top: 16px;
}
.message-thread {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin: 20px 0;
}
.message-item {
  max-width: 88%;
  padding: 12px 16px;
  border: 1px solid var(--console-border);
  border-radius: 10px 10px 10px 2px;
  background: var(--console-subtle);
}
.message-item.is-admin {
  align-self: flex-end;
  border-radius: 10px 10px 2px 10px;
  background: color-mix(
    in srgb,
    var(--console-accent) 8%,
    var(--console-surface)
  );
}
.message-item header {
  display: flex;
  justify-content: space-between;
  gap: 18px;
  color: var(--console-muted);
  font-size: 12px;
}
.message-item header strong {
  color: var(--console-text);
}
.message-item p {
  margin: 8px 0 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  line-height: 1.75;
}
.detail-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  padding: 14px 0;
  border-top: 1px solid var(--console-border);
}
.reply-form {
  padding-top: 14px;
  border-top: 1px solid var(--console-border);
}
.ticket-card {
  cursor: pointer;
}
.ticket-card + .ticket-card {
  margin-top: 10px;
}
.ticket-card-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}
.ticket-card-heading strong {
  min-width: 0;
  overflow-wrap: anywhere;
}
.ticket-card-meta {
  display: flex;
  gap: 20px;
  margin: 10px 0 0;
  color: var(--console-muted);
  font-size: 12px;
}
.ticket-card-meta div {
  display: flex;
  gap: 6px;
}
.ticket-card-meta dt {
  font-weight: 600;
}
.ticket-card-meta dd {
  margin: 0;
}
@media (max-width: 900px) {
  .config-grid {
    grid-template-columns: 1fr 1fr;
  }
  .config-grid .ant-form-item:nth-child(2) {
    grid-column: 1 / -1;
  }
}
@media (max-width: 640px) {
  .config-grid {
    grid-template-columns: 1fr;
  }
  .config-grid .ant-form-item:nth-child(2) {
    grid-column: auto;
  }
  .desktop-ticket-table {
    display: none;
  }
  .mobile-ticket-list {
    display: block;
  }
  .filter-form {
    display: flex;
    flex-direction: column;
    align-items: stretch;
  }
  .filter-form .ant-form-item {
    margin-inline-end: 0;
  }
  .filter-form .ant-input-search,
  .filter-form .ant-select {
    width: 100% !important;
  }
  .message-item {
    max-width: 100%;
  }
}
</style>
