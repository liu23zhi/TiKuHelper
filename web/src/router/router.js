import { createRouter, createWebHistory } from 'vue-router';
import { h } from 'vue'
import { message } from 'ant-design-vue'
import { getStoredCurrentUser, isAdminUser, resolveHomePath } from '../utils/currentUser'
import { getPublicRegisterStatus } from '../api/TikuComponentApi'

import AdapterLoginLayout from '../layout/LoginLayout.vue';
import AdapterLoginPage from '../pages/Login/index.vue'
const AdapterRegisterPage = () => import(/* webpackChunkName: "route-adapter-register-page" */ '../pages/Register/index.vue')
const WechatBind = () => import(/* webpackChunkName: "route-wechat-bind" */ '../pages/WechatBind/index.vue')
const Profile = () => import(/* webpackChunkName: "route-profile" */ '../pages/Profile/index.vue')

const TikuAdapterLayout = () => import(/* webpackChunkName: "route-tiku-adapter-layout" */ '../layout/TikuAdapterLayout.vue')
const TikuComponent = () => import(/* webpackChunkName: "route-tiku-component" */ '../pages/TikuComponent/index.vue')
const IntelligentImport = () => import(/* webpackChunkName: "route-intelligent-import" */ '../pages/IntelligentImport/index.vue')
const UserList = () => import(/* webpackChunkName: "route-user-list" */ '../pages/UserList/index.vue')
// 已归档账号管理（task_doc/76 §3.2.3）。管理员专属：纳入 adminOnlyRouteNames。
const ArchivedUsers = () => import(/* webpackChunkName: "route-archived-users" */ '../pages/ArchivedUsers/index.vue')
const UserUsagePage = () => import(/* webpackChunkName: "route-user-usage-page" */ '../pages/UserUsage/index.vue')
const LogList = () => import(/* webpackChunkName: "route-log-list" */ '../pages/LogList/index.vue')
const SearchAccountConfig = () => import(/* webpackChunkName: "route-search-account-config" */ '../pages/SearchAccountConfig/index.vue')
const AISearchConfig = () => import(/* webpackChunkName: "route-a-i-search-config" */ '../pages/AISearchConfig/index.vue')
const SystemSettings = () => import(/* webpackChunkName: "route-system-settings" */ '../pages/SystemSettings/index.vue')
const AccessTokenManagement = () => import(/* webpackChunkName: "route-access-token-management" */ '../pages/AccessTokenManagement/index.vue')
const ScriptManagement = () => import(/* webpackChunkName: "route-script-management" */ '../pages/ScriptManagement/index.vue')
const BillingRecordList = () => import(/* webpackChunkName: "route-billing-record-list" */ '../pages/BillingRecordList/index.vue')
const RechargeOrderList = () => import(/* webpackChunkName: "route-recharge-order-list" */ '../pages/RechargeOrderList/index.vue')
const RechargeResult = () => import(/* webpackChunkName: "route-recharge-result" */ '../pages/RechargeResult/index.vue')
const LocationCache = () => import(/* webpackChunkName: "route-location-cache" */ '../pages/LocationCache/index.vue')
const ExtensionUpdates = () => import(/* webpackChunkName: "route-extension-updates" */ '../pages/ExtensionUpdates/index.vue')
const Tickets = () => import(/* webpackChunkName: "route-tickets" */ '../pages/Tickets/index.vue')
const OCRCache = () => import(/* webpackChunkName: "route-o-c-r-cache" */ '../pages/OCRCache/index.vue')
const UserExamTool = () => import(/* webpackChunkName: "route-user-exam-tool" */ '../pages/UserExamTool/index.vue')
const MyReferral = () => import(/* webpackChunkName: "route-my-referral" */ '../pages/MyReferral/index.vue')
const ReferralRebates = () => import(/* webpackChunkName: "route-referral-rebates" */ '../pages/ReferralRebates/index.vue')
const ReviewRebate = () => import(/* webpackChunkName: "route-review-rebate" */ '../pages/ReviewRebate/index.vue')
const ReviewRebateAdmin = () => import(/* webpackChunkName: "route-review-rebate-admin" */ '../pages/ReviewRebateAdmin/index.vue')
const Blacklist = () => import(/* webpackChunkName: "route-blacklist" */ '../pages/Blacklist/index.vue')
const IPSegment = () => import(/* webpackChunkName: "route-ip-segment" */ '../pages/IpSegment/index.vue')
const UserLevel = () => import(/* webpackChunkName: "route-user-level" */ '../pages/UserLevel/index.vue')
const routes = [
  {
    path: '/',
    name: 'loginlayout',
    component: AdapterLoginLayout,
    redirect: 'login',
    children: [
      {
        path: 'login',
        name: 'adapterlogin',
        component: AdapterLoginPage
      },
      {
        path: 'register',
        name: 'adapterregister',
        component: AdapterRegisterPage
      },
      {
        path: 'wechat-bind',
        name: 'wechat-bind',
        component: WechatBind
      }
    ]
  },
  {
    path: '/adapter/recharge/result',
    name: 'recharge-result',
    component: RechargeResult,
  },
  {
    path: '/adapter',
    name: 'adapterlayout',
    component: TikuAdapterLayout,
    children: [
      {
        path: 'profile',
        name: 'profile',
        component: Profile,
      },
      {
        path: 'component',
        name: 'component',
        component: TikuComponent,
      },
      {
        path: 'import',
        name: 'import',
        component: IntelligentImport,
      },
      {
        path: 'userlist',
        name: 'userlist',
        component: UserList,
      },
      {
        // 已归档账号管理（task_doc/76 §3.2.3）:管理员删除与用户注销的账号都在这里。
        path: 'archived-users',
        name: 'archived-users',
        component: ArchivedUsers,
      },
      {
        path: 'user-usage',
        name: 'user-usage',
        component: UserUsagePage,
      },
      {
        path: 'loglist',
        name: 'loglist',
        component: LogList,
      },
      {
        path: 'search-account-config',
        name: 'search-account-config',
        component: SearchAccountConfig,
      },
      {
        path: 'ai-search-config',
        name: 'ai-search-config',
        component: AISearchConfig,
      },
      {
        path: 'ocr-cache',
        name: 'ocr-cache',
        component: OCRCache,
      },
      {
        path: 'user-exam-tool',
        name: 'user-exam-tool',
        component: UserExamTool,
      },
      {
        path: 'my-referral',
        name: 'my-referral',
        component: MyReferral,
      },
      {
        path: 'review-rebate',
        name: 'review-rebate',
        component: ReviewRebate,
      },
      {
        path: 'referral-rebates',
        name: 'referral-rebates',
        component: ReferralRebates,
      },
      {
        path: 'review-rebate-admin',
        name: 'review-rebate-admin',
        component: ReviewRebateAdmin,
      },
      {
        // 黑名单管理（BL-5，46-黑名单设计.md §6）。
        path: 'blacklist',
        name: 'blacklist',
        component: Blacklist,
      },
      {
        path: 'extension-updates',
        name: 'extension-updates',
        component: ExtensionUpdates,
      },
      {
        path: 'system-settings',
        name: 'system-settings',
        component: SystemSettings,
      },
      {
        path: 'access-tokens',
        name: 'access-tokens',
        component: AccessTokenManagement,
      },
      {
        path: 'scripts',
        name: 'scripts',
        component: ScriptManagement,
      },
      {
        path: 'recharge-orders',
        name: 'recharge-orders',
        component: RechargeOrderList,
      },
      {
        path: 'location-cache',
        name: 'location-cache',
        redirect: '/adapter/ip-location-cache',
      },
      {
        path: 'ip-location-cache',
        name: 'ip-location-cache',
        component: LocationCache,
        meta: { locationKind: 'ip' },
      },
      {
        // IP 段归属管理（task_doc/71 §3.5）。路径用复数 ip-segments，
        // 与上面的单 IP 归属地缓存 ip-location-cache 区分。
        path: 'ip-segments',
        name: 'ip-segments',
        component: IPSegment,
      },
      {
        path: 'phone-location-cache',
        name: 'phone-location-cache',
        component: LocationCache,
        meta: { locationKind: 'phone' },
      },
      {
        // 等级与充值阶梯（94-*.md §2 C1/C2）。管理员专属：等级门槛决定谁能拿到
        // 什么设备上限（C3 会读 user_levels.device_limit），不对普通用户开放。
        path: 'user-levels',
        name: 'user-levels',
        component: UserLevel,
      },
      {
        path: 'billing-records',
        name: 'billing-records',
        component: BillingRecordList,
      },
      {
        path: 'tickets/:id?',
        name: 'tickets',
        component: Tickets,
      }
    ]
  }

];

