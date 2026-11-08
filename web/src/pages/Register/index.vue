<template>
  <div ref="authRoot" class="auth-shell">
    <div class="auth-card auth-card--single">
      <AuthPageIntro :eyebrow="publicAuthSettings.siteName" title="创建普通用户账号" description="注册成功后即可使用新账号登录后台。">
        <dl class="auth-guide">
          <div>
            <dt>设置账号资料</dt>
            <dd>填写用户名与密码，昵称可按需补充。</dd>
          </div>
          <div>
            <dt>验证手机号</dt>
            <dd>输入短信验证码，完成账号注册。</dd>
          </div>
          <div v-if="publicAuthSettings.wechatLoginEnabled">
            <dt>按需绑定微信</dt>
            <dd>支持注册时绑定，之后也可在个人中心管理。</dd>
          </div>
        </dl>
      </AuthPageIntro>

      <a-card :key="formKey" :bordered="false" class="form-card">
        <div class="form-header">
          <div>
            <h2 class="form-title">公开注册</h2>
            <div class="form-subtitle">注册后将创建普通用户账号。</div>
          </div>
        </div>

        <a-form
          layout="vertical"
          :name="formName"
          :model="registerForm"
          @validate="renderHandlers.handleFieldValidation"
          @finish="renderHandlers.handleRegister"
          @finishFailed="renderHandlers.handleFinishFailed"
          class="form"
        >
          <h3 class="form-section-heading">账号资料</h3>
          <a-form-item name="username" :help="fieldHelp('username')" label="用户名" :rules="usernameRules">
            <a-input v-bind="fieldAttrs('username')" autocomplete="username" aria-required="true" :value="registerForm.username" @update:value="renderHandlers.setUsername" placeholder="请输入用户名" size="large">
              <template #prefix>
                <UserOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
            </a-input>
          </a-form-item>
          <a-form-item name="password" :help="fieldHelp('password')" label="密码" :rules="[{ required: true, message: '请输入密码' }]">
            <a-input-password v-bind="fieldAttrs('password')" autocomplete="new-password" aria-required="true" :value="registerForm.password" @update:value="renderHandlers.setPassword" placeholder="请输入密码" size="large">
              <template #prefix>
                <LockOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
            </a-input-password>
          </a-form-item>
          <a-form-item name="nickname" :help="fieldHelp('nickname')" label="昵称">
            <a-input v-bind="fieldAttrs('nickname')" autocomplete="nickname" :value="registerForm.nickname" @update:value="renderHandlers.setNickname" placeholder="可选，便于后台识别" size="large">
              <template #prefix>
                <IdcardOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
            </a-input>
          </a-form-item>
          <h3 class="form-section-heading phone-section-heading">手机号验证</h3>
          <a-form-item name="phone" :help="fieldHelp('phone')" label="手机号" :rules="[{ required: true, message: '请输入手机号' }]">
            <MainlandPhoneInput
              v-bind="fieldAttrs('phone')" autocomplete="tel-national" aria-required="true" :value="registerForm.phone" @update:value="renderHandlers.setPhone"
              size="large"
            />
          </a-form-item>
          <a-form-item name="smsCode" :help="fieldHelp('smsCode')" label="验证码" :rules="[{ required: true, message: '请输入验证码' }]">
            <div class="sms-code-row">
              <a-input
                v-bind="fieldAttrs('smsCode')" autocomplete="one-time-code" aria-required="true" :value="registerForm.smsCode" @update:value="renderHandlers.setSMSCode"
                placeholder="请输入验证码"
                size="large"
                inputmode="numeric"
                :maxlength="6"
              >
                <template #prefix>
                  <SafetyCertificateOutlined style="color: rgba(0, 0, 0, 0.25)" />
                </template>
              </a-input>
              <a-button
                :loading="sendingCode"
                :disabled="sendingCode || smsCooldownActive"
                @click="renderHandlers.sendRegisterCode"
              >
                {{ smsCooldownText }}
              </a-button>
            </div>
          </a-form-item>
          <div v-if="publicAuthSettings.wechatLoginEnabled" class="wechat-register-panel">
            <h3 class="form-section-heading">微信绑定（可选）</h3>
            <div v-if="!wechatBindingActive" class="wechat-register-intro">
              <span>注册时绑定微信，后续可使用微信登录。</span>
              <a-button type="default" @click="renderHandlers.startWechatBinding">绑定微信</a-button>
            </div>
            <template v-else>
              <a-radio-group
                v-if="publicAuthSettings.wechatScanLoginEnabled && publicAuthSettings.wechatCodeLoginEnabled"
                :value="wechatRegisterMode" @update:value="renderHandlers.setWechatMode"
                button-style="solid"
              >
                <a-radio-button value="scan">微信扫描</a-radio-button>
                <a-radio-button value="code">微信验证码</a-radio-button>
              </a-radio-group>
              <template v-if="wechatRegisterMode === 'scan'">
                <div class="wechat-qrcode-frame">
                  <a-spin v-if="wechatQrLoading" />
                  <img v-else-if="wechatQrCodeImageData" :src="wechatQrCodeImageData" alt="微信绑定二维码" />
                  <div v-else class="wechat-qrcode-empty">点击下方按钮获取二维码</div>
                </div>
                <div class="wechat-status" role="status" aria-live="polite" aria-atomic="true">{{ wechatRegisterStatusText }}</div>
                <a-button block :loading="wechatQrLoading" @click="renderHandlers.refreshWechatRegisterQRCode">刷新二维码</a-button>
              </template>
              <template v-else>
                <div class="wechat-qrcode-frame">
                  <img v-if="publicAuthSettings.wechatCodeStaticQrcodeUrl" :src="publicAuthSettings.wechatCodeStaticQrcodeUrl" alt="微信验证码二维码" />
                  <div v-else class="wechat-qrcode-empty">请联系管理员配置验证码二维码</div>
                </div>
                <div class="wechat-status">扫码关注后发送“验证码”，再输入收到的验证码</div>
                <a-input :value="wechatRegisterCode" @update:value="renderHandlers.setWechatCode" placeholder="请输入微信验证码" size="large" :maxlength="12" />
                <a-button
                  type="primary"
                  block
                  :loading="wechatCodeSubmitting"
                  :disabled="wechatCodeSubmitting || !wechatRegisterCode || !publicAuthSettings.wechatCodeStaticQrcodeUrl"
                  @click="renderHandlers.submitWechatRegisterCode"
                >
                  确认微信验证码
                </a-button>
              </template>
              <div class="wechat-register-actions">
                <span v-if="wechatRegisterStaged" class="wechat-register-staged">微信已暂存，提交注册后完成绑定</span>
                <a-button type="link" @click="renderHandlers.cancelWechatBinding">取消绑定</a-button>
              </div>
            </template>
          </div>
          <a-form-item>
            <a-button
              type="primary"
              html-type="submit"
              :loading="submitting"
              :disabled="submitting || !registerForm.username || !registerForm.password || !registerForm.phone || !registerForm.smsCode"
              class="submit-button"
              block
            >
              注 册
            </a-button>
          </a-form-item>
        </a-form>

        <a-alert
          v-if="registerDisabledNotice"
          type="warning"
          show-icon
          class="register-alert"
          :message="registerDisabledNotice"
        />

        <div class="auth-actions">
          <div class="action-title">已经有账号？</div>
          <div class="action-subtitle">注册完成后会自动带回登录页，你也可以直接返回登录。</div>
          <a-button class="secondary-button" block @click="renderHandlers.goToLogin">返回登录页</a-button>
        </div>
      </a-card>
    </div>
  </div>
