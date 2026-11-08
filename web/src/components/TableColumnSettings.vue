<template>
  <a-popover v-model:open="open" trigger="click" placement="bottomRight">
    <template #content>
      <section :id="panelId" class="column-settings-panel" aria-label="显示列设置">
        <div class="column-settings-actions">
          <a-button v-if="priorityKeys.length" size="small" @click="choosePriority">常用列</a-button>
          <a-button size="small" @click="reset">恢复全部列</a-button>
        </div>
        <a-checkbox-group :value="modelValue" class="column-settings-options" @change="update">
          <a-checkbox v-for="option in options" :key="option.key" :value="option.key" :disabled="option.disabled">{{ option.label }}</a-checkbox>
        </a-checkbox-group>
        <p>仅调整表格显示；必需列保留，原始数据和权限不变。</p>
      </section>
    </template>
    <a-button class="table-column-settings" :aria-controls="panelId" :aria-expanded="open" aria-label="列设置">
      <SettingOutlined aria-hidden="true" /><span>列设置</span>
    </a-button>
  </a-popover>
</template>
<script>
import { Button, Checkbox, Popover } from 'ant-design-vue'
import { SettingOutlined } from '@ant-design/icons-vue'
import { defineComponent, onBeforeUnmount, ref, useId, watch } from 'vue'

export default defineComponent({
  name: 'TableColumnSettings',
  components: { AButton: Button, ACheckbox: Checkbox, ACheckboxGroup: Checkbox.Group, APopover: Popover, SettingOutlined },
  props: {
    modelValue: { type: Array, default: () => [] },
    options: { type: Array, default: () => [] },
    contextKey: { type: String, default: '' },
    priorityKeys: { type: Array, default: () => [] },
  },
  emits: ['update:modelValue', 'reset'],
  setup(props, { emit }) {
    const open = ref(false)
    const panelId = 'column-settings-' + useId()
    let alive = true
    let openedContext = null
    watch(open, value => { openedContext = value ? props.contextKey : null }, { flush: 'sync' })
    watch(() => props.contextKey, () => { open.value = false; openedContext = null }, { flush: 'sync' })
    onBeforeUnmount(() => { alive = false; open.value = false; openedContext = null })
    const owns = () => alive && open.value && openedContext === props.contextKey
    const update = keys => { if (owns()) emit('update:modelValue', keys) }
    const reset = () => { if (owns()) emit('reset') }
    const choosePriority = () => {
      if (!owns()) return
      const required = props.options.filter(option => option.disabled).map(option => option.key)
      emit('update:modelValue', [...new Set([...props.priorityKeys, ...required])])
    }
    return { open, panelId, update, reset, choosePriority }
  },
})
</script>
<style scoped>
.column-settings-panel { width: min(320px, calc(100vw - 40px)); max-height: min(60vh, 480px); overflow: auto; color: var(--console-text); }
.column-settings-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 12px; }
.column-settings-options { display: flex; flex-direction: column; gap: 8px; }
.column-settings-options :deep(.ant-checkbox-wrapper) { margin-inline-start: 0; min-height: 32px; align-items: center; }
.column-settings-panel p { margin: 12px 0 0; color: var(--console-muted); font-size: 12px; }
</style>
