<template>
  <nav class="sider-menu-wrap" :class="{ 'is-collapsed': collapsed, 'is-mobile': mobile }" :aria-label="isAdmin ? '管理导航' : '用户导航'">
    <div class="menu-heading">
      <div v-if="!collapsed" class="menu-title">{{ isAdmin ? '管理导航' : '用户导航' }}</div>
      <div v-if="!collapsed" class="menu-subtitle">
        {{ isAdmin ? '管理题库、AI 搜索、用户和账单' : '管理个人记录、充值、令牌和账单' }}
      </div>
    </div>
    <a-menu
      :id="navigationId"
      mode="inline"
      :theme="resolvedTheme"
      :inline-collapsed="collapsed"
      v-model:selectedKeys="selectedKeys"
      @click="handleSelect"
      class="menu"
    >
      <a-menu-item v-for="item in visibleMenuItems" :key="item.key">
        <template #icon>
          <component :is="item.icon" />
        </template>
        <span class="menu-label"><a class="navigation-link" :href="'/adapter/' + item.key" @click="handleLinkClick">{{ item.label }}</a></span>
      </a-menu-item>
    </a-menu>
    <a-button
      v-if="showCollapse"
      type="text"
      class="collapse-button"
      :class="{ 'is-collapsed': collapsed }"
      :aria-expanded="!collapsed"
      :aria-controls="navigationId"
      :aria-label="collapsed ? '展开侧边栏' : '折叠侧边栏'"
      :title="collapsed ? '展开侧边栏' : '折叠侧边栏'"
      @click="$emit('toggle')"
    >
      <component :is="collapsed ? MenuUnfoldOutlined : MenuFoldOutlined" class="collapse-button-icon" />
      <span v-if="!collapsed" class="collapse-button-label">折叠侧边栏</span>
    </a-button>
  </nav>
</template>
<script>
import { defineComponent, computed, inject, onBeforeUnmount, ref, watch } from 'vue';
import { useRouter, useRoute } from 'vue-router';
import {
  UserOutlined,
  PieChartOutlined,
  DownloadOutlined,
  TeamOutlined,
  GlobalOutlined,
  PhoneOutlined,
  FileTextOutlined,
  SettingOutlined,
  RobotOutlined,
  ToolOutlined,
  SafetyCertificateOutlined,
  AccountBookOutlined,
  CreditCardOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
} from '@ant-design/icons-vue';
import { isAdminUser } from '../../utils/currentUser';
import { THEME_CONTROLLER_KEY } from '../../utils/themeMode.mjs';

const ADMIN_MENU_ITEMS = [
  { key: 'profile', label: '个人中心', icon: UserOutlined },
  { key: 'component', label: '题库总览', icon: PieChartOutlined },
  { key: 'import', label: '智能导入', icon: DownloadOutlined },
  { key: 'user-usage', label: '个人使用', icon: CreditCardOutlined },
  { key: 'userlist', label: '用户管理', icon: TeamOutlined },
  { key: 'loglist', label: '日志列表', icon: FileTextOutlined },
  { key: 'recharge-orders', label: '充值记录', icon: CreditCardOutlined },
  { key: 'billing-records', label: '账单明细', icon: AccountBookOutlined },
  { key: 'ip-location-cache', label: 'IP 归属地信息管理', icon: GlobalOutlined },
  { key: 'phone-location-cache', label: '手机号归属地信息管理', icon: PhoneOutlined },
  { key: 'search-account-config', label: '默认账号配置', icon: SettingOutlined },
  { key: 'ai-search-config', label: 'AI 搜索配置', icon: RobotOutlined },
  { key: 'system-settings', label: '系统管理', icon: ToolOutlined },
  { key: 'access-tokens', label: '令牌管理', icon: SafetyCertificateOutlined },
]

const USER_MENU_ITEMS = [
  { key: 'profile', label: '个人中心', icon: UserOutlined },
  { key: 'user-usage', label: '个人使用', icon: CreditCardOutlined },
  { key: 'access-tokens', label: '令牌管理', icon: SafetyCertificateOutlined },
  { key: 'recharge-orders', label: '充值记录', icon: CreditCardOutlined },
  { key: 'billing-records', label: '我的账单', icon: AccountBookOutlined },
]

