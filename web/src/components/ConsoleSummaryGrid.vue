<template><section class="console-summary-grid" aria-label="汇总"><slot /></section></template>
<script>export default { name: 'ConsoleSummaryGrid' }</script>
<style scoped>
.console-summary-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 190px), 1fr)); gap: 16px; min-width: 0; }
.console-summary-grid :deep(> *) { min-width: 0; }
.console-summary-grid :deep(> :not(.ant-card)) { padding: 20px 24px; border: 1px solid var(--console-border); border-radius: var(--console-radius); background: var(--console-surface); }
@media (max-width: 768px) { .console-summary-grid :deep(> :not(.ant-card)) { padding: 16px; } }
</style>
