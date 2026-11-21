<template>
  <div class="console-page my-devices-page">
    <ConsolePageHeading
      title="我的设备"
      description="这里列出的都是调 API 的客户端设备（浏览器扩展 / 考试客户端），不包含网页后台登录。你可以移除设备释放名额，也可以删除已移除设备的历史记录；想彻底停用请用「拉黑」。"
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
      <div class="limit-settings-row">
        <dl class="limit-metrics" aria-label="设备上限概况">
          <div class="limit-metric">
            <dt class="limit-label">当前生效上限</dt>
            <dd class="limit-value" data-device-limit>{{ limitText }}</dd>
            <dd class="limit-hint">{{ limitHint }}</dd>
          </div>
          <div class="limit-metric">
            <dt class="limit-label">最多可设置</dt>
            <dd class="limit-value" data-device-limit-maximum>{{ limitCeilingText }}</dd>
            <dd class="limit-hint">{{ limitCeilingHint }}</dd>
          </div>
        </dl>

        <div class="limit-editor">
          <label :for="limitInputId" class="limit-label">调整设备上限</label>
          <div class="limit-controls">
            <a-input-number
              :id="limitInputId"
              v-model:value="limitDraft"
              :min="0"
              :max="limitView.hasUpperBound ? limitView.upperBound : undefined"
              :precision="0"
              :disabled="!limitEditable"
              :status="limitError ? 'error' : undefined"
              :aria-describedby="limitRuleId"
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
          </div>
          <p v-if="limitError" class="limit-error" role="alert">{{ limitError }}</p>
        </div>
      </div>

      <p :id="limitRuleId" class="limit-rule">{{ limitRuleText }}</p>
      <p class="hint">
        调低后超出的设备不会被立刻踢下线，但新设备会被拒绝，直到在用台数降到上限以内。
      </p>
    </ConsolePanel>

    <ConsolePanel
      title="设备闲置自动移除"
      description="设备连续无 API 活跃达到设定天数后会自动移除绑定，释放设备名额。"
    >
      <div class="limit-controls">
        <a-input-number v-model:value="expiryDays" :min="expiryAllowDisable ? 0 : 1" :max="expiryMaxDays || undefined" :precision="0" :disabled="!expiryLoaded || expirySaving || busy() || expiryMaxDays === 0" />
        <span class="limit-suffix">天</span>
        <a-button type="primary" :loading="expirySaving" :disabled="!expiryLoaded || busy() || expiryDays === null || expiryMaxDays === 0" @click="saveExpiry">保存</a-button>
      </div>
      <p v-if="!expiryLoaded" class="hint">{{ loading ? '正在读取设备自动移除策略…' : '设备自动移除策略暂不可用，请刷新重试。' }}</p>
      <p v-else-if="expiryMaxDays === 0" class="hint">管理员已关闭设备闲置自动移除，当前不会因闲置自动移除设备绑定。</p>
      <p v-else class="hint">管理员上限：{{ expiryMaxDays }} 天。{{ expiryAllowDisable ? '你可以设置 0 天来关闭自己的自动移除。' : '管理员未允许用户关闭自动移除，最小为 1 天。' }}</p>
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
        :scroll="{ x: 1440 }"
      >
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'label'">
            <div class="device-label">{{ record.deviceLabel || '未命名的设备' }}</div>
            <div class="device-id" :title="record.deviceId">{{ maskDeviceId(record.deviceId) }}</div>
          </template>
          <template v-else-if="column.key === 'firstIp' || column.key === 'lastIp'">
            <div class="device-ip" :class="{ 'device-ip-empty': !record[column.key] }">
              {{ formatValueWithLocation(record[column.key], ipLocations[normalizeLocationKey(record[column.key])]) || '未记录' }}
            </div>
          </template>
          <template v-else-if="column.key === 'firstSeenAt' || column.key === 'lastSeenAt'">
            <span class="device-time">{{ formatDeviceUsageTime(record[column.key]) }}</span>
          </template>
          <template v-else-if="column.key === 'status'">
            <a-tag :color="record.tone === 'success' ? 'green' : (record.tone === 'danger' ? 'red' : 'default')">
              {{ record.statusText }}
            </a-tag>
            <div v-if="record.revokedReason === 'user_removed'" class="status-note">你自己移除的</div>
            <div v-else-if="record.revokedReason === 'idle_10d' || record.revokedReason === 'idle_timeout'" class="status-note">闲置超时自动移除</div>
            <div v-else-if="record.revokedReason === 'device_banned'" class="status-note">该设备已被封禁</div>
            <div v-else-if="record.revokedReason === 'admin_unbound'" class="status-note">管理员解绑</div>
          </template>
          <template v-else-if="column.key === 'actions'">
            <a-space>
              <!--
                重命名放在最前：它是唯一「无副作用」的动作（只改展示名），
                而移除/拉黑都会改变准入状态。把安全动作放前面减少误点。
                孤儿拉黑行（id=0）没有绑定行可改，renamable 为 false 时不渲染。
              -->
              <a-button
                v-if="record.renamable"
                size="small"
                :disabled="busy()"
                @click="openRename(record)"
              >
                重命名
              </a-button>

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
                v-if="record.deletable"
                title="删除后将无法再查看这条历史记录，确定删除？"
                ok-text="删除"
                cancel-text="取消"
                :disabled="busy()"
                @confirm="deleteRemovedDevice(record)"
              >
                <a-button size="small" danger :disabled="busy()">删除记录</a-button>
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
      <p class="hint">使用时间均为北京时间。历史未采集的首次 IP 显示为「未记录」。</p>

      <!--
        重命名弹窗。
        ─────────────────────────────────────────────────────────────────
        为什么用 Modal 而不是行内编辑：表格行内输入框在 720px 下的窄屏上
        会被「操作」列的按钮挤成一格十几像素，输入中文时看不见自己打了什么。
        Modal 给的宽度是稳定的。

        为什么要显式说明「留空 = 恢复默认」：否则用户不知道该敲什么才能取消
        自己起的名字，会去填一个空格 —— 那是脏数据（后端会 trim 成空串，
        但用户不知道）。
      -->
      <a-modal
        v-model:open="renameOpen"
        title="重命名设备"
        ok-text="保存"
        cancel-text="取消"
        :confirm-loading="renameSaving"
        :ok-button-props="{ disabled: renameSaving }"
        @ok="submitRename"
        @cancel="closeRename"
      >
        <div class="rename-target">
          <div class="rename-target-label">{{ renameTargetLabel }}</div>
          <div class="device-id">{{ maskDeviceId(renameTargetId) }}</div>
        </div>
        <a-input
          v-model:value="renameDraft"
          :maxlength="DEVICE_LABEL_MAX_RUNES"
          show-count
          allow-clear
          placeholder="给这台设备起个能认出来的名字，例如「家里的笔记本」"
          :status="renameError ? 'error' : undefined"
          @press-enter="submitRename"
        />
        <p class="rename-hint">
          留空并保存会恢复成「未命名的设备」。
        </p>
        <p v-if="renameError" class="rename-error" role="alert">{{ renameError }}</p>
      </a-modal>

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
  deleteMyRemovedDevice,
  getMyDeviceLimit,
  getMyDeviceExpiry,
  getMyLocations,
  listMyDevices,
  removeMyDevice,
  renameMyDevice,
  unblockMyDevice,
  updateMyDeviceLimit,
  updateMyDeviceExpiry,
} from '@/api/TikuComponentApi'
import { getCurrentUserLogoutEpoch, useCurrentUserDisplay } from '@/utils/currentUser'
import { currentUserIdentity } from '@/utils/currentUserScope'
import {
  DEVICE_LABEL_MAX_RUNES,
  formatDeviceUsageTime,
  deviceLimitMaximumText,
  emptyDeviceLimitState,
  emptyDeviceListState,
  maskDeviceId,
  normalizeDeviceList,
  normalizeDeviceLimit,
  validateDeviceLabel,
  validateDeviceLimitInput,
  validateDeviceExpiryInput,
} from '@/utils/myDevicesHelpers.mjs'
import { collectLocationKeys, formatValueWithLocation, lookupLocationAddresses, normalizeLocationKey } from '@/utils/locationHelpers.mjs'

