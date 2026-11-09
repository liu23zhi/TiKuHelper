<template>
  <div class="console-page review-rebate-admin-page">
    <ConsolePageHeading title="评价返现管理" description="配置活动与规则表，并复核用户提交的截图。Agent 打分只作建议，最终发放由这里决定。">
      <template #actions>
        <a-button :loading="loading" :disabled="!canManage() || loading || busy" @click="loadAll">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert v-if="!canManage()" type="warning" show-icon class="page-alert" message="当前身份无评价返现管理权限，请使用管理员账号登录。" />

    <template v-if="canManage()">
      <section v-if="programFailure" class="page-alert">
        <RequestFailureAlert
          :key="programFailureContextKey"
          :failure="programFailure"
          :message="programLoaded ? '活动配置刷新失败，页面显示的仍是上次读取的结果。' : '活动配置尚未加载成功，请重试读取。'"
          :retryable="true"
          :busy="loading || savingProgram"
          :context-key="programFailureContextKey"
          :is-current="isCurrentProgramFailure"
          @retry="loadProgram"
        />
      </section>

      <!-- ── 活动配置 ────────────────────────────────────────────── -->
      <ConsolePanel title="活动配置" description="修改后立即生效。关闭开关后用户端整页显示「功能未开启」。">
        <a-form layout="vertical" class="config-form" :disabled="loading || savingProgram">
          <div class="config-grid">
            <a-form-item label="活动名称" :html-for="fieldId('programName')">
              <a-input :id="fieldId('programName')" :value="programForm.name" @update:value="value => setProgramField('name', value)" placeholder="例如：商店评价返现" />
            </a-form-item>
            <a-form-item label="商店条目地址" :html-for="fieldId('storeUrl')">
              <a-input :id="fieldId('storeUrl')" :value="programForm.storeUrl" type="url" @update:value="value => setProgramField('storeUrl', value)" placeholder="https://…" />
            </a-form-item>
            <a-form-item label="扩展 ID" :html-for="fieldId('extensionId')">
              <a-input :id="fieldId('extensionId')" :value="programForm.extensionId" @update:value="value => setProgramField('extensionId', value)" placeholder="用于限定抓取范围的扩展条目 ID" />
            </a-form-item>
            <a-form-item label="每账号可领次数" :html-for="fieldId('maxClaimsPerUser')">
              <a-input-number :id="fieldId('maxClaimsPerUser')" :value="programForm.maxClaimsPerUser" :min="1" :precision="0" style="width: 100%" @update:value="value => setProgramField('maxClaimsPerUser', value)" />
            </a-form-item>
            <a-form-item label="每账号可领金额（元）" :html-for="fieldId('maxAmountPerUser')">
              <a-input-number :id="fieldId('maxAmountPerUser')" :value="programForm.maxAmountPerUser" :min="0" :precision="6" style="width: 100%" @update:value="value => setProgramField('maxAmountPerUser', value)" />
            </a-form-item>
            <a-form-item label="Agent 置信度门槛" :html-for="fieldId('minAgentConfidence')">
              <a-input-number :id="fieldId('minAgentConfidence')" :value="programForm.minAgentConfidence" :min="0" :max="1" :step="0.05" :precision="2" style="width: 100%" @update:value="value => setProgramField('minAgentConfidence', value)" />
              <div class="hint">0–1 之间。低于此值的提交会被标记并强制人工复核。</div>
            </a-form-item>
            <a-form-item label="政策版本" :html-for="fieldId('policyVersion')">
              <a-input :id="fieldId('policyVersion')" :value="programForm.policyVersion" @update:value="value => setProgramField('policyVersion', value)" placeholder="用于在发放记录里保留当时口径" />
            </a-form-item>
            <a-form-item label="启用活动" :html-for="fieldId('programEnabled')">
              <a-switch :id="fieldId('programEnabled')" :checked="programForm.enabled" checked-children="开启" un-checked-children="关闭" @update:checked="value => setProgramField('enabled', value === true)" />
            </a-form-item>
          </div>

          <p v-if="programValidation" class="form-error" role="alert">{{ programValidation }}</p>
          <a-space wrap>
            <a-button type="primary" :loading="savingProgram" :disabled="loading || busy || !!programValidation" @click="saveProgram">保存活动配置</a-button>
            <a-button :disabled="loading || busy" @click="resetProgramForm">还原</a-button>
          </a-space>
        </a-form>
        <a-alert v-if="programSaved" type="success" show-icon class="status-alert" message="活动配置已保存" />
      </ConsolePanel>

      <!-- ── 规则表 ──────────────────────────────────────────────── -->
      <ConsolePanel title="规则表" description="区间可以重叠，按优先级从高到低取第一条命中；都不命中时金额为 0。">
        <template #actions>
          <a-button type="primary" :disabled="loading || busy" @click="openRuleDialog()">新增规则</a-button>
        </template>

        <section v-if="rulesFailure" class="status-alert">
          <RequestFailureAlert
            :key="rulesFailureContextKey"
            :failure="rulesFailure"
            :message="rulesLoaded ? '规则表刷新失败，页面显示的仍是上次读取的结果。' : '规则表尚未加载成功，请重试读取。'"
            :retryable="true"
            :busy="loading || savingRule"
            :context-key="rulesFailureContextKey"
            :is-current="isCurrentRulesFailure"
            @retry="loadRules"
          />
        </section>
        <a-alert v-else-if="ruleWarning" type="warning" show-icon :message="ruleWarning" class="status-alert" />

        <ConsoleTableShell v-if="rules.length" label="评价返现规则表">
          <a-table table-layout="fixed" :data-source="rules" :columns="ruleColumns" :pagination="false" row-key="id" size="small" :scroll="{ x: sumTableColumnWidths(ruleColumns, 150) }">
            <template #bodyCell="{ column, record }">
              <span v-if="column.key === 'id'">{{ record.id ?? '-' }}</span>
              <span v-else-if="column.key === 'stars'">{{ rangeText(record.minStars, record.maxStars) }}</span>
              <span v-else-if="column.key === 'contentScore'">{{ rangeText(record.minContentScore, record.maxContentScore) }}</span>
              <span v-else-if="column.key === 'amount'" class="money-cell">{{ moneyText(record.amount) }}</span>
              <span v-else-if="column.key === 'priority'">{{ record.priority ?? '—' }}</span>
              <a-tag v-else-if="column.key === 'enabled'" :color="record.enabled === false ? 'default' : 'green'">{{ record.enabled === false ? '已停用' : '已启用' }}</a-tag>
              <span v-else-if="column.key === 'remark'" class="note-cell">{{ record.remark || '—' }}</span>
              <a-space v-else-if="column.key === 'action'" wrap>
                <a-button type="link" :disabled="loading || busy" @click="openRuleDialog(record)">编辑</a-button>
                <a-popconfirm title="删除这条规则？" ok-text="删除" cancel-text="取消" @confirm="removeRule(record)">
                  <a-button type="link" danger :disabled="loading || busy">删除</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table>
        </ConsoleTableShell>
        <a-empty v-else-if="rulesLoaded && !loading" description="还没有配置任何规则；没有规则时所有提交都会判为未达门槛。" />
      </ConsolePanel>

      <!-- ── 待审列表 ────────────────────────────────────────────── -->
      <ConsolePanel title="提交列表" description="置信度低于门槛的行会有标记；批准会按命中规则发放赠金，拒绝必须填写理由。">
        <template #actions>
          <a-button :loading="loadingClaims" :disabled="loading || busy" @click="loadClaims">刷新提交</a-button>
        </template>

        <section v-if="claimsFailure" class="status-alert">
          <RequestFailureAlert
            :key="claimsFailureContextKey"
            :failure="claimsFailure"
            :message="claimsLoaded ? '提交列表刷新失败，页面显示的仍是上次读取的结果。' : '提交列表尚未加载成功，请重试读取。'"
            :retryable="true"
            :busy="loadingClaims || deciding"
            :context-key="claimsFailureContextKey"
            :is-current="isCurrentClaimsFailure"
            @retry="loadClaims"
          />
        </section>
        <a-alert v-else-if="decisionWarning" type="warning" show-icon :message="decisionWarning" class="status-alert" />

        <ConsoleTableShell v-if="claims.length" label="评价返现提交列表">
          <a-table table-layout="fixed" :data-source="claims" :columns="claimColumns" :loading="loadingClaims" :pagination="false" row-key="id" size="small" :scroll="{ x: sumTableColumnWidths(claimColumns) }">
            <template #bodyCell="{ column, record }">
              <span v-if="column.key === 'id'">{{ record.id ?? '-' }}</span>
              <span v-else-if="column.key === 'username'">{{ buildUserName(record.username, record.userId) }}</span>
              <a-tag v-else-if="column.key === 'status'" :color="claimStatusColor(record.status)">{{ claimStatusLabel(record.status) }}</a-tag>
              <span v-else-if="column.key === 'screenshot'">
                <a
                  v-if="record.screenshotUrl"
                  :href="record.screenshotUrl"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="screenshot-link"
                >查看截图</a>
                <span v-else class="action-disabled">无图</span>
              </span>
              <span v-else-if="column.key === 'agentScore'">
                {{ buildAgentScore(record) }}
                <a-tag v-if="scorerLabel(record)" color="default" class="scorer-tag">{{ scorerLabel(record) }}</a-tag>
              </span>
              <span v-else-if="column.key === 'agentConfidence'" :class="{ 'is-low-confidence': isLowConfidence(record) }">
                {{ formatConfidence(record.agentConfidence) }}
                <a-tag v-if="isLowConfidence(record)" color="orange" class="low-tag">低于门槛</a-tag>
              </span>
              <span v-else-if="column.key === 'matchedRule'">{{ record.matchedRuleId ?? '—' }}</span>
              <span v-else-if="column.key === 'awardAmount'" class="money-cell">{{ moneyText(record.awardAmount) }}</span>
              <span v-else-if="column.key === 'createdAt'" class="time-cell">{{ formatDateTime(record.createdAt) }}</span>
              <a-space v-else-if="column.key === 'action'" wrap>
                <template v-if="isReviewable(record)">
                  <a-button type="link" :disabled="loading || busy" @click="openDecisionDialog(record, 'approve')">批准</a-button>
                  <a-button type="link" danger :disabled="loading || busy" @click="openDecisionDialog(record, 'reject')">拒绝</a-button>
                </template>
                <span v-else class="action-disabled">{{ claimStatusLabel(record.status) }}</span>
              </a-space>
            </template>
          </a-table>
        </ConsoleTableShell>
        <a-empty v-else-if="claimsLoaded && !loadingClaims" description="当前没有待处理的提交" />
      </ConsolePanel>

      <!-- ── 规则编辑弹窗 ────────────────────────────────────────── -->
      <a-modal
        v-if="ruleDialogOpen"
        :key="ruleDialogKey"
        :open="ruleDialogOpen"
        :title="editingRuleId ? '编辑规则' : '新增规则'"
        ok-text="保存"
        cancel-text="取消"
        :confirm-loading="savingRule"
        :closable="!savingRule"
        :mask-closable="!savingRule"
        :keyboard="!savingRule"
        :cancel-button-props="{ disabled: savingRule }"
        :ok-button-props="{ disabled: savingRule || !!ruleFormError }"
        :wrap-props="{ 'aria-modal': true }"
        @ok="saveRule"
        @cancel="closeRuleDialog"
      >
        <a-form layout="vertical" :disabled="savingRule">
          <div class="rule-form-grid">
            <a-form-item label="星数下限" :html-for="fieldId('minStars')" required>
              <a-input-number :id="fieldId('minStars')" :value="ruleForm.minStars" :min="1" :max="5" :precision="0" style="width: 100%" @update:value="value => setRuleField('minStars', value)" />
            </a-form-item>
            <a-form-item label="星数上限" :html-for="fieldId('maxStars')" required>
              <a-input-number :id="fieldId('maxStars')" :value="ruleForm.maxStars" :min="1" :max="5" :precision="0" style="width: 100%" @update:value="value => setRuleField('maxStars', value)" />
            </a-form-item>
            <a-form-item label="内容分下限" :html-for="fieldId('minContentScore')" required>
              <a-input-number :id="fieldId('minContentScore')" :value="ruleForm.minContentScore" :min="1" :max="10" :precision="0" style="width: 100%" @update:value="value => setRuleField('minContentScore', value)" />
            </a-form-item>
            <a-form-item label="内容分上限" :html-for="fieldId('maxContentScore')" required>
              <a-input-number :id="fieldId('maxContentScore')" :value="ruleForm.maxContentScore" :min="1" :max="10" :precision="0" style="width: 100%" @update:value="value => setRuleField('maxContentScore', value)" />
            </a-form-item>
            <a-form-item label="发放金额（元）" :html-for="fieldId('ruleAmount')" required>
              <a-input-number :id="fieldId('ruleAmount')" :value="ruleForm.amount" :min="0" :precision="6" style="width: 100%" @update:value="value => setRuleField('amount', value)" />
            </a-form-item>
            <a-form-item label="优先级" :html-for="fieldId('rulePriority')">
              <a-input-number :id="fieldId('rulePriority')" :value="ruleForm.priority" :precision="0" style="width: 100%" @update:value="value => setRuleField('priority', value)" />
              <div class="hint">多条命中时取优先级最大的一条。</div>
            </a-form-item>
            <a-form-item label="启用规则" :html-for="fieldId('ruleEnabled')">
              <a-switch :id="fieldId('ruleEnabled')" :checked="ruleForm.enabled" @update:checked="value => setRuleField('enabled', value === true)" />
            </a-form-item>
            <a-form-item label="备注" :html-for="fieldId('ruleRemark')">
              <a-input :id="fieldId('ruleRemark')" :value="ruleForm.remark" @update:value="value => setRuleField('remark', value)" placeholder="例如：5星 + 内容分 10 → 1.00" />
            </a-form-item>
          </div>
          <p v-if="ruleFormError" class="form-error" role="alert">{{ ruleFormError }}</p>
        </a-form>
      </a-modal>

      <!-- ── 审核结论弹窗 ────────────────────────────────────────── -->
      <a-modal
        v-if="decisionDialogOpen"
        :key="decisionDialogKey"
        :open="decisionDialogOpen"
        :title="decisionForm.decision === 'approve' ? '批准这条评价返现？' : '拒绝这条评价返现？'"
        ok-text="提交结论"
        cancel-text="取消"
        :confirm-loading="deciding"
        :closable="!deciding"
        :mask-closable="!deciding"
        :keyboard="!deciding"
        :cancel-button-props="{ disabled: deciding }"
        :ok-button-props="{ disabled: deciding || !!decisionValidation, danger: decisionForm.decision === 'reject' }"
        :wrap-props="{ 'aria-modal': true }"
        @ok="submitDecision"
        @cancel="closeDecisionDialog"
      >
        <a-descriptions :column="1" size="small" bordered class="decision-summary">
          <a-descriptions-item label="提交号">{{ decisionRecord.id ?? '—' }}</a-descriptions-item>
          <a-descriptions-item label="用户">{{ buildUserName(decisionRecord.username, decisionRecord.userId) }}</a-descriptions-item>
          <a-descriptions-item label="Agent 打分">{{ buildAgentScore(decisionRecord) }}</a-descriptions-item>
          <a-descriptions-item label="打分理由">
            <span class="note-cell">{{ decisionRecord.agentRationale || '—' }}</span>
          </a-descriptions-item>
          <a-descriptions-item label="置信度">
            <span :class="{ 'is-low-confidence': isLowConfidence(decisionRecord) }">{{ formatConfidence(decisionRecord.agentConfidence) }}</span>
            <a-tag v-if="isLowConfidence(decisionRecord)" color="orange" class="low-tag">低于门槛</a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="命中规则">{{ decisionRecord.matchedRuleId ?? '—' }}</a-descriptions-item>
          <a-descriptions-item label="拟发放金额">{{ moneyText(decisionRecord.awardAmount) }}</a-descriptions-item>
        </a-descriptions>
        <a-form layout="vertical" :disabled="deciding">
          <a-form-item
            :label="decisionForm.decision === 'reject' ? '拒绝理由（必填）' : '审核备注（可选）'"
            :html-for="fieldId('decisionNote')"
            :required="decisionForm.decision === 'reject'"
          >
            <a-textarea
              :id="fieldId('decisionNote')"
              :value="decisionForm.note"
              :rows="3"
              :maxlength="255"
              show-count
              :disabled="deciding"
              placeholder="说明依据，会展示给用户"
              @update:value="updateDecisionNote"
            />
          </a-form-item>
        </a-form>
        <p v-if="decisionValidation" class="form-error" role="alert">{{ decisionValidation }}</p>
        <p v-else-if="decisionForm.decision === 'approve'" class="hint">批准后按命中金额发放到赠金；发放记录保留当时规则与政策版本。</p>
      </a-modal>
    </template>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, reactive, ref, shallowRef, useId, watch } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleTableShell from '@/components/ConsoleTableShell.vue'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import {
  getReviewRebatePrograms,
  updateReviewRebateProgram,
  getReviewRebateRules,
  createReviewRebateRule,
  updateReviewRebateRule,
  deleteReviewRebateRule,
  getReviewRebateClaims,
  decideReviewRebateClaim,
} from '@/api/ReviewRebateApi'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

