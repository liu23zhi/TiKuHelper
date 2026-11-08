<template>
  <div ref="formRoot" class="auth-shell">
    <div class="auth-card auth-card--single">
      <AuthPageIntro eyebrow="微信账号关联" title="绑定已有账号" description="微信扫码成功后，请使用已有账号完成绑定。">
        <dl class="auth-guide">
          <div>
            <dt>验证已有账号</dt>
            <dd>输入当前账号的用户名与密码，确认微信绑定归属。</dd>
          </div>
          <div>
            <dt>绑定后直接登录</dt>
            <dd>后续可通过微信登录，也可在个人中心管理绑定。</dd>
          </div>
        </dl>
      </AuthPageIntro>

      <a-card :bordered="false" class="form-card">
        <div class="form-header">
          <div>
            <h2 class="form-title">绑定已有账号</h2>
            <div class="form-subtitle">绑定成功后，下次即可直接微信扫码登录。</div>
          </div>
        </div>

        <a-alert
          v-if="!bindToken"
          type="warning"
          show-icon
          class="bind-alert"
          message="绑定令牌无效或已过期，请返回登录页重新扫码。"
        />

        <a-form
          layout="vertical"
          :name="bindFormName"
          :model="bindForm"
          :key="renderOwner.key"
          @finish="formHandlers.handleBind"
          @finishFailed="formHandlers.handleFinishFailed"
          class="form"
        >
          <a-form-item name="username" label="用户名" :rules="[{ required: true, message: '请输入用户名' }]">
            <a-input :value="bindForm.username" @update:value="formHandlers.updateUsername" placeholder="请输入已有账号用户名" autocomplete="username" size="large">
              <template #prefix>
                <UserOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
            </a-input>
          </a-form-item>

          <a-form-item name="password" label="密码" :rules="[{ required: true, message: '请输入密码' }]">
            <a-input-password :value="bindForm.password" @update:value="formHandlers.updatePassword" placeholder="请输入已有账号密码" autocomplete="current-password" size="large">
              <template #prefix>
                <LockOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
            </a-input-password>
          </a-form-item>

          <a-form-item>
            <a-button
              type="primary"
              html-type="submit"
              :loading="submitting"
              :disabled="submitting || !bindToken || !bindForm.username || !bindForm.password"
              class="submit-button"
              block
            >
              绑定并登录
            </a-button>
          </a-form-item>
        </a-form>

        <div class="auth-actions">
          <a-button class="secondary-button" block @click="formHandlers.goToLogin">返回登录页</a-button>
        </div>
      </a-card>
    </div>
  </div>
</template>

