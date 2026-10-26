<template>
  <div class="layout-shell" @keydown.esc="siderCollapsed = true">
    <Header :hasLogout="true" />
    <main class="layout-main">
      <button v-if="compact && !siderCollapsed" class="layout-backdrop" aria-label="收起侧边栏" @click="toggleSider" />
      <aside class="layout-sider" :class="{ 'layout-sider--collapsed': siderCollapsed }">
        <SiderMenu
          :currentUser="currentUser"
          :collapsed="siderCollapsed"
          @toggle="toggleSider"
        />
      </aside>
      <section class="layout-content">
        <router-view></router-view>
      </section>
    </main>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import Header from '../components/PageHeader'
import SiderMenu from '../components/SiderMenu'
import { useRouter } from 'vue-router';
import { getStoredCurrentUser, isAdminUser, resolveHomePath } from '../utils/currentUser'

export default {
  name: 'AdapterLayout',
  setup() {
    const router = useRouter()
    const compactViewport = typeof window !== 'undefined'
      ? window.matchMedia('(max-width: 960px)')
      : null
    const siderCollapsed = ref(Boolean(compactViewport?.matches))
    const compact = ref(Boolean(compactViewport?.matches))
    const currentUser = computed(() => getStoredCurrentUser())
    const toggleSider = () => {
      siderCollapsed.value = !siderCollapsed.value
    }
    const syncSiderToViewport = (event) => {
      compact.value = event.matches
      siderCollapsed.value = event.matches
    }
    onMounted(() => {
      if (compactViewport?.addEventListener) compactViewport.addEventListener('change', syncSiderToViewport)
      else compactViewport?.addListener?.(syncSiderToViewport)
    })
    onBeforeUnmount(() => {
      if (compactViewport?.removeEventListener) compactViewport.removeEventListener('change', syncSiderToViewport)
      else compactViewport?.removeListener?.(syncSiderToViewport)
    })
    if (!currentUser.value) {
      router.push('/login')
      return {
        currentUser,
        compact,
        siderCollapsed,
        toggleSider,
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
      compact,
      siderCollapsed,
      toggleSider,
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
  height: 100vh;
  height: 100dvh;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  background: var(--console-canvas);
}

.layout-main {
  position: relative;
  min-height: 0;
  flex: 1;
  width: 100%;
  display: flex;
}

.layout-sider {
  width: 260px;
  flex-shrink: 0;
  min-height: 0;
  background: var(--console-surface);
  border-right: 1px solid var(--console-border);
  transition: width 160ms ease;
}

.layout-sider--collapsed {
  width: 64px;
}

.layout-content {
  flex: 1;
  min-width: 0;
  padding: 24px;
  overflow-x: hidden;
  overflow-y: auto;
  scrollbar-gutter: stable;
}
.layout-backdrop { position: absolute; inset: 0; z-index: 20; border: 0; background: rgb(23 43 39 / 15%); cursor: pointer; }

@media (max-width: 960px) {
  .layout-sider {
    position: absolute;
    inset: 0 auto 0 0;
    width: 260px;
    z-index: 30;
    box-shadow: 8px 0 24px rgb(23 43 39 / 8%);
  }

  .layout-sider.layout-sider--collapsed {
    width: 64px;
    box-shadow: none;
  }

  .layout-content {
    padding: 16px;
    margin-left: 64px;
  }
}
</style>
