<template>
  <div class="auth-shell">
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

      <a-card :bordered="false" class="form-card">
        <div class="form-header">
          <div>
            <h2 class="form-title">公开注册</h2>
            <div class="form-subtitle">注册后将创建普通用户账号。</div>
          </div>
        </div>

        <a-form
          layout="vertical"
          :model="registerForm"
          @finish="handleRegister"
          @finishFailed="handleFinishFailed"
          class="form"
        >
          <h3 class="form-section-heading">账号资料</h3>
          <a-form-item name="username" label="用户名" :rules="usernameRules">
            <a-input v-model:value="registerForm.username" placeholder="请输入用户名" size="large" @input="registerForm.username = limitUsernameInput(registerForm.username)">
              <template #prefix>
                <UserOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
            </a-input>
          </a-form-item>
          <a-form-item name="password" label="密码" :rules="[{ required: true, message: '请输入密码' }]">
            <a-input-password v-model:value="registerForm.password" placeholder="请输入密码" size="large">
              <template #prefix>
                <LockOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
            </a-input-password>
          </a-form-item>
          <a-form-item name="nickname" label="昵称">
            <a-input v-model:value="registerForm.nickname" placeholder="可选，便于后台识别" size="large">
              <template #prefix>
                <IdcardOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
            </a-input>
          </a-form-item>
          <h3 class="form-section-heading phone-section-heading">手机号验证</h3>
          <a-form-item name="phone" label="手机号" :rules="[{ required: true, message: '请输入手机号' }]">
            <MainlandPhoneInput
              v-model:value="registerForm.phone"
              size="large"
            />
          </a-form-item>
          <a-form-item name="smsCode" label="验证码" :rules="[{ required: true, message: '请输入验证码' }]">
            <div class="sms-code-row">
              <a-input
                v-model:value="registerForm.smsCode"
                placeholder="请输入验证码"
                size="large"
                inputmode="numeric"
                :maxlength="6"
                @input="registerForm.smsCode = normalizeSMSCodeInput(registerForm.smsCode)"
              >
                <template #prefix>
                  <SafetyCertificateOutlined style="color: rgba(0, 0, 0, 0.25)" />
                </template>
              </a-input>
              <a-button
                :loading="sendingCode"
                :disabled="sendingCode || smsCooldownActive"
                @click="sendRegisterCode"
              >
                {{ smsCooldownText }}
              </a-button>
            </div>
          </a-form-item>
          <div v-if="publicAuthSettings.wechatLoginEnabled" class="wechat-register-panel">
            <h3 class="form-section-heading">微信绑定（可选）</h3>
            <div v-if="!wechatBindingActive" class="wechat-register-intro">
              <span>注册时绑定微信，后续可使用微信登录。</span>
              <a-button type="default" @click="startWechatBinding">绑定微信</a-button>
            </div>
            <template v-else>
              <a-radio-group
                v-if="publicAuthSettings.wechatScanLoginEnabled && publicAuthSettings.wechatCodeLoginEnabled"
                v-model:value="wechatRegisterMode"
                button-style="solid"
                @change="handleWechatRegisterModeChange"
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
                <div class="wechat-status">{{ wechatRegisterStatusText }}</div>
                <a-button block :loading="wechatQrLoading" @click="refreshWechatRegisterQRCode(true)">刷新二维码</a-button>
              </template>
              <template v-else>
                <div class="wechat-qrcode-frame">
                  <img v-if="publicAuthSettings.wechatCodeStaticQrcodeUrl" :src="publicAuthSettings.wechatCodeStaticQrcodeUrl" alt="微信验证码二维码" />
                  <div v-else class="wechat-qrcode-empty">请联系管理员配置验证码二维码</div>
                </div>
                <div class="wechat-status">扫码关注后发送“验证码”，再输入收到的验证码</div>
                <a-input v-model:value="wechatRegisterCode" placeholder="请输入微信验证码" size="large" :maxlength="12" />
                <a-button
                  type="primary"
                  block
                  :loading="wechatCodeSubmitting"
                  :disabled="!wechatRegisterCode || !publicAuthSettings.wechatCodeStaticQrcodeUrl"
                  @click="submitWechatRegisterCode"
                >
                  确认微信验证码
                </a-button>
              </template>
              <div class="wechat-register-actions">
                <span v-if="wechatRegisterStaged" class="wechat-register-staged">微信已暂存，提交注册后完成绑定</span>
                <a-button type="link" @click="cancelWechatBinding">取消绑定</a-button>
              </div>
            </template>
          </div>
          <a-form-item>
            <a-button
              type="primary"
              html-type="submit"
              :loading="submitting"
              :disabled="!registerForm.username || !registerForm.password || !registerForm.phone || !registerForm.smsCode"
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
          <a-button class="secondary-button" block @click="goToLogin">返回登录页</a-button>
        </div>
      </a-card>
    </div>
  </div>
