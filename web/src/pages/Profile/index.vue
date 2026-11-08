<template>
  <div
    ref="profileRoot" :key="formKey" class="console-page profile-page"
    @input.capture="renderHandlers.guardEvent" @paste.capture="renderHandlers.guardEvent"
    @change.capture="renderHandlers.guardEvent" @click.capture="renderHandlers.guardEvent"
    @keydown.capture="renderHandlers.guardEvent"
  >
    <ConsolePageHeading title="个人中心" description="查看账号状态，更新个人资料与登录绑定信息。">
      <template #actions>
      <a-button @click="renderHandlers.loadProfile" :loading="loading">刷新</a-button>
      </template>
    </ConsolePageHeading>

    <a-alert v-if="profileError" type="warning" show-icon :message="profileError" />
    <section v-if="currentUserFailure" data-profile-me-feedback>
      <RequestFailureAlert :key="currentUserFailureContextKey" :failure="currentUserFailure" :message="currentUserFailureText" :context-key="currentUserFailureContextKey" :is-current="isCurrentCurrentUserFailure" />
    </section>

    <ConsoleSummaryGrid>
        <a-card :bordered="false" class="summary-card">
          <div class="summary-label">账号状态</div>
          <div data-profile-private class="summary-value">{{ statusText(currentUser.status) }}</div>
        </a-card>
        <a-card :bordered="false" class="summary-card">
          <div class="summary-label">账号角色</div>
          <div data-profile-private class="summary-value">{{ roleText(currentUser.role) }}</div>
        </a-card>
        <a-card :bordered="false" class="summary-card">
          <div class="summary-label">账户余额</div>
          <div data-profile-private class="summary-value">¥ {{ formatBalance(currentUser.balance) }}</div>
        </a-card>
        <a-card :bordered="false" class="summary-card">
          <div class="summary-label">上次登录</div>
          <div data-profile-private class="summary-value summary-value--small">{{ formatDateTime(currentUser.lastLoginAt) }}</div>
        </a-card>
    </ConsoleSummaryGrid>

    <a-card :bordered="false" class="page-card" :loading="loading">
      <a-row :gutter="[16, 16]">
        <a-col :xs="24" :lg="10">
          <ConsolePanel class="profile-panel" title="基础信息" description="账号创建与最近登录情况。">
            <div class="profile-field">
              <span class="profile-field-label">用户名</span>
              <span data-profile-private>{{ currentUser.username || '-' }}</span>
            </div>
            <div class="profile-field">
              <span class="profile-field-label">昵称</span>
              <span data-profile-private>{{ currentUser.nickname || '-' }}</span>
            </div>
            <div class="profile-field">
              <span class="profile-field-label">注册来源</span>
              <span data-profile-private>{{ registerSourceText(currentUser.registerSource) }}</span>
            </div>
            <div class="profile-field">
              <span class="profile-field-label">注册时间</span>
              <span data-profile-private>{{ formatDateTime(currentUser.createdAt) }}</span>
            </div>
            <div class="profile-field">
              <span class="profile-field-label">登录 IP</span>
              <span data-profile-private class="location-value">{{ formatValueWithLocation(currentUser.lastLoginIp, ipLocations[normalizeLocationKey(currentUser.lastLoginIp)]) || '-' }}</span>
            </div>
            <div class="profile-section-divider"></div>
            <div class="panel-heading panel-heading--form">
              <h3 class="panel-title">修改用户名和昵称</h3>
              <p class="panel-subtitle">更新后使用新用户名登录。</p>
            </div>
            <a-form :name="fieldId('profile')" layout="vertical" :model="profileForm" @finish="renderHandlers.submitProfile">
              <a-form-item label="用户名" name="username" required>
                <a-input
                  :value="profileForm.username"
                  @update:value="renderHandlers.setUsername"
                  :disabled="!profileReady"
                  placeholder="只能使用中文、英文、数字、下划线和@"
                  :maxlength="255"
                />
              </a-form-item>
              <a-form-item label="昵称" name="nickname">
                <a-input
                  :value="profileForm.nickname"
                  @update:value="renderHandlers.setNickname"
                  :disabled="!profileReady"
                  placeholder="可选，最多255个字符"
                  :maxlength="255"
                />
              </a-form-item>
              <a-button type="primary" html-type="submit" :loading="updatingProfile" :disabled="!profileReady">
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
                  当前：<span data-profile-private class="location-value">{{ currentUser.phone ? formatValueWithLocation(currentUser.phone, phoneLocations[normalizeLocationKey(currentUser.phone)]) : '未绑定' }}</span>
                  <span data-profile-private v-if="currentUser.phoneBoundAt" class="location-extra">，绑定于 {{ formatDateTime(currentUser.phoneBoundAt) }}</span>
                </div>
              </div>
            </template>
            <a-alert
              v-if="!publicAuthSettings.smsLoginEnabled"
              type="warning"
              show-icon
              message="管理员尚未启用手机号，当前无法更换手机号。"
            />
            <a-form v-else layout="vertical" :model="phoneForm" @finish="renderHandlers.submitPhoneChange">
              <a-form-item label="新手机号" :html-for="fieldId('phone')" required>
                <MainlandPhoneInput
                  :value="phoneForm.phone"
                  @update:value="renderHandlers.setPhone"
                  :id="fieldId('phone')"
                  :disabled="!profileReady"
                />
              </a-form-item>
              <a-form-item label="新手机号验证码" :html-for="fieldId('sms-code')" required>
                <div class="sms-code-row">
                  <a-input
                    :value="phoneForm.smsCode"
                    @update:value="renderHandlers.setSMSCode"
                    :id="fieldId('sms-code')"
                    :disabled="!profileReady"
                    placeholder="请输入短信验证码"
                    :maxlength="6"
                    inputmode="numeric"
                  />
                  <a-button
                    :loading="sendingPhoneCode"
                    :disabled="!profileReady || sendingPhoneCode || smsCooldownActive || !phoneForm.phone"
                    @click="renderHandlers.sendPhoneChangeCode"
                  >
                    {{ smsCooldownText }}
                  </a-button>
                </div>
              </a-form-item>
              <a-button
                type="primary"
                html-type="submit"
                :loading="updatingPhone"
                :disabled="!profileReady || updatingPhone || !phoneForm.phone || !phoneForm.smsCode"
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
        <div data-profile-private class="wechat-binding-id">
          微信ID：{{ currentUser.wechatId }}
        </div>
        <div data-profile-private class="wechat-bound-time">
          绑定时间：{{ formatDateTime(currentUser.wechatBoundAt) }}
        </div>
        <a-popconfirm
          title="确定解绑当前微信吗？解绑后将不能继续使用该微信快捷登录。"
          ok-text="确认解绑"
          cancel-text="取消"
          @confirm="wechatHandlers.unbindWechat"
        >
          <a-button danger :disabled="!profileReady" :loading="unbindingWechat">解绑微信</a-button>
        </a-popconfirm>
      </div>

      <div v-else :key="wechatModeKey" class="wechat-bind-panel">
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
            :value="wechatBindSubMode"
            @update:value="wechatHandlers.setMode"
            button-style="solid"
            class="wechat-mode-switch"
          >
            <a-radio-button value="scan">微信扫描</a-radio-button>
            <a-radio-button value="code">微信验证码</a-radio-button>
          </a-radio-group>

          <template v-if="wechatRenderMode === 'scan'">
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
            <section v-if="wechatQrFailure" data-profile-wechat-feedback>
              <RequestFailureAlert :key="wechatQrFailureContextKey" :failure="wechatQrFailure" :message="wechatQrFailureText" :busy="wechatQrLoading" :context-key="wechatQrFailureContextKey" :is-current="isCurrentWechatQrFailure" />
            </section>
            <a-button block :disabled="!profileReady" :loading="wechatQrLoading" @click="wechatHandlers.startWechatBind">刷新绑定二维码</a-button>
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
                :value="wechatCodeForm.code"
                @update:value="wechatHandlers.setCode"
                :disabled="!profileReady"
                placeholder="请输入微信验证码"
                inputmode="numeric"
                :maxlength="12"
              />
            </div>
            <a-button
              type="primary"
              block
              :loading="wechatCodeSubmitting"
              :disabled="!profileReady || wechatCodeSubmitting || !wechatCodeForm.code || !publicAuthSettings.wechatCodeStaticQrcodeUrl"
              @click="wechatHandlers.submitWechatCodeBind"
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
import { computed, defineComponent, getCurrentInstance, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, useId, watch } from 'vue'
import { useRouter } from 'vue-router'
import RequestFailureAlert from '@/components/RequestFailureAlert.vue'
import { ensureRequestFailure } from '@/utils/requestFailureView.mjs'
import { message } from 'ant-design-vue'
import {
  bindCurrentUserWechat, bindCurrentUserWechatByCode, createCaptchaChallenge, createWechatLoginQRCode,
  getCurrentUser, updateCurrentUserProfile, getPublicRegisterStatus, getWechatLoginStatus,
  sendUserPhoneChangeSMSCode, unbindCurrentUserWechat, updateCurrentUserPhone,
  verifyCaptchaChallenge, verifyRemoteCaptcha, getMyLocations,
} from '@/api/TikuComponentApi'
import { ensureAuthCaptchaToken } from '@/utils/captchaDialog.mjs'
import { normalizeSMSCodeInput, ensurePhoneBeforeSMSCaptcha } from '@/utils/captchaHelpers.mjs'
import MainlandPhoneInput from '@/components/MainlandPhoneInput.vue'
import { buildRegisterSourceLabel } from '@/utils/userPortalHelpers.mjs'
import { formatMoneyValue } from '@/utils/moneyFormat.mjs'
import { captureCurrentUserLease, getCurrentUserLogoutEpoch, getStoredCurrentUser, setStoredCurrentUser, useCurrentUserDisplay } from '@/utils/currentUser.js'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { useSmsCooldown } from '@/utils/smsCooldown.js'
import { createWechatQrStatusController } from '@/utils/wechatQrStatus.mjs'
import { extractLocationAddresses, normalizeLocationKey, formatValueWithLocation } from '@/utils/locationHelpers.mjs'

