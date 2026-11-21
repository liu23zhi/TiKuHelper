<template>
  <div class="console-page user-portrait-page">
    <ConsolePageHeading title="整体用户画像（全站）" description="全站用户的行为画像总览。">
      <template #actions>
        <a-button @click="reload" :loading="loading">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert v-if="!canViewPortrait" type="warning" show-icon class="page-alert" message="用户画像仅管理员可见。" />

    <RequestFailureAlert
      v-if="canViewPortrait && failure"
      :key="failureContextKey"
      :failure="failure"
      message="获取整体用户画像失败"
      retryable
      :busy="loading"
      :context-key="failureContextKey"
      @retry="reload"
    />

    <div v-if="canViewPortrait && loading && !portrait" class="portrait-loading" role="status" aria-live="polite">
      <a-spin />
      <span>正在读取全站画像</span>
    </div>
    <a-empty
      v-else-if="canViewPortrait && !portrait && !failure"
      description="暂无全站画像数据"
      class="portrait-empty"
    />

    <template v-if="canViewPortrait && portrait">
      <ConsoleSummaryGrid class="portrait-summary">
        <a-card v-for="tile in tiles" :key="tile.key" :bordered="false" class="summary-card">
          <a-statistic :title="tile.title" :value="tile.missing ? PORTRAIT_EMPTY_HINT : tile.value" />
        </a-card>
      </ConsoleSummaryGrid>

      <section class="portrait-section" aria-labelledby="portrait-activity-title">
        <div class="portrait-section__heading">
          <h2 id="portrait-activity-title">使用与账单分布</h2>
          <p>基于服务端账单记录聚合；免费额度内未产生账单的调用不计入时段与区域统计。</p>
        </div>
        <div class="portrait-charts">
          <PortraitChart
            v-for="chart in chartList"
            :key="chart.key"
            :title="chart.title"
            :hint="chart.hint"
            :options="chart.options"
            :aria-label="chart.ariaLabel"
          />
        </div>
      </section>

      <section class="portrait-section" aria-labelledby="portrait-breakdown-title">
        <div class="portrait-section__heading">
          <h2 id="portrait-breakdown-title">全站分布明细</h2>
          <p>区域为账单 IP 的本地归属地聚合；注册地区与注册来源按用户记录统计。</p>
        </div>
        <div class="portrait-tables">
          <section v-for="table in tableViewList" :key="table.key" class="portrait-table-panel">
            <h3>{{ table.title }}</h3>
            <a-empty v-if="table.empty" :description="PORTRAIT_EMPTY_HINT" />
            <div v-else class="portrait-table-scroll">
              <table class="portrait-table">
                <thead>
                  <tr>
                    <th v-for="column in table.columns" :key="column.key" :class="{ 'is-right': column.align === 'right' }" scope="col">
                      {{ column.title }}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="(row, index) in table.rows" :key="`${table.key}-${index}`">
                    <td v-for="column in table.columns" :key="column.key" :class="{ 'is-right': column.align === 'right' }">
                      {{ column.money && row[column.key] !== null && row[column.key] !== undefined ? formatMoneyValue(row[column.key]) : displayCell(row[column.key]) }}
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>
        </div>
      </section>

      <section v-if="notes.length" class="portrait-section portrait-notes" aria-labelledby="portrait-notes-title">
        <h2 id="portrait-notes-title">数据口径说明</h2>
        <ul>
          <li v-for="(note, index) in notes" :key="index">{{ note }}</li>
        </ul>
      </section>

      <p class="portrait-generated-at">生成时间：{{ generatedAt }}</p>
    </template>
  </div>
</template>

