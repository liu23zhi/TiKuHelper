<template>
  <div ref="authRoot" class="auth-shell">
    <div class="auth-card auth-card--single">
      <AuthPageIntro eyebrow="账号控制台" :title="publicAuthSettings.siteName" description="搜题聚合题库平台，集成AI搜索，提供更准确的答案。">
        <dl class="auth-guide">
          <div>
            <dt>统一账号入口</dt>
            <dd>登录后管理个人资料、查看账户余额与使用记录。</dd>
          </div>
          <div v-if="publicAuthSettings.smsLoginEnabled || publicAuthSettings.wechatLoginEnabled">
            <dt>选择熟悉的登录方式</dt>
            <dd>通过已绑定的账号信息完成验证，继续使用题库服务。</dd>
          </div>
        </dl>
      </AuthPageIntro>

      <a-card :key="formKey" :bordered="false" class="form-card">
        <div class="form-header">
          <div>
            <h2 class="form-title">用户登录</h2>
            <div class="form-subtitle">使用已有账号登录获取更多功能。</div>
          </div>
        </div>

        <a-radio-group :value="loginMode" @update:value="renderHandlers.setMode" button-style="solid" class="login-mode-switch" aria-label="登录方式">
          <a-radio-button value="password">账号密码登录</a-radio-button>
          <a-radio-button v-if="publicAuthSettings.smsLoginEnabled" value="sms">短信验证码登录</a-radio-button>
          <a-radio-button v-if="publicAuthSettings.wechatLoginEnabled" value="wechat">微信登录</a-radio-button>
        </a-radio-group>

    <section v-if="currentUserFailure" data-login-me-feedback>
      <RequestFailureAlert :key="currentUserFailureContextKey" :failure="currentUserFailure" :message="currentUserFailureText" :context-key="currentUserFailureContextKey" :is-current="isCurrentCurrentUserFailure" />
    </section>

        <a-form
          layout="vertical"
          :name="formName"
          :model="activeLoginForm"
          @validate="renderHandlers.handleFieldValidation"
          @finish="renderHandlers.handleLogin"
          @finishFailed="renderHandlers.handleFinishFailed"
          class="form"
        >
          <a-form-item v-if="loginMode === 'password'" name="username" :help="fieldHelp('username')" label="用户名" :rules="[{ required: true, message: '请输入用户名' }]">
            <a-input v-bind="fieldAttrs('username')" autocomplete="username" aria-required="true" :value="loginForm.username" @update:value="renderHandlers.setUsername" placeholder="请输入用户名" size="large">
              <template #prefix>
                <UserOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
            </a-input>
          </a-form-item>
          <a-form-item v-if="loginMode === 'password'" name="password" :help="fieldHelp('password')" label="密码" :rules="[{ required: true, message: '请输入密码' }]">
            <a-input-password v-bind="fieldAttrs('password')" autocomplete="current-password" aria-required="true" :value="loginForm.password" @update:value="renderHandlers.setPassword" placeholder="请输入密码" size="large">
              <template #prefix>
                <LockOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
              <template #iconRender="visible">
                <button type="button" class="auth-password-toggle" data-auth-password-toggle="true"
                  :aria-label="visible ? '隐藏密码' : '显示密码'" :aria-pressed="visible">
                  <EyeInvisibleOutlined v-if="visible" aria-hidden="true" />
                  <EyeOutlined v-else aria-hidden="true" />
                </button>
              </template>
            </a-input-password>
          </a-form-item>

          <template v-if="loginMode === 'sms'">
            <a-form-item name="phone" :help="fieldHelp('phone')" label="手机号" :rules="[{ required: true, message: '请输入手机号' }]">
              <MainlandPhoneInput
                :value="smsLoginForm.phone" @update:value="renderHandlers.setPhone"
                v-bind="fieldAttrs('phone')"
                autocomplete="tel-national"
                aria-required="true"
                size="large"
              />
            </a-form-item>
            <a-form-item name="smsCode" :help="fieldHelp('smsCode')" label="验证码" :rules="[{ required: true, message: '请输入验证码' }]">
              <div class="sms-code-row">
                <a-input
                  :value="smsLoginForm.smsCode" @update:value="renderHandlers.setSMSCode"
                    v-bind="fieldAttrs('smsCode')"
                    autocomplete="one-time-code"
                    aria-required="true"
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
                  :loading="sendingLoginCode"
                  :disabled="sendingLoginCode || smsCooldownActive"
                  @click="renderHandlers.sendLoginCode"
                >
                  {{ smsCooldownText }}
                </a-button>
              </div>
            </a-form-item>
          </template>

          <template v-if="loginMode === 'wechat'">
            <div class="wechat-login-panel">
              <a-radio-group
                v-if="publicAuthSettings.wechatScanLoginEnabled && publicAuthSettings.wechatCodeLoginEnabled"
                :value="wechatLoginSubMode" @update:value="renderHandlers.setWechatMode"
                button-style="solid"
                class="wechat-mode-switch"
                aria-label="微信登录方式"
              >
                <a-radio-button value="scan">微信扫描</a-radio-button>
                <a-radio-button value="code">微信验证码</a-radio-button>
              </a-radio-group>

              <template v-if="wechatLoginSubMode === 'scan'">
                <div class="wechat-qrcode-frame">
                  <a-spin v-if="wechatQrLoading" />
                  <img v-else-if="wechatQrCodeImageData" :src="wechatQrCodeImageData" alt="微信登录二维码" />
                  <div v-else class="wechat-qrcode-empty">点击下方按钮获取二维码</div>
                </div>
                <div class="wechat-status">
                  <span role="status" aria-live="polite" aria-atomic="true">{{ wechatLoginStatusText }}</span>
                  <span v-if="wechatRemainingSeconds !== null">，剩余 {{ wechatRemainingSeconds }} 秒</span>
                  <span v-if="wechatTransportText">，{{ wechatTransportText }}</span>
                </div>
                <section v-if="wechatQrFailure" data-login-wechat-feedback>
                  <RequestFailureAlert :key="wechatQrFailureContextKey" :failure="wechatQrFailure" :message="wechatQrFailureText" :busy="wechatQrLoading" :context-key="wechatQrFailureContextKey" :is-current="isCurrentWechatQrFailure" />
                </section>
                <a-button block :loading="wechatQrLoading" @click="renderHandlers.startWechatLogin">刷新二维码</a-button>
              </template>

              <template v-else>
                <div class="wechat-qrcode-frame">
                  <img v-if="publicAuthSettings.wechatCodeStaticQrcodeUrl" :src="publicAuthSettings.wechatCodeStaticQrcodeUrl" alt="微信验证码二维码" />
                  <div v-else class="wechat-qrcode-empty">请联系管理员配置验证码二维码</div>
                </div>
                <div class="wechat-status">扫码关注后发送“验证码”，再输入收到的验证码登录</div>
                <a-form-item name="code" :help="fieldHelp('code')" label="微信验证码" :rules="[{ required: true, message: '请输入微信验证码' }]">
                  <a-input
                    :value="wechatCodeForm.code" @update:value="renderHandlers.setWechatCode"
                    v-bind="fieldAttrs('code')"
                    autocomplete="one-time-code"
                    aria-required="true"
                    placeholder="请输入微信验证码"
                    size="large"
                    inputmode="numeric"
                    :maxlength="12"
                  >
                    <template #prefix>
                      <SafetyCertificateOutlined style="color: rgba(0, 0, 0, 0.25)" />
                    </template>
                  </a-input>
                </a-form-item>
                <a-button
                  type="primary"
                  block
                  :loading="wechatCodeSubmitting"
                  :disabled="wechatCodeSubmitting || !wechatCodeForm.code || !publicAuthSettings.wechatCodeStaticQrcodeUrl"
                  @click="renderHandlers.submitWechatCodeLogin"
                >
                  微信验证码登录
                </a-button>
              </template>
            </div>
          </template>

          <a-form-item v-if="loginMode !== 'wechat'">
            <a-button
              type="primary"
              html-type="submit"
              :loading="submitting"
              :disabled="submitting || (loginMode === 'sms' ? !smsLoginForm.phone || !smsLoginForm.smsCode : !loginForm.username || !loginForm.password)"
              class="submit-button"
              block
            >
              登 录
            </a-button>
          </a-form-item>
        </a-form>

        <div class="auth-actions">
          <div class="action-title">还没有账号？</div>
          <a-button class="secondary-button" block @click="renderHandlers.goToRegister">前往注册页</a-button>
        </div>
      </a-card>
    </div>
  </div>
