// Both token entry routes reuse the same Vue component in production.
module.exports = async function createTokenViewRouter(component) {
  const { createRouter, createMemoryHistory } = require('vue-router')
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/my-tokens', component, meta: { viewMode: 'self' } },
    { path: '/token-admin', component, meta: { viewMode: 'admin' } },
  ] })
  await router.push('/my-tokens'); await router.isReady(); return router
}
