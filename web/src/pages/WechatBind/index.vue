<template>
  <div class="auth-shell">
    <div class="auth-card auth-card--single">
      <div class="hero hero--wechat-bind">
        <div class="hero-badge">微信账号关联</div>
        <h1>绑定已有账号</h1>
        <p>微信扫码成功后，请使用已有账号完成绑定。</p>
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
      </div>

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
          :model="bindForm"
          @finish="handleBind"
          @finishFailed="handleFinishFailed"
          class="form"
        >
          <a-form-item name="username" label="用户名" :rules="[{ required: true, message: '请输入用户名' }]">
            <a-input v-model:value="bindForm.username" placeholder="请输入已有账号用户名" size="large">
              <template #prefix>
                <UserOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
            </a-input>
          </a-form-item>

          <a-form-item name="password" label="密码" :rules="[{ required: true, message: '请输入密码' }]">
            <a-input-password v-model:value="bindForm.password" placeholder="请输入已有账号密码" size="large">
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
              :disabled="!bindToken || !bindForm.username || !bindForm.password"
              class="submit-button"
              block
            >
              绑定并登录
            </a-button>
          </a-form-item>
        </a-form>

        <div class="auth-actions">
          <a-button class="secondary-button" block @click="goToLogin">返回登录页</a-button>
        </div>
      </a-card>
    </div>
  </div>
</template>

<script>
import { defineComponent, reactive, ref } from 'vue'
import { UserOutlined, LockOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { useRoute, useRouter } from 'vue-router'
import { bindWechatLogin, getCurrentUser } from '../../api/TikuComponentApi.js'
import { resolveHomePath, setStoredCurrentUser } from '../../utils/currentUser.js'

export default defineComponent({
  name: 'WechatBind',
  setup() {
    const route = useRoute()
    const router = useRouter()
    const submitting = ref(false)
    const bindToken = ref(String(route.query.bindToken || '').trim())
    const bindForm = reactive({
      username: '',
      password: '',
    })

    const finishLogin = () => {
      return getCurrentUser().then((userRes) => {
        if (userRes?.status === 200) {
          setStoredCurrentUser(userRes.data)
          message.success('绑定成功，已登录')
          router.push(resolveHomePath(userRes.data))
          return
        }
        throw new Error(userRes?.message || '获取当前用户失败')
      })
    }

    const handleBind = () => {
      if (!bindToken.value) {
        message.error('绑定令牌无效或已过期')
        return
      }
      submitting.value = true
      bindWechatLogin({
        bindToken: bindToken.value,
        username: bindForm.username,
        password: bindForm.password,
      }).then((res) => {
        if (res?.status === 200) {
          return finishLogin(res?.data?.jwt)
        }
        message.error(res?.message || '绑定失败')
        return null
      }).catch((error) => {
        message.error(error?.message || '绑定失败')
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

    return {
      submitting,
      bindToken,
      bindForm,
      finishLogin,
      handleBind,
      goToLogin,
      handleFinishFailed,
    }
  },
  components: {
    UserOutlined,
    LockOutlined,
  },
})
</script>

<style scoped>
.bind-alert {
  margin-bottom: 16px;
}
</style>