// 本页是**普通用户页面**：不得登记进 router.js 的 ADMIN_ONLY_ROUTE_NAMES，
// 后端六条接口也只要求 Auth（恒取当前登录用户，不接受 userId 参数）。
//
// ## 为什么所有文案/校验都在 myDevicesHelpers.mjs
//
// `npm run test:unit` 只跑 `src/utils/` 下的单测文件。把上限校验写在这里 = 没有任何
// 单测入口，而它恰恰是「前端放行、后端 400」这类可见缺陷的高发点。
// 所以上下界、状态映射、时间文案全部走纯函数层，页面只负责渲染与发请求。
//
// ⚠️ 这段注释里**刻意不写** glob 通配符（星号加斜杠）：
// 前端源码断言测试会用 `/\*[\s\S]*?\*\//` 剥块注释，而这样的字面量会被
// 误认成块注释的开头，从而把其后整段源码（含本文件的 <script> 与 columns 定义）
// 全部吞掉 —— 表现为「明明写了却断言不通过」，极难定位。
// 本仓已实测踩到一次（新增的一条 CSS 块注释成了那个误判配对的结尾）。
export default {
  name: 'MyDevicesPage',
  components: { ConsolePageHeading, ConsolePanel },
  setup() {
    const route = useRoute()
    const limitInputId = 'my-devices-limit-' + useId()
    const limitRuleId = 'my-devices-limit-rule-' + useId()
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()

    const loading = ref(false)
    const actionBusy = ref('')
    const savingLimit = ref(false)
    const failure = ref('')
    const notice = ref('')
    const ipLocations = ref({})

    const listView = ref(emptyDeviceListState())
    const limitView = ref(emptyDeviceLimitState())
    const limitDraft = ref(null)
    const limitError = ref('')
    const expiryDays = ref(null)
    const expiryLoaded = ref(false)
    const expiryMaxDays = ref(0)
    const expiryAllowDisable = ref(false)
    const expirySaving = ref(false)

    // 重命名弹窗的状态。四个字段而不是一个对象：
    // 弹窗关闭时要清空 draft/error 但**保留** target 供过渡动画读，
    // 拆开能让「清空哪些」显式可见。
    const renameOpen = ref(false)
    const renameSaving = ref(false)
    const renameDraft = ref('')
    const renameError = ref('')
    const renameTarget = ref(null)

    const renameTargetLabel = computed(() => {
      const target = renameTarget.value
      if (!target) return ''
      return target.deviceLabel || '未命名的设备'
    })
    const renameTargetId = computed(() => {
      const target = renameTarget.value
      return target ? target.deviceId : ''
    })

    let alive = true
    const mountedLogoutEpoch = getCurrentUserLogoutEpoch()

    // 与其它用户页一致：身份/路由/登出任一变化都不再让旧响应写回页面。
    const pageActive = () => alive && route.path === mountedPath
    const canLoad = () => pageActive() && getCurrentUserLogoutEpoch() === mountedLogoutEpoch
      && Boolean(currentUserIdentity(currentUser.value))
    const busy = () => loading.value || actionBusy.value !== '' || savingLimit.value || renameSaving.value || expirySaving.value
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
      // 无上界时直接说明等级不限，避免和可设置范围的描述冲突。
      if (!limitView.value.hasUpperBound) return '你可以自己设一个上限；等级本身不限设备数，不设也不会被拦。'
      return '上限受当前等级权益限制，可在允许范围内调整。'
    })

    const limitRuleText = computed(() => {
      if (!limitView.value.loaded) return '正在读取上限规则…'
      if (!limitView.value.hookReady || !limitView.value.editable) return '上限规则暂时不可用。'
      if (!limitView.value.hasUpperBound) {
        return '等级本身不限设备数，可设置任意非负整数；设置后，该数值会成为当前生效上限。'
      }
      return `设置值不能超过 ${limitView.value.upperBound} 台；具体生效上限以你的设置为准。`
    })

    const limitCeilingText = computed(() => {
      return deviceLimitMaximumText(limitView.value)
    })

    const limitCeilingHint = computed(() => {
      if (!limitView.value.loaded) return '正在读取等级权益'
      if (!limitView.value.hookReady || !limitView.value.editable) return '暂时无法确认等级上限'
      if (!limitView.value.hasUpperBound) return '当前等级本身不限设备数量'
      return '由当前等级权益决定'
    })

    const columns = [
      { title: '设备', key: 'label' },
      { title: '首次使用 IP', key: 'firstIp', width: 180 },
      { title: '最近使用 IP', key: 'lastIp', width: 180 },
      { title: '首次使用时间', key: 'firstSeenAt', width: 180 },
      { title: '最近使用时间', key: 'lastSeenAt', width: 180 },
      { title: '状态', key: 'status', width: 160 },
      { title: '操作', key: 'actions', width: 320 },
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
      return Boolean(record.renamable) || record.removable || record.status === 'active'
        || record.deletable || (record.blocked && record.blockId > 0)
    }

    // ───────────── 重命名 ─────────────
    //
    // 为什么把「校验」放在 submitRename 里而不是输入框的 onInput：
    // 用户打字过程中前缀一定「合法」（不可能是超长），实时报红只会让人烦躁。
    // 点保存时校验一次，错误就地显示在弹窗里（不跳全局 toast）。

    function openRename(record) {
      if (!record || !record.renamable) return
      clearNotice()
      renameTarget.value = record
      // 初值取**当前展示名**：用户改了两个字就能保存，不用重敲。
      // 但「未命名的设备」这个占位文案不能填进去 —— 那会让用户以为它已经叫这名。
      renameDraft.value = record.deviceLabel || ''
      renameError.value = ''
      renameOpen.value = true
    }

    function closeRename() {
      renameOpen.value = false
      renameSaving.value = false
      renameError.value = ''
      // draft 不清：用户误点取消后重新打开还能看到刚才打的字。
      // 它在下一次 openRename 时会被覆盖成该行的当前值，不会串到别的设备。
    }

    async function submitRename() {
      const target = renameTarget.value
      if (!target || renameSaving.value || !canLoad()) return
      const verdict = validateDeviceLabel(renameDraft.value)
      if (!verdict.ok) {
        renameError.value = verdict.message
        return
      }
      renameError.value = ''
      renameSaving.value = true
      try {
        const res = await renameMyDevice(target.id, verdict.value)
        if (!pageActive()) return
        if (res?.status === 400) {
          // 后端是最后防线：原样显示它的文案，不改写成「保存失败」
          // （那样用户不知道到底哪里不对）。
          renameError.value = res.message || '设备名称不符合要求'
          return
        }
        if (res?.status !== 200) {
          renameError.value = '重命名失败' + (res?.message ? '：' + res.message : '')
          return
        }
        renameOpen.value = false
        notice.value = verdict.value === '' ? '已恢复为「未命名的设备」。' : '设备已重命名。'
        await reload()
      } catch (error) {
        if (pageActive()) renameError.value = '重命名失败：' + (error?.message || error)
      } finally {
        if (pageActive()) renameSaving.value = false
      }
    }

    function enrichDeviceIps(rows, owns) {
      const ips = collectLocationKeys(rows, ['firstIp', 'lastIp'])
      if (!ips.length) return
      lookupLocationAddresses('ip', ips, getMyLocations, 'ip').then(addresses => {
        if (owns()) ipLocations.value = { ...ipLocations.value, ...addresses }
      })
    }

    async function loadList() {
      const owner = currentUserIdentity(currentUser.value)
      const res = await listMyDevices()
      if (!canLoad() || owner !== currentUserIdentity(currentUser.value)) return
      if (res?.status !== 200 || !res.data) {
        failure.value = '设备列表读取失败' + (res?.message ? '：' + res.message : '')
        return
      }
      const view = decorate(normalizeDeviceList(res.data))
      listView.value = view
      const loadedView = listView.value
      enrichDeviceIps(view.devices, () => canLoad() && owner === currentUserIdentity(currentUser.value) && listView.value === loadedView)
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
      try {
        await Promise.all([loadList(), loadLimit(), loadExpiry()])
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

    async function deleteRemovedDevice(record) {
      if (!canLoad() || busy() || !record?.deletable) return
      clearNotice(); actionBusy.value = record.rowKey
      try {
        const res = await deleteMyRemovedDevice(record.id)
        if (!pageActive()) return
        if (res?.status !== 200) { failure.value = '删除设备记录失败' + (res?.message ? '：' + res.message : ''); return }
        notice.value = '已删除该设备的历史记录。'
        await reload()
      } catch (error) { if (pageActive()) failure.value = '删除设备记录失败：' + (error?.message || error) }
      finally { if (pageActive()) actionBusy.value = '' }
    }

    async function loadExpiry() {
      const owner = currentUserIdentity(currentUser.value)
      expiryLoaded.value = false
      const res = await getMyDeviceExpiry()
      if (!canLoad() || owner !== currentUserIdentity(currentUser.value)) return
      const data = res?.data?.data || res?.data
      if (res?.status !== 200 || !data || !Number.isInteger(data.days) || !Number.isInteger(data.maxDays)) { expiryLoaded.value = false; failure.value = '设备过期策略读取失败' + (res?.message ? '：' + res.message : ''); return }
      expiryDays.value = data.days
      expiryMaxDays.value = data.maxDays
      expiryAllowDisable.value = Boolean(data.allowDisable)
      expiryLoaded.value = true
    }

    async function saveExpiry() {
      if (!canLoad() || busy() || !expiryLoaded.value || expiryMaxDays.value === 0) return
      const verdict = validateDeviceExpiryInput(expiryDays.value, expiryMaxDays.value, expiryAllowDisable.value)
      if (!verdict.ok) { failure.value = verdict.message; return }
      expirySaving.value = true; clearNotice()
      try {
        const res = await updateMyDeviceExpiry(verdict.value)
        if (!canLoad()) return
        if (res?.status !== 200) { failure.value = '保存设备过期策略失败' + (res?.message ? '：'+res.message : ''); return }
        notice.value = '设备自动移除时间已更新。'
        await loadExpiry()
      } catch (error) { if (pageActive()) failure.value = '保存设备过期策略失败：' + (error?.message || error) }
      finally { if (pageActive()) expirySaving.value = false }
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
      ipLocations.value = {}
      limitView.value = emptyDeviceLimitState()
      limitDraft.value = null
      limitError.value = ''
      expiryDays.value = null
      expiryLoaded.value = false
      expiryMaxDays.value = 0
      expiryAllowDisable.value = false
      expirySaving.value = false
      actionBusy.value = ''
      loading.value = false
      failure.value = ''
      notice.value = ''
      // 弹窗也要收起：否则用户在弹窗打开时登出/切页，弹窗会浮在一个已经
      // 没有数据的页面上（且 renameTarget 指向一条已不在列表里的记录）。
      renameOpen.value = false
      renameSaving.value = false
      renameError.value = ''
      renameTarget.value = null
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
      loading, actionBusy, savingLimit, failure, notice, ipLocations,
      listView, limitView, limitDraft, limitError, limitEditable, expiryDays, expiryLoaded, expiryMaxDays, expiryAllowDisable, expirySaving,
      limitText, limitHint, limitPanelDescription, limitCeilingText, limitCeilingHint,
      limitRuleText,
      limitInputId, limitRuleId, columns, busy,
      reload, removeDevice, deleteRemovedDevice, blockDevice, unblockDevice, saveDeviceLimit, saveExpiry,
      hasAnyAction, formatDeviceUsageTime, maskDeviceId,
      // 来源 IP 与重命名
      formatValueWithLocation, normalizeLocationKey, DEVICE_LABEL_MAX_RUNES,
      renameOpen, renameSaving, renameDraft, renameError,
      renameTargetLabel, renameTargetId,
      openRename, closeRename, submitRename,
    }
  },
}
</script>

