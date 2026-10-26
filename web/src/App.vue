<template>
  <a-config-provider :locale="locale" :theme="theme" :auto-insert-space-in-button="false">
  <div id="app">
    <router-view></router-view>
  </div>
  </a-config-provider>
</template>

<script>
import { onMounted } from 'vue'
import { getPublicRegisterStatus } from './api/TikuComponentApi'
import { applySiteName } from './utils/siteName'
import zhCN from 'ant-design-vue/es/locale/zh_CN'

export default {
  name: 'App',
  setup() {
    const theme = { token: {
      colorPrimary: '#0f766e', colorInfo: '#0f766e', colorSuccess: '#15803d',
      colorPrimaryBg: '#e7f3ef', colorPrimaryBgHover: '#d9ebe6',
      colorInfoBg: '#e7f3ef', colorInfoBorder: '#b8d7ce', controlItemBgActive: '#e7f3ef',
      colorWarning: '#a16207', colorError: '#c13e41', colorText: '#172b27',
      colorTextSecondary: '#62766e', colorBorder: '#dce5e1', borderRadius: 6,
      fontSize: 14, fontFamily: "'Noto Sans SC', 'Microsoft YaHei', sans-serif",
    } }
    onMounted(() => {
      getPublicRegisterStatus().then((res) => {
        if (res?.status === 200) {
          applySiteName(res?.data?.siteName)
        }
      }).catch(() => {
        applySiteName()
      })
    })
    return { locale: zhCN, theme }
  },
}
</script>
