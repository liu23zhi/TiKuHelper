<template>
  <div class="console-page profile-page">
    <ConsolePageHeading title="个人中心" description="查看账号状态，更新个人资料与登录绑定信息。">
      <template #actions>
      <a-button @click="loadProfile" :loading="loading">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <ConsoleSummaryGrid>
        <a-card :bordered="false" class="summary-card">
          <div class="summary-label">账号状态</div>
          <div class="summary-value">{{ statusText(currentUser.status) }}</div>
        </a-card>
        <a-card :bordered="false" class="summary-card">
          <div class="summary-label">账号角色</div>
          <div class="summary-value">{{ roleText(currentUser.role) }}</div>
        </a-card>
        <a-card :bordered="false" class="summary-card">
          <div class="summary-label">账户余额</div>
          <div class="summary-value">¥ {{ formatBalance(currentUser.balance) }}</div>
        </a-card>
        <a-card :bordered="false" class="summary-card">
          <div class="summary-label">上次登录</div>
          <div class="summary-value summary-value--small">{{ formatDateTime(currentUser.lastLoginAt) }}</div>
        </a-card>
    </ConsoleSummaryGrid>

    <a-card :bordered="false" class="page-card" :loading="loading">
      <a-row :gutter="[16, 16]">
        <a-col :xs="24" :lg="10">
          <ConsolePanel class="profile-panel" title="基础信息" description="账号创建与最近登录情况。">
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
              <span class="location-value">{{ formatValueWithLocation(currentUser.lastLoginIp, ipLocations[normalizeLocationKey(currentUser.lastLoginIp)]) || '-' }}</span>
            </div>
            <div class="profile-section-divider"></div>
            <div class="panel-heading panel-heading--form">
              <h3 class="panel-title">修改用户名和昵称</h3>
              <p class="panel-subtitle">更新后使用新用户名登录。</p>
            </div>
            <a-form layout="vertical" :model="profileForm" @finish="submitProfile">
              <a-form-item label="用户名" name="username" required>
                <a-input
                  v-model:value="profileForm.username"
                  placeholder="只能使用中文、英文、数字、下划线和@"
                  :maxlength="255"
                />
              </a-form-item>
              <a-form-item label="昵称" name="nickname">
                <a-input
                  v-model:value="profileForm.nickname"
                  placeholder="可选，最多255个字符"
                  :maxlength="255"
                />
              </a-form-item>
              <a-button type="primary" html-type="submit" :loading="updatingProfile">
                保存资料
              </a-button>
            </a-form>
          </ConsolePanel>
        </a-col>

        <a-col :xs="24" :lg="14">
          <ConsolePanel class="profile-panel">
            <template #heading>
              <div>
                <h2 class="panel-title">手机号绑定</h2>
                <div class="panel-subtitle">
                  当前：<span class="location-value">{{ currentUser.phone ? formatValueWithLocation(currentUser.phone, phoneLocations[normalizeLocationKey(currentUser.phone)]) : '未绑定' }}</span>
                  <span v-if="currentUser.phoneBoundAt" class="location-extra">，绑定于 {{ formatDateTime(currentUser.phoneBoundAt) }}</span>
                </div>
              </div>
            </template>
            <a-alert
              v-if="!publicAuthSettings.smsLoginEnabled"
              type="warning"
              show-icon
              message="管理员尚未启用手机号，当前无法更换手机号。"
            />
            <a-form v-else layout="vertical" :model="phoneForm" @finish="submitPhoneChange">
              <a-form-item label="新手机号" required>
                <MainlandPhoneInput
                  v-model:value="phoneForm.phone"
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
          </ConsolePanel>
        </a-col>
      </a-row>
    </a-card>

    <a-card :bordered="false" class="page-card" :loading="loading">
      <template #title>
        <div>
          <h2 class="page-title">微信绑定</h2>
          <div class="page-subtitle">绑定后可使用微信扫描或微信验证码登录当前账号。</div>
        </div>
      </template>

      <div v-if="currentUser.wechatId" class="wechat-bound-panel">
        <a-tag color="green">已绑定</a-tag>
        <div class="wechat-binding-id">
          微信ID：{{ currentUser.wechatId }}
        </div>
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
        <div class="wechat-binding-id wechat-binding-id--empty">未绑定</div>
        <a-alert
          v-if="!publicAuthSettings.wechatLoginEnabled"
          type="warning"
          show-icon
          message="管理员尚未开启微信，当前无法绑定。"
        />
        <template v-else>
          <a-radio-group
            v-if="publicAuthSettings.wechatScanLoginEnabled && publicAuthSettings.wechatCodeLoginEnabled"
            v-model:value="wechatBindSubMode"
            button-style="solid"
            class="wechat-mode-switch"
            @change="handleWechatBindSubModeChange"
          >
            <a-radio-button value="scan">微信扫描</a-radio-button>
            <a-radio-button value="code">微信验证码</a-radio-button>
          </a-radio-group>

          <template v-if="wechatBindSubMode === 'scan'">
            <div class="wechat-qrcode-frame">
              <a-spin v-if="wechatQrLoading" />
              <img v-else-if="wechatQrCodeImageData" :src="wechatQrCodeImageData" alt="微信绑定二维码" />
              <div v-else class="wechat-qrcode-empty">点击下方按钮获取绑定二维码</div>
            </div>
            <div class="wechat-status">
              {{ wechatStatusText }}
              <span v-if="wechatRemainingSeconds !== null">，剩余 {{ wechatRemainingSeconds }} 秒</span>
              <span v-if="wechatTransportText">，{{ wechatTransportText }}</span>
            </div>
            <a-button block :loading="wechatQrLoading" @click="startWechatBind">刷新绑定二维码</a-button>
          </template>

          <template v-else>
            <div class="wechat-qrcode-frame">
              <img
                v-if="publicAuthSettings.wechatCodeStaticQrcodeUrl"
                :src="publicAuthSettings.wechatCodeStaticQrcodeUrl"
                alt="微信验证码二维码"
              />
              <div v-else class="wechat-qrcode-empty">请联系管理员配置验证码二维码</div>
            </div>
            <div class="wechat-status">扫码关注后发送“验证码”，再输入收到的验证码绑定</div>
            <div class="wechat-code-field">
              <label for="profile-wechat-code">微信验证码</label>
              <a-input
                id="profile-wechat-code"
                v-model:value="wechatCodeForm.code"
                placeholder="请输入微信验证码"
                inputmode="numeric"
                :maxlength="12"
                @input="wechatCodeForm.code = normalizeWechatCodeInput(wechatCodeForm.code)"
              />
            </div>
            <a-button
              type="primary"
              block
              :loading="wechatCodeSubmitting"
              :disabled="!wechatCodeForm.code || !publicAuthSettings.wechatCodeStaticQrcodeUrl"
              @click="submitWechatCodeBind"
            >
              微信验证码绑定
            </a-button>
          </template>
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
  bindCurrentUserWechatByCode,
  createCaptchaChallenge,
  createWechatLoginQRCode,
  getCurrentUser,
  updateCurrentUserProfile,
  getPublicRegisterStatus,
  getWechatLoginStatus,
  sendUserPhoneChangeSMSCode,
  unbindCurrentUserWechat,
  updateCurrentUserPhone,
  verifyCaptchaChallenge,
  verifyRemoteCaptcha,
  getMyLocations,
} from '@/api/TikuComponentApi'
import { ensureAuthCaptchaToken } from '@/utils/captchaDialog.mjs'
import {
  normalizeSMSCodeInput,
  ensurePhoneBeforeSMSCaptcha,
} from '@/utils/captchaHelpers.mjs'
import MainlandPhoneInput from '@/components/MainlandPhoneInput.vue'
import { buildRegisterSourceLabel } from '@/utils/userPortalHelpers.mjs'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'
import { setStoredCurrentUser } from '@/utils/currentUser.js'
import { useSmsCooldown } from '@/utils/smsCooldown.js'
import { createWechatQrStatusController } from '@/utils/wechatQrStatus.mjs'
import { extractLocationAddresses, normalizeLocationKey, formatValueWithLocation as formatValueWithLocationBase } from '@/utils/locationHelpers.mjs'

