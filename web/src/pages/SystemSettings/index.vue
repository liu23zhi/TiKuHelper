<template>
  <div class="console-page system-settings-page">
    <header class="section-heading settings-heading">
      <div>
        <h1>系统设置</h1>
        <p class="page-lead">集中管理认证、通知、支付和站点运行参数。</p>
      </div>
      <a-space wrap class="section-actions">
        <a-button @click="handleLoad" :loading="tabLoading">刷新</a-button>
        <a-button type="primary" @click="handleSave" :loading="saving">保存</a-button>
      </a-space>
    </header>

      <a-alert
        type="info"
        show-icon
        class="page-alert"
        message="这里维护公开认证、微信登录、验证码、短信宝、支付回调和支付方式。修改后会立即影响后续注册、登录与充值链路。"
      />

      <a-form :label-col="labelCol" :wrapper-col="wrapperCol" class="settings-form">
        <section class="console-panel settings-section">
          <header class="section-heading"><h2>站点与日志</h2></header>
        <a-form-item label="网站名称">
          <a-input v-model:value="formState.siteName" placeholder="例如 题库" />
          <div class="hint">用于浏览器标题、页头品牌和登录页名称，留空时恢复为“题库”。</div>
        </a-form-item>

        <a-form-item label="最大日志大小">
          <a-input-number
            v-model:value="formState.maxLogSizeValue"
            :min="minimumLogSizeValue"
            :precision="0"
            :step="1"
            :parser="parseIntegerInput"
            style="width: 180px"
          />
          <a-select v-model:value="formState.maxLogSizeUnit" style="width: 110px; margin-left: 8px">
            <a-select-option v-for="unit in LOG_SIZE_UNITS" :key="unit.value" :value="unit.value">
              {{ unit.label }}
            </a-select-option>
          </a-select>
          <div class="hint">日志目录全部日志文件的总占用上限，默认 5GB。支持 KB、MB、GB、TB，保存时会转换为字节。超过上限时会优先删除最旧的归档日志。</div>
        </a-form-item>
        </section>

        <section class="console-panel settings-section">
          <header class="section-heading"><h2>认证与充值</h2></header>
        <a-form-item label="允许公开注册">
          <a-switch v-model:checked="formState.registerEnabled" />
          <div class="hint">关闭后，注册页会直接阻止进入；管理员在后台创建用户不受影响。</div>
        </a-form-item>

        <a-form-item label="新用户赠送金额">
          <a-input-number
            v-model:value="formState.newUserBonusAmount"
            :min="0.01"
            :precision="6"
            style="width: 220px"
          />
          <div class="hint">新用户注册或后台创建成功后自动计入余额；后台创建时可单独调整或关闭。金额最多支持 6 位小数。</div>
        </a-form-item>

        <a-form-item label="充值最低金额">
          <a-input-number
            v-model:value="formState.minRechargeAmount"
            :min="0.01"
            :precision="2"
            :step="0.01"
            style="width: 220px"
          />
          <div class="hint">限制用户每次充值的最低金额，默认 1 元，最多支持两位小数。</div>
        </a-form-item>

        <a-form-item label="开启短信登录">
          <a-switch v-model:checked="formState.smsLoginEnabled" />
        </a-form-item>

        <a-form-item label="开启微信登录">
          <a-switch v-model:checked="formState.wechatLoginEnabled" />
          <div class="hint">总开关会随微信扫描或微信验证码方式自动生效；两个方式都关闭时会自动关闭。</div>
        </a-form-item>
        </section>

        <section class="console-panel settings-section">
          <header class="section-heading"><h2>微信登录与验证码</h2></header>
        <a-form-item label="微信扫描">
          <a-switch v-model:checked="formState.wechatScanLoginEnabled" />
        </a-form-item>

        <a-form-item label="微信验证码">
          <a-switch v-model:checked="formState.wechatCodeLoginEnabled" />
        </a-form-item>

        <a-form-item label="默认微信方式">
          <a-select v-model:value="formState.wechatDefaultLoginMode">
            <a-select-option value="scan" :disabled="!formState.wechatScanLoginEnabled">微信扫描</a-select-option>
            <a-select-option value="code" :disabled="!formState.wechatCodeLoginEnabled">微信验证码</a-select-option>
          </a-select>
        </a-form-item>

        <a-form-item label="微信 API 地址">
          <a-input v-model:value="formState.wechatApiBaseUrl" placeholder="例如 https://wechat.example.com" />
        </a-form-item>

        <a-form-item label="Bearer Token">
          <a-input-password v-model:value="formState.wechatApiBearerToken" placeholder="请输入微信登录服务 Bearer Token" />
        </a-form-item>

        <a-form-item label="验证码二维码">
          <a-input v-model:value="formState.wechatCodeStaticQrcodeUrl" placeholder="请输入微信验证码模式下展示的静态二维码图片地址" />
          <!-- <div class="hint">微信验证码模式会在登录页展示该二维码，用户扫码关注后发送“验证码”获取登录码。</div> -->
        </a-form-item>

        <a-form-item label="验证码提供方">
          <a-select v-model:value="formState.captchaProvider">
            <a-select-option value="remote">远程 TAC 验证码</a-select-option>
            <a-select-option value="local">站内图片验证码</a-select-option>
          </a-select>
          <div class="hint">远程验证码不可用时，登录和注册页面会自动回退到站内图片验证码。</div>
        </a-form-item>

        <a-form-item label="远程服务地址" v-if="useRemoteCaptcha">
          <a-input v-model:value="formState.captchaApiBaseUrl" placeholder="https://captcha.api.zelly.cn" />
        </a-form-item>

        <a-form-item label="远程服务 Key" v-if="useRemoteCaptcha">
          <a-input-password v-model:value="formState.captchaApiKey" placeholder="请输入 X-Captcha-Key" />
        </a-form-item>

        <a-form-item label="验证码类型" v-if="useRemoteCaptcha">
          <a-select v-model:value="formState.captchaType">
            <a-select-option value="RANDOM">随机</a-select-option>
            <a-select-option value="SLIDER">滑块</a-select-option>
            <a-select-option value="ROTATE">旋转</a-select-option>
            <a-select-option value="CONCAT">滑动还原</a-select-option>
            <a-select-option value="WORD_IMAGE_CLICK">点选</a-select-option>
          </a-select>
        </a-form-item>
        </section>

        <section class="console-panel settings-section">
          <header class="section-heading"><h2>归属地查询</h2></header>
        <a-form-item label="UApiPro API 密钥">
          <a-input-password v-model:value="formState.uapiApiKey" placeholder="请输入 UApiPro API 密钥（以 uapi- 开头）" />
          <div class="hint">
            用于「用户管理」「日志列表」展示手机号 / IP 归属地。留空表示不查询归属地（对应列只显示原始值）。
          </div>
        </a-form-item>
        </section>

        <section class="console-panel settings-section">
          <header class="section-heading"><h2>支付回调</h2></header>
        <a-form-item label="接口版本">
          <a-input value="v2" disabled />
          <div class="hint">当前仅兼容易支付 v2；支付方式请在本页下方维护。</div>
        </a-form-item>

        <a-form-item label="网关地址">
          <a-input v-model:value="formState.epayGatewayUrl" placeholder="例如 https://pay.example.com/submit.php" />
        </a-form-item>

        <a-form-item label="商户编号">
          <a-input v-model:value="formState.epayPartnerId" placeholder="请输入易支付商户编号" />
        </a-form-item>

        <a-form-item label="平台公钥">
          <a-input-password v-model:value="formState.epayPlatformPublicKey" placeholder="请输入易支付平台公钥" />
        </a-form-item>

        <a-form-item label="商户私钥">
          <a-input-password v-model:value="formState.epayMerchantPrivateKey" placeholder="请输入易支付商户私钥" />
        </a-form-item>

        <a-form-item label="网站 Base 地址">
          <a-space direction="vertical" style="width: 100%" :size="8">
            <a-space style="width: 100%" wrap>
              <a-input v-model:value="formState.siteBaseUrl" placeholder="例如 https://app.example.com" class="base-url-input" />
              <a-button @click="fillBaseUrlFromBrowser">从浏览器地址栏获取</a-button>
            </a-space>
            <div class="hint">只需要维护当前站点的 base 地址，系统会按易支付 SDK 所需参数自动补全异步通知与同步跳转地址。</div>
          </a-space>
        </a-form-item>

        <a-form-item label="异步通知地址">
          <a-input :value="derivedNotifyUrl" readonly placeholder="将根据网站 Base 地址自动补全" />
          <div class="hint">固定补全为 /adapter-service/payment/notify，并作为 notify_url 参与易支付下单。</div>
        </a-form-item>

        <a-form-item label="同步跳转地址">
          <a-input :value="derivedReturnUrl" readonly placeholder="将根据网站 Base 地址自动补全" />
          <div class="hint">固定补全为 /adapter/recharge/result，并作为 return_url 参与易支付下单。</div>
        </a-form-item>
        </section>

        <section class="console-panel settings-section">
          <header class="section-heading"><h2>短信验证码配置</h2></header>
        <a-form-item label="短信提供方">
          <a-input v-model:value="formState.smsProvider" disabled />
          <!-- <div class="hint">首期固定使用短信宝，后续如有需要再扩展多供应商。</div> -->
        </a-form-item>

        <a-form-item label="短信宝用户名">
          <a-input v-model:value="formState.smsbaoUsername" placeholder="请输入短信宝用户名" />
        </a-form-item>

        <a-form-item label="短信宝密钥">
          <a-input-password v-model:value="formState.smsbaoSecret" placeholder="请输入密码或 ApiKey" />
        </a-form-item>

        <a-form-item label="凭证类型">
          <a-select v-model:value="formState.smsbaoCredentialType">
            <a-select-option value="apikey">ApiKey，原样发送</a-select-option>
            <a-select-option value="password">登录密码，发送前 MD5</a-select-option>
          </a-select>
        </a-form-item>

        <a-form-item label="发送区域">
          <a-select v-model:value="formState.smsbaoRegionMode">
            <a-select-option value="auto">自动识别</a-select-option>
            <a-select-option value="domestic">仅国内</a-select-option>
            <a-select-option value="international">仅国际</a-select-option>
          </a-select>
        </a-form-item>

        <a-form-item label="短信签名">
          <a-input v-model:value="formState.smsCodeSignName" placeholder="例如 题库助手" />
        </a-form-item>

        <a-form-item label="验证码模板">
          <a-textarea v-model:value="formState.smsCodeTemplate" :rows="3" placeholder="例如 【{sign}】您的验证码是{code}，5分钟内有效。" />
          <div class="hint">请保留 {sign} 和 {code} 占位符，系统会在发送前替换。</div>
        </a-form-item>
        </section>

      </a-form>

    <a-card :bordered="false" class="page-card">
      <template #title>
        <div class="section-title-wrap">
          <div>
            <div class="section-title">支付方式管理</div>
            <div class="section-subtitle">统一维护充值时可选的支付方式名称、图标、易支付通道、启停状态与排序。</div>
          </div>
        </div>
      </template>
      <template #extra>
        <a-space>
          <a-button @click="handleLoadPaymentMethods" :loading="paymentMethodLoading">刷新</a-button>
          <a-button type="primary" @click="openCreatePaymentMethodModal">新增支付方式</a-button>
        </a-space>
      </template>

      <a-alert
        type="info"
        show-icon
        class="page-alert"
        message="订单只保存支付方式名称快照；如需调整实际通道值、图标或排序，请直接在这里编辑。"
      />

      <a-table
        bordered
        :data-source="paymentMethods"
        :columns="paymentMethodColumns"
        :loading="paymentMethodLoading"
        :pagination="false"
        row-key="id"
        :scroll="{ x: 1180 }"
      >
        <template #iconSvg="{ record }">
          <div class="icon-cell">
            <div v-if="record.iconSvg" class="icon-preview" v-html="record.iconSvg"></div>
            <span v-else>-</span>
          </div>
        </template>
        <template #ePayType="{ record }">
          <a-tag color="blue">{{ record.epayType || '-' }}</a-tag>
        </template>
        <template #enabled="{ record }">
          <a-badge :status="record.enabled ? 'success' : 'default'" :text="record.enabled ? '启用' : '停用'" />
        </template>
        <template #remark="{ record }">
          <span>{{ record.remark || '-' }}</span>
        </template>
        <template #operation="{ record }">
          <a-space size="small">
            <a-button type="link" style="padding: 0" @click="openEditPaymentMethodModal(record)">编辑</a-button>
            <a-popconfirm
              ok-text="确定"
              cancel-text="取消"
              title="确定删除该支付方式吗？"
              @confirm="handleDeletePaymentMethod(record.id)"
            >
              <a-button type="link" danger style="padding: 0">删除</a-button>
            </a-popconfirm>
          </a-space>
        </template>
        <template #emptyText>
          <a-empty description="当前还没有支付方式，请先新增可用渠道。" />
        </template>
      </a-table>
    </a-card>

    <a-modal
      v-model:open="paymentMethodVisible"
      :title="paymentMethodModalTitle"
      @ok="handleSubmitPaymentMethod"
      :afterClose="resetPaymentMethodFormState"
      ok-text="确定"
      cancel-text="取消"
      :confirm-loading="paymentMethodSubmitting"
      width="760px"
    >
      <a-form :model="paymentMethodFormState" :label-col="paymentMethodLabelCol" :wrapper-col="paymentMethodWrapperCol" layout="horizontal">
        <a-form-item label="支付方式名称" required>
          <a-input v-model:value="paymentMethodFormState.name" placeholder="例如 支付宝" />
        </a-form-item>
        <a-form-item label="SVG 图标">
          <a-textarea v-model:value="paymentMethodFormState.iconSVG" :rows="4" placeholder="可选，直接粘贴 SVG 源码" />
          <div class="hint">支持直接粘贴内联 SVG，用于充值选择弹窗与管理页预览。</div>
        </a-form-item>
        <a-form-item label="易支付通道值" required>
          <a-input v-model:value="paymentMethodFormState.ePayType" placeholder="例如 alipay / wxpay" />
        </a-form-item>
        <a-form-item label="启用状态">
          <a-switch v-model:checked="paymentMethodFormState.enabled" />
        </a-form-item>
        <a-form-item label="排序">
          <a-input-number v-model:value="paymentMethodFormState.sort" :min="0" :step="1" style="width: 100%" />
        </a-form-item>
        <a-form-item label="备注">
          <a-textarea v-model:value="paymentMethodFormState.remark" :rows="3" placeholder="可选，例如推荐场景或渠道说明" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script>
