<template>
  <div class="console-page user-level-page">
    <ConsolePageHeading
      title="等级与充值阶梯"
      description="等级按「累计充值 + 累计消费」两个门槛判定，达标即取最高级（允许跳级，级数无上限）。退款会让累计充值回落并自动降级。充值阶梯决定充多少送多少；档位表为空时使用系统默认六档。"
    >
      <template #actions>
        <a-button :loading="loading" :disabled="!canManage() || busy()" @click="load">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert
      v-if="!canManage()"
      type="warning"
      show-icon
      class="page-alert"
      message="当前身份无等级管理权限，请使用管理员账号登录。"
    />

    <template v-if="canManage()">
      <a-alert
        v-if="actionWarning"
        type="error"
        show-icon
        closable
        class="page-alert"
        :message="actionWarning"
        @close="actionWarning = ''"
      />
      <a-alert
        v-if="actionNotice"
        type="success"
        show-icon
        closable
        class="page-alert"
        :message="actionNotice"
        @close="actionNotice = ''"
      />

      <!-- ───────────── 等级定义 ───────────── -->
      <ConsolePanel title="等级定义" description="排序号决定高低（必须唯一；0 保留给「无等级」）。两个门槛同时满足才算达到该级。设备上限是本轮的契约占位，暂不参与任何判定。">
        <template #actions>
          <a-button type="primary" :disabled="busy()" @click="openCreate">新增等级</a-button>
          <a-button :loading="reevaluating" :disabled="busy()" @click="runReevaluate">重评一批用户</a-button>
        </template>

        <a-table
          :columns="levelColumns"
          :data-source="levels"
          :pagination="false"
          :loading="loading"
          row-key="id"
          size="middle"
          :scroll="{ x: 900 }"
        >
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'rank'">{{ record.rank }}</template>
            <template v-else-if="column.key === 'recharge'">{{ formatMoney(record.minRechargeTotal) }}</template>
            <template v-else-if="column.key === 'consumption'">{{ formatMoney(record.minConsumptionTotal) }}</template>
            <template v-else-if="column.key === 'deviceLimit'">
              {{ record.deviceLimit > 0 ? record.deviceLimit + ' 台' : '不限制（未启用）' }}
            </template>
            <template v-else-if="column.key === 'enabled'">
              <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
            </template>
            <template v-else-if="column.key === 'actions'">
              <a-space>
                <a-button size="small" :disabled="busy()" @click="openEdit(record)">编辑</a-button>
                <a-popconfirm
                  title="删除后，仍指向该等级的用户会回落为「无等级」，并留一条变更记录。确定删除？"
                  ok-text="删除"
                  cancel-text="取消"
                  :disabled="busy()"
                  @confirm="removeLevel(record)"
                >
                  <a-button size="small" danger :disabled="busy()">删除</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </template>
        </a-table>
        <a-empty v-if="!loading && levels.length === 0" description="还没有等级。新增一个「基础会员」（排序号 1、两个门槛都填 0）作为兜底级。" />
      </ConsolePanel>

      <!-- ───────────── 充值阶梯 ───────────── -->
      <ConsolePanel title="充值阶梯赠送档位" description="取不高于本笔实付金额的最高门槛，整笔按该档比例赠送。同一门槛不允许配两个不同比例（那会让选档结果不确定）。清空并保存 = 回退到系统默认六档。">
        <template #actions>
          <a-button :disabled="busy()" @click="addTierRow">加一档</a-button>
          <a-button type="primary" :loading="savingTiers" :disabled="busy()" @click="saveTiers">保存档位</a-button>
        </template>

        <a-alert
          v-if="tierSource === 'default'"
          type="info"
          show-icon
          class="tier-source-alert"
          message="当前档位表为空，正在使用系统默认六档（8.8→10%、28→25%、88→50%、288→100%、888→150%、1688→200%）。保存任意档位后将以表为准。"
        />

        <a-table
          :columns="tierColumns"
          :data-source="tierRows"
          :pagination="false"
          row-key="uid"
          size="middle"
          :scroll="{ x: 760 }"
        >
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'threshold'">
              <a-input-number
                :value="record.threshold"
                :min="0"
                :step="0.1"
                :precision="2"
                :disabled="busy()"
                addon-before="¥"
                @update:value="value => updateTierRow(record, 'threshold', value)"
              />
            </template>
            <template v-else-if="column.key === 'rate'">
              <a-input-number
                :value="record.ratePercent"
                :min="0"
                :step="1"
                :precision="2"
                :disabled="busy()"
                addon-after="%"
                @update:value="value => updateTierRow(record, 'ratePercent', value)"
              />
            </template>
            <template v-else-if="column.key === 'enabled'">
              <a-switch
                :checked="record.enabled"
                :disabled="busy()"
                checked-children="启用"
                un-checked-children="停用"
                @update:checked="value => updateTierRow(record, 'enabled', value)"
              />
            </template>
            <template v-else-if="column.key === 'preview'">
              <span class="tier-preview">{{ previewText(record) }}</span>
            </template>
            <template v-else-if="column.key === 'actions'">
              <a-button size="small" danger :disabled="busy()" @click="removeTierRow(record)">移除</a-button>
            </template>
          </template>
        </a-table>
        <a-empty v-if="tierRows.length === 0" description="没有配置任何档位（保存后回退到系统默认六档，等于不限制）。" />

        <div class="tier-preview-box">
          <a-form layout="inline" :onFinish="runTierPreview">
            <a-form-item label="试算充值金额" :html-for="fieldId('preview-amount')">
              <a-input-number
                :id="fieldId('preview-amount')"
                :value="previewAmount"
                :min="0"
                :step="1"
                :precision="2"
                :disabled="busy()"
                @update:value="value => (previewAmount = value)"
              />
            </a-form-item>
            <a-form-item>
              <a-button html-type="submit" :loading="previewLoading" :disabled="busy()">按当前未保存的档位试算</a-button>
            </a-form-item>
          </a-form>
          <p v-if="previewError" class="filter-error" role="alert">{{ previewError }}</p>
          <p v-if="previewResult" class="preview-result">
            充 ¥{{ formatMoney(previewAmount) }}：{{ previewResult.matched
              ? `命中门槛 ¥${formatMoney(previewResult.threshold)}（${formatPercent(previewResult.rate)}），送 ¥${formatMoney(previewResult.gift)}`
              : '低于最低门槛，不参与赠送' }}
          </p>
        </div>
      </ConsolePanel>
    </template>

    <!-- ───────────── 等级编辑弹窗 ───────────── -->
    <a-modal
      :open="editor.open"
      :title="editor.id ? '编辑等级' : '新增等级'"
      :confirm-loading="editor.saving"
      ok-text="保存"
      cancel-text="取消"
      :width="560"
      @ok="submitEditor"
      @cancel="closeEditor"
    >
      <a-form layout="vertical" class="level-form">
        <a-form-item label="等级名称" :html-for="fieldId('level-name')" required>
          <a-input :id="fieldId('level-name')" :value="editor.name" :maxlength="64" @update:value="value => (editor.name = value)" />
        </a-form-item>
        <a-form-item label="排序号（越大越高，必须唯一）" :html-for="fieldId('level-rank')" required>
          <a-input-number :id="fieldId('level-rank')" :value="editor.rank" :min="1" :precision="0" @update:value="value => (editor.rank = value)" />
        </a-form-item>
        <a-form-item label="累计充值门槛（元）" :html-for="fieldId('level-recharge')">
          <a-input-number :id="fieldId('level-recharge')" :value="editor.minRechargeTotal" :min="0" :precision="2" @update:value="value => (editor.minRechargeTotal = value)" />
        </a-form-item>
        <a-form-item label="累计消费门槛（元）" :html-for="fieldId('level-consumption')">
          <a-input-number :id="fieldId('level-consumption')" :value="editor.minConsumptionTotal" :min="0" :precision="2" @update:value="value => (editor.minConsumptionTotal = value)" />
        </a-form-item>
        <a-form-item label="设备上限（契约占位，当前不参与判定）" :html-for="fieldId('level-device')">
          <a-input-number :id="fieldId('level-device')" :value="editor.deviceLimit" :min="0" :precision="0" @update:value="value => (editor.deviceLimit = value)" />
        </a-form-item>
        <a-form-item label="备注">
          <a-input :value="editor.description" :maxlength="255" @update:value="value => (editor.description = value)" />
        </a-form-item>
        <a-form-item label="启用">
          <a-switch :checked="editor.enabled" checked-children="启用" un-checked-children="停用" @update:checked="value => (editor.enabled = value)" />
        </a-form-item>
        <p v-if="editor.error" class="filter-error" role="alert">{{ editor.error }}</p>
      </a-form>
    </a-modal>
  </div>
