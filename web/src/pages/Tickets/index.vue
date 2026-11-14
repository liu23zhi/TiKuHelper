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
      <!-- 顶部操作区只有「刷新」与「提交工单」。
           ⚠️ 这里**不再**有「退款工单」这类按类型切换的独立入口按钮
           （用户 2026-11-14 明确要求：所有工单都在同一个工单列表里看，
           不要为某一种类型单独提供显示按钮）。退款区块现在常驻在列表页内，
           见下方 TicketRefundPanel —— 功能一个没少，只是不再需要一个"入口按钮"。 -->
      <template #actions
        ><a-button :loading="listLoading" :disabled="!canUseTickets()" :onClick="pageEntry.bind(refreshPage)">刷新</a-button
        ><a-button v-if="canUseTickets() && !isAdmin" type="primary" :onClick="pageEntry.bind(openCreate)"
          >提交工单</a-button
        ></template
      >
    </ConsolePageHeading>
    <!-- 退款区块常驻（不再是"点按钮才展开"的独立入口）。
         保留它是因为它是全站**唯一**的退款申请/审核入口：
           · 用户侧：选可退款订单、提交原路/人工退款申请、上传凭证、查看进度
           · 管理侧：审核通过/驳回（冻结余额）、提交原路退款、撤销解冻、对账打款
         退款记录在独立表 ticket_refunds（entity/ticket_refund.go:17 注释
         「separate from general-purpose tickets」，无 ticket_id 外键），
         所以它无法并进 /tickets 列表；能做的、也正是用户要的，是
         去掉那个「要点一下才看得到」的按类型入口按钮。
         权限沿用 canUseTickets()：未登录/禁用账号不会渲染该区块。

         ⚠️ 必须有**明确的分界**：它直接铺在工单列表页里，用户会分不清
         「这是工单还是退款」。所以外面套一层带标题 + 说明的区块容器，
         并用 data-section 打稳定标记（测试与将来迁移都靠它定位）。
         **不要静默铺一块面板在列表下面。** -->
    <section
      v-if="canUseTickets()"
      data-section="ticket-refund"
      class="refund-section"
      aria-labelledby="ticket-refund-heading"
    >
      <header class="refund-section-heading">
        <h2 id="ticket-refund-heading" class="refund-section-title">退款申请与审核</h2>
        <p class="refund-section-lead">退款走独立流程，不进入上方工单列表。</p>
      </header>
      <TicketRefundPanel
        :key="pageEpoch"
        :is-admin="isAdmin"
        :capture-owner="capturePage"
      />
    </section>
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
        <!-- 分类筛选放在状态/日期之后：前面插入同级控件会让「取第一个 .ant-select」
             这类位置选择器失效（历史坑），新控件一律追加在末尾。 -->
        <a-form-item v-if="isAdmin" label="分类"
          ><a-select
            :value="filters.category" :onUpdate:value="pageEntry.bind(setFilter, 'category')"
            allow-clear
            placeholder="全部类别"
            style="width: 150px"
            :options="categoryOptions"
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
            ><span class="ticket-card-tags"
              ><a-tag v-if="ticketCategoryLabel(ticket.category)" :color="ticketCategoryColor(ticket.category)">{{
                ticketCategoryLabel(ticket.category)
              }}</a-tag
              ><a-tag v-if="isSystemTicket(ticket)" color="geekblue">{{ TICKET_SYSTEM_ORIGIN_LABEL }}</a-tag
              ><a-tag :color="ticketStatusColor(ticket.status)">{{
                ticketStatusLabel(ticket.status)
              }}</a-tag></span
            >
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
          :scroll="{ x: isAdmin ? 900 : 760 }"
          :onChange="pageEntry.bind(handleTableChange)"
          ><template #bodyCell="{ column, record }"
            ><span v-if="column.key === 'subject'" class="ticket-subject"
              ><a-tag v-if="isSystemTicket(record)" color="geekblue">{{ TICKET_SYSTEM_ORIGIN_LABEL }}</a-tag
              >{{ record.subject || `工单 #${record.id}` }}</span
            ><span v-else-if="column.key === 'userName'">{{
              record.userName || `用户 #${record.userId || "-"}`
            }}</span
            ><span v-else-if="column.key === 'category'">{{
              ticketCategoryLabel(record.category) || "—"
            }}</span
            ><a-tag
              v-else-if="column.key === 'status'"
              :color="ticketStatusColor(record.status)"
              >{{ ticketStatusLabel(record.status) }}</a-tag
            ><!-- T-209：退款行必须显示**退款真实状态**（7 态），
                 而不是工单那 4 态的映射结果 —— 「冻结中 / 处理中 / 结果待核对」
                 才是管理员判断该不该重试打款的依据。 -->
            <span v-else-if="column.key === 'refundStatus'" class="refund-status-cell"
              ><a-tag v-if="isRefundTicketRow(record) && ticketRowRefundStatus(record)" color="gold">{{
                refundStatusLabel(ticketRowRefundStatus(record))
              }}</a-tag
              ><span v-else>—</span></span
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
        ><a-alert v-if="createSaved" type="info" show-icon message="提交的工单已保存，当前修改尚未提交。" />
      <!-- T-209：新建时先选工单类型。只有两项可选（都不带管理员侧一键动作），
           规则与理由见 utils/ticketHelpers.mjs 的 TICKET_CREATE_TYPE_OPTIONS。 -->
      <a-form-item label="工单类型" required :html-for="createFieldId('type')"
        ><a-select
          :id="createFieldId('type')"
          :value="draft.ticketType"
          :options="createTypeOptions"
          :disabled="createLoading"
          @update:value="createEntry.bind(setDraft, 'ticketType')" />
        <template #help
          ><span>{{ createTypeHint }}</span></template
        ></a-form-item
      >
      <!-- 退款类型：字段整体切换成退款需要的（订单 / 金额 / 理由），
           而不是只把分类字符串改一下。 -->
      <template v-if="createNeedsRefundFields">
        <a-form-item label="退款订单" :html-for="createFieldId('order')">
          <a-select
            :id="createFieldId('order')"
            :value="draft.refundOrderId"
            :options="refundOrderOptions"
            allow-clear
            show-search
            option-filter-prop="label"
            :loading="refundOrdersLoading"
            :disabled="createLoading"
            placeholder="不选表示无订单的人工退款"
            @update:value="createEntry.bind(setDraft, 'refundOrderId')" />
          <template #help
            ><span>选择一笔已支付的充值订单可原路退回；不选则按人工打款处理，需管理员核对凭证。</span></template
          >
        </a-form-item>
        <a-form-item label="退款金额（元）" required :validate-status="createValidationMessage('requestedYuan') ? 'error' : undefined"
          ><a-input
            :id="createFieldId('amount')"
            :value="draft.requestedYuan" :onUpdate:value="createEntry.bind(setDraft, 'requestedYuan')"
            :aria-invalid="createValidationMessage('requestedYuan') ? 'true' : undefined"
            placeholder="例如 25.50"
            :disabled="createLoading" /><template #help><span v-if="createValidationMessage('requestedYuan')" role="alert">{{ createValidationMessage('requestedYuan') }}</span><span v-else>最多两位小数</span></template></a-form-item
        >
        <a-form-item label="退款理由" required :validate-status="createValidationMessage('reason') ? 'error' : undefined"
          ><a-textarea
            :id="createFieldId('reason')"
            :value="draft.reason" :onUpdate:value="createEntry.bind(setDraft, 'reason')"
            :aria-invalid="createValidationMessage('reason') ? 'true' : undefined"
            :maxlength="500"
            show-count
            :rows="4"
            :disabled="createLoading"
            placeholder="请说明退款原因" /><template #help><span v-if="createValidationMessage('reason')" role="alert">{{ createValidationMessage('reason') }}</span></template></a-form-item
        >
      </template>
      <template v-else
        ><a-form-item label="标题" required :validate-status="createValidationMessage('subject') ? 'error' : undefined"
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
            placeholder="请提供复现步骤、错误提示和期望结果" /><template #help><span v-if="createValidationMessage('body')" :id="createValidationErrorId('body')" role="alert">{{ createValidationMessage('body') }}</span></template></a-form-item
        ><a-form-item
          label="截图（可选）"
          help="支持 PNG / JPEG，单张不超过 2 MiB。图片上传后先做安全检测，检测未通过时只显示提示。"
          ><label class="ticket-attachment-input">
            <span>选择图片（最多 {{ TICKET_ATTACHMENT_MAX_PER_MESSAGE }} 张）</span>
            <input
              type="file"
              :accept="TICKET_ATTACHMENT_ACCEPT"
              :disabled="createLoading || uploadBusy"
              multiple
              @change="onPickDraftImages"
            /></label>
          <div v-if="draftAttachments.length" class="ticket-attachment-selected">
            <span v-for="item in draftAttachments" :key="item.id" class="ticket-attachment-chip"
              >#{{ item.id }}
              <a-button
                type="link"
                size="small"
                danger
                :onClick="createEntry.bind(removeDraftImage, item.id)"
                >移除</a-button
              ></span
            >
          </div>
        </a-form-item></template
      ></a-form
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
            ><a-tag
              v-if="ticketCategoryLabel(detailTicket.category)"
              :color="ticketCategoryColor(detailTicket.category)"
              >{{ ticketCategoryLabel(detailTicket.category) }}</a-tag
            ><a-tag v-if="isSystemTicket(detailTicket)" color="geekblue"
              >{{ TICKET_SYSTEM_ORIGIN_LABEL }}</a-tag
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
              <!-- 工单正文**只做展示遮挡**，绝不阻断提交（用户原话：工单系统要允许
                   敏感词，避免因为误判而导致无法处理，只是用户端做隐藏）。
                   后端未下发 sensitiveFlag 时本组件退化为纯文本，行为与今天一致。 -->
              <p>
                <SensitiveText
                  :text="item.body"
                  :is-sensitive="item.sensitiveFlag === true"
                  :server-masked="item.sensitiveMasked === true"
                  :can-reveal="isAdmin"
                />
              </p>
              <div
                v-if="attachmentsForMessage(item.id).length"
                class="message-attachments"
              >
                <TicketAttachment
                  v-for="attachment in attachmentsForMessage(item.id)"
                  :key="attachment.id"
                  :attachment="attachment"
                  :admin="isAdmin"
                />
              </div>
            </article>
          </div>
          <!-- 未绑定到某条消息的图片（历史数据 / 上传后未绑定）：单独一组展示。
               本组在 message-item 循环**之外**，没有对应正文，因此不涉及敏感词遮挡
               （上面的 <SensitiveText> 只包 item.body，包裹关系是兄弟节点而非嵌套，
               绝不会把附件一起遮掉）。
               若将来为这一组加说明文字/标题，该文字必须同样用 <SensitiveText> 包裹，
               否则会成为绕过遮挡的泄露通道。 -->
          <div
            v-if="attachmentsForMessage(null).length"
            class="message-attachments unbound-attachments"
          >
            <TicketAttachment
              v-for="attachment in attachmentsForMessage(null)"
              :key="attachment.id"
              :attachment="attachment"
              :admin="isAdmin"
            />
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
            ><a-form-item label="截图（可选）"
              ><label class="ticket-attachment-input">
                <span>选择图片（最多 {{ TICKET_ATTACHMENT_MAX_PER_MESSAGE }} 张）</span>
                <input
                  type="file"
                  :accept="TICKET_ATTACHMENT_ACCEPT"
                  :disabled="replyLoading || uploadBusy || !detailTicket"
                  multiple
                  @change="onPickReplyImages"
                /></label>
              <div v-if="replyAttachments.length" class="ticket-attachment-selected">
                <span v-for="item in replyAttachments" :key="item.id" class="ticket-attachment-chip"
                  >#{{ item.id }}
                  <a-button
                    type="link"
                    size="small"
                    danger
                    :onClick="detailEntry.bind(removeReplyImage, item.id)"
                    >移除</a-button
                  ></span
                >
              </div></a-form-item
            ><a-button
              type="primary"
              :loading="replyLoading"
              :disabled="!replyBody.trim() || uploadBusy"
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
  defineAsyncComponent,
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
import TicketAttachment from "@/components/TicketAttachment.vue";
// 敏感文本遮挡：默认遮挡，仅管理员可点「显示原文」。
// 工单正文**只做展示遮挡**，不参与任何提交校验 —— 误判不能让用户处理不了工单。
import SensitiveText from "@/components/SensitiveText.vue";
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
  uploadTicketAttachment,
  listTicketAttachments,
} from "@/api/TicketApi";
import {
  groupTicketAttachmentsByMessage,
  pickTicketAttachmentIds,
  validateTicketAttachmentFile,
  TICKET_ATTACHMENT_ACCEPT,
  TICKET_ATTACHMENT_MAX_PER_MESSAGE,
} from "@/utils/ticketAttachmentHelpers.mjs";
import { useCurrentUserDisplay, isAdminUser } from "@/utils/currentUser";
import { useCurrentUserRequestScope } from "@/utils/currentUserScope";
import {
  buildTicketListParams,
  formatTicketTime,
  isSystemTicket,
  normalizeTicketDetailResponse,
  normalizeTicketListResponse,
  notificationStatusText,
  ticketCategoryColor,
  ticketCategoryLabel,
  ticketStatusColor,
  ticketStatusLabel,
  TICKET_CATEGORY_OPTIONS,
  TICKET_STATUS_OPTIONS,
  TICKET_SYSTEM_ORIGIN_LABEL,
  getTicketDraftValidation,
} from "@/utils/ticketHelpers.mjs";
// 退款 7 态的中文文案、金额格式化与可退订单列表都与退款面板共用同一份实现，
// 避免列表与面板对同一状态/金额给出不同说法。
import { formatRefundCents, refundStatusLabel } from "./refundTicketHelpers.mjs";
// 新建工单时选类型的纯函数（T-209）：类型清单、校验、payload 构造。
import {
  buildTicketCreatePayload,
  getRefundDraftValidation,
  isRefundTicketRow,
  ticketCreateTypeNeedsRefundFields,
  ticketRowRefundStatus,
  TICKET_CREATE_TYPE_OPTIONS,
} from "./createTicketTypeHelpers.mjs";
import { getEligibleTicketRefundOrders } from "@/api/TikuComponentApi";

