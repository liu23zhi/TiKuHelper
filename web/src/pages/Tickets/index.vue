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
        ><a-button :loading="listLoading" @click="loadTickets">刷新</a-button
        ><a-button v-if="!isAdmin" type="primary" @click="openCreate"
          >提交工单</a-button
        ></template
      >
    </ConsolePageHeading>
    <a-alert
      v-if="errorMessage"
      type="error"
      show-icon
      :message="errorMessage"
      closable
      @close="errorMessage = ''"
    />
    <ConsolePanel
      v-if="isAdmin"
      title="企业微信工单提醒"
      description="提醒以异步队列发送，保存后可发送测试消息。Webhook 地址不会回显完整内容。"
      class="settings-panel"
    >
      <a-form
        :model="notificationConfig"
        layout="vertical"
        @finish="saveNotificationConfig"
      >
        <div class="config-grid">
          <a-form-item label="启用提醒"
            ><a-switch
              v-model:checked="notificationConfig.enabled"
              checked-children="开启"
              un-checked-children="关闭"
          /></a-form-item>
          <a-form-item
            label="Webhook 地址"
            help="只接受企业微信机器人 HTTPS 地址；留空表示保持已保存地址。"
          >
            <a-input
              v-model:value="notificationConfig.webhookUrl"
              type="url"
              allow-clear
              placeholder="输入新地址以替换已保存地址"
              @input="notificationConfig.clearWebhook = false"
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
                @click="
                  notificationConfig.clearWebhook = true;
                  notificationConfig.webhookUrl = '';
                "
                >清除已保存地址</a-button
              >
              <a-button
                v-else
                type="link"
                size="small"
                @click="notificationConfig.clearWebhook = false"
                >保留已保存地址</a-button
              >
            </div>
          </a-form-item>
          <a-form-item label="@成员 ID" help="多个成员用逗号分隔，可填写 @all。"
            ><a-input
              v-model:value="notificationConfig.mentionedList"
              allow-clear
              placeholder="成员 ID，可选"
          /></a-form-item>
        </div>
        <a-space
          ><a-button type="primary" html-type="submit" :loading="configSaving"
            >保存提醒设置</a-button
          ><a-button
            :loading="configTesting"
            :disabled="configSaving"
            @click="testNotification"
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
      <a-form layout="inline" class="filter-form" @submit.prevent="loadTickets">
        <a-form-item label="关键词"
          ><a-input-search
            v-model:value="keywordInput"
            allow-clear
            placeholder="标题 / 工单号 / 内容"
            style="width: 280px"
            @search="applyFilters"
            @press-enter="applyFilters"
        /></a-form-item>
        <a-form-item v-if="isAdmin" label="状态"
          ><a-select
            v-model:value="filters.status"
            allow-clear
            placeholder="全部状态"
            style="width: 150px"
            :options="statusOptions"
            @change="applyFilters"
        /></a-form-item>
        <a-form-item v-if="isAdmin" label="创建日期"
          ><a-input
            v-model:value="filters.createdFrom"
            type="date"
            aria-label="创建日期起始"
            @change="applyFilters"
        /></a-form-item>
        <a-form-item v-if="isAdmin"
          ><a-input
            v-model:value="filters.createdTo"
            type="date"
            aria-label="创建日期截止"
            @change="applyFilters"
        /></a-form-item>
        <a-form-item
          ><a-button @click="resetFilters">重置</a-button></a-form-item
        >
      </a-form>
      <div class="mobile-ticket-list">
        <a-card
          v-for="ticket in tickets"
          :key="ticket.id"
          class="ticket-card"
          size="small"
          @click="openDetail(ticket.id)"
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
          @change="handleMobilePageChange"
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
          @change="handleTableChange"
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
              @click="openDetail(record.id)"
              >查看详情</a-button
            ></template
          ><template #emptyText
            ><a-empty description="暂无工单" /></template></a-table
      ></ConsoleTableShell>
    </ConsolePanel>
    <a-modal
      v-model:open="createVisible"
      title="提交工单"
      :confirm-loading="createLoading"
      ok-text="提交"
      cancel-text="取消"
      @ok="submitTicket"
      ><a-form layout="vertical"
        ><a-form-item label="标题" required
          ><a-input
            v-model:value="draft.subject"
            :maxlength="120"
            show-count
            placeholder="请简要描述问题" /></a-form-item
        ><a-form-item label="问题描述" required
          ><a-textarea
            v-model:value="draft.body"
            :maxlength="10000"
            show-count
            :rows="8"
            placeholder="请提供复现步骤、错误提示和期望结果" /></a-form-item></a-form
    ></a-modal>
    <a-modal
      v-model:open="detailVisible"
      :title="detailTitle"
      :footer="null"
      width="820px"
      @cancel="closeDetail"
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
                @click="retryNotification"
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
          <div v-if="isAdmin" class="detail-actions">
            <a-select
              v-model:value="detailStatus"
              style="width: 150px"
              :options="statusOptions"
              @change="statusWasChosen = true"
            /><a-button :loading="statusSaving" @click="saveStatus"
              >更新状态</a-button
            >
          </div>
          <a-form
            layout="vertical"
            class="reply-form"
            @submit.prevent="submitReply"
            ><a-form-item label="追加回复"
              ><a-textarea
                v-model:value="replyBody"
                :maxlength="10000"
                show-count
                :rows="5"
                placeholder="请输入回复内容" /></a-form-item
            ><a-button
              type="primary"
              :loading="replyLoading"
              :disabled="!replyBody.trim()"
              @click="submitReply"
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
  onBeforeUnmount,
  onMounted,
  reactive,
  ref,
  watch,
} from "vue";
import { useRoute, useRouter } from "vue-router";
import { message } from "ant-design-vue";
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
import { getStoredCurrentUser, isAdminUser } from "@/utils/currentUser";
import {
  buildTicketListParams,
  formatTicketTime,
  normalizeTicketDetailResponse,
  normalizeTicketListResponse,
  notificationStatusText,
  ticketStatusColor,
  ticketStatusLabel,
  TICKET_STATUS_OPTIONS,
  validateTicketDraft,
} from "@/utils/ticketHelpers.mjs";

