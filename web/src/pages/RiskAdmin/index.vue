<template>
  <div class="console-page risk-admin-page">
    <ConsolePageHeading
      title="风控管理"
      description="风控告警只读查看 + 企业微信风控通知配置。命中只产生告警记录与推送，不做任何自动处置。"
    >
      <template #actions>
        <a-button :loading="loadingAlerts" :disabled="!canManage() || loadingAlerts || writing" @click="loadAlerts">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert
      v-if="!canManage()"
      type="warning"
      show-icon
      class="page-alert"
      message="当前身份无风控管理权限，请使用管理员账号登录。"
    />

    <!--
      C1 的用户可见形态：这条提示必须常驻，且页面**不存在**任何封禁/拒绝/改状态按钮。
      处置动作走黑名单与封禁各自的页面，风控页只读。
    -->
    <a-alert
      v-if="canManage()"
      type="info"
      show-icon
      class="page-alert"
      :message="RISK_ADVISORY_NOTE"
    />

    <template v-if="canManage()">
      <!-- ============ 风控通知配置（独立于工单，C2/C3） ============ -->
      <ConsolePanel
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
          v-if="loadError"
          type="error"
          show-icon
          class="status-alert"
          :message="loadError"
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
              :loading="saving"
              :disabled="!configLoaded || writing"
            >保存风控通知设置</a-button>
            <a-button
              :loading="testing"
              :disabled="!configLoaded || writing"
              @click="sendTest"
            >发送测试消息</a-button>
            <span v-if="config.configured" class="config-hint">当前已配置风控 Webhook</span>
          </a-space>
        </a-form>

        <p v-if="configError" class="filter-error" role="alert">{{ configError }}</p>
        <p v-if="configSuccess" class="config-success" role="status">{{ configSuccess }}</p>
      </ConsolePanel>

      <!-- ============ 规则阈值（可写，落库后立即生效） ============ -->
      <!--
        这些输入框在本次改动之前**故意不存在**：后端没有写入路径，
        放了输入框会「能填、能点保存、毫无反应」，比缺失更难排查。
        现在后端补了 GET/PUT/DELETE /risk/thresholds[/:key]，页面可以正式提供。
        范围校验以后端为准（service.ValidateRiskThresholdValue 一份），
        前端只做「是不是有限数」这一层类型守卫，不重复实现范围规则。
      -->
      <ConsolePanel class="risk-threshold-panel" title="规则阈值">
        <p v-if="thresholdError" class="filter-error" role="alert">{{ thresholdError }}</p>
        <p v-if="thresholdSuccess" class="config-success" role="status">{{ thresholdSuccess }}</p>
        <div v-if="!thresholds.length" class="field-hint">
          {{ thresholdsLoading ? '正在读取阈值…' : '暂无可调阈值' }}
        </div>
        <a-form v-else layout="vertical" class="risk-threshold-form">
          <div class="config-grid">
            <a-form-item
              v-for="item in thresholds"
              :key="item.key"
              :label="item.label"
              :help="describeThresholdSource(item)"
            >
              <a-input-group compact>
                <a-input-number
                  class="threshold-input"
                  :value="thresholdDraft[item.key]"
                  :min="0"
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

      <!-- ============ 告警列表（只读） ============ -->
      <ConsoleSummaryGrid class="risk-summary">
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

      <ConsolePanel title="筛选" description="时间范围按 Asia/Shanghai 解释；结束日期含当天整天。">
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

      <ConsolePanel title="告警列表" :description="alertScopeDescription">
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
              <span v-else-if="column.key === 'ruleId'">{{ riskRuleLabel(record.ruleId) }}</span>
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

    <!-- 告警详情：**只读**。这里刻意不放任何处置按钮（96 C1）。 -->
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
          <!--
            详情用原生 dl 而不是 a-descriptions：本仓 main.js 没有全局注册 Descriptions，
            各页都是局部 import。风控详情是纯只读展示，用语义化 dl 既省一次注册，
            也让「告警 ID / 规则 / 证据」在无 JS 组件的测试环境里可读。
          -->
          <dl class="detail-list">
            <div class="detail-row"><dt>告警 ID</dt><dd>{{ detail.id }}</dd></div>
            <div class="detail-row"><dt>规则</dt><dd>{{ riskRuleLabel(detail.ruleId) }}</dd></div>
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
        </template>
      </a-spin>
    </a-modal>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, reactive, ref, useId, watch } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import ConsoleTableShell from '@/components/ConsoleTableShell.vue'
import {
  getRiskAlertDetail,
  getRiskAlerts,
  getRiskThresholds,
  getRiskWebhookSettings,
  resetRiskThreshold,
  testRiskWebhook,
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
  describeRiskWebhookState,
  formatMentionedInput,
  formatRiskDateTime,
  normalizeRiskWebhookSettings,
  readRiskListBody,
  readRiskMessage,
  readRiskThrownMessage,
  riskRuleLabel,
  riskSeverityMeta,
  validateRiskAlertFilters,
} from './riskAdminHelpers.mjs'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

