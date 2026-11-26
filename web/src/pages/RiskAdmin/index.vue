<template>
  <div class="console-page risk-admin-page">
    <ConsolePageHeading
      title="风控管理"
      description="保护公开注册与密码登录；查看实际生效条件、告警证据和处置记录。"
    >
      <template #actions>
        <a-button v-if="section === 'alerts'" :loading="loadingAlerts"  :disabled="!canManage() || loadingAlerts || writing" @click="loadAlerts">刷新告警</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert
      v-if="!canManage()"
      type="warning"
      show-icon
      class="page-alert"
      message="当前身份无风控管理权限，请使用管理员账号登录。"
    />

    <a-alert
      v-if="canManage()"
      type="info"
      show-icon
      class="page-alert"
      :message="RISK_ADVISORY_NOTE"
    />

    <template v-if="canManage()">
      <nav class="risk-nav" aria-label="风控管理视图">
        <a-button v-for="tab in sections" :key="tab.key" :data-risk-nav="tab.key"
          :aria-pressed="section === tab.key" :type="section === tab.key ? 'primary' : 'default'"
          @click="section = tab.key">{{ tab.label }}</a-button>
      </nav>
      <ConsolePanel v-if="section === 'core'" title="注册与登录保护" description="每条规则单独设置处置，仅影响后续新告警；密码登录入口的实时限速仍独立生效。">
        <ul class="field-hint"><li v-for="item in coreDetectors" :key="item.type">{{ item.name }}：{{ item.summary }}</li></ul>
      </ConsolePanel>

      <!-- ============ 风控通知配置（独立于工单，C2/C3） ============ -->
      <ConsolePanel
        v-if="section === 'global'"
        title="企业微信风控通知"
        description="风控通道专用配置。保存后立即生效；Webhook 地址不会回显完整内容。"
        class="risk-config-panel"
      >
        <!--
          ⚠️ 这段提示是用户明确要求写进 UI 的原文（96 §4.2）。
          任何简化（如只写「两套配置互相独立」）都会让管理员误以为填同一个地址等于共享一份配置。
        -->
        <a-alert
          type="warning"
          show-icon
          class="risk-independence-notice"
          :message="RISK_INDEPENDENCE_NOTICE"
        />

        <a-alert
          v-if="configError"
          type="error"
          show-icon
          class="status-alert"
          :message="configError"
        />

        <a-form layout="vertical" class="risk-config-form" :on-finish="saveSettings">
          <div class="config-grid">
            <a-form-item label="启用风控通知">
              <a-switch
                :checked="config.enabled"
                :disabled="!configLoaded || writing || loadingConfig"
                checked-children="开启"
                un-checked-children="关闭"
                v-on="{ 'update:checked': toggleConfigEnabled }"
              />
              <div class="field-hint">
                关闭后风控告警仍然落库，只是不推送（节流与开关都不影响留痕）。
              </div>
            </a-form-item>

            <a-form-item
              label="风控 Webhook 地址"
              help="只接受企业微信机器人 HTTPS 地址；留空表示保持已保存地址。"
            >
              <a-input
                :disabled="!configLoaded || writing"
                :value="config.webhookUrl"
                type="url"
                allow-clear
                placeholder="输入新地址以替换已保存的风控地址"
                @update:value="value => setConfig('webhookUrl', value)"
              />
              <div v-if="config.configured" class="config-address-actions">
                <span>{{ config.clearWebhook ? '保存后将清除当前风控地址' : '已配置风控机器人' }}</span>
                <a-button
                  v-if="!config.clearWebhook"
                  type="link"
                  danger
                  size="small"
                  :disabled="writing"
                  @click="clearSavedWebhook"
                >清除风控地址</a-button>
                <a-button
                  v-else
                  type="link"
                  size="small"
                  :disabled="writing"
                  @click="keepSavedWebhook"
                >保留风控地址</a-button>
              </div>
              <div class="field-hint" data-risk-preview>
                {{ describeRiskWebhookState(settings) }}
              </div>
            </a-form-item>

            <a-form-item label="风控 @成员 ID" help="多个成员用逗号分隔，可填写 @all。">
              <a-input
                :disabled="!configLoaded || writing"
                :value="config.mentionedList"
                allow-clear
                placeholder="成员 ID，可选"
                @update:value="value => setConfig('mentionedList', value)"
              />
            </a-form-item>
          </div>

          <a-space wrap>
            <a-button
              type="primary"
              html-type="submit"
              :loading="writing"
              :disabled="!configLoaded || writing"
            >保存风控通知设置</a-button>
            <a-button
              :loading="writing"
              :disabled="!configLoaded || writing"
              @click="sendTest"
            >发送测试消息</a-button>
            <span v-if="config.configured" class="config-hint">当前已配置风控 Webhook</span>
          </a-space>
        </a-form>

        <p v-if="configError" class="filter-error" role="alert">{{ configError }}</p>
        <p v-if="configSuccess" class="config-success" role="status">{{ configSuccess }}</p>
      </ConsolePanel>

      <ConsolePanel v-if="section === 'core' || section === 'other'" class="risk-rules-panel"
        :title="section === 'core' ? '核心保护的触发条件' : '其他检测规则'"
        :description="section === 'core' ? '每条实例单独设置触发条件、通知级别、节流间隔和处置策略。' : '保留既有业务和行为告警；这些规则不会自动封禁。'">
        <template #actions>
          <a-button type="primary" :disabled="!canManage() || rulesLoading || ruleWriting" @click="openCreateRule">新增规则</a-button>
        </template>
        <p v-if="rulesError" class="filter-error" role="alert">{{ rulesError }}</p>
        <p v-if="rulesSuccess" class="config-success" role="status">{{ rulesSuccess }}</p>
        <a-input v-if="section === 'other'" v-model:value="ruleSearch" allow-clear placeholder="筛选规则名称或类型" aria-label="筛选其他规则" class="risk-search" />
        <div v-if="!visibleRules.length" class="field-hint">
          {{ rulesLoading ? '正在读取规则…' : '没有匹配的规则实例。' }}
        </div>
        <div v-else class="risk-rule-list">
          <div
            v-for="rule in visibleRules"
            :key="rule.ruleId"
            class="risk-rule-row"
            :class="{ 'risk-rule-row-disabled': !rule.enabled }"
          >
            <div class="risk-rule-main">
              <div class="risk-rule-title">
                <span class="risk-rule-name">{{ rule.name }}</span>
                <a-tag :color="rule.source === 'builtin' ? 'blue' : 'purple'">{{ rule.source === 'builtin' ? '内置默认' : '自定义' }}</a-tag>
                <a-tag :color="riskSeverityMeta(rule.severity).color">
                  {{ riskSeverityMeta(rule.severity).label }}
                </a-tag>
                <a-tag v-if="!rule.enabled" color="default">已停用</a-tag>
              </div>
              <div class="risk-rule-meta">
                <code class="risk-rule-id">{{ rule.ruleId }}</code>
                <span class="risk-rule-type">类型：{{ ruleTypeName(rule.type) }}</span>
                <span v-if="rule.hasThreshold" class="risk-rule-threshold">
                  生效阈值：{{ rule.threshold }}{{ ruleUnit(rule) }}
                </span>
              </div>
              <p class="field-hint" data-rule-response>生效处置：{{ rulePolicyLabel(rule.effectiveResponsePolicy) }}<span v-if="rulePolicyLabel(rule.responsePolicy) !== rulePolicyLabel(rule.effectiveResponsePolicy)"> · 已配置：{{ rulePolicyLabel(rule.responsePolicy) }}</span></p>
              <details v-if="rule.description" class="risk-rule-desc"><summary>规则说明与备注</summary>{{ rule.description }}</details>
              <p class="risk-rule-updated">更新于 {{ formatRiskDateTime(rule.updatedAt) }}<span v-if="rule.updatedBy"> · {{ rule.updatedBy }}</span></p>
            </div>
            <div class="risk-rule-actions">
              <a-switch
                :checked="rule.enabled"
                :loading="ruleToggling[rule.ruleId]"
                :disabled="ruleWriting || ruleDeleting[rule.ruleId]"
                checked-children="启用"
                un-checked-children="停用"
                @change="checked => toggleRule(rule, checked)"
              />
              <a-button size="small" :disabled="ruleWriting || ruleDeleting[rule.ruleId] || ruleToggling[rule.ruleId]" @click="openEditRule(rule)">编辑</a-button>
              <a-button v-if="sharedTypes.some(it => it.value === rule.type)" size="small" @click="openShared(rule.type)">同类共享条件</a-button>
              <a-button v-if="primaryOverride(rule)" size="small" :disabled="thresholdWriting" @click="resetThreshold(primaryOverride(rule))">恢复默认阈值</a-button>
              <a-popconfirm
                title="删除后该规则停止检测，已有告警记录会保留。确定删除吗？"
                ok-text="删除"
                cancel-text="取消"
                :onConfirm="() => deleteRule(rule)"
              >
                <a-button size="small" danger :loading="ruleDeleting[rule.ruleId]" :disabled="ruleWriting || ruleToggling[rule.ruleId]">删除</a-button>
              </a-popconfirm>
            </div>
          </div>
        </div>
        <p v-if="thresholdSuccess" class="config-success" role="status">{{ thresholdSuccess }}</p>
        <p v-if="thresholdError" class="filter-error" role="alert">{{ thresholdError }}</p>
      </ConsolePanel>

      <ConsolePanel v-if="section === 'shared'" title="类型共享条件" description="在这里修改会影响此类型的全部实例。每条实例的主阈值请在规则编辑器调整。">
        <label :for="fieldId('shared-detector')">检测类型</label>
        <a-select :id="fieldId('shared-detector')" :value="effectiveSharedType" :options="sharedTypes" class="filter-rule"
          @update:value="value => sharedType = value" />
        <span class="field-hint">当前影响 {{ rules.filter(rule => rule.type === effectiveSharedType).length }} 条实例（含停用）。</span>
        <RiskWindowSettings v-if="effectiveSharedType === 'offhours_activity'" class="window-settings" :key="ownerKey" :capture-scope="captureRuleScope" />
      </ConsolePanel>
      <ConsolePanel v-if="section === 'shared' && (visibleThresholds.length || thresholdError)" class="risk-threshold-panel" title="此类型的共享阈值">
        <p v-if="thresholdError" class="filter-error" role="alert">{{ thresholdError }}</p>
        <p v-if="thresholdSuccess" class="config-success" role="status">{{ thresholdSuccess }}</p>
        <div v-if="!visibleThresholds.length" class="field-hint">
          {{ thresholdsLoading ? '正在读取阈值…' : '暂无可调阈值' }}
        </div>
        <a-form v-else layout="vertical" class="risk-threshold-form">
          <div class="config-grid">
            <a-form-item
              v-for="item in visibleThresholds"
              :key="item.key"
              :label="item.label"
              :help="describeThresholdSource(item)"
            >
              <a-input-group compact>
                <a-input-number
                  class="threshold-input"
                  :value="thresholdDraft[item.key]"
                  :min="0" :max="item.maxValue || undefined" :precision="item.kind === 'integer' ? 0 : undefined"
                  :disabled="thresholdWriting"
                  @update:value="value => setThresholdDraft(item.key, value)"
                />
                <span class="threshold-unit">{{ item.unit }}</span>
                <a-button
                  type="primary"
                  size="small"
                  :loading="thresholdWriting"
                  @click="saveThreshold(item)"
                >保存</a-button>
                <a-button
                  v-if="item.configured"
                  size="small"
                  :disabled="thresholdWriting"
                  @click="resetThreshold(item)"
                >重置</a-button>
              </a-input-group>
            </a-form-item>
          </div>
        </a-form>
      </ConsolePanel>

      <ConsolePanel v-if="(section === 'shared' || section === 'global') && (visibleConfigs.length || configItemsError)" class="risk-config-panel"
        :title="section === 'global' ? '全站评估调度' : '此类型的共享配置'">
        <p v-if="configItemsError" class="filter-error" role="alert">{{ configItemsError }}</p>
        <p v-if="configItemsSuccess" class="config-success" role="status">{{ configItemsSuccess }}</p>
        <div v-if="!visibleConfigs.length" class="field-hint">
          {{ configItemsLoading ? '正在读取配置…' : '暂无可调配置' }}
        </div>
        <a-form v-else layout="vertical" class="risk-threshold-form">
          <div class="config-grid">
            <a-form-item
              v-for="item in visibleConfigs"
              :key="item.key"
              :label="item.label"
              :help="item.help"
            >
              <a-input
                v-if="item.control === 'textarea'"
                :value="configItemDraft[item.key]"
                :disabled="configItemsWriting"
                :placeholder="item.kind === 'asn_list' ? '形如 AS15169, AS16509（留空 = 关闭规则）' : ''"
                allow-clear
                @update:value="value => setConfigItemDraft(item.key, value)"
              />
              <a-input-number
                v-else
                class="threshold-input"
                :value="configItemDraft[item.key]"
                :min="item.min ?? 0"
                :max="item.max ?? undefined"
                :precision="0"
                :disabled="configItemsWriting"
                @update:value="value => setConfigItemDraft(item.key, value)"
              />
              <span v-if="item.unit" class="threshold-unit">{{ item.unit }}</span>
              <div class="field-hint">
                {{ describeRiskConfigEmptyMeaning(item) }}
              </div>
              <a-input-group compact>
                <a-button
                  type="primary"
                  size="small"
                  :loading="configItemsWriting"
                  @click="saveConfigItem(item)"
                >保存</a-button>
                <a-button
                  v-if="item.configured"
                  size="small"
                  :disabled="configItemsWriting"
                  @click="resetConfigItem(item)"
                >重置</a-button>
              </a-input-group>
              <div class="field-hint" data-risk-config-source>
                当前生效：{{ item.value || '（空）' }}{{ item.unit && item.value ? ' ' + item.unit : '' }}
                ｜默认值：{{ item.defaultValue || '（空）' }}{{ item.unit && item.defaultValue ? ' ' + item.unit : '' }}
                ｜{{ item.configured ? '来源：管理页配置' : '未显式配置（读的是代码默认值）' }}
              </div>
            </a-form-item>
          </div>
        </a-form>
      </ConsolePanel>

      <!-- 告警检索与证据；处置针对具体告警。 -->
      <ConsoleSummaryGrid v-if="section === 'alerts'" class="risk-summary">
        <div>
          <div class="summary-label">筛选结果</div>
          <div class="summary-value">{{ hasLoadedAlerts ? alertsTotal : '--' }} 条</div>
        </div>
        <div>
          <div class="summary-label">本页已推送</div>
          <div class="summary-value">{{ pushedOnPage }} / {{ alerts.length }}</div>
        </div>
        <div>
          <div class="summary-label">风控通知状态</div>
          <div class="summary-value summary-value-sm">{{ settings.configured ? (settings.enabled ? '已启用' : '已配置未启用') : '未配置' }}</div>
        </div>
      </ConsoleSummaryGrid>

      <ConsolePanel v-if="section === 'alerts'" title="筛选" description="时间按北京时间计算；结束日期含当天。">
        <a-form layout="inline" class="filter-form" :on-finish="reloadAlerts">
          <a-form-item label="规则" :html-for="fieldId('ruleId')">
            <a-select
              :id="fieldId('ruleId')"
              :value="alertFilters.ruleId"
              :options="ruleOptions"
              allow-clear
              placeholder="全部规则"
              class="filter-rule"
              :disabled="loadingAlerts"
              @update:value="value => setFilter('ruleId', value)"
            />
          </a-form-item>
          <a-form-item label="用户 ID" :html-for="fieldId('userId')">
            <a-input
              :id="fieldId('userId')"
              :value="alertFilters.userId"
              inputmode="numeric"
              allow-clear
              placeholder="精确用户 ID"
              class="filter-user"
              :disabled="loadingAlerts"
              @update:value="value => setFilter('userId', value)"
              @press-enter="reloadAlerts"
            />
          </a-form-item>
          <a-form-item label="推送状态" :html-for="fieldId('notified')">
            <a-select
              :id="fieldId('notified')"
              :value="alertFilters.notified"
              :options="notifiedOptions"
              allow-clear
              placeholder="全部状态"
              class="filter-notified"
              :disabled="loadingAlerts"
              @update:value="value => setFilter('notified', value)"
            />
          </a-form-item>
          <a-form-item label="开始日期" :html-for="fieldId('createdFrom')">
            <a-input
              :id="fieldId('createdFrom')"
              :value="alertFilters.createdFrom"
              type="date"
              class="filter-date"
              :disabled="loadingAlerts"
              @update:value="value => setFilter('createdFrom', value)"
            />
          </a-form-item>
          <a-form-item label="结束日期" :html-for="fieldId('createdTo')">
            <a-input
              :id="fieldId('createdTo')"
              :value="alertFilters.createdTo"
              type="date"
              class="filter-date"
              :disabled="loadingAlerts"
              @update:value="value => setFilter('createdTo', value)"
            />
          </a-form-item>
          <a-form-item>
            <a-space wrap>
              <a-button type="primary" html-type="submit" :loading="loadingAlerts" :disabled="loadingAlerts">查询</a-button>
              <a-button :disabled="loadingAlerts" @click="resetFilters">重置筛选</a-button>
            </a-space>
          </a-form-item>
        </a-form>

        <p v-if="filterError" class="filter-error" role="alert">{{ filterError }}</p>
        <a-alert v-if="alertError" type="error" show-icon :message="alertError" class="status-alert" />
      </ConsolePanel>

      <ConsolePanel v-if="section === 'alerts'" title="告警列表" :description="alertScopeDescription">
        <ConsoleTableShell label="风控告警列表">
          <a-table
            table-layout="fixed"
            :data-source="alerts"
            :columns="alertColumns"
            :loading="loadingAlerts"
            :pagination="alertPagination"
            row-key="id"
            :scroll="{ x: sumTableColumnWidths(alertColumns) }"
            :locale="{ emptyText: alertEmptyText }"
            @change="onTableChange"
          >
            <template #bodyCell="{ column, record }">
              <span v-if="column.key === 'id'">{{ record.id ?? '-' }}</span>
              <span v-else-if="column.key === 'ruleId'">{{ riskRuleLabel(record.ruleId, record.detail, rules) }}</span>
              <a-tag
                v-else-if="column.key === 'severity'"
                :color="riskSeverityMeta(record.severity).color"
              >{{ riskSeverityMeta(record.severity).label }}</a-tag>
              <span v-else-if="column.key === 'subjectKey'" class="subject-cell">{{ record.subjectKey || '—' }}</span>
              <span v-else-if="column.key === 'userId'">{{ record.userId ?? '—' }}</span>
              <a-tag v-else-if="column.key === 'notified'" :color="record.notified ? 'green' : 'default'">
                {{ record.notified ? '已推送' : '未推送' }}
              </a-tag>
              <span v-else-if="column.key === 'createdAt'" class="time-cell">{{ formatRiskDateTime(record.createdAt) }}</span>
              <template v-else-if="column.key === 'action'">
                <a-button
                  type="link"
                  :disabled="loadingAlerts"
                  @click="openDetail(record)"
                >查看详情</a-button>
              </template>
            </template>
          </a-table>
        </ConsoleTableShell>
      </ConsolePanel>
    </template>

    <!-- 证据快照不变；管理员的决定单独进入处置记录。 -->
    <a-modal
      :open="detailVisible"
      title="风控告警详情"
      :footer="null"
      :width="720"
      @cancel="closeDetail"
    >
      <a-spin :spinning="loadingDetail">
        <div v-if="detailError" class="filter-error" role="alert">{{ detailError }}</div>
        <template v-if="detail">
          <dl class="detail-list">
            <div class="detail-row"><dt>告警 ID</dt><dd>{{ detail.id }}</dd></div>
            <div class="detail-row"><dt>规则</dt><dd>{{ riskRuleLabel(detail.ruleId, detail.detail, rules) }}</dd></div>
            <div class="detail-row">
              <dt>级别</dt>
              <dd><a-tag :color="riskSeverityMeta(detail.severity).color">{{ riskSeverityMeta(detail.severity).label }}</a-tag></dd>
            </div>
            <div class="detail-row"><dt>主体</dt><dd>{{ detail.subjectKey || '—' }}</dd></div>
            <div class="detail-row"><dt>用户 ID</dt><dd>{{ detail.userId ?? '—' }}</dd></div>
            <div class="detail-row"><dt>来源 IP</dt><dd>{{ detail.ip || '—' }}</dd></div>
            <div class="detail-row"><dt>推送状态</dt><dd>{{ detail.notified ? '已推送' : '未推送' }}</dd></div>
            <div class="detail-row"><dt>推送时间</dt><dd>{{ formatRiskDateTime(detail.notifiedAt) }}</dd></div>
            <div class="detail-row"><dt>触发时间</dt><dd>{{ formatRiskDateTime(detail.createdAt) }}</dd></div>
          </dl>

          <h3 class="detail-evidence-title">证据快照</h3>
          <p class="field-hint">以下内容是触发时刻固化的证据，不是实时查询结果。</p>
          <a-table
            table-layout="fixed"
            size="small"
            :data-source="evidenceRows"
            :columns="evidenceColumns"
            :pagination="false"
            row-key="field"
          />
          <RiskAlertResponse v-if="detailVisible && canManage()" :key="ownerKey + ':' + detail.id"
            :alert-id="Number(detail.id)" :capture-scope="captureRuleScope" />
        </template>
      </a-spin>
    </a-modal>

    <a-modal
      :open="ruleEditorOpen"
      :title="ruleEditingId ? '编辑风控规则' : '新增风控规则'"
      :width="720"
      :style="{ maxWidth: 'calc(100vw - 32px)' }"
      :confirm-loading="ruleWriting"
      :ok-button-props="{ disabled: !ruleDraft.name.trim() || !ruleDraft.type }"
      ok-text="保存"
      cancel-text="取消"
      @ok="saveRule"
      @cancel="closeRuleEditor"
    >
      <a-form layout="vertical" class="risk-rule-editor">
        <section class="risk-editor-section" aria-label="规则信息">
          <h3>规则信息</h3>
          <div class="risk-rule-editor-grid">
            <a-form-item label="规则类型" :html-for="fieldId('editor-type')" :required="!ruleEditingId">
              <a-select
                v-if="!ruleEditingId"
                :id="fieldId('editor-type')"
                :value="ruleDraft.type"
                :options="ruleTypeOptions"
                placeholder="选择服务端支持的检测类型"
                @update:value="setRuleType"
              />
              <a-input
                v-else
                :id="fieldId('editor-type')"
                :value="selectedRuleType?.name || ruleDraft.type"
                disabled
              />
              <div class="field-hint">{{ ruleEditingId ? '创建后不能更换检测类型。' : '只能选择服务端支持的检测类型。' }}</div>
            </a-form-item>
            <a-form-item label="规则名称" :html-for="fieldId('editor-name')" required>
              <a-input :id="fieldId('editor-name')" v-model:value="ruleDraft.name" :maxlength="128" placeholder="为管理员容易识别的名称" />
            </a-form-item>
          </div>
          <a-form-item label="说明" :html-for="fieldId('editor-description')">
            <a-textarea :id="fieldId('editor-description')" v-model:value="ruleDraft.description" :rows="2" :maxlength="2048" show-count placeholder="说明规则触发条件与适用范围" />
          </a-form-item>
          <p class="field-hint risk-editor-identity">{{ ruleEditingId ? `规则 ID：${ruleEditingId}（保持不变）` : '规则 ID 由系统生成，创建后保持不变。' }}</p>
        </section>
        <section class="risk-editor-section" aria-label="触发条件">
          <h3>触发条件</h3>
          <p class="field-hint risk-editor-type-hint">{{ selectedRuleType?.description || '按所选检测类型的条件进行判断。' }}</p>
          <a-form-item
            v-if="selectedRuleType?.hasThreshold !== false"
            :label="`阈值${selectedRuleType?.thresholdUnit ? `（${selectedRuleType.thresholdUnit}）` : ''}`"
            :html-for="fieldId('editor-threshold')"
            required
          >
            <a-input-number :id="fieldId('editor-threshold')" v-model:value="ruleDraft.threshold" :min="selectedRuleType?.thresholdKind === 'integer' ? 1 : 0.000001" :max="selectedRuleType?.thresholdMax || undefined" :precision="selectedRuleType?.thresholdKind === 'integer' ? 0 : 6" class="risk-editor-number" />
            <div v-if="selectedRuleType" class="field-hint">默认值：{{ selectedRuleType.defaultThreshold }}{{ selectedRuleType.thresholdUnit ? ` ${selectedRuleType.thresholdUnit}` : '' }}。保存时由服务端校验该类型允许的范围。</div>
          </a-form-item>
          <p v-else class="field-hint">本检测不使用数字阈值，判断依据是同类型共享的名单。</p>
          <p v-if="sharedTypes.some(it => it.value === ruleDraft.type)" class="field-hint">时段、名单或辅助阈值由「类型共享条件」统一管理。</p>
        </section>
        <section class="risk-editor-section" aria-label="通知行为">
          <h3>通知行为</h3>
          <div class="risk-rule-editor-grid">
            <a-form-item label="告警级别" :html-for="fieldId('editor-severity')" required>
              <a-select :id="fieldId('editor-severity')" v-model:value="ruleDraft.severity" :options="severityOptions" />
              <div class="field-hint">仅用于发送通知时显示，不参与触发条件、处置动作或封禁范围判断。</div>
            </a-form-item>
            <a-form-item label="节流秒数" :html-for="fieldId('editor-throttle')">
              <a-input-number :id="fieldId('editor-throttle')" v-model:value="ruleDraft.throttleSeconds" :min="0" :precision="0" class="risk-editor-number" />
              <div class="field-hint">0 表示同一告警主体只记录一次；大于 0 时，同一主体在设定秒数内不重复记录。</div>
            </a-form-item>
          </div>
        </section>
        <section class="risk-editor-section" aria-label="处置策略">
          <h3>处置策略</h3>
          <p v-if="ruleEditingId" class="field-hint" data-editor-effective-policy>当前生效：{{ rulePolicyLabel(ruleDraft.savedEffectivePolicy) }}</p>
          <a-form-item label="处置动作" :html-for="fieldId('editor-response-mode')">
            <a-select :id="fieldId('editor-response-mode')" v-model:value="ruleDraft.responseMode" :options="responseModeOptions" :disabled="!selectedRuleType?.supportsResponsePolicy" />
            <p class="field-hint">{{ selectedRuleType?.supportsResponsePolicy ? '封禁遵守白名单和黑名单总开关，仅按可信单一来源归因；变更只用于新告警。' : '本检测类型未提供可信的网络处置来源，只能告警。' }}</p>
          </a-form-item>
          <template v-if="ruleDraft.responseMode !== 'notify'">
            <a-form-item label="封禁范围" :html-for="fieldId('editor-response-scope')">
              <a-select :id="fieldId('editor-response-scope')" v-model:value="ruleDraft.responseScope" :options="responseScopeOptions" />
              <p v-if="ruleDraft.responseScope === 'segment'" class="field-hint">只使用可信归属地缓存中的首尾地址；无可靠区间时只告警。</p>
            </a-form-item>
            <a-form-item v-if="ruleDraft.responseMode === 'temporary'" label="封禁时长（分钟）" :html-for="fieldId('editor-response-duration')" required>
              <a-input-number :id="fieldId('editor-response-duration')" v-model:value="ruleDraft.responseMinutes" :min="1" :max="1440" :precision="0" />
            </a-form-item>
            <a-checkbox v-if="rulePolicyRequiresConfirmation" v-model:checked="ruleDraft.responseConfirmed">我已核对范围与期限，确认对后续告警启用此策略</a-checkbox>
          </template>
        </section>
        <p v-if="rulesError" class="filter-error" role="alert">{{ rulesError }}</p>
      </a-form>
    </a-modal>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, reactive, ref, useId, watch } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import RiskWindowSettings from './RiskWindowSettings.vue'
