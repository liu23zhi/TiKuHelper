<template>
  <div class="profile-page">
    <a-row :gutter="16" class="summary-row">
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <div class="summary-label">账号状态</div>
          <div class="summary-value">{{ statusText(currentUser.status) }}</div>
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <div class="summary-label">账号角色</div>
          <div class="summary-value">{{ roleText(currentUser.role) }}</div>
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <div class="summary-label">账户余额</div>
          <div class="summary-value">¥ {{ formatBalance(currentUser.balance) }}</div>
        </a-card>
      </a-col>
      <a-col :xs="24" :sm="12" :lg="6">
        <a-card :bordered="false" class="summary-card">
          <div class="summary-label">上次登录</div>
          <div class="summary-value summary-value--small">{{ formatDateTime(currentUser.lastLoginAt) }}</div>
        </a-card>
      </a-col>
    </a-row>

    <a-card :bordered="false" class="page-card" :loading="loading">
      <template #title>
        <div>
          <div class="page-title">个人中心</div>
          <div class="page-subtitle">查看个人状态，并维护手机号与微信绑定信息。</div>
        </div>
      </template>
      <template #extra>
        <a-button @click="loadProfile" :loading="loading">刷新</a-button>
      </template>

      <a-row :gutter="16">
        <a-col :xs="24" :lg="10">
          <div class="profile-panel">
            <div class="panel-title">基础信息</div>
            <div class="profile-field">
              <span class="profile-field-label">用户名</span>
              <span>{{ currentUser.username || '-' }}</span>
            </div>
            <div class="profile-field">
              <span class="profile-field-label">昵称</span>
              <span>{{ currentUser.nickname || '-' }}</span>
            </div>
            <div class="profile-field">
              <span class="profile-field-label">注册来源</span>
              <span>{{ registerSourceText(currentUser.registerSource) }}</span>
            </div>
            <div class="profile-field">
              <span class="profile-field-label">注册时间</span>
              <span>{{ formatDateTime(currentUser.createdAt) }}</span>
            </div>
            <div class="profile-field">
              <span class="profile-field-label">登录 IP</span>
              <span>{{ currentUser.lastLoginIp || '-' }}</span>
            </div>
          </div>
        </a-col>

        <a-col :xs="24" :lg="14">
          <div class="profile-panel">
            <div class="panel-head">
              <div>
                <div class="panel-title">手机号绑定</div>
                <div class="panel-subtitle">
                  当前：{{ currentUser.phone || '未绑定' }}
                  <span v-if="currentUser.phoneBoundAt">，绑定于 {{ formatDateTime(currentUser.phoneBoundAt) }}</span>
                </div>
              </div>
            </div>
            <a-alert
              v-if="!publicAuthSettings.smsLoginEnabled"
              type="warning"
              show-icon
              message="管理员尚未启用手机号，当前无法更换手机号。"
            />
            <a-form v-else layout="vertical" :model="phoneForm" @finish="submitPhoneChange">
              <a-form-item label="新手机号" required>
                <a-input
                  v-model:value="phoneForm.phone"
                  placeholder="请输入新手机号，国际号码请带 + 区号"
                  :maxlength="20"
                  inputmode="tel"
                  @input="phoneForm.phone = normalizePhoneInput(phoneForm.phone)"
                />
              </a-form-item>
              <a-form-item label="新手机号验证码" required>
                <div class="sms-code-row">
                  <a-input
                    v-model:value="phoneForm.smsCode"
                    placeholder="请输入短信验证码"
                    :maxlength="6"
                    inputmode="numeric"
                    @input="phoneForm.smsCode = normalizeSMSCodeInput(phoneForm.smsCode)"
                  />
                  <a-button
                    :loading="sendingPhoneCode"
                    :disabled="sendingPhoneCode || smsCooldownActive || !phoneForm.phone"
                    @click="sendPhoneChangeCode"
                  >
                    {{ smsCooldownText }}
                  </a-button>
                </div>
              </a-form-item>
              <a-button
                type="primary"
                html-type="submit"
                :loading="updatingPhone"
                :disabled="!phoneForm.phone || !phoneForm.smsCode"
              >
                确认更换手机号
              </a-button>
            </a-form>
          </div>
        </a-col>
      </a-row>
    </a-card>

    <a-card :bordered="false" class="page-card" :loading="loading">
      <template #title>
        <div>
          <div class="page-title">微信绑定</div>
          <div class="page-subtitle">绑定后可直接使用微信扫码登录当前账号。</div>
        </div>
      </template>

      <div v-if="currentUser.wechatId" class="wechat-bound-panel">
        <a-tag color="green">已绑定</a-tag>
        <div class="wechat-bound-time">
          绑定时间：{{ formatDateTime(currentUser.wechatBoundAt) }}
        </div>
        <a-popconfirm
          title="确定解绑当前微信吗？解绑后将不能继续使用该微信快捷登录。"
          ok-text="确认解绑"
          cancel-text="取消"
          @confirm="unbindWechat"
        >
          <a-button danger :loading="unbindingWechat">解绑微信</a-button>
        </a-popconfirm>
      </div>

      <div v-else class="wechat-bind-panel">
        <a-alert
          v-if="!publicAuthSettings.wechatScanLoginEnabled"
          type="warning"
          show-icon
          message="管理员尚未开启微信，当前无法绑定。"
        />
        <template v-else>
          <div class="wechat-qrcode-frame">
            <a-spin v-if="wechatQrLoading" />
            <img v-else-if="wechatQrCodeImageData" :src="wechatQrCodeImageData" alt="微信绑定二维码" />
            <div v-else class="wechat-qrcode-empty">点击下方按钮获取绑定二维码</div>
          </div>
          <div class="wechat-status">{{ wechatStatusText }}</div>
          <a-button block :loading="wechatQrLoading" @click="startWechatBind">刷新绑定二维码</a-button>
        </template>
      </div>
    </a-card>
  </div>
