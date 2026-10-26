<template>
  <div class="console-page search-account-page">
    <ConsolePageHeading title="默认账号配置" description="维护题库渠道、默认参数和单价。">
      <template #actions><a-space wrap>
        <a-button :loading="tabLoading" :disabled="savingAll" @click="handleSearchAccountConfigs">刷新</a-button>
        <a-button type="primary" :loading="savingAll" :disabled="tabLoading" @click="onSaveAll">保存全部配置</a-button>
      </a-space></template>
    </ConsolePageHeading>
    <ConsolePanel title="渠道配置" description="按渠道维护默认参数，保存后应用于后续搜题请求。">
    <a-alert
      type="info"
      show-icon
      class="page-alert"
      message="匿名搜索请求不能使用系统默认账号；只有携带有效 Authorization: Basic xxxx 的请求，在未显式提供参数时才会回退到这里的默认配置。"
    />
    <ConsoleTableShell label="题库默认账号配置"><a-table
      bordered
      :data-source="dataSource"
      :columns="columns"
      :loading="tabLoading"
      :pagination="false"
      row-key="provider"
      table-layout="fixed"
      :scroll="{ x: sumTableColumnWidths(columns) }"
    >
      <template #bodyCell="{ column, record }">
        <div v-if="column.key === 'defaultParameter'" class="config-cell">
          <div class="config-cell-text">{{ buildSearchAccountDefaultParameterHint(record) }}</div>
          <template v-if="record.provider === 'ai'">
            <a-button type="primary" ghost class="config-ai-button" @click="goToAIConfig">前往 AI 搜索配置页</a-button>
          </template>
          <template v-else-if="record.tokenKey">
            <a-input-password v-model:value="record.tokenValue" placeholder="请输入默认 Token" />
          </template>
        </div>
        <div v-else-if="column.key === 'unitPrice'" class="config-cell">
          <div class="config-cell-text">{{ buildSearchAccountUnitPriceHint(record) }}</div>
          <template v-if="record.provider !== 'ai'">
            <a-input-number
              v-model:value="record.flatPrice"
              :min="0"
              :precision="8"
              :step="0.01"
              style="width: 100%"
              placeholder="请输入固定单价"
            />
          </template>
        </div>
        <a-switch v-else-if="column.key === 'enabled'" :checked="!!record.enabled" @change="checked => onEnabledChange(record.provider, checked)" />
        <a-input v-else-if="column.key === 'remark'" v-model:value="record.remark" placeholder="备注" />
      </template>
    </a-table></ConsoleTableShell>
    </ConsolePanel>
  </div>
</template>

<script>
import { defineComponent, ref } from 'vue';
import { useRouter } from 'vue-router';
import { message } from 'ant-design-vue';
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs';
import { getSearchAccountConfigs, updateSearchAccountConfig } from '@/api/TikuComponentApi';
import {
  buildSearchAccountConfigSavePayloads,
  buildSearchAccountDefaultParameterHint,
  buildSearchAccountUnitPriceHint,
} from '@/utils/paymentMethodHelpers.mjs';

const dataSource = ref([])
const tabLoading = ref(true)
const savingAll = ref(false)

export default defineComponent({
  name: 'SearchAccountConfig',
  setup() {
    const router = useRouter()
    const goToAIConfig = () => {
      router.push('/adapter/ai-search-config')
    }

    return {
      dataSource,
      tabLoading,
      savingAll,
      goToAIConfig,
      buildSearchAccountDefaultParameterHint,
      buildSearchAccountUnitPriceHint,
      sumTableColumnWidths,
      columns: [
        {
          title: '题库名称',
          dataIndex: 'providerLabel',
          key: 'providerLabel',
          width: 180,
        },
        {
          title: 'Provider',
          dataIndex: 'provider',
          key: 'provider',
          width: 140,
        },
        {
          title: '默认参数',
          dataIndex: 'defaultParameter',
          key: 'defaultParameter',
          width: 360,
        },
        {
          title: '单价',
          dataIndex: 'unitPrice',
          key: 'unitPrice',
          width: 280,
        },
        {
          title: '是否启用',
          dataIndex: 'enabled',
          key: 'enabled',
          width: 110,
        },
        {
          title: '备注',
          dataIndex: 'remark',
          key: 'remark',
          width: 220,
        },
      ],
    };
  },
  mounted() {
    this.handleSearchAccountConfigs()
  },
  methods: {
    normalizeConfigs(configs) {
      return (configs || []).map(item => ({
        ...item,
        enabled: !!item.enabled,
        flatPrice: Number(item?.flatPrice || 0),
      }))
    },
    handleSearchAccountConfigs() {
      tabLoading.value = true
      return getSearchAccountConfigs().then(res => {
        if (res?.status == 200) {
          dataSource.value = this.normalizeConfigs(res?.data)
        } else {
          message.error(res?.message || '获取默认账号配置失败')
        }
      }).catch(() => {
        message.error('获取默认账号配置失败')
      }).finally(() => {
        tabLoading.value = false
      })
    },
    onEnabledChange(provider, checked) {
      const target = dataSource.value.find(item => item.provider === provider)
      if (target) {
        target.enabled = !!checked
      }
    },
    async onSaveAll() {
      const payloads = buildSearchAccountConfigSavePayloads(dataSource.value)
      if (!payloads.length) {
        message.warning('当前没有可保存的默认账号配置')
        return
      }

      savingAll.value = true
      try {
        for (const item of payloads) {
          const res = await updateSearchAccountConfig(item.provider, item.payload)
          if (res?.status !== 200) {
            throw new Error(res?.message || `保存 ${item.provider} 配置失败`)
          }
        }
        message.success('保存成功')
        this.handleSearchAccountConfigs()
      } catch (error) {
        message.error(error?.message || '保存失败')
      } finally {
        savingAll.value = false
      }
    },
  }
});

</script>

<style scoped>
.page-alert { margin-bottom: 20px; }
.config-cell { display: flex; flex-direction: column; align-items: center; gap: 12px; }
.config-cell-text { color: var(--console-muted); line-height: 1.7; overflow-wrap: anywhere; }
.config-cell :deep(.ant-input-affix-wrapper), .config-cell :deep(.ant-input-number) { width: 100%; }
.config-ai-button { max-width: 100%; white-space: normal; height: auto; min-height: 32px; }
</style>
