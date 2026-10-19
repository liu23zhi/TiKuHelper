<template>
  <Header/>
  <main class="auth-main">
    <router-view></router-view>
  </main>
  <Footer/>
</template>

<script>
import Header from '../components/PageHeader'
import Footer from '../components/PageFoot'
import { useRouter } from 'vue-router';
import { getStoredCurrentUser, resolveHomePath } from '../utils/currentUser'
export default {
  name: 'LoginLayout',
  setup(){
    const router = useRouter()
    const currentUser = getStoredCurrentUser()
    if(currentUser){
      router.push(resolveHomePath(currentUser))
    }
  },
  components:{
    Header,
    Footer
  }
}
</script>

<style scoped>
.auth-main {
  width: 100%;
  min-height: calc(100vh - 120px);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 40px 24px;
  background: var(--console-canvas, #f5f7fb);
}

.auth-main :deep(.auth-shell) {
  width: min(1040px, 100%);
  min-width: 0;
  color: var(--console-text, #1f2937);
}

.auth-main :deep(.auth-card) {
  display: grid;
  grid-template-columns: minmax(0, 0.9fr) minmax(0, 1fr);
  gap: 32px;
  align-items: start;
}

.auth-main :deep(.hero) {
  padding: 32px;
  border: 1px solid var(--console-border, #e5e7eb);
  border-top: 3px solid var(--console-primary, #2563eb);
  border-radius: var(--console-radius, 12px);
  background: #eef3fa;
  overflow-wrap: anywhere;
}

.auth-main :deep(.hero-badge) {
  margin-bottom: 24px;
  color: var(--console-primary, #2563eb);
  font-size: 13px;
  font-weight: 600;
}

.auth-main :deep(.hero h1) {
  margin: 0;
  color: var(--console-text, #1f2937);
  font-size: 32px;
  font-weight: 700;
  line-height: 1.35;
  letter-spacing: 0;
}

.auth-main :deep(.hero p) {
  margin: 16px 0 0;
  color: var(--console-muted, #64748b);
  font-size: 14px;
  line-height: 1.8;
}

.auth-main :deep(.auth-guide) {
  display: grid;
  gap: 20px;
  margin: 32px 0 0;
  padding-top: 24px;
  border-top: 1px solid #dbe3ef;
}

.auth-main :deep(.auth-guide dt) {
  margin-bottom: 5px;
  color: var(--console-text, #1f2937);
  font-size: 14px;
  font-weight: 600;
}

.auth-main :deep(.auth-guide dd) {
  margin: 0;
  color: var(--console-muted, #64748b);
  font-size: 13px;
  line-height: 1.7;
}

.auth-main :deep(.form-card) {
  min-width: 0;
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: var(--console-radius, 12px);
  background: var(--console-surface, #fff);
  box-shadow: 0 4px 16px rgb(15 23 42 / 3%);
}

.auth-main :deep(.form-card > .ant-card-body) {
  padding: 28px;
}

.auth-main :deep(.form-header) {
  margin-bottom: 24px;
  padding-bottom: 20px;
  border-bottom: 1px solid var(--console-border, #e5e7eb);
}

.auth-main :deep(.form-title) {
  margin: 0;
  color: var(--console-text, #1f2937);
  font-size: 23px;
  font-weight: 700;
  line-height: 1.4;
}

.auth-main :deep(.form-subtitle) {
  margin-top: 8px;
  color: var(--console-muted, #64748b);
  font-size: 13px;
  line-height: 1.7;
}

.auth-main :deep(.form .ant-form-item) {
  margin-bottom: 18px;
}

.auth-main :deep(.form .ant-input-affix-wrapper-lg),
.auth-main :deep(.form input.ant-input-lg) {
  font-size: 14px;
}

.auth-main :deep(.sms-code-row) {
  display: flex;
  gap: 8px;
}

.auth-main :deep(.sms-code-row > .ant-input-affix-wrapper) {
  flex: 1;
  min-width: 0;
}

.auth-main :deep(.sms-code-row > .ant-btn) {
  flex-shrink: 0;
  height: 40px;
}

.auth-main :deep(.submit-button),
.auth-main :deep(.secondary-button) {
  height: 40px;
  border-radius: 8px;
  font-weight: 600;
}

.auth-main :deep(.auth-actions) {
  display: grid;
  gap: 12px;
  padding-top: 20px;
  border-top: 1px solid var(--console-border, #e5e7eb);
}

.auth-main :deep(.action-title) {
  color: var(--console-text, #1f2937);
  font-size: 14px;
  font-weight: 600;
}

.auth-main :deep(.action-subtitle) {
  margin: 0;
  color: var(--console-muted, #64748b);
  font-size: 13px;
  line-height: 1.7;
}

.auth-main :deep(.wechat-qrcode-frame) {
  width: min(220px, 100%);
  aspect-ratio: 1;
  margin: 0 auto;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: 8px;
  background: #fff;
}

.auth-main :deep(.wechat-qrcode-frame img) {
  width: 100%;
  height: 100%;
  object-fit: contain;
}

.auth-main :deep(.wechat-qrcode-empty),
.auth-main :deep(.wechat-status) {
  color: var(--console-muted, #64748b);
  font-size: 13px;
  text-align: center;
  line-height: 1.7;
  overflow-wrap: anywhere;
}

.auth-main :deep(.form-section-heading) {
  margin: 4px 0 16px;
  color: var(--console-text, #1f2937);
  font-size: 14px;
  font-weight: 600;
}

.auth-main :deep(.login-mode-switch) {
  display: flex;
  width: 100%;
  margin-bottom: 20px;
}

.auth-main :deep(.login-mode-switch .ant-radio-button-wrapper) {
  flex: 1;
  padding-inline: 8px;
  text-align: center;
  white-space: normal;
}

.auth-main :deep(.wechat-login-panel) {
  display: grid;
  gap: 14px;
  margin-bottom: 20px;
}

.auth-main :deep(.wechat-login-panel .wechat-mode-switch),
.auth-main :deep(.wechat-register-panel .ant-radio-group) {
  width: 100%;
  display: flex;
}

.auth-main :deep(.wechat-login-panel .ant-radio-button-wrapper),
.auth-main :deep(.wechat-register-panel .ant-radio-button-wrapper) {
  flex: 1;
  text-align: center;
}

.auth-main :deep(.wechat-register-panel) {
  margin: 8px 0 20px;
  padding: 18px;
  border: 1px solid var(--console-border, #e5e7eb);
  border-radius: 8px;
  background: #f7f9fc;
}

.auth-main :deep(.wechat-register-intro),
.auth-main :deep(.wechat-register-actions) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  color: var(--console-muted, #64748b);
  font-size: 13px;
}

.auth-main :deep(.wechat-register-panel .wechat-qrcode-frame) {
  width: min(180px, 100%);
  margin: 14px auto 10px;
}

.auth-main :deep(.wechat-register-panel .wechat-status) {
  margin: 8px 0 12px;
}

.auth-main :deep(.wechat-register-staged) {
  color: #15803d;
}

.auth-main :deep(.register-alert),
.auth-main :deep(.bind-alert) {
  margin: 0 0 16px;
  border-radius: 8px;
}

@media (max-width: 768px) {
  .auth-main {
    align-items: flex-start;
    padding: 24px 16px;
  }

  .auth-main :deep(.auth-card),
  .auth-main :deep(.auth-card--single) {
    grid-template-columns: minmax(0, 1fr);
    gap: 16px;
  }

  .auth-main :deep(.hero),
  .auth-main :deep(.form-card > .ant-card-body) {
    padding: 24px;
  }

  .auth-main :deep(.hero-badge) {
    margin-bottom: 12px;
  }

  .auth-main :deep(.auth-guide) {
    gap: 14px;
    margin-top: 20px;
    padding-top: 16px;
  }

  .auth-main :deep(.wechat-register-panel) {
    padding: 14px;
  }

  .auth-main :deep(.wechat-register-intro),
  .auth-main :deep(.wechat-register-actions) {
    align-items: flex-start;
    flex-direction: column;
  }
}

@media (max-width: 380px) {
  .auth-main :deep(.hero),
  .auth-main :deep(.form-card > .ant-card-body) {
    padding: 18px;
  }

  .auth-main :deep(.sms-code-row) {
    flex-direction: column;
  }
}
</style>