</template>

<script>
import { computed, defineComponent, getCurrentInstance, h, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue';
import { UserOutlined, LockOutlined, IdcardOutlined, SafetyCertificateOutlined } from '@ant-design/icons-vue';
import {
  registerUser,
  sendPublicSMSCode,
  createCaptchaChallenge,
  verifyCaptchaChallenge,
  verifyRemoteCaptcha,
  getPublicRegisterStatus,
  createWechatRegisterSession,
  createWechatRegisterQRCode,
  getWechatRegisterStatus,
  stageWechatRegisterCode,
  releaseWechatRegisterFlow,
} from '../../api/TikuComponentApi.js'
import { buildPublicRegisterPayload, buildPublicSMSCodePayload, limitUsernameInput, normalizeUsername } from '../../utils/phoneAuthHelpers.mjs'
import {
  ensurePhoneBeforeSMSCaptcha,
  normalizeSMSCodeInput,
} from '../../utils/captchaHelpers.mjs'
import MainlandPhoneInput from '../../components/MainlandPhoneInput.vue'
import AuthPageIntro from '../../components/AuthPageIntro.vue'
import { ensureAuthCaptchaToken } from '../../utils/captchaDialog.mjs'
import { useSmsCooldown } from '../../utils/smsCooldown.js'
import { applySiteName } from '../../utils/siteName.js'
import { message } from 'ant-design-vue';
import { useRoute, useRouter } from 'vue-router';
import { captureCurrentUserLease, useCurrentUserDisplay } from '../../utils/currentUser.js'
import {
  appendWechatRegisterFlowToken,
  createWechatRegisterFlowCoordinator,
} from '../../utils/wechatRegisterFlow.mjs'

const CaptchaSceneSMSSendRegister = 'sms_send_register'
const CaptchaSceneRegisterSubmit = 'register_submit'

export default defineComponent({
  name: 'AdapterRegister',
  setup() {
    const router = useRouter()
    const route = useRoute()
    const mountedPath = route.path
    const authRoot = ref(null)
    const renderEpoch = ref(0)
    const formKey = ref(0)
    const formPrefix = 'register-form-' + getCurrentInstance().uid
    const formName = computed(() => formPrefix + '-' + renderEpoch.value)
    const { currentUser } = useCurrentUserDisplay()
    let formAlive = true
    let inputEpoch = 0
    let phoneEpoch = 0
    let wechatInputEpoch = 0
    let flowEpoch = 0
    let internalInputChange = false
    let viewLease = captureCurrentUserLease()
    let wechatRegisterPollTimer = null
    let visibilityHandler = null
    const attempts = new Map()
    const routeKey = () => JSON.stringify([route.fullPath, route.path, route.query, route.params, route.hash])
    const pageActive = () => formAlive && route.path === mountedPath
    const submitting = ref(false)
    const sendingCode = ref(false)
    const smsCooldown = useSmsCooldown()
    const registerDisabledNotice = ref('')
    const fieldErrors = reactive({})
    const publicAuthSettings = ref({
      siteName: '题库', registerEnabled: false, smsLoginEnabled: false,
      wechatLoginEnabled: false, wechatScanLoginEnabled: false, wechatCodeLoginEnabled: false,
      wechatDefaultLoginMode: 'scan', wechatCodeStaticQrcodeUrl: '', alternativeLoginEnabled: false,
      captchaProvider: 'remote', captchaApiBaseUrl: 'https://captcha.api.zelly.cn', captchaType: 'RANDOM',
    })
    const registerForm = reactive({ username: '', password: '', nickname: '', phone: '', smsCode: '', captchaToken: '' })
    const wechatBindingActive = ref(false)
    const wechatRegisterStaged = ref(false)
    const wechatRegisterMode = ref('scan')
    const wechatQrLoading = ref(false)
    const wechatQrCodeImageData = ref('')
    const wechatRegisterSessionId = ref('')
    const wechatRegisterCode = ref('')
    const wechatCodeSubmitting = ref(false)
    const wechatRegisterStatusText = ref('请使用微信扫码绑定')
    const flowCoordinator = createWechatRegisterFlowCoordinator({
      onDuplicate: () => {
        if (!formAlive) return
        retireView({ clearFlow: false })
        wechatBindingActive.value = false
        wechatRegisterStaged.value = false
        wechatRegisterStatusText.value = '此注册流程已在其他标签页打开'
      },
    })
    const usernameRules = [
      { required: true, message: '请输入用户名' },
      { validator: (_rule, value) => {
        try { normalizeUsername(value); return Promise.resolve() }
        catch (error) {
          const allowed = ['用户名不能为空', '用户名长度不能超过255个字符', '用户名只能使用中文、英文、数字、下划线和@']
          return Promise.reject(new Error(allowed.includes(error?.message) ? error.message : '用户名不符合要求'))
        }
      } },
    ]
    const fieldKey = name => Array.isArray(name) ? name.join('_') : String(name || '')
    const fieldId = name => formName.value + '_' + fieldKey(name)
    const fieldAttrs = name => ({
      id: fieldId(name), 'aria-invalid': fieldErrors[name] ? 'true' : undefined,
      'aria-describedby': fieldErrors[name] ? fieldId(name) + '-error' : undefined,
    })
    const fieldHelp = name => fieldErrors[name] ? h('span', { id: fieldId(name) + '-error' }, fieldErrors[name]) : undefined
    const handleFieldValidation = (name, valid, errors) => {
      const key = fieldKey(name)
      if (!Object.prototype.hasOwnProperty.call(registerForm, key)) return
      if (valid || !errors?.length) delete fieldErrors[key]
      else fieldErrors[key] = String(errors[0])
    }
    const closeAttempt = owner => {
      if (!owner) return
      owner.controller.abort()
      owner.lease.release()
      if (attempts.get(owner.kind) !== owner) return
      attempts.delete(owner.kind)
      if (owner.kind === 'register') submitting.value = false
      if (owner.kind === 'sms-send') sendingCode.value = false
      if (owner.kind === 'wechat-qr' || owner.kind === 'wechat-start') wechatQrLoading.value = false
      if (owner.kind === 'wechat-code') wechatCodeSubmitting.value = false
    }
    const retireDraftAttempt = owner => {
      if (!owner) return
      // An issued mutation cannot be revoked by editing its draft. Retire paint
      // authority, but retain its lane/busy lock until the real promise settles.
      if (owner.issued) owner.controller.abort()
      else closeAttempt(owner)
    }
    const stopWechatRegisterPolling = () => {
      if (wechatRegisterPollTimer) clearInterval(wechatRegisterPollTimer)
      wechatRegisterPollTimer = null
      closeAttempt(attempts.get('wechat-observe'))
      closeAttempt(attempts.get('wechat-status'))
    }
    const clearPrivateInputs = () => {
      internalInputChange = true
      try {
        Object.keys(registerForm).forEach(key => { registerForm[key] = '' })
        wechatRegisterCode.value = ''
        authRoot.value?.querySelectorAll('input:not([type="radio"]):not([type="checkbox"]), textarea').forEach(input => {
          input.value = ''; input.defaultValue = ''; input.removeAttribute('value')
        })
      } finally { internalInputChange = false }
      Object.keys(fieldErrors).forEach(key => { delete fieldErrors[key] })
    }
    const clearWechatDisplay = () => {
      authRoot.value?.querySelectorAll('img[alt="微信绑定二维码"]').forEach(image => image.removeAttribute('src'))
      authRoot.value?.querySelectorAll('input[placeholder="请输入微信验证码"]').forEach(input => {
        input.value = ''; input.defaultValue = ''; input.removeAttribute('value')
      })
      wechatRegisterSessionId.value = ''
      wechatQrCodeImageData.value = ''
      internalInputChange = true
      try { wechatRegisterCode.value = '' } finally { internalInputChange = false }
    }
    const installVisibilityHandler = () => {
      if (typeof document === 'undefined') return
      if (visibilityHandler) document.removeEventListener('visibilitychange', visibilityHandler)
      const epoch = renderEpoch.value
      const location = routeKey()
      visibilityHandler = () => {
        if (!viewCurrent(epoch) || location !== routeKey()) return
        if (document.visibilityState === 'hidden') stopWechatRegisterPolling()
        else if (flowCoordinator.token && wechatBindingActive.value) {
          startWechatRegisterPolling()
          void checkWechatRegisterStatus()
        }
      }
      document.addEventListener('visibilitychange', visibilityHandler)
    }
    const retireView = ({ clearFlow = true, disposeFlow = false, clearInputs = true, preserveRegisterActions = false } = {}) => {
      renderEpoch.value += 1
      const retiredRender = renderEpoch.value
      // Drain Antd's accepted controlled values before replacing its native refs.
      // Authority and private native bytes retire now; no synthetic input.
      nextTick(async () => {
        await nextTick()
        if (formAlive && renderEpoch.value === retiredRender) formKey.value = retiredRender
      })
      for (const owner of [...attempts.values()]) {
        // Optional WeChat presentation is not a registration/SMS scope change.
        if (preserveRegisterActions && (owner.kind === 'register' || owner.kind === 'sms-send')) owner.render = retiredRender
        else if (preserveRegisterActions && owner.issued) retireDraftAttempt(owner)
        else closeAttempt(owner)
      }
      stopWechatRegisterPolling()
      if (clearInputs) clearPrivateInputs()
      clearWechatDisplay()
      if (!preserveRegisterActions) flowEpoch += 1
      if (clearFlow) {
        const token = flowCoordinator.token
        if (flowCoordinator.token === token) flowCoordinator.clear(token)
        wechatBindingActive.value = false
        wechatRegisterStaged.value = false
      }
      if (disposeFlow) {
        // Normal navigation/unmount drops this component's private capability,
        // but keeps the existing sessionStorage recovery contract intact.
        flowCoordinator.dispose()
        wechatBindingActive.value = false
        wechatRegisterStaged.value = false
      }
      if (clearInputs) {
        smsCooldown.stop()
        smsCooldown.remaining.value = 0
        registerDisabledNotice.value = ''
      }
      viewLease?.release()
      viewLease = pageActive() ? captureCurrentUserLease() : null
      if (pageActive()) installVisibilityHandler()
    }
    // Freshness is not permission: anonymous/no-id users and storage denial may
    // start registration. Only an observed generation transition retires it.
    const viewCurrent = (epoch = renderEpoch.value) => {
      if (!pageActive() || epoch !== renderEpoch.value) return false
      if (!viewLease || viewLease.isRetired()) { retireView(); return false }
      return true
    }
    const isCurrent = owner => {
      if (!owner || attempts.get(owner.kind) !== owner || !pageActive() ||
        owner.render !== renderEpoch.value || owner.route !== routeKey() ||
        (owner.inputBound && owner.input !== draftEpoch(owner.inputBound)) ||
        (owner.flowBound && owner.flow !== flowEpoch)) return false
      if (owner.lease.isRetired()) { retireView(); return false }
      return true
    }
    const draftEpoch = scope => scope === 'phone' ? phoneEpoch : scope === 'wechat' ? wechatInputEpoch : inputEpoch
    const beginAttempt = (kind, inputBound = true, flowBound = false) => {
      if (!viewCurrent() || attempts.has(kind)) return null
      const owner = { kind, inputBound, flowBound, input: draftEpoch(inputBound), flow: flowEpoch,
        render: renderEpoch.value, route: routeKey(), lease: captureCurrentUserLease(), controller: new AbortController() }
      attempts.set(kind, owner)
      return owner
    }
    const unknownOutcomeText = '操作结果未确认，请先核对当前状态，勿直接重复提交。'
    const publicReasons = new Map([
      ['请输入手机号', '请输入手机号'], ['请输入11位手机号', '请输入11位手机号'],
      ['手机号格式不正确', '请输入11位手机号'], ['请输入验证码', '请输入验证码'],
      ['请输入用户名', '请输入用户名'], ['请输入密码', '请输入密码'],
      ['用户名不符合要求', '用户名不符合要求'], ['用户名已存在', '用户名已存在，请更换用户名'],
      ['用户名不能为空', '用户名不能为空'], ['用户名长度不能超过255个字符', '用户名长度不能超过255个字符'],
      ['用户名只能使用中文、英文、数字、下划线和@', '用户名只能使用中文、英文、数字、下划线和@'],
      ['手机号已被使用', '手机号已被使用，请核对手机号'], ['未开放注册', '当前暂不可注册，请返回登录页。'],
      ['验证码错误或已过期', '验证码错误或已过期，请重新获取'],
      ['微信验证码已过期，请重新获取', '微信验证码已过期，请重新获取'],
      ['微信验证码无效，请重新获取', '微信验证码无效，请重新获取'],
      ['请先完成人机验证', '请先完成人机验证'], ['图形验证码错误', '图形验证码错误，请重新验证'],
      ['图形验证码校验失败', '图形验证码校验失败，请重新验证'],
      ['人机验证不存在或已过期', '人机验证已过期，请重新验证'],
      ['人机验证已过期，请重新验证', '人机验证已过期，请重新验证'],
      ['人机验证服务不可用', '人机验证服务暂不可用，请稍后再试'],
      ['远程验证码校验失败', '人机验证未通过，请重新验证'],
      ['发送过于频繁，请稍后再试', '请求过于频繁，请稍后再试'],
      ['手机号功能未启用', '手机号功能未启用，请联系管理员'],
      ['短信配置未完成', '短信服务尚未配置完成，请联系管理员'],
    ])
    const flowErrorStatus = error => Number(error?.response?.status ?? error?.status)
    const publicFailureText = (failure, fallback) => {
      if (failure?.code === 'UNKNOWN_OUTCOME') return unknownOutcomeText
      if (failure?.name === 'UserCancelledCaptchaError') return '已取消人机验证，尚未提交本次请求'
      const known = publicReasons.get(failure?.message)
      if (known) return known
      const status = flowErrorStatus(failure)
      if (status === 429) return '请求过于频繁，请稍后再试'
      if (status === 401) return '验证未通过，请核对当前状态后再试'
      if (status === 403) return '当前操作不可用，请联系管理员'
      return fallback
    }
    const reportError = (owner, error, fallback) => {
      if (!isCurrent(owner) || error?.name === 'AbortError' || error?.code === 'ERR_CANCELED') return
      const text = publicFailureText(error, fallback)
      if (text === unknownOutcomeText) message.warning(text)
      else message.error(text)
    }
    const ensureCaptchaToken = (scene, targetForm, owner) => ensureAuthCaptchaToken({
      scene, targetForm, publicSettings: { ...publicAuthSettings.value },
      createCaptchaChallenge, verifyCaptchaChallenge, verifyRemoteCaptcha,
      signal: owner.controller.signal, isCurrent: () => isCurrent(owner),
    })
    const loadPublicAuthSettings = async owner => {
      if (!isCurrent(owner)) return null
      const res = await getPublicRegisterStatus()
      if (!isCurrent(owner)) return null
      if (res?.status === 200) {
        const data = res?.data || {}
        publicAuthSettings.value = {
          siteName: data.siteName || '题库', registerEnabled: !!data.registerEnabled,
          smsLoginEnabled: !!data.smsLoginEnabled, wechatLoginEnabled: !!data.wechatLoginEnabled,
          wechatScanLoginEnabled: !!data.wechatScanLoginEnabled, wechatCodeLoginEnabled: !!data.wechatCodeLoginEnabled,
          wechatDefaultLoginMode: data.wechatDefaultLoginMode === 'code' ? 'code' : 'scan',
          wechatCodeStaticQrcodeUrl: data.wechatCodeStaticQrcodeUrl || '', alternativeLoginEnabled: !!data.alternativeLoginEnabled,
          captchaProvider: data.captchaProvider || 'remote', captchaApiBaseUrl: data.captchaApiBaseUrl || 'https://captcha.api.zelly.cn',
          captchaType: data.captchaType || 'RANDOM',
        }
        applySiteName(publicAuthSettings.value.siteName)
      }
      return res
    }
    const chooseWechatRegisterMode = () => {
      const settings = publicAuthSettings.value
      if (settings.wechatDefaultLoginMode === 'code' && settings.wechatCodeLoginEnabled) return 'code'
      if (settings.wechatScanLoginEnabled) return 'scan'
      return settings.wechatCodeLoginEnabled ? 'code' : 'scan'
    }
    const isTerminalWechatRegisterStatus = status => status === 401 || status === 409 || status === 410
    const isWechatQRCodeError = value => {
      const text = String(value?.message || value?.response?.data?.message || value?.response?.data?.detail || '')
      return text.includes('微信二维码会话无效') || text.includes('微信二维码已过期')
    }
    const isWechatFlowError = value => {
      const text = String(value?.message || value?.response?.data?.message || value?.response?.data?.detail || '')
      return text.includes('注册流程令牌无效') || text.includes('注册流程已过期')
    }
    const clearWechatRegisterFlow = ({ release = false } = {}, owner = null) => {
      if (owner && !isCurrent(owner)) return null
      const token = flowCoordinator.token
      // Compare-clear before awaiting release. Its late response cannot clear
      // a later capability, staged flow, QR image or busy state.
      stopWechatRegisterPolling()
      if (flowCoordinator.token === token) flowCoordinator.clear(token)
      flowEpoch += 1
      for (const attempt of [...attempts.values()]) if (attempt.flowBound) retireDraftAttempt(attempt)
      wechatBindingActive.value = false
      wechatRegisterStaged.value = false
      clearWechatDisplay()
      return release && token ? { registrationFlowToken: token } : null
    }
    const markWechatQRCodeExpired = owner => {
      if (!isCurrent(owner)) return
      stopWechatRegisterPolling()
      clearWechatDisplay()
      wechatRegisterStaged.value = false
      wechatRegisterStatusText.value = '二维码已过期，请刷新二维码'
    }
    const applyWechatRegisterSnapshot = (snapshot, owner) => {
      if (!isCurrent(owner)) return
      const data = snapshot || {}
      if (data.status === 'expired') { markWechatQRCodeExpired(owner); return }
      if (data.qrcode?.sessionId) {
        wechatRegisterSessionId.value = data.qrcode.sessionId
        wechatQrCodeImageData.value = data.qrcode.displayData || ''
      }
      wechatRegisterStaged.value = data.status === 'staged' || !!data.staged
      if (wechatRegisterStaged.value) wechatRegisterStatusText.value = '微信已暂存，提交注册后完成绑定'
      else if (data.status === 'pending') wechatRegisterStatusText.value = '请使用微信扫码绑定'
    }
    const ensureWechatRegisterFlow = async owner => {
      if (!isCurrent(owner)) return ''
      const token = flowCoordinator.token
      const res = await createWechatRegisterSession(token ? { registrationFlowToken: token } : undefined)
      if (!isCurrent(owner) || flowCoordinator.token !== token) return ''
      if (res?.status !== 200) {
        throw res
      }
      const nextToken = token || String(res?.data?.registrationFlowToken || '')
      if (!nextToken) throw new Error('微信注册流程创建失败')
      if (!token) {
        flowCoordinator.save(nextToken)
        flowEpoch += 1
        owner.flow = flowEpoch
        // Creating a new flow retires the previous release/observer generation.
        for (const attempt of [...attempts.values()]) {
          if (attempt !== owner && attempt.flowBound) retireDraftAttempt(attempt)
        }
      }
      if (!isCurrent(owner) || flowCoordinator.token !== nextToken) return ''
      applyWechatRegisterSnapshot(res.data, owner)
      return nextToken
    }
    const checkWechatRegisterStatus = async (observer = null) => {
      if ((observer && !isCurrent(observer)) || !viewCurrent() || !wechatBindingActive.value) return
      const token = flowCoordinator.token
      if (!token) return
      const owner = beginAttempt('wechat-status', false, true)
      if (!owner) return
      const sessionId = wechatRegisterSessionId.value
      try {
        if (!isCurrent(owner)) return
        const res = sessionId ? await getWechatRegisterStatus({ sessionId })
          : await createWechatRegisterSession({ registrationFlowToken: token })
        if (!isCurrent(owner) || flowCoordinator.token !== token || wechatRegisterSessionId.value !== sessionId) return
        if (res?.status === 200) applyWechatRegisterSnapshot(res.data, owner)
        else if (sessionId && isWechatQRCodeError(res)) markWechatQRCodeExpired(owner)
        else if (isTerminalWechatRegisterStatus(res?.status) && (!sessionId || isWechatFlowError(res))) {
          clearWechatRegisterFlow({}, owner)
          wechatRegisterStatusText.value = res.status === 410 ? '微信注册流程已过期，请重新绑定' : '微信绑定已被其他注册占用'
        } else if (isTerminalWechatRegisterStatus(res?.status)) markWechatQRCodeExpired(owner)
      } catch (error) {
        if (!isCurrent(owner) || flowCoordinator.token !== token || wechatRegisterSessionId.value !== sessionId) return
        const status = flowErrorStatus(error)
        if (isWechatQRCodeError(error)) markWechatQRCodeExpired(owner)
        else if (isTerminalWechatRegisterStatus(status) && (!sessionId || isWechatFlowError(error))) {
          clearWechatRegisterFlow({}, owner)
          wechatRegisterStatusText.value = status === 410 ? '微信注册流程已过期，请重新绑定' : '微信绑定已被其他注册占用'
        } else if (isTerminalWechatRegisterStatus(status)) markWechatQRCodeExpired(owner)
      } finally { closeAttempt(owner) }
    }
    const startWechatRegisterPolling = () => {
      stopWechatRegisterPolling()
      if (!viewCurrent() || !flowCoordinator.token || !wechatBindingActive.value ||
        (typeof document !== 'undefined' && document.visibilityState === 'hidden')) return
      const observer = beginAttempt('wechat-observe', false, true)
      if (!observer) return
      wechatRegisterPollTimer = setInterval(() => {
        if (isCurrent(observer)) void checkWechatRegisterStatus(observer)
      }, 5000)
    }
    const refreshWechatRegisterQRCode = async (refresh = false, inheritedOwner = null) => {
      if (!viewCurrent() || !wechatBindingActive.value || wechatRegisterMode.value !== 'scan' || wechatQrLoading.value) return
      const owner = inheritedOwner || beginAttempt('wechat-qr', false, true)
      if (!owner || !isCurrent(owner)) return
      wechatQrLoading.value = true
      try {
        const token = await ensureWechatRegisterFlow(owner)
        if (!token || !isCurrent(owner)) return
        const res = await createWechatRegisterQRCode({ registrationFlowToken: token, refresh })
        if (!isCurrent(owner) || flowCoordinator.token !== token) return
        if (res?.status !== 200 || !res?.data?.sessionId) {
          const text = publicFailureText(res, '二维码创建未完成，请核对当前状态后再操作。')
          if (isTerminalWechatRegisterStatus(res?.status)) clearWechatRegisterFlow({}, owner)
          message.error(text)
          return
        }
        wechatRegisterSessionId.value = res.data.sessionId
        wechatQrCodeImageData.value = res.data.displayData || ''
        wechatRegisterStatusText.value = '请使用微信扫码绑定'
        startWechatRegisterPolling()
      } catch (error) {
        if (!isCurrent(owner)) return
        const text = publicFailureText(error, '二维码创建结果未确认，请先核对，勿直接重复操作。')
        if (isTerminalWechatRegisterStatus(flowErrorStatus(error))) clearWechatRegisterFlow({}, owner)
        if (error?.code === 'UNKNOWN_OUTCOME') message.warning(text)
        else message.error(text)
      } finally {
        if (isCurrent(owner)) wechatQrLoading.value = false
        if (!inheritedOwner) closeAttempt(owner)
      }
    }
    const startWechatBinding = async () => {
      if (!viewCurrent() || !publicAuthSettings.value.wechatLoginEnabled || wechatBindingActive.value) return
      const owner = beginAttempt('wechat-start', false, true)
      if (!owner) return
      try {
        const token = await ensureWechatRegisterFlow(owner)
        if (!token || !isCurrent(owner)) return
        wechatBindingActive.value = true
        internalInputChange = true
        try { wechatRegisterMode.value = chooseWechatRegisterMode() } finally { internalInputChange = false }
        if (wechatRegisterMode.value === 'scan') await refreshWechatRegisterQRCode(false, owner)
        else startWechatRegisterPolling()
      } catch (error) {
        if (!isCurrent(owner)) return
        const text = publicFailureText(error, '微信绑定结果未确认，请先核对当前状态。')
        if (isTerminalWechatRegisterStatus(flowErrorStatus(error))) clearWechatRegisterFlow({}, owner)
        if (error?.code === 'UNKNOWN_OUTCOME') message.warning(text)
        else message.error(text)
      }
      finally { closeAttempt(owner) }
    }
    const handleWechatRegisterModeChange = () => {
      if (!viewCurrent() || !wechatBindingActive.value) return
      if (wechatRegisterMode.value === 'code') {
        wechatRegisterStatusText.value = wechatRegisterStaged.value ? '微信已暂存，提交注册后完成绑定' : '请输入微信验证码'
      } else if (!wechatQrCodeImageData.value) void refreshWechatRegisterQRCode(false)
      else startWechatRegisterPolling()
    }
    const submitWechatRegisterCode = async () => {
      if (!viewCurrent() || !wechatBindingActive.value || wechatRegisterMode.value !== 'code' ||
        !wechatRegisterCode.value || !publicAuthSettings.value.wechatCodeStaticQrcodeUrl) return
      const owner = beginAttempt('wechat-code', 'wechat', true)
      if (!owner) return
      const code = wechatRegisterCode.value.trim()
      wechatCodeSubmitting.value = true
      try {
        const token = await ensureWechatRegisterFlow(owner)
        if (!token || !isCurrent(owner)) return
        owner.issued = true
        const res = await stageWechatRegisterCode({ registrationFlowToken: token, code })
        if (!isCurrent(owner) || flowCoordinator.token !== token) return
        if (res?.status !== 200) {
          const text = publicFailureText(res, '微信验证码绑定未完成，请核对后再操作。')
          if (isTerminalWechatRegisterStatus(res?.status)) clearWechatRegisterFlow({}, owner)
          message.error(text)
          return
        }
        // A read issued before this mutation must not downgrade its result.
        closeAttempt(attempts.get('wechat-status'))
        wechatRegisterStaged.value = true
        wechatRegisterStatusText.value = '微信已暂存，提交注册后完成绑定'
        startWechatRegisterPolling()
      } catch (error) {
        if (!isCurrent(owner)) return
        const text = publicFailureText(error, '微信验证码绑定结果未确认，请先核对，勿直接重复操作。')
        if (isTerminalWechatRegisterStatus(flowErrorStatus(error))) clearWechatRegisterFlow({}, owner)
        if (error?.code === 'UNKNOWN_OUTCOME') message.warning(text)
        else message.error(text)
      } finally { closeAttempt(owner) }
    }
    const cancelWechatBinding = async () => {
      if (!viewCurrent()) return
      const payload = clearWechatRegisterFlow({ release: true })
      if (!payload) return
      const owner = beginAttempt('wechat-release', false, true)
      if (!owner) return
      try {
        if (!isCurrent(owner)) return
        await releaseWechatRegisterFlow(payload)
      } catch (error) { reportError(owner, error, '本地绑定已取消，服务端释放结果未确认，请先核对。') }
      finally { closeAttempt(owner) }
    }
    const resumeWechatRegisterFlow = async () => {
      if (!viewCurrent() || !flowCoordinator.token) return
      const owner = beginAttempt('wechat-resume', false, true)
      if (!owner) return
      const token = flowCoordinator.token
      try {
        const res = await createWechatRegisterSession({ registrationFlowToken: token })
        if (!isCurrent(owner) || flowCoordinator.token !== token) return
        if (res?.status !== 200) throw res
        applyWechatRegisterSnapshot(res.data, owner)
        if (!isCurrent(owner)) return
        wechatBindingActive.value = true
        internalInputChange = true
        try { wechatRegisterMode.value = chooseWechatRegisterMode() } finally { internalInputChange = false }
        startWechatRegisterPolling()
      } catch (error) {
        if (isCurrent(owner) && flowCoordinator.token === token && isTerminalWechatRegisterStatus(flowErrorStatus(error))) clearWechatRegisterFlow({}, owner)
      } finally { closeAttempt(owner) }
    }
    const sendRegisterCode = async () => {
      if (!viewCurrent() || smsCooldown.active.value) return
      const owner = beginAttempt('sms-send', 'phone')
      if (!owner) return
      sendingCode.value = true
      try {
        const form = { phone: ensurePhoneBeforeSMSCaptcha(registerForm.phone), captchaToken: '' }
        if (!isCurrent(owner)) return
        owner.phase = 'captcha'
        await ensureCaptchaToken(CaptchaSceneSMSSendRegister, form, owner)
        if (!isCurrent(owner)) return
        const payload = buildPublicSMSCodePayload(form, 'register')
        owner.phase = 'sms-send'
        owner.issued = true
        const res = await sendPublicSMSCode(payload)
        if (!isCurrent(owner)) return
        if (res?.status === 200) { message.success('发送成功，请注意查收'); smsCooldown.start() }
        else if (res?.status === 202) { message.warning('短信发送结果待核对，请先等待短信，勿重复发送'); smsCooldown.start() }
        else message.error(publicFailureText(res, '短信发送未完成，请先核对接收情况后再操作。'))
      } catch (error) {
        reportError(owner, error, owner.phase === 'sms-send' ? unknownOutcomeText : '人机验证未完成，请重新验证')
      } finally { closeAttempt(owner) }
    }
    const handleRegister = async () => {
      const owner = beginAttempt('register', true, true)
      if (!owner) return
      registerDisabledNotice.value = ''
      submitting.value = true
      try {
        const form = { ...registerForm, username: normalizeUsername(registerForm.username), captchaToken: '' }
        if (!isCurrent(owner)) return
        owner.phase = 'captcha'
        await ensureCaptchaToken(CaptchaSceneRegisterSubmit, form, owner)
        if (!isCurrent(owner)) return
        // Preserve the existing builder's username/phone/password rules.
        const payload = appendWechatRegisterFlowToken(buildPublicRegisterPayload(form),
          wechatRegisterStaged.value ? flowCoordinator.token : '')
        const username = payload.username
        const token = flowCoordinator.token
        if (!isCurrent(owner)) return
        owner.phase = 'register'
        owner.issued = true
        const res = await registerUser(payload)
        if (!isCurrent(owner) || flowCoordinator.token !== token) return
        if (res?.status === 200) {
          message.success('注册成功，请使用新账号登录')
          retireView()
          const navigationOwner = beginAttempt('register-navigation', false)
          if (!navigationOwner) return
          try {
            if (!isCurrent(navigationOwner)) return
            const navigation = await router.push({ path: '/login', query: { username } })
            if (navigation instanceof Error) throw new Error('页面跳转未完成')
          } catch (error) {
            reportError(navigationOwner, error, '注册已成功，但页面跳转未完成，请返回登录页，勿重复注册。')
          } finally { closeAttempt(navigationOwner) }
        } else {
          const text = publicFailureText(res, '注册未完成，请核对当前状态后再操作。')
          if (res?.message === '未开放注册') registerDisabledNotice.value = '当前暂不可注册，请返回登录页。'
          if (isTerminalWechatRegisterStatus(res?.status)) clearWechatRegisterFlow({}, owner)
          message.error(text)
        }
      } catch (error) {
        if (!isCurrent(owner)) return
        const text = publicFailureText(error, owner.phase === 'register' ? unknownOutcomeText : '注册资料或人机验证未完成，请核对后再操作。')
        if (isTerminalWechatRegisterStatus(flowErrorStatus(error))) clearWechatRegisterFlow({}, owner)
        if (text === unknownOutcomeText) message.warning(text)
        else if (error?.name !== 'AbortError' && error?.code !== 'ERR_CANCELED') message.error(text)
      } finally { closeAttempt(owner) }
    }
    const goToLogin = async () => {
      const owner = beginAttempt('login-navigation', false)
      if (!owner) return
      try {
        if (!isCurrent(owner)) return
        await router.push('/login')
      } catch (error) { reportError(owner, error, '页面跳转未完成，请在当前页面核对。') }
      finally { closeAttempt(owner) }
    }
    const handleFinishFailed = errors => {
      if (!viewCurrent()) return
      const epoch = renderEpoch.value
      const location = routeKey()
      const fields = Array.isArray(errors?.errorFields) ? errors.errorFields : []
      fields.forEach(field => handleFieldValidation(field.name, false, field.errors))
      const id = fields[0]?.name ? fieldId(fields[0].name) : ''
      if (id) nextTick(() => { if (viewCurrent(epoch) && location === routeKey()) document.getElementById(id)?.focus() })
    }
    const renderHandlers = computed(() => {
      const epoch = formKey.value
      const run = action => (...args) => { if (!internalInputChange && viewCurrent(epoch)) return action(...args) }
      return {
        handleRegister: run(handleRegister), handleFinishFailed: run(handleFinishFailed), handleFieldValidation: run(handleFieldValidation),
        sendRegisterCode: run(sendRegisterCode), startWechatBinding: run(startWechatBinding),
        refreshWechatRegisterQRCode: run(() => refreshWechatRegisterQRCode(true)),
        submitWechatRegisterCode: run(submitWechatRegisterCode), cancelWechatBinding: run(cancelWechatBinding), goToLogin: run(goToLogin),
        setUsername: run(value => { registerForm.username = limitUsernameInput(value) }),
        setPassword: run(value => { registerForm.password = value }), setNickname: run(value => { registerForm.nickname = value }),
        setPhone: run(value => { registerForm.phone = value }), setSMSCode: run(value => { registerForm.smsCode = normalizeSMSCodeInput(value) }),
        setWechatCode: run(value => { wechatRegisterCode.value = String(value || '').slice(0, 12) }),
        setWechatMode: run(value => { wechatRegisterMode.value = value }),
      }
    })
    watch(() => [registerForm.username, registerForm.password, registerForm.nickname, registerForm.phone, registerForm.smsCode], () => {
      if (internalInputChange) return
      inputEpoch += 1
      for (const owner of [...attempts.values()]) if (owner.inputBound === true) retireDraftAttempt(owner)
    }, { flush: 'sync' })
    watch(() => registerForm.phone, () => {
      if (internalInputChange) return
      phoneEpoch += 1
      for (const owner of [...attempts.values()]) if (owner.inputBound === 'phone') retireDraftAttempt(owner)
    }, { flush: 'sync' })
    watch(wechatRegisterCode, () => {
      if (internalInputChange) return
      wechatInputEpoch += 1
      for (const owner of [...attempts.values()]) if (owner.inputBound === 'wechat') retireDraftAttempt(owner)
    }, { flush: 'sync' })
    watch(wechatRegisterMode, () => {
      if (internalInputChange) return
      // A staged capability survives a mode switch, unlike old native handlers.
      retireView({ clearFlow: false, clearInputs: false, preserveRegisterActions: true })
      handleWechatRegisterModeChange()
    }, { flush: 'sync' })
    watch(routeKey, () => retireView({ clearFlow: false, disposeFlow: !pageActive() }), { flush: 'sync' })
    watch(currentUser, () => retireView(), { flush: 'sync' })
    onMounted(async () => {
      installVisibilityHandler()
      const owner = beginAttempt('settings', false)
      if (!owner) return
      try {
        await loadPublicAuthSettings(owner)
        if (isCurrent(owner)) await resumeWechatRegisterFlow()
      } catch (_) { /* Existing public defaults remain after a failed read. */ }
      finally { closeAttempt(owner) }
    })
    onBeforeUnmount(() => {
      formAlive = false
      retireView({ clearFlow: false, disposeFlow: true })
      flowCoordinator.dispose()
      if (typeof document !== 'undefined' && visibilityHandler) document.removeEventListener('visibilitychange', visibilityHandler)
    })
    return {
      authRoot, renderEpoch, formKey, formName, renderHandlers, fieldAttrs, fieldHelp,
      submitting, sendingCode, smsCooldownText: smsCooldown.buttonText, smsCooldownActive: smsCooldown.active,
      startSmsCooldown: smsCooldown.start, registerDisabledNotice, publicAuthSettings,
      wechatBindingActive, wechatRegisterStaged, wechatRegisterMode, wechatQrLoading, wechatQrCodeImageData,
      wechatRegisterCode, wechatCodeSubmitting, wechatRegisterStatusText, registerForm, usernameRules,
      normalizeSMSCodeInput, limitUsernameInput, handleRegister, sendRegisterCode, startWechatBinding,
      refreshWechatRegisterQRCode, handleWechatRegisterModeChange, submitWechatRegisterCode, cancelWechatBinding,
      goToLogin, handleFinishFailed,
    }
  },

  components: {
    AuthPageIntro,
    UserOutlined,
    LockOutlined,
    IdcardOutlined,
    SafetyCertificateOutlined,
    MainlandPhoneInput,
  },
})
</script>

<style scoped>
.wechat-register-panel {
  margin: 8px 0 20px;
  padding: 18px;
  border: 1px solid var(--console-border, #dce5e1);
  border-radius: 12px;
  background: var(--console-surface-secondary, #f8faf9);
}

.register-alert {
  margin-top: 4px;
  margin-bottom: 16px;
}

</style>
