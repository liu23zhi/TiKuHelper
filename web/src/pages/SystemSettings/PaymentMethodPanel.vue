<template>
  <ConsolePanel title="支付方式管理" description="维护充值支付方式的名称、图标、易支付通道、启停状态和排序。">
    <template #actions>
      <a-space wrap>
        <a-button :loading="loading" :disabled="disabled" :onClick="entry.bind(() => $emit('refresh'))">刷新</a-button>
        <a-button type="primary" :disabled="disabled" :onClick="entry.bind(event => $emit('create', event))">新增支付方式</a-button>
      </a-space>
    </template>
    <a-alert type="info" show-icon class="page-alert" message="订单只保存支付方式名称快照；如需调整实际通道值、图标或排序，请直接在这里编辑。" />
    <ConsoleTableShell label="支付方式管理">
      <a-table bordered table-layout="fixed" :data-source="rows" :columns="columns" :loading="loading" :pagination="false" row-key="id" :scroll="{ x: sumTableColumnWidths(columns) }">
        <template #bodyCell="{ column, record }">
          <div v-if="column.key === 'iconSvg'" class="icon-cell">
            <SafePaymentIcon v-if="record.iconSvg" :icon-svg="record.iconSvg" />
            <span v-else>-</span>
          </div>
          <a-tag v-else-if="column.key === 'epayType'" color="cyan">{{ record.epayType || '-' }}</a-tag>
          <a-badge v-else-if="column.key === 'enabled'" :status="record.enabled ? 'success' : 'default'" :text="record.enabled ? '启用' : '停用'" />
          <span v-else-if="column.key === 'remark'">{{ record.remark || '-' }}</span>
          <a-space v-else-if="column.key === 'operation'" size="small" wrap>
            <a-button type="link" :disabled="disabled" :onClick="entry.bind(event => $emit('edit', record, event))">编辑</a-button>
            <a-popconfirm ok-text="确定" cancel-text="取消" title="确定删除该支付方式吗？" :disabled="disabled" :onConfirm="entry.bind(() => $emit('delete', record.id))">
              <a-button type="link" danger :disabled="disabled">删除</a-button>
            </a-popconfirm>
          </a-space>
        </template>
        <template #emptyText><a-empty :description="error ? '支付方式尚未加载成功，请重试' : loading ? '正在读取支付方式…' : loaded ? '当前还没有支付方式，请先新增可用渠道。' : '尚未读取支付方式。'" /></template>
      </a-table>
    </ConsoleTableShell>
  </ConsolePanel>
</template>

<script>
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs'
import SafePaymentIcon from '@/components/SafePaymentIcon.vue'

export default {
  name: 'PaymentMethodPanel',
  components: { SafePaymentIcon },
  props: {
    rows: { type: Array, default: () => [] },
    columns: { type: Array, default: () => [] },
    loading: Boolean,
    loaded: { type: Boolean, default: true },
    error: { type: String, default: '' },
    disabled: Boolean,
    entry: { type: Object, default: () => ({ bind: fn => fn }) },
  },
  emits: ['refresh', 'create', 'edit', 'delete'],
  methods: { sumTableColumnWidths },
}
</script>

<style scoped>
.page-alert { margin-bottom: 16px; }
.icon-cell { min-height: 36px; display: flex; align-items: center; justify-content: center; }
</style>