const CaptchaSceneSMSSendChangePhone = 'sms_send_change_phone'
const ipLocations = reactive({})
const phoneLocations = reactive({})
let locationLoadGeneration = 0

function formatValueWithLocation(rawValue, address) {
  return formatValueWithLocationBase(rawValue, address)
}

function clearLocationMap(target) {
  Object.keys(target).forEach((key) => {
    delete target[key]
  })
}

async function loadProfileLocations() {
  const generation = ++locationLoadGeneration
  clearLocationMap(ipLocations)
  clearLocationMap(phoneLocations)
  try {
    const locationRes = await getMyLocations()
    if (generation !== locationLoadGeneration) {
      return
    }
    const ipAddresses = extractLocationAddresses(locationRes, 'ip')
    const phoneAddresses = extractLocationAddresses(locationRes, 'phone')
    Object.keys(ipAddresses).forEach((key) => {
      ipLocations[normalizeLocationKey(key)] = ipAddresses[key]
    })
    Object.keys(phoneAddresses).forEach((key) => {
      phoneLocations[normalizeLocationKey(key)] = phoneAddresses[key]
    })
  } catch {
    // 归属地只是展示增强，查询失败时保留原始值。
  }
}

export default defineComponent({
  name: 'UserProfile',
  setup() {
    const loading = ref(false)
    const updatingPhone = ref(false)
    const updatingProfile = ref(false)
    const sendingPhoneCode = ref(false)
    const unbindingWechat = ref(false)
    const wechatQrLoading = ref(false)
    const wechatQrCodeImageData = ref('')
    const wechatLoginToken = ref('')
    const wechatStatusText = ref('请使用微信扫码完成绑定')
    const wechatRemainingSeconds = ref(null)
    const wechatTransportText = ref('')
    const wechatBindSubMode = ref('scan')
    const wechatCodeSubmitting = ref(false)
    let wechatScanGeneration = 0
    const currentUser = reactive({})
    const profileForm = reactive({
      username: '',
      nickname: '',
    })
    const phoneForm = reactive({
      phone: '',
      smsCode: '',
      captchaToken: '',
    })
    const wechatCodeForm = reactive({
      code: '',
    })
    const publicAuthSettings = ref({
      captchaProvider: 'remote',
      captchaApiBaseUrl: 'https://captcha.api.zelly.cn',
      captchaType: 'RANDOM',
      smsLoginEnabled: false,
      wechatLoginEnabled: false,
      wechatScanLoginEnabled: false,
      wechatCodeLoginEnabled: false,
      wechatDefaultLoginMode: 'scan',
      wechatCodeStaticQrcodeUrl: '',
    })
    const smsCooldown = useSmsCooldown()

    const applyCurrentUser = (user) => {
      Object.keys(currentUser).forEach((key) => {
        delete currentUser[key]
      })
      Object.assign(currentUser, user || {})
      if (user) {
        setStoredCurrentUser(user)
        profileForm.username = user.username || ''
        profileForm.nickname = user.nickname || ''
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
        loadProfileLocations()
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
            wechatLoginEnabled: !!res?.data?.wechatLoginEnabled,
            wechatScanLoginEnabled: !!res?.data?.wechatScanLoginEnabled,
            wechatCodeLoginEnabled: !!res?.data?.wechatCodeLoginEnabled,
            wechatDefaultLoginMode: res?.data?.wechatDefaultLoginMode || 'scan',
            wechatCodeStaticQrcodeUrl: res?.data?.wechatCodeStaticQrcodeUrl || '',
          }
          wechatBindSubMode.value = chooseWechatBindSubMode(publicAuthSettings.value)
        }
      } catch (_) {
        // The profile still works when public settings are temporarily unavailable.
      }
    }

    const chooseWechatBindSubMode = (settings = publicAuthSettings.value) => {
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
        phoneForm.phone = ensurePhoneBeforeSMSCaptcha(phoneForm.phone)
        await ensureCaptchaToken(CaptchaSceneSMSSendChangePhone, phoneForm)
        const res = await sendUserPhoneChangeSMSCode({
          phone: phoneForm.phone,
          captchaToken: phoneForm.captchaToken,
        })
        if (res?.status === 202) {
          message.warning('短信发送结果待核对，请先等待短信，勿重复发送')
          smsCooldown.start()
          return
        }
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
        phoneForm.phone = ensurePhoneBeforeSMSCaptcha(phoneForm.phone)
        if (!phoneForm.smsCode) throw new Error('请输入短信验证码')
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
        loadProfileLocations()
        phoneForm.phone = ''
        phoneForm.smsCode = ''
        phoneForm.captchaToken = ''
      } catch (error) {
        message.error(error?.message || '更换手机号失败')
      } finally {
        updatingPhone.value = false
      }
    }

    const submitProfile = async () => {
      updatingProfile.value = true
      try {
        const res = await updateCurrentUserProfile({
          username: profileForm.username,
          nickname: profileForm.nickname,
        })
        if (res?.status !== 200) {
          message.error(res?.message || '保存资料失败')
          return
        }
        message.success('资料保存成功')
        applyCurrentUser(res.data || {})
      } catch (error) {
        message.error(error?.message || '保存资料失败')
      } finally {
        updatingProfile.value = false
      }
    }

    const resolveWechatBindStatus = async (payload = {}) => {
      const loginToken = wechatLoginToken.value
      const generation = wechatScanGeneration
      const isCurrent = () => generation === wechatScanGeneration && !!loginToken && loginToken === wechatLoginToken.value && wechatBindSubMode.value === 'scan'
      try {
        let resolved = payload || {}
        if (resolved.status === 'need_bind' && !resolved.bindToken) {
          const res = await getWechatLoginStatus({
            loginToken,
            purpose: 'bind',
          })
          if (!isCurrent()) return false
          if (res?.status !== 200) {
            wechatStatusText.value = res?.message || '微信绑定状态查询失败'
            return false
          }
          resolved = res.data || {}
        }
        if (resolved.status === 'need_bind') {
          wechatStatusText.value = '扫码成功，正在绑定当前账号'
          const bindRes = await bindCurrentUserWechat({ bindToken: resolved.bindToken || '' })
          if (!isCurrent()) return false
          if (bindRes?.status !== 200) {
            message.error(bindRes?.message || '微信绑定失败')
            wechatStatusText.value = '绑定失败，请刷新二维码重试'
            return false
          }
          message.success('微信绑定成功')
          applyCurrentUser(bindRes.data || {})
          wechatQrCodeImageData.value = ''
          wechatLoginToken.value = ''
          wechatRemainingSeconds.value = null
          wechatTransportText.value = ''
          return true
        }
        if (resolved.status === 'success') {
          wechatStatusText.value = '该微信已绑定其他账号，请先在该账号中解绑'
          return true
        }
      } catch (error) {
        if (!isCurrent()) return false
        wechatStatusText.value = error?.message || '微信绑定状态查询失败'
      }
      return false
    }

    const wechatStatusController = createWechatQrStatusController({
      checkStatus: () => getWechatLoginStatus({
        loginToken: wechatLoginToken.value,
        purpose: 'bind',
      }),
      onTransport: (transport) => {
        wechatTransportText.value = transport === 'websocket' ? '实时连接' : '轮询中'
      },
      onRemaining: (seconds) => {
        wechatRemainingSeconds.value = seconds
      },
      onExpired: () => {
        wechatStatusText.value = '二维码已过期，请刷新'
      },
      onError: (error) => {
        wechatStatusText.value = error?.message || '微信绑定状态查询失败，正在重试'
      },
      onStatus: async (status, payload) => {
        if (status === 'pending') {
          wechatStatusText.value = '请使用微信扫码确认绑定'
          return
        }
        if (status === 'scanned') {
          wechatStatusText.value = '已扫描，请在微信中确认'
          return
        }
        if (status === 'success' || status === 'need_bind') {
          return resolveWechatBindStatus(payload)
        }
      },
    })

    const resetWechatScanState = () => {
      wechatScanGeneration += 1
      wechatStatusController.stop()
      wechatQrLoading.value = false
      wechatQrCodeImageData.value = ''
      wechatLoginToken.value = ''
      wechatRemainingSeconds.value = null
      wechatTransportText.value = ''
    }

    const startWechatBind = async () => {
      if (wechatBindSubMode.value !== 'scan' || !publicAuthSettings.value.wechatScanLoginEnabled) {
        return
      }
      resetWechatScanState()
      const generation = wechatScanGeneration
      const isCurrent = () => generation === wechatScanGeneration && wechatBindSubMode.value === 'scan'
      wechatQrLoading.value = true
      wechatStatusText.value = '正在创建绑定二维码'
      try {
        const res = await createWechatLoginQRCode()
        if (!isCurrent()) return
        if (res?.status !== 200) {
          throw new Error(res?.message || '二维码创建失败，请稍后重试')
        }
        wechatQrCodeImageData.value = res?.data?.qrcodeImageData || ''
        wechatLoginToken.value = res?.data?.loginToken || ''
        if (!wechatQrCodeImageData.value || !wechatLoginToken.value) {
          throw new Error('二维码创建失败，请稍后重试')
        }
        wechatStatusText.value = '请使用微信扫码确认绑定'
        wechatStatusController.start({
          loginToken: wechatLoginToken.value,
          expireSeconds: res?.data?.expireSeconds,
          expiresAtUnix: res?.data?.expiresAtUnix,
        })
      } catch (error) {
        if (!isCurrent()) return
        wechatStatusText.value = error?.message || '二维码创建失败，请稍后重试'
        message.error(wechatStatusText.value)
      } finally {
        if (isCurrent()) wechatQrLoading.value = false
      }
    }

    const handleWechatBindSubModeChange = () => {
      if (wechatBindSubMode.value === 'scan') {
        wechatStatusText.value = '请使用微信扫码完成绑定'
        return
      }
      resetWechatScanState()
      wechatStatusText.value = '请输入微信验证码'
    }

    const submitWechatCodeBind = async () => {
      if (!wechatCodeForm.code) {
        message.warning('请输入微信验证码')
        return
      }
      wechatCodeSubmitting.value = true
      try {
        const res = await bindCurrentUserWechatByCode({ code: wechatCodeForm.code })
        if (res?.status !== 200) {
          message.error(res?.message || '微信验证码绑定失败')
          return
        }
        message.success('微信绑定成功')
        applyCurrentUser(res.data || {})
        wechatCodeForm.code = ''
        wechatQrCodeImageData.value = ''
        wechatLoginToken.value = ''
        wechatRemainingSeconds.value = null
        wechatTransportText.value = ''
      } catch (error) {
        message.error(error?.message || '微信验证码绑定失败')
      } finally {
        wechatCodeSubmitting.value = false
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
        wechatStatusController.stop()
        wechatQrCodeImageData.value = ''
        wechatLoginToken.value = ''
        wechatRemainingSeconds.value = null
        wechatTransportText.value = ''
        wechatCodeForm.code = ''
        wechatBindSubMode.value = chooseWechatBindSubMode(publicAuthSettings.value)
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

    const formatBalance = (value) => formatMoneyValue(value || 0)

    const roleText = (value) => String(value || '').toLowerCase() === 'admin' ? '管理员' : '普通用户'
    const statusText = (value) => String(value || '').toLowerCase() === 'disabled' ? '已禁用' : '正常'
    const normalizeWechatCodeInput = (value) => String(value || '').replace(/\D/g, '').slice(0, 12)

    onMounted(() => {
      void Promise.all([loadProfile(), loadPublicSettings()])
    })

    onBeforeUnmount(() => {
      resetWechatScanState()
    })

    return {
      loading,
      updatingPhone,
      updatingProfile,
      sendingPhoneCode,
      unbindingWechat,
      wechatQrLoading,
      wechatQrCodeImageData,
      wechatStatusText,
      wechatRemainingSeconds,
      wechatTransportText,
      wechatBindSubMode,
      wechatCodeSubmitting,
      currentUser,
      phoneForm,
      profileForm,
      wechatCodeForm,
      publicAuthSettings,
      smsCooldownText: smsCooldown.buttonText,
      smsCooldownActive: smsCooldown.active,
      sendPhoneChangeCode,
      submitPhoneChange,
      submitProfile,
      startWechatBind,
      handleWechatBindSubModeChange,
      submitWechatCodeBind,
      unbindWechat,
      loadProfile,
      normalizeSMSCodeInput,
      normalizeWechatCodeInput,
      registerSourceText: buildRegisterSourceLabel,
      formatDateTime,
      formatBalance,
      ipLocations,
      phoneLocations,
      normalizeLocationKey,
      formatValueWithLocation,
      roleText,
      statusText,
    }
  },
  components: { MainlandPhoneInput },
})
</script>