<style scoped>
.page-alert { margin-bottom: 16px; }
.limit-settings-row { display: grid; grid-template-columns: minmax(0, 1fr) minmax(250px, .8fr); align-items: start; gap: 20px; }
.limit-metrics { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; margin: 0; }
.limit-metric { min-width: 0; padding: 16px; border: 1px solid var(--console-border); border-radius: 8px; background: var(--console-surface-secondary, #f8faf9); }
.limit-metric dd { margin: 0; }
.limit-value { color: var(--console-text); font-size: clamp(22px, 3vw, 30px); font-weight: 700; font-variant-numeric: tabular-nums; }
.limit-metric .limit-hint { margin-top: 6px; color: var(--console-muted); font-size: 13px; line-height: 1.6; }
.limit-editor { display: grid; gap: 8px; min-width: 0; }
.limit-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.limit-label { color: var(--console-muted); font-size: 13px; }
.limit-input { width: 120px; }
.limit-suffix { color: var(--console-muted); font-size: 13px; }
.limit-error { margin: 0; color: var(--console-danger, #b91c1c); font-size: 13px; }
.limit-rule { margin: 16px 0 0; color: var(--console-muted); font-size: 13px; line-height: 1.7; }
.device-label { color: var(--console-text); font-weight: 600; }
.device-id { margin-top: 2px; color: var(--console-muted); font-family: var(--console-mono, ui-monospace, SFMono-Regular, Consolas, monospace); font-size: 12px; overflow-wrap: anywhere; }
/* IPv6 最长 45 字符：允许换行，否则定宽列里会撑破表格。 */
.device-ip { font-family: var(--console-mono, ui-monospace, SFMono-Regular, Consolas, monospace); font-size: 12px; overflow-wrap: anywhere; white-space: pre-line; }
.device-time { font-size: 12px; white-space: nowrap; font-variant-numeric: tabular-nums; }
.device-ip-empty { color: var(--console-muted); font-style: italic; }
.status-note { margin-top: 4px; color: var(--console-muted); font-size: 12px; }
.muted { color: var(--console-muted); }
.hint { margin: 12px 0 0; color: var(--console-muted); font-size: 13px; line-height: 1.7; }
@media (max-width: 720px) {
  .limit-settings-row { grid-template-columns: minmax(0, 1fr); }
}
@media (max-width: 460px) {
  .limit-metrics { grid-template-columns: minmax(0, 1fr); }
}
.rename-target { margin-bottom: 12px; }
.rename-target-label { color: var(--console-text); font-weight: 600; }
.rename-hint { margin: 8px 0 0; color: var(--console-muted); font-size: 12px; line-height: 1.6; }
.rename-error { margin: 8px 0 0; color: var(--console-danger, #b91c1c); font-size: 13px; }
</style>