const detailStatusId = `tickets-${useId()}-detail-status`;
// 退款 7 态的中文文案与退款面板同一份（refundStatusLabel 在上方 import），
// 避免列表与面板对同一状态给出不同说法。
const TicketRefundPanel = defineAsyncComponent(() => import("./TicketRefundPanel.vue"));
const { currentUser } = useCurrentUserDisplay();
const route = useRoute();
const router = useRouter();
let listRequestVersion = 0;
let detailRequestVersion = 0;
let detailFocusEntry = null, detailFocusVersion = 0;
function retireDetailFocus() { detailFocusEntry = null; detailFocusVersion += 1; }
// 视图模式由**入口**决定，而不是只由角色决定。
//
// 为什么必须这样（2026-11-14 用户实测报出的缺陷）：
// 管理员侧边栏同时有「工单反馈」（普通用户可见区）与「工单管理」（仅管理员可见区）
// 两个入口，但它们曾指向同一个 `tickets` 路由。而这里原来只判角色：
//
//     const isAdmin = computed(() => isAdminUser(currentUser.value))
//
// ⇒ 管理员点「工单反馈」进去看到的仍是**管理视图**（标题写着「工单管理」），
// 而「工单管理」那个入口因为没有独立路由，侧边栏也无法单独选中。
//
// 现在改成：路由 meta.viewMode 优先。
//   meta.viewMode === 'self'  → 强制用户视图（工单反馈）：只看自己的单、能新建
//   meta.viewMode === 'admin' → 强制管理视图（工单管理）：全站单、状态筛选
//   未声明                     → 退回按角色判断（保持既有行为，普通用户仍走 self）
//
// 权限边界不在这里：`ticket-admin` 已登记进 router 的 ADMIN_ONLY_ROUTE_NAMES，
// 普通用户直接敲 URL 会被路由守卫挡掉。这里只管「渲染哪个视图」。
const routeViewMode = computed(() => {
  const meta = route?.meta || {};
  return typeof meta.viewMode === 'string' ? meta.viewMode : '';
});
const isAdminRoleUser = computed(() => isAdminUser(currentUser.value));
const isAdmin = computed(() => {
  if (routeViewMode.value === 'self') return false;
  if (routeViewMode.value === 'admin') return true;
  return isAdminRoleUser.value;
});
const tickets = ref([]);
const listLoading = ref(false);
const errorMessage = ref("");
const filters = reactive({
  keyword: "",
  status: "",
  category: "",
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
// 分类筛选/展示用；建单**不**透传 category（服务端按来源强制赋值，
// 用户自选只会变成「挑能一键补发」的套利口子）。
const categoryOptions = TICKET_CATEGORY_OPTIONS.map(({ value, label }) => ({
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
    { title: "分类", dataIndex: "category", key: "category", width: 130 },
    { title: "状态", dataIndex: "status", key: "status", width: 120 },
    // T-209：退款专属列。普通工单这里显示「—」；退款行显示 7 态真实状态。
    // 单开一列而不是塞进「状态」列：两者含义不同（工单状态 vs 资金状态），
    // 合成一格会让管理员以为「工单 closed 就代表钱退了」。
    { title: "退款状态", key: "refundStatus", width: 150 },
    { title: "最近更新", dataIndex: "updatedAt", key: "updatedAt", width: 180 },
    { title: "操作", key: "action", width: 100 }
  );
  return base;
});
const createVisible = ref(false);
const createLoading = ref(false);
// draft 是新建工单表单。T-209 增加了「工单类型」以及退款专属字段。
//
// 退款字段与普通字段**并存**在同一个对象里，但提交时由 buildTicketCreatePayload
// 按类型只挑该传的那些（普通类型绝不带退款字段，反之亦然 —— 后端会对
// 参数矛盾报错，这是有意的检测）。
const draft = reactive({
  ticketType: "general",
  subject: "",
  body: "",
  requestedYuan: "",
  reason: "",
  refundOrderId: undefined,
});
const createValidation = shallowRef(null);
const createValidationErrorId = field => `${detailStatusId}-create-${field}-error`;
// 表单控件的 id（用于 label 的 html-for 与 aria 关联），与错误提示 id 分开。
const createFieldId = field => `${detailStatusId}-create-field-${field}`;
// 当前类型是否需要退款专属字段（任务书：不能只换个 category 字符串）。
const createNeedsRefundFields = computed(() => ticketCreateTypeNeedsRefundFields(draft.ticketType));
const createTypeOptions = TICKET_CREATE_TYPE_OPTIONS.map(({ value, label }) => ({ value, label }));
const createTypeHint = computed(() => {
  const matched = TICKET_CREATE_TYPE_OPTIONS.find((item) => item.value === draft.ticketType);
  return matched ? matched.hint : "";
});
// 可退订单列表：切到退款类型时才拉一次（普通工单不该触发这个请求）。
const refundOrderOptions = ref([]);
const refundOrdersLoading = ref(false);
let refundOrdersLoaded = false;
async function loadRefundOrders() {
  if (refundOrdersLoaded || refundOrdersLoading.value) return;
  refundOrdersLoading.value = true;
  try {
    const response = await getEligibleTicketRefundOrders();
    const rows = Array.isArray(response?.data) ? response.data : [];
    refundOrderOptions.value = rows
      .filter((row) => row && Number.isSafeInteger(Number(row.id)))
      .map((row) => ({
        value: Number(row.id),
        label: `${row.orderNo || "#" + row.id} · 实付 ${formatRefundCents(row.paidCents)} 元`,
      }));
    refundOrdersLoaded = true;
  } catch {
    // 拉取失败不阻断建单：用户仍可提交「无订单的人工退款」，
    // 只是不能选原路退回。错误由消息提示，不把整个弹窗卡住。
    refundOrderOptions.value = [];
  } finally {
    refundOrdersLoading.value = false;
  }
}
function createValidationMessage(field) { return createValidation.value?.field === field ? createValidation.value.message : ""; }
function retireCreateValidation() { createValidation.value = null; }
// 当前表单用的是哪一套校验：随类型切换（退款 / 普通）。
function currentDraftValidation() {
  return ticketCreateTypeNeedsRefundFields(draft.ticketType)
    ? getRefundDraftValidation({ requestedYuan: draft.requestedYuan, reason: draft.reason })
    : getTicketDraftValidation(draft.subject, draft.body);
}
function retireStaleCreateValidation() {
  const previous = createValidation.value;
  if (!previous) return;
  const current = currentDraftValidation();
  if (current.field !== previous.field || current.message !== previous.message) retireCreateValidation();
}
const detailVisible = ref(false);
const detailLoading = ref(false);
const detailTicket = ref(null);
const messages = ref([]);
const attachments = ref([]);
const notification = ref(null);
const replyBody = ref("");
const replyLoading = ref(false);
// 待提交的图片：创建工单时上传的草稿图 / 回复时上传的图。
// 只保留 { id, visible, hiddenReason, scanState }，不保留本地文件对象。
const draftAttachments = ref([]);
const replyAttachments = ref([]);
const uploadBusy = ref(false);
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
  detailTicket.value = null; messages.value = []; attachments.value = []; notification.value = null; replyBody.value = ""; detailStatus.value = ""; statusWasChosen.value = false;
  draft.ticketType = "general"; draft.subject = ""; draft.body = ""; draft.requestedYuan = ""; draft.reason = ""; draft.refundOrderId = undefined; errorMessage.value = "";
  draftAttachments.value = []; replyAttachments.value = []; uploadBusy.value = false;
  Object.assign(notificationConfig, { enabled: false, webhookUrl: "", mentionedList: "", configured: false, clearWebhook: false });
  keywordInput.value = ""; Object.assign(filters, { keyword: "", status: "", category: "", createdFrom: "", createdTo: "", updatedFrom: "", updatedTo: "" });
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
watch([() => draft.subject, () => draft.body, () => draft.requestedYuan, () => draft.reason], retireStaleCreateValidation, { flush: "sync" });
// 切到退款类型时才拉可退订单（普通工单不触发这个请求）。
watch(createNeedsRefundFields, needs => { if (needs) void loadRefundOrders(); }, { flush: "sync" });
watch(createVisible, next => { if (!next) { retireCreateValidation(); createVersion += 1; createLoading.value = false; createSaved.value = false; draft.ticketType = "general"; draft.subject = ""; draft.body = ""; draft.requestedYuan = ""; draft.reason = ""; draft.refundOrderId = undefined; } }, { flush: "sync" });
// Focus leases also retire for same-id non-admin role ABA and unexpected routes.
watch(() => String(currentUser.value?.role ?? ""), retireDetailFocus, { flush: "sync" });
watch(() => route.fullPath, path => {
  if (path !== "/tickets" && path !== detailFocusEntry?.path) retireDetailFocus();
}, { flush: "sync" });
watch(() => currentUser.value?.status, () => { clearOwned(); reloadOwned(); }, { flush: "sync" });
onBeforeUnmount(() => { active = false; retireReplyDraft(); });
onBeforeUnmount(retireActionFailures);
onBeforeUnmount(retireCreateValidation);
const detailTitle = computed(() => detailTicket.value?.subject || "工单详情");
const attachmentGroups = computed(() =>
  groupTicketAttachmentsByMessage(attachments.value)
);
// attachmentsForMessage 返回某条消息的图片；传 null / undefined 得到「未绑定到消息」的一组。
function attachmentsForMessage(messageId) {
  const key = messageId == null ? "ticket" : String(messageId);
  return attachmentGroups.value.get(key) || [];
}
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
  filters.category = "";
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
  draft.ticketType = "general";
  draft.subject = "";
  draft.body = "";
  draft.requestedYuan = "";
  draft.reason = "";
  draft.refundOrderId = undefined;
  createVisible.value = true;
}
async function submitTicket() {
  if (!canUseTickets() || !createVisible.value || createLoading.value) return;
  // 两种类型是**两套字段与两套校验**（任务书明确要求：不能只换个 category 字符串）。
  // 校验在这里统一做一次，失败就停，不发请求。
  const refundMode = ticketCreateTypeNeedsRefundFields(draft.ticketType);
  const validation = refundMode
    ? getRefundDraftValidation({ requestedYuan: draft.requestedYuan, reason: draft.reason })
    : getTicketDraftValidation(draft.subject, draft.body);
  if (validation.message) { createValidation.value = validation; message.warning(validation.message); return; }
  retireCreateValidation();
  const owns = captureCreate(), subject = draft.subject, body = draft.body;
  const failureDraft = createFailureDraft, ownsFailure = () => owns() && failureDraft === createFailureDraft;
  const attachmentIds = pickTicketAttachmentIds(draftAttachments.value);
  actionFailures.create.clear();
  createLoading.value = true; createSaved.value = false;
  try {
    // 没有图片时**不**带 attachmentIds：保持「无附件建单」的请求体与历史完全一致
    // （既有契约测试断言 payload 恰好是 {body, subject}）。
    //
    // 退款类型走 buildTicketCreatePayload 的另一条分支：带 refundRequestedCents /
    // refundReason / refundOrderId，不带 subject/body。
    const built = buildTicketCreatePayload({
      ticketType: draft.ticketType,
      subject, body,
      requestedYuan: draft.requestedYuan,
      reason: draft.reason,
      refundOrderId: draft.refundOrderId,
      attachmentIds,
    });
    const payload = { ...built.payload };
    if (refundMode) {
      // 退款不带草稿附件（退款证据走退款自己的凭证上传，与工单附件是两套）。
      delete payload.attachmentIds;
    }
    await createTicket(payload);
    if (!owns()) return;
    message.success(refundMode ? "退款申请已提交，可在工单列表跟踪进度" : "工单已提交");
    draftAttachments.value = [];
    if (draft.subject === subject && draft.body === body) createVisible.value = false;
    else createSaved.value = true;
    pagination.current = 1; await loadTickets();
  } catch (error) { if (owns()) { actionFailures.create.show(error, ownsFailure); message.error(responseMessage(error, "工单提交失败")); } }
  finally { if (owns()) createLoading.value = false; }
}

// ---- 图片上传 ----
//
// 统一入口：本地预检 → 上传（服务端校验 + 加密 + pending）→ 记录附件 ID。
// 上传**不等待**敏感检测：服务端返回时 scan_state 一定是 pending，
// 因此刚上传的图片在提交前一律按「未通过」展示，提交后在详情里按真实状态显示。
async function uploadImages(fileList, ticketId, target) {
  if (uploadBusy.value) return;
  const files = Array.from(fileList || []);
  if (!files.length) return;
  const current = target === "reply" ? replyAttachments.value : draftAttachments.value;
  const remaining = TICKET_ATTACHMENT_MAX_PER_MESSAGE - current.length;
  if (remaining <= 0) {
    message.warning(`最多添加 ${TICKET_ATTACHMENT_MAX_PER_MESSAGE} 张图片`);
    return;
  }
  uploadBusy.value = true;
  try {
    for (const file of files.slice(0, remaining)) {
      const invalid = validateTicketAttachmentFile(file);
      if (invalid) { message.warning(`${file.name || "图片"}：${invalid}`); continue; }
      const response = await uploadTicketAttachment(file, ticketId);
      const created = response?.data?.data;
      if (!created || !Number.isInteger(Number(created.id))) {
        message.error("图片上传返回格式异常");
        continue;
      }
      if (target === "reply") replyAttachments.value = [...replyAttachments.value, created];
      else draftAttachments.value = [...draftAttachments.value, created];
    }
  } catch (error) {
    message.error(responseMessage(error, "图片上传失败"));
  } finally {
    uploadBusy.value = false;
  }
}

function onPickDraftImages(event) {  const input = event?.target;
  const files = input?.files;
  if (input) input.value = "";
  if (!createVisible.value) return;
  uploadImages(files, null, "create");
}

function onPickReplyImages(event) {
  const input = event?.target;
  const files = input?.files;
  if (input) input.value = "";
  const ticketId = detailTicket.value?.id;
  if (!detailVisible.value || !ticketId) return;
  uploadImages(files, ticketId, "reply");
}

function removeDraftImage(id) {
  if (!createVisible.value) return;
  draftAttachments.value = draftAttachments.value.filter(item => item.id !== id);
}function removeReplyImage(id) {
  if (!detailVisible.value) return;
  replyAttachments.value = replyAttachments.value.filter(item => item.id !== id);
}

// refreshAttachments 在「回复返回体里没有 attachments」时补齐一次列表。
// 失败静默：图片是附属信息，不该打断回复成功的提示。
async function refreshAttachments(ticketId) {
  try {
    const response = await listTicketAttachments(ticketId);
    const rows = response?.data?.data;
    if (Array.isArray(rows) && detailTicket.value?.id === ticketId) attachments.value = rows;
  } catch { /* 静默降级 */ }
}
function openDetail(id, event) {
  if (!canUseTickets()) return;
  retireDetailFocus();
  const path = `/tickets/${encodeURIComponent(id)}`, opener = event?.currentTarget;
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
  attachments.value = [];
  notification.value = null;
  replyBody.value = "";
  replyAttachments.value = [];
  statusWasChosen.value = false;
  try {
    const result = normalizeTicketDetailResponse(await getTicket(id));
    if (!owns() || version !== detailRequestVersion) return;
    detailTicket.value = result.ticket;
    messages.value = result.messages;
    attachments.value = result.attachments;
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
  replyBody.value = ""; replyAttachments.value = []; messages.value = []; attachments.value = []; notification.value = null;
  detailVisible.value = false;
  detailTicket.value = null;
  // The original key/version retirement unmounts the old Modal before afterClose.
  // Use one owned continuation after navigation/disposal, not the dead vendor callback.
  return router.replace("/tickets").then(async failure => {
    if (failure || !focus) return failure;
    await nextTick(); await nextTick();
    if (!focus.owns() || ticket !== detailFocusVersion || create !== createVersion
      || route.fullPath !== "/tickets" || route.params.id || detailVisible.value || createVisible.value) return;
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
  const attachmentIds = pickTicketAttachmentIds(replyAttachments.value);
  clearReplyFailure();
  replyLoading.value = true;
  try {
    const payload = { body };
    if (attachmentIds.length) payload.attachmentIds = attachmentIds;
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
      // 服务端在绑定成功时会给回最新附件列表（含刚绑定到本条消息的图片）。
      if (Array.isArray(result.attachments)) attachments.value = result.attachments;
      else await refreshAttachments(ticketId);
      replyAttachments.value = [];
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
async function saveStatus() {  if (!canManage() || statusSaving.value || !detailVisible.value) return;
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
/* 退款区块的视觉分界：它和工单列表是两条独立流程，不能看起来像同一份数据。
   左侧色条 + 独立标题，让用户一眼看出「这块不是工单列表」。 */
.refund-section {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding-left: 14px;
  border-left: 3px solid var(--console-accent);
}
.refund-section-heading {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.refund-section-title {
  margin: 0;
  color: var(--console-text);
  font-size: 16px;
  font-weight: 600;
}
.refund-section-lead {
  margin: 0;
  color: var(--console-muted);
  font-size: 13px;
  line-height: 1.6;
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
.message-attachments {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 10px;
}
.unbound-attachments {
  margin: 0 0 20px;
}
.ticket-attachment-input {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  color: var(--console-muted);
  font-size: 13px;
}
.ticket-attachment-input input[type="file"] {
  max-width: 100%;
}
.ticket-attachment-selected {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 8px;
}
.ticket-attachment-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 4px 2px 10px;
  border: 1px solid var(--console-border);
  border-radius: 999px;
  background: var(--console-subtle);
  font-size: 12px;
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
/* 分类 / 系统来源 / 状态三枚标签：换行时靠右收拢，避免把标题挤成一列。 */
.ticket-card-tags {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px;
  justify-content: flex-end;
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