</template>

<script>
import { defineComponent, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import {
  bindCurrentUserWechat,
  createCaptchaChallenge,
  createWechatLoginQRCode,
  getCurrentUser,
  getPublicRegisterStatus,
  getWechatLoginStatus,
  sendUserPhoneChangeSMSCode,
  unbindCurrentUserWechat,
  updateCurrentUserPhone,
  verifyCaptchaChallenge,
  verifyRemoteCaptcha,
} from '@/api/TikuComponentApi'
import { ensureAuthCaptchaToken } from '@/utils/captchaDialog.mjs'
import {
  normalizePhoneInput,
  normalizeSMSCodeInput,
} from '@/utils/captchaHelpers.mjs'
import { buildRegisterSourceLabel } from '@/utils/userPortalHelpers.mjs'
import { setStoredCurrentUser } from '@/utils/currentUser.js'
import { useSmsCooldown } from '@/utils/smsCooldown.js'

const CaptchaSceneSMSSendChangePhone = 'sms_send_change_phone'

export default defineComponent({
  name: 'UserProfile',
  setup() {
    const loading = ref(false)
    const updatingPhone = ref(false)
    const sendingPhoneCode = ref(false)
    const unbindingWechat = ref(false)
    const wechatQrLoading = ref(false)
    const wechatQrCodeImageData = ref('')
    const wechatLoginToken = ref('')
    const wechatStatusText = ref('请使用微信扫码完成绑定')
    const wechatPollTimer = ref(null)
    const currentUser = reactive({})
    const phoneForm = reactive({
      phone: '',
      smsCode: '',
      captchaToken: '',
    })
    const publicAuthSettings = ref({
      captchaProvider: 'remote',
      captchaApiBaseUrl: 'https://captcha.api.zelly.cn',
      captchaType: 'RANDOM',
      smsLoginEnabled: false,
      wechatScanLoginEnabled: false,
    })
    const smsCooldown = useSmsCooldown()

    const applyCurrentUser = (user) => {
      Object.keys(currentUser).forEach((key) => {
        delete currentUser[key]
      })
      Object.assign(currentUser, user || {})
      if (user) {
        setStoredCurrentUser(user)
      }
    }

    const loadProfile = async () => {
      loading.value = true
      try {
        const res = await getCurrentUser()
        if (res?.status !== 200) {
          message.error(res?.message || '获取个人状态失败')
          return
        }
        applyCurrentUser(res.data || {})
      } catch (error) {
        message.error(error?.message || '获取个人状态失败')
      } finally {
        loading.value = false
      }
    }

    const loadPublicSettings = async () => {
      try {
        const res = await getPublicRegisterStatus()
        if (res?.status === 200) {
          publicAuthSettings.value = {
            captchaProvider: res?.data?.captchaProvider || 'remote',
            captchaApiBaseUrl: res?.data?.captchaApiBaseUrl || 'https://captcha.api.zelly.cn',
            captchaType: res?.data?.captchaType || 'RANDOM',
            smsLoginEnabled: !!res?.data?.smsLoginEnabled,
            wechatScanLoginEnabled: !!res?.data?.wechatScanLoginEnabled,
          }
        }
      } catch (_) {
        // The profile still works when public settings are temporarily unavailable.
      }
    }

    const ensureCaptchaToken = (scene, targetForm) => ensureAuthCaptchaToken({
      scene,
      targetForm,
      publicSettings: publicAuthSettings.value,
      createCaptchaChallenge,
      verifyCaptchaChallenge,
      verifyRemoteCaptcha,
    })

    const sendPhoneChangeCode = async () => {
      sendingPhoneCode.value = true
      try {
        phoneForm.phone = normalizePhoneInput(phoneForm.phone)
        if (!phoneForm.phone) {
          throw new Error('请输入新手机号')
        }
        await ensureCaptchaToken(CaptchaSceneSMSSendChangePhone, phoneForm)
        const res = await sendUserPhoneChangeSMSCode({
          phone: phoneForm.phone,
          captchaToken: phoneForm.captchaToken,
        })
        if (res?.status !== 200) {
          message.error(res?.message || '发送失败')
          return
        }
        message.success(res?.message || '发送成功，请注意查收')
        smsCooldown.start()
      } catch (error) {
        message.error(error?.message || '发送失败')
      } finally {
        sendingPhoneCode.value = false
      }
    }

    const submitPhoneChange = async () => {
      updatingPhone.value = true
      try {
        const res = await updateCurrentUserPhone({
          phone: phoneForm.phone,
          smsCode: phoneForm.smsCode,
        })
        if (res?.status !== 200) {
          message.error(res?.message || '更换手机号失败')
          return
        }
        message.success('手机号更换成功')
        applyCurrentUser(res.data || {})
        phoneForm.phone = ''
        phoneForm.smsCode = ''
        phoneForm.captchaToken = ''
      } catch (error) {
        message.error(error?.message || '更换手机号失败')
      } finally {
        updatingPhone.value = false
      }
    }

    const stopWechatPolling = () => {
      if (wechatPollTimer.value) {
        clearInterval(wechatPollTimer.value)
        wechatPollTimer.value = null
      }
    }

    const pollWechatBindStatus = async () => {
      if (!wechatLoginToken.value) {
        return
      }
      try {
        const res = await getWechatLoginStatus({ loginToken: wechatLoginToken.value })
        if (res?.status !== 200) {
          wechatStatusText.value = res?.message || '二维码已过期，请刷新'
          stopWechatPolling()
          return
        }
        const status = res?.data?.status
        if (status === 'need_bind') {
          stopWechatPolling()
          wechatStatusText.value = '扫码成功，正在绑定当前账号'
          const bindRes = await bindCurrentUserWechat({ bindToken: res?.data?.bindToken || '' })
          if (bindRes?.status !== 200) {
            message.error(bindRes?.message || '微信绑定失败')
            wechatStatusText.value = '绑定失败，请刷新二维码重试'
            return
          }
          message.success('微信绑定成功')
          applyCurrentUser(bindRes.data || {})
          wechatQrCodeImageData.value = ''
          wechatLoginToken.value = ''
          return
        }
        if (status === 'success') {
          stopWechatPolling()
          wechatStatusText.value = '该微信已绑定其他账号，请先在该账号中解绑'
          return
        }
        wechatStatusText.value = '等待扫码确认'
      } catch (error) {
        wechatStatusText.value = error?.message || '微信绑定状态查询失败'
      }
    }

    const startWechatBind = async () => {
      stopWechatPolling()
      wechatQrLoading.value = true
      wechatStatusText.value = '正在创建绑定二维码'
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
        wechatStatusText.value = '请使用微信扫码确认绑定'
        wechatPollTimer.value = setInterval(() => {
          void pollWechatBindStatus()
        }, 2000)
      } catch (error) {
        wechatStatusText.value = error?.message || '二维码创建失败，请稍后重试'
        message.error(wechatStatusText.value)
      } finally {
        wechatQrLoading.value = false
      }
    }

    const unbindWechat = async () => {
      unbindingWechat.value = true
      try {
        const res = await unbindCurrentUserWechat()
        if (res?.status !== 200) {
          message.error(res?.message || '微信解绑失败')
          return
        }
        message.success('微信已解绑')
        applyCurrentUser(res.data || {})
        wechatQrCodeImageData.value = ''
        wechatLoginToken.value = ''
      } catch (error) {
        message.error(error?.message || '微信解绑失败')
      } finally {
        unbindingWechat.value = false
      }
    }

    const formatDateTime = (value) => {
      if (!value) {
        return '-'
      }
      const date = new Date(value)
      return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
    }

    const formatBalance = (value) => {
      const amount = Number(value || 0)
      return Number.isFinite(amount) ? amount.toFixed(2) : '0.00'
    }

    const roleText = (value) => String(value || '').toLowerCase() === 'admin' ? '管理员' : '普通用户'
    const statusText = (value) => String(value || '').toLowerCase() === 'disabled' ? '已禁用' : '正常'

    onMounted(() => {
      void Promise.all([loadProfile(), loadPublicSettings()])
    })

    onBeforeUnmount(stopWechatPolling)

    return {
      loading,
      updatingPhone,
      sendingPhoneCode,
      unbindingWechat,
      wechatQrLoading,
      wechatQrCodeImageData,
      wechatStatusText,
      currentUser,
      phoneForm,
      publicAuthSettings,
      smsCooldownText: smsCooldown.buttonText,
      smsCooldownActive: smsCooldown.active,
      sendPhoneChangeCode,
      submitPhoneChange,
      startWechatBind,
      unbindWechat,
      loadProfile,
      normalizePhoneInput,
      normalizeSMSCodeInput,
      registerSourceText: buildRegisterSourceLabel,
      formatDateTime,
      formatBalance,
      roleText,
      statusText,
    }
  },
})
</script>

