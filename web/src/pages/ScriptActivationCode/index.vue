<template>
  <div class="console-page script-activation-code-page">
    <ConsolePageHeading
      title="脚本一次性激活码"
      description="管理员预生成的一次性脚本激活码。列表只显示后 4 位；详情才显示全码并附完整使用记录（兑换 / 下载 / 运行），便于客服处理工单。"
    >
      <template #actions>
        <a-button :loading="loading" :disabled="!canManage() || loading || writing" @click="loadCodes">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert
      v-if="!canManage()"
      type="warning"
      show-icon
      class="page-alert"
      message="当前身份无脚本激活码管理权限，请使用管理员账号登录。"
    />

    <template v-if="canManage()">
      <ConsolePanel title="生成激活码" description="单次最多 100 个。生成的码只在生成结果里显示一次全码。">
        <a-form layout="vertical" :onFinish="submitCreate">
          <a-row :gutter="16">
            <a-col :xs="24" :md="6">
              <a-form-item label="生成数量">
                <a-input-number
                  :value="createForm.count"
                  :min="1"
                  :max="batchLimit"
                  :precision="0"
                  style="width: 100%"
                  @update:value="value => (createForm.count = value)"
                />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="10">
              <a-form-item label="备注 / 用途（客服记录工单号）">
                <a-input
                  :value="createForm.remark"
                  :maxlength="255"
                  placeholder="例如：工单 2024-0413 补偿"
                  @update:value="value => (createForm.remark = value)"
                />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="8">
              <a-form-item label="有效期（留空 = 永不过期）">
                <a-input
                  :value="createForm.expiresAt"
                  placeholder="YYYY-MM-DD HH:mm:ss（北京时间）"
                  @update:value="value => (createForm.expiresAt = value)"
                />
              </a-form-item>
            </a-col>
          </a-row>
          <a-space wrap>
            <a-button type="primary" html-type="submit" :loading="writing" :disabled="writing">生成</a-button>
            <span class="hint">上限 {{ batchLimit }} 个 / 次；超过会被拒绝（不会静默截断）。</span>
          </a-space>
        </a-form>

        <a-alert v-if="createError" type="error" show-icon :message="createError" class="page-alert" />

        <div v-if="createdCodes.length > 0" class="created-block">
          <a-alert type="success" show-icon :message="`已生成 ${createdCodes.length} 个激活码，请立即复制保存`" />
          <a-textarea
            :value="createdCodes.map(item => item.code).join('\n')"
            :rows="Math.min(createdCodes.length, 8)"
            readonly
            class="created-codes"
          />
        </div>
      </ConsolePanel>

      <ConsolePanel title="激活码列表" description="只显示后 4 位；点击「详情」查看全码与使用记录。">
        <a-space wrap class="filter-bar">
          <a-select
            :value="filters.status"
            style="width: 160px"
            :options="statusOptions"
            @update:value="value => { filters.status = value; loadCodes() }"
          />
          <a-input
            :value="filters.usedBy"
            style="width: 180px"
            placeholder="按使用者 ID 筛选"
            @update:value="value => (filters.usedBy = value)"
            @press-enter="loadCodes"
          />
          <a-button :loading="loading" @click="loadCodes">查询</a-button>
        </a-space>

        <a-alert v-if="loadError" type="error" show-icon :message="loadError" class="page-alert" />

        <a-table
          :columns="columns"
          :data-source="codes"
          :loading="loading"
          :pagination="pagination"
          size="small"
          row-key="id"
          :scroll="{ x: 'max-content' }"
          @change="handleTableChange"
        >
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'codeMask'">
              <code class="code-mask">{{ record.codeMask }}</code>
              <span v-if="masksCode(record)" class="hint">（仅后 4 位）</span>
            </template>
            <template v-else-if="column.key === 'status'">
              <a-tag :color="statusMeta(record.status).color">{{ statusMeta(record.status).label }}</a-tag>
            </template>
            <template v-else-if="column.key === 'usedBy'">
              <span v-if="record.usedBy">{{ record.usedByUsername || '—' }} <span class="hint">#{{ record.usedBy }}</span></span>
              <span v-else>—</span>
            </template>
            <template v-else-if="column.key === 'actions'">
              <a-space>
                <a-button size="small" @click="openDetail(record)">详情</a-button>
                <a-button
                  size="small"
                  :disabled="record.status === 'used'"
                  @click="toggleDisabled(record)"
                >{{ record.status === 'disabled' ? '启用' : '禁用' }}</a-button>
              </a-space>
            </template>
          </template>
        </a-table>
      </ConsolePanel>
    </template>

    <a-drawer
      :open="detailOpen"
      :width="720"
      title="激活码详情"
      placement="right"
      @close="closeDetail"
    >
      <a-spin :spinning="detailLoading">
        <a-alert v-if="detailError" type="error" show-icon :message="detailError" class="page-alert" />

        <template v-if="detail">
          <a-alert
            :type="summary.used ? 'info' : 'success'"
            show-icon
            :message="summary.text"
            class="page-alert"
          />

          <a-descriptions :column="1" size="small" bordered class="detail-descriptions">
            <a-descriptions-item label="激活码">
              <code class="code-full">{{ detail.code.code }}</code>
            </a-descriptions-item>
            <a-descriptions-item label="状态">
              <a-tag :color="statusMeta(detail.code.status).color">{{ statusMeta(detail.code.status).label }}</a-tag>
            </a-descriptions-item>
            <a-descriptions-item label="备注">{{ detail.code.remark || '—' }}</a-descriptions-item>
            <a-descriptions-item label="有效期">{{ formatTime(detail.code.expiresAt) || '永不过期' }}</a-descriptions-item>
            <a-descriptions-item label="创建时间">{{ formatTime(detail.code.createdAt) || '—' }}</a-descriptions-item>
          </a-descriptions>

          <h3 class="section-title">使用记录</h3>
          <a-empty v-if="timeline.length === 0" description="该激活码尚未被使用" />
          <a-timeline v-else>
            <a-timeline-item v-for="(item, index) in timeline" :key="index">
              <div class="timeline-title">
                <strong>{{ item.label }}</strong>
                <span class="hint"> {{ formatTime(item.at) }}</span>
              </div>
              <div class="hint">
                <span v-if="item.username">用户：{{ item.username }}</span>
                <span v-if="item.scriptName"> · 脚本：{{ item.scriptName }}</span>
                <span v-if="item.detail"> · {{ item.detail }}</span>
              </div>
            </a-timeline-item>
          </a-timeline>
        </template>
      </a-spin>
    </a-drawer>
  </div>