import { computed, defineComponent, onMounted, reactive, ref, watch } from 'vue';
import { message } from 'ant-design-vue';
import {
  createPaymentMethod,
  deletePaymentMethod,
  getPaymentMethods,
  getSystemSettings,
  updatePaymentMethod,
  updateSystemSettings,
} from '@/api/TikuComponentApi';
import {
  normalizePaymentMethodForm,
  sortPaymentMethodsForDisplay,
} from '@/utils/paymentMethodHelpers.mjs';

const notifyPath = '/adapter-service/payment/notify'
const returnPath = '/adapter/recharge/result'
const DEFAULT_MAX_LOG_SIZE_BYTES = 5 * 1024 * 1024 * 1024
const DEFAULT_MIN_RECHARGE_AMOUNT = 1
const MIN_LOG_SIZE_BYTES = 1 * 1024 * 1024
const LOG_SIZE_UNITS = [
  { value: 'B', label: '字节', bytes: 1 },
  { value: 'KB', label: 'KB', bytes: 1024 },
  { value: 'MB', label: 'MB', bytes: 1024 * 1024 },
  { value: 'GB', label: 'GB', bytes: 1024 * 1024 * 1024 },
  { value: 'TB', label: 'TB', bytes: 1024 * 1024 * 1024 * 1024 },
]
const LOG_SIZE_UNIT_MAP = Object.fromEntries(LOG_SIZE_UNITS.map(unit => [unit.value, unit]))

