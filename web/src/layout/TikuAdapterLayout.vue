<template>
  <div class="layout-shell">
    <a class="skip-link" href="#portal-main-content" @click.prevent="focusContent">跳至主要内容</a>
    <PageHeader :hasLogout="true" />
    <div v-if="isMobile" class="mobile-navigation-bar">
      <a-button ref="mobileMenuButton" class="mobile-menu-button" aria-label="打开导航"
        aria-controls="mobile-portal-navigation" :aria-expanded="drawerVisible" @click="openMobileMenu">
        <MenuOutlined aria-hidden="true" /><span>导航</span>
      </a-button>
      <span class="mobile-workspace-label">{{ isAdmin ? '管理工作台' : '个人工作台' }}</span>
    </div>
    <div class="layout-main">
      <aside v-if="!isMobile" ref="desktopNavigation" class="layout-sider" :class="{ 'layout-sider--collapsed': siderCollapsed }"
             @click.capture="captureDesktopNavigation" @keydown.capture="captureDesktopNavigation">
        <SiderMenu :currentUser="currentUser" :collapsed="siderCollapsed" @toggle="toggleSider"
                   @navigation-settled="settleDesktopNavigation" />
      </aside>
      <main ref="contentRoot" id="portal-main-content" class="layout-content" tabindex="-1" aria-label="主要内容">
        <router-view v-if="canAccessRoute"></router-view>
      </main>
    </div>
    <a-drawer v-if="isMobile && drawerRendered" :key="mobileDrawerEntry" :open="drawerVisible" placement="left"
      width="min(88vw, 340px)" :closable="false" :destroy-on-close="true"
      :body-style="{ padding: 0 }" root-class-name="mobile-navigation-drawer"
      @after-open-change="mobileDrawerAfterChange" @close="closeMobileMenu()">
      <div ref="navigationDialog" class="mobile-navigation-dialog" role="dialog"
        aria-modal="true" aria-labelledby="mobile-navigation-title" @keydown="handleNavigationKeydown">
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
import PageHeader from '../components/PageHeader'
import SiderMenu from '../components/SiderMenu'
import { useRouter } from 'vue-router'
import { useCurrentUserDisplay, isAdminUser, resolveHomePath, getCurrentUserLogoutEpoch, getCurrentSessionRevision } from '../utils/currentUser'
import { currentUserIdentity } from '../utils/currentUserScope'
import { checkUserSession } from '../api/TikuComponentApi'