const CLAIM_STATUS = {
  submitted: { label: '已提交', color: 'blue' },
  scraping: { label: '抓取中', color: 'blue' },
  scored: { label: '已打分', color: 'cyan' },
  pending_review: { label: '待人工复核', color: 'gold' },
  approved: { label: '已通过', color: 'green' },
  rejected: { label: '已拒绝', color: 'red' },
  below_threshold: { label: '未达门槛', color: 'default' },
  duplicate: { label: '疑似重复', color: 'orange' },
  limit_reached: { label: '超出上限', color: 'orange' },
}
const REVIEWABLE = ['submitted', 'scraping', 'scored', 'pending_review']

function readNonnegativeNumber(value) {
  if (!['number', 'string'].includes(typeof value) || String(value).trim() === '') return null
  const number = Number(value)
  return Number.isFinite(number) && number >= 0 ? number : null
}
function readPositiveInt(value) {
  const number = readNonnegativeNumber(value)
  return Number.isInteger(number) && number > 0 ? number : null
}

const emptyProgramForm = () => ({
  name: '', storeUrl: '', extensionId: '', enabled: false,
  maxClaimsPerUser: 3, maxAmountPerUser: 5.04, minAgentConfidence: 0.6, policyVersion: '',
})
const emptyRuleForm = () => ({
  minStars: 5, maxStars: 5, minContentScore: 1, maxContentScore: 10,
  amount: 0, priority: 0, enabled: true, remark: '',
})

