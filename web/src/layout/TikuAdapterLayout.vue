<template>
  <div class="layout-shell">
    <a class="skip-link" href="#portal-main-content" @click.prevent="focusContent">跳至主要内容</a>
    <Header :hasLogout="true" />
    <div v-if="isMobile" class="mobile-navigation-bar">
      <a-button ref="mobileMenuButton" class="mobile-menu-button" aria-label="打开导航"
        aria-controls="mobile-portal-navigation" :aria-expanded="drawerVisible" @click="openMobileMenu">
        <MenuOutlined aria-hidden="true" /><span>导航</span>
      </a-button>
      <span class="mobile-workspace-label">{{ isAdmin ? '管理工作台' : '个人工作台' }}</span>
    </div>
    <div class="layout-main">
      <aside v-if="!isMobile" class="layout-sider" :class="{ 'layout-sider--collapsed': siderCollapsed }">
        <SiderMenu :currentUser="currentUser" :collapsed="siderCollapsed" @toggle="toggleSider" />
      </aside>
      <main ref="contentRoot" id="portal-main-content" class="layout-content" tabindex="-1" aria-label="主要内容">
        <router-view v-if="canAccessRoute"></router-view>
      </main>
    </div>
    <a-drawer v-if="isMobile && drawerRendered" :open="drawerVisible" placement="left"
      width="min(88vw, 340px)" :closable="false" :destroy-on-close="true"
      :body-style="{ padding: 0 }" root-class-name="mobile-navigation-drawer"
      role="dialog" aria-modal="true" aria-labelledby="mobile-navigation-title"
      @after-open-change="afterDrawerVisibilityChange" @close="closeMobileMenu()">
      <div ref="navigationDialog" class="mobile-navigation-dialog" @keydown="handleNavigationKeydown">
        <div class="mobile-navigation-heading">
          <h2 id="mobile-navigation-title">导航</h2>
          <a-button type="text" class="mobile-close-button" aria-label="关闭导航" @click="closeMobileMenu()">
            <CloseOutlined aria-hidden="true" />
          </a-button>
        </div>
        <SiderMenu :currentUser="currentUser" :mobile="true" :show-collapse="false"
          navigation-id="mobile-portal-navigation" @navigate="handleMobileNavigate" />
      </div>
    </a-drawer>
  </div>
</template>

<script>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Drawer, message } from 'ant-design-vue'
import { CloseOutlined, MenuOutlined } from '@ant-design/icons-vue'
import Header from '../components/PageHeader'
import SiderMenu from '../components/SiderMenu'
import { useRouter } from 'vue-router'
import { useCurrentUserDisplay, isAdminUser, resolveHomePath } from '../utils/currentUser'