<script>
import { computed, defineComponent, nextTick, onBeforeUnmount, reactive, ref, shallowRef, useId, watch } from 'vue'
import AuthPageIntro from '../../components/AuthPageIntro.vue'
import { UserOutlined, LockOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { useRoute, useRouter } from 'vue-router'
import { bindWechatLogin, getCurrentUser } from '../../api/TikuComponentApi.js'
import {
  captureCurrentUserLease,
  getStoredCurrentUser,
  resolveHomePath,
  setStoredCurrentUser,
  useCurrentUserDisplay,
} from '../../utils/currentUser.js'

export default defineComponent({
  name: 'WechatBind',
  setup() {
    const route = useRoute()
    const router = useRouter()
    const { currentUser } = useCurrentUserDisplay()
    const bindFormName = 'wechat-bind-' + useId().replace(/[^A-Za-z0-9_-]/g, '')
    const formRoot = ref(null)
    const submitting = ref(false)
    const bindToken = ref('')
    const bindForm = reactive({ username: '', password: '' })
    const renderOwner = shallowRef(null)
    const formRevision = ref(0)
    let mounted = true
    let nextRenderKey = 0
    let commit = null

    const routeToken = () => String(route.query?.bindToken || '').trim()
    const routeScope = () => JSON.stringify([route.name, route.path, route.fullPath, routeToken()])
    const isRendered = (owner) => mounted && owner?.active && renderOwner.value === owner && owner.route === routeScope()

    const retireAttempt = (owner) => {
      if (!owner.attempt) return
      owner.attempt.active = false
      owner.attempt.lease.release()
      owner.attempt = null
      if (renderOwner.value === owner) submitting.value = false
    }
    const clearPrivateFields = () => {
      bindForm.username = ''
      bindForm.password = ''
      // Clear the retired native controls now, not only after Vue/Antd's next
      // render; a retained input node must not keep the old owner's credentials.
      for (const input of formRoot.value?.querySelectorAll('input') || []) {
        input.value = ''
        input.defaultValue = ''
      }
    }
    const replaceOwner = (token) => {
      const previous = renderOwner.value
      if (previous) {
        previous.active = false
        retireAttempt(previous)
        previous.lease.release()
      }
      clearPrivateFields()
      bindToken.value = token
      renderOwner.value = {
        key: ++nextRenderKey, active: true, route: routeScope(), token,
        lease: captureCurrentUserLease(), attempt: null,
      }
    }
    const ownsRender = (owner) => {
      if (!isRendered(owner)) return false
      // Anonymous/blocked storage is valid at entry; only a retired lease cancels it.
      if (!owner.lease.isRetired()) return true
      replaceOwner('')
      return false
    }
    const ownsAttempt = (attempt) => attempt?.active && ownsRender(attempt.owner) &&
      attempt.owner.attempt === attempt && !attempt.lease.isRetired()

    replaceOwner(routeToken())
    watch(routeScope, () => replaceOwner(routeToken()), { flush: 'sync' })
    watch(currentUser, (user) => {
      // Our own storage commit advances the lease once. Any intervening generation
      // (including an identical second write) still retires this binding flow.
      if (commit && isRendered(commit.attempt.owner) && !commit.seen && JSON.stringify(user) === commit.expected) {
        commit.seen = true
        return
      }
      if (commit) commit.foreign = true
      replaceOwner('')
    }, { flush: 'sync' })
    watch(() => [bindForm.username, bindForm.password], () => {
      formRevision.value += 1
      if (renderOwner.value) retireAttempt(renderOwner.value)
    }, { flush: 'sync' })
    onBeforeUnmount(() => {
      mounted = false
      const owner = renderOwner.value
      owner.active = false
      retireAttempt(owner)
      owner.lease.release()
      clearPrivateFields()
      bindToken.value = ''
    })

    const finishLogin = async (attempt) => {
      if (!ownsAttempt(attempt)) return
      attempt.phase = 'profile'
      const userRes = await getCurrentUser()
      if (!ownsAttempt(attempt)) return
      if (userRes?.status !== 200 || !userRes.data || typeof userRes.data !== 'object' || Array.isArray(userRes.data)) {
        message.error('绑定已完成，但无法获取登录状态，请返回登录页重试')
        return
      }
      attempt.phase = 'storage'
      commit = { attempt, expected: JSON.stringify(userRes.data), seen: false, foreign: false }
      try {
        // Check immediately before storage. setStoredCurrentUser intentionally
        // retires the old leases; transfer only after reading our exact commit.
        if (!ownsAttempt(attempt)) return
        setStoredCurrentUser(userRes.data)
        const stored = getStoredCurrentUser()
        if (commit.foreign || !isRendered(attempt.owner)) return
        const lease = captureCurrentUserLease()
        const attemptLease = captureCurrentUserLease()
        if (commit.foreign || !isRendered(attempt.owner) || lease.isRetired() || attemptLease.isRetired()) {
          lease.release()
          attemptLease.release()
          return
        }
        attempt.owner.lease.release()
        attempt.lease.release()
        attempt.owner.lease = lease
        attempt.lease = attemptLease
        // Unknown storage can retain owned feedback, but cannot authorize a
        // successful login. A readable exact commit is required for navigation.
        if (JSON.stringify(stored) !== commit.expected || !lease.isCurrent() || !attemptLease.isCurrent()) {
          throw new Error('STORAGE_UNAVAILABLE')
        }
      } finally {
        commit = null
      }
      if (!ownsAttempt(attempt)) return
      attempt.phase = 'navigation'
      message.success('绑定成功，已登录')
      if (ownsAttempt(attempt)) await router.push(resolveHomePath(userRes.data))
    }

    const handleBind = async (owner, revision, values) => {
      if (!ownsRender(owner) || revision !== formRevision.value || owner.attempt) return
      // Antd may finish an old asynchronous validation after the draft changed.
      if (values !== undefined && (values.username !== bindForm.username || values.password !== bindForm.password)) return
      if (!owner.token || owner.token !== bindToken.value) {
        message.error('绑定令牌无效或已过期')
        return
      }
      if (!String(bindForm.username || '').trim()) { message.error('请输入用户名'); return }
      if (!bindForm.password) { message.error('请输入密码'); return }
      const attempt = {
        owner, active: true, phase: 'bind', lease: captureCurrentUserLease(),
        payload: Object.freeze({ bindToken: owner.token, username: bindForm.username, password: bindForm.password }),
      }
      owner.attempt = attempt
      submitting.value = true
      try {
        if (!ownsAttempt(attempt)) return
        const res = await bindWechatLogin(attempt.payload)
        if (!ownsAttempt(attempt)) return
        if (res?.status === 200) await finishLogin(attempt)
        else message.error('绑定失败，请检查账号信息后重试')
      } catch (error) {
        if (!ownsAttempt(attempt)) return
        message.error(attempt.phase === 'bind' && error?.code === 'UNKNOWN_OUTCOME' ? '绑定结果尚未确认，请核对账号状态，勿直接重复提交' :
          attempt.phase === 'profile' ? '绑定已完成，但无法获取登录状态，请返回登录页重试' :
          attempt.phase === 'storage' ? '绑定已完成，但登录状态无法保存，请返回登录页重试' :
            attempt.phase === 'navigation' ? '绑定成功，请从登录页重新进入' : '绑定失败，请稍后重试')
      } finally {
        if (ownsAttempt(attempt)) {
          owner.attempt = null
          submitting.value = false
        }
        attempt.active = false
        attempt.lease.release()
      }
    }

    // Native callbacks hold this exact render/draft, never a later route's fields.
    const formHandlers = computed(() => {
      const owner = renderOwner.value
      const revision = formRevision.value
      const current = () => ownsRender(owner) && revision === formRevision.value
      return {
        handleBind: (values) => handleBind(owner, revision, values),
        handleFinishFailed: async (errors) => {
          if (!current()) return
          message.error('请检查用户名和密码')
          const field = errors?.errorFields?.[0]?.name?.[0]
          if (field !== 'username' && field !== 'password') return
          await nextTick()
          if (current()) formRoot.value?.querySelector(`[id="${bindFormName}_${field}"]`)?.focus()
        },
        updateUsername: (value) => { if (current()) bindForm.username = value },
        updatePassword: (value) => { if (current()) bindForm.password = value },
        goToLogin: () => { if (current()) router.push('/login') },
      }
    })

    return {
      bindFormName, formRoot, submitting, bindToken, bindForm, renderOwner, formHandlers,
      handleBind: computed(() => formHandlers.value.handleBind),
      goToLogin: computed(() => formHandlers.value.goToLogin),
      handleFinishFailed: computed(() => formHandlers.value.handleFinishFailed),
    }
  },
  components: { AuthPageIntro, UserOutlined, LockOutlined },
})
</script>

<style scoped>
.bind-alert {
  margin-bottom: 16px;
}
</style>
