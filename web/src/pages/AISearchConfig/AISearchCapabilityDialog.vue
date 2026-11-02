<template>
  <a-modal :open="open" title="深度思考能力" :footer="null" width="960px" @update:open="$emit('update:open', $event)">
    <a-spin :spinning="loading">
      <a-alert v-if="error" type="warning" show-icon class="page-alert" :message="error" />
      <section v-if="detail" class="capability-detail-panel">
        <header class="capability-detail-panel__header">
          <div class="capability-detail-panel__identity">
            <h3>{{ detail.engine.display_name || detail.engine.name }}</h3>
            <p>{{ detail.engine.description || '暂无搜索引擎描述' }}</p>
          </div>
          <a-tag :color="detail.engine.available ? 'green' : 'red'">{{ detail.engine.available ? '可用' : '不可用' }}</a-tag>
        </header>
        <dl class="capability-detail-facts">
          <div><dt>引擎标识</dt><dd>{{ detail.engine.name || '-' }}</dd></div>
          <div><dt>配置版本</dt><dd>{{ detail.engine.version || '-' }}</dd></div>
          <div><dt>默认结果数</dt><dd>{{ detail.limits.default ?? '-' }}</dd></div>
          <div><dt>最大结果数</dt><dd>{{ detail.limits.max ?? '-' }}</dd></div>
        </dl>
        <div class="capability-detail-list">
          <h4>支持的能力</h4>
          <a-space v-if="detail.features.length" wrap><a-tag v-for="feature in detail.features" :key="feature">{{ feature }}</a-tag></a-space>
          <span v-else class="capability-detail-empty">未提供</span>
        </div>
        <div class="capability-detail-list">
          <h4>支持的参数</h4>
          <a-space v-if="detail.supportedParameters.length" wrap><a-tag v-for="parameter in detail.supportedParameters" :key="parameter">{{ parameter }}</a-tag></a-space>
          <span v-else class="capability-detail-empty">未提供</span>
        </div>
      </section>
      <h3 v-if="cards.length || rows.length" class="capability-usage-heading">UAPI Pro 服务商端点统计</h3>
      <p v-if="cards.length || rows.length" class="capability-usage-note">以下数字由 UAPI Pro 返回，表示服务商端点的累计调用次数，不是本服务本地的调用次数；本地账单和 OCR 缓存条目请分别查看账单明细与 OCR 缓存管理。</p>
      <div v-if="cards.length" class="capability-status-grid">
        <div v-for="card in cards" :key="card.key" class="capability-status-card">
          <div class="capability-status-card__header"><span>{{ card.title }}</span><a-tag :color="card.color">{{ card.status }}</a-tag></div>
          <div class="capability-status-card__meta">{{ card.detail }}</div>
          <div v-if="card.count !== null" class="capability-status-card__count">服务商累计调用 {{ card.count }} 次</div>
        </div>
      </div>
      <a-empty v-else-if="!loading" description="服务商暂未返回端点统计" />
      <ConsoleTableShell v-if="rows.length" label="UAPI Pro 服务商端点统计明细">
        <a-table size="small" bordered table-layout="fixed" row-key="path" :pagination="false" :data-source="rows" :columns="columns" :scroll="{ x: sumTableColumnWidths(columns) }" />
      </ConsoleTableShell>
    </a-spin>
  </a-modal>
</template>

<script>
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

export default {
  name: 'AISearchCapabilityDialog',
  props: {
    open: Boolean,
    loading: Boolean,
    error: { type: String, default: '' },
    detail: { type: Object, default: null },
    cards: { type: Array, default: () => [] },
    rows: { type: Array, default: () => [] },
    columns: { type: Array, default: () => [] },
  },
  emits: ['update:open'],
  methods: { sumTableColumnWidths },
}
</script>

<style scoped>
.page-alert { margin-bottom: 16px; }
.capability-detail-panel { margin-bottom: 24px; padding-bottom: 20px; border-bottom: 1px solid var(--console-border); }
.capability-detail-panel__header, .capability-status-card__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.capability-detail-panel__identity { min-width: 0; }
.capability-detail-panel__identity h3 { margin: 0 0 6px; font-size: 17px; }
.capability-detail-panel__identity p { margin: 0; color: var(--console-muted); overflow-wrap: anywhere; }
.capability-detail-facts { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 16px; margin: 20px 0; }
.capability-detail-facts dt, .capability-detail-list h4 { margin: 0 0 6px; color: var(--console-muted); font-size: 12px; font-weight: 600; }
.capability-detail-facts dd { margin: 0; overflow-wrap: anywhere; }
.capability-detail-list + .capability-detail-list { margin-top: 16px; }
.capability-detail-empty { color: var(--console-muted); }
.capability-usage-heading { margin: 20px 0 4px; font-size: 15px; }
.capability-usage-note { margin: 0 0 12px; color: var(--console-muted); font-size: 12px; line-height: 1.7; }
.capability-status-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(240px, 100%), 1fr)); gap: 12px; margin-bottom: 16px; }
.capability-status-card { min-width: 0; padding: 16px; border: 1px solid var(--console-border); border-radius: 8px; background: var(--console-subtle); }
.capability-status-card__header { font-weight: 600; }
.capability-status-card__meta { margin-top: 10px; color: var(--console-muted); overflow-wrap: anywhere; }
.capability-status-card__count { margin-top: 12px; color: var(--console-primary); font-variant-numeric: tabular-nums; }
</style>