export default {
  name: 'ReviewRebateAdminPage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleTableShell, RequestFailureAlert },
  setup() {
    const route = useRoute()
    const prefix = 'review-rebate-admin-' + useId()
    const fieldId = field => `${prefix}-${field}`
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const loading = ref(false)
    const loadedProgram = ref(null)
    const programForm = reactive(emptyProgramForm())
    const programLoaded = ref(false)
    const savingProgram = ref(false)
    const programSaved = ref(false)

    const rules = ref([])
    const rulesLoaded = ref(false)
    const savingRule = ref(false)
    const ruleWarning = ref('')

    const claims = ref([])
    const claimsLoaded = ref(false)
    const loadingClaims = ref(false)
    const deciding = ref(false)
    const decisionWarning = ref('')

    const ruleDialogOpen = ref(false)
    const ruleDialogKey = ref(0)
    const editingRuleId = ref(0)
    const ruleForm = reactive(emptyRuleForm())

    const decisionDialogOpen = ref(false)
    const decisionDialogKey = ref(0)
    const decisionRecord = ref({})
    const decisionForm = reactive({ decision: 'approve', note: '' })

    const programFailure = shallowRef(null)
    const programFailureContextKey = ref(0)
    let programFailureOwner = () => false
    const rulesFailure = shallowRef(null)
    const rulesFailureContextKey = ref(0)
    let rulesFailureOwner = () => false
    const claimsFailure = shallowRef(null)
    const claimsFailureContextKey = ref(0)
    let claimsFailureOwner = () => false

    let alive = true
    let mounted = false
    let programVersion = 0
    let rulesVersion = 0
    let claimsVersion = 0
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
    const busy = computed(() => savingProgram.value || savingRule.value || deciding.value || loadingClaims.value)

    const minConfidence = computed(() => readNonnegativeNumber(programForm.minAgentConfidence))

    const ruleColumns = [
      { title: 'ID', dataIndex: 'id', key: 'id', width: 80 },
      { title: '星数', dataIndex: 'stars', key: 'stars', width: 110 },
      { title: '内容分', dataIndex: 'contentScore', key: 'contentScore', width: 110 },
      { title: '金额', dataIndex: 'amount', key: 'amount', width: 130 },
      { title: '优先级', dataIndex: 'priority', key: 'priority', width: 95 },
      { title: '状态', dataIndex: 'enabled', key: 'enabled', width: 100 },
      { title: '备注', dataIndex: 'remark', key: 'remark', width: 200 },
      { title: '操作', key: 'action', width: 150 },
    ]
    const claimColumns = [
      { title: '提交号', dataIndex: 'id', key: 'id', width: 90 },
      { title: '用户', dataIndex: 'username', key: 'username', width: 175 },
      { title: '状态', dataIndex: 'status', key: 'status', width: 125 },
      { title: '截图', dataIndex: 'screenshot', key: 'screenshot', width: 100 },
      { title: 'Agent 打分', dataIndex: 'agentScore', key: 'agentScore', width: 160 },
      { title: '置信度', dataIndex: 'agentConfidence', key: 'agentConfidence', width: 170 },
      { title: '命中规则', dataIndex: 'matchedRule', key: 'matchedRule', width: 100 },
      { title: '金额', dataIndex: 'awardAmount', key: 'awardAmount', width: 130 },
      { title: '提交时间', dataIndex: 'createdAt', key: 'createdAt', width: 175 },
      { title: '操作', key: 'action', width: 140 },
    ]

    const rangeText = (min, max) => {
      const low = readNonnegativeNumber(min)
      const high = readNonnegativeNumber(max)
      if (low === null && high === null) return '—'
      if (low === null) return `≤ ${high}`
      if (high === null) return `≥ ${low}`
      return low === high ? String(low) : `${low}–${high}`
    }
    // 与 UserUsage 一致：金额最多 6 位小数、去尾零（后端 util.MoneyPrecision = 6）。
    const moneyText = value => {
      const amount = readNonnegativeNumber(value)
      return amount === null ? '—' : `¥ ${formatMoneyValue(amount)}`
    }
    const formatConfidence = value => {
      const confidence = readNonnegativeNumber(value)
      return confidence === null ? '—' : confidence.toFixed(2)
    }
    const formatDateTime = value => {
      if (!value) return '—'
      const date = new Date(value)
      return Number.isFinite(date.getTime())
        ? date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
        : '—'
    }
    const buildUserName = (username, userId) => {
      const name = String(username || '').trim()
      const id = readPositiveInt(userId)
      if (name && id) return `${name}（#${id}）`
      if (name) return name
      if (id) return `#${id}`
      return '—'
    }
    const buildAgentScore = record => {
      const stars = readNonnegativeNumber(record?.agentStars)
      const score = readNonnegativeNumber(record?.agentContentScore)
      if (stars === null && score === null) return '—'
      return `${stars === null ? '—' : stars} 星 / ${score === null ? '—' : score} 分`
    }
    // 打分来源：agent=模型看过截图，stub=未配置模型时按截图摘要派生（不代表任何核验）。
    // 空值 = 历史行或注入式打分器，无法归因。
    const scorerLabel = record => {
      const kind = String(record?.agentScorer || '').trim().toLowerCase()
      if (kind === 'agent') return ''
      if (kind === 'stub') return '占位'
      return record?.agentStars ? '来源未知' : ''
    }
    function claimStatusLabel(value) {
      const key = String(value || '').trim().toLowerCase()
      return CLAIM_STATUS[key]?.label || value || '-'
    }
    function claimStatusColor(value) {
      const key = String(value || '').trim().toLowerCase()
      return CLAIM_STATUS[key]?.color || 'default'
    }
    function isReviewable(record) {
      return REVIEWABLE.includes(String(record?.status || '').trim().toLowerCase())
    }
    function isLowConfidence(record) {
      const confidence = readNonnegativeNumber(record?.agentConfidence)
      const threshold = minConfidence.value
      return confidence !== null && threshold !== null && confidence < threshold
    }

    // ── 校验 ────────────────────────────────────────────────────
    const programValidation = computed(() => {
      const maxClaims = readNonnegativeNumber(programForm.maxClaimsPerUser)
      if (maxClaims === null || !Number.isInteger(maxClaims) || maxClaims < 1) return '每账号可领次数必须是大于 0 的整数'
      const amount = readNonnegativeNumber(programForm.maxAmountPerUser)
      if (amount === null) return '每账号可领金额必须是非负数字'
      const confidence = readNonnegativeNumber(programForm.minAgentConfidence)
      if (confidence === null || confidence > 1) return 'Agent 置信度门槛必须在 0 到 1 之间'
      const url = String(programForm.storeUrl || '').trim()
      if (url && !/^https?:\/\//i.test(url)) return '商店条目地址必须以 http:// 或 https:// 开头'
      return ''
    })
    const ruleFormError = computed(() => {
      const minStars = readNonnegativeNumber(ruleForm.minStars)
      const maxStars = readNonnegativeNumber(ruleForm.maxStars)
      if (minStars === null || maxStars === null) return '星数上下限都必须填写'
      if (minStars < 1 || maxStars > 5) return '星数必须在 1 到 5 之间'
      if (minStars > maxStars) return '星数下限不能大于上限'
      const minScore = readNonnegativeNumber(ruleForm.minContentScore)
      const maxScore = readNonnegativeNumber(ruleForm.maxContentScore)
      if (minScore === null || maxScore === null) return '内容分上下限都必须填写'
      if (minScore < 1 || maxScore > 10) return '内容分必须在 1 到 10 之间'
      if (minScore > maxScore) return '内容分下限不能大于上限'
      const amount = readNonnegativeNumber(ruleForm.amount)
      if (amount === null) return '发放金额必须是非负数字'
      return ''
    })
    const decisionValidation = computed(() => {
      if (decisionForm.decision === 'reject' && !String(decisionForm.note || '').trim()) return '拒绝时必须填写理由'
      return ''
    })

    // ── 失败态 ──────────────────────────────────────────────────
    const clearProgramFailure = () => { programFailure.value = null; programFailureOwner = () => false; programFailureContextKey.value += 1 }
    const clearRulesFailure = () => { rulesFailure.value = null; rulesFailureOwner = () => false; rulesFailureContextKey.value += 1 }
    const clearClaimsFailure = () => { claimsFailure.value = null; claimsFailureOwner = () => false; claimsFailureContextKey.value += 1 }
    const isCurrentProgramFailure = (source, context) => programFailureOwner() && Boolean(source) && source === programFailure.value && context === programFailureContextKey.value
    const isCurrentRulesFailure = (source, context) => rulesFailureOwner() && Boolean(source) && source === rulesFailure.value && context === rulesFailureContextKey.value
    const isCurrentClaimsFailure = (source, context) => claimsFailureOwner() && Boolean(source) && source === claimsFailure.value && context === claimsFailureContextKey.value

    // ── 读取 ────────────────────────────────────────────────────
    function setProgramField(field, value) {
      if (!canManage() || savingProgram.value || !Object.hasOwn(programForm, field)) return
      programForm[field] = value
      programSaved.value = false
    }
    function applyProgram(data) {
      // 后端 GET /admin/review-rebate/programs 返回 {data:[...]}（与 rules 一致的信封）；
      // 直接把 {data:[...]} 当成活动对象会让整张表单变成空值 —— 看起来像「配置丢了」。
      // 三种历史形状都兼容：裸对象 / 裸数组 / {data:[...]} / {program:{...}}。
      const body = Array.isArray(data)
        ? data[0]
        : (data?.program
          || (Array.isArray(data?.data) ? data.data[0] : (data?.data && typeof data.data === 'object' ? data.data : data)))
      if (!body || typeof body !== 'object') throw new Error('invalid program payload')
      loadedProgram.value = body
      programForm.name = String(body.name || '')
      programForm.storeUrl = String(body.storeUrl || '')
      programForm.extensionId = String(body.extensionId || '')
      programForm.enabled = body.enabled === true
      programForm.maxClaimsPerUser = readNonnegativeNumber(body.maxClaimsPerUser) ?? 3
      programForm.maxAmountPerUser = readNonnegativeNumber(body.maxAmountPerUser) ?? 5.04
      programForm.minAgentConfidence = readNonnegativeNumber(body.minAgentConfidence) ?? 0.6
      programForm.policyVersion = String(body.policyVersion || '')
      programLoaded.value = true
    }
    const loadProgram = async () => {
      if (!canManage()) return false
      const ticket = ++programVersion
      const owns = () => canManage() && ticket === programVersion
      clearProgramFailure()
      const context = programFailureContextKey.value
      const ownsFailure = () => owns() && context === programFailureContextKey.value
      try {
        const res = await getReviewRebatePrograms()
        if (!ownsFailure()) return false
        if (res?.status !== 200 || !res.data) {
          programFailureOwner = ownsFailure; programFailure.value = ensureRequestFailure(res); return false
        }
        applyProgram(res.data)
        return true
      } catch (error) {
        if (ownsFailure()) { programFailureOwner = ownsFailure; programFailure.value = ensureRequestFailure(error) }
        return false
      }
    }

    function applyRules(data) {
      const list = Array.isArray(data) ? data : (Array.isArray(data?.data) ? data.data : null)
      if (!list) throw new Error('invalid rules payload')
      rules.value = list.filter(rule => rule && typeof rule === 'object').map(rule => ({ ...rule }))
      rulesLoaded.value = true
    }
    const loadRules = async () => {
      if (!canManage()) return false
      const ticket = ++rulesVersion
      const owns = () => canManage() && ticket === rulesVersion
      clearRulesFailure()
      const context = rulesFailureContextKey.value
      const ownsFailure = () => owns() && context === rulesFailureContextKey.value
      try {
        const res = await getReviewRebateRules()
        if (!ownsFailure()) return false
        if (res?.status !== 200) { rulesFailureOwner = ownsFailure; rulesFailure.value = ensureRequestFailure(res); return false }
        applyRules(res.data)
        return true
      } catch (error) {
        if (ownsFailure()) { rulesFailureOwner = ownsFailure; rulesFailure.value = ensureRequestFailure(error) }
        return false
      }
    }

    const loadClaims = async () => {
      if (!canManage()) return false
      const ticket = ++claimsVersion
      const owns = () => canManage() && ticket === claimsVersion
      clearClaimsFailure()
      const context = claimsFailureContextKey.value
      const ownsFailure = () => owns() && context === claimsFailureContextKey.value
      loadingClaims.value = true
      try {
        const res = await getReviewRebateClaims()
        if (!ownsFailure()) return false
        const body = res?.data
        const list = Array.isArray(body) ? body : body?.data
        if (res?.status !== 200 || !Array.isArray(list)) {
          claimsFailureOwner = ownsFailure; claimsFailure.value = ensureRequestFailure(res); return false
        }
        claims.value = list.filter(item => item && typeof item === 'object').map(item => ({ ...item }))
        claimsLoaded.value = true
        return true
      } catch (error) {
        if (ownsFailure()) { claimsFailureOwner = ownsFailure; claimsFailure.value = ensureRequestFailure(error) }
        return false
      } finally {
        if (owns()) loadingClaims.value = false
      }
    }

    const loadAll = async () => {
      if (!canManage() || loading.value) return
      loading.value = true
      try {
        await Promise.all([loadProgram(), loadRules(), loadClaims()])
      } finally {
        if (canManage()) loading.value = false
      }
    }

    // ── 写操作 ──────────────────────────────────────────────────
    const saveProgram = async () => {
      if (!canManage() || savingProgram.value || programValidation.value) return
      const id = readPositiveInt(loadedProgram.value?.id)
      if (!id) { programFailure.value = ensureRequestFailure({ code: 'MALFORMED_RESPONSE' }); programFailureOwner = canManage; programFailureContextKey.value += 1; return }
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      clearProgramFailure()
      const context = programFailureContextKey.value
      const ownsFailure = () => owns() && context === programFailureContextKey.value
      savingProgram.value = true
      programSaved.value = false
      try {
        const payload = {
          name: String(programForm.name || '').trim(),
          storeUrl: String(programForm.storeUrl || '').trim(),
          extensionId: String(programForm.extensionId || '').trim(),
          enabled: programForm.enabled === true,
          maxClaimsPerUser: Number(programForm.maxClaimsPerUser),
          maxAmountPerUser: Number(programForm.maxAmountPerUser),
          minAgentConfidence: Number(programForm.minAgentConfidence),
          policyVersion: String(programForm.policyVersion || '').trim(),
        }
        const res = await updateReviewRebateProgram(id, payload)
        if (!ownsFailure()) return
        if (res?.status !== 200) { programFailureOwner = ownsFailure; programFailure.value = ensureRequestFailure(res); return }
        applyProgram(res.data)
        programSaved.value = true
        message.success('活动配置已保存')
      } catch (error) {
        if (ownsFailure()) { programFailureOwner = ownsFailure; programFailure.value = ensureRequestFailure(error) }
      } finally {
        if (owns()) savingProgram.value = false
      }
    }
    const resetProgramForm = () => {
      if (!canManage() || savingProgram.value || !loadedProgram.value) return
      applyProgram(loadedProgram.value)
      programSaved.value = false
    }

    function setRuleField(field, value) {
      if (savingRule.value || !Object.hasOwn(ruleForm, field)) return
      ruleForm[field] = value
    }
    const openRuleDialog = record => {
      if (!canManage() || busy.value) return
      ruleDialogKey.value += 1
      editingRuleId.value = readPositiveInt(record?.id) || 0
      Object.assign(ruleForm, emptyRuleForm(), editingRuleId.value ? {
        minStars: readNonnegativeNumber(record.minStars) ?? 5,
        maxStars: readNonnegativeNumber(record.maxStars) ?? 5,
        minContentScore: readNonnegativeNumber(record.minContentScore) ?? 1,
        maxContentScore: readNonnegativeNumber(record.maxContentScore) ?? 10,
        amount: readNonnegativeNumber(record.amount) ?? 0,
        priority: readNonnegativeNumber(record.priority) ?? 0,
        enabled: record.enabled !== false,
        remark: String(record.remark || ''),
      } : {})
      ruleDialogOpen.value = true
    }
    const closeRuleDialog = () => {
      if (savingRule.value) return
      ruleDialogOpen.value = false
      editingRuleId.value = 0
      Object.assign(ruleForm, emptyRuleForm())
    }
    const saveRule = async () => {
      if (!canManage() || savingRule.value || ruleFormError.value) return
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      clearRulesFailure()
      const context = rulesFailureContextKey.value
      const ownsFailure = () => owns() && context === rulesFailureContextKey.value
      savingRule.value = true
      ruleWarning.value = ''
      try {
        const payload = {
          minStars: Number(ruleForm.minStars),
          maxStars: Number(ruleForm.maxStars),
          minContentScore: Number(ruleForm.minContentScore),
          maxContentScore: Number(ruleForm.maxContentScore),
          amount: Number(ruleForm.amount),
          priority: Number(ruleForm.priority || 0),
          enabled: ruleForm.enabled === true,
          remark: String(ruleForm.remark || '').trim(),
        }
        const res = editingRuleId.value
          ? await updateReviewRebateRule(editingRuleId.value, payload)
          : await createReviewRebateRule(payload)
        if (!ownsFailure()) return
        if (res?.status !== 200 && res?.status !== 201) {
          rulesFailureOwner = ownsFailure; rulesFailure.value = ensureRequestFailure(res); return
        }
        ruleDialogOpen.value = false
        editingRuleId.value = 0
        Object.assign(ruleForm, emptyRuleForm())
        message.success('规则已保存')
        await loadRules()
      } catch (error) {
        if (ownsFailure()) { rulesFailureOwner = ownsFailure; rulesFailure.value = ensureRequestFailure(error) }
      } finally {
        if (owns()) savingRule.value = false
      }
    }
    const removeRule = async record => {
      const id = readPositiveInt(record?.id)
      if (!id || !canManage() || busy.value) return
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      clearRulesFailure()
      const context = rulesFailureContextKey.value
      const ownsFailure = () => owns() && context === rulesFailureContextKey.value
      savingRule.value = true
      ruleWarning.value = ''
      try {
        const res = await deleteReviewRebateRule(id)
        if (!ownsFailure()) return
        if (res?.status === 409) { ruleWarning.value = '该规则已被引用，暂不能删除；可改为停用。'; return }
        if (res?.status !== 200 && res?.status !== 204) {
          rulesFailureOwner = ownsFailure; rulesFailure.value = ensureRequestFailure(res); return
        }
        message.success('规则已删除')
        await loadRules()
      } catch (error) {
        if (ownsFailure()) { rulesFailureOwner = ownsFailure; rulesFailure.value = ensureRequestFailure(error) }
      } finally {
        if (owns()) savingRule.value = false
      }
    }

    const openDecisionDialog = (record, decision) => {
      if (!canManage() || busy.value || !isReviewable(record)) return
      decisionDialogKey.value += 1
      decisionRecord.value = { ...record }
      decisionForm.decision = decision
      decisionForm.note = ''
      decisionDialogOpen.value = true
    }
    const closeDecisionDialog = () => {
      if (deciding.value) return
      decisionDialogOpen.value = false
      decisionRecord.value = {}
      decisionForm.note = ''
    }
    const updateDecisionNote = value => { if (!deciding.value) decisionForm.note = String(value ?? '') }
    const submitDecision = async () => {
      if (!canManage() || deciding.value || decisionValidation.value) return
      const id = readPositiveInt(decisionRecord.value?.id)
      if (!id) return
      const ticket = ++writeVersion
      const owns = () => canManage() && ticket === writeVersion
      clearClaimsFailure()
      const context = claimsFailureContextKey.value
      const ownsFailure = () => owns() && context === claimsFailureContextKey.value
      deciding.value = true
      decisionWarning.value = ''
      try {
        const res = await decideReviewRebateClaim(id, decisionForm.decision, decisionForm.note)
        if (!ownsFailure()) return
        if (res?.status === 409) { decisionWarning.value = '该提交状态已变更，请刷新后核对。'; await loadClaims(); return }
        if (res?.status !== 200) {
          claimsFailureOwner = ownsFailure; claimsFailure.value = ensureRequestFailure(res); return
        }
        decisionDialogOpen.value = false
        decisionRecord.value = {}
        decisionForm.note = ''
        message.success(decisionForm.decision === 'approve' ? '已批准' : '已拒绝')
        await loadClaims()
      } catch (error) {
        if (ownsFailure()) { claimsFailureOwner = ownsFailure; claimsFailure.value = ensureRequestFailure(error) }
      } finally {
        if (owns()) deciding.value = false
      }
    }

    const retire = () => {
      programVersion += 1; rulesVersion += 1; claimsVersion += 1; writeVersion += 1
      rules.value = []; rulesLoaded.value = false
      claims.value = []; claimsLoaded.value = false; loadingClaims.value = false
      ruleDialogOpen.value = false; decisionDialogOpen.value = false
      ruleWarning.value = ''; decisionWarning.value = ''
      savingProgram.value = false; savingRule.value = false; deciding.value = false
      clearProgramFailure(); clearRulesFailure(); clearClaimsFailure()
    }

    watch(() => [currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status], () => {
      retire()
      programLoaded.value = false
      if (mounted && canManage()) void loadAll()
    }, { flush: 'sync' })
    watch(() => getCurrentUserLogoutEpoch(), value => {
      if (value === observedLogout) return
      observedLogout = value
      retire()
      programLoaded.value = false
    }, { flush: 'sync' })
    watch(() => route.path, () => { retire() }, { flush: 'sync' })

    onMounted(() => {
      mounted = true
      if (canManage()) void loadAll()
    })
    onBeforeUnmount(() => {
      alive = false; mounted = false
      retire()
    })

    return {
      fieldId, canManage, busy, loading,
      programForm, programLoaded, savingProgram, programSaved, programValidation,
      rules, rulesLoaded, savingRule, ruleWarning, ruleColumns,
      claims, claimsLoaded, loadingClaims, deciding, decisionWarning, claimColumns,
      programFailure, programFailureContextKey, isCurrentProgramFailure,
      rulesFailure, rulesFailureContextKey, isCurrentRulesFailure,
      claimsFailure, claimsFailureContextKey, isCurrentClaimsFailure,
      ruleDialogOpen, ruleDialogKey, editingRuleId, ruleForm, ruleFormError,
      decisionDialogOpen, decisionDialogKey, decisionRecord, decisionForm, decisionValidation,
      rangeText, moneyText, formatConfidence, formatDateTime, buildUserName, buildAgentScore,
      scorerLabel,
      claimStatusLabel, claimStatusColor, isReviewable, isLowConfidence,
      setProgramField, saveProgram, resetProgramForm, loadProgram, loadRules, loadClaims, loadAll,
      setRuleField, openRuleDialog, closeRuleDialog, saveRule, removeRule,
      openDecisionDialog, closeDecisionDialog, updateDecisionNote, submitDecision,
      sumTableColumnWidths,
    }
  },
}
</script>

<style scoped>
.page-alert, .status-alert { margin-bottom: 16px; }
.config-form { margin-bottom: 8px; }
.config-grid, .rule-form-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 220px), 1fr)); gap: 16px 24px; }
.rule-form-grid { grid-template-columns: repeat(auto-fit, minmax(min(100%, 175px), 1fr)); }
.hint { margin: 8px 0 0; color: var(--console-muted); font-size: 12px; line-height: 1.7; }
.form-error { margin: 0 0 16px; color: var(--console-danger, #c13e41); font-size: 13px; }
.money-cell, .time-cell { font-variant-numeric: tabular-nums; }
.note-cell { overflow-wrap: anywhere; white-space: pre-wrap; }
.scorer-tag { margin-inline-start: 6px; }
.screenshot-link { overflow-wrap: anywhere; }
.action-disabled { color: var(--console-muted); font-size: 13px; }
.is-low-confidence { color: var(--console-danger, #c13e41); font-weight: 600; }
.low-tag { margin-inline-start: 6px; }
.decision-summary { margin-bottom: 16px; }
</style>