<style scoped>
.profile-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.summary-row {
  margin-bottom: 0;
}

.summary-card,
.page-card,
.profile-panel {
  border-radius: 18px;
}

.summary-label {
  color: #6b7280;
  font-size: 13px;
}

.summary-value {
  margin-top: 10px;
  color: #0f172a;
  font-size: 24px;
  font-weight: 700;
}

.summary-value--small {
  font-size: 16px;
  line-height: 1.5;
}

.page-title {
  color: #1f2937;
  font-size: 21px;
  font-weight: 700;
}

.page-subtitle,
.panel-subtitle {
  margin-top: 6px;
  color: #6b7280;
  font-size: 13px;
  line-height: 1.7;
}

.profile-panel {
  height: 100%;
  padding: 20px;
  border: 1px solid #e5e7eb;
  background: linear-gradient(180deg, #ffffff 0%, #f8fafc 100%);
}

.panel-title {
  color: #1f2937;
  font-size: 17px;
  font-weight: 700;
}

.panel-head {
  margin-bottom: 18px;
}

.profile-field {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 0;
  border-bottom: 1px solid #eef2f7;
  color: #334155;
}

.profile-field:last-child {
  border-bottom: none;
}

.profile-field-label {
  color: #64748b;
}

.sms-code-row {
  display: flex;
  gap: 8px;
}

.sms-code-row :deep(.ant-input) {
  flex: 1;
}

.wechat-bound-panel,
.wechat-bind-panel {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 14px;
  padding: 18px;
  border: 1px solid #e5e7eb;
  border-radius: 16px;
  background: #f8fafc;
}

.wechat-bound-time,
.wechat-status,
.wechat-qrcode-empty {
  color: #64748b;
  font-size: 13px;
  text-align: center;
}

.wechat-qrcode-frame {
  width: 220px;
  height: 220px;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  border: 1px solid #e5e7eb;
  border-radius: 16px;
  background: #fff;
}

.wechat-qrcode-frame img {
  width: 100%;
  height: 100%;
  object-fit: contain;
}

@media (max-width: 640px) {
  .sms-code-row {
    flex-direction: column;
  }
}
</style>
