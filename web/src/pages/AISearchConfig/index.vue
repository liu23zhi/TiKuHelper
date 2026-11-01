<template>
  <div class="console-page ai-settings-page">
    <ConsolePageHeading title="AI 搜索配置" description="管理服务连接、执行预算和模型价格。">
      <template #actions><a-space wrap class="section-actions">
        <a-button @click="handleLoad" :loading="tabLoading">刷新</a-button>
        <a-button type="primary" @click="handleSave" :loading="saving">保存配置</a-button>
      </a-space></template>
    </ConsolePageHeading>

    <a-alert
      type="info"
      show-icon
      class="page-alert"
      message="AI 搜索只在 use=ai 的请求模式下生效。账号余额必须大于 0；显式传入 url、key、model 时优先使用请求参数，未显式提供时才回退到这里的系统默认配置。"
    />

    <a-alert
      v-if="pricingGaps.configuredModel && !pricingGaps.configuredModelPriced"
      type="warning"
      show-icon
      class="page-alert"
      message="当前默认模型还没有配置价格"
      :description="`模型「${pricingGaps.configuredModel}」不在模型价格表中，调用时会按 0 元计费（不扣费），账单中会记录为未定价。请在下方「模型价格表」中新增该模型，并填写输入 / 缓存输入 / 输出单价。`"
    >
      <template #action>
        <a-button size="small" type="primary" ghost @click="openPriceModalForConfiguredModel">
          去设置价格
        </a-button>
      </template>
    </a-alert>

    <a-alert
      v-else-if="pricingGaps.unresolvedModels.length"
      type="warning"
      show-icon
      class="page-alert"
      message="模型列表中有模型还没有配置价格"
      :description="`以下模型缺少价格，命中时会按 0 元计费：${pricingGaps.unresolvedModels.join('、')}。请在下方「模型价格表」中补充单价。`"
    />

    <a-form layout="vertical" class="ai-config-form" autocomplete="off">
      <ConsolePanel title="连接与模型" description="配置 AI 服务连接、认证信息和可用模型。">
        <template #actions><a-button @click="handleOpenTestDialog">测试</a-button></template>
        <div class="console-form-grid connection-grid">
      <a-form-item label="是否启用" class="full-width">
        <a-switch v-model:checked="formState.enabled" />
        <span class="switch-description">{{ formState.enabled ? '已启用 AI 搜索' : 'AI 搜索已关闭' }}</span>
      </a-form-item>

      <a-form-item label="服务地址">
        <a-input
          v-model:value="formState.baseUrl"
          name="ai-service-endpoint"
          type="url"
          autocomplete="off"
          autocapitalize="none"
          :spellcheck="false"
          placeholder="例如 https://api.openai.com 或 https://api.openai.com/v1"
        />
        <div class="hint">如果地址未包含 /v1，保存时会提示你是否补全；也可以保持原样保存。</div>
      </a-form-item>

      <a-form-item label="API Key">
        <a-input-password
          v-model:value="formState.apiKey"
          name="ai-service-api-key"
          autocomplete="new-password"
          autocapitalize="none"
          :spellcheck="false"
          placeholder="请输入 API Key"
        />
      </a-form-item>

      <a-form-item label="默认模型" class="full-width">
        <a-input v-model:value="formState.model" placeholder="可手动输入模型名" />
        <div class="hint">也可以点击下方模型列表快速填入。</div>
        <div v-if="pricingGaps.configuredModel && !pricingGaps.configuredModelPriced" class="hint hint-warning">
          该模型尚未配置价格，调用会按 0 元计费。
          <a @click="openPriceModalForConfiguredModel">去设置价格</a>
        </div>
      </a-form-item>

      <a-form-item label="模型列表" class="full-width">
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
        <div class="hint">保存时会以此处内容作为可用模型列表，点击下方标签可填入默认模型。</div>
        <div class="hint">标签上带「未定价」的模型需要先在下方「模型价格表」中补价，否则会按 0 元计费。</div>
        <a-space wrap class="model-tags">
          <a-tag
            v-for="item in modelOptions"
            :key="item.value"
            :color="isModelPriced(item.value, priceRows) ? 'blue' : 'orange'"
            style="cursor: pointer"
            @click="selectModel(item.value)"
          >
            {{ item.label }}<span v-if="!isModelPriced(item.value, priceRows)">（未定价）</span>
          </a-tag>
          <span v-if="modelOptions.length === 0" class="hint">暂无模型列表，请先手动填写或获取远程模型。</span>
        </a-space>
      </a-form-item>

        </div>
      </ConsolePanel>

      <ConsolePanel title="执行策略" description="设置模型轮次、工具预算和输出要求。">
        <template #actions><a-button @click="loadSearchEngineInfo" :loading="searchEngineLoading">查看深度思考能力</a-button></template>
        <div class="console-form-grid execution-grid">
      <a-divider orientation="left" class="full-width">AI 搜索预算</a-divider>
      <a-form-item label="模型最大调用次数">
        <a-input-number
          v-model:value="formState.maxAISearchModelCalls"
          :min="MIN_AI_SEARCH_MODEL_CALLS"
          :precision="0"
          :step="1"
          :parser="parseIntegerInput"
          style="width: 180px"
        />
        <span class="unit-label">次</span>
        <div class="hint">每次 AI 搜索最多执行的模型逻辑轮次，默认 4 轮，填写正整数。同一轮内的失败重试不额外占用轮次。每一轮提示中都会告知模型剩余调用次数；实际执行仍受单次外呼超时和请求取消控制。</div>
      </a-form-item>

      <a-form-item label="最大搜索次数">
        <a-input-number
          v-model:value="formState.maxAISearches"
          :min="MIN_AI_SEARCHES"
          :precision="0"
          :step="1"
          :parser="parseIntegerInput"
          style="width: 180px"
        />
        <span class="unit-label">次</span>
        <div class="hint">深度思考模式下，每次 AI 搜索最多调用搜索 API 的次数，默认 3 次，填写正整数。每一轮模型调用前都会告知剩余搜索次数，最后一轮模型调用直接输出答案。</div>
      </a-form-item>

      <a-form-item label="每轮重试次数">
        <a-input-number
          v-model:value="formState.maxAISearchRetries"
          :min="MIN_AI_SEARCH_RETRIES"
          :precision="0"
          :step="1"
          :parser="parseIntegerInput"
          style="width: 180px"
        />
        <span class="unit-label">次</span>
        <div class="hint">每轮模型调用最多尝试的机会（包含首次调用），默认 3 次，填写正整数。对可重试的失败会在本轮内自动重试，耗尽后才结束本轮。</div>
      </a-form-item>

      <a-form-item label="联网搜索单价（元/次）">
        <a-input-number
          v-model:value="formState.searchPricePerCall"
          :min="0"
          :precision="6"
          :step="0.01"
          style="width: 180px"
        />
        <span class="unit-label">元 / 次</span>
        <div class="hint">默认 0 元。每次成功完成联网搜索 API 调用都会单独记一条账单，单价为 0 时也会保留 0 元明细，并合并显示在对应搜题账单详情中；命中本次 AI 搜索内的相同查询缓存不会重复计价。</div>
      </a-form-item>

      <a-divider orientation="left" class="full-width">网页读取与 Prompt</a-divider>

      <a-form-item label="网页读取次数">
        <a-input-number
          v-model:value="formState.maxAIWebReads"
          :min="MIN_AI_WEB_READS"
          :precision="0"
          :step="1"
          :parser="parseIntegerInput"
          style="width: 180px"
        />
        <span class="unit-label">次</span>
        <div class="hint">深度思考模式下，模型每次 AI 搜索最多调用网页读取工具的次数，默认 5 次，填写正整数。普通模式仍保持原先的一次性回复，不会调用网页读取。</div>
      </a-form-item>

      <a-form-item label="网页读取单价（元/次）">
        <a-input-number
          v-model:value="formState.webReadPricePerCall"
          :min="0"
          :precision="6"
          :step="0.01"
          style="width: 180px"
        />
        <span class="unit-label">元 / 次</span>
        <div class="hint">默认 0 元。每次成功完成网页读取都会单独写入一条账单，单价为 0 时也会保留 0 元明细，并合并到对应的搜题账单详情中；失败调用只进入诊断。</div>
      </a-form-item>

      <a-form-item label="图片 OCR 单价（元/次）">
        <a-input-number
          v-model:value="formState.imageOCRPricePerCall"
          :min="0"
          :precision="6"
          :step="0.01"
          style="width: 180px"
        />
        <span class="unit-label">元 / 次</span>
        <div class="hint">默认 0 元。配置 UAPI（uapi-）密钥后，服务器会先下载题图，再将转换后的本地图片上传 OCR；原图仍发送给视觉模型。成功 OCR 调用在单价为 0 时也会保留 0 元账单明细，失败调用只进入诊断。</div>
      </a-form-item>

      <a-divider orientation="left" class="full-width">上下文压缩</a-divider>
      <a-form-item label="自动压缩上下文" class="full-width">
        <a-switch v-model:checked="formState.contextCompressionEnabled" />
        <div class="hint">按模型价格表中的上下文窗口估算触发比例；未设置窗口时，仅在上游明确返回上下文超限后尝试。压缩后会重新注入原题和系统规则，不将估算值当作精确 Token 数。</div>
      </a-form-item>
      <template v-if="formState.contextCompressionEnabled">
        <a-form-item label="压缩执行顺序" class="full-width">
          <AICompressionOrder v-model="formState.contextCompressionOrder" />
        </a-form-item>
        <a-form-item label="压缩触发比例">
          <a-input-number v-model:value="formState.contextCompressionTriggerPercent" :min="50" :max="95" :precision="0" :step="1" style="width: 140px" />
          <span class="unit-label">%</span>
          <div class="hint">估算上下文达到窗口的此比例时触发，范围 50～95%。</div>
        </a-form-item>
        <a-form-item label="压缩目标比例">
          <a-input-number v-model:value="formState.contextCompressionTargetPercent" :min="10" :max="Math.max(10, Number(formState.contextCompressionTriggerPercent) - 1)" :precision="0" :step="1" style="width: 140px" />
          <span class="unit-label">%</span>
          <div class="hint">至少 10%，且必须小于触发比例；保留原题和规则所需的空间。</div>
        </a-form-item>
        <a-form-item label="压缩最大尝试次数" class="full-width">
          <a-input-number v-model:value="formState.contextCompressionMaxAttempts" :min="1" :precision="0" :step="1" :parser="parseIntegerInput" style="width: 140px" />
          <span class="unit-label">次</span>
          <div class="hint">填写正整数，含首次；独立压缩、服务器压缩测试和本地摘要各阶段按此次数尝试，长历史可分块摘要后归并。正常答题的服务器自动压缩与同次模型请求共用每轮重试次数；回退不重复已完成工具，实际模型用量均计入账单。</div>
        </a-form-item>
      </template>

      <a-form-item label="Prompt 设置" class="full-width">
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
        </div>
      </ConsolePanel>
    </a-form>

    <ConsolePanel title="图片 OCR 缓存" description="按图片 URL 和图片内容指纹复用成功 OCR 结果；每次请求仍会下载图片确认内容未变化。">
      <template #actions>
        <a-space wrap>
          <a-button @click="loadAIOCRCacheStatus" :loading="ocrCacheLoading">刷新</a-button>
          <a-popconfirm
            title="确定清空图片 OCR 缓存吗？"
            description="正在执行的识别请求会继续完成，但其旧结果不会重新写入缓存。"
            ok-text="清空"
            cancel-text="取消"
            @confirm="handleClearAIOCRCache"
          >
            <a-button danger :disabled="!ocrCacheStatus || ocrCacheStatus.entries === 0" :loading="ocrCacheClearing">清空缓存</a-button>
          </a-popconfirm>
        </a-space>
      </template>
      <a-alert v-if="ocrCacheError" type="error" show-icon :message="ocrCacheError" />
      <div class="ocr-cache-grid">
        <div class="ocr-cache-stat">
          <span class="ocr-cache-stat__label">识别服务</span>
          <a-tag :color="ocrCacheStatus?.configured ? 'green' : 'default'">
            {{ ocrCacheStatus?.configured ? 'UAPI 已配置' : '未配置有效 UAPI 密钥' }}
          </a-tag>
        </div>
        <div class="ocr-cache-stat">
          <span class="ocr-cache-stat__label">缓存条目</span>
          <strong>{{ ocrCacheStatus?.entries ?? 0 }}</strong>
        </div>
        <div class="ocr-cache-stat">
          <span class="ocr-cache-stat__label">最近更新</span>
          <span>{{ formatAIOCRCacheTime(ocrCacheStatus?.latestUpdatedAt) }}</span>
        </div>
      </div>
      <div class="hint">命中缓存仍按现有搜题计费口径处理；清空只删除已持久化的 OCR 结果，不会取消正在执行的请求。</div>
    </ConsolePanel>

    <AISearchCapabilityDialog
      v-model:open="capabilityModalVisible"
      :loading="capabilityLoading"
      :error="capabilityError"
      :detail="searchEngineDetail"
      :cards="usedCapabilityCards"
      :rows="usedUsageRows"
      :columns="usageColumns"
    />

    <div class="pricing-grid">
      <ConsolePanel title="模型价格表" description="单价单位为 元 / 百万 token（人民币，无汇率换算）。实际扣费 =（输入 − 缓存输入）× 输入价 + 缓存输入 × 缓存价 + 输出 × 输出价。">
        <template #actions>
          <a-space wrap>
            <a-popconfirm title="确定恢复内置默认价格表吗？自定义价格会被清空。" ok-text="确定" cancel-text="取消" @confirm="handleResetModelPrices">
              <a-button :loading="priceResetting">恢复默认</a-button>
            </a-popconfirm>
            <a-button @click="loadAIModelPrices" :loading="priceLoading">刷新</a-button>
            <a-button type="primary" @click="openPriceModal()">新增模型价格</a-button>
          </a-space>
        </template>

        <a-form class="staged-billing-settings" :label-col="labelCol" :wrapper-col="wrapperCol">
          <a-form-item label="启用阶段计费">
            <a-switch v-model:checked="formState.enableAIStagedTokenBilling" />
            <div class="hint">按每次实际上游模型响应的总 token 数应用阶段倍率；具体阈值和倍率以你下方配置的阶段表为准。关闭时保持现有价格表计费。</div>
          </a-form-item>
          <a-form-item label="平滑变化倍率">
            <a-switch
              v-model:checked="formState.smoothAIStagedTokenMultiplier"
              :disabled="!formState.enableAIStagedTokenBilling"
            />
            <div class="hint">开启后在相邻阶段节点之间线性平滑变化；阶段计费关闭时只保存此选项，不影响计费。</div>
          </a-form-item>
        </a-form>

        <div class="staged-points-editor">
          <div class="staged-points-editor__header">
            <div>
              <div class="sub-card-title">阶段倍率价格表</div>
              <div class="sub-card-subtitle">按单次实际上游模型响应的总 token 数匹配倍率；固定模式取所在区间的节点倍率，平滑模式在相邻节点之间线性插值。</div>
            </div>
            <a-space>
              <a-button size="small" @click="resetStagedMultiplierPoints">恢复默认阶段表</a-button>
              <a-button size="small" type="primary" @click="addStagedMultiplierPoint">新增阶段</a-button>
            </a-space>
          </div>
          <ConsoleTableShell label="阶段倍率价格表"><a-table
            bordered
            size="small"
            :row-key="stagedPointRowKey"
            :pagination="false"
            :data-source="formState.stagedTokenMultiplierPoints"
            :columns="stagedPointColumns"
            table-layout="fixed"
            :scroll="{ x: sumTableColumnWidths(stagedPointColumns) }"
          >
            <template #bodyCell="{ column, record, index }">
              <a-input v-if="column.key === 'tokens'" v-model:value="record.tokens" inputmode="numeric" autocomplete="off" style="width: 180px" />
              <a-input-number v-else-if="column.key === 'multiplier'" v-model:value="record.multiplier" :precision="6" :step="0.1" style="width: 140px" />
              <a-button v-else-if="column.key === 'action'" type="link" danger :disabled="index === 0 || formState.stagedTokenMultiplierPoints.length <= 1" @click="removeStagedMultiplierPoint(index)">删除</a-button>
            </template>
          </a-table></ConsoleTableShell>
          <div v-if="stagedPointError" class="hint hint-warning">{{ stagedPointError }}；修正后再保存。</div>
          <div v-else class="hint">至少保留首个 0 token 节点；后续阈值必须严格递增，倍率必须为有限数值且只能保持不变或递减。阈值不设业务上限，倍率推荐使用 0.3x～4x。</div>
        </div>

        <ConsoleTableShell label="模型价格表">
          <a-table
            bordered
            row-key="id"
            :data-source="priceRows"
            :columns="priceColumns"
            :loading="priceLoading"
            :pagination="{ pageSize: 8, size: 'small' }"
            table-layout="fixed"
            :scroll="{ x: sumTableColumnWidths(priceColumns) }"
          >
            <template #bodyCell="{ column, record }">
              <span v-if="['inputPrice', 'cachedPrice', 'cacheWritePrice', 'outputPrice'].includes(column.key)">{{ formatPriceAmount(record[column.key]) }}</span>
              <span v-else-if="column.key === 'contextWindowTokens'">{{ formatAIModelContextWindowTokens(record.contextWindowTokens) }}</span>
              <span v-else-if="column.key === 'multiplier'" :class="{ 'multiplier-cell--custom': Number(record.multiplier || 1) !== 1 }">
                ×{{ formatMultiplier(record.multiplier) }}
              </span>
              <span v-else-if="column.key === 'remark'">{{ record.remark || '-' }} <a-tag v-if="record.isUserModified" color="orange">已改</a-tag></span>
              <a-space v-else-if="column.key === 'action'" size="small" wrap>
                <a-button type="link" style="padding: 0" @click="openPriceModal(record)">编辑</a-button>
                <a-popconfirm title="确定删除该模型价格吗？" ok-text="确定" cancel-text="取消" @confirm="handleDeleteModelPrice(record)">
                  <a-button type="link" danger style="padding: 0">删除</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table>
        </ConsoleTableShell>
      </ConsolePanel>

    </div>

    <a-modal
      v-model:open="priceModalVisible"
      :title="priceForm.id ? '编辑模型价格' : '新增模型价格'"
      ok-text="确定"
      cancel-text="取消"
      :confirm-loading="priceSaving"
      @ok="handleSaveModelPrice"
      @cancel="closePriceModal"
    >
      <a-form :label-col="modalLabelCol" :wrapper-col="modalWrapperCol">
        <a-form-item label="模型名称" required>
          <a-input v-model:value="priceForm.modelName" placeholder="需与 AI 搜索配置中的模型名一致，例如 GPT-5.6 Sol" />
        </a-form-item>
        <a-form-item label="显示名称">
          <a-input v-model:value="priceForm.displayName" placeholder="留空时与模型名称相同" />
        </a-form-item>
        <a-form-item label="上下文窗口">
          <a-input v-model:value="priceForm.contextWindowTokens" inputmode="numeric" autocomplete="off" placeholder="完整 Token 数，0 表示未设置" />
          <div class="hint">填写供应商公布的上下文 Token 数，以其输入/输出预算规则为准；此项不是单次输出上限。</div>
        </a-form-item>
        <a-form-item label="输入单价">
          <a-input-number v-model:value="priceForm.inputPrice" :min="0" :precision="6" :step="0.01" style="width: 100%" />
          <div class="hint">元 / 百万 token，未命中缓存的输入部分按此计价。</div>
        </a-form-item>
        <a-form-item label="缓存输入单价">
          <a-input-number v-model:value="priceForm.cachedPrice" :min="0" :precision="6" :step="0.01" style="width: 100%" />
          <div class="hint">元 / 百万 token，上游返回的 cached_tokens 按此计价。</div>
        </a-form-item>
        <a-form-item label="缓存写入单价">
          <a-input-number v-model:value="priceForm.cacheWritePrice" :min="0" :precision="6" :step="0.01" style="width: 100%" />
          <div class="hint">元 / 百万 token，缓存写入（cache writes）部分按此计价；上游不提供该价格时填 0。</div>
        </a-form-item>
        <a-form-item label="输出单价">
          <a-input-number v-model:value="priceForm.outputPrice" :min="0" :precision="6" :step="0.01" style="width: 100%" />
          <div class="hint">元 / 百万 token。</div>
        </a-form-item>
        <a-form-item label="倍率">
          <a-input-number v-model:value="priceForm.multiplier" :min="0.0001" :precision="4" :step="0.1" style="width: 100%" />
          <div class="hint">最终扣费 =（输入 + 缓存输入 + 缓存写入 + 输出 各项金额）× 倍率；默认 1，不改单价即可整体加价或打折。</div>
        </a-form-item>
        <a-form-item label="备注">
          <a-input v-model:value="priceForm.remark" placeholder="备注" />
        </a-form-item>
      </a-form>
    </a-modal>

    <AISearchTestDialog v-model:open="testModalVisible" :config="formState" :model-options="modelOptions" :saved-compression-order="savedCompressionOrder" />
  </div>