export default {
  name: 'AdapterLayout',
  components: { PageHeader, SiderMenu, ADrawer: Drawer, MenuOutlined, CloseOutlined },
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
    const desktopNavigation = ref(null)
    const { currentUser } = useCurrentUserDisplay()
    const isAdmin = computed(() => isAdminUser(currentUser.value))
    const adminOnlyPrefixes = [
      '/adapter/component', '/adapter/import', '/adapter/userlist', '/adapter/loglist',
      // 已归档账号管理（task_doc/76 §3.2.3）：非管理员不仅看不到菜单项，
      // 直接敲 URL 也进不来（canAccessRoute 在这里就 false）。
      '/adapter/archived-users',
      '/adapter/search-account-config', '/adapter/ai-search-config', '/adapter/ocr-cache', '/adapter/system-settings', '/adapter/extension-updates',
      '/adapter/ip-location-cache', '/adapter/phone-location-cache',
    ]
    const isAdminOnlyCurrentRoute = computed(() => (router.currentRoute.value?.matched || [])
      .some(record => adminOnlyPrefixes.includes(record.aliasOf?.path || record.path)))
    const canAccessRoute = computed(() => Boolean(currentUser.value) && (!isAdminOnlyCurrentRoute.value || isAdmin.value))
    const triggerElement = () => mobileMenuButton.value?.$el || mobileMenuButton.value
    const focusContent = () => contentRoot.value?.focus({ preventScroll: true })
    const focusTrigger = () => triggerElement()?.focus({ preventScroll: true })
    const focusClose = () => navigationDialog.value?.querySelector('[aria-label="关闭导航"]')?.focus()
    let desktopFocusTicket = null
    let mobileCloseFocusTicket = null
    let mobileOpenFocusOwner = null
    let navigationFocusRevision = 0
    let viewportFocusVersion = 0
    const mobileDrawerEntry = ref(0)
    const mobileDrawerAfterChange = ref(null)
    const desktopFocusOwner = () => [currentUserIdentity(currentUser.value),
      String(currentUser.value?.role || '').trim().toLowerCase(),
      String(currentUser.value?.status ?? ''), getCurrentUserLogoutEpoch()]
    watch(desktopFocusOwner, (next, previous) => {
      if (next.some((value, index) => value !== previous[index])) {
        desktopFocusTicket = null
        mobileOpenFocusOwner = null
        if (mobileCloseFocusTicket) mobileCloseFocusTicket.invalidated = true
      }
    }, { flush: 'sync' })
    const visibleFocusDialog = (ignored) => [...document.querySelectorAll('[role="dialog"], [aria-modal="true"]')].some(dialog => {
      if (ignored?.has(dialog)) return false
      for (let node = dialog; node; node = node.parentElement) {
        const style = window.getComputedStyle(node)
        if (node.hidden || node.inert || node.getAttribute('aria-hidden') === 'true'
          || style.display === 'none' || style.visibility === 'hidden') return false
      }
      return true
    })
    const mobileDrawerScope = () => {
      const inner = navigationDialog.value
      const drawer = inner?.closest('.ant-drawer')
      const ownDialogs = new Set()
      for (let node = inner; node && drawer?.contains(node); node = node.parentElement) {
        if (node.matches('[role="dialog"], [aria-modal="true"]')) ownDialogs.add(node)
      }
      return { drawer, ownDialogs, trigger: triggerElement(), main: contentRoot.value }
    }
    const mobileOwnsFocus = (scope, target) => {
      if (target === document.body || target === document.documentElement) return true
      const dialog = target?.closest?.('[role="dialog"], [aria-modal="true"]')
      if (dialog && !scope.ownDialogs.has(dialog)) return false
      return Boolean(target && (scope.drawer?.contains(target) || target === scope.trigger || target === scope.main))
    }
    const canFocusOpeningDrawer = () => {
      if (!mounted || !isMobile.value || !drawerVisible.value || !mobileOpenFocusOwner
        || !mobileOpenFocusOwner.every((value, index) => value === desktopFocusOwner()[index])) return false
      const scope = mobileDrawerScope()
      const active = document.activeElement
      return !visibleFocusDialog(scope.ownDialogs) && (active === document.body || active === document.documentElement
        || active === scope.trigger || active === scope.drawer || scope.ownDialogs.has(active))
    }
    const captureDesktopNavigation = (event) => {
      desktopFocusTicket = null
      if (!mounted || isMobile.value || !canAccessRoute.value
        || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return
      if (!(event.type === 'click' && event.button === 0)
        && !(event.type === 'keydown' && (event.key === 'Enter' || event.which === 13))) return
      const item = event.target?.closest?.('[role="menuitem"]')
      if (!item || !desktopNavigation.value?.contains(item) || visibleFocusDialog()) return
      desktopFocusTicket = { event, item, opener: document.activeElement, owner: desktopFocusOwner(), invalidated: false }
    }
    const observeDesktopFocus = (event) => {
      if (event.target !== document.body && event.target !== document.documentElement) navigationFocusRevision += 1
      const closing = mobileCloseFocusTicket
      if (closing && !mobileOwnsFocus(closing, event.target)) closing.invalidated = true
      const ticket = desktopFocusTicket
      if (ticket && event.target !== document.body && event.target !== document.documentElement
        && event.target !== ticket.opener && !ticket.item.contains(event.target)) ticket.invalidated = true
    }
    const settleDesktopNavigation = (event, admitted) => {
      const ticket = desktopFocusTicket
      if (!event || !ticket || ticket.event !== event) return
      if (!admitted || ticket.invalidated) { desktopFocusTicket = null; return }
      const route = router.currentRoute.value.fullPath
      const main = contentRoot.value
      nextTick(() => {
        if (desktopFocusTicket !== ticket) return
        desktopFocusTicket = null
        if (!mounted || isMobile.value || !canAccessRoute.value || ticket.invalidated
          || !ticket.owner.every((value, index) => value === desktopFocusOwner()[index])
          || router.currentRoute.value.fullPath !== route || contentRoot.value !== main || !main?.isConnected
          || visibleFocusDialog()) return
        const active = document.activeElement
        if (active !== ticket.opener && !ticket.item.contains(active)
          && !(active === document.body && (!ticket.opener?.isConnected || ticket.opener === document.body))) return
        focusContent()
      })
    }
    const finishClosing = (ticket = mobileCloseFocusTicket) => {
      if (!ticket || mobileCloseFocusTicket !== ticket || ticket.entry !== mobileDrawerEntry.value
        || drawerVisible.value || !mounted) return
      window.clearTimeout(closeTimer)
      const destination = focusDestination
      focusDestination = null
      mobileOpenFocusOwner = null
      // Closing our Drawer is independent of still owning the right to move focus.
      drawerRendered.value = false
      nextTick(() => {
        if (mobileCloseFocusTicket !== ticket) return
        mobileCloseFocusTicket = null
        if (!mounted || !isMobile.value || drawerVisible.value || ticket.entry !== mobileDrawerEntry.value
          || ticket.invalidated || destination !== ticket.destination
          || !ticket.owner.every((value, index) => value === desktopFocusOwner()[index])
          || router.currentRoute.value.fullPath !== ticket.route
          || visibleFocusDialog(ticket.ownDialogs) || !mobileOwnsFocus(ticket, document.activeElement)) return
        if (destination === 'trigger') {
          if (triggerElement() === ticket.trigger && ticket.trigger?.isConnected) focusTrigger()
        } else if (contentRoot.value === ticket.main && ticket.main?.isConnected && canAccessRoute.value) focusContent()
      })
    }
    const openMobileMenu = () => {
      window.clearTimeout(closeTimer)
      mobileCloseFocusTicket = null
      viewportFocusVersion += 1
      const entry = ++mobileDrawerEntry.value
      mobileOpenFocusOwner = desktopFocusOwner()
      mobileDrawerAfterChange.value = visible => afterDrawerVisibilityChange(visible, entry)
      focusDestination = null
      drawerRendered.value = true
      drawerVisible.value = true
      nextTick(() => { if (entry === mobileDrawerEntry.value && canFocusOpeningDrawer()) focusClose() })
    }
    const closeMobileMenu = (destination = 'trigger') => {
      if (!drawerVisible.value) return
      const scope = mobileDrawerScope()
      const ticket = { ...scope, destination, entry: mobileDrawerEntry.value,
        owner: desktopFocusOwner(), route: router.currentRoute.value.fullPath, invalidated: false }
      ticket.invalidated = !mobileOwnsFocus(ticket, document.activeElement) || visibleFocusDialog(ticket.ownDialogs)
      mobileCloseFocusTicket = ticket
      focusDestination = destination
      drawerVisible.value = false
      // Both the transition callback and fallback must retain this exact open/close session.
      closeTimer = window.setTimeout(() => finishClosing(ticket), 240)
    }
    const afterDrawerVisibilityChange = (visible, entry = mobileDrawerEntry.value) => {
      if (!mounted || entry !== mobileDrawerEntry.value) return
      if (visible && canFocusOpeningDrawer()) focusClose()
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
      desktopFocusTicket = null
      mobileCloseFocusTicket = null
      mobileOpenFocusOwner = null
      const viewportVersion = ++viewportFocusVersion
      const focusRevision = navigationFocusRevision
      const owner = desktopFocusOwner()
      const route = router.currentRoute.value.fullPath
      const scope = mobileDrawerScope()
      const wasOpen = drawerVisible.value
      const triggerFocused = document.activeElement === triggerElement()
      const siderFocused = Boolean(desktopNavigation.value?.contains(document.activeElement))
      const ownedSourceFocus = siderFocused || mobileOwnsFocus(scope, document.activeElement)
      mode = nextMode
      isMobile.value = mode === 'phone'
      siderCollapsed.value = mode === 'tablet'
      drawerVisible.value = false
      drawerRendered.value = false
      focusDestination = null
      window.clearTimeout(closeTimer)
      nextTick(() => {
        if (!mounted || viewportVersion !== viewportFocusVersion || mode !== nextMode
          || focusRevision !== navigationFocusRevision || !ownedSourceFocus
          || !owner.every((value, index) => value === desktopFocusOwner()[index])
          || router.currentRoute.value.fullPath !== route || visibleFocusDialog()) return
        if (wasOpen || triggerFocused) focusContent()
        else if (siderFocused && isMobile.value) focusTrigger()
      })
    }
    let sessionTimer = null, sessionRequest = null, sessionChecksStopped = false
    const stopSessionChecks = () => {
      sessionChecksStopped = true
      if (sessionTimer !== null) window.clearInterval(sessionTimer)
      sessionTimer = null
      window.removeEventListener('focus', checkSession)
      document.removeEventListener('visibilitychange', checkSession)
      sessionRequest?.abort()
    }
    const checkSession = async () => {
      if (!mounted || sessionChecksStopped || sessionRequest || document.visibilityState === 'hidden' || !currentUser.value) return
      const controller = new AbortController(), revision = getCurrentSessionRevision(), owner = desktopFocusOwner()
      sessionRequest = controller
      try {
        const response = await checkUserSession(controller.signal)
        if ((response?.status === 401 || response?.status === 403) && revision === getCurrentSessionRevision()
          && owner.every((value, index) => value === desktopFocusOwner()[index])) stopSessionChecks()
      } catch {
        // Temporary network failures remain subject to the original next heartbeat.
      } finally { if (sessionRequest === controller) sessionRequest = null }
    }
    const observe = (query, add) => {
      if (query.addEventListener) query[add ? 'addEventListener' : 'removeEventListener']('change', syncViewport)
      else query[add ? 'addListener' : 'removeListener'](syncViewport)
    }
    onMounted(() => {
      observe(phoneQuery, true); observe(compactQuery, true)
      document.addEventListener('focusin', observeDesktopFocus, true)
      if (currentUser.value) {
        sessionTimer = window.setInterval(checkSession, 30_000)
        window.addEventListener('focus', checkSession)
        document.addEventListener('visibilitychange', checkSession)
        void checkSession()
      }
    })
    onBeforeUnmount(() => {
      stopSessionChecks()
      mounted = false
      desktopFocusTicket = null
      mobileCloseFocusTicket = null
      mobileOpenFocusOwner = null
      document.removeEventListener('focusin', observeDesktopFocus, true)
      window.clearTimeout(closeTimer)
      observe(phoneQuery, false)
      observe(compactQuery, false)
    })
    watch(() => router.currentRoute.value.fullPath, () => {
      if (drawerVisible.value) closeMobileMenu('content')
    })
    watch(() => Boolean(currentUser.value), (hasIdentity) => {
      if (hasIdentity) return
      stopSessionChecks()
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
      mobileDrawerEntry, mobileDrawerAfterChange,
      openMobileMenu, closeMobileMenu, handleMobileNavigate, handleNavigationKeydown,
      afterDrawerVisibilityChange, focusContent, desktopNavigation, captureDesktopNavigation, settleDesktopNavigation,
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
