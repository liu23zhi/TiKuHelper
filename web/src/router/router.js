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
// 微信 / 手机号绑定历史共用一个 chunk：两页是同一组件，拆成两个 chunk 只会重复打一份。
const BindingHistory = () => import(/* webpackChunkName: "route-binding-history" */ '../pages/BindingHistory/index.vue')
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
// 第 8 轮新增页面。三个都是管理员专属，均已登记进 ADMIN_ONLY_ROUTE_NAMES。
const UserPortrait = () => import(/* webpackChunkName: "route-user-portrait" */ '../pages/UserPortrait/index.vue')
const RiskAdmin = () => import(/* webpackChunkName: "route-risk-admin" */ '../pages/RiskAdmin/index.vue')
const SensitiveImage = () => import(/* webpackChunkName: "route-sensitive-image" */ '../pages/SensitiveImage/index.vue')
const SensitiveWord = () => import(/* webpackChunkName: "route-sensitive-word" */ '../pages/SensitiveWord/index.vue')
const TicketImageCache = () => import(/* webpackChunkName: "route-ticket-image-cache" */ '../pages/TicketImageCache/index.vue')
const Whitelist = () => import(/* webpackChunkName: "route-whitelist" */ '../pages/Whitelist/index.vue')
// 账号封禁（121 §4.8 / 121 决策 6）。管理员专属，已登记进 ADMIN_ONLY_ROUTE_NAMES。
const UserBan = () => import(/* webpackChunkName: "route-user-ban" */ '../pages/UserBan/index.vue')
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
    path: '/recharge/result',
    name: 'recharge-result',
    component: RechargeResult,
  },
  {
    // 主应用布局挂在根路径下（2026-11-14 路径改名：不再出现 /adapter 段）。
    //
    // ⚠️ 它必须排在登录布局 `/` 之后：Vue Router 对同深度路由按声明顺序匹配，
    // 登录布局的 children 只有 login/register/wechat-bind 三条具名子路由，
    // 其余路径会落到本路由上。两个布局的子路径名互不重叠，无需额外判重。
    path: '/',
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
        // 用户画像（98 §0）。一个页面的两种视角：不带 query = 全站整体，
        // ?userId= = 单个用户。用户管理操作列的「画像」按钮跳到同一个 name。
        path: 'user-portrait',
        name: 'user-portrait',
        component: UserPortrait,
      },
      {
        // 风控告警（96 / 118 §2）。只读：本页没有任何处置动作，
        // 处置必须去黑名单/封禁页人工再确认一次（96 C1）。
        path: 'risk-admin',
        name: 'risk-admin',
        component: RiskAdmin,
      },
      {
        // 敏感图片检测（Q1）。两个 Tab：检测记录 / 超额未检测。
        path: 'sensitive-image',
        name: 'sensitive-image',
        component: SensitiveImage,
      },
      {
        // 敏感词检测记录（T-27）。只读三态筛选。
        path: 'sensitive-word',
        name: 'sensitive-word',
        component: SensitiveWord,
      },
      {
        // 工单图片缓存（T-12）。删除受 ref_count 保护。
        path: 'ticket-image-cache',
        name: 'ticket-image-cache',
        component: TicketImageCache,
      },
      {
        // 白名单（T-04 / T-05）。与黑名单分开一页，规则结构相同。
        path: 'whitelist',
        name: 'whitelist',
        component: Whitelist,
      },
      {
        // 账号封禁（121 §4.8 / 121 决策 6）。与黑名单/白名单相邻：三者都是风控入口，
        // 但语义不同 —— 黑/白名单按 IP/手机号/微信，封禁按**账号**。
        path: 'user-bans',
        name: 'user-bans',
        component: UserBan,
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
        // 我的令牌（普通用户可见区）。viewMode: 'self' 让**管理员**从这个入口
        // 进来时看到的是自己的令牌，而不是「令牌管理」的全站视图。
        // 与 scripts（我的脚本）/ script-admin（脚本管理）同一范式。
        path: 'my-tokens',
        name: 'my-tokens',
        component: AccessTokenManagement,
        meta: { viewMode: 'self' },
      },
      {
        // 令牌管理（仅管理员可见区）。独立 name 才能被侧边栏单独选中，
        // 也才能登记进 ADMIN_ONLY_ROUTE_NAMES（那里能看全站令牌、能改别人的额度上限）。
        path: 'token-admin',
        name: 'token-admin',
        component: AccessTokenManagement,
        meta: { viewMode: 'admin' },
      },
      {
        // 旧入口，保留 301 式重定向而不是删除：用户在书签/浏览器历史里
        // 留着 /adapter/access-tokens，直接 404 等于"页面凭空消失"。
        // 按角色分流到对应的新入口 —— 普通用户去 my-tokens，管理员去 token-admin。
        path: 'access-tokens',
        name: 'access-tokens',
        redirect: () => (isAdminUser(getStoredCurrentUser()) ? '/adapter/token-admin' : '/adapter/my-tokens'),
      },
      {
        // 我的脚本（普通用户可见区）。viewMode: 'self' 让**管理员**从这个入口
        // 进来时看到的是自己的脚本视图，而不是「脚本管理」的全站视图。
        path: 'scripts',
        name: 'scripts',
        component: ScriptManagement,
        meta: { viewMode: 'self' },
      },
      {
        // 脚本管理（仅管理员可见区）。独立 name 才能被侧边栏单独选中，
        // 也才能登记进 ADMIN_ONLY_ROUTE_NAMES（那里有全站脚本与计费政策）。
        path: 'script-admin',
        name: 'script-admin',
        component: ScriptManagement,
        meta: { viewMode: 'admin' },
      },
      {
        path: 'recharge-orders',
        name: 'recharge-orders',
        component: RechargeOrderList,
      },
      {
        path: 'location-cache',
        name: 'location-cache',
        redirect: '/ip-location-cache',
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
      // 绑定历史（task_doc/70 §3.6）：微信 / 手机号两页**共用同一个组件**，
      // 只用 meta.bindingKind 区分。写成两个 component 会出现两套文案与两处
      // 待修的重复逻辑 —— 这是 70 号文档点名要避免的。
      {
        path: 'wechat-bindings',
        name: 'wechat-bindings',
        component: BindingHistory,
        meta: { bindingKind: 'wechat' },
      },
      {
        path: 'phone-bindings',
        name: 'phone-bindings',
        component: BindingHistory,
        meta: { bindingKind: 'phone' },
      },
      {
        // 等级与充值阶梯（94-*.md §2 C1/C2）。管理员专属：等级门槛决定谁能拿到
        // 什么设备上限（C3 会读 user_levels.device_limit），不对普通用户开放。
        path: 'user-levels',
        name: 'user-levels',
        component: UserLevel,
      },
      {
        // 我的账单（普通用户可见区）。viewMode: 'self' 让**管理员**从这个入口
        // 进来时看到的是自己的账单，而不是「账单明细」的全站视图。
        path: 'billing-records',
        name: 'billing-records',
        component: BillingRecordList,
        meta: { viewMode: 'self' },
      },
      {
        // 账单明细（仅管理员可见区）。独立 name 才能被侧边栏单独选中，
        // 也才能登记进 ADMIN_ONLY_ROUTE_NAMES（那里有全站账单与改账单权限）。
        path: 'billing-admin',
        name: 'billing-admin',
        component: BillingRecordList,
        meta: { viewMode: 'admin' },
      },
      {
        // 工单反馈（普通用户可见区）。显式声明 viewMode: 'self'，
        // 让**管理员**从这个入口进来时看到的是用户视图（自己的单 + 能新建），
        // 而不是管理视图。不声明就退回「按角色判」——那正是 2026-11-14 的缺陷。
        path: 'tickets/:id?',
        name: 'tickets',
        component: Tickets,
        meta: { viewMode: 'self' },
      },
      {
        // 工单管理（仅管理员可见区）。独立 name 才能被侧边栏单独选中、
        // 才能登记进 ADMIN_ONLY_ROUTE_NAMES 挡掉普通用户直敲 URL。
        path: 'ticket-admin/:id?',
        name: 'ticket-admin',
        component: Tickets,
        meta: { viewMode: 'admin' },
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
  // 绑定历史两页（task_doc/70 §3.6）：同一组件的两个入口，两个 name 都要列。
  'wechat-bindings',
  'phone-bindings',
  'referral-rebates',
  'review-rebate-admin',
  'blacklist',
  // 白名单：能编辑白名单等于能决定谁不被拦截，普通用户可改就等于关掉黑名单（T-04）。
  'whitelist',
  // 账号封禁：能封禁等于能拒绝任何账号使用服务，是最高危的运营动作之一。
  // 必须挡住「直接敲 URL」—— 页面上的 canManage() 只是体验层，权限边界在 router 与后端。
  'user-bans',
  // 工单管理：与 `tickets`（工单反馈）是两个 name、两个入口。
  // 普通用户直敲 /ticket-admin 必须被挡住 —— 那里渲染的是全站工单。
  'ticket-admin',
  // 脚本管理 / 账单明细：与 scripts（我的脚本）、billing-records（我的账单）
  // 成对存在。管理端那侧含全站数据与写权限，普通用户直敲 URL 必须被挡。
  'script-admin',
  'billing-admin',
  // 令牌管理 / 我的令牌：与 script-admin / scripts 同一对。
  // token-admin 能看全站令牌、能改别人的额度上限，普通用户直敲 URL 必须被挡。
  // 注意 my-tokens **不能**加进来 —— 它是普通用户自己的入口。
  'token-admin',
  // 第 8 轮新增五个管理页。逐项说明为什么必须挡住「直接敲 URL」：
  //   user-portrait     —— 全站用户行为聚合，98 §0 明确仅管理员（四层权限的第 ④ 层）。
  //   risk-admin        —— 告警里带触发证据快照（IP / 用户 / 规则），属敏感运营数据。
  //   sensitive-image   —— 缩略图即违规内容本身，且带工单归属。
  //   sensitive-word    —— 记录里有 maskedText（原文部分可见）与判定来源。
  //   ticket-image-cache —— 能删 blob，误操作会影响所有引用它的工单。
  'user-portrait',
  'risk-admin',
  'sensitive-image',
  'sensitive-word',
  'ticket-image-cache',
  // 等级与充值阶梯：等级门槛 = 谁能拿什么设备上限（C3 会读 device_limit），
  // 阶梯档位 = 平台补贴口径。两者都是运营配置，非管理员直接敲 URL 也要挡住。
  'user-levels',
])
const PUBLIC_ADAPTER_ROUTE_NAMES = new Set([
  // 支付回跳：下单后浏览器跳回来时会话可能尚未建立。
  'recharge-result',
  // 登录流程三步（loginlayout 的 children）。2026-11-14 路径改名后主布局
  // 挂在根路径，这三条必须在白名单里，否则未登录访问 /login 会被踢回 /login 死循环。
  'adapterlogin',
  'adapterregister',
  'wechat-bind',
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

  // ⚠️ 2026-11-14 路径改名后，主布局挂在根路径，**不能**再用
  // 「to.path 不以 /adapter 开头就放行」来区分公开页 —— 那等于全部放行。
  //
  // 公开页判定改用 route name（见 PUBLIC_ADAPTER_ROUTE_NAMES），
  // 与 loginlayout 的 children 一起构成完整白名单：
  //   recharge-result（支付回跳，可能尚无会话）
  //   adapterlogin / adapterregister / wechat-bind（登录流程三步）

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