<style scoped>
.profile-page {
  width: 100%;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.summary-card,
.page-card,
.profile-panel {
  border-radius: var(--console-radius, 12px);
}

.summary-card,
.page-card {
  border: 1px solid var(--console-border, #dce5e1);
  box-shadow: none;
}

.summary-card :deep(.ant-card-body) {
  min-height: 104px;
  display: flex;
  flex-direction: column;
  justify-content: center;
  padding: 18px;
}

.summary-label {
  color: var(--console-muted, #62766e);
  font-size: 13px;
}

.summary-value {
  margin-top: 10px;
  color: var(--console-text, #172b27);
  font-size: 21px;
  font-weight: 700;
  overflow-wrap: anywhere;
}

.summary-value--small {
  font-size: 16px;
  line-height: 1.5;
}

.page-title {
  margin: 0;
  color: var(--console-text, #172b27);
  font-size: 16px;
  font-weight: 600;
}

.page-subtitle,
.panel-subtitle {
  margin-top: 6px;
  color: var(--console-muted, #62766e);
  font-size: 13px;
  line-height: 1.7;
}

.location-value {
  display: inline-block;
  white-space: pre-line;
  line-height: 1.5;
  text-align: left;
}

.location-extra {
  display: block;
}

.profile-panel {
  height: 100%;
  min-width: 0;
  border: 1px solid var(--console-border, #dce5e1);
  background: var(--console-surface, #fff);
}

.panel-title {
  margin: 0;
  color: var(--console-text, #172b27);
  font-size: 16px;
  font-weight: 600;
}

.panel-heading--form {
  margin: 0 0 16px;
}

.panel-heading .panel-subtitle {
  margin-bottom: 0;
}

.profile-section-divider {
  height: 1px;
  margin: 18px 0;
  background: var(--console-border, #dce5e1);
}

.profile-field {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 0;
  border-bottom: 1px solid var(--console-border, #dce5e1);
  color: var(--console-text, #172b27);
  overflow-wrap: anywhere;
}

.profile-field:last-child {
  border-bottom: none;
}

.profile-field-label {
  color: var(--console-muted, #62766e);
  flex-shrink: 0;
}

.sms-code-row {
  display: flex;
  gap: 8px;
}

.sms-code-row :deep(.ant-input) {
  flex: 1;
  min-width: 0;
}

.wechat-bound-panel,
.wechat-bind-panel {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 14px;
  padding: 18px;
  border: 1px solid var(--console-border, #dce5e1);
  border-radius: 12px;
  background: var(--console-surface-secondary, #f8faf9);
}

.wechat-bound-time,
.wechat-binding-id,
.wechat-status,
.wechat-qrcode-empty {
  color: var(--console-muted, #62766e);
  font-size: 13px;
  text-align: center;
}

.wechat-binding-id {
  max-width: 100%;
  overflow-wrap: anywhere;
  color: var(--console-text, #172b27);
  font-weight: 600;
}

.wechat-binding-id--empty {
  color: var(--console-muted, #62766e);
  font-weight: 500;
}

.wechat-mode-switch {
  width: 100%;
  display: flex;
}

.wechat-mode-switch :deep(.ant-radio-button-wrapper) {
  flex: 1;
  text-align: center;
}

.wechat-code-field {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.wechat-code-field label {
  color: var(--console-text, #172b27);
  font-size: 14px;
}

.wechat-qrcode-frame {
  width: 220px;
  max-width: 100%;
  aspect-ratio: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  border: 1px solid var(--console-border, #dce5e1);
  border-radius: 12px;
  background: #fff;
}

.wechat-qrcode-frame img {
  width: 100%;
  height: 100%;
  object-fit: contain;
}

@media (max-width: 640px) {
  .profile-field {
    align-items: flex-start;
    gap: 12px;
  }

  .profile-field > :last-child {
    max-width: 65%;
    text-align: right;
  }

  .sms-code-row {
    flex-direction: column;
  }
}
</style>
