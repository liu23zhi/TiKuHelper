<template>
  <div class="console-page sensitive-image-page">
    <ConsolePageHeading
      title="敏感图片检测管理"
      description="按图片内容（SHA256）缓存判定结论。检测记录与每日额度是两件事：额度限制「触发新外呼的图」，本页两者都能看。"
    >
      <template #actions>
        <a-button :loading="loadingQuota" :disabled="!canManage() || busy()" @click="loadQuota">刷新额度</a-button>
        <a-button
          :loading="loadingCurrentTab"
          :disabled="!canManage() || busy()"
          @click="reloadCurrentTab"
        >刷新列表</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert
      v-if="!canManage()"
      type="warning"
      show-icon
      class="page-alert"
      message="当前身份无敏感图片检测管理权限，请使用管理员账号登录。"
    />

    <template v-if="canManage()">
      <ConsoleSummaryGrid class="sensitive-image-summary">
        <div>
          <div class="summary-label">检测开关</div>
          <div class="summary-value">
            {{ quota.enabled ? '已启用' : '已停用' }}
          </div>
        </div>
        <div>
          <div class="summary-label">每工单上限（张）</div>
          <div class="summary-value">{{ quota.perTicketLimit === null ? '--' : quota.perTicketLimit }}</div>
        </div>
        <div>
          <div class="summary-label">每人每日检测额度（张/天）</div>
          <div class="summary-value">{{ quota.dailyUserLimit === null ? '--' : quota.dailyUserLimit }}</div>
        </div>
        <div>
          <div class="summary-label">当日剩余（可选用户）</div>
          <div class="summary-value">
            {{ quota.remaining === null ? '--' : quota.remaining }}
            <span v-if="quota.date" class="summary-hint">{{ quota.date }}</span>
          </div>
        </div>
      </ConsoleSummaryGrid>

      <a-alert v-if="quotaError" type="error" show-icon :message="quotaError" class="status-alert" />

      <ConsolePanel
        title="额度配置"
        description="三个键互相独立：只提交改动的那一项，未提交的键保持现值。上限填 0 会被后端拒绝。"
      >
        <a-form layout="inline" class="quota-form" :onFinish="saveQuota">
          <a-form-item label="启用检测">
            <a-switch
              :checked="quotaDraft.enabled"
              :disabled="savingQuota || busy()"
              @change="value => setQuotaDraft('enabled', value === true)"
            />
          </a-form-item>
          <a-form-item label="每工单上限" :html-for="fieldId('per-ticket')">
            <a-input
              :id="fieldId('per-ticket')"
              :value="quotaDraft.perTicketLimit"
              class="quota-input"
              :disabled="savingQuota || busy()"
              placeholder="例如 50"
              @update:value="value => setQuotaDraft('perTicketLimit', value)"
            />
          </a-form-item>
          <a-form-item label="每人每日检测额度" :html-for="fieldId('daily')">
            <a-input
              :id="fieldId('daily')"
              :value="quotaDraft.dailyUserLimit"
              class="quota-input"
              :disabled="savingQuota || busy()"
              placeholder="例如 20"
              @update:value="value => setQuotaDraft('dailyUserLimit', value)"
            />
          </a-form-item>
          <a-form-item>
            <a-button type="primary" html-type="submit" :loading="savingQuota" :disabled="busy()">保存额度配置</a-button>
          </a-form-item>
        </a-form>
        <p class="field-hint">
          「每人每日检测额度」是防反复外呼的闸门（累计在 sensitive_image_daily_quotas）；「每工单上限」限制单个工单能外呼多少张。二者不是同一件事。
        </p>
        <p v-if="quotaSaveError" class="filter-error" role="alert">{{ quotaSaveError }}</p>
      </ConsolePanel>

      <ConsolePanel
        title="手动触发检测"
        description="管理员复核通道，不经过额度闸门。结论按 SHA256 生效：一张图被多张工单引用时，本次会把所有引用行一起改掉。"
      >
        <a-form layout="inline" class="detect-form" :onFinish="runDetect">
          <a-form-item label="图片标识（SHA256）" :html-for="fieldId('detect-sha')">
            <a-input
              :id="fieldId('detect-sha')"
              :value="detectForm.imageSha256"
              class="detect-sha"
              allow-clear
              :disabled="detecting || busy()"
              placeholder="64 位十六进制，或点列表里的『手动检测』"
              @update:value="value => setDetectField('imageSha256', value)"
            />
          </a-form-item>
          <a-form-item label="或 工单编号" :html-for="fieldId('detect-ticket')">
            <a-input
              :id="fieldId('detect-ticket')"
              :value="detectForm.ticketId"
              class="detect-ticket"
              allow-clear
              :disabled="detecting || busy()"
              placeholder="按工单批量重检"
              @update:value="value => setDetectField('ticketId', value)"
            />
          </a-form-item>
          <a-form-item>
            <a-button type="primary" html-type="submit" :loading="detecting" :disabled="busy()">手动触发检测</a-button>
          </a-form-item>
        </a-form>
        <p class="field-hint">
          两个输入框都填时只处理图片标识（按 SHA256 重判），工单编号会被忽略 —— 页面会明确提示这一点，不会假装批量也跑了。
        </p>
        <p v-if="detectError" class="filter-error" role="alert">{{ detectError }}</p>

        <!-- 判据②：本次涉及多少行必须写在响应区，且数字缺失时写「未知」而不是 0。 -->
        <a-alert
          v-if="detectOutcome"
          :type="detectOutcome.attachSyncError ? 'warning' : 'success'"
          show-icon
          class="detect-outcome"
          data-testid="detect-outcome"
        >
          <template #message>
            <span data-testid="detect-outcome-summary">{{ detectOutcomeText }}</span>
          </template>
          <template #description>
            <div class="detect-outcome-detail">
              <p data-testid="detect-outcome-rows">
                涉及行数：
                <strong>{{ detectOutcome.affectedAttachments === null ? '未知' : detectOutcome.affectedAttachments }}</strong>
                <span class="detail-hint">（附件行 = 引用该图片的工单数，不是工单里图片的张数）</span>
              </p>
              <p v-if="detectOutcome.attachSyncError" data-testid="detect-outcome-sync-error">
                缓存已更新，但附件行同步失败：{{ detectOutcome.attachSyncError }}
              </p>
              <p v-if="detectOutcome.mode === 'ticket'" data-testid="detect-outcome-ticket">
                本工单共 {{ detectOutcome.total === null ? '未知' : detectOutcome.total }} 张，重新检测
                {{ detectOutcome.detected === null ? '未知' : detectOutcome.detected }} 张，跳过
                {{ detectOutcome.skipped === null ? '未知' : detectOutcome.skipped }} 张（字节取不到）。
              </p>
              <p class="detail-hint" data-testid="detect-outcome-raw-only">
                响应只回判定结论与计数；图片字节与上游原始响应不在本接口响应里，也不会在本页展示。
              </p>
            </div>
          </template>
        </a-alert>
      </ConsolePanel>

      <a-tabs v-model:activeKey="activeTab" class="sensitive-image-tabs">
        <!--
          两个 Tab 各自独立分页：quotaPagination / recordPagination 是两套对象，
          「在 A Tab 翻到第 3 页再切到 B Tab」不会串页。
        -->
        <a-tab-pane
          v-for="tab in tabs"
          :key="tab.key"
          :tab="tab.label"
        >
          <ConsolePanel :title="tab.label" :description="tab.description">
            <a-form v-if="tab.key === QUOTA_TAB" layout="inline" class="filter-form" :onFinish="reloadCurrentTab">
              <a-form-item label="记录状态" :html-for="fieldId('quota-status')">
                <a-select
                  :id="fieldId('quota-status')"
                  :value="quotaStatus"
                  :options="quotaStatusOptions"
                  class="filter-status"
                  :disabled="busy()"
                  @update:value="value => setQuotaStatus(value)"
                  @change="reloadCurrentTab"
                />
              </a-form-item>
              <a-form-item>
                <a-button type="primary" html-type="submit" :loading="loadingCurrentTab" :disabled="busy()">查询</a-button>
              </a-form-item>
            </a-form>
            <p v-if="tab.key === QUOTA_TAB" class="field-hint" data-testid="quota-tab-limitation">
              这一 Tab 列出的是「未得出可信结论」的检测记录。因额度耗尽而<span class="emphasis">从未写入缓存</span>的图片不在其中 —— 后端目前没有按 scan_state 反查这类图片的接口，本页不假装能列出来。
            </p>
            <!--
              sensitive 结论的歧义提示（captain 在验收时从后端源码确认）：
              额度耗尽时 sensitive_image_detect.go:305-307 会把 scan_state 也写成
              "sensitive"，与真违规同值，且表里没有列能区分。不提示的话管理员会
              把「没检测就按敏感处理」当成真违规去封禁。
            -->
            <p v-else class="field-hint" data-testid="records-tab-sensitive-caveat">
              本表里的<span class="emphasis">敏感</span>有两种来源：上游真的判定为敏感，以及<span class="emphasis">额度耗尽时的默认按敏感处理</span>。后端目前没有列能区分这两者 —— 请结合「判定来源」与「原因」两列人工判断，不要只看结论列就下处置。
            </p>

            <p v-if="currentError" class="filter-error" role="alert">{{ currentError }}</p>

            <ConsoleTableShell :label="tab.label">
              <a-table
                table-layout="fixed"
                :data-source="currentRows"
                :columns="columns"
                :loading="currentLoading"
                :pagination="currentPagination"
                row-key="imageSha256"
                :scroll="{ x: sumTableColumnWidths(columns) }"
                :locale="{ emptyText: currentEmptyText }"
                @change="onCurrentTableChange"
              >
                <template #bodyCell="{ column, record }">
                  <code v-if="column.key === 'imageSha256'" class="hash-cell" :title="record.imageSha256">{{ shortSha(record.imageSha256) }}</code>
                  <a-tag v-else-if="column.key === 'status'" :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
                  <span v-else-if="column.key === 'contentType'">{{ record.contentType || '—' }}</span>
                  <span v-else-if="column.key === 'fileSize'">{{ formatBytes(record.fileSize) }}</span>
                  <span v-else-if="column.key === 'provider'">{{ record.provider || '—' }}</span>
                  <span v-else-if="column.key === 'reason'" class="reason-cell">{{ record.reason || '—' }}</span>
                  <span v-else-if="column.key === 'hitCount'" class="count-cell">{{ record.hitCount ?? 0 }}</span>
                  <span v-else-if="column.key === 'updatedAt'">{{ formatDateTime(record.updatedAt) }}</span>
                  <a-space v-else-if="column.key === 'action'" wrap>
                    <a-button
                      type="link"
                      size="small"
                      :disabled="detecting || busy() || !record.imageSha256"
                      @click="detectRecord(record)"
                    >手动检测</a-button>
                    <a-button
                      v-if="shouldOfferGifControls(record.contentType)"
                      type="link"
                      size="small"
                      :disabled="busy()"
                      @click="openGifPreview(record)"
                    >动图预览</a-button>
                  </a-space>
                </template>
              </a-table>
            </ConsoleTableShell>
          </ConsolePanel>
        </a-tab-pane>
      </a-tabs>

      <!-- GIF 播放控制器：只在动图记录上出现（后端目前只收 png/jpeg，故当前恒不出现）。 -->
      <a-modal
        v-if="gifPreview.visible"
        :open="gifPreview.visible"
        title="动图预览控制"
        :footer="null"
        :mask-closable="!gifPreview.playing"
        @cancel="closeGifPreview"
      >
        <div class="gif-controller" data-testid="gif-playback-controller">
          <p class="gif-meta">
            图片标识：<code class="hash-cell">{{ shortSha(gifPreview.imageSha256) }}</code>
          </p>
          <p class="gif-frame" data-testid="gif-frame-label">当前帧 {{ gifPreview.frameLabel }}</p>
          <p v-if="gifFrameHint" class="field-hint" data-testid="gif-frame-hint">{{ gifFrameHint }}</p>
          <a-space wrap class="gif-controls">
            <a-button size="small" data-action="gif-toggle" @click="toggleGifPlayback">
              {{ gifPreview.playing ? '暂停' : '播放' }}
            </a-button>
            <a-button size="small" data-action="gif-restart" @click="restartGifPlayback">重新开始</a-button>
            <a-button size="small" data-action="gif-prev" :disabled="gifPreview.playing" @click="stepGifFrame(-1)">上一帧</a-button>
            <a-button size="small" data-action="gif-next" :disabled="gifPreview.playing" @click="stepGifFrame(1)">下一帧</a-button>
          </a-space>
          <p class="field-hint">
            预览控制只管理播放状态与帧序号。图片字节由独立的管理端附件接口下发，检测接口的响应里不含字节与上游原文。
          </p>
        </div>
      </a-modal>
    </template>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, reactive, ref, useId } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import ConsoleTableShell from '@/components/ConsoleTableShell.vue'