import RiskAlertResponse from './RiskAlertResponse.vue'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import ConsoleTableShell from '@/components/ConsoleTableShell.vue'
import {
  getRiskAlertDetail,
  getRiskAlerts,
  getRiskConfigs,
  getRiskRules,
  getRiskRuleTypes,
  getRiskThresholds,
  getRiskWebhookSettings,
  createRiskRule,
  deleteRiskRule as deleteRiskRuleRequest,
  resetRiskConfig,
  resetRiskThreshold,
  setRiskRuleEnabled,
  testRiskWebhook,
  updateRiskConfig,
  updateRiskRule,
  updateRiskThreshold,
  updateRiskWebhookSettings,
} from '@/api/RiskAdminApi'
import {
  RISK_ADVISORY_NOTE,
  RISK_INDEPENDENCE_NOTICE,
  RISK_RULE_OPTIONS,
  buildRiskAlertListParams,
  buildRiskEvidenceRows,
  buildRiskWebhookUpdatePayload,
  describeRiskConfigEmptyMeaning,
  describeRiskWebhookState,
  formatMentionedInput,
  formatRiskDateTime,
  normalizeRiskConfigItems,
  normalizeRiskWebhookSettings,
  readRiskListBody,
  readRiskMessage,
  readRiskThrownMessage,
  riskRuleLabel,
  riskSeverityMeta,
  validateRiskAlertFilters,
  validateRiskConfigDraft,
} from './riskAdminHelpers.mjs'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

