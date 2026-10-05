<template>
  <div class="auth-shell">
    <div class="auth-card auth-card--single">
      <div class="hero">
        <div class="hero-badge">Tiku Adapter</div>
        <h1>题库管理平台</h1>
        <p>
          搜题聚合题库平台，集成AI搜索，提供更准确的答案。
        </p>
      </div>

      <a-card :bordered="false" class="form-card">
        <div class="form-header">
          <div>
            <div class="form-title">用户登录</div>
            <div class="form-subtitle">使用已有账号登录获取更多功能。</div>
          </div>
        </div>

        <a-radio-group v-model:value="loginMode" button-style="solid" class="login-mode-switch" @change="handleLoginModeChange">
          <a-radio-button value="password">账号密码登录</a-radio-button>
          <a-radio-button v-if="publicAuthSettings.smsLoginEnabled" value="sms">短信验证码登录</a-radio-button>
          <a-radio-button v-if="publicAuthSettings.wechatLoginEnabled" value="wechat">微信登录</a-radio-button>
        </a-radio-group>

        <a-form
          layout="vertical"
          :model="activeLoginForm"
          @finish="handleLogin"
          @finishFailed="handleFinishFailed"
          class="form"
        >
          <a-form-item v-if="loginMode === 'password'" name="username" label="用户名" :rules="[{ required: true, message: '请输入用户名' }]">
            <a-input v-model:value="loginForm.username" placeholder="请输入用户名" size="large">
              <template #prefix>
                <UserOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
            </a-input>
          </a-form-item>
          <a-form-item v-if="loginMode === 'password'" name="password" label="密码" :rules="[{ required: true, message: '请输入密码' }]">
            <a-input-password v-model:value="loginForm.password" placeholder="请输入密码" size="large">
              <template #prefix>
                <LockOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
            </a-input-password>
          </a-form-item>

          <template v-if="loginMode === 'sms'">
            <a-form-item name="phone" label="手机号" :rules="[{ required: true, message: '请输入手机号' }]">
              <a-input
                v-model:value="smsLoginForm.phone"
                placeholder="请输入绑定手机号，国际号码请带 + 区号"
                size="large"
                inputmode="tel"
                :maxlength="20"
                @input="smsLoginForm.phone = normalizePhoneInput(smsLoginForm.phone)"
              >
                <template #prefix>
                  <MobileOutlined style="color: rgba(0, 0, 0, 0.25)" />
                </template>
              </a-input>
            </a-form-item>
            <a-form-item name="smsCode" label="验证码" :rules="[{ required: true, message: '请输入验证码' }]">
              <div class="sms-code-row">
                <a-input
                  v-model:value="smsLoginForm.smsCode"
                  placeholder="请输入验证码"
                  size="large"
                  inputmode="numeric"
                  :maxlength="6"
                  @input="smsLoginForm.smsCode = normalizeSMSCodeInput(smsLoginForm.smsCode)"
                >
                  <template #prefix>
                    <SafetyCertificateOutlined style="color: rgba(0, 0, 0, 0.25)" />
                  </template>
                </a-input>
                <a-button :loading="sendingLoginCode" @click="sendLoginCode">发送验证码</a-button>
              </div>
            </a-form-item>
          </template>

          <template v-if="loginMode === 'wechat'">
            <div class="wechat-login-panel">
              <a-radio-group
                v-if="publicAuthSettings.wechatScanLoginEnabled && publicAuthSettings.wechatCodeLoginEnabled"
                v-model:value="wechatLoginSubMode"
                button-style="solid"
                class="wechat-mode-switch"
                @change="handleWechatSubModeChange"
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
                <div class="wechat-status">{{ wechatLoginStatusText }}</div>
                <a-button block :loading="wechatQrLoading" @click="startWechatLogin">刷新二维码</a-button>
              </template>

              <template v-else>
                <div class="wechat-qrcode-frame">
                  <img v-if="publicAuthSettings.wechatCodeStaticQrcodeUrl" :src="publicAuthSettings.wechatCodeStaticQrcodeUrl" alt="微信验证码二维码" />
                  <div v-else class="wechat-qrcode-empty">请联系管理员配置验证码二维码</div>
                </div>
                <div class="wechat-status">扫码关注后发送“验证码”，再输入收到的验证码登录</div>
                <a-form-item name="wechatCode" label="微信验证码" :rules="[{ required: true, message: '请输入微信验证码' }]">
                  <a-input
                    v-model:value="wechatCodeForm.code"
                    placeholder="请输入微信验证码"
                    size="large"
                    inputmode="numeric"
                    :maxlength="12"
                    @input="wechatCodeForm.code = normalizeWechatCodeInput(wechatCodeForm.code)"
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
                  :disabled="!wechatCodeForm.code || !publicAuthSettings.wechatCodeStaticQrcodeUrl"
                  @click="submitWechatCodeLogin"
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
              :disabled="loginMode === 'sms' ? !smsLoginForm.phone || !smsLoginForm.smsCode : !loginForm.username || !loginForm.password"
              class="submit-button"
              block
            >
              登 录
            </a-button>
          </a-form-item>
        </a-form>

        <div class="auth-actions">
          <div class="action-title">还没有账号？</div>
          <a-button class="secondary-button" block @click="goToRegister">前往注册页</a-button>
        </div>
      </a-card>
    </div>
  </div>
