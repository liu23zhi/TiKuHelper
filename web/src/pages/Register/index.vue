<template>
  <div class="auth-shell">
    <div class="auth-card auth-card--single">
      <div class="hero hero--register">
        <div class="hero-badge">Public Register</div>
        <h1>创建普通用户账号</h1>
        <p>
          注册成功后即可使用新账号登录后台。
        </p>
      </div>

      <a-card :bordered="false" class="form-card">
        <div class="form-header">
          <div>
            <div class="form-title">公开注册</div>
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
          <a-form-item name="username" label="用户名" :rules="[{ required: true, message: '请输入用户名' }]">
            <a-input v-model:value="registerForm.username" placeholder="请输入用户名" size="large">
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
          <a-form-item name="phone" label="手机号" :rules="[{ required: true, message: '请输入手机号' }]">
            <a-input v-model:value="registerForm.phone" placeholder="请输入手机号" size="large">
              <template #prefix>
                <MobileOutlined style="color: rgba(0, 0, 0, 0.25)" />
              </template>
            </a-input>
          </a-form-item>
          <a-form-item name="smsCode" label="验证码" :rules="[{ required: true, message: '请输入验证码' }]">
            <div class="sms-code-row">
              <a-input v-model:value="registerForm.smsCode" placeholder="请输入验证码" size="large">
                <template #prefix>
                  <SafetyCertificateOutlined style="color: rgba(0, 0, 0, 0.25)" />
                </template>
              </a-input>
              <a-button :loading="sendingCode" @click="sendRegisterCode">发送验证码</a-button>
            </div>
          </a-form-item>
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
import { defineComponent, reactive, ref } from 'vue';
import { UserOutlined, LockOutlined, IdcardOutlined, MobileOutlined, SafetyCertificateOutlined } from '@ant-design/icons-vue';
import { registerUser, sendPublicSMSCode } from '../../api/TikuComponentApi.js'
import { buildPublicRegisterPayload, buildPublicSMSCodePayload } from '../../utils/phoneAuthHelpers.mjs'
import { message } from 'ant-design-vue';
import { useRouter } from 'vue-router';

export default defineComponent({
  name: 'AdapterRegister',
  setup() {
    const router = useRouter()
    const submitting = ref(false)
    const sendingCode = ref(false)
    const registerDisabledNotice = ref('')
    const registerForm = reactive({
      username: '',
      password: '',
      nickname: '',
      phone: '',
      smsCode: '',
    })

    const resetRegisterForm = () => {
      registerForm.username = ''
      registerForm.password = ''
      registerForm.nickname = ''
      registerForm.phone = ''
      registerForm.smsCode = ''
    }

    const sendRegisterCode = () => {
      sendingCode.value = true
      sendPublicSMSCode(buildPublicSMSCodePayload(registerForm, 'register')).then((res) => {
        if (res?.status === 200) {
          message.success(res?.message || '发送成功，请注意查收')
        } else {
          message.error(res?.message || '发送失败')
        }
      }).catch((error) => {
        message.error(error?.message || '发送失败')
      }).finally(() => {
        sendingCode.value = false
      })
    }

    const handleRegister = () => {
      registerDisabledNotice.value = ''
      submitting.value = true
      let payload
      try {
        payload = buildPublicRegisterPayload(registerForm)
      } catch (error) {
        submitting.value = false
        message.error(error?.message || '注册失败')
        return
      }

      registerUser(payload).then((res) => {
        if (res?.status === 200) {
          message.success('注册成功，请使用新账号登录')
          const username = registerForm.username.trim()
          resetRegisterForm()
          router.push({ path: '/login', query: { username } })
        } else {
          if (res?.message === '未开放注册') {
            registerDisabledNotice.value = '当前暂不可注册，请返回登录页。'
          }
          message.error(res?.message || '注册失败')
        }
      }).catch((error) => {
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

    return {
      submitting,
      sendingCode,
      registerDisabledNotice,
      registerForm,
      handleRegister,
      sendRegisterCode,
      goToLogin,
      handleFinishFailed,
    }
  },
  components: {
    UserOutlined,
    LockOutlined,
    IdcardOutlined,
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

.hero--register {
  background: linear-gradient(135deg, #0f766e 0%, #10b981 52%, #6ee7b7 100%);
  box-shadow: 0 20px 45px rgba(16, 185, 129, 0.24);
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

.register-alert {
  margin-top: 4px;
  margin-bottom: 16px;
  border-radius: 12px;
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
