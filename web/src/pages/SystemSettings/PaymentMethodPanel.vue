<template>
  <ConsolePanel title="支付方式管理" description="维护充值支付方式的名称、图标、易支付通道、启停状态和排序。">
    <template #actions>
      <a-space wrap>
        <a-button :loading="loading" @click="$emit('refresh')">刷新</a-button>
        <a-button type="primary" @click="$emit('create')">新增支付方式</a-button>
      </a-space>
    </template>
    <a-alert type="info" show-icon class="page-alert" message="订单只保存支付方式名称快照；如需调整实际通道值、图标或排序，请直接在这里编辑。" />
    <ConsoleTableShell label="支付方式管理">
      <a-table bordered table-layout="fixed" :data-source="rows" :columns="columns" :loading="loading" :pagination="false" row-key="id" :scroll="{ x: sumTableColumnWidths(columns) }">
        <template #bodyCell="{ column, record }">
          <div v-if="column.key === 'iconSvg'" class="icon-cell">
            <div v-if="record.iconSvg" class="icon-preview" v-html="record.iconSvg"></div>
            <span v-else>-</span>
          </div>
          <a-tag v-else-if="column.key === 'epayType'" color="cyan">{{ record.epayType || '-' }}</a-tag>
          <a-badge v-else-if="column.key === 'enabled'" :status="record.enabled ? 'success' : 'default'" :text="record.enabled ? '启用' : '停用'" />
          <span v-else-if="column.key === 'remark'">{{ record.remark || '-' }}</span>
          <a-space v-else-if="column.key === 'operation'" size="small" wrap>
            <a-button type="link" @click="$emit('edit', record)">编辑</a-button>
            <a-popconfirm ok-text="确定" cancel-text="取消" title="确定删除该支付方式吗？" @confirm="$emit('delete', record.id)">
              <a-button type="link" danger>删除</a-button>
            </a-popconfirm>
          </a-space>
        </template>
        <template #emptyText><a-empty description="当前还没有支付方式，请先新增可用渠道。" /></template>
      </a-table>
    </ConsoleTableShell>
  </ConsolePanel>
</template>

<script>
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'

export default {
  name: 'PaymentMethodPanel',
  props: {
    rows: { type: Array, default: () => [] },
    columns: { type: Array, default: () => [] },
    loading: Boolean,
  },
  emits: ['refresh', 'create', 'edit', 'delete'],
  methods: { sumTableColumnWidths },
}
</script>

<style scoped>
.page-alert { margin-bottom: 16px; }
.icon-cell { min-height: 36px; display: flex; align-items: center; justify-content: center; }
.icon-preview :deep(svg) { width: 28px; height: 28px; display: block; }
</style>