export default {
  name: 'AdapterLayout',
  components: { Header, SiderMenu, ADrawer: Drawer, MenuOutlined, CloseOutlined },
  setup() {
    const router = useRouter()
    const phoneQuery = window.matchMedia('(max-width: 767px)')
    const compactQuery = window.matchMedia('(max-width: 1199px)')
    const readMode = () => phoneQuery.matches ? 'phone' : compactQuery.matches ? 'tablet' : 'desktop'
    let mode = readMode()
    let mounted = true
    let closeTimer
    let focusDestination = null
    const isMobile = ref(mode === 'phone')
    const siderCollapsed = ref(mode === 'tablet')
    const drawerVisible = ref(false)
    const drawerRendered = ref(false)
    const mobileMenuButton = ref(null)
    const contentRoot = ref(null)
    const navigationDialog = ref(null)
    const { currentUser } = useCurrentUserDisplay()
    const isAdmin = computed(() => isAdminUser(currentUser.value))
    const adminOnlyPrefixes = [
      '/adapter/component', '/adapter/import', '/adapter/userlist', '/adapter/loglist',
      '/adapter/search-account-config', '/adapter/ai-search-config', '/adapter/system-settings',
      '/adapter/ip-location-cache', '/adapter/phone-location-cache',
    ]
    const isAdminOnlyCurrentRoute = computed(() => (router.currentRoute.value?.matched || [])
      .some(record => adminOnlyPrefixes.includes(record.aliasOf?.path || record.path)))
    const canAccessRoute = computed(() => Boolean(currentUser.value) && (!isAdminOnlyCurrentRoute.value || isAdmin.value))
    const triggerElement = () => mobileMenuButton.value?.$el || mobileMenuButton.value
    const focusContent = () => contentRoot.value?.focus({ preventScroll: true })
    const focusTrigger = () => triggerElement()?.focus({ preventScroll: true })
    const focusClose = () => navigationDialog.value?.querySelector('[aria-label="关闭导航"]')?.focus()
    const finishClosing = () => {
      if (drawerVisible.value || !mounted) return
      window.clearTimeout(closeTimer)
      const destination = focusDestination
      focusDestination = null
      drawerRendered.value = false
      nextTick(() => {
        if (!mounted || !destination) return
        if (destination === 'trigger' && isMobile.value) focusTrigger()
        else focusContent()
      })
    }
    const openMobileMenu = () => {
      window.clearTimeout(closeTimer)
      focusDestination = null
      drawerRendered.value = true
      drawerVisible.value = true
      nextTick(focusClose)
    }
    const closeMobileMenu = (destination = 'trigger') => {
      if (!drawerVisible.value) return
      focusDestination = destination
      drawerVisible.value = false
      // Normal cleanup follows transitionend; the fallback also handles disabled/interrupted motion.
      closeTimer = window.setTimeout(finishClosing, 240)
    }
    const afterDrawerVisibilityChange = (visible) => {
      if (visible && drawerVisible.value) focusClose()
      else if (!visible) finishClosing()
    }
    const handleMobileNavigate = () => closeMobileMenu('content')
    const handleNavigationKeydown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        event.stopPropagation()
        closeMobileMenu()
        return
      }
      if (event.key !== 'Tab') return
      const dialog = navigationDialog.value
      const elements = Array.from(dialog.querySelectorAll('button:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])'))
        .filter(element => !element.closest('[aria-hidden="true"]') && window.getComputedStyle(element).display !== 'none')
      const first = elements[0]
      const last = elements[elements.length - 1]
      if (!first) return
      const active = document.activeElement
      if (event.shiftKey && (active === first || !dialog.contains(active))) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && (active === last || !dialog.contains(active))) {
        event.preventDefault()
        first.focus()
      }
    }
    const toggleSider = () => { siderCollapsed.value = !siderCollapsed.value }
    const syncViewport = () => {
      const nextMode = readMode()
      if (nextMode === mode) return
      const wasOpen = drawerVisible.value
      const triggerFocused = document.activeElement === triggerElement()
      const siderFocused = !!document.activeElement?.closest('.layout-sider')
      mode = nextMode
      isMobile.value = mode === 'phone'
      siderCollapsed.value = mode === 'tablet'
      drawerVisible.value = false
      drawerRendered.value = false
      focusDestination = null
      window.clearTimeout(closeTimer)
      nextTick(() => {
        if (!mounted) return
        if (wasOpen || triggerFocused) focusContent()
        else if (siderFocused && isMobile.value) focusTrigger()
      })
    }
    const observe = (query, add) => {
      if (query.addEventListener) query[add ? 'addEventListener' : 'removeEventListener']('change', syncViewport)
      else query[add ? 'addListener' : 'removeListener'](syncViewport)
    }
    onMounted(() => { observe(phoneQuery, true); observe(compactQuery, true) })
    onBeforeUnmount(() => {
      mounted = false
      window.clearTimeout(closeTimer)
      observe(phoneQuery, false)
      observe(compactQuery, false)
    })
    watch(() => router.currentRoute.value.fullPath, () => {
      if (drawerVisible.value) closeMobileMenu('content')
    })
    watch(() => Boolean(currentUser.value), (hasIdentity) => {
      if (hasIdentity) return
      // Hide and unmount the private leaf first; login is not an unsaved-draft decision.
      nextTick(() => {
        if (!mounted || currentUser.value) return
        drawerVisible.value = false
        drawerRendered.value = false
        router.replace('/login').catch(() => {
          if (mounted && !currentUser.value) message.error('登录状态已失效，暂时无法打开登录页，请刷新重试。')
        })
      })
    }, { flush: 'post' })
    watch([isAdmin, isAdminOnlyCurrentRoute], ([admin, adminOnly]) => {
      if (!currentUser.value || admin || !adminOnly) return
      // Wait for RouterView's unmount callbacks to clear the old route instance
      // before extracting leave guards. The unauthorized child is already hidden.
      nextTick(() => {
        if (!mounted || !currentUser.value || isAdmin.value || !isAdminOnlyCurrentRoute.value) return
        message.warning({
          key: 'admin-access-revoked',
          content: '管理权限已变更，已返回个人工作台；未保存内容已丢弃。',
        })
        const navigationFailed = () => {
          if (mounted) message.error('权限已变更，暂时无法打开个人工作台，请刷新重试。')
        }
        router.replace(resolveHomePath(currentUser.value)).then(failure => {
          if (failure) navigationFailed()
          else nextTick(() => { if (mounted) focusContent() })
        }).catch(navigationFailed)
      })
    }, { flush: 'post' })
    if (!currentUser.value) router.push('/login')
    else if (!isAdmin.value && isAdminOnlyCurrentRoute.value) router.replace(resolveHomePath(currentUser.value))
    return {
      currentUser, isAdmin, canAccessRoute, isMobile, siderCollapsed, toggleSider,
      drawerVisible, drawerRendered, mobileMenuButton, contentRoot, navigationDialog,
      openMobileMenu, closeMobileMenu, handleMobileNavigate, handleNavigationKeydown,
      afterDrawerVisibilityChange, focusContent,
    }
  },
}
</script>

