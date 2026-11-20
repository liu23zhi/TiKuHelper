<template>
  <section class="search-history" aria-labelledby="search-history-title">
    <div class="search-history__header">
      <h3 id="search-history-title">最近 24 小时</h3>
      <span>搜题接口 · 每格 5 分钟</span>
    </div>
    <div
      class="search-history__band"
      data-search-history-band
      role="slider"
      tabindex="0"
      aria-label="最近24小时搜题请求状态，左右方向键切换时间段"
      aria-orientation="horizontal"
      :aria-valuemin="0"
      :aria-valuemax="287"
      :aria-valuenow="selected"
      :aria-valuetext="active.detail"
      @keydown="moveSelection"
      @pointermove="selectPosition"
      @click="selectPosition"
    >
      <span
        v-for="(bucket, index) in view.buckets"
        :key="bucket.startMs"
        class="search-history__cell"
        data-search-history-cell
        :data-history-tier="bucket.tier"
        :style="{ backgroundColor: bucket.color }"
        :title="bucket.detail"
        :aria-hidden="true"
        @click="selected = index"
      ></span>
    </div>
    <div class="search-history__axis" aria-hidden="true"><span>24 小时前</span><span>12 小时前</span><span>现在</span></div>
    <p class="search-history__detail" data-search-history-detail aria-live="polite">{{ active.detail }}</p>
    <p class="search-history__updated" data-search-history-updated>数据更新于 {{ updatedText }}</p>
    <div class="search-history__legend" aria-label="色带图例">
      <span><i class="search-history__empty"></i>无请求</span>
      <span><i class="search-history__scale"></i>成功率 0% → 100%</span>
    </div>
    <p class="search-history__hint" data-search-history-hint>{{ view.hint }}</p>
  </section>
</template>

<script>
import { computed, ref } from 'vue'
import { buildSearchStatusHistory } from '@/utils/searchStatusHistory.mjs'

export default {
  name: 'SearchStatusHistory',
  props: { payload: { type: Object, default: null }, sampledAt: { type: Number, default: () => Date.now() } },
  setup(props) {
    const selected = ref(287)
    const view = computed(() => buildSearchStatusHistory(props.payload, props.sampledAt))
    const active = computed(() => view.value.buckets[selected.value])
    const updatedText = computed(() => new Date(props.sampledAt).toLocaleTimeString('zh-CN', {
      timeZone: 'Asia/Shanghai', hour: '2-digit', minute: '2-digit', hour12: false,
    }))
    const moveSelection = event => {
      const deltas = { ArrowLeft: -1, ArrowDown: -1, ArrowRight: 1, ArrowUp: 1 }
      if (event.key === 'Home') selected.value = 0
      else if (event.key === 'End') selected.value = 287
      else if (event.key in deltas) selected.value = Math.max(0, Math.min(287, selected.value + deltas[event.key]))
      else return
      event.preventDefault()
    }
    const selectPosition = event => {
      const rect = event.currentTarget.getBoundingClientRect()
      if (!rect.width || !Number.isFinite(event.clientX)) return
      selected.value = Math.max(0, Math.min(287, Math.floor((event.clientX - rect.left) / rect.width * 288)))
    }
    return { selected, view, active, updatedText, moveSelection, selectPosition }
  },
}
</script>

<style scoped>
.search-history { min-width: 0; margin-top: 24px; padding-top: 20px; border-top: 1px solid var(--console-border); }
.search-history__header { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 8px; margin-bottom: 12px; }
.search-history__header h3 { margin: 0; color: var(--console-text); font-size: 15px; font-weight: 600; }
.search-history__header > span, .search-history__axis { color: var(--console-muted); font-size: 12px; }
.search-history__band { display: flex; width: 100%; height: 24px; overflow: hidden; border-radius: 6px; cursor: crosshair; background: #d1d5db; }
.search-history__band:focus-visible { outline: 2px solid var(--console-primary, #1677ff); outline-offset: 4px; }
.search-history__cell { flex: 1 1 0; min-width: 0; height: 100%; }
.search-history__axis { display: flex; justify-content: space-between; margin-top: 8px; }
.search-history__detail { margin: 12px 0 8px; color: var(--console-text); font-size: 13px; line-height: 1.7; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.search-history__updated { margin: 0 0 8px; color: var(--console-muted); font-size: 12px; font-variant-numeric: tabular-nums; }
.search-history__legend { display: flex; flex-wrap: wrap; gap: 16px; color: var(--console-muted); font-size: 12px; }
.search-history__legend > span { display: inline-flex; align-items: center; gap: 6px; }
.search-history__legend i { display: inline-block; width: 14px; height: 8px; border-radius: 2px; }
.search-history__legend .search-history__empty { background: #d1d5db; }
.search-history__legend .search-history__scale { width: 64px; background: linear-gradient(90deg, #7f1d1d, #dc2626, #f97316, #eab308, #22c55e, #15803d); }
.search-history__hint { margin: 8px 0 0; color: var(--console-muted); font-size: 12px; line-height: 1.7; }
</style>