</template>

<script>
import { computed, defineComponent, getCurrentInstance, h, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, watch } from 'vue';
import { UserOutlined, LockOutlined, SafetyCertificateOutlined, EyeOutlined, EyeInvisibleOutlined } from '@ant-design/icons-vue';
import Swal from 'sweetalert2'
import {
  login,
  loginBySMS,
  getCurrentUser,
  getPublicRegisterStatus,
  sendPublicSMSCode,
  createCaptchaChallenge,
  createWechatLoginQRCode,
  getWechatLoginStatus,
  loginWechatByCode,
  verifyCaptchaChallenge,
  verifyRemoteCaptcha,
} from '../../api/TikuComponentApi.js'
import { buildPublicSMSCodePayload, buildSMSLoginPayload } from '../../utils/phoneAuthHelpers.mjs'
import {
  ensurePhoneBeforeSMSCaptcha,
  normalizeSMSCodeInput,
} from '../../utils/captchaHelpers.mjs'
import MainlandPhoneInput from '../../components/MainlandPhoneInput.vue'
import AuthPageIntro from '../../components/AuthPageIntro.vue'
import { ensureAuthCaptchaToken } from '../../utils/captchaDialog.mjs'
import { captureCurrentUserLease, getCurrentUserLogoutEpoch, getStoredCurrentUser, setStoredCurrentUser, useCurrentUserDisplay, resolveHomePath } from '../../utils/currentUser.js'
import { useSmsCooldown } from '../../utils/smsCooldown.js'
import { applySiteName } from '../../utils/siteName.js'
import { createWechatQrStatusController } from '../../utils/wechatQrStatus.mjs'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import { message } from 'ant-design-vue';
import { useRoute, useRouter } from 'vue-router';

const CaptchaSceneLoginPassword = 'login_password'
const CaptchaSceneLoginSMS = 'login_sms'
const CaptchaSceneSMSSendLogin = 'sms_send_login'

function showRegisterDisabledAlert() {
  return Swal.fire({
    icon: 'warning',
    title: '未开启公共注册',
    text: '当前站点暂未开放公共注册，请联系管理员处理。',
    confirmButtonText: '我知道了',
    heightAuto: false,
    scrollbarPadding: false,
  })
}

function isUnregisteredPhoneLoginMessage(value) {
  const message = String(value || '').trim()
  return message.includes('手机号未绑定账号') || message.includes('手机号未注册')
}

