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
        <a-card :bordered="false" class="summary-card balance-card"
          @pointerenter="onBalancePointerEnter" @pointerleave="onBalancePointerLeave"
        >
          <button type="button" class="balance-trigger" :aria-expanded="balanceDetailsOpen"
            :aria-controls="fieldId('balance-details')" :aria-labelledby="fieldId('balance-label')"
            :aria-describedby="fieldId('balance-value') + ' ' + fieldId('balance-hint')"
            @focus="onBalanceFocus" @blur="closeBalanceDetails" @click="toggleBalanceDetails"
          >
            <span :id="fieldId('balance-label')" class="summary-label">账户余额合计</span>
            <span :id="fieldId('balance-value')" data-profile-private class="summary-value" aria-live="polite">{{ balanceDisplay.total }}</span>
            <span :id="fieldId('balance-hint')" class="summary-hint">充值本金与赠送余额合计</span>
            <span class="balance-chevron" aria-hidden="true">{{ balanceDetailsOpen ? '▴' : '▾' }}</span>
          </button>
          <dl v-show="balanceDetailsOpen" :id="fieldId('balance-details')" ref="balanceDetails" class="balance-details">
            <dt>充值本金（可退款）</dt>
            <dd data-profile-private>{{ balanceDisplay.principal }}</dd>
            <dt>赠送余额（不可退款）</dt>
            <dd data-profile-private>{{ balanceDisplay.gift }}</dd>
          </dl>
        </a-card>
        <a-card :bordered="false" class="summary-card">
          <div class="summary-label">上次登录</div>
          <div data-profile-private class="summary-value summary-value--small">{{ formatDateTime(currentUser.lastLoginAt) }}</div>
        </a-card>
        <!-- 可用设备数量（123 号 T-24）。
             ⚠️ 这里只显示**上限**，没有「已用设备数」—— 那个数据当前不存在：
             ① user_levels.device_limit 的业务消费点是 0 处（entity/user_level.go:46
                自称「契约占位」）；
             ② account_device_bindings 表尚未建。
             所以不要在这里补一个 0 或"已用 0 台"占位（63 号 U-03 红线）。
             数据源是已存在的 GET /api/user/level，不新造接口。 -->
        <a-card :bordered="false" class="summary-card">
          <div class="summary-label">可用设备数量</div>
          <div data-profile-private data-profile-device-limit class="summary-value">{{ deviceLimitText }}</div>
          <div class="summary-hint">{{ deviceLimitHint }}</div>
        </a-card>
    </ConsoleSummaryGrid>

    <div class="account-columns">
      <div class="account-column">
        <AccountCard kind="base" :heading-id="fieldId('base-heading')" title="基础信息"
          description="账号创建与最近登录情况。" :view="cards.base">
          <template v-if="profileReady">
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
              <span class="profile-field-label">上次登录</span>
              <span data-profile-private>{{ formatDateTime(currentUser.lastLoginAt) }}</span>
            </div>
            <div class="profile-field">
              <span class="profile-field-label">登录 IP</span>
              <span data-profile-private class="location-value">{{ formatValueWithLocation(currentUser.lastLoginIp, ipLocations[normalizeLocationKey(currentUser.lastLoginIp)]) || '-' }}</span>
            </div>
          </template>
        </AccountCard>
        <AccountCard kind="identity" :heading-id="fieldId('identity-heading')" title="修改用户名和昵称"
          description="更新后使用新用户名登录。" :view="cards.identity">
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
        </AccountCard>
      </div>
      <div class="account-column">
        <AccountCard kind="phone" :heading-id="fieldId('phone-heading')" title="手机号绑定"
          description="使用新手机号与验证码绑定或更换。" :view="cards.phone">
          <div v-if="profileReady && hasPhoneField" class="binding-summary">
            <strong data-profile-private>{{ maskedPhone || '未绑定' }}</strong>
            <span v-if="boundPhoneLocation" data-profile-private>{{ boundPhoneLocation }}</span>
            <span data-profile-private v-if="currentUser.phoneBoundAt">绑定于 {{ formatDateTime(currentUser.phoneBoundAt) }}</span>
          </div>
          <a-alert
            v-if="settingsState === 'ready' && bindingSettingsKnown.phone && !publicAuthSettings.smsLoginEnabled"
            type="warning"
            show-icon
            message="管理员尚未启用手机号，当前无法更换手机号。"
          />
          <a-form v-else-if="settingsState === 'ready' && bindingSettingsKnown.phone && publicAuthSettings.smsLoginEnabled" layout="vertical" :model="phoneForm" @finish="renderHandlers.submitPhoneChange">
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
          <a-button v-if="(settingsState === 'error' || (settingsState === 'ready' && !bindingSettingsKnown.phone)) && profileReady" @click="renderHandlers.loadSettings">重新读取绑定设置</a-button>
        </AccountCard>
        <AccountCard kind="wechat" :heading-id="fieldId('wechat-heading')" title="微信绑定"
          description="绑定后可使用微信扫码或验证码登录。" :view="cards.wechat">
          <div v-if="profileReady && hasWechatField && currentUser.wechatId" key="wechat-bound" class="wechat-bound-panel">
            <a-tag color="green">已绑定</a-tag>
            <div data-profile-private class="wechat-binding-id">
              {{ maskedWechat }}
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

          <div v-else-if="profileReady && hasWechatField" :key="'wechat-unbound-' + wechatModeKey" class="wechat-bind-panel">
            <div class="wechat-binding-id wechat-binding-id--empty">未绑定</div>
            <a-alert
              v-if="settingsState === 'ready' && bindingSettingsKnown.wechat && !publicAuthSettings.wechatLoginEnabled"
              type="warning"
              show-icon
              message="管理员尚未开启微信，当前无法绑定。"
            />
            <template v-else-if="settingsState === 'ready' && bindingSettingsKnown.wechat && publicAuthSettings.wechatLoginEnabled">
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
                  <label :for="fieldId('wechat-code')">微信验证码</label>
                  <a-input
                    :id="fieldId('wechat-code')"
                    data-profile-wechat-code
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

          <a-button v-if="(settingsState === 'error' || (settingsState === 'ready' && !bindingSettingsKnown.wechat)) && profileReady && !currentUser.wechatId" @click="renderHandlers.loadSettings">重新读取绑定设置</a-button>
        </AccountCard>
      </div>
    </div>

    <!-- ─────────── 自助注销（B2-5）───────────
         用户诉求：「注销提供两个入口，另一个入口在个人中心」。
         这是**主入口**（工单里的注销申请是兜底），所以放在个人中心页脚，
         且标题/说明写清它是「立即生效、可撤销」的那条路。

         组件复用工单向导里的 ClosureGuide（mode=self-service 直接进流程），
         避免两处各写一套申请/撤销逻辑 —— 那一定会漂移成「个人中心能撤销、
         工单页不能」这类不一致。

         权限沿用 profileReady：未确认当前用户时不渲染（与其它表单一致），
         否则会在别人的账号上显示注销按钮。 -->
    <a-card
      v-if="profileReady"
      :bordered="false"
      class="page-card account-closure-card"
      data-section="account-closure"
    >
      <template #title>
        <div>
          <h2 class="page-title">自助注销</h2>
          <div class="page-subtitle">
            立即生效，等待期内可随时撤销；到期后账号会被归档，无法自行恢复。
          </div>
        </div>
      </template>
      <ClosureGuide mode="self-service" />
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
// 自助注销 UI（申请 / 撤销 / 查询状态）与工单向导的注销引导共用同一组件，
// 避免两处各写一套申请/撤销逻辑后漂移。
import ClosureGuide from '@/pages/Tickets/ClosureGuide.vue'
import { buildRegisterSourceLabel } from '@/utils/userPortalHelpers.mjs'
import { buildDeviceLimitHint, buildDeviceLimitText } from '@/utils/userLevelHelpers.mjs'
import { getMyUserLevel } from '@/api/UserLevelApi'
import AccountCard from './AccountCard.vue'
import { buildProfileBalanceDisplay } from '@/utils/profileBalance.mjs'
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
    const balanceDetailsOpen = ref(false), balanceDetails = ref(null)
    const balanceDisplay = computed(() => buildProfileBalanceDisplay(currentUser.value,
      loading.value ? 'loading' : profileError.value ? 'failed' : profileReady.value ? 'ready' : 'loading'))
    let balanceEscapeDocument = null
    const onBalanceEscape = event => {
      if (event.key !== 'Escape' || event.defaultPrevented || event.isComposing) return
      const root = profileRoot.value
      const target = event.target
      if (!root || (target !== root.ownerDocument.body && !root.contains(target))) return
      // A dialog may render inside Profile. Let it handle its own dismissal.
      if (target?.closest?.('dialog, [role="dialog"], [role="alertdialog"], [aria-modal="true"]')) return
      closeBalanceDetails()
    }
    const closeBalanceDetails = () => {
      balanceDetailsOpen.value = false
      balanceEscapeDocument?.removeEventListener('keydown', onBalanceEscape)
      balanceEscapeDocument = null
    }
    const openBalanceDetails = () => {
      if (balanceDetailsOpen.value) return
      balanceDetailsOpen.value = true
      // Hover can open the details without bringing focus to the trigger.
      balanceEscapeDocument = profileRoot.value?.ownerDocument
      balanceEscapeDocument?.addEventListener('keydown', onBalanceEscape)
      nextTick(() => {
        if (!balanceDetailsOpen.value || !balanceDetails.value) return
        // Keep the in-flow panel inside the visible screen, including mobile
        // browsers where the layout and visual viewports can differ.
        const viewport = window.visualViewport
        const top = viewport?.offsetTop ?? 0
        const bottom = top + (viewport?.height ?? window.innerHeight)
        const rect = balanceDetails.value.getBoundingClientRect()
        if (rect.bottom > bottom) window.scrollBy({ top: Math.ceil(rect.bottom - bottom), behavior: 'instant' })
      })
    }
    const onBalancePointerEnter = event => { if (event.pointerType === 'mouse') openBalanceDetails() }
    const onBalancePointerLeave = event => { if (event.pointerType === 'mouse') closeBalanceDetails() }
    const onBalanceFocus = event => { if (event.currentTarget.matches(':focus-visible')) openBalanceDetails() }
    const toggleBalanceDetails = () => {
      if (balanceDetailsOpen.value) closeBalanceDetails()
      else openBalanceDetails()
    }
    const sendingPhoneCode = ref(false), unbindingWechat = ref(false), wechatQrLoading = ref(false)
    const wechatQrCodeImageData = ref(''), wechatLoginToken = ref('')
    const wechatStatusText = ref('请使用微信扫码完成绑定')
    const wechatRemainingSeconds = ref(null), wechatTransportText = ref('')
    const wechatBindSubMode = ref('scan'), wechatRenderMode = ref('scan'), wechatCodeSubmitting = ref(false)
    const profileForm = reactive({ username: '', nickname: '' })
    const phoneForm = reactive({ phone: '', smsCode: '', captchaToken: '' })
    const wechatCodeForm = reactive({ code: '' })
    const ipLocations = reactive({}), phoneLocations = reactive({})
    // 等级摘要（123 号 T-24）。null = 尚未读到，**不等于**「不限」。
    // 只用 shallowRef 存服务端下发的快照，不在本地推导设备上限：
    // 设备上限的口径唯一来源是 user_levels.device_limit。
    const userLevelSummary = shallowRef(null)
    const publicAuthSettings = ref({
      captchaProvider: 'remote', captchaApiBaseUrl: 'https://captcha.api.zelly.cn', captchaType: 'RANDOM',
      smsLoginEnabled: false, wechatLoginEnabled: false, wechatScanLoginEnabled: false,
      wechatCodeLoginEnabled: false, wechatDefaultLoginMode: 'scan', wechatCodeStaticQrcodeUrl: '',
    })
    const settingsState = ref('loading')
    const bindingSettingsKnown = ref({ phone: false, wechat: false })
    const cardNotes = reactive({ identity: null, phone: null, wechat: null })
    const note = (key, state, message) => { cardNotes[key] = { state, message } }
    // The authenticated /me DTO has an account ID. Neither cached role nor binding data confirms a principal.
    const confirmedProfile = value => Number.isSafeInteger(Number(value?.id)) && Number(value.id) > 0 &&
      (typeof value.id === 'number' || typeof value.id === 'string')
    const hasPhoneField = computed(() => currentUser.value.phone === null || typeof currentUser.value.phone === 'string')
    const hasWechatField = computed(() => currentUser.value.wechatId === null || typeof currentUser.value.wechatId === 'string')
    const maskedPhone = computed(() => {
      const value = String(currentUser.value.phone ?? '').trim()
      if (!value) return ''
      return /^1[0-9]{10}$/.test(value) ? '尾号 ' + value.slice(-4) : '已绑定（号码已隐藏）'
    })
    const maskedWechat = computed(() => {
      const id = String(currentUser.value.wechatId || '').trim()
      return id.length >= 8 && /[A-Za-z0-9_-]{4}$/.test(id) ? '标识尾号 ' + id.slice(-4) : '微信标识已隐藏'
    })
    const boundPhoneLocation = computed(() => {
      const phone = String(currentUser.value.phone || '')
      const region = phone && phoneLocations[normalizeLocationKey(phone)]
      return region && !region.includes(phone) && !(currentUser.value.wechatId && region.includes(currentUser.value.wechatId)) ? region : ''
    })
    const view = (state, message = '') => ({ state, message })
    const cardView = key => {
      if (!profileReady.value || loading.value) {
        return profileError.value ? view('error', '尚未确认本人资料，请刷新重试。') : view('loading', '正在读取本人资料…')
      }
      const pending = key === 'identity' ? updatingProfile.value :
        key === 'phone' ? updatingPhone.value || sendingPhoneCode.value :
          key === 'wechat' && (unbindingWechat.value || wechatQrLoading.value || wechatCodeSubmitting.value)
      if (pending) return view('loading', key === 'identity' ? '正在保存资料…' : '正在处理，请稍候…')
      if (cardNotes[key]) return cardNotes[key]
      if (profileError.value) return view('error', '刷新失败，保留上次确认的资料。')
      if (key === 'base') {
        return ['username', 'nickname', 'registerSource', 'createdAt', 'lastLoginAt', 'lastLoginIp'].some(k => currentUser.value[k])
          ? view('ready') : view('empty', '暂无基础资料。')
      }
      if (key === 'identity') return currentUser.value.username || currentUser.value.nickname ? view('ready') : view('empty', '尚未设置用户名或昵称。')
      const known = key === 'phone' ? hasPhoneField.value : hasWechatField.value
      const bound = key === 'phone' ? maskedPhone.value : currentUser.value.wechatId
      if (!known) return view('error', '未收到绑定状态，请刷新核对。')
      if ((key === 'phone' || !bound) && settingsState.value !== 'ready') return settingsState.value === 'loading'
        ? view('loading', '正在读取绑定设置…') : view('error', '绑定设置读取失败；已确认的绑定状态不变。')
      if ((key === 'phone' || !bound) && !bindingSettingsKnown.value[key]) return view('error', '未收到绑定设置，请重试。')
      return bound ? view('ready') : view('empty')
    }
    const cards = computed(() => Object.fromEntries(['base', 'identity', 'phone', 'wechat'].map(key => [key, cardView(key)])))
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
        if (attempts.has('settings')) settingsState.value = 'error'
        for (const key of Object.keys(cardNotes)) cardNotes[key] = null
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
        closeBalanceDetails()
        profileUser.value = null; profileReady.value = false; profileDraftDirty = false
        profileError.value = '账号上下文已变化，请刷新个人信息后继续操作'
        clearLocationMap(ipLocations); clearLocationMap(phoneLocations)
        // 等级摘要与 IP/手机号归属地同属"账号私有数据"，账号上下文变化必须清空。
        // 清成 null ⇒ 界面回到「—」（未读到），而不是残留上一个账号的设备上限。
        userLevelSummary.value = null
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
      const key = kind === 'profile-save' ? 'identity' : kind.startsWith('phone-') ? 'phone' : kind.startsWith('wechat-') ? 'wechat' : ''
      if (key) cardNotes[key] = null
      const owner = {
        card: key, kind, mutation, draft, input: draftEpoch[draft], render: renderEpoch.value, route: routeKey(),
        actor: actorKey(profileUser.value), lease: captureCurrentUserLease(), controller: new AbortController(),
        mode: mode ? wechatModeEpoch.value : undefined, data: dataGeneration, profileDraft: draftEpoch.profile,
      }
      attempts.set(kind, owner)
      return owner
    }
    const reportError = (owner, failure, fallback) => {
      if (!isCurrent(owner) || failure?.name === 'AbortError' || failure?.code === 'ERR_CANCELED') return
      const text = publicFailureText(failure, fallback)
      if (owner.card) note(owner.card, 'error', text)
      if (text === unknownOutcomeText) message.warning(text)
      else message.error(text)
    }
    const confirmsChange = (user, owner) => {
      switch (owner.kind) {
        case 'phone-change': return user.phone === owner.snapshot?.phone
        case 'wechat-unbind': return user.wechatId === null || user.wechatId === ''
        case 'wechat-code':
        case 'wechat-scan': return typeof user.wechatId === 'string' && Boolean(user.wechatId.trim())
        case 'profile-save': {
          // The service stores strings.TrimSpace(input), including an empty nickname.
          const canonical = text => text.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, '')
          const { username, nickname } = owner.snapshot
          return typeof username === 'string' && typeof nickname === 'string' &&
            user.username !== '' && user.username === canonical(username) && user.nickname === canonical(nickname)
        }
        default: return true
      }
    }
    const applyCurrentUser = (user, owner, fields = null) => {
      if (!isCurrent(owner)) return false
      if (!user || typeof user !== 'object' || Array.isArray(user) || !confirmedProfile(user) ||
        (fields && (!Object.hasOwn(user, fields[0]) || !confirmsChange(user, owner)))) {
        if (owner.mutation) {
          note(owner.card, 'error', unknownOutcomeText)
          message.warning('操作已提交，但当前资料尚未确认，请先刷新核对，勿重复提交。')
        }
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
    // loadUserLevel 读取当前用户的等级摘要（GET /api/user/level）。
    //
    // 只读、只用于展示「可用设备数量」的上限。它是**可选展示增强**：
    // 失败时不覆盖任何已确认资料、不弹全局错误（errorFeedback: 'local'），
    // 保持「—」这个"未读到"的语义 —— 绝不能因为读不到就退化成「不限」
    // （那会把一次网络失败说成"你没有任何设备限制"）。
    //
    // 走与 profile-read / locations 相同的 owner 机制（beginAttempt/isCurrent），
    // 这样账号上下文切换、路由切走、组件卸载都会自动作废本次读取。
    const loadUserLevel = async () => {
      closeAttempt(attempts.get('user-level'))
      const owner = beginAttempt('user-level')
      if (!owner) return
      try {
        if (!isCurrent(owner)) return
        const res = await getMyUserLevel()
        if (!isCurrent(owner) || owner.data !== dataGeneration) return
        // 后端把摘要包在 {"data": {...}} 里（controller/user_level.go:146）。
        const summary = res?.status === 200 ? res?.data?.data : null
        if (!summary || typeof summary !== 'object' || Array.isArray(summary)) return
        userLevelSummary.value = summary
      } catch { /* 设备上限读不到就显示「—」，不影响个人中心其它功能。 */ }
      finally { closeAttempt(owner) }
    }
    const loadProfile = async () => {
      closeAttempt(attempts.get('profile-read'))
      const owner = beginAttempt('profile-read')
      if (!owner) return
      loading.value = true; profileError.value = ''
      for (const key of Object.keys(cardNotes)) cardNotes[key] = null
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
        if (res?.status !== 200 || !res.data || typeof res.data !== 'object' || Array.isArray(res.data) || !confirmedProfile(res.data)) {
          profileError.value = '获取个人状态失败，已确认的资料与未保存内容保持不变，请刷新重试'
          showCurrentUserFailure(res, ownsFailure)
          reportError(owner, res, '获取个人状态失败，请稍后刷新重试')
          return
        }
        if (applyCurrentUser(res.data, owner)) { void loadProfileLocations(); void loadUserLevel() }
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
      settingsState.value = 'loading'
      try {
        if (!isCurrent(owner)) return
        const res = await getPublicRegisterStatus()
        if (!isCurrent(owner)) return
        if (res?.status !== 200 || !res.data || typeof res.data !== 'object' || Array.isArray(res.data)) { settingsState.value = 'error'; return }
        bindingSettingsKnown.value = { phone: typeof res.data.smsLoginEnabled === 'boolean', wechat: typeof res.data.wechatLoginEnabled === 'boolean' }
        settingsState.value = 'ready'
        publicAuthSettings.value = {
          captchaProvider: res?.data?.captchaProvider || 'remote',
          captchaApiBaseUrl: res?.data?.captchaApiBaseUrl || 'https://captcha.api.zelly.cn', captchaType: res?.data?.captchaType || 'RANDOM',
          smsLoginEnabled: !!res?.data?.smsLoginEnabled, wechatLoginEnabled: !!res?.data?.wechatLoginEnabled,
          wechatScanLoginEnabled: !!res?.data?.wechatScanLoginEnabled, wechatCodeLoginEnabled: !!res?.data?.wechatCodeLoginEnabled,
          wechatDefaultLoginMode: res?.data?.wechatDefaultLoginMode || 'scan', wechatCodeStaticQrcodeUrl: res?.data?.wechatCodeStaticQrcodeUrl || '',
        }
        internalModeChange = true
        try { wechatBindSubMode.value = chooseWechatBindSubMode(); wechatRenderMode.value = wechatBindSubMode.value } finally { internalModeChange = false }
      } catch { if (isCurrent(owner)) settingsState.value = 'error' }
      finally { closeAttempt(owner) }
    }
    const ensureCaptchaToken = (scene, targetForm, owner) => ensureAuthCaptchaToken({
      scene, targetForm, publicSettings: { ...publicAuthSettings.value },
      createCaptchaChallenge, verifyCaptchaChallenge, verifyRemoteCaptcha,
      signal: owner.controller.signal, isCurrent: () => isCurrent(owner),
    })
    const sendPhoneChangeCode = async () => {
      if (settingsState.value !== 'ready' || !publicAuthSettings.value.smsLoginEnabled || smsCooldown.active.value) return
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
          note('phone', 'error', '短信发送结果待核对；尚未更改绑定。')
          message.warning('短信发送结果待核对，请先等待短信，勿重复发送'); smsCooldown.start(); return
        }
        if (res?.status !== 200) { reportError(owner, res, '短信发送未完成，请稍后再试'); return }
        note('phone', 'ready', '验证码已发送，尚未更改绑定。')
        message.success('发送成功，请注意查收'); smsCooldown.start()
      } catch (error) {
        reportError(owner, error, owner.phase === 'issued' ? unknownOutcomeText : owner.phase === 'captcha' ? '人机验证未完成，请重新验证' : '请输入11位手机号')
      } finally { closeAttempt(owner) }
    }
    const submitPhoneChange = async () => {
      if (settingsState.value !== 'ready' || !publicAuthSettings.value.smsLoginEnabled) return
      const owner = beginAttempt('phone-change', { mutation: true, draft: 'phone' })
      if (!owner) return
      updatingPhone.value = true
      try {
        const payload = owner.snapshot = { phone: ensurePhoneBeforeSMSCaptcha(phoneForm.phone), smsCode: phoneForm.smsCode }
        if (!payload.smsCode) { note('phone', 'error', '请输入短信验证码'); message.warning('请输入短信验证码'); return }
        withInternalInputs(() => { phoneForm.phone = payload.phone })
        if (!isCurrent(owner)) return
        owner.phase = 'issued'
        owner.issued = true
        const res = await updateCurrentUserPhone({ ...payload })
        if (!isCurrent(owner)) return
        if (res?.status !== 200) { reportError(owner, res, '更换手机号未完成，请核对验证码后再操作'); return }
        if (!applyCurrentUser(res.data, owner, ['phone', 'phoneBoundAt'])) return
        note('phone', 'ready', '手机号更换成功'); message.success('手机号更换成功')
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
        if (applyCurrentUser(res.data, owner, ['username', 'nickname'])) {
          note('identity', 'ready', '资料已保存；下次登录请使用更新后的用户名。')
          message.success('资料保存成功')
        }
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
            note('wechat', 'ready', '微信绑定成功'); message.success('微信绑定成功')
            completed = true
            return true
          }
        } else if (resolved.status === 'success') {
          note('wechat', 'error', '该微信已绑定其他账号。')
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
      if (!hasWechatField.value || settingsState.value !== 'ready' || wechatBindSubMode.value !== 'scan' || !publicAuthSettings.value.wechatLoginEnabled || !publicAuthSettings.value.wechatScanLoginEnabled || currentUser.value.wechatId) return
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
        note('wechat', 'loading', '等待扫码确认；尚未绑定。')
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
            clearWechatScan(); wechatStatusText.value = '二维码已过期，请刷新'; note('wechat', 'error', '二维码已过期，请刷新。'); closeAttempt(owner)
          },
          onError: () => {
            if (!owns()) return
            wechatStatusText.value = '微信绑定状态未确认，正在重试；请勿重复绑定。'
            note('wechat', 'error', wechatStatusText.value)
          },
          onStatus: async (status, payload) => {
            if (!owns()) return
            if (status === 'pending' || status === 'scanned') {
              wechatStatusText.value = status === 'pending' ? '请使用微信扫码确认绑定' : '已扫描，请在微信中确认'
              note('wechat', 'loading', '等待微信确认；尚未绑定。')
            }
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
      cardNotes.wechat = null
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
      scrubInputs('[data-profile-wechat-code]')
      wechatStatusText.value = wechatBindSubMode.value === 'scan' ? '请使用微信扫码完成绑定' : '请输入微信验证码'
      nextTick().then(() => nextTick()).then(() => {
        if (active && renderEpoch.value === epoch && wechatModeEpoch.value === modeEpoch) {
          wechatRenderMode.value = wechatBindSubMode.value
          wechatModeKey.value = modeEpoch
        }
      })
    }
    const submitWechatCodeBind = async () => {
      if (!hasWechatField.value || settingsState.value !== 'ready' || wechatBindSubMode.value !== 'code' || !publicAuthSettings.value.wechatLoginEnabled || !publicAuthSettings.value.wechatCodeLoginEnabled || currentUser.value.wechatId || attempts.has('wechat-unbind') || attempts.get('wechat-scan')?.resolving) return
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
        note('wechat', 'ready', '微信绑定成功'); message.success('微信绑定成功')
        withInternalInputs(() => { wechatCodeForm.code = '' }); scrubInputs('[data-profile-wechat-code]'); clearWechatScan()
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
        note('wechat', 'empty', '微信已解绑'); message.success('微信已解绑'); closeAttempt(attempts.get('wechat-scan')); clearWechatScan()
        withInternalInputs(() => { wechatCodeForm.code = '' }); scrubInputs('[data-profile-wechat-code]')
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
    // 可用设备数量（123 号 T-24）：只渲染上限，且 0 必须显示为「不限」。
    // 未读到等级摘要时 buildDeviceLimitText(null) 返回 '—'，与「不限」严格区分。
    const deviceLimitText = computed(() => buildDeviceLimitText(userLevelSummary.value?.deviceLimit))
    const deviceLimitHint = computed(() => buildDeviceLimitHint(userLevelSummary.value?.deviceLimit))
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
        loadSettings: run(loadPublicSettings),
        loadProfile: run(() => { if (settingsState.value !== 'ready') void loadPublicSettings(); return loadProfile() }), submitProfile: run(submitProfile), submitPhoneChange: run(submitPhoneChange), sendPhoneChangeCode: run(sendPhoneChangeCode),
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
      cardNotes[draft === 'profile' ? 'identity' : draft] = null
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
      cards, settingsState, bindingSettingsKnown, maskedPhone, maskedWechat, boundPhoneLocation, hasPhoneField, hasWechatField,
      profileRoot, formKey, wechatModeKey, renderHandlers, wechatHandlers, profileReady, profileError,
      loading, updatingPhone, updatingProfile, sendingPhoneCode, unbindingWechat,
      currentUserFailure, currentUserFailureContextKey, currentUserFailureText, isCurrentCurrentUserFailure,
      wechatQrFailure, wechatQrFailureContextKey, wechatQrFailureText, isCurrentWechatQrFailure,
      wechatQrLoading, wechatQrCodeImageData, wechatStatusText, wechatRemainingSeconds, wechatTransportText,
      wechatBindSubMode, wechatRenderMode, wechatCodeSubmitting, currentUser, phoneForm, profileForm, wechatCodeForm, publicAuthSettings,
      smsCooldownText: smsCooldown.buttonText, smsCooldownActive: smsCooldown.active,
      sendPhoneChangeCode, submitPhoneChange, submitProfile, startWechatBind, handleWechatBindSubModeChange,
      submitWechatCodeBind, unbindWechat, loadProfile, normalizeSMSCodeInput, normalizeWechatCodeInput,
      registerSourceText: buildRegisterSourceLabel, formatDateTime,
      balanceDisplay, balanceDetails, balanceDetailsOpen, onBalancePointerEnter, onBalancePointerLeave,
      onBalanceFocus, closeBalanceDetails, toggleBalanceDetails,
      deviceLimitText, deviceLimitHint,
      ipLocations, phoneLocations, normalizeLocationKey, formatValueWithLocation, roleText, statusText,
    }
  },
  components: { AccountCard, ClosureGuide, MainlandPhoneInput, RequestFailureAlert },
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
.page-card {
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

.balance-card :deep(.ant-card-body) { justify-content: flex-start; }
.balance-trigger {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  width: 100%;
  margin: 0;
  padding: 0 16px 0 0;
  border: 0;
  background: transparent;
  text-align: left;
  cursor: pointer;
}
.balance-trigger:focus-visible {
  outline: 2px solid currentColor;
  outline-offset: 4px;
  border-radius: 4px;
}
.balance-trigger .summary-value { max-width: 100%; }
.balance-chevron {
  position: absolute;
  inset-inline-end: 0;
  inset-block-start: 0;
  color: var(--console-muted, #62766e);
}
.balance-details {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 8px 10px;
  margin: 12px 0 0;
  padding: 10px;
  border: 1px solid var(--console-border, #dce5e1);
  border-radius: 8px;
  font-size: 12px;
}
.balance-details dd {
  max-width: 100%;
  margin: 0;
  text-align: end;
  font-weight: 600;
  overflow-wrap: anywhere;
}

.summary-value--small {
  font-size: 16px;
  line-height: 1.5;
}

/* 设备上限说明：必须能看到「已用设备数暂不可用」这句，否则用户会以为漏显示。 */
.summary-hint {
  margin-top: 6px;
  color: var(--console-muted, #62766e);
  font-size: 12px;
  line-height: 1.6;
  overflow-wrap: anywhere;
}

.page-title {
  margin: 0;
  color: var(--console-text, #172b27);
  font-size: 16px;
  font-weight: 600;
}

.page-subtitle {
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

.account-columns { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: start; gap: 16px; }
.account-column { min-width: 0; display: grid; gap: 16px; }
.binding-summary { display: flex; flex-wrap: wrap; align-items: baseline; gap: 6px 12px; margin-bottom: 12px; }
.binding-summary span { color: var(--console-muted, #62766e); font-size: 13px; }
@media (max-width: 800px) { .account-columns { grid-template-columns: minmax(0, 1fr); } }
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
