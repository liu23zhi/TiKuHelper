<template>
  <section class="search-history" data-status-history :data-history-scope="scope" :aria-labelledby="titleId" @keydown.esc.stop.prevent="closeZoom">
    <div class="search-history__header">
      <h3 :id="titleId">最近 24 小时</h3>
      <span>{{ scope === 'admin' ? '全站 API' : '搜题接口' }} · 每格 5 分钟 · Asia/Shanghai</span>
    </div>
    <div
      class="search-history__band"
      data-search-history-band data-status-history-band
      role="slider"
      tabindex="0"
      aria-label="最近24小时请求状态，左右方向键切换时间段"
      aria-orientation="horizontal"
      :aria-valuemin="0"
      :aria-valuemax="lastIndex"
      :aria-valuenow="selected"
      :aria-valuetext="active.detail"
      @focus="opened = true"
      @keydown="moveSelection"
      @pointermove="inspectPointer"
      @click="openAtPosition"
    >
      <span
        v-for="(bucket, index) in view.buckets"
        :key="index"
        class="search-history__cell"
        data-search-history-cell data-status-history-cell
        :data-history-tier="bucket.tier"
        :style="{ backgroundColor: bucket.color }"
        aria-hidden="true"
      ></span>
    </div>
    <div v-if="view.axis.length" class="search-history__axis">
      <span v-for="label in view.axis" :key="label">{{ label }}</span>
    </div>
    <p class="search-history__detail" data-search-history-detail data-status-history-active aria-live="polite">{{ active.detail }}</p>
    <button
      ref="toggle"
      class="search-history__action"
      type="button"
      data-history-zoom-toggle
      :aria-expanded="opened"
      :aria-controls="zoomId"
      @click="opened ? closeZoom() : opened = true"
    >{{ opened ? '收起放大历史' : '查看放大历史' }}</button>

    <section
      v-show="opened"
      :id="zoomId"
      ref="zoom"
      class="search-history__zoom"
      :data-history-zoom="opened ? '' : null"
      :aria-labelledby="zoomTitleId"
    >
      <div class="search-history__header">
        <h4 :id="zoomTitleId">放大历史与分钟样本</h4>
        <button type="button" class="search-history__action" data-history-close @click="closeZoom">关闭放大历史</button>
      </div>
      <p v-if="!view.available" class="search-history__hint">{{ view.hint }}</p>
      <template v-else>
        <p class="search-history__hint">放大刻度：{{ zoomCells[0].bucket.label.split(' – ')[0] }} 至 {{ zoomCells.at(-1).bucket.label.split(' – ').at(-1) }}</p>
        <div class="search-history__zoom-grid" aria-label="放大后的五分钟区间">
          <button
            v-for="{ bucket, index } in zoomCells"
            :key="index"
            type="button"
            class="search-history__zoom-cell"
            :data-history-zoom-index="index"
            :aria-pressed="selected === index"
            :aria-label="bucket.detail"
            @click="selected = index"
          >
            <i :style="{ backgroundColor: bucket.color }" aria-hidden="true"></i>
            {{ bucket.label.split(' – ')[0].split(' ').at(-1) }}
          </button>
        </div>
        <div class="search-history__nav">
          <button type="button" class="search-history__action" data-history-prev :disabled="selected === 0" @click="shift(-1)">上一个 5 分钟</button>
          <button type="button" class="search-history__action" data-history-next :disabled="selected === lastIndex" @click="shift(1)">下一个 5 分钟</button>
        </div>
        <p class="search-history__detail">{{ active.detail }}</p>
        <ol class="search-history__minutes">
          <li v-for="row in active.minutes" :key="row.minute" :data-history-minute="row.minute">
            <strong>{{ row.time }}</strong> · {{ row.status }}
            <template v-if="row.request">
              · {{ count(row.request.totalRequests) }} 次请求（成功 {{ count(row.request.successRequests) }}）
              <span v-if="row.request.totalRequests"> · 平均延迟 {{ count(row.request.avgLatencyMs) }} ms</span>
              <p v-if="scope === 'admin'">失败 {{ count(row.request.failedRequests) }} · 上游失败 {{ count(row.request.upstreamFailures) }} · 超时失败 {{ count(row.request.timeoutFailures) }} · 认证失败 {{ count(row.request.authFailures) }} · 额度失败 {{ count(row.request.quotaFailures) }}</p>
            </template>
            <template v-if="scope === 'admin'">
              <p v-if="row.process">
                进程采样 {{ row.process.sampledAt }} · Goroutine {{ count(row.process.goroutines) }} · 运行 {{ count(row.process.uptimeSeconds) }} 秒 ·
                堆分配 {{ bytes(row.process.memory.heapAllocBytes) }} · 堆系统 {{ bytes(row.process.memory.heapSysBytes) }} ·
                系统内存 {{ bytes(row.process.memory.sysBytes) }} · 栈占用 {{ bytes(row.process.memory.stackInuseBytes) }} · GC {{ count(row.process.memory.gcCycles) }}
              </p>
              <p v-else>暂无进程采样</p>
            </template>
          </li>
        </ol>
      </template>
    </section>

    <p class="search-history__updated" data-search-history-updated>服务端时窗截至 {{ view.updatedText }}</p>
    <div class="search-history__legend" aria-label="色带图例">
      <span><i class="search-history__unknown"></i>暂无采样</span>
      <span><i class="search-history__empty"></i>已采样无请求</span>
      <span><i class="search-history__scale"></i>成功率 0% → 100%</span>
    </div>
    <p class="search-history__hint" data-search-history-hint>{{ view.hint }}</p>
    <p v-if="view.restartText" class="search-history__hint" data-history-restart>{{ view.restartText }}</p>
  </section>