export default defineComponent({
  name: 'AdapterLogin',
  setup() {
    const router = useRouter()
    const route = useRoute()
    const formPrefix = 'login-form-' + getCurrentInstance().uid
    const authRoot = ref(null)
    const renderEpoch = ref(0)
    const formKey = ref(0)
    const formName = computed(() => formPrefix + '-' + renderEpoch.value)
    const mountedPath = route.path
    const { currentUser } = useCurrentUserDisplay()
    let formAlive = true
    let inputEpoch = 0
    let internalInputChange = false
    let ownCommit = null
    let viewLease = captureCurrentUserLease()
    let wechatStatusController = null
    const attempts = new Map()
    const routeKey = () => JSON.stringify([route.fullPath, route.path, route.query, route.params, route.hash])
    const pageActive = () => formAlive && route.path === mountedPath
    const fieldErrors = reactive({})
    const submitting = ref(false)
    const sendingLoginCode = ref(false)
    const smsCooldown = useSmsCooldown()
    const wechatQrLoading = ref(false)
    const wechatQrCodeImageData = ref('')
    const wechatLoginToken = ref('')
    const wechatLoginStatusText = ref('请使用微信扫码登录')
    const wechatRemainingSeconds = ref(null)
    const wechatTransportText = ref('')
    const wechatLoginSubMode = ref('scan')
    const wechatCodeSubmitting = ref(false)
    const loginMode = ref('password')
    const currentUserFailure = shallowRef(null)
    const currentUserFailureContextKey = ref(0)
    const currentUserFailureText = '身份验证已通过，但当前用户信息尚未确认。请先核对登录状态，勿重复提交。'
    let currentUserFailureOwner = () => false
    const clearCurrentUserFailure = () => {
      currentUserFailure.value = null
      currentUserFailureOwner = () => false
      currentUserFailureContextKey.value += 1
    }
    const captureCurrentUserFailureOwner = owner => {
      const { render, route: ownerRoute, input } = owner
      const context = currentUserFailureContextKey.value
      return () => context === currentUserFailureContextKey.value && viewCurrent(render)
        && ownerRoute === routeKey() && input === inputEpoch
    }
    const showCurrentUserFailure = (failure, owns) => {
      if (!owns() || failure?.name === 'AbortError' || failure?.code === 'ERR_CANCELED') return
      currentUserFailureOwner = owns
      currentUserFailure.value = ensureRequestFailure(failure)
    }
    const isCurrentCurrentUserFailure = (failure, context) => currentUserFailureOwner() && Boolean(failure)
      && failure === currentUserFailure.value && context === currentUserFailureContextKey.value
    const wechatQrFailure = shallowRef(null)
    const wechatQrFailureContextKey = ref(0)
    const wechatQrFailureText = ref('')
    let wechatQrFailureOwner = () => false
    const clearWechatQrFailure = () => {
      wechatQrFailure.value = null
      wechatQrFailureText.value = ''
      wechatQrFailureOwner = () => false
      wechatQrFailureContextKey.value += 1
    }
    const captureWechatQrFailureOwner = owner => {
      const { render, route, input } = owner
      const context = wechatQrFailureContextKey.value
      // A feedback view outlives the failed attempt, never its released request lease.
      return () => context === wechatQrFailureContextKey.value && viewCurrent(render)
        && route === routeKey() && input === inputEpoch
        && loginMode.value === 'wechat' && wechatLoginSubMode.value === 'scan'
    }
    const showWechatQrFailure = (failure, owns, fallback) => {
      if (!owns() || failure?.name === 'AbortError' || failure?.code === 'ERR_CANCELED') return
      wechatQrFailureOwner = owns
      wechatQrFailureText.value = publicFailureText(failure, fallback)
      wechatQrFailure.value = ensureRequestFailure(failure)
    }
    const isCurrentWechatQrFailure = (failure, context) => wechatQrFailureOwner() && Boolean(failure)
      && failure === wechatQrFailure.value && context === wechatQrFailureContextKey.value
    const publicAuthSettings = ref({
      siteName: '题库',
      registerEnabled: false,
      smsLoginEnabled: false,
      wechatLoginEnabled: false,
      wechatScanLoginEnabled: false,
      wechatCodeLoginEnabled: false,
      wechatDefaultLoginMode: 'scan',
      wechatCodeStaticQrcodeUrl: '',
      alternativeLoginEnabled: false,
      captchaProvider: 'remote',
      captchaApiBaseUrl: 'https://captcha.api.zelly.cn',
      captchaType: 'RANDOM',
    })
    const loginForm = reactive({
      username: String(route.query.username || ''),
      password: '',
      captchaToken: '',
    })
    const smsLoginForm = reactive({
      phone: '',
      smsCode: '',
      captchaToken: '',
    })
    const wechatCodeForm = reactive({
      code: '',
    })
    const activeLoginForm = computed(() => {
      if (loginMode.value === 'sms') {
        return smsLoginForm
      }
      if (loginMode.value === 'wechat' && wechatLoginSubMode.value === 'code') {
        return wechatCodeForm
      }
      return loginForm
    })

    const fieldKey = name => Array.isArray(name) ? name.join('_') : String(name || '')
    const fieldId = name => formName.value + '_' + fieldKey(name)
    const fieldAttrs = name => ({
      id: fieldId(name),
      'aria-invalid': fieldErrors[name] ? 'true' : undefined,
      'aria-describedby': fieldErrors[name] ? fieldId(name) + '-error' : undefined,
    })
    const fieldHelp = name => fieldErrors[name]
      ? h('span', { id: fieldId(name) + '-error' }, fieldErrors[name])
      : undefined
    const handleFieldValidation = (name, valid, errors) => {
      const key = fieldKey(name)
      if (!formAlive || !Object.prototype.hasOwnProperty.call(activeLoginForm.value, key)) return
      if (valid || !errors?.length) delete fieldErrors[key]
      else fieldErrors[key] = String(errors[0])
    }
    // Freshness only: anonymous/no-id/ordinary users remain eligible, and
    // unavailable storage must not block an initial public login request.
    const closeAttempt = (owner) => {
      if (!owner) return
      owner.controller.abort()
      owner.lease.release()
      if (attempts.get(owner.kind) !== owner) return
      attempts.delete(owner.kind)
      if (owner.kind === 'login') submitting.value = false
      if (owner.kind === 'sms-send') sendingLoginCode.value = false
      if (owner.kind === 'wechat-code') wechatCodeSubmitting.value = false
      if (owner.kind === 'wechat-scan') wechatQrLoading.value = false
    }
    const resetWechatScanState = () => {
      clearWechatQrFailure()
      wechatStatusController?.stop()
      wechatStatusController = null
      // The scan image encodes a private login capability; scrub retained DOM
      // synchronously, not only when Vue eventually removes this mode.
      authRoot.value?.querySelectorAll('img[alt="微信登录二维码"]').forEach(image => image.removeAttribute('src'))
      wechatQrCodeImageData.value = ''
      wechatLoginToken.value = ''
      wechatRemainingSeconds.value = null
      wechatTransportText.value = ''
    }
    const clearPrivateInputs = () => {
      internalInputChange = true
      try {
        loginForm.username = ''
        loginForm.password = ''
        loginForm.captchaToken = ''
        smsLoginForm.phone = ''
        smsLoginForm.smsCode = ''
        smsLoginForm.captchaToken = ''
        wechatCodeForm.code = ''
        // Scrub native nodes synchronously. Do not dispatch synthetic input:
        // Antd queues input-ref work which would outlive a real unmount.
        authRoot.value?.querySelectorAll('input:not([type="radio"]):not([type="checkbox"]), textarea').forEach((input) => {
          input.value = ''
          input.defaultValue = ''
          input.removeAttribute('value')
        })
      } finally {
        internalInputChange = false
      }
      Object.keys(fieldErrors).forEach(key => { delete fieldErrors[key] })
    }
    const retireView = () => {
      clearCurrentUserFailure()
      renderEpoch.value += 1
      const retiredRender = renderEpoch.value
      // First let controlled empty values drain Antd's accepted state, then
      // dispose the form. Authority retires now, not at this queued repaint.
      nextTick(() => {
        if (formAlive && renderEpoch.value === retiredRender) formKey.value = retiredRender
      })
      for (const owner of [...attempts.values()]) closeAttempt(owner)
      resetWechatScanState()
      clearPrivateInputs()
      viewLease?.release()
      viewLease = pageActive() ? captureCurrentUserLease() : null
    }
    const viewCurrent = (epoch = renderEpoch.value) => {
      if (!pageActive() || epoch !== renderEpoch.value) return false
      if (!viewLease || viewLease.isRetired()) {
        retireView()
        return false
      }
      return true
    }
    const isCurrent = (owner) => {
      if (!owner || attempts.get(owner.kind) !== owner ||
        owner.render !== renderEpoch.value || owner.route !== routeKey() ||
        (owner.inputBound && owner.input !== inputEpoch) || !pageActive()) return false
      if (owner.lease.isRetired()) {
        retireView()
        return false
      }
      return true
    }
    const beginAttempt = (kind, inputBound = true) => {
      if (!viewCurrent() || attempts.has(kind)) return null
      if (['login', 'wechat-code', 'wechat-scan'].includes(kind)) clearCurrentUserFailure()
      const owner = {
        kind, inputBound, input: inputEpoch, render: renderEpoch.value,
        route: routeKey(), lease: captureCurrentUserLease(), controller: new AbortController(),
      }
      attempts.set(kind, owner)
      return owner
    }
    const unknownOutcomeText = '操作结果未确认，请先核对当前状态，勿直接重复提交。'
    // Only exact public reasons are admitted. Arbitrary upstream/transport text
    // may reflect a password, SMS code or login/bind capability.
    const publicReasons = new Map([
      ['请输入手机号', '请输入手机号'],
      ['请输入11位手机号', '请输入11位手机号'],
      ['手机号格式不正确', '请输入11位手机号'],
      ['请输入验证码', '请输入验证码'],
      ['请输入用户名和密码', '请输入用户名和密码'],
      ['用户名或密码错误', '用户名或密码错误'],
      ['该手机号未绑定账号，请先注册', '该手机号未绑定账号，请先注册'],
      ['用户已被禁用', '账号已被禁用，请联系管理员'],
      ['验证码错误或已过期', '验证码错误或已过期，请重新获取'],
      ['微信验证码已过期，请重新获取', '微信验证码已过期，请重新获取'],
      ['微信验证码无效，请重新获取', '微信验证码无效，请重新获取'],
      ['请先完成人机验证', '请先完成人机验证'],
      ['图形验证码错误', '图形验证码错误，请重新验证'],
      ['图形验证码校验失败', '图形验证码校验失败，请重新验证'],
      ['人机验证不存在或已过期', '人机验证已过期，请重新验证'],
      ['人机验证已过期，请重新验证', '人机验证已过期，请重新验证'],
      ['人机验证服务不可用', '人机验证服务暂不可用，请稍后再试'],
      ['远程验证码校验失败', '人机验证未通过，请重新验证'],
      ['发送过于频繁，请稍后再试', '请求过于频繁，请稍后再试'],
      ['短信验证码登录未开启', '短信登录未启用，请使用其他登录方式'],
      ['手机号功能未启用', '手机号功能未启用，请使用其他登录方式'],
      ['短信配置未完成', '短信服务尚未配置完成，请联系管理员'],
      ['微信登录未开启或配置未完成', '微信登录未启用或配置未完成，请使用其他登录方式'],
      ['微信扫码登录未开启或配置未完成', '微信扫码登录未启用或配置未完成，请使用其他登录方式'],
      ['微信验证码登录未开启或配置未完成', '微信验证码登录未启用或配置未完成，请使用其他登录方式'],
      ['微信二维码已过期，请刷新', '二维码已过期，请刷新'],
    ])
    const publicFailureText = (failure, fallback) => {
      if (failure?.code === 'UNKNOWN_OUTCOME') return unknownOutcomeText
      if (failure?.name === 'UserCancelledCaptchaError') return '已取消人机验证，尚未提交本次请求'
      const knownReason = publicReasons.get(failure?.message)
      if (knownReason) return knownReason
      const status = Number(failure?.status ?? failure?.response?.status)
      if (status === 429) return '请求过于频繁，请稍后再试'
      if (status === 401) return '登录凭据未通过验证，请核对后再试'
      if (status === 403) return '当前登录方式不可用，请联系管理员'
      return fallback
    }
    const reportError = (owner, error, fallback) => {
      if (!isCurrent(owner) || error?.name === 'AbortError' || error?.code === 'ERR_CANCELED') return
      const incompletePhase = {
        'current-user': '身份验证已通过，但当前用户信息尚未确认。请先核对登录状态，勿重复提交。',
        storage: '身份验证已通过，但本地登录状态保存未完成。请检查浏览器存储并核对登录状态，勿重复提交。',
        navigation: '身份验证已通过且本地状态已保存，但页面跳转未完成。请先核对登录状态，勿重复提交。',
      }[owner.phase]
      const text = error?.code === 'UNKNOWN_OUTCOME' ? unknownOutcomeText
        : incompletePhase || publicFailureText(error, fallback)
      if (incompletePhase || text === unknownOutcomeText) message.warning(text)
      else message.error(text)
    }
    const ensureCaptchaToken = (scene, targetForm, owner) => ensureAuthCaptchaToken({
      scene, targetForm,
      publicSettings: { ...publicAuthSettings.value },
      createCaptchaChallenge, verifyCaptchaChallenge, verifyRemoteCaptcha,
      signal: owner.controller.signal,
      isCurrent: () => isCurrent(owner),
    })

    const chooseWechatLoginSubMode = (settings = publicAuthSettings.value) => {
      if (!settings.wechatLoginEnabled) {
        return 'scan'
      }
      const defaultMode = settings.wechatDefaultLoginMode === 'code' ? 'code' : 'scan'
      if (defaultMode === 'code' && settings.wechatCodeLoginEnabled) {
        return 'code'
      }
      if (defaultMode === 'scan' && settings.wechatScanLoginEnabled) {
        return 'scan'
      }
      if (settings.wechatScanLoginEnabled) {
        return 'scan'
      }
      if (settings.wechatCodeLoginEnabled) {
        return 'code'
      }
      return 'scan'
    }

    const loadPublicAuthSettings = async (owner) => {
      if (!isCurrent(owner)) return null
      const res = await getPublicRegisterStatus()
      if (!isCurrent(owner)) return null
      if (res?.status === 200) {
        publicAuthSettings.value = {
          siteName: res?.data?.siteName || '题库',
          registerEnabled: !!res?.data?.registerEnabled,
          smsLoginEnabled: !!res?.data?.smsLoginEnabled,
          wechatLoginEnabled: !!res?.data?.wechatLoginEnabled,
          wechatScanLoginEnabled: !!res?.data?.wechatScanLoginEnabled,
          wechatCodeLoginEnabled: !!res?.data?.wechatCodeLoginEnabled,
          wechatDefaultLoginMode: res?.data?.wechatDefaultLoginMode || 'scan',
          wechatCodeStaticQrcodeUrl: res?.data?.wechatCodeStaticQrcodeUrl || '',
          alternativeLoginEnabled: !!res?.data?.alternativeLoginEnabled,
          captchaProvider: res?.data?.captchaProvider || 'remote',
          captchaApiBaseUrl: res?.data?.captchaApiBaseUrl || 'https://captcha.api.zelly.cn',
          captchaType: res?.data?.captchaType || 'RANDOM',
        }
        applySiteName(publicAuthSettings.value.siteName)
        wechatLoginSubMode.value = chooseWechatLoginSubMode(publicAuthSettings.value)
        if ((!publicAuthSettings.value.smsLoginEnabled && loginMode.value === 'sms') ||
          (!publicAuthSettings.value.wechatLoginEnabled && loginMode.value === 'wechat')) {
          loginMode.value = 'password'
        }
      }
      return res
    }

    const navigateOwned = async (owner, destination) => {
      if (!isCurrent(owner)) return
      for (const other of [...attempts.values()]) if (other !== owner) closeAttempt(other)
      resetWechatScanState()
      clearPrivateInputs()
      if (!isCurrent(owner)) return false
      const navigation = await router.push(destination)
      if (navigation instanceof Error) throw new Error('页面跳转未完成')
      return true
    }
    const finishLogin = async (owner) => {
      if (!isCurrent(owner)) return false
      owner.phase = 'current-user'
      clearCurrentUserFailure()
      const ownsFailure = captureCurrentUserFailureOwner(owner)
      let userRes
      try {
        userRes = await getCurrentUser({ errorFeedback: 'local' })
      } catch (error) {
        // Only this read Error may persist; generic auth/captcha failures never do.
        if (isCurrent(owner)) showCurrentUserFailure(error, ownsFailure)
        throw error
      }
      if (!isCurrent(owner)) return false
      if (userRes?.status !== 200) {
        showCurrentUserFailure(userRes, ownsFailure)
        message.warning('身份验证已通过，但当前用户信息尚未确认。请先核对登录状态，勿重复提交。')
        return false
      }
      // An own successful storage commit retires the old lease too. Transfer
      // only this exact observed value, never a different/re-entrant B write.
      owner.expectedUser = JSON.stringify(userRes.data)
      ownCommit = owner
      owner.phase = 'storage'
      try {
        setStoredCurrentUser(userRes.data)
      } finally {
        ownCommit = null
      }
      if (attempts.get(owner.kind) !== owner || !pageActive() ||
        owner.render !== renderEpoch.value || owner.route !== routeKey() ||
        JSON.stringify(getStoredCurrentUser()) !== owner.expectedUser) {
        retireView()
        return false
      }
      owner.lease.release()
      owner.lease = captureCurrentUserLease()
      viewLease?.release()
      viewLease = captureCurrentUserLease()
      if (!isCurrent(owner)) return false
      for (const other of [...attempts.values()]) if (other !== owner) closeAttempt(other)
      owner.phase = 'navigation'
      const navigated = await navigateOwned(owner, resolveHomePath(userRes.data))
      if (navigated && isCurrent(owner)) message.success('登录成功')
      return Boolean(navigated)
    }

    const handleLogin = async () => {
      if (!viewCurrent()) return
      if (loginMode.value === 'wechat') {
        if (wechatLoginSubMode.value === 'code') await submitWechatCodeLogin()
        else await startWechatLogin()
        return
      }
      const mode = loginMode.value
      if (mode === 'sms' && !publicAuthSettings.value.smsLoginEnabled) return
      const owner = beginAttempt('login')
      if (!owner) return
      submitting.value = true
      try {
        // Validate before CAPTCHA; no reactive credentials cross an await.
        const form = mode === 'sms'
          ? { phone: ensurePhoneBeforeSMSCaptcha(smsLoginForm.phone), smsCode: String(smsLoginForm.smsCode || '').trim(), captchaToken: '' }
          : { username: loginForm.username, password: loginForm.password, captchaToken: '' }
        if (mode === 'sms' && !form.smsCode) throw new Error('请输入验证码')
        if (mode !== 'sms' && (!form.username || !form.password)) throw new Error('请输入用户名和密码')
        if (!isCurrent(owner)) return
        owner.phase = 'captcha'
        await ensureCaptchaToken(mode === 'sms' ? CaptchaSceneLoginSMS : CaptchaSceneLoginPassword, form, owner)
        if (!isCurrent(owner)) return
        const payload = mode === 'sms' ? buildSMSLoginPayload(form) : { ...form }
        owner.phase = 'auth'
        const res = await (mode === 'sms' ? loginBySMS(payload) : login(payload))
        if (!isCurrent(owner)) return
        if (res?.status === 200) await finishLogin(owner)
        else if (mode === 'sms' && isUnregisteredPhoneLoginMessage(res?.message)) await showUnregisteredPhoneAlert(owner)
        else message.error(publicFailureText(res, '登录失败，请核对账号信息后重试。'))
      } catch (error) {
        if (isCurrent(owner) && mode === 'sms' && isUnregisteredPhoneLoginMessage(error?.response?.data?.message)) await showUnregisteredPhoneAlert(owner)
        else reportError(owner, error, owner.phase === 'captcha' ? '人机验证未完成，请重新验证' : owner.phase === 'auth' ? unknownOutcomeText : '登录失败，请核对账号信息后重试。')
      } finally {
        closeAttempt(owner)
      }
    }

    const handleWechatLoginResult = async (res, owner) => {
      if (!isCurrent(owner)) return false
      const status = res?.data?.status
      if (status === 'success') {
        return finishLogin(owner)
      }
      if (status === 'need_bind') {
        if (!res?.data?.bindToken) return false
        return navigateOwned(owner, { path: '/wechat-bind', query: { bindToken: res.data.bindToken } })
      }
      return false
    }

    const resolveWechatLoginStatus = async (payload, owner, token) => {
      if (!isCurrent(owner) || owner.resolving) return false
      owner.resolving = true
      let completed = false
      try {
        if ((payload?.status === 'success' && payload?.jwt) ||
          (payload?.status === 'need_bind' && payload?.bindToken)) {
          completed = await handleWechatLoginResult({ data: payload }, owner)
          return completed
        }
        if (!isCurrent(owner)) return false
        const res = await getWechatLoginStatus({ loginToken: token })
        if (!isCurrent(owner)) return false
        if (res?.status !== 200) {
          wechatLoginStatusText.value = publicFailureText(res, '微信登录状态未确认，请先核对，勿直接重复登录。')
          return false
        }
        completed = await handleWechatLoginResult(res, owner)
        return completed
      } catch (error) {
        reportError(owner, error, '微信登录状态未确认，请先核对，勿直接重复登录。')
        return false
      } finally {
        owner.resolving = false
        if (completed) closeAttempt(owner)
      }
    }

    const startWechatLogin = async () => {
      if (!viewCurrent() || loginMode.value !== 'wechat' || wechatLoginSubMode.value !== 'scan' ||
        !publicAuthSettings.value.wechatLoginEnabled || !publicAuthSettings.value.wechatScanLoginEnabled ||
        wechatQrLoading.value) return
      closeAttempt(attempts.get('wechat-scan'))
      resetWechatScanState()
      const owner = beginAttempt('wechat-scan')
      if (!owner) return
      const ownsFeedback = captureWechatQrFailureOwner(owner)
      wechatQrLoading.value = true
      wechatLoginStatusText.value = '正在创建二维码'
      try {
        if (!isCurrent(owner)) return
        const res = await createWechatLoginQRCode({ errorFeedback: 'local' })
        if (!isCurrent(owner)) return
        if (res?.status !== 200) {
          wechatLoginStatusText.value = publicFailureText(res, '二维码创建未完成，请核对当前状态后再操作。')
          message.error(wechatLoginStatusText.value)
          showWechatQrFailure(res, ownsFeedback, wechatLoginStatusText.value)
          closeAttempt(owner)
          return
        }
        const token = String(res?.data?.loginToken || '')
        const image = res?.data?.qrcodeImageData || ''
        if (!image || !token) throw new Error('二维码响应不完整')
        wechatQrCodeImageData.value = image
        wechatLoginToken.value = token
        wechatLoginStatusText.value = '请使用微信扫码确认登录'
        // Native socket/poll callbacks close over this exact owner and token.
        wechatStatusController = createWechatQrStatusController({
          checkStatus: async () => {
            if (!isCurrent(owner)) return null
            const response = await getWechatLoginStatus({ loginToken: token })
            return isCurrent(owner) ? response : null
          },
          onTransport: transport => {
            if (isCurrent(owner)) wechatTransportText.value = transport === 'websocket' ? '实时连接' : '轮询中'
          },
          onRemaining: seconds => {
            if (isCurrent(owner)) wechatRemainingSeconds.value = seconds
          },
          onExpired: () => {
            if (!isCurrent(owner)) return
            wechatLoginStatusText.value = '二维码已过期，请刷新'
            wechatLoginToken.value = ''
            closeAttempt(owner)
          },
          onError: () => {
            if (isCurrent(owner)) wechatLoginStatusText.value = '微信登录状态未确认，正在重试；勿直接重复登录。'
          },
          onStatus: async (status, payload) => {
            if (!isCurrent(owner)) return
            if (status === 'pending') wechatLoginStatusText.value = '请使用微信扫码确认登录'
            else if (status === 'scanned') wechatLoginStatusText.value = '已扫描，请在微信中确认'
            else if (status === 'success' || status === 'need_bind') return resolveWechatLoginStatus(payload, owner, token)
          },
        })
        if (isCurrent(owner)) wechatStatusController.start({
          loginToken: token, expireSeconds: res?.data?.expireSeconds, expiresAtUnix: res?.data?.expiresAtUnix,
        })
      } catch (error) {
        if (isCurrent(owner)) {
          wechatLoginStatusText.value = publicFailureText(error, '二维码创建结果未确认，请先核对，勿直接重复操作。')
          reportError(owner, error, '二维码创建结果未确认，请先核对，勿直接重复操作。')
          showWechatQrFailure(error, ownsFeedback, wechatLoginStatusText.value)
        }
        closeAttempt(owner)
      } finally {
        if (isCurrent(owner)) wechatQrLoading.value = false
      }
    }

    const submitWechatCodeLogin = async () => {
      if (!viewCurrent() || loginMode.value !== 'wechat' || wechatLoginSubMode.value !== 'code' ||
        !publicAuthSettings.value.wechatLoginEnabled || !publicAuthSettings.value.wechatCodeLoginEnabled ||
        !publicAuthSettings.value.wechatCodeStaticQrcodeUrl) return
      if (!wechatCodeForm.code) {
        message.warning('请输入微信验证码')
        return
      }
      const owner = beginAttempt('wechat-code')
      if (!owner) return
      const payload = { code: wechatCodeForm.code }
      wechatCodeSubmitting.value = true
      try {
        if (!isCurrent(owner)) return
        owner.phase = 'auth'
        const res = await loginWechatByCode(payload)
        if (!isCurrent(owner)) return
        if (res?.status !== 200) {
          message.error(publicFailureText(res, '微信验证码登录未完成，请核对验证码后再操作。'))
          return
        }
        const handled = await handleWechatLoginResult(res, owner)
        if (!handled && isCurrent(owner)) message.error(publicFailureText(res, '微信验证码登录未完成，请核对验证码后再操作。'))
      } catch (error) {
        reportError(owner, error, unknownOutcomeText)
      } finally {
        closeAttempt(owner)
      }
    }

    const handleWechatSubModeChange = () => {
      if (!viewCurrent()) return
      if (wechatLoginSubMode.value === 'scan') void startWechatLogin()
      else wechatLoginStatusText.value = '请输入微信验证码'
    }
    const handleLoginModeChange = () => {
      if (!viewCurrent() || loginMode.value !== 'wechat') return
      const nextMode = chooseWechatLoginSubMode(publicAuthSettings.value)
      if (wechatLoginSubMode.value !== nextMode) wechatLoginSubMode.value = nextMode
      else handleWechatSubModeChange()
    }

    const sendLoginCode = async () => {
      if (!viewCurrent() || loginMode.value !== 'sms' || !publicAuthSettings.value.smsLoginEnabled || smsCooldown.active.value) return
      const owner = beginAttempt('sms-send')
      if (!owner) return
      sendingLoginCode.value = true
      try {
        const form = { phone: ensurePhoneBeforeSMSCaptcha(smsLoginForm.phone), captchaToken: '' }
        if (!isCurrent(owner)) return
        owner.phase = 'captcha'
        await ensureCaptchaToken(CaptchaSceneSMSSendLogin, form, owner)
        if (!isCurrent(owner)) return
        const payload = buildPublicSMSCodePayload(form, 'login')
        owner.phase = 'sms-send'
        const res = await sendPublicSMSCode(payload)
        if (!isCurrent(owner)) return
        if (res?.status === 200) {
          message.success('发送成功，请注意查收')
          smsCooldown.start()
        } else if (res?.status === 202) {
          message.warning('短信发送结果待核对，请先等待短信，勿重复发送')
          smsCooldown.start()
        } else message.error(publicFailureText(res, '短信发送未完成，请先核对接收情况后再操作。'))
      } catch (error) {
        reportError(owner, error, owner.phase === 'sms-send' ? unknownOutcomeText : '人机验证未完成，请重新验证')
      } finally {
        closeAttempt(owner)
      }
    }

    const goToRegister = async () => {
      const owner = beginAttempt('register', false)
      if (!owner) return
      try {
        if (publicAuthSettings.value.registerEnabled) {
          await navigateOwned(owner, '/register')
          return
        }
        const res = await loadPublicAuthSettings(owner)
        if (!isCurrent(owner)) return
        if (res?.status === 200 && publicAuthSettings.value.registerEnabled) await navigateOwned(owner, '/register')
        else await showRegisterDisabledAlert()
      } catch (error) {
        if (isCurrent(owner) && error?.name !== 'AbortError') await showRegisterDisabledAlert()
      } finally {
        closeAttempt(owner)
      }
    }

    const showUnregisteredPhoneAlert = async (owner) => {
      if (!isCurrent(owner)) return null
      let popup
      const closeOwnPopup = () => {
        if (popup && Swal.getPopup() === popup) Swal.close()
      }
      owner.controller.signal.addEventListener('abort', closeOwnPopup, { once: true })
      try {
        const result = await Swal.fire({
        icon: 'warning',
        title: '手机号未注册',
        text: '该手机号未绑定账号，请先注册。',
        confirmButtonText: '前往注册',
        showCancelButton: true,
        cancelButtonText: '取消',
        heightAuto: false,
        scrollbarPadding: false,
          didOpen: element => { popup = element; if (!isCurrent(owner)) closeOwnPopup() },
        })
        if (result?.isConfirmed && isCurrent(owner)) return goToRegister()
        return null
      } finally {
        owner.controller.signal.removeEventListener('abort', closeOwnPopup)
      }
    }

    const handleFinishFailed = (errors) => {
      if (!viewCurrent()) return
      const epoch = renderEpoch.value
      const location = routeKey()
      const fields = Array.isArray(errors?.errorFields) ? errors.errorFields : []
      fields.forEach(field => handleFieldValidation(field.name, false, field.errors))
      const first = fields[0]?.name
      const id = first ? fieldId(first) : ''
      if (id) nextTick(() => {
        if (viewCurrent(epoch) && location === routeKey()) document.getElementById(id)?.focus()
      })
    }

    const normalizeWechatCodeInput = value => String(value || '').replace(/\D/g, '').slice(0, 12)
    const renderHandlers = computed(() => {
      // A still-draining old Antd instance must not receive new authority
      // through updated listener props before its keyed replacement mounts.
      const epoch = formKey.value
      const run = action => (...args) => {
        if (!internalInputChange && viewCurrent(epoch)) return action(...args)
      }
      return {
        handleLogin: run(handleLogin), handleFieldValidation: run(handleFieldValidation),
        handleFinishFailed: run(handleFinishFailed), sendLoginCode: run(sendLoginCode),
        startWechatLogin: run(startWechatLogin), submitWechatCodeLogin: run(submitWechatCodeLogin),
        goToRegister: run(goToRegister),
        setMode: run(value => { loginMode.value = value }),
        setWechatMode: run(value => { wechatLoginSubMode.value = value }),
        setUsername: run(value => { loginForm.username = value }),
        setPassword: run(value => { loginForm.password = value }),
        setPhone: run(value => { smsLoginForm.phone = value }),
        setSMSCode: run(value => { smsLoginForm.smsCode = normalizeSMSCodeInput(value) }),
        setWechatCode: run(value => { wechatCodeForm.code = normalizeWechatCodeInput(value) }),
      }
    })
    watch(() => [loginForm.username, loginForm.password, smsLoginForm.phone, smsLoginForm.smsCode, wechatCodeForm.code], () => {
      if (internalInputChange) return
      inputEpoch += 1
      clearCurrentUserFailure()
      for (const owner of [...attempts.values()]) if (owner.inputBound) closeAttempt(owner)
      resetWechatScanState()
    }, { flush: 'sync' })
    watch(loginMode, () => {
      retireView()
      handleLoginModeChange()
    }, { flush: 'sync' })
    watch(wechatLoginSubMode, () => {
      if (loginMode.value !== 'wechat') return
      retireView()
      handleWechatSubModeChange()
    }, { flush: 'sync' })
    watch(routeKey, retireView, { flush: 'sync' })
    let observedQrFeedbackLogoutEpoch = getCurrentUserLogoutEpoch()
    watch(() => [currentUser.value, getCurrentUserLogoutEpoch()], () => {
      const epoch = getCurrentUserLogoutEpoch()
      if (epoch !== observedQrFeedbackLogoutEpoch) { clearWechatQrFailure(); clearCurrentUserFailure() }
      observedQrFeedbackLogoutEpoch = epoch
    }, { flush: 'sync' })
    watch(currentUser, (user) => {
      if (ownCommit && !ownCommit.observedWrite && JSON.stringify(user) === ownCommit.expectedUser) {
        ownCommit.observedWrite = true
        return
      }
      retireView()
    }, { flush: 'sync' })

    onMounted(async () => {
      const owner = beginAttempt('settings', false)
      if (!owner) return
      try {
        await loadPublicAuthSettings(owner)
      } catch (_) {
        // Existing public settings defaults remain after a failed read.
      } finally {
        closeAttempt(owner)
      }
    })

    onBeforeUnmount(() => {
      formAlive = false
      retireView()
    })

    return {
      authRoot, renderEpoch, formKey, renderHandlers, formName, fieldAttrs, fieldHelp, handleFieldValidation,
      submitting,
      sendingLoginCode,
      smsCooldownText: smsCooldown.buttonText,
      smsCooldownActive: smsCooldown.active,
      startSmsCooldown: smsCooldown.start,
      currentUserFailure, currentUserFailureContextKey, currentUserFailureText, isCurrentCurrentUserFailure,
      wechatQrFailure, wechatQrFailureContextKey, wechatQrFailureText, isCurrentWechatQrFailure,
      wechatQrLoading,
      wechatQrCodeImageData,
      wechatLoginStatusText,
      wechatRemainingSeconds,
      wechatTransportText,
      wechatLoginSubMode,
      wechatCodeSubmitting,
      loginMode,
      publicAuthSettings,
      activeLoginForm,
      loginForm,
      smsLoginForm,
      wechatCodeForm,
      normalizeSMSCodeInput,
      normalizeWechatCodeInput,
      handleLogin,
      startWechatLogin,
      submitWechatCodeLogin,
      handleWechatSubModeChange,
      handleLoginModeChange,
      sendLoginCode,
      goToRegister,
      handleFinishFailed,
    }
  },
  components: {
    RequestFailureAlert,
    AuthPageIntro,
    UserOutlined,
    LockOutlined,
    SafetyCertificateOutlined,
    EyeOutlined,
    EyeInvisibleOutlined,
    MainlandPhoneInput,
  },
})
</script>

<style scoped>
.login-mode-switch {
  width: 100%;
  margin-bottom: 20px;
}

.wechat-login-panel {
  display: grid;
  gap: 14px;
  margin-bottom: 20px;
}

.auth-password-toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  cursor: pointer;
}
.auth-password-toggle:focus-visible {
  outline: var(--focus-outline-width, 2px) solid var(--focus, #0066CC);
  outline-offset: var(--focus-outline-offset, 2px);
}
</style>
