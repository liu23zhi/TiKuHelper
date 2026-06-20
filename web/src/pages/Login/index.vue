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

        <a-radio-group v-model:value="loginMode" button-style="solid" class="login-mode-switch">
          <a-radio-button value="password">账号密码登录</a-radio-button>
          <a-radio-button v-if="publicAuthSettings.smsLoginEnabled" value="sms">短信验证码登录</a-radio-button>
        </a-radio-group>

        <a-form
          layout="vertical"
          :model="loginMode === 'sms' ? smsLoginForm : loginForm"
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

          <template v-else>
            <a-form-item name="phone" label="手机号" :rules="[{ required: true, message: '请输入手机号' }]">
              <a-input
                v-model:value="smsLoginForm.phone"
                addonBefore="+86"
                placeholder="请输入绑定手机号"
                size="large"
                inputmode="numeric"
                :maxlength="11"
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

          <a-form-item>
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
import { defineComponent, onMounted, reactive, ref } from 'vue';
import { UserOutlined, LockOutlined, MobileOutlined, SafetyCertificateOutlined } from '@ant-design/icons-vue';
import Swal from 'sweetalert2'
import {
  login,
  loginBySMS,
  getCurrentUser,
  getPublicRegisterStatus,
  sendPublicSMSCode,
  createCaptchaChallenge,
  verifyCaptchaChallenge,
} from '../../api/TikuComponentApi.js'
import { buildPublicSMSCodePayload, buildSMSLoginPayload } from '../../utils/phoneAuthHelpers.mjs'
import {
  buildCaptchaChallengePayload,
  buildCaptchaVerifyPayload,
  ensurePhoneBeforeSMSCaptcha,
  normalizePhoneInput,
  normalizeSMSCodeInput,
} from '../../utils/captchaHelpers.mjs'
import { openCaptchaDialog } from '../../utils/captchaDialog.mjs'
import { setCookies } from '../../utils/cookies.js'
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
    const loginMode = ref('password')
    const publicAuthSettings = ref({ registerEnabled: false, smsLoginEnabled: false })
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

    const ensureCaptchaToken = async (scene, targetForm) => {
      const requestCaptchaChallenge = async () => {
        const challengeRes = await createCaptchaChallenge(buildCaptchaChallengePayload(scene))
        if (challengeRes?.status !== 200) {
          throw new Error(challengeRes?.message || '获取图形验证码失败')
        }
        return challengeRes
      }

      let challengeRes = await requestCaptchaChallenge()
      const refreshCaptchaChallenge = async () => {
        const refreshedChallenge = await requestCaptchaChallenge()
        challengeRes = refreshedChallenge
        return refreshedChallenge.data.imageData
      }

      const captchaCode = await openCaptchaDialog({
        imageData: challengeRes.data.imageData,
        refreshCaptchaChallenge,
      })

      if (!captchaCode) {
        throw new Error('请先完成人机验证')
      }

      const verifyRes = await verifyCaptchaChallenge(buildCaptchaVerifyPayload({
        challengeId: challengeRes.data.challengeId,
        scene,
        captchaCode,
      }))
      if (verifyRes?.status !== 200) {
        throw new Error(verifyRes?.message || '图形验证码校验失败')
      }
      targetForm.captchaToken = verifyRes?.data?.captchaToken || ''
      return targetForm.captchaToken
    }

    const loadPublicAuthSettings = () => {
      return getPublicRegisterStatus().then((res) => {
        if (res?.status === 200) {
          publicAuthSettings.value = {
            registerEnabled: !!res?.data?.registerEnabled,
            smsLoginEnabled: !!res?.data?.smsLoginEnabled,
          }
          if (!publicAuthSettings.value.smsLoginEnabled && loginMode.value === 'sms') {
            loginMode.value = 'password'
          }
        }
        return res
      })
    }

    const finishLogin = (jwt) => {
      setCookies('token', jwt)
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

    onMounted(() => {
      loadPublicAuthSettings()
    })

    return {
      submitting,
      sendingLoginCode,
      loginMode,
      publicAuthSettings,
      loginForm,
      smsLoginForm,
      normalizePhoneInput,
      normalizeSMSCodeInput,
      handleLogin,
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
