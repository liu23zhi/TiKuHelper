import { Alert, Button, Form, Input, Card, Radio, Spin, Tooltip, ConfigProvider } from 'ant-design-vue'

export function registerPublicUI(app) {
  for (const widget of [Alert, Button, Form, Input, Card, Radio, Spin, Tooltip, ConfigProvider]) app.use(widget)
  return app
}

// Public login / registration do not render tables, uploads or any console shell.
// Vue Router awaits this gate on a direct console visit as well as after login.
export function installConsoleUIGuard(app, router, load = () => import(/* webpackChunkName: "console-ui" */ './registerUI')) {
  let ready
  router.beforeResolve(async to => {
    if (to.matched.some(record => record.name === 'loginlayout')) return
    if (!ready) {
      ready = load().then(({ registerConsoleExtras }) => { registerConsoleExtras(app) })
        .catch(error => { ready = undefined; throw error })
    }
    await ready
  })
}