</template>

<script>
import { computed, nextTick, ref, useId } from 'vue'
import { buildStatusHistory, formatStatusBytes, formatStatusCount, SEARCH_HISTORY_BUCKET_COUNT } from '@/utils/searchStatusHistory.mjs'

const ZOOM_GROUP = 12

export default {
  name: 'SearchStatusHistory',
  props: {
    payload: { type: Object, default: null },
    scope: { type: String, default: 'search', validator: value => ['search', 'admin'].includes(value) },
  },
  setup(props) {
    const id = 'status-history-' + useId()
    const lastIndex = SEARCH_HISTORY_BUCKET_COUNT - 1
    const selected = ref(lastIndex)
    const opened = ref(false)
    const toggle = ref(null)
    const zoom = ref(null)
    const view = computed(() => buildStatusHistory(props.payload, props.scope))
    const active = computed(() => view.value.buckets[selected.value])
    const zoomCells = computed(() => {
      const first = Math.floor(selected.value / ZOOM_GROUP) * ZOOM_GROUP
      return view.value.buckets.slice(first, first + ZOOM_GROUP).map((bucket, offset) => ({ bucket, index: first + offset }))
    })
    const shift = delta => { selected.value = Math.max(0, Math.min(lastIndex, selected.value + delta)) }
    const closeZoom = () => {
      if (!opened.value) return
      const returnFocus = zoom.value?.contains(document.activeElement)
      opened.value = false
      if (returnFocus) void nextTick(() => toggle.value?.focus())
    }
    const moveSelection = event => {
      const deltas = { ArrowLeft: -1, ArrowDown: -1, ArrowRight: 1, ArrowUp: 1 }
      if (event.key === 'Home') selected.value = 0
      else if (event.key === 'End') selected.value = lastIndex
      else if (Object.hasOwn(deltas, event.key)) shift(deltas[event.key])
      else if (event.key !== 'Enter' && event.key !== ' ') return
      opened.value = true
      event.preventDefault()
    }
    const selectPosition = event => {
      const rect = event.currentTarget.getBoundingClientRect()
      if (!rect.width || !Number.isFinite(event.clientX)) return
      const index = Math.floor((event.clientX - rect.left) / rect.width * SEARCH_HISTORY_BUCKET_COUNT)
      selected.value = Math.max(0, Math.min(lastIndex, index))
    }
    const inspectPointer = event => {
      if (event.pointerType !== 'mouse') return
      if (!window.matchMedia('(min-width: 761px)').matches) return
      selectPosition(event)
      opened.value = true
    }
    const openAtPosition = event => {
      selectPosition(event)
      opened.value = true
    }
    return {
      titleId: id, zoomId: id + '-zoom', zoomTitleId: id + '-zoom-title',
      view, active, selected, lastIndex, zoomCells, opened, toggle, zoom, shift,
      moveSelection, inspectPointer, openAtPosition, closeZoom, count: formatStatusCount, bytes: formatStatusBytes,
    }
  },
}
</script>