</template>

<script>
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import { isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import {
  createScriptActivationCodes,
  getScriptActivationCodes,
  getScriptActivationCodeDetail,
  updateScriptActivationCode,
} from '@/api/ScriptActivationCodeApi'
import {
  BATCH_LIMIT,
  statusMeta,
  normalizeBatchCount,
  masksCode,
  buildUsageTimeline,
  summarizeUsage,
  parseExpiryInput,
} from '@/utils/scriptActivationCodes.mjs'

export default {
  name: 'ScriptActivationCodePage',
  components: { ConsolePageHeading, ConsolePanel },
  setup() {
    const route = useRoute()
    const { currentUser } = useCurrentUserDisplay()
    const mountedPath = route.path

    // 与其它管理页一致：路由、角色、状态任一变化都不再允许操作。
    const canManage = () => route.path === mountedPath && isAdminUser(currentUser.value)
      && !['disabled', '0'].includes(String(currentUser.value?.status ?? 'enabled'))

    const batchLimit = BATCH_LIMIT
    const loading = ref(false)
    const writing = ref(false)
    const loadError = ref('')
    const createError = ref('')
    const codes = ref([])
    const total = ref(0)
    const createdCodes = ref([])

    const createForm = reactive({ count: 1, remark: '', expiresAt: null })
    const filters = reactive({ status: '', usedBy: '', page: 1, pageSize: 20 })

    const detailOpen = ref(false)
    const detailLoading = ref(false)
    const detailError = ref('')
    const detail = ref(null)
    const summary = computed(() => summarizeUsage(detail.value))
    const timeline = computed(() => buildUsageTimeline(detail.value?.usages))

    const statusOptions = [
      { label: '全部状态', value: '' },
      { label: '未使用', value: 'unused' },
      { label: '已使用', value: 'used' },
      { label: '已禁用', value: 'disabled' },
      { label: '已过期', value: 'expired' },
    ]

    const columns = [
      { title: '激活码', key: 'codeMask', dataIndex: 'codeMask' },
      { title: '状态', key: 'status', dataIndex: 'status' },
      { title: '备注', key: 'remark', dataIndex: 'remark', ellipsis: true },
      { title: '使用者', key: 'usedBy', dataIndex: 'usedBy' },
      { title: '使用时间', key: 'usedAt', dataIndex: 'usedAt' },
      { title: '有效期至', key: 'expiresAt', dataIndex: 'expiresAt' },
      { title: '操作', key: 'actions' },
    ]

    const pagination = computed(() => ({
      current: filters.page,
      pageSize: filters.pageSize,
      total: total.value,
      showSizeChanger: false,
    }))

    // 时间一律用原生 Date + toLocaleString 按上海时区渲染。
    //
    // 不用 dayjs / a-date-picker：本仓没有这两样依赖（见 TikuComponent 里
    // 「本仓没有 dayjs/DatePicker 那套依赖」的注释），引入会改 package.json
    // 并让 CI 的 npm ci 要求 lock 同步。
    const formatTime = value => {
      if (value === null || value === undefined || String(value).trim() === '') return ''
      const date = new Date(value)
      if (!Number.isFinite(date.getTime())) return ''
      return date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
    }

    const loadCodes = async () => {
      if (!canManage()) return
      loading.value = true
      loadError.value = ''
      try {
        const response = await getScriptActivationCodes({
          page: filters.page,
          pageSize: filters.pageSize,
          status: filters.status,
          usedBy: Number(filters.usedBy) || 0,
        })
        if (response?.status === 200) {
          codes.value = response.data?.items || []
          total.value = Number(response.data?.total) || 0
          // 列表项的时间统一格式化，避免表格里出现 ISO 原串。
          codes.value = codes.value.map(item => ({
            ...item,
            usedAt: formatTime(item.usedAt),
            expiresAt: item.expiresAt ? formatTime(item.expiresAt) : '',
          }))
        } else if (response?.status === 403) {
          loadError.value = '当前账号没有该权限'
        } else {
          loadError.value = response?.data?.message || '读取激活码列表失败'
        }
      } catch {
        loadError.value = '读取激活码列表失败'
      } finally {
        loading.value = false
      }
    }

    const submitCreate = async () => {
      if (!canManage() || writing.value) return
      createError.value = ''
      const checked = normalizeBatchCount(createForm.count)
      if (!checked.ok) {
        createError.value = checked.message
        return
      }
      // 有效期是手填字符串，必须在提交前解析：解析不了就当场报错，
      // 而不是把非法串发给后端再让它 500。
      const expiry = parseExpiryInput(createForm.expiresAt)
      if (!expiry.ok) {
        createError.value = expiry.message
        return
      }
      writing.value = true
      try {
        const response = await createScriptActivationCodes({
          count: checked.count,
          remark: createForm.remark,
          expiresAt: expiry.value,
        })
        if (response?.status === 200) {
          createdCodes.value = response.data?.items || []
          message.success(`已生成 ${createdCodes.value.length} 个激活码`)
          createForm.remark = ''
          await loadCodes()
        } else {
          createError.value = response?.data?.message || '生成失败'
        }
      } catch {
        createError.value = '生成失败'
      } finally {
        writing.value = false
      }
    }

    const openDetail = async record => {
      if (!canManage()) return
      detailOpen.value = true
      detailLoading.value = true
      detailError.value = ''
      detail.value = null
      try {
        const response = await getScriptActivationCodeDetail(record.id)
        if (response?.status === 200) {
          detail.value = response.data
        } else {
          detailError.value = response?.data?.message || '读取详情失败'
        }
      } catch {
        detailError.value = '读取详情失败'
      } finally {
        detailLoading.value = false
      }
    }

    const closeDetail = () => {
      detailOpen.value = false
      detail.value = null
      detailError.value = ''
    }

    const toggleDisabled = async record => {
      if (!canManage() || writing.value) return
      writing.value = true
      try {
        const nextStatus = record.status === 'disabled' ? 'unused' : 'disabled'
        const response = await updateScriptActivationCode(record.id, { status: nextStatus })
        if (response?.status === 200) {
          message.success(nextStatus === 'disabled' ? '已禁用' : '已启用')
          await loadCodes()
        } else {
          message.error(response?.data?.message || '操作失败')
        }
      } catch {
        message.error('操作失败')
      } finally {
        writing.value = false
      }
    }

    const handleTableChange = page => {
      filters.page = page?.current || 1
      loadCodes()
    }

    onMounted(() => { loadCodes() })

    return {
      canManage, batchLimit, loading, writing, loadError, createError,
      codes, total, createdCodes, createForm, filters, statusOptions, columns,
      pagination, detailOpen, detailLoading, detailError, detail, summary, timeline,
      loadCodes, submitCreate, openDetail, closeDetail, toggleDisabled,
      handleTableChange, formatTime, statusMeta, masksCode,
    }
  },
}
</script>

<style scoped>
.script-activation-code-page .page-alert { margin-bottom: 16px; }
.filter-bar { margin-bottom: 16px; }
.created-block { margin-top: 16px; }
.created-codes { margin-top: 8px; font-family: monospace; }
.code-mask, .code-full { font-family: monospace; }
.code-full { font-size: 14px; word-break: break-all; }
.detail-descriptions { margin-bottom: 16px; }
.section-title { font-size: 14px; font-weight: 600; margin: 16px 0 8px; }
.timeline-title { display: flex; align-items: baseline; }
.hint { color: rgba(0, 0, 0, 0.45); font-size: 12px; }
</style>
