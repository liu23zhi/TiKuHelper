<template>
  <div id="app">
    <router-view></router-view>
  </div>
</template>

<script>
import { onMounted } from 'vue'
import { getPublicRegisterStatus } from './api/TikuComponentApi'
import { applySiteName } from './utils/siteName'

export default {
  name: 'App',
  setup() {
    onMounted(() => {
      getPublicRegisterStatus().then((res) => {
        if (res?.status === 200) {
          applySiteName(res?.data?.siteName)
        }
      }).catch(() => {
        applySiteName()
      })
    })
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
  color: #2c3e50;
  height: 100%;
  background-color: #f0f2f5;
}
</style>