</template>

<script>
import { defineComponent, computed, onMounted, reactive, ref } from 'vue';
import { message, Modal } from 'ant-design-vue';
import AISearchCapabilityDialog from './AISearchCapabilityDialog.vue';
import AISearchTestDialog from './AISearchTestDialog.vue';
import AICompressionOrder from './AICompressionOrder.vue';
import { sumTableColumnWidths } from '@/utils/tableLayout.mjs';
import { buildAIContextCompressionSettings } from '@/utils/aiSearchConfigHelpers.mjs';
import {
  getAISearchConfig,
  getAISearchModels,
  updateAISearchConfig,
  getAIModelPrices,
  getUAPISearchEngines,
  getUAPIUsageStatus,
  getAIOCRCacheStatus,
  clearAIOCRCache,
  createAIModelPrice,
  updateAIModelPrice,
  deleteAIModelPrice,
  resetAIModelPrices,
} from '@/api/TikuComponentApi';
import {
  buildModelNameOptions,
  buildModelNameText,
  buildAISearchConfigSavePayload,
  buildAIModelPriceSavePayload,
  buildAIModelPricingGaps,
  formatAIModelContextWindowTokens,
  isModelPriced,
  filterRemoteModelNames,
  DEFAULT_AI_STAGED_TOKEN_MULTIPLIER_POINTS,
} from '@/utils/aiSearchModelHelpers.mjs';
import {
  createStagedPointRowKey,
  getStagedMultiplierPointsError,
  insertStagedMultiplierPoint,
} from '@/utils/aiStagedMultiplierHelpers.mjs';
import { formatMoneyCny, formatMoneyValue } from '@/utils/moneyFormat.mjs';

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

