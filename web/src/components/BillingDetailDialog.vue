<template>
  <a-modal
    :open="open"
    title="账单详情"
    :width="720"
    centered
    class="billing-detail-dialog"
    @cancel="$emit('update:open', false)"
  >
    <div v-if="record" class="billing-detail">
      <section class="billing-detail__overview">
        <div class="billing-detail__heading">
          <a-space wrap>
            <a-tag :color="detail.direction === 'income' ? 'green' : 'blue'">{{ detail.directionLabel }}</a-tag>
            <span>{{ detail.typeLabel }}</span>
          </a-space>
          <span v-if="detail.mode !== '-'" class="billing-detail__mode">{{ detail.mode }}</span>
        </div>
        <p class="billing-detail__subject">{{ detail.subject }}</p>
      </section>

      <section class="billing-detail__fees">
        <header class="billing-detail__section-heading">
          <div><h3>费用详情</h3><p>按实际调用拆分</p></div>
          <span class="billing-detail__count">{{ detail.items.length }} 项</span>
        </header>
        <article v-for="item in detail.items" :key="item.key" class="billing-detail__fee-item">
          <div class="billing-detail__fee-heading">
            <strong>{{ item.title }}</strong>
            <strong :class="amountClass">{{ item.amount }}</strong>
          </div>
          <dl v-if="item.metadata.length" class="billing-detail__metadata">
            <div v-for="entry in item.metadata" :key="entry.label" :class="{ 'billing-detail__keyword': entry.keyword }">
              <dt>{{ entry.label }}</dt><dd>{{ entry.value }}</dd>
            </div>
          </dl>
        </article>
        <div class="billing-detail__total"><strong>合计</strong><strong :class="amountClass">{{ detail.amount }}</strong></div>
      </section>

      <dl v-if="detail.metadata.length" class="billing-detail__metadata billing-detail__record-meta">
        <div v-for="entry in detail.metadata" :key="entry.label"><dt>{{ entry.label }}</dt><dd>{{ entry.value }}</dd></div>
      </dl>
      <section v-if="detail.canEditStatement" class="billing-detail__statement">
        <a-switch
          :checked="detail.showInStatement"
          :loading="statementSaving"
          :disabled="statementSaving"
          aria-label="显示在用户账单流水"
          @change="requestStatementChange"
        />
        <span>显示在用户账单流水</span>
      </section>
    </div>
    <template #footer><a-button @click="$emit('update:open', false)">关闭</a-button></template>
  </a-modal>
</template>

<script>
import { computed, defineComponent } from 'vue'
import { buildBillingDetailViewModel } from '@/utils/userPortalHelpers.mjs'

export default defineComponent({
  name: 'BillingDetailDialog',
  props: {
    open: { type: Boolean, default: false },
    record: { type: Object, default: null },
    viewerScope: { type: String, default: 'self' },
    canEditStatement: { type: Boolean, default: false },
    statementSaving: { type: Boolean, default: false },
  },
  emits: ['update:open', 'statement-visibility-change'],
  setup(props, { emit }) {
    const detail = computed(() => buildBillingDetailViewModel(props.record, {
      viewerScope: props.viewerScope,
      canEditStatement: props.canEditStatement,
    }))
    const amountClass = computed(() => detail.value.direction === 'income' ? 'billing-detail__income' : 'billing-detail__expense')
    function requestStatementChange(showInStatement) {
      if (!props.open || props.statementSaving || !detail.value.canEditStatement || detail.value.id == null) return
      emit('statement-visibility-change', { id: detail.value.id, showInStatement })
    }
    return { detail, amountClass, requestStatementChange }
  },
})
</script>

<style scoped>
.billing-detail { display: grid; gap: 20px; color: var(--console-text); }
.billing-detail__overview { padding: 20px; border: 1px solid var(--console-border); border-radius: 10px; background: var(--console-selected, #e7f3ef); }
.billing-detail__heading, .billing-detail__fee-heading, .billing-detail__section-heading, .billing-detail__total { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.billing-detail__heading, .billing-detail__fee-heading { flex-wrap: wrap; }
.billing-detail__mode { color: var(--console-muted); font-size: 12px; }
.billing-detail__subject { margin: 16px 0 0; font-size: 16px; font-weight: 600; white-space: pre-wrap; overflow-wrap: anywhere; }
.billing-detail__fees { display: grid; gap: 12px; }
.billing-detail__section-heading h3 { margin: 0; font-size: 16px; }
.billing-detail__section-heading p { margin: 4px 0 0; color: var(--console-muted); font-size: 12px; }
.billing-detail__count { padding: 4px 10px; border-radius: 20px; color: var(--console-primary); background: var(--console-selected, #e7f3ef); }
.billing-detail__fee-item { padding: 16px; border: 1px solid var(--console-border); border-radius: 8px; }
.billing-detail__fee-heading > strong { overflow-wrap: anywhere; }
.billing-detail__income { color: var(--console-success, #15803d); }
.billing-detail__expense { color: var(--console-danger, #c13e41); }
.billing-detail__metadata { display: flex; flex-wrap: wrap; gap: 10px 20px; margin: 12px 0 0; }
.billing-detail__metadata > div { display: flex; flex-wrap: wrap; gap: 4px; min-width: 0; }
.billing-detail__metadata dt { color: var(--console-muted); }
.billing-detail__metadata dt::after { content: '：'; }
.billing-detail__metadata dd { flex: 1; min-width: 0; margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
.billing-detail__metadata .billing-detail__keyword { flex-basis: 100%; padding: 10px 12px; border-radius: 6px; background: var(--console-surface-muted, #f8faf9); }
.billing-detail__record-meta { padding: 16px; margin: 0; border: 1px solid var(--console-border); border-radius: 8px; }
.billing-detail__total { padding: 16px; border-top: 1px solid var(--console-border); }
.billing-detail__total strong:last-child { font-size: 20px; }
.billing-detail__statement { display: flex; align-items: center; gap: 12px; padding: 16px; border: 1px solid var(--console-border); border-radius: 8px; }
@media (max-width: 480px) {
  .billing-detail { gap: 16px; }
  .billing-detail__overview, .billing-detail__fee-item { padding: 14px; }
  .billing-detail__fee-heading { align-items: flex-start; }
}
</style>
