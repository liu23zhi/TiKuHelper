<template>
  <div class="layout-shell">
    <Header :hasLogout="true" />
    <main class="layout-main">
      <aside class="layout-sider">
        <SiderMenu :currentUser="currentUser" />
      </aside>
      <section class="layout-content">
        <router-view></router-view>
      </section>
    </main>
  </div>
</template>

<script>
import { computed } from 'vue'
import Header from '../components/PageHeader'
import SiderMenu from '../components/SiderMenu'
import { useRouter } from 'vue-router';
import { getStoredCurrentUser, isAdminUser, resolveHomePath } from '../utils/currentUser'

export default {
  name: 'AdapterLayout',
  setup() {
    const router = useRouter()
    const currentUser = computed(() => getStoredCurrentUser())
    if (!currentUser.value) {
      router.push('/login')
      return {
        currentUser,
      }
    }

    const routePath = String(router.currentRoute.value?.path || '')
    if (!isAdminUser(currentUser.value)) {
      const adminOnlyPrefixes = [
        '/adapter/component',
        '/adapter/import',
        '/adapter/userlist',
        '/adapter/loglist',
        '/adapter/search-account-config',
        '/adapter/ai-search-config',
        '/adapter/system-settings',
      ]
      if (adminOnlyPrefixes.some(prefix => routePath.startsWith(prefix))) {
        router.replace(resolveHomePath(currentUser.value))
      }
    }

    return {
      currentUser,
    }
  },
  components: {
    Header,
    SiderMenu
  }
}
</script>

<style scoped>
.layout-shell {
  min-height: 100%;
  background: #f5f7fb;
}

.layout-main {
  min-height: calc(100vh - 64px);
  width: 100%;
  display: flex;
}

.layout-sider {
  width: 260px;
  flex-shrink: 0;
  background: #ffffff;
  border-right: 1px solid #eef2f7;
  box-shadow: 0 8px 24px rgba(15, 23, 42, 0.04);
}

.layout-content {
  flex: 1;
  min-width: 0;
  padding: 24px;
  overflow-x: hidden;
  overflow-y: auto;
}

@media (max-width: 960px) {
  .layout-main {
    flex-direction: column;
  }

  .layout-sider {
    width: 100%;
    border-right: none;
    border-bottom: 1px solid #eef2f7;
  }

  .layout-content {
    padding: 16px;
  }
}
</style>