export default defineComponent({
  name: 'SiderMenu',
  props: {
    currentUser: {
      type: Object,
      default: null,
    },
    collapsed: {
      type: Boolean,
      default: false,
    },
    mobile: { type: Boolean, default: false },
    showCollapse: { type: Boolean, default: true },
    navigationId: { type: String, default: 'portal-navigation' },
  },
  emits: ['toggle', 'navigate'],
  setup(props, { emit }) {
    const router = useRouter();
    const route = useRoute();
    const selectedKeys = ref([]);
    const themeController = inject(THEME_CONTROLLER_KEY, null);
    const resolvedTheme = ref(themeController?.getState().resolved || 'light');
    const unsubscribeTheme = themeController?.subscribe(state => { resolvedTheme.value = state.resolved; });
    onBeforeUnmount(() => unsubscribeTheme?.());
    const isAdmin = computed(() => isAdminUser(props.currentUser));
    const visibleMenuItems = computed(() => (isAdmin.value ? ADMIN_MENU_ITEMS : USER_MENU_ITEMS));

    const syncSelectedKeys = () => {
      const pathSegments = String(route.path || '').split('/');
      const currentKey = pathSegments[pathSegments.length - 1];
      const matched = visibleMenuItems.value.some((item) => item.key === currentKey)
        ? currentKey
        : visibleMenuItems.value[0]?.key;
      selectedKeys.value = matched ? [matched] : [];
    };

    watch(
      () => [route.path, visibleMenuItems.value.map((item) => item.key).join(',')],
      syncSelectedKeys,
      { immediate: true }
    );

    const handleSelect = async ({ key }) => {
      if (!visibleMenuItems.value.some(item => item.key === key)) return;
      selectedKeys.value = [key];
      await router.push('/adapter/' + key);
      emit('navigate');
    };
    const handleLinkClick = (event) => {
      if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || event.button !== 0) event.stopPropagation();
      else event.preventDefault();
    };

    return {
      selectedKeys,
      resolvedTheme,
      visibleMenuItems,
      isAdmin,
      handleSelect,
      handleLinkClick,
      MenuFoldOutlined,
      MenuUnfoldOutlined,
    };
  },
});
</script>

<style scoped>
.sider-menu-wrap {
  display: flex;
  flex-direction: column;
  min-height: calc(100vh - var(--layout-header-height));
  min-height: calc(100dvh - 64px);
  box-sizing: border-box;
  padding: var(--sider-menu-padding);
  background: var(--surface, var(--surface));
  color: var(--text, var(--text));
}

.sider-menu-wrap.is-collapsed {
  padding-left: var(--g-zero);
  padding-right: var(--g-zero);
}

.menu-heading {
  min-width: 0;
}

.menu-title {
  font-size: 16px;
  font-weight: 700;
  color: var(--text, var(--text));
}

.menu-subtitle {
  margin-top: var(--sider-menu-subtitle-margin-top);
  margin-bottom: var(--sider-menu-subtitle-margin-bottom);
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-muted, #5F6368);
}

.collapse-button {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--control-icon-label-gap);
  width: 100%;
  min-height: var(--control-touch-min);
  height: auto;
  margin-top: var(--g-auto);
  padding: var(--sider-collapse-padding);
  color: var(--text-muted, #5F6368);
  border-radius: var(--control-radius);
  line-height: 1.4;
  white-space: normal;
}

/* 折叠后侧边栏很窄，只留居中图标，避免文字挤压换行。 */
.collapse-button.is-collapsed {
  padding: var(--sider-collapse-padding-collapsed);
}

.collapse-button-icon {
  font-size: 16px;
}

.collapse-button-label {
  min-width: 0;
}

.collapse-button:hover,
.collapse-button:focus {
  color: var(--link, #0066CC);
  background: var(--surface-subtle, #F5F5F7);
}

/* 图标跟随按钮主色，与菜单项保持一致。 */
.collapse-button :deep(.anticon) {
  color: inherit;
}

.menu {
  border-inline-end: none !important;
  background: transparent;
}

.sider-menu-wrap.is-mobile { min-height: 0; }
.navigation-link { color: inherit; text-decoration: none; }
.menu :deep(.ant-menu-item:focus-within) { outline: var(--focus-outline-width) solid var(--focus, #0066CC); outline-offset: var(--focus-menu-outline-offset); }
@media (prefers-reduced-motion: reduce) {
  .sider-menu-wrap :deep(*) { transition-duration: 1ms !important; }
}
</style>