const emptyFilters = () => ({ ruleId: undefined, userId: '', notified: undefined, createdFrom: '', createdTo: '' })
const emptyConfig = () => ({ enabled: false, webhookUrl: '', mentionedList: '', clearWebhook: false })
const emptySettings = () => ({ enabled: false, configured: false, webhookUrl: '', preview: '', mentionedList: [] })

export default {
  name: 'RiskAdminPage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell },
  setup() {
    const route = useRoute()
    const prefix = 'risk-admin-' + useId()
    const fieldId = field => prefix + '-' + field
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    // 列表
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

    const alertPagination = reactive({
      current: 1, pageSize: 20, total: 0,
      showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'],
      showTotal: value => `共 ${value} 条`,
    })

    let alive = true
    let mounted = false
    let readVersion = 0
    let writeVersion = 0
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

    const ruleOptions = RISK_RULE_OPTIONS.map(item => ({ label: item.label, value: item.value }))
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
        scope.ruleId && `规则：${riskRuleLabel(scope.ruleId)}`,
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
    const loadThresholds = async () => {
      if (!canManage()) return false
      thresholdsLoading.value = true
      thresholdError.value = ''
      try {
        const res = await getRiskThresholds()
        if (!canManage()) return false
        if (Number(res?.status) !== 200) {
          thresholdError.value = readRiskMessage(res, '读取规则阈值失败')
          return false
        }
        applyThresholdItems(res?.data?.data?.items)
        return true
      } catch (error) {
        if (canManage()) thresholdError.value = readRiskThrownMessage(error, '读取规则阈值失败')
        return false
      } finally {
        if (canManage()) thresholdsLoading.value = false
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
      if (!item.configured) {
        return `当前生效：${item.value}${unit} 默认值：${item.defaultValue} 未显式配置（读的是代码默认值）`
      }
      const source = item.source === 'system_settings' ? '管理页配置' : (item.source === 'risk_rules' ? '规则表列值' : '代码默认值')
      return `当前生效：${item.value}${unit} 来源：${source} 默认值：${item.defaultValue}`
    }
    const saveThreshold = async item => {
      if (!canManage() || thresholdWriting.value || !item?.key) return
      const value = Number(thresholdDraft[item.key])
      thresholdWriting.value = true
      thresholdError.value = ''
      thresholdSuccess.value = ''
      try {
        const res = await updateRiskThreshold(item.key, value)
        if (!canManage()) return
        if (Number(res?.status) !== 200) {
          // 400 的文案由后端给出（占比越界/必须大于 0 等），直接展示。
          thresholdError.value = readRiskMessage(res, '保存阈值失败')
          return
        }
        const saved = res?.data?.data?.item
        if (saved && saved.key) thresholdDraft[saved.key] = Number(saved.value)
        thresholdSuccess.value = `${item.label} 已保存为 ${saved?.value ?? value}，判定立即生效`
        // 重新拉一次，保证整张表与服务端一致（含 source/configured）。
        await loadThresholds()
      } catch (error) {
        if (canManage()) thresholdError.value = readRiskThrownMessage(error, '保存阈值失败')
      } finally {
        if (canManage()) thresholdWriting.value = false
      }
    }
    const resetThreshold = async item => {
      if (!canManage() || thresholdWriting.value || !item?.key) return
      thresholdWriting.value = true
      thresholdError.value = ''
      thresholdSuccess.value = ''
      try {
        const res = await resetRiskThreshold(item.key)
        if (!canManage()) return
        if (Number(res?.status) !== 200) {
          thresholdError.value = readRiskMessage(res, '重置阈值失败')
          return
        }
        thresholdSuccess.value = `${item.label} 已重置为默认值 ${item.defaultValue}`
        await loadThresholds()
      } catch (error) {
        if (canManage()) thresholdError.value = readRiskThrownMessage(error, '重置阈值失败')
      } finally {
        if (canManage()) thresholdWriting.value = false
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
      readVersion += 1
      writeVersion += 1
    }

    watch(() => [currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status], () => {
      retire()
      alerts.value = []; alertsTotal.value = 0; alertPagination.total = 0; hasLoadedAlerts.value = false
      detailVisible.value = false; detail.value = null
      configLoaded.value = false
      settings.value = emptySettings()
      Object.assign(config, emptyConfig())
      if (mounted && canManage()) { void loadAlerts(); void loadConfig(); void loadThresholds() }
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
      }
    })
    onBeforeUnmount(() => {
      alive = false
      mounted = false
      retire()
    })

    return {
      fieldId, canManage,
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
      riskRuleLabel, riskSeverityMeta, formatRiskDateTime, describeRiskWebhookState,
      sumTableColumnWidths,
    }
  },
}
</script>

<style scoped>
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
</style>
