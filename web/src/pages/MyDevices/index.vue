<template>
  <div class="console-page my-devices-page">
    <ConsolePageHeading
      title="我的设备"
      description="设备上限是账号权益的一部分。用满后可以在下面移除不再使用的设备释放名额；被移除的设备下次登录会被静默重新绑定，所以想彻底停用请用「拉黑」。"
    >
      <template #actions>
        <a-button :loading="loading" :disabled="busy()" @click="reload">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert
      v-if="failure"
      type="error"
      show-icon
      closable
      class="page-alert"
      :message="failure"
      @close="failure = ''"
    />
    <a-alert
      v-if="notice"
      type="success"
      show-icon
      closable
      class="page-alert"
      :message="notice"
      @close="notice = ''"
    />

    <!-- ───────────── 设备上限 ───────────── -->
    <ConsolePanel
      title="设备上限"
      :description="limitPanelDescription"
    >
      <div class="limit-row">
        <div class="limit-summary">
          <div class="limit-value" data-device-limit>
            {{ limitText }}
          </div>
          <div class="limit-hint">
            {{ limitHint }}
          </div>
        </div>

        <div class="limit-editor">
          <label :for="limitInputId" class="limit-label">调整为</label>
          <a-input-number
            :id="limitInputId"
            v-model:value="limitDraft"
            :min="0"
            :max="limitView.hasUpperBound ? limitView.upperBound : undefined"
            :precision="0"
            :disabled="!limitEditable"
            :status="limitError ? 'error' : undefined"
            class="limit-input"
            @press-enter="saveDeviceLimit"
          />
          <span class="limit-suffix">台</span>
          <a-button
            type="primary"
            :loading="savingLimit"
            :disabled="!limitEditable || savingLimit"
            @click="saveDeviceLimit"
          >
            保存
          </a-button>
          <span v-if="limitError" class="limit-error" role="alert">{{ limitError }}</span>
        </div>
      </div>
      <p class="hint">
        上限只能调低不能调高：{{ limitCeilingText }}。
        调低后超出的设备不会被立刻踢下线，但新设备会被拒绝，直到在用台数降到上限以内。
      </p>
    </ConsolePanel>

    <!-- ───────────── 设备列表 ───────────── -->
    <ConsolePanel
      title="设备列表"
      :description="'共 ' + listView.total + ' 台（使用中 ' + listView.active + ' 台，已拉黑 ' + listView.blocked + ' 台）。已移除与已拉黑的设备也列在这里，便于核对历史。'"
    >
      <a-table
        :columns="columns"
        :data-source="listView.devices"
        :pagination="false"
        :loading="loading"
        row-key="rowKey"
        size="middle"
        :scroll="{ x: 880 }"
      >
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'label'">
            <div class="device-label">{{ record.deviceLabel || '未命名的设备' }}</div>
            <div class="device-id" :title="record.deviceId">{{ maskDeviceId(record.deviceId) }}</div>
          </template>
          <template v-else-if="column.key === 'lastSeen'">
            {{ describeLastSeen(record.lastSeenAt, now) }}
          </template>
          <template v-else-if="column.key === 'status'">
            <a-tag :color="record.tone === 'success' ? 'green' : (record.tone === 'danger' ? 'red' : 'default')">
              {{ record.statusText }}
            </a-tag>
            <div v-if="record.revokedReason === 'user_removed'" class="status-note">你自己移除的</div>
            <div v-else-if="record.revokedReason === 'idle_10d'" class="status-note">超过 10 天未活跃</div>
            <div v-else-if="record.revokedReason === 'device_banned'" class="status-note">该设备已被封禁</div>
            <div v-else-if="record.revokedReason === 'admin_unbound'" class="status-note">管理员解绑</div>
          </template>
          <template v-else-if="column.key === 'actions'">
            <a-space>
              <a-popconfirm
                v-if="record.removable"
                title="移除后该设备的名额会释放，下次登录可能被静默重新绑定。确定移除？"
                ok-text="移除"
                cancel-text="取消"
                :disabled="busy()"
                @confirm="removeDevice(record)"
              >
                <a-button size="small" :disabled="busy()">移除</a-button>
              </a-popconfirm>

              <a-popconfirm
                v-if="record.status === 'active'"
                title="拉黑后这台设备将无法使用本账号（你可以随时解除）。确定拉黑？"
                ok-text="拉黑"
                cancel-text="取消"
                :disabled="busy()"
                @confirm="blockDevice(record)"
              >
                <a-button size="small" danger :disabled="busy()">拉黑</a-button>
              </a-popconfirm>

              <a-button
                v-if="record.blocked && record.blockId > 0"
                size="small"
                type="primary"
                ghost
                :loading="actionBusy === record.rowKey"
                :disabled="busy()"
                @click="unblockDevice(record)"
              >
                解除拉黑
              </a-button>

              <span v-if="!hasAnyAction(record)" class="muted">—</span>
            </a-space>
          </template>
        </template>
      </a-table>

      <a-empty
        v-if="listView.loaded && listView.devices.length === 0"
        description="还没有任何设备记录。用带设备标识的客户端（浏览器扩展 / 考试客户端）登录一次，这里就会出现。"
      />
      <p v-if="!listView.schemaReady" class="hint">
        设备表尚未就绪（数据表待迁移），当前列表为空表示「没有数据」而不是「没有设备」。
      </p>
    </ConsolePanel>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import { useRoute } from 'vue-router'
