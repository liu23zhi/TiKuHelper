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
      <template v-for="section in menuSections" :key="section.key">
        <!-- 管理员看到两个带标题的分区；普通用户只有一个不带标题的扁平列表。 -->
        <a-menu-item-group v-if="section.title" :data-menu-section="section.key">
          <template #title>
            <span class="menu-section-title">{{ section.title }}</span>
          </template>
          <a-menu-item v-for="item in section.items" :key="item.key">
            <template #icon>
              <component :is="item.icon" />
            </template>
            <span class="menu-label"><a class="navigation-link" :href="'/adapter/' + item.key" @click="handleLinkClick">{{ item.label }}</a></span>
          </a-menu-item>
        </a-menu-item-group>
        <template v-else>
          <a-menu-item v-for="item in section.items" :key="item.key">
            <template #icon>
              <component :is="item.icon" />
            </template>
            <span class="menu-label"><a class="navigation-link" :href="'/adapter/' + item.key" @click="handleLinkClick">{{ item.label }}</a></span>
          </a-menu-item>
        </template>
      </template>
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
import { useRouter, useRoute, isNavigationFailure, NavigationFailureType } from 'vue-router';
import { message } from 'ant-design-vue';
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
  DatabaseOutlined,
  InboxOutlined,
  GiftOutlined,
  ShareAltOutlined,
  StarOutlined,
  StopOutlined,
  ApartmentOutlined,
  TrophyOutlined,
  WechatOutlined,
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
  // 已归档账号管理（task_doc/76 §3.2.3）：管理员删除与用户注销的账号都归到这里。
  { key: 'archived-users', label: '已归档账号', icon: InboxOutlined },
  { key: 'loglist', label: '日志列表', icon: FileTextOutlined },
  { key: 'recharge-orders', label: '充值记录', icon: CreditCardOutlined },
  { key: 'billing-records', label: '账单明细', icon: AccountBookOutlined },
  { key: 'ip-location-cache', label: 'IP 归属地信息管理', icon: GlobalOutlined },
  { key: 'ip-segments', label: 'IP 段归属管理', icon: ApartmentOutlined },
  { key: 'phone-location-cache', label: '手机号归属地信息管理', icon: PhoneOutlined },
  // 绑定历史两页（task_doc/70 §3.6）：同一组件的两个入口，只差 route.meta.bindingKind。
  { key: 'wechat-bindings', label: '微信绑定历史', icon: WechatOutlined },
  { key: 'phone-bindings', label: '手机号绑定历史', icon: PhoneOutlined },
  { key: 'search-account-config', label: '默认账号配置', icon: SettingOutlined },
  { key: 'ai-search-config', label: 'AI 搜索配置', icon: RobotOutlined },
  { key: 'ocr-cache', label: 'OCR 缓存管理', icon: FileTextOutlined },
  { key: 'user-exam-tool', label: '客户端数据库工具', icon: DatabaseOutlined },
  // 「赠送记录」= 注册赠送 / 评价返现 / 邀请返佣 / 充值赠金 四类聚合（Q75-1）。
  // 路由 key 仍是 referral-rebates：改名只动用户可见文案，不动路由名与权限注册。
  { key: 'referral-rebates', label: '赠送记录', icon: GiftOutlined },
  { key: 'review-rebate-admin', label: '评价返现管理', icon: StarOutlined },
  { key: 'blacklist', label: '黑名单管理', icon: StopOutlined },
  // 等级与充值阶梯（94-*.md §2 C1/C2）：等级门槛 + 补贴档位都在这一页。
  { key: 'user-levels', label: '等级与充值阶梯', icon: TrophyOutlined },
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
  { key: 'my-referral', label: '我的邀请', icon: ShareAltOutlined },
  { key: 'review-rebate', label: '评价返现', icon: StarOutlined },
  { key: 'tickets', label: '工单反馈', icon: FileTextOutlined },
]

// 普通用户可见组从 USER_MENU_ITEMS 派生：手抄一份清单会在新增页面时漂移，
// 所以只用 USER_MENU_ITEMS 的 key 集合做过滤，key 是唯一判据。
// >>> sidebar-section-derivation (sidebarSectionSource.test.mjs 依赖此段原文执行，改动请同步该测试)
const USER_MENU_KEYS = new Set(USER_MENU_ITEMS.map((item) => item.key))

// 分区标题（task_doc/123：普通用户可见区 / 仅管理员可见区）。
const ADMIN_SECTION_TITLES = {
  shared: '普通用户可见',
  adminOnly: '仅管理员可见',
}

// 管理员侧边栏两个分区：shared 由 USER_MENU_ITEMS 派生，adminOnly 是管理员独有的剩余项。
// 两组合计必须与 ADMIN_MENU_ITEMS 完全一致（不增不减、不重复），由 sidebarSectionSource.test.mjs 钉住。
const ADMIN_MENU_SECTIONS = [
  {
    key: 'shared',
    title: ADMIN_SECTION_TITLES.shared,
    items: ADMIN_MENU_ITEMS.filter((item) => USER_MENU_KEYS.has(item.key)),
  },
  {
    key: 'admin-only',
    title: ADMIN_SECTION_TITLES.adminOnly,
    items: ADMIN_MENU_ITEMS.filter((item) => !USER_MENU_KEYS.has(item.key)),
  },
]