function normalizeString(value) {
  return String(value || '').trim()
}

function normalizeBonusAmount(value) {
  const amount = Number(value || 0)
  // 与后端保持一致：金额最多 6 位小数。
  return Number.isFinite(amount) && amount >= 0 ? Math.round(amount * 1e6) / 1e6 : 0
}

function normalizeMinRechargeAmount(value) {
  const raw = String(value ?? '').trim()
  if (!raw) {
    return DEFAULT_MIN_RECHARGE_AMOUNT
  }
  const amount = Number(raw)
  return Number.isFinite(amount) && amount > 0 && amount <= Number.MAX_SAFE_INTEGER
    ? Math.round(amount * 100) / 100
    : DEFAULT_MIN_RECHARGE_AMOUNT
}

function parseIntegerInput(value) {
  if (value === null || value === undefined || value === '') {
    return ''
  }
  return String(value).replace(/[^0-9]/g, '')
}

function normalizeMaxLogSizeBytes(value) {
  const parsed = Number(value)
  return Number.isSafeInteger(parsed) && parsed >= MIN_LOG_SIZE_BYTES
    ? parsed
    : DEFAULT_MAX_LOG_SIZE_BYTES
}

function splitMaxLogSizeBytes(value) {
  const bytes = normalizeMaxLogSizeBytes(value)
  for (let index = LOG_SIZE_UNITS.length - 1; index >= 0; index -= 1) {
    const unit = LOG_SIZE_UNITS[index]
    if (bytes >= unit.bytes && bytes % unit.bytes === 0) {
      return { value: bytes / unit.bytes, unit: unit.value }
    }
  }
  return { value: bytes, unit: 'B' }
}