</template>

<script>
import { defineComponent, onBeforeUnmount, onMounted, reactive, ref } from 'vue';
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
import { useRouter } from 'vue-router';
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
    const submitting = ref(false)
    const sendingCode = ref(false)
    const smsCooldown = useSmsCooldown()
    const registerDisabledNotice = ref('')
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
    const registerForm = reactive({
      username: '',
      password: '',
      nickname: '',
      phone: '',
      smsCode: '',
      captchaToken: '',
    })
    const wechatBindingActive = ref(false)
    const wechatRegisterStaged = ref(false)
    const wechatRegisterMode = ref('scan')
    const wechatQrLoading = ref(false)
    const wechatQrCodeImageData = ref('')
    const wechatRegisterSessionId = ref('')
    const wechatRegisterCode = ref('')
    const wechatCodeSubmitting = ref(false)
    const wechatRegisterStatusText = ref('请使用微信扫码绑定')
    let wechatRegisterPollTimer = null
    const flowCoordinator = createWechatRegisterFlowCoordinator({
      onDuplicate: () => {
        wechatBindingActive.value = false
        wechatRegisterStaged.value = false
        wechatRegisterStatusText.value = '此注册流程已在其他标签页打开'
      },
    })
    const usernameRules = [
      { required: true, message: '请输入用户名' },
      {
        validator: (_rule, value) => {
          try {
            normalizeUsername(value)
            return Promise.resolve()
          } catch (error) {
            return Promise.reject(error)
          }
        },
      },
    ]

    const ensureCaptchaToken = (scene, targetForm) => ensureAuthCaptchaToken({
      scene,
      targetForm,
      publicSettings: publicAuthSettings.value,
      createCaptchaChallenge,
      verifyCaptchaChallenge,
      verifyRemoteCaptcha,
    })

    const loadPublicAuthSettings = () => {
      return getPublicRegisterStatus().then((res) => {
        if (res?.status === 200) {
          publicAuthSettings.value = {
            siteName: res?.data?.siteName || '题库',
            registerEnabled: !!res?.data?.registerEnabled,
            smsLoginEnabled: !!res?.data?.smsLoginEnabled,
            wechatLoginEnabled: !!res?.data?.wechatLoginEnabled,
            wechatScanLoginEnabled: !!res?.data?.wechatScanLoginEnabled,
            wechatCodeLoginEnabled: !!res?.data?.wechatCodeLoginEnabled,
            wechatDefaultLoginMode: res?.data?.wechatDefaultLoginMode === 'code' ? 'code' : 'scan',
            wechatCodeStaticQrcodeUrl: res?.data?.wechatCodeStaticQrcodeUrl || '',
            alternativeLoginEnabled: !!res?.data?.alternativeLoginEnabled,
            captchaProvider: res?.data?.captchaProvider || 'remote',
            captchaApiBaseUrl: res?.data?.captchaApiBaseUrl || 'https://captcha.api.zelly.cn',
            captchaType: res?.data?.captchaType || 'RANDOM',
          }
          applySiteName(publicAuthSettings.value.siteName)
        }
        return res
      })
    }

    const chooseWechatRegisterMode = () => {
      const settings = publicAuthSettings.value
      if (settings.wechatDefaultLoginMode === 'code' && settings.wechatCodeLoginEnabled) return 'code'
      if (settings.wechatScanLoginEnabled) return 'scan'
      return settings.wechatCodeLoginEnabled ? 'code' : 'scan'
    }

    const stopWechatRegisterPolling = () => {
      if (wechatRegisterPollTimer) {
        clearInterval(wechatRegisterPollTimer)
        wechatRegisterPollTimer = null
      }
    }

    const clearWechatRegisterFlow = async ({ release = false } = {}) => {
      stopWechatRegisterPolling()
      const token = flowCoordinator.token
      if (release && token) {
        try {
          await releaseWechatRegisterFlow({ registrationFlowToken: token })
        } catch (_error) {
          // The local token must still be cleared when the server flow expired.
        }
      }
      flowCoordinator.clear()
      wechatBindingActive.value = false
      wechatRegisterStaged.value = false
      wechatRegisterSessionId.value = ''
      wechatQrCodeImageData.value = ''
      wechatRegisterCode.value = ''
    }

    const flowErrorStatus = (error) => error?.response?.status || error?.status

    const isTerminalWechatRegisterStatus = (status) => status === 401 || status === 409 || status === 410

    const isWechatQRCodeError = (value) => {
      const message = String(value?.message || value?.response?.data?.message || value?.response?.data?.detail || value || '')
      return message.includes('微信二维码会话无效') || message.includes('微信二维码已过期')
    }

    const isWechatFlowError = (value) => {
      const message = String(value?.message || value?.response?.data?.message || value?.response?.data?.detail || value || '')
      return message.includes('注册流程令牌无效') || message.includes('注册流程已过期')
    }

    const markWechatQRCodeExpired = () => {
      stopWechatRegisterPolling()
      wechatRegisterSessionId.value = ''
      wechatQrCodeImageData.value = ''
      wechatRegisterStaged.value = false
      wechatRegisterStatusText.value = '二维码已过期，请刷新二维码'
    }

    const applyWechatRegisterSnapshot = (snapshot) => {
      const data = snapshot || {}
      if (data.status === 'expired') {
        markWechatQRCodeExpired()
        return
      }
      if (data.qrcode?.sessionId) {
        wechatRegisterSessionId.value = data.qrcode.sessionId
        wechatQrCodeImageData.value = data.qrcode.displayData || ''
      }
      wechatRegisterStaged.value = data.status === 'staged' || !!data.staged
      if (wechatRegisterStaged.value) {
        wechatRegisterStatusText.value = '微信已暂存，提交注册后完成绑定'
      } else if (data.status === 'pending') {
        wechatRegisterStatusText.value = '请使用微信扫码绑定'
      }
    }

    const ensureWechatRegisterFlow = async () => {
      if (flowCoordinator.token) {
        const resumed = await createWechatRegisterSession({ registrationFlowToken: flowCoordinator.token })
        if (resumed?.status !== 200) {
          if (isTerminalWechatRegisterStatus(resumed?.status)) await clearWechatRegisterFlow()
          throw resumed
        }
        applyWechatRegisterSnapshot(resumed.data)
        return flowCoordinator.token
      }
      const created = await createWechatRegisterSession()
      const token = created?.data?.registrationFlowToken
      if (created?.status !== 200 || !token) throw new Error('微信注册流程创建失败')
      flowCoordinator.save(token)
      applyWechatRegisterSnapshot(created.data)
      return token
    }

    const checkWechatRegisterStatus = async () => {
      const token = flowCoordinator.token
      if (!token) return
      try {
        if (wechatRegisterSessionId.value) {
          const res = await getWechatRegisterStatus({ sessionId: wechatRegisterSessionId.value })
          if (res?.status === 200) applyWechatRegisterSnapshot(res.data)
          else if (isWechatQRCodeError(res)) {
            markWechatQRCodeExpired()
          } else if (isTerminalWechatRegisterStatus(res?.status) && isWechatFlowError(res)) {
            await clearWechatRegisterFlow()
            wechatRegisterStatusText.value = res.status === 410 ? '微信注册流程已过期，请重新绑定' : '微信绑定已被其他注册占用'
          } else if (isTerminalWechatRegisterStatus(res?.status)) {
            markWechatQRCodeExpired()
          }
          return
        }
        const res = await createWechatRegisterSession({ registrationFlowToken: token })
        if (res?.status === 200) applyWechatRegisterSnapshot(res.data)
        else if (isTerminalWechatRegisterStatus(res?.status)) {
          await clearWechatRegisterFlow()
          wechatRegisterStatusText.value = res.status === 410 ? '微信注册流程已过期，请重新绑定' : '微信绑定已被其他注册占用'
        }
      } catch (error) {
        const status = flowErrorStatus(error)
        if (isWechatQRCodeError(error)) {
          markWechatQRCodeExpired()
        } else if (isTerminalWechatRegisterStatus(status) && isWechatFlowError(error)) {
          await clearWechatRegisterFlow()
          wechatRegisterStatusText.value = status === 410 ? '微信注册流程已过期，请重新绑定' : '微信绑定已被其他注册占用'
        } else if (isTerminalWechatRegisterStatus(status)) {
          markWechatQRCodeExpired()
        }
      }
    }

    const startWechatRegisterPolling = () => {
      stopWechatRegisterPolling()
      wechatRegisterPollTimer = setInterval(checkWechatRegisterStatus, 5000)
    }

    const refreshWechatRegisterQRCode = async (refresh = false) => {
      wechatQrLoading.value = true
      try {
        const token = await ensureWechatRegisterFlow()
        const res = await createWechatRegisterQRCode({ registrationFlowToken: token, refresh })
        if (res?.status !== 200) {
          if (isTerminalWechatRegisterStatus(res?.status)) await clearWechatRegisterFlow()
          throw new Error(res?.message || '二维码创建失败')
        }
        if (!res?.data?.sessionId) throw new Error(res?.message || '二维码创建失败')
        wechatRegisterSessionId.value = res.data.sessionId
        wechatQrCodeImageData.value = res.data.displayData || ''
        wechatRegisterStatusText.value = '请使用微信扫码绑定'
        startWechatRegisterPolling()
      } catch (error) {
        const status = flowErrorStatus(error)
        if (isTerminalWechatRegisterStatus(status)) await clearWechatRegisterFlow()
        message.error(error?.message || '二维码创建失败')
      } finally {
        wechatQrLoading.value = false
      }
    }

    const startWechatBinding = async () => {
      try {
        await ensureWechatRegisterFlow()
        wechatBindingActive.value = true
        wechatRegisterMode.value = chooseWechatRegisterMode()
        if (wechatRegisterMode.value === 'scan') {
          await refreshWechatRegisterQRCode(false)
        } else {
          startWechatRegisterPolling()
        }
      } catch (error) {
        message.error(error?.message || '微信绑定暂不可用')
      }
    }

    const handleWechatRegisterModeChange = () => {
      if (wechatRegisterMode.value === 'code') {
        stopWechatRegisterPolling()
        wechatRegisterSessionId.value = ''
        wechatQrCodeImageData.value = ''
        wechatRegisterStatusText.value = wechatRegisterStaged.value
          ? '微信已暂存，提交注册后完成绑定'
          : '请输入微信验证码'
        return
      }
      if (!wechatQrCodeImageData.value) {
        refreshWechatRegisterQRCode(false)
      } else {
        startWechatRegisterPolling()
      }
    }

    const submitWechatRegisterCode = async () => {
      if (!wechatRegisterCode.value) return
      wechatCodeSubmitting.value = true
      try {
        const token = await ensureWechatRegisterFlow()
        const res = await stageWechatRegisterCode({
          registrationFlowToken: token,
          code: wechatRegisterCode.value.trim(),
        })
        if (res?.status !== 200) {
          if (isTerminalWechatRegisterStatus(res?.status)) await clearWechatRegisterFlow()
          throw new Error(res?.message || '微信验证码绑定失败')
        }
        wechatRegisterStaged.value = true
        wechatRegisterStatusText.value = '微信已暂存，提交注册后完成绑定'
        startWechatRegisterPolling()
      } catch (error) {
        const status = flowErrorStatus(error)
        if (isTerminalWechatRegisterStatus(status)) await clearWechatRegisterFlow()
        message.error(error?.message || '微信验证码绑定失败')
      } finally {
        wechatCodeSubmitting.value = false
      }
    }

    const cancelWechatBinding = () => {
      clearWechatRegisterFlow({ release: true })
    }

    const resumeWechatRegisterFlow = async () => {
      if (!flowCoordinator.token) return
      try {
        const res = await createWechatRegisterSession({ registrationFlowToken: flowCoordinator.token })
        if (res?.status !== 200) throw res
        applyWechatRegisterSnapshot(res.data)
        wechatBindingActive.value = true
        wechatRegisterMode.value = chooseWechatRegisterMode()
        startWechatRegisterPolling()
      } catch (error) {
        const status = flowErrorStatus(error)
        if (isTerminalWechatRegisterStatus(status)) {
          await clearWechatRegisterFlow()
        }
      }
    }

    const resetRegisterForm = () => {
      registerForm.username = ''
      registerForm.password = ''
      registerForm.nickname = ''
      registerForm.phone = ''
      registerForm.smsCode = ''
    }

    const sendRegisterCode = async () => {
      sendingCode.value = true
      try {
        registerForm.phone = ensurePhoneBeforeSMSCaptcha(registerForm.phone)
        await ensureCaptchaToken(CaptchaSceneSMSSendRegister, registerForm)
        const res = await sendPublicSMSCode(buildPublicSMSCodePayload(registerForm, 'register'))
        if (res?.status === 200) {
          message.success(res?.message || '发送成功，请注意查收')
          smsCooldown.start()
        } else if (res?.status === 202) {
          message.warning('短信发送结果待核对，请先等待短信，勿重复发送')
          smsCooldown.start()
        } else {
          message.error(res?.message || '发送失败')
        }
      } catch (error) {
        message.error(error?.message || '发送失败')
      } finally {
        sendingCode.value = false
      }
    }

    const handleRegister = async () => {
      registerDisabledNotice.value = ''
      submitting.value = true
      try {
        registerForm.username = normalizeUsername(registerForm.username)
      } catch (error) {
        submitting.value = false
        message.error(error?.message || '用户名不符合要求')
        return
      }
      try {
        await ensureCaptchaToken(CaptchaSceneRegisterSubmit, registerForm)
      } catch (error) {
        submitting.value = false
        message.error(error?.message || '请先完成人机验证')
        return
      }

      let payload
      try {
        payload = buildPublicRegisterPayload(registerForm)
        payload = appendWechatRegisterFlowToken(payload, wechatRegisterStaged.value ? flowCoordinator.token : '')
      } catch (error) {
        submitting.value = false
        message.error(error?.message || '注册失败')
        return
      }

      registerUser(payload).then((res) => {
        if (res?.status === 200) {
          message.success('注册成功，请使用新账号登录')
          const username = registerForm.username.trim()
          clearWechatRegisterFlow()
          resetRegisterForm()
          router.push({ path: '/login', query: { username } })
        } else {
          if (isTerminalWechatRegisterStatus(res?.status)) {
            clearWechatRegisterFlow()
          }
          if (res?.message === '未开放注册') {
            registerDisabledNotice.value = '当前暂不可注册，请返回登录页。'
          }
          message.error(res?.message || '注册失败')
        }
      }).catch((error) => {
        const status = flowErrorStatus(error)
        if (isTerminalWechatRegisterStatus(status)) {
          clearWechatRegisterFlow()
        }
        message.error(error?.message || '注册失败')
      }).finally(() => {
        submitting.value = false
      })
    }

    const goToLogin = () => {
      router.push('/login')
    }

    const handleFinishFailed = (errors) => {
      console.log(errors)
    }

    const handleWechatRegisterVisibility = () => {
      if (typeof document === 'undefined') return
      if (document.visibilityState === 'hidden') {
        stopWechatRegisterPolling()
      } else if (flowCoordinator.token && wechatBindingActive.value) {
        checkWechatRegisterStatus()
        startWechatRegisterPolling()
      }
    }

    onMounted(async () => {
      if (typeof document !== 'undefined') document.addEventListener('visibilitychange', handleWechatRegisterVisibility)
      try {
        await loadPublicAuthSettings()
      } finally {
        await resumeWechatRegisterFlow()
      }
    })

    onBeforeUnmount(() => {
      stopWechatRegisterPolling()
      flowCoordinator.dispose()
      if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', handleWechatRegisterVisibility)
    })

    return {
      submitting,
      sendingCode,
      smsCooldownText: smsCooldown.buttonText,
      smsCooldownActive: smsCooldown.active,
      startSmsCooldown: smsCooldown.start,
      registerDisabledNotice,
      publicAuthSettings,
      wechatBindingActive,
      wechatRegisterStaged,
      wechatRegisterMode,
      wechatQrLoading,
      wechatQrCodeImageData,
      wechatRegisterCode,
      wechatCodeSubmitting,
      wechatRegisterStatusText,
      registerForm,
      usernameRules,
      normalizeSMSCodeInput,
      limitUsernameInput,
      handleRegister,
      sendRegisterCode,
      startWechatBinding,
      refreshWechatRegisterQRCode,
      handleWechatRegisterModeChange,
      submitWechatRegisterCode,
      cancelWechatBinding,
      goToLogin,
      handleFinishFailed,
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