// 普通用户仍然只有一个无标题的扁平列表：不出现任何分区标题。
const USER_MENU_SECTIONS = [{ key: 'user', title: '', items: USER_MENU_ITEMS }]
// <<< sidebar-section-derivation

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
  emits: ['toggle', 'navigate', 'navigation-settled'],
  setup(props, { emit }) {
    const router = useRouter();
    const route = useRoute();
    const selectedKeys = ref([]);
    const themeController = inject(THEME_CONTROLLER_KEY, null);
    const resolvedTheme = ref(themeController?.getState().resolved || 'light');
    const unsubscribeTheme = themeController?.subscribe(state => { resolvedTheme.value = state.resolved; });
    let navigationActive = true;
    let navigationIntent = 0;
    onBeforeUnmount(() => {
      navigationActive = false;
      navigationIntent += 1;
      unsubscribeTheme?.();
    });
    const isAdmin = computed(() => isAdminUser(props.currentUser));
    const visibleMenuItems = computed(() => (isAdmin.value ? ADMIN_MENU_ITEMS : USER_MENU_ITEMS));
    // 分区只影响渲染分组，不影响权限判定与选中/跳转逻辑：两者都仍以 visibleMenuItems 为准。
    const menuSections = computed(() => (isAdmin.value ? ADMIN_MENU_SECTIONS : USER_MENU_SECTIONS));

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

    const handleSelect = async ({ key, domEvent }) => {
      if (!navigationActive || !visibleMenuItems.value.some(item => item.key === key)) return;
      const intent = ++navigationIntent;
      const isCurrent = () => navigationActive && intent === navigationIntent;
      selectedKeys.value = [key];
      let failure;
      let fulfilled = false;
      try {
        failure = await router.push('/adapter/' + key);
        fulfilled = true;
      } catch (error) {
        if (isCurrent()) message.error('页面暂时无法打开，请稍后重试。');
        throw error;
      } finally {
        if (isCurrent()) syncSelectedKeys();
        emit('navigation-settled', domEvent, fulfilled && isCurrent()
          && (!failure || isNavigationFailure(failure, NavigationFailureType.duplicated)));
      }
      // A refused/superseded leave is not a completed menu navigation. Clicking
      // the current page is different: preserve its existing Drawer-close action.
      if (isCurrent() && (!failure || isNavigationFailure(failure, NavigationFailureType.duplicated))) {
        emit('navigate');
      }
    };
    const handleLinkClick = (event) => {
      if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || event.button !== 0) event.stopPropagation();
      else event.preventDefault();
    };

    return {
      selectedKeys,
      resolvedTheme,
      visibleMenuItems,
      menuSections,
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
  min-height: calc(100dvh - var(--layout-header-height));
  box-sizing: border-box;
  padding: var(--sider-menu-padding);
  background: var(--surface, var(--surface));
  color: var(--text, var(--text));
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
  margin-top: var(--sider-menu-subtitle-margin-top);
  margin-bottom: var(--sider-menu-subtitle-margin-bottom);
  font-size: 12px;
  line-height: 1.6;
  color: var(--console-muted);
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
/* 分区标题：两组之间用标题 + 一条细分隔线区分，不改变菜单项本身的排版。 */
.menu :deep(.ant-menu-item-group-title) {
  padding: 10px 12px 4px;
  font-size: 12px;
  font-weight: 600;
  line-height: 1.5;
  color: var(--console-muted);
  white-space: normal;
}
.menu :deep(.ant-menu-item-group:not(:first-child) .ant-menu-item-group-title) {
  margin-top: 4px;
  border-top: 1px solid var(--console-border, rgba(0, 0, 0, 0.06));
  padding-top: 10px;
}
.menu.ant-menu-inline-collapsed { width: 64px; }
.menu.ant-menu-inline-collapsed :deep(.ant-menu-item) { padding-inline: 24px; }
.menu.ant-menu-inline-collapsed :deep(.ant-menu-title-content) { display: none; }
/* 折叠态只留图标，分区标题没有可读空间，直接隐藏（否则会撑坏 64px 宽度）。 */
.menu.ant-menu-inline-collapsed :deep(.ant-menu-item-group-title) { display: none; }
.menu.ant-menu-inline-collapsed :deep(.ant-menu-item-group:not(:first-child)) { border-top: 1px solid var(--console-border, rgba(0, 0, 0, 0.06)); margin-top: 8px; padding-top: 8px; }

.sider-menu-wrap.is-mobile { min-height: 0; }
.navigation-link { color: inherit; text-decoration: none; }
.menu :deep(.ant-menu-item:focus-within) { outline: var(--focus-outline-width) solid var(--focus); outline-offset: var(--focus-menu-outline-offset); }
@media (prefers-reduced-motion: reduce) { .sider-menu-wrap :deep(*) { transition-duration: 1ms !important; } }
</style>