function composeMaxLogSizeBytes(value, unitValue) {
  const amount = Number(value)
  const unit = LOG_SIZE_UNIT_MAP[unitValue] || LOG_SIZE_UNIT_MAP.GB
  if (!Number.isSafeInteger(amount) || amount < 0 || !Number.isSafeInteger(amount * unit.bytes)) {
    return 0
  }
  return amount * unit.bytes
}

function trimTrailingSlash(value) {
  return normalizeString(value).replace(/\/+$/, '')
}

function buildAbsoluteURL(baseUrl, path) {
  const normalizedBase = trimTrailingSlash(baseUrl)
  if (!normalizedBase) {
    return ''
  }
  return `${normalizedBase}${path}`
}

function inferSiteBaseURL(detail = {}) {
  const candidates = [detail.epayNotifyUrl, detail.epayReturnUrl]
  for (const candidate of candidates) {
    const normalized = normalizeString(candidate)
    if (!normalized) {
      continue
    }
    if (normalized.endsWith(notifyPath)) {
      return normalized.slice(0, -notifyPath.length)
    }
    if (normalized.endsWith(returnPath)) {
      return normalized.slice(0, -returnPath.length)
    }
    try {
      const parsed = new URL(normalized)
      return parsed.origin
    } catch (_) {
      continue
    }
  }
  return ''
}

