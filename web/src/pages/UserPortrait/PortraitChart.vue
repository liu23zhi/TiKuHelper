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
// 本组件 watch options 深比较后 setOption。
//
// # 为什么 echarts 是动态 import
// 图表不在首屏路径上（进画像页才用），静态 import 会把 echarts 打进主 chunk，
// 撞 check-bundle.cjs 的 appJsGzip: 40KB 限额。动态 import 会单独切一个 chunk。
import { defineComponent, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { PORTRAIT_EMPTY_HINT } from '@/utils/portraitCharts.mjs'

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
    let disposed = false
    // 串行化初始化：动态 import 是异步的，而 options 可能在其完成前变更多次。
    let pendingToken = 0

    async function ensureChart() {
      if (chart || disposed || !host.value) return chart
      // 动态 import：单独切 chunk，不占首屏。
      const echarts = await import('echarts/core')
      const [{ BarChart, PieChart, LineChart }, { GridComponent, TooltipComponent, LegendComponent }, { CanvasRenderer }] = await Promise.all([
        import('echarts/charts'),
        import('echarts/components'),
        import('echarts/renderers'),
      ])
      echarts.use([BarChart, PieChart, LineChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])
      // 等 import 期间组件可能已卸载（快速切换视角时会发生）。
      if (disposed || !host.value) return null
      chart = echarts.init(host.value, null, { renderer: 'canvas' })
      return chart
    }

    async function render() {
      if (disposed) return
      if (!props.options) {
        // 空数据：销毁旧实例，避免上一个视角的图残留在 DOM 里。
        // 这一步是「空数据不画图」的执行点 —— 少了它，切到空视角会看到旧图。
        disposeChart()
        return
      }
      const token = ++pendingToken
      await nextTick()
      const instance = await ensureChart()
      // 只让最后一次 render 生效：旧 token 的 setOption 会覆盖新数据。
      if (!instance || disposed || token !== pendingToken) return
      instance.setOption(props.options, true)
      instance.resize()
    }

    function disposeChart() {
      if (chart) {
        chart.dispose()
        chart = null
      }
    }

    // 容器尺寸变化（侧边栏折叠 / 窗口缩放 / 移动端横竖屏）要重新布局。
    let observer = null
    function observeResize() {
      if (typeof ResizeObserver !== 'function' || !host.value) return
      observer = new ResizeObserver(() => {
        if (chart) chart.resize()
      })
      observer.observe(host.value)
    }

    watch(() => props.options, () => { render() }, { deep: true })
    watch(host, (value, previous) => {
      if (previous && !value) disposeChart()
      if (value) {
        observeResize()
        render()
      }
    })

    // 首次挂载：options 已经就绪（父组件 await 数据后才渲染本组件）。
    nextTick(() => {
      observeResize()
      render()
    })

    onBeforeUnmount(() => {
      disposed = true
      observer?.disconnect()
      observer = null
      disposeChart()
    })

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