const ADMIN_ONLY_ROUTE_NAMES = new Set([
  'component',
  'import',
  'userlist',
  // 已归档账号：列表里有账号快照与绑定记录，只能管理员看（task_doc/76 §3.2.3）。
  'archived-users',
  'loglist',
  'search-account-config',
  'ai-search-config',
  'ocr-cache',
  'user-exam-tool',
  'system-settings',
  'extension-updates',
  'location-cache',
  'ip-location-cache',
  'ip-segments',
  'phone-location-cache',
  'referral-rebates',
  'review-rebate-admin',
  'blacklist',
  // 等级与充值阶梯：等级门槛 = 谁能拿什么设备上限（C3 会读 device_limit），
  // 阶梯档位 = 平台补贴口径。两者都是运营配置，非管理员直接敲 URL 也要挡住。
  'user-levels',
])
const PUBLIC_ADAPTER_ROUTE_NAMES = new Set([
  'recharge-result',
])

const router = createRouter({
  history: createWebHistory(),
  routes
});

router.beforeEach((to, from, next) => {
  if (PUBLIC_ADAPTER_ROUTE_NAMES.has(String(to.name || ''))) {
    next()
    return
  }

  if (to.path === '/register') {
    getPublicRegisterStatus().then((res) => {
      if (res?.status === 200 && res?.data?.registerEnabled) {
        next()
        return
      }
      next('/login')
    }).catch(() => {
      next('/login')
    })
    return
  }

  if (!to.path.startsWith('/adapter')) {
    next()
    return
  }

  const currentUser = getStoredCurrentUser()
  if (!currentUser) {
    next('/login')
    return
  }

  if (ADMIN_ONLY_ROUTE_NAMES.has(String(to.name || '')) && !isAdminUser(currentUser)) {
    next(resolveHomePath(currentUser))
    return
  }

  next()
})

router.onError((error) => {
  const chunkFailure = error?.name === 'ChunkLoadError'
    || /Loading (?:CSS )?chunk|Failed to fetch dynamically imported module|Importing a module script failed/i.test(String(error?.message || ''))
  if (!chunkFailure) return
  message.error({
    key: 'route-load-failure', duration: 0,
    content: h('span', [
      '页面资源加载失败。刷新会丢弃未保存内容，请确认后重试。 ',
      h('button', { type: 'button', 'aria-label': '刷新页面', style: { minHeight: '44px', cursor: 'pointer' }, onClick: () => window.location.reload() }, '刷新页面'),
    ]),
  })
})

export default router;