const currentUser = ref(getStoredCurrentUser());
const route = useRoute();
const router = useRouter();
let listRequestVersion = 0;
let detailRequestVersion = 0;
const isAdmin = computed(() => isAdminUser(currentUser.value));
const tickets = ref([]);
const listLoading = ref(false);
const errorMessage = ref("");
const filters = reactive({
  keyword: "",
  status: "",
  createdFrom: "",
  createdTo: "",
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
const detailVisible = ref(false);
const detailLoading = ref(false);
const detailTicket = ref(null);
const messages = ref([]);
const notification = ref(null);
const replyBody = ref("");
const replyLoading = ref(false);
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
  const version = ++listRequestVersion;
  listLoading.value = true;
  errorMessage.value = "";
  try {
    const result = normalizeTicketListResponse(
      await getTickets(
        buildTicketListParams(filters, pagination.current, pagination.pageSize)
      )
    );
    if (version !== listRequestVersion) return;
    tickets.value = result.items;
    pagination.total = result.total;
  } catch (error) {
    if (version === listRequestVersion)
      errorMessage.value = responseMessage(error, "工单列表加载失败");
  } finally {
    if (version === listRequestVersion) listLoading.value = false;
  }
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
  draft.subject = "";
  draft.body = "";
  createVisible.value = true;
}
async function submitTicket() {
  const validation = validateTicketDraft(draft.subject, draft.body);
  if (validation) {
    message.warning(validation);
    return;
  }
  createLoading.value = true;
  try {
    await createTicket({
      subject: draft.subject.trim(),
      body: draft.body.trim(),
    });
    message.success("工单已提交");
    createVisible.value = false;
    pagination.current = 1;
    await loadTickets();
  } catch (error) {
    message.error(responseMessage(error, "工单提交失败"));
  } finally {
    createLoading.value = false;
  }
}
function openDetail(id) {
  return router.push(`/adapter/tickets/${encodeURIComponent(id)}`);
}
async function loadDetail(id) {
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
    if (version !== detailRequestVersion) return;
    detailTicket.value = result.ticket;
    messages.value = result.messages;
    notification.value = result.notification;
    detailStatus.value = result.ticket?.status || "";
  } catch (error) {
    if (version !== detailRequestVersion) return;
    message.error(responseMessage(error, "工单详情加载失败"));
    detailVisible.value = false;
  } finally {
    if (version === detailRequestVersion) detailLoading.value = false;
  }
}
function closeDetail() {
  ++detailRequestVersion;
  detailVisible.value = false;
  detailTicket.value = null;
  router.replace("/adapter/tickets");
}
async function submitReply() {
  const body = String(replyBody.value || "").trim();
  if (!body) return;
  const ticketId = detailTicket.value?.id;
  const version = detailRequestVersion;
  if (!ticketId) return;
  replyLoading.value = true;
  try {
    const payload = { body };
    if (isAdmin.value && statusWasChosen.value)
      payload.status = detailStatus.value;
    const result = normalizeTicketDetailResponse(
      await addTicketMessage(ticketId, payload)
    );
    if (
      version === detailRequestVersion &&
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
      detailStatus.value = detailTicket.value.status;
      statusWasChosen.value = false;
      replyBody.value = "";
    }
    await loadTickets();
    message.success("回复已发送");
  } catch (error) {
    message.error(responseMessage(error, "回复发送失败"));
  } finally {
    replyLoading.value = false;
  }
}
async function saveStatus() {
  if (!detailTicket.value || !detailStatus.value) return;
  const ticketId = detailTicket.value.id;
  const status = detailStatus.value;
  const version = detailRequestVersion;
  statusSaving.value = true;
  try {
    const result = normalizeTicketDetailResponse(
      await updateTicket(ticketId, { status })
    );
    if (
      version === detailRequestVersion &&
      detailTicket.value?.id === ticketId
    ) {
      detailTicket.value = result.ticket || {
        ...detailTicket.value,
        status,
      };
      detailStatus.value = detailTicket.value.status;
      messages.value = result.messages.length
        ? result.messages
        : messages.value;
      notification.value = result.notification || notification.value;
      statusWasChosen.value = false;
    }
    await loadTickets();
    message.success("工单状态已更新");
  } catch (error) {
    message.error(responseMessage(error, "工单状态更新失败"));
  } finally {
    statusSaving.value = false;
  }
}
async function retryNotification() {
  if (!detailTicket.value?.id || !notification.value?.id) return;
  const ticketId = detailTicket.value.id;
  const notificationId = notification.value.id;
  const version = detailRequestVersion;
  try {
    await retryTicketNotification(ticketId, notificationId);
    if (
      version === detailRequestVersion &&
      detailTicket.value?.id === ticketId
    ) {
      notification.value = {
        ...notification.value,
        status: "pending",
        lastError: "",
      };
    }
    message.success("提醒已加入重试队列");
  } catch (error) {
    message.error(responseMessage(error, "提醒重试失败"));
  }
}
async function loadNotificationConfig() {
  try {
    const response = await getTicketNotificationConfig();
    const data = response?.data?.data || response?.data || {};
    notificationConfig.enabled = Boolean(data.enabled);
    notificationConfig.configured = Boolean(data.configured);
    notificationConfig.webhookUrl = "";
    notificationConfig.clearWebhook = false;
    notificationConfig.mentionedList = Array.isArray(data.mentionedList)
      ? data.mentionedList.join(",")
      : String(data.mentionedList || "").trim();
  } catch (error) {
    errorMessage.value = responseMessage(error, "企业微信配置加载失败");
  }
}
async function saveNotificationConfig() {
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
    notificationConfig.webhookUrl = "";
    message.success("企业微信提醒设置已保存");
    await loadNotificationConfig();
  } catch (error) {
    message.error(responseMessage(error, "企业微信提醒设置保存失败"));
  } finally {
    configSaving.value = false;
  }
}
async function testNotification() {
  configTesting.value = true;
  try {
    await testTicketNotification();
    message.success("测试消息已发送");
  } catch (error) {
    message.error(responseMessage(error, "测试消息发送失败"));
  } finally {
    configTesting.value = false;
  }
}
onMounted(() => {
  loadTickets();
  if (isAdmin.value) loadNotificationConfig();
});
onBeforeUnmount(() => {
  ++listRequestVersion;
  ++detailRequestVersion;
});
watch(
  () => route.params.id,
  (id) => {
    if (id) loadDetail(String(id));
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
