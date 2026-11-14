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
          <!--
            ⚠️ menu-item 的 key 必须带分区前缀：DUAL_ENTRY_KEYS 会让同一个
            `item.key` 同时出现在 shared 与 admin-only 两个分区里，只用 key 会
            触发 Vue 的重复 key 警告，且 a-menu 的选中态会同时高亮两行。
            跳转仍用 item.key（见 href 与 handleSelect 的 data-menu-key）。
          -->
          <a-menu-item
            v-for="item in section.items"
            :key="section.key + ':' + item.key"
            :data-menu-key="item.key"
          >
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
  // ⚠️ 用户端也有、且**语义不同**的三个入口，管理端必须同时保留两份（各自独立路由）：
  //   tickets         → 工单反馈（viewMode='self'）    /  ticket-admin → 工单管理（viewMode='admin'）
  //   scripts         → 我的脚本（'self'）             /  script-admin → 脚本管理（'admin'）
  //   billing-records → 我的账单（'self'）             /  billing-admin → 账单明细（'admin'）
  // 它们按 key 自然分到 shared / admin-only 两个分区：
  // 用户端那份进「普通用户可见」组（文案取用户端），管理端那份进「仅管理员可见」组。
  { key: 'tickets', label: '工单反馈', icon: FileTextOutlined },
  { key: 'userlist', label: '用户管理', icon: TeamOutlined },
  // 用户画像（98 §0）。仅管理员：全站行为聚合，普通用户直接敲 URL 会被 router 跳回首页。
  { key: 'user-portrait', label: '用户画像', icon: PieChartOutlined },
  // 风控告警（96 / 118 §2）。只读页，处置仍去黑名单/封禁。
  { key: 'risk-admin', label: '风控告警', icon: StopOutlined },
  // 已归档账号管理（task_doc/76 §3.2.3）：管理员删除与用户注销的账号都归到这里。
  { key: 'archived-users', label: '已归档账号', icon: InboxOutlined },
  { key: 'loglist', label: '日志列表', icon: FileTextOutlined },
  { key: 'recharge-orders', label: '充值记录', icon: CreditCardOutlined },
  { key: 'billing-records', label: '我的账单', icon: AccountBookOutlined },
  { key: 'billing-admin', label: '账单明细', icon: AccountBookOutlined },
  { key: 'ip-location-cache', label: 'IP 归属地信息管理', icon: GlobalOutlined },
  { key: 'ip-segments', label: 'IP 段归属管理', icon: ApartmentOutlined },
  { key: 'phone-location-cache', label: '手机号归属地信息管理', icon: PhoneOutlined },
  // 绑定历史两页（task_doc/70 §3.6）：同一组件的两个入口，只差 route.meta.bindingKind。
  { key: 'wechat-bindings', label: '微信绑定历史', icon: WechatOutlined },
  { key: 'phone-bindings', label: '手机号绑定历史', icon: PhoneOutlined },
  { key: 'search-account-config', label: '默认账号配置', icon: SettingOutlined },
  { key: 'ai-search-config', label: 'AI 搜索配置', icon: RobotOutlined },
  { key: 'ocr-cache', label: 'OCR 缓存管理', icon: FileTextOutlined },
  // 敏感词检测记录（T-27）。紧跟 ocr-cache：两者都是「缓存命中情况」类只读页。
  { key: 'sensitive-word', label: '敏感词检测记录', icon: FileTextOutlined },
  { key: 'user-exam-tool', label: '客户端数据库工具', icon: DatabaseOutlined },
  // 「赠送记录」= 注册赠送 / 评价返现 / 邀请返佣 / 充值赠金 四类聚合（Q75-1）。
  // 路由 key 仍是 referral-rebates：改名只动用户可见文案，不动路由名与权限注册。
  { key: 'referral-rebates', label: '赠送记录', icon: GiftOutlined },
  { key: 'review-rebate-admin', label: '评价返现管理', icon: StarOutlined },
  { key: 'blacklist', label: '黑名单管理', icon: StopOutlined },
  // 白名单（T-04 / T-05）。紧跟黑名单：两者规则结构相同，管理员要能并排看。
  { key: 'whitelist', label: '白名单管理', icon: SafetyCertificateOutlined },
  // 账号封禁（121 §4.8 / 121 决策 6）。与黑/白名单并列：三者都是风控入口，
  // 但封禁按**账号**，前两者按 IP/手机号/微信。
  // ⚠️ 只登记在 ADMIN_MENU_ITEMS，**绝不加进 USER_MENU_ITEMS**：
  // 普通用户能看到「账号封禁」入口本身就是信息泄漏（暴露了封禁机制的存在与入口）。
  { key: 'user-bans', label: '账号封禁', icon: StopOutlined },
  // 敏感图片检测（Q1）与工单图片缓存（T-12）相邻：前者判违规、后者管存储，
  // 是同一份附件的两个视角。
  { key: 'sensitive-image', label: '敏感图片检测', icon: FileTextOutlined },
  { key: 'ticket-image-cache', label: '工单图片缓存', icon: DatabaseOutlined },
  // 等级与充值阶梯（94-*.md §2 C1/C2）：等级门槛 + 补贴档位都在这一页。
  { key: 'user-levels', label: '等级与充值阶梯', icon: TrophyOutlined },
  { key: 'system-settings', label: '系统管理', icon: ToolOutlined },
  { key: 'extension-updates', label: '扩展更新', icon: DownloadOutlined },
  { key: 'access-tokens', label: '令牌管理', icon: SafetyCertificateOutlined },
  // 脚本管理是独立路由 script-admin（见 router 的 viewMode 说明），不是 scripts 的别名。
  { key: 'scripts', label: '我的脚本', icon: CodeOutlined },
  { key: 'script-admin', label: '脚本管理', icon: CodeOutlined },
  // 「工单管理」是**独立路由**（ticket-admin），不是 tickets 的别名。
  //
  // 为什么必须独立路由（2026-11-14 用户实测报出）：
  // 两个入口曾共用 `tickets` 路由，而 Tickets 组件只按角色判视图 ⇒
  // 管理员在「普通用户可见」区点「工单反馈」看到的却是管理视图，
  // 「工单管理」那个入口也无法被单独的 selectedKeys 选中（同一 key）。
  // 现在 `tickets` → meta.viewMode='self'（用户视图），
  // `ticket-admin` → meta.viewMode='admin'（管理视图），两个 name、两个 key。
  { key: 'ticket-admin', label: '工单管理', icon: FileTextOutlined },
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