export default defineComponent({
  name: 'SystemSettings',
  setup() {
    const tabLoading = ref(true)
    const saving = ref(false)
    const paymentMethodLoading = ref(false)
    const paymentMethodVisible = ref(false)
    const paymentMethodSubmitting = ref(false)
    const paymentMethodEditing = ref(false)
    const paymentMethods = ref([])
    const formState = reactive({
      siteName: '题库',
      maxLogSizeValue: DEFAULT_MAX_LOG_SIZE_BYTES / LOG_SIZE_UNIT_MAP.GB.bytes,
      maxLogSizeUnit: 'GB',
      registerEnabled: false,
      newUserBonusAmount: 0,
      minRechargeAmount: DEFAULT_MIN_RECHARGE_AMOUNT,
      epayVersion: 'v2',
      epayGatewayUrl: '',
      epayPartnerId: '',
      epayPlatformPublicKey: '',
      epayMerchantPrivateKey: '',
      siteBaseUrl: '',
      epayNotifyUrl: '',
      epayReturnUrl: '',
      smsProvider: 'smsbao',
      smsbaoUsername: '',
      smsbaoSecret: '',
      smsCodeSignName: '',
      smsCodeTemplate: '',
      smsLoginEnabled: false,
      wechatLoginEnabled: false,
      wechatScanLoginEnabled: false,
      wechatCodeLoginEnabled: false,
      wechatDefaultLoginMode: 'scan',
      wechatCodeStaticQrcodeUrl: '',
      wechatApiBaseUrl: '',
      wechatApiBearerToken: '',
      captchaProvider: 'remote',
      captchaApiBaseUrl: 'https://captcha.api.zelly.cn',
      captchaApiKey: '',
      captchaType: 'RANDOM',
      smsbaoCredentialType: 'apikey',
      smsbaoRegionMode: 'auto',
      uapiApiKey: '',
    })
    const paymentMethodFormState = ref({
      id: undefined,
      name: '',
      iconSVG: '',
      ePayType: '',
      enabled: true,
      sort: 0,
      remark: '',
    })

    const derivedNotifyUrl = computed(() => buildAbsoluteURL(formState.siteBaseUrl, notifyPath))
    const derivedReturnUrl = computed(() => buildAbsoluteURL(formState.siteBaseUrl, returnPath))
    const paymentMethodModalTitle = computed(() => (paymentMethodEditing.value ? '编辑支付方式' : '新增支付方式'))
    const minimumLogSizeValue = computed(() => {
      const unit = LOG_SIZE_UNIT_MAP[formState.maxLogSizeUnit] || LOG_SIZE_UNIT_MAP.GB
      return Math.ceil(MIN_LOG_SIZE_BYTES / unit.bytes)
    })

    const applyDetail = (data = {}) => {
      formState.siteName = data.siteName || '题库'
      const maxLogSize = splitMaxLogSizeBytes(data.maxLogSizeBytes)
      formState.maxLogSizeValue = maxLogSize.value
      formState.maxLogSizeUnit = maxLogSize.unit
      formState.registerEnabled = !!data.registerEnabled
      formState.newUserBonusAmount = Number(data.newUserBonusAmount || 0)
      formState.minRechargeAmount = normalizeMinRechargeAmount(data.minRechargeAmount)
      formState.epayVersion = 'v2'
      formState.epayGatewayUrl = data.epayGatewayUrl || ''
      formState.epayPartnerId = data.epayPartnerId || ''
      formState.epayPlatformPublicKey = data.epayPlatformPublicKey || ''
      formState.epayMerchantPrivateKey = data.epayMerchantPrivateKey || ''
      formState.siteBaseUrl = inferSiteBaseURL(data)
      formState.epayNotifyUrl = data.epayNotifyUrl || ''
      formState.epayReturnUrl = data.epayReturnUrl || ''
      formState.smsProvider = data.smsProvider || 'smsbao'
      formState.smsbaoUsername = data.smsbaoUsername || ''
      formState.smsbaoSecret = data.smsbaoSecret || ''
      formState.smsCodeSignName = data.smsCodeSignName || ''
      formState.smsCodeTemplate = data.smsCodeTemplate || ''
      formState.smsLoginEnabled = !!data.smsLoginEnabled
      formState.wechatLoginEnabled = !!data.wechatLoginEnabled
      formState.wechatScanLoginEnabled = !!data.wechatScanLoginEnabled
      formState.wechatCodeLoginEnabled = !!data.wechatCodeLoginEnabled
      formState.wechatDefaultLoginMode = data.wechatDefaultLoginMode || (formState.wechatScanLoginEnabled ? 'scan' : 'code')
      formState.wechatCodeStaticQrcodeUrl = data.wechatCodeStaticQrcodeUrl || ''
      formState.wechatApiBaseUrl = data.wechatApiBaseUrl || ''
      formState.wechatApiBearerToken = data.wechatApiBearerToken || ''
      formState.captchaProvider = data.captchaProvider || 'remote'
      formState.captchaApiBaseUrl = data.captchaApiBaseUrl || 'https://captcha.api.zelly.cn'
      formState.captchaApiKey = data.captchaApiKey || ''
      formState.captchaType = data.captchaType || 'RANDOM'
      formState.smsbaoCredentialType = data.smsbaoCredentialType || 'apikey'
      formState.smsbaoRegionMode = data.smsbaoRegionMode || 'auto'
      // 明文回填（系统设置页仅管理员可见），密码框负责隐藏显示。
      formState.uapiApiKey = data.uapiApiKey || ''
    }

    const loadSettings = () => {
      tabLoading.value = true
      getSystemSettings().then(res => {
        if (res?.status === 200) {
          applyDetail(res?.data || {})
        } else {
          message.error(res?.message || '获取系统设置失败')
        }
      }).catch(() => {
        message.error('获取系统设置失败')
      }).finally(() => {
        tabLoading.value = false
      })
    }

    const loadPaymentMethods = () => {
      paymentMethodLoading.value = true
      getPaymentMethods().then((res) => {
        if (res?.status === 200) {
          const rows = Array.isArray(res?.data) ? res.data : []
          paymentMethods.value = sortPaymentMethodsForDisplay(rows)
        } else {
          message.error(res?.message || '获取支付方式失败')
        }
      }).catch(() => {
        message.error('获取支付方式失败')
      }).finally(() => {
        paymentMethodLoading.value = false
      })
    }

    const fillBaseUrlFromBrowser = () => {
      if (typeof window === 'undefined' || !window.location) {
        message.error('当前无法获取浏览器地址栏')
        return
      }
      formState.siteBaseUrl = window.location.origin || ''
      message.success('已从浏览器地址栏获取站点地址')
    }

    watch(
      () => [formState.wechatScanLoginEnabled, formState.wechatCodeLoginEnabled],
      ([scanEnabled, codeEnabled]) => {
        if (!scanEnabled && !codeEnabled) {
          formState.wechatLoginEnabled = false
          return
        }
        if (!formState.wechatLoginEnabled) {
          formState.wechatLoginEnabled = true
        }
        if (formState.wechatDefaultLoginMode === 'code' && !codeEnabled) {
          formState.wechatDefaultLoginMode = scanEnabled ? 'scan' : 'code'
        } else if (formState.wechatDefaultLoginMode !== 'code' && !scanEnabled && codeEnabled) {
          formState.wechatDefaultLoginMode = 'code'
        }
      }
    )

    onMounted(() => {
      loadSettings()
      loadPaymentMethods()
    })

    return {
      labelCol: { style: { width: '140px' } },
      wrapperCol: { span: 14 },
      paymentMethodLabelCol: { style: { width: '110px' } },
      paymentMethodWrapperCol: { span: 17 },
      // 选「站内图片验证码」时，远程服务地址 / Key / 验证码类型都无意义，直接隐藏。
      useRemoteCaptcha: computed(() => formState.captchaProvider !== 'local'),
      tabLoading,
      saving,
      paymentMethodLoading,
      paymentMethodVisible,
      paymentMethodSubmitting,
      paymentMethodEditing,
      paymentMethods,
      formState,
      MIN_LOG_SIZE_BYTES,
      LOG_SIZE_UNITS,
      minimumLogSizeValue,
      parseIntegerInput,
      paymentMethodFormState,
      derivedNotifyUrl,
      derivedReturnUrl,
      paymentMethodModalTitle,
      loadSettings,
      loadPaymentMethods,
      fillBaseUrlFromBrowser,
      paymentMethodColumns: [
        {
          title: '名称',
          dataIndex: 'name',
          key: 'name',
          width: 160,
        },
        {
          title: '图标',
          dataIndex: 'iconSvg',
          key: 'iconSvg',
          slots: {
            customRender: 'iconSvg',
          },
          width: 120,
        },
        {
          title: '易支付通道',
          dataIndex: 'epayType',
          key: 'epayType',
          slots: {
            customRender: 'ePayType',
          },
          width: 150,
        },
        {
          title: '状态',
          dataIndex: 'enabled',
          key: 'enabled',
          slots: {
            customRender: 'enabled',
          },
          width: 120,
        },
        {
          title: '排序',
          dataIndex: 'sort',
          key: 'sort',
          width: 90,
        },
        {
          title: '备注',
          dataIndex: 'remark',
          key: 'remark',
          slots: {
            customRender: 'remark',
          },
          width: 240,
        },
        {
          title: '操作',
          dataIndex: 'operation',
          key: 'operation',
          fixed: 'right',
          slots: {
            customRender: 'operation',
          },
          width: 120,
        },
      ],
    }
  },
  methods: {
    handleLoad() {
      this.loadSettings()
      this.loadPaymentMethods()
    },
    handleSave() {
      const maxLogSizeBytes = composeMaxLogSizeBytes(this.formState.maxLogSizeValue, this.formState.maxLogSizeUnit)
      if (!Number.isSafeInteger(maxLogSizeBytes) || maxLogSizeBytes < MIN_LOG_SIZE_BYTES) {
        message.error('最大日志大小必须是不小于 1MB 的整数')
        return
      }
      const siteBaseUrl = trimTrailingSlash(this.formState.siteBaseUrl)
      const notifyUrl = buildAbsoluteURL(siteBaseUrl, notifyPath)
      const returnUrl = buildAbsoluteURL(siteBaseUrl, returnPath)
      const wechatScanLoginEnabled = !!this.formState.wechatScanLoginEnabled
      const wechatCodeLoginEnabled = !!this.formState.wechatCodeLoginEnabled
      const wechatLoginEnabled = !!this.formState.wechatLoginEnabled && (wechatScanLoginEnabled || wechatCodeLoginEnabled)
      let wechatDefaultLoginMode = normalizeString(this.formState.wechatDefaultLoginMode || 'scan')
      if (wechatDefaultLoginMode === 'code' && !wechatCodeLoginEnabled) {
        wechatDefaultLoginMode = wechatScanLoginEnabled ? 'scan' : 'code'
      }
      if (wechatDefaultLoginMode !== 'code' && !wechatScanLoginEnabled && wechatCodeLoginEnabled) {
        wechatDefaultLoginMode = 'code'
      }
      const minRechargeAmount = normalizeMinRechargeAmount(this.formState.minRechargeAmount)
      if (!Number.isFinite(minRechargeAmount) || minRechargeAmount <= 0) {
        message.error('充值最低金额必须是大于 0 的有效金额')
        return
      }
      const payload = {
        siteName: normalizeString(this.formState.siteName || '题库'),
        maxLogSizeBytes,
        registerEnabled: !!this.formState.registerEnabled,
        newUserBonusAmount: normalizeBonusAmount(this.formState.newUserBonusAmount),
        minRechargeAmount,
        epayVersion: 'v2',
        epayGatewayUrl: normalizeString(this.formState.epayGatewayUrl),
        epayPartnerId: normalizeString(this.formState.epayPartnerId),
        epayPlatformPublicKey: normalizeString(this.formState.epayPlatformPublicKey),
        epayMerchantPrivateKey: normalizeString(this.formState.epayMerchantPrivateKey),
        epayNotifyUrl: notifyUrl,
        epayReturnUrl: returnUrl,
        smsProvider: normalizeString(this.formState.smsProvider || 'smsbao'),
        smsbaoUsername: normalizeString(this.formState.smsbaoUsername),
        smsbaoSecret: normalizeString(this.formState.smsbaoSecret),
        smsCodeSignName: normalizeString(this.formState.smsCodeSignName),
        smsCodeTemplate: normalizeString(this.formState.smsCodeTemplate),
        smsLoginEnabled: !!this.formState.smsLoginEnabled,
        wechatLoginEnabled,
        wechatScanLoginEnabled,
        wechatCodeLoginEnabled,
        wechatDefaultLoginMode,
        wechatCodeStaticQrcodeUrl: normalizeString(this.formState.wechatCodeStaticQrcodeUrl),
        wechatApiBaseUrl: normalizeString(this.formState.wechatApiBaseUrl),
        wechatApiBearerToken: normalizeString(this.formState.wechatApiBearerToken),
        captchaProvider: normalizeString(this.formState.captchaProvider || 'remote'),
        captchaApiBaseUrl: normalizeString(this.formState.captchaApiBaseUrl || 'https://captcha.api.zelly.cn'),
        captchaApiKey: normalizeString(this.formState.captchaApiKey),
        captchaType: normalizeString(this.formState.captchaType || 'RANDOM'),
        smsbaoCredentialType: normalizeString(this.formState.smsbaoCredentialType || 'apikey'),
        smsbaoRegionMode: normalizeString(this.formState.smsbaoRegionMode || 'auto'),
        // 明文提交；留空即为清空密钥（不做脱敏占位符的特殊处理）。
        uapiApiKey: normalizeString(this.formState.uapiApiKey),
      }

      this.saving = true
      updateSystemSettings(payload).then(res => {
        if (res?.status === 200) {
          message.success('保存成功')
          this.loadSettings()
        } else {
          message.error(res?.message || '保存失败')
        }
      }).catch(() => {
        message.error('保存失败')
      }).finally(() => {
        this.saving = false
      })
    },
    handleLoadPaymentMethods() {
      this.loadPaymentMethods()
    },
    openCreatePaymentMethodModal() {
      this.resetPaymentMethodFormState()
      this.paymentMethodEditing = false
      this.paymentMethodVisible = true
    },
    openEditPaymentMethodModal(record) {
      this.paymentMethodEditing = true
      this.paymentMethodFormState = {
        id: record.id,
        name: record.name || '',
        iconSVG: record.iconSvg || '',
        ePayType: record.epayType || '',
        enabled: !!record.enabled,
        sort: Number(record.sort || 0),
        remark: record.remark || '',
      }
      this.paymentMethodVisible = true
    },
    handleSubmitPaymentMethod() {
      const payload = normalizePaymentMethodForm(this.paymentMethodFormState)
      if (!payload.name) {
        message.warning('请输入支付方式名称')
        return
      }
      if (!payload.epayType) {
        message.warning('请输入易支付通道值')
        return
      }

      this.paymentMethodSubmitting = true
      const submitter = this.paymentMethodEditing
        ? updatePaymentMethod(payload)
        : createPaymentMethod(payload)

      submitter.then((res) => {
        if (res?.status === 200) {
          message.success(this.paymentMethodEditing ? '更新支付方式成功' : '创建支付方式成功')
          this.paymentMethodVisible = false
          this.loadPaymentMethods()
        } else {
          message.error(res?.message || (this.paymentMethodEditing ? '更新支付方式失败' : '创建支付方式失败'))
        }
      }).catch(() => {
        message.error(this.paymentMethodEditing ? '更新支付方式失败' : '创建支付方式失败')
      }).finally(() => {
        this.paymentMethodSubmitting = false
      })
    },
    handleDeletePaymentMethod(id) {
      deletePaymentMethod({ id }).then((res) => {
        if (res?.status === 200) {
          message.success('删除支付方式成功')
          this.loadPaymentMethods()
        } else {
          message.error(res?.message || '删除支付方式失败')
        }
      }).catch(() => {
        message.error('删除支付方式失败')
      })
    },
    resetPaymentMethodFormState() {
      this.paymentMethodFormState = {
        id: undefined,
        name: '',
        iconSVG: '',
        ePayType: '',
        enabled: true,
        sort: 0,
        remark: '',
      }
      this.paymentMethodEditing = false
    },
  }
})
</script>