const CaptchaSceneSMSSendChangePhone = 'sms_send_change_phone'
const unknownOutcomeText = '操作结果未确认，请先核对当前状态，勿直接重复提交。'
const publicReasons = new Map([
  ['请输入手机号', '请输入手机号'], ['请输入11位手机号', '请输入11位手机号'],
  ['手机号格式不正确', '请输入11位手机号'], ['请输入短信验证码', '请输入短信验证码'],
  ['验证码错误或已过期', '验证码错误或已过期，请重新获取'],
  ['短信验证码错误或已过期', '短信验证码错误或已过期，请重新获取'],
  ['请输入用户名', '请输入用户名'], ['用户名已存在', '用户名已存在'],
  ['用户名已经被使用', '用户名已经被使用'], ['昵称长度不能超过255个字符', '昵称长度不能超过255个字符'],
  ['手机号和验证码不能为空', '手机号和验证码不能为空'],
  ['用户名、手机号或微信已绑定', '用户名、手机号或微信已绑定'],
  ['该手机号已被绑定', '该手机号已被绑定'], ['手机号已被绑定', '手机号已被绑定'],
  ['请先完成人机验证', '请先完成人机验证'], ['图形验证码错误', '图形验证码错误，请重新验证'],
  ['图形验证码校验失败', '图形验证码校验失败，请重新验证'],
  ['人机验证不存在或已过期', '人机验证已过期，请重新验证'],
  ['人机验证已过期，请重新验证', '人机验证已过期，请重新验证'],
  ['人机验证服务不可用', '人机验证服务暂不可用，请稍后再试'],
  ['远程验证码校验失败', '人机验证未通过，请重新验证'],
  ['发送过于频繁，请稍后再试', '请求过于频繁，请稍后再试'],
  ['手机号功能未启用', '手机号功能未启用，请联系管理员'],
  ['短信配置未完成', '短信服务尚未配置完成，请联系管理员'],
  ['请输入微信验证码', '请输入微信验证码'],
  ['微信验证码不能为空', '请输入微信验证码'], ['当前账号已绑定微信，请先解绑', '当前账号已绑定微信，请先解绑'],
  ['微信验证码已过期，请重新获取', '微信验证码已过期，请重新获取'],
  ['微信验证码无效，请重新获取', '微信验证码无效，请重新获取'],
  ['微信登录未开启或配置未完成', '微信登录未启用或配置未完成，请联系管理员'],
  ['微信扫码登录未开启或配置未完成', '微信扫码登录未启用或配置未完成，请联系管理员'],
  ['微信验证码登录未开启或配置未完成', '微信验证码登录未启用或配置未完成，请联系管理员'],
  ['请先设置密码或绑定手机号后再解绑微信', '请先设置密码或绑定手机号后再解绑微信'],
])
const publicFailureText = (failure, fallback) => {
  if (failure?.code === 'UNKNOWN_OUTCOME') return unknownOutcomeText
  if (failure?.name === 'UserCancelledCaptchaError') return '已取消人机验证，尚未提交本次请求'
  const known = publicReasons.get(failure?.message)
  if (known) return known
  const status = Number(failure?.status ?? failure?.response?.status)
  if (status === 429) return '请求过于频繁，请稍后再试'
  if (status === 401) return '登录状态未通过验证，请刷新个人信息后重试'
  if (status === 403) return '当前操作不可用，请核对账号状态或联系管理员'
  return fallback
}
const clearLocationMap = target => Object.keys(target).forEach(key => { delete target[key] })
const actorKey = user => JSON.stringify([currentUserIdentity(user), String(user?.role || '').trim().toLowerCase()])
const statusKey = user => String(user?.status || '').trim().toLowerCase()

