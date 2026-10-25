const { defineConfig } = require('@vue/cli-service')
module.exports = defineConfig({
  transpileDependencies: true,
  // Bound Babel worker processes on high-core hosts and constrained CI containers.
  parallel: 2,
  chainWebpack(config) {
    // Route chunks are fetched on demand, not prefetched into the initial navigation.
    config.plugins.delete('prefetch')
  },
  devServer: {
    proxy: {
      "/adapter-service": {
        target: 'http://127.0.0.1:8060',
        changeOrigin: true,
      },
      "/sqp/api": {
        target: 'http://127.0.0.1:8060',
        changeOrigin: true,
      },
    },
  },
})