<style scoped>
.search-history { min-width: 0; margin-top: 24px; padding-top: 20px; border-top: 1px solid var(--console-border); }
.search-history__header { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 8px; margin-bottom: 12px; }
.search-history__header h3, .search-history__header h4 { margin: 0; color: var(--console-text); font-size: 15px; font-weight: 600; }
.search-history__header > span, .search-history__axis { color: var(--console-muted); font-size: 12px; }
.search-history__band { display: flex; width: 100%; height: 24px; overflow: hidden; border-radius: 6px; cursor: crosshair; background: #94a3b8; }
.search-history :is(button, [role='slider']):focus-visible { outline: 2px solid var(--console-primary, #1677ff); outline-offset: 2px; }
.search-history__cell { flex: 1 1 0; min-width: 0; height: 100%; }
.search-history__axis { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); text-align: center; gap: 2px; margin-top: 8px; overflow-wrap: anywhere; font-variant-numeric: tabular-nums; }
.search-history__axis span:first-child { text-align: left; }
.search-history__axis span:last-child { text-align: right; }
.search-history__detail { margin: 12px 0 8px; color: var(--console-text); font-size: 13px; line-height: 1.7; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.search-history__updated { margin: 12px 0 8px; color: var(--console-muted); font-size: 12px; font-variant-numeric: tabular-nums; }
.search-history__legend { display: flex; flex-wrap: wrap; gap: 8px 16px; color: var(--console-muted); font-size: 12px; }
.search-history__legend > span { display: inline-flex; align-items: center; gap: 6px; }
.search-history__legend i { display: inline-block; width: 14px; height: 8px; border-radius: 2px; }
.search-history__legend .search-history__unknown { background: #94a3b8; }
.search-history__legend .search-history__empty { background: #d1d5db; }
.search-history__legend .search-history__scale { width: 64px; background: linear-gradient(90deg, #7f1d1d, #dc2626, #f97316, #eab308, #22c55e, #15803d); }
.search-history__hint { margin: 8px 0 0; color: var(--console-muted); font-size: 12px; line-height: 1.7; }
.search-history__zoom { margin-top: 10px; padding: 12px; border: 1px solid var(--console-border, #d1d5db); border-radius: 10px; color: var(--console-text); background: var(--console-surface, transparent); }
.search-history__zoom-grid { display: grid; grid-template-columns: repeat(12, minmax(0, 1fr)); gap: 4px; margin-top: 10px; }
.search-history__action, .search-history__zoom-cell { border: 1px solid var(--console-border, #d1d5db); background: var(--console-surface, transparent); color: var(--console-text, inherit); border-radius: 6px; min-height: 36px; cursor: pointer; }
.search-history__action { padding: 4px 10px; }
.search-history__action:disabled { cursor: not-allowed; opacity: .5; }
.search-history__zoom-cell { min-width: 0; padding: 3px 1px; font-size: 11px; }
.search-history__zoom-cell[aria-pressed='true'] { border-color: var(--console-primary, #1677ff); font-weight: 700; }
.search-history__zoom-cell i { display: block; width: 90%; height: 8px; margin: 0 auto 3px; border-radius: 2px; }
.search-history__nav { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px; }
.search-history__minutes { margin: 0; padding: 0; list-style: none; font-size: 12px; line-height: 1.65; overflow-wrap: anywhere; }
.search-history__minutes li + li { border-top: 1px solid var(--console-border, #d1d5db); }
.search-history__minutes li { padding: 5px 0; }
.search-history__minutes p { margin: 3px 0 0; color: var(--console-muted); }
@media (max-width: 760px) {
  .search-history__zoom-grid { grid-template-columns: repeat(6, minmax(0, 1fr)); }
  .search-history__action, .search-history__zoom-cell { min-height: 44px; }
}
</style>
