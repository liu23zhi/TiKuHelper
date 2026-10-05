<template>
  <a-card title="AI 搜索配置" style="width: 100%" :bordered="false" class="page-card">
    <template #extra>
      <a-space>
        <a-button @click="handleLoad" :loading="tabLoading">刷新</a-button>
        <a-button @click="handleOpenTestDialog">测试</a-button>
        <a-button type="primary" @click="handleSave" :loading="saving">保存</a-button>
      </a-space>
    </template>

    <a-alert
      type="info"
      show-icon
      class="page-alert"
      message="AI 搜索只在 use=ai 的请求模式下生效。显式传入 url、key、model 时优先使用请求参数；只有带有效 Basic 认证且未显式提供时，才会回退到这里的系统默认配置。"
    />

    <a-form :label-col="labelCol" :wrapper-col="wrapperCol">
      <a-form-item label="是否启用">
        <a-switch v-model:checked="formState.enabled" />
      </a-form-item>

      <a-form-item label="服务地址">
        <a-input v-model:value="formState.baseUrl" placeholder="例如 https://api.openai.com 或 https://api.openai.com/v1" />
        <div class="hint">如果地址未包含 /v1，保存时会提示你是否补全；也可以保持原样保存。</div>
      </a-form-item>

      <a-form-item label="API Key">
        <a-input-password v-model:value="formState.apiKey" placeholder="请输入 API Key" />
      </a-form-item>

      <a-form-item label="默认模型">
        <a-input v-model:value="formState.model" placeholder="可手动输入模型名" />
        <div class="hint">也可以点击下方模型列表快速填入。</div>
      </a-form-item>

      <a-form-item label="模型列表">
        <div class="model-editor">
          <a-textarea
            v-model:value="formState.manualModelsText"
            :auto-size="{ minRows: 4, maxRows: 8 }"
            placeholder="支持英文逗号、中文逗号或换行分隔，例如\ngpt-5.4,gpt-5.4-mini,gpt-5.3-codex"
          />
          <a-button class="fetch-btn" @click="handleFetchModels" :loading="fetchingModels">
            获取远程模型列表
          </a-button>
        </div>
        <div class="hint">保存时会以此处内容作为模型列表，并自动为缺失模型补充默认架构。</div>
        <a-space wrap class="model-tags">
          <a-tag
            v-for="item in modelOptions"
            :key="item.value"
            color="blue"
            style="cursor: pointer"
            @click="selectModel(item.value)"
          >
            {{ item.label }}
          </a-tag>
          <span v-if="modelOptions.length === 0" class="hint">暂无模型列表，请先手动填写或获取远程模型。</span>
        </a-space>
      </a-form-item>

      <a-form-item label="汇率">
        <a-input-number v-model:value="formState.exchangeRate" :min="0" :precision="6" :step="0.01" style="width: 100%" />
        <div class="hint">模型价格按美元口径返回，实际计费会先乘以这里的汇率；默认 1。</div>
      </a-form-item>

      <a-form-item label="启用全局倍率">
        <a-switch v-model:checked="formState.useGlobalBillingMultiplier" />
        <div class="hint">开启后所有模型都使用同一个全局倍率，不再读取模型架构表里的单模型倍率。</div>
      </a-form-item>

      <a-form-item label="全局倍率">
        <a-input-number v-model:value="formState.globalBillingMultiplier" :min="0" :precision="4" :step="0.1" style="width: 100%" />
        <div class="hint">最终 AI 扣费倍率 = 汇率 × 全局倍率；关闭全局倍率时为 汇率 × 单模型倍率。</div>
      </a-form-item>

      <a-form-item label="Prompt 设置">
        <a-textarea
          v-model:value="formState.prompt"
          :auto-size="{ minRows: 10, maxRows: 18 }"
          placeholder="留空时将自动使用系统内置 Prompt"
        />
        <div class="hint-block">
          <div class="hint">AI 只应输出 JSON，格式固定为 {"answers":["答案1","答案2"]}，系统会自动解析并转换为当前题库接口兼容的答案结构。</div>
          <div class="hint">建议保留“只返回 JSON、信息不足返回不确定、优先返回与选项一致的答案文本”这一类约束，避免输出额外说明影响后续处理。</div>
          <div class="hint">如果你清空 Prompt 保存，后端会自动回退到内置默认 Prompt；下次加载时，页面会直接显示当前生效的默认内容。</div>
          <div class="hint">该输出还需要兼容现有题库聚合结构以及 OCSJS 的后续判断逻辑，所以不建议改成自然语言长段回答。</div>
        </div>
      </a-form-item>

      <a-form-item label="温度">
        <a-input :value="fixedTemperatureText" readonly disabled />
        <div class="hint">固定为 0，用于降低随机性，让相同题目的返回结果更稳定。</div>
      </a-form-item>

      <a-form-item label="Top P">
        <a-input :value="fixedTopPText" readonly disabled />
        <div class="hint">固定为 0，与温度一起保持低随机性，不开放自定义调整。</div>
      </a-form-item>
    </a-form>

    <div class="pricing-grid" v-if="!formState.useGlobalBillingMultiplier">
      <a-card :bordered="false" class="sub-card">
        <template #title>
          <div class="sub-card-title-wrap">
            <div>
              <div class="sub-card-title">单模型倍率配置</div>
              <div class="sub-card-subtitle">关闭全局倍率后才会使用这里的模型倍率、启停状态与排序。</div>
            </div>
          </div>
        </template>
        <template #extra>
          <a-space>
            <a-button @click="loadAIModelArchitectures" :loading="architectureLoading">刷新</a-button>
            <a-button type="primary" @click="openArchitectureModal()">新增架构</a-button>
          </a-space>
        </template>

        <div class="architecture-table-wrap">
          <a-table
            bordered
            row-key="id"
            :data-source="architectureRows"
            :columns="architectureColumns"
            :loading="architectureLoading"
            :pagination="false"
            :scroll="{ x: 980 }"
          >
            <template #enabled="{ record }">
              <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
            </template>
            <template #billingMultiplier="{ record }">{{ formatMultiplier(record.billingMultiplier, record) }}</template>
            <template #remark="{ record }">{{ record.remark || '-' }}</template>
            <template #action="{ record }">
              <a-space size="small">
                <a-button type="link" style="padding: 0" @click="openArchitectureModal(record)">编辑</a-button>
                <a-popconfirm title="确定删除该模型架构吗？" ok-text="确定" cancel-text="取消" @confirm="handleDeleteArchitecture(record)">
                  <a-button type="link" danger style="padding: 0">删除</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table>
        </div>
      </a-card>
    </div>

    <a-modal
      v-model:visible="testModalVisible"
      title="测试 AI 搜索配置"
      ok-text="发送 hello 测试"
      cancel-text="关闭"
      :confirm-loading="testSubmitting"
      width="920px"
      @ok="handleRunTest"
    >
      <a-form :label-col="modalLabelCol" :wrapper-col="modalWrapperCol">
        <a-form-item label="测试模型" required>
          <a-select
            v-model:value="testForm.model"
            show-search
            allow-clear
            :options="modelOptions"
            placeholder="请选择模型或先在上方手动填写模型名"
          />
          <div class="hint">会向当前配置对应的 AI 服务发送固定测试问题：hello。</div>
        </a-form-item>
        <a-form-item label="服务地址">
          <a-input v-model:value="testForm.baseUrl" placeholder="留空时使用当前页面中的服务地址" />
        </a-form-item>
        <a-form-item label="API Key">
          <a-input-password v-model:value="testForm.apiKey" placeholder="留空时使用当前页面中的 API Key" />
        </a-form-item>
        <a-form-item label="Prompt 覆盖">
          <a-textarea
            v-model:value="testForm.prompt"
            :auto-size="{ minRows: 4, maxRows: 8 }"
            placeholder="留空时使用当前页面中的 Prompt"
          />
        </a-form-item>
      </a-form>

      <a-alert
        type="info"
        show-icon
        class="test-alert"
        message="测试请求固定发送 hello，弹窗会展示完整请求体、原始流式响应以及最终解析出的答案。"
      />

      <div v-if="hasAISearchTestResult(testResult)" class="test-result-panel">
        <div class="test-result-block">
          <div class="test-result-title">解析答案</div>
          <a-space wrap>
            <a-tag v-for="item in testResult.answers" :key="item" color="blue">{{ item }}</a-tag>
            <span v-if="!testResult.answers.length" class="hint">暂无解析答案</span>
          </a-space>
        </div>
        <div class="test-result-block">
          <div class="test-result-title">请求内容</div>
          <pre class="test-result-pre">{{ testResult.requestBody || '-' }}</pre>
        </div>
        <div class="test-result-block">
          <div class="test-result-title">完整回复</div>
          <pre class="test-result-pre">{{ testResult.rawResponse || '-' }}</pre>
        </div>
      </div>
    </a-modal>

    <a-modal
      v-model:visible="architectureModalVisible"
      :title="architectureForm.id ? '编辑模型架构' : '新增模型架构'"
      ok-text="确定"
      cancel-text="取消"
      :confirm-loading="architectureSaving"
      @ok="handleSaveArchitecture"
      @cancel="closeArchitectureModal"
    >
      <a-form :label-col="modalLabelCol" :wrapper-col="modalWrapperCol">
        <a-form-item label="架构名称" required>
          <a-input v-model:value="architectureForm.name" placeholder="例如 GPT-4o" />
        </a-form-item>
        <a-form-item label="模型名称">
          <a-input v-model:value="architectureForm.modelName" placeholder="例如 gpt-4o" />
        </a-form-item>
        <a-form-item label="计费倍率">
          <a-input-number v-model:value="architectureForm.billingMultiplier" :min="0" :precision="4" :step="0.1" style="width: 100%" :disabled="formState.useGlobalBillingMultiplier" />
          <div class="hint">关闭全局倍率后生效；实际扣费 = AI token 基础用量 × 汇率 × 当前模型倍率。</div>
        </a-form-item>
        <a-form-item label="启用">
          <a-switch v-model:checked="architectureForm.enabled" />
        </a-form-item>
        <a-form-item label="排序">
          <a-input-number v-model:value="architectureForm.sort" :min="0" :step="1" style="width: 100%" />
        </a-form-item>
        <a-form-item label="备注">
          <a-input v-model:value="architectureForm.remark" placeholder="备注" />
        </a-form-item>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script>
