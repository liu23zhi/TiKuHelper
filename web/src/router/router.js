import { createRouter, createWebHistory } from 'vue-router';
import { getCookies } from '../utils/cookies'
import { getStoredCurrentUser, isAdminUser, resolveHomePath } from '../utils/currentUser'
import { getPublicRegisterStatus } from '../api/TikuComponentApi'

import AdapterLoginLayout from '../layout/LoginLayout.vue';
import AdapterLoginPage from '../pages/Login/index.vue'
import AdapterRegisterPage from '../pages/Register/index.vue'

import TikuAdapterLayout from '../layout/TikuAdapterLayout.vue';
import TikuComponent from '../pages/TikuComponent/index.vue';
import IntelligentImport from '../pages/IntelligentImport/index.vue';
import UserList from '../pages/UserList/index.vue'
import UserUsagePage from '../pages/UserUsage/index.vue'
import LogList from '../pages/LogList/index.vue'
import SearchAccountConfig from '../pages/SearchAccountConfig/index.vue'
import AISearchConfig from '../pages/AISearchConfig/index.vue'
import SystemSettings from '../pages/SystemSettings/index.vue'
import AccessTokenManagement from '../pages/AccessTokenManagement/index.vue'
import BillingRecordList from '../pages/BillingRecordList/index.vue'
import RechargeResult from '../pages/RechargeResult/index.vue'
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
      }
    ]
  },
  {
    path: '/adapter',
    name: 'adapterlayout',
    component: TikuAdapterLayout,
    children: [
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
        path: 'billing-records',
        name: 'billing-records',
        component: BillingRecordList,
      },
      {
        path: 'recharge/result',
        name: 'recharge-result',
        component: RechargeResult,
      }
    ]
  }

];

const ADMIN_ONLY_ROUTE_NAMES = new Set([
  'component',
  'import',
  'userlist',
  'loglist',
  'search-account-config',
  'ai-search-config',
  'system-settings',
])

const router = createRouter({
  history: createWebHistory(),
  routes
});

router.beforeEach((to, from, next) => {
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

  const token = getCookies('token')
  if (!token) {
    next('/login')
    return
  }

  const currentUser = getStoredCurrentUser()
  if (ADMIN_ONLY_ROUTE_NAMES.has(String(to.name || '')) && !isAdminUser(currentUser)) {
    next(resolveHomePath(currentUser))
    return
  }

  next()
})

export default router;


