<template>
  <header class="page-header">
    <div class="brand">
      <ReadOutlined />
      <span class="brand-text">题库管理平台</span>
    </div>
    <div v-if="hasLogout" class="actions">
      <a-tooltip placement="bottom">
        <template #title>
          <span>退出登录</span>
        </template>
        <a-button type="text" class="logout-btn" @click="logoutOut">
          <LogoutOutlined />
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
      navigateToImport
    }
  },
  components: {
    ReadOutlined,
    LogoutOutlined
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
  background: linear-gradient(90deg, #195ca3 0%, #2563eb 100%);
  border-bottom: 1px solid #e8e8e8;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px;
  color: #fff;
  box-shadow: 0 10px 30px rgba(25, 92, 163, 0.15);
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 20px;
  font-weight: 700;
}

.brand-text {
  color: #fff;
}

.actions {
  display: flex;
  align-items: center;
}

.logout-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: #fff;
  border-radius: 10px;
}

.logout-btn:hover,
.logout-btn:focus {
  color: #fff;
  background: rgba(255, 255, 255, 0.12);
}
</style>