const DEFAULT_MAX_AI_SEARCH_MODEL_CALLS = 4
const DEFAULT_MAX_AI_SEARCHES = 3
const DEFAULT_MAX_AI_SEARCH_RETRIES = 3
const MIN_AI_SEARCH_MODEL_CALLS = 1
const MIN_AI_SEARCHES = 1
const MIN_AI_SEARCH_RETRIES = 1
const DEFAULT_MAX_AI_WEB_READS = 5
const MIN_AI_WEB_READS = 1

function parseIntegerInput(value) {
  if (value === null || value === undefined || value === '') {
    return ''
  }
  return String(value).replace(/[^0-9]/g, '')
}

function normalizeAISearchBudget(value, fallback) {
  const parsed = Number(value)
  if (!Number.isSafeInteger(parsed) || parsed < 1) {
    return fallback
  }
  return parsed
}

function normalizeAISearchRetries(value, fallback = DEFAULT_MAX_AI_SEARCH_RETRIES) {
  const parsed = Number(value)
  if (!Number.isSafeInteger(parsed) || parsed < MIN_AI_SEARCH_RETRIES) {
    return fallback
  }
  return parsed
}

function normalizeAIWebReads(value, fallback = DEFAULT_MAX_AI_WEB_READS) {
  const parsed = Number(value)
  if (!Number.isSafeInteger(parsed) || parsed < MIN_AI_WEB_READS) {
    return fallback
  }
  return parsed
}