<script>
import { computed, defineComponent, inject, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { message } from 'ant-design-vue'

import { getPortraitOverview } from '@/api/UserPortraitApi'
import ConsoleSummaryGrid from '@/components/ConsoleSummaryGrid.vue'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import PortraitChart from './PortraitChart.vue'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'
import { useCurrentUserDisplay, isAdminUser } from '@/utils/currentUser'
import { currentUserIdentity, useCurrentUserRequestScope } from '@/utils/currentUserScope'
import { THEME_CONTROLLER_KEY } from '@/utils/themeMode.mjs'
import {
  PORTRAIT_EMPTY_HINT,
  buildPortraitBreakdownTables,
  buildPortraitCharts,
  buildOverallPortraitTiles,
} from '@/utils/portraitCharts.mjs'
import {
  PORTRAIT_HOURS_TITLE,
  PORTRAIT_TYPES_TITLE,
  PORTRAIT_WEEKDAYS_TITLE,
  buildPortraitAllOptions,
  readPortraitPalette,
} from './portraitChartOptions.mjs'

export default defineComponent({
  name: 'UserPortrait',
  components: { ConsolePageHeading, ConsoleSummaryGrid, RequestFailureAlert, PortraitChart },
  setup() {
    const { currentUser } = useCurrentUserDisplay()
    const canViewPortrait = computed(() => Boolean(currentUserIdentity(currentUser.value)) && isAdminUser(currentUser.value))

    const themeController = inject(THEME_CONTROLLER_KEY, null)
    const themeResolved = ref(themeController?.getState().resolved || 'light')
    const unsubscribeTheme = themeController?.subscribe(state => { themeResolved.value = state.resolved })

    const portrait = shallowRef(null)
    const loading = ref(false)
    const failure = shallowRef(null)
    const failureContextKey = ref(0)
    let requestVersion = 0

    function clearOwnedData() {
      requestVersion += 1
      portrait.value = null
      loading.value = false
      failure.value = null
      failureContextKey.value += 1
    }
    const { captureDataOwner, canLoadForCurrentUser } = useCurrentUserRequestScope(currentUser, {
      clear: clearOwnedData,
      reset: () => {},
      reload: () => { void load() },
    })

    const palette = computed(() => {
      void themeResolved.value
      if (typeof window === 'undefined' || typeof window.getComputedStyle !== 'function') {
        return readPortraitPalette({ element: null })
      }
      return readPortraitPalette({
        element: document.documentElement,
        readStyle: (el, prop) => window.getComputedStyle(el).getPropertyValue(prop),
      })
    })

    const notes = computed(() => (Array.isArray(portrait.value?.notes) ? portrait.value.notes : []))
    const tiles = computed(() => buildOverallPortraitTiles(portrait.value || {}, {
      money: value => formatMoneyValue(value),
      count: value => Number(value).toLocaleString('zh-CN'),
    }))
    const charts = computed(() => buildPortraitCharts(portrait.value || {}))
    const chartOptions = computed(() => buildPortraitAllOptions(charts.value, palette.value))
    const chartList = computed(() => [
      {
        key: 'daily', title: '近 30 天账单趋势', hint: '按北京时间的自然日统计，仅展示有账单的日期。',
        ariaLabel: '近三十天账单记录数量趋势', options: chartOptions.value.daily,
      },
      {
        key: 'hours', title: PORTRAIT_HOURS_TITLE,
        hint: '只显示有账单记录的小时，不补齐空时段。',
        ariaLabel: '按小时统计的账单记录分布', options: chartOptions.value.hours,
      },
      {
        key: 'types', title: PORTRAIT_TYPES_TITLE,
        hint: '按账单类型展示记录数；金额明细见下方表格。',
        ariaLabel: '账单类型分布环形图', options: chartOptions.value.types,
      },
      {
        key: 'weekdays', title: PORTRAIT_WEEKDAYS_TITLE,
        hint: '只显示出现过账单记录的星期。',
        ariaLabel: '按星期统计的账单记录分布', options: chartOptions.value.weekdays,
      },
      ...[
        ['regions', '账单来源地区', '按账单 IP 归属地统计记录数，未解析的地址归入未知地区。'],
        ['tickets', '工单处理状态', '全部工单按当前状态统计。'],
        ['registerSources', '账号注册来源', '按未归档账号的注册来源统计用户数。'],
        ['registerRegions', '账号注册地区', '按未归档账号的注册 IP 归属地统计用户数。'],
        ['userStatuses', '账号状态', '按未归档账号的当前状态统计用户数。'],
        ['userRoles', '账号角色', '普通用户与管理员均以实际账号记录为准。'],
      ].map(([key, title, hint]) => ({ key, title, hint, ariaLabel: `${title}分布图`, options: chartOptions.value[key] })),
    ])
    const tableViewList = computed(() => buildPortraitBreakdownTables(portrait.value || {}))
    const generatedAt = computed(() => {
      const value = portrait.value?.generatedAt
      const date = value ? new Date(value) : null
      return date && Number.isFinite(date.getTime())
        ? date.toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false })
        : '未知'
    })

    function clearFailure() {
      failure.value = null
      failureContextKey.value += 1
    }

    async function load() {
      if (!canViewPortrait.value || !canLoadForCurrentUser()) return false
      const version = ++requestVersion
      const owns = captureDataOwner()
      const isCurrent = () => owns() && version === requestVersion && canViewPortrait.value
      loading.value = true
      clearFailure()
      try {
        const response = await getPortraitOverview()
        if (!isCurrent()) return false
        if (Number(response?.status) === 200 && response.data && typeof response.data === 'object' && response.data.scope === 'overall') {
          portrait.value = response.data
          return true
        }
        failure.value = ensureRequestFailure(response)
        portrait.value = null
        return false
      } catch (error) {
        if (!isCurrent()) return false
        failure.value = ensureRequestFailure(error)
        portrait.value = null
        return false
      } finally {
        if (isCurrent()) loading.value = false
      }
    }

    function reload() {
      return load()
    }

    function displayCell(value) {
      if (value === null || value === undefined || value === '') return '—'
      return typeof value === 'number' ? value.toLocaleString('zh-CN') : value
    }

    onMounted(() => {
      if (!canViewPortrait.value) {
        message.warning('用户画像仅管理员可见')
        return
      }
      void load()
    })
    onBeforeUnmount(() => {
      unsubscribeTheme?.()
      clearOwnedData()
    })

    return {
      PORTRAIT_EMPTY_HINT,
      canViewPortrait,
      portrait,
      loading,
      failure,
      failureContextKey,
      tiles,
      chartList,
      tableViewList,
      notes,
      generatedAt,
      reload,
      displayCell,
      formatMoneyValue,
    }
  },
})
</script>

