<template>
  <!--
    单张图表的容器（t200「请添加图表等，这样会直观一些」）。

    两种形态：
      - options 为 null（后端返回空数组）→ 渲染「暂无数据」占位，**不画图**；
      - options 有值 → 挂 echarts 实例。

    为什么空态在这一层统一处理：三个图的空态判据完全一样（数据层给 empty:true），
    分散到页面会导致「某个图忘了判断」时画出全 0 图，而那种 bug 肉眼很难发现。
  -->
  <section class="portrait-chart">
    <h3 class="portrait-chart__title">{{ title }}</h3>
    <p v-if="hint" class="portrait-chart__hint">{{ hint }}</p>
    <a-empty v-if="!options" :description="emptyHint" class="portrait-chart__empty" />
    <div
      v-else
      ref="host"
      class="portrait-chart__canvas"
      role="img"
      :aria-label="ariaLabel"
    />
  </section>
</template>

<script>
// 单图组件：负责「建实例 / 换数据 / 跟随主题重建 / 卸载时 dispose」。
//
// # 用 Canvas 渲染而不是 SVG
// 三个图的数据量都很小（≤24 个桶），但 canvas 在移动端滚动时更稳；
// 更重要的是 canvas 不吃 DOM，页面在窄屏不会有横向溢出。
//
// # 主题
// ECharts 是 canvas，CSS 变量对它无效 —— 颜色必须由父组件用
// getComputedStyle 读出来传进 options。所以主题切换时父组件重建 options，
// 本组件使用新的 options 对象更新图表。
//
// # 为什么 echarts 是动态 import
// 图表不在首屏路径上（进画像页才用），静态 import 会把 echarts 打进主 chunk，
// 撞 check-bundle.cjs 的 appJsGzip: 40KB 限额。动态 import 会单独切一个 chunk。
import { defineComponent, onBeforeUnmount, ref, watch } from 'vue'
import { PORTRAIT_EMPTY_HINT } from '@/utils/portraitCharts.mjs'

let echartsPromise
function loadECharts() {
  echartsPromise ||= import('./portraitECharts.mjs').catch(error => {
    echartsPromise = undefined
    throw error
  })
  return echartsPromise
}

export default defineComponent({
  name: 'PortraitChart',
  props: {
    title: { type: String, required: true },
    // null = 空数据 → 显示占位
    options: { type: Object, default: null },
    // 口径说明（例如「横轴为小时桶，后端暂无按天趋势」），如实标注、不隐藏限制
    hint: { type: String, default: '' },
    emptyHint: { type: String, default: PORTRAIT_EMPTY_HINT },
    // 无障碍标签：canvas 本身对读屏不可见，必须给 role=img + aria-label
    ariaLabel: { type: String, default: '' },
  },
  setup(props) {
    const host = ref(null)
    let chart = null
    let target = null
    let resizeObserver = null
    let intersectionObserver = null
    let frame = null
    let renderedOptions = null

    async function update(element) {
      const echarts = chart ? null : await loadECharts()
      // 动态加载期间可能关闭弹窗、切换账号或变成空数据。
      if (element !== target || !props.options) return
      const width = element.clientWidth
      const height = element.clientHeight
      if (!width || !height) return
      if (!chart) {
        chart = echarts.init(element, null, { renderer: 'canvas', width, height })
      } else if (chart.getWidth() !== width || chart.getHeight() !== height) {
        chart.resize({ width, height })
      }
      if (renderedOptions !== props.options) {
        chart.setOption(props.options, { notMerge: true })
        renderedOptions = props.options
      }
    }

    function scheduleUpdate() {
      if (!target || intersectionObserver || frame !== null) return
      const element = target
      frame = requestAnimationFrame(() => {
        frame = null
        return update(element).catch(error => { console.error('画像图表加载失败', error) })
      })
    }

    function clearHost() {
      target = null
      resizeObserver?.disconnect()
      intersectionObserver?.disconnect()
      resizeObserver = intersectionObserver = null
      if (frame !== null) cancelAnimationFrame(frame)
      frame = null
      chart?.dispose()
      chart = renderedOptions = null
    }

    watch(host, element => {
      clearHost()
      if (!element) return
      target = element
      if (typeof ResizeObserver === 'function') {
        resizeObserver = new ResizeObserver(scheduleUpdate)
        resizeObserver.observe(element)
      }
      if (typeof IntersectionObserver === 'function') {
        const observer = new IntersectionObserver(entries => {
          if (target !== element || !entries.some(entry => entry.isIntersecting)) return
          observer.disconnect()
          intersectionObserver = null
          scheduleUpdate()
        })
        intersectionObserver = observer
        observer.observe(element)
      }
      scheduleUpdate()
    }, { flush: 'post' })
    watch(() => props.options, scheduleUpdate, { flush: 'post' })
    onBeforeUnmount(clearHost)

    return { host }
  },
})
</script>

<style scoped>
.portrait-chart {
  /* min-width: 0 是关键：grid/flex 子项默认 min-width:auto，
     长标签会把容器撑出父级，产生横向滚动条（任务书要求移动端无横向滚动）。 */
  min-width: 0;
}

.portrait-chart__title {
  font-size: 14px;
  font-weight: 600;
  margin: 0 0 4px;
}

.portrait-chart__hint {
  font-size: 12px;
  color: var(--text-muted);
  margin: 0 0 8px;
}

.portrait-chart__empty {
  /* 与画布同高，避免空态时卡片高度突然塌陷造成页面跳动 */
  min-height: 220px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.portrait-chart__canvas {
  width: 100%;
  /* 固定高度 + 宽度自适应：窄屏下 canvas 跟随容器缩放，不产生横向溢出 */
  height: 240px;
  min-width: 0;
}

@media (max-width: 480px) {
  .portrait-chart__canvas {
    height: 200px;
  }
}
</style>
