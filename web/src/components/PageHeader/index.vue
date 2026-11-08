<template>
  <header class="page-header">
    <div class="brand">
      <ReadOutlined aria-hidden="true" />
      <span class="brand-text">{{ siteName }}</span>
      <span class="source-version" :title="sourceRevision" aria-label="源码版本">{{ sourceRevision.slice(0, 8) }}</span>
    </div>
    <div class="actions">
      <ThemeControl />
      <a-tooltip v-if="hasLogout" placement="bottom">
        <template #title>
          <span>退出登录</span>
        </template>
        <a-button type="text" class="logout-btn" @click="logoutOut">
          <LogoutOutlined aria-hidden="true" />
          <span>退出</span>
        </a-button>
      </a-tooltip>
    </div>
  </header>
</template>

<script>
import {
  ReadOutlined,
  LogoutOutlined
} from '@ant-design/icons-vue';
import { clearStoredCurrentUser } from '../../utils/currentUser.js'
import { logout } from '../../api/TikuComponentApi.js'
import { useRouter } from 'vue-router';
import { defineComponent } from 'vue';
import { siteName } from '../../utils/siteName.js'
import ThemeControl from '../ThemeControl'

export default defineComponent({
  name: 'PageHeader',
  props: {
    hasLogout: {
      type: Boolean,
      default: false
    }
  },
  setup() {
    const router = useRouter()
    const navigateToImport = () => {
      router.push('/login');
    };
    return {
      siteName,
      sourceRevision: process.env.VUE_APP_SOURCE_REVISION || 'development',
      navigateToImport
    }
  },
  components: {
    ReadOutlined,
    LogoutOutlined,
    ThemeControl
  },
  methods: {
    logoutOut() {
      logout().finally(() => {
        clearStoredCurrentUser()
        this.navigateToImport()
      })
    }
  }

})
</script>

<style scoped>
.page-header {
  width: 100%;
  min-height: 64px;
  flex-shrink: 0;
  background: var(--console-surface);
  border-bottom: 1px solid var(--console-border);
  border-top: 2px solid var(--console-primary);
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px;
  color: var(--console-text);
}
.brand {
  display: flex;
  align-items: center;
  gap: var(--header-brand-gap);
  min-width: 0;
  font-size: 20px;
  font-weight: 700;
  min-width: 0;
}

.brand-text {
  color: var(--console-text);
  overflow-wrap: anywhere;
}

.actions {
  display: flex;
  align-items: center;
  flex-shrink: 0;
}

.logout-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--console-text);
  border-radius: 6px;
}
.logout-btn:hover,
.logout-btn:focus {
  color: var(--console-primary);
  background: var(--console-selected);
}
@media (max-width: 768px) { .page-header { padding: 0 16px; } .brand { font-size: 18px; } }
.source-version { font-size: 11px; font-family: monospace; font-weight: 400; color: var(--console-muted); overflow-wrap: anywhere; }
</style>