<style scoped>
.system-settings-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}

.settings-heading {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 16px;
}

.settings-heading h1 {
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

.settings-form {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}

.settings-section {
  min-width: 0;
  padding: 12px 0 16px;
  border-bottom: 1px solid var(--console-border, #e5e7eb);
}

.settings-section .section-heading {
  margin-bottom: 16px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--console-border, #e5e7eb);
}

.settings-section .section-heading h2 {
  margin: 0;
  color: var(--console-text, #1f2937);
  font-size: 15px;
  font-weight: 600;
}

.settings-section :deep(.ant-form-item:last-child) {
  margin-bottom: 0;
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

.base-url-input {
  min-width: min(320px, 100%);
}

.section-title-wrap {
  display: flex;
  align-items: center;
}

.section-title {
  font-size: 18px;
  font-weight: 650;
  color: var(--console-text, #1f2937);
}

.section-subtitle {
  margin-top: 6px;
  color: var(--console-muted, #64748b);
  font-size: 13px;
}

.system-settings-page > .page-card {
  min-width: 0;
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: var(--console-radius, 12px);
}

.icon-cell {
  min-height: 36px;
  display: flex;
  align-items: center;
}

.icon-preview :deep(svg) {
  width: 28px;
  height: 28px;
  display: block;
}

@media (max-width: 640px) {
  .settings-section {
    padding: 12px;
  }

  .base-url-input {
    width: 100%;
  }
}
</style>