// 管理员侧边栏两个分区（彻底分开，无重叠）：
//   shared    = ADMIN ∩ USER —— 文案取**用户端**的（告诉管理员「普通用户看到的叫什么」）
//   adminOnly = ADMIN ∖ USER —— 文案取**管理端**的
//
// ⚠️ 2026-11-14 用户实测报出的缺陷与最终修法（务必看完再改）：
//
// 用户原话：「我希望的侧边栏是普通用户可见和仅管理员可见彻底分开。现在的工单反馈，
// 但对管理员来说就是工单·管理，我希望的是正常显示这两个页面在管理员那，
// 而不是只显示一。」
//
// 第一版修法是「让同一个 key 同时进两个分区」（DUAL_ENTRY_KEYS），**被用户否掉了**：
// 「管理员在普通用户可见点击工单反馈，看到的却是工单管理的页面；
//   仅管理员可见的工单管理却不可选中。」
//
// 根因：两个入口共用同一个 route.name，而页面组件只按**角色**判视图模式
// （`isAdmin = isAdminUser(currentUser)`）⇒ 管理员点哪个入口都是管理视图；
// 同一个 key 出现在两处也让 selectedKeys 无法区分，管理端那一行永远选不中。
//
// 正确修法是**拆路由**，不是拆文案：
//   tickets（viewMode='self'）  ↔  ticket-admin（viewMode='admin'）
//   scripts（viewMode='self'）  ↔  script-admin（viewMode='admin'）
//   billing-records（'self'）   ↔  billing-admin（'admin'）
// 页面组件读 route.meta.viewMode 决定渲染哪个视图，与角色解耦。
//
// ⇒ 于是两个分区**重新回到严格互斥**：同一个 key 不会再出现两次，
// 用户端的 key 在 shared 区（用户文案），管理端的 key 在 adminOnly 区（管理文案）。
const USER_MENU_BY_KEY = new Map(USER_MENU_ITEMS.map((item) => [item.key, item]))
/** sharedSectionItem 取管理端顺序 + 用户端文案，两者都不丢。 */
const sharedSectionItem = (adminItem) => {
  const userItem = USER_MENU_BY_KEY.get(adminItem.key)
  // 理论上 shared 组里每一项都必然在 USER_MENU_ITEMS 里（filter 保证），
  // 这里仍做兜底而不是断言：渲染期抛错会让整个侧边栏白屏。
  return userItem ? { ...adminItem, label: userItem.label } : adminItem
}
const ADMIN_MENU_SECTIONS = [
  {
    key: 'shared',
    title: ADMIN_SECTION_TITLES.shared,
    items: ADMIN_MENU_ITEMS.filter((item) => USER_MENU_KEYS.has(item.key)).map(sharedSectionItem),
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

    // 分区里 menu-item 的 key 带分区前缀（见模板注释），选中态要还原成真实 key。
    const REAL_KEY_SEPARATOR = ':';
    const keysOfSection = (group) => (group.items || []).map((item) => item.key);
    const selectedKeyFor = (realKey) => {
      const section = (menuSections.value || []).find((group) => keysOfSection(group).includes(realKey));
      return section ? section.key + REAL_KEY_SEPARATOR + realKey : realKey;
    };

    const syncSelectedKeys = () => {
      const pathSegments = String(route.path || '').split('/');
      const currentKey = String(route.name || '') === 'tickets' ? 'tickets' : pathSegments[pathSegments.length - 1];
      const matched = visibleMenuItems.value.some((item) => item.key === currentKey)
        ? currentKey
        : visibleMenuItems.value[0]?.key;
      selectedKeys.value = matched ? [selectedKeyFor(matched)] : [];
    };

    watch(
      () => [route.path, visibleMenuItems.value.map((item) => item.key).join(',')],
      syncSelectedKeys,
      { immediate: true }
    );

    const handleSelect = async ({ key, domEvent }) => {
      // a-menu 回传的是带分区前缀的 key，取回真实 key 再做权限判定与跳转。
      const realKey = String(key || '').includes(REAL_KEY_SEPARATOR)
        ? String(key).slice(String(key).indexOf(REAL_KEY_SEPARATOR) + 1)
        : String(key || '');
      if (!navigationActive || !visibleMenuItems.value.some(item => item.key === realKey)) return;
      const intent = ++navigationIntent;
      const isCurrent = () => navigationActive && intent === navigationIntent;
      selectedKeys.value = [String(key)];
      let failure;
      let fulfilled = false;
      try {
        failure = await router.push('/adapter/' + realKey);
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