const coreDetectors = [
  { type: 'device_register_burst', name: '同一浏览器批量注册', summary: '基于同一注册浏览器标识统计成功注册的不同账号；不把共享 IP 当作一台设备。' },
  { type: 'login_bruteforce', name: '密码爆破', summary: '区分账号维度与来源 IP 维度；仅能归因到一个 IP 的告警可用于网络处置。' },
]
const coreType = type => coreDetectors.some(item => item.type === type)
const sections = [{ key: 'core', label: '核心保护' }, { key: 'other', label: '其他规则' }, { key: 'shared', label: '类型共享条件' }, { key: 'alerts', label: '告警与处置' }, { key: 'global', label: '全站设置' }]
const emptyFilters = () => ({ ruleId: undefined, userId: '', notified: undefined, createdFrom: '', createdTo: '' })
const emptyConfig = () => ({ enabled: false, webhookUrl: '', mentionedList: '', clearWebhook: false })
const emptySettings = () => ({ enabled: false, configured: false, webhookUrl: '', preview: '', mentionedList: [] })

export default {
  name: 'RiskAdminPage',
  components: { RiskWindowSettings, RiskAlertResponse, ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell },
  setup() {
    const route = useRoute()
    const prefix = 'risk-admin-' + useId()
    const fieldId = field => prefix + '-' + field
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    // 列表
    const ownerKey = computed(() => currentUserIdentity(currentUser.value))
    const section = ref('core'), ruleSearch = ref(''), sharedType = ref('offhours_activity')
    const alerts = ref([])
    const alertsTotal = ref(0)
    const hasLoadedAlerts = ref(false)
    const loadingAlerts = ref(false)
    const alertError = ref('')
    const filterError = ref('')
    const alertFilters = reactive(emptyFilters())
    const appliedFilters = ref({})

    // 详情
    const detail = ref(null)
    const detailVisible = ref(false)
    const loadingDetail = ref(false)
    const detailError = ref('')

    // 配置
    const settings = ref(emptySettings())
    const config = reactive(emptyConfig())
    const configLoaded = ref(false)
    const loadingConfig = ref(false)
    const writing = ref(false)
    const configError = ref('')
    const configSuccess = ref('')

    // 规则阈值（可写，落库后判定立即生效）
    //
    // thresholds 是服务端返回的**权威列表**（含每项的 kind/value/defaultValue/source），
    // thresholdDraft 只是编辑中的副本：不在 thresholds 上直接改，否则「保存失败」
    // 与「已生效」在界面上无法区分。
    const thresholds = ref([])
    const thresholdDraft = reactive({})
    const thresholdsLoading = ref(false)
    const thresholdWriting = ref(false)
    const thresholdError = ref('')
    const thresholdSuccess = ref('')
    // ---- 规则清单（启停 + 说明）----
    // rules 是 GET /risk/rules 的权威列表：每项含 ruleId / name / enabled /
    // severity / threshold / exists / description。
    // ruleToggling 按 ruleId 记录「正在保存」，避免整页 spinner 让其他行不可操作。
    const rules = ref([])
    const rulesLoading = ref(false)
    const rulesError = ref('')
    const rulesSuccess = ref('')
    const ruleToggling = reactive({})
    const ruleTypes = ref([])
    const ruleEditorOpen = ref(false)
    const ruleEditingId = ref('')
    const ruleWriting = ref(false)
    const ruleDeleting = reactive({})
    const ruleDraft = reactive({ type: '', name: '', description: '', severity: 'medium', threshold: 1, throttleSeconds: 0,
      responseMode: 'notify', responseScope: 'ip', responseMinutes: null, responseConfirmed: false, savedEffectivePolicy: null })
    const responseModeOptions = [
      { label: '仅告警', value: 'notify' }, { label: '告警 + 临时封禁', value: 'temporary' }, { label: '告警 + 永久封禁', value: 'permanent' },
    ]
    const responseScopeOptions = [{ label: '单 IP', value: 'ip' }, { label: '可信精确 IP 段', value: 'segment' }]
    const rulePolicyLabel = policy => {
      if (policy?.mode === 'disabled') return '规则已停用'
      const scope = policy?.scope === 'segment' ? '可信精确 IP 段' : '单 IP'
      if (policy?.mode === 'temporary') return '告警 + 临时封禁 · ' + scope + ' · ' + policy.durationMinutes + ' 分钟'
      if (policy?.mode === 'permanent') return '告警 + 永久封禁 · ' + scope + ' · 直至撤销'
      return '仅告警'
    }
    const rulePolicyRequiresConfirmation = computed(() => ruleDraft.responseMode === 'permanent' ||
      (ruleDraft.responseMode !== 'notify' && ruleDraft.responseScope === 'segment'))
    watch(() => [ruleDraft.responseMode, ruleDraft.responseScope, ruleDraft.responseMinutes], () => {
      ruleDraft.responseConfirmed = false
    }, { flush: 'sync' })
    const fillPolicyDraft = (policy, effective = null) => Object.assign(ruleDraft, {
      responseMode: policy?.mode || 'notify', responseScope: policy?.scope || 'ip',
      responseMinutes: policy?.durationMinutes ?? null, responseConfirmed: false, savedEffectivePolicy: effective,
    })
    const availableTypes = computed(() => ruleTypes.value.filter(item => coreType(item.type) === (section.value === 'core')))
    const ruleTypeOptions = computed(() => availableTypes.value.map(item => ({ label: item.name + ' · ' + item.type, value: item.type })))
    const selectedRuleType = computed(() => ruleTypes.value.find(item => item.type === ruleDraft.type) || null)
    const severityOptions = [
      { label: '低', value: 'low' },
      { label: '中', value: 'medium' },
      { label: '高', value: 'high' },
    ]

    // ---- 非数值配置（ASN 名单 / 评估间隔）----
    //
    // configItems 是 GET /risk/config 的权威列表（含 kind/help/configured）。
    // configItemDraft 只是编辑中的副本 —— 与阈值同一条纪律：
    // 不在权威列表上直接改，否则「保存失败」与「已生效」在界面上无法区分。
    const configItems = ref([])
    const configItemDraft = reactive({})
    const configItemsLoading = ref(false)
    const configItemsWriting = ref(false)
    const configItemsError = ref('')
    const configItemsSuccess = ref('')

    const visibleRules = computed(() => rules.value.filter(rule => coreType(rule.type) === (section.value === 'core') &&
      (section.value === 'core' || [rule.name, rule.type, rule.ruleId].some(value => value.toLowerCase().includes(ruleSearch.value.trim().toLowerCase())))))
    const sharedTypes = computed(() => {
      const ids = new Set([...thresholds.value.filter(t => t.scope === 'detector'), ...configItems.value].map(it => it.ruleId).filter(Boolean))
      return [...ids].map(value => ({ value, label: ruleTypeName(value) }))
    })
    const effectiveSharedType = computed(() => sharedTypes.value.some(it => it.value === sharedType.value) ? sharedType.value : sharedTypes.value[0]?.value)
    const visibleThresholds = computed(() => thresholds.value.filter(it => it.scope === 'detector' && it.ruleId === effectiveSharedType.value))
    const visibleConfigs = computed(() => configItems.value.filter(it => section.value === 'global' ? !it.ruleId :
      Boolean(it.ruleId && it.ruleId === effectiveSharedType.value && it.kind !== 'hour_of_day')))
    const primaryOverride = rule => thresholds.value.find(it => it.scope === 'instance' && it.ruleId === rule.ruleId && it.configured)
    const openShared = type => { sharedType.value = type; section.value = 'shared' }

    const alertPagination = reactive({
      current: 1, pageSize: 20, total: 0,
      showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'],
      showTotal: value => `共 ${value} 条`,
    })

    let alive = true
    let mounted = false
    let readVersion = 0
    let writeVersion = 0
    let ruleScopeVersion = 0
    let rulesReadVersion = 0
    let ruleTypesReadVersion = 0
    let thresholdReadVersion = 0
    let configItemsReadVersion = 0
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()
    let observedLogout = mountedLogoutEpoch

    const validAdmin = () => {
      const user = currentUser.value
      const id = Number(user?.id)
      return Number.isSafeInteger(id) && id > 0 && id <= 2147483647 && isAdminUser(user)
        && !['disabled', '0'].includes(String(user?.status ?? 'enabled'))
    }
    const canManage = () => alive && route.path === mountedPath
      && getCurrentUserLogoutEpoch() === mountedLogoutEpoch && validAdmin()
    const captureRuleScope = () => {
      const scope = ruleScopeVersion
      return () => canManage() && scope === ruleScopeVersion
    }

    const ruleOptions = computed(() => {
      const options = new Map(RISK_RULE_OPTIONS.map(item => [item.value, { label: item.label, value: item.value }]))
      for (const rule of rules.value) options.set(rule.ruleId, { label: rule.name, value: rule.ruleId })
      return [...options.values()]
    })
    const notifiedOptions = [
      { label: '已推送', value: 'true' },
      { label: '未推送', value: 'false' },
    ]

    const alertColumns = [
      { title: 'ID', dataIndex: 'id', key: 'id', width: 80 },
      { title: '规则', dataIndex: 'ruleId', key: 'ruleId', width: 180 },
      { title: '级别', dataIndex: 'severity', key: 'severity', width: 90 },
      { title: '主体', dataIndex: 'subjectKey', key: 'subjectKey', width: 220 },
      { title: '用户 ID', dataIndex: 'userId', key: 'userId', width: 100 },
      { title: '推送', dataIndex: 'notified', key: 'notified', width: 110 },
      { title: '触发时间', dataIndex: 'createdAt', key: 'createdAt', width: 180 },
      { title: '操作', key: 'action', width: 110 },
    ]
    const evidenceColumns = [
      { title: '字段', dataIndex: 'field', key: 'field', width: 180 },
      { title: '值', dataIndex: 'value', key: 'value' },
    ]

    const pushedOnPage = computed(() => alerts.value.filter(row => row?.notified === true).length)
    const evidenceRows = computed(() => (detail.value ? buildRiskEvidenceRows(detail.value.detail) : []))

    const alertScopeDescription = computed(() => {
      const scope = appliedFilters.value
      const parts = [
        scope.ruleId && `规则：${riskRuleLabel(scope.ruleId, '', rules.value)}`,
        scope.userId && `用户 ID：${scope.userId}`,
        scope.notified && `推送：${scope.notified === 'true' ? '已推送' : '未推送'}`,
        scope.createdFrom && `起：${scope.createdFrom}`,
        scope.createdTo && `止：${scope.createdTo}`,
      ].filter(Boolean)
      return parts.length ? `当前筛选：${parts.join('；')}。` : '当前范围：全部风控告警（按 ID 倒序）。'
    })
    const alertEmptyText = computed(() => (alertError.value
      ? '列表加载失败，请重试'
      : hasLoadedAlerts.value ? '暂无符合条件的风控告警' : '风控告警尚未读取成功'))

    // ---- 列表 ----
    const loadAlerts = async () => {
      if (!canManage()) return false
      const ticket = ++readVersion
      const owns = () => canManage() && ticket === readVersion
      loadingAlerts.value = true
      try {
        const res = await getRiskAlerts(buildRiskAlertListParams(appliedFilters.value, alertPagination.current, alertPagination.pageSize))
        if (!owns()) return false
        const parsed = readRiskListBody(res)
        // 失败必须保留上一批行：渲染成空列表会让运营以为告警被清空了。
        if (!parsed.ok) {
          alertError.value = '风控告警刷新失败，页面显示的仍是上次读取的结果。'
          return false
        }
        alerts.value = parsed.items
        alertsTotal.value = parsed.total
        alertPagination.total = parsed.total
        hasLoadedAlerts.value = true
        alertError.value = ''
        return true
      } catch (error) {
        if (owns()) alertError.value = '风控告警刷新失败，页面显示的仍是上次读取的结果。'
        void error
        return false
      } finally {
        if (owns()) loadingAlerts.value = false
      }
    }

    const setFilter = (field, value) => {
      if (!canManage() || loadingAlerts.value || !Object.hasOwn(alertFilters, field)) return
      alertFilters[field] = value
    }
    const reloadAlerts = () => {
      if (!canManage() || loadingAlerts.value || writing.value) return
      const problem = validateRiskAlertFilters(alertFilters)
      if (problem) { filterError.value = problem; return }
      filterError.value = ''
      appliedFilters.value = {
        ruleId: alertFilters.ruleId || '',
        userId: String(alertFilters.userId || '').trim(),
        notified: alertFilters.notified || '',
        createdFrom: String(alertFilters.createdFrom || '').trim(),
        createdTo: String(alertFilters.createdTo || '').trim(),
      }
      alertPagination.current = 1
      return loadAlerts()
    }
    const resetFilters = () => {
      if (!canManage() || loadingAlerts.value || writing.value) return
      Object.assign(alertFilters, emptyFilters())
      filterError.value = ''
      appliedFilters.value = {}
      alertPagination.current = 1
      return loadAlerts()
    }
    const onTableChange = page => {
      if (!canManage() || loadingAlerts.value) return
      alertPagination.current = page?.pageSize === alertPagination.pageSize ? Number(page.current) || 1 : 1
      alertPagination.pageSize = Number(page?.pageSize) || alertPagination.pageSize
      return loadAlerts()
    }

    // ---- 详情（只读） ----
    const openDetail = async record => {
      if (!canManage()) return
      const id = Number(record?.id)
      if (!Number.isInteger(id) || id <= 0) return
      detailVisible.value = true
      detail.value = null
      detailError.value = ''
      loadingDetail.value = true
      try {
        const res = await getRiskAlertDetail(id)
        if (!canManage()) return
        if (Number(res?.status) !== 200) {
          detailError.value = readRiskMessage(res, '读取风控告警详情失败')
          return
        }
        const row = res?.data?.data
        detail.value = row && typeof row === 'object' ? { ...row } : null
        if (!detail.value) detailError.value = '读取风控告警详情失败'
      } catch (error) {
        if (canManage()) detailError.value = readRiskThrownMessage(error, '读取风控告警详情失败')
      } finally {
        loadingDetail.value = false
      }
    }
    const closeDetail = () => {
      detailVisible.value = false
      detail.value = null
      detailError.value = ''
    }

    // ---- 配置 ----
    const applySettings = data => {
      const normalized = normalizeRiskWebhookSettings(data)
      settings.value = normalized
      config.enabled = normalized.enabled
      // 凭据不回显：输入框恒为空，只有 preview 可展示。
      config.webhookUrl = ''
      config.mentionedList = formatMentionedInput(normalized.mentionedList)
      config.clearWebhook = false
      configLoaded.value = true
    }

    const loadConfig = async () => {
      if (!canManage()) return false
      loadingConfig.value = true
      configError.value = ''
      try {
        const res = await getRiskWebhookSettings()
        if (!canManage()) return false
        if (Number(res?.status) !== 200) {
          configError.value = readRiskMessage(res, '读取风控通知配置失败')
          return false
        }
        applySettings(res?.data?.data)
        return true
      } catch (error) {
        if (canManage()) configError.value = readRiskThrownMessage(error, '读取风控通知配置失败')
        return false
      } finally {
        if (canManage()) loadingConfig.value = false
      }
    }

    const setConfig = (field, value) => {
      if (!canManage() || writing.value || !Object.hasOwn(config, field)) return
      config[field] = value
      // 输入新地址即取消「清空」意图（两者互斥，同 ticket 页语义）。
      if (field === 'webhookUrl' && String(value || '').trim()) config.clearWebhook = false
      configError.value = ''
      configSuccess.value = ''
    }

    // ---- 规则阈值 ----
    //
    // 语义要点：
    //  · 保存成功后**用服务端返回值回填** thresholds，而不是把输入值写进去 ——
    //    否则「后端夹紧/拒绝后的真实值」与界面显示会不一致。
    //  · thresholdDraft 只承载编辑中的值；保存失败时它保持用户输入，
    //    但 thresholds 仍是权威值，界面能看出「没生效」。
    const applyThresholdItems = items => {
      const list = Array.isArray(items) ? items : []
      thresholds.value = list
      for (const key of Object.keys(thresholdDraft)) delete thresholdDraft[key]
      for (const item of list) {
        if (item && item.key) thresholdDraft[item.key] = Number(item.value)
      }
    }
    // ruleUnit 给规则行里的「生效阈值」补单位。
    //
    // ⚠️ 单位来自**后端** `RiskRuleState.Unit`（2026-11 补齐），不在前端映射。
    // 原因：前端自己写一份 ruleId → 单位的表就是「第二份真相」，与
    // riskThresholdSpecs 的 Unit 必然漂移（新增规则忘改前端就显示错单位）。
    // 那份内联映射本来写过，被 riskAdminSource.test.mjs 的
    // 「页面不得内联规则名映射」断言拦下了 —— 拦得对，改由后端给更彻底。
    const ruleUnit = rule => (rule && rule.unit ? ' ' + rule.unit : '')
    const ruleTypeName = type => ruleTypes.value.find(item => item.type === type)?.name || type

    const applyRuleItems = items => {
      const list = Array.isArray(items) ? items : []
      rules.value = list
        .filter(item => item && typeof item.ruleId === 'string' && item.ruleId)
        .map(item => ({
          ruleId: item.ruleId,
          name: String(item.name ?? item.ruleId),
          enabled: item.enabled === true,
          severity: String(item.severity ?? ''),
          threshold: Number(item.threshold ?? 0),
          hasThreshold: item.hasThreshold !== false,
          exists: item.exists === true,
          description: String(item.description ?? ''),
          type: String(item.type ?? item.ruleId),
          source: item.source === 'custom' ? 'custom' : 'builtin',
          throttleSeconds: Number(item.throttleSeconds ?? 0),
          responsePolicy: item.responsePolicy || { mode: 'notify' },
          effectiveResponsePolicy: item.effectiveResponsePolicy || { mode: item.enabled === true ? 'notify' : 'disabled' },
          unit: String(item.unit ?? ''),
          updatedAt: item.updatedAt || '',
          updatedBy: String(item.updatedBy ?? ''),
        }))
    }

    const loadRuleTypes = async () => {
      if (!canManage()) return false
      const scope = ruleScopeVersion
      const ticket = ++ruleTypesReadVersion
      const owns = () => canManage() && scope === ruleScopeVersion && ticket === ruleTypesReadVersion
      try {
        const res = await getRiskRuleTypes()
        if (!owns()) return false
        if (Number(res?.status) !== 200) {
          rulesError.value = readRiskMessage(res, '读取可用规则类型失败')
          return false
        }
        const body = res?.data?.data ?? res?.data
        const items = Array.isArray(body) ? body : Array.isArray(body?.items) ? body.items : Array.isArray(body?.data) ? body.data : []
        ruleTypes.value = items.filter(item => item && typeof item.type === 'string' && item.type)
        return true
      } catch (error) {
        if (owns()) rulesError.value = readRiskThrownMessage(error, '读取可用规则类型失败')
        return false
      }
    }

    const loadRules = async () => {
      if (!canManage()) return false
      const scope = ruleScopeVersion
      const ticket = ++rulesReadVersion
      const owns = () => canManage() && scope === ruleScopeVersion && ticket === rulesReadVersion
      rulesLoading.value = true
      rulesError.value = ''
      try {
        const res = await getRiskRules()
        if (!owns()) return false
        if (Number(res?.status) !== 200) {
          rulesError.value = readRiskMessage(res, '读取规则清单失败')
          return false
        }
        const body = res?.data
        const fullRules = body?.data
        applyRuleItems(Array.isArray(fullRules) ? fullRules : body?.items || fullRules?.items)
        return true
      } catch (error) {
        if (owns()) rulesError.value = readRiskThrownMessage(error, '读取规则清单失败')
        return false
      } finally {
        if (owns()) rulesLoading.value = false
      }
    }

    const resetRuleDraft = () => {
      const first = availableTypes.value[0]
      ruleDraft.type = first?.type || ''
      ruleDraft.name = ''
      ruleDraft.description = ''
      ruleDraft.severity = first?.defaultSeverity || 'medium'
      ruleDraft.threshold = Number(first?.defaultThreshold ?? 1)
      ruleDraft.throttleSeconds = Number(first?.defaultThrottleSeconds ?? 0)
      fillPolicyDraft()
    }
    const openCreateRule = async () => {
      if (!canManage() || rulesLoading.value || ruleWriting.value) return
      const owns = captureRuleScope()
      rulesError.value = ''
      rulesSuccess.value = ''
      if (!ruleTypes.value.length && !await loadRuleTypes()) return
      if (!owns()) return
      ruleEditingId.value = ''
      resetRuleDraft()
      ruleEditorOpen.value = true
    }
    const openEditRule = async rule => {
      if (!canManage() || !rule || rulesLoading.value || ruleWriting.value || ruleDeleting[rule.ruleId] || ruleToggling[rule.ruleId]) return
      rulesError.value = ''
      rulesSuccess.value = ''
      const owns = captureRuleScope()
      if (!ruleTypes.value.length && !await loadRuleTypes()) return
      if (!owns()) return
      ruleEditingId.value = rule.ruleId
      Object.assign(ruleDraft, {
        type: rule.type,
        name: rule.name,
        description: rule.description,
        severity: rule.severity || 'medium',
        threshold: Number(rule.threshold ?? 0),
        throttleSeconds: Number(rule.throttleSeconds ?? 0),
      })
      fillPolicyDraft(rule.responsePolicy, rule.effectiveResponsePolicy)
      ruleEditorOpen.value = true
    }
    const setRuleType = type => {
      if (!canManage() || ruleEditingId.value) return
      const selected = ruleTypes.value.find(item => item.type === type)
      if (!selected) return
      ruleDraft.type = selected.type
      ruleDraft.threshold = Number(selected.defaultThreshold ?? 1)
      ruleDraft.severity = selected.defaultSeverity || 'medium'
      ruleDraft.throttleSeconds = Number(selected.defaultThrottleSeconds ?? 0)
      fillPolicyDraft()
    }
    const closeRuleEditor = () => {
      if (ruleWriting.value) return
      ruleEditorOpen.value = false
      ruleEditingId.value = ''
      resetRuleDraft()
    }
    const saveRule = async () => {
      if (!canManage() || ruleWriting.value) return
      const scope = ruleScopeVersion
      const owns = () => canManage() && scope === ruleScopeVersion
      const name = String(ruleDraft.name || '').trim()
      const threshold = Number(ruleDraft.threshold)
      const throttleSeconds = Number(ruleDraft.throttleSeconds)
      const type = selectedRuleType.value
      const badThreshold = type?.hasThreshold !== false && (!Number.isFinite(threshold) || threshold <= 0 ||
        (type?.thresholdMax > 0 && threshold > type.thresholdMax) || (type?.thresholdKind === 'integer' && !Number.isInteger(threshold)))
      if (!name || !type || badThreshold || !Number.isInteger(throttleSeconds) || throttleSeconds < 0 || throttleSeconds > 2678400) {
        rulesError.value = '请填写名称、受支持的规则类型、有效阈值和非负整数节流秒数。'
        return
      }
      const mode = ruleDraft.responseMode
      const responseMinutes = Number(ruleDraft.responseMinutes)
      if (mode === 'temporary' && (!Number.isInteger(responseMinutes) || responseMinutes < 1 || responseMinutes > 1440)) {
        rulesError.value = '临时封禁时长必须为 1 至 1440 分钟。'
        return
      }
      if (rulePolicyRequiresConfirmation.value && !ruleDraft.responseConfirmed) {
        rulesError.value = '永久封禁或 IP 段封禁须明确确认后才能保存。'
        return
      }
      const responsePolicy = {
        mode, ...(mode !== 'notify' ? { scope: ruleDraft.responseScope } : {}),
        ...(mode === 'temporary' ? { durationMinutes: responseMinutes } : {}),
        ...(rulePolicyRequiresConfirmation.value ? { confirmed: true } : {}),
      }
      ruleWriting.value = true
      rulesError.value = ''
      rulesSuccess.value = ''
      const editingId = ruleEditingId.value
      const payload = {
        name,
        description: String(ruleDraft.description || '').trim(),
        severity: ruleDraft.severity,
        ...(type.hasThreshold === false ? {} : { threshold }),
        throttleSeconds,
        config: {},
        responsePolicy,
      }
      try {
        const res = editingId
          ? await updateRiskRule(editingId, payload)
          : await createRiskRule({
            ...payload,
            type: ruleDraft.type,
            enabled: true,
          })
        if (!owns()) return
        if (Number(res?.status) !== 200 && Number(res?.status) !== 201) {
          rulesError.value = readRiskMessage(res, '保存规则失败')
          return
        }
        await Promise.all([loadRules(), loadThresholds(), loadConfigItems()])
        if (!owns()) return
        rulesSuccess.value = editingId ? `已更新规则「${name}」` : `已新增规则「${name}」`
        ruleEditorOpen.value = false
        ruleEditingId.value = ''
      } catch (error) {
        if (owns()) rulesError.value = readRiskThrownMessage(error, '保存规则失败')
      } finally {
        if (owns()) ruleWriting.value = false
      }
    }
    const deleteRule = async rule => {
      if (!canManage() || !rule?.ruleId || ruleWriting.value || ruleDeleting[rule.ruleId] || ruleToggling[rule.ruleId]) return
      const scope = ruleScopeVersion
      const owns = () => canManage() && scope === ruleScopeVersion
      ruleDeleting[rule.ruleId] = true
      rulesError.value = ''
      rulesSuccess.value = ''
      try {
        const res = await deleteRiskRuleRequest(rule.ruleId)
        if (!owns()) return
        if (Number(res?.status) !== 200) {
          rulesError.value = readRiskMessage(res, '删除规则失败')
          return
        }
        rules.value = rules.value.filter(item => item.ruleId !== rule.ruleId)
        rulesSuccess.value = `已删除规则「${rule.name}」；已有告警记录会保留`
        await Promise.all([loadThresholds(), loadConfigItems()])
      } catch (error) {
        if (owns()) rulesError.value = readRiskThrownMessage(error, '删除规则失败')
      } finally {
        if (owns()) ruleDeleting[rule.ruleId] = false
      }
    }

    // toggleRule 切换一条规则的启用状态。
    //
    // 成功后**就地更新**该行（不整表重载）：整表重载会让所有行闪一下，
    // 管理员刚点的那一行反而看不出来变了没有。
    // 失败时保持原状态并报错 —— 不能乐观更新，否则「以为停用了其实没停」。
    const toggleRule = async (rule, nextEnabled) => {
      if (!canManage() || !rule || ruleWriting.value || ruleDeleting[rule.ruleId]) return
      const scope = ruleScopeVersion
      const owns = () => canManage() && scope === ruleScopeVersion
      const ruleId = rule.ruleId
      if (ruleToggling[ruleId]) return
      ruleToggling[ruleId] = true
      rulesError.value = ''
      rulesSuccess.value = ''
      try {
        const res = await setRiskRuleEnabled(ruleId, nextEnabled === true)
        if (!owns()) return
        if (Number(res?.status) !== 200) {
          rulesError.value = readRiskMessage(res, '保存规则状态失败')
          return
        }
        // 以服务端返回的 item 为准（后端可能规范化过），拿不到才用本地值。
        const saved = res?.data?.item || res?.data?.data?.item
        if (saved && typeof saved.ruleId === 'string') {
          const index = rules.value.findIndex(item => item.ruleId === saved.ruleId)
          if (index >= 0) {
            rules.value.splice(index, 1, {
              ...rules.value[index],
              enabled: saved.enabled === true,
              description: String(saved.description ?? rules.value[index].description),
              threshold: Number(saved.threshold ?? rules.value[index].threshold),
              unit: String(saved.unit ?? rules.value[index].unit),
              source: String(saved.source ?? rules.value[index].source),
              type: String(saved.type ?? rules.value[index].type),
              throttleSeconds: Number(saved.throttleSeconds ?? rules.value[index].throttleSeconds),
              responsePolicy: saved.responsePolicy || rules.value[index].responsePolicy,
              effectiveResponsePolicy: saved.effectiveResponsePolicy || { mode: saved.enabled === true ? 'notify' : 'disabled' },
            })
          }
        } else {
          rule.enabled = nextEnabled === true
          rule.effectiveResponsePolicy = { mode: nextEnabled === true ? 'notify' : 'disabled' }
        }
        rulesSuccess.value = nextEnabled === true
          ? `已启用「${rule.name}」`
          : `已停用「${rule.name}」——历史告警保留，只是不再产生新告警`
      } catch (error) {
        if (owns()) rulesError.value = readRiskThrownMessage(error, '保存规则状态失败')
      } finally {
        if (owns()) ruleToggling[ruleId] = false
      }
    }

    // ---- 非数值配置（ASN 名单 / 评估间隔）----
    //
    // 与阈值同一条纪律：保存成功后**用服务端返回值回填**，而不是把输入值写进去。
    // 文本类的「归一化」在后端（as15169 → AS15169、去重、统一分隔符），
    // 前端若自己回填原始输入，界面会显示一个与落库形态不同的值。
    const applyConfigItems = items => {
      const list = normalizeRiskConfigItems(items)
      configItems.value = list
      for (const key of Object.keys(configItemDraft)) delete configItemDraft[key]
      for (const item of list) configItemDraft[item.key] = item.value
    }

    const loadConfigItems = async () => {
      if (!canManage()) return false
      const currentScope = captureRuleScope(), ticket = ++configItemsReadVersion
      const owns = () => currentScope() && ticket === configItemsReadVersion
      configItemsLoading.value = true
      configItemsError.value = ''
      try {
        const res = await getRiskConfigs()
        if (!owns()) return false
        if (Number(res?.status) !== 200) {
          configItemsError.value = readRiskMessage(res, '读取风控配置失败')
          return false
        }
        applyConfigItems(res?.data?.items || res?.data?.data?.items)
        return true
      } catch (error) {
        if (owns()) {
          // normalizeRiskConfigItems 对未知 kind 会抛错（明确报出是哪个键），
          // 这条路径必须把原文展示出来，而不是笼统的「读取失败」。
          configItemsError.value = readRiskThrownMessage(error, '读取风控配置失败')
        }
        return false
      } finally {
        if (owns()) configItemsLoading.value = false
      }
    }

    const setConfigItemDraft = (key, value) => {
      if (!canManage() || configItemsWriting.value) return
      if (!Object.hasOwn(configItemDraft, key)) return
      configItemDraft[key] = value
      configItemsError.value = ''
      configItemsSuccess.value = ''
    }

    // replaceConfigItem 把服务端返回的单项写回权威列表（就地更新，不整表重载）。
    const replaceConfigItem = item => {
      const index = configItems.value.findIndex(row => row.key === item.key)
      if (index < 0) return
      // 用同一个归一化函数处理单项：保持 control/min/max 等派生字段一致。
      const [next] = normalizeRiskConfigItems([item])
      if (!next) return
      configItems.value.splice(index, 1, next)
      configItemDraft[next.key] = next.value
    }

    const saveConfigItem = async item => {
      if (!canManage() || configItemsWriting.value || !item?.key) return
      const owns = captureRuleScope()
      const raw = String(configItemDraft[item.key] ?? '')
      // 前端只做类型守卫（整数/区间/长度），形态校验（ASN 归一化）留在后端。
      const problem = validateRiskConfigDraft(item, raw)
      if (problem) {
        configItemsError.value = problem
        configItemsSuccess.value = ''
        return
      }
      configItemsWriting.value = true
      configItemsError.value = ''
      configItemsSuccess.value = ''
      try {
        const res = await updateRiskConfig(item.key, raw.trim())
        if (!owns()) return
        if (Number(res?.status) !== 200) {
          configItemsError.value = readRiskMessage(res, '保存风控配置失败')
          return
        }
        const saved = res?.data?.item || res?.data?.data?.item
        if (saved && saved.key) replaceConfigItem(saved)
        // 文案必须说明「变成了什么」：清空与设值是两种效果，不能都只说「已保存」。
        const message = String(res?.data?.message ?? res?.data?.data?.message ?? '')
        configItemsSuccess.value = message.includes('清空')
          ? `${item.label} 已清空，回到默认值`
          : `${item.label} 已保存，判定立即生效`
        await Promise.all([loadConfigItems(), loadRules()])
      } catch (error) {
        if (owns()) configItemsError.value = readRiskThrownMessage(error, '保存风控配置失败')
      } finally {
        if (owns()) configItemsWriting.value = false
      }
    }

    const resetConfigItem = async item => {
      if (!canManage() || configItemsWriting.value || !item?.key) return
      const owns = captureRuleScope()
      configItemsWriting.value = true
      configItemsError.value = ''
      configItemsSuccess.value = ''
      try {
        const res = await resetRiskConfig(item.key)
        if (!owns()) return
        if (Number(res?.status) !== 200) {
          configItemsError.value = readRiskMessage(res, '重置风控配置失败')
          return
        }
        configItemsSuccess.value = `${item.label} 已重置为默认值`
        await Promise.all([loadConfigItems(), loadRules()])
      } catch (error) {
        if (owns()) configItemsError.value = readRiskThrownMessage(error, '重置风控配置失败')
      } finally {
        if (owns()) configItemsWriting.value = false
      }
    }

    const loadThresholds = async () => {
      if (!canManage()) return false
      const currentScope = captureRuleScope(), ticket = ++thresholdReadVersion
      const owns = () => currentScope() && ticket === thresholdReadVersion
      thresholdsLoading.value = true
      thresholdError.value = ''
      try {
        const res = await getRiskThresholds()
        if (!owns()) return false
        if (Number(res?.status) !== 200) {
          thresholdError.value = readRiskMessage(res, '读取规则阈值失败')
          return false
        }
        applyThresholdItems(res?.data?.items || res?.data?.data?.items)
        return true
      } catch (error) {
        if (owns()) thresholdError.value = readRiskThrownMessage(error, '读取规则阈值失败')
        return false
      } finally {
        if (owns()) thresholdsLoading.value = false
      }
    }
    const setThresholdDraft = (key, value) => {
      if (!canManage() || thresholdWriting.value) return
      if (!Object.hasOwn(thresholdDraft, key)) return
      thresholdDraft[key] = value
      thresholdError.value = ''
      thresholdSuccess.value = ''
    }
    // describeThresholdSource 说明当前值的来源，避免「以为在调、其实读的是代码默认值」。
    const describeThresholdSource = item => {
      if (!item) return ''
      const unit = item.unit ? `（单位：${item.unit}）` : ''
      const source = item.source === 'system_settings' ? '管理页配置' : (item.source === 'risk_rules' ? '规则表列值' : '代码默认值')
      return `当前生效：${item.value}${unit} 来源：${source} 默认值：${item.defaultValue}`
    }
    const saveThreshold = async item => {
      if (!canManage() || thresholdWriting.value || !item?.key) return
      const owns = captureRuleScope()
      const value = Number(thresholdDraft[item.key])
      thresholdWriting.value = true
      thresholdError.value = ''
      thresholdSuccess.value = ''
      try {
        const res = await updateRiskThreshold(item.key, value)
        if (!owns()) return
        if (Number(res?.status) !== 200) {
          // 400 的文案由后端给出（占比越界/必须大于 0 等），直接展示。
          thresholdError.value = readRiskMessage(res, '保存阈值失败')
          return
        }
        const saved = res?.data?.item || res?.data?.data?.item
        if (saved && saved.key) thresholdDraft[saved.key] = Number(saved.value)
        thresholdSuccess.value = `${item.label} 已保存为 ${saved?.value ?? value}，判定立即生效`
        // 重新拉一次，保证整张表与服务端一致（含 source/configured）。
        await Promise.all([loadThresholds(), loadRules()])
      } catch (error) {
        if (owns()) thresholdError.value = readRiskThrownMessage(error, '保存阈值失败')
      } finally {
        if (owns()) thresholdWriting.value = false
      }
    }
    const resetThreshold = async item => {
      if (!canManage() || thresholdWriting.value || !item?.key) return
      const owns = captureRuleScope()
      thresholdWriting.value = true
      thresholdError.value = ''
      thresholdSuccess.value = ''
      try {
        const res = await resetRiskThreshold(item.key)
        if (!owns()) return
        if (Number(res?.status) !== 200) {
          thresholdError.value = readRiskMessage(res, '重置阈值失败')
          return
        }
        thresholdSuccess.value = `${item.label} 已重置为默认值 ${item.defaultValue}`
        await Promise.all([loadThresholds(), loadRules()])
      } catch (error) {
        if (owns()) thresholdError.value = readRiskThrownMessage(error, '重置阈值失败')
      } finally {
        if (owns()) thresholdWriting.value = false
      }
    }

    const toggleConfigEnabled = next => {
      // 开关本身只改本地状态，必须点「保存」才落库 —— 避免误触把风控通道关掉。
      if (!canManage() || writing.value) return
      config.enabled = next === true
      configError.value = ''
      configSuccess.value = ''
    }
    const clearSavedWebhook = () => {
      if (!canManage() || !configLoaded.value || writing.value) return
      config.clearWebhook = true
      config.webhookUrl = ''
      configError.value = ''
      configSuccess.value = ''
    }
    const keepSavedWebhook = () => {
      if (!canManage() || !configLoaded.value || writing.value) return
      config.clearWebhook = false
      configError.value = ''
      configSuccess.value = ''
    }

    const saveSettings = async () => {
      if (!canManage() || writing.value || !configLoaded.value) return
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      writing.value = true
      configError.value = ''
      configSuccess.value = ''
      try {
        const payload = buildRiskWebhookUpdatePayload(config)
        const res = await updateRiskWebhookSettings(payload)
        if (!owns()) return
        if (Number(res?.status) !== 200) {
          configError.value = readRiskMessage(res, '保存风控通知配置失败')
          return
        }
        applySettings(res?.data?.data)
        configSuccess.value = payload.clearWebhook ? '风控通知配置已保存（风控地址已清除，工单地址不受影响）' : '风控通知配置已保存'
        message.success('风控通知配置已保存')
      } catch (error) {
        if (owns()) configError.value = readRiskThrownMessage(error, '保存风控通知配置失败')
      } finally {
        if (owns()) writing.value = false
      }
    }

    const sendTest = async () => {
      if (!canManage() || writing.value) return
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      writing.value = true
      configError.value = ''
      configSuccess.value = ''
      try {
        const res = await testRiskWebhook()
        if (!owns()) return
        if (Number(res?.status) !== 200) {
          // 后端会原文回「请先配置企业微信**风控**机器人 Webhook（与工单 Webhook 是两套独立配置）」
          configError.value = readRiskMessage(res, '企业微信风控测试发送失败')
          return
        }
        configSuccess.value = readRiskMessage(res, '测试通知已发送')
        message.success('测试通知已发送（走的是风控通道）')
      } catch (error) {
        if (owns()) configError.value = readRiskThrownMessage(error, '企业微信风控测试发送失败')
      } finally {
        if (owns()) writing.value = false
      }
    }

    const retire = () => {
      section.value = 'core'; ruleSearch.value = ''; sharedType.value = 'offhours_activity'
      readVersion += 1
      writeVersion += 1
      ruleScopeVersion += 1
      rules.value = []
      ruleTypes.value = []
      rulesLoading.value = false
      ruleEditorOpen.value = false
      ruleWriting.value = false
      rulesError.value = ''
      rulesSuccess.value = ''
      ruleEditingId.value = ''
      thresholds.value = []
      configItems.value = []
      thresholdWriting.value = false
      thresholdsLoading.value = false
      thresholdError.value = ''
      thresholdSuccess.value = ''
      configItemsWriting.value = false
      configItemsLoading.value = false
      configItemsError.value = ''
      configItemsSuccess.value = ''
      for (const key of Object.keys(thresholdDraft)) delete thresholdDraft[key]
      for (const key of Object.keys(configItemDraft)) delete configItemDraft[key]
      for (const key of Object.keys(ruleToggling)) delete ruleToggling[key]
      for (const key of Object.keys(ruleDeleting)) delete ruleDeleting[key]
      resetRuleDraft()
    }

    watch(() => [currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status], () => {
      retire()
      alerts.value = []; alertsTotal.value = 0; alertPagination.total = 0; hasLoadedAlerts.value = false
      detailVisible.value = false; detail.value = null
      configLoaded.value = false
      settings.value = emptySettings()
      Object.assign(config, emptyConfig())
      if (mounted && canManage()) { void loadAlerts(); void loadConfig(); void loadThresholds(); void loadRules(); void loadRuleTypes(); void loadConfigItems() }
    }, { flush: 'sync' })
    watch(() => getCurrentUserLogoutEpoch(), value => {
      if (value === observedLogout) return
      observedLogout = value
      retire()
      alerts.value = []; alertsTotal.value = 0; alertPagination.total = 0; hasLoadedAlerts.value = false
      detailVisible.value = false; detail.value = null
      configLoaded.value = false
      alertError.value = ''; configError.value = ''; detailError.value = ''; filterError.value = ''
    }, { flush: 'sync' })
    watch(() => route.path, () => {
      retire()
      detailVisible.value = false; detail.value = null
    }, { flush: 'sync' })

    onMounted(() => {
      mounted = true
      if (canManage()) {
        void loadAlerts()
        void loadConfig()
        // ⚠️ loadThresholds 原本只挂在 watch（用户身份变化）上，首次进入页面
        // **不会加载**阈值 —— 表现是「规则阈值面板一直显示『暂无可调阈值』，
        // 刷新页面才出来」。本次一并补上，否则新加的规则清单会有同样问题。
        void loadThresholds()
        void loadRules()
        void loadRuleTypes()
        // 非数值配置（ASN 名单 / 评估间隔）同样必须首屏加载：
        // 这两项此前完全没有 UI，现在有了却只在 watch 里加载，就会重演
        // 「刷新页面才出来」那个缺陷。
        void loadConfigItems()
      }
    })
    onBeforeUnmount(() => {
      alive = false
      mounted = false
      retire()
    })

    return {
      fieldId, canManage, captureRuleScope, ownerKey, section, sections, coreDetectors, ruleSearch, visibleRules,
      sharedType, effectiveSharedType, sharedTypes, visibleThresholds, visibleConfigs, primaryOverride, openShared,
      RISK_ADVISORY_NOTE, RISK_INDEPENDENCE_NOTICE,
      alerts, alertsTotal, hasLoadedAlerts, loadingAlerts, alertError, filterError,
      alertFilters, appliedFilters, alertPagination, alertColumns, evidenceColumns,
      ruleOptions, notifiedOptions,
      pushedOnPage, evidenceRows, alertScopeDescription, alertEmptyText,
      detail, detailVisible, loadingDetail, detailError,
      settings, config, configLoaded, loadingConfig, writing, configError, configSuccess,
      setFilter, reloadAlerts, resetFilters, onTableChange, loadAlerts,
      openDetail, closeDetail,
      setConfig, toggleConfigEnabled, clearSavedWebhook, keepSavedWebhook, saveSettings, sendTest, loadConfig,
      thresholds, thresholdDraft, thresholdsLoading, thresholdWriting, thresholdError, thresholdSuccess,
      loadThresholds, setThresholdDraft, saveThreshold, resetThreshold, describeThresholdSource,
      rules, rulesLoading, rulesError, rulesSuccess, ruleToggling,
      ruleTypes, ruleTypeOptions, selectedRuleType, severityOptions, ruleEditorOpen, ruleEditingId, ruleWriting, ruleDeleting, ruleDraft,
      rulePolicyLabel, rulePolicyRequiresConfirmation, responseModeOptions, responseScopeOptions,
      loadRules, toggleRule, ruleUnit, ruleTypeName, openCreateRule, openEditRule, closeRuleEditor, setRuleType, saveRule, deleteRule,
      // 非数值配置（ASN 名单 / 评估间隔）
      configItems, configItemDraft, configItemsLoading, configItemsWriting,
      configItemsError, configItemsSuccess,
      loadConfigItems, setConfigItemDraft, saveConfigItem, resetConfigItem,
      describeRiskConfigEmptyMeaning,
      riskRuleLabel, riskSeverityMeta, formatRiskDateTime, describeRiskWebhookState,
      sumTableColumnWidths,
    }
  },
}
</script>