export default defineComponent({
  name: 'UserProfile',
  setup() {
    const fieldIdPrefix = `profile-${useId()}`
    const fieldId = field => `${fieldIdPrefix}-${field}`
    const router = useRouter()
    const instance = getCurrentInstance()
    const route = () => router?.currentRoute?.value || instance?.proxy?.$route || {}
    const routeKey = () => JSON.stringify([route().fullPath, route().path, route().query, route().params, route().hash])
    const mountedPath = route().path
    const { currentUser: displayCurrentUser } = useCurrentUserDisplay()
    const profileRoot = ref(null)
    const renderEpoch = ref(0), formKey = ref(0), wechatModeEpoch = ref(0), wechatModeKey = ref(0)
    const profileUser = shallowRef(null)
    const currentUser = computed(() => profileUser.value || {})
    const profileReady = ref(false), profileError = ref('')
    const loading = ref(false), updatingPhone = ref(false), updatingProfile = ref(false)
    const sendingPhoneCode = ref(false), unbindingWechat = ref(false), wechatQrLoading = ref(false)
    const wechatQrCodeImageData = ref(''), wechatLoginToken = ref('')
    const wechatStatusText = ref('请使用微信扫码完成绑定')
    const wechatRemainingSeconds = ref(null), wechatTransportText = ref('')
    const wechatBindSubMode = ref('scan'), wechatRenderMode = ref('scan'), wechatCodeSubmitting = ref(false)
    const profileForm = reactive({ username: '', nickname: '' })
    const phoneForm = reactive({ phone: '', smsCode: '', captchaToken: '' })
    const wechatCodeForm = reactive({ code: '' })
    const ipLocations = reactive({}), phoneLocations = reactive({})
    const publicAuthSettings = ref({
      captchaProvider: 'remote', captchaApiBaseUrl: 'https://captcha.api.zelly.cn', captchaType: 'RANDOM',
      smsLoginEnabled: false, wechatLoginEnabled: false, wechatScanLoginEnabled: false,
      wechatCodeLoginEnabled: false, wechatDefaultLoginMode: 'scan', wechatCodeStaticQrcodeUrl: '',
    })
    const smsCooldown = useSmsCooldown()
    const attempts = new Map()
    const draftEpoch = { profile: 0, phone: 0, wechat: 0 }
    let active = true, internalInputChange = false, internalModeChange = false, retiring = false
    let ownCommit = null, viewLease = captureCurrentUserLease(), dataGeneration = 0, locationLoadGeneration = 0
    let observedLogoutEpoch = getCurrentUserLogoutEpoch(), metadataSequence = 0
    let profileDraftDirty = false
    const currentUserFailure = shallowRef(null)
    const currentUserFailureContextKey = ref(0)
    const currentUserFailureText = '获取个人状态失败，已确认的资料与未保存内容保持不变，请刷新重试'
    let currentUserFailureOwner = () => false
    const clearCurrentUserFailure = () => {
      currentUserFailure.value = null
      currentUserFailureOwner = () => false
      currentUserFailureContextKey.value += 1
    }
    const captureCurrentUserFailureOwner = owner => {
      const { render, route: ownerRoute, data } = owner
      const context = currentUserFailureContextKey.value
      // Initial /me failure is legal even before any actor has been confirmed.
      return () => context === currentUserFailureContextKey.value && viewCurrent(render)
        && ownerRoute === routeKey() && data === dataGeneration
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
      const { render, route: ownerRoute, mode, actor } = owner
      const status = statusKey(profileUser.value), context = wechatQrFailureContextKey.value
      // Reuse the current view lease: benign same-actor metadata may transfer it.
      return () => context === wechatQrFailureContextKey.value && viewCurrent(render)
        && ownerRoute === routeKey() && profileReady.value
        && actor === actorKey(profileUser.value) && status === statusKey(profileUser.value)
        && mode === wechatModeEpoch.value && wechatBindSubMode.value === 'scan'
    }
    const showWechatQrFailure = (failure, owns, fallback) => {
      if (!owns() || failure?.name === 'AbortError' || failure?.code === 'ERR_CANCELED') return
      wechatQrFailureOwner = owns
      wechatQrFailureText.value = publicFailureText(failure, fallback)
      wechatQrFailure.value = ensureRequestFailure(failure)
    }
    const isCurrentWechatQrFailure = (failure, context) => wechatQrFailureOwner() && Boolean(failure)
      && failure === wechatQrFailure.value && context === wechatQrFailureContextKey.value
    const pageActive = () => active && route().path === mountedPath
    const withInternalInputs = action => {
      internalInputChange = true
      try { action() } finally { internalInputChange = false }
    }
    const closeAttempt = owner => {
      if (!owner) return
      owner.controller.abort()
      owner.qrStatus?.stop()
      owner.lease.release()
      owner.token = ''; owner.expectedUser = ''
      if (owner.snapshot) Object.keys(owner.snapshot).forEach(key => { owner.snapshot[key] = '' })
      if (attempts.get(owner.kind) !== owner) return
      attempts.delete(owner.kind)
      if (owner.kind === 'profile-read') loading.value = false
      if (owner.kind === 'profile-save') updatingProfile.value = false
      if (owner.kind === 'phone-send') sendingPhoneCode.value = false
      if (owner.kind === 'phone-change') updatingPhone.value = false
      if (owner.kind === 'wechat-scan') wechatQrLoading.value = false
      if (owner.kind === 'wechat-code') wechatCodeSubmitting.value = false
      if (owner.kind === 'wechat-unbind') unbindingWechat.value = false
    }
    const clearWechatScan = () => {
      profileRoot.value?.querySelectorAll('.wechat-qrcode-frame img').forEach(image => image.removeAttribute('src'))
      wechatQrCodeImageData.value = ''; wechatLoginToken.value = ''
      wechatRemainingSeconds.value = null; wechatTransportText.value = ''
    }
    const scrubInputs = selector => {
      profileRoot.value?.querySelectorAll(selector).forEach(input => {
        input.value = ''; input.defaultValue = ''; input.removeAttribute('value')
        // No synthetic input: Antd's queued input-ref work must not outlive disposal.
      })
    }
    const retireView = () => {
      if (retiring) return
      retiring = true
      try {
        clearCurrentUserFailure()
        clearWechatQrFailure()
        renderEpoch.value += 1
        const epoch = renderEpoch.value
        // Empty controlled values drain before disposing Antd's accepted state.
        nextTick().then(() => nextTick()).then(() => {
          if (active && epoch === renderEpoch.value) {
            // An actor/route retirement may interrupt an in-progress mode drain.
            // The replacement owns a matched mode key, never a permanently stale one.
            wechatRenderMode.value = wechatBindSubMode.value
            wechatModeKey.value = wechatModeEpoch.value
            formKey.value = epoch
          }
        })
        for (const owner of [...attempts.values()]) closeAttempt(owner)
        clearWechatScan()
        withInternalInputs(() => {
          profileForm.username = ''; profileForm.nickname = ''
          phoneForm.phone = ''; phoneForm.smsCode = ''; phoneForm.captchaToken = ''
          wechatCodeForm.code = ''
        })
        scrubInputs('input:not([type="radio"]):not([type="checkbox"]), textarea')
        profileRoot.value?.querySelectorAll('[data-profile-private]').forEach(node => { node.textContent = '' })
        profileUser.value = null; profileReady.value = false; profileDraftDirty = false
        profileError.value = '账号上下文已变化，请刷新个人信息后继续操作'
        clearLocationMap(ipLocations); clearLocationMap(phoneLocations)
        dataGeneration += 1; locationLoadGeneration += 1
        smsCooldown.stop(); smsCooldown.remaining.value = 0
        wechatStatusText.value = '请使用微信扫码完成绑定'
        viewLease?.release()
        viewLease = pageActive() ? captureCurrentUserLease() : null
      } finally { retiring = false }
    }
    const viewCurrent = (epoch = renderEpoch.value) => {
      if (!pageActive() || epoch !== renderEpoch.value) return false
      if (!viewLease || viewLease.isRetired()) { retireView(); return false }
      return true
    }
    const isCurrent = owner => {
      if (!owner || attempts.get(owner.kind) !== owner || !pageActive() ||
        owner.render !== renderEpoch.value || owner.route !== routeKey() ||
        (owner.draft && owner.input !== draftEpoch[owner.draft]) ||
        (owner.mode !== undefined && owner.mode !== wechatModeEpoch.value)) return false
      if (owner.lease.isRetired()) { retireView(); return false }
      return !owner.mutation || (profileReady.value && owner.actor === actorKey(profileUser.value))
    }
    const beginAttempt = (kind, { mutation = false, draft, mode = false } = {}) => {
      if (!viewCurrent() || formKey.value !== renderEpoch.value || attempts.has(kind)) return null
      // Initial /me is always a legal read. Local identity/id/admin is never its permission gate.
      // A mutation owns a successfully confirmed server actor. Unavailable local
      // storage is unknown, not evidence of logout or a different actor.
      if (mutation && !profileReady.value) {
        message.warning('请先刷新并确认当前账号信息后继续操作')
        return null
      }
      const owner = {
        kind, mutation, draft, input: draftEpoch[draft], render: renderEpoch.value, route: routeKey(),
        actor: actorKey(profileUser.value), lease: captureCurrentUserLease(), controller: new AbortController(),
        mode: mode ? wechatModeEpoch.value : undefined, data: dataGeneration, profileDraft: draftEpoch.profile,
      }
      attempts.set(kind, owner)
      return owner
    }
    const reportError = (owner, failure, fallback) => {
      if (!isCurrent(owner) || failure?.name === 'AbortError' || failure?.code === 'ERR_CANCELED') return
      const text = publicFailureText(failure, fallback)
      if (text === unknownOutcomeText) message.warning(text)
      else message.error(text)
    }
    const applyCurrentUser = (user, owner, fields = null) => {
      if (!isCurrent(owner)) return false
      if (!user || typeof user !== 'object' || Array.isArray(user) || !Object.keys(user).length) {
        if (owner.mutation) message.warning('操作已提交，但当前资料尚未确认，请先刷新核对，勿重复提交。')
        return false
      }
      if (profileUser.value?.id != null && user.id != null && String(profileUser.value.id) !== String(user.id)) {
        retireView(); return false
      }
      if (profileUser.value && user.role != null && String(user.role).trim().toLowerCase() !== String(profileUser.value.role || '').trim().toLowerCase()) {
        retireView(); return false
      }
      // Confirmed data supersedes an old read diagnostic, not proof of GET recovery.
      clearCurrentUserFailure()
      // Mutation DTOs may be concurrent full snapshots. Merge only their changed
      // fields, so a phone response cannot revert a separately saved nickname.
      const nextUser = fields ? { ...profileUser.value } : { ...user }
      if (fields) fields.forEach(key => { if (Object.prototype.hasOwnProperty.call(user, key)) nextUser[key] = user[key] })
      owner.expectedUser = JSON.stringify(nextUser)
      ownCommit = { owner, expectedUser: owner.expectedUser, observed: false }
      let writeFailed = false
      try { setStoredCurrentUser(nextUser) } catch { writeFailed = true } finally { ownCommit = null }
      if (attempts.get(owner.kind) !== owner || !pageActive() || owner.render !== renderEpoch.value || owner.route !== routeKey()) return false
      if (!writeFailed && JSON.stringify(getStoredCurrentUser()) !== owner.expectedUser) { retireView(); return false }
      if (!writeFailed) {
        // This exact own write changes shared display generation. Transfer every
        // independent still-owned action, not just the completing save.
        viewLease?.release(); viewLease = captureCurrentUserLease()
        for (const pending of attempts.values()) {
          pending.lease.release(); pending.lease = captureCurrentUserLease()
        }
      } else if (!isCurrent(owner)) return false
      for (const pending of attempts.values()) pending.actor = actorKey(nextUser)
      profileUser.value = nextUser
      profileReady.value = true
      if (!fields || fields.includes('username')) {
        if ((owner.kind === 'profile-save' || !profileDraftDirty) && owner.profileDraft === draftEpoch.profile) {
          withInternalInputs(() => { profileForm.username = nextUser.username || ''; profileForm.nickname = nextUser.nickname || '' })
          profileDraftDirty = false
        }
      }
      if (fields) dataGeneration += 1
      if (writeFailed) message.warning(owner.mutation
        ? '服务器已完成本次操作，但本地账号状态未保存，请检查浏览器存储并刷新核对。'
        : '当前信息已读取，但本地账号状态未保存，请检查浏览器存储并刷新核对。')
      return true
    }
    const loadProfileLocations = async () => {
      closeAttempt(attempts.get('locations'))
      const owner = beginAttempt('locations')
      if (!owner) return
      const generation = ++locationLoadGeneration
      clearLocationMap(ipLocations); clearLocationMap(phoneLocations)
      try {
        if (!isCurrent(owner)) return
        const res = await getMyLocations()
        if (!isCurrent(owner) || generation !== locationLoadGeneration || owner.data !== dataGeneration) return
        const ips = extractLocationAddresses(res, 'ip'), phones = extractLocationAddresses(res, 'phone')
        Object.keys(ips).forEach(key => { ipLocations[normalizeLocationKey(key)] = ips[key] })
        Object.keys(phones).forEach(key => { phoneLocations[normalizeLocationKey(key)] = phones[key] })
      } catch { /* Optional display enhancement; keep the original IP/phone. */ }
      finally { closeAttempt(owner) }
    }
    const loadProfile = async () => {
      closeAttempt(attempts.get('profile-read'))
      const owner = beginAttempt('profile-read')
      if (!owner) return
      loading.value = true; profileError.value = ''
      clearCurrentUserFailure()
      const ownsFailure = captureCurrentUserFailureOwner(owner)
      try {
        if (!isCurrent(owner)) return
        let res
        try {
          res = await getCurrentUser({ errorFeedback: 'local' })
        } catch (error) {
          if (isCurrent(owner)) showCurrentUserFailure(error, ownsFailure)
          throw error
        }
        if (!isCurrent(owner) || owner.data !== dataGeneration) return
        if (res?.status !== 200 || !res.data || typeof res.data !== 'object' || !Object.keys(res.data).length) {
          profileError.value = '获取个人状态失败，已确认的资料与未保存内容保持不变，请刷新重试'
          showCurrentUserFailure(res, ownsFailure)
          reportError(owner, res, '获取个人状态失败，请稍后刷新重试')
          return
        }
        if (applyCurrentUser(res.data, owner)) void loadProfileLocations()
      } catch (error) {
        if (!isCurrent(owner)) return
        profileError.value = '获取个人状态失败，已确认的资料与未保存内容保持不变，请刷新重试'
        reportError(owner, error, '获取个人状态失败，请稍后刷新重试')
      } finally { closeAttempt(owner) }
    }
    const chooseWechatBindSubMode = (settings = publicAuthSettings.value) => {
      if (!settings.wechatLoginEnabled) return 'scan'
      const defaultMode = settings.wechatDefaultLoginMode === 'code' ? 'code' : 'scan'
      if (defaultMode === 'code' && settings.wechatCodeLoginEnabled) return 'code'
      if (defaultMode === 'scan' && settings.wechatScanLoginEnabled) return 'scan'
      return settings.wechatScanLoginEnabled ? 'scan' : settings.wechatCodeLoginEnabled ? 'code' : 'scan'
    }
    const loadPublicSettings = async () => {
      const owner = beginAttempt('settings')
      if (!owner) return
      try {
        if (!isCurrent(owner)) return
        const res = await getPublicRegisterStatus()
        if (!isCurrent(owner) || res?.status !== 200) return
        publicAuthSettings.value = {
          captchaProvider: res?.data?.captchaProvider || 'remote',
          captchaApiBaseUrl: res?.data?.captchaApiBaseUrl || 'https://captcha.api.zelly.cn', captchaType: res?.data?.captchaType || 'RANDOM',
          smsLoginEnabled: !!res?.data?.smsLoginEnabled, wechatLoginEnabled: !!res?.data?.wechatLoginEnabled,
          wechatScanLoginEnabled: !!res?.data?.wechatScanLoginEnabled, wechatCodeLoginEnabled: !!res?.data?.wechatCodeLoginEnabled,
          wechatDefaultLoginMode: res?.data?.wechatDefaultLoginMode || 'scan', wechatCodeStaticQrcodeUrl: res?.data?.wechatCodeStaticQrcodeUrl || '',
        }
        internalModeChange = true
        try { wechatBindSubMode.value = chooseWechatBindSubMode(); wechatRenderMode.value = wechatBindSubMode.value } finally { internalModeChange = false }
      } catch { /* Public settings failure must not discard confirmed profile data. */ }
      finally { closeAttempt(owner) }
    }
    const ensureCaptchaToken = (scene, targetForm, owner) => ensureAuthCaptchaToken({
      scene, targetForm, publicSettings: { ...publicAuthSettings.value },
      createCaptchaChallenge, verifyCaptchaChallenge, verifyRemoteCaptcha,
      signal: owner.controller.signal, isCurrent: () => isCurrent(owner),
    })
    const sendPhoneChangeCode = async () => {
      if (!publicAuthSettings.value.smsLoginEnabled || smsCooldown.active.value) return
      const owner = beginAttempt('phone-send', { mutation: true, draft: 'phone' })
      if (!owner) return
      sendingPhoneCode.value = true
      try {
        const form = owner.snapshot = { phone: ensurePhoneBeforeSMSCaptcha(phoneForm.phone), captchaToken: '' }
        withInternalInputs(() => { phoneForm.phone = form.phone })
        if (!isCurrent(owner)) return
        owner.phase = 'captcha'
        await ensureCaptchaToken(CaptchaSceneSMSSendChangePhone, form, owner)
        if (!isCurrent(owner)) return
        const payload = { phone: form.phone, captchaToken: form.captchaToken }
        owner.phase = 'issued'
        owner.issued = true
        const res = await sendUserPhoneChangeSMSCode(payload)
        if (!isCurrent(owner)) return
        if (res?.status === 202) {
          message.warning('短信发送结果待核对，请先等待短信，勿重复发送'); smsCooldown.start(); return
        }
        if (res?.status !== 200) { reportError(owner, res, '短信发送未完成，请稍后再试'); return }
        message.success('发送成功，请注意查收'); smsCooldown.start()
      } catch (error) {
        reportError(owner, error, owner.phase === 'issued' ? unknownOutcomeText : owner.phase === 'captcha' ? '人机验证未完成，请重新验证' : '请输入11位手机号')
      } finally { closeAttempt(owner) }
    }
    const submitPhoneChange = async () => {
      if (!publicAuthSettings.value.smsLoginEnabled) return
      const owner = beginAttempt('phone-change', { mutation: true, draft: 'phone' })
      if (!owner) return
      updatingPhone.value = true
      try {
        const payload = owner.snapshot = { phone: ensurePhoneBeforeSMSCaptcha(phoneForm.phone), smsCode: phoneForm.smsCode }
        if (!payload.smsCode) { message.warning('请输入短信验证码'); return }
        withInternalInputs(() => { phoneForm.phone = payload.phone })
        if (!isCurrent(owner)) return
        owner.phase = 'issued'
        owner.issued = true
        const res = await updateCurrentUserPhone({ ...payload })
        if (!isCurrent(owner)) return
        if (res?.status !== 200) { reportError(owner, res, '更换手机号未完成，请核对验证码后再操作'); return }
        if (!applyCurrentUser(res.data, owner, ['phone', 'phoneBoundAt'])) return
        message.success('手机号更换成功')
        const pendingSend = attempts.get('phone-send')
        if (!pendingSend?.issued) closeAttempt(pendingSend)
        // The successful change supersedes the old phone draft, while an
        // already-sent SMS still owns its dedup/busy lane until it settles.
        draftEpoch.phone += 1
        withInternalInputs(() => { phoneForm.phone = ''; phoneForm.smsCode = ''; phoneForm.captchaToken = '' })
        scrubInputs('.sms-code-row input, input[autocomplete="tel-national"]')
        void loadProfileLocations()
      } catch (error) { reportError(owner, error, owner.phase === 'issued' ? unknownOutcomeText : '请输入11位手机号') }
      finally { closeAttempt(owner) }
    }
    const submitProfile = async () => {
      const owner = beginAttempt('profile-save', { mutation: true, draft: 'profile' })
      if (!owner) return
      const payload = owner.snapshot = { username: profileForm.username, nickname: profileForm.nickname }
      updatingProfile.value = true
      try {
        if (!isCurrent(owner)) return
        owner.issued = true
        const res = await updateCurrentUserProfile({ ...payload })
        if (!isCurrent(owner)) return
        if (res?.status !== 200) { reportError(owner, res, '保存资料未完成，请核对用户名和昵称后重试'); return }
        if (applyCurrentUser(res.data, owner, ['username', 'nickname'])) message.success('资料保存成功')
      } catch (error) { reportError(owner, error, unknownOutcomeText) }
      finally { closeAttempt(owner) }
    }
    const resolveWechatBindStatus = async (payload, owner, token) => {
      if (!isCurrent(owner) || owner.resolving || owner.token !== token || wechatLoginToken.value !== token) return false
      owner.resolving = true
      let completed = false, retrySafe = true
      try {
        let resolved = payload || {}
        if (resolved.status === 'need_bind' && !resolved.bindToken) {
          if (!isCurrent(owner)) return false
          const res = await getWechatLoginStatus({ loginToken: token, purpose: 'bind' })
          if (!isCurrent(owner)) return false
          if (res?.status !== 200) { reportError(owner, res, '微信绑定状态未确认，请刷新个人信息核对后再操作'); return false }
          resolved = res.data || {}
        }
        if (resolved.status === 'need_bind') {
          if (!resolved.bindToken) { wechatStatusText.value = '微信绑定状态未确认，请刷新个人信息核对后再操作'; return false }
          wechatStatusText.value = '扫码成功，正在绑定当前账号'
          if (!isCurrent(owner)) return false
          owner.phase = 'issued'
          owner.issued = true
          // A failed read may retry. An already-issued write with unknown
          // outcome must never be resubmitted by the status controller.
          retrySafe = false
          const res = await bindCurrentUserWechat({ bindToken: resolved.bindToken })
          if (!isCurrent(owner)) return false
          if (res?.status !== 200) {
            retrySafe = [400, 401, 403, 404, 409, 422].includes(Number(res?.status))
            reportError(owner, res, retrySafe ? '微信绑定未完成，请核对当前状态后再操作' : unknownOutcomeText)
            wechatStatusText.value = '绑定未完成，请先核对当前状态'
            return false
          }
          if (applyCurrentUser(res.data, owner, ['wechatId', 'wechatBoundAt'])) {
            message.success('微信绑定成功')
            completed = true
            return true
          }
        } else if (resolved.status === 'success') {
          wechatStatusText.value = '该微信已绑定其他账号，请先在该账号中解绑'
          completed = true
          return true
        }
      } catch (error) { reportError(owner, error, owner.phase === 'issued' ? unknownOutcomeText : '微信绑定状态未确认，请刷新个人信息核对后再操作') }
      finally {
        owner.resolving = false
        if (completed || !retrySafe) {
          if (isCurrent(owner)) clearWechatScan()
          closeAttempt(owner)
        }
      }
      return false
    }
    const startWechatBind = async () => {
      if (wechatBindSubMode.value !== 'scan' || !publicAuthSettings.value.wechatLoginEnabled || !publicAuthSettings.value.wechatScanLoginEnabled || currentUser.value.wechatId) return
      if (wechatQrLoading.value || attempts.has('wechat-code') || attempts.has('wechat-unbind') || attempts.get('wechat-scan')?.resolving) return
      closeAttempt(attempts.get('wechat-scan')); clearWechatScan()
      const owner = beginAttempt('wechat-scan', { mutation: true, mode: true })
      if (!owner) return
      clearWechatQrFailure()
      const ownsFeedback = captureWechatQrFailureOwner(owner)
      wechatQrLoading.value = true; wechatStatusText.value = '正在创建绑定二维码'
      try {
        if (!isCurrent(owner)) return
        const res = await createWechatLoginQRCode({ errorFeedback: 'local' })
        if (!isCurrent(owner)) return
        if (res?.status !== 200 || !res?.data?.qrcodeImageData || !res?.data?.loginToken) {
          reportError(owner, res, '二维码创建未完成，请稍后重试'); showWechatQrFailure(res, ownsFeedback, '二维码创建未完成，请稍后重试'); closeAttempt(owner); return
        }
        const token = owner.token = res.data.loginToken
        wechatQrCodeImageData.value = res.data.qrcodeImageData; wechatLoginToken.value = token
        wechatStatusText.value = '请使用微信扫码确认绑定'
        // Every native callback captures this operation and exact token, in
        // addition to the existing controller's generation/expiry/backoff guard.
        const owns = () => isCurrent(owner) && owner.token === token && wechatLoginToken.value === token
        owner.qrStatus = createWechatQrStatusController({
          checkStatus: async () => {
            if (!owns()) return null
            const response = await getWechatLoginStatus({ loginToken: token, purpose: 'bind' })
            return owns() ? response : null
          },
          onTransport: transport => { if (owns()) wechatTransportText.value = transport === 'websocket' ? '实时连接' : '轮询中' },
          onRemaining: seconds => { if (owns()) wechatRemainingSeconds.value = seconds },
          onExpired: () => {
            if (!owns()) return
            clearWechatScan(); wechatStatusText.value = '二维码已过期，请刷新'; closeAttempt(owner)
          },
          onError: () => { if (owns()) wechatStatusText.value = '微信绑定状态未确认，正在重试；请勿重复绑定。' },
          onStatus: async (status, payload) => {
            if (!owns()) return
            if (status === 'pending') wechatStatusText.value = '请使用微信扫码确认绑定'
            else if (status === 'scanned') wechatStatusText.value = '已扫描，请在微信中确认'
            else if (status === 'success' || status === 'need_bind') return resolveWechatBindStatus(payload, owner, token)
          },
        })
        if (owns()) owner.qrStatus.start({ loginToken: token, expireSeconds: res.data.expireSeconds, expiresAtUnix: res.data.expiresAtUnix })
      } catch (error) {
        if (isCurrent(owner)) { clearWechatScan(); wechatStatusText.value = '二维码创建结果未确认，请先核对后再操作'; reportError(owner, error, '二维码创建结果未确认，请先核对，勿直接重复操作'); showWechatQrFailure(error, ownsFeedback, '二维码创建结果未确认，请先核对，勿直接重复操作') }
        closeAttempt(owner)
      } finally { if (isCurrent(owner)) wechatQrLoading.value = false }
    }
    const handleWechatBindSubModeChange = () => {
      clearWechatQrFailure()
      wechatModeEpoch.value += 1
      const modeEpoch = wechatModeEpoch.value, epoch = renderEpoch.value
      for (const kind of ['wechat-scan', 'wechat-code']) {
        const owner = attempts.get(kind)
        // A sent bind cannot be withdrawn by switching the presentation mode.
        // Keep its lane occupied until the real promise settles, but retire paint.
        if (owner?.issued) { owner.controller.abort(); owner.qrStatus?.stop() }
        else closeAttempt(owner)
      }
      clearWechatScan()
      withInternalInputs(() => { wechatCodeForm.code = '' })
      scrubInputs('#profile-wechat-code')
      wechatStatusText.value = wechatBindSubMode.value === 'scan' ? '请使用微信扫码完成绑定' : '请输入微信验证码'
      nextTick().then(() => nextTick()).then(() => {
        if (active && renderEpoch.value === epoch && wechatModeEpoch.value === modeEpoch) {
          wechatRenderMode.value = wechatBindSubMode.value
          wechatModeKey.value = modeEpoch
        }
      })
    }
    const submitWechatCodeBind = async () => {
      if (wechatBindSubMode.value !== 'code' || !publicAuthSettings.value.wechatLoginEnabled || !publicAuthSettings.value.wechatCodeLoginEnabled || currentUser.value.wechatId || attempts.has('wechat-unbind') || attempts.get('wechat-scan')?.resolving) return
      if (!wechatCodeForm.code) { if (viewCurrent()) message.warning('请输入微信验证码'); return }
      const owner = beginAttempt('wechat-code', { mutation: true, draft: 'wechat', mode: true })
      if (!owner) return
      const payload = owner.snapshot = { code: wechatCodeForm.code }
      wechatCodeSubmitting.value = true
      try {
        if (!isCurrent(owner)) return
        owner.issued = true
        const res = await bindCurrentUserWechatByCode({ ...payload })
        if (!isCurrent(owner)) return
        if (res?.status !== 200) { reportError(owner, res, '微信验证码绑定未完成，请核对验证码后再操作'); return }
        if (!applyCurrentUser(res.data, owner, ['wechatId', 'wechatBoundAt'])) return
        message.success('微信绑定成功')
        withInternalInputs(() => { wechatCodeForm.code = '' }); scrubInputs('#profile-wechat-code'); clearWechatScan()
      } catch (error) { reportError(owner, error, unknownOutcomeText) }
      finally { closeAttempt(owner) }
    }
    const unbindWechat = async () => {
      if (!currentUser.value.wechatId || attempts.has('wechat-code') || attempts.get('wechat-scan')?.resolving) return
      const owner = beginAttempt('wechat-unbind', { mutation: true })
      if (!owner) return
      unbindingWechat.value = true
      try {
        if (!isCurrent(owner)) return
        owner.issued = true
        const res = await unbindCurrentUserWechat()
        if (!isCurrent(owner)) return
        if (res?.status !== 200) { reportError(owner, res, '微信解绑未完成，请核对账号的其他登录方式后重试'); return }
        if (!applyCurrentUser(res.data, owner, ['wechatId', 'wechatBoundAt'])) return
        message.success('微信已解绑'); closeAttempt(attempts.get('wechat-scan')); clearWechatScan()
        withInternalInputs(() => { wechatCodeForm.code = '' }); scrubInputs('#profile-wechat-code')
        internalModeChange = true
        try { wechatBindSubMode.value = chooseWechatBindSubMode(); wechatRenderMode.value = wechatBindSubMode.value } finally { internalModeChange = false }
      } catch (error) { reportError(owner, error, unknownOutcomeText) }
      finally { closeAttempt(owner) }
    }
    const formatDateTime = value => {
      if (!value) return '-'
      const date = new Date(value)
      return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
    }
    const formatBalance = value => formatMoneyValue(value || 0)
    const roleText = value => String(value || '').toLowerCase() === 'admin' ? '管理员' : '普通用户'
    const statusText = value => String(value || '').toLowerCase() === 'disabled' ? '已禁用' : '正常'
    const normalizeWechatCodeInput = value => String(value || '').replace(/\D/g, '').slice(0, 12)
    const renderHandlers = computed(() => {
      const epoch = formKey.value
      const run = action => (...args) => { if (!internalInputChange && viewCurrent(epoch)) return action(...args) }
      return {
        guardEvent: event => {
          if (viewCurrent(epoch)) return
          event.stopImmediatePropagation(); event.preventDefault()
          const input = event.target
          if (input && ['INPUT', 'TEXTAREA'].includes(input.tagName)) {
            input.value = ''; input.defaultValue = ''; input.removeAttribute('value')
          }
        },
        loadProfile: run(loadProfile), submitProfile: run(submitProfile), submitPhoneChange: run(submitPhoneChange), sendPhoneChangeCode: run(sendPhoneChangeCode),
        setUsername: run(value => { profileForm.username = value }), setNickname: run(value => { profileForm.nickname = value }),
        setPhone: run(value => { phoneForm.phone = value }), setSMSCode: run(value => { phoneForm.smsCode = normalizeSMSCodeInput(value) }),
      }
    })
    const wechatHandlers = computed(() => {
      const epoch = formKey.value, mode = wechatModeKey.value
      const run = action => (...args) => {
        if (!internalInputChange && viewCurrent(epoch) && mode === wechatModeEpoch.value) return action(...args)
      }
      return {
        startWechatBind: run(startWechatBind), submitWechatCodeBind: run(submitWechatCodeBind), unbindWechat: run(unbindWechat),
        setCode: run(value => { wechatCodeForm.code = normalizeWechatCodeInput(value) }), setMode: run(value => { wechatBindSubMode.value = value }),
      }
    })
    const retireDraft = draft => {
      if (internalInputChange) return
      draftEpoch[draft] += 1
      for (const owner of [...attempts.values()]) {
        // A draft edit revokes completion/paint authority, not a request already
        // sent. Preserve its busy/dedup lane until finally sees the real result.
        if (owner.draft === draft && !owner.issued) closeAttempt(owner)
      }
      if (draft === 'profile') profileDraftDirty = true
      if (draft === 'phone') phoneForm.captchaToken = ''
    }
    watch(() => [profileForm.username, profileForm.nickname], () => retireDraft('profile'), { flush: 'sync' })
    watch(() => [phoneForm.phone, phoneForm.smsCode], () => retireDraft('phone'), { flush: 'sync' })
    watch(() => wechatCodeForm.code, () => retireDraft('wechat'), { flush: 'sync' })
    watch(wechatBindSubMode, () => { if (!internalModeChange) handleWechatBindSubModeChange() }, { flush: 'sync' })
    watch(() => String(currentUser.value.wechatId || ''), clearWechatQrFailure, { flush: 'sync' })
    watch(routeKey, retireView, { flush: 'sync' })
    // The readonly ref wrapper may suppress a same-null direct watcher after
    // triggerRef. A tuple observes that notification and its explicit logout epoch.
    watch(() => [displayCurrentUser.value, getCurrentUserLogoutEpoch()], ([user], [previous]) => {
      const logoutEpoch = getCurrentUserLogoutEpoch()
      const sameLogoutEpoch = logoutEpoch === observedLogoutEpoch
      observedLogoutEpoch = logoutEpoch
      const sequence = ++metadataSequence
      if (sameLogoutEpoch && ownCommit && !ownCommit.observed && JSON.stringify(user) === ownCommit.expectedUser) { ownCommit.observed = true; return }
      // A confirmed actor's balance/nickname metadata is not a new principal.
      // Keep all drafts and independently pending operations; only transfer their
      // freshness leases. Null/clear, actor, role, status or logout changes still
      // retire the whole private view (including unknown null -> null logout).
      if (sameLogoutEpoch && profileReady.value && profileUser.value && user && previous &&
        currentUserIdentity(user) && currentUserIdentity(previous) && currentUserIdentity(profileUser.value) &&
        actorKey(user) === actorKey(previous) && actorKey(user) === actorKey(profileUser.value) &&
        statusKey(user) === statusKey(previous) && statusKey(user) === statusKey(profileUser.value) && pageActive()) {
        const epoch = renderEpoch.value
        const nextUser = { ...profileUser.value, ...user }
        viewLease?.release(); viewLease = captureCurrentUserLease()
        for (const pending of attempts.values()) {
          pending.lease.release(); pending.lease = captureCurrentUserLease()
        }
        if (sequence !== metadataSequence || epoch !== renderEpoch.value || !pageActive()) return
        if (viewLease.isRetired()) { retireView(); return }
        profileUser.value = nextUser
        return
      }
      retireView()
    }, { flush: 'sync' })
    onMounted(() => { void Promise.all([loadProfile(), loadPublicSettings()]) })
    onBeforeUnmount(() => { active = false; retireView() })

    return {
      fieldId,
      profileRoot, formKey, wechatModeKey, renderHandlers, wechatHandlers, profileReady, profileError,
      loading, updatingPhone, updatingProfile, sendingPhoneCode, unbindingWechat,
      currentUserFailure, currentUserFailureContextKey, currentUserFailureText, isCurrentCurrentUserFailure,
      wechatQrFailure, wechatQrFailureContextKey, wechatQrFailureText, isCurrentWechatQrFailure,
      wechatQrLoading, wechatQrCodeImageData, wechatStatusText, wechatRemainingSeconds, wechatTransportText,
      wechatBindSubMode, wechatRenderMode, wechatCodeSubmitting, currentUser, phoneForm, profileForm, wechatCodeForm, publicAuthSettings,
      smsCooldownText: smsCooldown.buttonText, smsCooldownActive: smsCooldown.active,
      sendPhoneChangeCode, submitPhoneChange, submitProfile, startWechatBind, handleWechatBindSubModeChange,
      submitWechatCodeBind, unbindWechat, loadProfile, normalizeSMSCodeInput, normalizeWechatCodeInput,
      registerSourceText: buildRegisterSourceLabel, formatDateTime, formatBalance,
      ipLocations, phoneLocations, normalizeLocationKey, formatValueWithLocation, roleText, statusText,
    }
  },
  components: { MainlandPhoneInput, RequestFailureAlert },
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
