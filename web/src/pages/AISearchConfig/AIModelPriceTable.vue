<template>
<div class="price-table-wrap">
  <ConsoleTableShell label="模型价格表">
  <a-table
    bordered
    row-key="id"
    :data-source="rows"
    :columns="priceColumns"
    :loading="loading"
    :pagination="{ pageSize: 8, size: 'small' }"
    table-layout="fixed"
    :scroll="{ x: sumTableColumnWidths(priceColumns) }"
  >
    <template #emptyText>
      <p v-if="loading" role="status">正在读取模型价格，请稍候。</p>
      <p v-else-if="readFailure" role="status">暂时无法读取模型价格，请使用上方重试。</p>
      <p v-else-if="!readConfirmed" role="status">模型价格表尚未确认，请使用上方刷新。</p>
      <a-empty v-else description="暂无模型价格。" />
    </template>
    <template #status="{ record }">
      <a-tag :color="record.status === 'enabled' ? 'green' : 'default'">
        {{ record.status === 'enabled' ? '启用' : '停用' }}
      </a-tag>
    </template>
    <template #bodyCell="{ column, record }">
      <span v-if="['inputPrice', 'cachedPrice', 'cacheWritePrice', 'outputPrice'].includes(column.key)">{{ formatPriceAmount(record[column.key]) }}</span>
      <span v-else-if="column.key === 'contextWindowTokens'">{{ formatAIModelContextWindowTokens(record.contextWindowTokens) }}</span>
      <span v-else-if="column.key === 'multiplier'" :class="{ 'multiplier-cell--custom': Number(record.multiplier || 1) !== 1 }">
        ×{{ formatMultiplier(record.multiplier) }}
      </span>
      <span v-else-if="column.key === 'remark'">{{ record.remark || '-' }} <a-tag v-if="record.isUserModified" color="orange">已改</a-tag></span>
      <a-space v-else-if="column.key === 'action'" size="small" wrap>
        <a-button type="link" style="padding: 0" @click="$emit('edit', record)">编辑</a-button>
        <a-popconfirm title="确定删除该模型价格吗？" ok-text="确定" cancel-text="取消" @confirm="$emit('delete', record)">
          <a-button type="link" danger style="padding: 0">删除</a-button>
        </a-popconfirm>
      </a-space>
    </template>
  </a-table>
  </ConsoleTableShell>
</div>
</template>

<script>
import { formatMoneyCny, formatMoneyValue } from '@/utils/moneyFormat.mjs'
import { formatAIModelContextWindowTokens } from '@/utils/aiSearchModelHelpers.mjs'
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'
import ConsoleTableShell from '@/components/ConsoleTableShell.vue'

export default {
  name: 'AIModelPriceTable',
  components: { ConsoleTableShell },
  props: {
    rows: { type: Array, default: () => [] },
    loading: { type: Boolean, default: false },
    readFailure: { default: null },
    readConfirmed: { type: Boolean, default: false },
  },
  emits: ['edit', 'delete'],
  data() {
    return {
      priceColumns: [
      { title: 'ID', dataIndex: 'id', key: 'id', width: 60 },
      { title: '模型 ID', dataIndex: 'modelName', key: 'modelName', width: 190, ellipsis: true },
      { title: '显示名称', dataIndex: 'displayName', key: 'displayName', width: 165, ellipsis: true },
      { title: '上下文窗口（Token）', dataIndex: 'contextWindowTokens', key: 'contextWindowTokens', width: 150, align: 'right' },
      { title: '输入（￥/M）', dataIndex: 'inputPrice', key: 'inputPrice', width: 105, align: 'right' },
      { title: '缓存输入（￥/M）', dataIndex: 'cachedPrice', key: 'cachedPrice', width: 120, align: 'right' },
      { title: '缓存写入（￥/M）', dataIndex: 'cacheWritePrice', key: 'cacheWritePrice', width: 125, align: 'right' },
      { title: '输出（￥/M）', dataIndex: 'outputPrice', key: 'outputPrice', width: 105, align: 'right' },
      { title: '倍率', dataIndex: 'multiplier', key: 'multiplier', width: 85, align: 'right' },
      { title: '备注', dataIndex: 'remark', key: 'remark', width: 140, ellipsis: true },
      { title: '操作', dataIndex: 'action', key: 'action', fixed: 'right', width: 110 },
    ],
    }
  },
  methods: {
    sumTableColumnWidths,
    formatAIModelContextWindowTokens,
    formatPriceAmount(value) {
      const amount = Number(value)
      if (!Number.isFinite(amount)) {
        return '￥0'
      }
      // 价格表统一使用人民币符号，并按统一的 6 位精度去尾零展示。
      return formatMoneyCny(amount)
    },
    formatMultiplier(value) {
      const multiplier = Number(value)
      if (!Number.isFinite(multiplier) || multiplier <= 0) {
        return '1'
      }
      // 最多 4 位小数并去掉尾零，1 显示为 1 而不是 1.0000。
      return formatMoneyValue(multiplier)
    },
  },
}
</script>

<style scoped>
.price-table-wrap {
  width: 100%;
  max-width: 100%;
  min-width: 0;
  overflow-x: auto;
}

/* 非默认倍率用醒目标色，方便一眼看出哪些模型被加价/打折。 */
.multiplier-cell--custom {
  color: var(--console-warning);
  font-weight: 600;
}
</style>
