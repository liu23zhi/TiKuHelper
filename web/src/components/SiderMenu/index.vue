<template>
  <div class="sider-menu-wrap">
    <div class="menu-title">{{ isAdmin ? '管理导航' : '用户导航' }}</div>
    <div class="menu-subtitle">
      {{ isAdmin ? '围绕搜题、AI 配置、账单与账户管理的后台入口' : '查看个人使用记录、充值入口、访问令牌与账单明细' }}
    </div>
    <a-menu
      mode="inline"
      theme="light"
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
  </div>
</template>
<script>
import { defineComponent, computed, ref, watch } from 'vue';
import { useRouter, useRoute } from 'vue-router';
import {
  PieChartOutlined,
  DownloadOutlined,
  TeamOutlined,
  FileTextOutlined,
  SettingOutlined,
  RobotOutlined,
  ToolOutlined,
  SafetyCertificateOutlined,
  AccountBookOutlined,
  CreditCardOutlined,
} from '@ant-design/icons-vue';
import { isAdminUser } from '../../utils/currentUser';

const ADMIN_MENU_ITEMS = [
  { key: 'component', label: '题库总览', icon: PieChartOutlined },
  { key: 'import', label: '智能导入', icon: DownloadOutlined },
  { key: 'user-usage', label: '个人使用', icon: CreditCardOutlined },
  { key: 'userlist', label: '用户管理', icon: TeamOutlined },
  { key: 'loglist', label: '日志列表', icon: FileTextOutlined },
  { key: 'billing-records', label: '账单明细', icon: AccountBookOutlined },
  { key: 'search-account-config', label: '默认账号配置', icon: SettingOutlined },
  { key: 'ai-search-config', label: 'AI 搜索配置', icon: RobotOutlined },
  { key: 'system-settings', label: '系统管理', icon: ToolOutlined },
  { key: 'access-tokens', label: '令牌管理', icon: SafetyCertificateOutlined },
]

const USER_MENU_ITEMS = [
  { key: 'user-usage', label: '个人使用', icon: CreditCardOutlined },
  { key: 'access-tokens', label: '令牌管理', icon: SafetyCertificateOutlined },
  { key: 'billing-records', label: '我的账单', icon: AccountBookOutlined },
]

export default defineComponent({
  name: 'SiderMenu',
  props: {
    currentUser: {
      type: Object,
      default: null,
    },
  },
  setup(props) {
    const router = useRouter();
    const route = useRoute();
    const selectedKeys = ref([]);
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

    const handleSelect = ({ key }) => {
      selectedKeys.value = [key];
      router.push(`/adapter/${key}`);
    };

    return {
      selectedKeys,
      visibleMenuItems,
      isAdmin,
      handleSelect,
    };
  },
});
</script>

<style scoped>
.sider-menu-wrap {
  padding: 18px 14px 14px;
}

.menu-title {
  font-size: 16px;
  font-weight: 700;
  color: #1f2937;
}

.menu-subtitle {
  margin-top: 6px;
  margin-bottom: 14px;
  font-size: 12px;
  line-height: 1.6;
  color: #6b7280;
}

.menu {
  border-inline-end: none !important;
  background: transparent;
}
</style>