<style scoped>
.layout-shell { min-height: 100vh; min-height: 100dvh; background: var(--bg); }
.layout-main { display: flex; width: 100%; min-height: calc(100vh - var(--layout-header-height)); min-height: calc(100dvh - 64px); }
.layout-sider {
  position: sticky; top: 0; align-self: flex-start;
  width: var(--layout-sider-width); max-height: 100vh; max-height: 100dvh; overflow-y: auto; flex-shrink: 0;
  background: var(--surface); border-right: var(--layout-divider-width) solid var(--border);
  box-shadow: var(--layout-sider-shadow-geometry) var(--shadow-color); transition: width 0.18s ease;
}
.layout-sider--collapsed { width: var(--layout-sider-collapsed-width); }
.layout-content { flex: 1; min-width: 0; padding: var(--layout-content-padding-desktop); }
.layout-content :deep(.ant-table-content), .layout-content :deep(.ant-table-body) { max-width: 100%; overflow-x: auto; }
.layout-content :deep(.ant-card) { min-width: 0; }
.skip-link {
  position: absolute; top: var(--skip-link-inset); left: var(--skip-link-inset); z-index: 1100; padding: var(--skip-link-padding);
  color: var(--on-primary); background: var(--primary); border-radius: var(--control-radius); transform: translateY(-160%);
}
.skip-link:focus { transform: translateY(0); outline: var(--focus-outline-width) solid var(--focus); outline-offset: var(--focus-outline-offset); }
.mobile-navigation-bar {
  display: flex; align-items: center; justify-content: space-between; gap: var(--mobile-navigation-gap);
  min-height: var(--mobile-navigation-bar-height); padding: var(--mobile-navigation-bar-padding); background: var(--surface); border-bottom: var(--layout-divider-width) solid var(--border);
}
.mobile-menu-button, .mobile-close-button { min-width: var(--control-touch-min); min-height: var(--control-touch-min); }
.mobile-menu-button { display: inline-flex; align-items: center; gap: var(--mobile-menu-gap); border-radius: var(--mobile-menu-radius); }
.mobile-workspace-label { font-size: 14px; color: var(--text-muted); }
@media (max-width: 767px) {
  .layout-content { padding: var(--layout-content-padding-mobile); }
  .layout-main { min-height: calc(100vh - var(--layout-mobile-stack-height)); min-height: calc(100dvh - 120px); }
}
@media (prefers-reduced-motion: reduce) { .layout-sider, .skip-link { transition: none; } }
</style>

<style>
.mobile-navigation-drawer .ant-drawer-content-wrapper { transition-duration: 180ms !important; }
.mobile-navigation-drawer .ant-drawer-body { padding: var(--g-zero); }
.mobile-navigation-dialog { background: var(--surface); }
.mobile-navigation-heading {
  display: flex; align-items: center; justify-content: space-between;
  min-height: var(--layout-header-height); padding: var(--mobile-navigation-heading-padding); border-bottom: var(--layout-divider-width) solid var(--border);
}
.mobile-navigation-heading h2 { margin: var(--g-zero); font-size: 18px; color: var(--text); }
.mobile-navigation-dialog > nav {
  max-height: calc(100vh - var(--layout-header-height)); max-height: calc(100dvh - 64px);
  overflow-y: auto; overscroll-behavior: contain;
}
.mobile-navigation-drawer button:focus-visible, .mobile-navigation-drawer a:focus-visible { outline: var(--focus-outline-width) solid var(--focus); outline-offset: var(--focus-outline-offset); }
@media (prefers-reduced-motion: reduce) { .mobile-navigation-drawer .ant-drawer-content-wrapper { transition-duration: 1ms !important; } }
</style>
