<template>
  <ConsolePanel :title="source.label" class="source-status">
    <template #actions>
      <a-tag :color="source.lastError ? 'warning' : source.available ? 'success' : 'default'">{{ extensionStatusText(source) }}</a-tag>
      <a-button :aria-label="`检查 ${source.label} 更新`" :loading="refreshing || source.updating" :disabled="disabled || !source.configured || source.updating" @click="$emit('refresh', source.source)">检查更新</a-button>
    </template>
    <a-alert v-if="source.lastError" type="warning" show-icon message="最近检查失败，已有缓存仍可使用。请稍后重试。" class="source-alert" />
    <dl class="release-details">
      <dt>扩展 ID</dt><dd>{{ source.extensionId || '-' }}</dd>
      <dt>缓存版本</dt><dd>{{ source.release?.version || '-' }}</dd>
      <dt>文件大小</dt><dd>{{ formatExtensionSize(source.release?.size) }}</dd>
      <dt>SHA-256</dt><dd class="digest">{{ source.release?.sha256 || '-' }}</dd>
      <dt>最近检查</dt><dd>{{ formatExtensionTime(source.lastCheckedAt || source.release?.checkedAt) }}</dd>
      <dt>缓存更新时间</dt><dd>{{ formatExtensionTime(source.release?.updatedAt) }}</dd>
    </dl>
    <p v-if="!source.configured" class="source-hint">填写对应商店的扩展 ID 并保存后启用。</p>
    <p v-else-if="!source.available" class="source-hint">首次检查成功后，客户端即可获取扩展包。</p>
  </ConsolePanel>
</template>

<script setup>
import { extensionStatusText, formatExtensionSize, formatExtensionTime } from '@/utils/extensionUpdateHelpers.mjs'

defineProps({ source: { type: Object, required: true }, disabled: Boolean, refreshing: Boolean })
defineEmits(['refresh'])
</script>

<style scoped>
.source-alert { margin-bottom: 16px; }
.release-details { display: grid; grid-template-columns: 108px minmax(0, 1fr); gap: 12px 16px; margin: 0; }
.release-details dt { color: var(--console-muted); }
.release-details dd { margin: 0; overflow-wrap: anywhere; }
.digest { font-family: monospace; font-size: 12px; line-height: 1.7; }
.source-hint { margin: 20px 0 0; color: var(--console-muted); line-height: 1.7; }
@media (max-width: 480px) { .release-details { grid-template-columns: 1fr; gap: 4px; } .release-details dd { margin-bottom: 12px; } }
</style>