import {
  getSensitiveImageEntries,
  getSensitiveImageQuota,
  manualDetectSensitiveImage,
  updateSensitiveImageQuota,
} from '@/api/SensitiveImageApi'
import {
  SENSITIVE_IMAGE_QUOTA_TAB_STATUS,
  SENSITIVE_IMAGE_QUOTA_TAB_STATUS_OPTIONS,
  SENSITIVE_IMAGE_TAB_QUOTA,
  SENSITIVE_IMAGE_TAB_RECORDS,
  SENSITIVE_IMAGE_TABS,
  buildDetectPayload,
  buildEntryQuery,
  buildGifControllerOptions,
  createGifPlaybackController,
  describeDetectOutcome,
  projectDetectResponse,
  readPositiveInt,
  shouldOfferGifControls,
  summarizeQuota,
} from './sensitiveImageHelpers.mjs'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

const STATUS_LABELS = { pending: '检测中', clean: '正常', sensitive: '敏感', error: '检测失败' }
const STATUS_COLORS = { pending: 'gold', clean: 'green', sensitive: 'red', error: 'default' }

const emptyQuotaDraft = () => ({ enabled: true, perTicketLimit: '', dailyUserLimit: '' })
const emptyDetectForm = () => ({ imageSha256: '', ticketId: '' })