</template>

<script>
import { computed, defineComponent, onBeforeUnmount, onMounted, reactive, ref } from 'vue';
import { UserOutlined, LockOutlined, MobileOutlined, SafetyCertificateOutlined } from '@ant-design/icons-vue';
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
  normalizePhoneInput,
  normalizeSMSCodeInput,
} from '../../utils/captchaHelpers.mjs'
import { ensureAuthCaptchaToken } from '../../utils/captchaDialog.mjs'
import { setStoredCurrentUser, resolveHomePath } from '../../utils/currentUser.js'
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

export default defineComponent({
  name: 'AdapterLogin',
  setup() {
    const router = useRouter()
    const route = useRoute()
    const submitting = ref(false)
    const sendingLoginCode = ref(false)
    const wechatQrLoading = ref(false)
    const wechatQrCodeImageData = ref('')
    const wechatLoginToken = ref('')
    const wechatLoginStatusText = ref('请使用微信扫码登录')
    const wechatPollTimer = ref(null)
    const wechatLoginSubMode = ref('scan')
    const wechatCodeSubmitting = ref(false)
    const loginMode = ref('password')
    const publicAuthSettings = ref({
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

    const ensureCaptchaToken = (scene, targetForm) => ensureAuthCaptchaToken({
      scene,
      targetForm,
      publicSettings: publicAuthSettings.value,
      createCaptchaChallenge,
      verifyCaptchaChallenge,
      verifyRemoteCaptcha,
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

    const loadPublicAuthSettings = () => {
      return getPublicRegisterStatus().then((res) => {
        if (res?.status === 200) {
          publicAuthSettings.value = {
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
          wechatLoginSubMode.value = chooseWechatLoginSubMode(publicAuthSettings.value)
          if (!publicAuthSettings.value.smsLoginEnabled && loginMode.value === 'sms') {
            loginMode.value = 'password'
          }
          if (!publicAuthSettings.value.wechatLoginEnabled && loginMode.value === 'wechat') {
            loginMode.value = 'password'
          }
        }
        return res
      })
    }

    const finishLogin = () => {
      return getCurrentUser().then((userRes) => {
        if (userRes?.status === 200) {
          setStoredCurrentUser(userRes.data)
          message.success('登录成功')
          router.push(resolveHomePath(userRes.data))
          return
        }
        throw new Error(userRes?.message || '获取当前用户失败')
      })
    }

    const handleLogin = async () => {
      if (loginMode.value === 'wechat') {
        await startWechatLogin()
        return
      }
      submitting.value = true
      try {
        if (loginMode.value === 'sms') {
          await ensureCaptchaToken(CaptchaSceneLoginSMS, smsLoginForm)
        } else {
          await ensureCaptchaToken(CaptchaSceneLoginPassword, loginForm)
        }
      } catch (error) {
        submitting.value = false
        message.error(error?.message || '请先完成人机验证')
        return
      }

      let requester
      try {
        requester = loginMode.value === 'sms'
          ? loginBySMS(buildSMSLoginPayload(smsLoginForm))
          : login(loginForm)
      } catch (error) {
        submitting.value = false
        message.error(error?.message || '登录失败')
        return
      }

      requester.then((res) => {
        if (res?.status === 200) {
          return finishLogin(res?.data?.jwt)
        }
        message.error(res?.message || '登录失败')
        return null
      }).catch((error) => {
        console.log(error)
        message.error(error?.message || '登录失败')
      }).finally(() => {
        submitting.value = false
      })
    }

    const stopWechatPolling = () => {
      if (wechatPollTimer.value) {
        clearInterval(wechatPollTimer.value)
        wechatPollTimer.value = null
      }
    }

    const resetWechatScanState = () => {
      stopWechatPolling()
      wechatQrCodeImageData.value = ''
      wechatLoginToken.value = ''
    }

    const handleWechatLoginResult = async (res) => {
      const status = res?.data?.status
      if (status === 'success') {
        await finishLogin(res?.data?.jwt)
        return true
      }
      if (status === 'need_bind') {
        router.push({ path: '/wechat-bind', query: { bindToken: res?.data?.bindToken || '' } })
        return true
      }
      return false
    }

    const pollWechatLoginStatus = async () => {
      if (!wechatLoginToken.value) {
        return
      }
      const res = await getWechatLoginStatus({ loginToken: wechatLoginToken.value })
      if (res?.status !== 200) {
        wechatLoginStatusText.value = res?.message || '二维码已过期，请刷新'
        stopWechatPolling()
        return
      }
      const status = res?.data?.status
      if (status === 'success') {
        stopWechatPolling()
        wechatLoginStatusText.value = '扫码成功，正在登录'
        await handleWechatLoginResult(res)
        return
      }
      if (status === 'need_bind') {
        stopWechatPolling()
        await handleWechatLoginResult(res)
        return
      }
      wechatLoginStatusText.value = '等待扫码确认'
    }

    const startWechatLogin = async () => {
      if (wechatLoginSubMode.value !== 'scan' || !publicAuthSettings.value.wechatScanLoginEnabled) {
        return
      }
      stopWechatPolling()
      wechatQrLoading.value = true
      wechatLoginStatusText.value = '正在创建二维码'
      try {
        const res = await createWechatLoginQRCode()
        if (res?.status !== 200) {
          throw new Error(res?.message || '二维码创建失败，请稍后重试')
        }
        wechatQrCodeImageData.value = res?.data?.qrcodeImageData || ''
        wechatLoginToken.value = res?.data?.loginToken || ''
        if (!wechatQrCodeImageData.value || !wechatLoginToken.value) {
          throw new Error('二维码创建失败，请稍后重试')
        }
        wechatLoginStatusText.value = '请使用微信扫码确认登录'
        wechatPollTimer.value = setInterval(() => {
          void pollWechatLoginStatus()
        }, 2000)
      } catch (error) {
        wechatLoginStatusText.value = error?.message || '二维码创建失败，请稍后重试'
        message.error(wechatLoginStatusText.value)
      } finally {
        wechatQrLoading.value = false
      }
    }

    const submitWechatCodeLogin = async () => {
      if (!wechatCodeForm.code) {
        message.warning('请输入微信验证码')
        return
      }
      wechatCodeSubmitting.value = true
      try {
        const res = await loginWechatByCode({ code: wechatCodeForm.code })
        if (res?.status !== 200) {
          message.error(res?.message || '微信验证码登录失败')
          return
        }
        const handled = await handleWechatLoginResult(res)
        if (!handled) {
          message.error(res?.message || '微信验证码登录失败')
        }
      } catch (error) {
        message.error(error?.message || '微信验证码登录失败')
      } finally {
        wechatCodeSubmitting.value = false
      }
    }

    const handleWechatSubModeChange = () => {
      if (wechatLoginSubMode.value === 'scan') {
        void startWechatLogin()
        return
      }
      resetWechatScanState()
      wechatLoginStatusText.value = '请输入微信验证码'
    }

    const handleLoginModeChange = () => {
      if (loginMode.value === 'wechat') {
        wechatLoginSubMode.value = chooseWechatLoginSubMode(publicAuthSettings.value)
        if (wechatLoginSubMode.value === 'scan') {
          void startWechatLogin()
        }
        return
      }
      resetWechatScanState()
    }

    const sendLoginCode = async () => {
      sendingLoginCode.value = true
      try {
        smsLoginForm.phone = ensurePhoneBeforeSMSCaptcha(smsLoginForm.phone)
        await ensureCaptchaToken(CaptchaSceneSMSSendLogin, smsLoginForm)
        const res = await sendPublicSMSCode(buildPublicSMSCodePayload(smsLoginForm, 'login'))
        if (res?.status === 200) {
          message.success(res?.message || '发送成功，请注意查收')
        } else {
          message.error(res?.message || '发送失败')
        }
      } catch (error) {
        message.error(error?.message || '发送失败')
      } finally {
        sendingLoginCode.value = false
      }
    }

    const goToRegister = () => {
      if (publicAuthSettings.value.registerEnabled) {
        router.push('/register')
        return
      }
      loadPublicAuthSettings().then((res) => {
        if (res?.status === 200 && publicAuthSettings.value.registerEnabled) {
          router.push('/register')
          return
        }
        return showRegisterDisabledAlert()
      }).catch(() => {
        return showRegisterDisabledAlert()
      })
    }

    const handleFinishFailed = (errors) => {
      console.log(errors)
    }

    const normalizeWechatCodeInput = (value) => String(value || '').replace(/\D/g, '').slice(0, 12)

    onMounted(() => {
      loadPublicAuthSettings().then(() => {
        if (loginMode.value === 'wechat' && wechatLoginSubMode.value === 'scan') {
          void startWechatLogin()
        }
      })
    })

    onBeforeUnmount(() => {
      stopWechatPolling()
    })

    return {
      submitting,
      sendingLoginCode,
      wechatQrLoading,
      wechatQrCodeImageData,
      wechatLoginStatusText,
      wechatLoginSubMode,
      wechatCodeSubmitting,
      loginMode,
      publicAuthSettings,
      activeLoginForm,
      loginForm,
      smsLoginForm,
      wechatCodeForm,
      normalizePhoneInput,
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
    UserOutlined,
    LockOutlined,
    MobileOutlined,
    SafetyCertificateOutlined,
  },
})
</script>

<style scoped>
.auth-shell {
  width: 100%;
  display: flex;
  justify-content: center;
  padding: 24px;
}

.auth-card {
  width: min(1080px, 100%);
  display: grid;
  grid-template-columns: minmax(280px, 1fr) minmax(360px, 460px);
  gap: 24px;
  align-items: stretch;
}

.auth-card--single {
  grid-template-columns: minmax(320px, 1fr) minmax(360px, 440px);
}

.hero {
  min-height: 520px;
  background: linear-gradient(135deg, #195ca3 0%, #2f80ed 52%, #6fb1fc 100%);
  border-radius: 28px;
  padding: 40px;
  color: #fff;
  display: flex;
  flex-direction: column;
  justify-content: center;
  box-shadow: 0 20px 45px rgba(25, 92, 163, 0.24);
}

.hero-badge {
  width: fit-content;
  padding: 6px 12px;
  border-radius: 999px;
  background: rgba(255, 255, 255, 0.18);
  font-size: 13px;
  margin-bottom: 18px;
}

.hero h1 {
  margin: 0;
  font-size: 40px;
  line-height: 1.2;
  color: #fff;
}

.hero p {
  margin: 18px 0 0;
  max-width: 460px;
  font-size: 15px;
  line-height: 1.9;
  color: rgba(255, 255, 255, 0.92);
}

.hero-alert {
  margin-top: 24px;
  border-radius: 14px;
}

.form-card {
  border-radius: 28px;
  box-shadow: 0 18px 40px rgba(16, 24, 40, 0.08);
}

.form-header {
  margin-bottom: 20px;
}

.form-title {
  font-size: 24px;
  font-weight: 700;
  color: #1f2937;
}

.form-subtitle {
  margin-top: 6px;
  color: #6b7280;
  font-size: 14px;
}

.form {
  margin-top: 8px;
}

.login-mode-switch {
  width: 100%;
  margin-bottom: 16px;
}

.sms-code-row {
  display: flex;
  gap: 8px;
}

.sms-code-row :deep(.ant-input-affix-wrapper) {
  flex: 1;
}

.wechat-login-panel {
  display: grid;
  gap: 14px;
  margin-bottom: 24px;
}

.wechat-qrcode-frame {
  width: 220px;
  height: 220px;
  margin: 0 auto;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px solid #e5e7eb;
  border-radius: 16px;
  background: #f8fafc;
  overflow: hidden;
}

.wechat-qrcode-frame img {
  width: 100%;
  height: 100%;
  object-fit: contain;
}

.wechat-qrcode-empty,
.wechat-status {
  text-align: center;
  color: #6b7280;
  font-size: 13px;
  line-height: 1.7;
}

.submit-button,
.secondary-button {
  height: 46px;
  border-radius: 12px;
  font-weight: 600;
}

.auth-actions {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid #f0f0f0;
}

.action-title {
  font-size: 15px;
  font-weight: 600;
  color: #1f2937;
  margin-bottom: 14px;
}

.action-subtitle {
  margin: 8px 0 14px;
  color: #6b7280;
  font-size: 13px;
  line-height: 1.8;
}

@media (max-width: 960px) {
  .auth-card,
  .auth-card--single {
    grid-template-columns: 1fr;
  }

  .hero {
    min-height: auto;
    padding: 28px;
  }

  .hero h1 {
    font-size: 32px;
  }
}
</style>