import { defineComponent, computed, onMounted, reactive, ref } from 'vue';
import { message, Modal } from 'ant-design-vue';
import {
  getAISearchConfig,
  getAISearchModels,
  testAISearchConfig,
  updateAISearchConfig,
  getAIModelArchitectures,
  createAIModelArchitecture,
  updateAIModelArchitecture,
  deleteAIModelArchitecture,
} from '@/api/TikuComponentApi';
import {
  buildModelNameOptions,
  buildModelNameText,
  buildAISearchConfigSavePayload,
  buildMissingArchitecturePayloads,
  filterRemoteModelNames,
} from '@/utils/aiModelArchitectureHelpers.mjs';
import {
  buildAISearchTestForm,
  buildAISearchTestPayload,
  buildAISearchTestResult,
  hasAISearchTestResult,
} from '@/utils/aiSearchConfigHelpers.mjs';

function trimBaseUrl(url) {
  return String(url || '').trim().replace(/\/+$/, '')
}

function appendV1(url) {
  const trimmed = trimBaseUrl(url)
  if (!trimmed || trimmed.endsWith('/v1')) {
    return trimmed
  }
  return `${trimmed}/v1`
}

export default defineComponent({
  name: 'AISearchConfig',
  setup() {
    const tabLoading = ref(true)
    const saving = ref(false)
    const fetchingModels = ref(false)
    const architectureLoading = ref(false)
    const architectureSaving = ref(false)
    const testModalVisible = ref(false)
    const testSubmitting = ref(false)
    const testForm = reactive({
      baseUrl: '',
      apiKey: '',
      model: '',
      prompt: '',
    })
    const testResult = reactive({
      answers: [],
      requestBody: '',
      rawResponse: '',
    })
    const fixedTemperatureText = '0'
    const fixedTopPText = '0'
    const formState = reactive({
      enabled: false,
      baseUrl: '',
      apiKey: '',
      model: '',
      manualModelsText: '',
      prompt: '',
      remark: '',
      temperature: 0,
      topP: 0,
      exchangeRate: 1,
      useGlobalBillingMultiplier: true,
      globalBillingMultiplier: 1,
    })

    const architectureRows = ref([])
    const architectureModalVisible = ref(false)
    const architectureForm = reactive({
      id: null,
      name: '',
      modelName: '',
      billingMultiplier: 1,
      enabled: true,
      sort: 0,
      remark: '',
    })

    const modelOptions = computed(() => {
      return buildModelNameOptions(formState.manualModelsText)
    })

    const architectureColumns = [
      { title: 'ID', dataIndex: 'id', key: 'id', width: 80 },
      { title: '架构名称', dataIndex: 'name', key: 'name', width: 180 },
      { title: '模型名称', dataIndex: 'modelName', key: 'modelName', width: 180 },
      { title: '计费倍率', dataIndex: 'billingMultiplier', key: 'billingMultiplier', width: 140, slots: { customRender: 'billingMultiplier' } },
      { title: '状态', dataIndex: 'enabled', key: 'enabled', width: 100, slots: { customRender: 'enabled' } },
      { title: '排序', dataIndex: 'sort', key: 'sort', width: 100 },
      { title: '备注', dataIndex: 'remark', key: 'remark', width: 200, slots: { customRender: 'remark' } },
      { title: '操作', dataIndex: 'action', key: 'action', fixed: 'right', width: 120, slots: { customRender: 'action' } },
    ]

    const loadConfig = () => {
      tabLoading.value = true
      getAISearchConfig().then(res => {
        if (res?.status === 200) {
          const data = res?.data || {}
          formState.enabled = !!data.enabled
          formState.baseUrl = data.baseUrl || ''
          formState.apiKey = data.apiKey || ''
          formState.model = data.model || ''
          formState.manualModelsText = buildModelNameText(data.manualModels || [])
          formState.prompt = data.prompt || ''
          formState.remark = data.remark || ''
          formState.temperature = data.temperature ?? 0
          formState.topP = data.topP ?? 0
          formState.exchangeRate = Number(data.exchangeRate || 1)
          formState.useGlobalBillingMultiplier = data.useGlobalBillingMultiplier !== false
          formState.globalBillingMultiplier = Number(data.globalBillingMultiplier || 1)
        } else {
          message.error(res?.message || '获取 AI 搜索配置失败')
        }
      }).catch(() => {
        message.error('获取 AI 搜索配置失败')
      }).finally(() => {
        tabLoading.value = false
      })
    }

    const loadAIModelArchitectures = () => {
      architectureLoading.value = true
      return getAIModelArchitectures().then(res => {
        if (res?.status === 200) {
          architectureRows.value = Array.isArray(res?.data) ? res.data : []
        } else {
          message.error(res?.message || '获取模型架构失败')
        }
      }).catch(() => {
        message.error('获取模型架构失败')
      }).finally(() => {
        architectureLoading.value = false
      })
    }

    onMounted(() => {
      loadConfig()
      loadAIModelArchitectures()
    })

    const resetArchitectureForm = () => {
      architectureForm.id = null
      architectureForm.name = ''
      architectureForm.modelName = ''
      architectureForm.billingMultiplier = 1
      architectureForm.enabled = true
      architectureForm.sort = 0
      architectureForm.remark = ''
    }

    return {
      labelCol: { style: { width: '140px' } },
      wrapperCol: { span: 14 },
      modalLabelCol: { style: { width: '110px' } },
      modalWrapperCol: { span: 16 },
      tabLoading,
      saving,
      fetchingModels,
      architectureLoading,
      architectureSaving,
      testModalVisible,
      testSubmitting,
      testForm,
      testResult,
      fixedTemperatureText,
      fixedTopPText,
      formState,
      modelOptions,
      architectureRows,
      architectureColumns,
      architectureModalVisible,
      architectureForm,
      hasAISearchTestResult,
      loadConfig,
      loadAIModelArchitectures,
      resetArchitectureForm,
    }
  },
  methods: {
    handleLoad() {
      this.loadConfig()
      this.loadAIModelArchitectures()
    },
    selectModel(model) {
      this.formState.model = model
    },
    syncMissingArchitectures(modelSource) {
      const missingPayloads = buildMissingArchitecturePayloads(this.architectureRows, modelSource)
      if (!missingPayloads.length) {
        return Promise.resolve(0)
      }

      return Promise.all(missingPayloads.map(payload => createAIModelArchitecture(payload))).then(results => {
        const failed = results.find(item => item?.status !== 200)
        if (failed) {
          throw new Error(failed?.message || '同步模型架构失败')
        }
        return this.loadAIModelArchitectures().then(() => missingPayloads.length)
      })
    },
    handleFetchModels() {
      const params = {}
      const currentBaseUrl = trimBaseUrl(this.formState.baseUrl)
      const currentApiKey = String(this.formState.apiKey || '').trim()
      if (currentBaseUrl) {
        params.url = currentBaseUrl
      }
      if (currentApiKey) {
        params.key = currentApiKey
      }

      this.fetchingModels = true
      getAISearchModels(params).then(res => {
        if (res?.status !== 200) {
          throw new Error(res?.message || '获取 AI 模型列表失败')
        }

        const remoteModels = Array.isArray(res?.data?.models) ? res.data.models : []
        const filteredRemoteModels = filterRemoteModelNames(remoteModels)
        this.formState.manualModelsText = buildModelNameText(filteredRemoteModels)
        return this.syncMissingArchitectures(this.formState.manualModelsText)
      }).then(addedCount => {
        if (addedCount > 0) {
          message.success(`已同步模型架构，新增 ${addedCount} 个模型`)
        } else {
          message.success('模型架构已刷新，暂无新增模型')
        }
      }).catch(error => {
        message.error(error?.message || '获取 AI 模型列表失败')
      }).finally(() => {
        this.fetchingModels = false
      })
    },
    submitSave(baseUrl) {
      this.saving = true
      this.syncMissingArchitectures(this.formState.manualModelsText).then(() => {
        const payload = buildAISearchConfigSavePayload(this.formState, baseUrl)
        return updateAISearchConfig(payload)
      }).then(res => {
        if (res?.status === 200) {
          message.success('保存成功')
          this.loadConfig()
          return
        }
        throw new Error(res?.message || '保存失败')
      }).catch(error => {
        message.error(error?.message || '保存失败')
      }).finally(() => {
        this.saving = false
      })
    },
    handleSave() {
      const rawBaseUrl = this.formState.baseUrl || ''
      const trimmedBaseUrl = trimBaseUrl(rawBaseUrl)
      const completedBaseUrl = appendV1(rawBaseUrl)

      if (trimmedBaseUrl && !trimmedBaseUrl.endsWith('/v1')) {
        Modal.confirm({
          title: '地址未包含 /v1',
          content: `可选补全为：${completedBaseUrl}。你可以继续补全保存，也可以保持原样保存。`,
          okText: '补全后保存',
          cancelText: '保持原样',
          onOk: () => this.submitSave(completedBaseUrl),
          onCancel: () => this.submitSave(trimmedBaseUrl),
        })
        return
      }

      this.submitSave(trimmedBaseUrl)
    },
    resetTestResult() {
      this.testResult.answers = []
      this.testResult.requestBody = ''
      this.testResult.rawResponse = ''
    },
    handleOpenTestDialog() {
      const nextForm = buildAISearchTestForm(this.formState)
      this.testForm.baseUrl = nextForm.baseUrl
      this.testForm.apiKey = nextForm.apiKey
      this.testForm.model = nextForm.model
      this.testForm.prompt = nextForm.prompt
      this.resetTestResult()
      this.testModalVisible = true
    },
    handleRunTest() {
      let payload
      try {
        payload = buildAISearchTestPayload(this.testForm, this.formState)
      } catch (error) {
        message.warning(error?.message || '请选择要测试的 AI 模型')
        return
      }

      this.testSubmitting = true
      testAISearchConfig(payload).then(res => {
        if (res?.status === 200) {
          const result = buildAISearchTestResult(res)
          this.testResult.answers = result.answers
          this.testResult.requestBody = result.requestBody
          this.testResult.rawResponse = result.rawResponse
          message.success('AI 搜索测试完成')
          return
        }
        message.error(res?.message || 'AI 搜索测试失败')
      }).catch(() => {
        message.error('AI 搜索测试失败')
      }).finally(() => {
        this.testSubmitting = false
      })
    },
    openArchitectureModal(record) {
      if (record) {
        this.architectureForm.id = record.id
        this.architectureForm.name = record.name || ''
        this.architectureForm.modelName = record.modelName || ''
        this.architectureForm.billingMultiplier = Number(record.billingMultiplier || record.inputTokenPrice || record.outputTokenPrice || 1)
        this.architectureForm.enabled = !!record.enabled
        this.architectureForm.sort = Number(record.sort || 0)
        this.architectureForm.remark = record.remark || ''
      } else {
        this.resetArchitectureForm()
      }
      this.architectureModalVisible = true
    },
    closeArchitectureModal() {
      this.architectureModalVisible = false
      this.resetArchitectureForm()
    },
    handleSaveArchitecture() {
      const payload = {
        id: this.architectureForm.id,
        name: this.architectureForm.name,
        modelName: this.architectureForm.modelName,
        billingMultiplier: Number(this.architectureForm.billingMultiplier || 1),
        enabled: !!this.architectureForm.enabled,
        sort: Number(this.architectureForm.sort || 0),
        remark: this.architectureForm.remark || '',
      }
      this.architectureSaving = true
      const action = payload.id ? updateAIModelArchitecture(payload) : createAIModelArchitecture(payload)
      action.then(res => {
        if (res?.status === 200) {
          message.success('保存成功')
          this.closeArchitectureModal()
          this.loadAIModelArchitectures()
          return
        }
        message.error(res?.message || '保存失败')
      }).catch(() => {
        message.error('保存失败')
      }).finally(() => {
        this.architectureSaving = false
      })
    },
    handleDeleteArchitecture(record) {
      deleteAIModelArchitecture({ id: record.id }).then(res => {
        if (res?.status === 200) {
          message.success('删除成功')
          this.loadAIModelArchitectures()
          return
        }
        message.error(res?.message || '删除失败')
      }).catch(() => {
        message.error('删除失败')
      })
    },
    formatMultiplier(value, record) {
      const multiplier = Number(value || record?.inputTokenPrice || record?.outputTokenPrice || 1)
      return Number.isFinite(multiplier) && multiplier > 0 ? `${multiplier.toFixed(4)} 倍` : '1.0000 倍'
    },
  }
})
</script>

