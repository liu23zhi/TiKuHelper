<template>
  <a-form :model="config" :disabled="disabled || !loaded" layout="vertical" :onFinish="entry.bind(save)">
    <a-form-item label="启用提醒">
      <a-switch :checked="config.enabled" :onUpdate:checked="entry.bind(setField, 'enabled')" checked-children="开启" un-checked-children="关闭" />
    </a-form-item>
    <a-form-item label="Webhook 地址" help="只接受企业微信机器人 HTTPS 地址；留空表示保持已保存地址。">
      <a-input :value="config.webhookUrl" :onUpdate:value="entry.bind(setField, 'webhookUrl')" type="url" allow-clear placeholder="输入新地址以替换已保存地址" />
      <div v-if="config.configured" class="address-actions">
        <span>{{ config.clearWebhook ? '保存后将清除当前地址' : '已配置企业微信机器人' }}</span>
        <a-button v-if="!config.clearWebhook" type="link" danger size="small" :onClick="entry.bind(clearWebhook)">清除已保存地址</a-button>
        <a-button v-else type="link" size="small" :onClick="entry.bind(keepWebhook)">保留已保存地址</a-button>
      </div>
    </a-form-item>
    <a-form-item label="@成员 ID" help="多个成员用逗号分隔，可填写 @all。">
      <a-input :value="config.mentionedList" :onUpdate:value="entry.bind(setField, 'mentionedList')" allow-clear placeholder="成员 ID，可选" />
    </a-form-item>
    <a-space wrap>
      <a-button type="primary" html-type="submit" :loading="saving" :disabled="disabled || !loaded || testing">保存提醒设置</a-button>
      <a-button :loading="testing" :disabled="disabled || !loaded || saving" :onClick="entry.bind(test)">发送测试消息</a-button>
      <span v-if="config.configured" class="hint">当前已配置 Webhook</span>
    </a-space>
  </a-form>
</template>

<script setup>
defineProps({
  config: { type: Object, required: true },
  entry: { type: Object, required: true },
  loaded: Boolean,
  disabled: Boolean,
  saving: Boolean,
  testing: Boolean,
  save: { type: Function, required: true },
  test: { type: Function, required: true },
  setField: { type: Function, required: true },
  clearWebhook: { type: Function, required: true },
  keepWebhook: { type: Function, required: true },
})
</script>

<style scoped>
.address-actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-top: 8px; }
.hint { color: var(--console-muted); font-size: 12px; }
</style>
