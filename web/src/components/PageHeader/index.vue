<template>
  <header class="page-header">
    <div class="brand">
      <ReadOutlined aria-hidden="true" />
      <span class="brand-text">{{ siteName }}</span>
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
  min-height: var(--layout-header-height);
  box-sizing: border-box;
  background: var(--surface, var(--surface));
  border-bottom: var(--layout-divider-width) solid var(--border, #D6D6DA);
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--header-gap);
  padding: var(--header-padding);
  color: var(--text, var(--text));
}
.brand {
  display: flex;
  align-items: center;
  gap: var(--header-brand-gap);
  min-width: 0;
  font-size: 20px;
  font-weight: 700;
}
.brand-text { color: var(--text, var(--text)); overflow-wrap: anywhere; }
.actions { display: flex; align-items: center; flex-shrink: 0; gap: var(--header-actions-gap); }
.logout-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: var(--control-icon-label-gap);
  min-width: var(--header-logout-min-width);
  min-height: var(--control-touch-min);
  color: var(--text, var(--text));
  border-radius: var(--control-radius);
}
.logout-btn:hover,
.logout-btn:focus {
  color: var(--link, #0066CC);
  background: var(--surface-subtle, #F5F5F7);
}
.logout-btn:focus-visible { outline: var(--focus-outline-width) solid var(--focus, #0066CC); outline-offset: var(--focus-outline-offset); }
@media (max-width: 480px) {
  .page-header { flex-wrap: wrap; gap: var(--header-gap-mobile); padding: var(--header-padding-mobile); }
  .brand { flex: 1 1 var(--header-brand-basis-mobile); gap: var(--header-brand-gap-mobile); font-size: 18px; }
  .actions { margin-left: var(--g-auto); }
}
</style>
