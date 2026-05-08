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

        <a-form
          layout="vertical"
          :model="loginForm"
          @finish="handleLogin"
          @finishFailed="handleFinishFailed"
          class="form"
        >
          <a-form-item name="username" label="用户名" :rules="[{ required: true, message: '请输入用户名' }]">
            <a-input v-model:value="loginForm.username" placeholder="请输入用户名" size="large">
              <template #prefix>
                <UserOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
            </a-input>
          </a-form-item>
          <a-form-item name="password" label="密码" :rules="[{ required: true, message: '请输入密码' }]">
            <a-input-password v-model:value="loginForm.password" placeholder="请输入密码" size="large">
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
              :disabled="!loginForm.username || !loginForm.password"
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
import { defineComponent, reactive, ref } from 'vue';
import { UserOutlined, LockOutlined } from '@ant-design/icons-vue';
import Swal from 'sweetalert2'
import { login, getCurrentUser, getPublicRegisterStatus } from '../../api/TikuComponentApi.js'
import { setCookies } from '../../utils/cookies.js'
import { setStoredCurrentUser, resolveHomePath } from '../../utils/currentUser.js'
import { message } from 'ant-design-vue';
import { useRoute, useRouter } from 'vue-router';

function showRegisterDisabledAlert() {
  return Swal.fire({
    icon: 'warning',
    title: '未开启公共注册',
    text: '当前站点暂未开放公共注册，请联系管理员处理。',
    confirmButtonText: '我知道了'
  })
}

export default defineComponent({
  name: 'AdapterLogin',
  setup() {
    const router = useRouter()
    const route = useRoute()
    const submitting = ref(false)
    const loginForm = reactive({
      username: String(route.query.username || ''),
      password: '',
    })

    const handleLogin = () => {
      submitting.value = true
      login(loginForm).then((res) => {
        if (res?.status === 200) {
          setCookies('token', res?.data?.jwt)
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
        message.error(res?.message || '登录失败')
        return null
      }).catch((error) => {
        console.log(error)
        message.error('登录失败')
      }).finally(() => {
        submitting.value = false
      })
    }

    const goToRegister = () => {
      getPublicRegisterStatus().then((res) => {
        if (res?.status === 200 && res?.data?.registerEnabled) {
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

    return {
      submitting,
      loginForm,
      handleLogin,
      goToRegister,
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
