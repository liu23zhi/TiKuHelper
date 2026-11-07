<template>
  <div class="sider-menu-wrap" :class="{ 'is-collapsed': collapsed }">
    <div class="menu-heading">
      <div v-if="!collapsed" class="menu-title">{{ isAdmin ? '管理导航' : '用户导航' }}</div>
      <div v-if="!collapsed" class="menu-subtitle">
        {{ isAdmin ? '管理题库、AI 搜索、用户和账单' : '管理个人记录、充值、令牌和账单' }}
      </div>
    </div>
    <a-menu
      id="portal-navigation"
      mode="inline"
      theme="light"
      :inline-collapsed="collapsed"
      v-model:selectedKeys="selectedKeys"
      @select="handleSelect"
      class="menu"
    >
      <a-menu-item v-for="item in visibleMenuItems" :key="item.key">
        <template #icon>
          <component :is="item.icon" />
        </template>
        <span>{{ item.label }}</span>
      </a-menu-item>
    </a-menu>
    <a-button
      type="text"
      class="collapse-button"
      :class="{ 'is-collapsed': collapsed }"
      :aria-expanded="!collapsed"
      aria-controls="portal-navigation"
      :aria-label="collapsed ? '展开侧边栏' : '折叠侧边栏'"
      :title="collapsed ? '展开侧边栏' : '折叠侧边栏'"
      @click="$emit('toggle')"
    >
      <component :is="collapsed ? MenuUnfoldOutlined : MenuFoldOutlined" class="collapse-button-icon" />
      <span class="collapse-button-label">{{ collapsed ? '展开' : '折叠侧边栏' }}</span>
    </a-button>
  </div>
</template>
<script>
import { defineComponent, computed, ref, watch } from 'vue';
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
  CodeOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
} from '@ant-design/icons-vue';
import { isAdminUser } from '../../utils/currentUser';

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
  { key: 'ocr-cache', label: 'OCR 缓存管理', icon: FileTextOutlined },
  { key: 'system-settings', label: '系统管理', icon: ToolOutlined },
  { key: 'extension-updates', label: '扩展更新', icon: DownloadOutlined },
  { key: 'access-tokens', label: '令牌管理', icon: SafetyCertificateOutlined },
  { key: 'scripts', label: '脚本管理', icon: CodeOutlined },
  { key: 'tickets', label: '工单管理', icon: FileTextOutlined },
]

const USER_MENU_ITEMS = [
  { key: 'profile', label: '个人中心', icon: UserOutlined },
  { key: 'user-usage', label: '个人使用', icon: CreditCardOutlined },
  { key: 'access-tokens', label: '令牌管理', icon: SafetyCertificateOutlined },
  { key: 'scripts', label: '我的脚本', icon: CodeOutlined },
  { key: 'recharge-orders', label: '充值记录', icon: CreditCardOutlined },
  { key: 'billing-records', label: '我的账单', icon: AccountBookOutlined },
  { key: 'tickets', label: '工单反馈', icon: FileTextOutlined },
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
  },
  emits: ['toggle'],
  setup(props) {
    const router = useRouter();
    const route = useRoute();
    const selectedKeys = ref([]);
    const isAdmin = computed(() => isAdminUser(props.currentUser));
    const visibleMenuItems = computed(() => (isAdmin.value ? ADMIN_MENU_ITEMS : USER_MENU_ITEMS));

    const syncSelectedKeys = () => {
      const pathSegments = String(route.path || '').split('/');
      const currentKey = String(route.name || '') === 'tickets' ? 'tickets' : pathSegments[pathSegments.length - 1];
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

    const handleSelect = ({ key }) => {
      selectedKeys.value = [key];
      router.push(`/adapter/${key}`);
    };

    return {
      selectedKeys,
      visibleMenuItems,
      isAdmin,
      handleSelect,
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
  height: 100%;
  min-height: 0;
  box-sizing: border-box;
  padding: 18px 14px 14px;
}

.sider-menu-wrap.is-collapsed {
  padding: 12px 0;
}

.menu-heading {
  min-width: 0;
}

.menu-title {
  font-size: 16px;
  font-weight: 700;
  color: var(--console-text);
}

.menu-subtitle {
  margin-top: 6px;
  margin-bottom: 14px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--console-muted);
}

.collapse-button {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  width: 100%;
  min-height: 36px;
  height: auto;
  margin-top: auto;
  padding: 8px 4px;
  color: var(--console-muted);
  border-radius: 6px;
  flex-shrink: 0;
  line-height: 1.4;
  white-space: normal;
}

.collapse-button.is-collapsed {
  padding: 8px 0;
  font-size: 12px;
}

.collapse-button-icon {
  font-size: 16px;
}

.collapse-button-label {
  min-width: 0;
}

.collapse-button.is-collapsed .collapse-button-label {
  display: inline;
}
.collapse-button.is-collapsed .collapse-button-icon { display: none; }

.collapse-button:hover,
.collapse-button:focus {
  color: var(--console-primary);
  background: var(--console-selected);
}

/* 图标跟随按钮主色，与菜单项保持一致。 */
.collapse-button :deep(.anticon) {
  color: inherit;
}

.menu {
  border-inline-end: none !important;
  background: transparent;
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}
.menu :deep(.ant-menu-item) { min-height: 44px; height: auto; line-height: 1.5; padding-block: 12px; border-radius: 6px; }
.menu :deep(.ant-menu-title-content) { white-space: normal; }
.menu.ant-menu-inline-collapsed { width: 64px; }
.menu.ant-menu-inline-collapsed :deep(.ant-menu-item) { padding-inline: 24px; }
.menu.ant-menu-inline-collapsed :deep(.ant-menu-title-content) { display: none; }
</style>
