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
           退款操作在工单详情里（TicketRefundActions）—— 功能一个没少，只是不再需要入口按钮。 -->
      <template #actions
        ><a-button :loading="listLoading" :disabled="!canUseTickets()" :onClick="pageEntry.bind(refreshPage)">刷新</a-button
        ><!-- ⚠️ 全页**唯一**的建单入口（B2-3 / 判据 T6）。
             不要在这里再加第二个「发起」类按钮，也不要在别处提供「直接新建某类型」
             的入口 —— 类型选择已经收进向导第一步。 -->
        <!-- 创建入口由当前视图决定：管理员也可以在「工单反馈」提交自己的工单。 -->
        <!-- 通知设置入口：只在管理视图出现（它是管理员配置）。 -->         <!-- 只判 canManage，不判 configLoaded：后者是异步的，会在页面刚打开的一瞬间
             让按钮消失（管理员会以为没有这个功能）。配置未就绪的提示由弹窗内部处理。 -->
        <a-button v-if="canManage()" :onClick="pageEntry.bind(openNotificationConfig)"           >提醒设置</a-button         >         <a-button v-if="canCreateTickets()" type="primary" :onClick="pageEntry.bind(openCreate)"
          >发起工单</a-button
        ></template
      >
    </ConsolePageHeading>
    <!-- 2026-11-18 重构：原 TicketRefundPanel 整块（退款申请/审核工作台）已删除。
         理由：数据早已合并进 ticket 表，UI 却是第二套 —— 用户在「工单列表」下方
         又看到一个「退款申请与审核」区块，等于同一份数据两个列表。
         现在退款操作收进**工单详情弹窗**（见 TicketRefundActions.vue）：
           点开一张退款工单 → 详情里显示金额/状态，管理员可直接审核、打款。
         提交入口仍然是「发起工单 → 退款申请」，不另设按钮。 -->
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
    <!-- 通知设置收进弹窗（2026-11-18）：它是**配置**不是列表内容，
         常驻页面会和「工单列表」抢版面。入口只留一个按钮，点开才展开。
         仅管理员可见（canManage），与原来 ConsolePanel 的可见条件一致。 -->
    <a-modal
      v-if="canManage()"
      :open="notificationConfigVisible"
      title="企业微信工单提醒"
      :footer="null"
      :width="560"
      :onUpdate:open="pageEntry.bind(setNotificationConfigVisible)"
      data-testid="ticket-notification-config-modal"
    >
      <p class="config-modal-lead">
        提醒以异步队列发送，保存后可发送测试消息。Webhook 地址不会回显完整内容。
      </p>
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
    </a-modal>
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
        <a-form-item label="状态"
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
        <a-form-item label="分类"
          ><a-select
            :value="filters.category" :onUpdate:value="pageEntry.bind(setFilter, 'category')"
            allow-clear
            placeholder="全部类别"
            style="width: 150px"
            :options="categoryOptions"
            :onChange="pageEntry.bind(applyFilters)"
        /></a-form-item>
        <!-- 快捷筛选：一屏看到所有待处理退款。
             这就是原「退款工作台」的队列价值 —— 用筛选实现，不再需要第二个列表。
             组合条件即后端已有的 category + status，不新增接口。 -->
        <a-form-item v-if="isAdmin"
          ><a-button :type="refundPendingActive ? 'primary' : 'default'"
                    :onClick="pageEntry.bind(toggleRefundPending)"
                    data-testid="filter-refund-pending">待处理退款</a-button></a-form-item>
        <a-form-item
          ><a-button :onClick="pageEntry.bind(resetFilters)">重置</a-button></a-form-item>
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
    <!-- ══════════════ 发起工单：两步向导（B2-3 / B2-4）══════════════
         用户诉求（138 号 §2.3）：列表右上角点「发起工单」→ **先选类型** →
         才出对应模板。所以表单里**没有**类型下拉（那是旧的一步向导），
         类型选择是一个独立步骤。

         三个步骤，由 createStep 驱动（不是多个布尔，避免出现「两个步骤同时为真」）：
           type          选类型（4 项白名单）
           closure-guide 注销申请专属：先推荐自助注销（**主**按钮），
                         次按钮才进表单
           form          对应类型的模板字段

         ⚠️ footer 用自定义而不是 :onOk —— 每个步骤的主动作不同
         （下一步 / 提交），共用一个 ok 按钮会让「取消」在向导中途变成「提交」。 -->
    <a-modal
      :key="pageEpoch + ':' + createVersion" :open="createVisible" :onUpdate:open="createEntry.bind(setCreateVisible)"
      :title="createWizardTitle"
      :footer="null"
      :mask-closable="false"
      ><section v-if="actionFailures.create.state.failure" data-ticket-create-feedback>
      <RequestFailureAlert :key="actionFailures.create.state.context" :failure="actionFailures.create.state.failure" :message="actionFailures.create.state.message" :context-key="actionFailures.create.state.context" :is-current="actionFailures.create.isCurrent" :busy="createLoading" closable :onClose="actionFailures.create.close" />
    </section>
      <!-- ───── 第一步：选类型（白名单 4 项）───── -->
      <div v-if="createStep === TICKET_WIZARD_STEP_TYPE" class="ticket-wizard-step" data-ticket-step="type">
        <p class="ticket-wizard-lead">请选择要提交的工单类型。选完后再填写对应内容。</p>
        <!--
          类型选择用**原生按钮列表**，不用 a-radio-group。

          2026-11-18：原实现是 a-radio-group + v-for + a-radio，实测（真实鼠标点击、
          原生 click/change 事件）都无法让选中态生效 —— draft.ticketType 始终为空、
          「下一步」永远禁用，且不报错。headless 与真实浏览器均复现。

          改用 role=radiogroup 的按钮列表：
            · 点击后直接调用 selectCreateType(value)，不经过任何守卫包装；
            · 选中态由 :class 显式绑定 draft.ticketType，不依赖组件内部状态；
            · 保留 role/aria-checked，键盘与读屏行为不受影响。
        -->
        <div
          class="ticket-type-choices"
          role="radiogroup"
          aria-label="工单类型"
          :aria-busy="createLoading ? 'true' : undefined"
        >
          <button
            v-for="item in createTypeOptions"
            :key="item.value"
            type="button"
            role="radio"
            :aria-checked="draft.ticketType === item.value ? 'true' : 'false'"
            :disabled="createLoading"
            :data-ticket-type-option="item.value"
            :class="['ticket-type-choice', { 'ticket-type-choice--checked': draft.ticketType === item.value }]"
            @click="selectCreateType(item.value)"
          >
            <span class="ticket-type-choice__dot" aria-hidden="true" />
            <span class="ticket-type-choice__body">
              <span class="ticket-type-choice__label">{{ item.label }}</span>
              <span class="ticket-type-choice__hint">{{ item.hint }}</span>
            </span>
          </button>
        </div>
      </div>
      <ClosureGuide
        v-else-if="createStep === TICKET_WIZARD_STEP_CLOSURE_GUIDE"
        mode="guide"
        data-ticket-step="closure-guide"
        @fallback="goCreateForm"
      />
      <!-- ───── 第二步：对应类型的模板字段 ───── -->
      <a-form v-else-if="createStep === TICKET_WIZARD_STEP_FORM" layout="vertical" data-ticket-step="form"
        ><a-alert v-if="createSaved" type="info" show-icon message="提交的工单已保存，当前修改尚未提交。" />
        <!-- 退款类型：字段整体切换成退款需要的（订单 / 金额 / 理由），
             而不是只把分类字符串改一下。 -->
        <template v-if="createNeedsRefundFields">
          <!-- 退款金额：**第一行**（2026-11-19 用户要求）。
               金额是「目标总额」，订单选择是「如何凑出这个总额」，所以它在前。 -->
          <a-form-item label="退款金额（元）" required :validate-status="createValidationMessage('requestedYuan') ? 'error' : ''">
            <a-input
              :id="createFieldId('amount')"
              :value="draft.requestedYuan"
              :onUpdate:value="setRefundRequestedYuan"
              :aria-invalid="createValidationMessage('requestedYuan') ? 'true' : undefined"
              placeholder="例如 25.50"
              :disabled="createLoading"
            /><template #help>
              <span v-if="createValidationMessage('requestedYuan')" role="alert">{{ createValidationMessage('requestedYuan') }}</span>
              <div class="ticket-refund-summary" data-ticket-refund-available aria-live="polite">
                <span v-if="refundSummaryLoading">正在查询可退款金额…</span>
                <span v-else-if="refundSummaryError" role="alert">{{ refundSummaryError }} <button type="button" class="ticket-refund-retry" @click="loadRefundSummary">重试</button></span>
                <div v-else-if="refundAvailableCents !== null" class="ticket-refund-summary__content">
                  <dl class="ticket-refund-summary__amounts">
                    <div><dt>账户余额</dt><dd>{{ formatRefundCents(refundBalanceCents) }} 元</dd></div>
                    <div class="ticket-refund-summary__refundable"><dt>可退款</dt><dd>{{ formatRefundCents(refundAvailableCents) }} 元</dd></div>
                    <div class="ticket-refund-summary__nonrefundable"><dt>不可退款</dt><dd>{{ formatRefundCents(refundNonRefundableCents) }} 元</dd></div>
                  </dl>
                  <p class="ticket-refund-summary__reason"><strong>原因：</strong>{{ refundNonRefundableReason || '无不可退款金额' }}</p>
                </div>
              </div>
            </template>
          </a-form-item>

          <!-- 退回方式：**第二行**（2026-11-19 用户要求）。

               用原生 button 而不是 a-radio-group：本项目 antd 4.2.6 实测点击后
               既不 emit update:value 也不 emit change，选中态永远落不到 DOM，
               且全程无报错。同样的坑之前在工单类型选择上踩过一次。 -->
          <a-form-item label="退回方式" required>
            <div class="ticket-refund-method" role="radiogroup" aria-label="退回方式">
              <button
                type="button"
                class="ticket-refund-method__option"
                :class="{ 'ticket-refund-method__option--active': draft.refundMethod === REFUND_METHOD_ORIGINAL }"
                role="radio"
                :aria-checked="draft.refundMethod === REFUND_METHOD_ORIGINAL ? 'true' : 'false'"
                data-ticket-refund-method="original"
                :disabled="createLoading"
                @click="selectRefundMethod(REFUND_METHOD_ORIGINAL)"
              >
                <span class="ticket-refund-method__title">原路退回</span>
                <span class="ticket-refund-method__desc">退回原支付渠道，需选择对应的充值记录</span>
              </button>
              <button
                type="button"
                class="ticket-refund-method__option"
                :class="{ 'ticket-refund-method__option--active': draft.refundMethod === REFUND_METHOD_MANUAL }"
                role="radio"
                :aria-checked="draft.refundMethod === REFUND_METHOD_MANUAL ? 'true' : 'false'"
                data-ticket-refund-method="manual"
                :disabled="createLoading"
                @click="selectRefundMethod(REFUND_METHOD_MANUAL)"
              >
                <span class="ticket-refund-method__title">人工退回</span>
                <span class="ticket-refund-method__desc">由管理员人工打款，需上传收款码</span>
              </button>
            </div>
          </a-form-item>

          <!-- 原路退回：选充值记录 + 按新旧分摊 -->
          <template v-if="draft.refundMethod === REFUND_METHOD_ORIGINAL">
            <a-form-item label="退款订单" :html-for="createFieldId('order')">
              <a-button data-ticket-refund-orders-open :disabled="createLoading" @click="openRefundOrders">选择充值记录（已选 {{ draft.refundAllocations.length }} 笔）</a-button>
              <a-modal :open="refundOrdersVisible" title="选择原路退回的充值记录" :width="640" :footer="null" @cancel="refundOrdersVisible = false">
              <p v-if="refundOrdersLoading" class="ticket-refund-orders-hint">正在读取充值记录…</p>
              <p v-else-if="refundOrdersError" class="ticket-refund-orders-hint" role="alert">{{ refundOrdersError }} <button type="button" class="ticket-refund-retry" @click="loadRefundOrders">重试</button></p>
              <p v-else-if="!refundOrderOptions.length" class="ticket-refund-orders-hint">
                暂无已支付或已结束的充值记录，未支付订单不在此显示。
              </p>
              <div v-else class="ticket-refund-orders" role="group" aria-label="退款订单（可多选）">
                <div
                  v-for="item in refundOrderOptions"
                  :key="item.value"
                  class="ticket-refund-order"
                  :class="{ 'ticket-refund-order--checked': isRefundOrderSelected(item.value), 'ticket-refund-order--disabled': !item.originalSupported }"
                  :data-ticket-refund-order="item.value"
                >
                  <input
                    type="checkbox"
                    :aria-label="`选择订单 ${item.label}`"
                    :value="item.value"
                    :checked="isRefundOrderSelected(item.value)"
                    :disabled="createLoading || !canToggleRefundOrder(item.value)"
                    @change="toggleRefundOrder(item.value)"
                  />
                  <span class="ticket-refund-order__body">
                    <span class="ticket-refund-order__label">{{ item.label }}</span>
                    <span class="ticket-refund-order__tag" v-if="!item.originalSupported">不可原路退回</span>
                    <span v-if="item.statusLabel || item.createdAt" class="ticket-refund-order__meta">{{ item.statusLabel }}<template v-if="item.createdAt"> · 创建于 {{ formatTicketTime(item.createdAt) }}</template></span>
                    <span
                      class="ticket-refund-order__reason"
                      v-if="!item.originalSupported && item.originalUnsupportedReason"
                      :data-ticket-refund-order-reason="item.value"
                    >{{ item.originalUnsupportedReason }}</span>
                    <span class="ticket-refund-alloc" v-if="isRefundOrderSelected(item.value)">
                      <span class="ticket-refund-alloc__label">本次退</span>
                      <input
                        type="number"
                        class="ticket-refund-alloc__input"
                        min="0"
                        step="0.01"
                        :max="(item.paidCents / 100).toFixed(2)"
                        :value="(refundAllocationOf(item.value).cents / 100).toFixed(2)"
                        :disabled="createLoading"
                        :data-ticket-refund-alloc="item.value"
                        @change="setRefundAllocationCents(item.value, $event.target.value)"
                      />
                      <span class="ticket-refund-alloc__unit">元</span>
                      <span
                        class="ticket-refund-alloc__badge"
                        :class="{ 'ticket-refund-alloc__badge--partial': refundAllocationOf(item.value).cents < item.paidCents }"
                      >{{ refundAllocationOf(item.value).cents >= item.paidCents ? "全额" : "部分" }}</span>
                    </span>
                  </span>
                </div>
              </div>
              <div class="ticket-refund-summary" data-ticket-refund-summary>
                已分配 <strong>{{ (refundAllocatedCents / 100).toFixed(2) }}</strong> 元 /
                申请 <strong>{{ (refundRequestedCents / 100).toFixed(2) }}</strong> 元
                <span v-if="refundRemainingCents > 0">
                  ，还差 <strong data-ticket-refund-gap>{{ (refundRemainingCents / 100).toFixed(2) }}</strong> 元
                </span>
                <span v-else-if="refundTotals.excess > 0" role="alert">，超出 {{ (refundTotals.excess / 100).toFixed(2) }} 元，请调整手动金额</span>
                <span v-else>，已达申请金额（要再选订单请先把某笔金额调小）</span>
              </div>
              <a-button data-ticket-refund-orders-confirm type="primary" @click="refundOrdersVisible = false">完成选择</a-button>
              </a-modal>
              <p v-if="createValidationMessage('order')" role="alert">{{ createValidationMessage('order') }}</p>
              <template #help
                ><span>
                  按充值时间<strong>最旧优先</strong>自动分摊：较旧的先全额，剩余部分给最新一笔。
                  你可以直接改每一笔的金额 —— 改过的行不会再被自动调整；
                  把某笔调小就能腾出额度再选下一笔。
                </span></template
              >
            </a-form-item>
          </template>

          <!-- 人工退回：手机号确认 + 收款码 -->
          <template v-else-if="draft.refundMethod === REFUND_METHOD_MANUAL">
            <a-form-item label="手机号验证">
              <div class="ticket-refund-phone" data-ticket-refund-phone>
                <template v-if="refundPhoneStatus.required">
                  <span class="ticket-refund-phone__value">{{ refundPhoneStatus.phoneMasked }}</span>
                  <a-input :value="draft.refundPhoneSmsCode" autocomplete="one-time-code" inputmode="numeric" :maxlength="6" aria-label="退款手机验证码" :disabled="createLoading" @update:value="draft.refundPhoneSmsCode = $event" />
                  <a-button :loading="refundSmsBusy" :disabled="createLoading || refundSmsCooldown > 0" @click="sendRefundSms">{{ refundSmsCooldown > 0 ? `${refundSmsCooldown} 秒后重发` : '发送验证码' }}</a-button>
                </template>
                <span v-else class="ticket-refund-phone__missing">
                  {{ refundPhoneStatus.loaded ? '当前账号未绑定可验证的手机号，或短信服务未启用，可继续申请人工退款。' : '正在确认手机号验证是否可用…' }}
                </span>
              </div>
              <p v-if="createValidationMessage('refundPhone')" role="alert">{{ createValidationMessage('refundPhone') }}</p>
            </a-form-item>

            <a-form-item label="收款码" required>
              <RefundPayoutQr
                ref="payoutQrComponent"
                :user-id="Number(currentUser?.id)"
                :disabled="createLoading"
                @update:attachment-id="draft.refundPayoutQrAttachmentId = $event"
                @busy="payoutQrBusy = $event"
                @changed="resetAgreement"
              />
            </a-form-item>

            <!-- 承诺书（2026-11-19 用户要求）：本人承诺收款码所属账户为本人账户。
                 勾选即弹出协议（需读 10 秒 + 滚到底 + 签名）。 -->
            <a-form-item label="承诺书" required>
              <div class="ticket-refund-agreement" data-ticket-refund-agreement>
                <label class="ticket-refund-agreement__check">
                  <input
                    type="checkbox"
                    :checked="draft.refundAgreementSigned"
                    :disabled="createLoading"
                    data-ticket-refund-agreement-check
                    @change="onToggleAgreement"
                  />
                  <span>我已阅读并同意《退款收款码使用承诺书》</span>
                </label>
                <a-button
                  v-if="draft.refundAgreementSigned"
                  type="link"
                  size="small"
                  data-ticket-refund-agreement-review
                  @click="openAgreement"
                >查看协议</a-button>
              </div>
            </a-form-item>
          </template>

          <a-form-item label="退款理由" required :validate-status="createValidationMessage('reason') ? 'error' : ''">
            <a-textarea
              :id="createFieldId('reason')"
              :value="draft.reason"
              :onUpdate:value="createEntry.bind(setDraft, 'reason')"
              :aria-invalid="createValidationMessage('reason') ? 'true' : undefined"
              :maxlength="500"
              show-count
              :rows="4"
              :disabled="createLoading"
              placeholder="请说明退款原因"
            /><template #help><span v-if="createValidationMessage('reason')" role="alert">{{ createValidationMessage('reason') }}</span></template>
          </a-form-item>

          <!-- 协议弹窗。签名弹窗是它的内层浮层，
               关闭签名弹窗**不会**关掉协议弹窗（用户明确要求）。 -->
          <RefundAgreementDialog
            :key="createVersion"
            :visible="agreementVisible"
            :text="agreementText"
            :signed-attachment-id="draft.refundSignatureAttachmentId"
            @close="agreementVisible = false"
            @signed="onAgreementSigned"
          />
        </template>
        <!-- 账号问题（B2-6）：问题类型下拉 + 说明文本域。
             问题类型只作为标题前缀供管理员分诊，不是独立后端字段
             （tickets 表没有 issue_type 列）。 -->
        <template v-else-if="createNeedsAccountFields">
          <a-form-item label="问题类型" required :validate-status="createValidationMessage('issueType') ? 'error' : undefined"
            ><a-select
              :id="createFieldId('issue')"
              :value="draft.issueType"
              :options="createIssueOptions"
              :disabled="createLoading"
              placeholder="请选择问题类型"
              @update:value="createEntry.bind(setDraft, 'issueType')" /><template #help><span v-if="createValidationMessage('issueType')" role="alert">{{ createValidationMessage('issueType') }}</span><span v-else>选择后标题会自动带上该类型，便于管理员分诊。</span></template></a-form-item
          >
          <a-form-item label="问题说明" required :validate-status="createValidationMessage('body') ? 'error' : undefined"
            ><a-textarea
              :id="createFieldId('body')"
              :value="draft.body" :onUpdate:value="createEntry.bind(setDraft, 'body')"
              :aria-invalid="createValidationMessage('body') ? 'true' : undefined" :aria-describedby="createValidationMessage('body') ? createValidationErrorId('body') : undefined"
              :maxlength="10000"
              show-count
              :rows="8"
              placeholder="请提供账号、时间、现象与期望结果" /><template #help><span v-if="createValidationMessage('body')" :id="createValidationErrorId('body')" role="alert">{{ createValidationMessage('body') }}</span></template></a-form-item
          >
        </template>
        <!-- 注销申请（兜底路径）：用户已明确选择继续建单，只填原因。
             标题由服务端/前端固定生成（【注销申请】前缀），不让用户自由填写。 -->
        <template v-else-if="createNeedsClosureFields">
          <a-alert
            type="info"
            show-icon
            message="你选择了提交注销工单（兜底路径）"
            description="管理员会人工核对。若希望更快生效且可随时撤销，可返回上一步使用自助注销。"
          />
          <a-form-item label="注销原因" required :validate-status="createValidationMessage('body') ? 'error' : undefined"
            ><a-textarea
              :id="createFieldId('body')"
              :value="draft.body" :onUpdate:value="createEntry.bind(setDraft, 'body')"
              :aria-invalid="createValidationMessage('body') ? 'true' : undefined" :aria-describedby="createValidationMessage('body') ? createValidationErrorId('body') : undefined"
              :maxlength="10000"
              show-count
              :rows="6"
              placeholder="请说明注销原因，例如不再使用、换号、隐私考虑" /><template #help><span v-if="createValidationMessage('body')" :id="createValidationErrorId('body')" role="alert">{{ createValidationMessage('body') }}</span></template></a-form-item
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
              placeholder="请提供复现步骤、错误提示和期望结果" /><template #help><span v-if="createValidationMessage('body')" :id="createValidationErrorId('body')" role="alert">{{ createValidationMessage('body') }}</span></template></a-form-item>
      </template>
      <!-- 截图：**所有类型共用**（2026-11-18 用户要求）。

           原来这块写在「普通工单」分支（v-else）里，于是退款 / 账号 / 注销
           三种类型都没有上传入口。附件本身与类型无关（后端 CreateTicketInput
           .AttachmentIds 是通用字段），所以移出分支、放在 </a-form> 之前。

           保留在 <a-form> 内而不是外面：它能跟着表单的 layout/间距走，
           视觉上仍是「填内容」的一部分。 -->
      <a-form-item
        label="图片（可选）"
        help="支持 PNG / JPEG，单张不超过 2 MiB。图片上传后先做安全检测，检测未通过时只显示提示。"
      >
        <TicketImagePicker :max="TICKET_ATTACHMENT_MAX_PER_MESSAGE" :disabled="createLoading || uploadBusy" @change="onPickDraftImages" />
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
      </a-form-item>
      </a-form>
      <!-- 向导 footer：每步主动作不同，所以自定义。

           「上一步」覆盖**除第一步外的所有步骤**：
             · FORM（填内容）→ 回类型选择；
             · CLOSURE_GUIDE（注销引导 / 不支持自助注销的提示）→ 同样回类型选择。
           原实现只在 FORM 显示「上一步」，导致进了注销引导页后只剩「取消」，
           想换类型必须整单关掉重开（用户 2026-11-18 指出）。
           注意：「取消」仍然保留 —— 它是「放弃建单」，与「回上一级」是两件事。 -->
      <div class="ticket-wizard-footer">
        <a-button :disabled="createLoading" :onClick="createEntry.bind(closeCreate)">取消</a-button>
        <a-button
          v-if="!createAtFirstStep"
          :disabled="createLoading"
          :onClick="createEntry.bind(backToTypeStep)"
          data-testid="ticket-wizard-back"
          >上一步</a-button
        >
        <a-button
          v-if="createStep === TICKET_WIZARD_STEP_TYPE"
          type="primary"
          :disabled="!createTypeChosen"
          :onClick="createEntry.bind(advanceFromTypeStep)"
          >下一步</a-button
        >
        <a-button
          v-else-if="createStep === TICKET_WIZARD_STEP_FORM"
          type="primary"
          :loading="createLoading"
          :disabled="payoutQrBusy || agreementLoading || uploadBusy"
          :onClick="createEntry.bind(submitTicket)"
          >提交</a-button
        >
      </div>
    </a-modal>
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
          <!-- 退款工单的金额/状态/审核动作。
               数据来自 detail.refund（后端 TicketDetail.Refund，2026-11-18 新增）——
               这是「删掉独立退款面板」后审核操作唯一的入口。 -->
          <TicketRefundActions
            v-if="detailTicket.refund"
            :refund="detailTicket.refund"
            :is-admin="isAdmin"
            :on-changed="detailEntry.bind(reloadDetail)"
          />
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
            ><a-form-item label="图片（可选）"
              ><TicketImagePicker :max="TICKET_ATTACHMENT_MAX_PER_MESSAGE" :disabled="replyLoading || uploadBusy || !detailTicket" @change="onPickReplyImages" />
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
import Swal from 'sweetalert2';
import RequestFailureAlert from "@/components/RequestFailureAlert.vue";
import TicketAttachment from "@/components/TicketAttachment.vue";
import TicketImagePicker from "@/components/TicketImagePicker.vue";
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
} from "@/utils/ticketHelpers.mjs";
// 退款 7 态的中文文案、金额格式化与可退订单列表都与退款面板共用同一份实现，
// 避免列表与面板对同一状态/金额给出不同说法。
import { formatRefundCents, refundStatusLabel } from "./refundTicketHelpers.mjs"
// 分摊算法抽成纯函数：「不覆盖用户手改金额」这条
// 写在 .vue 里测不到，只能靠人点界面看 —— 那不叫验证。
import {
  computeRefundAllocations,
  refundAllocationTotals,
} from "./refundAllocation.mjs";
// 新建工单时选类型的纯函数（T-209 建立，B2 扩到四项 + 两步向导）：
// 类型清单、向导步骤、按类型分派的校验、payload 构造。
//
// 注释：各类型的字段校验统一走 getTicketCreateDraftValidation —— 页面不再
// 单独 import getTicketDraftValidation / getRefundDraftValidation /
// getAccountDraftValidation（各留一份会漂移成「前端通过、后端报错」）。
import {
  buildTicketCreatePayload,
  getTicketCreateDraftValidation,
  isAccountClosureTicketType,
  isRefundTicketRow,
  parseRefundYuanToCents,
  ticketCreateTypeNeedsRefundFields,
  ticketRowRefundStatus,
  ticketWizardCanAdvanceFromType,
  ticketWizardCanSubmit,
  ticketWizardInitialStep,
  ticketWizardNextStep,
  TICKET_ACCOUNT_ISSUE_OPTIONS,
  TICKET_CREATE_TYPE_ACCOUNT,
  TICKET_CREATE_TYPE_GENERAL,
  TICKET_CREATE_TYPE_OPTIONS,
  TICKET_WIZARD_STEP_CLOSURE_GUIDE,
  TICKET_WIZARD_STEP_FORM,
  TICKET_WIZARD_STEP_TYPE,
} from "./createTicketTypeHelpers.mjs";
import {
  getEligibleTicketRefundOrders,
  getTicketRefundSummary,
  getRefundAgreement,
  getRefundPhoneVerification,
  sendRefundPhoneSMSCode,
} from "@/api/TikuComponentApi";
import RefundAgreementDialog from "@/components/RefundAgreementDialog.vue";
import RefundPayoutQr from "@/components/RefundPayoutQr.vue";