<style scoped>
.page-card {
  border-radius: 20px;
}

.page-alert {
  margin-bottom: 16px;
}

.hint {
  margin-top: 6px;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 1.6;
}

.hint-block {
  margin-top: 6px;
}

.model-editor {
  display: flex;
  gap: 12px;
  align-items: flex-start;
}

.model-editor :deep(.ant-input-textarea) {
  flex: 1;
}

.fetch-btn {
  flex-shrink: 0;
}

.model-tags {
  margin-top: 10px;
}

.test-alert {
  margin-bottom: 16px;
}

.test-result-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.test-result-block {
  padding: 12px 14px;
  border-radius: 12px;
  background: #fafafa;
  border: 1px solid #f0f0f0;
}

.test-result-title {
  margin-bottom: 8px;
  font-size: 13px;
  font-weight: 600;
  color: #1f2937;
}

.test-result-pre {
  margin: 0;
  max-height: 240px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 12px;
  line-height: 1.6;
  color: #334155;
}

.pricing-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: 16px;
  margin-top: 16px;
}

.sub-card {
  border-radius: 20px;
}

.sub-card-title-wrap {
  display: flex;
  align-items: center;
}

.sub-card-title {
  font-size: 18px;
  font-weight: 700;
  color: #1f2937;
}

.sub-card-subtitle {
  margin-top: 6px;
  color: #6b7280;
  font-size: 13px;
}

.architecture-table-wrap {
  width: 100%;
  overflow-x: auto;
}

@media (max-width: 768px) {
  .page-card,
  .sub-card {
    border-radius: 8px;
  }

  .model-editor {
    display: grid;
  }

  .fetch-btn {
    width: 100%;
  }

  .sub-card-title {
    font-size: 16px;
  }
}
</style>
