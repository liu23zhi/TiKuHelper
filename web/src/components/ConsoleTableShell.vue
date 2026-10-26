<template><div class="console-table-shell" role="region" :aria-label="label"><slot /></div></template>
<script>
export default { name: 'ConsoleTableShell', props: { label: { type: String, default: '数据表格' } } }
</script>
<style scoped>
.console-table-shell { min-width: 0; width: 100%; overflow: hidden; }
</style>
