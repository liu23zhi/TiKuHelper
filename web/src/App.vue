<template>
  <div id="app">
    <a-config-provider :locale="locale" :theme="antdTheme">
      <router-view></router-view>
    </a-config-provider>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, provide, ref } from 'vue'
import { ConfigProvider, theme } from 'ant-design-vue'
import zhCN from 'ant-design-vue/lib/locale/zh_CN'
import { createThemeController, THEME_CONTROLLER_KEY } from './utils/themeMode.mjs'
import { getPublicRegisterStatus } from './api/TikuComponentApi'
import { applySiteName } from './utils/siteName'

export default {
  name: 'App',
  components: { AConfigProvider: ConfigProvider },
  setup() {
    const controller = createThemeController()
    provide(THEME_CONTROLLER_KEY, controller)
    const themeState = ref(controller.getState())
    const stopTheme = controller.subscribe(state => { themeState.value = state })
    const antdTheme = computed(() => {
      const dark = themeState.value.resolved === 'dark'
      return {
        algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm,
        token: {
          colorPrimary: dark ? '#69A9FF' : '#0066CC',
          colorBgBase: dark ? '#111113' : '#F5F5F7',
          colorBgContainer: dark ? '#1C1C1E' : '#FFFFFF',
          colorBgElevated: dark ? '#242426' : '#FFFFFF',
          colorText: dark ? '#F5F5F7' : '#1D1D1F',
          colorTextSecondary: dark ? '#A1A1AA' : '#5F6368',
          colorBorder: dark ? '#48484F' : '#D8DADF',
          borderRadius: 8,
        },
      }
    })
    onBeforeUnmount(() => { stopTheme(); controller.dispose() })
    onMounted(() => {
      getPublicRegisterStatus().then((res) => {
        if (res?.status === 200) {
          applySiteName(res?.data?.siteName)
        }
      }).catch(() => {
        applySiteName()
      })
    })
    return { locale: zhCN, antdTheme }
  },
}
</script>

<style>
html,
body {
  min-height: 100%;
}

body.swal2-height-auto {
  height: auto !important;
}

#app {
  font-family: Avenir, Helvetica, Arial, sans-serif;
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
  color: var(--console-text);
  height: 100%;
  background-color: var(--console-canvas);
}
</style>