<style scoped>
.risk-nav { display: flex; gap: 8px; flex-wrap: wrap; }
.policy-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(250px, 100%), 1fr)); gap: 14px; }
.risk-search { max-width: 360px; margin: 0 0 14px; }
.window-settings { margin-top: 18px; }
.risk-rule-desc summary { cursor: pointer; }

.page-alert, .filter-form, .status-alert { margin-bottom: 16px; }
.risk-independence-notice { margin-bottom: 16px; }
.risk-config-panel, .risk-summary { margin-bottom: 16px; }
.config-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 320px), 1fr)); gap: 16px; }
.config-address-actions { display: flex; align-items: center; gap: 8px; margin-top: 6px; font-size: 12px; color: var(--console-muted); }
.config-hint { color: var(--console-muted); font-size: 13px; }
.config-success { margin: 12px 0 0; color: var(--console-success, #2f7d32); font-size: 13px; }
.summary-label { color: var(--console-muted); font-size: 14px; }
.summary-value { margin-top: 8px; color: var(--console-text); font-size: clamp(18px, 2.2vw, 24px); font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.summary-value-sm { font-size: clamp(15px, 1.7vw, 18px); }
.filter-rule, .filter-notified { width: 180px; }
.filter-user, .filter-date { width: 160px; }
.filter-error { margin: 0 0 16px; color: var(--console-danger, #c13e41); font-size: 13px; }
.field-hint { margin: 6px 0 0; color: var(--console-muted); font-size: 12px; line-height: 1.6; }
.subject-cell { overflow-wrap: anywhere; }
.time-cell { font-variant-numeric: tabular-nums; }
.detail-list { margin: 0 0 16px; border: 1px solid var(--console-border); border-radius: var(--console-radius); overflow: hidden; }
.detail-row { display: grid; grid-template-columns: 140px 1fr; gap: 12px; padding: 10px 16px; border-bottom: 1px solid var(--console-border); }
.detail-row:last-child { border-bottom: none; }
.detail-row dt { color: var(--console-muted); font-size: 13px; }
.detail-row dd { margin: 0; color: var(--console-text); font-size: 13px; overflow-wrap: anywhere; }
.detail-evidence-title { margin: 0 0 4px; font-size: 15px; color: var(--console-text); }
@media (max-width: 640px) {
  .filter-form :deep(.ant-form-item) { width: 100%; margin-inline-end: 0; }
  .filter-rule, .filter-notified, .filter-user, .filter-date { width: 100%; }
}
/* ============ 规则清单（启停 + 说明） ============ */
.risk-rule-list { display: flex; flex-direction: column; gap: 10px; }
.risk-rule-row {
  display: grid; grid-template-columns: minmax(0, 1fr) 190px; gap: 16px; align-items: start;
  padding: 12px 14px; border: 1px solid var(--console-border); border-radius: 8px;
  background: var(--console-surface);
}
/* 停用行的视觉状态：整行降低不透明度 + 左侧灰条。
   不能只靠开关的位置变化 —— 扫一眼列表时要能立刻看出哪条是关着的。 */
.risk-rule-row-disabled {
  opacity: 0.62;
  border-left: 3px solid var(--console-muted);
}
.risk-rule-main { min-width: 0; flex: 1; }
.risk-rule-title { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.risk-rule-name { font-size: 14px; font-weight: 600; color: var(--console-text); }
.risk-rule-meta { display: flex; gap: 12px; align-items: center; flex-wrap: wrap; margin-top: 4px; }
.risk-rule-id {
  font-family: var(--console-mono, monospace); font-size: 12px;
  color: var(--console-muted); overflow-wrap: anywhere;
}
.risk-rule-threshold { font-size: 12px; color: var(--console-muted); }
/* 说明文字是本次接线的核心目的：它是运营判断「这条规则什么时候不该信」的唯一来源，
   所以不能折成一行省略号 —— 允许换行、限制最大宽度保证可读。 */
.risk-rule-desc {
  margin: 6px 0 0; font-size: 12.5px; line-height: 1.6;
  color: var(--console-muted); max-width: 78ch; overflow-wrap: anywhere;
}
.risk-rule-actions { display: flex; align-items: center; justify-content: flex-end; flex-wrap: wrap; gap: 8px; flex-shrink: 0; }
.risk-rule-type, .risk-rule-updated { color: var(--console-muted); font-size: 12px; }
.risk-rule-updated { margin: 6px 0 0; }
.risk-rule-editor { display: grid; gap: 16px; }
.risk-rule-editor-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 220px), 1fr));
  gap: 16px;
}
.risk-editor-section {
  min-width: 0;
  padding: 16px;
  border: 1px solid var(--console-border);
  border-radius: 8px;
}
.risk-editor-section h3 {
  margin: 0 0 16px;
  color: var(--console-text);
  font-size: 14px;
  font-weight: 600;
}
.risk-editor-section > :deep(.ant-form-item),
.risk-rule-editor-grid > :deep(.ant-form-item) { margin-bottom: 0; }
.risk-editor-section > .risk-rule-editor-grid + :deep(.ant-form-item) { margin-top: 16px; }
.risk-editor-number { width: 100%; }
.risk-editor-type-hint { margin: -6px 0 16px; }
.risk-editor-identity { margin-top: 10px; overflow-wrap: anywhere; }
@media (max-width: 640px) {
  .risk-rule-row { grid-template-columns: minmax(0, 1fr); }
  .risk-rule-actions { justify-content: flex-start; }
  .risk-editor-section { padding: 12px; }
}
</style>