</template>

<script>
import { reactive, ref, shallowRef, useId } from 'vue'
import { useRoute } from 'vue-router'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import { getCurrentUserLogoutEpoch, isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser'
import {
  createUserLevel,
  deleteUserLevel,
  getRechargePromotionTiers,
  getUserLevels,
  previewRechargePromotionTier,
  reevaluateUserLevels,
  replaceRechargePromotionTiers,
  updateUserLevel,
} from '@/api/UserLevelApi'

// 金额展示：后端是 6 位小数口径，这里只展示 2 位（充值/消费都是钱，不需要 6 位尾数）。
function formatMoney(value) {
  const number = Number(value)
  if (!Number.isFinite(number)) return '0.00'
  return number.toFixed(2)
}

function formatPercent(rate) {
  const number = Number(rate)
  if (!Number.isFinite(number)) return '0.00%'
  return (number * 100).toFixed(2) + '%'
}

let tierRowSeed = 0

export default {
  name: 'UserLevelPage',
  components: { ConsolePageHeading, ConsolePanel },
  setup() {
    const route = useRoute()
    const prefix = 'user-level-' + useId()
    const fieldId = field => prefix + '-' + field
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const loading = ref(false)
    const mutating = ref(false)
    const reevaluating = ref(false)
    const savingTiers = ref(false)
    const hasLoaded = ref(false)
    const actionWarning = ref('')
    const actionNotice = ref('')

    const levels = ref([])
    const tierRows = ref([])
    const tierSource = ref('default')

    const previewAmount = ref(100)
    const previewLoading = ref(false)
    const previewError = ref('')
    const previewResult = shallowRef(null)

    const editor = reactive({
      open: false, id: 0, saving: false, error: '',
      name: '', rank: 1, minRechargeTotal: 0, minConsumptionTotal: 0,
      deviceLimit: 0, description: '', enabled: true,
    })

    let alive = true
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()

    const validAdmin = () => {
      const user = currentUser.value
      const id = Number(user?.id)
      return Number.isSafeInteger(id) && id > 0 && id <= 2147483647 && isAdminUser(user)
        && !['disabled', '0'].includes(String(user?.status ?? 'enabled'))
    }
    const canManage = () => alive && route.path === mountedPath
      && getCurrentUserLogoutEpoch() === mountedLogoutEpoch && validAdmin()
    const busy = () => loading.value || mutating.value

    const levelColumns = [
      { title: '名称', dataIndex: 'name', key: 'name' },
      { title: '排序号', key: 'rank', width: 90 },
      { title: '累计充值门槛', key: 'recharge', width: 140 },
      { title: '累计消费门槛', key: 'consumption', width: 140 },
      { title: '设备上限', key: 'deviceLimit', width: 150 },
      { title: '状态', key: 'enabled', width: 90 },
      { title: '备注', dataIndex: 'description', key: 'description' },
      { title: '操作', key: 'actions', width: 150 },
    ]

    const tierColumns = [
      { title: '门槛（元）', key: 'threshold', width: 180 },
      { title: '赠送比例', key: 'rate', width: 160 },
      { title: '状态', key: 'enabled', width: 120 },
      { title: '说明', key: 'preview' },
      { title: '操作', key: 'actions', width: 100 },
    ]

    function previewText(row) {
      const threshold = Number(row.threshold)
      if (!Number.isFinite(threshold) || threshold <= 0) return '门槛必须为正'
      return `充满 ¥${formatMoney(threshold)} 按 ${formatMoney(row.ratePercent)}% 赠送`
    }

    function decodeTierRow(row) {
      return {
        uid: 'tier-' + (++tierRowSeed),
        threshold: Number(row?.threshold ?? 0),
        // 后端是 0.10 = 10%，界面上按百分数编辑更直观（管理员不会把 10% 填成 0.1）。
        ratePercent: Number(((Number(row?.rate ?? 0)) * 100).toFixed(2)),
        enabled: row?.enabled !== false,
        sortOrder: Number(row?.sortOrder ?? 0),
      }
    }

    function encodeTierRow(row) {
      return {
        threshold: Number(row.threshold ?? 0),
        rate: Number(((Number(row.ratePercent ?? 0)) / 100).toFixed(6)),
        enabled: row.enabled !== false,
        sortOrder: Number(row.sortOrder ?? 0),
      }
    }

    async function load() {
      if (!canManage()) return
      loading.value = true
      actionWarning.value = ''
      try {
        const [levelRes, tierRes] = await Promise.all([getUserLevels(), getRechargePromotionTiers()])
        if (!alive) return
        levels.value = Array.isArray(levelRes?.data?.data) ? levelRes.data.data : (Array.isArray(levelRes?.data) ? levelRes.data : [])
        const tierPayload = tierRes?.data ?? {}
        tierSource.value = tierPayload.source === 'table' ? 'table' : 'default'
        const rows = Array.isArray(tierPayload.rows) ? tierPayload.rows : []
        tierRows.value = rows.length > 0 ? rows.map(decodeTierRow) : []
        hasLoaded.value = true
      } catch (error) {
        actionWarning.value = readErrorMessage(error, '加载等级与档位失败')
      } finally {
        loading.value = false
      }
    }

    function readErrorMessage(error, fallback) {
      const message = error?.response?.data?.message
      if (typeof message === 'string' && message.trim()) return message
      if (typeof error?.message === 'string' && error.message.trim()) return error.message
      return fallback
    }

    function openCreate() {
      editor.open = true
      editor.id = 0
      editor.error = ''
      editor.name = ''
      // 新等级的排序号默认排在现有最高级之后（管理员最常做的就是这个）。
      const maxRank = levels.value.reduce((max, level) => Math.max(max, Number(level.rank) || 0), 0)
      editor.rank = maxRank + 1
      editor.minRechargeTotal = 0
      editor.minConsumptionTotal = 0
      editor.deviceLimit = 0
      editor.description = ''
      editor.enabled = true
    }

    function openEdit(record) {
      editor.open = true
      editor.id = Number(record.id)
      editor.error = ''
      editor.name = String(record.name ?? '')
      editor.rank = Number(record.rank ?? 1)
      editor.minRechargeTotal = Number(record.minRechargeTotal ?? 0)
      editor.minConsumptionTotal = Number(record.minConsumptionTotal ?? 0)
      editor.deviceLimit = Number(record.deviceLimit ?? 0)
      editor.description = String(record.description ?? '')
      editor.enabled = record.enabled !== false
    }

    function closeEditor() {
      if (editor.saving) return
      editor.open = false
    }

    async function submitEditor() {
      if (!canManage() || editor.saving) return
      if (!String(editor.name).trim()) {
        editor.error = '等级名称不能为空'
        return
      }
      editor.saving = true
      editor.error = ''
      mutating.value = true
      try {
        const payload = {
          name: editor.name,
          rank: Number(editor.rank),
          minRechargeTotal: Number(editor.minRechargeTotal ?? 0),
          minConsumptionTotal: Number(editor.minConsumptionTotal ?? 0),
          deviceLimit: Number(editor.deviceLimit ?? 0),
          description: editor.description,
          enabled: Boolean(editor.enabled),
        }
        if (editor.id > 0) await updateUserLevel(editor.id, payload)
        else await createUserLevel(payload)
        editor.open = false
        actionNotice.value = editor.id > 0 ? '等级已保存' : '等级已新增'
        await load()
      } catch (error) {
        // 冲突类错误（排序号被占用）必须留在弹窗里显示：关掉弹窗等于让管理员
        // 丢掉刚填的整张表单重来。
        editor.error = readErrorMessage(error, '保存等级失败')
      } finally {
        editor.saving = false
        mutating.value = false
      }
    }

    async function removeLevel(record) {
      if (!canManage() || busy()) return
      mutating.value = true
      actionWarning.value = ''
      try {
        await deleteUserLevel(record.id)
        actionNotice.value = `等级「${record.name}」已删除`
        await load()
      } catch (error) {
        actionWarning.value = readErrorMessage(error, '删除等级失败')
      } finally {
        mutating.value = false
      }
    }

    async function runReevaluate() {
      if (!canManage() || busy()) return
      reevaluating.value = true
      actionWarning.value = ''
      try {
        const res = await reevaluateUserLevels(200)
        const payload = res?.data ?? {}
        actionNotice.value = `已重评 ${Number(payload.processed ?? 0)} 个用户，其中 ${Number(payload.changed ?? 0)} 个等级发生变化`
      } catch (error) {
        actionWarning.value = readErrorMessage(error, '重新评估失败')
      } finally {
        reevaluating.value = false
      }
    }

    function addTierRow() {
      tierRows.value = [...tierRows.value, decodeTierRow({ threshold: 0, rate: 0 })]
    }

    function removeTierRow(row) {
      tierRows.value = tierRows.value.filter(item => item.uid !== row.uid)
    }

    function updateTierRow(row, key, value) {
      tierRows.value = tierRows.value.map(item => (item.uid === row.uid ? { ...item, [key]: value } : item))
    }

    async function saveTiers() {
      if (!canManage() || savingTiers.value) return
      savingTiers.value = true
      mutating.value = true
      actionWarning.value = ''
      try {
        const res = await replaceRechargePromotionTiers(tierRows.value.map(encodeTierRow))
        const payload = res?.data ?? {}
        tierSource.value = payload.source === 'table' ? 'table' : 'default'
        const rows = Array.isArray(payload.rows) ? payload.rows : []
        tierRows.value = rows.map(decodeTierRow)
        actionNotice.value = tierRows.value.length > 0
          ? '档位已保存，此后按表发放'
          : '档位表已清空，改用系统默认六档'
      } catch (error) {
        // 「同门槛不同比例」的冲突文案带具体门槛与两个比例，必须原样展示。
        actionWarning.value = readErrorMessage(error, '保存档位失败')
      } finally {
        savingTiers.value = false
        mutating.value = false
      }
    }

    async function runTierPreview() {
      if (!canManage() || previewLoading.value) return
      previewLoading.value = true
      previewError.value = ''
      try {
        const res = await previewRechargePromotionTier(Number(previewAmount.value), tierRows.value.map(encodeTierRow))
        previewResult.value = res?.data ?? null
      } catch (error) {
        previewResult.value = null
        previewError.value = readErrorMessage(error, '试算失败')
      } finally {
        previewLoading.value = false
      }
    }

    load()

    return {
      loading, mutating, reevaluating, savingTiers, hasLoaded,
      actionWarning, actionNotice,
      levels, tierRows, tierSource,
      previewAmount, previewLoading, previewError, previewResult,
      editor,
      levelColumns, tierColumns,
      canManage, busy, fieldId, load,
      openCreate, openEdit, closeEditor, submitEditor, removeLevel, runReevaluate,
      addTierRow, removeTierRow, updateTierRow, saveTiers, runTierPreview,
      previewText, formatMoney, formatPercent,
    }
  },
}
</script>

<style scoped>
.page-alert { margin-bottom: 12px; }
.tier-source-alert { margin-bottom: 12px; }
.tier-preview { color: var(--console-muted); }
.tier-preview-box { margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--console-border); }
.preview-result { margin: 8px 0 0; color: var(--console-text); }
.filter-error { margin: 8px 0 0; color: var(--console-danger); }
.level-form { max-width: 100%; }
</style>