const detailStatusId = `tickets-${useId()}-detail-status`;
// 退款 7 态的中文文案与退款面板同一份（refundStatusLabel 在上方 import），
// 避免列表与面板对同一状态给出不同说法。
// 退款操作区（详情内）。原名 TicketRefundPanel 是「整套退款工作台」，
// 2026-11-18 重构后只保留「单张工单的退款动作」，故改名以示职责收窄。
const TicketRefundActions = defineAsyncComponent(() => import("./TicketRefundActions.vue"));
// 注销引导 / 自助注销：与工单向导的「注销申请」分支、个人中心入口共用同一组件。
const ClosureGuide = defineAsyncComponent(() => import("./ClosureGuide.vue"));
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
// createStep 是**两步向导**的当前步骤（B2-3）。
//
// 用一个步骤字符串而不是多个布尔（如选类型中 / 填表单中）：
// 多布尔一定会出现「两个同时为真」的中间态，界面就会同时渲染两套模板。
// 取值常量来自 createTicketTypeHelpers.mjs：type / closure-guide / form。
const createStep = ref(ticketWizardInitialStep());
// draft 是新建工单表单。T-209 增加了「工单类型」以及退款专属字段；
// B2 增加了 account 的 issueType 与 closure 的固定标题（不入 draft）。
//
// 各类型字段与普通字段**并存**在同一个对象里，但提交时由 buildTicketCreatePayload
// 按类型只挑该传的那些（退款绝不带 subject/body，普通类型绝不带退款字段 ——
// 后端会对参数矛盾报错，这是有意的检测）。
const draft = reactive({
  ticketType: TICKET_CREATE_TYPE_GENERAL,
  subject: "",
  body: "",
  requestedYuan: "",
  reason: "",
  // 多选（2026-11-18）：一个订单一张退款单，服务端展开；空数组 = 无订单人工退款。
  //
  // 2026-11-19：分摊模式下改用 refundAllocations（带金额）。
  // 这个字段保留为空数组，不再写入 —— 两份真相并存必然漂移。
  refundOrderIds: [],
  // 退回方式（2026-11-19）：“original” 原路退回 / “manual” 人工退回。
  refundMethod: '',
  // 专用退款短信验证码，只保留在本次草稿内。
  refundPhoneSmsCode: '',
  // 承诺书签署（2026-11-19）。
  //
  // 已签状态与签名图只属于本次申请；查看协议时保留显示。
  refundAgreementSigned: false,
  refundSignatureAttachmentId: 0,
  // 分摊表：[{ orderId, cents, manual }]
  //   manual=true 表示用户手改过这一行，任何重算都不得覆盖它。
  refundAllocations: [],
  // 收款码附件 ID（可选，2026-11-19）。null = 没上传/已清除。
  // 与 draftAttachments 分开：那是工单截图，这是退款收款凭据。
  refundPayoutQrAttachmentId: null,
  // account 专用：登录异常 / 绑定问题 / 封禁申诉（只做标题前缀，不是后端字段）。
  issueType: "",
});
const createValidation = shallowRef(null);
const createValidationErrorId = field => `${detailStatusId}-create-${field}-error`;
// 表单控件的 id（用于 label 的 html-for 与 aria 关联），与错误提示 id 分开。
const createFieldId = field => `${detailStatusId}-create-field-${field}`;
// 当前类型是否需要退款专属字段（任务书：不能只换个 category 字符串）。
const createNeedsRefundFields = computed(() => ticketCreateTypeNeedsRefundFields(draft.ticketType));
// account（账号问题）：问题类型下拉 + 说明文本域（B2-6）。
const createNeedsAccountFields = computed(() => draft.ticketType === TICKET_CREATE_TYPE_ACCOUNT);
// account_closure（注销申请）：只填注销原因（标题固定生成）。
const createNeedsClosureFields = computed(() => isAccountClosureTicketType(draft.ticketType));
const createTypeOptions = TICKET_CREATE_TYPE_OPTIONS.map(({ value, label, hint }) => ({ value, label, hint }));
const createIssueOptions = TICKET_ACCOUNT_ISSUE_OPTIONS.map(({ value, label }) => ({ value, label }));
// 第一步「下一步」是否可用：必须选了一个**白名单内**的类型。
const createTypeChosen = computed(() => ticketWizardCanAdvanceFromType(draft.ticketType));
// 弹窗标题随步骤变化：让用户始终知道自己在哪一步。
const createWizardTitle = computed(() => {
  if (createStep.value === TICKET_WIZARD_STEP_TYPE) return "发起工单 · 选择类型";
  if (createStep.value === TICKET_WIZARD_STEP_CLOSURE_GUIDE) return "发起工单 · 注销申请";
  const matched = TICKET_CREATE_TYPE_OPTIONS.find((item) => item.value === draft.ticketType);
  return `发起工单 · ${matched ? matched.label : "填写内容"}`;
});
// 查询失败与空列表分开显示，不能把网络错误解释成「没有订单」。
const refundOrderOptions = ref([]);
const refundOrdersVisible = ref(false);
const refundOrdersLoading = ref(false);
const refundOrdersError = ref('');
const refundAvailableCents = ref(null), refundBalanceCents = ref(null), refundNonRefundableCents = ref(null), refundNonRefundableReason = ref('');
const refundSummaryLoading = ref(false), refundSummaryError = ref('');
let refundAmountTouched = false;
let refundOrdersVersion = 0, refundSummaryVersion = 0;
async function loadRefundSummary() {
  if (!createVisible.value || !createNeedsRefundFields.value || refundSummaryLoading.value) return;
  const createOwns = captureCreate(), version = ++refundSummaryVersion;
  const owns = () => createOwns() && version === refundSummaryVersion;
  refundSummaryLoading.value = true; refundSummaryError.value = '';
  try {
    const summary = (await getTicketRefundSummary())?.data;
    const cents = summary?.refundableCents;
    if (!owns()) return;
    if (!Number.isSafeInteger(cents) || cents < 0 ||
        !Number.isSafeInteger(summary?.balanceCents) || summary.balanceCents < 0 ||
        !Number.isSafeInteger(summary?.nonRefundableCents) || summary.nonRefundableCents < 0 ||
        typeof summary?.nonRefundableReason !== 'string') throw new Error('退款金额汇总数据无效');
    refundAvailableCents.value = cents;
    refundBalanceCents.value = summary.balanceCents;
    refundNonRefundableCents.value = summary.nonRefundableCents;
    refundNonRefundableReason.value = summary.nonRefundableReason;
    if (!refundAmountTouched) draft.requestedYuan = formatRefundCents(cents);
  } catch (error) {
    if (owns()) {
      refundAvailableCents.value = refundBalanceCents.value = refundNonRefundableCents.value = null;
      refundNonRefundableReason.value = ''; refundSummaryError.value = responseMessage(error, '可退款金额查询失败');
      if (!refundAmountTouched) draft.requestedYuan = '';
    }
  } finally { if (owns()) refundSummaryLoading.value = false; }
}
function openRefundOrders() {
  if (!canCreateTickets() || !createVisible.value || createLoading.value || draft.refundMethod !== 'original') return;
  if (!parseRefundYuanToCents(draft.requestedYuan)) {
    const empty = !String(draft.requestedYuan || '').trim();
    void Swal.fire({ icon: 'warning', title: empty ? '请先填写退款金额' : '退款金额格式不正确', text: empty ? '填写需要申请的退款金额后，再选择充值记录。' : '请输入大于 0 且最多两位小数的金额。', confirmButtonText: '知道了', returnFocus: false });
    return;
  }
  refundOrdersVisible.value = true;
  void loadRefundOrders();
}
async function loadRefundOrders() {
  if (refundOrdersLoading.value || !refundOrdersVisible.value) return;
  const createOwns = captureCreate(), version = ++refundOrdersVersion;
  const owns = () => createOwns() && version === refundOrdersVersion;
  refundOrdersLoading.value = true; refundOrdersError.value = '';
  try {
    const response = await getEligibleTicketRefundOrders();
    if (!owns()) return;
    const rows = response?.data;
    if (!Array.isArray(rows)) throw new Error('充值记录数据格式无效');
    refundOrderOptions.value = rows
      .filter((row) => row && Number.isSafeInteger(Number(row.id)))
      .map((row) => ({
        value: Number(row.id),
        label: `${row.orderNo || "#" + row.id} · 可退 ${formatRefundCents(row.paidCents)} 元`,
        // 2026-11-19：不能原路退回的订单也要显示，并写明原因（用户要求）。
        // 后端给的是**具体原因**而不是布尔值 —— 「渠道未配置」与
        // 「这笔单的回调没写流水号」对用户的意义完全不同。
        originalSupported: row.originalSupported === true,
        originalUnsupportedReason: String(row.originalUnsupportedReason || ''),
        // 分摊算法需要两个原始值：
        //   paidCents —— 这笔最多能退多少（上限）
        //   paidAt    —— 新旧排序（用户要求「最旧那笔优先全额」）
        paidCents: Number(row.paidCents) || 0,
        paidAt: String(row.paidAt || ''),
        createdAt: String(row.createdAt || ''),
        statusLabel: ({ paid: '已支付', refunded: '已退款', closed: '已关闭', expired: '已过期', failed: '支付失败' })[row.status] || String(row.status || ''),
      }));
    // 刷新后资格可能变化；保留仍可退行的手动金额，移除已不可退的选择。
    draft.refundAllocations = draft.refundAllocations.filter(item => refundOrderOptions.value.some(order => order.value === item.orderId && order.originalSupported));
    recomputeRefundAllocations();
  } catch (error) {
    if (!owns()) return;
    refundOrdersError.value = responseMessage(error, '充值记录加载失败');
  } finally {
    if (owns()) refundOrdersLoading.value = false;
  }
}
function createValidationMessage(field) { return createValidation.value?.field === field ? createValidation.value.message : ""; }
function retireCreateValidation() { createValidation.value = null; }
// 当前表单用的是哪一套校验：随类型切换（退款 / 账号 / 注销 / 普通）。
//
// 统一走 getTicketCreateDraftValidation，避免页面里再维护一张类型→校验的映射表
// （两张表一定会漂移，症状是「某类型前端通过、后端报错」）。
function currentDraftValidation() {
  return getTicketCreateDraftValidation({
    ticketType: draft.ticketType,
    subject: draft.subject,
    body: draft.body,
    requestedYuan: draft.requestedYuan,
    reason: draft.reason,
    issueType: draft.issueType,
    // 2026-11-19 退款新字段：方式 / 分摊 / 收款码 / 手机号确认。
    refundMethod: draft.refundMethod,
    refundAllocations: draft.refundAllocations,
    refundPayoutQrAttachmentId: draft.refundPayoutQrAttachmentId,
    refundSignatureAttachmentId: draft.refundSignatureAttachmentId,
    refundAgreementSigned: draft.refundAgreementSigned,
    refundPhoneVerificationRequired: refundPhoneStatus.required,
    refundPhoneSmsCode: draft.refundPhoneSmsCode,
    // 手机号从当前用户现取而不存进 draft：
    // 存一份副本会在换账号后变成旧值。

  });
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
function canCreateTickets() { return canUseTickets() && !isAdmin.value; }
// 收款码组件按草稿生命周期隔离；设备私钥只在服务端记忆注册成功后保存。
const payoutQrComponent = ref(null), payoutQrBusy = ref(false)
async function ensurePayoutQrDraft(owns) {
  const component = payoutQrComponent.value
  if (!component) throw new Error('请上传或解锁收款码')
  const id = await component.ensureDraft()
  if (owns() && id) draft.refundPayoutQrAttachmentId = id
}

const REFUND_METHOD_ORIGINAL = 'original', REFUND_METHOD_MANUAL = 'manual'
const refundAllocationOf = orderId => draft.refundAllocations.find(item => item.orderId === Number(orderId)) || null
const isRefundOrderSelected = orderId => refundAllocationOf(orderId) !== null
const refundRequestedCents = computed(() => parseRefundYuanToCents(draft.requestedYuan) || 0)
const refundTotals = computed(() => refundAllocationTotals(refundRequestedCents.value, draft.refundAllocations))
const refundAllocatedCents = computed(() => refundTotals.value.allocated)
const refundRemainingCents = computed(() => refundTotals.value.remaining)
function recomputeRefundAllocations(addedOrderId) {
  draft.refundAllocations = computeRefundAllocations({
    orders: refundOrderOptions.value.map(item => ({ id: item.value, paidCents: item.paidCents, paidAt: item.paidAt })),
    requestedCents: refundRequestedCents.value, selections: draft.refundAllocations, addedOrderId,
  })
}
const canToggleRefundOrder = orderId => isRefundOrderSelected(orderId) ||
  (refundRemainingCents.value > 0 && refundOrderOptions.value.some(item => item.value === Number(orderId) && item.originalSupported))
function setRefundRequestedYuan(value) {
  if (createLoading.value) return
  refundAmountTouched = true
  draft.requestedYuan = value; recomputeRefundAllocations(); retireCreateValidation()
}
function setRefundAllocationCents(orderId, yuan) {
  if (createLoading.value) return
  const row = refundAllocationOf(orderId)
  if (!row) return
  const cap = Number(refundOrderOptions.value.find(item => item.value === Number(orderId))?.paidCents) || 0
  row.cents = Math.min(Math.max(0, parseRefundYuanToCents(yuan) || 0), cap)
  row.manual = true
  retireCreateValidation()
}
function selectRefundMethod(method) {
  if (createLoading.value || draft.refundMethod === method) return
  draft.refundMethod = method; resetAgreement(); retireCreateValidation()
  refundOrdersVisible.value = false;
  if (method === REFUND_METHOD_MANUAL) { void loadRefundPhoneStatus() }
}
const refundPhoneStatus = reactive({ required: false, phoneMasked: '', loaded: false })
const refundSmsBusy = ref(false), refundSmsCooldown = ref(0)
let refundSmsTimer = null
async function loadRefundPhoneStatus() {
  const owns = captureCreate()
  refundPhoneStatus.loaded = false
  try {
    const response = await getRefundPhoneVerification()
    if (typeof response?.data?.required !== "boolean") throw new Error("手机号验证状态无效，请重试");
    if (owns()) Object.assign(refundPhoneStatus, response.data, { loaded: true })
  } catch (error) { if (owns()) message.error(responseMessage(error, '无法确认手机号验证状态，请重新选择人工退款')) }
}
async function sendRefundSms() {
  if (refundSmsBusy.value || refundSmsCooldown.value > 0 || !refundPhoneStatus.required) return
  const owns = captureCreate(); refundSmsBusy.value = true
  try {
    await sendRefundPhoneSMSCode()
    if (!owns()) return
    refundSmsCooldown.value = 60
    refundSmsTimer = setInterval(() => { if (--refundSmsCooldown.value <= 0) { clearInterval(refundSmsTimer); refundSmsTimer = null } }, 1000)
    message.success('验证码已发送至绑定手机号')
  } catch (error) { if (owns()) message.error(responseMessage(error, '验证码发送失败')) }
  finally { if (owns()) refundSmsBusy.value = false }
}
const agreementVisible = ref(false), agreementText = ref(''), agreementLoading = ref(false)
function resetAgreement() { agreementVisible.value = false; agreementText.value = ''; draft.refundAgreementSigned = false; draft.refundSignatureAttachmentId = 0 }
async function openAgreement() {
  if (agreementLoading.value || createLoading.value) return
  if (agreementText.value) { agreementVisible.value = true; return }
  const owns = captureCreate(); agreementLoading.value = true
  try {
    const response = await getRefundAgreement()
    if (!owns() || draft.refundMethod !== REFUND_METHOD_MANUAL) return
    const text = String(response?.data?.text || '').trim()
    if (!text) throw new Error('协议暂不可用，请稍后重试')
    agreementText.value = text; agreementVisible.value = true
  } catch (error) { if (owns()) message.error(responseMessage(error, '读取承诺书失败')) }
  finally { if (owns()) agreementLoading.value = false }
}
function onToggleAgreement(event) {
  if (event?.target) event.target.checked = draft.refundAgreementSigned
  void openAgreement()
}
function onAgreementSigned(attachmentId) {
  if (!createVisible.value || draft.refundMethod !== REFUND_METHOD_MANUAL) return
  draft.refundSignatureAttachmentId = Number(attachmentId) || 0
  draft.refundAgreementSigned = draft.refundSignatureAttachmentId > 0
  agreementVisible.value = false; retireCreateValidation()
}
function resetRefundDraft() {
  resetAgreement(); agreementLoading.value = false; refundOrdersVisible.value = false
  draft.refundMethod = ''; draft.refundAllocations = []; draft.refundOrderIds = []; draft.refundPhoneSmsCode = ''
  Object.assign(refundPhoneStatus, { required: false, phoneMasked: '', loaded: false })
  refundSmsBusy.value = false; refundSmsCooldown.value = 0
  clearInterval(refundSmsTimer); refundSmsTimer = null
  refundSummaryVersion += 1; refundAvailableCents.value = refundBalanceCents.value = refundNonRefundableCents.value = null;
  refundNonRefundableReason.value = ''; refundSummaryLoading.value = false; refundSummaryError.value = ''; refundAmountTouched = false;
  refundOrdersVersion += 1; refundOrdersLoading.value = false; refundOrdersError.value = '';
}
function clearOwned() {
  resetRefundDraft();
  draft.refundPayoutQrAttachmentId = null;
  payoutQrBusy.value = false;
  refundOrdersVersion += 1; refundOrderOptions.value = []; refundOrdersLoading.value = false;
  retireDetailFocus();
  retireCreateValidation();
  retireActionFailures();
  retireReplyDraft();
  listRequestVersion += 1; detailRequestVersion += 1; createVersion += 1; configReadVersion += 1; configDraftVersion += 1; pageEpoch.value += 1;
  tickets.value = []; pagination.total = 0; pagination.current = 1;
  listLoading.value = detailLoading.value = createLoading.value = replyLoading.value = statusSaving.value = configSaving.value = configTesting.value = retryLoading.value = configLoading.value = false;
  createVisible.value = detailVisible.value = configLoaded.value = createSaved.value = configDirty.value = false;
  detailTicket.value = null; messages.value = []; attachments.value = []; notification.value = null; replyBody.value = ""; detailStatus.value = ""; statusWasChosen.value = false;
  draft.ticketType = TICKET_CREATE_TYPE_GENERAL; draft.subject = ""; draft.body = ""; draft.requestedYuan = ""; draft.reason = ""; draft.refundOrderId = undefined; draft.issueType = ""; errorMessage.value = "";
  createStep.value = ticketWizardInitialStep();
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
  return () => owns() && version === createVersion && createVisible.value && canCreateTickets();
}
function captureDetail() {
  const owns = capturePage(), version = detailRequestVersion;
  return () => owns() && version === detailRequestVersion && detailVisible.value;
}
// bind 把 action 包上一层「页面/弹窗归属」守卫，避免旧响应或已关闭的弹窗回写状态。
//
// ⚠️ 2026-11-18 修复：守卫必须在**调用时**才捕获快照，不能在绑定时捕获。
//
// 原实现 `const owns = capture()` 在 bind() 那一瞬间就把 version 冻住了，而
// 模板的绑定发生在首次渲染时；用户点「发起工单」会执行 openCreate()，它在
// 中途 `createVersion += 1`（用于给弹窗换 key 强制重建）。两者一叠加：
//
//   绑定瞬间快照 version=0 → openCreate 把 createVersion 改成 1 →
//   回调触发时 0 === 1 为假 → owns() 恒 false → action 永远不执行。
//
// 症状就是「点类型没反应、下一步一直灰着」——所有 create 侧交互全部失效，
// 且不报任何错（静默丢弃）。同一 bug 也影响 detailEntry（detailRequestVersion
// 在打开详情时递增）。
//
// 修法：每次触发时重新 capture()，这样 version 比较的是「当前这次交互」
// 与「当前页面代次」，语义才对。
function entry(capture) { return { bind(action, ...args) {
  return (...events) => {
    // 每次调用都重新取守卫：comparison 的对象必须是"此刻"，不是"绑定时"。
    const owns = capture();
    if (owns()) return action(...args, ...events);
  };
} }; }
const pageEntry = entry(capturePage), createEntry = entry(captureCreate), detailEntry = entry(captureDetail);
function setKeyword(value) { if (canUseTickets()) keywordInput.value = value; }
function setFilter(key, value) { if (canUseTickets()) filters[key] = value; }
// 通知设置弹窗开关。默认收起：它是配置，不该和工单列表抢版面。
const notificationConfigVisible = ref(false);
function openNotificationConfig() { if (canManage()) notificationConfigVisible.value = true; }
function setNotificationConfigVisible(value) { if (!value) notificationConfigVisible.value = false; }
function setConfig(key, value) {
  if (!canManage() || !configLoaded.value) return;
  notificationConfig[key] = value;
  configDirty.value = true;
  if (key === "webhookUrl") notificationConfig.clearWebhook = false;
}
function clearSavedWebhook() { if (canManage() && configLoaded.value) { notificationConfig.clearWebhook = true; notificationConfig.webhookUrl = ""; configDirty.value = true; } }
function keepSavedWebhook() { if (canManage() && configLoaded.value) { notificationConfig.clearWebhook = false; configDirty.value = true; } }
// 退款订单多选（2026-11-18）：用原生 checkbox，不用 a-select mode="multiple"。
//
// 为什么不用 a-select：本项目 antd 4.2.6 在此环境下，下拉能展开、选项能渲染，
// 但真实鼠标点击与键盘回车都不会让选中态生效（.ant-select-item-option-selected 恒 0）。
// 与 a-radio-group 那个「选不动」同源 —— 组件内部 emit 链断掉，且不报错。
// toggleRefundOrder 改写为操作分摊表（2026-11-19）。
//
// 旧实现维护 draft.refundOrderIds（只有 id、没有金额），表达不出
// 「第一笔 7.5 全额、第二笔 2.5 部分」。现在选中状态 = 分摊表里有没有这一行。
function toggleRefundOrder(id) {
  if (!createVisible.value || createLoading.value) return;
  const value = Number(id);
  if (!canToggleRefundOrder(value)) return;
  if (!Number.isSafeInteger(value) || value <= 0) return;
  const adding = !isRefundOrderSelected(value);
  if (!adding) {
    draft.refundAllocations = (Array.isArray(draft.refundAllocations)
      ? draft.refundAllocations
      : []
    ).filter(item => item.orderId !== value)
  } else {
    // 已达申请金额 → 不能再加。
    // 用户原话：「因为两笔的退款金额已经达到了申请退款金额，
    // 所以不能选择第三笔」。要选第三笔就得先把金额改小。
    if (refundRemainingCents.value <= 0) return
    draft.refundAllocations = [
      ...(Array.isArray(draft.refundAllocations) ? draft.refundAllocations : []),
      { orderId: value, cents: 0, manual: false },
    ]
  }
  recomputeRefundAllocations(adding ? value : undefined)
  retireCreateValidation()
}

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
watch([() => draft.subject, () => draft.body, () => draft.requestedYuan, () => draft.reason, () => draft.issueType], retireStaleCreateValidation, { flush: "sync" });
watch(createNeedsRefundFields, needs => { if (needs) void loadRefundSummary(); }, { flush: "sync" });
watch(createVisible, next => { if (!next) { resetRefundDraft(); payoutQrBusy.value = false; retireCreateValidation(); createVersion += 1; createLoading.value = false; createSaved.value = false; draft.ticketType = TICKET_CREATE_TYPE_GENERAL; draft.subject = ""; draft.body = ""; draft.requestedYuan = ""; draft.reason = ""; draft.refundOrderId = undefined; draft.issueType = ""; createStep.value = ticketWizardInitialStep(); } }, { flush: "sync" });
watch(isAdmin, () => { createVisible.value = false; clearOwned(); reloadOwned(); }, { flush: "sync" });
// Focus leases also retire for same-id non-admin role ABA and unexpected routes.
watch(() => String(currentUser.value?.role ?? ""), retireDetailFocus, { flush: "sync" });
watch(() => route.fullPath, path => {
  if (path !== "/tickets" && path !== detailFocusEntry?.path) retireDetailFocus();
}, { flush: "sync" });
watch(() => currentUser.value?.status, () => { clearOwned(); reloadOwned(); }, { flush: "sync" });
onBeforeUnmount(() => { active = false; resetRefundDraft(); retireReplyDraft(); });
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
    const params = buildTicketListParams(filters, pagination.current, pagination.pageSize);
    if (routeViewMode.value === 'self' && isAdminRoleUser.value) params.scope = 'self';
    const result = normalizeTicketListResponse(await getTickets(params));
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
// 「待处理退款」快捷筛选的激活判据。
//
// 不用独立 state，直接从 filters 推导：这样用户手动把分类/状态改掉时，
// 按钮高亮会自动跟随，不会出现「按钮亮着但列表已不是那个条件」的错配。
// ⚠️ 判据只认 category，**不能**加 status。
//
// 退款工单在 ticket 表上的 status 由 ticketRefundShellStatus 映射出两个值：
//   pending_review / frozen       → open
//   processing / unknown          → pending
// 也就是「待处理」横跨 open 与 pending 两个 ticket status。后端 status 是单值
// 参数，写死 open 会漏掉 processing 那批（正在打款的单），这正是最需要看的。
// 所以快捷筛选只按 category=refund，状态交给用户自己在下拉里细分。
const refundPendingActive = computed(() => filters.category === "refund");
// toggleRefundPending 一键切到「退款 + 待处理」。
//
// 再点一次回到不筛选：否则用户「点一下想取消」时会卡住（找不到回到全部的路）。
// status 用 open：退款工单建单后即 open（见后端 createRefundTicketWithVerifier），
// 处理完会被推进到 closed，所以 open 就是「还没处理」。
function toggleRefundPending() {
  if (!canUseTickets()) return;
  if (refundPendingActive.value) {
    filters.category = "";
  } else {
    filters.category = "refund";
    // 不动 status：用户可能正按某个状态在看，切换类别时保留他的状态选择。
  }
  pagination.current = 1;
  applyFilters();
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
// openCreate 打开**两步向导的第一步**（选类型）。
//
// ⚠️ 不要在这里预设类型（例如落到 general）：用户点的是「发起工单」，
// 替他预先选中某一个模板等于替他做了决定，而他要的正是「先选类型」。
function openCreate() {
  if (!canCreateTickets()) return;
  retireCreateValidation();
  actionFailures.create.clear(); createFailureDraft = {};
  createVersion += 1; createLoading.value = false; createSaved.value = false;
  refundOrdersVersion += 1; refundOrdersLoading.value = false;
  refundOrderOptions.value = [];
  draft.ticketType = "";
  draft.subject = "";
  draft.body = "";
  draft.requestedYuan = "";
  draft.reason = "";
  draft.refundOrderIds = [];
  // 承诺书「每次都要签」（2026-11-19 用户要求）：重置时清掉。
  // 不清的话用户下一次发起退款会被当成已签 —— 而那一签是上一张单的。
  draft.refundAgreementSigned = false;
  draft.refundSignatureAttachmentId = 0;
  // 收款码记忆由后端和本机设备密钥恢复。
  // 用户下次发起退款会自动带出上次那张，避免每次重传。
  // 真正的清除入口是图片左上角的删除按钮。
  draft.issueType = "";
  createStep.value = ticketWizardInitialStep();
  // 选择人工退款时，收款码组件查询后端并尝试本机设备验证。
  resetRefundDraft();
  createVisible.value = true;
}
// selectCreateType 是第一步的单选回调。
//
// 只改 draft.ticketType，**不**自动前进：向导的「下一步」是用户的显式动作
// （选错类型要能改回来，自动跳步会让他被迫回退）。
function selectCreateType(value) {
  draft.ticketType = String(value ?? "").trim().toLowerCase();
  retireCreateValidation();
}
// advanceFromTypeStep 是第一步的「下一步」。
//
// 注销申请走**引导页**（T4：不能跳过引导直接进表单）；
// 其余类型直接进模板。分叉逻辑放在 helper 里（ticketWizardNextStep），
// 页面只负责应用结果 —— 这样「哪些类型要先引导」只有一处定义。
function advanceFromTypeStep() {
  if (!createVisible.value || !createTypeChosen.value) return;
  retireCreateValidation();
  createStep.value = ticketWizardNextStep(draft.ticketType);
}
// goCreateForm 是引导页**次按钮**（仍然提交工单）的回调：
// 用户已明确拒绝自助注销，这一步才允许进入注销工单表单。
function goCreateForm() {
  if (!createVisible.value) return;
  retireCreateValidation();
  createStep.value = TICKET_WIZARD_STEP_FORM;
}
// backToTypeStep 回第一步（表单里的「上一步」）。
// createAtFirstStep 判断当前是否停在第一步（类型选择）。
//
// 「上一步」在除第一步外的所有步骤都要出现（含注销引导页），
// 所以 footer 用它做 v-if，而不是逐步骤枚举 —— 枚举的写法在新增步骤时
// 必然漏掉，而漏掉的表现就是「这一页只有取消」（正是本次要修的）。
const createAtFirstStep = computed(() => createStep.value === TICKET_WIZARD_STEP_TYPE);function backToTypeStep() {
  if (!createVisible.value || createLoading.value) return;
  retireCreateValidation();
  createStep.value = TICKET_WIZARD_STEP_TYPE;
}
// closeCreate 是「取消」：直接关闭弹窗（与右上角 X 同义）。
function closeCreate() {
  createVisible.value = false;
}
async function submitTicket() {
  if (!canCreateTickets() || !createVisible.value || createLoading.value || payoutQrBusy.value || agreementLoading.value || uploadBusy.value) return;
  // 只在「填模板」这一步允许提交。
  //
  // ⚠️ 这就是 T4 的钉子：如果这里不判步骤，注销类型会在引导页就被提交
  // （用户还没看到「推荐自助注销」就被建了一张注销工单）。
  // 判定走 helper 的 ticketWizardCanSubmit（可在 node 里直接测），
  // 不在页面里内联一句 if —— 内联的判定只能靠手工点界面才发现回归。
  if (!ticketWizardCanSubmit(createStep.value)) return;
  // 各类型是**多套字段与多套校验**（任务书明确要求：不能只换个 category 字符串）。
  // 校验在这里统一做一次，失败就停，不发请求。
  const refundMode = ticketCreateTypeNeedsRefundFields(draft.ticketType);
  const validation = currentDraftValidation();
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
    // account / account_closure 各自生成标题（账号问题带类型前缀、注销固定前缀）。
    if (refundMode && draft.refundMethod === REFUND_METHOD_MANUAL) {
      if (!refundPhoneStatus.loaded) throw new Error("请等待手机号验证状态加载完成");
      await ensurePayoutQrDraft(owns);
      if (!owns()) return;
    }
    const built = buildTicketCreatePayload({
      ticketType: draft.ticketType,
      subject, body,
      requestedYuan: draft.requestedYuan,
      reason: draft.reason,
      refundOrderIds: draft.refundOrderIds,
      refundMethod: draft.refundMethod,
      refundAllocations: draft.refundAllocations,
      refundPayoutQrAttachmentId: draft.refundPayoutQrAttachmentId,
      refundSignatureAttachmentId: draft.refundSignatureAttachmentId,
      refundAgreementSigned: draft.refundAgreementSigned,
      refundAgreementText: agreementText.value,
      refundPhoneSmsCode: draft.refundPhoneSmsCode,
      refundPhoneVerificationRequired: refundPhoneStatus.required,
      issueType: draft.issueType,
      attachmentIds,
    });
    const payload = { ...built.payload };
    // 2026-11-18：**所有类型**都要支持上传图片（用户明确要求）。
    //
    // 原来退款类型会在这里 delete payload.attachmentIds，理由是「退款证据走退款
    // 自己的凭证上传」。但那是两回事：退款凭证是审核用的付款凭据，工单附件是
    // 用户说明问题的截图 —— 退款申请同样需要后者（比如订单页面的报错截图）。
    // 去掉这条限制后，四种类型的建单请求体都带 attachmentIds。
    await createTicket(payload);
    if (!owns()) return;
    message.success(refundMode ? "退款申请已提交，可在工单列表跟踪进度" : "工单已提交");
    draftAttachments.value = [];
    if (draft.subject === subject && draft.body === body) createVisible.value = false;
    else createSaved.value = true;
    pagination.current = 1; await loadTickets();
  } catch (error) { if (owns()) {
    if (refundMode && /重新签署当前协议/.test(String(error?.message || ""))) resetAgreement();
    actionFailures.create.show(error, ownsFailure); message.error(responseMessage(error, "工单提交失败"));
  } }
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
// reloadDetail 重新拉当前详情，用于退款审核/打款成功后同步状态。
//
// 不直接改本地 detailTicket.refund：审核会改 approvedCents、打款会改 status，
// 本地拼状态容易与后端漂移（例如并发的另一次审核）。重拉一次最省心，
// 代价只是一次 GET。
function reloadDetail() {
  const id = detailTicket.value?.id;
  if (id) loadDetail(id);
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
/* 退款区块的视觉分界：它和工单列表**不是**两条独立流程（数据已合并），
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
.ticket-refund-retry { padding: 0; border: 0; background: transparent; color: var(--primary); cursor: pointer; font: inherit; text-decoration: underline; }
.ticket-refund-order--disabled { background: var(--console-subtle, #f5f5f5); border-color: var(--console-border); color: var(--console-muted); }
.ticket-refund-order--disabled .ticket-refund-order__label { color: var(--console-muted); }
.ticket-refund-order__meta { color: var(--console-muted); font-size: 12px; }
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
/* ── 发起工单两步向导（B2-3）── */
.ticket-wizard-step {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.ticket-wizard-lead {
  margin: 0;
  color: var(--console-muted);
  font-size: 13px;
}
/* 类型选项用竖排卡片而不是下拉：4 项都带说明，下拉里读不到 hint。 */
.ticket-type-choices {
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 8px;
  width: 100%;
}
/* 退款订单多选：原生复选框列表（与类型选择同一套思路，不依赖组件内部状态）。 */
.ticket-refund-orders {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
}
.ticket-refund-orders-hint {
  margin: 0;
  color: var(--console-muted);
  font-size: 13px;
}
.ticket-refund-order {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 8px 10px;
  border: 1px solid var(--console-border);
  border-radius: 8px;
  cursor: pointer;
  font-weight: normal;
}
.ticket-refund-order:hover {
  border-color: var(--primary);
}
.ticket-refund-order--checked {
  border-color: var(--primary);
  box-shadow: inset 0 0 0 1px var(--primary);
}
.ticket-refund-order input {
  margin-top: 3px;
  flex: 0 0 auto;
}
.ticket-refund-order__body {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.ticket-refund-order__label {
  color: var(--console-text);
}
/* 「仅人工退款」标记：缺少渠道凭证的订单不能原路退回，必须让用户看见。 */
/* 不能原路退回的原因：单独一行，比 tag 更淡，避免抢了订单号的注意力。 */
/* 退回方式（原生 button，不用 a-radio-group）。 */
.ticket-refund-method {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}
.ticket-refund-method__option {
  flex: 1 1 220px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 12px 14px;
  text-align: left;
  cursor: pointer;
  border: 1px solid var(--console-border);
  border-radius: 8px;
  background: var(--console-surface);
  color: inherit;
  font: inherit;
}
.ticket-refund-method__option--active {
  border-color: var(--primary);
  box-shadow: 0 0 0 1px var(--primary) inset;
}
.ticket-refund-method__option:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}
.ticket-refund-method__title {
  font-weight: 600;
}
.ticket-refund-method__desc {
  color: var(--console-muted);
  font-size: 12px;
  line-height: 18px;
}
/* 分摊金额：每行订单可直接改金额。 */
.ticket-refund-alloc {
  flex-basis: 100%;
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 6px;
}
.ticket-refund-alloc__label {
  color: var(--console-muted);
  font-size: 12px;
}
.ticket-refund-alloc__input {
  width: 92px;
  padding: 2px 6px;
  border: 1px solid var(--console-border);
  border-radius: 4px;
  background: var(--console-surface);
  color: inherit;
  font: inherit;
  font-size: 13px;
}
.ticket-refund-alloc__unit {
  color: var(--console-muted);
  font-size: 12px;
}
.ticket-refund-alloc__badge {
  padding: 0 6px;
  border-radius: 999px;
  background: var(--success, #52c41a);
  color: #fff;
  font-size: 11px;
  line-height: 16px;
}
.ticket-refund-alloc__badge--partial {
  background: var(--warning, #faad14);
}
.ticket-refund-summary {
  margin-top: 10px;
  padding: 8px 10px;
  border-radius: 6px;
  background: var(--console-surface-sunken, rgba(0, 0, 0, 0.03));
  color: var(--console-muted);
  font-size: 13px;
  line-height: 1.5;
}
.ticket-refund-summary__content { display: grid; gap: 6px; max-width: 520px; }
.ticket-refund-summary__amounts { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px 16px; margin: 0; }
.ticket-refund-summary__amounts > div { display: grid; grid-template-columns: max-content 1fr; align-items: baseline; gap: 5px; min-width: 0; }
.ticket-refund-summary__amounts dt { color: var(--console-muted); }
.ticket-refund-summary__amounts dd { margin: 0; color: var(--console-text); font-variant-numeric: tabular-nums; white-space: nowrap; }
.ticket-refund-summary__refundable dd { color: var(--primary); font-weight: 600; }
.ticket-refund-summary__nonrefundable dd { color: var(--console-muted); }
.ticket-refund-summary__reason { margin: 0; overflow-wrap: anywhere; }
.ticket-refund-summary__reason strong { color: var(--console-text); font-weight: 500; }
@media (max-width: 520px) { .ticket-refund-summary__amounts { grid-template-columns: 1fr; gap: 3px; } }
/* 手机号确认。 */
.ticket-refund-phone {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.ticket-refund-phone__value {
  font-weight: 600;
  letter-spacing: 0.5px;
}
.ticket-refund-phone__confirm {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  font-size: 13px;
  line-height: 20px;
}
.ticket-refund-agreement {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.ticket-refund-agreement__check {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  font-size: 13px;
  line-height: 20px;
}
.ticket-refund-phone__missing {
  color: var(--console-muted);
  font-size: 12px;
  line-height: 18px;
}

.ticket-refund-order__reason {
  flex-basis: 100%;
  color: var(--console-muted);
  font-size: 12px;
  line-height: 18px;
}
.ticket-refund-order__tag {
  padding: 0 6px;
  border: 1px solid var(--console-border);
  border-radius: 999px;
  color: var(--console-muted);
  font-size: 12px;
  line-height: 18px;
}
.ticket-type-choice {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  width: 100%;
  padding: 10px 12px;
  border: 1px solid var(--console-border);
  border-radius: 8px;
  background: transparent;
  text-align: left;
  cursor: pointer;
  font: inherit;
  color: inherit;
}
.ticket-type-choice:hover:not(:disabled) {
  border-color: var(--primary);
}
/* 选中态由 :class 显式绑定 draft.ticketType，不依赖任何组件内部状态。 */
.ticket-type-choice--checked {
  border-color: var(--primary);
  box-shadow: inset 0 0 0 1px var(--primary);
}
.ticket-type-choice:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}
/* 自绘圆点：避免依赖 antd 的 .ant-radio-inner（那个元素 opacity:0 只剩视觉层）。 */
.ticket-type-choice__dot {
  flex: 0 0 auto;
  width: 16px;
  height: 16px;
  margin-top: 2px;
  border: 1px solid var(--console-border);
  border-radius: 50%;
}
.ticket-type-choice--checked .ticket-type-choice__dot {
  border-color: var(--primary);
  border-width: 5px;
}
.ticket-type-choice__body {
  display: flex;
  flex-direction: column;
  gap: 2px;
  flex: 1 1 auto;
  min-width: 0;
}
.ticket-type-choice__label {
  color: var(--console-text);
  font-weight: 500;
}
.ticket-type-choice__hint {
  color: var(--console-muted);
  font-size: 12px;
}
.ticket-wizard-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 16px;
}
</style>