export default {
  name: 'SensitiveImagePage',
  components: { ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell },
  setup() {
    const route = useRoute()
    const prefix = 'sensitive-image-' + useId()
    const fieldId = field => prefix + '-' + field
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    // 常量暴露给模板（避免模板里写字面量导致与 helper 定义漂移）。
    const QUOTA_TAB = SENSITIVE_IMAGE_TAB_QUOTA
    const tabs = SENSITIVE_IMAGE_TABS

    const activeTab = ref(SENSITIVE_IMAGE_TAB_RECORDS)
    const quotaStatusOptions = SENSITIVE_IMAGE_QUOTA_TAB_STATUS_OPTIONS
    const quotaStatus = ref(SENSITIVE_IMAGE_QUOTA_TAB_STATUS)

    // ── 两套独立分页（判据①） ────────────────────────────────────────
    const quotaPagination = reactive({
      current: 1, pageSize: 20, total: 0,
      showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'],
      showTotal: value => `共 ${value} 条`,
    })
    const recordPagination = reactive({
      current: 1, pageSize: 20, total: 0,
      showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'],
      showTotal: value => `共 ${value} 条`,
    })

    // 两套独立数据与加载态：切 Tab 不会互相覆盖。
    const quotaRows = ref([])
    const recordRows = ref([])
    const loadingQuotaRows = ref(false)
    const loadingRecordRows = ref(false)
    const hasLoadedQuotaRows = ref(false)
    const hasLoadedRecordRows = ref(false)
    const quotaRowsError = ref('')
    const recordRowsError = ref('')

    // quotaRaw 先于 quota 声明：computed 惰性求值虽然不会在定义时读它，
    // 但把依赖放在使用者之后会让阅读者以为写错了（TDZ 陷阱的潜在来源）。
    const quotaRaw = ref({})
    const quota = computed(() => summarizeQuota(quotaRaw.value))
    const loadingQuota = ref(false)
    const savingQuota = ref(false)
    const quotaError = ref('')
    const quotaSaveError = ref('')
    const quotaDraft = reactive(emptyQuotaDraft())

    const detectForm = reactive(emptyDetectForm())
    const detecting = ref(false)
    const detectError = ref('')
    const detectOutcome = ref(null)
    const detectOutcomeText = ref('')

    const gifPreview = reactive({
      visible: false, playing: true, frameLabel: '1 / 1', imageSha256: '',
    })
    let gifController = null
    let gifTimer = null

    let alive = true
    let quotaReadVersion = 0
    let quotaWriteVersion = 0
    let quotaRowsVersion = 0
    let recordRowsVersion = 0
    let detectVersion = 0
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()

    const validAdmin = () => {
      const user = currentUser.value
      const id = Number(user?.id)
      return Number.isSafeInteger(id) && id > 0 && id <= 2147483647 && isAdminUser(user)
        && !['disabled', '0'].includes(String(user?.status ?? 'enabled'))
    }
    const canManage = () => alive && route.path === mountedPath
      && getCurrentUserLogoutEpoch() === mountedLogoutEpoch && validAdmin()
    const busy = () => loadingQuota.value || savingQuota.value || detecting.value
      || loadingQuotaRows.value || loadingRecordRows.value

    // ── Tab 派生状态 ────────────────────────────────────────────────
    const currentRows = computed(() => (activeTab.value === QUOTA_TAB ? quotaRows.value : recordRows.value))
    const currentLoading = computed(() => (activeTab.value === QUOTA_TAB ? loadingQuotaRows.value : loadingRecordRows.value))
    const currentError = computed(() => (activeTab.value === QUOTA_TAB ? quotaRowsError.value : recordRowsError.value))
    const currentPagination = computed(() => (activeTab.value === QUOTA_TAB ? quotaPagination : recordPagination))
    const loadingCurrentTab = computed(() => currentLoading.value)
    const currentEmptyText = computed(() => {
      if (currentError.value) return '列表加载失败，请重试'
      const loaded = activeTab.value === QUOTA_TAB ? hasLoadedQuotaRows.value : hasLoadedRecordRows.value
      return loaded ? '暂无符合条件的检测记录' : '检测记录尚未读取成功'
    })

    const columns = [
      { title: '图片标识（SHA256）', key: 'imageSha256', dataIndex: 'imageSha256', width: 200 },
      { title: '结论', key: 'status', dataIndex: 'status', width: 120 },
      { title: '类型', key: 'contentType', dataIndex: 'contentType', width: 130 },
      { title: '大小', key: 'fileSize', dataIndex: 'fileSize', width: 110 },
      { title: '判定来源', key: 'provider', dataIndex: 'provider', width: 110 },
      { title: '原因', key: 'reason', dataIndex: 'reason', width: 240 },
      { title: '命中次数', key: 'hitCount', dataIndex: 'hitCount', width: 110 },
      { title: '更新时间', key: 'updatedAt', dataIndex: 'updatedAt', width: 180 },
      { title: '操作', key: 'action', width: 170 },
    ]

    // ── 格式化 ──────────────────────────────────────────────────────
    const shortSha = value => {
      const text = String(value ?? '')
      return text.length > 16 ? text.slice(0, 12) + '…' + text.slice(-4) : (text || '—')
    }
    const statusLabel = value => STATUS_LABELS[String(value ?? '').toLowerCase()] || (value || '—')
    const statusColor = value => STATUS_COLORS[String(value ?? '').toLowerCase()] || 'default'
    const formatBytes = value => {
      const size = Number(value)
      if (!Number.isFinite(size) || size <= 0) return '—'
      if (size < 1024) return `${size} B`
      if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KiB`
      return `${(size / (1024 * 1024)).toFixed(2)} MiB`
    }
    const formatDateTime = value => {
      if (!value) return '—'
      const date = new Date(value)
      if (!Number.isFinite(date.getTime())) return '—'
      return date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
    }

    // ── 额度读取与保存 ──────────────────────────────────────────────
    const applyQuotaDraft = source => {
      quotaDraft.enabled = source.enabled === true
      quotaDraft.perTicketLimit = source.perTicketLimit === null ? '' : String(source.perTicketLimit)
      quotaDraft.dailyUserLimit = source.dailyUserLimit === null ? '' : String(source.dailyUserLimit)
    }

    const loadQuota = async () => {
      if (!canManage() || loadingQuota.value) return false
      const ticket = ++quotaReadVersion
      const owns = () => canManage() && ticket === quotaReadVersion
      loadingQuota.value = true
      quotaError.value = ''
      try {
        const res = await getSensitiveImageQuota()
        if (!owns()) return false
        if (res?.status !== 200 || !res?.data || typeof res.data !== 'object') {
          quotaError.value = String(res?.message || '').trim() || '读取额度配置失败'
          return false
        }
        quotaRaw.value = res.data
        applyQuotaDraft(summarizeQuota(res.data))
        return true
      } catch (error) {
        if (owns()) quotaError.value = String(error?.message || '').trim() || '读取额度配置失败'
        return false
      } finally {
        if (owns()) loadingQuota.value = false
      }
    }

    const setQuotaDraft = (field, value) => {
      if (!canManage() || savingQuota.value || !Object.hasOwn(quotaDraft, field)) return
      quotaDraft[field] = value
    }

    const saveQuota = async () => {
      if (!canManage() || savingQuota.value) return
      // 只提交**真的改过**的键：后端用指针语义，未提交 = 保持现值。
      // 三个都提交会让「只改开关」顺带覆盖两个上限。
      const payload = {}
      if (quotaDraft.enabled !== (quota.value.enabled === true)) payload.enabled = quotaDraft.enabled === true
      const perTicket = readPositiveInt(quotaDraft.perTicketLimit)
      if (perTicket !== null && perTicket !== quota.value.perTicketLimit) payload.perTicketLimit = perTicket
      const dailyLimit = readPositiveInt(quotaDraft.dailyUserLimit)
      if (dailyLimit !== null && dailyLimit !== quota.value.dailyUserLimit) payload.dailyUserLimit = dailyLimit
      if (!Object.keys(payload).length) {
        quotaSaveError.value = '没有需要保存的改动（上限必须是正整数）'
        return
      }
      const ticket = ++quotaWriteVersion
      const owns = () => canManage() && ticket === quotaWriteVersion
      savingQuota.value = true
      quotaSaveError.value = ''
      try {
        const res = await updateSensitiveImageQuota(payload)
        if (!owns()) return
        if (res?.status !== 200) {
          quotaSaveError.value = String(res?.data?.message || res?.message || '').trim() || '保存额度配置失败'
          return
        }
        // 以后端回传为准：避免「本地改了但没落库」显示成已生效。
        quotaRaw.value = res.data && typeof res.data === 'object' ? res.data : quotaRaw.value
        applyQuotaDraft(summarizeQuota(quotaRaw.value))
        message.success('额度配置已保存')
      } catch (error) {
        if (owns()) quotaSaveError.value = String(error?.message || '').trim() || '保存额度配置失败'
      } finally {
        if (owns()) savingQuota.value = false
      }
    }

    // ── 列表读取（两个 Tab 各走自己的版本号与分页） ─────────────────
    const loadQuotaRows = async () => {
      if (!canManage()) return false
      const ticket = ++quotaRowsVersion
      const owns = () => canManage() && ticket === quotaRowsVersion
      loadingQuotaRows.value = true
      quotaRowsError.value = ''
      try {
        const params = buildEntryQuery(QUOTA_TAB, {
          page: quotaPagination.current,
          pageSize: quotaPagination.pageSize,
          status: quotaStatus.value,
        })
        const res = await getSensitiveImageEntries(params)
        if (!owns()) return false
        const body = res?.data
        if (res?.status !== 200 || !body || !Array.isArray(body.data)) {
          quotaRowsError.value = String(res?.message || '').trim() || '读取超额未检测记录失败'
          return false
        }
        quotaRows.value = body.data.filter(item => item && typeof item === 'object')
        const total = Number(body.total)
        quotaPagination.total = Number.isSafeInteger(total) && total >= 0 ? total : quotaRows.value.length
        hasLoadedQuotaRows.value = true
        return true
      } catch (error) {
        if (owns()) quotaRowsError.value = String(error?.message || '').trim() || '读取超额未检测记录失败'
        return false
      } finally {
        if (owns()) loadingQuotaRows.value = false
      }
    }

    const loadRecordRows = async () => {
      if (!canManage()) return false
      const ticket = ++recordRowsVersion
      const owns = () => canManage() && ticket === recordRowsVersion
      loadingRecordRows.value = true
      recordRowsError.value = ''
      try {
        const params = buildEntryQuery(SENSITIVE_IMAGE_TAB_RECORDS, {
          page: recordPagination.current,
          pageSize: recordPagination.pageSize,
        })
        const res = await getSensitiveImageEntries(params)
        if (!owns()) return false
        const body = res?.data
        if (res?.status !== 200 || !body || !Array.isArray(body.data)) {
          recordRowsError.value = String(res?.message || '').trim() || '读取检测记录失败'
          return false
        }
        recordRows.value = body.data.filter(item => item && typeof item === 'object')
        const total = Number(body.total)
        recordPagination.total = Number.isSafeInteger(total) && total >= 0 ? total : recordRows.value.length
        hasLoadedRecordRows.value = true
        return true
      } catch (error) {
        if (owns()) recordRowsError.value = String(error?.message || '').trim() || '读取检测记录失败'
        return false
      } finally {
        if (owns()) loadingRecordRows.value = false
      }
    }

    const reloadCurrentTab = () => {
      if (!canManage()) return
      return activeTab.value === QUOTA_TAB ? loadQuotaRows() : loadRecordRows()
    }

    const setQuotaStatus = value => {
      if (!canManage() || loadingQuotaRows.value) return
      quotaStatus.value = String(value || SENSITIVE_IMAGE_QUOTA_TAB_STATUS)
      quotaPagination.current = 1
    }

    // onCurrentTableChange 只动**当前 Tab** 的分页对象。
    // 这是判据①的核心：另一个 Tab 的 current/pageSize 一字不改。
    const onCurrentTableChange = page => {
      if (!canManage() || currentLoading.value) return
      const target = activeTab.value === QUOTA_TAB ? quotaPagination : recordPagination
      const nextSize = Number(page?.pageSize) || target.pageSize
      target.current = nextSize === target.pageSize ? Number(page?.current) || 1 : 1
      target.pageSize = nextSize
      return activeTab.value === QUOTA_TAB ? loadQuotaRows() : loadRecordRows()
    }

    // ── 手动触发 ────────────────────────────────────────────────────
    const setDetectField = (field, value) => {
      if (!canManage() || detecting.value || !Object.hasOwn(detectForm, field)) return
      detectForm[field] = value
    }

    const runDetect = async () => {
      if (!canManage() || detecting.value) return
      const built = buildDetectPayload({ imageSha256: detectForm.imageSha256, ticketId: detectForm.ticketId })
      if (built.error) {
        detectError.value = built.error
        return
      }
      const ticket = ++detectVersion
      const owns = () => canManage() && ticket === detectVersion
      detecting.value = true
      detectError.value = ''
      detectOutcome.value = null
      detectOutcomeText.value = ''
      let reloadAfter = false
      try {
        const res = await manualDetectSensitiveImage(built.payload)
        if (!owns()) return
        if (res?.status !== 200) {
          detectError.value = String(res?.data?.message || res?.message || '').trim() || '手动触发检测失败'
          return
        }
        // 判据③：只投影白名单字段，字节与上游原文一律不进页面状态。
        const view = projectDetectResponse(res.data)
        detectOutcome.value = view
        detectOutcomeText.value = describeDetectOutcome(view)
        if (built.ignoredTicketId) {
          message.info('同时填了图片标识与工单编号，本次只按图片标识重判；工单编号已忽略')
        } else {
          message.success('手动检测完成')
        }
        reloadAfter = true
      } catch (error) {
        if (owns()) detectError.value = String(error?.message || '').trim() || '手动触发检测失败'
      } finally {
        if (owns()) detecting.value = false
      }
      // 列表读取会拒绝在 busy() 期间执行，所以必须等 detecting 释放后再刷新。
      if (reloadAfter && owns()) await reloadCurrentTab()
    }

    const detectRecord = record => {
      if (!canManage() || detecting.value) return
      const sha = String(record?.imageSha256 || '').trim()
      if (!sha) return
      detectForm.imageSha256 = sha
      detectForm.ticketId = ''
      return runDetect()
    }

    // ── GIF 播放控制 ────────────────────────────────────────────────
    //
    // 帧数与帧时长由 buildGifControllerOptions 从记录里读（纯函数，可测）。
    // **不在这里解码图片字节**：检测接口的响应里没有字节（判据③），
    // 解码必须由独立的管理端附件接口提供，本页只管理播放状态。
    //
    // `<img>` 不暴露 GIF 播放进度，所以控制器按固定帧时长自己推进帧序号。
    const gifFrameHint = ref('')
    const ensureGifTimer = () => {
      if (gifTimer !== null) return
      gifTimer = setInterval(() => {
        if (!gifController) return
        gifController.tick(100)
        gifPreview.playing = gifController.playing
        gifPreview.frameLabel = gifController.frameLabel
      }, 100)
    }
    const stopGifTimer = () => {
      if (gifTimer === null) return
      clearInterval(gifTimer)
      gifTimer = null
    }

    const openGifPreview = record => {
      if (!canManage() || !shouldOfferGifControls(record?.contentType)) return
      const options = buildGifControllerOptions(record)
      gifController = createGifPlaybackController({
        frameCount: options.frameCount,
        frameDurationMs: options.frameDurationMs,
        playing: options.playing,
      })
      // 帧数未知时如实说明：显示「1 / 1」而不解释会让人以为动图只有一帧。
      gifFrameHint.value = options.frameCountKnown
        ? ''
        : '该记录的帧数未知（检测接口不下发图片字节与帧信息），控制条按单帧工作。'
      gifPreview.visible = true
      gifPreview.imageSha256 = String(record?.imageSha256 || '')
      gifPreview.playing = gifController.playing
      gifPreview.frameLabel = gifController.frameLabel
      ensureGifTimer()
    }
    const closeGifPreview = () => {
      gifPreview.visible = false
      gifPreview.imageSha256 = ''
      gifFrameHint.value = ''
      gifController = null
      stopGifTimer()
    }
    const syncGifState = () => {
      if (!gifController) return
      gifPreview.playing = gifController.playing
      gifPreview.frameLabel = gifController.frameLabel
    }
    const toggleGifPlayback = () => {
      if (!gifController) return
      gifController.toggle()
      syncGifState()
    }
    const restartGifPlayback = () => {
      if (!gifController) return
      gifController.restart()
      syncGifState()
    }
    const stepGifFrame = step => {
      if (!gifController) return
      gifController.selectFrame(gifController.frameIndex + Number(step || 0))
      syncGifState()
    }

    onMounted(() => {
      if (!canManage()) return
      void loadQuota()
      void loadRecordRows()
      void loadQuotaRows()
    })
    onBeforeUnmount(() => {
      alive = false
      stopGifTimer()
      gifController = null
    })

    return {
      fieldId, canManage, busy, tabs, QUOTA_TAB,
      activeTab, quotaStatus, quotaStatusOptions, setQuotaStatus,
      quotaPagination, recordPagination,
      currentRows, currentLoading, currentError, currentPagination,
      currentEmptyText, loadingCurrentTab,
      columns,
      quota, loadingQuota, savingQuota, quotaError, quotaSaveError, quotaDraft,
      setQuotaDraft, saveQuota, loadQuota,
      detectForm, detecting, detectError, detectOutcome, detectOutcomeText,
      setDetectField, runDetect, detectRecord,
      gifPreview, gifFrameHint, openGifPreview, closeGifPreview,
      toggleGifPlayback, restartGifPlayback, stepGifFrame,
      shouldOfferGifControls,
      shortSha, statusLabel, statusColor, formatBytes, formatDateTime,
      reloadCurrentTab, onCurrentTableChange,
      sumTableColumnWidths,
    }
  },
}
</script>

<style scoped>
.page-alert, .status-alert { margin-bottom: 16px; }
.sensitive-image-summary { margin-bottom: 16px; }
.summary-label { color: var(--console-muted); font-size: 14px; }
.summary-value { margin-top: 8px; color: var(--console-text); font-size: clamp(18px, 2.2vw, 24px); font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.summary-hint { margin-left: 8px; color: var(--console-muted); font-size: 12px; font-weight: 400; }
.quota-form, .detect-form, .filter-form { margin-bottom: 12px; }
.quota-input { width: 130px; }
.detect-sha { width: 320px; }
.detect-ticket { width: 180px; }
.filter-status { width: 190px; }
.filter-error { margin: 0 0 16px; color: var(--console-danger, #c13e41); font-size: 13px; }
.field-hint { margin: 6px 0 0; color: var(--console-muted); font-size: 12px; line-height: 1.7; }
.emphasis { color: var(--console-text); font-weight: 600; }
.hash-cell { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-variant-numeric: tabular-nums; }
.reason-cell { overflow-wrap: anywhere; }
.count-cell { font-variant-numeric: tabular-nums; }
.detect-outcome { margin-top: 12px; }
.detect-outcome-detail p { margin: 0 0 6px; font-size: 13px; line-height: 1.7; }
.detail-hint { color: var(--console-muted); font-size: 12px; }
.gif-controller { display: flex; flex-direction: column; gap: 10px; }
.gif-meta, .gif-frame { margin: 0; font-size: 13px; }
.gif-frame { font-variant-numeric: tabular-nums; font-size: 16px; font-weight: 600; }
.sensitive-image-tabs { margin-top: 8px; }
@media (max-width: 640px) {
  .quota-form :deep(.ant-form-item), .detect-form :deep(.ant-form-item) { width: 100%; margin-inline-end: 0; }
  .quota-input, .detect-sha, .detect-ticket, .filter-status { width: 100%; }
}
</style>
