<template>
  <div class="console-page user-portrait-page">
    <ConsolePageHeading :title="scopeTitle" :description="scopeDescription">
      <template #actions>
        <a-space wrap class="section-actions">
          <a-button @click="reload" :loading="loading">刷新</a-button>
          <a-button v-if="activeUserId" @click="switchToOverall">查看整体画像</a-button>
        </a-space>
      </template>
    </ConsolePageHeading>

    <!--
      用户搜索框（用户 2026-11-13 决策）：
      本页主要用途是**全站整体画像**（默认视图），选中某个用户后切到该用户画像。
      这是「一个页面的两种视角」，不是两个独立页面。
    -->
    <a-card :bordered="false" class="page-card portrait-switcher">
      <a-form layout="inline">
        <a-form-item label="查看单个用户画像" :html-for="selectorId">
          <a-select
            :id="selectorId"
            v-model:value="selectedUserId"
            show-search
            allow-clear
            :filter-option="filterUserOption"
            :options="userOptions"
            :loading="userOptionsLoading"
            placeholder="输入用户名 / 昵称 / ID 搜索"
            style="width: 320px"
            not-found-content="没有匹配的用户"
            @search="searchUsers"
            @change="onUserChange"
          />
        </a-form-item>
        <a-form-item>
          <span class="form-hint">清空选择即回到全站整体画像。</span>
        </a-form-item>
      </a-form>
    </a-card>

    <a-alert v-if="!canViewPortrait" type="warning" show-icon class="page-alert" message="用户画像仅管理员可见。" />

    <RequestFailureAlert
      v-if="canViewPortrait && failure"
      :key="failureContextKey"
      :failure="failure"
      :message="failureMessage"
      retryable
      :busy="loading"
      :context-key="failureContextKey"
      @retry="reload"
    />

    <template v-if="canViewPortrait">
      <!-- 指标卡：缺失显示「暂无数据」，绝不显示 0（「没有数据」与「数据是 0」不同） -->
      <ConsoleSummaryGrid>
        <a-card v-for="tile in tiles" :key="tile.key" :bordered="false" class="summary-card">
          <a-statistic :title="tile.title" :value="tile.missing ? PORTRAIT_EMPTY_HINT : tile.value" />
        </a-card>
      </ConsoleSummaryGrid>

      <a-card :bordered="false" class="page-card">
        <a-alert
          type="info"
          show-icon
          class="page-alert"
          message="本页所有数字均来自账单 / 工单 / 用户表的真实统计。设备指纹与答题正确率没有数据源，因此不展示。"
        />

        <!-- 基础栏只在单用户视角出现 -->
        <a-descriptions v-if="base" :column="2" size="small" bordered class="portrait-base">
          <a-descriptions-item label="用户">#{{ base.userId }} {{ base.nickname || base.username }}</a-descriptions-item>
          <a-descriptions-item label="角色">{{ base.role }}</a-descriptions-item>
          <a-descriptions-item label="状态">{{ base.status }}</a-descriptions-item>
          <a-descriptions-item label="注册来源">{{ base.registerSource }}</a-descriptions-item>
          <a-descriptions-item label="余额">¥ {{ formatMoneyValue(base.balance) }}</a-descriptions-item>
          <a-descriptions-item label="赠金">¥ {{ formatMoneyValue(base.giftBalance) }}</a-descriptions-item>
          <a-descriptions-item label="注册 IP">{{ base.registerIp || '-' }} {{ base.registerRegion ? `（${base.registerRegion}）` : '' }}</a-descriptions-item>
          <a-descriptions-item label="最近登录 IP">{{ base.lastLoginIp || '-' }} {{ base.lastLoginRegion ? `（${base.lastLoginRegion}）` : '' }}</a-descriptions-item>
        </a-descriptions>

        <div class="portrait-charts">
          <section v-for="chart in chartList" :key="chart.key" class="portrait-chart">
            <h3 class="portrait-chart__title">{{ chart.title }}</h3>
            <!--
              空态：后端无数据时返回空数组，这里显示「暂无数据」。
              不画 0 值扇区 —— 那会让「从没发生过」看起来像「发生过但次数为 0」。
            -->
            <a-empty v-if="chart.empty" :description="PORTRAIT_EMPTY_HINT" />
            <!--
              图表容器：真实 echarts 实例在拥有构建环境时挂载到该节点。
              本仓 web/node_modules 为空，无法验证渲染，因此这里只预留容器 + 数据表回退。
              数据表回退也让无图表库的环境仍然可读（不是「白屏」）。
            -->
            <div v-else class="portrait-chart__body">
              <table class="portrait-chart__table">
                <thead>
                  <tr><th scope="col">分类</th><th scope="col">数量</th></tr>
                </thead>
                <tbody>
                  <tr v-for="(label, index) in chart.labels" :key="label + index">
                    <td>{{ label }}</td>
                    <td>{{ chart.values[index] }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>
        </div>

        <!--
          如实标注「哪些栏位当前没有数据源」（98 §1）。留空或画 0 会被误读成「用得少」。
        -->
        <section v-if="notes.length" class="portrait-notes">
          <h3 class="portrait-chart__title">数据口径说明</h3>
          <ul>
            <li v-for="(note, index) in notes" :key="index">{{ note }}</li>
          </ul>
        </section>
      </a-card>
    </template>
  </div>
</template>

<script>
// 用户画像页（T-17 / task_doc/98）。
//
// # 一个页面，两种视角（用户 2026-11-13 决策）
//
//   缺省                   → 全站整体画像（本页「主要」用途）
//   ?userId=<id>           → 单个用户画像（用户管理「查看」弹窗里点「画像」跳过来）
//   页面顶部用户搜索框     → 选中即切到该用户；清空即回到整体
//
// 不是两个独立页面：数据、渲染、空态、失败态全部共用一套代码，
// 差异只在「查哪条接口」与「是否显示基础栏」。
//
// # 权限（98 号 §0 第 ④ 层）
//
// 非管理员：菜单项不可见（SiderMenu 的 ADMIN_MENU_ITEMS 才列它），直接敲 URL 会被
// router.js 的 ADMIN_ONLY_ROUTE_NAMES 跳走。**但这只是可见性，不是安全边界** ——
// 真正的闸门在后端（Auth + RequireAdmin + handler 内二次校验）。
// 因此本页即使被绕过也只可能拿到 403，不会拿到数据。
import { computed, defineComponent, onMounted, ref, shallowRef, useId, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message } from 'ant-design-vue'

import { getPortraitOverview, getUserPortrait } from '@/api/UserPortraitApi'
import { getUserList } from '@/api/TikuComponentApi'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'
import { useCurrentUserDisplay, isAdminUser } from '@/utils/currentUser'
import {
  PORTRAIT_EMPTY_HINT,
  PORTRAIT_SCOPE_OVERALL,
  buildPortraitCharts,
  buildPortraitTiles,
  buildPortraitUserOption,
  normalizePortraitUserId,
  portraitScopeHint,
} from '@/utils/portraitCharts.mjs'

export default defineComponent({
  name: 'UserPortrait',
  components: { RequestFailureAlert },
  setup() {
    const route = useRoute()
    const router = useRouter()
    const { currentUser } = useCurrentUserDisplay()
    const selectorId = `portrait-user-${useId()}`

    const canViewPortrait = computed(() => isAdminUser(currentUser.value))

    // 视角：'' = 整体；正整数 = 该用户。
    const activeUserId = ref(normalizePortraitUserId(route.query.userId))
    const selectedUserId = ref(activeUserId.value || undefined)

    const portrait = shallowRef(null)
    const loading = ref(false)
    const failure = shallowRef(null)
    const failureContextKey = ref(0)
    // 请求版本号：切换视角时让旧响应失效（否则「选了 A 又快速选 B」会显示 A 的数据）。
    let requestVersion = 0

    const userOptions = ref([])
    const userOptionsLoading = ref(false)

    const scopeTitle = computed(() => portraitScopeHint(portrait.value || { scope: activeUserId.value ? 'user' : PORTRAIT_SCOPE_OVERALL, targetUserId: activeUserId.value }))
    const scopeDescription = computed(() => (activeUserId.value
      ? '查看该用户的消费、时段、区域与工单分布。'
      : '全站用户的行为画像总览。使用上方搜索框可切换到单个用户。'))
    const failureMessage = computed(() => (activeUserId.value ? '获取该用户画像失败' : '获取整体用户画像失败'))

    const base = computed(() => (portrait.value && portrait.value.base) || null)
    const notes = computed(() => (Array.isArray(portrait.value?.notes) ? portrait.value.notes : []))
    const tiles = computed(() => buildPortraitTiles(portrait.value || {}, {
      money: value => formatMoneyValue(value),
      count: value => value,
    }))
    const chartList = computed(() => {
      const charts = buildPortraitCharts(portrait.value || {})
      return [
        { key: 'hours', ...charts.hours },
        { key: 'weekdays', ...charts.weekdays },
        { key: 'regions', ...charts.regions },
        { key: 'billingTypes', ...charts.billingTypes },
      ]
    })

    const isCurrentFailure = (source, context) => Boolean(source) && source === failure.value
      && context === failureContextKey.value

    function clearFailure() {
      failure.value = null
      failureContextKey.value += 1
    }

    async function load() {
      if (!canViewPortrait.value) return
      const version = ++requestVersion
      const isCurrent = () => version === requestVersion
      loading.value = true
      clearFailure()
      try {
        const response = activeUserId.value
          ? await getUserPortrait(activeUserId.value)
          : await getPortraitOverview()
        if (!isCurrent()) return
        // 后端 200 才是有效数据；其余一律走失败态（不拿 403 的 body 当画像渲染）。
        if (response?.status === 200 && response.data && typeof response.data === 'object') {
          portrait.value = response.data
        } else {
          failure.value = ensureRequestFailure(response)
          portrait.value = null
        }
      } catch (error) {
        if (!isCurrent()) return
        failure.value = ensureRequestFailure(error)
        portrait.value = null
      } finally {
        if (isCurrent()) loading.value = false
      }
    }

    function reload() {
      return load()
    }

    function switchToOverall() {
      selectedUserId.value = undefined
      activeUserId.value = ''
      // 用 replace 而不是 push：视角切换不该在浏览器历史里堆一串记录。
      router.replace({ path: route.path, query: {} }).catch(() => {})
      return load()
    }

    function onUserChange(value) {
      const normalized = normalizePortraitUserId(value)
      activeUserId.value = normalized
      router.replace({ path: route.path, query: normalized ? { userId: String(normalized) } : {} }).catch(() => {})
      return load()
    }

    // 用户搜索：只在管理员输够 1 个字符时才查，避免打开页面就拉全量用户。
    async function searchUsers(keyword) {
      const text = String(keyword ?? '').trim()
      if (!text) {
        userOptions.value = []
        return
      }
      userOptionsLoading.value = true
      try {
        const response = await getUserList({ keyword: text, page: 1, pageSize: 20 })
        const rows = Array.isArray(response?.data?.data) ? response.data.data : []
        userOptions.value = rows
          .map(buildPortraitUserOption)
          .filter(option => option !== null)
      } catch {
        // 搜索失败不覆盖画像失败态：那是搜索框自己的问题，静默降级为空候选。
        userOptions.value = []
      } finally {
        userOptionsLoading.value = false
      }
    }

    function filterUserOption(input, option) {
      const text = String(input ?? '').trim().toLowerCase()
      if (!text) return true
      return String(option?.label ?? '').toLowerCase().includes(text)
    }

    // 路由 query 变化（含浏览器前进/后退）要跟着切换视角。
    watch(() => route.query.userId, value => {
      const normalized = normalizePortraitUserId(value)
      if (normalized === activeUserId.value) return
      activeUserId.value = normalized
      selectedUserId.value = normalized || undefined
      load()
    })

    onMounted(() => {
      if (!canViewPortrait.value) {
        // 非管理员：路由守卫本来就该挡住；这里只做兜底提示，不请求接口。
        message.warning('用户画像仅管理员可见')
        return
      }
      load()
    })

    return {
      PORTRAIT_EMPTY_HINT,
      selectorId,
      canViewPortrait,
      activeUserId,
      selectedUserId,
      loading,
      failure,
      failureContextKey,
      isCurrentFailure,
      userOptions,
      userOptionsLoading,
      scopeTitle,
      scopeDescription,
      failureMessage,
      base,
      notes,
      tiles,
      chartList,
      reload,
      switchToOverall,
      onUserChange,
      searchUsers,
      filterUserOption,
      formatMoneyValue,
    }
  },
})
</script>

<style scoped>
.portrait-switcher {
  margin-bottom: var(--g-auto);
}

.portrait-base {
  margin-bottom: var(--g-auto);
}

.portrait-charts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  gap: var(--g-auto);
}

.portrait-chart__title {
  font-size: 14px;
  font-weight: 600;
  margin: 0 0 var(--g-auto);
}

.portrait-chart__body {
  max-height: 260px;
  overflow: auto;
}

.portrait-chart__table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.portrait-chart__table th,
.portrait-chart__table td {
  padding: 4px 8px;
  border-bottom: 1px solid rgba(0, 0, 0, 0.06);
  text-align: left;
}

.portrait-chart__table td:last-child,
.portrait-chart__table th:last-child {
  text-align: right;
}

.portrait-notes {
  margin-top: var(--g-auto);
  font-size: 13px;
}

.portrait-notes ul {
  padding-left: 20px;
  margin: 0;
}
</style>
