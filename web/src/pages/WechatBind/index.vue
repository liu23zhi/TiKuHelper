<template>
  <div class="auth-shell">
    <div class="auth-card auth-card--single">
      <div class="hero hero--wechat-bind">
        <div class="hero-badge">WeChat Bind</div>
        <h1>绑定已有账号</h1>
        <p>微信扫码成功后，请使用已有账号完成绑定。</p>
      </div>

      <a-card :bordered="false" class="form-card">
        <div class="form-header">
          <div>
            <div class="form-title">绑定已有账号</div>
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
import { setCookies } from '../../utils/cookies.js'
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

    const finishLogin = (jwt) => {
      setCookies('token', jwt)
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
.auth-shell {
  width: 100%;
  display: flex;
  justify-content: center;
  padding: 24px;
}

.auth-card {
  width: min(1080px, 100%);
  display: grid;
  grid-template-columns: minmax(320px, 1fr) minmax(360px, 440px);
  gap: 24px;
  align-items: stretch;
}

.hero {
  min-height: 520px;
  border-radius: 28px;
  padding: 40px;
  color: #fff;
  display: flex;
  flex-direction: column;
  justify-content: center;
  box-shadow: 0 20px 45px rgba(15, 118, 110, 0.2);
}

.hero--wechat-bind {
  background: linear-gradient(135deg, #0f766e 0%, #0ea5e9 100%);
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

.bind-alert {
  margin-bottom: 16px;
  border-radius: 12px;
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

@media (max-width: 960px) {
  .auth-card {
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