function normalizeUsageRows(payload) {
  if (Array.isArray(payload?.endpoints)) {
    return payload.endpoints
      .filter(item => item && typeof item === 'object')
      .map(item => ({ path: String(item.path || ''), count: Number(item.count || 0) }))
      .filter(item => item.path)
  }
  if (payload?.path) {
    return [{ path: String(payload.path), count: Number(payload.count || 0) }]
  }
  return []
}

export default defineComponent({
  name: 'AISearchConfig',
  components: { AISearchCapabilityDialog, AISearchTestDialog, AICompressionOrder },
  setup() {
    const tabLoading = ref(true)
    const saving = ref(false)
    const fetchingModels = ref(false)
    const searchEngineLoading = ref(false)
    const searchEngineInfo = ref(null)
    const capabilityModalVisible = ref(false)
    const capabilityError = ref('')
    const usageInfo = ref(null)
    const testModalVisible = ref(false)
    const ocrCacheLoading = ref(false)
    const ocrCacheClearing = ref(false)
    const ocrCacheStatus = ref(null)
    const ocrCacheError = ref('')
    const savedCompressionOrder = ref(buildAIContextCompressionSettings().contextCompressionOrder)
    const fixedTemperatureText = '0'
    const fixedTopPText = '0'
    const formState = reactive({
      ...buildAIContextCompressionSettings(),
      enabled: false,
      baseUrl: '',
      apiKey: '',
      model: '',
      manualModelsText: '',
      prompt: '',
      remark: '',
      temperature: 0,
      topP: 0,
      maxAISearchModelCalls: DEFAULT_MAX_AI_SEARCH_MODEL_CALLS,
      maxAISearches: DEFAULT_MAX_AI_SEARCHES,
      maxAISearchRetries: DEFAULT_MAX_AI_SEARCH_RETRIES,
      searchPricePerCall: 0,
      maxAIWebReads: DEFAULT_MAX_AI_WEB_READS,
      webReadPricePerCall: 0,
      imageOCRPricePerCall: 0,
      enableAIStagedTokenBilling: false,
      smoothAIStagedTokenMultiplier: false,
      stagedTokenMultiplierPoints: DEFAULT_AI_STAGED_TOKEN_MULTIPLIER_POINTS.map(point => ({ ...point, tokens: String(point.tokens) })),
    })

    const priceRows = ref([])
    const priceLoading = ref(false)
    const priceSaving = ref(false)
    const priceResetting = ref(false)
    const priceModalVisible = ref(false)
    const priceForm = reactive({
      id: null,
      modelName: '',
      displayName: '',
      contextWindowTokens: 0,
      inputPrice: 0,
      cachedPrice: 0,
      cacheWritePrice: 0,
      outputPrice: 0,
      multiplier: 1,
      remark: '',
    })

    const modelOptions = computed(() => {
      return buildModelNameOptions(formState.manualModelsText)
    })
    const stagedPointError = computed(() => getStagedMultiplierPointsError(formState.stagedTokenMultiplierPoints))
    const stagedPointRowKey = createStagedPointRowKey()

    const usageRows = computed(() => normalizeUsageRows(usageInfo.value))
    const searchUsageRows = computed(() => usageRows.value.filter(item => /\/search\/(?:aggregate|engines)(?:\/|$)/i.test(item.path) && item.count > 0))
    const webReadUsageRows = computed(() => usageRows.value.filter(item => /\/web\/tomarkdown(?:\/|$)/i.test(item.path) && item.count > 0))
    const ocrUsageRows = computed(() => usageRows.value.filter(item => /\/image\/ocr(?:\/|$)/i.test(item.path) && item.count > 0))
    const usedUsageRows = computed(() => [...searchUsageRows.value, ...webReadUsageRows.value, ...ocrUsageRows.value])
    const searchEngineDetail = computed(() => {
      const response = searchEngineInfo.value
      if (!response?.engine || typeof response.engine !== 'object') return null
      return {
        engine: response.engine,
        limits: response.limits || {},
        features: Array.isArray(response.engine.features) ? response.engine.features : [],
        supportedParameters: Array.isArray(response.supported_parameters) ? response.supported_parameters : [],
      }
    })
    const usedCapabilityCards = computed(() => {
      const cards = []
      if (searchUsageRows.value.length) {
        const count = searchUsageRows.value.reduce((sum, item) => sum + item.count, 0)
        const engineAvailable = searchEngineInfo.value?.engine?.available
        cards.push({
          key: 'search',
          title: '联网搜索 API',
          status: searchEngineInfo.value ? (engineAvailable ? '可用' : '不可用') : '已调用',
          color: searchEngineInfo.value ? (engineAvailable ? 'green' : 'red') : 'blue',
          detail: searchEngineInfo.value?.engine?.display_name || searchEngineInfo.value?.engine?.name || '搜索端点已有实际调用记录',
          count,
        })
      }
      if (webReadUsageRows.value.length) {
        const count = webReadUsageRows.value.reduce((sum, item) => sum + item.count, 0)
        cards.push({
          key: 'web-read',
          title: '网页转 Markdown API',
          status: '已调用',
          color: 'green',
          detail: '异步网页读取端点已有实际调用记录',
          count,
        })
      }
      if (ocrUsageRows.value.length) {
        const count = ocrUsageRows.value.reduce((sum, item) => sum + item.count, 0)
        cards.push({
          key: 'image-ocr',
          title: '图片 OCR API',
          status: '已调用',
          color: 'green',
          detail: '图片 OCR 端点已有实际调用记录',
          count,
        })
      }
      return cards
    })
    const usageColumns = [
      { title: '调用位置', dataIndex: 'path', key: 'path', width: 540 },
      { title: '调用次数', dataIndex: 'count', key: 'count', width: 120 },
    ]

    // 哪些已配置的模型还没有价格：用于页面提示，避免命中后按 0 元计费却无人察觉。
    const pricingGaps = computed(() => buildAIModelPricingGaps(formState, priceRows.value))

    const priceColumns = [
      { title: 'ID', dataIndex: 'id', key: 'id', width: 60 },
      { title: '模型 ID', dataIndex: 'modelName', key: 'modelName', width: 190 },
      { title: '显示名称', dataIndex: 'displayName', key: 'displayName', width: 165 },
      { title: '上下文窗口（Token）', dataIndex: 'contextWindowTokens', key: 'contextWindowTokens', width: 150 },
      { title: '输入（￥/M）', dataIndex: 'inputPrice', key: 'inputPrice', width: 105 },
      { title: '缓存输入（￥/M）', dataIndex: 'cachedPrice', key: 'cachedPrice', width: 120 },
      { title: '缓存写入（￥/M）', dataIndex: 'cacheWritePrice', key: 'cacheWritePrice', width: 125 },
      { title: '输出（￥/M）', dataIndex: 'outputPrice', key: 'outputPrice', width: 105 },
      { title: '倍率', dataIndex: 'multiplier', key: 'multiplier', width: 85 },
      { title: '备注', dataIndex: 'remark', key: 'remark', width: 170 },
      { title: '操作', dataIndex: 'action', key: 'action', width: 110 },
    ]

    const stagedPointColumns = [
      { title: '总 token 起点', dataIndex: 'tokens', key: 'tokens', width: 220 },
      { title: '倍率', dataIndex: 'multiplier', key: 'multiplier', width: 190 },
      { title: '操作', dataIndex: 'action', key: 'action', width: 90 },
    ]

    const loadConfig = () => {
      tabLoading.value = true
      getAISearchConfig().then(res => {
        if (res?.status === 200) {
          const data = res?.data || {}
          Object.assign(formState, buildAIContextCompressionSettings(data))
          savedCompressionOrder.value = [...formState.contextCompressionOrder]
          formState.enabled = !!data.enabled
          formState.baseUrl = data.baseUrl || ''
          formState.apiKey = data.apiKey || ''
          formState.model = data.model || ''
          formState.manualModelsText = buildModelNameText(data.manualModels || [])
          formState.prompt = data.prompt || ''
          formState.remark = data.remark || ''
          formState.temperature = data.temperature ?? 0
          formState.topP = data.topP ?? 0
          formState.maxAISearchModelCalls = normalizeAISearchBudget(data.maxAISearchModelCalls, DEFAULT_MAX_AI_SEARCH_MODEL_CALLS)
          formState.maxAISearches = normalizeAISearchBudget(data.maxAISearches, DEFAULT_MAX_AI_SEARCHES)
          formState.maxAISearchRetries = normalizeAISearchRetries(data.maxAISearchRetries, DEFAULT_MAX_AI_SEARCH_RETRIES)
          formState.searchPricePerCall = Number(data.searchPricePerCall ?? 0)
          formState.maxAIWebReads = normalizeAIWebReads(data.maxAIWebReads, DEFAULT_MAX_AI_WEB_READS)
          formState.webReadPricePerCall = Number(data.webReadPricePerCall ?? 0)
          formState.imageOCRPricePerCall = Number(data.imageOCRPricePerCall ?? 0)
          formState.enableAIStagedTokenBilling = !!data.enableAIStagedTokenBilling
          formState.smoothAIStagedTokenMultiplier = !!data.smoothAIStagedTokenMultiplier
          formState.stagedTokenMultiplierPoints = Array.isArray(data.stagedTokenMultiplierPoints) && data.stagedTokenMultiplierPoints.length
            ? data.stagedTokenMultiplierPoints.map(point => ({ tokens: String(point.tokens ?? 0), multiplier: Number(point.multiplier ?? 1) }))
            : DEFAULT_AI_STAGED_TOKEN_MULTIPLIER_POINTS.map(point => ({ ...point, tokens: String(point.tokens) }))
        } else {
          message.error(res?.message || '获取 AI 搜索配置失败')
        }
      }).catch(() => {
        message.error('获取 AI 搜索配置失败')
      }).finally(() => {
        tabLoading.value = false
      })
    }

    const loadAIModelPrices = () => {
      priceLoading.value = true
      return getAIModelPrices().then(res => {
        if (res?.status === 200) {
          priceRows.value = Array.isArray(res?.data) ? res.data : []
        } else {
          message.error(res?.message || '获取模型价格表失败')
        }
      }).catch(() => {
        message.error('获取模型价格表失败')
      }).finally(() => {
        priceLoading.value = false
      })
    }

    const loadSearchEngineInfo = () => {
      capabilityModalVisible.value = true
      searchEngineLoading.value = true
      capabilityError.value = ''
      searchEngineInfo.value = null
      usageInfo.value = null
      return Promise.allSettled([getUAPISearchEngines(), getUAPIUsageStatus()]).then(([engineResult, usageResult]) => {
        const errors = []
        if (engineResult.status === 'fulfilled' && engineResult.value?.status === 200) {
          searchEngineInfo.value = engineResult.value?.data || null
        } else {
          errors.push('搜索引擎状态获取失败')
        }
        if (usageResult.status === 'fulfilled' && usageResult.value?.status === 200) {
          usageInfo.value = usageResult.value?.data || null
        } else {
          errors.push('UAPI 端点使用统计获取失败')
        }
        capabilityError.value = errors.length ? `${errors.join('；')}，请检查 UApiPro 密钥或服务状态` : ''
      }).catch(() => {
        capabilityError.value = '深度思考能力获取失败，请检查 UApiPro 密钥或服务状态'
      }).finally(() => {
        searchEngineLoading.value = false
      })
    }

    const loadAIOCRCacheStatus = () => {
      ocrCacheLoading.value = true
      ocrCacheError.value = ''
      return getAIOCRCacheStatus().then(res => {
        if (res?.status !== 200) {
          throw new Error(res?.message || '获取图片 OCR 缓存状态失败')
        }
        ocrCacheStatus.value = res?.data || { configured: false, entries: 0 }
      }).catch(error => {
        ocrCacheError.value = error?.message || '获取图片 OCR 缓存状态失败'
      }).finally(() => {
        ocrCacheLoading.value = false
      })
    }

    onMounted(() => {
      loadConfig()
      loadAIModelPrices()
      loadAIOCRCacheStatus()
    })

    const resetPriceForm = () => {
      priceForm.id = null
      priceForm.modelName = ''
      priceForm.displayName = ''
      priceForm.contextWindowTokens = 0
      priceForm.inputPrice = 0
      priceForm.cachedPrice = 0
      priceForm.cacheWritePrice = 0
      priceForm.outputPrice = 0
      priceForm.multiplier = 1
      priceForm.remark = ''
    }

    return {
      labelCol: { style: { width: '140px' } },
      wrapperCol: { span: 14 },
      modalLabelCol: { style: { width: '110px' } },
      modalWrapperCol: { span: 16 },
      tabLoading,
      saving,
      fetchingModels,
      searchEngineLoading,
      searchEngineInfo,
      searchEngineDetail,
      capabilityModalVisible,
      capabilityError,
      capabilityLoading: searchEngineLoading,
      usageInfo,
      usageRows,
      usedUsageRows,
      usedCapabilityCards,
      searchUsageRows,
      webReadUsageRows,
      usageColumns,
      testModalVisible,
      savedCompressionOrder,
      fixedTemperatureText,
      fixedTopPText,
      formState,
      MIN_AI_SEARCH_MODEL_CALLS,
      MIN_AI_SEARCHES,
      MIN_AI_SEARCH_RETRIES,
      MIN_AI_WEB_READS,
      parseIntegerInput,
      modelOptions,
      priceRows,
      priceColumns,
      priceLoading,
      priceSaving,
      priceResetting,
      priceModalVisible,
      priceForm,
      ocrCacheLoading,
      ocrCacheClearing,
      ocrCacheStatus,
      ocrCacheError,
      stagedPointColumns,
      stagedPointError,
      stagedPointRowKey,
      pricingGaps,
      isModelPriced,
      loadConfig,
      loadAIModelPrices,
      loadSearchEngineInfo,
      loadAIOCRCacheStatus,
      resetPriceForm,
    }
  },
  methods: {
    sumTableColumnWidths,
    formatAIModelContextWindowTokens,
    handleLoad() {
      this.loadConfig()
      this.loadAIModelPrices()
      this.loadAIOCRCacheStatus()
    },
    formatAIOCRCacheTime(value) {
      if (!value) return '暂无'
      const date = new Date(value)
      return Number.isNaN(date.getTime()) ? '暂无' : date.toLocaleString()
    },
    handleClearAIOCRCache() {
      this.ocrCacheClearing = true
      clearAIOCRCache().then(res => {
        if (res?.status !== 200) {
          throw new Error(res?.message || '清空图片 OCR 缓存失败')
        }
        message.success(`已清空 ${Number(res?.data?.deleted || 0)} 条图片 OCR 缓存`)
        return this.loadAIOCRCacheStatus()
      }).catch(error => {
        this.ocrCacheError = error?.message || '清空图片 OCR 缓存失败'
      }).finally(() => {
        this.ocrCacheClearing = false
      })
    },
    selectModel(model) {
      this.formState.model = model
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
        message.success(`已获取 ${filteredRemoteModels.length} 个模型，保存后生效`)
      }).catch(error => {
        message.error(error?.message || '获取 AI 模型列表失败')
      }).finally(() => {
        this.fetchingModels = false
      })
    },
    submitSave(baseUrl) {
      const stagedPointError = getStagedMultiplierPointsError(this.formState.stagedTokenMultiplierPoints)
      if (stagedPointError) {
        message.error(stagedPointError)
        return
      }
      const maxAISearchModelCalls = Number(this.formState.maxAISearchModelCalls)
      const maxAISearches = Number(this.formState.maxAISearches)
      const maxAISearchRetries = Number(this.formState.maxAISearchRetries)
      const searchPricePerCall = Number(this.formState.searchPricePerCall)
      const maxAIWebReads = Number(this.formState.maxAIWebReads)
      const webReadPricePerCall = Number(this.formState.webReadPricePerCall)
      const imageOCRPricePerCall = Number(this.formState.imageOCRPricePerCall)
      if (!Number.isSafeInteger(maxAISearchModelCalls) || maxAISearchModelCalls < MIN_AI_SEARCH_MODEL_CALLS) {
        message.error('模型最大调用次数必须是正整数，且不能超过安全整数范围')
        return
      }
      if (!Number.isSafeInteger(maxAISearches) || maxAISearches < MIN_AI_SEARCHES) {
        message.error('最大搜索次数必须是正整数，且不能超过安全整数范围')
        return
      }
      if (!Number.isSafeInteger(maxAISearchRetries) || maxAISearchRetries < MIN_AI_SEARCH_RETRIES) {
        message.error('每轮重试次数必须是正整数，且不能超过安全整数范围')
        return
      }
      if (!Number.isFinite(searchPricePerCall) || searchPricePerCall < 0) {
        message.error('联网搜索单价必须是大于或等于 0 的有效金额')
        return
      }
      if (!Number.isSafeInteger(maxAIWebReads) || maxAIWebReads < MIN_AI_WEB_READS) {
        message.error('网页读取次数必须是正整数，且不能超过安全整数范围')
        return
      }
      if (!Number.isFinite(webReadPricePerCall) || webReadPricePerCall < 0) {
        message.error('网页读取单价必须是大于或等于 0 的有效金额')
        return
      }
      if (!Number.isFinite(imageOCRPricePerCall) || imageOCRPricePerCall < 0) {
        message.error('图片 OCR 单价必须是大于或等于 0 的有效金额')
        return
      }
      let payload
      try {
        payload = { ...buildAISearchConfigSavePayload(this.formState, baseUrl), ...buildAIContextCompressionSettings(this.formState) }
      } catch (error) {
        message.error(error.message)
        return
      }
      this.saving = true
      updateAISearchConfig(payload).then(res => {
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
    addStagedMultiplierPoint() {
      try {
        this.formState.stagedTokenMultiplierPoints = insertStagedMultiplierPoint(this.formState.stagedTokenMultiplierPoints)
      } catch (error) {
        message.warning(error?.message || '无法新增阶段节点')
      }
    },
    removeStagedMultiplierPoint(index) {
      if (index === 0) {
        message.warning('首个 0 token 节点不能删除')
        return
      }
      if (this.formState.stagedTokenMultiplierPoints.length <= 1) {
        message.warning('阶段表至少需要保留首个 0 token 节点')
        return
      }
      this.formState.stagedTokenMultiplierPoints.splice(index, 1)
    },
    resetStagedMultiplierPoints() {
      this.formState.stagedTokenMultiplierPoints = DEFAULT_AI_STAGED_TOKEN_MULTIPLIER_POINTS.map(point => ({ ...point, tokens: String(point.tokens) }))
    },
    handleOpenTestDialog() {
      this.testModalVisible = true
    },
    formatPriceAmount(value) {
      const amount = Number(value)
      if (!Number.isFinite(amount)) {
        return '￥0'
      }
      // 价格表统一使用人民币符号，并按统一的 6 位精度去尾零展示。
      return formatMoneyCny(amount)
    },
    formatMultiplier(value) {
      const multiplier = Number(value)
      if (!Number.isFinite(multiplier) || multiplier <= 0) {
        return '1'
      }
      // 最多 4 位小数并去掉尾零，1 显示为 1 而不是 1.0000。
      return formatMoneyValue(multiplier)
    },
    openPriceModal(record) {
      if (record) {
        this.priceForm.id = record.id
        this.priceForm.modelName = record.modelName || ''
        this.priceForm.displayName = record.displayName || ''
        this.priceForm.contextWindowTokens = record.contextWindowTokens ?? 0
        this.priceForm.inputPrice = Number(record.inputPrice || 0)
        this.priceForm.cachedPrice = Number(record.cachedPrice || 0)
        this.priceForm.cacheWritePrice = Number(record.cacheWritePrice || 0)
        this.priceForm.outputPrice = Number(record.outputPrice || 0)
        this.priceForm.multiplier = Number(record.multiplier || 1)
        this.priceForm.remark = record.remark || ''
      } else {
        this.resetPriceForm()
      }
      this.priceModalVisible = true
    },
    // 从「默认模型未配置价格」提示直接跳到新增价格，并预填模型名。
    openPriceModalForConfiguredModel() {
      const modelName = String(this.pricingGaps?.configuredModel || this.formState.model || '').trim()
      this.resetPriceForm()
      this.priceForm.modelName = modelName
      this.priceForm.displayName = modelName
      this.priceModalVisible = true
    },
    closePriceModal() {
      this.priceModalVisible = false
      this.resetPriceForm()
    },
    handleSaveModelPrice() {
      let payload
      try {
        payload = buildAIModelPriceSavePayload(this.priceForm)
      } catch (error) {
        message.warning(error.message)
        return
      }
      this.priceSaving = true
      const action = payload.id ? updateAIModelPrice(payload) : createAIModelPrice(payload)
      action.then(res => {
        if (res?.status === 200) {
          message.success('保存成功')
          this.closePriceModal()
          this.loadAIModelPrices()
          return
        }
        message.error(res?.message || '保存失败')
      }).catch(() => {
        message.error('保存失败')
      }).finally(() => {
        this.priceSaving = false
      })
    },
    handleDeleteModelPrice(record) {
      deleteAIModelPrice({ id: record.id }).then(res => {
        if (res?.status === 200) {
          message.success('删除成功')
          this.loadAIModelPrices()
          return
        }
        message.error(res?.message || '删除失败')
      }).catch(() => {
        message.error('删除失败')
      })
    },
    handleResetModelPrices() {
      this.priceResetting = true
      resetAIModelPrices().then(res => {
        if (res?.status === 200) {
          message.success('已恢复内置默认价格表')
          this.loadAIModelPrices()
          return
        }
        message.error(res?.message || '恢复默认失败')
      }).catch(() => {
        message.error('恢复默认失败')
      }).finally(() => {
        this.priceResetting = false
      })
    },
  }
})
</script>

<style scoped>
.ai-config-form { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
.console-form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); column-gap: 32px; row-gap: 8px; }
.full-width { grid-column: 1 / -1; }
.switch-description { margin-left: 12px; color: var(--console-muted); }
.unit-label { margin-left: 8px; color: var(--console-muted); }
.hint { margin-top: 8px; color: var(--console-muted); font-size: 12px; line-height: 1.8; overflow-wrap: anywhere; }
.hint-warning, .multiplier-cell--custom { color: var(--console-warning); }
.multiplier-cell--custom { font-weight: 600; }
.model-editor { display: flex; gap: 12px; align-items: flex-start; }
.model-editor :deep(.ant-input-textarea) { min-width: 0; flex: 1; }
.fetch-btn { flex-shrink: 0; }
.model-tags { margin-top: 12px; }
.model-tags :deep(.ant-tag) { padding: 4px 8px; border-radius: 6px; }
.pricing-grid { display: grid; gap: 16px; min-width: 0; }
.ocr-cache-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 16px; margin-top: 16px; }
.ocr-cache-stat { display: flex; align-items: center; gap: 10px; min-width: 0; }
.ocr-cache-stat__label { color: var(--console-muted); white-space: nowrap; }
.ocr-cache-stat > :last-child { overflow-wrap: anywhere; }
.staged-points-editor { margin: 20px 0 24px; }
.staged-points-editor__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; flex-wrap: wrap; margin-bottom: 16px; }
.staged-points-editor__header > div { flex: 1 1 320px; min-width: 0; }
.staged-points-editor__header :deep(.ant-space) { flex-wrap: wrap; }
.sub-card-title { color: var(--console-text); font-size: 15px; font-weight: 600; }
.sub-card-subtitle { margin-top: 6px; color: var(--console-muted); font-size: 13px; line-height: 1.7; overflow-wrap: anywhere; }
@media (max-width: 768px) { .console-form-grid { grid-template-columns: minmax(0, 1fr); } .ocr-cache-grid { grid-template-columns: minmax(0, 1fr); } .model-editor { display: grid; } .fetch-btn { width: 100%; } .staged-billing-settings :deep(.ant-form-item) { display: block; } .staged-billing-settings :deep(.ant-form-item-label) { width: 100% !important; text-align: left; } .staged-billing-settings :deep(.ant-form-item-control) { max-width: 100%; } }
</style>
