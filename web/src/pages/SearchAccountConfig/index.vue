<template>
  <div class="console-page search-account-page">
    <header class="section-heading page-heading">
      <div>
        <h1>默认账号配置</h1>
        <p class="page-lead">维护各题库默认参数和单价。</p>
      </div>
      <a-button type="primary" :loading="savingAll" @click="onSaveAll">保存全部配置</a-button>
    </header>
    <section class="console-panel">
    <a-alert
      type="info"
      show-icon
      class="page-alert"
      message="匿名搜索请求不能使用系统默认账号；只有携带有效 Authorization: Basic xxxx 的请求，在未显式提供参数时才会回退到这里的默认配置。"
    />
    <a-table
      bordered
      :data-source="dataSource"
      :columns="columns"
      :loading="tabLoading"
      :pagination="false"
      row-key="provider"
      :scroll="{ x: 1180 }"
    >
      <template #defaultParameter="{ record }">
        <div class="config-cell">
          <div class="config-cell-text">{{ buildSearchAccountDefaultParameterHint(record) }}</div>
          <template v-if="record.provider === 'ai'">
            <a-button type="primary" ghost class="config-ai-button" @click="goToAIConfig">前往 AI 搜索配置页</a-button>
          </template>
          <template v-else-if="record.tokenKey">
            <a-input-password v-model:value="record.tokenValue" placeholder="请输入默认 Token" />
          </template>
        </div>
      </template>
      <template #unitPrice="{ record }">
        <div class="config-cell">
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
      </template>
      <template #enabled="{ record }">
        <a-switch :checked="!!record.enabled" @change="checked => onEnabledChange(record.provider, checked)" />
      </template>
      <template #remark="{ record }">
        <a-input v-model:value="record.remark" placeholder="备注" />
      </template>
    </a-table>
    </section>
  </div>
</template>

<script>
import { defineComponent, ref } from 'vue';
import { useRouter } from 'vue-router';
import { message } from 'ant-design-vue';
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
          slots: {
            customRender: 'defaultParameter',
          },
          width: 360,
        },
        {
          title: '单价',
          dataIndex: 'unitPrice',
          key: 'unitPrice',
          slots: {
            customRender: 'unitPrice',
          },
          width: 280,
        },
        {
          title: '是否启用',
          dataIndex: 'enabled',
          key: 'enabled',
          slots: {
            customRender: 'enabled',
          },
          width: 110,
        },
        {
          title: '备注',
          dataIndex: 'remark',
          key: 'remark',
          slots: {
            customRender: 'remark',
          },
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
      getSearchAccountConfigs().then(res => {
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
.console-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}

.page-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}

.page-heading h1 {
  margin: 0;
  color: var(--console-text, #1f2937);
  font-size: 20px;
  font-weight: 650;
}

.page-lead {
  margin: 4px 0 0;
  color: var(--console-muted, #64748b);
  font-size: 13px;
}

.console-panel {
  min-width: 0;
  padding: 16px;
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: var(--console-radius, 12px);
  background: var(--console-surface, #fff);
}

.config-cell :deep(.ant-input),
.config-cell :deep(.ant-input-number) {
  width: 100%;
}

.search-account-page :deep(.ant-table) {
  font-size: 13px;
}

.page-alert {
  margin-bottom: 16px;
}

.config-cell {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.config-cell-text {
  font-size: 13px;
  line-height: 1.7;
  color: #6b7280;
  word-break: break-all;
}

.config-ai-button {
  align-self: flex-start;
}

@media (max-width: 640px) {
  .console-panel {
    padding: 12px;
  }
}
</style>
