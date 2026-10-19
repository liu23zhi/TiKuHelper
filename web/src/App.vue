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
:root {
  --console-canvas: #f5f7fb;
  --console-surface: #ffffff;
  --console-primary: #2563eb;
  --console-border: #e5e7eb;
  --console-text: #1f2937;
  --console-muted: #64748b;
  --console-radius: 12px;
}

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