<style scoped>
.page-alert { margin-bottom: var(--g-auto); }
.portrait-loading,
.portrait-empty {
  min-height: 180px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  color: var(--text-muted);
}
.portrait-summary { margin-bottom: var(--g-auto); }
.portrait-section {
  min-width: 0;
  padding: 20px 0;
  border-top: 1px solid var(--border);
}
.portrait-section__heading { margin-bottom: 16px; }
.portrait-section__heading h2,
.portrait-notes h2 {
  margin: 0;
  color: var(--text);
  font-size: 17px;
  font-weight: 600;
}
.portrait-section__heading p {
  margin: 5px 0 0;
  color: var(--text-muted);
  font-size: 13px;
  line-height: 1.6;
}
.portrait-charts,
.portrait-tables {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 320px), 1fr));
  gap: 20px 24px;
}
.portrait-table-panel { min-width: 0; }
.portrait-table-panel h3 {
  margin: 0 0 10px;
  color: var(--text);
  font-size: 14px;
  font-weight: 600;
}
.portrait-table-scroll { max-height: 300px; overflow: auto; }
.portrait-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.portrait-table th,
.portrait-table td {
  padding: 8px 10px;
  border-bottom: 1px solid var(--border);
  text-align: left;
  overflow-wrap: anywhere;
}
.portrait-table th { color: var(--text-muted); font-weight: 500; }
.portrait-table .is-right { text-align: right; white-space: nowrap; }
.portrait-notes ul {
  margin: 8px 0 0;
  padding-left: 20px;
  color: var(--text-muted);
  font-size: 13px;
  line-height: 1.7;
}
.portrait-generated-at {
  margin: 0;
  padding-top: 12px;
  color: var(--text-muted);
  font-size: 12px;
}
@media (max-width: 480px) {
  .portrait-section { padding: 16px 0; }
  .portrait-table th,
  .portrait-table td { padding: 7px 6px; }
}
</style>