import { message } from 'ant-design-vue'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import {
  blockMyDevice,
  getMyDeviceLimit,
  listMyDevices,
  removeMyDevice,
  unblockMyDevice,
  updateMyDeviceLimit,
} from '@/api/TikuComponentApi'
import { getCurrentUserLogoutEpoch, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import {
  describeLastSeen,
  emptyDeviceLimitState,
  emptyDeviceListState,
  maskDeviceId,
  normalizeDeviceList,
  normalizeDeviceLimit,
  validateDeviceLimitInput,
} from '@/utils/myDevicesHelpers.mjs'

// 本页是**普通用户页面**：不得登记进 router.js 的 ADMIN_ONLY_ROUTE_NAMES，
// 后端六条接口也只要求 Auth（恒取当前登录用户，不接受 userId 参数）。
//
// ## 为什么所有文案/校验都在 myDevicesHelpers.mjs
//
// `npm run test:unit` 只跑 `src/utils/*.test.mjs`。把上限校验写在这里 = 没有任何
// 单测入口，而它恰恰是「前端放行、后端 400」这类可见缺陷的高发点。
// 所以上下界、状态映射、时间文案全部走纯函数层，页面只负责渲染与发请求。
export default {
  name: 'MyDevicesPage',
  components: { ConsolePageHeading, ConsolePanel },
  setup() {
    const route = useRoute()
    const limitInputId = 'my-devices-limit-' + useId()
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const loading = ref(false)
    const actionBusy = ref('')
    const savingLimit = ref(false)
    const failure = ref('')
    const notice = ref('')
    const now = ref(Date.now())

    const listView = ref(emptyDeviceListState())
    const limitView = ref(emptyDeviceLimitState())
    const limitDraft = ref(null)
    const limitError = ref('')

    let alive = true
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()

    // 与其它用户页一致：身份/路由/登出任一变化都不再让旧响应写回页面。
    const pageActive = () => alive && route.path === mountedPath
    const canLoad = () => pageActive() && getCurrentUserLogoutEpoch() === mountedLogoutEpoch
      && Boolean(currentUserIdentity(currentUser.value))
    const busy = () => loading.value || actionBusy.value !== '' || savingLimit.value
    const limitEditable = computed(() => limitView.value.loaded && limitView.value.editable
      && limitView.value.hookReady && !busy())

    const limitText = computed(() => {
      if (!limitView.value.loaded) return '读取中…'
      if (!limitView.value.limitKnown) return '未知'
      if (limitView.value.unlimited) return '不限制'
      return `${limitView.value.limit} 台`
    })

    const limitHint = computed(() => {
      if (!limitView.value.loaded) return ''
      const parts = []
      if (limitView.value.sourceText) parts.push('来源：' + limitView.value.sourceText)
      if (limitView.value.levelName) parts.push('当前等级：' + limitView.value.levelName)
      if (limitView.value.override !== null && limitView.value.override >= 0) {
        parts.push('你自己设置了 ' + limitView.value.override + ' 台')
      }
      return parts.join(' · ')
    })

    const limitPanelDescription = computed(() => {
      if (!limitView.value.loaded) return '正在读取当前生效的设备上限。'
      if (!limitView.value.hookReady) return '上限计算组件尚未就绪，暂时无法调整。'
      if (!limitView.value.editable) return '当前无法调整上限（数据表待迁移或等级信息缺失）。'
      return '只能调低不能调高：上限不会超过你的等级权益。'
    })

    const limitCeilingText = computed(() => {
      if (!limitView.value.loaded) return '上界读取中'
      if (!limitView.value.hasUpperBound) return '你的等级本身不限设备数，可以设为任意非负整数'
      return `当前等级上限 ${limitView.value.upperBound} 台`
    })

    const columns = [
      { title: '设备', key: 'label' },
      { title: '最近活跃', key: 'lastSeen', width: 150 },
      { title: '状态', key: 'status', width: 170 },
      { title: '操作', key: 'actions', width: 260 },
    ]

    function clearNotice() { failure.value = ''; notice.value = '' }

    // 列表行需要稳定的 rowKey：孤儿拉黑行的 id 是 0（没有绑定行），
    // 用 id 做 key 会让同一页多个孤儿行撞 key（渲染错位）。
    function decorate(view) {
      const devices = view.devices.map((item) => ({
        ...item,
        rowKey: item.id > 0 ? 'binding-' + item.id : 'block-' + item.blockId + '-' + item.deviceId,
      }))
      return { ...view, devices }
    }

    function hasAnyAction(record) {
      return record.removable || record.status === 'active' || (record.blocked && record.blockId > 0)
    }

    async function loadList() {
      const res = await listMyDevices()
      if (!pageActive()) return
      if (res?.status !== 200 || !res.data) {
        failure.value = '设备列表读取失败' + (res?.message ? '：' + res.message : '')
        return
      }
      listView.value = decorate(normalizeDeviceList(res.data))
    }

    async function loadLimit() {
      const res = await getMyDeviceLimit()
      if (!pageActive()) return
      if (res?.status !== 200 || !res.data) {
        failure.value = '设备上限读取失败' + (res?.message ? '：' + res.message : '')
        return
      }
      const view = normalizeDeviceLimit(res.data)
      limitView.value = view
      // 输入框初值取**当前生效值**；用户改的就是它。读到未知时不填 0
      // （0 是「不限」的语义值，凭空写进去等于替用户做了一次放宽）。
      limitDraft.value = view.limitKnown ? view.limit : null
      limitError.value = ''
    }

    async function reload() {
      if (!canLoad() || loading.value) return
      clearNotice()
      loading.value = true
      now.value = Date.now()
      try {
        await Promise.all([loadList(), loadLimit()])
      } catch (error) {
        if (pageActive()) failure.value = '读取失败：' + (error?.message || error)
      } finally {
        if (pageActive()) loading.value = false
      }
    }

    async function removeDevice(record) {
      if (!canLoad() || busy()) return
      clearNotice()
      actionBusy.value = record.rowKey
      try {
        const res = await removeMyDevice(record.id)
        if (!pageActive()) return
        if (res?.status !== 200) {
          failure.value = '移除设备失败' + (res?.message ? '：' + res.message : '')
          return
        }
        notice.value = res.data?.alreadyRevoked ? '这台设备此前已被移除。' : '已移除该设备，名额已释放。'
        await reload()
      } catch (error) {
        if (pageActive()) failure.value = '移除设备失败：' + (error?.message || error)
      } finally {
        if (pageActive()) actionBusy.value = ''
      }
    }

    async function blockDevice(record) {
      if (!canLoad() || busy()) return
      clearNotice()
      actionBusy.value = record.rowKey
      try {
        const res = await blockMyDevice(record.id)
        if (!pageActive()) return
        if (res?.status !== 200) {
          failure.value = '拉黑设备失败' + (res?.message ? '：' + res.message : '')
          return
        }
        notice.value = '已拉黑该设备，它现在无法使用本账号。'
        await reload()
      } catch (error) {
        if (pageActive()) failure.value = '拉黑设备失败：' + (error?.message || error)
      } finally {
        if (pageActive()) actionBusy.value = ''
      }
    }

    async function unblockDevice(record) {
      if (!canLoad() || busy()) return
      if (!record.blockId || record.blockId <= 0) return
      clearNotice()
      actionBusy.value = record.rowKey
      try {
        const res = await unblockMyDevice(record.blockId)
        if (!pageActive()) return
        if (res?.status !== 200) {
          failure.value = '解除拉黑失败' + (res?.message ? '：' + res.message : '')
          return
        }
        notice.value = '已解除拉黑。'
        await reload()
      } catch (error) {
        if (pageActive()) failure.value = '解除拉黑失败：' + (error?.message || error)
      } finally {
        if (pageActive()) actionBusy.value = ''
      }
    }

    async function saveDeviceLimit() {
      if (!canLoad() || !limitEditable.value) return
      clearNotice()
      const verdict = validateDeviceLimitInput(limitDraft.value, limitView.value)
      if (!verdict.ok) {
        limitError.value = verdict.message
        return
      }
      limitError.value = ''
      savingLimit.value = true
      try {
        const res = await updateMyDeviceLimit(verdict.value)
        if (!pageActive()) return
        if (res?.status === 400) {
          // 后端是最后防线：把它的文案原样显示（不要改写成「保存失败」，
          // 那样用户不知道到底哪里不对）。
          limitError.value = res.message || '设备上限不符合要求'
          return
        }
        if (res?.status !== 200 || !res.data) {
          failure.value = '保存设备上限失败' + (res?.message ? '：' + res.message : '')
          return
        }
        limitView.value = normalizeDeviceLimit(res.data)
        limitDraft.value = limitView.value.limitKnown ? limitView.value.limit : null
        notice.value = '设备上限已更新。'
        message.success('设备上限已更新')
      } catch (error) {
        if (pageActive()) failure.value = '保存设备上限失败：' + (error?.message || error)
      } finally {
        if (pageActive()) savingLimit.value = false
      }
    }

    function retire() {
      listView.value = emptyDeviceListState()
      limitView.value = emptyDeviceLimitState()
      limitDraft.value = null
      limitError.value = ''
      actionBusy.value = ''
      loading.value = false
      failure.value = ''
      notice.value = ''
    }

    onMounted(() => {
      alive = true
      if (canLoad()) void reload()
    })
    onBeforeUnmount(() => {
      alive = false
      retire()
    })

    return {
      loading, actionBusy, savingLimit, failure, notice, now,
      listView, limitView, limitDraft, limitError, limitEditable,
      limitText, limitHint, limitPanelDescription, limitCeilingText,
      limitInputId, columns, busy,
      reload, removeDevice, blockDevice, unblockDevice, saveDeviceLimit,
      hasAnyAction, describeLastSeen, maskDeviceId,
    }
  },
}
</script>

<style scoped>
.page-alert { margin-bottom: 16px; }
.limit-row { display: flex; flex-wrap: wrap; align-items: flex-end; gap: 24px; }
.limit-summary { min-width: 200px; }
.limit-value { color: var(--console-text); font-size: clamp(22px, 3vw, 30px); font-weight: 700; font-variant-numeric: tabular-nums; }
.limit-hint { margin-top: 6px; color: var(--console-muted); font-size: 13px; line-height: 1.6; }
.limit-editor { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.limit-label { color: var(--console-muted); font-size: 13px; }
.limit-input { width: 120px; }
.limit-suffix { color: var(--console-muted); font-size: 13px; }
.limit-error { color: var(--console-danger, #b91c1c); font-size: 13px; }
.device-label { color: var(--console-text); font-weight: 600; }
.device-id { margin-top: 2px; color: var(--console-muted); font-family: var(--console-mono, ui-monospace, SFMono-Regular, Consolas, monospace); font-size: 12px; overflow-wrap: anywhere; }
.status-note { margin-top: 4px; color: var(--console-muted); font-size: 12px; }
.muted { color: var(--console-muted); }
.hint { margin: 12px 0 0; color: var(--console-muted); font-size: 13px; line-height: 1.7; }
</style>
